package perf

// ============================================================================
// LESSON 44: BENCHMARKS, PROFILING AND ESCAPE ANALYSIS
//
// WHY THIS MATTERS
// Go makes performance work unusually pleasant: the benchmark runner, the
// profiler and the allocation tracker are all in the standard toolchain,
// and they agree with each other. You never have to guess.
//
// THE ONLY RULE THAT MATTERS: MEASURE FIRST.
// Every "optimisation" in this file is a legitimate technique, and every
// one of them is the WRONG choice in some program. The point isn't to
// memorise the tricks — it's to learn the loop:
//
//     benchmark -> profile -> change ONE thing -> benchmark -> benchstat
//
// This file holds several implementations of the same few jobs, written
// slow and fast, so perf_test.go can measure the difference. Run:
//
//	go test -bench=. -benchmem ./44_benchmarks_and_profiling/
//
// See README.md in this folder for the profiling walkthrough.
// ============================================================================

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// JOB 1: JOIN NUMBERS INTO A STRING
// The classic: string concatenation in a loop is O(n^2) because strings are
// immutable, so every += allocates and copies the whole thing again.
// ---------------------------------------------------------------------------

// JoinConcat is the naive version. Fine for 3 items, catastrophic for 10,000.
func JoinConcat(nums []int) string {
	s := ""
	for _, n := range nums {
		s += strconv.Itoa(n) + ","
	}
	return strings.TrimSuffix(s, ",")
}

// JoinBuilder uses strings.Builder: one growing buffer, O(n).
func JoinBuilder(nums []int) string {
	var b strings.Builder
	for i, n := range nums {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.Itoa(n))
	}
	return b.String()
}

// JoinBuilderGrow pre-sizes the buffer. Builder doubles its capacity as it
// grows, so without Grow you pay several copies. If you can estimate the
// final size, this removes them.
func JoinBuilderGrow(nums []int) string {
	var b strings.Builder
	b.Grow(len(nums) * 4) // rough estimate: ~3 digits + a comma
	for i, n := range nums {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.Itoa(n))
	}
	return b.String()
}

// JoinAppend avoids the intermediate strings entirely: strconv.AppendInt
// writes the digits straight into a []byte with no allocation per number.
// This is usually the fastest, and the least readable — a fair trade only
// once a profile says this function matters.
func JoinAppend(nums []int) string {
	buf := make([]byte, 0, len(nums)*4)
	for i, n := range nums {
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = strconv.AppendInt(buf, int64(n), 10)
	}
	// This conversion DOES copy. unsafe.String would avoid it; don't.
	return string(buf)
}

// JoinSprintf is here as a warning. fmt is convenient and reflection-heavy —
// never put it in a hot loop.
func JoinSprintf(nums []int) string {
	s := ""
	for _, n := range nums {
		s = fmt.Sprintf("%s%d,", s, n)
	}
	return strings.TrimSuffix(s, ",")
}

// ---------------------------------------------------------------------------
// JOB 2: BUILD A SLICE
// Preallocating with make([]T, 0, n) removes the repeated grow-and-copy that
// append does as a slice outgrows its capacity (lesson 09).
// ---------------------------------------------------------------------------

func SquaresNoPrealloc(n int) []int {
	var out []int // nil slice, capacity 0
	for i := range n {
		out = append(out, i*i) // grows: 1,2,4,8,16... each a fresh allocation
	}
	return out
}

func SquaresPrealloc(n int) []int {
	out := make([]int, 0, n) // one allocation, exactly the right size
	for i := range n {
		out = append(out, i*i)
	}
	return out
}

// ---------------------------------------------------------------------------
// JOB 3: COUNT WORDS
// Demonstrates that the obvious "split then count" allocates a whole slice
// of substrings you immediately throw away.
// ---------------------------------------------------------------------------

func CountWordsSplit(s string) map[string]int {
	counts := make(map[string]int)
	for _, w := range strings.Fields(s) { // allocates a []string
		counts[strings.ToLower(w)]++ // ToLower allocates per word
	}
	return counts
}

// CountWordsScan walks the string once and only allocates for keys that are
// actually new. strings.FieldsSeq (Go 1.24+) iterates without building a
// slice at all.
func CountWordsScan(s string) map[string]int {
	counts := make(map[string]int, 64) // sized hint: fewer rehashes
	for w := range strings.FieldsSeq(s) {
		// The map lookup with a non-allocating key first: only lowercase
		// (and thus allocate) when the word isn't already lowercase.
		if hasUpper(w) {
			w = strings.ToLower(w)
		}
		counts[w]++
	}
	return counts
}

func hasUpper(s string) bool {
	for i := range len(s) {
		if s[i] >= 'A' && s[i] <= 'Z' {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// JOB 4: LOOKUP — slice vs map
// The answer is "it depends on n", and the benchmark will show you where
// the crossover is. For small n, a linear scan over a contiguous slice beats
// a map, because of cache locality and hashing cost.
// ---------------------------------------------------------------------------

type Lookup struct {
	slice []string
	set   map[string]struct{} // struct{} uses zero bytes — the Go "set" idiom
}

func NewLookup(items []string) *Lookup {
	set := make(map[string]struct{}, len(items))
	for _, it := range items {
		set[it] = struct{}{}
	}
	sorted := slices.Clone(items)
	slices.Sort(sorted)
	return &Lookup{slice: sorted, set: set}
}

func (l *Lookup) HasLinear(s string) bool {
	return slices.Contains(l.slice, s)
}

func (l *Lookup) HasBinary(s string) bool {
	_, found := slices.BinarySearch(l.slice, s)
	return found
}

func (l *Lookup) HasMap(s string) bool {
	_, ok := l.set[s]
	return ok
}

// ---------------------------------------------------------------------------
// JOB 5: ESCAPE ANALYSIS — stack vs heap
//
// Go decides for you whether a value lives on the stack (free, freed on
// return) or the heap (costs an allocation and GC pressure). A value
// "escapes to the heap" when the compiler can't prove its lifetime ends
// with the function.
//
// See exactly what it decided:
//
//	go build -gcflags='-m' ./44_benchmarks_and_profiling/
//	go build -gcflags='-m -m' ./...   # more detail on WHY
// ---------------------------------------------------------------------------

type Point struct{ X, Y float64 }

// Stays on the stack: the value is copied out, nothing outlives the call.
func NewPointValue(x, y float64) Point {
	p := Point{x, y}
	return p
}

// Escapes: we return a pointer, so p must outlive the function.
func NewPointPointer(x, y float64) *Point {
	p := Point{x, y}
	return &p // "moved to heap: p"
}

// Escapes: `any` (an interface) forces a heap allocation for the boxed
// value. This is why fmt.Println is more expensive than it looks — every
// argument is boxed into an interface.
func Box(p Point) any {
	return p
}

// SumPoints takes a slice by value; the backing array is the CALLER's, so
// nothing escapes here.
func SumPoints(ps []Point) Point {
	var total Point
	for _, p := range ps {
		total.X += p.X
		total.Y += p.Y
	}
	return total
}

// ----------------------------------------------------------------------------
// THE HABITS THAT ACTUALLY MATTER (in rough order of payoff)
//   1. Pick the right algorithm and data structure. No micro-optimisation
//      beats turning O(n^2) into O(n).
//   2. Don't do the work at all: cache it, batch it, or do it once at
//      startup.
//   3. Preallocate slices and maps when you know the size.
//   4. strings.Builder / bytes.Buffer instead of += in a loop.
//   5. Keep fmt out of hot paths.
//   6. Reduce allocations before you reduce CPU — in Go, allocation
//      pressure IS usually the CPU problem, via the GC.
//   7. Only then: sync.Pool, unsafe tricks, assembly. Almost never worth it.
// ----------------------------------------------------------------------------
