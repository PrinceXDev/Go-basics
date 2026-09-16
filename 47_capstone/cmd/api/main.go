// Command api is the capstone service's entry point.
//
// main() does exactly four things: load config, build dependencies, start
// the server, shut it down cleanly. All the actual logic lives in
// internal/ packages, which is what makes it testable.
//
//	go run ./47_capstone/cmd/api
//	go run ./47_capstone/cmd/api -addr 127.0.0.1:9000 -log-level debug
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"example.com/go-basics/47_capstone/internal/api"
	"example.com/go-basics/47_capstone/internal/config"
	"example.com/go-basics/47_capstone/internal/store"
)

// Injected at build time with -ldflags (lesson 46).
var (
	version   = "dev"
	commit    = "none"
	buildDate = "unknown"
)

func main() {
	// main is deliberately tiny. Everything real happens in run(), which
	// returns an error — so every `defer` inside it actually executes.
	// os.Exit (which log.Fatal calls) skips defers.
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	// ---------- 1. config (lesson 41) ----------
	cfg, err := config.Load(args)
	if err != nil {
		return fmt.Errorf("config:\n%w", err)
	}

	// ---------- 2. logging (lesson 40) ----------
	level, _ := cfg.SlogLevel() // already validated by config.Load
	var handler slog.Handler
	if cfg.Env == "dev" {
		handler = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	} else {
		handler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	}
	log := slog.New(handler).With("service", "tasks-api", "version", version)
	slog.SetDefault(log) // so stray log.Printf calls in libraries are structured too

	log.Info("starting",
		"env", cfg.Env, "addr", cfg.HTTP.Addr, "db", cfg.DB.Path,
		"commit", commit, "build_date", buildDate,
		"go", runtime.Version(), "gomaxprocs", runtime.GOMAXPROCS(0),
		"api_key", cfg.Auth.APIKey, // prints [REDACTED] — the Secret type
	)

	// ---------- 3. signals -> context (lesson 42) ----------
	// Set this up BEFORE the slow work below, so Ctrl+C during startup is
	// still honoured.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// ---------- 4. database (lessons 36, 37) ----------
	db, err := store.Open(ctx, cfg.DB.Path, cfg.DB.MaxOpenConns)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			log.Error("closing db", "err", cerr)
		}
		log.Info("database closed")
	}()

	applied, err := store.Migrate(ctx, db)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	log.Info("migrations complete", "applied", applied)

	// ---------- 5. wire it up ----------
	// Dependencies flow downward and are constructed once, here. No
	// globals, no init() magic, no service locator.
	repo := store.NewSQLRepo(db)
	srv := api.NewServer(api.Options{
		Repo:           repo,
		Logger:         log,
		APIKey:         cfg.Auth.APIKey.Reveal(),
		RequestTimeout: cfg.HTTP.RequestTimeout,
	})

	httpSrv := &http.Server{
		Addr:              cfg.HTTP.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       cfg.HTTP.ReadTimeout,
		WriteTimeout:      cfg.HTTP.WriteTimeout,
		IdleTimeout:       cfg.HTTP.IdleTimeout,
		ErrorLog:          slog.NewLogLogger(handler, slog.LevelError), // net/http's own errors, structured
	}

	// ---------- 6. serve ----------
	serverErr := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.HTTP.Addr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()

	select {
	case err := <-serverErr:
		if err != nil {
			return fmt.Errorf("server: %w", err) // e.g. address already in use
		}
		return nil
	case <-ctx.Done():
		log.Info("shutdown signal received")
	}

	// ---------- 7. graceful shutdown (lesson 42) ----------
	// Stop listening for signals so a second Ctrl+C force-quits.
	stop()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.HTTP.ShutdownTimeout)
	defer cancel()

	start := time.Now()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Error("graceful shutdown timed out; forcing", "err", err)
		_ = httpSrv.Close()
		return fmt.Errorf("shutdown: %w", err)
	}
	log.Info("drained", "took_ms", time.Since(start).Milliseconds())

	// The deferred db.Close() runs after this returns — dependencies last,
	// because in-flight requests needed them until the line above.
	log.Info("goodbye")
	return nil
}
