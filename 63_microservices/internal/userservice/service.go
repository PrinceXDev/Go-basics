// Package userservice owns users. It has no dependencies on other services --
// it is a LEAF. Leaves are easy; the interesting failure modes live one level
// up, in orderservice, which has to survive this one being down.
package userservice

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	userv1 "example.com/go-basics/63_microservices/gen/user/v1"
	"example.com/go-basics/63_microservices/internal/platform"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Service struct {
	userv1.UnimplementedUserServiceServer

	mu    sync.RWMutex
	users map[string]*userv1.User
	spent map[string]int64

	// CHAOS CONTROLS -- not production code, but the only honest way to
	// demonstrate retries and circuit breakers. Real teams do this properly
	// with a fault-injection proxy (toxiproxy) or a service-mesh fault filter.
	failUntil atomic.Int64 // unix nano: reject everything until this instant
	latency   atomic.Int64 // artificial delay, nanoseconds
}

func New() *Service {
	s := &Service{users: map[string]*userv1.User{}, spent: map[string]int64{}}
	s.users["u1"] = &userv1.User{Id: "u1", Email: "prince@example.com", FullName: "Prince Panchani", CreditLimitCents: 50_000, Active: true}
	s.users["u2"] = &userv1.User{Id: "u2", Email: "broke@example.com", FullName: "Skint Steve", CreditLimitCents: 1_000, Active: true}
	s.users["u3"] = &userv1.User{Id: "u3", Email: "banned@example.com", FullName: "Banned Betty", CreditLimitCents: 99_000, Active: false}
	return s
}

// FailFor makes every RPC return Unavailable for d -- i.e. "the pod is down".
func (s *Service) FailFor(d time.Duration) { s.failUntil.Store(time.Now().Add(d).UnixNano()) }

// Recover ends the outage immediately.
func (s *Service) Recover() { s.failUntil.Store(0) }

// SetLatency simulates a slow dependency (a locked table, a cold cache).
func (s *Service) SetLatency(d time.Duration) { s.latency.Store(int64(d)) }

func (s *Service) chaos(ctx context.Context) error {
	if until := s.failUntil.Load(); until > 0 && time.Now().UnixNano() < until {
		// Unavailable is the RIGHT code for "I am down": it is the only one
		// callers are allowed to retry blindly.
		return status.Error(codes.Unavailable, "user service unavailable (simulated)")
	}
	if d := time.Duration(s.latency.Load()); d > 0 {
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return status.FromContextError(ctx.Err()).Err()
		}
	}
	return nil
}

func (s *Service) GetUser(ctx context.Context, req *userv1.GetUserRequest) (*userv1.GetUserResponse, error) {
	if err := s.chaos(ctx); err != nil {
		return nil, err
	}
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}

	s.mu.RLock()
	u, ok := s.users[req.GetId()]
	s.mu.RUnlock()
	if !ok {
		return nil, status.Errorf(codes.NotFound, "user %s not found", req.GetId())
	}

	platform.Logger(ctx).Info("user fetched", "user_id", u.Id)
	return &userv1.GetUserResponse{User: u}, nil
}

func (s *Service) ValidateCredit(ctx context.Context, req *userv1.ValidateCreditRequest) (*userv1.ValidateCreditResponse, error) {
	if err := s.chaos(ctx); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	u, ok := s.users[req.GetUserId()]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "user %s not found", req.GetUserId())
	}
	if !u.Active {
		// FailedPrecondition, not InvalidArgument: the request was fine, the
		// SYSTEM STATE is what blocks it. The caller can fix state and retry.
		return nil, status.Error(codes.FailedPrecondition, "account is not active")
	}

	remaining := u.CreditLimitCents - s.spent[u.Id]
	if req.GetAmountCents() > remaining {
		// Note: NOT an error. "Declined" is a perfectly successful RPC with a
		// negative answer. Reserve error codes for things that went WRONG --
		// otherwise your error rate dashboard measures customer behaviour.
		return &userv1.ValidateCreditResponse{
			Approved: false, RemainingCents: remaining,
			Reason: "insufficient credit",
		}, nil
	}

	s.spent[u.Id] += req.GetAmountCents()
	return &userv1.ValidateCreditResponse{Approved: true, RemainingCents: remaining - req.GetAmountCents()}, nil
}
