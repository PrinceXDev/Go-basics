package store

import (
	"context"
	"fmt"
	"time"
)

// Read-side queries for the dashboard. Ranges are resolved on hour buckets
// (from is rounded down, to is rounded up) except for the minute-level rate.

type Summary struct {
	TotalClicks     int64   `json:"total_clicks"`
	UniqueClicks    int64   `json:"unique_clicks"`
	Impressions     int64   `json:"impressions"`
	CTR             float64 `json:"ctr"`               // clicks / impressions, 0 when no impressions
	ClicksPerMinute float64 `json:"clicks_per_minute"` // average over the last 5 minutes
}

func (s *Store) Summary(ctx context.Context, from, to time.Time) (Summary, error) {
	defer s.timed("summary")()
	var out Summary
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(clicks),0), COALESCE(SUM(unique_clicks),0)
		FROM click_agg WHERE granularity='hour' AND bucket >= date_trunc('hour',$1::timestamptz) AND bucket < $2`,
		from, to).Scan(&out.TotalClicks, &out.UniqueClicks)
	if err != nil {
		return out, err
	}
	if err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(impressions),0) FROM impression_agg
		WHERE bucket >= date_trunc('hour',$1::timestamptz) AND bucket < $2`, from, to).Scan(&out.Impressions); err != nil {
		return out, err
	}
	if out.Impressions > 0 {
		out.CTR = float64(out.TotalClicks) / float64(out.Impressions)
	}
	var last5 int64
	if err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(clicks),0) FROM click_agg
		WHERE granularity='minute' AND bucket >= date_trunc('minute', now()) - interval '5 minutes'`).Scan(&last5); err != nil {
		return out, err
	}
	out.ClicksPerMinute = float64(last5) / 5
	return out, nil
}

type Point struct {
	Bucket time.Time `json:"bucket"`
	Clicks int64     `json:"clicks"`
	Unique int64     `json:"unique_clicks"`
}

func (s *Store) Timeseries(ctx context.Context, gran string, from, to time.Time, campaignID, adID string) ([]Point, error) {
	defer s.timed("timeseries")()
	if gran != "minute" && gran != "hour" && gran != "day" {
		return nil, fmt.Errorf("granularity must be minute|hour|day")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT bucket, SUM(clicks), SUM(unique_clicks) FROM click_agg
		WHERE granularity=$1 AND bucket >= $2 AND bucket < $3
		  AND ($4 = '' OR campaign_id = $4) AND ($5 = '' OR ad_id = $5)
		GROUP BY bucket ORDER BY bucket`, gran, from, to, campaignID, adID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Point{}
	for rows.Next() {
		var p Point
		if err := rows.Scan(&p.Bucket, &p.Clicks, &p.Unique); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

type AdStat struct {
	AdID        string  `json:"ad_id"`
	Name        string  `json:"name"`
	CampaignID  string  `json:"campaign_id"`
	Clicks      int64   `json:"clicks"`
	Unique      int64   `json:"unique_clicks"`
	Impressions int64   `json:"impressions"`
	CTR         float64 `json:"ctr"`
}

func (s *Store) TopAds(ctx context.Context, from, to time.Time, limit int) ([]AdStat, error) {
	defer s.timed("top_ads")()
	rows, err := s.pool.Query(ctx, `
		WITH c AS (
		  SELECT ad_id, SUM(clicks) AS clicks, SUM(unique_clicks) AS uniq FROM click_agg
		  WHERE granularity='hour' AND bucket >= date_trunc('hour',$1::timestamptz) AND bucket < $2 GROUP BY ad_id
		), i AS (
		  SELECT ad_id, SUM(impressions) AS imps FROM impression_agg
		  WHERE bucket >= date_trunc('hour',$1::timestamptz) AND bucket < $2 GROUP BY ad_id
		)
		SELECT a.id, a.name, a.campaign_id, c.clicks, c.uniq, COALESCE(i.imps,0)
		FROM c JOIN ads a ON a.id = c.ad_id LEFT JOIN i ON i.ad_id = c.ad_id
		ORDER BY c.clicks DESC LIMIT $3`, from, to, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AdStat{}
	for rows.Next() {
		var a AdStat
		if err := rows.Scan(&a.AdID, &a.Name, &a.CampaignID, &a.Clicks, &a.Unique, &a.Impressions); err != nil {
			return nil, err
		}
		if a.Impressions > 0 {
			a.CTR = float64(a.Clicks) / float64(a.Impressions)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

type CampaignStat struct {
	CampaignID   string `json:"campaign_id"`
	Name         string `json:"name"`
	AdvertiserID string `json:"advertiser_id"`
	Clicks       int64  `json:"clicks"`
	Unique       int64  `json:"unique_clicks"`
}

func (s *Store) Campaigns(ctx context.Context, from, to time.Time) ([]CampaignStat, error) {
	defer s.timed("campaigns")()
	rows, err := s.pool.Query(ctx, `
		SELECT c.id, c.name, c.advertiser_id, SUM(g.clicks), SUM(g.unique_clicks)
		FROM click_agg g JOIN campaigns c ON c.id = g.campaign_id
		WHERE g.granularity='hour' AND g.bucket >= date_trunc('hour',$1::timestamptz) AND g.bucket < $2
		GROUP BY c.id, c.name, c.advertiser_id ORDER BY SUM(g.clicks) DESC`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CampaignStat{}
	for rows.Next() {
		var c CampaignStat
		if err := rows.Scan(&c.CampaignID, &c.Name, &c.AdvertiserID, &c.Clicks, &c.Unique); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

type Slice struct {
	Name   string `json:"name"`
	Clicks int64  `json:"clicks"`
}

// Breakdown groups by "country" or "device_type" (whitelisted, never user SQL).
func (s *Store) Breakdown(ctx context.Context, dim string, from, to time.Time) ([]Slice, error) {
	defer s.timed("breakdown")()
	if dim != "country" && dim != "device_type" {
		return nil, fmt.Errorf("unsupported dimension")
	}
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s, SUM(clicks) FROM click_agg
		WHERE granularity='hour' AND bucket >= date_trunc('hour',$1::timestamptz) AND bucket < $2
		GROUP BY 1 ORDER BY 2 DESC`, dim), from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Slice{}
	for rows.Next() {
		var x Slice
		if err := rows.Scan(&x.Name, &x.Clicks); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
