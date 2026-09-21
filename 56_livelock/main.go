package main

// ============================================================================
// CONCEPT: Livelock — goroutines stay busy but never make real progress.
//
// Different from deadlock: nobody is blocked/asleep. Every goroutine is
// actively running, actively responding to the other — they're just stuck
// in a loop of reacting to each other forever, like two people in a
// hallway who both keep stepping aside for the other and never get past.
//
// Typically caused by "polite" retry logic: a goroutine tries to grab a
// resource, sees it's contended, backs off and retries -- and if BOTH
// goroutines back off and retry in lockstep, they can keep colliding
// forever without either one ever winning.
//
// JS/TS comparison: no real equivalent — this requires two things running
// truly concurrently and reacting to shared state in real time, which
// single-threaded JS can't do outside of manufactured setTimeout loops.
// ============================================================================

import (
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"
)

type spinLock struct {
	locked atomic.Bool
}

// tryLock is a naive "try, and if busy, immediately try again" lock.
// This is the ingredient that causes livelock below.
func (s *spinLock) tryLock() bool {
	return s.locked.CompareAndSwap(false, true)
}
func (s *spinLock) unlock() { s.locked.Store(false) }

func main() {
	fmt.Println("== Livelock-prone: both sides retry instantly on contention ==")
	livelockProne()

	fmt.Println("\n== Fixed: randomized backoff breaks the lockstep ==")
	fixedWithBackoff()
}

// livelockProne shows two goroutines that, when they collide, both back
// off and immediately retry -- with no randomness, they can keep colliding
// in near lockstep for a while. Capped with maxAttempts so the demo
// terminates instead of actually hanging.
func livelockProne() {
	lock := &spinLock{}
	var wg sync.WaitGroup
	const maxAttempts = 200

	politeWorker := func(id int) {
		defer wg.Done()
		attempts := 0
		for attempts < maxAttempts {
			if lock.tryLock() {
				lock.unlock()
				fmt.Printf("worker %d: acquired after %d attempts\n", id, attempts)
				return
			}
			attempts++
			// No randomness here: if both workers retry at the same cadence,
			// they can keep bouncing off each other's lock repeatedly.
		}
		fmt.Printf("worker %d: gave up after %d attempts (livelock symptom)\n", id, attempts)
	}

	wg.Add(2)
	go politeWorker(1)
	go politeWorker(2)
	wg.Wait()
}

// fixedWithBackoff adds a small RANDOM delay before retrying. That randomness
// is what breaks the symmetry -- eventually one goroutine's retry lands
// while the other is still waiting, and it wins the lock.
func fixedWithBackoff() {
	lock := &spinLock{}
	var wg sync.WaitGroup

	backoffWorker := func(id int) {
		defer wg.Done()
		attempts := 0
		for {
			if lock.tryLock() {
				fmt.Printf("worker %d: acquired after %d attempts\n", id, attempts)
				time.Sleep(5 * time.Millisecond) // simulate doing work
				lock.unlock()
				return
			}
			attempts++
			// Randomized backoff: breaks the lockstep that caused livelock.
			time.Sleep(time.Duration(rand.Intn(5)) * time.Millisecond)
		}
	}

	wg.Add(2)
	go backoffWorker(1)
	go backoffWorker(2)
	wg.Wait()
}
