// Package adcache keeps an in-memory copy of the ads table.
package adcache

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"adclick/internal/store"
)

type Loader interface {
	LoadAds(ctx context.Context) (map[string]store.AdInfo, error)
}

// AdCache is an in-memory copy of the ads table, refreshed in the background.
// If Postgres is down, the last good copy keeps being served.
type AdCache struct {
	mu     sync.RWMutex
	ads    map[string]store.AdInfo
	loaded bool
}

func (c *AdCache) Lookup(id string) (store.AdInfo, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	ai, ok := c.ads[id]
	return ai, ok
}

// Loaded reports whether at least one refresh succeeded.
func (c *AdCache) Loaded() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.loaded
}

func (c *AdCache) Set(m map[string]store.AdInfo) {
	c.mu.Lock()
	c.ads, c.loaded = m, true
	c.mu.Unlock()
}

// Refresh loads now, then every interval, until ctx ends.
func (c *AdCache) Refresh(ctx context.Context, s Loader, every time.Duration, log *slog.Logger) {
	load := func() {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if m, err := s.LoadAds(cctx); err != nil {
			log.Warn("ad cache refresh failed; keeping previous copy", "err", err)
		} else {
			c.Set(m)
		}
	}
	load()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			load()
		case <-ctx.Done():
			return
		}
	}
}
