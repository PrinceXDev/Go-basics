package main

// ============================================================================
// CONCEPT: `select`, `sync.Mutex`, and race conditions.
//
// PART A — select
// `select` waits on MULTIPLE channels at once and proceeds with whichever
// one is ready first. Syntactically it looks like `switch` (lesson 06),
// but each `case` is a channel operation, not a value comparison.
// JS/TS comparison: closest is `Promise.race([...])` — proceed with
// whichever promise/channel resolves first.
//
// PART B — sync.Mutex and race conditions
// Channels aren't the only way to coordinate goroutines. Sometimes
// multiple goroutines need to safely read/modify ONE shared variable —
// that's what a Mutex (mutual exclusion lock) is for. Without one, you
// get a RACE CONDITION: unpredictable results because goroutines step on
// each other mid-update. JS never has this problem for synchronous code,
// because JS is single-threaded — this class of bug is genuinely new
// territory coming from JS.
// ============================================================================

import (
	"fmt"
	"sync"
	"time"
)

func main() {
	// ---------- select with multiple channels ----------
	ch1 := make(chan string)
	ch2 := make(chan string)

	go func() {
		time.Sleep(30 * time.Millisecond)
		ch1 <- "result from ch1"
	}()
	go func() {
		time.Sleep(10 * time.Millisecond)
		ch2 <- "result from ch2"
	}()

	// select blocks until ONE of the cases is ready. Since ch2's
	// goroutine sleeps for less time, its case fires first.
	for i := 0; i < 2; i++ {
		select {
		case msg1 := <-ch1:
			fmt.Println("Got:", msg1)
		case msg2 := <-ch2:
			fmt.Println("Got:", msg2)
		}
	}

	// select with a timeout — an extremely common real-world pattern:
	// "wait for a result, but give up after N milliseconds".
	slowChannel := make(chan string)
	go func() {
		time.Sleep(200 * time.Millisecond)
		slowChannel <- "finally done"
	}()

	select {
	case res := <-slowChannel:
		fmt.Println("Received:", res)
	case <-time.After(50 * time.Millisecond):
		fmt.Println("Timed out waiting for slowChannel")
	}

	// select with `default` — makes a channel check NON-BLOCKING: if no
	// case is ready immediately, `default` runs instead of waiting.
	nonBlocking := make(chan int)
	select {
	case v := <-nonBlocking:
		fmt.Println("got value:", v)
	default:
		fmt.Println("no value ready, moving on")
	}

	// ---------- RACE CONDITION demonstration ----------
	// Many goroutines incrementing the SAME variable with no protection.
	// The "expected" result is 1000, but without synchronization, some
	// increments get lost because goroutines can read-modify-write the
	// variable at overlapping times.
	counter := 0
	var wg sync.WaitGroup
	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			counter++ // NOT safe: read-modify-write isn't atomic
		}()
	}
	wg.Wait()
	fmt.Println("Unsafe counter (expected 1000, may be less):", counter)

	// ---------- THE FIX: sync.Mutex ----------
	// Lock() before touching shared state, Unlock() after — guarantees
	// only one goroutine executes that section at a time.
	var mu sync.Mutex
	safeCounter := 0
	var wg2 sync.WaitGroup
	for i := 0; i < 1000; i++ {
		wg2.Add(1)
		go func() {
			defer wg2.Done()
			mu.Lock()
			safeCounter++
			mu.Unlock()
		}()
	}
	wg2.Wait()
	fmt.Println("Safe counter (always exactly 1000):", safeCounter)
}
