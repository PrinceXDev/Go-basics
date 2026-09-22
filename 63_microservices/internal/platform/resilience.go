package platform

// ===========================================================================
// RESILIENCE: the three patterns that stop one sick service taking the whole
// system down. Interviewers ask about these constantly, and the reason is
// that getting them WRONG is worse than not having them at all.
//
//	TIMEOUT          bound how long you wait. Non-negotiable, on every call.
//	RETRY            recover from a blip -- but only for SAFE operations,
//	                 only on retryable codes, and always with backoff+jitter.
//	CIRCUIT BREAKER  stop calling a service that is clearly down, so you fail
//	                 fast instead of piling up goroutines on a 30s timeout.
//
// HOW THEY COMPOSE (this order, always):
//
//	breaker( retry( timeout( call ) ) )
//
// The per-attempt timeout is INSIDE the retry, so each attempt gets its own
// budget. The breaker is OUTSIDE, so it sees the final verdict and is not
// fooled by intermediate attempt failures.
//
// THE DEADLY MISTAKE: retries multiply. Gateway retries 3x, order retries
// 3x, user retries 3x = 27 requests hitting a database that is already on
// fire. This is a "retry storm", and it is how a small outage becomes a big
// one. Rule: retry at ONE layer only (usually the outermost caller), and
// always cap with a deadline budget that shrinks as you go down the chain.
// ===========================================================================

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ---------------------------------------------------------------------------
// RETRY
// ---------------------------------------------------------------------------

type RetryPolicy struct {
	MaxAttempts int           // total attempts, not retries-after-the-first
	Base        time.Duration // first backoff
	Max         time.Duration // backoff ceiling
}

func DefaultRetry() RetryPolicy {
	return RetryPolicy{MaxAttempts: 3, Base: 50 * time.Millisecond, Max: 500 * time.Millisecond}
}

// Retryable decides whether a failure is worth another attempt.
//
// Unavailable      -> the server is down / the connection dropped. Safe.
// ResourceExhausted-> rate limited. Safe WITH backoff (never without).
// Aborted          -> a concurrency conflict; the whole transaction retries.
//
// Everything else is NOT retryable:
//   - InvalidArgument/NotFound/PermissionDenied: the answer will not change.
//   - DeadlineExceeded: you already ran out of time; retrying steals budget
//     from the caller. (gRPC's own built-in policy also excludes it.)
//   - Internal: the server has a bug; hammering it will not fix the bug.
func Retryable(err error) bool {
	switch status.Code(err) {
	case codes.Unavailable, codes.ResourceExhausted, codes.Aborted:
		return true
	default:
		return false
	}
}

// Do runs fn with retries, backoff and FULL JITTER.
//
// Why jitter: without it, 500 clients that failed at the same instant all
// retry at exactly t+50ms, then t+100ms... You rebuild the thundering herd
// you were trying to smooth out. Randomising the wait spreads the load.
func (p RetryPolicy) Do(ctx context.Context, name string, fn func(ctx context.Context) error) error {
	var lastErr error

	for attempt := 1; attempt <= p.MaxAttempts; attempt++ {
		lastErr = fn(ctx)
		if lastErr == nil {
			return nil
		}
		if !Retryable(lastErr) {
			return lastErr // fail fast: another attempt cannot help
		}
		if attempt == p.MaxAttempts {
			break
		}

		// Exponential backoff: base * 2^(attempt-1), capped.
		backoff := min(p.Base<<(attempt-1), p.Max)
		wait := time.Duration(rand.Int63n(int64(backoff) + 1)) // full jitter

		Logger(ctx).Warn("retrying",
			"target", name, "attempt", attempt,
			"code", status.Code(lastErr).String(), "backoff_ms", wait.Milliseconds())

		select {
		case <-time.After(wait):
		case <-ctx.Done():
			// The caller's deadline beat us. Stop -- do NOT start another
			// attempt you cannot finish.
			return fmt.Errorf("%s: %w (last: %v)", name, ctx.Err(), lastErr)
		}
	}
	return fmt.Errorf("%s: exhausted %d attempts: %w", name, p.MaxAttempts, lastErr)
}

// ---------------------------------------------------------------------------
// CIRCUIT BREAKER
//
// THE STATE MACHINE -- draw this in an interview and you have answered the
// question before they finish asking it:
//
//	                    failures >= threshold
//	   +--------+  ------------------------------->  +--------+
//	   | CLOSED |                                    |  OPEN  |
//	   +--------+  <-------------------------------  +--------+
//	        ^          probe succeeded                    |
//	        |                                             | cooldown elapsed
//	        |          +-----------+                      |
//	        +--------- | HALF-OPEN | <--------------------+
//	                   +-----------+
//	                        |  probe failed -> straight back to OPEN
//
//	CLOSED    : normal. Count failures.
//	OPEN      : fail INSTANTLY without calling. This is the point -- you
//	            protect the sick service from load AND protect yourself from
//	            goroutines piling up on a call that will time out anyway.
//	HALF-OPEN : after the cooldown, let ONE request through as a probe.
//	            Success -> CLOSED. Failure -> OPEN for another cooldown.
//
// WHAT IT IS NOT: a retry. A retry hopes the next call works; a breaker has
// concluded it will not. They solve different failures and belong together.
// ---------------------------------------------------------------------------

type BreakerState int

const (
	Closed BreakerState = iota
	Open
	HalfOpen
)

func (s BreakerState) String() string {
	switch s {
	case Closed:
		return "CLOSED"
	case Open:
		return "OPEN"
	default:
		return "HALF-OPEN"
	}
}

var ErrCircuitOpen = errors.New("circuit breaker open")

type Breaker struct {
	name      string
	threshold int           // consecutive failures before opening
	cooldown  time.Duration // how long to stay open

	mu            sync.Mutex
	state         BreakerState
	failures      int
	openedAt      time.Time
	probeInFlight bool
	onChange      func(name string, from, to BreakerState)
}

func NewBreaker(name string, threshold int, cooldown time.Duration, onChange func(string, BreakerState, BreakerState)) *Breaker {
	return &Breaker{name: name, threshold: threshold, cooldown: cooldown, onChange: onChange}
}

func (b *Breaker) State() BreakerState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

// Do runs fn unless the circuit says otherwise.
func (b *Breaker) Do(ctx context.Context, fn func(ctx context.Context) error) error {
	if err := b.allow(); err != nil {
		return err
	}

	err := fn(ctx)

	// A caller-side cancellation is not the callee's fault -- do not let it
	// trip the breaker, or one impatient client can open the circuit for
	// everybody. Only count failures that indicate the DEPENDENCY is sick.
	if errors.Is(err, context.Canceled) {
		b.release()
		return err
	}

	if err != nil && (Retryable(err) || status.Code(err) == codes.DeadlineExceeded || status.Code(err) == codes.Internal) {
		b.onFailure()
	} else {
		b.onSuccess()
	}
	return err
}

func (b *Breaker) allow() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case Closed:
		return nil

	case Open:
		if time.Since(b.openedAt) < b.cooldown {
			return fmt.Errorf("%s: %w", b.name, ErrCircuitOpen) // fail fast, no call made
		}
		b.transition(HalfOpen)
		b.probeInFlight = true
		return nil

	default: // HalfOpen: exactly ONE probe at a time
		if b.probeInFlight {
			return fmt.Errorf("%s: %w (probe in flight)", b.name, ErrCircuitOpen)
		}
		b.probeInFlight = true
		return nil
	}
}

func (b *Breaker) release() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.probeInFlight = false
}

func (b *Breaker) onSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures = 0
	b.probeInFlight = false
	if b.state != Closed {
		b.transition(Closed)
	}
}

func (b *Breaker) onFailure() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.probeInFlight = false

	if b.state == HalfOpen {
		b.openedAt = time.Now()
		b.transition(Open) // the probe failed: straight back to open
		return
	}

	b.failures++
	if b.failures >= b.threshold && b.state == Closed {
		b.openedAt = time.Now()
		b.transition(Open)
	}
}

// transition must be called with the lock held.
func (b *Breaker) transition(to BreakerState) {
	from := b.state
	b.state = to
	if b.onChange != nil && from != to {
		go b.onChange(b.name, from, to) // never call out while holding a lock
	}
}
