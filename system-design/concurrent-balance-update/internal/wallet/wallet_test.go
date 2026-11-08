package wallet_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"example.com/concurrent-balance-update/internal/wallet"
)

const acct = int64(1)

func newRepo(t *testing.T) *wallet.Repo {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set; skipping PostgreSQL tests")
	}
	repo, err := wallet.Open(context.Background(), url)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(repo.Close)
	return repo
}

func balance(t *testing.T, r *wallet.Repo) wallet.Account {
	t.Helper()
	a, err := r.Get(context.Background(), acct)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// Strategies that must always be correct. Think widens the race window so a
// broken strategy would fail reliably.
func safeStrategies(r *wallet.Repo) []wallet.Strategy {
	p, th := r.Pool(), 2*time.Millisecond
	return []wallet.Strategy{
		wallet.Atomic{Pool: p},
		wallet.Pessimistic{Pool: p, Think: th},
		&wallet.Optimistic{Pool: p, Think: th, MaxAttempts: 1000},
		&wallet.Isolated{Pool: p, Level: pgx.RepeatableRead, Think: th, MaxAttempts: 1000},
		&wallet.Isolated{Pool: p, Level: pgx.Serializable, Think: th, MaxAttempts: 1000},
	}
}

func TestNaiveLosesUpdate(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	if err := repo.Reset(ctx, acct, 40); err != nil {
		t.Fatal(err)
	}
	s := wallet.Naive{Pool: repo.Pool(), Think: 100 * time.Millisecond}
	res := wallet.RunConcurrent(ctx, s, acct, []int64{+20, -20})

	if res.Succeeded != 2 {
		t.Fatalf("both calls should report success, got %+v", res)
	}
	// Correct answer is 40. The bug leaves 60 or 20 depending on who writes last.
	if got := balance(t, repo).Balance; got == 40 {
		t.Fatalf("expected a lost update, but balance is the correct 40")
	}
}

func TestTwoUsersAddAndDeduct(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	for _, s := range safeStrategies(repo) {
		t.Run(s.Name(), func(t *testing.T) {
			if err := repo.Reset(ctx, acct, 40); err != nil {
				t.Fatal(err)
			}
			res := wallet.RunConcurrent(ctx, s, acct, []int64{+20, -20})
			if res.Succeeded != 2 {
				t.Fatalf("want 2 successes, got %+v", res)
			}
			if got := balance(t, repo).Balance; got != 40 {
				t.Fatalf("balance = %d, want 40", got)
			}
		})
	}
}

func TestManyDepositsAndWithdrawals(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	for _, s := range safeStrategies(repo) {
		t.Run(s.Name(), func(t *testing.T) {
			if err := repo.Reset(ctx, acct, 1000); err != nil {
				t.Fatal(err)
			}
			deltas := append(wallet.Repeat(+10, 30), wallet.Repeat(-10, 30)...)
			res := wallet.RunConcurrent(ctx, s, acct, deltas)
			if res.Succeeded != 60 {
				t.Fatalf("want 60 successes, got %+v", res)
			}
			if got := balance(t, repo).Balance; got != 1000 {
				t.Fatalf("balance = %d, want 1000", got)
			}
		})
	}
}

func TestInsufficientFundsUnderContention(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	for _, s := range safeStrategies(repo) {
		t.Run(s.Name(), func(t *testing.T) {
			if err := repo.Reset(ctx, acct, 50); err != nil {
				t.Fatal(err)
			}
			// 10 x 20 = 200 requested, only 50 available: exactly 2 can succeed.
			res := wallet.RunConcurrent(ctx, s, acct, wallet.Repeat(-20, 10))
			if res.Succeeded != 2 || res.Insufficient != 8 || res.Failed != 0 {
				t.Fatalf("want 2 ok / 8 insufficient, got %+v", res)
			}
			if got := balance(t, repo).Balance; got != 10 {
				t.Fatalf("balance = %d, want 10", got)
			}
		})
	}
}

func TestNaiveOverspends(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	if err := repo.Reset(ctx, acct, 50); err != nil {
		t.Fatal(err)
	}
	s := wallet.Naive{Pool: repo.Pool(), Think: 50 * time.Millisecond}
	res := wallet.RunConcurrent(ctx, s, acct, wallet.Repeat(-20, 10))
	// All 10 read 50 and passed the check, so "200 rupees" left a 50 rupee account.
	if res.Succeeded <= 2 {
		t.Fatalf("expected overspending, got %+v", res)
	}
}

func TestOptimisticVersionAndConflicts(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	if err := repo.Reset(ctx, acct, 0); err != nil {
		t.Fatal(err)
	}
	s := &wallet.Optimistic{Pool: repo.Pool(), Think: 5 * time.Millisecond, MaxAttempts: 1000}
	res := wallet.RunConcurrent(ctx, s, acct, wallet.Repeat(+1, 10))
	a := balance(t, repo)
	if res.Succeeded != 10 || a.Balance != 10 {
		t.Fatalf("got %+v balance=%d", res, a.Balance)
	}
	if a.Version != 10 {
		t.Fatalf("version = %d, want 10 (one bump per successful write)", a.Version)
	}
	if s.Retries() == 0 {
		t.Fatal("expected conflicts with 10 overlapping writers")
	}
}

func TestOptimisticGivesUp(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	if err := repo.Reset(ctx, acct, 0); err != nil {
		t.Fatal(err)
	}
	s := &wallet.Optimistic{Pool: repo.Pool(), Think: 50 * time.Millisecond, MaxAttempts: 1}
	res := wallet.RunConcurrent(ctx, s, acct, wallet.Repeat(+1, 5))
	// One attempt each: one wins, the other four see a stale version.
	if res.Succeeded != 1 || res.Failed != 4 {
		t.Fatalf("want 1 ok / 4 conflict, got %+v", res)
	}
	if got := balance(t, repo).Balance; got != 1 {
		t.Fatalf("balance = %d, want 1", got)
	}
}

func TestIsolationRetriesOnSerializationFailure(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	if err := repo.Reset(ctx, acct, 40); err != nil {
		t.Fatal(err)
	}
	s := &wallet.Isolated{Pool: repo.Pool(), Level: pgx.RepeatableRead, Think: 50 * time.Millisecond, MaxAttempts: 50}
	wallet.RunConcurrent(ctx, s, acct, []int64{+20, -20})
	if s.Retries() == 0 {
		t.Fatal("PostgreSQL should have aborted one transaction with 40001")
	}
	if got := balance(t, repo).Balance; got != 40 {
		t.Fatalf("balance = %d, want 40", got)
	}
}

func TestIsolationWithoutRetryReportsConflict(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	if err := repo.Reset(ctx, acct, 40); err != nil {
		t.Fatal(err)
	}
	s := &wallet.Isolated{Pool: repo.Pool(), Level: pgx.RepeatableRead, Think: 50 * time.Millisecond, MaxAttempts: 1}
	res := wallet.RunConcurrent(ctx, s, acct, []int64{+20, -20})
	if res.Succeeded != 1 || res.Failed != 1 {
		t.Fatalf("want 1 ok / 1 failed without retry, got %+v", res)
	}
}

func TestUnknownAccount(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	for _, s := range safeStrategies(repo) {
		t.Run(s.Name(), func(t *testing.T) {
			err := s.Apply(ctx, 999999, 10)
			if !errors.Is(err, wallet.ErrAccountNotFound) {
				t.Fatalf("err = %v, want ErrAccountNotFound", err)
			}
		})
	}
}
