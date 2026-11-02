# Fixed Window Counter Rate Limiter — Plan & Design

Status: PLAN ONLY. No code is written until the plan is understood and approved.

## 1. The use case

**Multi-tenant SaaS API quotas.** Think of a service like Stripe, GitHub or Twilio:
many customers (tenants) share one API. Each tenant pays for a plan, and each plan
has a request budget. One noisy tenant must never slow down the others.

Example limits (all configurable):

| Scope    | Who is counted         | Example limit        | Protects against                    |
|----------|------------------------|----------------------|-------------------------------------|
| tenant   | whole company          | 100 requests / min   | one customer eating shared capacity |
| api_key  | one integration/script | 40 requests / min    | a buggy script in an infinite loop  |
| user     | one human              | 20 requests / min    | one person hammering the API        |

A request is allowed only if **all three** scopes still have budget.

## 2. Open-source reference: Kong (rate-limiting plugin)

Kong is an API gateway. Its `rate-limiting` plugin is a fixed window counter and
offers three storage modes ("policies"):

- `local`   — counter in the gateway's own memory (fast, not shared between nodes)
- `cluster` — counter in the database (Postgres) shared by all nodes
- `redis`   — counter in Redis shared by all nodes

Related projects: Envoy's `ratelimit` service (Lyft) uses fixed windows in Redis;
GitHub's public API uses an hourly fixed window with `X-RateLimit-*` headers.

What we learn from Kong:
1. Counter key = (who, window start). A new window is simply a new row. No reset job.
2. Increment must be one atomic database statement (`INSERT ... ON CONFLICT DO UPDATE`).
3. Storage is a swappable "policy" -> hide storage behind a small Go interface.
4. Response headers are part of the product, not an afterthought.

How ours differs: Kong is a generic gateway plugin. Ours is a small Go service with
middleware, multi-scope checks in one transaction, and a learning-sized codebase.
(Details of Kong's internals are from memory; we will not copy its code.)

## 3. The algorithm

Time is cut into fixed buckets. Each bucket has its own counter.

    window = 60s            limit = 5

    12:00:00 ──────────── 12:01:00 ──────────── 12:02:00
    |  bucket A: count 0→5 |  bucket B: count 0→...  |
    req1 req2 req3 req4 req5 | req6 ...
                      req6 at 12:00:59 → 429 (A is full)
                      req7 at 12:01:00 → allowed (B is new, count=1)

    window_start = floor(now / window) * window     (pure arithmetic)

Known weakness (the "boundary burst"): 5 requests at 12:00:59 + 5 at 12:01:01 =
10 requests in 2 seconds, although the limit is 5/min. That is the price for
being simple and cheap. Sliding window / token bucket fix it; we only document it.

## 4. Postgres alone vs Redis

| Question                 | Postgres only                         | Redis                                  |
|--------------------------|---------------------------------------|----------------------------------------|
| Atomic increment         | `INSERT ON CONFLICT DO UPDATE` (row lock) | `INCR` (single-threaded, very fast) |
| Speed                    | ~ thousands/sec per hot row, ms latency | 100k+/sec, sub-ms                    |
| Expiry of old windows    | needs a cleanup query/job             | `EXPIRE` built in                      |
| Multi-scope all-or-nothing | real transactions, trivial          | needs Lua script or MULTI              |
| Durability               | yes                                   | usually weaker                         |
| Extra thing to run       | no (we already need Postgres)         | yes                                    |
| Hot-key contention       | all requests of one tenant queue on one row | none practical                    |

Can Postgres alone do it? **Yes**, correctly, including across many app servers,
because the database is the single shared place and the upsert is atomic.

Why companies add Redis: at tens of thousands of requests per second, the
limiter runs on *every* request and a database write per request becomes the
bottleneck.

**Recommendation: Postgres only for v1.** Reasons: one fewer moving part, real
transactions, easy to inspect with SQL while learning, and the limiter code sits
behind an interface (`Store`) so a Redis implementation can be added later without
touching the middleware. Redis becomes a "Phase 2 / when you measure the need" topic.

## 5. Components and request lifecycle

    Client ──HTTP──▶ Router ──▶ [Auth middleware] ──▶ [RateLimit middleware] ──▶ Handler
                                  (reads headers:        │  allowed                (/orders etc.)
                                   X-API-Key, X-User-ID, │
                                   X-Tenant-ID)          ▼
                                                     Limiter (service)
                                                       │ 1. now → window_start
                                                       │ 2. for each scope: rule lookup
                                                       ▼
                                                     Store (Postgres)
                                                       one transaction:
                                                       upsert counter per scope

    Rejected → 429 + Retry-After + X-RateLimit-* headers, handler never runs.

Package responsibilities (kept deliberately small):

| Package                  | Job                                                        |
|--------------------------|------------------------------------------------------------|
| `internal/config`        | read env vars, plan→limits table                           |
| `internal/limiter`       | pure logic: window math, decision (allowed/remaining/reset) |
| `internal/store`         | Postgres access: the atomic upsert                         |
| `internal/middleware`    | HTTP glue: extract identity, call limiter, write headers   |
| `cmd/api`                | wiring + a demo endpoint                                   |

## 6. Data model (one table)

    rate_limit_counters
    ─────────────────────────────────────────────
    scope_type    text         -- 'tenant' | 'api_key' | 'user'
    scope_key     text         -- e.g. 'acme', 'key_123'
    window_start  timestamptz  -- start of the bucket
    request_count integer      -- requests seen in that bucket
    PRIMARY KEY (scope_type, scope_key, window_start)

The primary key is what makes concurrency safe: two simultaneous first requests
cannot both insert; one inserts, the other updates.

Core statement (taught in step 3):

    INSERT INTO rate_limit_counters (...) VALUES (..., 1)
    ON CONFLICT (scope_type, scope_key, window_start)
    DO UPDATE SET request_count = rate_limit_counters.request_count + 1
    RETURNING request_count;

Design choice to discuss: this counts *rejected* requests too. The alternative is
`... DO UPDATE ... WHERE request_count < $limit` which stops counting at the limit
(no row returned = blocked). Trade-off: simpler vs. more accurate counts.

Limits live in config (YAML/env), not the database, for v1: less code, no admin API.

## 7. Technical decisions and trade-offs

| Decision                              | Why                                         | Cost                                  |
|---------------------------------------|---------------------------------------------|---------------------------------------|
| Fixed window (not sliding/token)      | simplest, one row per window, easy to reason about | boundary burst up to 2x limit  |
| DB time (`now()` in SQL) vs app time  | avoids clock drift between app servers      | one extra concept                     |
| Single transaction for all scopes     | a denied request must not consume tenant budget | slightly longer lock time         |
| Fail-open vs fail-closed on DB error  | choose per product; we pick **fail-open** and log loudly | brief unprotected period  |
| Plain `database/sql` + `pgx`, no ORM  | you see the real SQL                        | a little more boilerplate             |
| Interface `Store`, nothing else abstracted | enables unit tests and later Redis     | one interface only, no "factory" layers |

## 8. Concurrency and distributed deployment

- 100 simultaneous requests for one tenant: Postgres row lock serialises the
  upserts, so the count is exact. No Go mutex needed (and a mutex would not help
  across multiple servers anyway).
- Many app instances: all share the same table, so the limit is global.
- Deadlocks: when locking several scopes in one transaction, always lock in the
  same order (tenant → api_key → user).
- Old rows grow forever: a cleanup `DELETE ... WHERE window_start < now() - interval`.
- DB down: fail-open (decision above), log the error, optionally add a metric.

## 9. Step-by-step plan (each step small, runnable, tested)

| Step | What we build                                  | You learn                                   | How we test            |
|------|------------------------------------------------|---------------------------------------------|------------------------|
| 1    | Docker Compose Postgres + migration + config   | schema design, env config                   | `psql` query           |
| 2    | `limiter` package: window math + decision      | the algorithm, pure functions, unit tests   | `go test`              |
| 3    | `store`: atomic upsert (single scope)          | `ON CONFLICT`, atomicity                    | integration test       |
| 4    | Middleware + `429`, `Retry-After`, headers     | HTTP contract                               | Postman                |
| 5    | Multi-scope (tenant + key + user) in one tx    | transactions, lock order, all-or-nothing    | integration test       |
| 6    | Concurrency test (100 goroutines) + DB failure | race conditions, fail-open                  | `go test -race`        |
| 7    | Cleanup job + Postman collection polish + README | production concerns                      | Postman runner         |

Folder layout (trimmed from your suggestion — no `pkg/`, no `tests/` split until needed):

    system-design/fixed-window-counter/
      README.md
      docker-compose.yml
      .env.example
      cmd/api/main.go
      internal/{config,limiter,store,middleware}/
      migrations/
      postman/
      docs/

Integration tests live next to the code they test (`store/store_test.go`) because
Go convention keeps tests beside packages; we add a top-level `tests/` only if needed.

## 10. Out of scope (on purpose)

Redis, sliding window, token bucket, admin UI, plan billing, real authentication
(we trust identity headers; a real system derives them from a verified token).
