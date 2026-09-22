-- ===========================================================================
-- schema.sql -- the SHAPE of the database.
--
-- sqlc parses this file statically (it never connects to a database) to learn
-- your tables, column types and nullability. That is how it can tell you, at
-- BUILD time, that a query references a column that does not exist.
--
-- In a real project this file is not hand-maintained: you point sqlc at your
-- golang-migrate `migrations/` folder and it replays the up-migrations to
-- derive the schema. See sqlc.yaml for how that is wired.
-- ===========================================================================

CREATE TABLE users (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    email      TEXT    NOT NULL UNIQUE,
    full_name  TEXT    NOT NULL,
    -- NULLABLE on purpose: watch what sqlc generates for this field
    -- (sql.NullString, not string). Nullability in SQL becomes a different
    -- Go TYPE -- that is the whole value proposition of sqlc.
    bio        TEXT,
    created_at TEXT    NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE tasks (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title       TEXT    NOT NULL,
    status      TEXT    NOT NULL DEFAULT 'todo', -- todo | doing | done
    priority    INTEGER NOT NULL DEFAULT 1,
    due_at      TEXT,
    completed_at TEXT,
    created_at  TEXT    NOT NULL DEFAULT (datetime('now'))
);

-- Indexes belong in the schema, not in an afterthought. Any column you filter
-- or sort on at scale needs one; this pair covers the two hot queries below.
CREATE INDEX idx_tasks_user_status ON tasks(user_id, status);
CREATE INDEX idx_tasks_due ON tasks(due_at);
