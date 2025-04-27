// File: bollywood/engine_test.go
package bollywood

import (
	"errors" // Import errors
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// --- Test Actor ---

type testActor struct {
	mu           sync.Mutex
	Received     []interface{}
	StartedCount int32
	StoppedCount int32
	StartedWg    *sync.WaitGroup // Signal when Started is received
	MessageWg    *sync.WaitGroup // Signal when a specific message is received
	StopWg       *sync.WaitGroup // Signal when Stopping is received
	PanicOnMsg   interface{}     // Message type to panic on
	ReplyToAsk   interface{}     // Message to reply with for Ask requests
}

func newTestActor(startedWg, messageWg, stopWg *sync.WaitGroup) *testActor {
	return &testActor{
		StartedWg: startedWg,
		MessageWg: messageWg,
		StopWg:    stopWg,
	}
}

func (a *testActor) Receive(ctx Context) {
	a.mu.Lock()
	msg := ctx.Message() // Capture message while locked
	a.Received = append(a.Received, msg)
	reply := a.ReplyToAsk // Capture reply while locked
	a.mu.Unlock()         // Unlock before potentially blocking wg.Done() or Reply

	// Handle panics for testing recovery
	if a.PanicOnMsg != nil && fmt.Sprintf("%T", msg) == fmt.Sprintf("%T", a.PanicOnMsg) {
		panic(fmt.Sprintf("test panic on %T", msg))
	}

	// Handle Ask requests
	if ctx.RequestID() != "" {
		if reply != nil {
			ctx.Reply(reply)
		} else {
			// Default reply for Ask if none specified
			ctx.Reply(fmt.Sprintf("ACK for %T", msg))
		}
		if a.MessageWg != nil {
			a.MessageWg.Done() // Count Ask messages too
		}
		return // Don't process Ask messages further in this test actor
	}

	// Handle regular messages
	switch msg.(type) {
	case Started:
		atomic.AddInt32(&a.StartedCount, 1)
		if a.StartedWg != nil {
			a.StartedWg.Done() // Signal that Started was received
		}
	case Stopping:
		// Stopping logic if needed
		if a.StopWg != nil {
			a.StopWg.Done() // Signal that Stopping was received
		}
	case Stopped:
		atomic.AddInt32(&a.StoppedCount, 1)
		// Note: StoppedWg might be tricky as the actor goroutine exits immediately after
	case string, int: // Example user messages
		if a.MessageWg != nil {
			a.MessageWg.Done() // Signal that a user message was received
		}
	}
}

func (a *testActor) GetReceived() []interface{} {
	a.mu.Lock()
	defer a.mu.Unlock()
	// Return a copy
	msgs := make([]interface{}, len(a.Received))
	copy(msgs, a.Received)
	return msgs
}

// --- Engine Tests ---

func TestEngine_NewEngine(t *testing.T) {
	engine := NewEngine()
	assert.NotNil(t, engine)
	assert.NotNil(t, engine.actors)
	assert.False(t, engine.stopping.Load())
}

func TestEngine_Spawn(t *testing.T) {
	engine := NewEngine()
	defer engine.Shutdown(1 * time.Second)

	var startedWg sync.WaitGroup
	startedWg.Add(1)

	props := NewProps(func() Actor { return newTestActor(&startedWg, nil, nil) })
	pid := engine.Spawn(props)

	assert.NotNil(t, pid)
	assert.NotEmpty(t, pid.ID)

	// Check if actor exists in engine map
	engine.mu.RLock()
	_, exists := engine.actors[pid.ID]
	engine.mu.RUnlock()
	assert.True(t, exists, "Actor process should exist in engine map")

	// Wait for the actor to receive the Started message
	waitTimeout(&startedWg, 500*time.Millisecond, t, "Actor did not start")

	// Check mailbox exists
	mailbox := engine.Mailbox(pid)
	assert.NotNil(t, mailbox, "Mailbox should exist for spawned actor")
}

func TestEngine_Spawn_DuringShutdown(t *testing.T) {
	engine := NewEngine()
	engine.stopping.Store(true) // Simulate shutdown state

	props := NewProps(func() Actor { return newTestActor(nil, nil, nil) })
	pid := engine.Spawn(props)

	assert.Nil(t, pid, "Should not spawn actor during shutdown")
}

func TestEngine_Send_Basic(t *testing.T) {
	engine := NewEngine()
	defer engine.Shutdown(1 * time.Second)

	var startedWg, messageWg sync.WaitGroup
	startedWg.Add(1)
	messageWg.Add(2) // Expecting two messages

	actor := newTestActor(&startedWg, &messageWg, nil)
	props := NewProps(func() Actor { return actor })
	pid := engine.Spawn(props)

	waitTimeout(&startedWg, 500*time.Millisecond, t, "Actor did not start")

	engine.Send(pid, "hello", nil)
	engine.Send(pid, 123, nil)

	waitTimeout(&messageWg, 500*time.Millisecond, t, "Actor did not receive messages")

	received := actor.GetReceived()
	assert.Contains(t, received, "hello")
	assert.Contains(t, received, 123)
	assert.IsType(t, Started{}, received[0], "First message should be Started")
}

func TestEngine_Send_ToNilPID(t *testing.T) {
	engine := NewEngine()
	defer engine.Shutdown(1 * time.Second)
	// Should not panic or error
	engine.Send(nil, "test", nil)
}

func TestEngine_Send_ToNonExistentPID(t *testing.T) {
	engine := NewEngine()
	defer engine.Shutdown(1 * time.Second)
	// Should not panic or error
	engine.Send(&PID{ID: "actor-does-not-exist"}, "test", nil)
}

func TestEngine_Send_DuringShutdown(t *testing.T) {
	engine := NewEngine()
	var startedWg sync.WaitGroup
	startedWg.Add(1)
	actor := newTestActor(&startedWg, nil, nil)
	props := NewProps(func() Actor { return actor })
	pid := engine.Spawn(props)
	waitTimeout(&startedWg, 500*time.Millisecond, t, "Actor did not start")

	engine.stopping.Store(true) // Simulate shutdown

	// System messages should still be allowed (e.g., Stopping, Stopped)
	engine.Send(pid, Stopping{}, nil)
	// User messages should be dropped
	engine.Send(pid, "should be dropped", nil)

	time.Sleep(100 * time.Millisecond) // Allow time for potential processing

	received := actor.GetReceived()
	assert.Contains(t, received, Stopping{}, "Stopping message should be received during shutdown")
	assert.NotContains(t, received, "should be dropped", "User message should be dropped during shutdown")

	engine.Shutdown(1 * time.Second) // Complete shutdown
}

func TestEngine_Stop_Basic(t *testing.T) {
	engine := NewEngine()
	defer engine.Shutdown(1 * time.Second)

	var startedWg, stopWg sync.WaitGroup
	startedWg.Add(1)
	stopWg.Add(1)

	actor := newTestActor(&startedWg, nil, &stopWg)
	props := NewProps(func() Actor { return actor })
	pid := engine.Spawn(props)

	waitTimeout(&startedWg, 500*time.Millisecond, t, "Actor did not start")

	engine.Stop(pid)

	// Wait for the actor to receive the Stopping message
	waitTimeout(&stopWg, 500*time.Millisecond, t, "Actor did not receive Stopping")

	// Allow time for the actor goroutine to exit and be removed
	time.Sleep(100 * time.Millisecond)

	// Check actor is removed from engine maps
	engine.mu.RLock()
	_, exists := engine.actors[pid.ID]
	engine.mu.RUnlock()
	assert.False(t, exists, "Actor process should be removed after stop")

	_, mailboxExists := engine.mailboxMap.Load(pid.ID)
	assert.False(t, mailboxExists, "Actor mailbox should be removed after stop")

	// Send message to stopped actor - should be dropped silently
	engine.Send(pid, "after stop", nil)
	time.Sleep(50 * time.Millisecond)
	received := actor.GetReceived()
	assert.NotContains(t, received, "after stop")
}

func TestEngine_Stop_NilPID(t *testing.T) {
	engine := NewEngine()
	defer engine.Shutdown(1 * time.Second)
	// Should not panic
	engine.Stop(nil)
}

func TestEngine_Stop_NonExistentPID(t *testing.T) {
	engine := NewEngine()
	defer engine.Shutdown(1 * time.Second)
	// Should not panic
	engine.Stop(&PID{ID: "actor-does-not-exist"})
}

func TestEngine_Stop_AlreadyStopped(t *testing.T) {
	engine := NewEngine()
	defer engine.Shutdown(1 * time.Second)

	var startedWg, stopWg sync.WaitGroup
	startedWg.Add(1)
	stopWg.Add(1)

	props := NewProps(func() Actor { return newTestActor(&startedWg, nil, &stopWg) })
	pid := engine.Spawn(props)
	waitTimeout(&startedWg, 500*time.Millisecond, t, "Actor did not start")

	engine.Stop(pid)
	waitTimeout(&stopWg, 500*time.Millisecond, t, "Actor did not receive Stopping")
	time.Sleep(100 * time.Millisecond) // Allow removal

	// Stop again - should not panic or error
	engine.Stop(pid)
}

func TestEngine_Shutdown_Graceful(t *testing.T) {
	engine := NewEngine()
	// No defer shutdown, we are testing it

	var startedWg1, startedWg2 sync.WaitGroup
	var stopWg1, stopWg2 sync.WaitGroup
	startedWg1.Add(1)
	startedWg2.Add(1)
	stopWg1.Add(1)
	stopWg2.Add(1)

	props1 := NewProps(func() Actor { return newTestActor(&startedWg1, nil, &stopWg1) })
	props2 := NewProps(func() Actor { return newTestActor(&startedWg2, nil, &stopWg2) })

	pid1 := engine.Spawn(props1)
	pid2 := engine.Spawn(props2)

	waitTimeout(&startedWg1, 500*time.Millisecond, t, "Actor 1 did not start")
	waitTimeout(&startedWg2, 500*time.Millisecond, t, "Actor 2 did not start")

	shutdownDone := make(chan struct{})
	go func() {
		engine.Shutdown(2 * time.Second) // Generous timeout for test
		close(shutdownDone)
	}()

	// Wait for actors to receive Stopping
	waitTimeout(&stopWg1, 1*time.Second, t, "Actor 1 did not receive Stopping during shutdown")
	waitTimeout(&stopWg2, 1*time.Second, t, "Actor 2 did not receive Stopping during shutdown")

	// Wait for shutdown to complete
	select {
	case <-shutdownDone:
		// Shutdown completed
	case <-time.After(3 * time.Second):
		t.Fatal("Engine shutdown timed out")
	}

	// Verify engine state after shutdown
	assert.True(t, engine.stopping.Load(), "Engine should be marked as stopping")
	engine.mu.RLock()
	assert.Empty(t, engine.actors, "Actors map should be empty after shutdown")
	engine.mu.RUnlock()

	// Verify mailboxes removed
	_, mb1Exists := engine.mailboxMap.Load(pid1.ID)
	_, mb2Exists := engine.mailboxMap.Load(pid2.ID)
	assert.False(t, mb1Exists, "Mailbox 1 should be removed")
	assert.False(t, mb2Exists, "Mailbox 2 should be removed")
}

// Test helper for waiting on WaitGroup with timeout
func waitTimeout(wg *sync.WaitGroup, timeout time.Duration, t *testing.T, failMsg string) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		// Wait succeeded
	case <-time.After(timeout):
		t.Fatalf("%s within %v", failMsg, timeout)
	}
}

// TestEngine_Shutdown_Timeout requires an actor that blocks in Stopping
type blockingActor struct {
	StopWg *sync.WaitGroup
}

func (a *blockingActor) Receive(ctx Context) {
	switch ctx.Message().(type) {
	case Stopping:
		if a.StopWg != nil {
			a.StopWg.Done() // Signal that stopping started
		}
		// Block indefinitely to cause timeout
		select {}
	}
}

func TestEngine_Shutdown_Timeout(t *testing.T) {
	engine := NewEngine()

	var stopWg sync.WaitGroup
	stopWg.Add(1)

	// Spawn one actor that will block on stopping
	props := NewProps(func() Actor { return &blockingActor{StopWg: &stopWg} })
	pid := engine.Spawn(props)
	assert.NotNil(t, pid)

	// Spawn a normal actor that should stop
	var startedWg, normalStopWg sync.WaitGroup
	startedWg.Add(1)
	normalStopWg.Add(1)
	normalActor := newTestActor(&startedWg, nil, &normalStopWg)
	propsNormal := NewProps(func() Actor { return normalActor })
	pidNormal := engine.Spawn(propsNormal)
	waitTimeout(&startedWg, 500*time.Millisecond, t, "Normal actor did not start")

	shutdownDone := make(chan struct{})
	go func() {
		engine.Shutdown(200 * time.Millisecond) // Short timeout to trigger timeout logic
		close(shutdownDone)
	}()

	// Wait for the blocking actor to start stopping
	waitTimeout(&stopWg, 100*time.Millisecond, t, "Blocking actor did not receive Stopping")
	// Wait for the normal actor to start stopping
	waitTimeout(&normalStopWg, 100*time.Millisecond, t, "Normal actor did not receive Stopping")

	// Wait for shutdown to complete (it will timeout)
	select {
	case <-shutdownDone:
		// Shutdown completed
	case <-time.After(1 * time.Second): // Wait longer than shutdown timeout
		t.Fatal("Engine shutdown call did not return after timeout")
	}

	// Verify engine state after shutdown timeout
	assert.True(t, engine.stopping.Load(), "Engine should be marked as stopping")
	engine.mu.RLock()
	// The actors map should be empty because the timeout logic forcibly clears it
	assert.Empty(t, engine.actors, "Actors map should be empty after shutdown timeout")
	engine.mu.RUnlock()

	// Verify mailboxes removed
	_, mbExists := engine.mailboxMap.Load(pid.ID)
	_, mbNormalExists := engine.mailboxMap.Load(pidNormal.ID)
	assert.False(t, mbExists, "Blocking actor mailbox should be removed")
	assert.False(t, mbNormalExists, "Normal actor mailbox should be removed")
}

// --- Ask Tests ---

func TestEngine_Ask_Success(t *testing.T) {
	engine := NewEngine()
	defer engine.Shutdown(1 * time.Second)

	var startedWg, messageWg sync.WaitGroup
	startedWg.Add(1)
	messageWg.Add(1) // Expect one Ask message

	actor := newTestActor(&startedWg, &messageWg, nil)
	actor.ReplyToAsk = "pong" // Configure reply for Ask
	props := NewProps(func() Actor { return actor })
	pid := engine.Spawn(props)

	waitTimeout(&startedWg, 500*time.Millisecond, t, "Actor did not start")

	reply, err := engine.Ask(pid, "ping", 500*time.Millisecond)

	assert.NoError(t, err)
	assert.Equal(t, "pong", reply)

	// Wait for the actor to process the message (optional, Ask already waited)
	waitTimeout(&messageWg, 100*time.Millisecond, t, "Actor did not process Ask message")

	received := actor.GetReceived()
	assert.Contains(t, received, "ping", "Actor should have received 'ping'")
}

func TestEngine_Ask_Timeout(t *testing.T) {
	engine := NewEngine()
	defer engine.Shutdown(1 * time.Second)

	var startedWg sync.WaitGroup
	startedWg.Add(1)

	// Actor that delays processing
	actor := ActorFunc(func(ctx Context) {
		switch ctx.Message().(type) {
		case Started:
			startedWg.Done()
		case string: // The ask message
			time.Sleep(200 * time.Millisecond) // Delay longer than timeout
			if ctx.RequestID() != "" {
				ctx.Reply("too late")
			}
		}
	})

	props := NewProps(func() Actor { return actor })
	pid := engine.Spawn(props)
	waitTimeout(&startedWg, 500*time.Millisecond, t, "Actor did not start")

	reply, err := engine.Ask(pid, "ping", 100*time.Millisecond) // 100ms timeout

	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrTimeout), "Error should be ErrTimeout")
	assert.Nil(t, reply)

	// Check future was cleaned up
	foundFuture := false
	engine.futures.Range(func(key, value interface{}) bool {
		foundFuture = true
		return false // Stop iteration
	})
	assert.False(t, foundFuture, "Futures map should be empty after timeout")
}

func TestEngine_Ask_ActorNotFound(t *testing.T) {
	engine := NewEngine()
	defer engine.Shutdown(1 * time.Second)

	pid := &PID{ID: "non-existent-actor"}
	reply, err := engine.Ask(pid, "ping", 100*time.Millisecond)

	assert.Error(t, err)
	assert.Nil(t, reply)
	assert.Contains(t, err.Error(), "not found for ask")
}

func TestEngine_Ask_DuringShutdown(t *testing.T) {
	engine := NewEngine()
	var startedWg sync.WaitGroup
	startedWg.Add(1)
	props := NewProps(func() Actor { return newTestActor(&startedWg, nil, nil) })
	pid := engine.Spawn(props)
	waitTimeout(&startedWg, 500*time.Millisecond, t, "Actor did not start")

	engine.stopping.Store(true) // Simulate shutdown

	reply, err := engine.Ask(pid, "ping", 100*time.Millisecond)

	assert.Error(t, err)
	assert.Nil(t, reply)
	assert.Contains(t, err.Error(), "engine is stopping")

	engine.Shutdown(1 * time.Second) // Clean up properly
}

// Helper type for simple functional actors
type ActorFunc func(ctx Context)

func (f ActorFunc) Receive(ctx Context) {
	f(ctx)
}
