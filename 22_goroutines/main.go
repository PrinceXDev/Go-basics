package main

// ============================================================================
// CONCEPT: Goroutines — lightweight, concurrently-running functions.
//
// WHY THIS MATTERS
// JS is single-threaded with an event loop: async code (Promises,
// async/await) interleaves on ONE thread, and true parallel CPU work
// needs Worker Threads. Go is different: a goroutine is a lightweight
// thread MANAGED BY THE GO RUNTIME, and Go programs can genuinely run
// code in parallel across multiple CPU cores.
//
// Starting one is almost absurdly simple: put `go` before a function call.
// That's the entire syntax. The tricky part isn't starting them — it's
// coordinating them safely, which is what this lesson's examples build
// toward (channels get their own lesson, 23).
//
// JS/TS comparison: `go doSomething()` is a bit like firing off a Promise
// without awaiting it — it starts running and you move on immediately.
// The difference: goroutines can run truly in parallel on multi-core
// machines, and there's no built-in "await" for a goroutine's completion —
// you need explicit coordination (sync.WaitGroup, or channels).
// ============================================================================

import (
	"fmt"
	"sync"
	"time"
)

func sayHello(name string) {
	fmt.Println("Hello from", name)
}

func main() {
	// ---------- THE PROBLEM: goroutines run independently ----------
	// Starting a goroutine does NOT wait for it to finish — main()
	// continues immediately to the next line. Without something to make
	// main() wait, the program might exit before the goroutine ever runs.
	go sayHello("goroutine 1")
	fmt.Println("main continues immediately, goroutine may not have run yet")

	// A crude (and NOT idiomatic) fix: sleep briefly to give it time.
	// This is fragile and only here to illustrate the problem — real Go
	// code never uses sleep for synchronization.
	time.Sleep(50 * time.Millisecond)

	// ---------- THE REAL FIX: sync.WaitGroup ----------
	// WaitGroup lets main() wait for a known number of goroutines to
	// finish, with no guessing about timing.
	//   Add(n)  -> "expect n more goroutines to finish"
	//   Done()  -> "I'm finished" (called by each goroutine, usually deferred)
	//   Wait()  -> blocks until the count reaches zero
	var wg sync.WaitGroup

	names := []string{"Alice", "Bob", "Charlie"}
	for _, name := range names {
		wg.Add(1)
		// IMPORTANT GOTCHA: `name` is passed as an ARGUMENT here, not
		// captured directly from the loop variable. In older Go versions
		// (pre-1.22), closing over a loop variable directly was a classic
		// bug (all goroutines would see the SAME final value of `name`).
		// Passing it as a parameter sidesteps this entirely and is still
		// the clearest, most defensive style to use.
		go func(n string) {
			defer wg.Done() // guarantees Done() runs even if this panics
			fmt.Println("Processing:", n)
		}(name)
	}
	wg.Wait() // blocks here until all 3 goroutines call Done()
	fmt.Println("All goroutines finished")

	// ---------- DEMONSTRATING ACTUAL CONCURRENCY ----------
	// Launch several goroutines that each "do work" (simulated with a
	// short sleep) — notice they interleave rather than running strictly
	// one after another.
	var wg2 sync.WaitGroup
	for i := 1; i <= 3; i++ {
		wg2.Add(1)
		go func(id int) {
			defer wg2.Done()
			time.Sleep(time.Duration(id) * 10 * time.Millisecond)
			fmt.Printf("Worker %d done\n", id)
		}(i)
	}
	wg2.Wait()
	fmt.Println("All workers finished")
}
