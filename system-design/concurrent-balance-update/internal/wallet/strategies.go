package wallet

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ---------- 1. Naive read-modify-write (BROKEN, for demonstration) ----------

// Naive reads the balance, computes the new value in Go, and writes it back.
// Under READ COMMITTED two of these can both read 40 and overwrite each other.
type Naive struct {
	Pool  *pgxpool.Pool
	Think time.Duration
}

func (Naive) Name() string   { return "naive (broken)" }
func (Naive) Retries() int64 { return 0 }
func (n Naive) Apply(ctx context.Context, id, delta int64) error {
	tx, err := n.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var bal int64
	if err := tx.QueryRow(ctx, `SELECT balance FROM accounts WHERE id = $1`, id).Scan(&bal); err != nil {
		return mapNoRows(err)
	}
	next := bal + delta
	if next < 0 {
		return ErrInsufficientFunds
	}
	if err := think(ctx, n.Think); err != nil {
		return err
	}
	// Absolute value computed from a possibly stale read: this is the lost update.
	if _, err := tx.Exec(ctx, `UPDATE accounts SET balance = $1 WHERE id = $2`, next, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ---------- 2. Atomic SQL update ----------

// Atomic lets the database do the arithmetic and the balance check in a single
// statement, so there is no gap between read and write.
type Atomic struct{ Pool *pgxpool.Pool }

func (Atomic) Name() string   { return "atomic SQL" }
func (Atomic) Retries() int64 { return 0 }
func (a Atomic) Apply(ctx context.Context, id, delta int64) error {
	tag, err := a.Pool.Exec(ctx, `
		UPDATE accounts SET balance = balance + $1
		WHERE id = $2 AND balance + $1 >= 0`, delta, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	// Zero rows: either no such account or the guard rejected it. Tell them apart.
	var exists bool
	if err := a.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM accounts WHERE id = $1)`, id).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrAccountNotFound
	}
	return ErrInsufficientFunds
}

// ---------- 3. Pessimistic locking: SELECT ... FOR UPDATE ----------

// Pessimistic locks the row on read; other writers wait until we commit.
type Pessimistic struct {
	Pool  *pgxpool.Pool
	Think time.Duration
}

func (Pessimistic) Name() string   { return "pessimistic (FOR UPDATE)" }
func (Pessimistic) Retries() int64 { return 0 }
func (p Pessimistic) Apply(ctx context.Context, id, delta int64) error {
	tx, err := p.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var bal int64
	if err := tx.QueryRow(ctx, `SELECT balance FROM accounts WHERE id = $1 FOR UPDATE`, id).Scan(&bal); err != nil {
		return mapNoRows(err)
	}
	next := bal + delta
	if next < 0 {
		return ErrInsufficientFunds
	}
	if err := think(ctx, p.Think); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE accounts SET balance = $1 WHERE id = $2`, next, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ---------- 4. Optimistic locking: version column ----------

// Optimistic takes no lock. The UPDATE only succeeds if the version is still
// the one we read; otherwise someone else got there first and we retry.
type Optimistic struct {
	Pool        *pgxpool.Pool
	Think       time.Duration
	MaxAttempts int
	retries     atomic.Int64
}

func (*Optimistic) Name() string     { return "optimistic (version)" }
func (o *Optimistic) Retries() int64 { return o.retries.Load() }
func (o *Optimistic) Apply(ctx context.Context, id, delta int64) error {
	attempts := o.MaxAttempts
	if attempts <= 0 {
		attempts = 10
	}
	for attempt := 0; attempt < attempts; attempt++ {
		var bal, ver int64
		err := o.Pool.QueryRow(ctx, `SELECT balance, version FROM accounts WHERE id = $1`, id).Scan(&bal, &ver)
		if err != nil {
			return mapNoRows(err)
		}
		next := bal + delta
		if next < 0 {
			return ErrInsufficientFunds
		}
		if err := think(ctx, o.Think); err != nil {
			return err
		}
		tag, err := o.Pool.Exec(ctx, `
			UPDATE accounts SET balance = $1, version = version + 1
			WHERE id = $2 AND version = $3`, next, id, ver)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 1 {
			return nil
		}
		o.retries.Add(1)
		if err := backoff(ctx, attempt); err != nil {
			return err
		}
	}
	return ErrConflict
}

// ---------- 5. Transaction isolation levels ----------

// Isolated keeps the naive read-modify-write but runs it at REPEATABLE READ or
// SERIALIZABLE. PostgreSQL then aborts the loser with SQLSTATE 40001 and we
// rerun the whole transaction.
type Isolated struct {
	Pool        *pgxpool.Pool
	Level       pgx.TxIsoLevel
	Think       time.Duration
	MaxAttempts int
	retries     atomic.Int64
}

func (i *Isolated) Name() string   { return "isolation: " + string(i.Level) }
func (i *Isolated) Retries() int64 { return i.retries.Load() }
func (i *Isolated) Apply(ctx context.Context, id, delta int64) error {
	attempts := i.MaxAttempts
	if attempts <= 0 {
		attempts = 10
	}
	var err error
	for attempt := 0; attempt < attempts; attempt++ {
		err = i.once(ctx, id, delta)
		if !isRetryable(err) {
			return err
		}
		i.retries.Add(1)
		if err := backoff(ctx, attempt); err != nil {
			return err
		}
	}
	return ErrConflict
}

func (i *Isolated) once(ctx context.Context, id, delta int64) error {
	tx, err := i.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: i.Level})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var bal int64
	if err := tx.QueryRow(ctx, `SELECT balance FROM accounts WHERE id = $1`, id).Scan(&bal); err != nil {
		return mapNoRows(err)
	}
	next := bal + delta
	if next < 0 {
		return ErrInsufficientFunds
	}
	if err := think(ctx, i.Think); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE accounts SET balance = $1 WHERE id = $2`, next, id); err != nil {
		return err
	}
	// Under SERIALIZABLE the conflict can also surface at commit.
	return tx.Commit(ctx)
}

func mapNoRows(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrAccountNotFound
	}
	return err
}
