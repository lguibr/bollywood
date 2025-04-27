// File: engine.go
package bollywood

import (
	"fmt"
	"strings" // Import strings
	"sync"
	"sync/atomic"
	"time"
)

// Engine manages the lifecycle and message dispatching for actors.
type Engine struct {
	pidCounter uint64
	actors     map[string]*process
	mu         sync.RWMutex // Protects the actors map
	stopping   atomic.Bool  // Indicates if the engine is shutting down
	mailboxMap sync.Map     // Store mailboxes separately for non-blocking send
}

// Mailbox returns the mailbox channel for a given PID.
// Used for potential non-blocking sends or external monitoring.
func (e *Engine) Mailbox(pid *PID) chan *messageEnvelope {
	if pid == nil {
		return nil
	}
	val, ok := e.mailboxMap.Load(pid.ID)
	if !ok {
		return nil
	}
	mailbox, ok := val.(chan *messageEnvelope)
	if !ok {
		// This should not happen if stored correctly
		fmt.Printf("ERROR: Invalid mailbox type found for PID %s\n", pid.ID)
		return nil
	}
	return mailbox
}

// NewEngine creates a new actor engine.
func NewEngine() *Engine {
	return &Engine{
		actors: make(map[string]*process),
		// mailboxMap is zero-value initialized, which is fine for sync.Map
	}
}

// nextPID generates a unique process ID.
func (e *Engine) nextPID() *PID {
	id := atomic.AddUint64(&e.pidCounter, 1)
	return &PID{ID: fmt.Sprintf("actor-%d", id)}
}

// Spawn creates and starts a new actor based on the provided Props.
// It returns the PID of the newly created actor.
func (e *Engine) Spawn(props *Props) *PID {
	if e.stopping.Load() {
		fmt.Println("Engine is stopping, cannot spawn new actors")
		return nil
	}

	pid := e.nextPID()
	proc := newProcess(e, pid, props)

	// Store mailbox in the sync.Map before starting the process
	e.mailboxMap.Store(pid.ID, proc.mailbox)

	e.mu.Lock()
	e.actors[pid.ID] = proc
	e.mu.Unlock()

	go proc.run() // Start the actor's run loop

	return pid
}

// Send delivers a message to the actor identified by the PID.
func (e *Engine) Send(pid *PID, message interface{}, sender *PID) {
	if pid == nil {
		return
	}
	// Allow system messages during shutdown for cleanup
	_, isStopping := message.(Stopping)
	_, isStopped := message.(Stopped)
	isSystemMsg := isStopping || isStopped

	if e.stopping.Load() && !isSystemMsg {
		return
	}

	val, ok := e.mailboxMap.Load(pid.ID)
	if !ok {
		return
	}

	mailbox, ok := val.(chan *messageEnvelope)
	if !ok {
		fmt.Printf("ERROR: Invalid mailbox type found for PID %s\n", pid.ID)
		return
	}

	envelope := &messageEnvelope{
		Sender:  sender,
		Message: message,
	}

	// Use non-blocking send
	select {
	case mailbox <- envelope:
		// Message sent
	default:
		// Mailbox full, message dropped (avoid logging spam)
	}
}

// Stop requests an actor to stop processing messages and shut down.
// It signals the actor's stop channel; the actor's run loop handles cleanup.
func (e *Engine) Stop(pid *PID) {
	if pid == nil {
		return
	}
	e.mu.RLock()
	proc, ok := e.actors[pid.ID]
	e.mu.RUnlock()

	if ok && proc != nil {
		// Directly signal the stop channel to ensure termination.
		// The process loop will handle calling the Stopping handler.
		select {
		case <-proc.stopCh: // Already closed
		default:
			close(proc.stopCh)
		}
	}
	// Removed empty else block here
}

// remove removes an actor process from the engine's tracking. Called by process.run defer.
func (e *Engine) remove(pid *PID) {
	if pid == nil {
		return
	}
	e.mu.Lock()
	delete(e.actors, pid.ID)
	e.mu.Unlock()
	// Remove mailbox from sync.Map as well
	e.mailboxMap.Delete(pid.ID)
}

// Shutdown stops all actors and waits for them to terminate gracefully.
func (e *Engine) Shutdown(timeout time.Duration) {
	if !e.stopping.CompareAndSwap(false, true) {
		fmt.Println("Engine already shutting down")
		return
	}
	fmt.Println("Engine shutdown initiated...")

	// Collect PIDs to stop while holding lock
	e.mu.RLock()
	pidsToStop := make([]*PID, 0, len(e.actors))
	for _, proc := range e.actors {
		if proc != nil && proc.pid != nil {
			pidsToStop = append(pidsToStop, proc.pid)
		}
	}
	e.mu.RUnlock()

	fmt.Printf("Stopping %d actors...\n", len(pidsToStop))
	for _, pid := range pidsToStop {
		e.Stop(pid) // Stop now only closes stopCh
	}

	// Wait for actors to stop
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		e.mu.RLock()
		remaining := len(e.actors)
		e.mu.RUnlock()
		if remaining == 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Check remaining actors after timeout
	e.mu.RLock()
	remainingCount := len(e.actors)
	if remainingCount > 0 {
		remainingActors := []string{}
		for pidStr := range e.actors {
			remainingActors = append(remainingActors, pidStr)
		}
		fmt.Printf("Engine shutdown timeout: %d actors did not stop gracefully: %s\n",
			remainingCount, strings.Join(remainingActors, ", "))
		e.mu.RUnlock()
		e.mu.Lock()
		for pidStr, proc := range e.actors {
			e.mailboxMap.Delete(pidStr)
			if proc != nil {
				select {
				case <-proc.stopCh:
				default:
					close(proc.stopCh)
				}
			}
		}
		e.actors = make(map[string]*process) // Clear the map
		e.mu.Unlock()
	} else {
		e.mu.RUnlock()
	}

	fmt.Println("Engine shutdown complete.")
}
