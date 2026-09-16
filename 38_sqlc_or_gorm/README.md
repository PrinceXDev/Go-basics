# 38 — sqlc or GORM?

`main.go` runs the **GORM** half of this lesson. This README covers **sqlc**,
which can't be demonstrated with a plain `go run` because it's a code
generator you install as a separate binary.

Run the GORM demo first:

```bash
go run ./38_sqlc_or_gorm
```

---

## The mental model

| | You write | Tool produces | Type safety |
|---|---|---|---|
| **raw** (36/37) | SQL + `Scan()` | nothing | none — wrong column order compiles |
| **sqlx** | SQL + struct tags | nothing | partial — tags checked at runtime |
| **sqlc** | SQL + schema | typed Go functions | **compile-time, against your real schema** |
| **GORM** | Go structs + method chains | SQL, at runtime | runtime reflection |

sqlc inverts the ORM idea: instead of hiding SQL behind objects, you write
plain SQL and it hands you the Go.

---

## sqlc in four files

### 1. Install

```bash
go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest
```

(Or use the Docker image — sqlc is a build-time tool, not a dependency of
your program. Nothing it generates imports sqlc.)

### 2. `schema.sql` — the truth about your tables

Point this at the **same** migration files from lesson 37; don't maintain a
second copy.

```sql
CREATE TABLE tasks (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    title      TEXT     NOT NULL,
    done       BOOLEAN  NOT NULL DEFAULT 0,
    priority   INTEGER  NOT NULL DEFAULT 3,
    created_at DATETIME NOT NULL
);
```

### 3. `query.sql` — your queries, annotated

The `-- name:` comment tells sqlc what to call the generated function and
what shape to return (`:one`, `:many`, `:exec`, `:execrows`, `:batchexec`).

```sql
-- name: GetTask :one
SELECT * FROM tasks WHERE id = ? LIMIT 1;

-- name: ListPendingTasks :many
SELECT * FROM tasks
WHERE done = 0 AND priority <= ?
ORDER BY priority ASC, created_at DESC
LIMIT ?;

-- name: CreateTask :one
INSERT INTO tasks (title, done, priority, created_at)
VALUES (?, 0, ?, ?)
RETURNING *;

-- name: SetTaskDone :execrows
UPDATE tasks SET done = ? WHERE id = ?;

-- name: DeleteTask :exec
DELETE FROM tasks WHERE id = ?;
```

### 4. `sqlc.yaml` — the config

```yaml
version: "2"
sql:
  - engine: "sqlite"
    schema: "../37_migrations_and_repository/migrations"
    queries: "query.sql"
    gen:
      go:
        package: "db"
        out: "internal/db"
        emit_json_tags: true
        emit_interface: true          # generates a Querier interface -> mockable
        emit_empty_slices: true       # :many returns [] not nil (JSON!)
        emit_pointers_for_null_types: true
```

### 5. Generate

```bash
sqlc generate
```

You also get `sqlc vet` (lint your queries) and `sqlc diff` (fail CI when
the generated code is stale).

---

## What comes out

sqlc writes three files under `internal/db`:

- `models.go` — a `Task` struct matching the table exactly
- `querier.go` — a `Querier` interface with every query as a method
- `query.sql.go` — the implementations

```go
// generated — you never edit this
type Task struct {
	ID        int64     `json:"id"`
	Title     string    `json:"title"`
	Done      bool      `json:"done"`
	Priority  int64     `json:"priority"`
	CreatedAt time.Time `json:"created_at"`
}

func (q *Queries) ListPendingTasks(
	ctx context.Context, arg ListPendingTasksParams,
) ([]Task, error) { /* the Scan loop from lesson 36, written for you */ }
```

And you use it like this:

```go
conn, _ := sql.Open("sqlite", "tasks.db")   // still lesson 36 underneath
q := db.New(conn)

tasks, err := q.ListPendingTasks(ctx, db.ListPendingTasksParams{
	Priority: 2,
	Limit:    10,
})
```

**The payoff:** rename a column in a migration, re-run `sqlc generate`, and
every query that referenced it fails to compile. With GORM or raw SQL you'd
find out in production.

---

## Transactions with sqlc

`db.New` accepts anything with `ExecContext`/`QueryContext` — so a `*sql.DB`
or a `*sql.Tx` both work:

```go
tx, err := conn.BeginTx(ctx, nil)
if err != nil { return err }
defer tx.Rollback()

qtx := q.WithTx(tx)          // same Querier, bound to the transaction
if _, err := qtx.CreateTask(ctx, params); err != nil { return err }
if err := qtx.SetTaskDone(ctx, doneParams); err != nil { return err }

return tx.Commit()
```

---

## Where sqlc is awkward

- **Dynamic filters.** There's no `WHERE 1=1 AND ...` builder. Options:
  write one query per filter combination, use
  `WHERE (sqlc.narg('done') IS NULL OR done = sqlc.narg('done'))`, or drop
  to hand-written SQL for the two screens that genuinely need it.
- **Dynamic `IN (...)`** needs `sqlc.slice()` (supported on Postgres/MySQL,
  patchier on SQLite).
- It's a **build step**. Generated code must be committed and kept in sync;
  add `sqlc diff` to CI.

---

## The recommendation

For a new Go service in 2026:

> **lesson 37's migrations + sqlc + a thin repository layer.**

The migrations own the schema, sqlc turns it into compile-checked Go, and
the repository (lesson 37) keeps the generated types from leaking into your
HTTP handlers. Reach for GORM when you're prototyping CRUD fast or your
domain is genuinely a deep object graph; reach for `sqlx` when the service
is small and you don't want a codegen step.

Whichever you pick: it's `database/sql` all the way down, so lesson 36 was
never wasted.
