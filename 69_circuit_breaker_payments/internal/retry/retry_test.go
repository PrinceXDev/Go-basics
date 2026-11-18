package retry

import (
	"context"
	"errors"
	"testing"
	"time"
)

var errTemp = errors.New("temporary")
var errFatal = errors.New("fatal")

func TestBackoffGrowsAndCaps(t *testing.T) {
	p := Policy{InitialBackoff: 100 * time.Millisecond, MaxBackoff: time.Second, Multiplier: 2}
	want := []time.Duration{100, 200, 400, 800, 1000, 1000}
	for i, w := range want {
		if got := p.Backoff(i + 1); got != w*time.Millisecond {
			t.Errorf("attempt %d: got %v want %v", i+1, got, w*time.Millisecond)
		}
	}
}

func TestJitterStaysBelowCap(t *testing.T) {
	p := Policy{InitialBackoff: 100 * time.Millisecond, MaxBackoff: time.Second, Multiplier: 2, Jitter: true}
	for i := 0; i < 1000; i++ {
		if d := p.Backoff(3); d < 0 || d >= 400*time.Millisecond {
			t.Fatalf("jittered backoff out of range: %v", d)
		}
	}
}

func TestDoRetriesUntilSuccess(t *testing.T) {
	calls := 0
	attempts, err := Do(context.Background(), Policy{MaxAttempts: 5, InitialBackoff: time.Millisecond},
		func(context.Context, int) error {
			calls++
			if calls < 3 {
				return errTemp
			}
			return nil
		})
	if err != nil || attempts != 3 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
}

func TestDoStopsOnNonRetryable(t *testing.T) {
	attempts, err := Do(context.Background(), Policy{
		MaxAttempts: 5, InitialBackoff: time.Millisecond,
		ShouldRetry: func(err error) bool { return !errors.Is(err, errFatal) },
	}, func(context.Context, int) error { return errFatal })
	if !errors.Is(err, errFatal) || attempts != 1 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
}

func TestDoGivesUpAfterMaxAttempts(t *testing.T) {
	attempts, err := Do(context.Background(), Policy{MaxAttempts: 3, InitialBackoff: time.Millisecond},
		func(context.Context, int) error { return errTemp })
	if !errors.Is(err, errTemp) || attempts != 3 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
}

func TestDoRespectsContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := Do(ctx, Policy{MaxAttempts: 10, InitialBackoff: time.Second},
		func(context.Context, int) error { return errTemp })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
}
