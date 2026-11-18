// Package retry implements retry with exponential backoff and "full jitter".
package retry

import (
	"context"
	"math"
	"math/rand/v2"
	"time"
)

type Policy struct {
	MaxAttempts    int // total attempts, including the first one
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	Multiplier     float64

	// Jitter spreads retries out so 1,000 clients that failed together don't
	// all retry at the same millisecond (the "thundering herd").
	Jitter bool

	// ShouldRetry must return false for errors that can never succeed on a
	// retry (card declined, validation error, circuit open).
	ShouldRetry func(error) bool

	OnRetry func(attempt int, err error, wait time.Duration)
}

// Backoff returns the wait before retry number `attempt` (1-based):
//
//	cap  = min(MaxBackoff, InitialBackoff * Multiplier^(attempt-1))
//	wait = cap                 (no jitter)
//	wait = random[0, cap)      (full jitter)
func (p Policy) Backoff(attempt int) time.Duration {
	mult := p.Multiplier
	if mult <= 0 {
		mult = 2
	}
	d := float64(p.InitialBackoff) * math.Pow(mult, float64(attempt-1))
	if p.MaxBackoff > 0 && d > float64(p.MaxBackoff) {
		d = float64(p.MaxBackoff)
	}
	if p.Jitter && d > 0 {
		d = rand.Float64() * d
	}
	return time.Duration(d)
}

// Do calls fn until it succeeds, returns a non-retryable error, runs out of
// attempts, or ctx is done. It returns the last error and the attempts made.
func Do(ctx context.Context, p Policy, fn func(ctx context.Context, attempt int) error) (int, error) {
	if p.MaxAttempts <= 0 {
		p.MaxAttempts = 1
	}
	var err error
	for attempt := 1; ; attempt++ {
		err = fn(ctx, attempt)
		if err == nil {
			return attempt, nil
		}
		if attempt >= p.MaxAttempts || (p.ShouldRetry != nil && !p.ShouldRetry(err)) {
			return attempt, err
		}
		wait := p.Backoff(attempt)
		if p.OnRetry != nil {
			p.OnRetry(attempt, err, wait)
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return attempt, ctx.Err()
		case <-t.C:
		}
	}
}
