-- Migration 0001: the initial tasks table.
-- Migrations are FORWARD-ONLY and IMMUTABLE. Once a numbered file has run
-- anywhere (especially production), you never edit it — you add 0002.
CREATE TABLE tasks (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    title      TEXT     NOT NULL,
    done       BOOLEAN  NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL
);
