package main

// ============================================================================
// LESSON 46: SHIPPING A GO SERVICE IN A CONTAINER
//
// This is the app we're going to containerise — deliberately small, but with
// the three things a real deployable needs:
//
//   1. a /healthz endpoint, so the orchestrator can check on it
//   2. version info injected AT BUILD TIME with -ldflags (no code change
//      between environments)
//   3. graceful shutdown on SIGTERM (lesson 42), which is what makes a
//      rolling deploy clean
//
// Run it locally:      go run ./46_docker
// Build it locally:    see the Makefile targets in README.md
// Build the image:     docker build -f 46_docker/Dockerfile -t go-basics:dev .
//
// Go's story here is genuinely excellent: a statically linked binary with no
// runtime, no interpreter and no shared libraries means the production image
// is just the binary plus a CA bundle — typically 10-20 MB, or ~5 MB with
// `scratch`. Compare a Node image at 150 MB+.
// ============================================================================

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"syscall"
	"time"
)

// ---------------------------------------------------------------------------
// BUILD-TIME VARIABLES
// These are `var`, not `const`, and left empty on purpose: the linker
// overwrites them at build time via -ldflags="-X main.version=1.2.3".
//
// You can only inject STRINGS, and only into package-level vars. The path
// must be the full package path: -X 'main.version=...' works because this
// is package main; for another package it'd be
// -X 'example.com/app/internal/build.Version=...'.
// ---------------------------------------------------------------------------

var (
	version   = "dev"
	commit    = "none"
	buildDate = "unknown"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// ---------- 12-factor config (lesson 41) ----------
	// In a container, config comes from the environment. The port
	// especially: hardcoding it makes the image environment-specific,
	// which defeats the point of building it once.
	port := envOr("PORT", "8080")
	// Bind 0.0.0.0, NOT 127.0.0.1. A process listening on loopback inside a
	// container is unreachable from outside it — a classic first-time
	// Docker mistake that looks like a networking problem.
	addr := "0.0.0.0:" + port

	log.Info("starting",
		"version", version,
		"commit", commit,
		"build_date", buildDate,
		"go", runtime.Version(),
		"goos", runtime.GOOS,
		"goarch", runtime.GOARCH,
		// GOMAXPROCS defaults to the number of host CPUs, which in a
		// container with a CPU limit is WRONG and causes throttling. See
		// the README's note on automaxprocs.
		"gomaxprocs", runtime.GOMAXPROCS(0),
	)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{
			"message": "hello from a container",
			"version": version,
		})
	})

	// Liveness: "is the process alive?" Keep it trivial — no database
	// check. If this fails, the orchestrator RESTARTS you, and restarting
	// won't fix a downed database.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// Readiness: "should traffic be routed to me?" THIS is where you check
	// dependencies, and this is what you flip to false during shutdown.
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})

	// Exposing build info is invaluable during an incident: "which commit
	// is actually running in prod right now?"
	mux.HandleFunc("GET /version", func(w http.ResponseWriter, _ *http.Request) {
		info := map[string]any{
			"version": version, "commit": commit, "build_date": buildDate,
			"go": runtime.Version(),
		}
		// debug.ReadBuildInfo reads the module graph baked into every Go
		// binary — including VCS info the toolchain adds automatically,
		// so you get the commit for free even without -ldflags.
		if bi, ok := debug.ReadBuildInfo(); ok {
			info["module"] = bi.Main.Path
			for _, s := range bi.Settings {
				if s.Key == "vcs.revision" || s.Key == "vcs.time" || s.Key == "vcs.modified" {
					info[s.Key] = s.Value
				}
			}
		}
		writeJSON(w, http.StatusOK, info)
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// ---------- graceful shutdown (lesson 42) ----------
	// `docker stop` sends SIGTERM and waits 10s before SIGKILL. This is the
	// half of containerisation people forget, and it's why deploys drop
	// requests.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Info("listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("listen failed", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	log.Info("shutdown signal received; draining")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("graceful shutdown failed", "err", err)
		os.Exit(1)
	}
	log.Info("stopped cleanly")
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
