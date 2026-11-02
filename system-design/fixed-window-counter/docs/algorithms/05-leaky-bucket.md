# 5. Leaky Bucket  [later]

## Real-world use case
**Sending SMS or email through a provider that allows 10 messages per second.**
Our app may receive 500 send requests at once, but the output must stay at a steady 10/sec.
Extra requests wait in a queue (or are dropped if the queue is full). (NGINX's `limit_req` is a well-known
leaky bucket implementation.)

## How it works
Requests pour into a bucket. The bucket leaks at a constant rate. If the bucket is full, new requests overflow (rejected).

```
        requests in (bursty)
            │ │ │ │ │
         ┌──▼─▼─▼─▼─▼──┐
         │   bucket    │  capacity = queue size
         └──────┬──────┘
                ▼  constant drip: 10/sec
          downstream service
```

Two flavours:
- **As a queue:** requests wait and are processed at a fixed rate (traffic shaping).
- **As a meter:** no real queue, just a level that drains over time; reject when it would overflow.

```
level = max(0, level - (now - last_leak) * leak_rate)
if level + 1 <= capacity { level++; allow } else { reject }
```

## Code (meter version)
```go
type Leaky struct {
    Level    float64
    LastLeak time.Time
}

func (l *Leaky) Add(now time.Time, capacity, leakPerSec float64) bool {
    l.Level = max(0, l.Level-now.Sub(l.LastLeak).Seconds()*leakPerSec)
    l.LastLeak = now
    if l.Level+1 > capacity {
        return false
    }
    l.Level++
    return true
}
```
Queue version in Go: a buffered channel plus a `time.Ticker`:
```go
queue := make(chan Job, 100)                 // bucket capacity
ticker := time.NewTicker(100 * time.Millisecond) // 10 per second
go func() { for range ticker.C { send(<-queue) } }()
// enqueue: select { case queue <- job: default: reject }
```

## Token bucket vs leaky bucket (common question)

| | Token bucket | Leaky bucket |
|---|---|---|
| Output | Bursty, up to capacity | Steady, constant rate |
| Bucket holds | Spare permission (tokens) | Pending work (requests) |
| Protects | Average rate, tolerates spikes | A downstream that needs even flow |

## Pros / cons
- Pros: perfectly smooth output, protects fragile systems.
- Cons: bursts are delayed or dropped, queued requests add latency, queue needs memory and a worker.

## Choose it when
Outbound traffic to a third party with a strict rate (SMS, email, payment gateway, partner API), or any pipeline that needs steady flow.

## Interview one-liner
"Requests fill a bucket that drains at a constant rate; overflow is rejected. It smooths bursts into steady output, while token bucket lets bursts through."

## Later implementation
Start with the meter version (same two-number state as token bucket, so the Postgres code is nearly identical),
then show the queue version with a channel and ticker as a Go concurrency exercise, including graceful shutdown
so queued jobs are not lost.
