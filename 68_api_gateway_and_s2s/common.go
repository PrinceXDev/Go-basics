package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
)

// Headers every hop in the system understands. Defining them once stops the
// classic "X-Request-Id vs X-Request-ID vs X-Correlation-ID" drift between teams.
const (
	HeaderRequestID = "X-Request-ID"      // one ID for the whole journey: gateway -> payments -> accounts -> event
	HeaderUserID    = "X-User-ID"         // the VERIFIED customer, set by the gateway only
	HeaderIdemKey   = "Idempotency-Key"   // client-chosen key that makes a POST safe to retry
	HeaderReplay    = "Idempotent-Replay" // "true" when we returned a stored answer instead of re-running
)

type ctxKey int

const (
	ctxRequestID ctxKey = iota
	ctxUserID
	ctxCaller // which SERVICE called us (from the service token), not which customer
)

func withValue(ctx context.Context, k ctxKey, v string) context.Context {
	return context.WithValue(ctx, k, v)
}

func fromCtx(ctx context.Context, k ctxKey) string {
	v, _ := ctx.Value(k).(string)
	return v
}

func requestIDFrom(ctx context.Context) string { return fromCtx(ctx, ctxRequestID) }
func userIDFrom(ctx context.Context) string    { return fromCtx(ctx, ctxUserID) }
func callerFrom(ctx context.Context) string    { return fromCtx(ctx, ctxCaller) }

// errorBody is the ONE error shape every service returns. A shared contract
// means the gateway and other services can parse any error the same way.
type errorBody struct {
	Code      string `json:"code"`    // machine-readable: "insufficient_funds"
	Message   string `json:"message"` // human-readable
	RequestID string `json:"request_id,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, msg string) {
	writeJSON(w, status, errorBody{Code: code, Message: msg, RequestID: requestIDFrom(r.Context())})
}

func newID(prefix string) string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

// Money is stored as int64 paise, never float64: 0.1 + 0.2 != 0.3 in floating
// point, and a bank cannot lose a paisa to rounding.
func inr(paise int64) string {
	return fmt.Sprintf("INR %d.%02d", paise/100, paise%100)
}

// middleware is the standard Go shape: take a handler, return a wrapped one.
type middleware func(http.Handler) http.Handler

// chain applies middlewares so the FIRST one listed is the OUTERMOST, i.e. it
// runs first on the way in. Reading top-to-bottom = request order.
func chain(h http.Handler, mws ...middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// statusRecorder captures the status code for access logs.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController (used by ReverseProxy for flushing)
// reach the real writer underneath our wrapper.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }
