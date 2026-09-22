package main

// ============================================================================
// CONCEPT: sqlc -- "write SQL, get type-safe Go".
//
// THE THREE WAYS TO TALK TO A DATABASE IN GO
//
//	database/sql   you write SQL AND the row scanning. Total control, and a
//	  (lesson 36)  Scan() that silently rots the moment someone adds a column.
//
//	GORM           you write Go, it writes SQL. Fast to start; opaque queries,
//	  (lesson 38)  reflection cost, N+1 by accident, and you eventually fight it.
//
//	sqlc           you write SQL, it writes the Go. Compile-time checked
//	  (this one)   against your real schema. Zero runtime reflection --
//	               the generated code is the same database/sql you would
//	               have hand-written, minus the typos.
//
// HOW IT WORKS: sqlc is a CODE GENERATOR, not a library. It parses
// schema.sql + query.sql at BUILD time (never connects to a database) and
// writes internal/db/*.go. Your program imports that package. If you rename
// a column and forget to fix a query, `sqlc generate` fails -- you find out
// in CI, not at 2am in production.
//
// THE LOOP:
//
//	edit schema.sql / query.sql
//	        |
//	   sqlc generate        <-- commit the generated code to git
//	        |
//	   go build ./...       <-- callers that no longer type-check fail HERE
//
// Run: go run ./61_sqlc
// Regenerate: cd 61_sqlc && sqlc generate
// ============================================================================

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"log"
	"time"

	"example.com/go-basics/61_sqlc/internal/db"

	_ "modernc.org/sqlite" // pure-Go sqlite driver, registers as "sqlite"
)

//go:embed schema.sql
var schemaSQL string

func main() {
	ctx := context.Background()

	// ---------------------------------------------------------------------
	// SETUP. An in-memory database so the lesson leaves nothing behind.
	// MaxOpenConns(1) because each new connection to ":memory:" would get
	// its OWN empty database -- a classic sqlite-in-tests trap.
	// ---------------------------------------------------------------------
	sqlDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		log.Fatal(err)
	}
	defer sqlDB.Close()
	sqlDB.SetMaxOpenConns(1)

	if _, err := sqlDB.ExecContext(ctx, schemaSQL); err != nil {
		log.Fatal("schema: ", err)
	}

	// db.New takes a DBTX -- an interface satisfied by *sql.DB AND *sql.Tx.
	// That single design decision is what makes transactions painless below.
	q := db.New(sqlDB)

	// ---------------------------------------------------------------------
	// 1. INSERT ... RETURNING -> a fully populated struct, one round trip.
	// ---------------------------------------------------------------------
	fmt.Println("=== 1. CreateUser (:one with RETURNING) ===")
	prince, err := q.CreateUser(ctx, db.CreateUserParams{
		Email:    "prince@example.com",
		FullName: "Prince Panchani",
		// Bio is NULLABLE in the schema, so sqlc gave us sql.NullString.
		// The type system now FORCES you to decide about NULL. That is the
		// point -- a plain `string` would have silently turned NULL into "".
		Bio: sql.NullString{String: "Go + React", Valid: true},
	})
	must(err)
	fmt.Printf("  id=%d email=%s created_at=%s\n", prince.ID, prince.Email, prince.CreatedAt)

	ghost, err := q.CreateUser(ctx, db.CreateUserParams{
		Email:    "ghost@example.com",
		FullName: "No Bio Ghost",
		Bio:      sql.NullString{}, // Valid:false -> a real SQL NULL
	})
	must(err)
	fmt.Printf("  id=%d bio.Valid=%v (NULL in the database)\n", ghost.ID, ghost.Bio.Valid)

	// ---------------------------------------------------------------------
	// 2. THE MISSING-ROW CASE. :one returns sql.ErrNoRows. Never let that
	//    escape your repository layer -- translate it into a domain error
	//    so the HTTP layer can map it to 404 without importing database/sql.
	// ---------------------------------------------------------------------
	fmt.Println("\n=== 2. :one with no match ===")
	_, err = q.GetUserByEmail(ctx, "nobody@example.com")
	fmt.Printf("  errors.Is(err, sql.ErrNoRows) = %v\n", errors.Is(err, sql.ErrNoRows))
	if _, err := findUser(ctx, q, "nobody@example.com"); err != nil {
		fmt.Printf("  translated to domain error: %v\n", err)
	}

	// ---------------------------------------------------------------------
	// 3. NAMED PARAMS + NULLABLE PARAMS
	// ---------------------------------------------------------------------
	fmt.Println("\n=== 3. CreateTask (sqlc.arg + sqlc.narg) ===")
	yesterday := time.Now().Add(-24 * time.Hour).Format("2006-01-02 15:04:05")
	tomorrow := time.Now().Add(24 * time.Hour).Format("2006-01-02 15:04:05")

	t1, err := q.CreateTask(ctx, db.CreateTaskParams{
		UserID: prince.ID, Title: "Ship the gRPC service", Priority: 3,
		DueAt: sql.NullString{String: yesterday, Valid: true},
	})
	must(err)
	t2, err := q.CreateTask(ctx, db.CreateTaskParams{
		UserID: prince.ID, Title: "Write sqlc lesson", Priority: 2,
		DueAt: sql.NullString{String: tomorrow, Valid: true},
	})
	must(err)
	t3, err := q.CreateTask(ctx, db.CreateTaskParams{
		UserID: prince.ID, Title: "Someday: learn Rust", Priority: 1,
		DueAt: sql.NullString{}, // no deadline -> NULL
	})
	must(err)
	fmt.Printf("  created tasks %d, %d, %d\n", t1.ID, t2.ID, t3.ID)

	// ---------------------------------------------------------------------
	// 4. JOINS -> a purpose-built row struct, not the table struct.
	// ---------------------------------------------------------------------
	fmt.Println("\n=== 4. ListTasksByUser (JOIN -> ListTasksByUserRow) ===")
	rows, err := q.ListTasksByUser(ctx, db.ListTasksByUserParams{
		UserID: prince.ID,
		// GOTCHA: this field is `interface{}`, not sql.NullString. sqlc could
		// not infer a type for sqlc.narg() inside the `IS NULL OR ...`
		// expression. nil = "no filter". If that bothers you (it should),
		// split it into two explicit queries or add a type override.
		Status: nil,
	})
	must(err)
	for _, r := range rows {
		fmt.Printf("  [p%d] %-24s %-5s  %s\n", r.Priority, r.Title, r.Status, r.UserEmail)
	}

	fmt.Println("  -- same query, status filter applied:")
	rows, err = q.ListTasksByUser(ctx, db.ListTasksByUserParams{UserID: prince.ID, Status: "todo"})
	must(err)
	fmt.Printf("  %d rows with status=todo\n", len(rows))

	// ---------------------------------------------------------------------
	// 5. :execrows -- how you tell "updated" from "did not exist".
	// ---------------------------------------------------------------------
	fmt.Println("\n=== 5. CompleteTask (:execrows) ===")
	n, err := q.CompleteTask(ctx, t1.ID)
	must(err)
	fmt.Printf("  first call  -> %d row(s) affected\n", n)
	n, err = q.CompleteTask(ctx, t1.ID)
	must(err)
	fmt.Printf("  second call -> %d row(s) affected  (already done, idempotent)\n", n)
	n, err = q.CompleteTask(ctx, 9999)
	must(err)
	fmt.Printf("  missing id  -> %d row(s) affected  => return 404 from here\n", n)

	// ---------------------------------------------------------------------
	// 6. sqlc.slice -- a safe IN (...) with a dynamic number of values.
	// ---------------------------------------------------------------------
	fmt.Println("\n=== 6. GetTasksByIDs (sqlc.slice) ===")
	batch, err := q.GetTasksByIDs(ctx, []int64{t1.ID, t3.ID})
	must(err)
	for _, t := range batch {
		fmt.Printf("  #%d %s (%s)\n", t.ID, t.Title, t.Status)
	}

	// ---------------------------------------------------------------------
	// 7. Aggregates.
	// ---------------------------------------------------------------------
	fmt.Println("\n=== 7. CountTasksByStatus (GROUP BY) ===")
	counts, err := q.CountTasksByStatus(ctx, prince.ID)
	must(err)
	for _, c := range counts {
		fmt.Printf("  %-6s %d\n", c.Status, c.Total)
	}

	fmt.Println("\n=== 8. OverdueTasks ===")
	over, err := q.OverdueTasks(ctx, sql.NullString{String: time.Now().Format("2006-01-02 15:04:05"), Valid: true})
	must(err)
	fmt.Printf("  %d overdue (the done one no longer counts)\n", len(over))

	// ---------------------------------------------------------------------
	// 9. TRANSACTIONS -- the piece people always ask how sqlc handles.
	//    q.WithTx(tx) returns a *Queries bound to the transaction. Same
	//    methods, same types. There is no magic and no hidden session.
	// ---------------------------------------------------------------------
	fmt.Println("\n=== 9. Transactions (WithTx) ===")
	if err := transferAllTasks(ctx, sqlDB, q, prince.ID, ghost.ID); err != nil {
		fmt.Println("  transfer failed:", err)
	}
	ghostTasks, _ := q.ListTasksByUser(ctx, db.ListTasksByUserParams{UserID: ghost.ID})
	fmt.Printf("  ghost now owns %d task(s) -- committed atomically\n", len(ghostTasks))

	fmt.Println("\n  -- now a transaction that fails halfway:")
	before, _ := q.CountTasksByStatus(ctx, ghost.ID)
	err = failingTx(ctx, sqlDB, q, ghost.ID)
	fmt.Println("  error:", err)
	after, _ := q.CountTasksByStatus(ctx, ghost.ID)
	fmt.Printf("  status buckets before=%d after=%d -> rollback restored everything\n", len(before), len(after))

	// ---------------------------------------------------------------------
	// 10. TESTING WITHOUT A DATABASE.
	//     emit_interface: true generated db.Querier. Depend on THAT, and a
	//     fake is a struct with the three methods you actually use.
	// ---------------------------------------------------------------------
	fmt.Println("\n=== 10. Service layer against db.Querier (no database) ===")
	svc := &TaskService{q: fakeQuerier{}}
	msg, err := svc.Summary(ctx, 1)
	must(err)
	fmt.Println(" ", msg)

	fmt.Print(`
--- WHEN TO PICK WHAT ------------------------------------------------------

  sqlc      you know SQL, the schema is yours, you want compile-time safety
            and predictable queries. Default choice for a Go service.
  GORM      CRUD-heavy admin panels, prototypes, teams that do not want SQL.
  raw       one-off queries, dynamic reporting, anything sqlc cannot parse.
            Nothing stops you mixing: sqlc for 95%, database/sql for the rest.

--- WHAT SQLC DOES NOT DO --------------------------------------------------

  * No dynamic query building. Ten optional filters = write the SQL yourself
    (or use squirrel). This is a feature: dynamic SQL is where injections live.
  * No migrations. Pair it with golang-migrate (lesson 37) and point sqlc's
    "schema:" key at the migrations folder so the two can never drift.
  * No relation loading. A JOIN is a JOIN; you write it.

--- THE POSTGRES VERSION OF THIS LESSON ------------------------------------

  sqlc.yaml:  engine: "postgresql", and add
              sql_package: "pgx/v5"        (the modern driver)
  query.sql:  placeholders are $1, $2 instead of ?
  types:      pgtype.Text / *string instead of sql.NullString
              (set emit_pointers_for_null_types: true and you get *string)
  extras:     :copyfrom for bulk inserts (COPY protocol, ~10x faster than
              a loop of INSERTs) and :batchexec for pipelined batches --
              both postgres-only, and both worth knowing about in interviews.
`)
}

// ---------------------------------------------------------------------------
// The repository boundary: translate database errors into DOMAIN errors.
// Nothing above this line should ever import database/sql.
// ---------------------------------------------------------------------------

var ErrUserNotFound = errors.New("user not found")

func findUser(ctx context.Context, q db.Querier, email string) (db.User, error) {
	u, err := q.GetUserByEmail(ctx, email)
	if errors.Is(err, sql.ErrNoRows) {
		return db.User{}, fmt.Errorf("find %q: %w", email, ErrUserNotFound)
	}
	if err != nil {
		return db.User{}, fmt.Errorf("find %q: %w", email, err)
	}
	return u, nil
}

// ---------------------------------------------------------------------------
// Transactions. The generated Queries.WithTx(tx) is the whole API.
//
// The shape below (begin, defer rollback, do work, commit) is the standard
// Go transaction idiom. The deferred Rollback after a successful Commit is
// a no-op that returns sql.ErrTxDone -- that is why it is discarded.
// ---------------------------------------------------------------------------

func transferAllTasks(ctx context.Context, sqlDB *sql.DB, q *db.Queries, from, to int64) error {
	tx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op once Commit succeeded

	qtx := q.WithTx(tx) // same methods, now inside the transaction

	tasks, err := qtx.ListTasksByUser(ctx, db.ListTasksByUserParams{UserID: from})
	if err != nil {
		return err
	}
	for _, t := range tasks {
		// sqlc has no UPDATE-owner query in query.sql, so this shows the
		// escape hatch: the transaction is a plain *sql.Tx, use it directly.
		if _, err := tx.ExecContext(ctx, `UPDATE tasks SET user_id = ? WHERE id = ?`, to, t.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func failingTx(ctx context.Context, sqlDB *sql.DB, q *db.Queries, userID int64) error {
	tx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	qtx := q.WithTx(tx)
	if _, err := qtx.CreateTask(ctx, db.CreateTaskParams{UserID: userID, Title: "will vanish", Priority: 9}); err != nil {
		return err
	}
	// Violate the UNIQUE constraint on users.email on purpose.
	if _, err := qtx.CreateUser(ctx, db.CreateUserParams{Email: "prince@example.com", FullName: "dup"}); err != nil {
		return fmt.Errorf("aborting: %w", err) // deferred Rollback undoes the task too
	}
	return tx.Commit()
}

// ---------------------------------------------------------------------------
// Depending on the generated INTERFACE, not the concrete type.
// ---------------------------------------------------------------------------

type TaskService struct{ q db.Querier }

func (s *TaskService) Summary(ctx context.Context, userID int64) (string, error) {
	counts, err := s.q.CountTasksByStatus(ctx, userID)
	if err != nil {
		return "", err
	}
	total := int64(0)
	for _, c := range counts {
		total += c.Total
	}
	return fmt.Sprintf("user %d has %d task(s) across %d status bucket(s)", userID, total, len(counts)), nil
}

// fakeQuerier implements only what the test exercises. Embedding the
// interface means the other ~10 methods exist (and panic if ever called),
// so the fake does not break every time you add a query.
type fakeQuerier struct{ db.Querier }

func (fakeQuerier) CountTasksByStatus(context.Context, int64) ([]db.CountTasksByStatusRow, error) {
	return []db.CountTasksByStatusRow{{Status: "todo", Total: 4}, {Status: "done", Total: 1}}, nil
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
