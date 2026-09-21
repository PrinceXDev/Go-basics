package main

// ============================================================================
// CONCEPT: the sync/atomic package — lock-free operations on single values.
//
// Lesson 24 fixed a race condition with a Mutex: Lock, mutate, Unlock. For
// SIMPLE cases — a single counter, a single flag, a single pointer — the
// atomic package does the same job without an explicit lock, using CPU-level
// atomic instructions. It's narrower than a Mutex (it only protects ONE
// value, not a block of logic) but noticeably cheaper for that one case.
//
// Go 1.19+ gives typed atomics — prefer these over the old atomic.AddInt64
// style functions, they're harder to misuse:
//   atomic.Int64, atomic.Int32, atomic.Uint64, atomic.Bool, atomic.Value, ...
//   v.Load()        -> read the current value
//   v.Store(x)       -> set it
//   v.Add(delta)     -> add delta, returns new value
//   v.CompareAndSwap(old, new) -> "set to new, but ONLY if it's still old"
//                                  — the building block for lock-free algorithms
//
// Rule of thumb: one counter/flag/pointer -> atomic. Multiple related
// fields that must change together, or any logic beyond a single
// read/write -> Mutex (atomic can't protect "if X then Y" across two
// separate fields; a Mutex protects that whole section).
//
// JS/TS comparison: no equivalent need in plain JS (single-threaded). The
// closest conceptual cousin is Atomics on a SharedArrayBuffer with Web
// Workers — same idea (lock-free ops on shared memory), rarely used.
// ============================================================================

import (
	"fmt"
	"sync"
	"sync/atomic"
)

func main() {
	// ---------- atomic counter vs the lesson-24 race condition ----------
	var counter atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			counter.Add(1) // safe: no Mutex needed for a single counter
		}()
	}
	wg.Wait()
	fmt.Println("atomic counter (always exactly 1000):", counter.Load())

	// ---------- atomic.Bool as a "stop" flag ----------
	var stop atomic.Bool
	var wgWorkers sync.WaitGroup
	for i := 1; i <= 3; i++ {
		wgWorkers.Add(1)
		go func(id int) {
			defer wgWorkers.Done()
			n := 0
			for !stop.Load() { // cheap check, no lock contention
				n++
				if n > 1_000_000 {
					break // safety valve for this demo
				}
			}
			fmt.Printf("worker %d stopped after %d iterations\n", id, n)
		}(i)
	}
	stop.Store(true) // signal all workers to stop
	wgWorkers.Wait()

	// ---------- CompareAndSwap: the lock-free building block ----------
	// "Only update if nobody else changed it since I last looked."
	var state atomic.Int32
	state.Store(1)

	swapped := state.CompareAndSwap(1, 2) // succeeds: current value is 1
	fmt.Println("first CAS (1->2) succeeded:", swapped, "new value:", state.Load())

	swapped = state.CompareAndSwap(1, 3) // fails: current value is now 2, not 1
	fmt.Println("second CAS (1->3) succeeded:", swapped, "value unchanged:", state.Load())
}
