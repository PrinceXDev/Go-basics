# 68 — API Gateway & Service-to-Service Communication (banking edition)

```bash
go run ./68_api_gateway_and_s2s
```

The demo starts a **gateway**, a **payments** service, an **accounts** service
and an async **notifications** consumer on real localhost ports, then plays 15
scenarios. Each section below says which step shows it.

---

## 0. The mental model — a bank branch

| Real branch                                   | Microservices                   |
| --------------------------------------------- | ------------------------------- |
| Security guard + reception at the entrance    | **API gateway**                 |
| Checks your ID once, gives you a token number | Authentication at the edge      |
| "Max 1 visit per minute, please"              | Rate limiting                   |
| Sends you to the right counter                | Routing                         |
| Counters: Accounts, Loans, Cards              | Services                        |
| Staff phoning each other internally           | **Sync** service-to-service     |
| Staff calls show their staff ID badge         | Service identity (mTLS / token) |
| Notice board: "Txn done → send SMS, update passbook" | **Async** events (Kafka) |
| Every form stamped with one reference number  | Correlation / request ID        |

Two rules to remember from this:
1. The guard checks **who you are**. Each counter still checks **whether this
   account is yours** (the guard can't know that).
2. Staff inside still show badges to each other. Being "inside the building"
   is not proof of anything (**zero trust**).

---

## Part A — The API Gateway

### A1. Why it exists

Without a gateway, every mobile/web/partner client must know the address of
every service, and every service re-implements auth, rate limits, logging,
CORS, TLS. With 50 services that is 50 copies of security code, each slightly
different, each a potential hole.

```
 WITHOUT                                   WITH
 app ──> accounts  (auth, limits, logs)    app ──> GATEWAY ──> accounts
 app ──> payments  (auth, limits, logs)            (auth,  ──> payments
 app ──> cards     (auth, limits, logs)             limits,──> cards
 app ──> loans     (auth, limits, logs)             logs)  ──> loans
```

**One sentence for the interview:** an API gateway is a reverse proxy that is
the single entry point for external clients, and handles the *cross-cutting*
work (security, traffic control, routing, observability) so services can focus
on business logic.

### A2. What it does — in request order (`gateway.go`)

| # | Responsibility        | Why                                                          | Demo step |
|---|-----------------------|--------------------------------------------------------------|-----------|
| 1 | Request/correlation ID | One ID follows the request everywhere: logs, traces, support tickets | every step (`rid=`) |
| 2 | Access logging        | Audit trail. Never log tokens, card numbers, full bodies      | every step |
| 3 | Panic recovery        | A bug returns 500 instead of killing the process              | —         |
| 4 | Body size limit       | Cheap defence against memory exhaustion                      | 5         |
| 5 | Authentication        | Verify the customer's JWT/OAuth2 token **once**              | 2         |
| 6 | Rate limiting         | Protect backends from abuse, bots and runaway clients         | 15        |
| 7 | Timeout               | Bound how long anyone waits; cancels upstream work too        | 14        |
| 8 | Routing + rewrite     | `/api/v1/accounts/x` → `accounts:/accounts/x`                | 3         |
| 9 | Header hygiene        | Strip client-sent `X-User-ID`, swap customer token for service token | 4 |

Other things production gateways also do: TLS termination, CORS, request
validation against an OpenAPI schema, response caching, API keys for partners,
request/response transformation, canary/blue-green traffic splitting, and
**aggregation** (one call fans out to 3 services and merges the result).

### A3. What it should NOT do

Business logic. "Is the balance enough?", "is this account alice's?", "is
this a duplicate transfer?" all belong to the services. A gateway packed
with business rules turns into a deployment bottleneck that every team must
change. **Keep the gateway thin.**

### A4. Gateway vs. load balancer vs. reverse proxy vs. service mesh

| Thing          | Layer              | Main job                                            | Examples |
|----------------|--------------------|-----------------------------------------------------|----------|
| Load balancer  | L4 (TCP) or L7     | Spread traffic over identical copies of ONE service | AWS ALB/NLB, F5 |
| Reverse proxy  | L7                 | Forward requests to a backend, hide it              | NGINX, HAProxy |
| **API gateway**| L7 + API awareness | Reverse proxy + auth, quotas, versioning, API products | Kong, Apigee, AWS API Gateway, Azure APIM, Tyk |
| Service mesh   | Sidecar per pod    | **East-west** (service↔service): mTLS, retries, traffic policy | Istio, Linkerd |

**North-south** traffic = outside world → your system (gateway's job).
**East-west** traffic = service → service inside (mesh's job, or a client
library like `s2sclient.go`).

### A5. Patterns worth naming

- **BFF (Backend for Frontend):** a separate gateway per client type. The
  mobile app wants small payloads; netbanking wants rich ones; partners want a
  stable contract. One BFF each, owned by that client's team.
- **Internal vs external gateway:** banks typically run an *external* gateway
  in the DMZ (behind a WAF + DDoS protection) for customers and partners, and
  an *internal* one for branch/ops/back-office systems.
- **API versioning:** `/api/v1/...` in the path (used here), or a header.
  Never break v1; run v1 and v2 side by side and retire v1 slowly.

### A6. Rate limiting algorithms (`ratelimit.go`)

| Algorithm        | How it works                                     | Good for |
|------------------|--------------------------------------------------|----------|
| **Token bucket** | Bucket of N tokens, refilled at R/sec; each request takes one | Allowing bursts (app opens → 5 calls) — used here |
| Leaky bucket     | Requests leave a queue at a fixed rate           | Smoothing traffic into a fragile backend |
| Fixed window     | Count per calendar minute                        | Simple, but allows 2× at window edges |
| Sliding window   | Rolling count over the last 60s                  | Accurate, bit more memory |

Key the limit by **customer** (after auth), not IP — thousands of users share
one mobile-carrier IP. With many gateway pods, keep the counters in **Redis**
so the limit is global, not per-pod. Return **429** with `Retry-After`.

### A7. Is the gateway a single point of failure?

Only if you run one. It is **stateless** (no session data inside), so you run
many replicas across availability zones behind a load balancer, and scale
them horizontally. State it needs (rate-limit counters, token revocation
lists) lives in Redis.

---

## Part B — Service-to-Service Communication

### B1. Sync vs. async — the most important choice

```
SYNC (request/response)                 ASYNC (events / messages)
payments ──"debit ₹500"──> accounts     payments ──"TransferCompleted"──> [ Kafka ]
         <──"ok, bal ₹49,500"──                                            │  │  │
caller WAITS for the answer                               SMS  fraud  statements
                                        publisher does NOT wait or even know who listens
```

| Question                         | Sync (REST / gRPC)                 | Async (Kafka / RabbitMQ)          |
|----------------------------------|------------------------------------|-----------------------------------|
| Does the caller need the answer now? | Yes                           | No                                |
| Coupling                         | Tight: callee down = caller fails  | Loose: broker buffers if consumer down |
| Consistency                      | Immediate                          | Eventual                          |
| Failure handling                 | Timeouts, retries, breakers        | Retries, dead-letter queue, dedupe |
| Banking example                  | Balance check, debit, OTP verify   | SMS/email, fraud scoring, statements, rewards, audit feed |

**Rule of thumb:** the customer-facing *decision* (did the transfer succeed?)
is sync; everything that *reacts* to it is async. (Step 7: the 201 comes back
first, the SMS log line appears afterwards.)

### B2. REST vs. gRPC for internal calls

| | REST + JSON | gRPC + Protobuf |
|-|-|-|
| Format | Text, human readable | Binary, ~5-10× smaller |
| Contract | OpenAPI (optional) | `.proto` (mandatory, code-generated) |
| Transport | HTTP/1.1 or 2 | HTTP/2, multiplexed, streaming |
| Best for | Public APIs, partners, browsers | Internal, high-volume, low-latency calls |

Common setup: **REST at the gateway (public), gRPC inside.** Lesson
`63_microservices` shows the gRPC version; this lesson uses plain HTTP so
every header is visible.

### B3. How does service A find service B?

- **Kubernetes:** DNS. `http://accounts.payments-ns.svc.cluster.local` → a
  Service → load-balanced across healthy pods. This is what most banks on K8s use.
- **Registry-based:** Consul / Eureka. Services register themselves; callers
  look up healthy instances.
- Health checks (liveness/readiness) remove sick pods from rotation.

### B4. Securing internal calls — zero trust (`auth.go`, step 6)

"It's inside our network" is not security; one compromised pod would own
everything. Every internal call proves **who is calling**:

- **mTLS** (mutual TLS): both sides present certificates. Usually done by a
  service mesh (Istio) automatically — the code doesn't change.
- **Service tokens** (this demo): short-lived JWT, `sub=payments`,
  `aud=accounts`. Audience stops a token for one service being replayed at another.
- **Authorization per route:** authenticated ≠ allowed. Only `payments` may
  call `/debit` — the gateway gets **403** (step 6).

And keep **user identity** separate: the gateway verifies the customer and
passes `X-User-ID` downstream; services trust that header **only** because
the caller authenticated as an internal service. The gateway always deletes
any `X-User-ID` the client sent (step 4).

### B5. Resilience — the network WILL fail (`s2sclient.go`)

| Failure | Defence | Demo |
|-|-|-|
| Service hangs | **Timeout** on every call, per attempt, inside an overall deadline | 14 |
| Blip (pod restart) | **Retry** with exponential backoff + jitter, only transient errors | 10 |
| Business "no" (insufficient funds) | **Don't retry** 4xx | 11 |
| Service down for minutes | **Circuit breaker**: fail fast, let it recover | 13 |
| Reply lost after success | **Idempotency** on the server | 9 |
| One slow dependency eats all threads | **Bulkhead**: separate pools/limits per dependency | (see lesson 64 semaphores) |
| Dependency down, feature optional | **Fallback / graceful degradation** (show cached balance, hide rewards) | (lesson 63) |

Composition order: `breaker( retry( timeout( call ) ) )`.

**Retry storm:** gateway ×3 × payments ×3 × accounts ×3 = 27 calls into a
struggling database. Retry at **one** layer, and propagate a shrinking deadline.

### B6. Idempotency — the most important banking concept here

A customer taps "Pay" on a weak 4G network. The request reaches the bank, the
money moves, the **response** is lost. The app retries. Without idempotency
the customer pays twice.

```
client ── POST /transfers  Idempotency-Key: k-001 ──> payments
                                                      first time? run it, STORE result under k-001
client ── same request again (retry) ──────────────> payments
                                                      seen k-001? return the STORED result, do nothing
```

Rules implemented in `payments.go` (steps 8, 9):
- Key is **required** on money-moving endpoints (400 without it).
- Scope the key per customer.
- Same key + **different body** → 422 (client bug, not a retry).
- Same key while the first is still running → 409.
- Store the result, success or business failure; release the key on 5xx so a
  retry can finish the job.
- Derive **downstream refs** from the key (`TRF-x:debit`), so every hop is
  idempotent too. In Postgres: a `UNIQUE` constraint on the ref.

### B7. Distributed transactions — Saga (`payments.go`, step 12)

No shared DB ⇒ no single ACID transaction across accounts. Two-phase commit
(2PC) exists but blocks and scales badly, so microservices use a **saga**: a
chain of local transactions, each with a **compensating** action.

```
debit alice ✔ ──> credit carol ✘ (frozen) ──> COMPENSATE: refund alice ✔ ──> REVERSED
```

- **Orchestration** (used here): one coordinator (payments) calls each step.
  Easy to follow, one place to look.
- **Choreography**: each service reacts to the previous one's event. No
  coordinator, but the flow is spread across services and harder to trace.

Compensation is a **new** action (a refund entry), never deleting history —
banks need an audit trail.

### B8. Transactional outbox + idempotent consumer (step 7)

**Dual-write problem:** "save transfer to DB" and "publish event to Kafka"
are two systems. Crash in between → lost event, or an event for a transfer
that never saved.

**Outbox:** write the transfer **and** the event row in the *same* DB
transaction. A relay (or Debezium CDC) publishes outbox rows to Kafka.
This is **at-least-once**, so duplicates happen — the demo forces one.
**Inbox / idempotent consumer:** consumers record processed event IDs and skip
repeats ("duplicate ignored" in step 7). Together: *effectively exactly once*.

### B9. Observability across services

- **Correlation ID** — `X-Request-ID` set by the gateway, forwarded on every
  hop, put in every log line and even in the event (`rid=` is identical from
  gateway → payments → accounts → SMS).
- **Distributed tracing** — OpenTelemetry + Jaeger/Tempo: the same idea with
  timings per hop (`traceparent` header, W3C standard).
- **RED metrics** per service: **R**ate, **E**rrors, **D**uration (p50/p99).

---

## Part C — End-to-end: an IMPS/UPI-style transfer

```
Mobile app
  │ 1. POST /api/v1/transfers   Authorization: Bearer <customer JWT>
  │                             Idempotency-Key: 7c9e...
  ▼
WAF / DDoS ──> EXTERNAL API GATEWAY
                 verify JWT (OAuth2/OIDC), rate limit, request ID,
                 strip spoofed headers, mint service token, route
  ▼
PAYMENTS (orchestrator)
  2. idempotency check (key seen? replay)
  3. GET accounts/{from}        ownership (BOLA check)        ┐
  4. fraud/limits service       daily limit, velocity, device ├ sync, each with
  5. POST accounts/{from}/debit ref=TRF:debit                 │ timeout+retry+breaker
  6. external rail (NPCI) for other-bank beneficiary          │
     or POST accounts/{to}/credit for same-bank               ┘
  7. one DB tx: transfer row + outbox row
  8. 201 to customer
  ▼ (async, via Kafka)
SMS/push · statements · rewards · AML monitoring · audit/data lake
```

Talking points: the customer waits only for steps 2-8; anything slow or
optional is async; every step is idempotent; external rail timeouts end in a
**PENDING** state resolved by a status-check/reconciliation job, never a
blind retry.

---

## Part D — Where each concept lives in the code

| Concept | File / function | Step |
|-|-|-|
| Middleware chain, ordering | `gateway.go` `NewGateway`, `common.go` `chain` | all |
| Reverse proxy, path rewrite, header hygiene | `gateway.go` `newProxy` | 3, 4 |
| JWT verification, alg pinning | `auth.go` `verifyUserToken` | 2 |
| Service tokens, audience, per-route allow-list | `auth.go` `ServiceAuth`, `allow` | 6 |
| Object-level authorization (BOLA) | `accounts.go` `getAccount` | 3 |
| Token-bucket rate limit | `ratelimit.go` | 15 |
| Timeouts, retry+jitter, retryable errors | `s2sclient.go` `Call`, `retryable` | 10, 11, 14 |
| Circuit breaker | `s2sclient.go` `Breaker` | 13 |
| Idempotency keys (API) + refs (internal) | `payments.go` `createTransfer`, `accounts.go` `move` | 8, 9 |
| Saga + compensation | `payments.go` `execute` | 12 |
| Outbox, at-least-once, idempotent consumer | `payments.go` `commit`, `RunOutboxRelay`; `events.go` | 7 |

---

## Part E — Interview questions

### Concept questions

1. **What is an API gateway and why use one?** Single entry point / reverse
   proxy for external clients; centralises auth, rate limiting, routing, TLS,
   logging, versioning so services stay focused on business logic.
2. **Gateway vs load balancer?** LB spreads load across copies of one service
   (L4/L7). A gateway routes to *different* services and adds API concerns
   (auth, quotas, transformation). A gateway usually sits behind an LB.
3. **Gateway vs service mesh?** Gateway = north-south (outside in). Mesh =
   east-west (service to service): mTLS, retries, traffic splitting via sidecars.
4. **Where do you authenticate and where do you authorize?** Authenticate the
   customer once at the gateway. Coarse authorization (scopes) can be there
   too, but object-level authorization ("is this account yours?") must be in
   the owning service.
5. **How do services know who the user is?** Gateway passes a verified
   identity (header or an internal JWT via token exchange), after deleting any
   client-supplied copy. Services trust it only from authenticated callers.
6. **Sync or async?** Sync when the caller needs the answer to continue;
   async for side effects and fan-out. Give the banking example.
7. **REST or gRPC internally?** gRPC for high-volume, low-latency internal
   calls with strict contracts; REST for public/partner APIs.
8. **How do you make a POST idempotent?** Client-generated Idempotency-Key,
   stored with the result under a unique constraint; replay on repeat; reject
   same key with different payload.
9. **Explain the circuit breaker states.** Closed → (N failures) → Open (fail
   fast) → (cooldown) → Half-open (one probe) → Closed or back to Open.
10. **Which errors do you retry?** Network errors, timeouts, 502/503/504, 429
    (with backoff). Never 4xx business errors. Only idempotent operations.
11. **Why jitter?** Without it, all clients retry at the same instants and
    hammer the recovering service in synchronised waves.
12. **How do you handle a transaction across services?** Saga with
    compensations (orchestrated or choreographed); avoid 2PC. Explain the
    refund example.
13. **What is the transactional outbox?** Event stored in the same DB
    transaction as the state change; relay publishes it; solves dual-write.
14. **Exactly-once delivery?** Not achievable end-to-end over a network;
    use at-least-once + idempotent consumers = effectively once.
15. **How do you trace a request across 10 services?** Correlation ID /
    W3C `traceparent`, OpenTelemetry, centralised logs, RED metrics.

### Scenario questions (the ones that separate seniors)

1. **"Customer says they were debited twice."** Check whether the
   transfer endpoint requires an idempotency key, whether the internal debit
   is keyed by a unique ref, whether a client generated a new key per retry,
   and search logs by request ID. Fix: required keys, unique ref constraint,
   reconciliation job to detect and auto-reverse duplicates.
2. **"The accounts service is slow, and now the whole app is down."**
   Cascading failure: callers have no/long timeouts, threads pile up. Fix:
   timeouts with deadline propagation, circuit breaker, bulkhead per
   dependency, retry budget at one layer, degrade gracefully (cached balance).
3. **"Debit succeeded, NPCI call timed out — what do you tell the customer?"**
   Status *pending*, never failed (you don't know). Don't blindly retry the
   external call; use the rail's status-check API with the same reference, and
   a reconciliation job; auto-reverse if it finally fails; notify via event.
4. **"Kafka is down — do transfers stop?"** No: with the outbox the event is
   safely in the DB; the relay catches up when Kafka returns. Only
   notifications are delayed.
5. **"A bot hammers the login API."** Rate limit per IP *and* per account at
   the gateway (Redis-backed), WAF/bot rules, CAPTCHA/OTP step-up, lockout
   with backoff, alerts.
6. **"Someone calls an internal endpoint directly from a compromised pod."**
   Zero trust: mTLS/service tokens with audience, per-route caller allow-lists,
   network policies, least privilege (only payments can debit).
7. **"Gateway p99 latency jumped."** Check per-upstream metrics (which route?),
   connection pool exhaustion (`MaxIdleConnsPerHost` default is 2 in Go!),
   TLS handshakes, rate-limit Redis latency, GC. Trace one slow request.
8. **"We need to release v2 of the transfer API without breaking old apps."**
   Version in path, run both, route by version at the gateway, canary a small
   % of traffic, sunset v1 with deprecation headers and a timeline.
9. **"How do you roll out a new payments version safely?"** Canary via gateway
   or mesh weights, watch error rate/latency, automatic rollback, backwards
   compatible schema and event changes.
10. **"Design the API layer for a mobile banking app."** Walk Part C: WAF →
    gateway (OAuth2, rate limit, BFF) → services (sync core, async side effects)
    → idempotency, saga, outbox, tracing, multi-AZ.

---

## Part F — Exercises (write these yourself)

1. **Distributed rate limit:** replace `keyedLimiter` with a Redis-backed
   token bucket using `miniredis` (already used in lesson 65), so two gateway
   instances share one limit.
2. **Retry budget:** make `ServiceClient` stop retrying when the parent
   context has less than `AttemptTimeout` left.
3. **PENDING state:** when the credit fails transiently, save the transfer as
   `PENDING` and write a reconciler goroutine that completes it later.
4. **Aggregation endpoint:** add `GET /api/v1/dashboard` to the gateway that
   calls accounts and a new "cards" service in parallel with `errgroup` and
   returns partial data if cards is down.
5. **Bulkhead:** limit payments to 10 concurrent calls into accounts with a
   `semaphore.Weighted` and return 503 when full.
