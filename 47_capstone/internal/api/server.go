// Package api is the HTTP layer: routing, request decoding, response
// encoding, and the mapping from domain errors to status codes. It knows
// nothing about SQL.
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"example.com/go-basics/47_capstone/internal/store"
)

type Server struct {
	repo   store.Repository // the INTERFACE — tests inject a fake
	log    *slog.Logger
	apiKey string
	mux    *http.ServeMux

	reqTimeout time.Duration
	started    time.Time
}

type Options struct {
	Repo           store.Repository
	Logger         *slog.Logger
	APIKey         string
	RequestTimeout time.Duration
}

// NewServer returns a fully wired http.Handler. It does NOT listen — that's
// main's job, and keeping them separate is what makes this testable with
// httptest (lesson 43).
func NewServer(o Options) *Server {
	if o.RequestTimeout == 0 {
		o.RequestTimeout = 5 * time.Second
	}
	s := &Server{
		repo: o.Repo, log: o.Logger, apiKey: o.APIKey,
		mux: http.NewServeMux(), reqTimeout: o.RequestTimeout,
		started: time.Now(),
	}
	s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// Handler wraps the routes in the global middleware stack. Order matters:
// RequestID -> Logging -> Timeout -> Recover -> handler. Recover is INSIDE
// Timeout because Timeout spawns a goroutine (lesson 39).
func (s *Server) Handler() http.Handler {
	return Chain(s,
		RequestID,
		Logging(s.log),
		Timeout(s.reqTimeout),
		Recover,
	)
}

func (s *Server) routes() {
	// Public: no auth. Probes must work without credentials.
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
	s.mux.HandleFunc("GET /readyz", s.handleReadyz)

	// Protected: everything under /tasks.
	auth := APIKeyAuth(s.apiKey)
	protect := func(h http.HandlerFunc) http.Handler { return auth(h) }

	s.mux.Handle("GET /tasks", protect(s.handleList))
	s.mux.Handle("POST /tasks", protect(s.handleCreate))
	s.mux.Handle("GET /tasks/{id}", protect(s.handleGet))
	s.mux.Handle("PATCH /tasks/{id}", protect(s.handleUpdate))
	s.mux.Handle("DELETE /tasks/{id}", protect(s.handleDelete))
}

// ---------------------------------------------------------------------------
// response helpers
// ---------------------------------------------------------------------------

// errorBody is the ONE error shape this API ever returns. A consistent
// error envelope is worth more to clients than clever per-endpoint formats.
type errorBody struct {
	Error   string            `json:"error"`
	Details map[string]string `json:"details,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	// Order matters: headers, then WriteHeader, then body. A header set
	// after WriteHeader is silently dropped.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func writeError(w http.ResponseWriter, status int, msg string, details map[string]string) {
	writeJSON(w, status, errorBody{Error: msg, Details: details})
}

// writeStoreError is the single place domain errors become status codes.
// Add a new sentinel to the store and you extend the API here, once.
func (s *Server) writeStoreError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error(), nil)
	case errors.Is(err, store.ErrInvalid):
		writeError(w, http.StatusUnprocessableEntity, err.Error(), nil)
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, err.Error(), nil)
	case errors.Is(err, r.Context().Err()) && r.Context().Err() != nil:
		// The client hung up, or the Timeout middleware fired. Nothing to
		// send, and it is not a server fault.
		LoggerFrom(r.Context()).Warn("request cancelled", "err", err)
	default:
		// Log the detail; return a generic message. Never leak SQL, file
		// paths or stack traces to a client.
		LoggerFrom(r.Context()).Error("unhandled error", "err", err)
		writeError(w, http.StatusInternalServerError, "internal server error", nil)
	}
}

// decodeJSON reads a size-limited, strict JSON body.
func decodeJSON[T any](w http.ResponseWriter, r *http.Request) (T, error) {
	var v T
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)) // 1 MiB cap
	dec.DisallowUnknownFields()                                   // a typo'd field is a 400, not a silent no-op
	if err := dec.Decode(&v); err != nil {
		return v, err
	}
	// Reject trailing content, so `{"a":1}{"b":2}` isn't quietly accepted.
	if dec.More() {
		return v, errors.New("body must contain a single JSON object")
	}
	return v, nil
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64) // Go 1.22+ wildcards
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, "id must be a positive integer", nil)
		return 0, false
	}
	return id, true
}

// queryBool parses an optional ?key=true|false into a *bool: nil means
// "the client didn't ask", which is different from "the client asked for
// false".
func queryBool(r *http.Request, key string) (*bool, error) {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return nil, nil
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		return nil, errors.New(key + " must be true or false")
	}
	return &b, nil
}

func queryInt(r *http.Request, key string) (*int, error) {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return nil, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return nil, errors.New(key + " must be an integer")
	}
	return &n, nil
}
