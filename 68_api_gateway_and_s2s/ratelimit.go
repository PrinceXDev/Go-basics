package main

// TOKEN BUCKET: each customer has a bucket holding up to `burst` tokens,
// refilled at `rate` per second. Each request takes one token; an empty
// bucket means 429. Bursts are allowed (open the app -> 5 calls at once),
// sustained abuse is not.
//
// This limiter lives in ONE gateway's memory. With 6 gateway pods each
// allowing 20 rps, a customer really gets 120 rps. Production gateways keep
// the counters in Redis (or use Kong/Envoy's global rate-limit service) so
// the limit holds across all pods.

import (
	"sync"

	"golang.org/x/time/rate"
)

type keyedLimiter struct {
	mu       sync.Mutex
	limiters map[string]*rate.Limiter
	r        rate.Limit
	burst    int
}

func newKeyedLimiter(perSec float64, burst int) *keyedLimiter {
	return &keyedLimiter{limiters: map[string]*rate.Limiter{}, r: rate.Limit(perSec), burst: burst}
}

func (k *keyedLimiter) Allow(key string) bool {
	k.mu.Lock()
	l, ok := k.limiters[key]
	if !ok {
		// Real code also evicts idle keys, or this map grows forever.
		l = rate.NewLimiter(k.r, k.burst)
		k.limiters[key] = l
	}
	k.mu.Unlock()
	return l.Allow()
}
