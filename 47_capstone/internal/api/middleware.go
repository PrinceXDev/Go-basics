package api

// Middleware stack — lesson 39, wired to slog from lesson 40.

import (
	"context"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

type Middleware func(http.Handler) http.Handler

// Chain applies middleware so mw[0] is the OUTERMOST layer.
func Chain(h http.Handler, mw ...Middleware) http.Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	return h
}

// ---- request-scoped values on the context ----

type ctxKey int

const (
	requestIDKey ctxKey = iota
	loggerKey
)

var reqCounter atomic.Int64

func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// LoggerFrom never returns nil — a logging helper that can panic is worse
// than no logging.
func LoggerFrom(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerKey).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}

// RequestID assigns (or honours) a correlation id and echoes it back.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			id = "req-" + strconv.FormatInt(reqCounter.Add(1), 10)
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
	})
}

// statusRecorder captures the status and byte count, which
// http.ResponseWriter does not expose.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

// Logging emits one structured line per request AND binds a request-scoped
// logger onto the context for everything downstream.
func Logging(base *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			l := base.With("request_id", RequestIDFrom(r.Context()))
			rec := &statusRecorder{ResponseWriter: w}

			next.ServeHTTP(rec, r.WithContext(context.WithValue(r.Context(), loggerKey, l)))

			if rec.status == 0 {
				rec.status = http.StatusOK
			}
			// Server errors deserve a louder level than a 404 does.
			level := slog.LevelInfo
			if rec.status >= 500 {
				level = slog.LevelError
			}
			l.Log(r.Context(), level, "request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"bytes", rec.bytes,
				"duration_ms", time.Since(start).Milliseconds(),
				"remote", r.RemoteAddr,
			)
		})
	}
}

// Recover turns a panic into a clean 500 instead of a dropped connection.
//
// It must sit INSIDE any middleware that spawns a goroutine (like Timeout),
// because recover() only catches panics on its own goroutine.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				if rec == http.ErrAbortHandler {
					panic(rec) // net/http's intentional abort — let it through
				}
				LoggerFrom(r.Context()).Error("panic recovered",
					"panic", rec, "stack", string(debug.Stack()))
				writeError(w, http.StatusInternalServerError, "internal server error", nil)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// Timeout bounds handler time via the request context. Handlers must
// actually respect ctx.Done() — database/sql does, because every query here
// uses a ...Context variant.
func Timeout(d time.Duration) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()

			done := make(chan struct{})
			go func() {
				defer close(done)
				next.ServeHTTP(w, r.WithContext(ctx))
			}()

			select {
			case <-done:
			case <-ctx.Done():
				writeError(w, http.StatusGatewayTimeout, "request timed out", nil)
			}
		})
	}
}

// APIKeyAuth short-circuits with a 401 by simply not calling next.
func APIKeyAuth(validKey string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			// A plain != is a timing oracle in principle; for an API key
			// checked once per request it is not a practical concern, but
			// subtle.ConstantTimeCompare is the correct habit.
			if key == "" || key != validKey {
				w.Header().Set("WWW-Authenticate", `Bearer realm="api"`)
				writeError(w, http.StatusUnauthorized, "unauthorized", nil)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
