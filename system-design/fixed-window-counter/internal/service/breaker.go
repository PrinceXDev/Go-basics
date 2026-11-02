package service

import (
	"sync"
	"time"
)

type breakerState int

const (
	stateClosed   breakerState = iota // normal: calls go to the database
	stateOpen                         // database looks down: skip it, fail fast
	stateHalfOpen                     // cooldown over: let exactly one probe call through
)

func (s breakerState) String() string {
	return [...]string{"closed", "open", "half-open"}[s]
}

// breaker is a small circuit breaker. Without it, every request during an outage
// would wait for its full timeout (and retries), piling up goroutines and latency.
type breaker struct {
	mu        sync.Mutex
	threshold int
	cooldown  time.Duration
	now       func() time.Time
	onChange  func(from, to breakerState)

	state    breakerState
	failures int // consecutive failures while closed
	openedAt time.Time
	probing  bool // a half-open probe is in flight
}

func newBreaker(threshold int, cooldown time.Duration, now func() time.Time) *breaker {
	return &breaker{threshold: threshold, cooldown: cooldown, now: now}
}

// Allow reports whether a call may go to the database. Every true must be
// followed by exactly one of Success, Failure or Release.
func (b *breaker) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case stateOpen:
		if b.now().Sub(b.openedAt) < b.cooldown {
			return false
		}
		b.setState(stateHalfOpen)
		b.probing = true
		return true
	case stateHalfOpen:
		if b.probing {
			return false // one probe at a time
		}
		b.probing = true
		return true
	default:
		return true
	}
}

func (b *breaker) Success() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures = 0
	b.probing = false
	b.setState(stateClosed)
}

func (b *breaker) Failure() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.probing = false
	if b.state == stateHalfOpen {
		b.trip()
		return
	}
	b.failures++
	if b.failures >= b.threshold {
		b.trip()
	}
}

// Release ends an allowed call that says nothing about database health
// (the client hung up). Without it a cancelled probe would leave the breaker stuck half-open.
func (b *breaker) Release() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.probing = false
}

func (b *breaker) trip() {
	b.openedAt = b.now()
	b.failures = 0
	b.setState(stateOpen)
}

func (b *breaker) setState(to breakerState) {
	if b.state == to {
		return
	}
	from := b.state
	b.state = to
	if b.onChange != nil {
		b.onChange(from, to)
	}
}
