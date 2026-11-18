// Package httpapi exposes the payment service and demo controls as JSON.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"

	"cbpay/internal/gateway"
	"cbpay/internal/payment"
	"cbpay/internal/store"
)

type Server struct {
	Svc *payment.Service
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/payments", s.createPayment)
	mux.HandleFunc("GET /api/payments", s.listPayments)
	mux.HandleFunc("GET /api/payments/{orderId}", s.getPayment)
	mux.HandleFunc("GET /api/state", s.state)
	mux.HandleFunc("PUT /api/gateway", s.setGateway)
	mux.HandleFunc("POST /api/demo/burst", s.burst)
	mux.HandleFunc("POST /api/demo/reset", s.reset)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	return logRequests(mux)
}

type createReq struct {
	OrderID string  `json:"orderId"`
	Amount  float64 `json:"amount"` // in rupees/dollars; stored as cents
}

func (s *Server) createPayment(w http.ResponseWriter, r *http.Request) {
	var in createReq
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	cents := int64(in.Amount*100 + 0.5)
	if cents <= 0 {
		writeErr(w, http.StatusBadRequest, "amount must be > 0")
		return
	}
	if in.OrderID == "" {
		in.OrderID = newOrderID()
	}
	res, err := s.Svc.ProcessPayment(r.Context(), in.OrderID, cents)
	if err != nil {
		slog.Error("process payment", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, statusFor(res.Outcome), res)
}

// statusFor maps outcomes to HTTP codes. 202 Accepted is the honest answer
// for the fallback path: "we have your order, the payment isn't done yet".
func statusFor(o payment.Outcome) int {
	switch o {
	case payment.OutcomeSucceeded:
		return http.StatusCreated
	case payment.OutcomeQueued:
		return http.StatusAccepted
	case payment.OutcomeDeclined:
		return http.StatusPaymentRequired
	default:
		return http.StatusOK
	}
}

func (s *Server) listPayments(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	ps, err := s.Svc.Store.ListPayments(r.Context(), limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ps)
}

func (s *Server) getPayment(w http.ResponseWriter, r *http.Request) {
	p, source, err := s.Svc.GetPaymentLive(r.Context(), r.PathValue("orderId"))
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "payment not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"payment": p, "source": source})
}

// state is the single endpoint the dashboard polls.
func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ps, qs, err := s.Svc.Store.Stats(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	gw, gwErr := s.Svc.Gateway.AdminGet(ctx)
	out := map[string]any{
		"breaker":  s.Svc.Breaker.Snapshot(),
		"payments": ps,
		"queue":    qs,
		"gateway":  gw,
		"events":   s.Svc.Events.Recent(60),
	}
	if gwErr != nil {
		out["gatewayError"] = gwErr.Error()
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) setGateway(w http.ResponseWriter, r *http.Request) {
	var in gateway.Settings
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	v, err := s.Svc.Gateway.AdminSet(r.Context(), in)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.Svc.Events.Add("gateway", "", "external gateway switched to %q", v.Settings.Mode)
	writeJSON(w, http.StatusOK, v)
}

// burst fires N payments concurrently — a traffic spike against whatever
// mode the gateway is in, to watch the breaker trip and shed load.
func (s *Server) burst(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Count  int     `json:"count"`
		Amount float64 `json:"amount"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	if in.Count <= 0 || in.Count > 100 {
		in.Count = 20
	}
	if in.Amount <= 0 {
		in.Amount = 49.99
	}
	cents := int64(in.Amount*100 + 0.5)

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	counts := map[payment.Outcome]int{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 5) // at most 5 in flight, like 5 concurrent users
	start := time.Now()
	for i := 0; i < in.Count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			res, err := s.Svc.ProcessPayment(ctx, newOrderID(), cents)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				counts["error"]++
				return
			}
			counts[res.Outcome]++
		}()
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, map[string]any{"outcomes": counts, "tookMs": time.Since(start).Milliseconds()})
}

func (s *Server) reset(w http.ResponseWriter, r *http.Request) {
	if err := s.Svc.Store.Reset(r.Context()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Svc.Events.Add("demo", "", "demo data cleared")
	w.WriteHeader(http.StatusNoContent)
}

func newOrderID() string { return "ord_" + uuid.NewString()[:8] }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) { r.status = code; r.ResponseWriter.WriteHeader(code) }

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/state" { // polled every second; too noisy
			next.ServeHTTP(w, r)
			return
		}
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		start := time.Now()
		next.ServeHTTP(rec, r)
		slog.Info(fmt.Sprintf("%s %s", r.Method, r.URL.Path), "status", rec.status, "took", time.Since(start).Round(time.Millisecond))
	})
}
