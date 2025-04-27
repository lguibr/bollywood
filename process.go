// File: bollywood/process.go
package bollywood

import (
	"fmt"
	"runtime/debug"
	"sync/atomic"
)

const defaultMailboxSize = 1024

// process represents the running instance of an actor, including its state and mailbox.
type process struct {
	engine  *Engine
	pid     *PID
	actor   Actor
	mailbox chan interface{} // Changed to interface{} to accept different envelope types
	props   *Props
	stopCh  chan struct{} // Signal to stop the run loop
	stopped atomic.Bool   // Use atomic bool for safer concurrent checks
}

func newProcess(engine *Engine, pid *PID, props *Props) *process {
	return &process{
		engine:  engine,
		pid:     pid,
		props:   props,
		mailbox: make(chan interface{}, defaultMailboxSize), // Changed type here
		stopCh:  make(chan struct{}),
	}
}

// run is the main loop for the actor process.
func (p *process) run() {
	var stoppingInvoked bool // Track if Stopping handler has been called

	// Defer final cleanup and Stopped message
	defer func() {
		// Ensure actor is marked as stopped
		p.stopped.Store(true)

		// Recover from panic during Stopped processing
		defer func() {
			if r := recover(); r != nil {
				fmt.Printf("Actor %s panicked during final cleanup/Stopped processing: %v\n", p.pid.ID, r)
			}
			// Remove from engine *after* all cleanup attempts
			p.engine.remove(p.pid)
		}()

		// Send the final Stopped message if actor was initialized and Stopping was invoked
		if p.actor != nil && stoppingInvoked {
			p.invokeReceive(Stopped{}, nil, "") // Call Stopped handler
		} else if p.actor != nil && !stoppingInvoked {
			fmt.Printf("WARN: Actor %s stopped without Stopping handler being invoked (likely due to early panic).\n", p.pid.ID)
			p.invokeReceive(Stopped{}, nil, "")
		}

	}()

	// Defer panic recovery for the main loop and actor initialization
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("Actor %s panicked: %v\nStack trace:\n%s\n", p.pid.ID, r, string(debug.Stack()))
			// Ensure stopCh is closed on panic (non-blocking)
			// and mark as stopped immediately
			if p.stopped.CompareAndSwap(false, true) {
				select {
				case <-p.stopCh: // Already closed
				default:
					close(p.stopCh)
				}
				// Attempt to invoke Stopping handler on panic if not already invoked
				if p.actor != nil && !stoppingInvoked {
					p.invokeReceive(Stopping{}, nil, "")
					stoppingInvoked = true
				}
			}
		}
	}()

	// Create the actor instance
	p.actor = p.props.Produce()
	if p.actor == nil {
		panic(fmt.Sprintf("Actor %s producer returned nil actor", p.pid.ID))
	}
	// Send Started message *after* actor is created
	p.invokeReceive(Started{}, nil, "")

	// Main message processing loop
	for {
		select {
		case <-p.stopCh:
			// Stop signal received directly (e.g., from engine.Stop or panic recovery)
			if p.stopped.CompareAndSwap(false, true) {
				// If not already marked stopped (e.g., by Stopping message),
				// invoke Stopping handler now before exiting.
				if !stoppingInvoked {
					p.invokeReceive(Stopping{}, nil, "")
					stoppingInvoked = true
				}
			}
			return // Exit the loop, deferred functions will run

		case env, ok := <-p.mailbox: // env is now interface{}
			if !ok {
				// Mailbox closed unexpectedly? Should not happen with current design.
				fmt.Printf("Actor %s mailbox closed unexpectedly.\n", p.pid.ID)
				if p.stopped.CompareAndSwap(false, true) {
					select {
					case <-p.stopCh:
					default:
						close(p.stopCh)
					}
					if !stoppingInvoked {
						p.invokeReceive(Stopping{}, nil, "")
						stoppingInvoked = true
					}
				}
				return
			}

			var actualMessage interface{}
			var sender *PID
			var requestID string

			// Determine the type of envelope and extract details
			switch specificEnvelope := env.(type) {
			case *messageEnvelope: // Regular Send
				actualMessage = specificEnvelope.Message
				sender = specificEnvelope.Sender
				requestID = ""
			case *askEnvelope: // Ask request
				actualMessage = specificEnvelope.Message
				sender = specificEnvelope.Sender // Sender here is the original asker
				requestID = specificEnvelope.RequestID
			default:
				// Should not happen if only engine sends envelopes
				fmt.Printf("ERROR: Actor %s received unexpected envelope type: %T\n", p.pid.ID, env)
				continue
			}

			// Check if stopped *after* receiving from mailbox,
			// but before processing, unless it's a system message.
			_, isStopping := actualMessage.(Stopping)
			_, isStoppedMsg := actualMessage.(Stopped)
			if p.stopped.Load() && !isStopping && !isStoppedMsg {
				continue
			}

			// Handle system messages directly
			switch msg := actualMessage.(type) {
			case Stopping:
				if p.stopped.CompareAndSwap(false, true) { // Process only once
					if !stoppingInvoked {
						p.invokeReceive(msg, sender, requestID) // Pass sender/requestID
						stoppingInvoked = true
					}
					// Signal the loop to stop *after* processing Stopping
					select {
					case <-p.stopCh: // Already closed by engine.Stop?
					default:
						close(p.stopCh)
					}
				}
			case Stopped:
				fmt.Printf("WARN: Actor %s received unexpected Stopped message via mailbox.\n", p.pid.ID)
				if p.stopped.CompareAndSwap(false, true) {
					if !stoppingInvoked {
						p.invokeReceive(Stopping{}, nil, "") // Call stopping first
						stoppingInvoked = true
					}
					p.invokeReceive(msg, sender, requestID) // Call the received Stopped handler
					select {
					case <-p.stopCh:
					default:
						close(p.stopCh)
					}
				}
			default:
				// Process regular user message or Ask message
				p.invokeReceive(actualMessage, sender, requestID)
			}
		}
	}
}

// invokeReceive calls the actor's Receive method within a protected context.
func (p *process) invokeReceive(msg interface{}, sender *PID, requestID string) {
	// Create context for this message
	ctx := &context{
		engine:    p.engine,
		self:      p.pid,
		sender:    sender,
		message:   msg,
		requestID: requestID, // Pass requestID to context
	}

	// Call the actor's Receive method, recovering from panics within it
	func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Printf("Actor %s panicked during Receive(%T): %v\nStack trace:\n%s\n", p.pid.ID, msg, r, string(debug.Stack()))
				// Ensure stopCh is closed on panic within Receive
				if p.stopped.CompareAndSwap(false, true) {
					select {
					case <-p.stopCh:
					default:
						close(p.stopCh)
					}
					// Attempt to invoke Stopping handler on panic if not already invoked
					p.invokeReceive(Stopping{}, nil, "") // Call stopping handler
				}
				// If it was an Ask request, reply with an error
				if requestID != "" {
					ctx.Reply(fmt.Errorf("actor panicked during request: %v", r))
				}
			}
		}()
		p.actor.Receive(ctx)
	}()
}
