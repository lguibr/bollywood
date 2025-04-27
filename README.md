
[![Go Test](https://github.com/lguibr/bollywood/actions/workflows/test.yml/badge.svg)](https://github.com/lguibr/bollywood/actions/workflows/test.yml) [![Go Report Card](https://goreportcard.com/badge/github.com/lguibr/bollywood)](https://goreportcard.com/report/github.com/lguibr/bollywood) [![Go Reference](https://pkg.go.dev/badge/github.com/lguibr/bollywood.svg)](https://pkg.go.dev/github.com/lguibr/bollywood)

# Bollywood Actor Library

<p align="center">
  <img src="bitmap.png" alt="Logo" width="300"/>
</p>

Bollywood is a lightweight Actor Model implementation for Go, inspired by the principles of asynchronous message passing and state encapsulation. It aims to provide a simple yet powerful way to build concurrent applications using actors, channels, and minimal locking.

## Installation

```bash
go get github.com/lguibr/bollywood@latest
```

## Core Concepts

*   **Engine:** The central coordinator responsible for spawning, managing, and terminating actors. Provides methods like `Spawn`, `Send`, `Ask`, `Stop`, `Shutdown`.
*   **Actor:** An entity that encapsulates state and behavior. It implements the `Actor` interface, primarily the `Receive(Context)` method.
*   **PID (Process ID):** An opaque identifier used to reference and send messages to a specific actor instance.
*   **Context:** Provided to an actor's `Receive` method, allowing it to interact with the system (e.g., get its PID, sender PID, spawn children, send messages, check `RequestID`, `Reply` to Ask requests).
*   **Props:** Configuration object used to spawn new actors, specifying how to create an actor instance.
*   **Message:** Any Go `interface{}` value sent between actors.
*   **System Messages:** `Started`, `Stopping`, `Stopped` provide lifecycle hooks.

## Basic Usage

```go
package main

import (
	"errors"
	"fmt"
	"time"

	"github.com/lguibr/bollywood" // Use the new module path
)

// Define an actor struct
type MyActor struct {
	count int
}

// Implement the Actor interface
func (a *MyActor) Receive(ctx bollywood.Context) {
	switch msg := ctx.Message().(type) {
	case string:
		fmt.Printf("Actor %s received string: %s\n", ctx.Self().ID, msg)
		a.count++
		// If this was an Ask request, reply
		if ctx.RequestID() != "" {
			ctx.Reply(fmt.Sprintf("Processed string: %s", msg))
		}
	case int:
		fmt.Printf("Actor %s received int: %d\n", ctx.Self().ID, msg)
		// Example: Respond to sender (if it was a regular Send)
		if ctx.Sender() != nil && ctx.RequestID() == "" {
			ctx.Engine().Send(ctx.Sender(), fmt.Sprintf("Processed %d", msg), ctx.Self())
		}
		// If this was an Ask request, reply
		if ctx.RequestID() != "" {
			ctx.Reply(fmt.Sprintf("Processed int: %d", msg))
		}
	case bollywood.Started:
		fmt.Printf("Actor %s started\n", ctx.Self().ID)
	case bollywood.Stopping:
		fmt.Printf("Actor %s stopping\n", ctx.Self().ID)
	case bollywood.Stopped:
		fmt.Printf("Actor %s stopped\n", ctx.Self().ID)
	default:
		fmt.Printf("Actor %s received unknown message type\n", ctx.Self().ID)
		// If this was an Ask request, reply with an error
		if ctx.RequestID() != "" {
			ctx.Reply(errors.New("unknown message type"))
		}
	}
}

// Actor producer function
func newMyActor() bollywood.Actor {
	return &MyActor{}
}

func main() {
	// Create an actor engine
	engine := bollywood.NewEngine()
	defer engine.Shutdown(1 * time.Second) // Ensure shutdown

	// Define properties for the actor
	props := bollywood.NewProps(newMyActor)

	// Spawn the actor
	pid := engine.Spawn(props)

	// Send messages asynchronously
	engine.Send(pid, "hello world", nil) // nil sender for messages from outside actors
	engine.Send(pid, 42, nil)

	// Send a message and wait for a reply using Ask
	fmt.Println("Asking actor...")
	reply, err := engine.Ask(pid, "ask me something", 500*time.Millisecond)
	if err != nil {
		fmt.Printf("Ask failed: %v\n", err)
	} else {
		fmt.Printf("Ask received reply: %v\n", reply)
	}

	// Ask with an integer
	replyInt, errInt := engine.Ask(pid, 100, 500*time.Millisecond)
	if errInt != nil {
		fmt.Printf("Ask (int) failed: %v\n", errInt)
	} else {
		fmt.Printf("Ask (int) received reply: %v\n", replyInt)
	}

	// Allow time for async messages to be processed before shutdown
	time.Sleep(100 * time.Millisecond)

	fmt.Println("Engine finishing...")
}
```

## Ask Pattern

The `Engine.Ask` method provides a way to send a message to an actor and synchronously wait for a response within a specified timeout.

```go
reply, err := engine.Ask(targetPID, requestMessage, 500*time.Millisecond)
if err != nil {
    if errors.Is(err, bollywood.ErrTimeout) {
        // Handle timeout
    } else {
        // Handle other errors (e.g., actor not found)
    }
} else {
    // Process the reply
    fmt.Printf("Received reply: %v\n", reply)
}
```

Inside the receiving actor's `Receive` method, you can check if the message came via `Ask` and send a reply using `ctx.Reply()`:

```go
func (a *MyActor) Receive(ctx bollywood.Context) {
    msg := ctx.Message()
    requestID := ctx.RequestID() // Get the request ID

    if requestID != "" {
        // This message came via Ask
        response := fmt.Sprintf("Replying to %T", msg)
        ctx.Reply(response) // Send the reply
    } else {
        // This message came via Send
        fmt.Printf("Received async message: %T\n", msg)
    }
}
```

## Example Usage

See the [PonGo](https://github.com/lguibr/pongo) project for an example of how Bollywood can be used to build a concurrent game server.