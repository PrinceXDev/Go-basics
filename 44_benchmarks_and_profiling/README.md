# 44 — Benchmarks & Profiling

`perf.go` holds several implementations of the same jobs; `perf_test.go`
measures them. This README covers the tooling around them: `benchstat`,
`pprof`, escape analysis, and the trace viewer.

---

## 1. Run the benchmarks

```bash
go test -bench=. -benchmem ./44_benchmarks_and_profiling/
```

Reading the output:

```
BenchmarkJoin/Concat-24        3134    383718 ns/op   2939542 B/op   1997 allocs/op
                     │          │         │              │              └─ allocations per call
                     │          │         │              └─ bytes allocated per call
                     │          │         └─ nanoseconds per call  ← the headline
                     │          └─ how many iterations the runner chose
                     └─ GOMAXPROCS (your core count)
```

Real numbers from this folder, n=1000:

| implementation | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `JoinConcat` | 383,718 | 2,939,542 | 1,997 |
| `JoinSprintf` | 432,030 | 2,967,152 | 2,996 |
| `JoinBuilder` | 14,158 | 29,776 | 1,011 |
| `JoinBuilderGrow` | 13,464 | 21,328 | 1,000 |
| `JoinAppend` | **7,908** | 22,528 | **4** |

`strings.Builder` is **27x** faster than `+=`, and `AppendInt` cuts
allocations from 1,997 to 4. Note how closely ns/op tracks allocs/op — in
Go, allocation pressure usually *is* the CPU problem, via the garbage
collector.

Run just the scaling benchmark to watch `Concat` go quadratic:

```bash
go test -bench=JoinScaling -benchmem ./44_benchmarks_and_profiling/
```

---

## 2. Compare before/after properly: `benchstat`

A single benchmark run is noise. `benchstat` runs statistics over repeated
measurements and tells you whether a change is real.

```bash
go install golang.org/x/perf/cmd/benchstat@latest

# before your change
go test -bench=Join -count=10 ./44_benchmarks_and_profiling/ > old.txt
# ... make the change ...
go test -bench=Join -count=10 ./44_benchmarks_and_profiling/ > new.txt

benchstat old.txt new.txt
```

It prints a delta and a p-value. **If benchstat says `~` (no significant
difference), your optimisation did nothing** — revert it and keep the
simpler code.

---

## 3. CPU profiling

```bash
go test -bench=Join -cpuprofile=cpu.out ./44_benchmarks_and_profiling/
go tool pprof cpu.out
```

At the `(pprof)` prompt:

| command | what it shows |
|---|---|
| `top` | the 10 functions burning the most CPU |
| `top -cum` | ranked by *cumulative* time (includes callees) |
| `list JoinConcat` | the function's source, annotated line by line |
| `web` | a visual call graph (needs Graphviz installed) |
| `peek runtime.mallocgc` | who calls the allocator |

The killer command is `list`. It shows you the exact line:

```
(pprof) list JoinConcat
      .          .     43:   for _, n := range nums {
   2.31s      2.31s     44:       s += strconv.Itoa(n) + ","
      .          .     45:   }
```

Prefer the browser UI — flame graph, source view, and the call graph in one
place:

```bash
go tool pprof -http=:6060 cpu.out
```

---

## 4. Memory profiling

```bash
go test -bench=Squares -memprofile=mem.out ./44_benchmarks_and_profiling/
go tool pprof -sample_index=alloc_space mem.out
```

Four sample types, and picking the right one matters:

- `alloc_space` — total bytes allocated over the run. **Use this for GC
  pressure**, which is what usually hurts throughput.
- `alloc_objects` — total number of allocations.
- `inuse_space` — bytes still live at the snapshot. **Use this to hunt
  leaks.**
- `inuse_objects` — live object count.

---

## 5. Profiling a running server

Add one import to any HTTP service and you get live profiles from
production:

```go
import _ "net/http/pprof"   // registers handlers on http.DefaultServeMux

// Expose it on a SEPARATE, internal-only port. Never on your public API —
// these endpoints leak memory contents and let anyone DoS you.
go func() {
    log.Println(http.ListenAndServe("127.0.0.1:6060", nil))
}()
```

Then, against the live process:

```bash
go tool pprof http://localhost:6060/debug/pprof/profile?seconds=30   # CPU
go tool pprof http://localhost:6060/debug/pprof/heap                 # memory
go tool pprof http://localhost:6060/debug/pprof/goroutine            # goroutine leaks
go tool pprof http://localhost:6060/debug/pprof/mutex                # lock contention
go tool pprof http://localhost:6060/debug/pprof/block                # blocking ops
```

`/debug/pprof/goroutine?debug=2` in a browser dumps every goroutine's stack
— the fastest way to find a goroutine leak, which shows up as thousands of
goroutines parked on the same line.

**Security note:** if you use a custom `ServeMux` (you should — lesson 39),
`net/http/pprof` registers on `DefaultServeMux`, so it won't be exposed on
your API by accident. Keep it that way.

---

## 6. Escape analysis — stack or heap?

```bash
go build -gcflags='-m' ./44_benchmarks_and_profiling/
```

Output for `perf.go`:

```
./perf.go:229:2: moved to heap: p        # NewPointPointer — returns &p
./perf.go:237:9: p escapes to heap       # Box — boxed into an interface
```

Add a second `-m` for the reasoning (`-gcflags='-m -m'`).

Things that force a heap allocation:

- returning a pointer to a local
- storing a value in an `interface{}`/`any` (this is why `fmt.Println` is
  costly — every argument gets boxed)
- capturing a variable in a closure that outlives the function
- a slice or map whose size isn't known at compile time
- anything the compiler can't prove doesn't escape (e.g. passed to a
  function via an interface, so it can't see the callee)

Measured in `BenchmarkEscape`:

```
value/stack        0.70 ns/op    0 B/op   0 allocs/op
pointer/heap      18.70 ns/op   16 B/op   1 allocs/op
interface/boxed   15.00 ns/op   16 B/op   1 allocs/op
```

**Do not** restructure code to avoid the heap on instinct. A heap
allocation is ~20ns. Reach for this only when a profile points here.

---

## 7. Execution tracer

For concurrency problems — goroutines blocked, poor parallelism, GC pauses
— the tracer beats pprof:

```bash
go test -bench=LookupParallel -trace=trace.out ./44_benchmarks_and_profiling/
go tool trace trace.out
```

It opens a browser UI with a per-core timeline, goroutine analysis, network
blocking profile, and syscall blocking profile. This is what you use when
CPU usage is low but latency is high.

---

## 8. Benchmark pitfalls

1. **Dead-code elimination.** If you don't use the result, the compiler
   deletes the work. Assign to a package-level sink (see `sinkString` in
   `perf_test.go`), or use `b.Loop()` (Go 1.24+), which the compiler is
   required to keep.
2. **Constant folding.** `Box(Point{1, 2})` reported 0 allocs until the
   input was made non-constant — anything computable at build time will be.
3. **Setup inside the timer.** Use `b.ResetTimer()` after setup.
4. **A cold cache / first-run effects.** Use `-count=10` and `benchstat`.
5. **A laptop on battery, or a CPU thermal-throttling.** Benchmark on a
   quiet machine; be suspicious of <5% differences anywhere.
6. **Benchmarking the wrong thing.** An unrealistic input size gives you an
   answer to a question nobody asked. Use production-shaped data.

---

## The loop, one more time

```
1. Is it actually too slow?   (if no: stop, you are done)
2. Write a benchmark that reproduces the slowness.
3. Profile it. Find the top entry.
4. Change ONE thing.
5. benchstat old.txt new.txt. Significant? Keep it. `~`? Revert it.
6. Re-run the tests — a fast wrong answer is still wrong.
7. Go to 1.
```

Most Go performance work ends at step 1.
