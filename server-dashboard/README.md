# Server Fleet Dashboard — "10 servers report stats, keep a dashboard live" demo

This is a runnable answer to the interview question: *10 servers send
health/stat data to you — how do you manage that many servers and keep a
dashboard updated?*

```bash
# terminal 1
cd backend && go run ./cmd/server

# terminal 2
cd frontend && npm install && npm run dev
```

Open http://localhost:5173. Ten simulated servers report every 1.5–3s; the
dashboard updates live, no page refresh.

## The interview answer, in one paragraph

Don't have the browser poll 10 servers directly, and don't have the browser
poll your backend on a timer either — both scale badly and add latency.
Instead: each server **pushes** its stats to one backend over HTTP; the
backend holds the latest stats **in memory, behind a mutex**, keyed by
server ID; and the backend **pushes** updates out to every open dashboard
over a **WebSocket**. Two independent fan-in/fan-out problems, each solved
with the tool built for it.

## Architecture

```
 server-01 ─┐
 server-02 ─┤  POST /api/servers/{id}/health
    ...     ├─────────────────────────────────►  Go backend
 server-10 ─┘                                         │
                                                        │ 1. Upsert into
                                                        │    registry.Registry
                                                        │    (map + RWMutex)
                                                        │
                                                        │ 2. hub.Broadcast(msg)
                                                        ▼
                                              hub.Hub.Run  (one goroutine,
                                              owns the client set — no lock
                                              needed on it)
                                                        │
                                     fan-out over WebSocket, one buffered
                                     channel + one write-goroutine PER
                                     connected browser tab
                                                        │
                                                        ▼
                                    ┌──────────┐  ┌──────────┐  ┌──────────┐
                                    │dashboard │  │dashboard │  │dashboard │
                                    │  tab 1   │  │  tab 2   │  │  tab N   │
                                    └──────────┘  └──────────┘  └──────────┘
```

Code, mapped to that diagram:

| Piece | File | Concept it demonstrates |
|---|---|---|
| Thread-safe latest-stats store | [`backend/internal/registry/registry.go`](backend/internal/registry/registry.go) | `sync.RWMutex` — many concurrent writers (one per server), even-more-concurrent readers (every dashboard poll/connect) |
| Pub/sub fan-out to browsers | [`backend/internal/hub/hub.go`](backend/internal/hub/hub.go) | Channels instead of a locked map: the client set is only ever touched by one goroutine (`Run`'s `select` loop), so it needs no mutex at all |
| Per-connection read/write pumps | [`backend/internal/hub/client.go`](backend/internal/hub/client.go) | gorilla/websocket connections aren't safe for concurrent writes — exactly one goroutine per client may write, fed by a buffered channel |
| The real ingest path | [`backend/internal/api/api.go`](backend/internal/api/api.go) | `POST /api/servers/{id}/health` — what a real server's agent would call |
| Standing in for the 10 real servers | [`backend/internal/simulator/simulator.go`](backend/internal/simulator/simulator.go) | 10 independent goroutines, each its own ticker + jitter, `context.Context` for shutdown, `sync.WaitGroup` to know when they've all stopped |
| Wiring + graceful shutdown | [`backend/cmd/server/main.go`](backend/cmd/server/main.go) | `signal.NotifyContext`, `srv.Shutdown` |
| Live view | [`frontend/src/useServerFeed.js`](frontend/src/useServerFeed.js) | WebSocket client with reconnect/backoff, merging server-by-server updates into React state |

## Why these choices (the follow-up questions an interviewer will ask)

**"Why not just have the dashboard poll `GET /api/servers` every second?"**
It still works (that route exists, as a fallback / first-paint path — see
`useServerFeed`'s `loadInitialSnapshot`). But polling means every open tab
re-fetches all 10 servers' data on a timer regardless of whether anything
changed, and updates are only as fresh as the poll interval. Push means
zero wasted requests and sub-second latency, at the cost of holding one
open connection per tab.

**"Why a map + mutex instead of `sync.Map`?"** `sync.Map` is optimized for
disjoint-key or append-mostly workloads. Here every server repeatedly
overwrites its own single key — a plain map behind an `RWMutex` is faster
and, more importantly, lets you atomically read a full, consistent
snapshot for `GET /api/servers` (`sync.Map` has no such operation).

**"What happens if a dashboard tab is slow to read?"** `hub.Run`'s
broadcast loop does a non-blocking `select` with `default` on each client's
send channel — a full buffer means that one client gets dropped
(disconnected), not that the broadcast blocks and stalls the other nine
tabs. See the comment in `hub.go`.

**"What if a server stops reporting?"** Not implemented here on purpose —
it's the natural next exercise. You'd add a staleness sweep: a ticker
goroutine that walks the registry and flips any server whose
`UpdatedAt` is older than N seconds to `down`, then broadcasts that.

**"How does this scale past one backend instance?"** It doesn't, as
written — the registry and hub are in-process memory, so two backend
replicas would each see only the servers whose POSTs load-balanced their
way. Real fan-out across replicas needs a shared pub/sub (Redis
`PUBLISH`/`SUBSCRIBE`, NATS, Kafka) that every replica subscribes to and
re-broadcasts from — same `hub.Hub` code, different message source.
