# Ad Click Aggregation Platform

Event-driven analytics for ad clicks: **Go** API and consumers, **Kafka** as the durable buffer, **PostgreSQL** for aggregates, **Next.js** dashboard, **Prometheus + Grafana** for observability.

Phase-1 requirements and sizing: [docs/phase-1-architecture.md](docs/phase-1-architecture.md).

## Quick start

```bash
docker compose up -d --build        # Kafka x3, Postgres, API, consumer, web, Prometheus, Grafana
go run ./cmd/loadgen -rps 500 -duration 30s
```

| What | URL |
|---|---|
| Dashboard | http://localhost:3000 |
| API | http://localhost:8080 (`POST /v1/clicks`, `GET /c/{ad_id}`) |
| Prometheus | http://localhost:9090 |
| Grafana (dashboard "Ad Click Pipeline") | http://localhost:3001 |

Send one click by hand:

```bash
curl -X POST localhost:8080/v1/clicks -d '{"ad_id":"ad-1001","user_id":"u42","country":"US"}'
```

Seed ads: `ad-1001, ad-1002, ad-2001, ad-2002, ad-3001, ad-3002, ad-4001, ad-4002`. Configuration is by environment variables, see [.env.example](.env.example).

## Run without Docker (single Kafka broker)

Needs Java 17, Go and Node. Use one terminal per service. Commands are for Git Bash.

**1. Get Kafka (once).** Download <https://archive.apache.org/dist/kafka/4.0.0/kafka_2.13-4.0.0.tgz>, extract it, and point `KAFKA_HOME` at the folder. Copy `config/server.properties` to a new file (`my-server.properties`) and set `log.dirs=` to an empty folder you own.

**2. Format Kafka (once).** Kafka's Windows `.bat` scripts fail with "input line too long", so call the Java classes directly:

```bash
cd "$KAFKA_HOME"
ID=$(java -cp "libs/*" kafka.tools.StorageTool random-uuid | tail -1)
java -cp "libs/*" kafka.tools.StorageTool format --standalone -t "$ID" -c /path/to/my-server.properties
```

**3. Terminal 1, Kafka:**

```bash
cd "$KAFKA_HOME"
java -Xmx512m -cp "libs/*" kafka.Kafka /path/to/my-server.properties
```

**4. Terminal 2, Postgres** (a throw-away embedded Postgres, no Docker). Use another port if 5432 is taken:

```bash
go run ./cmd/devpg -dir /path/to/pgdata -port 55432
```

**5. Terminal 3, consumer** (creates the topics and the tables on start-up; wait for "postgres ready" first):

```bash
export DATABASE_URL="postgres://adclick:adclick@localhost:55432/adclick?sslmode=disable" RETRY_DELAY=2s
go run ./cmd/consumer
```

**6. Terminal 4, API:**

```bash
export DATABASE_URL="postgres://adclick:adclick@localhost:55432/adclick?sslmode=disable"
go run ./cmd/api
```

**7. Terminal 5, dashboard** (first start takes about 30 s to compile):

```bash
cd web && npm install && npm run dev
```

Check the API is ready: `curl -i localhost:8080/readyz` should return `200`. Stop everything with Ctrl+C in each terminal.

## Demo walkthrough

Works with either way of running. Keep the dashboard (http://localhost:3000) open beside the terminal. These are the results from a real run on a laptop with a single Kafka broker.

| # | Do | You should see |
|---|---|---|
| 1 | Send one click (command above) | `202`; dashboard total goes 0 → 1 |
| 2 | Send the same `event_id` three times | Total goes up by 1, not 3 (duplicates ignored) |
| 3 | Send `{"ad_id":"nope"}` | `404`, rejected before Kafka |
| 4 | `go run ./cmd/loadgen -rps 1000 -duration 15s` | 0 errors; dashboard total equals the "distinct events" line (plus any manual clicks) |
| 5 | Kill the consumer, run the load for 5 s, restart it | Dashboard freezes but the API still answers `202`; after restart the total catches up with nothing lost (a hard kill can take about 45 s, the Kafka session timeout) |
| 6 | Send garbage into `ad-click-events` (below) | It appears in `ad-click-dlq` with an `x-error` header giving the reason |
| 7 | Check consumer lag (below) | `LAG` is 0 once caught up; shows which worker owns which partition |

Duplicates (step 2):

```bash
B='{"event_id":"01a1e934-0000-7000-8000-00000000d00d","ad_id":"ad-1002","user_id":"bob"}'
for i in 1 2 3; do curl -s -o /dev/null -w "%{http_code}\n" -X POST localhost:8080/v1/clicks -H 'Content-Type: application/json' -d "$B"; done
```

Poison message and DLQ (step 6), from `$KAFKA_HOME`:

```bash
echo 'not json {{{' | java -cp "libs/*" org.apache.kafka.tools.ConsoleProducer --bootstrap-server localhost:9092 --topic ad-click-events
java -cp "libs/*" org.apache.kafka.tools.consumer.ConsoleConsumer --bootstrap-server localhost:9092 --topic ad-click-dlq --from-beginning --max-messages 1 --property print.headers=true
```

Consumer lag (step 7):

```bash
java -cp "libs/*" org.apache.kafka.tools.consumer.group.ConsumerGroupCommand --bootstrap-server localhost:9092 --describe --group click-aggregators
```

With Docker, the same Kafka tools are available inside the broker container, and the failure drills further down apply as written.

## Architecture

```
 Browser/Ad ──► Go API ──(acks=all, idempotent)──► Kafka: ad-click-events (12 partitions, RF=3)
                  │                                      │ consumer group "click-aggregators"
                  │ ad cache (memory,                    ▼
                  │ refreshed from PG)           Go consumers (N workers x M processes)
                  ▼                              validate → enrich → dedup → aggregate
            202 after Kafka ack                       │            │
                                          transient   │            │ ONE Postgres transaction per batch,
                                          failure     ▼            ▼ then commit Kafka offsets
                                   ad-click-retry   (delay)    PostgreSQL
                                          │ after N hops          ▲
                                          ▼                       │ read
                                    ad-click-dlq        Go query API + SSE ──► Next.js dashboard
```

### Event lifecycle

```
API:      validate ─► build Event(UUIDv7) ─► produce (wait for all in-sync replicas) ─► 202
Consumer: poll batch ─► decode/validate ──bad──► DLQ
                        │ unknown ad ───────────► retry topic (+delay, attempt+1) ─► DLQ after MAX_RETRIES
                        ▼ good events
                  BEGIN
                    INSERT click_events ... ON CONFLICT DO NOTHING RETURNING   ← which events are NEW
                    INSERT click_uniques ... ON CONFLICT DO NOTHING RETURNING  ← first click of visitor/ad/day
                    UPSERT click_agg (minute, hour, day) += deltas             ← only for NEW events
                  COMMIT
                  commit Kafka offsets            ← only now
```

## Code map

| Package | Responsibility |
|---|---|
| `internal/event` | Click model, validation, partition key, IP hashing |
| `internal/httpapi` | Ingestion (`POST /v1/clicks`, `GET /c/{ad}`), query API, SSE, middleware |
| `internal/kafka` | Topic creation, idempotent producer, headers |
| `internal/consumer` | `Processor` (pure logic, tested with fakes) and `Run` (franz-go loop, lag monitor) |
| `internal/aggregate` | Pure function: events → counter deltas |
| `internal/store` | All SQL, migrations, read queries |
| `internal/adcache` | In-memory ads table copy |
| `internal/metrics`, `internal/logging` | Prometheus metrics, `slog` JSON logs |
| `cmd/api`, `cmd/consumer` | The two services |
| `cmd/loadgen`, `cmd/dlqreplay` | Load generator, DLQ replay tool |
| `migrations/` | Schema (embedded in the binaries) |
| `web/` | Next.js dashboard |

## Kafka concepts: where each one lives

| Concept | Where / how |
|---|---|
| Topics and partitions | `kafka.EnsureTopics`: events 12, retry 6, dlq 3. Partition count caps consumer parallelism and is hard to reduce later, so size for the future. |
| Partition key | `Click.PartitionKey()` = `ad_id`. Same ad ⇒ same partition ⇒ ordered per ad, and a consumer batches updates to the same aggregate rows (fewer row-lock conflicts). Cost: one very hot ad concentrates on one partition (skew). Alternative: key by `event_id` for perfect balance, but you lose per-ad locality. |
| Replication | RF=3, `min.insync.replicas=2`, `acks=all`. An acknowledged click is on at least 2 brokers; one broker can die with no loss and no write outage. |
| Idempotent producer | franz-go enables it by default (needs acks=all). Broker sequence numbers drop duplicates from producer retries. This protects the producer→broker hop only. |
| Consumer groups, rebalancing | One group, `CONSUMER_CONCURRENCY` clients per process (each is a group member), plus any number of processes. `BlockRebalanceOnPoll` stops a rebalance from happening mid-batch; revocations are counted in `adclick_consumer_rebalances_total`. |
| Manual offset commit, at-least-once | `DisableAutoCommit`; `CommitRecords` runs after the DB transaction commits. A crash in between ⇒ redelivery, never loss. |
| Duplicates | `click_events.event_id` primary key plus `ON CONFLICT DO NOTHING`: redelivered or double-sent events never move a counter. Clients may send their own `event_id` so API retries dedup too. |
| Retry and DLQ | Invalid (permanent) ⇒ DLQ at once. Unknown ad (maybe transient) ⇒ `ad-click-retry` with exponential delay header, DLQ after `MAX_RETRIES`. Failed parking blocks the commit, so a message is never lost. `cmd/dlqreplay` re-injects DLQ messages. |
| Ordering | Guaranteed per partition only. Aggregation is commutative (`clicks = clicks + delta`), so cross-partition and out-of-order arrival do not matter. |
| Backpressure | Producer: `MaxBufferedRecords` full ⇒ API returns 503 + `Retry-After`. Consumer: while Postgres is down it stops polling (no commit), Kafka holds the data, lag grows. Batch size caps memory. |
| Batch processing | Up to `BATCH_SIZE` (500) records per transaction using `unnest` arrays: 3 statements for the whole batch. |
| Lag monitoring | `consumer.MonitorLag` publishes `adclick_consumer_lag{topic,partition}`. |
| Retention | events 7d (replay window), retry 1d, dlq 14d. `DEDUP_RETENTION` (8d) must exceed the events retention, otherwise replaying old data after the dedup rows are purged would double count. |

## Aggregation design

- **Buckets:** `click_agg` stores minute, hour and day rows per `(ad, country, device)`, with campaign and advertiser copied in, so rollups by ad, campaign or advertiser are plain `GROUP BY`s. The cost is 3 upserts per distinct key per batch.
- **Late and out-of-order events:** bucketed by `clicked_at` (event time), not arrival time. Increments are additive, so order is irrelevant. Events older than `MAX_EVENT_AGE` (7d) or too far in the future are rejected (to the DLQ).
- **Unique clicks:** first click of a visitor (user id, else IP hash) per ad per UTC day, tracked in `click_uniques`. The unique flag is added to every granularity at the first click's bucket, so hourly unique totals mean "first-time-today visitors", not "distinct visitors in that hour". This is a documented simplification.
- **CTR:** `clicks / impressions` from `impression_agg` (hourly). Impressions are posted via `POST /v1/impressions` and written straight to Postgres (a simplification: a real system would stream them through Kafka too).
- **Clicks per minute:** average of the last 5 minute-buckets, also streamed over SSE.
- **Deadlock avoidance:** `aggregate.Build` sorts rows in primary-key order so concurrent consumers lock rows in the same order. A test runs 8 concurrent writers.

### Honest guarantee statement

Kafka delivery is **at-least-once**. Postgres writes are idempotent (dedup by `event_id` inside the same transaction as the counter updates). The combination gives an **exactly-once effect** on the aggregates. It is not Kafka exactly-once: that needs Kafka transactions with a Kafka sink, and our sink is Postgres. Residual risks: (1) if the same logical click is submitted twice **without** a shared `event_id`, it counts twice (the API cannot know), (2) replay older than `DEDUP_RETENTION` double counts.

## Database

Tables: `advertisers`, `campaigns`, `ads` (reference data); `click_events` (raw + dedup); `click_uniques`; `click_agg`; `impression_agg`. Indexes and their purpose are commented in [migrations/001_init.sql](migrations/001_init.sql). Raw events are kept only for the dedup window (purged hourly), because retaining raw rows at 1k to 5k clicks/s would grow by 100 to 400 GB/day.

## Failure scenarios

| Scenario | What happens | Recovery |
|---|---|---|
| Kafka unavailable | Producer cannot get an ack; API returns **503 + Retry-After**; nothing is falsely acknowledged. `/readyz` fails. | Clients retry with the same `event_id`. Producer retries internally within `RecordDeliveryTimeout`. |
| One Kafka broker dies | RF=3 / min ISR 2: writes continue, leaders move. | Broker rejoins and catches up. |
| PostgreSQL unavailable | **API keeps ingesting** (uses the in-memory ad cache). Consumers back off with jitter, stop committing, stop polling. Dashboard shows last good values plus a warning banner. | When Postgres returns, the consumer resumes from the last committed offset; lag drains. |
| Consumer crashes mid-batch | Offsets were not committed ⇒ batch redelivered. If the DB transaction had already committed, the dedup insert returns zero new rows. | Automatic. Counters are not doubled (tested). |
| Duplicate messages | Producer retry duplicates: idempotent producer. Redelivery or client resend: `event_id` dedup. | Automatic. |
| Consumer rebalance | `BlockRebalanceOnPoll` waits for the in-flight batch; partitions move; new owner resumes from committed offsets. | Automatic. Spikes in the rebalance counter are visible. |
| Application restart / deploy | `SIGTERM` ⇒ HTTP drain, producer flush, consumer finishes the batch and leaves the group. | Graceful. If killed hard, see "crashes mid-batch". |
| Traffic spike | Kafka absorbs it; API p99 stays low because it only writes to Kafka. Consumer lag grows and drains. Producer buffer limit gives 503 instead of unbounded memory. | Add consumer instances (up to the partition count). |
| Slow database queries | `adclick_db_query_duration_seconds` by operation; write path uses 3 statements per 500 events; reads hit pre-aggregated tables. | Index review, tune `BATCH_SIZE`, add read replica or move analytics to ClickHouse. |
| Poison message | Decode/validation error ⇒ DLQ immediately; does not block the partition. | Inspect `x-error` header, fix, `go run ./cmd/dlqreplay`. |

## Observability

- Logs: JSON via `slog` with a `service` field; one access log line per request.
- Metrics (`/metrics` on the API :8080 and the consumer :9101): `adclick_http_request_duration_seconds`, `adclick_events_produced_total`, `adclick_events_consumed_total`, `adclick_events_applied_total`, `adclick_events_duplicate_total`, `adclick_events_failed_total{reason}`, `adclick_event_processing_latency_seconds`, `adclick_consumer_batch_size`, `adclick_db_query_duration_seconds{op}`, `adclick_consumer_lag{topic,partition}`, `adclick_consumer_rebalances_total`.
- Grafana dashboard: [deploy/grafana/dashboards/pipeline.json](deploy/grafana/dashboards/pipeline.json), 10 panels covering consumed/s, lag, failures, API p99, end-to-end latency, DB p95, duplicates, rebalances.

## Tests

```bash
go test -short ./...    # unit tests, no downloads
go test ./...           # + Postgres integration tests (embedded Postgres, downloaded once)
```

| Area | Tests |
|---|---|
| Validation, hashing | `internal/event` |
| Aggregation (granularity, late events, ordering) | `internal/aggregate` |
| Idempotency, uniques, late events, concurrency (deadlock), all read queries | `internal/store` (real Postgres) |
| Retry/DLQ/backoff/shutdown logic | `internal/consumer` (fakes) |
| Status codes, 503 on Kafka down, enrichment, beacon | `internal/httpapi` |

## Load testing

Generator: `go run ./cmd/loadgen -rps 1000 -duration 30s -dup-rate 0.02` (skewed ad popularity, 2% duplicate sends, prints p50/p95/p99).

Measured so far (laptop: i5-1135G7, 8 threads, Windows, Postgres 18):

| Test | Result |
|---|---|
| `BenchmarkApplyBatch`: dedup + uniques + 3 aggregate upserts, batch 500, single DB connection | **about 31,000 events/s** (16 ms per batch) |

This is the consumer's database ceiling per connection. It is well above the 5k/s peak target, and the consumer scales out by partition.

End-to-end run through a real Kafka (single broker, no replication, no Docker), same laptop:

| Test | Result |
|---|---|
| 1000 req/s for 15 s, 2% duplicate sends | 15,057 accepted, 0 errors; API latency p50 10 ms, p95 32 ms, p99 72 ms |
| Dashboard total after the run | 14,756 = 14,754 distinct load events + 2 manual clicks (303 duplicates ignored) |
| Consumer killed, 2,560 clicks sent, consumer restarted | Total caught up to exactly 14,756 + 2,560; about 45 s to recover after a hard kill |
| Poison message | Landed in `ad-click-dlq` with `x-error` header; normal processing unaffected |

Hot-ad skew is visible: partition 0 held 11,328 events and partition 7 held 615, the cost of keying by `ad_id`.

**Not yet measured:** the 3-broker Docker Compose stack (RF=3, ISR=2), Grafana, the Postgres-down drill, and consumer lag under a sustained spike. Run `docker compose up -d --build` and the drills below, then record results here.

## Failure drills to run (Phase 10)

```bash
docker compose stop postgres      # clicks still 202; lag grows; start it and watch lag drain
docker compose stop kafka-1       # writes continue (RF=3, ISR=2)
docker compose stop kafka-2 kafka-3   # API returns 503 + Retry-After; start them and retry
docker compose kill consumer      # restart: counts must still equal distinct events sent
docker compose up -d --scale consumer=3   # watch rebalances and partition assignment in logs
```

Correctness check after any drill: total clicks on the dashboard should equal `distinct events` reported by `loadgen` (once lag reaches 0), no matter how many duplicates were sent.

## Trade-offs and alternatives

| Decision | Chosen | Alternative | Why |
|---|---|---|---|
| Analytics store | Postgres | ClickHouse / Druid | Easy idempotency and transactions; ClickHouse wins beyond hundreds of millions of rows/day. |
| Delivery semantic | at-least-once + idempotent sink | Kafka transactions (EOS) | EOS only covers Kafka→Kafka; our sink is a database. |
| Aggregation | In the consumer, in SQL | Kafka Streams / Flink | Fewer moving parts; Flink wins for complex windows and joins. |
| Dedup | PK on `event_id` | Bloom filter / Redis | Exact and transactional with the counters; costs an insert per event. |
| Live updates | SSE + polling | WebSocket | One-way data; auto-reconnect; plain HTTP. |
| Kafka client | franz-go | confluent-kafka-go, segmentio/kafka-go | Pure Go, idempotent producer and cooperative rebalancing built in. |

## Interview questions this project answers

1. Why Kafka between the API and the database? What does the API promise when it returns 202?
2. Why `ad_id` as the partition key, and what breaks with a hot ad? How would you salt the key?
3. Commit offsets before or after the DB write, and what must the DB then guarantee?
4. Why is this "exactly-once effect" and not exactly-once? What are the residual duplicate risks?
5. How do late and out-of-order events behave, and why is additive aggregation safe?
6. Why sort rows before upserting? (Lock ordering and deadlocks.)
7. What happens when Postgres is down for 10 minutes? When Kafka is?
8. Why a retry topic instead of retrying in place? (Head-of-line blocking.)
9. Why must dedup retention exceed Kafka retention?
10. How would you scale to 100x, and when would you leave Postgres?
