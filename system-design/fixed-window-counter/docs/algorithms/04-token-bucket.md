# 4. Token Bucket  [later]

## Real-world use case
**Payments or checkout API that tolerates short spikes.** A client may send 20 requests in a second
after being idle, as long as the average stays near 5 per second. (Stripe has described using token buckets,
and AWS API Gateway documents a token bucket model for throttling.)

## How it works
A bucket holds up to `capacity` tokens. Tokens are added at a steady `rate`. Each request takes one token.
No token means rejected.

```
capacity 10, refill 2 tokens/sec

idle for a while → bucket full (10)
burst of 10 requests → allowed instantly, bucket 0
11th request → rejected
wait 3s → +6 tokens → 6 more requests allowed
```

Tokens are not added by a timer. When a request arrives we compute what *would* have been added:

```
tokens = min(capacity, tokens + (now - last_refill) * rate)
if tokens >= 1 { tokens--; allow } else { reject }
```

## Code
```go
type Bucket struct {
    Tokens     float64
    LastRefill time.Time
}

func (b *Bucket) Take(now time.Time, capacity, ratePerSec float64) bool {
    b.Tokens = min(capacity, b.Tokens+now.Sub(b.LastRefill).Seconds()*ratePerSec)
    b.LastRefill = now
    if b.Tokens < 1 {
        return false
    }
    b.Tokens--
    return true
}
```
Postgres: one row `(scope_key, tokens, last_refill)`. Read-modify-write must be atomic:
`SELECT ... FOR UPDATE` inside a transaction, or a single `UPDATE ... RETURNING` with the refill math in SQL.
Redis: a hash with `tokens` and `last_refill` fields updated in one Lua script (this is where `HMGET` and `HSET` fit).

## Retry-After
`ceil((1 - tokens) / rate)` seconds until one token exists.

## Pros / cons
- Pros: allows controlled bursts, cap on average rate, tiny state (two numbers).
- Cons: two settings to explain (capacity and rate), floating-point time math, a burst can still hit the backend hard.

## Choose it when
User-facing APIs where bursts are normal (page loads fire several calls at once) but sustained abuse must be capped.

## Interview one-liner
"A bucket refills at a steady rate and each request spends a token. Capacity sets the allowed burst, refill rate sets the long-term average."

## Later implementation
New table `token_buckets(scope_type, scope_key, tokens, last_refill)`. First the pure `Take` function with tests
(idle refill, cap at capacity, burst then empty), then the atomic SQL, then a 50-goroutine test that never lets more than `capacity` through.
