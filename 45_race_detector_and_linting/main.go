package main

// ============================================================================
// CONCEPT: the race detector, `go vet`, and golangci-lint — the three tools
// that should run on every commit.
//
// WHY THIS MATTERS
// Lesson 24 proved a race exists by showing a wrong number (992 instead of
// 1000). That only worked because the race was blatant. Real races are
// worse: they're rare, timing-dependent, and produce correct results on
// your laptop for months before corrupting data in production on a machine
// with more cores.
//
// The race detector removes the guesswork. It instruments every memory
// access and reports a race the FIRST time two goroutines touch the same
// address without synchronisation — even if the output happened to be
// correct that run.
//
// RUN IT BOTH WAYS. The contrast is the entire lesson:
//
//	go run ./45_race_detector_and_linting            # may print correct numbers
//	go run -race ./45_race_detector_and_linting      # reports the races
//
// ============================================================================

import (
	"flag"
	"fmt"
	"sync"
	"sync/atomic"
)

func main() {
	crash := flag.Bool("crash", false, "also run the concurrent-map demo, which kills the process")
	flag.Parse()

	fmt.Println("race detector enabled:", raceEnabled)
	fmt.Println()

	racyCounter()
	fixedCounterMutex()
	fixedCounterAtomic()
	racySlice()
	fixedSliceChannel()
	racyStructField()
	closureCapture()

	if *crash {
		fmt.Println()
		fmt.Println("== concurrent map writes (this KILLS the process) ==")
		concurrentMapCrash()
	} else {
		fmt.Println()
		fmt.Println("(skipping the concurrent-map demo — rerun with -crash to see it)")
	}

	fmt.Println()
	if raceEnabled {
		fmt.Println("Race detector was ON — scroll up for the WARNING: DATA RACE reports.")
	} else {
		fmt.Println("Now run it again with:  go run -race ./45_race_detector_and_linting")
	}
}

// ---------------------------------------------------------------------------
// RACE 1: the unsynchronised counter (lesson 24, revisited)
// `count++` is THREE operations: read, add, write. Two goroutines can read
// the same value, both add 1, and both write back the same result — one
// increment vanishes.
// ---------------------------------------------------------------------------

func racyCounter() {
	count := 0
	var wg sync.WaitGroup
	for range 1000 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			count++ // RACE: concurrent read/write of `count`
		}()
	}
	wg.Wait()
	// Often prints 1000 on a fast machine. Correct output is NOT proof of
	// correctness — that's the point.
	fmt.Printf("%-34s %d (want 1000)\n", "racy counter:", count)
}

// ---------------------------------------------------------------------------
// FIX A: a mutex. Use this when you're guarding more than one variable, or
// a whole invariant.
// ---------------------------------------------------------------------------

func fixedCounterMutex() {
	var (
		mu    sync.Mutex
		count int
		wg    sync.WaitGroup
	)
	for range 1000 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mu.Lock()
			defer mu.Unlock() // defer means it unlocks even if the body panics
			count++
		}()
	}
	wg.Wait()
	fmt.Printf("%-34s %d\n", "fixed with sync.Mutex:", count)
}

// ---------------------------------------------------------------------------
// FIX B: atomics. Faster and simpler, but ONLY for a single value. The
// typed wrappers (atomic.Int64, atomic.Bool, atomic.Pointer[T]) are much
// harder to misuse than the old atomic.AddInt64(&x, 1) functions.
// ---------------------------------------------------------------------------

func fixedCounterAtomic() {
	var count atomic.Int64
	var wg sync.WaitGroup
	for range 1000 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			count.Add(1)
		}()
	}
	wg.Wait()
	fmt.Printf("%-34s %d\n", "fixed with atomic.Int64:", count.Load())
}

// ---------------------------------------------------------------------------
// RACE 2: appending to a shared slice
// append can REALLOCATE the backing array. Two goroutines appending at once
// can write to the same index, or one can lose the other's reallocation
// entirely. This one usually DOES produce a visibly wrong length.
// ---------------------------------------------------------------------------

func racySlice() {
	var results []int
	var wg sync.WaitGroup
	for i := range 100 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results = append(results, i) // RACE
		}(i)
	}
	wg.Wait()
	fmt.Printf("%-34s %d (want 100)\n", "racy slice append:", len(results))
}

// ---------------------------------------------------------------------------
// FIX: collect through a channel. Often cleaner than a mutex, and it's the
// shape Go's proverb points at: "Don't communicate by sharing memory;
// share memory by communicating."
//
// (A mutex around the append is equally correct and sometimes simpler.
// Preallocating and writing to results[i] — distinct indices — is also
// race-free and the fastest option when you know the count up front.)
// ---------------------------------------------------------------------------

func fixedSliceChannel() {
	ch := make(chan int, 100) // buffered: senders never block
	var wg sync.WaitGroup
	for i := range 100 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ch <- i
		}(i)
	}
	// Close the channel once every sender is done, so the range below ends.
	go func() { wg.Wait(); close(ch) }()

	results := make([]int, 0, 100)
	for v := range ch { // one goroutine owns the slice -> no race
		results = append(results, v)
	}
	fmt.Printf("%-34s %d\n", "fixed with a channel:", len(results))
}

// ---------------------------------------------------------------------------
// RACE 3: a struct field written by one goroutine, read by another
// This is the shape of nearly every REAL race: a cache, a config reload, a
// "ready" flag, a connection pool's stats. Note the read side looks totally
// innocent.
// ---------------------------------------------------------------------------

type unsafeCache struct {
	hits int
	data map[string]string
}

func racyStructField() {
	c := &unsafeCache{data: map[string]string{"a": "1"}}
	var wg sync.WaitGroup

	wg.Add(2)
	go func() { // writer
		defer wg.Done()
		for range 1000 {
			c.hits++ // RACE
		}
	}()
	go func() { // reader — looks harmless, is not
		defer wg.Done()
		total := 0
		for range 1000 {
			total += c.hits // RACE
		}
	}()
	wg.Wait()
	fmt.Printf("%-34s %d\n", "racy struct field:", c.hits)
}

// ---------------------------------------------------------------------------
// NOT A RACE ANY MORE: the loop variable
// Before Go 1.22, `for i := range n { go func(){ use(i) }() }` shared ONE
// `i` across every goroutine — the single most common Go bug ever written.
// Go 1.22 made each iteration get its own variable, so this is now correct.
// You will still see the old `func(i int)` workaround everywhere; it is
// harmless, just no longer necessary.
// ---------------------------------------------------------------------------

func closureCapture() {
	var wg sync.WaitGroup
	seen := make([]int, 5)
	for i := range 5 {
		wg.Add(1)
		go func() { // no parameter needed since Go 1.22
			defer wg.Done()
			seen[i] = i * i // distinct indices -> not a race
		}()
	}
	wg.Wait()
	fmt.Printf("%-34s %v\n", "per-iteration loop var (1.22+):", seen)
}

// ---------------------------------------------------------------------------
// THE ONE THAT ISN'T A RACE — IT'S A CRASH
// Go's runtime detects concurrent map writes on its own, WITHOUT -race, and
// deliberately kills the process: "fatal error: concurrent map writes".
// It is not recoverable. Use sync.Map, a mutex, or shard the map.
// ---------------------------------------------------------------------------

func concurrentMapCrash() {
	m := map[int]int{}
	var wg sync.WaitGroup
	for i := range 1000 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			m[i] = i // fatal error: concurrent map writes
		}(i)
	}
	wg.Wait()
	fmt.Println("if you see this, you got lucky; run it again")
}

// ----------------------------------------------------------------------------
// HOW TO READ A RACE REPORT
//
//	WARNING: DATA RACE
//	Write at 0x00c000126010 by goroutine 8:      <- the second access
//	  main.racyCounter.func1()
//	      main.go:78 +0x2c
//
//	Previous read at 0x00c000126010 by goroutine 7:   <- the first access
//	  main.racyCounter.func1()
//	      main.go:78 +0x18
//
//	Goroutine 8 (running) created at:            <- where each was spawned
//	  main.racyCounter()
//	      main.go:76 +0x8c
//
// Read it as: "these two stacks touched the same address, and nothing
// happens-before between them." Both stacks are the evidence; the fix goes
// wherever the shared state is owned.
//
// ABOUT -race
//   * ~2-20x slower and ~5-10x more memory. Fine for tests and staging;
//     usually too expensive for production (though some teams run one
//     canary instance with it).
//   * It finds races that ACTUALLY HAPPEN during the run. It cannot prove
//     the absence of races — so exercise concurrency in your tests
//     (t.Parallel, b.RunParallel, a loop of 100 goroutines).
//   * ALWAYS run `go test -race ./...` in CI. This is non-negotiable for
//     any concurrent Go code.
//   * Build a racy binary with `go build -race` when you need to reproduce
//     something outside the test harness.
//
// See README.md in this folder for `go vet`, `golangci-lint`, and the
// .golangci.yml config.
// ----------------------------------------------------------------------------
