-- One row per order. order_id doubles as the idempotency key we send to the
-- gateway, so the same order can never be charged twice.
CREATE TABLE IF NOT EXISTS payments (
    order_id          TEXT PRIMARY KEY,
    amount_cents      BIGINT      NOT NULL CHECK (amount_cents > 0),
    status            TEXT        NOT NULL CHECK (status IN ('PENDING', 'SUCCEEDED', 'QUEUED', 'FAILED')),
    path              TEXT        NOT NULL DEFAULT 'sync' CHECK (path IN ('sync', 'async')),
    gateway_charge_id TEXT,
    failure_reason    TEXT,
    sync_attempts     INT         NOT NULL DEFAULT 0,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS payments_created_idx ON payments (created_at DESC);

-- The durable "message queue" for async retries. Postgres is the queue here:
-- workers claim rows with FOR UPDATE SKIP LOCKED, so many workers can run
-- without picking the same job. In a larger system this is Kafka/RabbitMQ/SQS.
CREATE TABLE IF NOT EXISTS payment_retry_jobs (
    id              BIGSERIAL PRIMARY KEY,
    order_id        TEXT        NOT NULL UNIQUE REFERENCES payments (order_id),
    status          TEXT        NOT NULL CHECK (status IN ('pending', 'processing', 'done', 'dead')),
    attempts        INT         NOT NULL DEFAULT 0,
    max_attempts    INT         NOT NULL,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    locked_at       TIMESTAMPTZ,
    last_error      TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Partial index: the worker only ever scans jobs that are waiting.
CREATE INDEX IF NOT EXISTS retry_jobs_due_idx
    ON payment_retry_jobs (next_attempt_at) WHERE status = 'pending';
