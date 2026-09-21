package main

// ============================================================================
// CONCEPT: sync.RWMutex — a Mutex that tells readers and writers apart.
//
// Lesson 24 covered sync.Mutex: only ONE goroutine may hold the lock,
// whether it's reading or writing. That's always safe, but it's wasteful
// when you have MANY readers and FEW writers — readers don't conflict with
// each other, only with a writer.
//
// RWMutex has two lock modes:
//   RLock() / RUnlock()  -> "read lock": any number of goroutines can hold
//                            this AT THE SAME TIME, as long as no one holds
//                            the write lock.
//   Lock()  / Unlock()   -> "write lock": exclusive, exactly like Mutex —
//                            blocks until every reader AND writer is done.
//
// Rule of thumb: reads vastly outnumber writes -> RWMutex is a real win.
// Roughly balanced or writes dominate -> plain Mutex is simpler and just
// as fast (RWMutex has slightly more bookkeeping overhead per lock).
//
// JS/TS comparison: there's no built-in equivalent — JS is single-threaded,
// so "many readers at once" was never a race to begin with. This class of
// optimization only exists because Go can run goroutines in true parallel.
// ============================================================================

import (
	"fmt"
	"sync"
	"time"
)

// SafeCache is read far more often than it's written — a textbook RWMutex case.
type SafeCache struct {
	mu   sync.RWMutex
	data map[string]int
}

func NewSafeCache() *SafeCache {
	return &SafeCache{data: make(map[string]int)}
}

func (c *SafeCache) Get(key string) (int, bool) {
	c.mu.RLock() // multiple goroutines can be inside Get() at once
	defer c.mu.RUnlock()
	v, ok := c.data[key]
	return v, ok
}

func (c *SafeCache) Set(key string, value int) {
	c.mu.Lock() // exclusive: every reader/writer is blocked out
	defer c.mu.Unlock()
	c.data[key] = value
}

func main() {
	cache := NewSafeCache()
	cache.Set("visits", 0)

	var wg sync.WaitGroup

	// 20 concurrent readers — none of them block each other.
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			v, _ := cache.Get("visits")
			_ = v // simulate using the value
		}(i)
	}

	// 5 concurrent writers — each needs exclusive access.
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			cache.mu.Lock()
			cache.data["visits"]++
			cache.mu.Unlock()
		}(i)
	}

	wg.Wait()
	v, _ := cache.Get("visits")
	fmt.Println("final visits count:", v) // always 5, writes are exclusive

	// ---------- Demonstrating readers overlap, writers don't ----------
	fmt.Println("\ntiming demo: readers run concurrently, writers serialize")
	demo := NewSafeCache()
	demo.Set("x", 1)

	start := time.Now()
	var wg2 sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg2.Add(1)
		go func(id int) {
			defer wg2.Done()
			demo.mu.RLock()
			time.Sleep(50 * time.Millisecond) // simulate slow read
			demo.mu.RUnlock()
		}(i)
	}
	wg2.Wait()
	// ~50ms total, not 200ms — the 4 reads overlapped instead of queuing.
	fmt.Println("4 concurrent reads took:", time.Since(start).Round(time.Millisecond))
}
