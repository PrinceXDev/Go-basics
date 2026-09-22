// Package orderservice owns orders AND depends on the user service.
//
// That dependency is the entire lesson. Everything below -- the per-call
// deadline, the retry, the circuit breaker, the degraded fallback, the
// idempotency key -- exists because a network call to another process can
// be slow, fail, or succeed-but-you-never-hear-about-it.
package orderservice

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	orderv1 "example.com/go-basics/63_microservices/gen/order/v1"
	userv1 "example.com/go-basics/63_microservices/gen/user/v1"
	"example.com/go-basics/63_microservices/internal/platform"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Service struct {
	orderv1.UnimplementedOrderServiceServer

	users   userv1.UserServiceClient
	breaker *platform.Breaker
	retry   platform.RetryPolicy

	mu     sync.RWMutex
	orders map[string]*orderv1.Order
	// idempotency: key -> order id. Survives retries so a re-sent
	// CreateOrder returns the SAME order instead of making a second one.
	idem map[string]string
	seq  int
}

func New(users userv1.UserServiceClient, log func(string, platform.BreakerState, platform.BreakerState)) *Service {
	return &Service{
		users: users,
		// 3 consecutive failures -> open for 2s. Tune per dependency: a
		// threshold that is too low flaps on a single blip; too high and
		// you never protect anything.
		breaker: platform.NewBreaker("user-service", 3, 2*time.Second, log),
		retry:   platform.DefaultRetry(),
		orders:  map[string]*orderv1.Order{},
		idem:    map[string]string{},
	}
}

func (s *Service) CreateOrder(ctx context.Context, req *orderv1.CreateOrderRequest) (*orderv1.CreateOrderResponse, error) {
	log := platform.Logger(ctx)

	if req.GetUserId() == "" || len(req.GetLines()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "user_id and at least one line are required")
	}

	// -------------------------------------------------------------------
	// IDEMPOTENCY. Check FIRST, before doing any work.
	//
	// Why this is non-negotiable: the caller retries on Unavailable. But
	// "Unavailable" can also mean "your request succeeded and the response
	// got lost". Without an idempotency key, that retry silently charges
	// the customer twice. This is the #1 correctness bug in distributed
	// systems, and it is invisible in testing.
	// -------------------------------------------------------------------
	if key := req.GetIdempotencyKey(); key != "" {
		s.mu.RLock()
		existing, ok := s.idem[key]
		s.mu.RUnlock()
		if ok {
			log.Info("idempotent replay", "key", key, "order_id", existing)
			s.mu.RLock()
			o := s.orders[existing]
			s.mu.RUnlock()
			return &orderv1.CreateOrderResponse{Order: o}, nil
		}
	}

	total := int64(0)
	for _, l := range req.GetLines() {
		if l.GetQuantity() <= 0 {
			return nil, status.Error(codes.InvalidArgument, "quantity must be positive")
		}
		total += int64(l.GetQuantity()) * l.GetUnitPriceCents()
	}

	// -------------------------------------------------------------------
	// THE CROSS-SERVICE CALL, wrapped in the full resilience stack:
	//
	//	breaker( retry( per-attempt deadline( rpc ) ) )
	// -------------------------------------------------------------------
	var credit *userv1.ValidateCreditResponse

	err := s.breaker.Do(ctx, func(ctx context.Context) error {
		return s.retry.Do(ctx, "user.ValidateCredit", func(ctx context.Context) error {
			// DEADLINE BUDGET: each attempt gets 300ms, and the whole thing
			// is still bounded by the caller's deadline (whichever is
			// sooner wins -- that is how context.WithTimeout composes).
			//
			// The general rule: a downstream timeout must be SHORTER than
			// your own, or you return DeadlineExceeded to your caller
			// while your dependency is still working. Budget shrinks as
			// you go down the call tree.
			attemptCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
			defer cancel()

			resp, err := s.users.ValidateCredit(attemptCtx, &userv1.ValidateCreditRequest{
				UserId: req.GetUserId(), AmountCents: total,
			})
			if err != nil {
				return err
			}
			credit = resp
			return nil
		})
	})

	if err != nil {
		// -----------------------------------------------------------------
		// DEGRADED MODE. The dependency is down. You now make a BUSINESS
		// decision, not a technical one:
		//
		//	fail closed -> reject the order. Safe, loses revenue.
		//	fail open   -> accept it as PENDING and verify later. Keeps the
		//	               checkout alive, accepts some risk, needs a
		//	               reconciliation job to finish the job.
		//
		// The right answer depends on the amount at stake. Here: small
		// orders go through as PENDING, large ones are rejected. Being able
		// to articulate this trade-off is what separates "I know the
		// circuit breaker pattern" from "I have run one in production".
		// -----------------------------------------------------------------
		if errors.Is(err, platform.ErrCircuitOpen) || status.Code(err) == codes.Unavailable {
			if total <= 10_000 {
				log.Warn("user service down -- accepting order as PENDING", "total_cents", total, "error", err)
				return s.persist(req, total, orderv1.Order_STATUS_PENDING)
			}
			log.Error("user service down -- rejecting high-value order", "total_cents", total, "error", err)
			return nil, status.Error(codes.Unavailable, "cannot verify credit right now, please retry")
		}

		// PRESERVE THE CODE. Flattening every upstream failure to Internal
		// is the most common error-handling bug in a service mesh: the
		// gateway then returns 500 for a timeout (should be 504) and for a
		// missing user (should be 404), and your error budget burns on
		// things that are not bugs.
		switch code := status.Code(err); code {
		case codes.NotFound, codes.FailedPrecondition, codes.InvalidArgument, codes.PermissionDenied:
			return nil, status.Errorf(code, "credit check: %s", status.Convert(err).Message())
		case codes.DeadlineExceeded:
			// We ran out of time waiting on a dependency. That is a gateway
			// timeout (504), not "we have a bug" (500).
			return nil, status.Error(codes.DeadlineExceeded, "credit check timed out")
		case codes.Canceled:
			return nil, status.Error(codes.Canceled, "request cancelled")
		}
		return nil, status.Errorf(codes.Internal, "credit check failed: %v", err)
	}

	if !credit.GetApproved() {
		return s.persistRejected(req, total, credit.GetReason())
	}

	return s.persist(req, total, orderv1.Order_STATUS_CONFIRMED)
}

func (s *Service) persist(req *orderv1.CreateOrderRequest, total int64, st orderv1.Order_Status) (*orderv1.CreateOrderResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.seq++
	o := &orderv1.Order{
		Id: fmt.Sprintf("o%d", s.seq), UserId: req.GetUserId(), Lines: req.GetLines(),
		TotalCents: total, Status: st, CreatedAt: timestamppb.Now(),
	}
	s.orders[o.Id] = o
	if k := req.GetIdempotencyKey(); k != "" {
		s.idem[k] = o.Id
	}
	return &orderv1.CreateOrderResponse{Order: o}, nil
}

func (s *Service) persistRejected(req *orderv1.CreateOrderRequest, total int64, reason string) (*orderv1.CreateOrderResponse, error) {
	resp, _ := s.persist(req, total, orderv1.Order_STATUS_REJECTED)
	// The order exists (rejected) AND the caller gets a clear code.
	// FailedPrecondition, because retrying the identical request is futile
	// until the user's credit situation changes.
	return resp, status.Errorf(codes.FailedPrecondition, "order rejected: %s", reason)
}

func (s *Service) GetOrder(ctx context.Context, req *orderv1.GetOrderRequest) (*orderv1.GetOrderResponse, error) {
	s.mu.RLock()
	o, ok := s.orders[req.GetId()]
	s.mu.RUnlock()
	if !ok {
		return nil, status.Errorf(codes.NotFound, "order %s not found", req.GetId())
	}
	return &orderv1.GetOrderResponse{Order: o}, nil
}

func (s *Service) ListOrders(ctx context.Context, req *orderv1.ListOrdersRequest) (*orderv1.ListOrdersResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]*orderv1.Order, 0, len(s.orders))
	for _, o := range s.orders {
		if req.GetUserId() == "" || o.UserId == req.GetUserId() {
			out = append(out, o)
		}
	}
	return &orderv1.ListOrdersResponse{Orders: out}, nil
}

// BreakerState is exposed so the demo can print it. In production this is a
// Prometheus gauge -- you absolutely want an alert on "breaker is open".
func (s *Service) BreakerState() platform.BreakerState { return s.breaker.State() }
