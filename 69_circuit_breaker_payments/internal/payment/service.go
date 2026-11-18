// Package payment holds the business flow:
//
//	Retry( CircuitBreaker( gateway call ) )  ->  on failure: fallback = queue it
//
// Retry is the OUTER layer and the breaker the INNER one (same default order
// as Resilience4j), so every retry attempt is seen by the breaker, and once
// the breaker opens the remaining retries stop instantly.
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

type Outcome string

const (
	OutcomeSucceeded Outcome = "succeeded" // charged during the request
	OutcomeQueued    Outcome = "queued"    // fallback: accepted, will finish async
	OutcomeDeclined  Outcome = "declined"  // business failure, shown to the user
	OutcomeExisting  Outcome = "existing"  // idempotent replay of an earlier request
)

type Result struct {
	Outcome Outcome       `json:"outcome"`
	Payment store.Payment `json:"payment"`
	Message string        `json:"message"`
}

type Service struct {
	Store   *store.Store
	Gateway *gateway.Client
	Breaker *breaker.Breaker
	Retry   retry.Policy
	Events  *EventLog

	// MaxAsyncAttempts is how many times the background worker tries before
	// the job goes to the dead-letter state.
	MaxAsyncAttempts int
}

// IsBreakerFailure: only "the gateway is sick" errors count. A decline is a
// perfectly healthy answer.
func IsBreakerFailure(err error) bool {
	return err != nil && !errors.Is(err, gateway.ErrDeclined)
}

// IsRetryable: transient gateway trouble only. ErrOpen is NOT retryable —
// retrying against an open breaker just burns time the user is waiting.
func IsRetryable(err error) bool {
	return errors.Is(err, gateway.ErrUnavailable)
}

// ProcessPayment is the Go version of:
//
//	return circuitBreaker.executeSupplier(() -> callPaymentGateway(orderId));
//
// plus retry-with-backoff and a queue-based fallback.
func (s *Service) ProcessPayment(ctx context.Context, orderID string, amountCents int64) (Result, error) {
	p, created, err := s.Store.CreatePayment(ctx, orderID, amountCents)
	if err != nil {
		return Result{}, err
	}
	if !created {
		// Client retried (double-click, network blip). Return the original
		// result instead of charging again.
		return Result{Outcome: OutcomeExisting, Payment: p, Message: "duplicate request: returning existing payment"}, nil
	}

	policy := s.Retry
	policy.OnRetry = func(attempt int, err error, wait time.Duration) {
		s.Events.Add("retry", orderID, "attempt %d failed (%v) — retrying in %s", attempt, err, wait.Round(time.Millisecond))
	}

	var charge gateway.ChargeResult
	attempts, err := retry.Do(ctx, policy, func(ctx context.Context, _ int) error {
		c, err := breaker.Call(s.Breaker, func() (gateway.ChargeResult, error) {
			// order_id is the idempotency key: retries of this order can
			// never create a second charge at the gateway.
			return s.Gateway.Charge(ctx, orderID, amountCents)
		})
		charge = c
		return err
	})

	switch {
	case err == nil:
		if err := s.Store.MarkSucceeded(ctx, orderID, charge.ID, attempts); err != nil {
			return Result{}, err
		}
		s.Events.Add("sync", orderID, "charged %s in %d attempt(s)", charge.ID, attempts)
		p, err := s.Store.GetPayment(ctx, orderID)
		return Result{Outcome: OutcomeSucceeded, Payment: p, Message: "payment successful"}, err

	case errors.Is(err, gateway.ErrDeclined):
		if err := s.Store.MarkFailed(ctx, orderID, "card declined", attempts); err != nil {
			return Result{}, err
		}
		s.Events.Add("sync", orderID, "card declined — not retried, breaker not affected")
		p, err := s.Store.GetPayment(ctx, orderID)
		return Result{Outcome: OutcomeDeclined, Payment: p, Message: "card declined, please use another card"}, err
	}

	// FALLBACK. The gateway is down (or the breaker is open). Don't show the
	// user an error page: accept the order, store the payment durably and let
	// the background worker finish it when the gateway is back.
	reason := err.Error()
	firstDelay := 2 * time.Second
	if errors.Is(err, breaker.ErrOpen) {
		reason = "circuit open — gateway call skipped"
	}
	// Use a fresh context: if the client hung up we still must persist this.
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.Store.Enqueue(saveCtx, orderID, reason, attempts, s.MaxAsyncAttempts, firstDelay); err != nil {
		return Result{}, err
	}
	s.Events.Add("fallback", orderID, "fallback: queued for async retry (%s)", reason)
	p, err = s.Store.GetPayment(saveCtx, orderID)
	return Result{
		Outcome: OutcomeQueued,
		Payment: p,
		Message: "order placed — payment is processing, we'll notify you when it completes",
	}, err
}

// GetPaymentLive shows the "cached response" fallback on a READ path: try to
// refresh from the gateway through the breaker; if that fails, serve our last
// known state from the database and say it may be stale. Never do this for
// WRITES — you can't pretend a charge succeeded.
func (s *Service) GetPaymentLive(ctx context.Context, orderID string) (store.Payment, string, error) {
	p, err := s.Store.GetPayment(ctx, orderID)
	if err != nil {
		return p, "", err
	}
	ch, err := breaker.Call(s.Breaker, func() (gateway.Charge, error) {
		return s.Gateway.GetCharge(ctx, orderID)
	})
	switch {
	case err == nil:
		if p.Status != store.StatusSucceeded && ch.Status == "succeeded" {
			// Reconciliation: the gateway knows something we didn't (e.g. a
			// timed-out call that actually captured the money).
			_ = s.Store.MarkSucceeded(ctx, orderID, ch.ID, p.SyncAttempts)
			s.Events.Add("reconcile", orderID, "gateway says %s already succeeded — local record fixed, retry job closed", ch.ID)
			p, _ = s.Store.GetPayment(ctx, orderID)
		}
		return p, "gateway", nil
	case errors.Is(err, gateway.ErrNotFound):
		return p, "gateway", nil
	default:
		return p, "cache (gateway unavailable: " + err.Error() + ")", nil
	}
}
