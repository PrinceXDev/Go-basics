package api_test

// End-to-end tests for the capstone, against a REAL SQLite database and the
// real migrations — an integration test, not a unit test with fakes.
//
// Because the store uses the pure-Go driver and ":memory:", each test gets a
// fresh, real database in microseconds with no Docker and no cleanup. That
// combination is unusually good, and it's a strong argument for SQLite as
// your test database even when production runs Postgres. (The caveat:
// SQLite is not Postgres. Anything relying on Postgres-specific SQL still
// needs testcontainers.)
//
//	go test ./47_capstone/...
//	go test -race -v ./47_capstone/...
//
// Note the package name: `api_test`, not `api`. An EXTERNAL test package
// can only touch exported identifiers, which means these tests exercise the
// package exactly the way a real caller would — and can't accidentally
// depend on internals.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"example.com/go-basics/47_capstone/internal/api"
	"example.com/go-basics/47_capstone/internal/store"
)

const testKey = "test-key"

// newTestAPI builds the whole stack — migrated database, repository, server,
// full middleware chain — and returns a live test server.
func newTestAPI(t *testing.T) *httptest.Server {
	t.Helper()
	ctx := context.Background()

	db, err := store.Open(ctx, ":memory:", 1) // 1 conn: :memory: is per-connection
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if _, err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	srv := api.NewServer(api.Options{
		Repo:   store.NewSQLRepo(db),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		APIKey: testKey,
	})

	// srv.Handler() means the tests exercise the real middleware stack too:
	// request IDs, logging, timeout, recover, auth.
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// req performs an authenticated request and returns the response.
func req(t *testing.T, ts *httptest.Server, method, path, body string) *http.Response {
	t.Helper()
	return rawReq(t, ts, method, path, body, testKey)
}

func rawReq(t *testing.T, ts *httptest.Server, method, path, body, key string) *http.Response {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = bytes.NewBufferString(body)
	}
	request, err := http.NewRequest(method, ts.URL+path, r)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	if key != "" {
		request.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := ts.Client().Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func decode[T any](t *testing.T, resp *http.Response) T {
	t.Helper()
	var v T
	body, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode %T: %v\nbody: %s", v, err, body)
	}
	return v
}

func wantStatus(t *testing.T, resp *http.Response, want int) {
	t.Helper()
	if resp.StatusCode != want {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("%s %s: status = %d, want %d\nbody: %s",
			resp.Request.Method, resp.Request.URL.Path, resp.StatusCode, want, body)
	}
}

// ---------------------------------------------------------------------------

func TestProbesAreUnauthenticated(t *testing.T) {
	ts := newTestAPI(t)

	for _, path := range []string{"/healthz", "/readyz"} {
		t.Run(path, func(t *testing.T) {
			// No Authorization header at all.
			wantStatus(t, rawReq(t, ts, http.MethodGet, path, "", ""), http.StatusOK)
		})
	}
}

func TestAuth(t *testing.T) {
	ts := newTestAPI(t)

	tests := []struct {
		name, key  string
		wantStatus int
	}{
		{"no key", "", http.StatusUnauthorized},
		{"wrong key", "nope", http.StatusUnauthorized},
		{"correct key", testKey, http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			wantStatus(t, rawReq(t, ts, http.MethodGet, "/tasks", "", tc.key), tc.wantStatus)
		})
	}
}

func TestCreateValidation(t *testing.T) {
	tests := []struct {
		name, body string
		want       int
	}{
		{"valid", `{"title":"write the capstone"}`, http.StatusCreated},
		{"valid with all fields", `{"title":"ship","priority":1,"tag":"work"}`, http.StatusCreated},
		// 422 = well-formed JSON, but it breaks a business rule. 400 is for
		// "I can't even parse this". The distinction genuinely helps clients.
		{"empty title", `{"title":""}`, http.StatusUnprocessableEntity},
		{"whitespace title", `{"title":"   "}`, http.StatusUnprocessableEntity},
		{"title too long", `{"title":"` + strings.Repeat("x", 201) + `"}`, http.StatusUnprocessableEntity},
		{"priority too low", `{"title":"ok","priority":0}`, http.StatusUnprocessableEntity},
		{"priority too high", `{"title":"ok","priority":9}`, http.StatusUnprocessableEntity},
		{"malformed JSON", `{"title":`, http.StatusBadRequest},
		{"unknown field", `{"title":"ok","titel":"typo"}`, http.StatusBadRequest},
		{"two objects", `{"title":"a"}{"title":"b"}`, http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ts := newTestAPI(t) // fresh database per subtest
			resp := req(t, ts, http.MethodPost, "/tasks", tc.body)
			wantStatus(t, resp, tc.want)

			if tc.want == http.StatusCreated {
				task := decode[store.Task](t, resp)
				if task.ID == 0 {
					t.Error("expected a non-zero id")
				}
				if task.CreatedAt.IsZero() {
					t.Error("expected created_at to be set")
				}
				if resp.Header.Get("Location") == "" {
					t.Error("201 should set Location")
				}
			}
		})
	}
}

func TestDefaultPriority(t *testing.T) {
	ts := newTestAPI(t)
	resp := req(t, ts, http.MethodPost, "/tasks", `{"title":"no priority given"}`)
	wantStatus(t, resp, http.StatusCreated)
	if got := decode[store.Task](t, resp).Priority; got != 3 {
		t.Errorf("default priority = %d, want 3", got)
	}
}

func TestFullLifecycle(t *testing.T) {
	ts := newTestAPI(t)

	// CREATE
	resp := req(t, ts, http.MethodPost, "/tasks", `{"title":"original","priority":2,"tag":"work"}`)
	wantStatus(t, resp, http.StatusCreated)
	created := decode[store.Task](t, resp)
	path := fmt.Sprintf("/tasks/%d", created.ID)

	// READ
	resp = req(t, ts, http.MethodGet, path, "")
	wantStatus(t, resp, http.StatusOK)
	if got := decode[store.Task](t, resp); got.Title != "original" {
		t.Errorf("title = %q", got.Title)
	}

	// PATCH: only the supplied field changes.
	resp = req(t, ts, http.MethodPatch, path, `{"done":true}`)
	wantStatus(t, resp, http.StatusOK)
	patched := decode[store.Task](t, resp)
	if !patched.Done {
		t.Error("done should be true")
	}
	if patched.Title != "original" {
		t.Errorf("PATCH must not clear unspecified fields; title = %q", patched.Title)
	}
	if patched.Priority != 2 || patched.Tag != "work" {
		t.Errorf("PATCH clobbered other fields: %+v", patched)
	}
	if !patched.UpdatedAt.After(created.UpdatedAt) && !patched.UpdatedAt.Equal(created.UpdatedAt) {
		t.Error("updated_at should have moved forward")
	}

	// PATCH with an explicit false — the case a non-pointer field can't express.
	resp = req(t, ts, http.MethodPatch, path, `{"done":false}`)
	wantStatus(t, resp, http.StatusOK)
	if decode[store.Task](t, resp).Done {
		t.Error(`{"done":false} must actually set done to false`)
	}

	// PATCH with nothing to do.
	wantStatus(t, req(t, ts, http.MethodPatch, path, `{}`), http.StatusBadRequest)

	// DELETE
	resp = req(t, ts, http.MethodDelete, path, "")
	wantStatus(t, resp, http.StatusNoContent)

	// ...and it's gone.
	wantStatus(t, req(t, ts, http.MethodGet, path, ""), http.StatusNotFound)
	wantStatus(t, req(t, ts, http.MethodDelete, path, ""), http.StatusNotFound)
}

func TestListFilteringAndPagination(t *testing.T) {
	ts := newTestAPI(t)

	seed := []string{
		`{"title":"a","priority":1,"tag":"work"}`,
		`{"title":"b","priority":2,"tag":"work"}`,
		`{"title":"c","priority":3,"tag":"home"}`,
		`{"title":"d","priority":4,"tag":"home"}`,
		`{"title":"e","priority":5,"tag":""}`,
	}
	for _, s := range seed {
		wantStatus(t, req(t, ts, http.MethodPost, "/tasks", s), http.StatusCreated)
	}
	// Mark one done so the done filter has something to find.
	wantStatus(t, req(t, ts, http.MethodPatch, "/tasks/1", `{"done":true}`), http.StatusOK)

	type listResp struct {
		Tasks  []store.Task `json:"tasks"`
		Total  int          `json:"total"`
		Limit  int          `json:"limit"`
		Offset int          `json:"offset"`
	}

	tests := []struct {
		name, query        string
		wantLen, wantTotal int
	}{
		{"all", "", 5, 5},
		{"done", "?done=true", 1, 1},
		{"not done", "?done=false", 4, 4},
		{"by tag", "?tag=work", 2, 2},
		{"by max priority", "?max_priority=2", 2, 2},
		{"combined", "?tag=work&done=false", 1, 1},
		{"page 1", "?limit=2", 2, 5}, // total is the FULL count, not the page
		{"page 2", "?limit=2&offset=2", 2, 5},
		{"page 3", "?limit=2&offset=4", 1, 5},
		{"past the end", "?offset=99", 0, 5},
		{"no matches", "?tag=nonexistent", 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := req(t, ts, http.MethodGet, "/tasks"+tc.query, "")
			wantStatus(t, resp, http.StatusOK)
			got := decode[listResp](t, resp)
			if len(got.Tasks) != tc.wantLen {
				t.Errorf("len(tasks) = %d, want %d", len(got.Tasks), tc.wantLen)
			}
			if got.Total != tc.wantTotal {
				t.Errorf("total = %d, want %d", got.Total, tc.wantTotal)
			}
		})
	}

	t.Run("empty result is [] not null", func(t *testing.T) {
		resp := req(t, ts, http.MethodGet, "/tasks?tag=nonexistent", "")
		body, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(body), `"tasks":[]`) {
			t.Errorf("want an empty array, got: %s", body)
		}
	})

	t.Run("page size is capped", func(t *testing.T) {
		// A client asking for 10,000 rows must not get them.
		resp := req(t, ts, http.MethodGet, "/tasks?limit=10000", "")
		wantStatus(t, resp, http.StatusOK)
		if got := decode[listResp](t, resp); got.Limit > 100 {
			t.Errorf("limit = %d, want it capped", got.Limit)
		}
	})
}

func TestBadRequests(t *testing.T) {
	ts := newTestAPI(t)

	tests := []struct {
		name, method, path string
		want               int
	}{
		{"non-numeric id", http.MethodGet, "/tasks/abc", http.StatusBadRequest},
		{"negative id", http.MethodGet, "/tasks/-1", http.StatusBadRequest},
		{"zero id", http.MethodGet, "/tasks/0", http.StatusBadRequest},
		{"missing task", http.MethodGet, "/tasks/9999", http.StatusNotFound},
		{"bad done filter", http.MethodGet, "/tasks?done=maybe", http.StatusBadRequest},
		{"bad limit", http.MethodGet, "/tasks?limit=lots", http.StatusBadRequest},
		{"unknown route", http.MethodGet, "/nope", http.StatusNotFound},
		{"wrong method", http.MethodPut, "/tasks", http.StatusMethodNotAllowed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			wantStatus(t, req(t, ts, tc.method, tc.path, ""), tc.want)
		})
	}
}

func TestRequestIDIsEchoed(t *testing.T) {
	ts := newTestAPI(t)

	t.Run("generated when absent", func(t *testing.T) {
		resp := req(t, ts, http.MethodGet, "/healthz", "")
		if resp.Header.Get("X-Request-ID") == "" {
			t.Error("expected a generated X-Request-ID")
		}
	})

	t.Run("honoured when supplied", func(t *testing.T) {
		request, _ := http.NewRequest(http.MethodGet, ts.URL+"/healthz", nil)
		request.Header.Set("X-Request-ID", "trace-abc-123")
		resp, err := ts.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if got := resp.Header.Get("X-Request-ID"); got != "trace-abc-123" {
			t.Errorf("X-Request-ID = %q, want the one we sent", got)
		}
	})
}

// TestConcurrentWrites exercises the whole stack under concurrency. Run it
// with -race: it covers the pool, the middleware's atomic counter, and the
// transaction in Update all at once.
func TestConcurrentWrites(t *testing.T) {
	ts := newTestAPI(t)

	const n = 30
	var wg sync.WaitGroup
	errCh := make(chan string, n)

	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := fmt.Sprintf(`{"title":"concurrent %d","priority":%d}`, i, i%5+1)
			resp := req(t, ts, http.MethodPost, "/tasks", body)
			if resp.StatusCode != http.StatusCreated {
				errCh <- fmt.Sprintf("task %d: status %d", i, resp.StatusCode)
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for msg := range errCh {
		t.Error(msg)
	}

	resp := req(t, ts, http.MethodGet, "/tasks?limit=100", "")
	var got struct {
		Total int `json:"total"`
	}
	body, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Total != n {
		t.Errorf("total = %d, want %d", got.Total, n)
	}
}

func TestErrorResponsesDoNotLeakInternals(t *testing.T) {
	ts := newTestAPI(t)

	resp := req(t, ts, http.MethodGet, "/tasks/9999", "")
	wantStatus(t, resp, http.StatusNotFound)

	body, _ := io.ReadAll(resp.Body)
	for _, forbidden := range []string{"SELECT", "sqlite", "database/sql", "goroutine", "D:\\"} {
		if strings.Contains(string(body), forbidden) {
			t.Errorf("error body leaked %q: %s", forbidden, body)
		}
	}
	// It should still be a usable, structured error.
	if !strings.Contains(string(body), `"error"`) {
		t.Errorf("expected an error field, got: %s", body)
	}
}
