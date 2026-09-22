# 63 — Microservices: three services that actually talk to each other

Everything up to lesson 62 taught one process. This lesson is about what
changes when the function call you make can **fail, hang, or succeed without
you finding out**.

```
   browser / curl
        │  HTTP + JSON
        ▼
   ┌────────────────┐        gRPC         ┌─────────────────┐
   │    gateway     │ ──────────────────▶ │  order service  │
   │  (Gin, :8080)  │                     │    (:50052)     │
   └────────────────┘                     └─────────────────┘
        │  gRPC                                    │ gRPC
        │                                          │ + 300 ms deadline
        │                                          │ + retry (jitter)
        │                                          │ + circuit breaker
        │                                          ▼
        └───────────────────────────▶ ┌─────────────────┐
                                      │  user service   │
                                      │    (:50051)     │
                                      └─────────────────┘
```

Each service owns its own data. **Nothing shares a database** — that single
rule is the difference between microservices and a distributed monolith.

## Run it

One command, all three services in one process, with a scripted scenario
including a simulated outage:

```bash
go run ./63_microservices/cmd/demo
```

As three genuinely separate processes (do this at least once — it is what
production looks like). Three terminals:

```bash
go run ./63_microservices/cmd/userd
```

```bash
go run ./63_microservices/cmd/orderd
```

```bash
go run ./63_microservices/cmd/gateway
```

Then drive it:

```bash
curl -s localhost:8080/users/u1 | jq
```

```bash
curl -s -X POST localhost:8080/orders -H 'Content-Type: application/json' -H 'Idempotency-Key: k1' -d '{"user_id":"u1","lines":[{"sku":"XSHOT","quantity":2,"unit_price_cents":2999}]}' | jq
```

Kill `userd` with Ctrl+C and repeat the POST: you will watch the retries, the
breaker trip, and small orders fall back to `PENDING` while large ones get a
503. Start it again and the breaker closes after one probe.

Poke the gRPC services directly (reflection is on in dev):

```bash
grpcurl -plaintext localhost:50051 list
```

```bash
grpcurl -plaintext -d '{"id":"u1"}' localhost:50051 user.v1.UserService/GetUser
```

```bash
grpcurl -plaintext localhost:50051 grpc.health.v1.Health/Check
```

Regenerate the protobuf code after editing a `.proto`:

```bash
cd 63_microservices && buf lint && buf generate
```

## Where each idea lives

| Concern | File |
|---|---|
| request-id propagation, slog, recovery, logging interceptors | `internal/platform/observability.go` |
| retry + backoff + jitter, circuit breaker state machine | `internal/platform/resilience.go` |
| server construction, health, graceful drain, `Dial` + discovery | `internal/platform/server.go` |
| a leaf service (+ fault injection for the demo) | `internal/userservice/service.go` |
| a service with a dependency: deadlines, degraded mode, idempotency | `internal/orderservice/service.go` |
| the edge: gRPC↔HTTP code mapping, DTOs, errgroup fan-out | `internal/gateway/gateway.go` |

## The decisions worth defending in an interview

**Deadline budget.** 300 ms per attempt inside the order service, 3 s at the
gateway, and the client's own timeout above that. A downstream timeout must
always be *shorter* than yours, or you report `DeadlineExceeded` upward while
your dependency is still happily working.

**Retry only what is retryable.** `Unavailable`, `ResourceExhausted`,
`Aborted`. Never `DeadlineExceeded` (you already ran out of time), never
`InvalidArgument` (the answer will not change), never `Internal` (hammering a
bug does not fix it).

**Retry at one layer only.** Gateway 3× × order 3× × user 3× = 27 requests to
a database that is already on fire. That is a retry storm, and it is how a
small outage becomes an outage.

**Idempotency keys are what make retries safe.** `Unavailable` can also mean
"your write succeeded and the response was lost". Without a key, the retry
charges the customer twice — invisible in testing, expensive in production.

**Circuit breaker ≠ retry.** A retry hopes the next call works. A breaker has
concluded it will not, and fails in microseconds instead of burning a
goroutine per request on a call that will time out anyway.

**Degraded mode is a business decision, not a technical one.** Here: orders
under ₹100 are accepted as `PENDING` and verified later; larger ones are
rejected with 503. Fail-open versus fail-closed depends on what is at stake,
and a reconciliation job has to finish the job either way.

**Status code translation happens exactly once, at the edge.** Flattening an
upstream `DeadlineExceeded` into a 500 is the most common error-handling bug
in a mesh — your error budget then burns on things that are not bugs.

## When NOT to do this

Almost always, at the start. Microservices buy you independent deploys and
team autonomy, and they cost you network latency, partial failure,
distributed transactions, and an operational burden that needs real tooling.

Split when **teams** start blocking each other, not when modules do. A
well-layered monolith with clean package boundaries beats a bad distributed
system every time — and splitting a good monolith later is far easier than
merging a bad mesh.
