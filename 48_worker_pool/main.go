// Worker pool: 1000 jobs, exactly 10 workers running at a time.
//
// Pipeline shape:
//
//	producer --> jobs chan --> [10 workers] --> results chan --> main collects
//
// Ownership rule used here: whoever writes to a channel is the one who closes it.
package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"os/signal"
	"runtime"
	"time"
)

const (
	totalJobs  = 1000
	numWorkers = 10
)

type Job struct {
	ID      int
	Payload string
}

type Result struct {
	JobID    int
	WorkerID int
	Output   string
	Err      error
	Took     time.Duration
}

// process is the "real work". Anything slow (HTTP call, DB query, image resize)
// goes here. It must watch ctx so a cancel is noticed mid-flight.
func process(ctx context.Context, j Job) (string, error) {
	work := time.Duration(rand.Intn(20)+5) * time.Millisecond

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(work):
	}

	if j.ID%97 == 0 {
		return "", fmt.Errorf("job %d: simulated downstream failure", j.ID)
	}
	return fmt.Sprintf("processed %q", j.Payload), nil
}

// produce feeds jobs into the pipeline and owns (closes) the jobs channel.
// Running it in its own goroutine is what lets workers start immediately
// instead of waiting for all 1000 jobs to be queued.
func produce(ctx context.Context, jobs chan<- Job) {
	defer close(jobs)

	for i := 1; i <= totalJobs; i++ {
		job := Job{ID: i, Payload: fmt.Sprintf("task-%04d", i)}

		select {
		case jobs <- job:
		case <-ctx.Done():
			// Cancelled: stop feeding. The deferred close() still runs, so
			// workers see an empty+closed channel and exit cleanly.
			return
		}
	}
}

func worker(ctx context.Context, id int, jobs <-chan Job, results chan<- Result) {
	for j := range jobs {
		// Cheap early-out so a cancelled run does not keep picking up work.
		if ctx.Err() != nil {
			return
		}

		start := time.Now()
		out, err := process(ctx, j)
		res := Result{JobID: j.ID, WorkerID: id, Output: out, Err: err, Took: time.Since(start)}

		// Never a bare `results <- res`: if main stopped reading, that blocks
		// forever and the goroutine leaks.
		select {
		case results <- res:
		case <-ctx.Done():
			return
		}
	}
}

func main() {
	start := time.Now()
	before := runtime.NumGoroutine()

	// Cancelled by Ctrl+C, or automatically when main returns (defer stop).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	jobs := make(chan Job, numWorkers)
	results := make(chan Result, numWorkers)

	go produce(ctx, jobs)

	// Start exactly 10 workers. done counts them out instead of a WaitGroup,
	// so the closer below needs no extra goroutine.
	done := make(chan struct{})
	for i := 1; i <= numWorkers; i++ {
		go func(id int) {
			defer func() { done <- struct{}{} }()
			worker(ctx, id, jobs, results)
		}(i)
	}

	// results is written by all 10 workers, so no single worker may close it.
	// One goroutine waits for all of them, then closes it once.
	go func() {
		for i := 0; i < numWorkers; i++ {
			<-done
		}
		close(results)
	}()

	var (
		ok      int
		failed  int
		collect = make([]Result, 0, totalJobs)
	)

	// Ranging until results is closed is the whole termination condition.
	for r := range results {
		collect = append(collect, r)
		if r.Err != nil {
			failed++
			continue
		}
		ok++
	}

	fmt.Printf("collected : %d / %d\n", len(collect), totalJobs)
	fmt.Printf("succeeded : %d\n", ok)
	fmt.Printf("failed    : %d\n", failed)
	fmt.Printf("elapsed   : %s\n", time.Since(start).Round(time.Millisecond))

	if err := ctx.Err(); errors.Is(err, context.Canceled) {
		fmt.Println("status    : cancelled before finishing all jobs")
	} else {
		fmt.Println("status    : completed")
	}

	// Should be ~1 + 2 signal-package goroutines. Anything growing with
	// totalJobs or numWorkers would mean a leak.
	time.Sleep(50 * time.Millisecond)
	fmt.Printf("goroutines: %d at start, %d now\n", before, runtime.NumGoroutine())
}
