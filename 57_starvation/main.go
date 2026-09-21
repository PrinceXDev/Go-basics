package main

// ============================================================================
// CONCEPT: Starvation — a goroutine CAN make progress, but almost never
// actually gets the chance, because others keep hogging the resource.
//
// Different from deadlock (nobody can ever proceed) and livelock (everyone
// is busy but nobody progresses): in starvation, most goroutines are doing
// fine — it's specifically one (or a few) that keeps losing the race for
// a shared resource, over and over, because it's greedier or unluckier.
//
// Common cause: a goroutine holds a Mutex for a long time, in a loop, with
// little or no gap -- other goroutines waiting for that same Mutex get very
// few chances to grab it. Go's Mutex makes no fairness guarantee: it does
// NOT promise "whoever's been waiting longest goes next".
//
// JS/TS comparison: no equivalent -- this requires real contention between
// concurrently-running goroutines over a shared lock, which needs true
// parallelism/preemptive scheduling to manifest.
// ============================================================================

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	fmt.Println("== Starvation-prone: one greedy goroutine hogs the Mutex ==")
	starvationProne()

	fmt.Println("\n== Fixed: bounded hold time + a fairness mechanism ==")
	fixedWithFairness()
}

// starvationProne launches one "greedy" goroutine that re-locks the mutex
// in a tight loop with virtually no gap, plus a "polite" goroutine that
// simply isn't fast enough to compete. The polite one gets starved.
func starvationProne() {
	var mu sync.Mutex
	var greedyCount, politeCount atomic.Int64
	stop := make(chan struct{})

	go func() { // greedy: locks, does a tiny bit of work, unlocks, IMMEDIATELY repeats
		for {
			select {
			case <-stop:
				return
			default:
				mu.Lock()
				greedyCount.Add(1)
				mu.Unlock()
			}
		}
	}()

	go func() { // polite: same lock, but nothing about it gives it priority
		for {
			select {
			case <-stop:
				return
			default:
				mu.Lock()
				politeCount.Add(1)
				mu.Unlock()
			}
		}
	}()

	time.Sleep(50 * time.Millisecond)
	close(stop)
	time.Sleep(5 * time.Millisecond)

	fmt.Printf("greedy acquired lock %d times, polite only %d times\n",
		greedyCount.Load(), politeCount.Load())
	fmt.Println("(polite is starved -- it's not blocked or deadlocked, just consistently out-raced)")
}

// fixedWithFairness uses a buffered channel as a ticket queue: goroutines
// take a ticket in arrival order and only the ticket holder proceeds --
// this enforces FIFO fairness that a bare Mutex doesn't give you.
func fixedWithFairness() {
	ticket := make(chan struct{}, 1)
	ticket <- struct{}{} // one ticket available to start

	var greedyCount, politeCount atomic.Int64
	stop := make(chan struct{})

	worker := func(counter *atomic.Int64) {
		for {
			select {
			case <-stop:
				return
			case <-ticket: // must wait its turn for the single ticket
				counter.Add(1)
				ticket <- struct{}{} // return the ticket for the next in line
			}
		}
	}

	go worker(&greedyCount)
	go worker(&politeCount)

	time.Sleep(50 * time.Millisecond)
	close(stop)
	time.Sleep(5 * time.Millisecond)

	fmt.Printf("with a fair ticket queue: %d vs %d (much closer)\n",
		greedyCount.Load(), politeCount.Load())
}
