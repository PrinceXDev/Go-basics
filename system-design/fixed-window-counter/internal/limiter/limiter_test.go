package limiter

import (
	"testing"
	"time"
)

func at(h, m, s int) time.Time {
	return time.Date(2026, 1, 1, h, m, s, 0, time.UTC)
}

func TestWindowStart(t *testing.T) {
	tests := []struct {
		name   string
		now    time.Time
		window time.Duration
		want   time.Time
	}{
		{"middle of a minute", at(12, 0, 37), time.Minute, at(12, 0, 0)},
		{"exactly on the boundary belongs to the new window", at(12, 1, 0), time.Minute, at(12, 1, 0)},
		{"one second before the boundary", at(12, 0, 59), time.Minute, at(12, 0, 0)},
		{"30s window", at(12, 0, 45), 30 * time.Second, at(12, 0, 30)},
		{"hour window", at(12, 59, 59), time.Hour, at(12, 0, 0)},
		{"sub-second part is ignored", at(12, 0, 59).Add(900 * time.Millisecond), time.Minute, at(12, 0, 0)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := WindowStart(tt.now, tt.window); !got.Equal(tt.want) {
				t.Errorf("WindowStart = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestWindowStartSameWindowSameKey(t *testing.T) {
	// Two requests in the same window must map to the same row.
	a := WindowStart(at(12, 0, 1), time.Minute)
	b := WindowStart(at(12, 0, 58), time.Minute)
	if !a.Equal(b) {
		t.Errorf("same window produced different starts: %s vs %s", a, b)
	}
}

func TestDecide(t *testing.T) {
	const limit = 5
	start := at(12, 0, 0)
	now := at(12, 0, 40) // 20s left in the window

	tests := []struct {
		name          string
		count         int
		wantAllowed   bool
		wantRemaining int
	}{
		{"first request", 1, true, 4},
		{"last allowed request", 5, true, 0},
		{"first rejected request", 6, false, 0},
		{"far over the limit", 500, false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := Decide(tt.count, limit, start, time.Minute, now)
			if d.Allowed != tt.wantAllowed {
				t.Errorf("Allowed = %v, want %v", d.Allowed, tt.wantAllowed)
			}
			if d.Remaining != tt.wantRemaining {
				t.Errorf("Remaining = %d, want %d", d.Remaining, tt.wantRemaining)
			}
			if !d.ResetAt.Equal(at(12, 1, 0)) {
				t.Errorf("ResetAt = %s, want 12:01:00", d.ResetAt)
			}
			if tt.wantAllowed && d.RetryAfter != 0 {
				t.Errorf("RetryAfter = %s, want 0 when allowed", d.RetryAfter)
			}
			if !tt.wantAllowed && d.RetryAfter != 20*time.Second {
				t.Errorf("RetryAfter = %s, want 20s", d.RetryAfter)
			}
		})
	}
}

func TestRetryAfterSecondsRoundsUp(t *testing.T) {
	tests := []struct {
		retry time.Duration
		want  int
	}{
		{0, 0},
		{time.Millisecond, 1},
		{time.Second, 1},
		{1500 * time.Millisecond, 2},
		{20 * time.Second, 20},
	}
	for _, tt := range tests {
		d := Decision{RetryAfter: tt.retry}
		if got := d.RetryAfterSeconds(); got != tt.want {
			t.Errorf("RetryAfter %s -> %d, want %d", tt.retry, got, tt.want)
		}
	}
}

// Walks the 12:00:59 / 12:01:00 example from the design doc.
func TestBoundaryBurst(t *testing.T) {
	const limit = 2
	window := time.Minute

	count := 0
	allowedBefore := 0
	for i := 0; i < 4; i++ {
		now := at(12, 0, 59)
		count++
		if Decide(count, limit, WindowStart(now, window), window, now).Allowed {
			allowedBefore++
		}
	}

	count = 0
	allowedAfter := 0
	for i := 0; i < 4; i++ {
		now := at(12, 1, 0)
		count++
		if Decide(count, limit, WindowStart(now, window), window, now).Allowed {
			allowedAfter++
		}
	}

	if allowedBefore != 2 || allowedAfter != 2 {
		t.Errorf("allowed %d then %d, want 2 and 2", allowedBefore, allowedAfter)
	}
	// 4 requests allowed within 1 second although the limit is 2/min: the known weakness.
}
