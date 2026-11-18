// Package kafka wraps franz-go: topic setup and the producer.
package kafka

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"

	"adclick/internal/config"
	"adclick/internal/event"
)

// ErrBackpressure means the producer buffer is full (Kafka slow or down).
// The API turns it into HTTP 503 + Retry-After instead of queueing forever.
var ErrBackpressure = errors.New("producer buffer full")

const (
	HeaderAttempt   = "x-attempt"    // how many times this event has been retried
	HeaderNotBefore = "x-not-before" // unix ms; do not process earlier than this
	HeaderError     = "x-error"      // why it was parked in the DLQ
	HeaderOrigin    = "x-origin-topic"
)

// EnsureTopics creates the three topics if missing.
//
//	events: main stream, 7 days retention so we can replay after a bug.
//	retry:  transient failures, parked so the main partition keeps moving.
//	dlq:    poison messages kept 14 days for humans.
//
// min.insync.replicas=2 with RF=3 plus acks=all means an acknowledged click is
// on at least two brokers.
func EnsureTopics(ctx context.Context, cl *kgo.Client, c config.Config) error {
	adm := kadm.NewClient(cl)
	minISR := "1"
	if c.ReplicationFactor >= 3 {
		minISR = "2"
	}
	day := int64(24 * time.Hour / time.Millisecond)
	specs := []struct {
		name  string
		parts int32
		ret   int64
	}{
		{c.TopicEvents, c.TopicPartitions, 7 * day},
		{c.TopicRetry, max(c.TopicPartitions/2, 1), 1 * day},
		{c.TopicDLQ, 3, 14 * day},
	}
	for _, s := range specs {
		ret, isr := strconv.FormatInt(s.ret, 10), minISR
		resp, err := adm.CreateTopic(ctx, s.parts, c.ReplicationFactor, map[string]*string{
			"retention.ms": &ret, "min.insync.replicas": &isr,
		}, s.name)
		if err != nil && !errors.Is(err, kerr.TopicAlreadyExists) {
			return fmt.Errorf("create topic %s: %w", s.name, err)
		}
		_ = resp
	}
	return nil
}

type Producer struct {
	cl *kgo.Client
}

func NewProducer(brokers []string) (*Producer, error) {
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		// Idempotent producer is on by default in franz-go: broker-side sequence
		// numbers drop duplicates caused by producer retries. It requires acks=all.
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.ProducerLinger(5*time.Millisecond), // small wait so records share a batch
		kgo.ProducerBatchCompression(kgo.Lz4Compression()),
		kgo.MaxBufferedRecords(20_000), // backpressure limit
		kgo.RecordDeliveryTimeout(8*time.Second),
	)
	if err != nil {
		return nil, err
	}
	return &Producer{cl: cl}, nil
}

// Client exposes the underlying client (used once for topic admin).
func (p *Producer) Client() *kgo.Client { return p.cl }

// Publish returns only after the broker acknowledged the record (acks=all).
func (p *Producer) Publish(ctx context.Context, topic string, c event.Click) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return p.PublishRaw(ctx, topic, c.PartitionKey(), b, nil)
}

func (p *Producer) PublishRaw(ctx context.Context, topic string, key, value []byte, headers []kgo.RecordHeader) error {
	rec := &kgo.Record{Topic: topic, Key: key, Value: value, Headers: headers}
	res := p.cl.ProduceSync(ctx, rec)
	if err := res.FirstErr(); err != nil {
		if errors.Is(err, kgo.ErrMaxBuffered) {
			return ErrBackpressure
		}
		return err
	}
	return nil
}

func (p *Producer) Ping(ctx context.Context) error { return p.cl.Ping(ctx) }

// Close flushes buffered records first, so a graceful shutdown loses nothing.
func (p *Producer) Close(ctx context.Context) {
	_ = p.cl.Flush(ctx)
	p.cl.Close()
}

// Header helpers -------------------------------------------------------------

func Header(r *kgo.Record, key string) string {
	for _, h := range r.Headers {
		if h.Key == key {
			return string(h.Value)
		}
	}
	return ""
}

func AttemptOf(r *kgo.Record) int {
	n, _ := strconv.Atoi(Header(r, HeaderAttempt))
	return n
}
