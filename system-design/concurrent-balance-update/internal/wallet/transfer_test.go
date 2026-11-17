package wallet_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"example.com/concurrent-balance-update/internal/wallet"
)

const (
	accA = int64(101)
	accB = int64(102)
)

func resetPair(t *testing.T, r *wallet.Repo, a, b int64) {
	t.Helper()
	ctx := context.Background()
	if err := r.Reset(ctx, accA, a); err != nil {
		t.Fatal(err)
	}
	if err := r.Reset(ctx, accB, b); err != nil {
		t.Fatal(err)
	}
}

func bal(t *testing.T, r *wallet.Repo, id int64) int64 {
	t.Helper()
	a, err := r.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return a.Balance
}

func TestOppositeTransfersDeadlock(t *testing.T) {
	repo := newRepo(t)
	resetPair(t, repo, 100, 100)
	tr := &wallet.Transfer{Pool: repo.Pool(), Think: 200 * time.Millisecond, OrderLocks: false}
	res := wallet.RunTransfers(context.Background(), tr, []wallet.Move{
		{From: accA, To: accB, Amount: 10},
		{From: accB, To: accA, Amount: 10},
	})
	// Each holds one lock and wants the other's: PostgreSQL kills exactly one.
	if res.Deadlocked != 1 || res.Succeeded != 1 {
		t.Fatalf("want 1 deadlock + 1 success, got %+v", res)
	}
}

func TestOrderedLocksAvoidDeadlock(t *testing.T) {
	repo := newRepo(t)
	resetPair(t, repo, 100, 100)
	tr := &wallet.Transfer{Pool: repo.Pool(), Think: 50 * time.Millisecond, OrderLocks: true}
	res := wallet.RunTransfers(context.Background(), tr, []wallet.Move{
		{From: accA, To: accB, Amount: 10},
		{From: accB, To: accA, Amount: 10},
	})
	if res.Succeeded != 2 || res.Deadlocked != 0 {
		t.Fatalf("want 2 successes, got %+v", res)
	}
	if a, b := bal(t, repo, accA), bal(t, repo, accB); a != 100 || b != 100 {
		t.Fatalf("balances %d/%d, want 100/100", a, b)
	}
}

func TestDeadlockRetrySucceeds(t *testing.T) {
	repo := newRepo(t)
	resetPair(t, repo, 100, 100)
	tr := &wallet.Transfer{Pool: repo.Pool(), Think: 200 * time.Millisecond, MaxAttempts: 10}
	res := wallet.RunTransfers(context.Background(), tr, []wallet.Move{
		{From: accA, To: accB, Amount: 30},
		{From: accB, To: accA, Amount: 10},
	})
	if res.Succeeded != 2 || tr.Retries() == 0 {
		t.Fatalf("want 2 successes after >=1 retry, got %+v retries=%d", res, tr.Retries())
	}
	// A: 100-30+10, B: 100+30-10
	if a, b := bal(t, repo, accA), bal(t, repo, accB); a != 80 || b != 120 {
		t.Fatalf("balances %d/%d, want 80/120", a, b)
	}
}

func TestManyTransfersConserveMoney(t *testing.T) {
	repo := newRepo(t)
	resetPair(t, repo, 500, 500)
	tr := &wallet.Transfer{Pool: repo.Pool(), Think: time.Millisecond, OrderLocks: true}
	var moves []wallet.Move
	for i := 0; i < 20; i++ {
		moves = append(moves,
			wallet.Move{From: accA, To: accB, Amount: 7},
			wallet.Move{From: accB, To: accA, Amount: 3})
	}
	res := wallet.RunTransfers(context.Background(), tr, moves)
	if res.Succeeded != 40 {
		t.Fatalf("want 40 successes, got %+v", res)
	}
	total, err := repo.Total(context.Background(), accA, accB)
	if err != nil || total != 1000 {
		t.Fatalf("total = %d (err %v), want 1000", total, err)
	}
	// A: 500 - 20*7 + 20*3 = 420
	if a := bal(t, repo, accA); a != 420 {
		t.Fatalf("A = %d, want 420", a)
	}
}

func TestTransferInsufficientFunds(t *testing.T) {
	repo := newRepo(t)
	resetPair(t, repo, 50, 0)
	tr := &wallet.Transfer{Pool: repo.Pool(), OrderLocks: true}
	res := wallet.RunTransfers(context.Background(), tr, wallet.Repeat3(accA, accB, 20, 10))
	if res.Succeeded != 2 || res.Insufficient != 8 {
		t.Fatalf("want 2 ok / 8 insufficient, got %+v", res)
	}
	if a, b := bal(t, repo, accA), bal(t, repo, accB); a != 10 || b != 40 {
		t.Fatalf("balances %d/%d, want 10/40", a, b)
	}
}

func TestTransferSameAccount(t *testing.T) {
	repo := newRepo(t)
	tr := &wallet.Transfer{Pool: repo.Pool(), OrderLocks: true}
	if err := tr.Apply(context.Background(), accA, accA, 5); !errors.Is(err, wallet.ErrSameAccount) {
		t.Fatalf("err = %v, want ErrSameAccount", err)
	}
}
