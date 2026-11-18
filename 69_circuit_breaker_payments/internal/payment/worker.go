package payment

import (
	"context"
	"errors"
	"time"

	"cbpay/internal/breaker"
	"cbpay/internal/gateway"
	"cbpay/internal/retry"
	"cbpay/internal/store"
)

// Worker drains the retry queue. It is the "event-driven async retry":
// the API produced a job, the worker consumes it later.
type Worker struct {
	Svc          *Service
	PollInterval time.Duration
	BatchSize    int
	Lease        time.Duration // reclaim jobs held longer than this
	Backoff      retry.Policy  // spacing between async attempts
}

func (w *Worker) Run(ctx context.Context) {
	t := time.NewTicker(w.PollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.tick(ctx)
		}
	}
}

func (w *Worker) tick(ctx context.Context) {
	s := w.Svc
	if n, err := s.Store.RequeueStalePending(ctx, time.Minute, s.MaxAsyncAttempts); err == nil && n > 0 {
		s.Events.Add("worker", "", "recovered %d stale PENDING payment(s) into the queue", n)
	}

	// While the breaker is OPEN, don't even pull jobs: we already know the
	// gateway is down, and hammering it would slow its recovery.
	if s.Breaker.State() == breaker.Open && s.Breaker.Snapshot().OpenRemainingMs > 0 {
		return
	}

	jobs, err := s.Store.ClaimJobs(ctx, w.BatchSize, w.Lease)
	if err != nil {
		s.Events.Add("worker", "", "claim failed: %v", err)
		return
	}
	for _, j := range jobs {
		w.process(ctx, j)
	}
}

func (w *Worker) process(ctx context.Context, j store.Job) {
	s := w.Svc
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	ch, err := breaker.Call(s.Breaker, func() (gateway.ChargeResult, error) {
		// Same idempotency key as the original sync call. If that call timed
		// out but actually charged the card, the gateway returns the stored
		// charge (Replayed=true) instead of charging twice.
		return s.Gateway.Charge(callCtx, j.OrderID, j.AmountCents)
	})

	switch {
	case err == nil:
		_ = s.Store.CompleteJob(ctx, j, store.StatusSucceeded, &ch.ID, nil)
		note := ""
		if ch.Replayed {
			note = " (idempotent replay — earlier timed-out call had already charged; no double charge)"
		}
		s.Events.Add("worker", j.OrderID, "async attempt %d/%d succeeded: %s%s", j.Attempts, j.MaxAttempts, ch.ID, note)

	case errors.Is(err, gateway.ErrDeclined):
		reason := "card declined"
		_ = s.Store.CompleteJob(ctx, j, store.StatusFailed, nil, &reason)
		s.Events.Add("worker", j.OrderID, "async attempt %d: card declined — user must be notified", j.Attempts)

	case errors.Is(err, breaker.ErrOpen):
		_ = s.Store.DeferJob(ctx, j, 2*time.Second)
		s.Events.Add("worker", j.OrderID, "breaker rejected call — job deferred, attempt not counted")

	case j.Attempts >= j.MaxAttempts:
		_ = s.Store.DeadLetter(ctx, j, err.Error())
		s.Events.Add("dlq", j.OrderID, "gave up after %d async attempts → dead letter (needs manual review/refund flow)", j.Attempts)

	default:
		wait := w.Backoff.Backoff(j.Attempts)
		_ = s.Store.RescheduleJob(ctx, j, wait, err.Error())
		s.Events.Add("worker", j.OrderID, "async attempt %d/%d failed — next try in %s", j.Attempts, j.MaxAttempts, wait.Round(time.Millisecond))
	}
}
