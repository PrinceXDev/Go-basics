# Pulse — Go Learning Capstone Plan

> A small, production-style uptime & latency monitor.
> Next.js + TypeScript → Go REST API → PostgreSQL → background worker pool → external HTTP targets.

---

## Part A — Candidate projects

### 1. Pulse — Uptime & Latency Monitor  ⭐ (selected)
1. **Overview.** Users register URLs ("monitors") with an interval. A background scheduler enqueues due
   checks; a worker pool performs HTTP probes concurrently; results are persisted; state transitions
   (up→down) open and close incidents. The API serves monitors, check history and incidents.
2. **Real problem.** Knowing when your site/API goes down, and how slow it is, without paying for
   Pingdom/BetterUptime.
3. **Core features.** CRUD monitors; scheduled probes; check history; uptime % + p95 latency;
   incident open/close; "check now"; optional webhook notification.
4. **Go concepts.** Everything on your list: packages, structs, methods, interfaces (prober,
   repository, notifier), sentinel + wrapped errors, pointers, worker-pool goroutines, buffered job
   channel, unbuffered result channel, `WaitGroup` on shutdown drain, `RWMutex` over a status cache,
   `context` for probe timeouts + shutdown cancellation, `net/http` server, middleware, JSON,
   constructor DI, `pgxpool`, transactions, graceful shutdown, `slog`, table-driven + `httptest` tests.
5. **Next.js concepts.** App Router, server components for first paint, client components with polling
   (SWR), route handlers as a thin proxy, forms + validation, charts, optimistic UI.
6. **PostgreSQL concepts.** FKs, enums/CHECK constraints, composite + partial indexes, a time-series
   table with retention, aggregate queries (`percentile_cont`, `FILTER`), transactions, pooling.
7. **Difficulty.** Moderate — the concurrency is genuinely required, not decorative.
8. **Time.** ~25–35 focused hours (10 milestones).

### 2. Dispatch — Webhook Delivery Service
1. **Overview.** Accept events over HTTP, store them in an outbox, deliver to subscriber endpoints with
   retries, exponential backoff and a dead-letter queue.
2. **Real problem.** Reliable at-least-once webhook delivery — every SaaS needs it and most get it wrong.
3. **Core features.** Register subscriptions; ingest events; delivery attempts with backoff; DLQ; replay;
   HMAC signing.
4. **Go concepts.** Similar coverage to Pulse, plus `crypto/hmac`; the retry/backoff state machine is a
   nice `time.Timer` + `select` exercise. Weaker on read/query APIs.
5. **Next.js.** Subscription CRUD, delivery log table, replay button. Less visual.
6. **PostgreSQL.** `SELECT ... FOR UPDATE SKIP LOCKED` (excellent, but advanced), JSONB payloads,
   partial indexes on pending rows.
7. **Difficulty.** Moderate-high — `SKIP LOCKED` + idempotency is a lot on top of new Go concepts.
8. **Time.** ~35–45 hours.

### 3. Ledger — Bulk CSV Import & Reconciliation
1. **Overview.** Upload a CSV of transactions; a background job parses and validates rows concurrently,
   inserts them in one transaction per batch, and reports per-row errors and live progress.
2. **Real problem.** Every back-office tool needs "import this spreadsheet and tell me what broke".
3. **Core features.** Upload; async job with progress; per-row error report; download of rejected rows;
   idempotent re-import.
4. **Go concepts.** Streaming `encoding/csv` over `io.Reader`, fan-out/fan-in, `errgroup`, context
   cancellation of an in-flight import, mutex-guarded progress counter, transactions. Weaker on:
   scheduling, long-lived workers, external services.
5. **Next.js.** File upload, progress polling, error table. Fewer screens.
6. **PostgreSQL.** `COPY`, batch inserts, unique constraints for idempotency, savepoints.
7. **Difficulty.** Moderate.
8. **Time.** ~20–28 hours.

### 4. Feedly-lite — Feed Aggregator
1. **Overview.** Subscribe to RSS feeds; a scheduler fetches them concurrently; items are deduped and
   shown in a reader with read/unread state.
2. **Real problem.** Personal news aggregation without a third-party service.
3. **Core features.** Feed CRUD, scheduled fetch, dedupe, read/unread, search.
4. **Go concepts.** Good: worker pool, context, XML/JSON parsing, fetcher interface. Weaker: no
   meaningful state machine, transactions feel bolted on.
5. **Next.js.** List/detail reading UI, infinite scroll, optimistic read-marking. Nicest frontend.
6. **PostgreSQL.** Full-text search (`tsvector`), unique dedupe index.
7. **Difficulty.** Low-moderate.
8. **Time.** ~20–25 hours.

### 5. Shortly — URL Shortener with Async Analytics
1. **Overview.** Short links plus a click pipeline: redirects write to a buffered channel, a background
   flusher batches inserts, an aggregator rolls up daily stats.
2. **Real problem.** Trackable short links where the redirect must stay fast under load.
3. **Core features.** Create/list links, redirect, click analytics, expiry.
4. **Go concepts.** The best *natural* buffered-channel story (drop-on-full backpressure), batching with
   `select` + ticker, mutex-guarded counters, benchmarks. Weaker on context cancellation, per-item error
   handling, transactions.
5. **Next.js.** Link table + one chart. Small.
6. **PostgreSQL.** Batch insert, rollup tables, `ON CONFLICT DO UPDATE`.
7. **Difficulty.** Low-moderate.
8. **Time.** ~18–22 hours.

### Why Pulse wins
It is the only candidate where **every** concept on your list is load-bearing: a scheduler and a worker
pool are the obvious design (goroutines); a job queue that must absorb bursts wants a *buffered* channel;
a single DB writer wants an *unbuffered* rendezvous; shutdown must drain in-flight probes (`WaitGroup` +
`context`); the API must read live status written by workers (`RWMutex`); recording a check and flipping
a monitor's state must be atomic (transaction); and the probe target is a real external service.
It also stays small: 4 tables, ~11 endpoints, 4 screens.

**Concepts deliberately NOT forced in**
- *Generics* — the repositories are concrete; a generic `Repository[T]` would be worse Go here. One small
  exception is fine: a `ptr[T](v T) *T` helper for optional JSON fields.
- *Embedding* — used only where real (a `deps` struct shared by handlers). `context.Context` as a struct
  field is an anti-pattern and is avoided.
- *`sync.Map` / `sync/atomic`* — contention here is tiny; a plain `RWMutex` is correct and clearer. Note
  where `atomic.Int64` would be right (a pure counter) without adopting it everywhere.
- *Elaborate fan-in / many-case `select`* — used once in the worker loop ("job or shutdown"), not sprinkled.

---

## Part B — The selected project

## 1. Product definition
- **Name:** Pulse
- **Problem:** Small teams don't know their endpoints are down until a customer tells them.
- **Target users:** A solo dev or small team with 5–50 HTTP endpoints.
- **Core workflow:** Authenticate with an API key → add a monitor (URL, method, interval, expected
  status, timeout) → Pulse probes it on schedule → dashboard shows current status, uptime % and p95
  latency → after N consecutive failures an incident opens; on recovery it closes.
- **MVP scope (do not exceed):**
  - IN: monitor CRUD, scheduled probing, check history (7-day retention), incidents, uptime/latency
    stats, "check now", API-key auth, one optional outgoing webhook notification.
  - OUT: multi-user orgs, email/SMS, public status pages, SSL-expiry checks, multi-region probing, cron
    expressions, WebSockets/SSE (the UI polls).

## 2. Architecture

```text
┌──────────────────────────────────────────────────────────────┐
│  Next.js (App Router, TS)                                     │
│  Dashboard · Monitor detail · Create/Edit form · Incidents     │
└───────────────┬───────────────────────────────────────────────┘
                │ JSON over HTTP  (X-API-Key)
┌───────────────▼───────────────────────────────────────────────┐
│  Go REST API  (net/http + chi)                                │
│  middleware: requestID → logging → recover → auth → timeout    │
│  handlers → services → repositories                           │
└───────┬──────────────────────────────┬────────────────────────┘
        │                              │  shares repos + status cache
┌───────▼──────────────┐   ┌───────────▼────────────────────────┐
│ PostgreSQL (pgxpool) │   │ Background workers (same process)  │
│ monitors/checks/...  │◄──┤ scheduler → [buffered jobs chan]   │
└──────────────────────┘   │   → worker pool → [results chan]   │
                           │   → recorder (tx write)            │
                           └───────────┬────────────────────────┘
                                       │ HTTP probe / notify
                           ┌───────────▼────────────────────────┐
                           │ External services                  │
                           │ monitored endpoints, webhook sink   │
                           └────────────────────────────────────┘
```

**Why each component exists**
- **Next.js** — the user-facing surface; also proves the API is genuinely usable over the wire (CORS,
  status codes, error shape).
- **Go API** — synchronous, latency-sensitive. It never performs a probe inline (even "check now" only
  *enqueues*).
- **PostgreSQL** — durable state and aggregate queries; the boundary that makes transactions meaningful.
- **Background workers** — probing is slow network I/O and periodic. Doing it in the request path would
  be wrong; this is the honest reason the project needs concurrency.
- **External services** — the thing being measured. Real timeouts, real failures, real flakiness.

**Deployment shape:** one binary, two subsystems (`-mode=all`), with the worker behind a flag so you can
later run `-mode=api` and `-mode=worker` separately. That constraint is *why* the status cache must be
optional and the DB must remain the source of truth.

## 3. Go architecture

```text
pulse/
├── cmd/
│   └── pulse/main.go            # flags/env → run() → wire deps → serve → shutdown
├── internal/
│   ├── config/config.go         # Config struct, Load() with env + defaults + validation
│   ├── domain/                  # pure types, imports nothing from other internal packages
│   │   ├── monitor.go           # Monitor, Status, Check, Incident + methods
│   │   └── errors.go            # ErrNotFound, ErrConflict, ValidationError
│   ├── storage/
│   │   ├── postgres.go          # pgxpool setup, Ping, WithTx helper
│   │   ├── monitor_repo.go      # MonitorRepository implementation
│   │   ├── check_repo.go        # CheckRepository (+ stats queries)
│   │   ├── incident_repo.go
│   │   └── migrations/*.sql
│   ├── service/
│   │   ├── monitor_service.go   # business rules, orchestrates repos + transactions
│   │   └── stats_service.go
│   ├── prober/
│   │   ├── prober.go            # Prober interface + HTTPProber
│   │   └── prober_test.go       # httptest-based
│   ├── worker/
│   │   ├── scheduler.go         # ticker → due monitors → jobs chan
│   │   ├── pool.go              # N workers, WaitGroup, ctx
│   │   ├── recorder.go          # single consumer of results, tx write, state machine
│   │   └── status_cache.go      # RWMutex-guarded map
│   ├── notify/notifier.go       # Notifier interface, WebhookNotifier, NoopNotifier
│   ├── httpapi/
│   │   ├── server.go            # router, deps struct, Routes()
│   │   ├── middleware.go        # requestID, logging, recover, auth, timeout, CORS
│   │   ├── monitors.go          # handlers
│   │   ├── request.go / response.go  # decode/encode + error→status mapping
│   │   └── *_test.go
│   └── platform/logger.go       # slog setup
└── web/                         # Next.js app
```

- **Package responsibilities.** `domain` knows nothing about HTTP or SQL. `storage` knows SQL. `service`
  knows rules. `httpapi` knows HTTP. `worker` orchestrates. Dependencies point inward only — if `domain`
  ever imports `storage`, the design has broken.
- **Interfaces** (defined by the *consumer*, kept small, each with a second implementation in tests):
  ```go
  type MonitorRepository interface {
      Create(ctx context.Context, m *domain.Monitor) error
      GetByID(ctx context.Context, id uuid.UUID) (*domain.Monitor, error)
      ClaimDue(ctx context.Context, limit int) ([]domain.Monitor, error)
      // ...
  }
  type Prober   interface { Probe(ctx context.Context, m domain.Monitor) domain.Check }
  type Notifier interface { Notify(ctx context.Context, ev domain.IncidentEvent) error }
  ```
- **Structs & pointers.** Value semantics by default (`domain.Check` travels through channels — small and
  immutable). Pointers where identity or mutation matters (`*Monitor` from repos, `nil` = absent).
  Pointer receivers only on methods that mutate.
- **Dependency injection.** Plain constructor injection, no framework: `storage.New(pool)` →
  `service.New(repos, logger)` → `httpapi.New(services, cfg, logger)`. `main.go` is the only place that
  knows the concrete types.
- **Error handling.** Sentinel errors in `domain`, wrapped with `fmt.Errorf("...: %w", err)` at each
  boundary, inspected with `errors.Is/As`. One place — `httpapi/response.go` — maps errors to status
  codes, so handlers never choose error status codes themselves.
- **Middleware.** `func(http.Handler) http.Handler`, chained in a fixed order; the request ID lives in
  the request context and is attached to every log line.
- **Services vs repositories.** Repositories = one query each, no rules. Services = rules + transactions.
  Handlers = decode, call, encode.
- **Workers.** Scheduler (producer), pool (N consumers), recorder (single writer). All take a `ctx` and
  return when it's cancelled.

## 4. Concurrency — why each primitive is there

| Primitive | Where | Problem it solves |
|---|---|---|
| **Goroutines** | 1 scheduler + N probe workers + 1 recorder | A probe blocks on network I/O for up to 10s. 50 monitors probed serially can't hold a 30s interval; probed concurrently the batch takes as long as the slowest one. |
| **Buffered channel** | `jobs chan domain.Monitor` (cap ≈ 4×workers) | Absorbs the burst when many monitors come due on the same tick, so the scheduler isn't blocked by a slow worker. A **full** buffer is a real signal: the scheduler sends with a non-blocking `select` and on `default` logs "queue full, skipping tick" — that is backpressure, not a dropped requirement. |
| **Unbuffered channel** | `results chan domain.Check` | Exactly one recorder goroutine writes to the DB, so state transitions are serialized without a lock. Unbuffered = rendezvous: a worker blocks until the recorder takes the result, so a DB slowdown throttles probing instead of silently growing an in-memory backlog of unwritten results. |
| **`sync.WaitGroup`** | around the worker pool | Graceful shutdown must not kill an in-flight probe mid-write. Shutdown = cancel ctx → close `jobs` → `wg.Wait()` → close `results` → recorder drains → exit. The WaitGroup is what makes "close `results` exactly once, after the last sender is gone" safe. |
| **Mutex (`sync.RWMutex`)** | `status_cache.go` | The dashboard endpoint is hit far more often than checks are written. A map of `monitorID → LiveStatus` written by one goroutine and read by many handlers is a data race without a lock; `RWMutex` is right because reads dominate. (A pure counter would use `atomic.Int64`; a map cannot.) |
| **Context cancellation** | probe timeout, shutdown, request timeout | `context.WithTimeout` per probe is the only correct way to bound an HTTP call. The same root ctx cancels scheduler and workers on SIGTERM, and request-scoped ctx propagates into `pgx` so a client disconnect cancels the query. |
| **`select`** | worker loop, scheduler loop | `select { case job, ok := <-jobs: …; case <-ctx.Done(): return }` — the standard "do work or give up" shape. |

Anti-patterns to consciously avoid (and be able to explain): `go f()` with no way to wait for it; a
channel where a mutex is simpler; `time.Sleep` as synchronization; `context.Context` in a struct field;
closing a channel from the receiving side.

## 5. Database

```sql
CREATE TYPE monitor_status AS ENUM ('unknown','up','down','paused');

CREATE TABLE users (
  id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  email        TEXT NOT NULL UNIQUE,
  api_key_hash TEXT NOT NULL,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE monitors (
  id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id              UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name                 TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
  url                  TEXT NOT NULL,
  method               TEXT NOT NULL DEFAULT 'GET' CHECK (method IN ('GET','HEAD','POST')),
  interval_secs        INT  NOT NULL CHECK (interval_secs BETWEEN 30 AND 3600),
  timeout_secs         INT  NOT NULL DEFAULT 10 CHECK (timeout_secs BETWEEN 1 AND 30),
  expected_status      INT  NOT NULL DEFAULT 200,
  failure_threshold    INT  NOT NULL DEFAULT 2 CHECK (failure_threshold BETWEEN 1 AND 10),
  status               monitor_status NOT NULL DEFAULT 'unknown',
  consecutive_failures INT  NOT NULL DEFAULT 0,
  last_checked_at      TIMESTAMPTZ,
  next_check_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (user_id, name)
);
-- the scheduler's only hot query: "what is due?"
CREATE INDEX idx_monitors_due ON monitors (next_check_at) WHERE status <> 'paused';

CREATE TABLE checks (
  id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  monitor_id  UUID NOT NULL REFERENCES monitors(id) ON DELETE CASCADE,
  checked_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  success     BOOLEAN NOT NULL,
  status_code INT,
  latency_ms  INT NOT NULL CHECK (latency_ms >= 0),
  error       TEXT
);
-- every history/stats query is (monitor, most recent first)
CREATE INDEX idx_checks_monitor_time ON checks (monitor_id, checked_at DESC);

CREATE TABLE incidents (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  monitor_id  UUID NOT NULL REFERENCES monitors(id) ON DELETE CASCADE,
  started_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  resolved_at TIMESTAMPTZ,
  cause       TEXT NOT NULL
);
-- at most one open incident per monitor — enforced by the DB, not by Go
CREATE UNIQUE INDEX idx_incidents_one_open ON incidents (monitor_id) WHERE resolved_at IS NULL;
```

- **Relationships.** users 1—N monitors 1—N checks, monitors 1—N incidents. `ON DELETE CASCADE` makes
  deleting a monitor one statement.
- **Constraints do real work.** The partial unique index makes "two open incidents" impossible even if
  the Go state machine has a bug. Lesson: push invariants into the database.
- **The transaction that matters.** Recording a result is three statements that must be atomic:
  `INSERT INTO checks` → `UPDATE monitors SET status, consecutive_failures, next_check_at` →
  `INSERT/UPDATE incidents`. Implement `storage.WithTx(ctx, pool, func(tx pgx.Tx) error { ... })` so the
  commit/rollback logic exists exactly once.
- **Claiming due work.** `UPDATE monitors SET next_check_at = now() + make_interval(secs => interval_secs)
  WHERE id IN (SELECT id FROM monitors WHERE next_check_at <= now() AND status <> 'paused'
  ORDER BY next_check_at LIMIT $1 FOR UPDATE SKIP LOCKED) RETURNING *` — one statement, still correct if
  you later run two worker processes.
- **Connection pooling.** `pgxpool` with `MaxConns` sized to *workers + API concurrency*, plus `MinConns`,
  `MaxConnLifetime`, `MaxConnIdleTime`, `HealthCheckPeriod`. Lesson: the pool is a shared finite resource
  — 20 workers against `MaxConns=5` gives you queueing you can actually measure.
- **Retention.** A daily `DELETE FROM checks WHERE checked_at < now() - interval '7 days'` in the janitor
  goroutine keeps the table small.

## 6. REST API

Base `/api/v1`. Auth: `X-API-Key` header. Content type `application/json`.
One error envelope everywhere:
```json
{ "error": { "code": "validation_failed", "message": "...", "fields": { "url": "must be http or https" } } }
```

| # | Method | Endpoint | Request | Success | Validation / errors |
|---|---|---|---|---|---|
| 1 | POST | `/monitors` | `{name,url,method?,interval_secs,timeout_secs?,expected_status?,failure_threshold?}` | 201 `Monitor` | name 1–100; url must parse, scheme http/https, non-empty host, reject private/loopback IPs; interval 30–3600 → 400; duplicate name → 409 |
| 2 | GET | `/monitors` | `?status=&limit=&offset=` | 200 `{items,total}` | limit ≤ 100, default 20 → 400 |
| 3 | GET | `/monitors/{id}` | – | 200 `Monitor` + live status | bad UUID → 400; another user's → 404 (not 403 — don't leak existence) |
| 4 | PATCH | `/monitors/{id}` | partial (`*string`, `*int` fields) | 200 `Monitor` | same rules; empty body → 400 |
| 5 | DELETE | `/monitors/{id}` | – | 204 | 404 |
| 6 | POST | `/monitors/{id}/pause` · `/resume` | – | 200 | 409 if already in that state |
| 7 | POST | `/monitors/{id}/check-now` | – | 202 `{"queued":true}` | 429 if queue full or re-triggered within 10s |
| 8 | GET | `/monitors/{id}/checks` | `?limit=&since=` | 200 `{items}` | limit ≤ 500 |
| 9 | GET | `/monitors/{id}/stats` | `?window=24h\|7d` | 200 `{uptime_pct,avg_ms,p95_ms,total}` | unknown window → 400 |
| 10 | GET | `/monitors/{id}/incidents` | `?open=true` | 200 `{items}` | |
| 11 | GET | `/healthz` · `/readyz` | – | 200 / 503 | readyz pings the pool; no auth |

Rules you'll enforce in code: validate shape in the handler and rules in the service; never return a raw
`err.Error()` to the client; always set a request timeout; log in middleware, not in handlers.

## 7. Frontend (Next.js + TypeScript)

| Screen | Route | Data | Talks to |
|---|---|---|---|
| Dashboard | `/` | monitor cards: name, status pill, 24h uptime, last latency | `GET /monitors` server-side for first paint, then client polling every 15s |
| Monitor detail | `/monitors/[id]` | latency sparkline (last 100 checks), uptime, incidents, Check now / Pause | `GET /monitors/{id}`, `/checks`, `/stats`, `/incidents`; `POST /check-now` |
| Create / Edit | `/monitors/new`, `/monitors/[id]/edit` | form | `POST` / `PATCH`, renders `error.fields` inline |
| Incidents | `/incidents` | open + recent incidents | `GET /monitors/{id}/incidents` |

- One typed API client (`lib/api.ts`) plus `lib/types.ts` mirroring the Go JSON tags — this is where
  you'll feel whether your JSON contract is clean.
- The API key lives in a server-side env var; browser calls go through Next route handlers
  (`app/api/.../route.ts`) so the key never reaches the client. That is also your CORS answer.
- Polling, not WebSockets: correct for 15s-granularity data and it keeps the Go side simple.

## 8. Development roadmap

| Phase | Build | Go concepts | Files | Definition of done |
|---|---|---|---|---|
| **1. Setup** | module, folder skeleton, `config.Load()`, `slog` logger, Makefile, `docker-compose` with Postgres | packages/modules, structs, env parsing, errors | `cmd/pulse`, `internal/config`, `internal/platform` | `go run ./cmd/pulse` logs a structured startup line and exits cleanly on Ctrl-C |
| **2. Database** | migrations, `pgxpool`, `Ping`, `WithTx` helper | pointers, error wrapping, `defer` | `internal/storage/postgres.go`, `migrations/` | `make migrate-up` creates all 4 tables; a ping check passes |
| **3. Go fundamentals** | `domain` types + methods (`Monitor.IsDue()`, `Check.Failed()`), repositories behind interfaces, fake repo | structs, methods, interfaces, sentinel errors, table-driven tests | `internal/domain`, `internal/storage/*_repo.go` | repo tests pass against a real Postgres |
| **4. REST API** | server, router, handlers 1–6 + healthz, middleware chain, error mapping | `net/http`, JSON, DI, middleware, closures | `internal/httpapi/*` | full CRUD via curl; every error path returns the envelope |
| **5. Concurrency** | prober, scheduler, buffered `jobs`, worker pool + WaitGroup, unbuffered `results`, recorder, status cache | goroutines, channels, `select`, `WaitGroup`, `RWMutex`, `context` | `internal/prober`, `internal/worker/*` | `go test -race ./...` clean; N monitors probed concurrently, rows land in `checks` |
| **6. Background workers** | incident state machine in a tx, notifier interface, janitor (retention), `check-now` enqueue, graceful shutdown | transactions, interfaces, signal handling | `internal/worker/recorder.go`, `internal/notify` | SIGTERM finishes in-flight probes, commits, closes the pool, exits 0 within 5s |
| **7. Frontend** | 4 screens, typed client, route handlers | (JSON contract feedback) | `web/` | you can add a monitor and watch it go down when you stop the target |
| **8. Testing** | domain table tests, `httptest` handler tests, fake prober, repo tests on a test DB, a race test for the cache | testing, interfaces as seams, `t.Cleanup` | `*_test.go` | `go test -race -cover ./...` ≥ 70% on `domain`/`service`/`httpapi` |
| **9. Docker** | multi-stage Dockerfile, compose for api+db+web, `.env.example` | build flags, `-ldflags` version | `Dockerfile`, `docker-compose.yml` | `docker compose up` gives a working stack from scratch |
| **10. Production readiness** | request IDs in logs, panic recovery, timeouts everywhere, pool tuning, `golangci-lint`, README | profiling, linting | repo-wide | lint clean, no goroutine leaks under `-race`, README explains every design decision |

## 9. Learning-mode milestones

Each milestone: concept → mental model → tiny example → your task → expected behaviour → hints on
request → review of your code → production-quality notes.

- **M1** Skeleton, config, logger, clean exit — *packages, structs, errors, `slog`*
- **M2** Postgres + migrations + pool + `WithTx` — *pointers, `defer`, wrapping*
- **M3** Domain types & methods — *structs, methods, value vs pointer receivers*
- **M4** Repositories behind interfaces + fakes — *interfaces, DI, table-driven tests*
- **M5** HTTP server, handlers, JSON, error envelope — *net/http, encoding/json*
- **M6** Middleware chain + auth + request IDs — *closures, context values*
- **M7** Prober + context timeouts — *context, interfaces, httptest*
- **M8** Scheduler + buffered jobs + worker pool + WaitGroup — *goroutines, channels, select*
- **M9** Recorder, unbuffered results, transaction, incident state machine, status cache — *mutex, tx*
- **M10** Graceful shutdown, janitor, notifier, Docker, hardening — *signals, lifecycle*
- **M11–M12** Next.js frontend, once the API contract is frozen.
