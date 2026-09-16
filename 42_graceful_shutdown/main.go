package main

// ============================================================================
// CONCEPT: graceful shutdown — signals, srv.Shutdown, draining in-flight
// work, and shutting down dependencies in the right order.
//
// WHY THIS MATTERS
// Every deploy, autoscale event and pod eviction kills your process. The
// platform sends SIGTERM, waits ~30 seconds, then sends SIGKILL. What your
// service does in those 30 seconds is the difference between a clean deploy
// and a handful of users seeing a connection reset, a half-written database
// row, or a background job that vanished.
//
// WHAT "GRACEFUL" MEANS, PRECISELY
//   1. Stop accepting NEW connections.
//   2. Let IN-FLIGHT requests finish (up to a deadline).
//   3. Stop background workers and wait for them.
//   4. Close dependencies (DB pool, queue, cache) — LAST, because the
//      requests in step 2 still need them.
//   5. Exit 0. If the deadline passes, force it and exit non-zero.
//
// JS/TS comparison: `process.on('SIGTERM', () => server.close(cb))`. Go's
// signal.NotifyContext turns a signal into a context (lesson 25), so the
// same cancellation you already thread through handlers also drives
// shutdown. That's the nice part.
//
// RUN IT
//   go run ./42_graceful_shutdown            # auto-shuts down after ~2.5s
//   go run ./42_graceful_shutdown -wait      # runs until you press Ctrl+C
// ============================================================================

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	addr            = "127.0.0.1:8098"
	shutdownTimeout = 10 * time.Second
)

func main() {
	waitForSignal := flag.Bool("wait", false, "run until Ctrl+C instead of auto-stopping")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if err := run(log, *waitForSignal); err != nil {
		log.Error("shutdown failed", "err", err)
		os.Exit(1) // non-zero tells the orchestrator the shutdown was dirty
	}
	log.Info("exited cleanly")
}

func run(log *slog.Logger, waitForSignal bool) error {
	// ---------- 1. turn signals into a context ----------
	// signal.NotifyContext (Go 1.16+) cancels ctx when one of these signals
	// arrives. This replaced the old `make(chan os.Signal, 1)` dance and is
	// strictly better, because ctx composes with everything else.
	//
	//   SIGINT  — Ctrl+C
	//   SIGTERM — what Docker/Kubernetes/systemd send. THE important one.
	//   SIGKILL — cannot be caught. Nothing you write runs. Don't try.
	//
	// The second signal is NOT caught (stop() is deferred), so a user
	// hammering Ctrl+C twice can always force-quit. Keep that property.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// For the demo: cancel after a short delay so `go run` terminates on
	// its own. Real services just wait for the signal.
	if !waitForSignal {
		go func() {
			time.Sleep(2500 * time.Millisecond)
			log.Info("(demo) simulating SIGTERM")
			stop() // release the signal handler...
			// ...and cancel by calling the same path a signal would take.
			demoShutdown()
		}()
	}

	// ---------- 2. fake dependencies ----------
	db := newFakeDB(log)

	// ---------- 3. background workers ----------
	// A WaitGroup (lesson 22) tracks them so shutdown can WAIT for them
	// rather than yanking the rug out.
	var workers sync.WaitGroup
	workerCtx, stopWorkers := context.WithCancel(context.Background())
	defer stopWorkers()

	for i := 1; i <= 2; i++ {
		workers.Add(1)
		go worker(workerCtx, &workers, log, i)
	}

	// ---------- 4. the server ----------
	mux := http.NewServeMux()
	mux.HandleFunc("GET /fast", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "done")
	})
	mux.HandleFunc("GET /slow", func(w http.ResponseWriter, r *http.Request) {
		// A request that is still running when shutdown starts. Shutdown
		// waits for this — that's the whole point.
		select {
		case <-time.After(1500 * time.Millisecond):
			fmt.Fprintln(w, "slow work finished")
		case <-r.Context().Done():
			// NOTE: srv.Shutdown does NOT cancel request contexts. This
			// fires only if the CLIENT disconnects. If you also want
			// in-flight requests to be told to hurry up, use BaseContext
			// (below) to give every request a context you control.
			return
		}
	})
	// readiness vs liveness: flip ready to false the instant shutdown
	// starts, so the load balancer stops routing new traffic BEFORE you
	// stop accepting connections. This is what removes the last few
	// connection resets from a deploy.
	var ready atomic.Bool
	ready.Store(true)
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !ready.Load() {
			http.Error(w, "shutting down", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintln(w, "ready")
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		// BaseContext gives every incoming request a context derived from
		// one you own — so cancelling it propagates into every handler.
		BaseContext: func(_ net.Listener) context.Context { return workerCtx },
	}

	// ---------- 5. start listening ----------
	// ListenAndServe BLOCKS, so it goes in a goroutine and reports its
	// error back over a channel (lesson 23). A buffered channel of 1 means
	// the goroutine can send and exit even if nobody is reading yet.
	serverErr := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", addr)
		// Shutdown makes ListenAndServe return http.ErrServerClosed. That
		// is the SUCCESS case, not a failure — always filter it out.
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()

	// Drive some traffic so there's something in flight when we stop.
	go generateTraffic(log)

	// ---------- 6. wait for either a signal or a server failure ----------
	select {
	case err := <-serverErr:
		if err != nil {
			return fmt.Errorf("server failed: %w", err) // e.g. port in use
		}
	case <-ctx.Done():
		log.Info("shutdown signal received")
	case <-demoSignal:
		log.Info("shutdown signal received (demo)")
	}

	// ---------- 7. the shutdown sequence ----------
	// Everything below is ordered deliberately.

	// 7a. Fail readiness FIRST. Give the load balancer a moment to notice
	// before you stop accepting connections. In k8s this is what
	// terminationGracePeriodSeconds and the preStop hook are for.
	ready.Store(false)
	log.Info("readiness set to false; draining")

	// 7b. Bound the whole shutdown. If we exceed this, we give up and let
	// the platform SIGKILL us — better than hanging forever.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	// 7c. Stop accepting new connections and wait for in-flight handlers.
	// Shutdown closes listeners immediately, then waits for active
	// requests to return. It does NOT wait for hijacked connections
	// (WebSockets) — you must track and close those yourself.
	start := time.Now()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		// Deadline exceeded: force-close every remaining connection.
		log.Error("graceful shutdown timed out; forcing", "err", err)
		_ = srv.Close()
		return fmt.Errorf("server shutdown: %w", err)
	}
	log.Info("http server drained", "took", time.Since(start).Round(time.Millisecond))

	// 7d. NOW stop the background workers and wait for them.
	stopWorkers()
	workersDone := make(chan struct{})
	go func() { workers.Wait(); close(workersDone) }()
	select {
	case <-workersDone:
		log.Info("workers stopped")
	case <-shutdownCtx.Done():
		return errors.New("workers did not stop before the deadline")
	}

	// 7e. Dependencies LAST — requests and workers needed them until now.
	// Close in reverse order of creation.
	if err := db.Close(); err != nil {
		return fmt.Errorf("close db: %w", err)
	}

	return nil
}

// ---------------------------------------------------------------------------
// supporting bits
// ---------------------------------------------------------------------------

// worker is a long-running background task. The shape — a for/select on
// ctx.Done() plus a ticker — is the standard Go worker loop.
func worker(ctx context.Context, wg *sync.WaitGroup, log *slog.Logger, id int) {
	defer wg.Done() // ALWAYS the first line, so it runs on every exit path

	ticker := time.NewTicker(700 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Finish the current unit of work, then leave. Do NOT start
			// anything new.
			log.Info("worker draining", "id", id)
			time.Sleep(200 * time.Millisecond) // simulate finishing up
			log.Info("worker stopped", "id", id)
			return
		case <-ticker.C:
			log.Debug("worker tick", "id", id)
		}
	}
}

type fakeDB struct{ log *slog.Logger }

func newFakeDB(log *slog.Logger) *fakeDB {
	log.Info("db pool opened")
	return &fakeDB{log: log}
}

func (d *fakeDB) Close() error {
	d.log.Info("db pool closed")
	return nil
}

func generateTraffic(log *slog.Logger) {
	time.Sleep(200 * time.Millisecond)
	client := &http.Client{Timeout: 5 * time.Second}
	for _, path := range []string{"/fast", "/readyz", "/slow"} {
		go func(p string) {
			resp, err := client.Get("http://" + addr + p)
			if err != nil {
				log.Warn("request failed", "path", p, "err", err)
				return
			}
			defer resp.Body.Close()
			log.Info("request completed", "path", p, "status", resp.StatusCode)
		}(path)
		time.Sleep(600 * time.Millisecond)
	}
}

// demoSignal lets the demo trigger the same shutdown path a real signal
// would. A real service does not need any of this.
var demoSignal = make(chan struct{})

func demoShutdown() { close(demoSignal) }

// ----------------------------------------------------------------------------
// KUBERNETES / DOCKER NOTES
//   * Docker `stop` and k8s both send SIGTERM, wait, then SIGKILL
//     (k8s: terminationGracePeriodSeconds, default 30s). Your
//     shutdownTimeout must be COMFORTABLY under that.
//   * In a Dockerfile use the exec form — `CMD ["./app"]`, not
//     `CMD ./app` — or your process runs under /bin/sh as PID 1 and never
//     receives the signal at all. This is the single most common reason
//     graceful shutdown "doesn't work". (Lesson 46.)
//   * Add a preStop hook that sleeps a few seconds: it gives kube-proxy
//     time to remove your pod from the Service endpoints before SIGTERM.
//   * /healthz (liveness) should stay 200 during draining — you're alive,
//     just not accepting work. Only /readyz flips to 503. Failing liveness
//     gets you SIGKILLed mid-drain.
//
// RULES
//   1. signal.NotifyContext for SIGINT + SIGTERM. Never try to catch
//      SIGKILL.
//   2. ListenAndServe in a goroutine; select on ctx.Done() and a server
//      error channel.
//   3. http.ErrServerClosed is success.
//   4. Always give Shutdown a context with a timeout, and srv.Close() if it
//      expires.
//   5. Shutdown order: readiness -> server -> workers -> dependencies.
//   6. srv.Shutdown does NOT cancel in-flight request contexts. Use
//      BaseContext if you want that.
//   7. Let a second Ctrl+C kill the process immediately.
//   8. Exit non-zero if shutdown was dirty, so your deploy tooling notices.
// ----------------------------------------------------------------------------
