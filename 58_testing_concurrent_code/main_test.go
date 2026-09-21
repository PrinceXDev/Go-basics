package main

import (
	"sync"
	"testing"
)

// TestCounter_Concurrent proves the final value with a WaitGroup -- NOT a
// time.Sleep guess. wg.Wait() returns only when every goroutine has truly
// called Done(), so this assertion is deterministic no matter how slow or
// fast the machine running it is.
func TestCounter_Concurrent(t *testing.T) {
	c := &Counter{}

	const goroutines = 500
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Inc()
		}()
	}
	wg.Wait() // <-- the correct way to know they're all done, never time.Sleep

	if got := c.Value(); got != goroutines {
		t.Fatalf("Value() = %d, want %d", got, goroutines)
	}
}

// TestCounter_Parallel and TestCounter_Parallel2 use t.Parallel() to run
// AGAINST EACH OTHER concurrently. Each uses its own *Counter, so there's
// no shared state between them -- that isolation is what makes running
// them in parallel safe. (If they touched a shared global Counter instead,
// running them in parallel would immediately expose that bug -- which is
// exactly the kind of mistake t.Parallel() is good at catching.)
func TestCounter_Parallel(t *testing.T) {
	t.Parallel()
	c := &Counter{}
	c.Inc()
	c.Inc()
	if got := c.Value(); got != 2 {
		t.Fatalf("Value() = %d, want 2", got)
	}
}

func TestCounter_Parallel2(t *testing.T) {
	t.Parallel()
	c := &Counter{}
	for i := 0; i < 10; i++ {
		c.Inc()
	}
	if got := c.Value(); got != 10 {
		t.Fatalf("Value() = %d, want 10", got)
	}
}

// TestCounter_NoRaceOnConcurrentReads exercises Inc and Value from many
// goroutines at once. On its own this test can't prove there's no race --
// that's what `go test -race` is for. Run:
//
//	go test -race ./58_testing_concurrent_code/...
//
// If Counter's Mutex were ever removed, -race would fail this test
// immediately with a DATA RACE report naming both goroutines and the
// exact line each touched -- without -race, it might just pass silently
// while still being unsafe.
func TestCounter_NoRaceOnConcurrentReads(t *testing.T) {
	c := &Counter{}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Inc()
			_ = c.Value()
		}()
	}
	wg.Wait()
	if got := c.Value(); got != 100 {
		t.Fatalf("Value() = %d, want 100", got)
	}
}
