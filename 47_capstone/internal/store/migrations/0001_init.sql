-- Initial schema.
CREATE TABLE tasks (
    id         INTEGER  PRIMARY KEY AUTOINCREMENT,
    title      TEXT     NOT NULL,
    done       BOOLEAN  NOT NULL DEFAULT 0,
    priority   INTEGER  NOT NULL DEFAULT 3,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL
);

CREATE INDEX idx_tasks_done_priority ON tasks (done, priority, created_at DESC);
