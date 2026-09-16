# 47 — Capstone: a production-shaped Go service

Everything from lessons 01–46, assembled into one service you could actually
deploy. A tasks API with authentication, a real database, versioned
migrations, structured logging, graceful shutdown, an integration test
suite, and a container image.

```bash
go run ./47_capstone/cmd/api
```

```bash
go test -race ./47_capstone/...
```

---

## Layout

```
47_capstone/
├── cmd/api/main.go              entry point: config → deps → serve → shutdown
├── internal/
│   ├── config/config.go         defaults ← env ← flags, validated (L41)
│   ├── store/
│   │   ├── store.go             domain types, errors, repository (L35,36,37)
│   │   └── migrations/*.sql     versioned, embedded in the binary (L37)
│   └── api/
│       ├── server.go            routing, JSON helpers, error mapping (L27,43)
│       ├── handlers.go          one handler per endpoint
│       ├── middleware.go        requestID, logging, timeout, recover, auth (L39,40)
│       └── api_test.go          integration tests over a real DB (L43)
├── Dockerfile                   multi-stage, distroless, non-root (L46)
├── Makefile                     run / test / lint / docker
└── README.md
```

Why this shape (lesson 29, applied):

- **`cmd/api`** — one directory per binary. Add `cmd/worker` later and it
  shares every `internal/` package.
- **`internal/`** — the Go compiler *enforces* that nothing outside this
  module can import these packages. Your API surface stays deliberate.
- **Dependencies point inward.** `api` imports `store`; `store` imports
  nothing of ours. There are no import cycles, and no package reaches back
  up to `main`.
- **No global state.** Everything is constructed in `run()` and passed down.
  That single rule is what makes the whole thing testable.

---

## The API

Everything under `/tasks` needs `Authorization: Bearer <APP_API_KEY>`
(default `dev-key`). The probes are deliberately unauthenticated.

| method | path | status | notes |
|---|---|---|---|
| `GET` | `/healthz` | 200 | liveness — checks nothing external |
| `GET` | `/readyz` | 200 / 503 | readiness — *does* check the database |
| `GET` | `/tasks` | 200 | `?done=&tag=&max_priority=&limit=&offset=` |
| `POST` | `/tasks` | 201 | sets `Location` |
| `GET` | `/tasks/{id}` | 200 / 404 | |
| `PATCH` | `/tasks/{id}` | 200 / 404 | partial update |
| `DELETE` | `/tasks/{id}` | 204 / 404 | |

Try it:

```bash
curl -s -X POST localhost:8080/tasks \
  -H "Authorization: Bearer dev-key" -H "Content-Type: application/json" \
  -d '{"title":"first task","priority":1,"tag":"work"}'
```

```bash
curl -s "localhost:8080/tasks?done=false&limit=5" -H "Authorization: Bearer dev-key"
```

```bash
curl -s -X PATCH localhost:8080/tasks/1 \
  -H "Authorization: Bearer dev-key" -H "Content-Type: application/json" \
  -d '{"done":true}'
```

### Status codes, deliberately

- **400** — I can't parse this (malformed JSON, unknown field, bad `id`)
- **401** — missing or wrong API key
- **404** — no such task
- **405** — path exists, wrong method (stdlib `ServeMux` does this for free)
- **422** — well-formed, but breaks a business rule (empty title, priority 9)
- **504** — the request exceeded `APP_REQUEST_TIMEOUT`

Splitting 400 from 422 tells a client whether to fix its serialisation or
fix its data. Worth the two extra lines.

---

## Configuration

Defaults ← environment ← flags, validated at startup. The service refuses to
boot on bad config rather than failing on the first request.

| variable | flag | default | |
|---|---|---|---|
| `APP_ENV` | `-env` | `dev` | `dev\|staging\|prod` |
| `APP_LOG_LEVEL` | `-log-level` | `info` | text handler in dev, JSON otherwise |
| `APP_ADDR` | `-addr` | `127.0.0.1:8080` | use `0.0.0.0:8080` in a container |
| `APP_DB_PATH` | `-db` | `tasks.db` | `:memory:` works too |
| `APP_DB_MAX_CONNS` | | `10` | |
| `APP_API_KEY` | | `dev-key` | **rejected in prod** |
| `APP_REQUEST_TIMEOUT` | | `5s` | |
| `APP_SHUTDOWN_TIMEOUT` | | `15s` | must be under the platform's grace period |

`APP_API_KEY` is typed as `config.Secret`, which returns `[REDACTED]` from
its `String()` — so the startup log line prints the whole config safely.

---

## Which lesson shows up where

| lesson | in the capstone |
|---|---|
| 11–15 structs, methods, interfaces, embedding | `store.Task`, `Repository`, `statusRecorder` embedding `http.ResponseWriter` |
| 16 JSON | struct tags on `Task`, the `listResponse` envelope |
| 21 generics | `decodeJSON[T]`, the test's `decode[T]` |
| 22–25 concurrency | server goroutine, `atomic.Int64` counter, `signal.NotifyContext` |
| 29 project structure | `cmd/` + `internal/` |
| 32 struct tags | JSON tags; validation lives in the store instead of a library |
| 33 time | `UpdatedAt`, timeouts, `time.ParseDuration` config |
| 34 stdlib | `slices.Sort` over migration names, `strings.TrimSpace` |
| 35 errors | `ErrNotFound`/`ErrInvalid`, `%w`, `errors.Is`, `errors.Join` in config |
| 36 database/sql | pool config, `QueryRowContext`, `rows.Err()`, transactions |
| 37 migrations + repository | `//go:embed migrations/*.sql`, `Repository` interface |
| 39 middleware | the full chain, ordered correctly |
| 40 slog | request-scoped logger on the context, JSON in prod |
| 41 config | `internal/config` end to end |
| 42 graceful shutdown | `run()` returns an error; shutdown order is server → deps |
| 43 testing | `httptest` over a real migrated database |
| 45 race/lint | `go test -race` passes; `.golangci.yml` at the repo root |
| 46 docker | multi-stage distroless build that runs the tests |

---

## Details worth stealing

**`main` is four lines.** All the work is in `run() error`, so every `defer`
actually executes. `log.Fatal` calls `os.Exit`, which skips defers — that's
how database connections get leaked on shutdown.

**`PATCH` uses pointer fields.**

```go
type updateTaskRequest struct {
    Done *bool `json:"done"`
}
```

With a plain `bool` you cannot distinguish "field omitted" from
`"done": false`, so a client can never un-complete a task. This is the same
gotcha as validator's `required` in lesson 32, and it's the single most
common bug in hand-written PATCH endpoints.

**Domain errors are translated once**, in `writeStoreError`. Add a sentinel
to the store, add a case here, and every endpoint gets the right status code.
No handler imports `database/sql`.

**The page-size cap is one exported rule** (`store.ClampLimit`). An earlier
version had the store capping silently while the handler echoed back the
client's requested `limit` — the test suite caught it. Shared policy belongs
in one exported place.

**Empty lists encode as `[]`.** `make([]Task, 0, n)` rather than
`var tasks []Task`, because a nil slice marshals to `null` and breaks every
client doing `data.map(...)`.

**`Recover` sits inside `Timeout`.** `Timeout` spawns a goroutine, and
`recover()` only catches panics on its own goroutine. Get this backwards and
a panicking handler takes down the whole process. (Lesson 39 has the crash
transcript.)

---

## Tests

```bash
go test -race -v ./47_capstone/...
```

They're **integration tests**: real SQLite, real migrations, real middleware
chain, over a real socket via `httptest.NewServer`. Because the driver is
pure Go and the database is `:memory:`, the whole suite runs in under two
seconds with no Docker and no cleanup.

They cover: auth, every validation rule, the full CRUD lifecycle, PATCH
merge semantics (including explicit `false`), filtering, pagination and its
cap, malformed input, request-ID propagation, 30 concurrent writers, and an
assertion that error bodies never leak SQL or file paths.

The test package is `api_test`, not `api` — an external test package can
only reach exported identifiers, so it exercises the package the way a real
caller would.

---

## Container

```bash
docker build -f 47_capstone/Dockerfile -t tasks-api:dev .
docker run --rm -p 8080:8080 -v tasksdata:/data \
  -e APP_API_KEY=change-me tasks-api:dev
```

Multi-stage, distroless, non-root, static binary, tests run during the
build. The final image is roughly 10 MB. It only works because
`modernc.org/sqlite` is pure Go — a CGO driver would force a glibc base.

---

## What a real service would add next

This is a genuine foundation, not a finished product. In rough priority
order:

1. **Real auth** — JWT or OIDC instead of a shared API key; per-user data.
2. **Postgres** — swap the driver and the placeholder style; `SQLRepo` is
   the only file that changes. Add `testcontainers-go` for tests.
3. **Metrics and tracing** — OpenTelemetry, `/metrics` for Prometheus. The
   request ID is already there to correlate with traces.
4. **Rate limiting** — `golang.org/x/time/rate` as another middleware.
5. **CORS** — if a browser will call it.
6. **OpenAPI** — generate the spec, or generate the handlers from it.
7. **CI** — the pipeline from `45_race_detector_and_linting/README.md`.
8. **Optimistic concurrency** — `ErrConflict` is declared but unused; wire
   it to a `version` column and an `If-Match` header.

That last one is left deliberately unfinished. It's a good first exercise
on your own code.
