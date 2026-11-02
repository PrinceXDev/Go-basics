// Package store talks to Postgres. It only counts; deciding allowed/denied is the limiter's job.
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Result is what one increment returns. Now comes from the database clock,
// so every app server agrees on which window we are in.
type Result struct {
	Count       int
	WindowStart time.Time
	Now         time.Time
}

// One statement does everything, so there is no read-then-write gap for another request to slip into:
//   - first request in a window: the INSERT creates the row with count 1
//   - any later request (or a concurrent one that lost the race): ON CONFLICT turns it into an UPDATE,
//     and Postgres locks the row so concurrent updates run one after another
//
// Window start is computed in SQL with the same formula as limiter.WindowStart.
const incrementSQL = `
INSERT INTO rate_limit_counters (scope_type, scope_key, window_start, request_count)
VALUES ($1, $2, to_timestamp(floor(extract(epoch FROM now()) / $3::float8) * $3::float8), 1)
ON CONFLICT (scope_type, scope_key, window_start)
DO UPDATE SET request_count = rate_limit_counters.request_count + 1
RETURNING request_count, window_start, now()`

// Increment counts one request for (scopeType, scopeKey) in the current window.
func (s *Store) Increment(ctx context.Context, scopeType, scopeKey string, window time.Duration) (Result, error) {
	var r Result
	err := s.pool.QueryRow(ctx, incrementSQL, scopeType, scopeKey, window.Seconds()).
		Scan(&r.Count, &r.WindowStart, &r.Now)
	if err != nil {
		return Result{}, fmt.Errorf("increment %s/%s: %w", scopeType, scopeKey, err)
	}
	return r, nil
}
