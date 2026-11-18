// Package consumer holds the event-processing logic (Processor, no Kafka types)
// and the Kafka loop that drives it (Run).
package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	"adclick/internal/event"
	"adclick/internal/metrics"
	"adclick/internal/store"
)

// Msg is a Kafka record reduced to what the logic needs.
type Msg struct {
	Topic     string
	Partition int32
	Offset    int64
	Key       []byte
	Value     []byte
	Attempt   int       // retry hops so far (0 for the main topic)
	NotBefore time.Time // zero = process now
}

type Sink interface {
	ApplyBatch(ctx context.Context, events []event.Click) (int, error)
}

type AdLookup interface {
	Lookup(adID string) (store.AdInfo, bool)
}

// Parker moves a message out of the main flow.
type Parker interface {
	Retry(ctx context.Context, m Msg, attempt int, notBefore time.Time) error
	DLQ(ctx context.Context, m Msg, reason string) error
}

type Options struct {
	MaxRetries int
	RetryDelay time.Duration
	Limits     event.Limits
}

type Processor struct {
	sink   Sink
	ads    AdLookup
	parker Parker
	m      *metrics.Metrics
	log    *slog.Logger
	opt    Options
	now    func() time.Time
}

func NewProcessor(s Sink, a AdLookup, p Parker, m *metrics.Metrics, l *slog.Logger, o Options) *Processor {
	return &Processor{sink: s, ads: a, parker: p, m: m, log: l, opt: o, now: time.Now}
}

// Process handles one batch. It returns nil only when every message is safely
// stored, retried or in the DLQ, which is the signal to commit offsets.
// Any error means "do not commit": the batch will be processed again, which is
// safe because ApplyBatch is idempotent.
func (p *Processor) Process(ctx context.Context, batch []Msg) error {
	good := make([]event.Click, 0, len(batch))

	for _, m := range batch {
		if err := p.waitUntil(ctx, m.NotBefore); err != nil {
			return err
		}
		c, err := p.decode(m)
		if err != nil {
			// Permanent: the same bytes will never become valid.
			p.m.Failed.WithLabelValues("invalid").Inc()
			if err := p.parker.DLQ(ctx, m, err.Error()); err != nil {
				return fmt.Errorf("dlq: %w", err)
			}
			continue
		}
		info, ok := p.ads.Lookup(c.AdID)
		if !ok {
			// Possibly transient: the ad may be created a moment later.
			if m.Attempt >= p.opt.MaxRetries {
				p.m.Failed.WithLabelValues("dlq").Inc()
				if err := p.parker.DLQ(ctx, m, "unknown ad "+c.AdID+" after retries"); err != nil {
					return fmt.Errorf("dlq: %w", err)
				}
				continue
			}
			p.m.Failed.WithLabelValues("retry").Inc()
			delay := p.opt.RetryDelay << m.Attempt // exponential backoff
			if err := p.parker.Retry(ctx, m, m.Attempt+1, p.now().Add(delay)); err != nil {
				return fmt.Errorf("retry: %w", err)
			}
			continue
		}
		// The ads table is the source of truth, not what the client sent.
		c.CampaignID, c.AdvertiserID = info.CampaignID, info.AdvertiserID
		good = append(good, c)
	}

	if len(good) == 0 {
		return nil
	}
	n, err := p.applyWithBackoff(ctx, good)
	if err != nil {
		return err
	}
	p.m.Applied.Add(float64(n))
	p.m.Duplicates.Add(float64(len(good) - n))
	p.m.BatchSize.Observe(float64(len(good)))
	now := p.now()
	for _, c := range good {
		p.m.E2ELatency.Observe(now.Sub(c.ClickedAt).Seconds())
	}
	return nil
}

func (p *Processor) decode(m Msg) (event.Click, error) {
	var c event.Click
	if err := json.Unmarshal(m.Value, &c); err != nil {
		return c, fmt.Errorf("%w: bad json: %v", event.ErrInvalid, err)
	}
	return c, c.Validate(p.now(), p.opt.Limits)
}

// applyWithBackoff keeps trying while Postgres is unavailable. Not committing
// offsets and not polling more is natural backpressure: Kafka keeps the data,
// consumer lag grows (and alerts), and we resume where we left off.
func (p *Processor) applyWithBackoff(ctx context.Context, evs []event.Click) (int, error) {
	delay := 200 * time.Millisecond
	for {
		n, err := p.sink.ApplyBatch(ctx, evs)
		if err == nil {
			return n, nil
		}
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		p.m.Failed.WithLabelValues("db").Inc()
		p.log.Warn("database write failed, backing off", "err", err, "retry_in", delay)
		jitter := time.Duration(rand.Int64N(int64(delay) / 2))
		select {
		case <-time.After(delay + jitter):
		case <-ctx.Done():
			return 0, ctx.Err()
		}
		delay = min(delay*2, 10*time.Second)
	}
}

func (p *Processor) waitUntil(ctx context.Context, t time.Time) error {
	d := t.Sub(p.now())
	if t.IsZero() || d <= 0 {
		return nil
	}
	select {
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
