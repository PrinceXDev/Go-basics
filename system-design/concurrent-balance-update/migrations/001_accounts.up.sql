-- Own schema so the demo never touches tables in a shared database.
CREATE SCHEMA IF NOT EXISTS balance_demo;

-- Per-database; the server must also preload it (see docker-compose.yml).
CREATE EXTENSION IF NOT EXISTS pg_stat_statements;

CREATE TABLE IF NOT EXISTS balance_demo.accounts (
    id      BIGINT PRIMARY KEY,
    balance BIGINT NOT NULL CHECK (balance >= 0), -- whole rupees; real systems use paise
    version BIGINT NOT NULL DEFAULT 0              -- only used by optimistic locking
);
