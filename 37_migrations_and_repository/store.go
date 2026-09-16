package main

// ============================================================================
// PART 2 OF LESSON 37: THE REPOSITORY PATTERN
//
// A repository (or "store") is the ONLY place in your codebase that knows
// SQL exists. Everything above it — services, HTTP handlers — talks to a Go
// interface with domain types and domain errors.
//
// Why bother?
//   * Your handlers become testable without a database (swap in a fake).
//   * sql.ErrNoRows / driver-specific errors never leak upward.
//   * Swapping SQLite for Postgres touches one file.
//   * All your SQL is in one place to review for injection and N+1s.
//
// The Go convention is: DEFINE THE INTERFACE WHERE IT IS USED (the
// consumer), not next to the implementation. It lives here purely so this
// one-folder lesson stays readable.
// ============================================================================

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ---------- domain types ----------
// Note: no `db:"..."` tags, no driver imports. This is a plain Go type that
// the rest of the app can use freely.

type Task struct {
	ID        int64
	Title     string
	Done      bool
	Priority  int
	CreatedAt time.Time
}

// ---------- domain errors (lesson 35) ----------

var (
	ErrTaskNotFound = errors.New("task not found")
	ErrInvalidTask  = errors.New("invalid task")
)

// ---------- the interface ----------
// Every method takes a ctx first. Nothing in this signature mentions SQL,
// so an in-memory fake satisfies it just as well as the real thing.

type TaskRepository interface {
	Create(ctx context.Context, title string, priority int) (Task, error)
	GetByID(ctx context.Context, id int64) (Task, error)
	List(ctx context.Context, f TaskFilter) ([]Task, error)
	SetDone(ctx context.Context, id int64, done bool) error
	Delete(ctx context.Context, id int64) error
}

// TaskFilter groups optional query parameters. Pointer fields distinguish
// "not supplied" (nil) from "supplied as false/zero" — the same trick from
// lesson 32's `required` gotcha.
type TaskFilter struct {
	Done        *bool
	MaxPriority *int
	Limit       int
}

// ---------- the SQL implementation ----------

type SQLTaskRepo struct {
	db *sql.DB
}

// Constructor returns the CONCRETE type, not the interface. Go proverb:
// "accept interfaces, return structs" — it keeps the caller free to use
// extra methods, and they can still assign it to the interface.
func NewSQLTaskRepo(db *sql.DB) *SQLTaskRepo { return &SQLTaskRepo{db: db} }

// A compile-time assertion that *SQLTaskRepo really satisfies the
// interface. Costs nothing at runtime and turns a confusing call-site error
// into a clear one right here. You'll see this line in a lot of Go code.
var _ TaskRepository = (*SQLTaskRepo)(nil)

const taskColumns = `id, title, done, priority, created_at`

func (r *SQLTaskRepo) Create(ctx context.Context, title string, priority int) (Task, error) {
	if title == "" {
		return Task{}, fmt.Errorf("%w: title is required", ErrInvalidTask)
	}
	if priority < 1 || priority > 5 {
		return Task{}, fmt.Errorf("%w: priority must be 1-5, got %d", ErrInvalidTask, priority)
	}

	t := Task{Title: title, Priority: priority, CreatedAt: time.Now().UTC()}
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO tasks (title, done, priority, created_at) VALUES (?, ?, ?, ?)`,
		t.Title, t.Done, t.Priority, t.CreatedAt)
	if err != nil {
		return Task{}, fmt.Errorf("create task: %w", err)
	}
	if t.ID, err = res.LastInsertId(); err != nil {
		return Task{}, fmt.Errorf("create task: read id: %w", err)
	}
	return t, nil
}

func (r *SQLTaskRepo) GetByID(ctx context.Context, id int64) (Task, error) {
	var t Task
	err := r.db.QueryRowContext(ctx,
		`SELECT `+taskColumns+` FROM tasks WHERE id = ?`, id).
		Scan(&t.ID, &t.Title, &t.Done, &t.Priority, &t.CreatedAt)

	// THE key translation: a database-layer error becomes a domain error.
	// Callers check errors.Is(err, ErrTaskNotFound) and never import
	// database/sql at all.
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, fmt.Errorf("task %d: %w", id, ErrTaskNotFound)
	}
	if err != nil {
		return Task{}, fmt.Errorf("get task %d: %w", id, err)
	}
	return t, nil
}

func (r *SQLTaskRepo) List(ctx context.Context, f TaskFilter) ([]Task, error) {
	// Dynamic WHERE clauses, safely: build the SQL from FIXED fragments and
	// collect the VALUES into an args slice. The user's data never touches
	// the query string.
	query := `SELECT ` + taskColumns + ` FROM tasks WHERE 1=1`
	var args []any

	if f.Done != nil {
		query += ` AND done = ?`
		args = append(args, *f.Done)
	}
	if f.MaxPriority != nil {
		query += ` AND priority <= ?`
		args = append(args, *f.MaxPriority)
	}
	query += ` ORDER BY priority ASC, created_at DESC`
	if f.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, f.Limit)
	}

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()

	// Return an empty slice, not nil, so JSON encodes [] instead of null.
	tasks := make([]Task, 0, 8)
	for rows.Next() {
		var t Task
		if err := rows.Scan(&t.ID, &t.Title, &t.Done, &t.Priority, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("list tasks: scan: %w", err)
		}
		tasks = append(tasks, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list tasks: iterate: %w", err)
	}
	return tasks, nil
}

func (r *SQLTaskRepo) SetDone(ctx context.Context, id int64, done bool) error {
	res, err := r.db.ExecContext(ctx, `UPDATE tasks SET done = ? WHERE id = ?`, done, id)
	if err != nil {
		return fmt.Errorf("set done on %d: %w", id, err)
	}
	// An UPDATE that matches nothing is not a driver error — turn it into
	// one so the caller can return a 404.
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("set done on %d: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("task %d: %w", id, ErrTaskNotFound)
	}
	return nil
}

func (r *SQLTaskRepo) Delete(ctx context.Context, id int64) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM tasks WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete %d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("task %d: %w", id, ErrTaskNotFound)
	}
	return nil
}

// ---------- an in-memory fake, for tests ----------
// Because the rest of the app depends on the INTERFACE, this is a complete
// drop-in replacement. No database, no Docker, microsecond-fast tests.

type MemTaskRepo struct {
	tasks  map[int64]Task
	nextID int64
}

func NewMemTaskRepo() *MemTaskRepo {
	return &MemTaskRepo{tasks: make(map[int64]Task), nextID: 1}
}

var _ TaskRepository = (*MemTaskRepo)(nil)

func (m *MemTaskRepo) Create(_ context.Context, title string, priority int) (Task, error) {
	if title == "" {
		return Task{}, fmt.Errorf("%w: title is required", ErrInvalidTask)
	}
	t := Task{ID: m.nextID, Title: title, Priority: priority, CreatedAt: time.Now().UTC()}
	m.tasks[t.ID] = t
	m.nextID++
	return t, nil
}

func (m *MemTaskRepo) GetByID(_ context.Context, id int64) (Task, error) {
	t, ok := m.tasks[id]
	if !ok {
		return Task{}, fmt.Errorf("task %d: %w", id, ErrTaskNotFound)
	}
	return t, nil
}

func (m *MemTaskRepo) List(_ context.Context, f TaskFilter) ([]Task, error) {
	out := make([]Task, 0, len(m.tasks))
	for _, t := range m.tasks {
		if f.Done != nil && t.Done != *f.Done {
			continue
		}
		out = append(out, t)
	}
	return out, nil
}

func (m *MemTaskRepo) SetDone(_ context.Context, id int64, done bool) error {
	t, ok := m.tasks[id]
	if !ok {
		return fmt.Errorf("task %d: %w", id, ErrTaskNotFound)
	}
	t.Done = done
	m.tasks[id] = t // structs are values (lesson 11) — write it back!
	return nil
}

func (m *MemTaskRepo) Delete(_ context.Context, id int64) error {
	if _, ok := m.tasks[id]; !ok {
		return fmt.Errorf("task %d: %w", id, ErrTaskNotFound)
	}
	delete(m.tasks, id)
	return nil
}
