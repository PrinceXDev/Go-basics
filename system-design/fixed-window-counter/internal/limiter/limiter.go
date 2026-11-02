// Package limiter holds the pure fixed-window logic: no database, no HTTP,
// no clock reads. Everything is a function of its inputs, which makes it trivial to test.
package limiter

import "time"

// WindowStart returns the start of the fixed window that contains now.
//
// It uses Unix seconds instead of time.Truncate: Truncate counts from year 1,
// so for odd window sizes its buckets would not line up with the SQL version
// (floor(epoch / window) * window) that the store will use.
func WindowStart(now time.Time, window time.Duration) time.Time {
	size := int64(window / time.Second)
	secs := now.Unix()
	return time.Unix(secs-secs%size, 0).UTC()
}

// Decision is the outcome for one scope. It carries everything the HTTP layer
// needs for the 429 response and the X-RateLimit-* headers.
type Decision struct {
	Allowed    bool
	Limit      int
	Remaining  int
	ResetAt    time.Time     // when the current window ends and the counter starts over
	RetryAfter time.Duration // 0 when allowed
	// Degraded means the counter store was unreachable and we let the request through
	// without counting it (fail-open). Limit/Remaining/ResetAt are not meaningful then.
	Degraded bool
}

// Decide turns "this request is number `count` in the window" into a Decision.
// count must already include the current request.
func Decide(count, limit int, windowStart time.Time, window time.Duration, now time.Time) Decision {
	resetAt := windowStart.Add(window)
	d := Decision{
		Allowed:   count <= limit,
		Limit:     limit,
		Remaining: max(limit-count, 0),
		ResetAt:   resetAt,
	}
	if !d.Allowed {
		d.RetryAfter = max(resetAt.Sub(now), 0)
	}
	return d
}

// RetryAfterSeconds is the value for the Retry-After header, which is a whole
// number of seconds. Rounding up means a client that waits that long never
// retries too early.
func (d Decision) RetryAfterSeconds() int {
	if d.RetryAfter <= 0 {
		return 0
	}
	return int((d.RetryAfter + time.Second - 1) / time.Second)
}
