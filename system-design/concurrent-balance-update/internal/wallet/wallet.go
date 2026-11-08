package wallet

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"example.com/concurrent-balance-update/migrations"
)

var (
	ErrInsufficientFunds = errors.New("insufficient funds")
	ErrAccountNotFound   = errors.New("account not found")
	// ErrConflict means an optimistic update kept losing the race and gave up.
	ErrConflict = errors.New("too many version conflicts")
)

type Account struct {
	ID      int64
	Balance int64
	Version int64
}

// Strategy is one way of doing "add delta to the balance" (negative = withdraw).
type Strategy interface {
	Name() string
	Apply(ctx context.Context, id, delta int64) error
	// Retries is how many times an operation had to start over (conflicts).
	Retries() int64
}

type Repo struct{ pool *pgxpool.Pool }

// Open creates the demo schema and returns a pool whose connections use it.
func Open(ctx context.Context, url string) (*Repo, error) {
	boot, err := pgx.Connect(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	_, err = boot.Exec(ctx, migrations.UpSQL)
	boot.Close(ctx)
	if err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 50
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		_, err := c.Exec(ctx, "SET search_path TO balance_demo")
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := warmUp(ctx, pool, 20); err != nil {
		pool.Close()
		return nil, err
	}
	return &Repo{pool: pool}, nil
}

// warmUp opens n connections up front. A cold pool staggers goroutines by the
// connect time, which can hide the very race the demo is meant to show.
func warmUp(ctx context.Context, pool *pgxpool.Pool, n int) error {
	conns := make([]*pgxpool.Conn, 0, n)
	defer func() {
		for _, c := range conns {
			c.Release()
		}
	}()
	for range n {
		c, err := pool.Acquire(ctx)
		if err != nil {
			return err
		}
		conns = append(conns, c)
	}
	return nil
}

func (r *Repo) Pool() *pgxpool.Pool { return r.pool }
func (r *Repo) Close()              { r.pool.Close() }

// Reset puts the account back to a known balance and version 0.
func (r *Repo) Reset(ctx context.Context, id, balance int64) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO accounts (id, balance, version) VALUES ($1, $2, 0)
		ON CONFLICT (id) DO UPDATE SET balance = EXCLUDED.balance, version = 0`, id, balance)
	return err
}

func (r *Repo) Get(ctx context.Context, id int64) (Account, error) {
	a := Account{ID: id}
	err := r.pool.QueryRow(ctx, `SELECT balance, version FROM accounts WHERE id = $1`, id).
		Scan(&a.Balance, &a.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, ErrAccountNotFound
	}
	return a, err
}

// isRetryable matches PostgreSQL's "try the whole transaction again" errors:
// 40001 serialization_failure and 40P01 deadlock_detected.
func isRetryable(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "40001" || pgErr.Code == "40P01")
}

// think simulates application work between read and write. It widens the race
// window so the demo is repeatable instead of depending on lucky timing.
func think(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	select {
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// backoff waits a random time that grows with each attempt, so competing
// retries do not collide again in lockstep.
func backoff(ctx context.Context, attempt int) error {
	max := time.Millisecond << min(attempt, 6)
	return think(ctx, time.Duration(rand.Int64N(int64(max))+1))
}
