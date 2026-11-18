# Phase 1: Requirements and Architecture

Ad Click Aggregation Platform: Go, Kafka, PostgreSQL, Next.js, Docker Compose.

## 1. The problem

An ad platform gets a lot of clicks. Each one must be:

1. Counted accurately (advertisers are billed by clicks).
2. Never lost, even if a server dies.
3. Never counted twice, even if the network retries.
4. Visible quickly on a dashboard (within a few seconds).

Writing straight to Postgres from the API breaks down:

| Problem | What happens |
|---|---|
| Traffic spike | The database is hit directly and becomes the bottleneck. |
| Database slow or down | Clicks are lost, or users wait on the redirect. |
| Counting | `UPDATE clicks = clicks + 1` on a hot ad row makes everyone wait on one row lock. |

Kafka sits in the middle as a durable buffer. The API accepts the click, writes it to the log and replies fast. Slow work happens later, at the consumer's pace.

## 2. Functional requirements

| ID | Requirement |
|---|---|
| F1 | Accept a click via `POST /v1/clicks` and `GET /c/{ad_id}` (tracking redirect). |
| F2 | Validate the click: required fields, known `ad_id`, sane timestamp. |
| F3 | Assign a unique `event_id` and publish to Kafka. |
| F4 | Consumers keep aggregates by ad, campaign, advertiser, minute, hour, day, country and device. |
| F5 | Metrics: total clicks, unique clicks, clicks per minute, CTR (needs impressions). |
| F6 | Failed events are retried, then moved to a DLQ with a reason. |
| F7 | Query API for the dashboard with date-range filters. |
| F8 | Dashboard: totals, trends, top ads, campaigns, country and device breakdown, near real time. |
| F9 | Admin can replay DLQ events. |

Out of scope: auth/billing, fraud detection (discussed, not built), real ad serving, full-scale impressions (a simple impression counter feeds CTR).

## 3. Non-functional requirements

| Area | Target |
|---|---|
| Ingest throughput | 1k clicks/s average, 5k/s peak (laptop-scale Docker) |
| API latency | p99 under 50 ms (API only writes to Kafka) |
| Freshness | Click visible on dashboard within about 5 s |
| Durability | Acknowledged click survives a broker crash (RF=3, `acks=all`) |
| Delivery | At-least-once plus idempotent writes = exactly-once effect |
| Availability | API keeps accepting clicks if Postgres is down |
| Scalability | Add consumers or API instances without code changes |
| Privacy | No raw IPs, only a salted hash |

Rough sizing:

```
5,000 clicks/s peak  ->  ~430M clicks/day at peak, ~86M/day at 1k/s average
~300 bytes/event     ->  ~26 GB/day raw
Kafka retention 7d   ->  ~180 GB x replication 3 = ~540 GB cluster disk
```

Postgres cannot keep all raw events forever at this scale. Aggregates are kept forever; raw events only briefly or in droppable partitions.

## 4. Architecture

```
                      ┌─────────────┐
   Browser / Ad  ───► │  Go API     │  validate + assign event_id
   (click)            │ (stateless) │  fast reply: 202 Accepted
                      └──────┬──────┘
                             │ produce (key = ad_id, acks=all, idempotent)
                             ▼
        ┌───────────────────────────────────────────┐
        │                 KAFKA                      │
        │  ad-click-events  (12 partitions, RF=3)    │
        │  ad-click-retry   (retry with delay)       │
        │  ad-click-dlq     (poison messages)        │
        └──────┬─────────────────────────┬───────────┘
               │ consumer group          │ (failed after N tries)
               ▼                         ▼
      ┌─────────────────┐         ┌─────────────┐
      │ Go Consumers    │────────►│ DLQ topic   │
      │ (N instances)   │ retry   └─────────────┘
      │ validate → dedup│
      │ → batch → write │
      └────────┬────────┘
               │ one DB transaction per batch
               ▼
      ┌───────────────────────────┐
      │ PostgreSQL                │
      │ processed_events (dedup)  │
      │ click_aggregates_*        │
      │ advertisers/campaigns/ads │
      └────────┬──────────────────┘
               │ read
               ▼
      ┌─────────────┐   polling/SSE   ┌──────────────┐
      │ Go Query API│◄───────────────►│ Next.js UI   │
      └─────────────┘                 └──────────────┘

   Side channel: all Go services expose /metrics ──► Prometheus ──► Grafana
```

## 5. Component responsibilities

| Component | Does | Does NOT do |
|---|---|---|
| HTTP API | Validate, create `event_id`, hash IP, publish | Touch Postgres on the click path |
| Producer | Idempotent, batched, `acks=all`, handle Kafka being down | Business logic |
| Consumer | Poll, decode, dedup, process in batches, commit offsets after the DB commit | Commit offsets before data is safe |
| Aggregation engine | Turn a batch of events into counter increments per bucket | Know about Kafka |
| Repository | All SQL, transactions, upserts | Know about HTTP |
| Retry/DLQ workers | Re-deliver with delay, park poison messages | Retry forever |
| Query API | Read aggregates for the dashboard | Write anything |
| Observability | Logs (`slog`), metrics, consumer lag | |

## 6. Event lifecycle

```
1  click ──► API validates ──► builds Event{event_id=UUIDv7, ...}
2  API ──► Kafka (key=ad_id)  ──► broker replicates to 2 followers ──► ack
3  API returns 202 (only after the ack)
4  Consumer polls a batch of up to e.g. 500 events
5  Consumer: validate each; bad ones ──► retry/DLQ
6  BEGIN TX
     INSERT INTO processed_events(event_id) ON CONFLICT DO NOTHING
        └─ rows actually inserted = the NEW events (duplicates drop out)
     UPSERT counters ONLY for the new events
   COMMIT
7  Consumer commits Kafka offsets   (only now)
8  Dashboard query sees the new numbers
```

If the consumer crashes between 6 and 7, Kafka re-delivers the batch and the dedup table drops the duplicates. At-least-once delivery plus an idempotent write gives the effect of exactly-once. We do not claim Kafka itself is exactly-once, because the Kafka-to-Postgres hop is not covered by Kafka transactions.

## 7. Topics

| Topic | Purpose | Partitions / RF | Retention |
|---|---|---|---|
| `ad-click-events` | Main stream | 12 / 3 | 7 days (allows replay after a bug) |
| `ad-click-retry` | Temporary failures (DB blip) so the main topic keeps flowing | 6 / 3 | 1 day |
| `ad-click-dlq` | Messages that failed N times, error in headers | 3 / 3 | 14 days |

A separate retry topic avoids head-of-line blocking: retrying a bad message in place would stall every message behind it in that partition.

## 8. Technology trade-offs

| Choice | Why | Alternative | When the alternative wins |
|---|---|---|---|
| Kafka | Durable log, replay, consumer groups, partition ordering | RabbitMQ / SQS | Simple work queues, no replay need |
| PostgreSQL for aggregates | Familiar SQL, strong transactions make idempotency easy | ClickHouse / Druid | Billions of rows, ad-hoc analytics |
| Polling first | Simple; SSE can come in Phase 7 | SSE / WebSocket | Many viewers needing sub-second updates |
| Aggregate in the consumer | Fewer moving parts | Kafka Streams / Flink | Complex windows, joins, very large scale |
| Go Kafka client (decide in Phase 3) | | franz-go, segmentio/kafka-go, confluent-kafka-go | Compare idempotent producer and manual commit support |

Postgres is not what a very large ad system would use for analytics. It is chosen here because it makes idempotency and transactions easy to learn.

## 9. Click event fields (details in Phase 2)

`event_id, ad_id, campaign_id, advertiser_id, user_id?, clicked_at, country, device_type, browser, source, ip_hash`

## 10. Open design questions (answer before Phase 2)

- Q1. Partition key: `ad_id`, `campaign_id`, `advertiser_id`, `user_id`, `event_id`, or none? Effect on ordering, load balance, and ease of aggregation. Which is a trap for one huge advertiser?
- Q2. Offset commit before vs after the Postgres write: what happens to the click on a crash in each case? Which for billing, and what must the DB do?
- Q3. "Unique clicks": same user, same ad, one hour apart: one or two? Does hourly vs daily change it? How to count cheaply at 5k events/s?

## 11. Interview questions

1. Why put Kafka between the API and the database?
2. What does `acks=all` buy you, and what does it cost?
3. Why can't we say "exactly-once" here? What would it take?
4. The DB is down for 10 minutes. What happens to incoming clicks?
5. How would you size the partition count, and why is it hard to reduce later?
6. Why is Postgres a compromise here, and what would you move to at 100x scale?
