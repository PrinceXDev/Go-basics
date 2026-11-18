// Package store holds all SQL. Nothing else in the project talks to Postgres.
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"adclick/internal/aggregate"
	"adclick/internal/event"
)

type Store struct {
	pool    *pgxpool.Pool
	observe func(op string, d time.Duration) // optional query-latency hook
}

func New(ctx context.Context, url string, observe func(string, time.Duration)) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 10
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{pool: pool, observe: observe}, nil
}

func (s *Store) Close()                         { s.pool.Close() }
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func (s *Store) timed(op string) func() {
	start := time.Now()
	return func() {
		if s.observe != nil {
			s.observe(op, time.Since(start))
		}
	}
}

// AdInfo is the authoritative ad -> campaign -> advertiser mapping.
type AdInfo struct{ CampaignID, AdvertiserID string }

func (s *Store) LoadAds(ctx context.Context) (map[string]AdInfo, error) {
	defer s.timed("load_ads")()
	rows, err := s.pool.Query(ctx, `
		SELECT a.id, a.campaign_id, c.advertiser_id
		FROM ads a JOIN campaigns c ON c.id = a.campaign_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]AdInfo{}
	for rows.Next() {
		var id string
		var ai AdInfo
		if err := rows.Scan(&id, &ai.CampaignID, &ai.AdvertiserID); err != nil {
			return nil, err
		}
		out[id] = ai
	}
	return out, rows.Err()
}

// ApplyBatch is the heart of idempotency. In ONE transaction it:
//  1. inserts events, keeping only those whose event_id is new (dedup),
//  2. decides which new events are a visitor's first click of the day (unique),
//  3. adds the deltas to the aggregate rows.
//
// If the transaction rolls back, nothing happened and the batch can be replayed.
// If it commits but the Kafka offset commit is lost, a replay finds every
// event_id already present, so step 1 returns nothing and counters do not move.
func (s *Store) ApplyBatch(ctx context.Context, events []event.Click) (applied int, err error) {
	if len(events) == 0 {
		return 0, nil
	}
	defer s.timed("apply_batch")()

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

	fresh, err := insertEvents(ctx, tx, events)
	if err != nil {
		return 0, err
	}
	if len(fresh) == 0 {
		return 0, tx.Commit(ctx)
	}

	unique, err := markUniques(ctx, tx, fresh)
	if err != nil {
		return 0, err
	}
	if err := upsertAgg(ctx, tx, aggregate.Build(fresh, unique)); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(fresh), nil
}

func insertEvents(ctx context.Context, tx pgx.Tx, evs []event.Click) ([]event.Click, error) {
	n := len(evs)
	ids := make([]string, n)
	ads, camps, advs, users := make([]string, n), make([]string, n), make([]string, n), make([]*string, n)
	at := make([]time.Time, n)
	ctry, dev, br, src, ip := make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n)
	for i, e := range evs {
		ids[i], ads[i], camps[i], advs[i], at[i] = e.EventID, e.AdID, e.CampaignID, e.AdvertiserID, e.ClickedAt
		if e.UserID != "" {
			u := e.UserID
			users[i] = &u
		}
		ctry[i], dev[i], br[i], src[i], ip[i] = e.Country, e.DeviceType, e.Browser, e.Source, e.IPHash
	}
	rows, err := tx.Query(ctx, `
		INSERT INTO click_events
		  (event_id, ad_id, campaign_id, advertiser_id, user_id, clicked_at, country, device_type, browser, source, ip_hash)
		SELECT * FROM unnest($1::text[]::uuid[], $2::text[], $3::text[], $4::text[], $5::text[],
		                     $6::timestamptz[], $7::text[], $8::text[], $9::text[], $10::text[], $11::text[])
		ON CONFLICT (event_id) DO NOTHING
		RETURNING event_id::text`,
		ids, ads, camps, advs, users, at, ctry, dev, br, src, ip)
	if err != nil {
		return nil, fmt.Errorf("insert events: %w", err)
	}
	defer rows.Close()
	isNew := make(map[string]bool, n)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		isNew[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// A batch can contain the same event_id twice; only the first copy counts.
	fresh := make([]event.Click, 0, len(isNew))
	for _, e := range evs {
		if isNew[e.EventID] {
			fresh = append(fresh, e)
			delete(isNew, e.EventID)
		}
	}
	return fresh, nil
}

func markUniques(ctx context.Context, tx pgx.Tx, evs []event.Click) ([]bool, error) {
	n := len(evs)
	ads, visitors := make([]string, n), make([]string, n)
	days := make([]time.Time, n)
	for i, e := range evs {
		ads[i], visitors[i] = e.AdID, e.Visitor()
		days[i] = e.ClickedAt.UTC().Truncate(24 * time.Hour)
	}
	rows, err := tx.Query(ctx, `
		INSERT INTO click_uniques (ad_id, day, visitor)
		SELECT * FROM unnest($1::text[], $2::date[], $3::text[])
		ON CONFLICT DO NOTHING
		RETURNING ad_id, day, visitor`, ads, days, visitors)
	if err != nil {
		return nil, fmt.Errorf("mark uniques: %w", err)
	}
	defer rows.Close()
	type k struct {
		ad, visitor string
		day         time.Time
	}
	first := map[k]bool{}
	for rows.Next() {
		var x k
		if err := rows.Scan(&x.ad, &x.day, &x.visitor); err != nil {
			return nil, err
		}
		first[k{x.ad, x.visitor, x.day.UTC()}] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// The first event in the batch for a newly seen key owns the "unique" flag.
	out := make([]bool, n)
	for i := range evs {
		key := k{ads[i], visitors[i], days[i]}
		if first[key] {
			out[i] = true
			delete(first, key)
		}
	}
	return out, nil
}

func upsertAgg(ctx context.Context, tx pgx.Tx, ds []aggregate.Delta) error {
	if len(ds) == 0 {
		return nil
	}
	n := len(ds)
	gran, ad, camp, adv, ctry, dev := make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n)
	bucket := make([]time.Time, n)
	clicks, uniq := make([]int64, n), make([]int64, n)
	for i, d := range ds {
		gran[i], bucket[i], ad[i], camp[i], adv[i], ctry[i], dev[i] = d.Granularity, d.Bucket, d.AdID, d.CampaignID, d.AdvertiserID, d.Country, d.DeviceType
		clicks[i], uniq[i] = d.Clicks, d.Unique
	}
	// ORDER BY ord keeps the global lock order chosen by aggregate.Build.
	_, err := tx.Exec(ctx, `
		INSERT INTO click_agg (granularity, bucket, ad_id, campaign_id, advertiser_id, country, device_type, clicks, unique_clicks)
		SELECT granularity, bucket, ad_id, campaign_id, advertiser_id, country, device_type, clicks, unique_clicks
		FROM unnest($1::text[], $2::timestamptz[], $3::text[], $4::text[], $5::text[], $6::text[], $7::text[], $8::bigint[], $9::bigint[])
		     WITH ORDINALITY AS t(granularity, bucket, ad_id, campaign_id, advertiser_id, country, device_type, clicks, unique_clicks, ord)
		ORDER BY ord
		ON CONFLICT (granularity, bucket, ad_id, country, device_type)
		DO UPDATE SET clicks = click_agg.clicks + EXCLUDED.clicks,
		              unique_clicks = click_agg.unique_clicks + EXCLUDED.unique_clicks`,
		gran, bucket, ad, camp, adv, ctry, dev, clicks, uniq)
	if err != nil {
		return fmt.Errorf("upsert agg: %w", err)
	}
	return nil
}

// PurgeOldEvents removes dedup rows older than the retention window.
func (s *Store) PurgeOldEvents(ctx context.Context, retention time.Duration) (int64, error) {
	defer s.timed("purge")()
	cutoff := time.Now().Add(-retention)
	tag, err := s.pool.Exec(ctx, `DELETE FROM click_events WHERE processed_at < $1`, cutoff)
	if err != nil {
		return 0, err
	}
	// Uniques older than the dedup window are no longer needed either.
	if _, err := s.pool.Exec(ctx, `DELETE FROM click_uniques WHERE day < $1::date`, cutoff); err != nil {
		return tag.RowsAffected(), err
	}
	return tag.RowsAffected(), nil
}

// AddImpressions records impressions for CTR (simplified: direct write, not via Kafka).
func (s *Store) AddImpressions(ctx context.Context, adID string, n int64, at time.Time) error {
	defer s.timed("add_impressions")()
	_, err := s.pool.Exec(ctx, `
		INSERT INTO impression_agg (bucket, ad_id, impressions) VALUES ($1, $2, $3)
		ON CONFLICT (bucket, ad_id) DO UPDATE SET impressions = impression_agg.impressions + EXCLUDED.impressions`,
		at.UTC().Truncate(time.Hour), adID, n)
	return err
}
