// Package service wraps the counter store with fault tolerance:
// per-attempt timeout -> bounded retry with backoff -> circuit breaker -> fail-open/fail-closed policy.
package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"example.com/fixed-window-counter/internal/config"
	"example.com/fixed-window-counter/internal/limiter"
	"example.com/fixed-window-counter/internal/store"
)

// ErrUnavailable is returned in fail-closed mode when the counter cannot be reached.
var ErrUnavailable = errors.New("rate limiter unavailable")

// Counter is the part of the store this package needs. Defined here (where it is used)
// so tests can swap in a fake, and a Redis store could be added later.
type Counter interface {
	Increment(ctx context.Context, scopeType, scopeKey string, window time.Duration) (store.Result, error)
}

type Options struct {
	FailOpen         bool
	CallTimeout      time.Duration
	MaxAttempts      int
	BaseBackoff      time.Duration
	BreakerThreshold int
	BreakerCooldown  time.Duration

	// Test hooks; nil means real time.
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error
}

func OptionsFromConfig(c config.Config) Options {
	return Options{
		FailOpen:         c.FailOpen,
		CallTimeout:      c.DBTimeout,
		MaxAttempts:      c.RetryAttempts,
		BaseBackoff:      c.RetryBackoff,
		BreakerThreshold: c.BreakerThreshold,
		BreakerCooldown:  c.BreakerCooldown,
	}
}

type Service struct {
	counter Counter
	opts    Options
	breaker *breaker
	log     *slog.Logger
}

func New(counter Counter, opts Options, log *slog.Logger) *Service {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Sleep == nil {
		opts.Sleep = sleepCtx
	}
	b := newBreaker(opts.BreakerThreshold, opts.BreakerCooldown, opts.Now)
	b.onChange = func(from, to breakerState) {
		log.Warn("circuit breaker state changed", "from", from.String(), "to", to.String())
	}
	return &Service{counter: counter, opts: opts, breaker: b, log: log}
}

// Check counts one request for the scope and returns the decision.
// The error is non-nil only when the caller gave up (ctx done) or when failing closed.
func (s *Service) Check(ctx context.Context, scopeType, scopeKey string, rule config.Rule) (limiter.Decision, error) {
	if !s.breaker.Allow() {
		return s.degrade(rule, scopeType, scopeKey, errors.New("circuit breaker open"))
	}

	res, err := s.increment(ctx, scopeType, scopeKey, rule.Window)
	switch {
	case err == nil:
		s.breaker.Success()
	case ctx.Err() != nil:
		s.breaker.Release()
		return limiter.Decision{}, ctx.Err()
	case retryable(err):
		s.breaker.Failure()
		return s.degrade(rule, scopeType, scopeKey, err)
	default:
		// The database answered with a real error (bug, bad data): not an outage, so the
		// breaker stays closed, but we still cannot count this request.
		s.breaker.Success()
		s.log.Error("counter failed with non-retryable error", "scope", scopeType, "err", err)
		return s.degrade(rule, scopeType, scopeKey, err)
	}

	return limiter.Decide(res.Count, rule.Limit, res.WindowStart, rule.Window, res.Now), nil
}

// degrade applies the failure policy:
//   - fail-open:   let the request through, uncounted. Availability over protection.
//   - fail-closed: refuse it (caller answers 503). Protection over availability.
func (s *Service) degrade(rule config.Rule, scopeType, scopeKey string, cause error) (limiter.Decision, error) {
	if s.opts.FailOpen {
		s.log.Warn("rate limiter degraded, failing open", "scope", scopeType, "key", scopeKey, "cause", cause)
		return limiter.Decision{Allowed: true, Limit: rule.Limit, Degraded: true}, nil
	}
	s.log.Error("rate limiter unavailable, failing closed", "scope", scopeType, "key", scopeKey, "cause", cause)
	return limiter.Decision{}, fmt.Errorf("%w: %w", ErrUnavailable, cause)
}

// increment makes up to MaxAttempts tries, each with its own timeout.
//
// A retry after a timeout can count a request twice (the first attempt may have committed
// before we gave up on it). For a rate limiter that errs on the safe side: it can only
// over-count slightly, never let extra traffic through.
func (s *Service) increment(ctx context.Context, scopeType, scopeKey string, window time.Duration) (store.Result, error) {
	var lastErr error
	for attempt := 1; attempt <= s.opts.MaxAttempts; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, s.opts.CallTimeout)
		res, err := s.counter.Increment(attemptCtx, scopeType, scopeKey, window)
		cancel()
		if err == nil {
			return res, nil
		}
		lastErr = err

		if ctx.Err() != nil || !retryable(err) || attempt == s.opts.MaxAttempts {
			break
		}
		delay := s.backoff(attempt)
		s.log.Warn("counter attempt failed, retrying", "attempt", attempt, "delay", delay, "err", err)
		if err := s.opts.Sleep(ctx, delay); err != nil {
			return store.Result{}, err
		}
	}
	return store.Result{}, lastErr
}

// backoff doubles each attempt (capped) and adds full jitter, so many instances that
// failed together do not all retry at the same instant.
func (s *Service) backoff(attempt int) time.Duration {
	d := min(s.opts.BaseBackoff<<(attempt-1), time.Second)
	if d <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(d) + 1))
}

// retryable reports whether the failure is temporary and the same call may work a moment later.
func retryable(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "40001", // serialization_failure
			"40P01", // deadlock_detected
			"57P03", // cannot_connect_now (server starting up)
			"53300": // too_many_connections
			return true
		}
		return strings.HasPrefix(pgErr.Code, "08") // connection exceptions
	}
	var netErr net.Error
	return errors.As(err, &netErr) || pgconn.SafeToRetry(err)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
