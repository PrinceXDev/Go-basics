package main

// ============================================================================
// CONCEPT: sync.Pool — reuse temporary objects instead of reallocating them.
//
// Every allocation costs time and adds pressure on the garbage collector.
// If your code repeatedly allocates short-lived objects of the SAME type
// (buffers, scratch slices, temporary structs), sync.Pool lets goroutines
// share a cache of already-allocated ones instead of allocating fresh
// every time.
//
//   pool.Get()  -> returns an existing item from the pool, or calls New()
//                  if the pool is currently empty
//   pool.Put(x) -> returns x to the pool for future reuse
//
// CRITICAL RULES:
//   - Pool is for PERFORMANCE, not correctness or lifetime management. The
//     runtime is free to silently drop anything in the pool at any time
//     (e.g. during GC) — never store something in a Pool that you need to
//     survive, and never rely on Get() returning something specific.
//   - ALWAYS reset an object's state before Put()-ing it back (or right
//     after Get()-ing it) — you're handed someone else's leftovers.
//   - Only pool objects that are actually expensive to allocate/reuse
//     repeatedly (buffers are the textbook case). Pooling a small struct
//     you allocate twice a second is not worth the complexity.
//
// JS/TS comparison: no real equivalent — V8's GC is generational and quite
// good with short-lived objects, and JS code rarely fights allocator
// pressure the way high-throughput Go servers do (this is the same reason
// e.g. encoding/json and bytes.Buffer-heavy code often reaches for Pool).
// ============================================================================

import (
	"bytes"
	"fmt"
	"sync"
)

var bufferPool = sync.Pool{
	// New is called only when the pool has nothing to hand out.
	New: func() any {
		fmt.Println("allocating a brand-new buffer")
		return new(bytes.Buffer)
	},
}

func render(id int) string {
	buf := bufferPool.Get().(*bytes.Buffer)
	buf.Reset() // MUST reset: this buffer may have someone else's leftover data
	defer bufferPool.Put(buf)

	fmt.Fprintf(buf, "rendered-item-%d", id)
	return buf.String()
}

func main() {
	// First few calls likely allocate; once buffers are returned to the
	// pool, later calls reuse them instead of allocating again.
	for i := 1; i <= 5; i++ {
		fmt.Println(render(i))
	}

	// ---------- Under concurrent load ----------
	// Many goroutines can Get/Put safely at once — Pool is itself
	// safe for concurrent use, no extra Mutex needed around it.
	var wg sync.WaitGroup
	results := make([]string, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = render(idx)
		}(i)
	}
	wg.Wait()
	fmt.Println("processed", len(results), "items using a shared buffer pool")
}
