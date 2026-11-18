// Command consumer reads clicks from Kafka and maintains the aggregates.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"adclick/internal/adcache"
	"adclick/internal/config"
	"adclick/internal/consumer"
	"adclick/internal/event"
	"adclick/internal/kafka"
	"adclick/internal/logging"
	"adclick/internal/metrics"
	"adclick/internal/store"
	"adclick/migrations"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := logging.New(cfg.LogLevel, "consumer")
	m := metrics.New()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var st *store.Store
	for st == nil {
		st, err = store.New(ctx, cfg.DatabaseURL, m.ObserveDB)
		if err != nil {
			log.Warn("waiting for postgres", "err", err)
			select {
			case <-time.After(2 * time.Second):
			case <-ctx.Done():
				return errors.Join(err, ctx.Err())
			}
		}
	}
	defer st.Close()
	if err := st.Migrate(ctx, migrations.FS); err != nil {
		return err
	}

	prod, err := kafka.NewProducer(cfg.KafkaBrokers)
	if err != nil {
		return err
	}
	if err := kafka.EnsureTopics(ctx, prod.Client(), cfg); err != nil {
		log.Warn("could not ensure topics (continuing)", "err", err)
	}

	ads := &adcache.AdCache{}
	var bg sync.WaitGroup
	spawn := func(f func()) { bg.Add(1); go func() { defer bg.Done(); f() }() }

	spawn(func() { ads.Refresh(ctx, st, cfg.AdCacheRefresh, log) })
	spawn(func() { consumer.MonitorLag(ctx, cfg, m, log) })
	spawn(func() { purgeLoop(ctx, st, cfg.DedupRetention, log) })

	mux := http.NewServeMux()
	mux.Handle("GET /metrics", m.Handler())
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	ms := &http.Server{Addr: cfg.MetricsAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	spawn(func() {
		if err := ms.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("metrics server", "err", err)
		}
	})

	proc := consumer.NewProcessor(st, ads, consumer.KafkaParker{P: prod, Cfg: cfg}, m, log, consumer.Options{
		MaxRetries: cfg.MaxRetries, RetryDelay: cfg.RetryDelay,
		Limits: event.Limits{MaxAge: cfg.MaxEventAge, FutureSkew: cfg.MaxFutureSkew},
	})

	log.Info("consumer starting", "group", cfg.ConsumerGroup, "workers", cfg.ConsumerConcurrency, "batch", cfg.BatchSize)
	runErr := consumer.Run(ctx, cfg, proc, m, log)

	// Run returns after ctx is cancelled and workers left the group cleanly.
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = ms.Shutdown(sctx)
	prod.Close(sctx)
	bg.Wait()
	log.Info("bye")
	return runErr
}

func purgeLoop(ctx context.Context, st *store.Store, retention time.Duration, log *slog.Logger) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n, err := st.PurgeOldEvents(ctx, retention)
			log.Info("purged old dedup rows", "rows", n, "err", err)
		}
	}
}
