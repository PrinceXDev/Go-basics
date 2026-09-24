package main

// ============================================================================
// CONCEPT: Worker Pool pattern — a fixed number of goroutines ("workers")
// pull jobs from a shared queue and process them concurrently, instead of
// spawning one goroutine per job.
//
// WHY THIS MATTERS (and why interviewers love asking this)
// If you have 1000 jobs and just did `for _, job := range jobs { go process(job) }`,
// you'd spawn 1000 goroutines at once. That's often fine in Go (goroutines are
// cheap), but if each job hits a database, an API with a rate limit, or uses a
// lot of memory, you can overwhelm the downstream system or your own process.
// A worker pool caps concurrency at a known number (here: 10) no matter how
// many jobs you have.
//
// JS/TS comparison:
//   - This is the same idea as a "concurrency limit" library like `p-limit`
//     or building a queue on top of `Promise.all` with a fixed batch size.
//     In JS you'd write something like:
//
//       async function runPool(jobs, limit) {
//         const results = [];
//         let i = 0;
//         async function worker() {
//           while (i < jobs.length) {
//             const idx = i++;
//             results[idx] = await process(jobs[idx]);
//           }
//         }
//         await Promise.all(Array.from({ length: limit }, worker));
//         return results;
//       }
//
//   - The big difference: JS's event loop is single-threaded, so "concurrency"
//     there means "in-flight async operations", not parallel CPU execution.
//     Go's goroutines are scheduled onto real OS threads (GOMAXPROCS of them),
//     so a Go worker pool gives you actual parallelism, not just interleaving.
// ============================================================================

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// STEP 1: Define the shapes of a "job" and a "result".
//
// Using structs (not just raw ints) is what makes this pattern extend to
// real work: a job could carry a URL to fetch, a row ID to process, etc.
// A result always carries BOTH a value and a possible error — like Go's
// `(value, err)` return convention, just carried across a channel.
// ---------------------------------------------------------------------------

type Job struct {
	ID int
}

type Result struct {
	JobID int
	Value int   // pretend "output" of the job
	Err   error // non-nil if the job failed
}

// simulateWork pretends to do something that takes variable time and can
// fail (like a network call). It respects ctx so it can be interrupted
// mid-flight instead of blindly sleeping.
func simulateWork(ctx context.Context, job Job) (int, error) {
	// Random 10-40ms of "work", occasionally an error, to make the demo
	// realistic — real jobs vary in duration and sometimes fail.
	workDuration := time.Duration(10+rand.Intn(30)) * time.Millisecond

	select {
	case <-time.After(workDuration):
		if job.ID%17 == 0 { // pretend job #17, #34, ... fail
			return 0, fmt.Errorf("job %d: simulated failure", job.ID)
		}
		return job.ID * 2, nil
	case <-ctx.Done():
		// The caller cancelled us (timeout, or user hit Ctrl+C, etc).
		// Returning ctx.Err() lets the caller see WHY we stopped.
		return 0, ctx.Err()
	}
}

// ---------------------------------------------------------------------------
// STEP 2: The worker function.
//
// Each worker is a goroutine that:
//   1. Reads jobs off the `jobs` channel until it's closed AND drained.
//   2. Sends exactly one Result per Job it processes onto `results`.
//   3. Stops immediately if ctx is cancelled — even mid-queue.
//   4. Calls wg.Done() exactly once when it returns, no matter which exit
//      path it takes. `defer` guarantees this even on early return.
//
// JS/TS comparison: this is like an `async function worker()` in the pool
// snippet above — a loop that keeps pulling work until there's none left.
// The difference is Go makes the "no more work" signal explicit via a
// CLOSED CHANNEL, rather than an index bound check.
// ---------------------------------------------------------------------------

func worker(ctx context.Context, id int, jobs <-chan Job, results chan<- Result, wg *sync.WaitGroup) {
	defer wg.Done() // ALWAYS runs when this function returns, any path.

	for {
		select {
		case job, ok := <-jobs:
			if !ok {
				// Channel closed AND empty — no more jobs will ever arrive.
				// This is the normal, successful exit path for a worker.
				return
			}
			value, err := simulateWork(ctx, job)
			result := Result{JobID: job.ID, Value: value, Err: err}

			// We must also respect cancellation while SENDING the result —
			// if nobody will ever read `results` again (e.g. main gave up),
			// a plain `results <- result` could block forever. Racing it
			// against ctx.Done() avoids that goroutine leak.
			select {
			case results <- result:
			case <-ctx.Done():
				return
			}

		case <-ctx.Done():
			// Cancelled while waiting for a job. Stop pulling new work.
			fmt.Printf("worker %d: stopping early (%v)\n", id, ctx.Err())
			return
		}
	}
}

// ---------------------------------------------------------------------------
// STEP 3: Orchestration — wires everything together.
//
// This is the part that's easy to get subtly wrong in an interview, so walk
// through it slowly:
//
//   - `jobs` is a BUFFERED channel sized to len(jobs) so the producer (this
//     function) can push every job and close the channel without needing a
//     separate goroutine just to feed it.
//   - `results` is buffered the same way so workers never block trying to
//     hand back a result, even if main is momentarily busy elsewhere.
//   - We launch exactly 10 workers, each added to the WaitGroup BEFORE it
//     starts (classic bug: calling wg.Add after `go worker(...)` races).
//   - A separate goroutine waits for all workers to finish, THEN closes
//     `results`. This is the standard "close on the producer side, after all
//     producers are done" rule — closing `results` is what lets the final
//     `for result := range results` loop in main() terminate instead of
//     blocking forever.
// ---------------------------------------------------------------------------

const numWorkers = 10

func RunJobs(ctx context.Context, jobs []Job) ([]Result, error) {
	jobsCh := make(chan Job, len(jobs))
	resultsCh := make(chan Result, len(jobs))

	for _, j := range jobs {
		jobsCh <- j
	}
	close(jobsCh) // Signals workers: "no more jobs will ever be sent."

	var wg sync.WaitGroup
	wg.Add(numWorkers)
	for w := 1; w <= numWorkers; w++ {
		go worker(ctx, w, jobsCh, resultsCh, &wg)
	}

	// This goroutine's ONLY job is to close resultsCh once every worker has
	// returned. Without it, main's `range resultsCh` below would block
	// forever after the last result, because the channel is never closed.
	go func() {
		wg.Wait()
		close(resultsCh)
	}()

	results := make([]Result, 0, len(jobs))
	var firstErr error

	for result := range resultsCh {
		results = append(results, result)
		if result.Err != nil && firstErr == nil {
			firstErr = result.Err
		}
	}

	// If the context was cancelled, surface that as the overall error even
	// if some jobs slipped through — the caller needs to know the run was
	// incomplete.
	if ctx.Err() != nil {
		return results, fmt.Errorf("run cancelled: %w", ctx.Err())
	}
	return results, firstErr
}

func main() {
	rand.Seed(time.Now().UnixNano())

	// Build 1000 jobs, IDs 1..1000.
	jobs := make([]Job, 1000)
	for i := range jobs {
		jobs[i] = Job{ID: i + 1}
	}

	// A generous timeout: 1000 jobs / 10 workers ≈ 100 jobs per worker,
	// each taking up to 40ms worst case -> ~4s worst case. 8s gives headroom.
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel() // ALWAYS defer cancel — releases ctx's internal timer
	// resources even if RunJobs returns before the timeout fires.

	start := time.Now()
	results, err := RunJobs(ctx, jobs)
	elapsed := time.Since(start)

	failed := 0
	for _, r := range results {
		if r.Err != nil {
			failed++
		}
	}

	fmt.Printf("\nProcessed %d/%d jobs in %v (%d failed)\n",
		len(results), len(jobs), elapsed, failed)

	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			fmt.Println("Run did not finish in time:", err)
		} else {
			fmt.Println("Run finished with at least one job error:", err)
		}
	} else {
		fmt.Println("All jobs completed with no errors.")
	}
}
