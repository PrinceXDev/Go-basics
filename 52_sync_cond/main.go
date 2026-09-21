package main

// ============================================================================
// CONCEPT: sync.Cond — wake goroutines up when a condition becomes true.
//
// A Mutex protects shared state. But what if a goroutine needs to WAIT
// until that state satisfies some condition (e.g. "queue is non-empty")
// before it can proceed? Spinning in a loop re-checking would waste CPU.
// sync.Cond solves this: goroutines sleep efficiently until someone
// explicitly signals that the condition might now be true.
//
//   cond := sync.NewCond(&mu)   // Cond is always tied to a Locker (a Mutex)
//   cond.Wait()   -> atomically unlocks mu and sleeps; on wake, re-locks mu
//                    before returning. MUST be called with mu already held.
//   cond.Signal() -> wakes ONE waiting goroutine
//   cond.Broadcast() -> wakes ALL waiting goroutines
//
// The waiting side ALWAYS re-checks the condition in a for-loop, never a
// plain `if` — a woken goroutine isn't guaranteed the condition is still
// true (someone else might have grabbed it first). This is the standard,
// mandatory pattern:
//
//   mu.Lock()
//   for !conditionIsTrue() {
//       cond.Wait()
//   }
//   ... use the state ...
//   mu.Unlock()
//
// In modern Go, channels or context usually replace Cond for simple
// signaling. Cond earns its keep for a shared-state condition that many
// goroutines need to re-check, like a bounded queue below.
//
// JS/TS comparison: nothing maps directly — this is coordinating multiple
// OS-scheduled goroutines around shared mutable state, a problem that
// doesn't arise in single-threaded JS.
// ============================================================================

import (
	"fmt"
	"sync"
)

// Queue is a small bounded FIFO. Consumers block (via Cond) when it's
// empty; producers block when it's full — both wake the other side.
type Queue struct {
	mu       sync.Mutex
	cond     *sync.Cond
	items    []int
	capacity int
}

func NewQueue(capacity int) *Queue {
	q := &Queue{capacity: capacity}
	q.cond = sync.NewCond(&q.mu)
	return q
}

func (q *Queue) Push(v int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.items) == q.capacity {
		q.cond.Wait() // full: sleep until a consumer makes room and signals
	}
	q.items = append(q.items, v)
	q.cond.Broadcast() // wake anyone waiting for "not empty"
}

func (q *Queue) Pop() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.items) == 0 {
		q.cond.Wait() // empty: sleep until a producer adds something
	}
	v := q.items[0]
	q.items = q.items[1:]
	q.cond.Broadcast() // wake anyone waiting for "not full"
	return v
}

func main() {
	q := NewQueue(3)
	var wg sync.WaitGroup

	// Producer: pushes 10 items into a queue that only holds 3 at a time —
	// it will block on Wait() whenever the queue is full.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 1; i <= 10; i++ {
			q.Push(i)
			fmt.Println("pushed", i)
		}
	}()

	// Consumer: pops 10 items, blocking on Wait() whenever the queue is
	// empty, until the producer pushes something and Broadcasts.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			v := q.Pop()
			fmt.Println("popped", v)
		}
	}()

	wg.Wait()
	fmt.Println("done: producer and consumer stayed coordinated via Cond")
}
