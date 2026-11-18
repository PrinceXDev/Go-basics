package consumer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"

	"adclick/internal/config"
	"adclick/internal/kafka"
	"adclick/internal/metrics"
)

// KafkaParker publishes retry/DLQ copies through the idempotent producer.
type KafkaParker struct {
	P   *kafka.Producer
	Cfg config.Config
}

func (k KafkaParker) Retry(ctx context.Context, m Msg, attempt int, notBefore time.Time) error {
	return k.P.PublishRaw(ctx, k.Cfg.TopicRetry, m.Key, m.Value, []kgo.RecordHeader{
		{Key: kafka.HeaderAttempt, Value: []byte(strconv.Itoa(attempt))},
		{Key: kafka.HeaderNotBefore, Value: []byte(strconv.FormatInt(notBefore.UnixMilli(), 10))},
	})
}

func (k KafkaParker) DLQ(ctx context.Context, m Msg, reason string) error {
	return k.P.PublishRaw(ctx, k.Cfg.TopicDLQ, m.Key, m.Value, []kgo.RecordHeader{
		{Key: kafka.HeaderError, Value: []byte(reason)},
		{Key: kafka.HeaderOrigin, Value: []byte(m.Topic)},
		{Key: kafka.HeaderAttempt, Value: []byte(strconv.Itoa(m.Attempt))},
	})
}

// Run starts cfg.ConsumerConcurrency clients in ONE consumer group and blocks
// until ctx is cancelled. Each client is an independent group member, so
// Kafka spreads partitions across them exactly like separate processes:
// this is the in-process "worker pool". Start more processes to scale out;
// partitions (12) cap the useful parallelism.
func Run(ctx context.Context, cfg config.Config, proc *Processor, m *metrics.Metrics, log *slog.Logger) error {
	var wg sync.WaitGroup
	errc := make(chan error, cfg.ConsumerConcurrency)
	for i := 0; i < cfg.ConsumerConcurrency; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			if err := runOne(ctx, cfg, proc, m, log.With("worker", id)); err != nil {
				errc <- err
			}
		}(i)
	}
	wg.Wait()
	close(errc)
	return errors.Join(drain(errc)...)
}

func drain(c chan error) (out []error) {
	for e := range c {
		out = append(out, e)
	}
	return
}

func runOne(ctx context.Context, cfg config.Config, proc *Processor, m *metrics.Metrics, log *slog.Logger) error {
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(cfg.KafkaBrokers...),
		kgo.ConsumerGroup(cfg.ConsumerGroup),
		kgo.ConsumeTopics(cfg.TopicEvents, cfg.TopicRetry),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()), // new group: read from the beginning
		kgo.DisableAutoCommit(),                           // WE decide when a batch is safe
		kgo.BlockRebalanceOnPoll(),                        // no rebalance while a batch is in flight
		kgo.OnPartitionsAssigned(func(_ context.Context, _ *kgo.Client, a map[string][]int32) {
			log.Info("partitions assigned", "partitions", a)
		}),
		kgo.OnPartitionsRevoked(func(_ context.Context, _ *kgo.Client, r map[string][]int32) {
			m.Rebalances.Inc()
			log.Info("partitions revoked (rebalance)", "partitions", r)
		}),
	)
	if err != nil {
		return err
	}
	// Close() leaves the group cleanly so partitions move to others immediately.
	defer cl.Close()

	for {
		fetches := cl.PollRecords(ctx, cfg.BatchSize)
		if fetches.IsClientClosed() || ctx.Err() != nil {
			return nil
		}
		fetches.EachError(func(t string, p int32, err error) {
			log.Warn("fetch error", "topic", t, "partition", p, "err", err)
		})

		var batch []Msg
		var recs []*kgo.Record
		fetches.EachRecord(func(r *kgo.Record) {
			recs = append(recs, r)
			batch = append(batch, toMsg(r))
			m.Consumed.WithLabelValues(r.Topic).Inc()
		})
		if len(batch) == 0 {
			cl.AllowRebalance()
			continue
		}

		// Process is idempotent, so retrying the same batch on a transient
		// failure is safe.
		for {
			err := proc.Process(ctx, batch)
			if err == nil {
				break
			}
			if ctx.Err() != nil {
				cl.AllowRebalance()
				return nil // shutting down: no commit, batch will be redelivered
			}
			log.Error("batch failed, retrying", "err", err)
			select {
			case <-time.After(time.Second):
			case <-ctx.Done():
				cl.AllowRebalance()
				return nil
			}
		}

		// Commit AFTER the database transaction: at-least-once delivery.
		if err := cl.CommitRecords(ctx, recs...); err != nil {
			log.Error("offset commit failed (batch may be redelivered)", "err", err)
		}
		cl.AllowRebalance()
	}
}

func toMsg(r *kgo.Record) Msg {
	msg := Msg{Topic: r.Topic, Partition: r.Partition, Offset: r.Offset, Key: r.Key, Value: r.Value, Attempt: kafka.AttemptOf(r)}
	if ms, err := strconv.ParseInt(kafka.Header(r, kafka.HeaderNotBefore), 10, 64); err == nil {
		msg.NotBefore = time.UnixMilli(ms)
	}
	return msg
}

// MonitorLag exports consumer lag per partition every interval.
// Lag = newest offset in the partition - last committed offset of the group.
func MonitorLag(ctx context.Context, cfg config.Config, m *metrics.Metrics, log *slog.Logger) {
	cl, err := kgo.NewClient(kgo.SeedBrokers(cfg.KafkaBrokers...))
	if err != nil {
		log.Error("lag monitor disabled", "err", err)
		return
	}
	defer cl.Close()
	adm := kadm.NewClient(cl)
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		lags, err := adm.Lag(cctx, cfg.ConsumerGroup)
		cancel()
		if err != nil {
			log.Warn("lag query failed", "err", err)
			continue
		}
		for _, gl := range lags {
			for topic, parts := range gl.Lag {
				for p, l := range parts {
					m.ConsumerLag.WithLabelValues(topic, fmt.Sprint(p)).Set(float64(l.Lag))
				}
			}
		}
	}
}
