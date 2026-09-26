// Package registry holds the latest known stats for every server, safe for
// concurrent access. Many goroutines write (one per incoming health report)
// while the HTTP handlers read — a classic case for sync.RWMutex: reads
// (snapshots for the dashboard) are far more frequent than writes, and
// RWMutex lets readers run in parallel with each other while still
// excluding writers.
package registry

import (
	"sort"
	"sync"
	"time"
)

// Status is the health state a server reports about itself.
type Status string

const (
	StatusHealthy  Status = "healthy"
	StatusDegraded Status = "degraded"
	StatusDown     Status = "down"
)

// ServerStats is one server's latest snapshot. It is the unit sent over the
// wire (HTTP ingest -> registry -> WebSocket broadcast -> React state).
type ServerStats struct {
	ID          string    `json:"id"`
	Status      Status    `json:"status"`
	CPUPercent  float64   `json:"cpuPercent"`
	MemPercent  float64   `json:"memPercent"`
	RequestRate float64   `json:"requestRate"` // requests/sec
	UpdatedAt   time.Time `json:"updatedAt"`
}

// Registry is a concurrency-safe map[string]ServerStats. Storing the struct
// by value (not *ServerStats) means a Snapshot() copy can never be mutated
// out from under a caller after the lock is released.
type Registry struct {
	mu   sync.RWMutex
	data map[string]ServerStats
}

func New() *Registry {
	return &Registry{data: make(map[string]ServerStats)}
}

// Upsert stores or replaces a server's latest stats.
func (r *Registry) Upsert(s ServerStats) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data[s.ID] = s
}

// Snapshot returns every server's latest stats, sorted by ID so the
// dashboard's grid doesn't jump around between renders.
func (r *Registry) Snapshot() []ServerStats {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]ServerStats, 0, len(r.data))
	for _, s := range r.data {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Get returns one server's stats.
func (r *Registry) Get(id string) (ServerStats, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.data[id]
	return s, ok
}
