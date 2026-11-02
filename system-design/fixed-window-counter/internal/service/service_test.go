package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"example.com/fixed-window-counter/internal/config"
	"example.com/fixed-window-counter/internal/store"
)

var (
	errTransient = &pgconn.PgError{Code: "40001"} // serialization_failure: retryable
	errPermanent = &pgconn.PgError{Code: "23505"} // unique_violation: not retryable
)

type fakeCounter struct {
	mu    sync.Mutex
	calls int
	fn    func(call int, ctx context.Context) (store.Result, error)
}

func (f *fakeCounter) Increment(ctx context.Context, _, _ string, _ time.Duration) (store.Result, error) {
	f.mu.Lock()
	f.calls++
	n := f.calls
	f.mu.Unlock()
	return f.fn(n, ctx)
}

func (f *fakeCounter) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

var rule = config.Rule{Limit: 5, Window: time.Minute}

func ok(count int) store.Result {
	start := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	return store.Result{Count: count, WindowStart: start, Now: start.Add(10 * time.Second)}
}

type fixture struct {
	svc   *Service
	fake  *fakeCounter
	clock *time.Time
}

func newFixture(t *testing.T, failOpen bool, fn func(call int, ctx context.Context) (store.Result, error)) fixture {
	t.Helper()
	clock := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	fake := &fakeCounter{fn: fn}
	svc := New(fake, Options{
		FailOpen:         failOpen,
		CallTimeout:      50 * time.Millisecond,
		MaxAttempts:      3,
		BaseBackoff:      time.Millisecond,
		BreakerThreshold: 2,
		BreakerCooldown:  10 * time.Second,
		Now:              func() time.Time { return clock },
		Sleep:            func(context.Context, time.Duration) error { return nil },
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return fixture{svc: svc, fake: fake, clock: &clock}
}

func alwaysFail(err error) func(int, context.Context) (store.Result, error) {
	return func(int, context.Context) (store.Result, error) { return store.Result{}, err }
}

func TestRetriesTransientErrorThenSucceeds(t *testing.T) {
	f := newFixture(t, true, func(call int, _ context.Context) (store.Result, error) {
		if call < 3 {
			return store.Result{}, errTransient
		}
		return ok(1), nil
	})

	d, err := f.svc.Check(context.Background(), "user", "u1", rule)
	if err != nil || !d.Allowed || d.Degraded {
		t.Fatalf("got %+v, %v; want a normal allowed decision", d, err)
	}
	if f.fake.Calls() != 3 {
		t.Errorf("calls = %d, want 3", f.fake.Calls())
	}
}

func TestDoesNotRetryPermanentError(t *testing.T) {
	f := newFixture(t, true, alwaysFail(errPermanent))

	d, _ := f.svc.Check(context.Background(), "user", "u1", rule)
	if f.fake.Calls() != 1 {
		t.Errorf("calls = %d, want 1 (no retry for permanent errors)", f.fake.Calls())
	}
	if !d.Degraded {
		t.Error("expected a degraded decision")
	}
}

func TestGivesUpAfterMaxAttempts(t *testing.T) {
	f := newFixture(t, true, alwaysFail(errTransient))

	f.svc.Check(context.Background(), "user", "u1", rule)
	if f.fake.Calls() != 3 {
		t.Errorf("calls = %d, want 3", f.fake.Calls())
	}
}

func TestSlowDatabaseIsCutOffByTimeout(t *testing.T) {
	f := newFixture(t, true, func(_ int, ctx context.Context) (store.Result, error) {
		<-ctx.Done() // a database that never answers
		return store.Result{}, ctx.Err()
	})
	f.svc.opts.MaxAttempts = 1

	start := time.Now()
	d, err := f.svc.Check(context.Background(), "user", "u1", rule)
	if time.Since(start) > time.Second {
		t.Error("check did not respect the call timeout")
	}
	if err != nil || !d.Degraded {
		t.Errorf("got %+v, %v; want degraded allow", d, err)
	}
}

// Fail-open: availability over protection.
func TestFailOpenAllowsRequestWhenDatabaseDown(t *testing.T) {
	f := newFixture(t, true, alwaysFail(errTransient))

	d, err := f.svc.Check(context.Background(), "user", "u1", rule)
	if err != nil {
		t.Fatalf("fail-open must not return an error, got %v", err)
	}
	if !d.Allowed || !d.Degraded {
		t.Errorf("got %+v, want allowed + degraded", d)
	}
}

// Fail-closed: protection over availability.
func TestFailClosedRejectsRequestWhenDatabaseDown(t *testing.T) {
	f := newFixture(t, false, alwaysFail(errTransient))

	_, err := f.svc.Check(context.Background(), "user", "u1", rule)
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
}

func TestBreakerOpensAndSkipsDatabase(t *testing.T) {
	f := newFixture(t, true, alwaysFail(errTransient))
	f.svc.opts.MaxAttempts = 1

	f.svc.Check(context.Background(), "user", "u1", rule)
	f.svc.Check(context.Background(), "user", "u1", rule) // threshold (2) reached
	before := f.fake.Calls()

	d, _ := f.svc.Check(context.Background(), "user", "u1", rule)
	if f.fake.Calls() != before {
		t.Error("database was called while the breaker was open")
	}
	if !d.Degraded {
		t.Error("expected a degraded decision while open")
	}
}

func TestBreakerRecoversThroughHalfOpenProbe(t *testing.T) {
	healthy := false
	f := newFixture(t, true, func(int, context.Context) (store.Result, error) {
		if healthy {
			return ok(1), nil
		}
		return store.Result{}, errTransient
	})
	f.svc.opts.MaxAttempts = 1
	f.svc.Check(context.Background(), "user", "u1", rule)
	f.svc.Check(context.Background(), "user", "u1", rule) // open

	healthy = true
	*f.clock = f.clock.Add(11 * time.Second) // cooldown (10s) has passed

	d, _ := f.svc.Check(context.Background(), "user", "u1", rule) // the probe
	if d.Degraded {
		t.Fatal("probe should have reached the healthy database")
	}
	before := f.fake.Calls()
	f.svc.Check(context.Background(), "user", "u1", rule)
	if f.fake.Calls() != before+1 {
		t.Error("breaker should be closed again and calling the database")
	}
}

func TestFailedProbeReopensBreaker(t *testing.T) {
	f := newFixture(t, true, alwaysFail(errTransient))
	f.svc.opts.MaxAttempts = 1
	f.svc.Check(context.Background(), "user", "u1", rule)
	f.svc.Check(context.Background(), "user", "u1", rule) // open

	*f.clock = f.clock.Add(11 * time.Second)
	f.svc.Check(context.Background(), "user", "u1", rule) // probe fails
	before := f.fake.Calls()

	f.svc.Check(context.Background(), "user", "u1", rule)
	if f.fake.Calls() != before {
		t.Error("breaker should be open again right after a failed probe")
	}
}

func TestClientCancellationDoesNotTripBreaker(t *testing.T) {
	f := newFixture(t, true, func(_ int, ctx context.Context) (store.Result, error) {
		<-ctx.Done()
		return store.Result{}, ctx.Err()
	})
	f.svc.opts.MaxAttempts = 1

	for i := 0; i < 5; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := f.svc.Check(ctx, "user", "u1", rule); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	}
	if !f.svc.breaker.Allow() {
		t.Error("client hang-ups must not open the breaker")
	}
}

func TestRetryable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"serialization failure", &pgconn.PgError{Code: "40001"}, true},
		{"deadlock", &pgconn.PgError{Code: "40P01"}, true},
		{"connection exception class", &pgconn.PgError{Code: "08006"}, true},
		{"timeout", context.DeadlineExceeded, true},
		{"unique violation", &pgconn.PgError{Code: "23505"}, false},
		{"syntax error", &pgconn.PgError{Code: "42601"}, false},
		{"unknown plain error", errors.New("boom"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := retryable(tt.err); got != tt.want {
				t.Errorf("retryable = %v, want %v", got, tt.want)
			}
		})
	}
}
