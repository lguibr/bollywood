// File: bollywood/messages.go
package bollywood

// --- System Messages ---

// Started is sent to an actor after its goroutine has started.
type Started struct{}

// Stopping is sent to an actor to signal it should prepare to stop.
// The actor should finish its current message and perform cleanup.
// No more user messages will be delivered after Stopping.
type Stopping struct{}

// Stopped is sent to an actor just before its goroutine exits.
// This is the final message an actor will receive.
type Stopped struct{}

// Failure is sent to a supervisor when a child actor crashes.
// (Not fully implemented in this basic version)
type Failure struct {
	Who    *PID
	Reason interface{}
}

// --- Message Envelope ---

// messageEnvelope wraps a user message with sender information for regular Send.
type messageEnvelope struct {
	Sender  *PID
	Message interface{}
}

// --- Ask Pattern Internals ---

// askEnvelope wraps a message sent via Ask, including a request ID for reply correlation.
type askEnvelope struct {
	RequestID string
	Sender    *PID // The original asker
	Message   interface{}
}

// futureResponse is used internally to pass Ask results back.
type futureResponse struct {
	Result interface{}
	Err    error
}
