# Microservices — Interview Questions & Answers

How to use this sheet: read the question, answer **out loud**, then check.
Each answer is short on purpose, the length you'd actually say in an interview.
⭐ = asked very often. 🔥 = senior-level, it makes you stand out.
"In my project" lines point at `63_microservices` so you can give a real example.

---

## A. Basics

### 1. ⭐ What is a microservice?
A small service that does **one business job**, **owns its own data**, can be
**deployed on its own**, and talks to other services only over the network
(an API or messages).

> In my project: `userd` owns users, `orderd` owns orders, and the `gateway` is the public entry point.

### 2. ⭐ Monolith vs microservices?
| | Monolith | Microservices |
|---|---|---|
| Deploy | everything together | each service separately |
| Calls | function calls (fast, never lost) | network calls (slow, can fail) |
| Database | one shared database | one database per service |
| Scaling | scale the whole app | scale only the busy service |
| Complexity | low | high (ops, monitoring, debugging) |

### 3. ⭐ Advantages of microservices?
- Independent deploys: the team ships without waiting for others
- Scale only what's busy (e.g. 10 order pods, 2 user pods)
- Fault isolation: one service crashing doesn't have to take down everything
- Each team can choose its own tech or language

### 4. ⭐ Disadvantages?
- Network delay and **partial failures**
- Transactions across services are hard (no single DB transaction)
- Harder debugging, so you need tracing and central logs
- Much more ops work: CI/CD, containers, monitoring

### 5. ⭐ When should you NOT use microservices?
For a new product, a small team, or an unclear domain. Start with a **clean
modular monolith**. Split when **teams** block each other, not when code
modules do.

> Strong line: "Splitting a good monolith later is easy. Merging a bad mesh of services is very hard."

### 6. What is a distributed monolith?
Services that are split into separate programs but still **tightly linked**:
they share a database, or must be deployed together. You pay all the costs of
microservices and get none of the benefits.

### 7. How do you decide service boundaries?
Split by **business capability**, not by technical layer (DDD calls this a
"bounded context"). Good: `users`, `orders`, `payments`. Bad: `database-service`,
`validation-service`.
Test: "Can this service change and deploy without changing another one?"

---

## B. Communication

### 8. ⭐ How do microservices communicate?
- **Sync**: REST (HTTP + JSON) or gRPC. The caller waits for a reply.
- **Async**: a message broker (Kafka, RabbitMQ, NATS). Fire an event and move on.

### 9. ⭐ REST vs gRPC?
| | REST | gRPC |
|---|---|---|
| Format | JSON (text) | Protobuf (binary, smaller) |
| Transport | HTTP/1.1 or 2 | HTTP/2 |
| Contract | loose (OpenAPI docs) | strict `.proto`, code generated from it |
| Browser support | yes | not directly |
| Streaming | limited | built in |
| Best for | public APIs | internal service-to-service calls |

> In my project: browser → gateway is REST; gateway → order → user is gRPC.

### 10. ⭐ What is an API Gateway and why use it?
The **single public entry point**. It handles auth, rate limiting, CORS,
request-ID creation, input checks, and protocol translation (HTTP ↔ gRPC).
Internal services stay private.
**Trap:** it must NOT contain business logic, or it becomes a new monolith.

### 11. What is service discovery?
How service A finds service B's address when IPs keep changing (pods restart).
- **Client-side**: ask a registry (Consul, etcd), then call the address
- **Server-side**: call a stable name and let a load balancer route it
  (Kubernetes `Service` / DNS such as `user-service:50051`)

### 12. What is a service mesh?
A sidecar proxy (Envoy) next to every service handles retries, mTLS, traffic
routing and metrics **outside your code**. Examples: Istio, Linkerd.
Use it when you have many services. Overkill for 3.

### 13. How do you version an API without breaking clients?
- Only make **additive** changes (add fields, never rename or remove)
- Protobuf: never reuse field numbers; mark old ones `reserved`
- For breaking changes: a new version (`/v2`, `order.v2` package) running side by side
- Contract tests or `buf breaking` in CI

---

## C. Sync vs Async

### 14. ⭐ Sync vs async communication?
| | Sync | Async |
|---|---|---|
| Example | phone call | WhatsApp message |
| Caller waits? | yes | no |
| Receiver down | caller fails too | message waits in the queue |
| Coupling | tight | loose |
| Consistency | immediate | eventual |
| Debugging | easier | harder |

### 15. ⭐ When to use which? Give examples.
- **Sync** when you **need the answer to continue**: credit check, stock check, login.
- **Async** for **side effects** that can happen later: sending emails, updating
  analytics, notifying the warehouse.

> In my project: `ValidateCredit` must be sync, because you can't confirm an order without it. "Send receipt" should be async.

### 16. ⭐ What is eventual consistency?
With async, different services' data agrees **after a short delay**, not
instantly. Example: an order shows `PENDING` until payment is confirmed a few
seconds later.

### 17. Kafka vs RabbitMQ?
| | Kafka | RabbitMQ |
|---|---|---|
| Model | log / stream (messages are kept) | queue (message is removed once consumed) |
| Replay old messages | yes | no |
| Best for | event streaming, high volume, analytics | task queues, routing, simple jobs |

### 18. Event vs command?
- **Command**: "do this" (`ChargeCard`). There's one receiver, and it can say no.
- **Event**: "this happened" (`OrderCreated`). Any number of listeners, and it's a past-tense fact.

### 19. 🔥 What are "at-least-once" delivery and idempotent consumers?
Most brokers guarantee **at least once**, so the same message can arrive twice.
The consumer must be **idempotent**: processing it twice gives the same result
(store processed message IDs and skip duplicates).
"Exactly once" is very hard. You fake it with at-least-once delivery plus idempotency.

### 20. What is a dead-letter queue (DLQ)?
Messages that keep failing after N retries are moved to a separate queue so
they don't block everything else. A person or a job inspects them later.

---

## D. Data

### 21. ⭐ Why database-per-service?
So each service can change its schema and deploy on its own. With a shared DB,
one table change can break three services.

### 22. ⭐ Service A needs data owned by service B. How?
1. **Ask B through its API** (sync), the simplest option
2. **Keep a local copy**: B publishes events (`UserUpdated`) and A stores the
   fields it needs (async, eventually consistent)
3. Never read B's database directly

> In my project: order asks user via `ValidateCredit`; it never touches the user data.

### 23. 🔥 How do you do a transaction across two services? (Saga)
You can't use one DB transaction. Use the **Saga pattern**: a chain of local
transactions, each with a **compensating action** (an undo step).
```
Create order → Charge card → Reserve stock
                                 ✗ fails
               Refund card  ←  Cancel order   (compensations)
```
- **Choreography**: services react to each other's events (no boss)
- **Orchestration**: one coordinator tells each service what to do

### 24. 🔥 What is the Outbox pattern?
Problem: you save the order in the DB, then the app crashes before it publishes
`OrderCreated`, so the event is lost.
Fix: write the order **and** the event into an `outbox` table in **the same DB
transaction**. A separate process reads the outbox and publishes. Nothing is lost.

### 25. What is CQRS?
**Command Query Responsibility Segregation**: separate models for writes and
reads. Writes go to one store; a read-optimized copy (built from events) serves
queries. Useful when reads and writes have very different needs.

---

## E. Resilience (failure handling)

### 26. ⭐ Service B is down. What happens to A, and how do you protect it?
Without protection, A's requests hang, goroutines pile up, and A goes down too
(a **cascading failure**). Protect it with:
1. **Timeout** on every call
2. **Retry** with backoff + jitter (only for temporary errors)
3. **Circuit breaker** to stop calling a service that's clearly down
4. **Fallback / degraded mode** to give a reduced answer instead of an error

### 27. ⭐ Why must every network call have a timeout?
Without one, a slow service holds your goroutine, connection and memory
forever. Rule: a **downstream timeout must be shorter than yours**
(e.g. gateway 3 s → order 300 ms per try).

### 28. ⭐ What is a circuit breaker?
```
CLOSED ──(N failures)──▶ OPEN ──(cooldown)──▶ HALF-OPEN ──(probe ok)──▶ CLOSED
  normal calls           fail instantly        let 1 test call      (probe fails → OPEN)
```
**Retry vs breaker:** a retry *hopes* the next call works. A breaker has
*decided* it won't, and fails in microseconds.

> In my project: 3 failures in a row → open for 2 s (demo steps 10–12).

### 29. 🔥 Which errors should you retry?
- ✅ Retry: `Unavailable`, `ResourceExhausted`, `Aborted` (temporary)
- ❌ Don't: `InvalidArgument` (the answer won't change), `NotFound`,
  `DeadlineExceeded` (you're already out of time), `Internal` (a bug)

### 30. 🔥 Why are retries dangerous?
1. **Duplicates**: the request may have succeeded and only the reply was lost,
   so a retry double-charges. Fix: **idempotency key**.
2. **Retry storm**: gateway 3× × order 3× × user 3× = **27 calls** to a service
   that's already struggling. Fix: retry at **one layer only**.
3. All clients retry at the same moment. Fix: **jitter** (random delay).

### 31. ⭐ What is an idempotency key?
A unique key the client sends with a write. The server stores `key → result`;
if the same key comes again, it returns the **saved result** instead of doing
the work twice.

> In my project: demo step 7, the same key twice returns order `o3` both times.

### 32. What is degraded mode / graceful degradation?
When a dependency is down, give a smaller result instead of an error.
**It's a business decision:**
- fail **closed** → reject (safe, loses sales)
- fail **open** → accept and verify later (keeps sales, takes some risk)

> In my project: orders ≤ ₹100 are accepted as `PENDING`; bigger ones get 503.

### 33. What are bulkhead and rate limiting?
- **Bulkhead**: separate pools (connections, goroutines) per dependency, so one
  slow service can't use up all resources. Named after ship compartments.
- **Rate limiting**: cap requests per client per second, which protects you from overload.

---

## F. Observability & errors

### 34. ⭐ How do you debug a request that goes through 5 services?
**Distributed tracing**: create a request/trace ID at the edge, pass it in
headers/metadata on every hop, and log it everywhere. Tools: OpenTelemetry + Jaeger.

> In my project: one `request_id` shows up in gateway, orderd and userd logs.

### 35. What are the three pillars of observability?
- **Logs**: what happened (structured JSON, e.g. Go `slog`)
- **Metrics**: numbers over time (requests/sec, error rate, latency), e.g. Prometheus
- **Traces**: the path of one request across services

### 36. 🔥 Why does error-code mapping matter?
If every upstream error becomes HTTP 500, clients retry things that will never
work, and your alerts fire on non-bugs. Map correctly, **once, at the gateway**:
`NotFound→404`, `InvalidArgument→400`, `FailedPrecondition→422`,
`Unavailable→503`, `DeadlineExceeded→504`.

### 37. What are health checks?
- **Liveness**: "is the process alive?" If not, restart it.
- **Readiness**: "can it take traffic right now?" If not, stop sending requests.

> In my project: the gRPC health service (`grpc.health.v1.Health/Check`).

---

## G. Deployment & scaling

### 38. ⭐ How are microservices usually deployed?
Each service runs in a **container** (Docker), and an orchestrator
(**Kubernetes**) handles running, scaling, restarting and routing. Each service
gets its own **CI/CD pipeline**.

### 39. What is graceful shutdown and why does it matter?
On a stop signal (SIGTERM): stop taking new requests, **finish the ones in
progress**, close connections, then exit. Without it, every deploy drops users'
requests.

> In my project: `platform/server.go` drains gRPC before exiting.

### 40. Deployment strategies?
- **Rolling**: replace pods a few at a time
- **Blue-green**: two full environments, then switch traffic at once
- **Canary**: send 5% of traffic to the new version, watch it, then increase

---

## H. Security

### 41. How do you secure service-to-service calls?
- **mTLS**: both sides prove who they are with certificates (often handled by the mesh)
- Pass the user's identity as a **JWT**, which the gateway checks
- Keep internal services off the public network; only the gateway is exposed
- Never forward internal error messages to clients (they leak details)

---

## I. Scenario questions (they test thinking, not facts)

### 42. ⭐ "Design order placement for an e-commerce site."
1. Client → **gateway** (auth, validation)
2. Gateway → **order service** (sync, with an idempotency key)
3. Order → **inventory** and **payment** (sync, because the answer is needed now), with timeouts, retries and a breaker
4. Save the order + an `OrderCreated` event in the **outbox**
5. Event → **email**, **analytics**, **shipping** (async)
6. If payment fails after stock was reserved → **saga** compensation (release stock)

### 43. "The payment service is slow and checkout is timing out. What do you do?"
Check traces to find where time goes → put a timeout on the payment call
shorter than checkout's → add a circuit breaker → decide on a fallback (accept
as `PENDING`?) → scale payment / fix the slow query → alert on p99 latency.

### 44. "A customer was charged twice. Why, and how do you prevent it?"
Most likely a **retry after a lost reply**: the first charge succeeded and the
response timed out. Fix: an **idempotency key** on the payment request, and
idempotent message consumers.

### 45. "Your company has a monolith. How would you migrate?"
**Strangler Fig pattern**: put a gateway/proxy in front, move one capability at
a time into a new service (start with the least tangled one), route its traffic
to the new service, and repeat. Never do a big-bang rewrite.

---

## J. Go-specific (good for Go roles)

### 46. Why is Go popular for microservices?
Small single binary (tiny Docker image), fast startup, cheap goroutines for
many concurrent requests, a strong standard library (`net/http`, `context`),
and first-class gRPC support.

### 47. ⭐ How does `context` help in microservices?
It carries **deadlines, cancellation and request-scoped values** (request ID)
across every function **and every network hop**. If the client disconnects,
the context is cancelled and all downstream work stops.
```go
ctx, cancel := context.WithTimeout(r.Context(), 300*time.Millisecond)
defer cancel()
resp, err := client.ValidateCredit(ctx, req)
```

### 48. How do you call two services at the same time in Go?
`errgroup`: run both calls concurrently. If one fails, the shared context is
cancelled and the first error is returned.

> In my project: `/users/:id/dashboard` fetches the user and the orders in parallel.

---

## 30-second pitch (memorise this)

> "Microservices split a system into small, independently deployable services,
> each owning its own data. They talk **synchronously** over REST or gRPC when
> the caller needs an answer right away, and **asynchronously** through a broker
> like Kafka for side effects, which gives loose coupling at the cost of
> eventual consistency. Because network calls fail, I always use timeouts,
> retries with idempotency keys, circuit breakers and tracing. And I wouldn't
> start with microservices: a clean monolith first, split when teams start
> blocking each other."
