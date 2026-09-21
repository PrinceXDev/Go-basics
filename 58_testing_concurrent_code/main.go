package main

// ============================================================================
// CONCEPT: Testing concurrent code.
//
// This file holds a small piece of concurrent code (a thread-safe counter);
// main_test.go next to it shows the four practical rules for testing
// anything concurrent:
//
//   1. Always run with -race. It won't catch every bug, but it catches
//      the single most common one (an unprotected shared variable) for free.
//   2. Never synchronize a test with time.Sleep. Sleeping "long enough" is
//      a guess -- flaky on a slow CI box, and it hides real bugs the rest
//      of the time. Use a WaitGroup or a channel to know FOR SURE something
//      finished.
//   3. Use t.Parallel() to actually run tests concurrently with each other --
//      this is itself a great way to surface shared-state bugs (e.g. tests
//      that accidentally depend on global state or run order).
//   4. Assert on the OUTCOME (final state, count, returned value), not on
//      timing or interleaving order, which is never guaranteed.
//
// Run: go test -race ./58_testing_concurrent_code/...
// ============================================================================

import "sync"

// Counter is deliberately simple -- the point of this lesson is HOW to
// test it, not the implementation itself.
type Counter struct {
	mu    sync.Mutex
	value int
}

func (c *Counter) Inc() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.value++
}

func (c *Counter) Value() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.value
}

func main() {
	c := &Counter{}
	var wg sync.WaitGroup
	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Inc()
		}()
	}
	wg.Wait()
	println("final count:", c.Value())
}
