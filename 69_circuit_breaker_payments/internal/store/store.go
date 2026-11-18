// Package store is the PostgreSQL layer: payments plus the retry-job queue.
package store

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"cbpay/migrations"
)

type Status string

const (
	StatusPending   Status = "PENDING"   // sync call in progress
	StatusSucceeded Status = "SUCCEEDED" // money captured
	StatusQueued    Status = "QUEUED"    // gateway was down; async retry will finish it
	StatusFailed    Status = "FAILED"    // declined, or retries exhausted (dead letter)
)

type Payment struct {
	OrderID         string     `json:"orderId"`
	AmountCents     int64      `json:"amountCents"`
	Status          Status     `json:"status"`
	Path            string     `json:"path"` // sync | async: which path finished it
	GatewayChargeID *string    `json:"gatewayChargeId"`
	FailureReason   *string    `json:"failureReason"`
	SyncAttempts    int        `json:"syncAttempts"`
	AsyncAttempts   *int       `json:"asyncAttempts"`
	JobStatus       *string    `json:"jobStatus"`
	NextAttemptAt   *time.Time `json:"nextAttemptAt"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

type Job struct {
	ID          int64
	OrderID     string
	AmountCents int64
	Attempts    int // already incremented for the attempt being made now
	MaxAttempts int
}

type Store struct{ pool *pgxpool.Pool }

func Open(ctx context.Context, url string) (*Store, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

// Migrate applies every embedded .sql file in name order. The files are
// idempotent (IF NOT EXISTS), which is enough for a demo; real projects use
// goose/migrate with a version table.
func (s *Store) Migrate(ctx context.Context) error {
	names, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, n := range names {
		sql, err := migrations.FS.ReadFile(n)
		if err != nil {
			return err
		}
		if _, err := s.pool.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("migration %s: %w", n, err)
		}
	}
	return nil
}

const paymentCols = `
	p.order_id, p.amount_cents, p.status, p.path, p.gateway_charge_id, p.failure_reason,
	p.sync_attempts, j.attempts, j.status, j.next_attempt_at, p.created_at, p.updated_at`

const paymentFrom = ` FROM payments p LEFT JOIN payment_retry_jobs j ON j.order_id = p.order_id`

func scanPayment(row pgx.Row) (Payment, error) {
	var p Payment
	err := row.Scan(&p.OrderID, &p.AmountCents, &p.Status, &p.Path, &p.GatewayChargeID, &p.FailureReason,
		&p.SyncAttempts, &p.AsyncAttempts, &p.JobStatus, &p.NextAttemptAt, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

var ErrNotFound = errors.New("payment not found")

// CreatePayment inserts a PENDING payment. created=false means the order
// already existed — the API is idempotent, so the caller gets the old row.
func (s *Store) CreatePayment(ctx context.Context, orderID string, amount int64) (p Payment, created bool, err error) {
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO payments (order_id, amount_cents, status) VALUES ($1, $2, 'PENDING')
		 ON CONFLICT (order_id) DO NOTHING`, orderID, amount)
	if err != nil {
		return Payment{}, false, err
	}
	p, err = s.GetPayment(ctx, orderID)
	return p, tag.RowsAffected() == 1, err
}

func (s *Store) GetPayment(ctx context.Context, orderID string) (Payment, error) {
	p, err := scanPayment(s.pool.QueryRow(ctx, `SELECT`+paymentCols+paymentFrom+` WHERE p.order_id = $1`, orderID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Payment{}, ErrNotFound
	}
	return p, err
}

func (s *Store) ListPayments(ctx context.Context, limit int) ([]Payment, error) {
	rows, err := s.pool.Query(ctx, `SELECT`+paymentCols+paymentFrom+` ORDER BY p.created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Payment{}
	for rows.Next() {
		p, err := scanPayment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// MarkSucceeded also closes any retry job for the order, so a payment that
// got reconciled some other way isn't charged-attempted again by the worker.
func (s *Store) MarkSucceeded(ctx context.Context, orderID, chargeID string, syncAttempts int) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`UPDATE payments SET status='SUCCEEDED', gateway_charge_id=$2, sync_attempts=$3,
			        failure_reason=NULL, updated_at=now()
			 WHERE order_id=$1`, orderID, chargeID, syncAttempts); err != nil {
			return err
		}
		_, err := tx.Exec(ctx,
			`UPDATE payment_retry_jobs SET status='done', locked_at=NULL, updated_at=now()
			 WHERE order_id=$1 AND status IN ('pending', 'processing')`, orderID)
		return err
	})
}

func (s *Store) MarkFailed(ctx context.Context, orderID, reason string, syncAttempts int) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE payments SET status='FAILED', failure_reason=$2, sync_attempts=$3, updated_at=now()
		 WHERE order_id=$1`, orderID, reason, syncAttempts)
	return err
}

// Enqueue marks the payment QUEUED and creates its retry job in ONE
// transaction. If these were two separate writes, a crash in between could
// leave a QUEUED payment that nobody will ever retry.
func (s *Store) Enqueue(ctx context.Context, orderID, reason string, syncAttempts, maxAttempts int, firstDelay time.Duration) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`UPDATE payments SET status='QUEUED', path='async', failure_reason=$2, sync_attempts=$3, updated_at=now()
			 WHERE order_id=$1`, orderID, reason, syncAttempts); err != nil {
			return err
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO payment_retry_jobs (order_id, status, max_attempts, next_attempt_at, last_error)
			 VALUES ($1, 'pending', $2, now() + $3::interval, $4)
			 ON CONFLICT (order_id) DO NOTHING`,
			orderID, maxAttempts, fmt.Sprintf("%d milliseconds", firstDelay.Milliseconds()), reason)
		return err
	})
}

// ClaimJobs atomically takes up to `limit` due jobs. SKIP LOCKED makes
// concurrent workers skip rows another worker has locked instead of waiting,
// so each job is handed to exactly one worker. Jobs stuck in 'processing'
// longer than `lease` (worker crashed mid-call) are reclaimed.
func (s *Store) ClaimJobs(ctx context.Context, limit int, lease time.Duration) ([]Job, error) {
	rows, err := s.pool.Query(ctx, `
		UPDATE payment_retry_jobs j
		   SET status='processing', locked_at=now(), attempts=j.attempts+1, updated_at=now()
		  FROM payments p
		 WHERE p.order_id = j.order_id
		   AND j.id IN (
		       SELECT id FROM payment_retry_jobs
		        WHERE (status='pending' AND next_attempt_at <= now())
		           OR (status='processing' AND locked_at < now() - $2::interval)
		        ORDER BY next_attempt_at
		        LIMIT $1
		        FOR UPDATE SKIP LOCKED)
		RETURNING j.id, j.order_id, p.amount_cents, j.attempts, j.max_attempts`,
		limit, fmt.Sprintf("%d milliseconds", lease.Milliseconds()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []Job
	for rows.Next() {
		var j Job
		if err := rows.Scan(&j.ID, &j.OrderID, &j.AmountCents, &j.Attempts, &j.MaxAttempts); err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

// CompleteJob finishes a job and its payment together (success or decline).
func (s *Store) CompleteJob(ctx context.Context, j Job, status Status, chargeID, reason *string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`UPDATE payments SET status=$2, gateway_charge_id=COALESCE($3, gateway_charge_id),
			        failure_reason=$4, updated_at=now() WHERE order_id=$1`,
			j.OrderID, status, chargeID, reason); err != nil {
			return err
		}
		_, err := tx.Exec(ctx,
			`UPDATE payment_retry_jobs SET status='done', locked_at=NULL, updated_at=now() WHERE id=$1`, j.ID)
		return err
	})
}

// RescheduleJob puts a job back with a later next_attempt_at.
func (s *Store) RescheduleJob(ctx context.Context, j Job, wait time.Duration, lastErr string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE payment_retry_jobs
		    SET status='pending', locked_at=NULL, last_error=$3,
		        next_attempt_at=now() + $2::interval, updated_at=now()
		  WHERE id=$1`, j.ID, fmt.Sprintf("%d milliseconds", wait.Milliseconds()), lastErr)
	return err
}

// DeferJob gives the attempt back (attempts-1): used when the breaker is
// open, because a call we never made shouldn't burn one of the job's tries.
func (s *Store) DeferJob(ctx context.Context, j Job, wait time.Duration) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE payment_retry_jobs
		    SET status='pending', locked_at=NULL, attempts=GREATEST(attempts-1, 0),
		        next_attempt_at=now() + $2::interval, updated_at=now()
		  WHERE id=$1`, j.ID, fmt.Sprintf("%d milliseconds", wait.Milliseconds()))
	return err
}

// DeadLetter: retries exhausted. The payment is FAILED and the job is kept
// as 'dead' so ops can inspect and replay it.
func (s *Store) DeadLetter(ctx context.Context, j Job, lastErr string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`UPDATE payments SET status='FAILED', failure_reason=$2, updated_at=now() WHERE order_id=$1`,
			j.OrderID, "retries exhausted: "+lastErr); err != nil {
			return err
		}
		_, err := tx.Exec(ctx,
			`UPDATE payment_retry_jobs SET status='dead', locked_at=NULL, last_error=$2, updated_at=now() WHERE id=$1`,
			j.ID, lastErr)
		return err
	})
}

// RequeueStalePending catches payments stuck in PENDING because the API
// process died mid-call; without this they'd never finish.
func (s *Store) RequeueStalePending(ctx context.Context, olderThan time.Duration, maxAttempts int) (int64, error) {
	var n int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`UPDATE payments SET status='QUEUED', path='async', failure_reason='recovered stale PENDING', updated_at=now()
			  WHERE status='PENDING' AND updated_at < now() - $1::interval
			RETURNING order_id`, fmt.Sprintf("%d milliseconds", olderThan.Milliseconds()))
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		for _, id := range ids {
			if _, err := tx.Exec(ctx,
				`INSERT INTO payment_retry_jobs (order_id, status, max_attempts) VALUES ($1, 'pending', $2)
				 ON CONFLICT (order_id) DO NOTHING`, id, maxAttempts); err != nil {
				return err
			}
		}
		n = int64(len(ids))
		return nil
	})
	return n, err
}

type QueueStats struct {
	Pending    int `json:"pending"`
	Processing int `json:"processing"`
	Done       int `json:"done"`
	Dead       int `json:"dead"`
}

type PaymentStats struct {
	Pending   int `json:"PENDING"`
	Succeeded int `json:"SUCCEEDED"`
	Queued    int `json:"QUEUED"`
	Failed    int `json:"FAILED"`
}

func (s *Store) Stats(ctx context.Context) (PaymentStats, QueueStats, error) {
	var ps PaymentStats
	var qs QueueStats
	err := s.pool.QueryRow(ctx, `
		SELECT
		  count(*) FILTER (WHERE status='PENDING'),
		  count(*) FILTER (WHERE status='SUCCEEDED'),
		  count(*) FILTER (WHERE status='QUEUED'),
		  count(*) FILTER (WHERE status='FAILED')
		FROM payments`).Scan(&ps.Pending, &ps.Succeeded, &ps.Queued, &ps.Failed)
	if err != nil {
		return ps, qs, err
	}
	err = s.pool.QueryRow(ctx, `
		SELECT
		  count(*) FILTER (WHERE status='pending'),
		  count(*) FILTER (WHERE status='processing'),
		  count(*) FILTER (WHERE status='done'),
		  count(*) FILTER (WHERE status='dead')
		FROM payment_retry_jobs`).Scan(&qs.Pending, &qs.Processing, &qs.Done, &qs.Dead)
	return ps, qs, err
}

// Reset wipes demo data.
func (s *Store) Reset(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `TRUNCATE payment_retry_jobs, payments`)
	return err
}
