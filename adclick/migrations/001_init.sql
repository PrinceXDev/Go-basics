-- Reference data -----------------------------------------------------------
CREATE TABLE IF NOT EXISTS advertisers (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS campaigns (
    id            TEXT PRIMARY KEY,
    advertiser_id TEXT NOT NULL REFERENCES advertisers(id),
    name          TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Serves "campaigns of advertiser X" lookups and FK checks.
CREATE INDEX IF NOT EXISTS idx_campaigns_advertiser ON campaigns(advertiser_id);

CREATE TABLE IF NOT EXISTS ads (
    id          TEXT PRIMARY KEY,
    campaign_id TEXT NOT NULL REFERENCES campaigns(id),
    name        TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_ads_campaign ON ads(campaign_id);

-- Raw events + dedup ---------------------------------------------------------
-- The primary key IS the idempotency guard: INSERT ... ON CONFLICT DO NOTHING
-- tells us which events in a batch are new. Rows must outlive Kafka retention,
-- otherwise a replay after the purge would be counted twice.
CREATE TABLE IF NOT EXISTS click_events (
    event_id      UUID PRIMARY KEY,
    ad_id         TEXT NOT NULL,
    campaign_id   TEXT NOT NULL,
    advertiser_id TEXT NOT NULL,
    user_id       TEXT,
    clicked_at    TIMESTAMPTZ NOT NULL,
    country       TEXT NOT NULL,
    device_type   TEXT NOT NULL,
    browser       TEXT NOT NULL,
    source        TEXT NOT NULL,
    ip_hash       TEXT NOT NULL,
    processed_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Used by the retention purge (range delete by age).
CREATE INDEX IF NOT EXISTS idx_click_events_processed_at ON click_events(processed_at);

-- One row per (ad, UTC day, visitor): decides whether a click is a "unique click".
CREATE TABLE IF NOT EXISTS click_uniques (
    ad_id   TEXT NOT NULL,
    day     DATE NOT NULL,
    visitor TEXT NOT NULL,
    PRIMARY KEY (ad_id, day, visitor)
);

-- Aggregates -------------------------------------------------------------------
-- One table, three granularities. Campaign/advertiser are copied in so rollups
-- by campaign or advertiser are a plain GROUP BY without joins.
-- Upserts are additive (clicks = clicks + delta), so late or out-of-order
-- events simply land in the bucket of their clicked_at.
CREATE TABLE IF NOT EXISTS click_agg (
    granularity   TEXT NOT NULL CHECK (granularity IN ('minute','hour','day')),
    bucket        TIMESTAMPTZ NOT NULL,
    ad_id         TEXT NOT NULL,
    campaign_id   TEXT NOT NULL,
    advertiser_id TEXT NOT NULL,
    country       TEXT NOT NULL,
    device_type   TEXT NOT NULL,
    clicks        BIGINT NOT NULL DEFAULT 0,
    unique_clicks BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (granularity, bucket, ad_id, country, device_type)
);
-- Time-range scans per granularity (dashboard trends, summary).
CREATE INDEX IF NOT EXISTS idx_click_agg_gran_bucket ON click_agg(granularity, bucket DESC);
-- Campaign drill-down within a time range.
CREATE INDEX IF NOT EXISTS idx_click_agg_campaign ON click_agg(granularity, campaign_id, bucket DESC);

-- Impressions (for CTR) ----------------------------------------------------------
CREATE TABLE IF NOT EXISTS impression_agg (
    bucket      TIMESTAMPTZ NOT NULL, -- hour
    ad_id       TEXT NOT NULL,
    impressions BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (bucket, ad_id)
);

-- Seed data ------------------------------------------------------------------
INSERT INTO advertisers (id, name) VALUES
    ('adv-acme', 'Acme Corp'), ('adv-globex', 'Globex'), ('adv-initech', 'Initech')
ON CONFLICT DO NOTHING;

INSERT INTO campaigns (id, advertiser_id, name) VALUES
    ('camp-summer', 'adv-acme', 'Summer Sale'),
    ('camp-launch', 'adv-acme', 'Product Launch'),
    ('camp-brand', 'adv-globex', 'Brand Awareness'),
    ('camp-retarget', 'adv-initech', 'Retargeting')
ON CONFLICT DO NOTHING;

INSERT INTO ads (id, campaign_id, name) VALUES
    ('ad-1001', 'camp-summer', 'Summer Banner'),
    ('ad-1002', 'camp-summer', 'Summer Video'),
    ('ad-2001', 'camp-launch', 'Launch Hero'),
    ('ad-2002', 'camp-launch', 'Launch Carousel'),
    ('ad-3001', 'camp-brand', 'Brand Story'),
    ('ad-3002', 'camp-brand', 'Brand Sidebar'),
    ('ad-4001', 'camp-retarget', 'Cart Reminder'),
    ('ad-4002', 'camp-retarget', 'Wishlist Nudge')
ON CONFLICT DO NOTHING;
