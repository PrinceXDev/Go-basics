// Package middleware is the HTTP glue: it reads who is calling, asks the limiter, and answers 429 when needed.
package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"example.com/fixed-window-counter/internal/config"
	"example.com/fixed-window-counter/internal/limiter"
	"example.com/fixed-window-counter/internal/service"
)

// Checker is satisfied by *service.Service. Defined here so tests can fake it.
type Checker interface {
	Check(ctx context.Context, scopeType, scopeKey string, rule config.Rule) (limiter.Decision, error)
}

const apiKeyHeader = "X-API-Key"

// RateLimit limits requests per API key. Tenant and user scopes arrive in the multi-scope step.
func RateLimit(c Checker, rule config.Rule, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get(apiKeyHeader)
			if key == "" {
				writeJSON(w, http.StatusUnauthorized, "missing "+apiKeyHeader+" header")
				return
			}

			d, err := c.Check(r.Context(), "api_key", key, rule)
			if err != nil {
				// Only reached when failing closed, or when the client already hung up.
				log.Warn("rate limit check failed", "err", err)
				if errors.Is(err, service.ErrUnavailable) {
					writeJSON(w, http.StatusServiceUnavailable, "rate limiter unavailable, try again shortly")
				}
				return
			}

			if d.Degraded {
				// Failed open: let it through, but tell the caller the numbers are not real.
				w.Header().Set("X-RateLimit-Degraded", "true")
				next.ServeHTTP(w, r)
				return
			}

			h := w.Header()
			h.Set("X-RateLimit-Limit", strconv.Itoa(d.Limit))
			h.Set("X-RateLimit-Remaining", strconv.Itoa(d.Remaining))
			h.Set("X-RateLimit-Reset", strconv.FormatInt(d.ResetAt.Unix(), 10))

			if !d.Allowed {
				h.Set("Retry-After", strconv.Itoa(max(d.RetryAfterSeconds(), 1)))
				writeJSON(w, http.StatusTooManyRequests, "rate limit exceeded")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func writeJSON(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
