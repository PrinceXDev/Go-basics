package middleware

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"example.com/fixed-window-counter/internal/config"
	"example.com/fixed-window-counter/internal/limiter"
	"example.com/fixed-window-counter/internal/service"
	"example.com/fixed-window-counter/internal/store"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

// memCounter is a fixed-window counter in memory, same behaviour as the Postgres upsert.
type memCounter struct {
	mu     sync.Mutex
	counts map[string]int
	now    time.Time
}

func (m *memCounter) Increment(_ context.Context, scopeType, key string, window time.Duration) (store.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	start := limiter.WindowStart(m.now, window)
	id := scopeType + "/" + key + "/" + start.String()
	m.counts[id]++
	return store.Result{Count: m.counts[id], WindowStart: start, Now: m.now}, nil
}

type failingCounter struct{}

func (failingCounter) Increment(context.Context, string, string, time.Duration) (store.Result, error) {
	return store.Result{}, errors.New("db down")
}

func newServer(t *testing.T, c service.Counter, failOpen bool, limit int) http.Handler {
	t.Helper()
	svc := service.New(c, service.Options{
		FailOpen: failOpen, CallTimeout: time.Second, MaxAttempts: 1,
		BreakerThreshold: 100, BreakerCooldown: time.Second,
	}, discard)
	rule := config.Rule{Limit: limit, Window: time.Minute}
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	return RateLimit(svc, rule, discard)(ok)
}

func do(h http.Handler, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/demo", nil)
	if key != "" {
		req.Header.Set("X-API-Key", key)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func fixedClock() time.Time { return time.Date(2026, 1, 1, 12, 0, 40, 0, time.UTC) }

// The scenario from the demo: 10 requests per minute, the rest are rejected.
func TestTenAllowedRestRejected(t *testing.T) {
	h := newServer(t, &memCounter{counts: map[string]int{}, now: fixedClock()}, true, 10)

	allowed, blocked := 0, 0
	for i := 1; i <= 15; i++ {
		rec := do(h, "key-a")
		switch rec.Code {
		case http.StatusOK:
			allowed++
		case http.StatusTooManyRequests:
			blocked++
			if got := rec.Header().Get("Retry-After"); got != "20" {
				t.Errorf("request %d: Retry-After = %q, want 20 (window ends 12:01:00)", i, got)
			}
			if got := rec.Header().Get("X-RateLimit-Remaining"); got != "0" {
				t.Errorf("request %d: Remaining = %q, want 0", i, got)
			}
		default:
			t.Fatalf("request %d: unexpected status %d", i, rec.Code)
		}
	}
	if allowed != 10 || blocked != 5 {
		t.Errorf("allowed=%d blocked=%d, want 10 and 5", allowed, blocked)
	}
}

func TestHeadersOnAllowedRequest(t *testing.T) {
	h := newServer(t, &memCounter{counts: map[string]int{}, now: fixedClock()}, true, 10)

	rec := do(h, "key-a")
	if rec.Header().Get("X-RateLimit-Limit") != "10" || rec.Header().Get("X-RateLimit-Remaining") != "9" {
		t.Errorf("headers = %v", rec.Header())
	}
	if rec.Header().Get("Retry-After") != "" {
		t.Error("Retry-After must only be sent on 429")
	}
}

func TestDifferentKeysHaveSeparateLimits(t *testing.T) {
	h := newServer(t, &memCounter{counts: map[string]int{}, now: fixedClock()}, true, 1)

	do(h, "key-a")
	if got := do(h, "key-a").Code; got != http.StatusTooManyRequests {
		t.Errorf("key-a second request = %d, want 429", got)
	}
	if got := do(h, "key-b").Code; got != http.StatusOK {
		t.Errorf("key-b first request = %d, want 200", got)
	}
}

func TestMissingAPIKeyIs401(t *testing.T) {
	h := newServer(t, &memCounter{counts: map[string]int{}, now: fixedClock()}, true, 10)
	if got := do(h, "").Code; got != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", got)
	}
}

func TestDatabaseDownFailOpen(t *testing.T) {
	rec := do(newServer(t, failingCounter{}, true, 10), "key-a")
	if rec.Code != http.StatusOK || rec.Header().Get("X-RateLimit-Degraded") != "true" {
		t.Errorf("status=%d degraded=%q, want 200 + degraded header", rec.Code, rec.Header().Get("X-RateLimit-Degraded"))
	}
}

func TestDatabaseDownFailClosed(t *testing.T) {
	if got := do(newServer(t, failingCounter{}, false, 10), "key-a").Code; got != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", got)
	}
}
