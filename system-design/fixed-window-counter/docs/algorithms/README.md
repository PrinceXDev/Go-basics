# Rate Limiting Algorithms: Overview

Five algorithms, one doc each. Only the first is implemented so far.

| # | Algorithm | Real use case | State per key | Accuracy | Allows bursts? | Status |
|---|---|---|---|---|---|---|
| 1 | [Fixed window](01-fixed-window.md) | Hourly API quota | 1 counter per window | Low (2x burst at edges) | At window edges | built |
| 2 | [Sliding window log](02-sliding-window-log.md) | Max 5 failed logins in 15 min | 1 timestamp per request | Exact | No | later |
| 3 | [Sliding window counter](03-sliding-window-counter.md) | Smooth 1000 req/min on a public API | 2 counters | Good (approximate) | Little | later |
| 4 | [Token bucket](04-token-bucket.md) | Payment API that tolerates short spikes | tokens + last refill time | Good | Yes, up to bucket size | later |
| 5 | [Leaky bucket](05-leaky-bucket.md) | Sending SMS at a steady 10/sec | level + last leak time | Good | No (smooths them out) | later |

## How to pick (one question each)

```
Need exact counts for security (login, OTP)?          → sliding window log
Just a simple quota, edge bursts are OK?               → fixed window
Want smooth limits without storing every request?      → sliding window counter
Want to allow short bursts but cap the average rate?   → token bucket
Protecting a fragile downstream that needs steady flow?→ leaky bucket
```

## Same questions for every algorithm

1. What is the state, and how big is it?
2. Is the update atomic, and how (Postgres / Redis)?
3. What does a client see (429, Retry-After)?
4. What is its weakness?

## Plan for implementing all of them

Today the service depends on a counter that is specific to fixed windows. When we add the others, we
introduce one small interface so the middleware does not care which algorithm runs:

```go
type Limiter interface {
    Allow(ctx context.Context, scopeType, scopeKey string) (limiter.Decision, error)
}
```

Each algorithm becomes one implementation. The fault-tolerance wrapper (timeout, retry, breaker,
fail-open/closed) stays the same for all. Suggested order: sliding window counter, token bucket,
sliding window log, leaky bucket.

Note: names of companies below are used to show where an algorithm is commonly seen. Details of their
internals are from public write-ups as remembered; check their docs before quoting them.
