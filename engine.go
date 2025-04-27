// File: bollywood/engine.go
package bollywood

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid" // Import UUID library
)

var ErrTimeout = errors.New("bollywood: ask timeout")

// Engine manages the lifecycle and message dispatching for actors.
type Engine struct {
	pidCounter uint64
	actors     map[string]*process
	mu         sync.RWMutex // Protects the actors map
	stopping   atomic.Bool  // Indicates if the engine is shutting down
	mailboxMap sync.Map     // Store mailboxes separately for non-blocking send
	futures    sync.Map     // Stores pending Ask futures: map[requestID string]chan futureResponse
}

// Mailbox returns the mailbox channel for a given PID.
// Used for potential non-blocking sends or external monitoring.
func (e *Engine) Mailbox(pid *PID) chan interface{} { // Return type changed to interface{}
	if pid == nil {
		return nil
	}
	val, ok := e.mailboxMap.Load(pid.ID)
	if !ok {
		return nil
	}
	// Type assertion to the actual mailbox type used internally
	mailbox, ok := val.(chan interface{}) // Changed type here
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
		// mailboxMap and futures are zero-value initialized, which is fine for sync.Map
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

// Send delivers a message asynchronously to the actor identified by the PID.
// The sender PID is optional and indicates the source actor, if any.
func (e *Engine) Send(pid *PID, message interface{}, sender *PID) {
	if pid == nil {
		return
	}
	// Allow system messages during shutdown for cleanup
	_, isStopping := message.(Stopping)
	_, isStopped := message.(Stopped)
	isSystemMsg := isStopping || isStopped

	if e.stopping.Load() && !isSystemMsg {
		// fmt.Printf("Engine stopping, dropping message %T to %s\n", message, pid.ID) // Debug log
		return
	}

	val, ok := e.mailboxMap.Load(pid.ID)
	if !ok {
		// fmt.Printf("Mailbox not found for PID %s, dropping message %T\n", pid.ID, message) // Debug log
		return
	}

	mailbox, ok := val.(chan interface{}) // Expecting chan interface{} now
	if !ok {
		fmt.Printf("ERROR: Invalid mailbox type found for PID %s\n", pid.ID)
		return
	}

	envelope := &messageEnvelope{ // Use the regular envelope for Send
		Sender:  sender,
		Message: message,
	}

	// Use non-blocking send
	select {
	case mailbox <- envelope:
		// Message sent
	default:
		// Mailbox full, message dropped (avoid logging spam)
		// fmt.Printf("Mailbox full for PID %s, dropping message %T\n", pid.ID, message) // Debug log
	}
}

// Ask sends a message to the target actor and waits for a reply within the timeout.
func (e *Engine) Ask(pid *PID, message interface{}, timeout time.Duration) (interface{}, error) {
	if pid == nil {
		return nil, errors.New("bollywood: ask target PID cannot be nil")
	}
	if e.stopping.Load() {
		return nil, errors.New("bollywood: engine is stopping, cannot perform ask")
	}

	// 1. Generate unique request ID
	requestID := uuid.NewString()

	// 2. Create future (channel)
	futureChan := make(chan futureResponse, 1) // Buffered channel of size 1

	// 3. Store future
	e.futures.Store(requestID, futureChan)
	defer e.futures.Delete(requestID) // Ensure cleanup

	// 4. Create ask envelope
	envelope := &askEnvelope{
		RequestID: requestID,
		Sender:    nil, // Ask doesn't have an actor sender context itself
		Message:   message,
	}

	// 5. Send envelope to target actor's mailbox
	val, ok := e.mailboxMap.Load(pid.ID)
	if !ok {
		return nil, fmt.Errorf("bollywood: actor %s not found for ask", pid.ID)
	}
	mailbox, ok := val.(chan interface{}) // Expecting chan interface{}
	if !ok {
		fmt.Printf("ERROR: Invalid mailbox type found for PID %s during Ask\n", pid.ID)
		return nil, fmt.Errorf("bollywood: internal error, invalid mailbox type for %s", pid.ID)
	}

	select {
	case mailbox <- envelope:
		// Envelope sent successfully
	default:
		// Mailbox full, cannot send Ask request
		return nil, fmt.Errorf("bollywood: mailbox full for actor %s, cannot perform ask", pid.ID)
	}

	// 6. Wait for response or timeout
	select {
	case resp := <-futureChan:
		return resp.Result, resp.Err // Return result or error from the future
	case <-time.After(timeout):
		// Timeout occurred, remove future (already deferred, but good practice)
		e.futures.Delete(requestID)
		return nil, ErrTimeout
	}
}

// replyFuture is called by context.Reply to send the result back to the Ask caller.
func (e *Engine) replyFuture(requestID string, replyMessage interface{}) {
	if futureVal, ok := e.futures.Load(requestID); ok {
		if futureChan, ok := futureVal.(chan futureResponse); ok {
			// Send non-blockingly in case the asker timed out and is gone
			select {
			case futureChan <- futureResponse{Result: replyMessage, Err: nil}:
				// Reply sent successfully
			default:
				// Asker might have timed out and deleted the future, or channel is blocked (shouldn't happen with buffer 1)
				fmt.Printf("WARN: Bollywood Ask future channel blocked or receiver gone for request ID %s\n", requestID)
			}
			// Future is deleted by the Ask method's defer
		} else {
			fmt.Printf("ERROR: Invalid future channel type found for request ID %s\n", requestID)
			e.futures.Delete(requestID) // Clean up invalid entry
		}
	}
	// If future not found, the Ask likely timed out already.
}

// Stop requests an actor to stop processing messages and shut down.
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

	// Cancel pending futures
	e.futures.Range(func(key, value interface{}) bool {
		requestID := key.(string)
		if futureChan, ok := value.(chan futureResponse); ok {
			// Send non-blockingly
			select {
			case futureChan <- futureResponse{Result: nil, Err: errors.New("bollywood: engine shutting down")}:
			default:
			}
		}
		e.futures.Delete(requestID) // Remove from map
		return true
	})
	fmt.Println("Pending Ask futures cancelled.")

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
