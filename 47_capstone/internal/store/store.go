// Package store owns everything that knows SQL exists: the schema
// migrations, the connection pool, and the repository. Nothing above this
// package imports database/sql.
//
// Lessons 36 (database/sql), 37 (migrations + repository), 35 (domain
// errors).
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver: no CGO, so the image can be distroless
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// ---------------------------------------------------------------------------
// domain types and errors
// ---------------------------------------------------------------------------

type Task struct {
	ID        int64     `json:"id"`
	Title     string    `json:"title"`
	Done      bool      `json:"done"`
	Priority  int       `json:"priority"`
	Tag       string    `json:"tag"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

var (
	ErrNotFound = errors.New("task not found")
	ErrInvalid  = errors.New("invalid task")
	ErrConflict = errors.New("task was modified concurrently")
)

// NewTask carries the fields a client may set on creation. Keeping this
// separate from Task stops a client from setting ID or CreatedAt.
type NewTask struct {
	Title    string
	Priority int
	Tag      string
}

// Patch is a partial update. Pointer fields mean "not supplied" (nil) is
// distinguishable from "set to the zero value" — lesson 32's gotcha, and
// the only correct way to model PATCH.
type Patch struct {
	Title    *string
	Done     *bool
	Priority *int
	Tag      *string
}

// Page-size bounds. Exported so the HTTP layer can report the EFFECTIVE
// limit rather than whatever the client asked for — one rule, one place.
const (
	DefaultLimit = 20
	MaxLimit     = 100
)

// ClampLimit applies the page-size policy.
func ClampLimit(n int) int {
	switch {
	case n <= 0:
		return DefaultLimit
	case n > MaxLimit:
		return MaxLimit
	default:
		return n
	}
}

// Filter is the query shape for List.
type Filter struct {
	Done    *bool
	Tag     string
	MaxPrio *int
	Limit   int
	Offset  int
}

// Repository is the seam the rest of the app depends on. Defined here for
// compactness; in a larger codebase it would live next to its consumer.
type Repository interface {
	Create(ctx context.Context, in NewTask) (Task, error)
	Get(ctx context.Context, id int64) (Task, error)
	List(ctx context.Context, f Filter) ([]Task, int, error)
	Update(ctx context.Context, id int64, p Patch) (Task, error)
	Delete(ctx context.Context, id int64) error
}

// ---------------------------------------------------------------------------
// opening + migrating
// ---------------------------------------------------------------------------

// Open returns a configured pool, verified reachable. ":memory:" gives an
// in-process database, which is what the tests use.
func Open(ctx context.Context, path string, maxOpen int) (*sql.DB, error) {
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	if path != ":memory:" {
		// WAL lets readers proceed during a write. Not available for
		// in-memory databases.
		dsn += "&_pragma=journal_mode(WAL)"
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxOpen)
	db.SetConnMaxLifetime(time.Hour)

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}
	return db, nil
}

// Migrate applies any migration files that haven't run yet. Lesson 37.
func Migrate(ctx context.Context, db *sql.DB) (int, error) {
	const bookkeeping = `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    TEXT PRIMARY KEY,
			applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`
	if _, err := db.ExecContext(ctx, bookkeeping); err != nil {
		return 0, fmt.Errorf("create schema_migrations: %w", err)
	}

	done, err := appliedVersions(ctx, db)
	if err != nil {
		return 0, err
	}

	names, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		return 0, fmt.Errorf("glob migrations: %w", err)
	}
	slices.Sort(names)

	applied := 0
	for _, name := range names {
		version := strings.TrimSuffix(strings.TrimPrefix(name, "migrations/"), ".sql")
		if slices.Contains(done, version) {
			continue
		}
		body, err := fs.ReadFile(migrationFiles, name)
		if err != nil {
			return applied, fmt.Errorf("read %s: %w", name, err)
		}
		if err := applyOne(ctx, db, version, string(body)); err != nil {
			return applied, err
		}
		applied++
	}
	return applied, nil
}

func applyOne(ctx context.Context, db *sql.DB, version, body string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin %s: %w", version, err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit

	if _, err := tx.ExecContext(ctx, body); err != nil {
		return fmt.Errorf("apply %s: %w", version, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version) VALUES (?)`, version); err != nil {
		return fmt.Errorf("record %s: %w", version, err)
	}
	return tx.Commit()
}

func appliedVersions(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("scan version: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// the repository
// ---------------------------------------------------------------------------

type SQLRepo struct{ db *sql.DB }

func NewSQLRepo(db *sql.DB) *SQLRepo { return &SQLRepo{db: db} }

var _ Repository = (*SQLRepo)(nil) // compile-time interface check

const cols = `id, title, done, priority, tag, created_at, updated_at`

func (r *SQLRepo) Create(ctx context.Context, in NewTask) (Task, error) {
	if err := validateNew(in); err != nil {
		return Task{}, err
	}

	now := time.Now().UTC().Truncate(time.Millisecond)
	t := Task{
		Title: strings.TrimSpace(in.Title), Priority: in.Priority,
		Tag: strings.TrimSpace(in.Tag), CreatedAt: now, UpdatedAt: now,
	}

	// RETURNING gives us the generated id without a second round trip, and
	// works the same on Postgres (unlike LastInsertId).
	err := r.db.QueryRowContext(ctx, `
		INSERT INTO tasks (title, done, priority, tag, created_at, updated_at)
		VALUES (?, 0, ?, ?, ?, ?)
		RETURNING id`,
		t.Title, t.Priority, t.Tag, t.CreatedAt, t.UpdatedAt).Scan(&t.ID)
	if err != nil {
		return Task{}, fmt.Errorf("create task: %w", err)
	}
	return t, nil
}

func (r *SQLRepo) Get(ctx context.Context, id int64) (Task, error) {
	var t Task
	err := r.db.QueryRowContext(ctx,
		`SELECT `+cols+` FROM tasks WHERE id = ?`, id).
		Scan(&t.ID, &t.Title, &t.Done, &t.Priority, &t.Tag, &t.CreatedAt, &t.UpdatedAt)

	// The translation that keeps database/sql out of the HTTP layer.
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, fmt.Errorf("task %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return Task{}, fmt.Errorf("get task %d: %w", id, err)
	}
	return t, nil
}

// List returns a page of tasks AND the total matching count, so the handler
// can report pagination metadata.
func (r *SQLRepo) List(ctx context.Context, f Filter) ([]Task, int, error) {
	// Dynamic filters built from FIXED fragments; values always go through
	// placeholders.
	where := ` WHERE 1=1`
	var args []any
	if f.Done != nil {
		where += ` AND done = ?`
		args = append(args, *f.Done)
	}
	if f.Tag != "" {
		where += ` AND tag = ?`
		args = append(args, f.Tag)
	}
	if f.MaxPrio != nil {
		where += ` AND priority <= ?`
		args = append(args, *f.MaxPrio)
	}

	var total int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks`+where, args...).
		Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count tasks: %w", err)
	}

	// Never let a client ask for everything.
	limit := ClampLimit(f.Limit)

	query := `SELECT ` + cols + ` FROM tasks` + where +
		` ORDER BY done ASC, priority ASC, created_at DESC LIMIT ? OFFSET ?`
	args = append(args, limit, max(f.Offset, 0))

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()

	tasks := make([]Task, 0, limit) // non-nil -> JSON [] rather than null
	for rows.Next() {
		var t Task
		if err := rows.Scan(&t.ID, &t.Title, &t.Done, &t.Priority, &t.Tag,
			&t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, 0, fmt.Errorf("list tasks: scan: %w", err)
		}
		tasks = append(tasks, t)
	}
	if err := rows.Err(); err != nil { // never skip this
		return nil, 0, fmt.Errorf("list tasks: iterate: %w", err)
	}
	return tasks, total, nil
}

// Update applies a partial patch inside a transaction: read, merge,
// validate, write. Doing it in one transaction is what makes concurrent
// PATCHes to the same row safe.
func (r *SQLRepo) Update(ctx context.Context, id int64, p Patch) (Task, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	var t Task
	err = tx.QueryRowContext(ctx,
		`SELECT `+cols+` FROM tasks WHERE id = ?`, id).
		Scan(&t.ID, &t.Title, &t.Done, &t.Priority, &t.Tag, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, fmt.Errorf("task %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return Task{}, fmt.Errorf("update task %d: %w", id, err)
	}

	// Merge: only fields the client actually sent.
	if p.Title != nil {
		t.Title = strings.TrimSpace(*p.Title)
	}
	if p.Done != nil {
		t.Done = *p.Done
	}
	if p.Priority != nil {
		t.Priority = *p.Priority
	}
	if p.Tag != nil {
		t.Tag = strings.TrimSpace(*p.Tag)
	}
	if err := validateNew(NewTask{Title: t.Title, Priority: t.Priority, Tag: t.Tag}); err != nil {
		return Task{}, err
	}
	t.UpdatedAt = time.Now().UTC().Truncate(time.Millisecond)

	if _, err := tx.ExecContext(ctx, `
		UPDATE tasks SET title = ?, done = ?, priority = ?, tag = ?, updated_at = ?
		WHERE id = ?`,
		t.Title, t.Done, t.Priority, t.Tag, t.UpdatedAt, id); err != nil {
		return Task{}, fmt.Errorf("update task %d: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return Task{}, fmt.Errorf("commit: %w", err)
	}
	return t, nil
}

func (r *SQLRepo) Delete(ctx context.Context, id int64) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM tasks WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete task %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete task %d: %w", id, err)
	}
	if n == 0 { // "matched nothing" is not a driver error — make it one
		return fmt.Errorf("task %d: %w", id, ErrNotFound)
	}
	return nil
}

// validateNew keeps business rules in the store, so every entry point (HTTP
// today, a CLI or a queue consumer tomorrow) gets them.
func validateNew(in NewTask) error {
	title := strings.TrimSpace(in.Title)
	switch {
	case title == "":
		return fmt.Errorf("%w: title is required", ErrInvalid)
	case len(title) > 200:
		return fmt.Errorf("%w: title must be at most 200 characters", ErrInvalid)
	case in.Priority < 1 || in.Priority > 5:
		return fmt.Errorf("%w: priority must be between 1 and 5, got %d", ErrInvalid, in.Priority)
	case len(in.Tag) > 40:
		return fmt.Errorf("%w: tag must be at most 40 characters", ErrInvalid)
	}
	return nil
}
