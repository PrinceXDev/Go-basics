// Package simulator stands in for the 10 real servers in the interview
// question. In production these numbers would arrive as HTTP POSTs from
// each server's own health-check agent (see api.IngestHealth) — this
// package just drives the same ingest function locally so the demo runs
// with `go run` and nothing else.
package simulator

import (
	"context"
	"math/rand/v2"
	"sync"
	"time"

	"serverdashboard/internal/registry"
)

// Ingest is whatever should happen when a server reports fresh stats —
// in main.go this is "update the registry, then broadcast to the hub".
type Ingest func(registry.ServerStats)

// Run starts one goroutine per server ID. Each goroutine is an independent
// producer: its own ticker, its own random jitter, no shared state with the
// others except the Ingest callback (which is responsible for its own
// synchronization — see registry.Registry's mutex). That independence is
// the point: server 7 being slow or bursty never affects server 3's
// goroutine.
//
// Run blocks until ctx is cancelled, then waits for every server goroutine
// to exit before returning — so callers can rely on "Run returned" meaning
// "no more simulated traffic is in flight".
func Run(ctx context.Context, ids []string, ingest Ingest) {
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			simulateServer(ctx, id, ingest)
		}(id)
	}
	wg.Wait()
}

func simulateServer(ctx context.Context, id string, ingest Ingest) {
	rng := rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))

	// Stagger the initial tick so all 10 servers don't report in lockstep
	// (real servers wouldn't be synchronized either).
	initialDelay := time.Duration(rng.IntN(1500)) * time.Millisecond
	timer := time.NewTimer(initialDelay)
	defer timer.Stop()

	// Slowly-drifting baseline load per server, so the dashboard shows
	// realistic variety (some servers consistently hotter than others)
	// instead of every card looking identical noise.
	cpuBaseline := 15 + rng.Float64()*50
	memBaseline := 20 + rng.Float64()*40

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		cpuBaseline = clamp(cpuBaseline+(rng.Float64()-0.5)*10, 2, 98)
		memBaseline = clamp(memBaseline+(rng.Float64()-0.5)*6, 5, 95)

		status := registry.StatusHealthy
		switch {
		case cpuBaseline > 90 || memBaseline > 90:
			status = registry.StatusDown
		case cpuBaseline > 75 || memBaseline > 80:
			status = registry.StatusDegraded
		}

		ingest(registry.ServerStats{
			ID:          id,
			Status:      status,
			CPUPercent:  round1(cpuBaseline),
			MemPercent:  round1(memBaseline),
			RequestRate: round1(rng.Float64() * 200),
			UpdatedAt:   time.Now(),
		})

		next := 1500 + rng.IntN(1500) // 1.5s-3s between reports, per server
		timer.Reset(time.Duration(next) * time.Millisecond)
	}
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func round1(v float64) float64 {
	return float64(int(v*10)) / 10
}
