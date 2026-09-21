package main

// ============================================================================
// CONCEPT: Deadlock — every goroutine involved is stuck waiting, forever.
//
// The classic definition needs ALL of these at once:
//   1. Mutual exclusion  - a resource only one goroutine can hold
//   2. Hold and wait     - a goroutine holds one resource while waiting for another
//   3. No preemption     - nothing can force a goroutine to give up what it holds
//   4. Circular wait     - A waits on B, B waits on A (a cycle)
//
// Break any one of those and deadlock becomes impossible. In Go, the two
// deadlocks you'll actually hit are:
//   a) Unbuffered channel send/receive with no matching partner.
//   b) Two Mutexes locked in inconsistent order by different goroutines.
//
// Go's runtime detects the special case "every single goroutine in the
// whole program is asleep" and panics immediately with:
//     fatal error: all goroutines are asleep - deadlock!
// It does NOT detect a partial deadlock where other goroutines are still
// running — that just hangs forever with no error. This file demonstrates
// both, with the actual deadlocking code commented out (uncomment to see
// it hang/panic) plus the fixed version left running.
//
// JS/TS comparison: no true equivalent — async/await over a single-threaded
// event loop can't deadlock this way (nothing "holds" a lock while another
// task waits on it); the nearest miss is an unresolved Promise nobody ever
// resolves, which just leaves a dangling handler, not a runtime panic.
// ============================================================================

import (
	"fmt"
	"sync"
)

func main() {
	fmt.Println("== Deadlock example 1: unbuffered channel with no receiver ==")
	deadlockChannelExample()

	fmt.Println("\n== Deadlock example 2: inconsistent Mutex lock order ==")
	deadlockMutexExample()

	fmt.Println("\nAll examples completed without deadlocking (fixed versions).")
}

func deadlockChannelExample() {
	// THE BUG (uncomment to see it hang, then Go detects and panics):
	//
	//   ch := make(chan int)
	//   ch <- 1  // blocks forever: unbuffered channel needs a receiver
	//            // ready AT THE SAME TIME, and there isn't one.
	//
	// fatal error: all goroutines are asleep - deadlock!

	// THE FIX: either buffer the channel, or have a receiver ready.
	ch := make(chan int, 1) // buffered: send succeeds without a receiver
	ch <- 1
	fmt.Println("buffered send succeeded, received:", <-ch)

	// Or: start the receiver first, in its own goroutine.
	ch2 := make(chan int)
	go func() { fmt.Println("receiver got:", <-ch2) }()
	ch2 <- 2
}

func deadlockMutexExample() {
	var mu1, mu2 sync.Mutex

	// THE BUG (uncomment to see it hang — no panic this time, since other
	// goroutines are still alive, it just hangs silently forever):
	//
	//   var wg sync.WaitGroup
	//   wg.Add(2)
	//   go func() { // goroutine A: locks mu1 then mu2
	//       defer wg.Done()
	//       mu1.Lock()
	//       time.Sleep(10 * time.Millisecond)
	//       mu2.Lock() // waits for B to release mu2... which it never does
	//       mu2.Unlock()
	//       mu1.Unlock()
	//   }()
	//   go func() { // goroutine B: locks mu2 then mu1 -- OPPOSITE ORDER
	//       defer wg.Done()
	//       mu2.Lock()
	//       time.Sleep(10 * time.Millisecond)
	//       mu1.Lock() // waits for A to release mu1... which it never does
	//       mu1.Unlock()
	//       mu2.Unlock()
	//   }()
	//   wg.Wait() // hangs forever: A holds mu1 waiting for mu2, B holds mu2
	//             // waiting for mu1 -- a circular wait.

	// THE FIX: every goroutine locks shared mutexes in the SAME order.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		mu1.Lock()
		mu2.Lock()
		mu2.Unlock()
		mu1.Unlock()
	}()
	go func() {
		defer wg.Done()
		mu1.Lock() // same order as above: mu1 then mu2, no cycle possible
		mu2.Lock()
		mu2.Unlock()
		mu1.Unlock()
	}()
	wg.Wait()
	fmt.Println("both goroutines locked mu1 then mu2, consistently -- no deadlock")
}
