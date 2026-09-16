package main

// ============================================================================
// CONCEPT: HTTP middleware — logging, recovery, auth, timeouts, request IDs.
//
// WHY THIS MATTERS
// Lesson 27 gave you handlers. Real services need cross-cutting behaviour
// that applies to EVERY request: log it, don't let a panic kill the
// process, check the API key, attach a request ID, enforce a timeout. You
// don't want that code copy-pasted into forty handlers.
//
// THE WHOLE IDEA IN ONE LINE
//   type Middleware func(http.Handler) http.Handler
// A middleware takes a handler and returns a NEW handler that does
// something before and/or after calling the original. That's it. No
// framework, no registration API — just function composition.
//
// JS/TS comparison: Express's `(req, res, next) => {}`. The difference is
// Go has no `next()` callback — you literally call the wrapped handler,
// which makes the control flow obvious and means "don't call it" is how you
// short-circuit (a 401, say).
// ============================================================================

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"time"
)

// ---------------------------------------------------------------------------
// THE TYPE
// ---------------------------------------------------------------------------

// Middleware is the shape every one of these functions has. Naming the type
// isn't required, but it makes the Chain helper below readable.
type Middleware func(http.Handler) http.Handler

// Chain composes middleware so that the FIRST argument is the OUTERMOST
// layer — i.e. it runs first on the way in and last on the way out.
//
//	Chain(h, A, B, C)  ==  A(B(C(h)))
//
// Request:  A -> B -> C -> h
// Response: h -> C -> B -> A
//
// Order matters enormously: Recover must wrap the handler that might panic,
// and RequestID must run before anything that logs the request ID.
func Chain(h http.Handler, mw ...Middleware) http.Handler {
	// Apply in reverse so mw[0] ends up outermost.
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	return h
}

// ---------------------------------------------------------------------------
// MIDDLEWARE 1: REQUEST ID
// Generates an id and puts it on the context (lesson 25) so every log line
// for this request can be correlated.
// ---------------------------------------------------------------------------

type ctxKey string

const requestIDKey ctxKey = "requestID"

var counter atomic.Int64 // atomic because handlers run concurrently (lesson 24)

func RequestID(next http.Handler) http.Handler {
	// http.HandlerFunc converts a plain func into an http.Handler — the
	// adapter pattern from lesson 14.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			id = fmt.Sprintf("req-%04d", counter.Add(1))
		}
		// You cannot mutate a Request — you derive a NEW one carrying a new
		// context. r.WithContext is the standard idiom.
		ctx := context.WithValue(r.Context(), requestIDKey, id)
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequestIDFrom is the typed accessor. Never make callers do the type
// assertion themselves.
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// ---------------------------------------------------------------------------
// MIDDLEWARE 2: LOGGING
// To log the status code we have to capture it, because http.ResponseWriter
// has no getter. The standard trick is a wrapper type.
// ---------------------------------------------------------------------------

type statusRecorder struct {
	http.ResponseWriter // embedded (lesson 15) — we inherit every method
	status              int
	bytes               int
}

// Override just the two methods we care about.
func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK // Write without WriteHeader implies 200
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

func Logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 0}

		next.ServeHTTP(rec, r) // everything downstream writes through rec

		if rec.status == 0 {
			rec.status = http.StatusOK // handler wrote nothing at all
		}
		log.Printf("%s %-5s %-18s %d %4dB %v",
			RequestIDFrom(r.Context()), r.Method, r.URL.Path,
			rec.status, rec.bytes, time.Since(start).Round(time.Microsecond))
	})
}

// ---------------------------------------------------------------------------
// MIDDLEWARE 3: RECOVER
// A panic in a handler does NOT crash the server — net/http recovers it per
// connection — but it closes the connection with no response and logs an
// ugly stack trace. Recover your own so the client gets a clean 500.
// ---------------------------------------------------------------------------

func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				// http.ErrAbortHandler is net/http's intentional "stop now"
				// signal — re-panic so the server handles it normally.
				if rec == http.ErrAbortHandler {
					panic(rec)
				}
				log.Printf("%s PANIC: %v\n%s", RequestIDFrom(r.Context()), rec, debug.Stack())
				writeJSON(w, http.StatusInternalServerError,
					map[string]string{"error": "internal server error"})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------------------
// MIDDLEWARE 4: AUTH (a short-circuiting middleware)
// Note it takes a parameter, so it's a FUNCTION THAT RETURNS a Middleware.
// This is how you make configurable middleware.
// ---------------------------------------------------------------------------

func APIKeyAuth(validKey string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if key != validKey {
				// Short-circuit: we simply DON'T call next. Nothing
				// downstream ever runs.
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ---------------------------------------------------------------------------
// MIDDLEWARE 5: TIMEOUT
// Bounds how long a handler may take, using the context from lesson 25.
//
// ⚠ THIS ONE SPAWNS A GOROUTINE, AND THAT CHANGES EVERYTHING ABOVE IT.
// `recover()` only catches a panic on ITS OWN goroutine. So any Recover
// middleware placed OUTSIDE this one is useless — the handler now panics on
// a different goroutine and takes the whole process down. Recover must sit
// INSIDE Timeout. (Author's note: the first draft of this file got that
// wrong and crashed. It is an extremely easy mistake to make.)
// ---------------------------------------------------------------------------

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
			case <-done: // handler finished in time
			case <-ctx.Done():
				// NOTE: the handler goroutine is still running. This is why
				// handlers must actually RESPECT ctx.Done() — a middleware
				// alone cannot stop them. In production prefer
				// http.TimeoutHandler, or a context-aware handler.
				writeJSON(w, http.StatusGatewayTimeout, map[string]string{"error": "timeout"})
			}
		})
	}
}

// ---------------------------------------------------------------------------
// HANDLERS
// ---------------------------------------------------------------------------

func healthHandler(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func meHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"user":      "ada",
		"requestID": RequestIDFrom(r.Context()), // read what the middleware set
	})
}

func boomHandler(http.ResponseWriter, *http.Request) {
	panic("something went very wrong")
}

func slowHandler(w http.ResponseWriter, r *http.Request) {
	select {
	case <-time.After(300 * time.Millisecond):
		writeJSON(w, http.StatusOK, map[string]string{"slept": "300ms"})
	case <-r.Context().Done(): // a well-behaved handler checks this
		return
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	// ORDER MATTERS: headers, then WriteHeader, then body. Setting a header
	// after WriteHeader is silently ignored — a very common bug.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ---------------------------------------------------------------------------

func main() {
	const apiKey = "secret-123"

	mux := http.NewServeMux()

	// PUBLIC routes: no auth.
	mux.HandleFunc("GET /health", healthHandler)

	// PROTECTED routes: wrap individual handlers with extra middleware.
	// Go 1.22+ ServeMux understands method + wildcard patterns.
	protected := APIKeyAuth(apiKey)
	mux.Handle("GET /me", protected(http.HandlerFunc(meHandler)))
	mux.Handle("GET /boom", protected(http.HandlerFunc(boomHandler)))
	mux.Handle("GET /slow", protected(http.HandlerFunc(slowHandler)))

	// GLOBAL middleware wraps the whole mux.
	// Order, outermost first:
	//   RequestID  first, so everything else can log the id
	//   Logging    outside Recover, so a panic still produces a log line
	//   Timeout    must be OUTSIDE Recover, because it spawns a goroutine
	//              and recover() is per-goroutine (see the warning above)
	//   Recover    innermost, on the same goroutine as the handler
	handler := Chain(mux,
		RequestID,
		Logging,
		Timeout(150*time.Millisecond),
		Recover,
	)

	srv := &http.Server{
		Addr:    "127.0.0.1:8099",
		Handler: handler,
		// ALWAYS set these. The zero value means "no timeout", which lets a
		// single slow client hold a connection open forever. This is the
		// most common Go production misconfiguration.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Start the server in the background so this lesson can drive it and
	// then exit. A real service just calls ListenAndServe and blocks
	// (see lesson 42 for the proper shutdown story).
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()
	time.Sleep(100 * time.Millisecond) // give it a moment to bind

	fmt.Println("== driving the middleware stack ==")
	fmt.Println("(log lines on the left come from the Logging middleware)")
	fmt.Println()

	client := &http.Client{Timeout: 3 * time.Second}
	for _, tc := range []struct {
		desc, path, key string
	}{
		{"public, no key needed", "/health", ""},
		{"protected, missing key", "/me", ""},
		{"protected, wrong key", "/me", "nope"},
		{"protected, good key", "/me", apiKey},
		{"handler panics", "/boom", apiKey},
		{"handler too slow", "/slow", apiKey},
	} {
		req, _ := http.NewRequest(http.MethodGet, "http://"+srv.Addr+tc.path, nil)
		if tc.key != "" {
			req.Header.Set("Authorization", "Bearer "+tc.key)
		}
		resp, err := client.Do(req)
		if err != nil {
			fmt.Printf("  %-24s ERROR %v\n", tc.desc, err)
			continue
		}
		var body map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		fmt.Printf("  %-24s %-8s -> %d %v\n", tc.desc, tc.path, resp.StatusCode, body)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	fmt.Println()
	fmt.Println("server stopped")
}

// ----------------------------------------------------------------------------
// RULES
//   1. Middleware is just `func(http.Handler) http.Handler`. No framework
//      needed, and third-party middleware (chi's, gorilla's) plugs straight
//      into a stdlib mux because everyone uses this same signature.
//   2. Order is semantics. RequestID -> Logging -> Timeout -> Recover ->
//      auth -> handler is a sane default. Recover goes INSIDE anything that
//      spawns a goroutine, because recover() is per-goroutine.
//   3. Short-circuit by NOT calling next.
//   4. Pass request-scoped data through the context, with an unexported key
//      type and a typed accessor.
//   5. Wrap ResponseWriter to observe the response — but if you need
//      Hijacker/Flusher/Pusher too, your wrapper must forward them
//      (http.ResponseController in Go 1.20+ makes this much easier).
//   6. Set the four Server timeouts. Every time.
//
// WHEN TO USE A ROUTER (chi, echo, gin) INSTEAD:
//   stdlib ServeMux since Go 1.22 handles methods (`GET /x`) and path
//   wildcards (`/users/{id}` -> r.PathValue("id")), which covers most
//   services. Reach for chi when you want per-group middleware and
//   sub-routers with less boilerplate; it's still plain http.Handler
//   underneath, so nothing in this lesson is wasted.
// ----------------------------------------------------------------------------
