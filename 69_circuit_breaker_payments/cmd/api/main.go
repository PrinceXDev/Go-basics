// api starts three things in one process:
//
//  1. a FAKE external payment gateway on GATEWAY_ADDR (it would be Stripe etc.)
//  2. our payment API on HTTP_ADDR
//  3. the async retry worker that drains the Postgres retry queue
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"cbpay/internal/breaker"
	"cbpay/internal/gateway"
	"cbpay/internal/httpapi"
	"cbpay/internal/payment"
	"cbpay/internal/retry"
	"cbpay/internal/store"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	dbURL := env("DATABASE_URL", "postgres://cbpay:cbpay@localhost:5433/cbpay?sslmode=disable")
	httpAddr := env("HTTP_ADDR", ":8080")
	gwAddr := env("GATEWAY_ADDR", ":8090")
	gwURL := env("GATEWAY_URL", "http://localhost"+gwAddr)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, dbURL)
	if err != nil {
		slog.Error("database", "err", err, "hint", "start one with: go run ./cmd/devpg")
		os.Exit(1)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		slog.Error("migrate", "err", err)
		os.Exit(1)
	}

	events := payment.NewEventLog(300)

	// Numbers are small so you can see every transition in a demo; the
	// comments give typical production values.
	cbCfg := breaker.DefaultConfig("paymentGateway")
	cbCfg.SlidingWindowSize = 10    // prod: 50-100
	cbCfg.MinimumNumberOfCalls = 5  // prod: 20+
	cbCfg.FailureRateThreshold = 50 // %
	cbCfg.SlowCallDurationThreshold = 800 * time.Millisecond
	cbCfg.SlowCallRateThreshold = 80                 // %
	cbCfg.WaitDurationInOpenState = 10 * time.Second // prod: 30-60s
	cbCfg.PermittedCallsInHalfOpen = 3
	cbCfg.IsFailure = payment.IsBreakerFailure
	cbCfg.OnStateChange = func(name string, from, to breaker.State) {
		msg := map[breaker.State]string{
			breaker.Open:     "gateway looks down — failing fast, payments go to the retry queue",
			breaker.HalfOpen: "wait elapsed — letting a few trial calls through",
			breaker.Closed:   "trial calls succeeded — normal traffic resumes",
		}[to]
		events.Add("breaker", "", "%s: %s → %s — %s", name, from, to, msg)
	}
	cb := breaker.New(cbCfg)

	svc := &payment.Service{
		Store:   st,
		Gateway: gateway.NewClient(gwURL, time.Second),
		Breaker: cb,
		Retry: retry.Policy{
			// Keep the synchronous budget small: the user is waiting.
			MaxAttempts:    3,
			InitialBackoff: 200 * time.Millisecond,
			MaxBackoff:     time.Second,
			Multiplier:     2,
			Jitter:         true,
			ShouldRetry:    payment.IsRetryable,
		},
		Events:           events,
		MaxAsyncAttempts: 6,
	}

	worker := &payment.Worker{
		Svc:          svc,
		PollInterval: time.Second,
		BatchSize:    10,
		Lease:        30 * time.Second,
		// Async retries can wait much longer than the user can: 2s, 4s, 8s…
		// capped at 30s (prod: minutes to hours).
		Backoff: retry.Policy{InitialBackoff: 2 * time.Second, MaxBackoff: 30 * time.Second, Multiplier: 2, Jitter: true},
	}

	gwSrv := &http.Server{Addr: gwAddr, Handler: gateway.NewServer().Handler(), ReadHeaderTimeout: 5 * time.Second}
	apiSrv := &http.Server{Addr: httpAddr, Handler: (&httpapi.Server{Svc: svc}).Handler(), ReadHeaderTimeout: 5 * time.Second}

	for _, s := range []*http.Server{gwSrv, apiSrv} {
		go func() {
			if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				slog.Error("listen", "addr", s.Addr, "err", err)
				stop()
			}
		}()
	}
	go worker.Run(ctx)

	slog.Info("ready", "api", httpAddr, "fake_gateway", gwAddr)
	events.Add("demo", "", "service started — breaker CLOSED, gateway healthy")
	<-ctx.Done()

	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = apiSrv.Shutdown(shutCtx)
	_ = gwSrv.Shutdown(shutCtx)
	slog.Info("stopped")
}
