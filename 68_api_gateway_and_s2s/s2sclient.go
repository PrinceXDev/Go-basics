package main

// ===========================================================================
// THE SERVICE-TO-SERVICE CLIENT — how payments talks to accounts.
//
// A network call is not a function call. It can:
//   - fail          (accounts restarted)            -> retry, with backoff
//   - hang          (accounts' DB is locked)        -> timeout every attempt
//   - succeed but you never hear back (response lost) -> idempotency on the server
//   - keep failing  (accounts is down)              -> circuit breaker, fail fast
//
// Layering, outermost first:   breaker( retry( timeout( HTTP call ) ) )
//
// Retry rules that keep you out of trouble in a bank:
//   1. Only retry calls that are SAFE to repeat: GETs, or POSTs carrying an
//      idempotency reference the server deduplicates. Every call this client
//      makes is one of those.
//   2. Only retry TRANSIENT failures: network errors, 502/503/504, 429.
//      Never 400/403/404/422 — "insufficient funds" won't fix itself.
//   3. Back off exponentially WITH JITTER, so 1,000 clients don't retry in
//      the same millisecond and knock the service over again.
//   4. Retry at ONE layer only. Gateway x3, payments x3, accounts x3 = 27
//      calls hitting a database that is already struggling ("retry storm").
// ===========================================================================

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"
)

// APIError is a response the other service DID send: it is up, and it said no.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("%d %s: %s", e.Status, e.Code, e.Message) }

var ErrCircuitOpen = errors.New("circuit open: failing fast without calling")

type ServiceClient struct {
	Caller, Target string
	BaseURL        string
	HTTP           *http.Client
	Auth           ServiceAuth
	Breaker        *Breaker
	MaxAttempts    int
	AttemptTimeout time.Duration
	Log            *slog.Logger
}

func (c *ServiceClient) Call(ctx context.Context, method, path string, in, out any) error {
	if !c.Breaker.Allow() {
		return ErrCircuitOpen
	}
	var body []byte
	if in != nil {
		var err error
		if body, err = json.Marshal(in); err != nil {
			return err
		}
	}

	var err error
	backoff := 50 * time.Millisecond
	for attempt := 1; ; attempt++ {
		err = c.once(ctx, method, path, body, out)
		if err == nil || !retryable(err) || attempt == c.MaxAttempts {
			break
		}
		// Full jitter: sleep a random time in [0, backoff).
		sleep := time.Duration(rand.Int64N(int64(backoff)))
		c.Log.Info("s2s retry", "rid", requestIDFrom(ctx), "to", c.Target, "path", path,
			"attempt", attempt, "err", err, "sleep", sleep.Round(time.Millisecond))
		select {
		case <-time.After(sleep):
		case <-ctx.Done(): // the caller's deadline wins over our retry plan
			c.Breaker.Record(false)
			return ctx.Err()
		}
		backoff *= 2
	}
	// A 4xx means the service is healthy and answered; only infra failures
	// count against the breaker.
	var apiErr *APIError
	c.Breaker.Record(err == nil || (errors.As(err, &apiErr) && apiErr.Status < 500))
	return err
}

func (c *ServiceClient) once(ctx context.Context, method, path string, body []byte, out any) error {
	// Per-attempt timeout INSIDE the retry: each attempt gets its own budget,
	// and the parent ctx still caps the total.
	ctx, cancel := context.WithTimeout(ctx, c.AttemptTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Auth.Mint(c.Caller, c.Target))
	req.Header.Set("Content-Type", "application/json")
	// Propagation: the same request ID and customer travel with every hop,
	// so one search in the log system shows the whole journey.
	req.Header.Set(HeaderRequestID, requestIDFrom(ctx))
	if uid := userIDFrom(ctx); uid != "" {
		req.Header.Set(HeaderUserID, uid)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("call %s %s: %w", c.Target, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode >= 400 {
		var eb errorBody
		_ = json.Unmarshal(raw, &eb)
		return &APIError{Status: resp.StatusCode, Code: eb.Code, Message: eb.Message}
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func retryable(err error) bool {
	if errors.Is(err, ErrCircuitOpen) || errors.Is(err, context.Canceled) {
		return false
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Status {
		case http.StatusTooManyRequests, http.StatusBadGateway,
			http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return true
		}
		return false
	}
	return true // network error or per-attempt timeout
}

// ---------------------------------------------------------------------------
// CIRCUIT BREAKER — like the electrical breaker in your house.
//
//   CLOSED ──(N failures in a row)──> OPEN ──(cooldown passes)──> HALF-OPEN
//     ^                                ^                              |
//     └────────(probe succeeds)────────┼──────────────────────────────┤
//                                      └──────(probe fails)───────────┘
//
// While OPEN we don't even try: the caller gets an error in microseconds
// instead of waiting for timeouts, and the sick service gets room to recover.
// ---------------------------------------------------------------------------

type breakerState string

const (
	stateClosed   breakerState = "CLOSED"
	stateOpen     breakerState = "OPEN"
	stateHalfOpen breakerState = "HALF-OPEN"
)

type Breaker struct {
	mu        sync.Mutex
	name      string
	state     breakerState
	failures  int
	threshold int
	cooldown  time.Duration
	openedAt  time.Time
	probing   bool
	log       *slog.Logger
}

func NewBreaker(name string, threshold int, cooldown time.Duration, log *slog.Logger) *Breaker {
	return &Breaker{name: name, state: stateClosed, threshold: threshold, cooldown: cooldown, log: log}
}

func (b *Breaker) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case stateOpen:
		if time.Since(b.openedAt) < b.cooldown {
			return false
		}
		b.set(stateHalfOpen)
		b.probing = true
		return true // exactly one probe request goes through
	case stateHalfOpen:
		return false // a probe is already in flight
	}
	return true
}

func (b *Breaker) Record(success bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if success {
		b.failures = 0
		if b.state != stateClosed {
			b.set(stateClosed)
		}
		return
	}
	b.failures++
	if b.state == stateHalfOpen || b.failures >= b.threshold {
		b.openedAt = time.Now()
		if b.state != stateOpen {
			b.set(stateOpen)
		}
	}
}

func (b *Breaker) State() breakerState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

func (b *Breaker) set(s breakerState) {
	b.log.Warn("circuit breaker", "target", b.name, "from", b.state, "to", s)
	b.state = s
}
