package api

// ============================================================================
// LESSON 43: TESTING HTTP SERVICES
//
// This file is the CODE UNDER TEST — a small task API that ties together
// everything from section C: a repository behind an interface (37),
// middleware (39), slog (40) and JSON handlers (27).
//
// The tests live in api_test.go. Run them with:
//
//	go test ./43_testing_http/                 # run
//	go test -v ./43_testing_http/              # see each subtest
//	go test -race ./43_testing_http/           # with the race detector
//	go test -cover ./43_testing_http/          # coverage
//	go test -run TestCreateTask ./43_testing_http/   # just one test
//
// Note the package name is `api`, not `main` — like lesson 20, because
// `go test` operates on packages, and a package you can import is a package
// you can test.
// ============================================================================

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// domain
// ---------------------------------------------------------------------------

type Task struct {
	ID        int64     `json:"id"`
	Title     string    `json:"title"`
	Done      bool      `json:"done"`
	CreatedAt time.Time `json:"created_at"`
}

var (
	ErrNotFound = errors.New("task not found")
	ErrInvalid  = errors.New("invalid task")
)

// Store is the seam that makes the handlers testable. Tests inject a
// MemStore; production would inject the SQL repository from lesson 37.
type Store interface {
	Create(title string) (Task, error)
	Get(id int64) (Task, error)
	List() ([]Task, error)
	Delete(id int64) error
}

// ---------------------------------------------------------------------------
// an in-memory Store — used by the tests, and a perfectly good dev backend
// ---------------------------------------------------------------------------

type MemStore struct {
	mu     sync.RWMutex // handlers run concurrently; see -race (lesson 45)
	tasks  map[int64]Task
	nextID int64
	// FailNext lets a test force an unexpected error, so the 500 path gets
	// exercised too. Injecting failure is as important as injecting data.
	FailNext error
}

func NewMemStore() *MemStore {
	return &MemStore{tasks: map[int64]Task{}, nextID: 1}
}

func (s *MemStore) Create(title string) (Task, error) {
	if err := s.takeFailure(); err != nil {
		return Task{}, err
	}
	if title == "" {
		return Task{}, fmt.Errorf("%w: title is required", ErrInvalid)
	}
	if len(title) > 100 {
		return Task{}, fmt.Errorf("%w: title must be <= 100 chars", ErrInvalid)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	t := Task{ID: s.nextID, Title: title, CreatedAt: time.Now().UTC()}
	s.tasks[t.ID] = t
	s.nextID++
	return t, nil
}

func (s *MemStore) Get(id int64) (Task, error) {
	if err := s.takeFailure(); err != nil {
		return Task{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tasks[id]
	if !ok {
		return Task{}, fmt.Errorf("task %d: %w", id, ErrNotFound)
	}
	return t, nil
}

func (s *MemStore) List() ([]Task, error) {
	if err := s.takeFailure(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Task, 0, len(s.tasks)) // not nil -> encodes as [] not null
	for _, t := range s.tasks {
		out = append(out, t)
	}
	return out, nil
}

func (s *MemStore) Delete(id int64) error {
	if err := s.takeFailure(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tasks[id]; !ok {
		return fmt.Errorf("task %d: %w", id, ErrNotFound)
	}
	delete(s.tasks, id)
	return nil
}

func (s *MemStore) takeFailure() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.FailNext
	s.FailNext = nil // one-shot
	return err
}

// ---------------------------------------------------------------------------
// the server
// ---------------------------------------------------------------------------

type Server struct {
	store Store
	log   *slog.Logger
	mux   *http.ServeMux
}

// NewServer builds a fully wired http.Handler. Returning a Handler — rather
// than starting a server — is the single most important testability
// decision in this file: a test can call it with no port, no goroutine and
// no cleanup.
func NewServer(store Store, log *slog.Logger) *Server {
	s := &Server{store: store, log: log, mux: http.NewServeMux()}
	s.routes()
	return s
}

// ServeHTTP makes *Server itself an http.Handler (lesson 14), so it can be
// passed straight to httptest.NewServer or wrapped in middleware.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	// Go 1.22+ patterns: method + path wildcard, no router library needed.
	s.mux.HandleFunc("GET /health", s.handleHealth)
	s.mux.HandleFunc("GET /tasks", s.handleList)
	s.mux.HandleFunc("POST /tasks", s.handleCreate)
	s.mux.HandleFunc("GET /tasks/{id}", s.handleGet)
	s.mux.HandleFunc("DELETE /tasks/{id}", s.handleDelete)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleList(w http.ResponseWriter, _ *http.Request) {
	tasks, err := s.store.List()
	if err != nil {
		s.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tasks)
}

type createRequest struct {
	Title string `json:"title"`
}

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req createRequest

	// DisallowUnknownFields turns a typo'd field into a 400 instead of a
	// silently ignored one. http.MaxBytesReader caps the body so a huge
	// upload can't exhaust memory.
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
		return
	}

	t, err := s.store.Create(req.Title)
	if err != nil {
		s.writeError(w, err)
		return
	}
	w.Header().Set("Location", "/tasks/"+strconv.FormatInt(t.ID, 10))
	writeJSON(w, http.StatusCreated, t)
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	t, err := s.store.Get(id)
	if err != nil {
		s.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.store.Delete(id); err != nil {
		s.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent) // 204: no body
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64) // Go 1.22+ wildcards
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id must be an integer"})
		return 0, false
	}
	return id, true
}

// writeError is the ONE place that maps domain errors to status codes —
// exactly the payoff of lesson 35's typed errors and lesson 37's domain
// error translation.
func (s *Server) writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrInvalid):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	default:
		// Log the real error; return a generic message. Never leak internal
		// details (SQL, file paths, stack traces) to a client.
		s.log.Error("unhandled error", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
