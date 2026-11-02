# 2. Sliding Window Log  [later]

## Real-world use case
**Login protection.** "At most 5 failed logins in any 15-minute period per account."
A security rule needs to be exact: a fixed window would let an attacker try 5 at 12:14 and 5 more at 12:16.
The log remembers each attempt's time, so the 15 minutes always slide with "now".

## How it works
Store the timestamp of every request. On a new request, drop timestamps older than the window,
count what is left, and allow if the count is under the limit.

```
limit 3 per 60s, now = 12:01:10
log: [12:00:20, 12:00:45, 12:01:05]
drop older than 12:00:10  → nothing dropped, count = 3 → request 4 is rejected
at 12:01:25 the 12:00:20 entry is older than 60s → dropped → count 2 → allowed
```

## Code
```go
func allowed(log []time.Time, now time.Time, window time.Duration, limit int) bool {
    cutoff := now.Add(-window)
    n := 0
    for _, t := range log {
        if t.After(cutoff) { n++ }
    }
    return n < limit
}
```
Postgres (needs a transaction or advisory lock so check + insert are atomic):
```sql
SELECT count(*) FROM request_log WHERE scope_key = $1 AND ts > now() - $2::interval;
INSERT INTO request_log (scope_key, ts) VALUES ($1, now());
```
Redis (sorted set, score = timestamp), usually inside one Lua script:
```
ZREMRANGEBYSCORE key 0 (now - window)
ZCARD key            -- count
ZADD key now member  -- record, only if count < limit
EXPIRE key window
```

## Pros / cons
- Pros: exact, no boundary burst.
- Cons: memory grows with the limit (1 entry per request). 10,000 req/min per key means 10,000 entries per key.
  Check-then-insert needs care to stay atomic.

## Choose it when
Small limits where exactness matters: logins, OTP, password resets, payment attempts.

## Interview one-liner
"Keep a log of request times and count only those inside the last N seconds. Exact, but memory is proportional to the limit, so I use it for small security limits."

## Later implementation
New table `request_log(scope_key, ts)` with an index on `(scope_key, ts)`, plus a cleanup of old rows.
Show the atomicity problem first with a naive version and a concurrency test that fails, then fix it with a lock.
