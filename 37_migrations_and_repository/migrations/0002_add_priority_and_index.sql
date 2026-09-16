-- Migration 0002: add a priority column and an index.
--
-- Note this file does TWO statements. Our runner wraps each FILE in one
-- transaction, so either both apply or neither does.
--
-- Adding a NOT NULL column to an existing table needs a DEFAULT, otherwise
-- existing rows would violate the constraint.
ALTER TABLE tasks ADD COLUMN priority INTEGER NOT NULL DEFAULT 3;

CREATE INDEX idx_tasks_done_priority ON tasks (done, priority);
