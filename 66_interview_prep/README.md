# 66 — Interview prep: the Q&A bank

Answers written the way you should say them out loud: claim first, reason
second, trade-off third. Every answer points at runnable code in this repo, so
"I've built that" is always true.

Run the fourteen executable gotchas first:

```bash
go run ./66_interview_prep
```

---

## 1. The language

**Why does Go have no exceptions?**
Errors are values, so they are ordinary data you can wrap, compare and test.
`panic` exists for programmer bugs (nil map write, index out of range), not
for control flow. The cost is verbosity; the benefit is that every failure
path is visible at the call site instead of hidden in an invisible second
control flow. See `08_error_handling`, `35_errors_advanced`.

**`errors.Is` vs `errors.As` vs `==`?**
`Is` walks the `%w` chain looking for a specific sentinel (`ErrNotFound`).
`As` walks it looking for a specific *type* and assigns it, so you can read
fields off a custom error. `==` only works if nothing ever wrapped it — which
you cannot guarantee, so don't use it. Wrap with `%w` when the caller may
need to inspect the cause; use `%v` when the cause is an implementation
detail you don't want to make part of your API.

**When do you use a pointer receiver?**
If the method mutates, if the struct is large, or if it contains a mutex.
Then be consistent across the whole type — mixed receivers make the method
set confusing and break interface satisfaction for the value type (`Q11`).

**Interfaces: where do you declare them?**
In the *consumer* package, as small as possible. "Accept interfaces, return
structs." A one-method interface defined next to the code that needs it is
trivially mockable; a big interface exported by the implementer is a
maintenance liability. See `14_interfaces`, `61_sqlc` (`db.Querier`).

**What's the zero value discipline?**
A type should be useful with no constructor when it reasonably can be —
`sync.Mutex`, `bytes.Buffer`, `slog` handlers all work at zero value. It
removes a whole class of "forgot to call New" bugs.

**Generics — when?**
When you'd otherwise write the same function for three types, or use `any`
plus a type switch. Not for "flexibility" in general: generics complicate
inference and error messages, and an interface is usually the better tool
for behaviour (as opposed to containers/algorithms). See `21_generics`.

**Slices vs arrays; what's the cost model?**
An array is a value (copied on assignment); a slice is a 3-word header
(pointer, len, cap) over one. Passing a slice is cheap but shares memory —
which is where the aliasing bugs in `Q3`/`Q4` come from. `make([]T, 0, n)`
when you know the size: growth is amortised doubling plus a copy each time.

**What escapes to the heap?**
Whatever the compiler cannot prove stays within the function: anything
returned by pointer, stored in an interface, captured by a closure that
outlives the call, or of unknown size. Check with `go build -gcflags='-m'`.
This is the first thing to look at when an allocation-heavy benchmark is
slow. See `44_benchmarks_and_profiling`.

---

## 2. Concurrency

**Goroutine vs OS thread?**
A goroutine starts at ~2–8 KB of stack that grows on demand, and is
multiplexed by the Go runtime onto OS threads. You can run a million; you
cannot run a million threads. Switching between them is a user-space
operation, so it costs tens of nanoseconds rather than a syscall.

**Explain the GMP scheduler.**
G = goroutine, M = OS thread, P = a processor context (`GOMAXPROCS` of them,
default = CPU count). A P holds a run queue of Gs and must be held by an M to
run them. Work-stealing keeps Ps busy. A blocking syscall detaches the M so
the P can be handed to another M — which is why blocking I/O doesn't stall
your whole program. Since 1.14 goroutines are asynchronously preemptible, so
a tight CPU loop can no longer starve everything. See `49_go_scheduler`.

**Channels or mutexes?**
Channels to transfer *ownership* of data and to coordinate; a mutex to
protect *state* that several goroutines read and write. "Share memory by
communicating" is a default, not a law — a counter behind a mutex is simpler
and faster than a channel-based one.

**What does `select` do when several cases are ready?**
Picks one uniformly at random, so no case can starve. A `default` makes the
whole select non-blocking. A `nil` channel case blocks forever, which is the
idiomatic way to *disable* a case dynamically.

**How do you stop a goroutine?**
You don't — you ask it to stop. Cancellation is cooperative: pass a
`context.Context` and have the goroutine select on `ctx.Done()`. There is no
`goroutine.Kill()`, by design. See `25_context`.

**What causes a goroutine leak?**
A goroutine blocked forever: sending on a channel nobody reads (fix: buffer
of 1, or select on ctx), ranging over a channel nobody closes, or waiting on
a context that never cancels. Find them with
`go tool pprof <url>/debug/pprof/goroutine` or `go.uber.org/goleak` in tests.
See `64_resilience` demo 7.

**Deadlock vs livelock vs starvation?**
Deadlock: everyone waits, nothing moves (Go's runtime detects the total case
and panics). Livelock: everyone moves, nothing progresses. Starvation: one
goroutine never gets scheduled or never gets the lock. See `55`–`57`.

**When is `sync.Map` the right choice?**
Rarely: only for append-mostly caches or disjoint key sets per goroutine. A
plain map behind an `RWMutex` is faster for most workloads and far easier to
reason about. Measure first.

**atomic vs mutex?**
`sync/atomic` for a single word (counter, flag, swapped pointer). A mutex the
moment you need two fields to change together — atomics give you no
atomicity *across* variables. See `54_atomic`, `50_rwmutex`.

**What does `-race` actually do?**
Instruments memory accesses and uses a happens-before (vector clock)
algorithm to report unsynchronised access from different goroutines. It only
detects races that actually occur in that run, so run it against your tests
and, if you can afford ~5–10x slowdown, in staging. See `45`, `58`.

---

## 3. HTTP and API design

**Walk me through a request in `net/http`.**
`Server.Serve` accepts a connection, spawns a goroutine per connection, the
mux routes by pattern (Go 1.22+ supports `GET /users/{id}`), middleware wraps
the handler, the handler writes to a `ResponseWriter`. Nothing is written
until you call `Write` or `WriteHeader` — so headers must be set first.
See `27`, `39`.

**How is middleware written?**
`func(http.Handler) http.Handler`. Order matters: recovery outermost so it
catches everything, then request-id, logging, auth, rate limiting, and the
timeout *outside* recovery so a panic doesn't escape the timeout wrapper.
See `39_middleware`.

**Which timeouts does a public server need?**
`ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`, `IdleTimeout` on the
server, plus a per-request context deadline. Without them one slow client
holds a connection and goroutine open indefinitely (Slowloris).

**Graceful shutdown, in order.**
Flip readiness to unhealthy → wait for the load balancer to notice → stop
accepting and drain in-flight requests (`srv.Shutdown`) → stop background
workers → close database and queue connections. Skip the first step and you
drop the requests the LB routed a millisecond before you died. See `42`,
and `63_microservices/internal/platform/server.go` for the gRPC twin.

**REST or gRPC?**
gRPC between your own services: a compiler-enforced contract, binary frames,
HTTP/2 multiplexing, real streaming. REST at the public edge: browsers,
caching, curl, and every integration partner. Most systems run both, with a
gateway translating at the boundary — which is exactly `63_microservices`.

---

## 4. Databases

**`database/sql`: what is `*sql.DB`?**
A *pool*, not a connection. It's safe for concurrent use, so you create one
and share it. Tune `SetMaxOpenConns` (never more than the server allows
divided by the number of replicas), `SetMaxIdleConns`, and
`SetConnMaxLifetime` (so connections rotate behind a proxy/failover).

**How do you avoid SQL injection?**
Placeholders, always. The driver sends the query and the arguments
separately, so a value can never become syntax. Never `fmt.Sprintf` a query.
For a dynamic `IN (...)`, generate the placeholders — or use `sqlc.slice`.

**sqlc vs GORM vs raw?**
sqlc: you write SQL, it generates type-safe Go, checked against the schema at
build time; zero runtime reflection. GORM: fast to start, opaque queries,
easy N+1, and you eventually fight it. Raw: fine, but `rows.Scan` rots
silently when a column is added. Default to sqlc for a service. See `38`,
`61`.

**Transactions — what do you watch for?**
Keep them short, never do network I/O inside one, always `defer tx.Rollback()`
(it's a no-op after commit), and pick the isolation level deliberately.
Postgres defaults to Read Committed; `SERIALIZABLE` will return
serialization failures that you must *retry* — which is why gRPC has an
`Aborted` code.

**Migrations?**
Versioned, forward-only, checked into git, run as a separate step (job or
init container) rather than on app start — otherwise five replicas race to
migrate. Expand/contract for anything breaking: add the new column, deploy
code that writes both, backfill, switch reads, then drop. See `37`.

---

## 5. Microservices and distributed systems

**When should a team NOT use microservices?**
Almost always at the start. You pay in latency, partial failure, distributed
transactions, and operational tooling. Split when *teams* block each other,
not when modules feel big. A well-layered monolith is easy to split later; a
bad mesh is nearly impossible to merge.

**How do services find each other?**
DNS in the target string (`dns:///orders.svc:50051`) plus a client-side
load-balancing policy is enough in Kubernetes. Otherwise a registry (Consul,
etcd) via a resolver, or a mesh sidecar. gRPC bakes the resolver + balancer
into the `ClientConn`, so it's one line of config. See
`63_microservices/internal/platform/server.go`.

**How do you prevent a cascading failure?**
Deadlines on every call, with a *budget* that shrinks going down the tree;
retries only on retryable codes, with backoff and jitter, at one layer only;
a circuit breaker so a dead dependency fails in microseconds instead of
piling up goroutines; bulkheads (a separate pool per dependency) so one slow
dependency can't consume every worker; and load shedding at the edge.

**Draw the circuit breaker.**
CLOSED → (n consecutive failures) → OPEN → (cooldown) → HALF-OPEN → probe
succeeds → CLOSED, probe fails → OPEN. The point is failing *fast*, not
retrying. Code: `63_microservices/internal/platform/resilience.go`.

**Why are retries dangerous?**
They multiply. Three layers retrying 3× each is 27 requests against a
database that is already on fire. And they are only safe if the operation is
idempotent — `Unavailable` can also mean "it worked, the response was lost".
Hence idempotency keys on every write.

**How do you do a distributed transaction?**
You don't — two-phase commit across services is a liveness trap. Use a saga:
local transactions plus compensating actions, driven by events. To avoid the
dual-write problem (DB commit succeeds, event publish fails), use the
transactional outbox: write the event to a table in the *same* transaction
and have a relay publish it.

**Exactly-once delivery?**
Doesn't exist end-to-end. You get at-least-once delivery plus idempotent
processing, which is observationally equivalent. Say that sentence.

**How do you debug a slow request across five services?**
Propagate a request/trace id from the edge and log it on every hop
(`63_microservices/internal/platform/observability.go`), then upgrade to
OpenTelemetry spans for the timing waterfall. Complement with RED metrics
(rate, errors, duration) per endpoint, and alert on p99 plus error rate, not
averages.

**gRPC status codes — which are retryable?**
`Unavailable`, `ResourceExhausted`, `Aborted`. Not `DeadlineExceeded` (the
budget is already gone), not `InvalidArgument`/`NotFound`/`PermissionDenied`
(the answer won't change), not `Internal` (retrying a bug doesn't fix it).

---

## 6. Queues and background work

**Why a queue rather than `go doWork()`?**
Durability (the job survives a crash or deploy), backpressure (a queue
absorbs a spike instead of melting the database), retries with backoff, and
independent scaling of workers vs API. A bare goroutine has none of these.
See `65_asynq`.

**What must every handler be?**
Idempotent. Delivery is at-least-once, so a job *will* occasionally run twice.

**Dead-letter queue — what for?**
A failure that has exhausted retries is parked, not lost: you fix the bug and
replay it. Alert on archive size *and* on the age of the oldest pending job —
a queue of 10 that hasn't moved in an hour is worse than 10,000 that are
draining.

---

## 7. Testing

**Table-driven tests — why the default?**
One test body, N cases, each as a named subtest so failures point at the
case. Add `t.Parallel()` to surface shared-state bugs. See `20`, `43`.

**Mock or fake?**
Fake (a small hand-written implementation) when you care about resulting
*state*; mock (testify/mock) when you care about *interactions* — was it
called, with what, how many times. Don't mock what you don't own: wrap the
third-party client in your own small interface and fake that. See `59`.

**How do you test HTTP handlers?**
`httptest.NewRecorder` for a handler in isolation (fast, no socket) and
`httptest.NewServer` when you need a real client and URL. See `43`.

**Integration tests against a database?**
`testcontainers-go` for a real Postgres per test run — because SQLite and
Postgres disagree exactly where it matters. Migrate, run, tear down.

**What coverage number do you aim for?**
None in particular. Cover the branches where a bug is expensive: error
paths, boundary conditions, concurrency. High coverage of trivial getters
buys nothing and makes refactoring harder.

---

## 8. Production

**How do you find a memory leak in Go?**
`/debug/pprof/heap` for live objects, `-inuse_space` vs `-alloc_space`, and
diff two snapshots. If the heap is flat but memory grows, look at goroutines
(each holds a stack) and at cgo/mmap. See `44`.

**A service's p99 latency is 10× the median. Where do you look?**
GC pauses (rare now, but check `GODEBUG=gctrace=1`), lock contention
(`/debug/pprof/mutex`, `block`), a connection pool that's too small (requests
queue), a slow dependency without a timeout, and noisy-neighbour CPU throttle
in Kubernetes. Profile in production; guessing is how you optimise the wrong
thing.

**How do you build the container?**
Multi-stage: build with the Go image, ship a `distroless`/`scratch` image
with just the binary. `CGO_ENABLED=0`, `-trimpath`, version via `-ldflags`.
Exec-form `ENTRYPOINT` so the binary is PID 1 and receives SIGTERM. See `46`.

**What does Go's GC actually do?**
Concurrent mark-and-sweep, non-generational, non-compacting, with a pacer
targeting `GOGC` (default 100 = heap doubles between collections). Tune with
`GOGC` or, better, `GOMEMLIMIT` as a soft ceiling — that's the flag that
stops a container being OOM-killed. Stop-the-world pauses are sub-millisecond;
the cost shows up as CPU, not pauses.

**`GOMAXPROCS` in a container?**
It defaults to the *host's* CPU count unless the runtime detects the cgroup
limit — historically it did not, so a 0.5-CPU pod spawned dozens of Ps and
thrashed. Go 1.25+ is container-aware; on older versions use
`automaxprocs`. Worth knowing as a war story either way.

---

## The three questions to have ready about yourself

1. **A production bug you diagnosed.** Symptom → hypothesis → how you proved
   it → the fix → what you changed so it can't recur. The last part is what
   separates senior answers.
2. **A design decision you'd now make differently.** Shows you review your
   own work rather than defending it.
3. **Something you chose *not* to build.** Scope control reads as seniority
   more reliably than any framework name.
