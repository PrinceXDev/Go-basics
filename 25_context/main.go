package main

// ============================================================================
// CONCEPT: context.Context — cancellation, timeouts, and request-scoped
// values, passed explicitly through a call chain.
//
// WHY THIS MATTERS
// Real Go programs (especially servers) constantly need to answer: "should
// this operation stop early?" — because the client disconnected, a timeout
// was hit, or a parent operation was cancelled. `context.Context` is Go's
// standard, explicit way to carry that signal through a chain of function
// calls, including across goroutines.
//
// JS/TS comparison: closest equivalent is an `AbortController` /
// `AbortSignal` passed into a fetch() call. Go's context is more general —
// it's threaded through almost every function in a real backend codebase
// (by convention, as the FIRST parameter, named `ctx`), not just HTTP
// requests.
// ============================================================================

import (
	"context"
	"fmt"
	"time"
)

// By convention, functions that might need to be cancelled take a
// context.Context as their FIRST parameter.
func doWork(ctx context.Context, id int) {
	select { // same select from lesson 24 — racing "work done" vs "cancelled"
	case <-time.After(100 * time.Millisecond):
		fmt.Printf("worker %d: finished normally\n", id)
	case <-ctx.Done():
		// ctx.Done() returns a channel that gets closed when the context
		// is cancelled or times out. ctx.Err() explains why.
		fmt.Printf("worker %d: cancelled (%v)\n", id, ctx.Err())
	}
}

func main() {
	// ---------- context.WithTimeout ----------
	// Creates a context that automatically cancels itself after the given
	// duration. `cancel` MUST be called (commonly via defer) to release
	// resources associated with the context, even if the timeout never
	// fires naturally.
	fmt.Println("-- WithTimeout: work finishes before timeout --")
	ctx1, cancel1 := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel1()
	doWork(ctx1, 1) // work takes 100ms, timeout is 200ms -> finishes normally

	fmt.Println("-- WithTimeout: timeout fires first --")
	ctx2, cancel2 := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel2()
	doWork(ctx2, 2) // work takes 100ms, timeout is 50ms -> gets cancelled

	// ---------- context.WithCancel ----------
	// Gives you MANUAL control: call cancel() yourself whenever you decide
	// the operation should stop, rather than waiting for a fixed duration.
	fmt.Println("-- WithCancel: manual cancellation --")
	ctx3, cancel3 := context.WithCancel(context.Background())

	go func() {
		time.Sleep(30 * time.Millisecond)
		fmt.Println("main: deciding to cancel worker 3 early")
		cancel3()
	}()
	doWork(ctx3, 3)

	// ---------- context.Background() ----------
	// The root of every context tree — an empty context with no
	// cancellation, no deadline, no values. Every WithTimeout/WithCancel
	// call above WRAPS context.Background() (or another context) to build
	// up more specific behavior — contexts form a parent-child chain.
	fmt.Println("-- context.Background has no deadline --")
	bg := context.Background()
	_, hasDeadline := bg.Deadline()
	fmt.Println("Background() has deadline:", hasDeadline)

	// ---------- passing values through context ----------
	// context.WithValue attaches a request-scoped value (e.g. a request
	// ID for logging) that any function further down the call chain can
	// read via ctx.Value(). Use sparingly — it's for cross-cutting
	// metadata, NOT a substitute for normal function parameters.
	ctx4 := context.WithValue(context.Background(), requestIDKey, "req-12345")
	logWithRequestID(ctx4)
}

// A custom, unexported type for context keys avoids collisions with keys
// used by other packages — using a plain string key is a common mistake
// since a package can't be sure no one else picked the same string.
type contextKey string

const requestIDKey contextKey = "requestID"

func logWithRequestID(ctx context.Context) {
	if reqID, ok := ctx.Value(requestIDKey).(string); ok {
		fmt.Println("Handling request:", reqID)
	}
}
