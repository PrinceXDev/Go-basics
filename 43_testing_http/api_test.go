package api

// ============================================================================
// TESTING HTTP HANDLERS
//
// There are two ways to test an HTTP handler in Go, and you want both:
//
//   1. httptest.NewRecorder  — calls ServeHTTP DIRECTLY. No network, no
//      port, microseconds per test. Use this for 95% of handler tests.
//
//   2. httptest.NewServer    — starts a REAL server on a random localhost
//      port. Slower, but it exercises the actual HTTP stack: status lines,
//      headers, timeouts, redirects, and real http.Client behaviour. Use
//      it for end-to-end tests and for testing CLIENTS.
//
// Everything here is stdlib. Go's testing package plus httptest genuinely
// removes the need for supertest/nock/msw.
// ============================================================================

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// TEST HELPERS
// A helper that calls t.Helper() reports failures at the CALLER's line
// number, which is the difference between "helpers.go:42 failed" and
// "api_test.go:118 failed". Always call it.
// ---------------------------------------------------------------------------

// newTestServer builds a Server with a discard logger so test output stays
// readable. Returning both lets a test seed or inspect the store.
func newTestServer(t *testing.T) (*Server, *MemStore) {
	t.Helper()
	store := NewMemStore()
	// slog.DiscardHandler (Go 1.24+) throws logs away. Before that:
	// slog.New(slog.NewTextHandler(io.Discard, nil)).
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewServer(store, log), store
}

// do performs a request against the handler IN PROCESS and returns the
// recorded response. This is the workhorse of handler testing.
func do(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r) // never fails; panics on a bad URL
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder() // an http.ResponseWriter that records
	h.ServeHTTP(rec, req)         // <- the entire "network layer"
	return rec
}

// decode unmarshals a response body into T, failing the test on bad JSON.
// Generics (lesson 21) make this reusable across every response type.
func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %T: %v\nbody was: %s", v, err, rec.Body.String())
	}
	return v
}

func assertStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Errorf("status = %d, want %d\nbody: %s", rec.Code, want, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// 1. THE SIMPLEST HANDLER TEST
// ---------------------------------------------------------------------------

func TestHealth(t *testing.T) {
	srv, _ := newTestServer(t)

	rec := do(t, srv, http.MethodGet, "/health", "")

	assertStatus(t, rec, http.StatusOK)

	// Assert on headers too — a JSON API that forgets Content-Type breaks
	// browser clients, and no handler test catches it unless you look.
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	got := decode[map[string]string](t, rec)
	if got["status"] != "ok" {
		t.Errorf("status field = %q, want ok", got["status"])
	}
}

// ---------------------------------------------------------------------------
// 2. TABLE-DRIVEN TESTS (the idiomatic Go pattern, from lesson 20)
// One test function, many cases, each as its own named subtest.
// ---------------------------------------------------------------------------

func TestCreateTask(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantErrSub string // substring expected in the error message
	}{
		{
			name:       "valid task",
			body:       `{"title":"write tests"}`,
			wantStatus: http.StatusCreated,
		},
		{
			name:       "empty title",
			body:       `{"title":""}`,
			wantStatus: http.StatusBadRequest,
			wantErrSub: "title is required",
		},
		{
			name:       "title too long",
			body:       `{"title":"` + strings.Repeat("x", 101) + `"}`,
			wantStatus: http.StatusBadRequest,
			wantErrSub: "100 chars",
		},
		{
			name:       "malformed JSON",
			body:       `{"title":`,
			wantStatus: http.StatusBadRequest,
			wantErrSub: "invalid JSON",
		},
		{
			name:       "unknown field is rejected",
			body:       `{"title":"ok","titel":"typo"}`,
			wantStatus: http.StatusBadRequest,
			wantErrSub: "unknown field",
		},
		{
			name:       "empty body",
			body:       ``,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		// t.Run creates a SUBTEST: it gets its own name in -v output, can
		// be selected with -run 'TestCreateTask/empty_title', and a
		// failure in one doesn't stop the others.
		t.Run(tc.name, func(t *testing.T) {
			// A fresh server per subtest = no shared state, so subtests
			// can't affect each other and could run in parallel.
			srv, _ := newTestServer(t)

			rec := do(t, srv, http.MethodPost, "/tasks", tc.body)

			assertStatus(t, rec, tc.wantStatus)

			if tc.wantErrSub != "" {
				got := decode[map[string]string](t, rec)
				if !strings.Contains(got["error"], tc.wantErrSub) {
					t.Errorf("error = %q, want it to contain %q", got["error"], tc.wantErrSub)
				}
			}

			if tc.wantStatus == http.StatusCreated {
				task := decode[Task](t, rec)
				if task.ID == 0 {
					t.Error("expected a non-zero ID")
				}
				if task.Done {
					t.Error("a new task should not be done")
				}
				if loc := rec.Header().Get("Location"); loc == "" {
					t.Error("201 response should set a Location header")
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 3. TESTING THE FULL CRUD LIFECYCLE
// Some behaviour only shows up in sequence. One test, ordered subtests,
// shared state — the deliberate exception to the "fresh state" rule.
// ---------------------------------------------------------------------------

func TestTaskLifecycle(t *testing.T) {
	srv, _ := newTestServer(t)
	var created Task

	t.Run("create", func(t *testing.T) {
		rec := do(t, srv, http.MethodPost, "/tasks", `{"title":"ship it"}`)
		assertStatus(t, rec, http.StatusCreated)
		created = decode[Task](t, rec)
	})

	t.Run("get", func(t *testing.T) {
		rec := do(t, srv, http.MethodGet, fmt.Sprintf("/tasks/%d", created.ID), "")
		assertStatus(t, rec, http.StatusOK)
		if got := decode[Task](t, rec); got.Title != "ship it" {
			t.Errorf("title = %q, want %q", got.Title, "ship it")
		}
	})

	t.Run("list contains it", func(t *testing.T) {
		rec := do(t, srv, http.MethodGet, "/tasks", "")
		assertStatus(t, rec, http.StatusOK)
		if got := decode[[]Task](t, rec); len(got) != 1 {
			t.Errorf("len(tasks) = %d, want 1", len(got))
		}
	})

	t.Run("delete", func(t *testing.T) {
		rec := do(t, srv, http.MethodDelete, fmt.Sprintf("/tasks/%d", created.ID), "")
		assertStatus(t, rec, http.StatusNoContent)
		if rec.Body.Len() != 0 {
			t.Errorf("204 must have an empty body, got %q", rec.Body.String())
		}
	})

	t.Run("get after delete is 404", func(t *testing.T) {
		rec := do(t, srv, http.MethodGet, fmt.Sprintf("/tasks/%d", created.ID), "")
		assertStatus(t, rec, http.StatusNotFound)
	})

	t.Run("empty list encodes as [] not null", func(t *testing.T) {
		rec := do(t, srv, http.MethodGet, "/tasks", "")
		// A classic JSON-API bug: a nil slice marshals to `null`, which
		// breaks clients doing `data.map(...)`. Assert on the raw bytes.
		if body := strings.TrimSpace(rec.Body.String()); body != "[]" {
			t.Errorf("empty list = %s, want []", body)
		}
	})
}

// ---------------------------------------------------------------------------
// 4. TESTING ERROR PATHS
// The 500 path is the one nobody tests, and the one that pages you at 3am.
// Injecting a failure into the fake store is how you reach it.
// ---------------------------------------------------------------------------

func TestStoreFailureBecomes500(t *testing.T) {
	srv, store := newTestServer(t)
	store.FailNext = errors.New("connection reset by peer")

	rec := do(t, srv, http.MethodGet, "/tasks", "")

	assertStatus(t, rec, http.StatusInternalServerError)

	// The internal message must NOT leak to the client.
	body := rec.Body.String()
	if strings.Contains(body, "connection reset") {
		t.Errorf("internal error leaked to the client: %s", body)
	}
}

func TestBadPathParams(t *testing.T) {
	srv, _ := newTestServer(t)

	for _, path := range []string{"/tasks/abc", "/tasks/1.5", "/tasks/-"} {
		t.Run(path, func(t *testing.T) {
			rec := do(t, srv, http.MethodGet, path, "")
			assertStatus(t, rec, http.StatusBadRequest)
		})
	}
}

func TestMethodNotAllowed(t *testing.T) {
	srv, _ := newTestServer(t)

	// Go 1.22+ ServeMux returns 405 automatically when the path matches
	// but the method doesn't — behaviour worth pinning down with a test.
	rec := do(t, srv, http.MethodPut, "/tasks", "")
	assertStatus(t, rec, http.StatusMethodNotAllowed)
}

// ---------------------------------------------------------------------------
// 5. httptest.NewServer — a REAL server over a real socket
// Use this when you need genuine client behaviour, or to test a CLIENT.
// ---------------------------------------------------------------------------

func TestEndToEndWithRealServer(t *testing.T) {
	srv, _ := newTestServer(t)

	ts := httptest.NewServer(srv) // binds 127.0.0.1 on a random free port
	// t.Cleanup runs at the end of THIS test, even if it fails or panics.
	// It's clearer than defer when the setup lives in a helper.
	t.Cleanup(ts.Close)

	client := ts.Client() // a client preconfigured for this server (TLS etc.)
	client.Timeout = 5 * time.Second

	// POST a task over a real socket.
	resp, err := client.Post(ts.URL+"/tasks", "application/json",
		bytes.NewBufferString(`{"title":"end to end"}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 201; body: %s", resp.StatusCode, b)
	}

	var task Task
	if err := json.NewDecoder(resp.Body).Decode(&task); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if task.Title != "end to end" {
		t.Errorf("title = %q", task.Title)
	}
}

// TestClientAgainstFakeUpstream flips the perspective: here httptest is
// standing in for a THIRD-PARTY API so we can test our own client code
// without touching the network. This replaces nock/msw.
func TestClientAgainstFakeUpstream(t *testing.T) {
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		// Assert on what OUR client sent.
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want Bearer test-key", got)
		}
		if got := r.URL.Query().Get("q"); got != "golang" {
			t.Errorf("query q = %q, want golang", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"results":["a","b","c"]}`)
	}))
	t.Cleanup(upstream.Close)

	// The client takes a base URL — which is precisely what makes it
	// testable. A client that hardcodes its endpoint cannot be tested.
	results, err := searchUpstream(upstream.URL, "test-key", "golang")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("len(results) = %d, want 3", len(results))
	}
	if calls != 1 {
		t.Errorf("upstream called %d times, want 1", calls)
	}
}

// searchUpstream is the client under test. Note baseURL as a parameter.
func searchUpstream(baseURL, apiKey, query string) ([]string, error) {
	req, err := http.NewRequest(http.MethodGet, baseURL+"/search?q="+query, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream returned %d", resp.StatusCode)
	}
	var out struct {
		Results []string `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return out.Results, nil
}

// ---------------------------------------------------------------------------
// 6. PARALLEL TESTS AND THE RACE DETECTOR
// ---------------------------------------------------------------------------

// t.Parallel() makes subtests run concurrently. It speeds up slow suites
// AND, under -race, actively hunts for data races in your handlers.
func TestConcurrentCreates(t *testing.T) {
	srv, store := newTestServer(t)

	const n = 50
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := do(t, srv, http.MethodPost, "/tasks", fmt.Sprintf(`{"title":"task %d"}`, i))
			if rec.Code != http.StatusCreated {
				t.Errorf("status = %d", rec.Code)
			}
		}(i)
	}
	wg.Wait()

	// Without MemStore's mutex, `go test -race ./43_testing_http/` reports
	// a data race here — try deleting the lock and running it.
	tasks, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != n {
		t.Errorf("created %d tasks, want %d", len(tasks), n)
	}
}

// ---------------------------------------------------------------------------
// 7. BENCHMARK (a preview of lesson 44)
// ---------------------------------------------------------------------------

func BenchmarkCreateTask(b *testing.B) {
	store := NewMemStore()
	srv := NewServer(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	body := `{"title":"benchmark"}`

	b.ReportAllocs()
	for b.Loop() { // Go 1.24+; before that: for i := 0; i < b.N; i++
		req := httptest.NewRequest(http.MethodPost, "/tasks", strings.NewReader(body))
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
	}
}

// ----------------------------------------------------------------------------
// RULES
//   1. Return an http.Handler from a constructor; never start a server in
//      code you want to test.
//   2. httptest.NewRecorder for handler tests (fast), httptest.NewServer
//      for end-to-end and for testing clients.
//   3. Table-driven tests with t.Run subtests. Name each case in words.
//   4. Every test gets fresh state. t.Cleanup for teardown.
//   5. Inject the store as an interface. Inject failures too, so the 500
//      path is covered.
//   6. Assert on status, headers AND body. Assert that internal errors
//      DON'T leak.
//   7. t.Error keeps going (report several problems); t.Fatal stops (the
//      rest of the test would be meaningless). Use t.Fatal after a failed
//      decode, t.Error for individual field mismatches.
//   8. Failure messages should read "got X, want Y". No assertion library
//      required — though github.com/google/go-cmp is worth adding for
//      deep struct comparison (cmp.Diff gives you a readable diff).
//   9. Run -race in CI. Always.
//
// WHAT'S NOT HERE
//   Integration tests against a REAL database. Two good options:
//     * SQLite in memory: sql.Open("sqlite", ":memory:") + the migrations
//       from lesson 37 in TestMain. Fast, no Docker, but not Postgres.
//     * testcontainers-go: spins up a real Postgres in Docker per test
//       package. Slower, but tests the database you actually deploy.
//   Use TestMain(m *testing.M) to set up and tear down either one once per
//   package.
// ----------------------------------------------------------------------------
