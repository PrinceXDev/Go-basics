# Interview Prep: Fixed Window Rate Limiter (Go + PostgreSQL)

How to use: read section 1 aloud until it feels natural, then skim the Q&A.
Status tags: [built] is in the code now, [planned] is a later step, [theory] is only discussed.

---

## 1. The 60-second pitch

> "I built a rate limiter for a multi-tenant SaaS API in Go with PostgreSQL.
> It uses the fixed window counter algorithm: time is cut into fixed buckets, like one minute,
> and each tenant, API key and user has a counter per bucket. Each request increments the
> counter with one atomic SQL upsert, so it's correct even with many servers and concurrent requests.
> If the count is over the limit, we return 429 with Retry-After headers.
> I also added fault tolerance: timeouts, retries with backoff, a circuit breaker, and a
> configurable fail-open or fail-closed policy for when the database is down."

---

## 2. Why rate limit at all?

- Protect shared capacity: one noisy customer must not slow down the others.
- Stop abuse and runaway scripts.
- Control cost of expensive endpoints.
- Enforce plans (free = 100 req/min, paid = more).

---

## 3. The algorithm [built]

Time is cut into fixed buckets. Each bucket has its own counter.

```
window = 60s, limit = 5

12:00:00 ───────────── 12:01:00 ─────────────
| count: 1 2 3 4 5 | 6th = 429 | new bucket, count restarts
```

```go
// which bucket am I in? (pure arithmetic, no timers, no reset job)
secs := now.Unix()
windowStart := time.Unix(secs-secs%windowSeconds, 0)
```

A new window is just a new database row, so nothing has to "reset" the counter.

**Boundary burst (the classic weakness):** 5 requests at 12:00:59 and 5 more at 12:01:00
are all allowed. That is 10 requests in 2 seconds with a limit of 5 per minute.
Worst case is up to 2x the limit.

### Compare with other algorithms

| Algorithm | Idea | Pros | Cons |
|---|---|---|---|
| Fixed window | One counter per time bucket | Simplest, cheap, 1 row per window | Boundary burst (2x) |
| Sliding window log | Store every request timestamp | Exact | Memory heavy |
| Sliding window counter | Weighted mix of this and previous window | Smooth, cheap | Approximate |
| Token bucket | Tokens refill at a steady rate | Allows controlled bursts | A bit more state |
| Leaky bucket | Requests drain at a fixed rate | Smooth output | Delays or drops bursts |

**When fixed window is fine:** quotas ("1000 calls per hour"), where exact smoothness is not critical.
**When it is not:** strict protection of a fragile backend. Use token bucket or sliding window.

---

## 4. Database design [built]

```sql
CREATE TABLE rate_limit_counters (
    scope_type    text        NOT NULL,   -- 'tenant' | 'api_key' | 'user'
    scope_key     text        NOT NULL,   -- 'acme', 'key_123'
    window_start  timestamptz NOT NULL,
    request_count integer     NOT NULL DEFAULT 0,
    PRIMARY KEY (scope_type, scope_key, window_start)
);
```

- **Primary key = one row per (who, window).** This is what makes concurrency safe.
- Extra index on `window_start` so a cleanup job can delete old rows fast.
- Old rows pile up, so a periodic `DELETE ... WHERE window_start < now() - interval` is needed [planned].

---

## 5. The atomic increment [built]  (most important part)

```sql
INSERT INTO rate_limit_counters (scope_type, scope_key, window_start, request_count)
VALUES ($1, $2, <window start>, 1)
ON CONFLICT (scope_type, scope_key, window_start)
DO UPDATE SET request_count = rate_limit_counters.request_count + 1
RETURNING request_count;
```

**How to explain it:**
1. First request in a window: the INSERT creates the row with count 1.
2. Any later request: the row exists, so `ON CONFLICT` turns the insert into an UPDATE (+1).
3. Postgres locks the row during the update, so simultaneous requests run one after another.
4. `RETURNING` gives back the new count in the same statement.

**Why not read-then-write?**

```
BAD:   A reads 4     B reads 4     A writes 5     B writes 5   → lost update, 2 requests counted as 1
GOOD:  one statement, DB does "count = count + 1" under a row lock → 5, then 6
```

**Why not a Go mutex?** A mutex only protects one process. With 3 app servers, each has its own
mutex and they would not see each other. The database is the shared place, so it must do the locking.

**Trade-off to mention:** this version also counts rejected requests, so the counter keeps growing
past the limit. The alternative adds `WHERE request_count < limit` to the UPDATE: more accurate,
but then no row comes back when blocked, so you lose the info needed for Retry-After.

**Use the database clock** (`now()` in SQL) instead of each app server's clock, so servers with
drifting clocks still agree which window they are in.

---

## 6. HTTP behaviour [planned: Step 4]

When blocked: status `429 Too Many Requests`.

```
HTTP/1.1 429 Too Many Requests
Retry-After: 20                  # seconds to wait (whole number, rounded UP)
X-RateLimit-Limit: 100
X-RateLimit-Remaining: 0
X-RateLimit-Reset: 1767268860    # unix time the window ends
```

- Send the headers on successful requests too, so clients can slow down before hitting the wall.
- `X-RateLimit-*` is a common convention (GitHub, Twitter), not an official standard.
- Rounding Retry-After up means a client that waits that long never retries too early.

Middleware pattern in Go: `func(next http.Handler) http.Handler`. It reads identity, asks the limiter,
and either calls `next` or writes 429. The handler never runs for blocked requests.

---

## 7. Multiple scopes: tenant, API key, user [planned: Step 5]

A request must pass all three limits. Do the three increments in one transaction:

```
BEGIN
  increment tenant   → count
  increment api_key  → count
  increment user     → count
  any over limit? → ROLLBACK (a denied request doesn't use up the tenant's budget)
COMMIT
```

- Always touch scopes in the same order (tenant → api_key → user). Two transactions locking rows in
  different orders can **deadlock**. Same order means no cycle.
- Real-world note: some designs let the denied request still count, because it keeps abusive clients throttled. It is a choice.

---

## 8. Concurrency and distributed systems

| Situation | What protects us |
|---|---|
| 100 requests at once for one tenant | Row lock in the upsert, so counts are exact |
| Many app servers | They all share the same table, so the limit is global |
| Two first requests at the same instant | Primary key: one inserts, the other becomes an update |
| Deadlock between scopes | Fixed lock order |

Test idea [built]: 50 goroutines call increment at once. The results must be exactly 1..50 with no duplicates and no gaps.

```go
for i := 0; i < 50; i++ { go func() { r, _ := s.Increment(ctx, "tenant", "busy", time.Hour); counts <- r.Count }() }
// assert: every number 1..50 appears exactly once
```

---

## 9. Postgres only vs Redis

| | Postgres | Redis |
|---|---|---|
| Atomic increment | Upsert + row lock | `INCR` (single-threaded) |
| Speed | Thousands/sec per hot row | 100k+/sec |
| Expiry of old windows | Cleanup job | `EXPIRE` built in |
| All-or-nothing across scopes | Real transactions | Lua script / MULTI |
| Durability | Strong | Weaker by default |
| Extra service | No | Yes |

**Answer:** "I started with Postgres only because it's correct, simple, one less moving part, and I
already need it. I put the counter behind a small `Counter` interface so I can add Redis when traffic
grows, e.g. tens of thousands of requests per second, where a DB write per request becomes the bottleneck."

Redis fixed-window sketch [theory]:

```
INCR  rl:tenant:acme:1767268800       # atomic count
EXPIRE rl:tenant:acme:1767268800 60   # cleanup for free
```

### Redis hash commands (HMSET / HMGET) [theory]

A hash is a small key-value map stored under one key.

```
HSET   rl:acme limit 100 window 60      # set several fields (HMSET does the same but is deprecated)
HMGET  rl:acme limit window             # read several fields in one call → ["100","60"]
HINCRBY rl:acme count 1                 # atomic +1 on one field (best for counters)
```

Use a hash when related values belong together (count + limit + reset time) so one round trip fetches all.

---

## 10. Fault tolerance [built]

What if Postgres is slow or down? The limiter must not take the whole API down.

```
request → breaker open? → yes: skip DB (fast) → failure policy
              no → try DB with timeout → temporary error? retry with backoff (max 3)
                                       → still failing? count failure, 5 in a row opens breaker
```

| Technique | Problem it solves | Key detail |
|---|---|---|
| **Timeout** | A hanging DB hangs every request | `context.WithTimeout` per attempt (200ms) |
| **Retry + backoff + jitter** | Short blips fix themselves | Wait doubles each time; random jitter stops all servers retrying together |
| **Circuit breaker** | During an outage, stop waiting on a dead DB | closed → open (after N failures) → half-open (one probe) → closed |
| **Fail-open / fail-closed** | What to do when we can't count | See below |

```go
attemptCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
res, err := counter.Increment(attemptCtx, scope, key, window)
cancel()
```

**Only retry temporary errors** (timeouts, deadlocks `40P01`, serialization failures `40001`, connection
errors). A permanent error like a unique violation would just fail again.

**Retry can double-count:** if the first attempt reached the DB but the reply was lost, the retry counts again.
For a limiter that is the safe direction: it may over-count slightly, never let extra traffic through.

### Fail-open vs fail-closed

| | Fail-open | Fail-closed |
|---|---|---|
| DB down → | requests pass, uncounted | requests get 503 |
| Priority | availability | protection |
| Use for | public API, checkout | login attempts, SMS sending, paid calls |
| Risk | abuse goes unlimited for a while | limiter bug = full outage |

Say: "Default fail-open for general traffic, fail-closed for sensitive endpoints, and always log and alert on degraded mode."

Circuit breaker pitfall I handled: if the client hangs up during the half-open probe, release the probe slot,
otherwise the breaker stays stuck half-open forever.

---

## 11. Async replication (database copies)

Primary commits a write and replies at once; it ships changes to the replica afterwards.

```
Client ─write─▶ PRIMARY ─"done"─▶ Client
                   └─ WAL changes (later) ─▶ REPLICA   → can be a little behind (replication lag)
```

| | Async | Sync |
|---|---|---|
| Primary waits for replica | No | Yes |
| Write latency | Low | Higher, blocked if replica is down |
| Primary dies | Last writes may be lost | Nothing lost |

**Why it matters here:**
- Counters must **never** be read from a replica. A lagging replica may show 3 when the real count is 5,
  so a request slips through. Always increment on the primary.
- Replicas are fine for reports ("usage per tenant today").
- Failover to a lagging replica can lose recent increments: clients get a slightly bigger budget for a moment. Acceptable for a limiter.

---

## 12. Other likely questions, short answers

**How would this scale?** Add app servers (stateless, shared DB). When the DB write rate becomes the limit:
move counters to Redis, or shard by tenant. Partition the counters table by time so old windows are dropped by detaching partitions.

**Hot tenant problem?** All requests of one tenant hit one row. Mitigations: Redis, or batch locally and sync counts
(trade accuracy for speed), or split the counter into N sub-rows and sum.

**How do you identify clients?** Tenant from auth token, API key from header, user from token. Never trust client
headers in production (our demo trusts them to stay small).

**How do you test it?** Pure logic: table-driven unit tests. Fault tolerance: fake counter that fails on demand.
Store: integration test on a real Postgres (guard: refuses to truncate unless the DB name contains "test").
Concurrency: many goroutines, check counts have no duplicates.

**What would you improve?** Sliding window counter to remove the boundary burst, per-endpoint fail policy,
metrics (rejections, degraded mode, breaker state), cleanup job, per-plan limits stored in the DB.

**What open-source project is similar?** Kong's rate-limiting plugin: fixed window with `local`, `cluster`
(database) and `redis` storage policies. Envoy's ratelimit service also uses fixed windows in Redis.
(Describe the ideas, not exact internals, unless you've read the source.)

---

## 13. One-page cheat sheet

- Fixed window = bucket per time slice; new window = new row; boundary burst up to 2x.
- Atomic increment = `INSERT ... ON CONFLICT DO UPDATE ... RETURNING`, row lock makes it exact.
- Go mutex doesn't work across servers; the DB is the shared lock.
- 429 + `Retry-After` (round up) + `X-RateLimit-*`.
- One transaction for all scopes; same lock order prevents deadlock; denied request must not use up other budgets.
- Timeout → retry (only temporary errors, backoff + jitter) → circuit breaker → fail-open/closed.
- Retry can double-count; for limiters over-counting is the safe side.
- Postgres first, Redis when throughput demands; hide storage behind an interface.
- Never read counters from an async replica.
