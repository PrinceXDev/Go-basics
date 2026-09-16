-- Add a free-text tag. NOT NULL needs a DEFAULT so existing rows stay valid.
ALTER TABLE tasks ADD COLUMN tag TEXT NOT NULL DEFAULT '';

CREATE INDEX idx_tasks_tag ON tasks (tag) WHERE tag != '';
