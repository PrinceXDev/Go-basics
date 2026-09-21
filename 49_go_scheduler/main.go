package main

// ============================================================================
// CONCEPT: The Go Scheduler — how goroutines actually get run.
//
// Go doesn't hand goroutines straight to the OS. It uses an M:N scheduler:
//   G = Goroutine   (your lightweight task, thousands can exist)
//   M = Machine     (a real OS thread)
//   P = Processor   (a context that lets an M run G's; count = GOMAXPROCS)
//
// Many G's are multiplexed onto few M's, via a handful of P's. That's the
// "M:N" part: M goroutines mapped onto N OS threads, not 1:1.
//
// GOMAXPROCS controls how many P's exist, i.e. how many goroutines can run
// TRULY IN PARALLEL (on separate CPU cores) at once. Default = number of
// CPU cores. More goroutines than that just means the scheduler switches
// between them on the same core (concurrency), not that they all run at
// the same instant (parallelism).
//
// JS/TS comparison: Node has ONE thread for your JS code (the event loop) —
// concurrency there comes entirely from I/O callbacks interleaving, never
// from two of your functions executing at the literal same instant. Go's
// scheduler can genuinely run goroutines simultaneously on multiple cores.
//
// A goroutine yields control (lets the scheduler run something else) at:
//   - a blocking channel op (send/receive)
//   - a blocking syscall (file/network I/O)
//   - time.Sleep
//   - a function call, sometimes (the compiler inserts periodic checks)
// A CPU-bound loop with none of these can, in rare cases, hog a P — this is
// why real workloads almost always have channel ops or I/O sprinkled in.
// ============================================================================

import (
	"fmt"
	"runtime"
	"sync"
)

func main() {
	// GOMAXPROCS: how many OS threads can run Go code in parallel right now.
	fmt.Println("GOMAXPROCS:", runtime.GOMAXPROCS(0))
	fmt.Println("CPU cores available:", runtime.NumCPU())

	// ---------- Concurrency vs parallelism, made visible ----------
	// Force everything onto ONE OS thread: goroutines now take turns
	// (concurrency) instead of running simultaneously (parallelism).
	prev := runtime.GOMAXPROCS(1)
	fmt.Println("\n-- with GOMAXPROCS(1): goroutines interleave, don't overlap --")
	runBusyWorkers()

	// Restore, then let the runtime use every core: on a multi-core
	// machine you may see the workers' output interleave differently,
	// because they can now genuinely execute at the same instant.
	runtime.GOMAXPROCS(prev)
	fmt.Println("\n-- with GOMAXPROCS restored: goroutines may run in true parallel --")
	runBusyWorkers()

	// ---------- runtime.NumGoroutine ----------
	// Cheap way to sanity-check you aren't leaking goroutines (lesson 15
	// covers leaks in depth) — the worker pool exercise uses this same
	// check before/after.
	fmt.Println("\ngoroutines alive right now:", runtime.NumGoroutine())

	// ---------- runtime.Gosched ----------
	// Voluntarily yields the current goroutine so the scheduler can run
	// something else. Rarely needed in real code (channel ops already
	// yield) — mainly useful for demonstrating that the scheduler is
	// cooperative, not preemptive-by-default for tight CPU loops.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 3; i++ {
			fmt.Println("background goroutine step", i)
			runtime.Gosched() // "someone else can go now"
		}
		close(done)
	}()
	<-done
}

func runBusyWorkers() {
	var wg sync.WaitGroup
	for i := 1; i <= 3; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			sum := 0
			for j := 0; j < 5_000_000; j++ {
				sum += j
			}
			fmt.Printf("worker %d finished (sum=%d)\n", id, sum)
		}(i)
	}
	wg.Wait()
}
