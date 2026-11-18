package main

// ACCOUNTS SERVICE — owns balances. Nobody else touches its data; other
// services must ask through its API. (Shared databases between services
// = a "distributed monolith": you get all the network pain and none of the
// independence.)

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

type Account struct {
	ID      string `json:"id"`
	Owner   string `json:"owner"`
	Balance int64  `json:"balance_paise"`
	Frozen  bool   `json:"frozen"`
}

type storedResult struct {
	status int
	body   any
}

type AccountsService struct {
	mu       sync.Mutex
	accounts map[string]*Account
	// applied remembers the outcome of every debit/credit by its reference.
	// In Postgres this is a UNIQUE constraint on (op, ref) in the ledger table.
	applied map[string]storedResult
	log     *slog.Logger

	// Fault injection, so the demo can show real failure modes.
	Down             atomic.Bool
	FailNext         atomic.Int32
	Latency          atomic.Int64 // nanoseconds
	LoseNextResponse atomic.Bool  // commit the debit, then pretend the reply was lost
}

func NewAccountsService(log *slog.Logger, accts ...*Account) *AccountsService {
	s := &AccountsService{accounts: map[string]*Account{}, applied: map[string]storedResult{}, log: log}
	for _, a := range accts {
		s.accounts[a.ID] = a
	}
	return s
}

func (s *AccountsService) Handler(auth ServiceAuth) http.Handler {
	mux := http.NewServeMux()
	// Public-ish read: reachable from the gateway (customers) and payments.
	mux.Handle("GET /accounts/{id}", allow([]string{"gateway", "payments"}, s.getAccount))
	// Money movement: internal only, and ONLY payments may call it.
	mux.Handle("POST /internal/accounts/{id}/debit", allow([]string{"payments"}, s.move(-1)))
	mux.Handle("POST /internal/accounts/{id}/credit", allow([]string{"payments"}, s.move(+1)))
	return chain(mux, auth.Authenticate("accounts"), s.chaos)
}

func (s *AccountsService) chaos(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if d := time.Duration(s.Latency.Load()); d > 0 {
			select {
			case <-time.After(d):
			case <-r.Context().Done(): // caller gave up; stop working too
				return
			}
		}
		if s.Down.Load() {
			writeError(w, r, http.StatusServiceUnavailable, "unavailable", "accounts is down")
			return
		}
		if s.FailNext.Load() > 0 {
			s.FailNext.Add(-1)
			writeError(w, r, http.StatusServiceUnavailable, "unavailable", "transient failure")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *AccountsService) getAccount(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	a, ok := s.accounts[r.PathValue("id")]
	var cp Account
	if ok {
		cp = *a
	}
	s.mu.Unlock()

	// OBJECT-LEVEL AUTHORIZATION (OWASP API #1, "BOLA"): a valid token is not
	// enough — the account must belong to THIS customer. We answer 404, not
	// 403, so an attacker can't even learn which account numbers exist.
	if !ok || (userIDFrom(r.Context()) != "" && cp.Owner != userIDFrom(r.Context())) {
		writeError(w, r, http.StatusNotFound, "account_not_found", "no such account")
		return
	}
	writeJSON(w, http.StatusOK, cp)
}

type moveReq struct {
	Amount int64  `json:"amount_paise"`
	Ref    string `json:"ref"` // idempotency reference, e.g. "TRF-ab12:debit"
}

func (s *AccountsService) move(sign int64) http.HandlerFunc {
	op := "credit"
	if sign < 0 {
		op = "debit"
	}
	return func(w http.ResponseWriter, r *http.Request) {
		var in moveReq
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Ref == "" || in.Amount <= 0 {
			writeError(w, r, http.StatusBadRequest, "bad_request", "amount_paise > 0 and ref are required")
			return
		}
		key := op + ":" + in.Ref

		s.mu.Lock()
		if prev, seen := s.applied[key]; seen {
			s.mu.Unlock()
			s.log.Info("accounts replay", "rid", requestIDFrom(r.Context()), "op", op, "ref", in.Ref)
			w.Header().Set(HeaderReplay, "true")
			writeJSON(w, prev.status, prev.body)
			return
		}
		status, body := s.apply(r.PathValue("id"), sign, in.Amount)
		s.applied[key] = storedResult{status, body} // store failures too: a retry must get the same "no"
		s.mu.Unlock()

		s.log.Info("accounts "+op, "rid", requestIDFrom(r.Context()), "account", r.PathValue("id"),
			"amount", inr(in.Amount), "status", status)

		if s.LoseNextResponse.Swap(false) {
			// The money moved, but the caller never hears about it. Without
			// the `applied` table, the retry would debit the customer twice.
			writeError(w, r, http.StatusServiceUnavailable, "unavailable", "connection reset (simulated)")
			return
		}
		writeJSON(w, status, body)
	}
}

// apply must be called with s.mu held.
func (s *AccountsService) apply(id string, sign, amount int64) (int, any) {
	a, ok := s.accounts[id]
	switch {
	case !ok:
		return http.StatusNotFound, errorBody{Code: "account_not_found", Message: "no such account"}
	case a.Frozen:
		return http.StatusUnprocessableEntity, errorBody{Code: "account_frozen", Message: "account is frozen"}
	case sign < 0 && a.Balance < amount:
		return http.StatusUnprocessableEntity, errorBody{Code: "insufficient_funds", Message: "balance too low"}
	}
	a.Balance += sign * amount
	return http.StatusOK, map[string]any{"account": id, "balance_paise": a.Balance}
}

func (s *AccountsService) Balance(id string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.accounts[id].Balance
}
