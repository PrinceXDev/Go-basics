# 3. Sliding Window Counter  [later]

## Real-world use case
**Public API gateway, 1000 requests per minute per key**, with millions of keys.
Fixed window has edge bursts and a log is too heavy at that scale, so the gateway keeps just two
numbers per key and estimates a sliding window. (Cloudflare has written about using this approximation
for edge rate limiting.)

## How it works
Keep the counter for the current window and for the previous window. Estimate how many requests fell
in the last `window` seconds by weighting the previous window by how much of it still overlaps.

```
estimated = previous_count * (1 - elapsed/window) + current_count
```

```
window 60s, limit 100, now is 15s into the current window
previous = 84, current = 36
overlap of previous = 1 - 15/60 = 0.75
estimated = 84 * 0.75 + 36 = 99   → allowed (99 < 100), request would make it ~100
```

It assumes the previous window's requests were evenly spread. That is the approximation.

## Code
```go
func estimate(prev, curr int, now, windowStart time.Time, window time.Duration) float64 {
    elapsed := now.Sub(windowStart).Seconds() / window.Seconds()   // 0..1
    return float64(prev)*(1-elapsed) + float64(curr)
}
```
Reuses the fixed window table: read the previous window's row and increment the current one.
```sql
-- current window: same upsert as fixed window
-- previous window: SELECT request_count FROM rate_limit_counters
--                  WHERE scope_type=$1 AND scope_key=$2 AND window_start = $3 - $4::interval;
```

## Pros / cons
- Pros: 2 counters per key, smooth limit, removes most of the boundary burst.
- Cons: approximate (assumes even traffic), slightly more logic than fixed window.

## Choose it when
High-volume APIs where you want smoothness without per-request storage. A very good default.

## Interview one-liner
"Two fixed-window counters blended by overlap: previous count times the remaining fraction, plus the current count. Nearly sliding-window accuracy for the memory of fixed windows."

## Later implementation
Smallest step from what we built: keep the fixed window table, add a read of the previous window, and add `estimate()`
to the `limiter` package with table-driven tests (including the 99 example above).
