// File: bollywood/context.go
package bollywood

// Context provides information and capabilities to an Actor during message processing.
type Context interface {
	// Engine returns the Actor Engine managing this actor.
	Engine() *Engine
	// Self returns the PID of the actor processing the message.
	Self() *PID
	// Sender returns the PID of the actor that sent the message, if available.
	// For messages sent via Ask, this is the PID of the original asker.
	Sender() *PID
	// Message returns the actual message being processed.
	Message() interface{}
	// RequestID returns a unique ID if the message was sent via Ask, otherwise empty string.
	RequestID() string
	// Reply sends a response back to the asker if the message was sent via Ask.
	// Does nothing if the message was not sent via Ask (RequestID is empty).
	Reply(replyMessage interface{})
}

// context implements the Context interface.
type context struct {
	engine    *Engine
	self      *PID
	sender    *PID // Original sender (could be nil for Send, or asker for Ask)
	message   interface{}
	requestID string // Populated only for Ask requests
}

func (c *context) Engine() *Engine      { return c.engine }
func (c *context) Self() *PID           { return c.self }
func (c *context) Sender() *PID         { return c.sender }
func (c *context) Message() interface{} { return c.message }
func (c *context) RequestID() string    { return c.requestID }

func (c *context) Reply(replyMessage interface{}) {
	if c.requestID != "" && c.engine != nil {
		c.engine.replyFuture(c.requestID, replyMessage)
	}
}
