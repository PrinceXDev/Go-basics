package store

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// newTestStore needs a real Postgres: set TEST_DATABASE_URL to a database whose name contains "test".
func newTestStore(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	// The tests wipe the table, so refuse to run against anything that isn't clearly a test database.
	var db string
	if err := pool.QueryRow(ctx, "SELECT current_database()").Scan(&db); err != nil {
		t.Fatalf("current_database: %v", err)
	}
	if !strings.Contains(db, "test") {
		t.Fatalf("refusing to truncate database %q: name must contain \"test\"", db)
	}
	if _, err := pool.Exec(ctx, "TRUNCATE rate_limit_counters"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return New(pool)
}

func TestIncrementCountsUp(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for want := 1; want <= 3; want++ {
		r, err := s.Increment(ctx, "tenant", "acme", time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if r.Count != want {
			t.Errorf("count = %d, want %d", r.Count, want)
		}
	}
}

func TestIncrementKeepsScopesSeparate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.Increment(ctx, "tenant", "acme", time.Hour)
	s.Increment(ctx, "tenant", "acme", time.Hour)

	for _, c := range []struct{ typ, key string }{
		{"tenant", "globex"}, // other tenant
		{"user", "acme"},     // same key, other scope type
	} {
		r, err := s.Increment(ctx, c.typ, c.key, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if r.Count != 1 {
			t.Errorf("%s/%s count = %d, want 1", c.typ, c.key, r.Count)
		}
	}
}

func TestIncrementResetsInNewWindow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	first, err := s.Increment(ctx, "user", "u1", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	second, err := s.Increment(ctx, "user", "u1", time.Second)
	if err != nil {
		t.Fatal(err)
	}

	if second.Count != 1 {
		t.Errorf("count after window passed = %d, want 1", second.Count)
	}
	if !second.WindowStart.After(first.WindowStart) {
		t.Errorf("window did not advance: %s then %s", first.WindowStart, second.WindowStart)
	}
}

// Fifty simultaneous requests must each get a different count 1..50: no lost updates, no duplicates.
func TestIncrementIsAtomicUnderConcurrency(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	const n = 50
	counts := make(chan int, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := s.Increment(ctx, "tenant", "busy", time.Hour)
			if err != nil {
				t.Error(err)
				return
			}
			counts <- r.Count
		}()
	}
	wg.Wait()
	close(counts)

	seen := make(map[int]bool)
	for c := range counts {
		if seen[c] {
			t.Errorf("count %d handed out twice", c)
		}
		seen[c] = true
	}
	for want := 1; want <= n; want++ {
		if !seen[want] {
			t.Errorf("count %d never handed out", want)
		}
	}
}
