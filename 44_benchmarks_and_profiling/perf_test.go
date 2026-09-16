package perf

// ============================================================================
// BENCHMARKS
//
// A benchmark is a function `func BenchmarkXxx(b *testing.B)` in a _test.go
// file. The runner calls your loop enough times to get a stable measurement
// and reports NANOSECONDS PER OPERATION.
//
//	go test -bench=. ./44_benchmarks_and_profiling/            # all benchmarks
//	go test -bench=Join -benchmem ./44_...                     # just Join*, with allocs
//	go test -bench=. -benchtime=5s ./44_...                    # run longer
//	go test -bench=. -count=10 ./44_... | tee new.txt          # for benchstat
//
// -benchmem is not optional. Wall-clock alone hides the allocation
// behaviour that usually IS the performance problem.
// ============================================================================

import (
	"fmt"
	"strings"
	"testing"
)

// Package-level sinks. Assigning results to a package variable stops the
// compiler from deleting your benchmark body as dead code — a real hazard
// that silently produces "0.3 ns/op" results.
var (
	sinkString string
	sinkInts   []int
	sinkMap    map[string]int
	sinkBool   bool
)

func makeNums(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i * 37
	}
	return out
}

// ---------------------------------------------------------------------------
// 1. COMPARING IMPLEMENTATIONS
// Same input, several algorithms, one sub-benchmark each. b.Run mirrors
// t.Run and gives you a table you can read top to bottom.
// ---------------------------------------------------------------------------

func BenchmarkJoin(b *testing.B) {
	nums := makeNums(1000)

	impls := map[string]func([]int) string{
		"Concat":      JoinConcat,
		"Sprintf":     JoinSprintf,
		"Builder":     JoinBuilder,
		"BuilderGrow": JoinBuilderGrow,
		"Append":      JoinAppend,
	}

	for name, fn := range impls {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs() // always. same as passing -benchmem.
			for b.Loop() {   // Go 1.24+: the modern loop, immune to dead-code elimination
				sinkString = fn(nums)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 2. SCALING: how does cost grow with input size?
// This is how you SEE an O(n^2) algorithm rather than reasoning about it.
// Watch Concat's ns/op go up ~100x from n=100 to n=1000 while Builder's
// goes up ~10x.
// ---------------------------------------------------------------------------

func BenchmarkJoinScaling(b *testing.B) {
	for _, n := range []int{10, 100, 1000, 10000} {
		nums := makeNums(n)

		b.Run(fmt.Sprintf("Concat/n=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				sinkString = JoinConcat(nums)
			}
		})
		b.Run(fmt.Sprintf("Builder/n=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				sinkString = JoinBuilder(nums)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 3. PREALLOCATION
// The clearest possible demonstration that allocs/op is the number to watch.
// ---------------------------------------------------------------------------

func BenchmarkSquares(b *testing.B) {
	const n = 10000

	b.Run("no-prealloc", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			sinkInts = SquaresNoPrealloc(n)
		}
	})
	b.Run("prealloc", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			sinkInts = SquaresPrealloc(n)
		}
	})
}

// ---------------------------------------------------------------------------
// 4. EXPENSIVE SETUP: b.ResetTimer / b.StopTimer
// Setup must not be counted. ResetTimer zeroes the clock after setup;
// Stop/StartTimer brackets per-iteration work you don't want measured.
// ---------------------------------------------------------------------------

func BenchmarkCountWords(b *testing.B) {
	// Building the corpus is setup — expensive, and not what we're measuring.
	var sb strings.Builder
	words := []string{"the", "quick", "Brown", "fox", "JUMPS", "over", "lazy", "dog"}
	for i := range 20000 {
		sb.WriteString(words[i%len(words)])
		sb.WriteByte(' ')
	}
	corpus := sb.String()

	b.Run("split", func(b *testing.B) {
		b.ResetTimer() // <- discard everything measured so far
		b.ReportAllocs()
		for b.Loop() {
			sinkMap = CountWordsSplit(corpus)
		}
	})
	b.Run("scan", func(b *testing.B) {
		b.ResetTimer()
		b.ReportAllocs()
		for b.Loop() {
			sinkMap = CountWordsScan(corpus)
		}
	})
}

// ---------------------------------------------------------------------------
// 5. WHERE THE CROSSOVER IS
// "Use a map for lookups" is only true above some n. Measure YOUR n.
// ---------------------------------------------------------------------------

func BenchmarkLookup(b *testing.B) {
	for _, n := range []int{4, 16, 64, 1024} {
		items := make([]string, n)
		for i := range items {
			items[i] = fmt.Sprintf("key-%05d", i)
		}
		l := NewLookup(items)
		miss := "key-99999" // worst case: a linear scan must check everything

		b.Run(fmt.Sprintf("linear/n=%d", n), func(b *testing.B) {
			for b.Loop() {
				sinkBool = l.HasLinear(miss)
			}
		})
		b.Run(fmt.Sprintf("binary/n=%d", n), func(b *testing.B) {
			for b.Loop() {
				sinkBool = l.HasBinary(miss)
			}
		})
		b.Run(fmt.Sprintf("map/n=%d", n), func(b *testing.B) {
			for b.Loop() {
				sinkBool = l.HasMap(miss)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 6. ESCAPE ANALYSIS, MEASURED
// The alloc counts here are the ground truth behind `-gcflags=-m`.
// ---------------------------------------------------------------------------

var (
	sinkPoint Point
	sinkPtr   *Point
	sinkAny   any
)

func BenchmarkEscape(b *testing.B) {
	b.Run("value/stack", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			sinkPoint = NewPointValue(1, 2) // expect 0 allocs/op
		}
	})
	b.Run("pointer/heap", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			sinkPtr = NewPointPointer(1, 2) // expect 1 alloc/op
		}
	})
	b.Run("interface/boxed", func(b *testing.B) {
		// The value must be non-constant, or the compiler boxes it into a
		// static read-only value and reports 0 allocs — a real trap when
		// writing micro-benchmarks. Anything the compiler can compute at
		// build time, it will.
		x := 1.0
		b.ReportAllocs()
		for b.Loop() {
			x++
			sinkAny = Box(Point{x, x}) // boxing into `any` allocates
		}
	})
}

// ---------------------------------------------------------------------------
// 7. PARALLEL BENCHMARKS
// RunParallel measures behaviour under concurrency — contention on mutexes,
// shared-cache-line effects, allocator pressure across threads. This is the
// realistic shape for a server benchmark.
// ---------------------------------------------------------------------------

func BenchmarkLookupParallel(b *testing.B) {
	items := make([]string, 1024)
	for i := range items {
		items[i] = fmt.Sprintf("key-%05d", i)
	}
	l := NewLookup(items)

	b.RunParallel(func(pb *testing.PB) {
		// Each goroutine gets its own local sink; writing a shared package
		// variable from many goroutines would be a data race (lesson 45).
		var local bool
		for pb.Next() {
			local = l.HasMap("key-00512")
		}
		sinkBool = local
	})
}

// ---------------------------------------------------------------------------
// 8. TESTS — because a fast wrong answer is still wrong
// Always keep correctness tests next to your benchmarks. The very first
// thing an optimisation breaks is behaviour at the edges.
// ---------------------------------------------------------------------------

func TestJoinImplementationsAgree(t *testing.T) {
	for _, nums := range [][]int{nil, {}, {7}, {1, 2, 3}, makeNums(50)} {
		want := JoinBuilder(nums)
		for name, fn := range map[string]func([]int) string{
			"Concat":      JoinConcat,
			"Sprintf":     JoinSprintf,
			"BuilderGrow": JoinBuilderGrow,
			"Append":      JoinAppend,
		} {
			if got := fn(nums); got != want {
				t.Errorf("%s(%v) = %q, want %q", name, nums, got, want)
			}
		}
	}
}

func TestCountWordsAgree(t *testing.T) {
	const corpus = "The quick BROWN fox the Fox FOX"
	split, scan := CountWordsSplit(corpus), CountWordsScan(corpus)
	if len(split) != len(scan) {
		t.Fatalf("different key counts: %v vs %v", split, scan)
	}
	for k, v := range split {
		if scan[k] != v {
			t.Errorf("key %q: split=%d scan=%d", k, v, scan[k])
		}
	}
	if split["fox"] != 3 {
		t.Errorf(`"fox" = %d, want 3`, split["fox"])
	}
}
