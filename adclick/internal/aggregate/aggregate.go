// Package aggregate turns a batch of new click events into counter deltas.
// It is pure (no I/O) so it is trivial to test.
package aggregate

import (
	"sort"
	"time"

	"adclick/internal/event"
)

var Granularities = []struct {
	Name string
	Step time.Duration
}{
	{"minute", time.Minute},
	{"hour", time.Hour},
	{"day", 24 * time.Hour},
}

type Key struct {
	Granularity  string
	Bucket       time.Time
	AdID         string
	CampaignID   string
	AdvertiserID string
	Country      string
	DeviceType   string
}

type Delta struct {
	Key
	Clicks int64
	Unique int64
}

// Build groups events by (granularity, bucket, ad, country, device).
// Events are bucketed by clicked_at (event time), not arrival time, so late and
// out-of-order events land in the right bucket. unique[i] says whether event i
// was the first click of its visitor for that ad and day.
//
// The result is sorted by primary key. Writing rows in one global order stops
// two consumers from locking the same rows in opposite order (deadlock).
func Build(events []event.Click, unique []bool) []Delta {
	acc := make(map[Key]*Delta, len(events))
	for i, e := range events {
		t := e.ClickedAt.UTC()
		for _, g := range Granularities {
			k := Key{g.Name, t.Truncate(g.Step), e.AdID, e.CampaignID, e.AdvertiserID, e.Country, e.DeviceType}
			d := acc[k]
			if d == nil {
				d = &Delta{Key: k}
				acc[k] = d
			}
			d.Clicks++
			if unique != nil && unique[i] {
				d.Unique++
			}
		}
	}
	out := make([]Delta, 0, len(acc))
	for _, d := range acc {
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool { return less(out[i].Key, out[j].Key) })
	return out
}

func less(a, b Key) bool {
	if a.Granularity != b.Granularity {
		return a.Granularity < b.Granularity
	}
	if !a.Bucket.Equal(b.Bucket) {
		return a.Bucket.Before(b.Bucket)
	}
	if a.AdID != b.AdID {
		return a.AdID < b.AdID
	}
	if a.Country != b.Country {
		return a.Country < b.Country
	}
	return a.DeviceType < b.DeviceType
}
