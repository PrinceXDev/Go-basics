package main

// ============================================================================
// CONCEPT: asynq -- Redis-backed background jobs for Go.
//
// This is the Go answer to BullMQ/Sidekiq/Celery, and it is the piece your CV
// claims. The mental model maps 1:1 onto BullMQ, so if you have shipped that,
// you already know this:
//
//	BullMQ                     asynq
//	------------------------   ----------------------------------------
//	new Queue().add(job)       client.Enqueue(task)
//	new Worker(fn)             mux.HandleFunc(type, fn) + srv.Run(mux)
//	concurrency: 10            Config{Concurrency: 10}
//	attempts + backoff         asynq.MaxRetry(n) + RetryDelayFunc
//	delay / repeat             asynq.ProcessIn / asynq.Scheduler (cron)
//	priority queues            Config{Queues: map[string]int}
//	failed queue               the "archive" (a dead-letter set)
//	job id (dedupe)            asynq.TaskID / asynq.Unique
//
// WHY A QUEUE AT ALL? Three reasons, and you should be able to say them:
//   1. LATENCY  -- return 202 in 5ms, do the 4-second PDF render later.
//   2. DURABILITY -- if the process dies mid-job, the job is still in Redis
//      and another worker picks it up. A bare `go doWork()` is gone forever.
//   3. BACKPRESSURE -- a queue absorbs a spike instead of melting your
//      database. Add workers to drain faster; the API stays up either way.
//
// THE RULE THAT MATTERS MOST: a handler must be IDEMPOTENT. At-least-once
// delivery is the guarantee; a job WILL occasionally run twice (a worker died
// after doing the work but before acknowledging). If running it twice is not
// safe, the queue has not made your system reliable -- it has made it
// unreliable in a new and interesting way.
//
// This lesson runs against miniredis (an in-process Redis) so it needs NO
// setup. Point it at a real Redis by setting REDIS_ADDR:
//
//	docker run -p 6379:6379 redis:7-alpine
//	REDIS_ADDR=127.0.0.1:6379 go run ./65_asynq
//
// Run: go run ./65_asynq
// ============================================================================

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"sync/atomic"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/hibiken/asynq"
)

// ---------------------------------------------------------------------------
// 1. TASK DEFINITIONS
//
// A task is a TYPE NAME plus a JSON payload. Keep the payload SMALL: ids, not
// objects. Two reasons -- Redis memory, and a payload captured at enqueue
// time is stale by the time it runs. Pass a user id, re-load the user.
// ---------------------------------------------------------------------------

const (
	TypeWelcomeEmail = "email:welcome"
	TypeThumbnail    = "image:thumbnail"
	TypeFlaky        = "demo:flaky"
	TypeReport       = "report:daily"
)

type WelcomeEmailPayload struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
}

type ThumbnailPayload struct {
	ImageID string `json:"image_id"`
	Width   int    `json:"width"`
}

func NewWelcomeEmailTask(userID, email string) (*asynq.Task, error) {
	b, err := json.Marshal(WelcomeEmailPayload{UserID: userID, Email: email})
	if err != nil {
		return nil, err
	}
	return asynq.NewTask(TypeWelcomeEmail, b), nil
}

// ---------------------------------------------------------------------------
// 2. HANDLERS -- the worker side.
//
// A handler is func(context.Context, *asynq.Task) error. Return nil and the
// job is done; return an error and asynq retries it with backoff until
// MaxRetry is exhausted, then archives it (the dead-letter queue).
// ---------------------------------------------------------------------------

var (
	emailsSent   atomic.Int64
	flakyRuns    atomic.Int64
	thumbnails   atomic.Int64
	archivedSeen atomic.Int64
)

func handleWelcomeEmail(ctx context.Context, t *asynq.Task) error {
	var p WelcomeEmailPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		// A malformed payload will NEVER succeed. Retrying it 25 times is
		// pure waste -- SkipRetry sends it straight to the archive.
		return fmt.Errorf("unmarshal: %v: %w", err, asynq.SkipRetry)
	}

	// ALWAYS honour ctx. asynq cancels it when the task times out or the
	// server is shutting down. A handler that ignores ctx blocks your deploy.
	select {
	case <-time.After(20 * time.Millisecond): // "send the email"
	case <-ctx.Done():
		return ctx.Err()
	}

	emailsSent.Add(1)
	log.Printf("   [worker] welcome email -> %s", p.Email)
	return nil
}

func handleThumbnail(ctx context.Context, t *asynq.Task) error {
	var p ThumbnailPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("%v: %w", err, asynq.SkipRetry)
	}
	time.Sleep(10 * time.Millisecond)
	thumbnails.Add(1)
	log.Printf("   [worker] thumbnail %s @ %dpx", p.ImageID, p.Width)
	return nil
}

// handleFlaky fails on its first attempt and succeeds on the retry -- so you
// can watch the retry machinery do its job end to end.
func handleFlaky(ctx context.Context, t *asynq.Task) error {
	n := flakyRuns.Add(1)

	// asynq.GetRetryCount tells the handler which attempt this is. Useful for
	// "log loudly on the last attempt" and for conditional fallbacks.
	retried, _ := asynq.GetRetryCount(ctx)
	maxRetry, _ := asynq.GetMaxRetry(ctx)

	if n < 2 {
		log.Printf("   [worker] flaky task FAILED (attempt %d/%d)", retried+1, maxRetry+1)
		return errors.New("upstream API returned 503")
	}
	log.Printf("   [worker] flaky task SUCCEEDED on attempt %d", retried+1)
	return nil
}

// handleReport always fails, to demonstrate the archive (dead-letter queue).
func handleReport(ctx context.Context, t *asynq.Task) error {
	return errors.New("report generator is broken")
}

// ---------------------------------------------------------------------------
// 3. MIDDLEWARE -- same idea as HTTP middleware (lesson 39) and gRPC
//    interceptors (lesson 62). Logging, metrics, panic recovery, tracing.
// ---------------------------------------------------------------------------

func loggingMiddleware(next asynq.Handler) asynq.Handler {
	return asynq.HandlerFunc(func(ctx context.Context, t *asynq.Task) error {
		start := time.Now()
		err := next.ProcessTask(ctx, t)

		id, _ := asynq.GetTaskID(ctx)
		queue, _ := asynq.GetQueueName(ctx)
		status := "ok"
		if err != nil {
			status = "error: " + err.Error()
		}
		log.Printf("   [mw] type=%s queue=%s id=%s took=%v %s",
			t.Type(), queue, short(id), time.Since(start).Round(time.Millisecond), status)
		return err
	})
}

func recoveryMiddleware(next asynq.Handler) asynq.Handler {
	return asynq.HandlerFunc(func(ctx context.Context, t *asynq.Task) (err error) {
		defer func() {
			if r := recover(); r != nil {
				// A panicking handler must not kill the worker process.
				// (asynq recovers too, but an explicit one lets you control
				// the error and whether it should retry.)
				err = fmt.Errorf("panic: %v: %w", r, asynq.SkipRetry)
			}
		}()
		return next.ProcessTask(ctx, t)
	})
}

// ===========================================================================

func main() {
	log.SetFlags(0)

	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		// miniredis: a real Redis protocol implementation, in this process.
		// Also the right way to test queue code in CI without a container.
		mr, err := miniredis.Run()
		if err != nil {
			log.Fatal(err)
		}
		defer mr.Close()
		addr = mr.Addr()
		fmt.Printf("using in-process miniredis at %s\n", addr)
	} else {
		fmt.Printf("using real Redis at %s\n", addr)
	}

	redisOpt := asynq.RedisClientOpt{Addr: addr}

	// -------------------------------------------------------------------
	// THE WORKER SERVER
	// -------------------------------------------------------------------
	srv := asynq.NewServer(redisOpt, asynq.Config{
		// Concurrency = how many tasks run SIMULTANEOUSLY in this process.
		// Each one is a goroutine. Size it by what the work blocks on:
		// CPU-bound -> ~NumCPU; IO-bound -> much higher (but never more
		// than your database connection pool can serve).
		Concurrency: 10,

		// WEIGHTED PRIORITY QUEUES. With these weights, the worker picks
		// from critical 6/10 of the time, default 3/10, low 1/10.
		// Non-strict (the default) means low-priority work still drains
		// instead of starving behind a busy critical queue.
		Queues: map[string]int{
			"critical": 6,
			"default":  3,
			"low":      1,
		},

		// Custom backoff. The default is ~2^n seconds with jitter, which is
		// right for most things. Shortened here so the demo finishes.
		RetryDelayFunc: func(n int, err error, t *asynq.Task) time.Duration {
			return time.Duration(100*(n+1)) * time.Millisecond
		},

		// Called when a task exhausts its retries OR fails. This is your
		// alerting hook -- a growing archive is a silent outage otherwise.
		ErrorHandler: asynq.ErrorHandlerFunc(func(ctx context.Context, t *asynq.Task, err error) {
			retried, _ := asynq.GetRetryCount(ctx)
			maxRetry, _ := asynq.GetMaxRetry(ctx)
			if retried >= maxRetry {
				archivedSeen.Add(1)
				log.Printf("   [ALERT] task %s exhausted retries and was ARCHIVED: %v", t.Type(), err)
			}
		}),

		// Kill a handler that overruns. Without this, one stuck job holds a
		// worker slot forever and your effective concurrency decays to zero.
		ShutdownTimeout: 5 * time.Second,
	})

	// ServeMux routes by task type, exactly like http.ServeMux routes by path.
	mux := asynq.NewServeMux()
	mux.Use(recoveryMiddleware, loggingMiddleware)
	mux.HandleFunc(TypeWelcomeEmail, handleWelcomeEmail)
	mux.HandleFunc(TypeThumbnail, handleThumbnail)
	mux.HandleFunc(TypeFlaky, handleFlaky)
	mux.HandleFunc(TypeReport, handleReport)

	if err := srv.Start(mux); err != nil {
		log.Fatal(err)
	}

	// -------------------------------------------------------------------
	// THE PRODUCER -- normally your HTTP handler, a different process.
	// -------------------------------------------------------------------
	client := asynq.NewClient(redisOpt)
	defer client.Close()

	fmt.Println("\n=== 1. Enqueue a task (what your HTTP handler does) ===")
	task, err := NewWelcomeEmailTask("u1", "prince@example.com")
	if err != nil {
		log.Fatal(err)
	}
	info, err := client.Enqueue(task, asynq.Queue("critical"), asynq.MaxRetry(3))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("  enqueued id=%s queue=%s state=%s\n", short(info.ID), info.Queue, info.State)
	fmt.Println("  the HTTP handler returns 202 Accepted RIGHT HERE -- it does")
	fmt.Println("  not wait for the email to be sent")

	fmt.Println("\n=== 2. Options: delay, timeout, deadline, retention ===")
	t2, _ := NewWelcomeEmailTask("u2", "later@example.com")
	info2, _ := client.Enqueue(t2,
		asynq.Queue("default"),
		asynq.ProcessIn(400*time.Millisecond), // run LATER (a "scheduled" task)
		asynq.MaxRetry(5),
		asynq.Timeout(10*time.Second), // per-attempt ctx deadline
		asynq.Retention(24*time.Hour), // keep the result for inspection
	)
	fmt.Printf("  scheduled id=%s state=%s (runs in 400ms)\n", short(info2.ID), info2.State)

	fmt.Println("\n=== 3. Deduplication: TaskID and Unique ===")
	// An explicit TaskID makes enqueueing idempotent: the second attempt
	// returns ErrTaskIDConflict instead of creating a duplicate. This is the
	// queue-side twin of the idempotency key from lesson 63.
	dedupe, _ := NewWelcomeEmailTask("u3", "once@example.com")
	if _, err := client.Enqueue(dedupe, asynq.TaskID("welcome:u3")); err != nil {
		fmt.Println("  first enqueue failed:", err)
	} else {
		fmt.Println("  first enqueue with TaskID welcome:u3 -> accepted")
	}
	dup, _ := NewWelcomeEmailTask("u3", "once@example.com")
	_, err = client.Enqueue(dup, asynq.TaskID("welcome:u3"))
	fmt.Printf("  second enqueue with the SAME id -> %v\n", err)
	fmt.Println("  (asynq.Unique(ttl) is the other flavour: dedupe by payload,")
	fmt.Println("   for a window, without you having to invent an id)")

	fmt.Println("\n=== 4. Priority queues under load ===")
	for i := 0; i < 5; i++ {
		b, _ := json.Marshal(ThumbnailPayload{ImageID: fmt.Sprintf("img%d", i), Width: 256})
		q := "low"
		if i%2 == 0 {
			q = "critical"
		}
		_, _ = client.Enqueue(asynq.NewTask(TypeThumbnail, b), asynq.Queue(q))
	}
	fmt.Println("  5 thumbnails split across critical/low -- critical drains first")

	fmt.Println("\n=== 5. Retries with backoff (fails once, then succeeds on the retry) ===")
	_, _ = client.Enqueue(asynq.NewTask(TypeFlaky, nil), asynq.MaxRetry(5))

	fmt.Println("\n=== 6. The dead-letter queue (asynq calls it the archive) ===")
	_, _ = client.Enqueue(asynq.NewTask(TypeReport, nil), asynq.MaxRetry(1))
	fmt.Println("  this one always fails -> 2 attempts -> archived + ErrorHandler fires")

	// Let the workers chew through everything.
	//
	// This wait is long on purpose. Retried and scheduled tasks do NOT sit in
	// a Go timer -- they are written to a Redis sorted set keyed by "run at",
	// and a background component (asynq's forwarder) sweeps that set every
	// few seconds and moves due tasks back to the pending queue. That design
	// is what makes a delayed task survive a worker restart, and it is why
	// "ProcessIn(400ms)" means "not before 400ms", never "at exactly 400ms".
	fmt.Println("\n  ... waiting ~9s for the forwarder to release scheduled/retried tasks ...")
	time.Sleep(9 * time.Second)

	// -------------------------------------------------------------------
	// 7. INSPECTION -- what the asynq CLI and Web UI use.
	// -------------------------------------------------------------------
	fmt.Println("\n=== 7. Inspecting queues at runtime ===")
	insp := asynq.NewInspector(redisOpt)
	defer insp.Close()

	queues, _ := insp.Queues()
	for _, q := range queues {
		info, err := insp.GetQueueInfo(q)
		if err != nil {
			continue
		}
		fmt.Printf("  %-9s size=%-3d active=%d pending=%d scheduled=%d retry=%d archived=%d completed=%d\n",
			info.Queue, info.Size, info.Active, info.Pending, info.Scheduled, info.Retry, info.Archived, info.Completed)
	}

	// Archived tasks are inspectable and REPLAYABLE -- fix the bug, then
	// insp.RunTask(queue, id) to re-run it. That is the whole point of a DLQ:
	// failures are parked, not lost.
	for _, q := range queues {
		tasks, err := insp.ListArchivedTasks(q, asynq.PageSize(10))
		if err != nil {
			continue
		}
		for _, t := range tasks {
			fmt.Printf("  ARCHIVED in %s: type=%s lastErr=%q -> insp.RunTask() to replay\n", q, t.Type, t.LastErr)
		}
	}

	fmt.Printf("\n  counters: emails=%d thumbnails=%d flakyAttempts=%d archived=%d\n",
		emailsSent.Load(), thumbnails.Load(), flakyRuns.Load(), archivedSeen.Load())

	// -------------------------------------------------------------------
	// 8. GRACEFUL SHUTDOWN -- stop accepting, let in-flight tasks finish.
	// -------------------------------------------------------------------
	fmt.Println("\n=== 8. Graceful shutdown ===")
	srv.Shutdown() // Stop() would only pause processing; Shutdown drains
	fmt.Println("  in-flight tasks were allowed to finish; unfinished ones stay")
	fmt.Println("  in Redis and are picked up by the next worker that starts")

	notes()
}

func short(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

func notes() {
	fmt.Print(`
--- PRODUCTION CHECKLIST ---------------------------------------------------

  * Handlers MUST be idempotent. Delivery is at-least-once: a worker that
    dies after doing the work but before acknowledging WILL re-run the task.
    Guard with a processed-ids table or a natural unique constraint.
  * Small payloads. Ids, not objects. The payload is stale the moment it is
    written; re-read the row inside the handler.
  * Set MaxRetry per task type, not globally. An email: 5. A payment
    capture: 0 or 1, with a human in the loop.
  * Use SkipRetry for permanent failures (bad payload, 4xx from an API).
    Retrying them 25 times is how you rate-limit yourself out of a vendor.
  * Alert on archive size and on queue latency (oldest pending task age),
    not just on queue length. A queue of 10 that has not moved in an hour is
    worse than a queue of 10,000 that is draining.
  * One Redis per environment, and remember asynq state IS Redis state:
    persistence off = jobs lost on restart. Turn on AOF.
  * Separate the worker binary from the API binary. They scale differently
    and you do not want a memory-hungry PDF render in your API's pod.

--- TOOLING ----------------------------------------------------------------

  go install github.com/hibiken/asynq/tools/asynq@latest
  asynq dash                       # a live TUI of every queue
  asynq queue ls
  asynq task ls --queue=default --state=archived
  asynq task archive|delete|run --queue=default --id=<id>

  Web UI:  github.com/hibiken/asynqmon (a container you point at Redis)

--- asynq vs the alternatives ----------------------------------------------

  asynq        Redis, simple, great tooling, Go-native. Default choice.
  River        Postgres-backed -- jobs commit in the SAME transaction as
               your data. No dual-write problem. Pick this if you are
               already on Postgres and correctness matters more than
               throughput.
  Kafka/NATS   Not a job queue: a LOG. Use for event streaming, fan-out to
               many consumers, replay. Different problem.
  machinery    Older, more features, more complexity.
  "go func()"  Fine for fire-and-forget work you can afford to lose on a
               deploy. That is a much smaller set of work than people think.

--- THE SCHEDULER (cron) ---------------------------------------------------

  asynq.NewScheduler + scheduler.Register("0 3 * * *", task) gives you
  periodic jobs. Only ONE scheduler instance should run per deployment, or
  every replica enqueues the same nightly job. (asynq's Periodic Task
  Manager solves this properly with a leader election.)
`)
}
