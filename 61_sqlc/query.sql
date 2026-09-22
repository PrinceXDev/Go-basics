-- ===========================================================================
-- query.sql -- you write SQL. sqlc writes the Go.
--
-- Every query carries a magic comment:
--
--   -- name: <GoFuncName> :<kind>
--
-- kinds:
--   :one       -> returns exactly one row  (Go: (Row, error), sql.ErrNoRows if none)
--   :many      -> returns a slice          (Go: ([]Row, error))
--   :exec      -> returns nothing but err  (Go: error)
--   :execrows  -> returns rows affected    (Go: (int64, error))
--   :execlastid-> returns last insert id   (Go: (int64, error)) -- sqlite/mysql
--   :copyfrom  -> bulk load                (postgres only, COPY protocol)
--   :batchexec -> pipelined batch          (postgres + pgx only)
--
-- This file is the ONLY place SQL lives. No ORM query builder, no string
-- concatenation in Go, no runtime surprises.
-- ===========================================================================

-- name: CreateUser :one
-- RETURNING lets us get the full row back in ONE round trip instead of
-- insert-then-select. Supported by sqlite 3.35+, postgres, and mariadb.
INSERT INTO users (email, full_name, bio)
VALUES (?, ?, ?)
RETURNING *;

-- name: GetUser :one
SELECT * FROM users WHERE id = ? LIMIT 1;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = ? LIMIT 1;

-- name: ListUsers :many
SELECT * FROM users ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?;

-- name: CreateTask :one
-- sqlc.arg(name) gives the generated Go param a readable name instead of
-- Column1/Column2. Worth doing on every query with more than two params.
INSERT INTO tasks (user_id, title, priority, due_at)
VALUES (sqlc.arg(user_id), sqlc.arg(title), sqlc.arg(priority), sqlc.narg(due_at))
RETURNING *;
-- sqlc.narg() = "nullable arg": the Go param becomes sql.NullString rather
-- than string, so you can genuinely pass NULL. sqlc.arg() on a nullable
-- column would force you to always send a value.

-- name: GetTask :one
SELECT * FROM tasks WHERE id = ? LIMIT 1;

-- name: ListTasksByUser :many
-- A JOIN produces a NEW anonymous row type. sqlc generates a dedicated
-- struct (ListTasksByUserRow) with exactly these columns -- not a Task.
SELECT
    t.id,
    t.title,
    t.status,
    t.priority,
    t.due_at,
    u.email      AS user_email,
    u.full_name  AS user_name
FROM tasks t
JOIN users u ON u.id = t.user_id
WHERE t.user_id = sqlc.arg(user_id)
  AND (sqlc.narg(status) IS NULL OR t.status = sqlc.narg(status))
ORDER BY t.priority DESC, t.id ASC;
-- The `(sqlc.narg(x) IS NULL OR col = sqlc.narg(x))` trick is how you do an
-- OPTIONAL filter without building SQL strings at runtime. One prepared
-- statement, two behaviours. Note the query planner may not use the index
-- for this shape -- at high scale, write two explicit queries instead.

-- name: CompleteTask :execrows
-- :execrows returns the affected-row count, which is how you distinguish
-- "updated" from "id did not exist" without a prior SELECT.
UPDATE tasks
SET status = 'done', completed_at = datetime('now')
WHERE id = ? AND status <> 'done';

-- name: DeleteTasksByStatus :execrows
DELETE FROM tasks WHERE user_id = ? AND status = ?;

-- name: CountTasksByStatus :many
-- Aggregates: sqlc infers the result type of COUNT(*) as int64.
SELECT status, COUNT(*) AS total
FROM tasks
WHERE user_id = ?
GROUP BY status
ORDER BY status;

-- name: GetTasksByIDs :many
-- sqlc.slice() expands to the right number of placeholders at call time --
-- the single hardest thing to do safely by hand with database/sql.
SELECT * FROM tasks WHERE id IN (sqlc.slice(ids));

-- name: OverdueTasks :many
SELECT t.id, t.title, t.due_at, u.email
FROM tasks t
JOIN users u ON u.id = t.user_id
WHERE t.due_at IS NOT NULL
  AND t.due_at < sqlc.arg(now)
  AND t.status <> 'done'
ORDER BY t.due_at ASC;
