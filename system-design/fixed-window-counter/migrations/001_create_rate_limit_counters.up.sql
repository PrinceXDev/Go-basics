CREATE TABLE rate_limit_counters (
    scope_type    text        NOT NULL CHECK (scope_type IN ('tenant', 'api_key', 'user')),
    scope_key     text        NOT NULL,
    window_start  timestamptz NOT NULL,
    request_count integer     NOT NULL DEFAULT 0 CHECK (request_count >= 0),

    -- One row per (who, window). Also what makes the atomic upsert possible.
    PRIMARY KEY (scope_type, scope_key, window_start)
);

-- The primary key can't serve "delete everything older than X" efficiently.
CREATE INDEX idx_rate_limit_counters_window_start ON rate_limit_counters (window_start);
