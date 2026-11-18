package breaker

import (
	"errors"
	"sync"
	"testing"
	"time"
)

var errDown = errors.New("gateway down")
var errDeclined = errors.New("card declined")

// fakeClock lets tests move time forward instead of sleeping.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) Add(d time.Duration) { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }

func newTestBreaker(clock *fakeClock, onChange func(from, to State)) *Breaker {
	cfg := DefaultConfig("test")
	cfg.SlidingWindowSize = 4
	cfg.MinimumNumberOfCalls = 4
	cfg.FailureRateThreshold = 50
	cfg.WaitDurationInOpenState = 10 * time.Second
	cfg.PermittedCallsInHalfOpen = 2
	cfg.Now = clock.Now
	cfg.IsFailure = func(err error) bool { return err != nil && !errors.Is(err, errDeclined) }
	if onChange != nil {
		cfg.OnStateChange = func(_ string, from, to State) { onChange(from, to) }
	}
	return New(cfg)
}

func fail() error    { return errDown }
func succeed() error { return nil }

func TestStaysClosedBelowMinimumCalls(t *testing.T) {
	b := newTestBreaker(&fakeClock{now: time.Unix(0, 0)}, nil)
	for i := 0; i < 3; i++ {
		_ = b.Execute(fail) // 3/3 failed, but minimum is 4
	}
	if b.State() != Closed {
		t.Fatalf("want CLOSED, got %v", b.State())
	}
}

func TestOpensAtThresholdAndRejects(t *testing.T) {
	b := newTestBreaker(&fakeClock{now: time.Unix(0, 0)}, nil)
	_ = b.Execute(succeed)
	_ = b.Execute(succeed)
	_ = b.Execute(fail)
	_ = b.Execute(fail) // 2/4 = 50% -> trips
	if b.State() != Open {
		t.Fatalf("want OPEN, got %v", b.State())
	}
	called := false
	err := b.Execute(func() error { called = true; return nil })
	if !errors.Is(err, ErrOpen) || called {
		t.Fatalf("open breaker must reject without calling fn (err=%v called=%v)", err, called)
	}
	if b.Snapshot().NotPermitted != 1 {
		t.Fatalf("want 1 not-permitted call")
	}
}

func TestBusinessErrorsDoNotTrip(t *testing.T) {
	b := newTestBreaker(&fakeClock{now: time.Unix(0, 0)}, nil)
	for i := 0; i < 10; i++ {
		_ = b.Execute(func() error { return errDeclined })
	}
	if b.State() != Closed {
		t.Fatalf("declines prove the gateway is up; want CLOSED, got %v", b.State())
	}
}

func TestHalfOpenRecoversToClosed(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	var transitions []string
	b := newTestBreaker(clock, func(from, to State) { transitions = append(transitions, from.String()+"->"+to.String()) })
	for i := 0; i < 4; i++ {
		_ = b.Execute(fail)
	}
	clock.Add(10 * time.Second)
	_ = b.Execute(succeed) // lazily moves OPEN -> HALF_OPEN, trial 1
	if b.State() != HalfOpen {
		t.Fatalf("want HALF_OPEN, got %v", b.State())
	}
	_ = b.Execute(succeed) // trial 2 -> 0% failures -> CLOSED
	if b.State() != Closed {
		t.Fatalf("want CLOSED, got %v", b.State())
	}
	want := []string{"CLOSED->OPEN", "OPEN->HALF_OPEN", "HALF_OPEN->CLOSED"}
	if len(transitions) != len(want) {
		t.Fatalf("transitions %v, want %v", transitions, want)
	}
	for i := range want {
		if transitions[i] != want[i] {
			t.Fatalf("transitions %v, want %v", transitions, want)
		}
	}
}

func TestHalfOpenFailureReopens(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	b := newTestBreaker(clock, nil)
	for i := 0; i < 4; i++ {
		_ = b.Execute(fail)
	}
	clock.Add(10 * time.Second)
	_ = b.Execute(fail)
	_ = b.Execute(fail)
	if b.State() != Open {
		t.Fatalf("want OPEN again, got %v", b.State())
	}
}

func TestHalfOpenLimitsTrialCalls(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	b := newTestBreaker(clock, nil)
	for i := 0; i < 4; i++ {
		_ = b.Execute(fail)
	}
	clock.Add(10 * time.Second)

	// Two trial calls in flight at once; the third must be rejected.
	release := make(chan struct{})
	var wg sync.WaitGroup
	started := make(chan struct{}, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = b.Execute(func() error { started <- struct{}{}; <-release; return nil })
		}()
	}
	<-started
	<-started
	if err := b.Execute(succeed); !errors.Is(err, ErrOpen) {
		t.Fatalf("third half-open call should be rejected, got %v", err)
	}
	close(release)
	wg.Wait()
	if b.State() != Closed {
		t.Fatalf("want CLOSED, got %v", b.State())
	}
}

func TestSlowCallsTrip(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	cfg := DefaultConfig("slow")
	cfg.SlidingWindowSize, cfg.MinimumNumberOfCalls = 4, 4
	cfg.SlowCallDurationThreshold = time.Second
	cfg.SlowCallRateThreshold = 50
	cfg.Now = clock.Now
	b := New(cfg)
	slow := func() error { clock.Add(2 * time.Second); return nil } // succeeds, but slowly
	_ = b.Execute(succeed)
	_ = b.Execute(succeed)
	_ = b.Execute(slow)
	_ = b.Execute(slow)
	if b.State() != Open {
		t.Fatalf("50%% slow calls should trip, got %v", b.State())
	}
}
