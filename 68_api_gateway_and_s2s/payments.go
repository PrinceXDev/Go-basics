package main

// ===========================================================================
// PAYMENTS SERVICE — orchestrates a fund transfer across services.
//
// A transfer touches TWO accounts, but there is no shared database, so there
// is no single ACID transaction to wrap it in. Instead we use a SAGA: a
// sequence of local steps, each with a "compensating" step that undoes it.
//
//   1. GET    accounts/{from}        ownership check (alice owns ACC-1001?)
//   2. POST   accounts/{from}/debit  ref = TRF-x:debit
//   3. POST   accounts/{to}/credit   ref = TRF-x:credit
//        └─ if 3 is REJECTED (frozen/closed account):
//           POST accounts/{from}/credit  ref = TRF-x:refund   <- compensation
//   4. save transfer + outbox event   (one local transaction)
//   5. relay publishes the event      -> notifications, fraud, statements...
//
// Steps 1-3 are SYNCHRONOUS (the customer waits; they need the answer now).
// Step 5 is ASYNCHRONOUS (nobody should wait for an SMS to be sent).
//
// Every ref is derived from the client's Idempotency-Key, so if anything
// fails half-way and the client retries, each step either runs for the first
// time or replays its stored result. Nothing ever happens twice.
// ===========================================================================

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

type Transfer struct {
	ID     string `json:"id"`
	From   string `json:"from"`
	To     string `json:"to"`
	Amount int64  `json:"amount_paise"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

type idemEntry struct {
	hash   string // fingerprint of the request body
	done   bool
	status int
	body   []byte
}

type PaymentsService struct {
	accounts  *ServiceClient
	mu        sync.Mutex
	idem      map[string]*idemEntry // in production: a table with a UNIQUE key + TTL (e.g. 24h)
	transfers map[string]*Transfer
	outbox    []*outboxRow
	log       *slog.Logger

	DuplicateNextPublish atomic.Bool // simulate "relay crashed after publishing, before marking sent"
}

type outboxRow struct {
	event Event
	sent  bool
}

func NewPaymentsService(accounts *ServiceClient, log *slog.Logger) *PaymentsService {
	return &PaymentsService{accounts: accounts, idem: map[string]*idemEntry{}, transfers: map[string]*Transfer{}, log: log}
}

func (p *PaymentsService) Handler(auth ServiceAuth) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("POST /transfers", allow([]string{"gateway"}, p.createTransfer))
	return chain(mux, auth.Authenticate("payments"))
}

func (p *PaymentsService) createTransfer(w http.ResponseWriter, r *http.Request) {
	user := userIDFrom(r.Context())
	key := r.Header.Get(HeaderIdemKey)
	if key == "" {
		writeError(w, r, http.StatusBadRequest, "idempotency_key_required", "send an Idempotency-Key header")
		return
	}
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	bodyHash := sha256Hex(raw)
	scoped := user + "|" + key // scope by customer: two customers may pick the same key

	p.mu.Lock()
	if e, ok := p.idem[scoped]; ok {
		p.mu.Unlock()
		switch {
		case e.hash != bodyHash:
			writeError(w, r, http.StatusUnprocessableEntity, "idempotency_key_reused", "same key, different request")
		case !e.done:
			writeError(w, r, http.StatusConflict, "request_in_progress", "the original request is still running")
		default:
			w.Header().Set(HeaderReplay, "true")
			writeRaw(w, e.status, e.body)
		}
		return
	}
	entry := &idemEntry{hash: bodyHash}
	p.idem[scoped] = entry // claim the key BEFORE doing work, so a double-tap gets 409
	p.mu.Unlock()

	var in struct {
		From   string `json:"from"`
		To     string `json:"to"`
		Amount int64  `json:"amount_paise"`
	}
	var status int
	var resp any
	if err := json.Unmarshal(raw, &in); err != nil || in.From == "" || in.To == "" || in.Amount <= 0 || in.From == in.To {
		status, resp = http.StatusBadRequest, errorBody{Code: "bad_request", Message: "from, to (different) and amount_paise > 0 required"}
	} else {
		t := &Transfer{ID: "TRF-" + sha256Hex([]byte(scoped))[:10], From: in.From, To: in.To, Amount: in.Amount}
		status, resp = p.execute(r.Context(), t)
	}
	if eb, ok := resp.(errorBody); ok {
		eb.RequestID = requestIDFrom(r.Context())
		resp = eb
	}
	out, _ := json.Marshal(resp)

	p.mu.Lock()
	if status >= 500 {
		// Outcome unknown/transient: release the key so the client's retry
		// re-runs the saga. The refs make the re-run safe.
		delete(p.idem, scoped)
	} else {
		entry.done, entry.status, entry.body = true, status, out
	}
	p.mu.Unlock()
	writeRaw(w, status, out)
}

func (p *PaymentsService) execute(ctx context.Context, t *Transfer) (int, any) {
	// The user ID in ctx is propagated, so accounts returns 404 unless
	// t.From really belongs to this customer.
	var from Account
	if err := p.accounts.Call(ctx, http.MethodGet, "/accounts/"+t.From, nil, &from); err != nil {
		return p.failure(err)
	}

	debit := moveReq{Amount: t.Amount, Ref: t.ID + ":debit"}
	if err := p.accounts.Call(ctx, http.MethodPost, "/internal/accounts/"+t.From+"/debit", debit, nil); err != nil {
		return p.failure(err)
	}

	credit := moveReq{Amount: t.Amount, Ref: t.ID + ":credit"}
	err := p.accounts.Call(ctx, http.MethodPost, "/internal/accounts/"+t.To+"/credit", credit, nil)
	var apiErr *APIError
	if err != nil && errors.As(err, &apiErr) && apiErr.Status < 500 {
		// The credit was definitively REJECTED -> compensate the debit.
		p.log.Warn("saga compensating", "rid", requestIDFrom(ctx), "transfer", t.ID, "reason", apiErr.Code)
		refund := moveReq{Amount: t.Amount, Ref: t.ID + ":refund"}
		if cerr := p.accounts.Call(ctx, http.MethodPost, "/internal/accounts/"+t.From+"/credit", refund, nil); cerr != nil {
			// Compensation itself failed: in production the transfer row
			// stays PENDING and a reconciliation job retries the refund.
			return p.failure(cerr)
		}
		t.Status, t.Reason = "REVERSED", apiErr.Code
		p.commit(ctx, t, "transfer.reversed")
		return http.StatusUnprocessableEntity, map[string]any{
			"code": "transfer_reversed", "message": "beneficiary rejected the credit; debit refunded", "transfer": t,
		}
	}
	if err != nil {
		// Transient failure AFTER the debit: money has left alice but not
		// reached bob. Return 503; the retry with the same key finishes the
		// job. Real systems also persist the saga state (PENDING) so a
		// background reconciler completes it if the client never retries.
		return p.failure(err)
	}

	t.Status = "COMPLETED"
	p.commit(ctx, t, "transfer.completed")
	return http.StatusCreated, t
}

// commit saves the transfer AND its event together. In Postgres this is one
// BEGIN ... INSERT transfers ... INSERT outbox ... COMMIT. That is the
// TRANSACTIONAL OUTBOX pattern: it fixes the "dual write" problem — saving
// to the DB and publishing to Kafka are two systems, and if we crash between
// them we'd either lose the event or announce a transfer that never saved.
func (p *PaymentsService) commit(ctx context.Context, t *Transfer, eventType string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.transfers[t.ID] = t
	p.outbox = append(p.outbox, &outboxRow{event: Event{
		ID: newID("evt-"), Type: eventType, RequestID: requestIDFrom(ctx), Transfer: *t,
	}})
}

// RunOutboxRelay polls the outbox and publishes unsent rows. It is
// AT-LEAST-ONCE: if it crashes after publishing but before marking the row
// sent, the event goes out again on restart. Consumers must dedupe.
func (p *PaymentsService) RunOutboxRelay(ctx context.Context, bus *Bus) {
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		p.mu.Lock()
		var pending []*outboxRow
		for _, row := range p.outbox {
			if !row.sent {
				pending = append(pending, row)
			}
		}
		p.mu.Unlock()

		for _, row := range pending {
			bus.Publish("transfers", row.event)
			if p.DuplicateNextPublish.Swap(false) {
				bus.Publish("transfers", row.event) // the "crash before mark-sent" redelivery
			}
			p.mu.Lock()
			row.sent = true
			p.mu.Unlock()
		}
	}
}

func (p *PaymentsService) failure(err error) (int, any) {
	var apiErr *APIError
	switch {
	case errors.Is(err, ErrCircuitOpen):
		return http.StatusServiceUnavailable, errorBody{Code: "dependency_unavailable", Message: "accounts is unavailable, please retry later"}
	case errors.As(err, &apiErr) && apiErr.Status < 500:
		return apiErr.Status, errorBody{Code: apiErr.Code, Message: apiErr.Message}
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, errorBody{Code: "dependency_timeout", Message: "accounts timed out"}
	default:
		return http.StatusServiceUnavailable, errorBody{Code: "dependency_unavailable", Message: "accounts failed, please retry"}
	}
}

func writeRaw(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
