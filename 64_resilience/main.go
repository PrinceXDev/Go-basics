package main

// ============================================================================
// CONCEPT: the concurrency-control toolbox every Go backend eventually needs.
//
// Lessons 22-25 and 48-58 taught the PRIMITIVES (goroutines, channels,
// mutexes). This lesson is about the five patterns you actually reach for in
// a service, and -- more importantly -- WHEN each one is the right answer:
//
//	errgroup           run N things in parallel, stop on the first error
//	errgroup.SetLimit  the same, but never more than N at once
//	semaphore          bound concurrency around a scarce resource
//	rate.Limiter       bound the RATE (requests per second), not the count
//	singleflight       collapse duplicate concurrent work into one call
//
// THE MENTAL MODEL. Three different words that people confuse constantly:
//
//	CONCURRENCY LIMIT   how many at the SAME TIME      (semaphore, SetLimit)
//	RATE LIMIT          how many PER SECOND            (rate.Limiter)
//	DEDUPLICATION       how many DISTINCT calls at all (singleflight)
//
// A database with 20 connections needs a concurrency limit. A third-party
// API that allows 100 req/s needs a rate limit. A cache stampede on one hot
// key needs singleflight. Using the wrong one does nothing useful.
//
// Run: go run ./64_resilience
// ============================================================================

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
	"golang.org/x/sync/singleflight"
	"golang.org/x/time/rate"
)

func main() {
	demo1WaitGroupVsErrgroup()
	demo2ErrgroupCancellation()
	demo3ErrgroupLimit()
	demo4Semaphore()
	demo5RateLimiter()
	demo6Singleflight()
	demo7GoroutineLeak()
	notes()
}

// ===========================================================================
// 1. errgroup vs sync.WaitGroup
//
// WaitGroup waits. That is all it does -- it has no idea an error happened,
// so you end up bolting on a mutex and an error slice. errgroup IS that
// pattern, done properly: it waits, returns the FIRST non-nil error, and
// (with WithContext) cancels everything else.
//
// Rule: if the parallel work can fail, use errgroup. If it truly cannot
// fail, WaitGroup is still fine and marginally cheaper.
// ===========================================================================

func demo1WaitGroupVsErrgroup() {
	fmt.Println("=== 1. errgroup: parallel fan-out with error handling ===")

	ctx := context.Background()
	start := time.Now()

	var (
		user    string
		orders  []string
		balance int
	)

	g, ctx := errgroup.WithContext(ctx)

	// Each Go() call runs in its own goroutine. Write to a DIFFERENT variable
	// in each one -- that is what makes this race-free without a mutex.
	// (Run this whole lesson with -race to prove it.)
	g.Go(func() error {
		v, err := fetch(ctx, "user-service", 80*time.Millisecond, "prince")
		user = v
		return err
	})
	g.Go(func() error {
		v, err := fetch(ctx, "order-service", 120*time.Millisecond, "o1,o2,o3")
		orders = []string{v}
		return err
	})
	g.Go(func() error {
		_, err := fetch(ctx, "billing-service", 60*time.Millisecond, "")
		balance = 4_200
		return err
	})

	if err := g.Wait(); err != nil {
		fmt.Println("  failed:", err)
		return
	}

	fmt.Printf("  got %s / %v / %d in %v\n", user, orders, balance, time.Since(start).Round(10*time.Millisecond))
	fmt.Println("  -> 80+120+60=260ms of work finished in ~120ms (the slowest one)")
}

// ===========================================================================
// 2. errgroup.WithContext: the first failure cancels the siblings.
//
// This is the part people miss. Without cancellation, a fan-out where one
// call fails immediately still waits for the 30-second one to finish before
// returning the error. With it, the slow sibling is told to stop the moment
// the verdict is known.
// ===========================================================================

func demo2ErrgroupCancellation() {
	fmt.Println("\n=== 2. First error cancels the rest ===")

	start := time.Now()
	g, ctx := errgroup.WithContext(context.Background())

	g.Go(func() error {
		time.Sleep(50 * time.Millisecond)
		return errors.New("payment service rejected the card") // fails fast
	})

	g.Go(func() error {
		// A well-behaved worker WATCHES ctx. One that ignores it keeps
		// burning CPU (or a DB connection) for the full duration -- the
		// cancellation only works if the callee cooperates.
		select {
		case <-time.After(3 * time.Second):
			fmt.Println("  slow task finished (you should NOT see this)")
			return nil
		case <-ctx.Done():
			fmt.Println("  slow task noticed the cancellation and stopped")
			return ctx.Err()
		}
	})

	err := g.Wait()
	fmt.Printf("  Wait returned %q after %v (not 3s)\n", err, time.Since(start).Round(10*time.Millisecond))
	fmt.Println("  note: Wait returns the FIRST error only. Need them all? -> errors.Join")
}

// ===========================================================================
// 3. errgroup.SetLimit: bounded fan-out.
//
// THE BUG THIS PREVENTS: `for _, id := range tenThousandIDs { g.Go(...) }`
// launches ten thousand concurrent database queries. Goroutines are cheap;
// the connections, file handles and remote rate limits behind them are not.
// SetLimit(n) makes Go() BLOCK until a slot frees up.
// ===========================================================================

func demo3ErrgroupLimit() {
	fmt.Println("\n=== 3. errgroup.SetLimit: bounded parallelism ===")

	var inFlight, peak atomic.Int64

	g := new(errgroup.Group)
	g.SetLimit(4) // never more than 4 at once

	for i := 1; i <= 20; i++ {
		g.Go(func() error {
			cur := inFlight.Add(1)
			for {
				old := peak.Load()
				if cur <= old || peak.CompareAndSwap(old, cur) {
					break
				}
			}
			defer inFlight.Add(-1)

			time.Sleep(time.Duration(20+rand.Intn(30)) * time.Millisecond)
			return nil
		})
	}
	_ = g.Wait()

	fmt.Printf("  20 jobs, limit 4 -> peak concurrency observed: %d\n", peak.Load())
}

// ===========================================================================
// 4. semaphore.Weighted: when the cost of each job DIFFERS.
//
// SetLimit counts jobs. A weighted semaphore counts RESOURCE UNITS, so a job
// that needs 4 units of something scarce (memory, GPU, connections) takes
// four slots. Also, unlike SetLimit, Acquire takes a context -- so a waiting
// job can give up when the request is cancelled.
// ===========================================================================

func demo4Semaphore() {
	fmt.Println("\n=== 4. semaphore.Weighted: weighted, cancellable limiting ===")

	const totalMemoryUnits = 8
	sem := semaphore.NewWeighted(totalMemoryUnits)

	ctx := context.Background()
	var wg sync.WaitGroup
	var used, peak atomic.Int64

	jobs := []struct {
		name string
		cost int64
	}{
		{"thumbnail", 1}, {"thumbnail", 1}, {"thumbnail", 1},
		{"video-transcode", 6}, {"pdf-render", 3}, {"thumbnail", 1},
	}

	for _, j := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()

			// Blocks until `cost` units are free, or ctx is cancelled.
			if err := sem.Acquire(ctx, j.cost); err != nil {
				return
			}
			defer sem.Release(j.cost)

			cur := used.Add(j.cost)
			if cur > peak.Load() {
				peak.Store(cur)
			}
			time.Sleep(40 * time.Millisecond)
			used.Add(-j.cost)
		}()
	}
	wg.Wait()

	fmt.Printf("  capacity %d units -> peak usage never exceeded %d\n", totalMemoryUnits, peak.Load())
}

// ===========================================================================
// 5. rate.Limiter: requests PER SECOND, with a burst allowance.
//
// TOKEN BUCKET, in one picture:
//
//	tokens refill at `limit` per second, up to `burst` capacity
//	every request removes one token; no token -> wait (or fail)
//
//	burst  = how much of a spike you tolerate
//	limit  = the sustained rate you promise not to exceed
//
// Three ways to consume:
//
//	Wait(ctx)  block until a token is free   -> for OUTBOUND calls you own
//	Allow()    true/false, never blocks      -> for INBOUND: reject with 429
//	Reserve()  tells you HOW LONG you'd wait -> for deciding between the two
// ===========================================================================

func demo5RateLimiter() {
	fmt.Println("\n=== 5. rate.Limiter: 10 req/s, burst 3 ===")

	lim := rate.NewLimiter(rate.Limit(10), 3) // 10/s sustained, 3 instant
	ctx := context.Background()
	start := time.Now()

	for i := 1; i <= 6; i++ {
		_ = lim.Wait(ctx) // blocks once the burst is spent
		fmt.Printf("  request %d at %v\n", i, time.Since(start).Round(10*time.Millisecond))
	}
	fmt.Println("  -> first 3 go instantly (the burst), then one every ~100ms")

	// Allow() is the INBOUND shape: never make a caller wait, tell them 429
	// and let them back off. Blocking an inbound request just moves the queue
	// into your own process, where it costs you goroutines and memory.
	lim2 := rate.NewLimiter(rate.Limit(2), 2)
	allowed, rejected := 0, 0
	for i := 0; i < 6; i++ {
		if lim2.Allow() {
			allowed++
		} else {
			rejected++
		}
	}
	fmt.Printf("  inbound style: %d allowed, %d rejected -> return 429 + Retry-After\n", allowed, rejected)
	fmt.Println("  (per-user limiting = a map[userID]*rate.Limiter behind a mutex,")
	fmt.Println("   with eviction; distributed limiting needs Redis, not this)")
}

// ===========================================================================
// 6. singleflight: collapse duplicate in-flight work.
//
// THE PROBLEM (cache stampede / thundering herd): a hot cache key expires.
// 500 concurrent requests all miss, and all 500 hit the database with the
// IDENTICAL query. The database falls over, everything retries, it gets
// worse.
//
// THE FIX: the first caller does the work; everyone else waiting on the same
// key gets that same result. One query instead of 500.
//
// THE CATCH: a shared result means a shared FAILURE, and a shared stale
// value. Do not use it for writes, and remember every caller gets the same
// error if the one real call fails.
// ===========================================================================

func demo6Singleflight() {
	fmt.Println("\n=== 6. singleflight: 100 concurrent misses -> 1 database call ===")

	var group singleflight.Group
	var dbCalls atomic.Int64

	loadUser := func(ctx context.Context, id string) (string, error) {
		// Do() dedupes by key. Concurrent callers with the same key wait for
		// the in-flight one instead of starting their own.
		v, err, shared := group.Do(id, func() (any, error) {
			dbCalls.Add(1)
			time.Sleep(80 * time.Millisecond) // the expensive query
			return "user:" + id, nil
		})
		_ = shared // true when this caller piggy-backed on someone else's call
		if err != nil {
			return "", err
		}
		return v.(string), nil
	}

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = loadUser(context.Background(), "u1")
		}()
	}
	wg.Wait()

	fmt.Printf("  100 goroutines asked for u1 -> %d actual database call(s)\n", dbCalls.Load())
	fmt.Println("  use group.Forget(key) after a failure so the next caller retries")
	fmt.Println("  instead of being served a cached error")
}

// ===========================================================================
// 7. THE GOROUTINE LEAK YOU WILL SHIP AT LEAST ONCE.
//
// A goroutine blocked forever on a channel send is never garbage collected.
// It holds its stack and everything it references. Thousands of them = an
// OOM that looks like a memory leak but is really a lifecycle bug.
//
// Three rules that prevent basically all of them:
//   1. Every goroutine needs a defined way to EXIT (ctx.Done, closed channel).
//   2. Give result channels a buffer of 1 if the reader might walk away.
//   3. Whoever WRITES to a channel is the one who CLOSES it. Never the reader.
// ===========================================================================

func demo7GoroutineLeak() {
	fmt.Println("\n=== 7. Goroutine leak: the same code, broken and fixed ===")

	// BROKEN: unbuffered channel + a caller that gives up on timeout.
	// The worker finishes later, blocks forever on send, and leaks.
	leaky := func(ctx context.Context) {
		ch := make(chan string) // <-- no buffer: the bug
		go func() {
			time.Sleep(200 * time.Millisecond)
			ch <- "result" // nobody is listening any more -> blocked FOREVER
		}()
		select {
		case <-ch:
		case <-ctx.Done(): // caller gives up first
		}
	}

	// FIXED: buffer of 1. The worker's send always succeeds, the goroutine
	// exits, and the unread value is simply garbage collected.
	fixed := func(ctx context.Context) {
		ch := make(chan string, 1) // <-- the whole fix
		go func() {
			time.Sleep(200 * time.Millisecond)
			ch <- "result"
		}()
		select {
		case <-ch:
		case <-ctx.Done():
		}
	}

	before := goroutines()

	for i := 0; i < 50; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		leaky(ctx)
		cancel()
	}
	time.Sleep(300 * time.Millisecond)
	leaked := goroutines()

	for i := 0; i < 50; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		fixed(ctx)
		cancel()
	}
	time.Sleep(300 * time.Millisecond)
	after := goroutines()

	fmt.Printf("  baseline:             %d goroutine(s)\n", before)
	fmt.Printf("  after 50 leaky calls: %d  (+%d, and they NEVER go away)\n", leaked, leaked-before)
	fmt.Printf("  after 50 fixed calls: %d  (+%d -- the buffered version leaks nothing;\n", after, after-leaked)
	fmt.Println("                            the count stays high only because the")
	fmt.Println("                            earlier leaked goroutines are still stuck)")
	fmt.Println("  find these in production with: go tool pprof <url>/debug/pprof/goroutine")
	fmt.Println("  or in tests with go.uber.org/goleak")
}

// ---------------------------------------------------------------------------

func fetch(ctx context.Context, service string, d time.Duration, result string) (string, error) {
	select {
	case <-time.After(d):
		return result, nil
	case <-ctx.Done():
		return "", fmt.Errorf("%s: %w", service, ctx.Err())
	}
}

func goroutines() int { return runtime.NumGoroutine() }

func notes() {
	fmt.Print(`
--- PICKING THE RIGHT TOOL -------------------------------------------------

  "run these 5 calls in parallel, fail fast"        errgroup.WithContext
  "...but never more than 10 at a time"             g.SetLimit(10)
  "jobs cost different amounts of a resource"       semaphore.NewWeighted
  "the vendor API allows 100 req/s"                 rate.NewLimiter + Wait
  "reject callers who exceed their quota"           rate.Limiter + Allow -> 429
  "500 requests want the same expired cache key"    singleflight
  "a queue of work with N durable workers"          worker pool (lesson 48)
  "one-time init, ever"                             sync.Once (lesson 51)
  "read-mostly shared map"                          sync.RWMutex (lesson 50)
                                                    or sync.Map for hot keys

--- THINGS THAT WILL BITE YOU ----------------------------------------------

1. errgroup.Wait returns only the FIRST error. Collect them all yourself
   (a mutex + errors.Join) when every failure matters.
2. SetLimit must be called BEFORE the first Go(). It panics otherwise.
3. A cancelled context only helps if the callee actually selects on it.
   Cancellation in Go is cooperative -- there is no goroutine.Kill().
4. Rate limiting in one process does not limit a fleet. Ten replicas with
   a 100/s limiter each is a 1000/s limiter. Distributed = Redis.
5. singleflight shares errors as well as successes. Call Forget(key) on
   failure, or one blip serves the same error to everyone for its duration.
6. Never take a mutex and then make a network call while holding it. That is
   how a slow dependency becomes a fully serialised service.
`)
}
