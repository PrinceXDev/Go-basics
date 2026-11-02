# 1. Fixed Window Counter  [built]

## Real-world use case
**Hourly API quota.** "Each API key gets 5000 requests per hour" (GitHub's REST API works this way).
Also: "max 3 password-reset emails per hour per account".
The user cares about a budget, not perfect smoothness, so a simple counter is enough.

## How it works
Time is cut into fixed buckets. Each bucket has one counter. New bucket = counter starts at 0.

```
limit 5 per minute
12:00:00 ──────────── 12:01:00 ────────────
 1 2 3 4 5 | 6th → 429 | new bucket: 1 2 ...
```

```
window_start = floor(now / window) * window
count = count + 1
allowed = count <= limit
```

## Code
```go
secs := now.Unix()
windowStart := time.Unix(secs-secs%windowSeconds, 0)   // which bucket
```
```sql
INSERT INTO rate_limit_counters (scope_type, scope_key, window_start, request_count)
VALUES ($1, $2, $3, 1)
ON CONFLICT (scope_type, scope_key, window_start)
DO UPDATE SET request_count = rate_limit_counters.request_count + 1
RETURNING request_count;
```

## Storage fit
- Postgres: one row per window, atomic upsert, row lock. Needs a cleanup job.
- Redis: `INCR key` then `EXPIRE key window`. Cleanup is automatic.

## Pros / cons
- Pros: simplest, tiny state, easy to explain, easy to debug with SQL.
- Cons: **boundary burst**: 5 requests at 12:00:59 plus 5 at 12:01:00 = 10 in 2 seconds (up to 2x the limit).

## Choose it when
Quotas and billing-style limits where edge bursts are acceptable.

## Interview one-liner
"One counter per time bucket, incremented atomically. Simple and cheap, but a client can get up to twice the limit around a window boundary."

## Later implementation
Already built: `limiter` (window math), `store` (upsert), `service` (fault tolerance). Next: middleware.
