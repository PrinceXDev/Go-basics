package wallet

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrSameAccount = errors.New("cannot transfer to the same account")

// IsDeadlock reports PostgreSQL's deadlock_detected error (40P01).
func IsDeadlock(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "40P01"
}

// Transfer moves money between two accounts inside one transaction, locking
// both rows with FOR UPDATE.
//
// With OrderLocks=false it locks "from" first then "to". Two opposite
// transfers (A->B and B->A) then each hold one lock and wait for the other:
// a deadlock. PostgreSQL detects it and aborts one with 40P01.
// With OrderLocks=true it always locks the lower id first, so a cycle is
// impossible and nobody is aborted.
type Transfer struct {
	Pool        *pgxpool.Pool
	Think       time.Duration // pause after the first lock so the overlap is certain
	OrderLocks  bool
	MaxAttempts int // >1 retries on deadlock; default 1
	retries     atomic.Int64
}

func (t *Transfer) Retries() int64 { return t.retries.Load() }

func (t *Transfer) Apply(ctx context.Context, from, to, amount int64) error {
	if from == to {
		return ErrSameAccount
	}
	attempts := max(t.MaxAttempts, 1)
	var err error
	for attempt := 0; attempt < attempts; attempt++ {
		err = t.once(ctx, from, to, amount)
		if !IsDeadlock(err) {
			return err
		}
		t.retries.Add(1)
		if attempt < attempts-1 {
			if berr := backoff(ctx, attempt); berr != nil {
				return berr
			}
		}
	}
	return err
}

func (t *Transfer) once(ctx context.Context, from, to, amount int64) error {
	tx, err := t.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	first, second := from, to
	if t.OrderLocks && from > to {
		first, second = to, from
	}

	bal := map[int64]int64{}
	for i, id := range []int64{first, second} {
		var b int64
		err := tx.QueryRow(ctx, `SELECT balance FROM accounts WHERE id = $1 FOR UPDATE`, id).Scan(&b)
		if err != nil {
			return mapNoRows(err)
		}
		bal[id] = b
		if i == 0 {
			if err := think(ctx, t.Think); err != nil {
				return err
			}
		}
	}
	if bal[from] < amount {
		return ErrInsufficientFunds
	}
	if _, err := tx.Exec(ctx, `UPDATE accounts SET balance = balance - $1 WHERE id = $2`, amount, from); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE accounts SET balance = balance + $1 WHERE id = $2`, amount, to); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Move is one requested transfer.
type Move struct{ From, To, Amount int64 }

type TransferResult struct {
	Succeeded    int
	Insufficient int
	Deadlocked   int // aborted by PostgreSQL and not retried
	Failed       int
	Elapsed      time.Duration
}

// RunTransfers fires all moves at once, like RunConcurrent does for deposits.
func RunTransfers(ctx context.Context, t *Transfer, moves []Move) TransferResult {
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		res   TransferResult
		start = make(chan struct{})
	)
	for _, m := range moves {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			err := t.Apply(ctx, m.From, m.To, m.Amount)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				res.Succeeded++
			case errors.Is(err, ErrInsufficientFunds):
				res.Insufficient++
			case IsDeadlock(err):
				res.Deadlocked++
			default:
				res.Failed++
			}
		}()
	}
	t0 := time.Now()
	close(start)
	wg.Wait()
	res.Elapsed = time.Since(t0)
	return res
}

// Total sums the balances of the given accounts.
func (r *Repo) Total(ctx context.Context, ids ...int64) (int64, error) {
	var sum int64
	err := r.pool.QueryRow(ctx, `SELECT COALESCE(SUM(balance), 0) FROM accounts WHERE id = ANY($1)`, ids).Scan(&sum)
	return sum, err
}
