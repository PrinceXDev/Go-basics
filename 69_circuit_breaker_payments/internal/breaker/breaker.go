// Package breaker is a small, hand-written circuit breaker modelled on
// Resilience4j's count-based CircuitBreaker, so every config field below maps
// 1:1 to a Resilience4j property (see docs/01-concepts.md).
package breaker

import (
	"errors"
	"sync"
	"time"
)

type State int

const (
	Closed   State = iota // normal: calls flow, outcomes are recorded
	Open                  // tripped: calls are rejected instantly
	HalfOpen              // probing: a few trial calls decide Closed vs Open
)

func (s State) String() string {
	switch s {
	case Closed:
		return "CLOSED"
	case Open:
		return "OPEN"
	case HalfOpen:
		return "HALF_OPEN"
	}
	return "UNKNOWN"
}

// ErrOpen is returned without calling the protected function. Callers should
// treat it as "fail fast, go to fallback", never as a reason to retry.
var ErrOpen = errors.New("circuit breaker is open")

type Config struct {
	Name string

	// Closed state: judge health over the last N calls, but only once at
	// least MinimumNumberOfCalls have been seen (avoids tripping on 1/1 = 100%).
	SlidingWindowSize    int
	MinimumNumberOfCalls int
	FailureRateThreshold float64 // percent, e.g. 50

	// A call that succeeds but takes too long is also a warning sign.
	SlowCallDurationThreshold time.Duration
	SlowCallRateThreshold     float64 // percent

	// Open state: how long to reject before allowing trial calls.
	WaitDurationInOpenState time.Duration

	// Half-open state: number of trial calls that decide the next state.
	PermittedCallsInHalfOpen int

	// IsFailure decides which errors count against the breaker. Business
	// errors (e.g. card declined) prove the gateway is healthy, so they must
	// NOT count — otherwise a wave of declines would trip the breaker.
	IsFailure func(error) bool

	OnStateChange func(name string, from, to State)

	Now func() time.Time // injectable clock for tests
}

func DefaultConfig(name string) Config {
	return Config{
		Name:                      name,
		SlidingWindowSize:         10,
		MinimumNumberOfCalls:      5,
		FailureRateThreshold:      50,
		SlowCallDurationThreshold: 2 * time.Second,
		SlowCallRateThreshold:     100,
		WaitDurationInOpenState:   10 * time.Second,
		PermittedCallsInHalfOpen:  3,
	}
}

type outcome struct{ failed, slow bool }

type Breaker struct {
	cfg Config

	mu         sync.Mutex
	state      State
	generation uint64 // bumps on every transition; stale results are ignored
	openedAt   time.Time

	// Closed: ring buffer of the last SlidingWindowSize outcomes.
	window      []outcome
	next        int
	filled      int
	failedCount int
	slowCount   int

	// Half-open bookkeeping.
	halfStarted int
	halfDone    int
	halfFailed  int
	halfSlow    int

	notPermitted int64 // calls rejected while open (lifetime counter)

	pending []func() // state-change callbacks, fired after mu is released
}

func New(cfg Config) *Breaker {
	if cfg.SlidingWindowSize <= 0 {
		cfg.SlidingWindowSize = 10
	}
	if cfg.MinimumNumberOfCalls <= 0 || cfg.MinimumNumberOfCalls > cfg.SlidingWindowSize {
		cfg.MinimumNumberOfCalls = cfg.SlidingWindowSize
	}
	if cfg.PermittedCallsInHalfOpen <= 0 {
		cfg.PermittedCallsInHalfOpen = 1
	}
	if cfg.SlowCallRateThreshold <= 0 {
		cfg.SlowCallRateThreshold = 100
	}
	if cfg.IsFailure == nil {
		cfg.IsFailure = func(err error) bool { return err != nil }
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Breaker{cfg: cfg, window: make([]outcome, cfg.SlidingWindowSize)}
}

// Execute runs fn if the breaker allows it and records the outcome.
func (b *Breaker) Execute(fn func() error) error {
	gen, err := b.acquire()
	if err != nil {
		return err
	}
	start := b.cfg.Now()
	callErr := fn()
	b.record(gen, callErr, b.cfg.Now().Sub(start))
	return callErr
}

// Call is the generic form (Go methods can't have type parameters) — the
// equivalent of Resilience4j's circuitBreaker.executeSupplier(() -> ...).
func Call[T any](b *Breaker, fn func() (T, error)) (T, error) {
	var out T
	err := b.Execute(func() error {
		var err error
		out, err = fn()
		return err
	})
	return out, err
}

func (b *Breaker) acquire() (uint64, error) {
	b.mu.Lock()
	defer b.unlockAndNotify()

	switch b.state {
	case Open:
		// The Open -> HalfOpen move is lazy: it happens on the first call
		// after the wait, so no background timer goroutine is needed.
		if b.cfg.Now().Sub(b.openedAt) < b.cfg.WaitDurationInOpenState {
			b.notPermitted++
			return 0, ErrOpen
		}
		b.transition(HalfOpen)
		fallthrough
	case HalfOpen:
		if b.halfStarted >= b.cfg.PermittedCallsInHalfOpen {
			b.notPermitted++
			return 0, ErrOpen
		}
		b.halfStarted++
	}
	return b.generation, nil
}

func (b *Breaker) record(gen uint64, err error, took time.Duration) {
	b.mu.Lock()
	defer b.unlockAndNotify()

	// A call that started before a transition must not influence the new
	// state (e.g. a slow call from CLOSED finishing while we're HALF_OPEN).
	if gen != b.generation {
		return
	}
	o := outcome{
		failed: err != nil && b.cfg.IsFailure(err),
		slow:   took >= b.cfg.SlowCallDurationThreshold && b.cfg.SlowCallDurationThreshold > 0,
	}

	switch b.state {
	case Closed:
		b.push(o)
		if b.filled >= b.cfg.MinimumNumberOfCalls && b.overThreshold(b.failedCount, b.slowCount, b.filled) {
			b.transition(Open)
		}
	case HalfOpen:
		b.halfDone++
		if o.failed {
			b.halfFailed++
		}
		if o.slow {
			b.halfSlow++
		}
		if b.halfDone >= b.cfg.PermittedCallsInHalfOpen {
			if b.overThreshold(b.halfFailed, b.halfSlow, b.halfDone) {
				b.transition(Open)
			} else {
				b.transition(Closed)
			}
		}
	}
}

func (b *Breaker) overThreshold(failed, slow, total int) bool {
	if total == 0 {
		return false
	}
	failRate := 100 * float64(failed) / float64(total)
	slowRate := 100 * float64(slow) / float64(total)
	return failRate >= b.cfg.FailureRateThreshold || slowRate >= b.cfg.SlowCallRateThreshold
}

func (b *Breaker) push(o outcome) {
	if b.filled == len(b.window) { // evict the oldest outcome
		old := b.window[b.next]
		if old.failed {
			b.failedCount--
		}
		if old.slow {
			b.slowCount--
		}
	} else {
		b.filled++
	}
	b.window[b.next] = o
	b.next = (b.next + 1) % len(b.window)
	if o.failed {
		b.failedCount++
	}
	if o.slow {
		b.slowCount++
	}
}

// transition must be called with mu held.
func (b *Breaker) transition(to State) {
	from := b.state
	if from == to {
		return
	}
	b.state = to
	b.generation++
	b.halfStarted, b.halfDone, b.halfFailed, b.halfSlow = 0, 0, 0, 0
	if to == Open {
		b.openedAt = b.cfg.Now()
	}
	if to == Closed { // start with a clean window after recovery
		b.window = make([]outcome, len(b.window))
		b.next, b.filled, b.failedCount, b.slowCount = 0, 0, 0, 0
	}
	if cb := b.cfg.OnStateChange; cb != nil {
		b.pending = append(b.pending, func() { cb(b.cfg.Name, from, to) })
	}
}

// unlockAndNotify releases mu, then runs queued callbacks in order. Running
// them outside the lock lets a callback safely call Snapshot().
func (b *Breaker) unlockAndNotify() {
	cbs := b.pending
	b.pending = nil
	b.mu.Unlock()
	for _, cb := range cbs {
		cb()
	}
}

// Snapshot is a read-only view for metrics and the dashboard.
type Snapshot struct {
	Name             string  `json:"name"`
	State            string  `json:"state"`
	FailureRate      float64 `json:"failureRate"`
	SlowCallRate     float64 `json:"slowCallRate"`
	BufferedCalls    int     `json:"bufferedCalls"`
	FailedCalls      int     `json:"failedCalls"`
	SlowCalls        int     `json:"slowCalls"`
	NotPermitted     int64   `json:"notPermittedCalls"`
	OpenRemainingMs  int64   `json:"openRemainingMs"`
	HalfOpenTrials   int     `json:"halfOpenTrials"`
	HalfOpenFinished int     `json:"halfOpenFinished"`
	Config           struct {
		SlidingWindowSize        int     `json:"slidingWindowSize"`
		MinimumNumberOfCalls     int     `json:"minimumNumberOfCalls"`
		FailureRateThreshold     float64 `json:"failureRateThreshold"`
		SlowCallDurationMs       int64   `json:"slowCallDurationThresholdMs"`
		WaitDurationInOpenMs     int64   `json:"waitDurationInOpenStateMs"`
		PermittedCallsInHalfOpen int     `json:"permittedCallsInHalfOpen"`
	} `json:"config"`
}

func (b *Breaker) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

func (b *Breaker) Snapshot() Snapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := Snapshot{
		Name:             b.cfg.Name,
		State:            b.state.String(),
		BufferedCalls:    b.filled,
		FailedCalls:      b.failedCount,
		SlowCalls:        b.slowCount,
		NotPermitted:     b.notPermitted,
		HalfOpenTrials:   b.halfStarted,
		HalfOpenFinished: b.halfDone,
	}
	if b.state == HalfOpen { // only the trial calls matter in this state
		s.BufferedCalls, s.FailedCalls, s.SlowCalls = b.halfDone, b.halfFailed, b.halfSlow
	}
	if s.BufferedCalls > 0 {
		s.FailureRate = 100 * float64(s.FailedCalls) / float64(s.BufferedCalls)
		s.SlowCallRate = 100 * float64(s.SlowCalls) / float64(s.BufferedCalls)
	}
	if b.state == Open {
		if rem := b.cfg.WaitDurationInOpenState - b.cfg.Now().Sub(b.openedAt); rem > 0 {
			s.OpenRemainingMs = rem.Milliseconds()
		}
	}
	s.Config.SlidingWindowSize = b.cfg.SlidingWindowSize
	s.Config.MinimumNumberOfCalls = b.cfg.MinimumNumberOfCalls
	s.Config.FailureRateThreshold = b.cfg.FailureRateThreshold
	s.Config.SlowCallDurationMs = b.cfg.SlowCallDurationThreshold.Milliseconds()
	s.Config.WaitDurationInOpenMs = b.cfg.WaitDurationInOpenState.Milliseconds()
	s.Config.PermittedCallsInHalfOpen = b.cfg.PermittedCallsInHalfOpen
	return s
}
