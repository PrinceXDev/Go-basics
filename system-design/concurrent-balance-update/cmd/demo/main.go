package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/jackc/pgx/v5"

	"example.com/concurrent-balance-update/internal/wallet"
)

const acct = int64(1)

func main() {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		log.Fatal("set DATABASE_URL, e.g. postgres://wallet:change_me@localhost:5433/wallet?sslmode=disable")
	}
	ctx := context.Background()
	repo, err := wallet.Open(ctx, url)
	if err != nil {
		log.Fatal(err)
	}
	defer repo.Close()

	p, think := repo.Pool(), 20*time.Millisecond
	strategies := []wallet.Strategy{
		wallet.Naive{Pool: p, Think: think},
		wallet.Atomic{Pool: p},
		wallet.Pessimistic{Pool: p, Think: think},
		&wallet.Optimistic{Pool: p, Think: think, MaxAttempts: 1000},
		&wallet.Isolated{Pool: p, Level: pgx.RepeatableRead, Think: think, MaxAttempts: 1000},
		&wallet.Isolated{Pool: p, Level: pgx.Serializable, Think: think, MaxAttempts: 1000},
	}

	fmt.Println("Scenario 1: balance 40, User A deposits +20 and User B withdraws -20 at the same time")
	fmt.Println("            correct final balance = 40")
	for _, s := range strategies {
		run(ctx, repo, s, 40, []int64{+20, -20}, 40)
	}

	fmt.Println("\nScenario 2: balance 1000, 30 deposits of +10 and 30 withdrawals of -10")
	fmt.Println("            correct final balance = 1000")
	mixed := append(wallet.Repeat(+10, 30), wallet.Repeat(-10, 30)...)
	for _, s := range strategies {
		run(ctx, repo, s, 1000, mixed, 1000)
	}

	fmt.Println("\nScenario 3: balance 50, ten withdrawals of -20 (only 2 can be paid)")
	fmt.Println("            correct: 2 succeed, 8 insufficient, final balance = 10")
	for _, s := range strategies {
		run(ctx, repo, s, 50, wallet.Repeat(-20, 10), 10)
	}
}

func run(ctx context.Context, repo *wallet.Repo, s wallet.Strategy, start int64, deltas []int64, want int64) {
	if err := repo.Reset(ctx, acct, start); err != nil {
		log.Fatal(err)
	}
	before := s.Retries()
	res := wallet.RunConcurrent(ctx, s, acct, deltas)
	a, err := repo.Get(ctx, acct)
	if err != nil {
		log.Fatal(err)
	}
	verdict := "OK"
	if a.Balance != want || res.Failed > 0 {
		verdict = "WRONG"
	}
	fmt.Printf("  %-28s ok=%-3d insufficient=%-3d failed=%-3d retries=%-4d final=%-5d %5s  (%v)\n",
		s.Name(), res.Succeeded, res.Insufficient, res.Failed, s.Retries()-before,
		a.Balance, verdict, res.Elapsed.Round(time.Millisecond))
}
