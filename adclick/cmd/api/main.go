// Command api serves click ingestion and the analytics query API.
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

	"adclick/internal/adcache"
	"adclick/internal/config"
	"adclick/internal/event"
	"adclick/internal/httpapi"
	"adclick/internal/kafka"
	"adclick/internal/logging"
	"adclick/internal/metrics"
	"adclick/internal/store"
	"adclick/migrations"
)

// kafkaPublisher binds the producer to the events topic.
type kafkaPublisher struct {
	p     *kafka.Producer
	topic string
}

func (k kafkaPublisher) Publish(ctx context.Context, c event.Click) error {
	return k.p.Publish(ctx, k.topic, c)
}

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
	log := logging.New(cfg.LogLevel, "api")
	m := metrics.New()

	// SIGINT/SIGTERM cancel ctx; everything below shuts down from it.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := connectDB(ctx, cfg, m, log)
	if err != nil {
		return err
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
	go ads.Refresh(ctx, st, cfg.AdCacheRefresh, log)

	srv := httpapi.New(httpapi.Deps{
		Pub: kafkaPublisher{prod, cfg.TopicEvents}, Queries: st, Ads: ads, Metrics: m, Log: log,
		Salt:   cfg.IPHashSalt,
		Limits: event.Limits{MaxAge: cfg.MaxEventAge, FutureSkew: cfg.MaxFutureSkew},
		// Ready depends on Kafka only: with Postgres down we can still ingest.
		Ready: prod.Ping,
	})
	hs := &http.Server{
		Addr: cfg.HTTPAddr, Handler: srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second,
		// No WriteTimeout: it would kill the SSE stream.
	}

	errc := make(chan error, 1)
	go func() {
		log.Info("api listening", "addr", cfg.HTTPAddr)
		errc <- hs.ListenAndServe()
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down: draining requests")
	sctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := hs.Shutdown(sctx); err != nil {
		log.Warn("http shutdown", "err", err)
	}
	prod.Close(sctx) // flush buffered records before exit
	log.Info("bye")
	return nil
}

func connectDB(ctx context.Context, cfg config.Config, m *metrics.Metrics, log *slog.Logger) (*store.Store, error) {
	for {
		st, err := store.New(ctx, cfg.DatabaseURL, m.ObserveDB)
		if err == nil {
			return st, nil
		}
		log.Warn("waiting for postgres", "err", err)
		select {
		case <-time.After(2 * time.Second):
		case <-ctx.Done():
			return nil, errors.Join(err, ctx.Err())
		}
	}
}
