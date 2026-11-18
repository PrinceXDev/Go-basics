# 03 — Interview Prep: answers, questions, traps

---

## The 60-second answer

> "First, I'd make sure a slow gateway can't take us down: **strict timeouts** on every
> call, and a **circuit breaker**, Resilience4j in Java or gobreaker in Go. It watches the
> failure rate over a sliding window. Once, say, 50% of the last 50 calls fail, it **opens**
> and rejects calls instantly for 30 seconds. Then it goes **half-open**, lets a few trial
> calls through, and closes again if they succeed. Only infrastructure errors count:
> timeouts and 5xx, not card declines.
>
> Inside that, I'd **retry** transient failures 2–3 times with **exponential backoff and
> jitter**, so retries don't hit the gateway in synchronized waves.
>
> If it still fails, or the breaker is open, the **fallback** is *not* an error page. We
> save the payment as `PENDING_RETRY` and write a retry event **in the same database
> transaction** (outbox). The user sees 'order placed, payment processing.' A worker
> consumes the queue with backoff. Every call carries an **idempotency key**, the order ID,
> so a retry after a timeout can never double-charge. After N attempts the job goes to a
> **dead-letter queue**: we alert ops and ask the customer to pay another way.
>
> Finally I'd monitor breaker state, queue age and DLQ size, and run a nightly
> **reconciliation** against the gateway's records."

### Whiteboard version (draw this)

```
 User ─► API ─► [Retry 3x, backoff+jitter] ─► [Circuit Breaker] ─► [Timeout] ─► Gateway
                          │                          │ OPEN
                          └──────── fail ───────────►┴─► Fallback: DB tx {order=PENDING, outbox row}
                                                                     │
                                         202 "processing" ◄──────────┘
                                                                     ▼
                                          Worker ◄── queue (Kafka/SQS/PG SKIP LOCKED)
                                            │  same Idempotency-Key
                                            ├─ ok      → PAID, notify user
                                            ├─ decline → FAILED, notify user
                                            └─ N fails → DLQ, alert, notify user
```

---

## Core questions

### Circuit breaker

**Q1. What is a circuit breaker and why use it?**
A proxy around a remote call that tracks failures and, past a threshold, stops making the
call for a while (fail fast). It prevents cascading failures (stuck threads, exhausted
pools) and gives the struggling dependency time to recover.

**Q2. Explain the three states.**
CLOSED: calls pass, outcomes are recorded. OPEN: calls rejected immediately for
`waitDuration`. HALF_OPEN: K trial calls allowed; if they succeed, go CLOSED, else back to OPEN.

**Q3. How does it decide to open?**
Failure rate (and/or slow-call rate) over a sliding window (the last N calls, or the last T
seconds) ≥ threshold, but only after a **minimum number of calls**. That guard stops 1
failure out of 1 call from counting as "100%".

**Q4. Count-based vs time-based window?**
Count-based: the last N calls. Predictable, but at low traffic the "last 100 calls" may span
an hour. Time-based: calls in the last N seconds. It reflects *now*, which suits variable
traffic. Pick time-based for spiky traffic and count-based for steady traffic.

**Q5. Which exceptions should count as failures?**
Timeouts, connection errors, 5xx, 429. **Not** 4xx business errors like card declined or
validation. Those prove the gateway is healthy. (Resilience4j: `ignoreExceptions`.)

**Q6. What happens to requests while the breaker is open?**
They fail fast with `CallNotPermittedException` / `ErrOpen` and go to the fallback. They are
**not** retried. Retrying an open breaker just adds latency.

**Q7. Why HALF_OPEN instead of going straight back to CLOSED?**
If the gateway is still down, letting all traffic through at once would cause another failure
wave and could knock a recovering service back over. A few trial calls test it safely.

**Q8. Hystrix vs Resilience4j?**
Hystrix is in maintenance mode (since 2018). It used thread-pool isolation per command, which
is heavy. Resilience4j is lightweight and functional (decorators you compose), integrates with
Spring Boot and Micrometer, and has separate modules: CircuitBreaker, Retry, RateLimiter,
Bulkhead, TimeLimiter.

**Q9. One breaker per what?**
Per **downstream dependency** (and sometimes per endpoint): `paymentGateway`, not one global
breaker. Otherwise a broken recommendations service would block payments.

**Q10. With 20 pods, is the breaker shared?**
Usually no. Each instance keeps local state and they converge quickly on their own. A shared
breaker (Redis) adds a new dependency and latency to every call. That's rarely worth it.

### Retry

**Q11. Why exponential backoff? Why jitter?**
Backoff gives the dependency time to recover. Jitter prevents the thundering herd, where
thousands of clients retry in sync. Full jitter is `sleep = random(0, min(cap, base·2^n))`.

**Q12. Retry or circuit breaker first?**
Retry **outside**, breaker **inside**: `Retry(CircuitBreaker(call))`. Each attempt updates
the breaker, and once it opens, retries stop immediately (ErrOpen isn't retryable).

**Q13. What's a retry storm and how do you avoid it?**
Retries at several layers multiply: 3 × 3 × 3 = 27 calls per click. Retry at one layer,
cap attempts, use retry budgets (e.g. at most 10% extra traffic from retries), and honour
`Retry-After`.

**Q14. Is it safe to retry a payment?**
Only with an **idempotency key**. A timeout is an *unknown* outcome, and the charge may have
succeeded. With the key, the gateway returns the original result instead of charging again.

### Fallback & async

**Q15. What's a good fallback for a payment?**
Durably accept the intent ("order placed, payment processing", HTTP 202) and complete it
asynchronously. Cached/default fallbacks are for reads. Never fake a successful payment.

**Q16. Why not retry later in a goroutine / `@Async` method?**
It's lost on crash or deploy, invisible to ops, and has no DLQ. Use a durable queue: Kafka,
SQS, RabbitMQ, or a DB table.

**Q17. How do you guarantee the retry event is published if the DB write succeeds?**
**Transactional outbox**: write the order status and the event row in the same DB transaction.
A relay (poller or CDC such as Debezium) publishes it to Kafka afterwards. This avoids the
dual-write problem.

**Q18. How do several workers process a DB-backed queue safely?**
`SELECT … FOR UPDATE SKIP LOCKED LIMIT n` plus a lease (`locked_at`) so jobs from crashed
workers are reclaimed. Kafka gets the same result with partitions + consumer groups.

**Q19. What is a dead-letter queue?**
Where messages go after max retries (or when they can't be processed at all), so they don't
block the queue. You alert on it, inspect it and replay it. For payments: notify the customer
and release reserved stock.

**Q20. How does the user find out the final result?**
Push, email or SMS, a webhook from the gateway, or the client polls / uses SSE on
`GET /payments/{id}`. Many gateways also send **webhooks** you should handle, idempotently too.

### Design & operations

**Q21. What timeout would you set?**
Based on the dependency's p99 latency (×2–3), and always below the caller's own timeout.
Use a total budget across retries, e.g. a 3s total for the sync path even with 3 attempts.

**Q22. What do you monitor?**
Breaker state and transitions, not-permitted calls, error rate, p99 latency, queue depth,
oldest message age, DLQ size, and reconciliation mismatches.

**Q23. How do you test this?**
Unit tests with a fake clock for state transitions. Fault injection (WireMock/Toxiproxy, or a
fake gateway with modes, as in this repo). Chaos experiments in staging. Contract tests
against the gateway's sandbox.

**Q24. What if the gateway is down for hours?**
The breaker stays open (cheap), the queue grows, and backoff caps at minutes. Alert ops, show
a banner ("payments delayed"), and switch to a secondary provider if you have one (that needs
its own idempotency scheme). Decide on the business side whether to hold or ship orders.

**Q25. Related patterns?**
Timeout, Bulkhead (isolate resource pools per dependency), Rate limiter (protect the
dependency), Saga (compensate: release stock, cancel order), Reconciliation (nightly
DB vs gateway report).

---

## Traps that lose points

| ❌ Weak answer | ✅ Strong answer |
|---|---|
| "Wrap it in a circuit breaker" and stop there | Breaker **+ timeout + retry + fallback + idempotency + async + DLQ** |
| Retrying everything | Retry only transient errors; never declines; never an open breaker |
| Counting declines as failures | Business errors are excluded from the breaker |
| Fallback returns "success" | Fallback = durably accept + complete later (202) |
| `new Thread(() -> retryLater())` | Durable queue + outbox in the same transaction |
| Ignoring "timeout but charged" | Idempotency key = order ID, plus reconciliation |
| No max attempts | Max attempts → DLQ → alert + customer notification |
| Mentioning only Hystrix | "Hystrix is deprecated, Resilience4j is the standard" |
| No numbers | Give concrete values: 50% over 50 calls, 30s open, 3 trials, 3 retries with 200ms→2s |

---

## Follow-up drills (answer out loud, then check the code)

1. Walk through what happens to the 6th payment after the breaker opens. → `acquire()` returns `ErrOpen`; `ProcessPayment` falls to `Store.Enqueue`.
2. A worker crashes after calling the gateway but before updating the DB. What happens? → The lease expires, the job is reclaimed, the call is repeated with the same key, the gateway replays the result, no double charge.
3. Why does the worker skip polling when the breaker is OPEN? → To avoid hammering a dead dependency, and because every call would be rejected anyway.
4. Why does `DeferJob` decrement attempts? → A rejected call was never really an attempt.
5. Why is the Open→HalfOpen transition "lazy"? → No timer goroutine is needed, and the state is checked on the next call.
6. What does the `generation` counter protect against? → Results from calls that started in an earlier state.
7. How would you swap Postgres for Kafka here? → The outbox table stays the same, a relay publishes to a `payment-retry` topic, and a consumer group replaces `ClaimJobs`. Delays come from a retry topic per backoff tier, or a delay queue.

---

## Glossary (plain words)

| Term | Meaning |
|---|---|
| Cascading failure | One broken service makes its callers break, which breaks their callers… |
| Fail fast | Return an error right away instead of waiting on something you know is broken |
| Idempotent | Doing it twice has the same effect as doing it once |
| Idempotency key | A unique ID sent with a request so the server can spot and ignore repeats |
| Outbox | A table where you save "messages to send" in the same transaction as your data |
| DLQ | A "parking lot" for messages that kept failing, for humans to inspect |
| Jitter | Random extra delay so retries don't line up |
| Thundering herd | Many clients hitting a server at the same moment |
| Bulkhead | Separate resource pools so one leak can't sink the whole ship |
