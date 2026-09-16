package main

// ============================================================================
// CONCEPT: `database/sql` — Go's standard database interface.
//
// WHY THIS MATTERS
// This is the single biggest gap between "I know Go" and "I can ship a Go
// service". database/sql is in the standard library and every Go database
// tool (sqlx, sqlc, GORM, ent) is built on top of it, so learning it first
// means you can read and debug all of them.
//
// database/sql is NOT an ORM. It is a thin, connection-pooling wrapper over
// raw SQL. You write the SQL; it manages connections, prepared statements
// and scanning results into Go variables.
//
// DRIVERS
// database/sql defines the interface; a DRIVER implements it for a specific
// database. Here we use modernc.org/sqlite — a pure-Go SQLite (no CGO, no C
// compiler, works out of the box on Windows). For Postgres you'd swap in
// github.com/jackc/pgx/v5/stdlib; for MySQL, github.com/go-sql-driver/mysql.
// Everything else in this file stays the same.
//
// JS/TS comparison: closest to `pg` or `better-sqlite3` — raw SQL plus a
// pool — rather than Prisma or TypeORM.
// ============================================================================

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"time"

	// The blank import `_` registers the driver with database/sql via its
	// init() function. We never call the package directly, so without the
	// `_` the compiler would reject the unused import. This is the one
	// place a blank import is completely normal Go.
	_ "modernc.org/sqlite"
)

type Book struct {
	ID        int64
	Title     string
	Author    string
	Pages     int
	Publisher sql.NullString // nullable column -> see the NULL section below
	CreatedAt time.Time
}

func main() {
	ctx := context.Background()
	dbPath := filepath.Join("36_database_sql", "books.db")

	// ---------- opening: sql.Open does NOT connect ----------
	// sql.Open validates arguments and creates a POOL. It is lazy — no
	// network/file access happens yet, so it almost never returns a useful
	// error. Always follow it with Ping to actually verify connectivity.
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		log.Fatalf("open: %v", err)
	}
	defer db.Close() // closes the whole pool; do this once, in main

	// ---------- pool tuning ----------
	// *sql.DB is a POOL, not a connection. Share ONE for the whole program
	// and pass it around — never open a new one per request.
	db.SetMaxOpenConns(10)                  // hard cap; excess callers block
	db.SetMaxIdleConns(5)                   // kept warm for reuse
	db.SetConnMaxLifetime(30 * time.Minute) // recycle (load balancers kill old conns)
	db.SetConnMaxIdleTime(5 * time.Minute)

	// Ping with a timeout so a dead database fails fast at startup instead
	// of hanging. Every database/sql method has a ...Context variant —
	// always prefer it (lesson 25).
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		log.Fatalf("ping: %v", err)
	}
	fmt.Println("connected to", dbPath)

	if err := run(ctx, db); err != nil {
		log.Fatalf("run: %v", err)
	}
}

func run(ctx context.Context, db *sql.DB) error {
	// ---------- schema ----------
	// Exec is for statements that return no rows: DDL, INSERT, UPDATE,
	// DELETE. Here we recreate the table so the lesson is re-runnable.
	schema := `
		DROP TABLE IF EXISTS books;
		CREATE TABLE books (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			title      TEXT    NOT NULL,
			author     TEXT    NOT NULL,
			pages      INTEGER NOT NULL DEFAULT 0,
			publisher  TEXT,                       -- nullable on purpose
			created_at DATETIME NOT NULL
		);`
	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}

	// ---------- INSERT with placeholders ----------
	// NEVER build SQL with fmt.Sprintf or string concatenation — that is
	// SQL injection. Use placeholders and pass the values as arguments;
	// the driver sends them separately from the query text.
	//
	// Placeholder syntax is DRIVER-SPECIFIC:
	//   sqlite / mysql : ?
	//   postgres       : $1, $2, ...
	//   oracle         : :name
	fmt.Println()
	fmt.Println("== INSERT ==")
	insert := `INSERT INTO books (title, author, pages, publisher, created_at)
	           VALUES (?, ?, ?, ?, ?)`

	res, err := db.ExecContext(ctx, insert,
		"The Go Programming Language", "Donovan & Kernighan", 380, "Addison-Wesley", time.Now())
	if err != nil {
		return fmt.Errorf("insert: %w", err)
	}

	// sql.Result gives you the new id and the affected-row count — though
	// NOT every driver supports both (Postgres has no LastInsertId; you use
	// `INSERT ... RETURNING id` with QueryRow instead).
	id, _ := res.LastInsertId()
	n, _ := res.RowsAffected()
	fmt.Printf("inserted id=%d rows=%d\n", id, n)

	// Passing nil for a nullable column writes a real SQL NULL.
	if _, err := db.ExecContext(ctx, insert,
		"Learning Go", "Jon Bodner", 375, nil, time.Now()); err != nil {
		return fmt.Errorf("insert: %w", err)
	}
	if _, err := db.ExecContext(ctx, insert,
		"Concurrency in Go", "Katherine Cox-Buday", 238, "O'Reilly", time.Now()); err != nil {
		return fmt.Errorf("insert: %w", err)
	}

	// ---------- QueryRow: exactly one row ----------
	// QueryRow returns a *sql.Row. Its error (including "no rows") is
	// deferred until you call Scan. Scan copies column values into the
	// pointers you pass, IN COLUMN ORDER.
	fmt.Println()
	fmt.Println("== QueryRow (single row) ==")
	var b Book
	err = db.QueryRowContext(ctx,
		`SELECT id, title, author, pages, publisher, created_at FROM books WHERE id = ?`, 1).
		Scan(&b.ID, &b.Title, &b.Author, &b.Pages, &b.Publisher, &b.CreatedAt)
	if err != nil {
		return fmt.Errorf("select one: %w", err)
	}
	fmt.Printf("%+v\n", b)

	// "Not found" is an ERROR in database/sql, not an empty result. Check
	// for it explicitly with errors.Is (lesson 35) and translate it into
	// your own domain error — a 404, usually.
	err = db.QueryRowContext(ctx, `SELECT title FROM books WHERE id = ?`, 999).Scan(new(string))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		fmt.Println("id=999: sql.ErrNoRows -> this is your 404")
	case err != nil:
		return fmt.Errorf("select missing: %w", err)
	}

	// ---------- Query: many rows ----------
	// Query returns *sql.Rows, a CURSOR. The loop shape below is fixed and
	// you will write it hundreds of times — learn it by heart.
	fmt.Println()
	fmt.Println("== Query (many rows) ==")
	books, err := listBooks(ctx, db, 200)
	if err != nil {
		return err
	}
	for _, bk := range books {
		pub := "<none>"
		if bk.Publisher.Valid { // .Valid is false when the column was NULL
			pub = bk.Publisher.String
		}
		fmt.Printf("  #%d %-30s %-22s %4dp  %s\n", bk.ID, bk.Title, bk.Author, bk.Pages, pub)
	}

	// ---------- UPDATE / DELETE ----------
	fmt.Println()
	fmt.Println("== UPDATE / DELETE ==")
	res, err = db.ExecContext(ctx, `UPDATE books SET pages = pages + 10 WHERE author LIKE ?`, "%Bodner%")
	if err != nil {
		return fmt.Errorf("update: %w", err)
	}
	n, _ = res.RowsAffected()
	fmt.Println("rows updated:", n)

	// RowsAffected == 0 is NOT an error — it means the WHERE matched
	// nothing. If "must exist" matters, check it yourself.
	res, err = db.ExecContext(ctx, `DELETE FROM books WHERE id = ?`, 999)
	if err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	n, _ = res.RowsAffected()
	fmt.Printf("rows deleted: %d (0 means 'nothing matched', not a failure)\n", n)

	// ---------- transactions ----------
	fmt.Println()
	fmt.Println("== transactions ==")
	if err := transferPages(ctx, db, 1, 3, 25); err != nil {
		return err
	}
	fmt.Println("committed")

	// A deliberately failing transaction, to show the rollback path.
	if err := transferPages(ctx, db, 1, 99, 25); err != nil {
		fmt.Println("rolled back as expected:", err)
	}

	// ---------- prepared statements ----------
	// Prepare once, execute many times. database/sql already caches plans
	// per connection, so reach for this only in hot loops — and remember to
	// Close it.
	fmt.Println()
	fmt.Println("== prepared statement ==")
	stmt, err := db.PrepareContext(ctx, `SELECT count(*) FROM books WHERE pages > ?`)
	if err != nil {
		return fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()
	for _, threshold := range []int{200, 300, 400} {
		var count int
		if err := stmt.QueryRowContext(ctx, threshold).Scan(&count); err != nil {
			return fmt.Errorf("count: %w", err)
		}
		fmt.Printf("  books with >%d pages: %d\n", threshold, count)
	}

	// ---------- pool stats ----------
	fmt.Println()
	fmt.Printf("pool stats: %+v\n", db.Stats())
	return nil
}

// listBooks is the canonical multi-row read. Note all four things it does:
// defer Close, Scan per row, check rows.Err after the loop, and take a ctx.
func listBooks(ctx context.Context, db *sql.DB, minPages int) ([]Book, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, title, author, pages, publisher, created_at
		 FROM books WHERE pages >= ? ORDER BY pages DESC`, minPages)
	if err != nil {
		return nil, fmt.Errorf("query books: %w", err)
	}
	// MUST close, or the connection leaks out of the pool and your service
	// eventually deadlocks waiting for a free connection. This is the #1
	// database/sql production bug.
	defer rows.Close()

	var out []Book
	for rows.Next() {
		var b Book
		if err := rows.Scan(&b.ID, &b.Title, &b.Author, &b.Pages, &b.Publisher, &b.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan book: %w", err)
		}
		out = append(out, b)
	}
	// rows.Next() returns false both for "done" and for "something broke".
	// rows.Err() is how you tell them apart. Skipping this check silently
	// truncates result sets — a genuinely nasty bug.
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate books: %w", err)
	}
	return out, nil
}

// transferPages shows the standard transaction shape.
func transferPages(ctx context.Context, db *sql.DB, fromID, toID int64, pages int) error {
	tx, err := db.BeginTx(ctx, nil) // nil = default isolation level
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	// Rollback after a successful Commit is a no-op, so this defer is a
	// safe catch-all for every early return AND for a panic.
	defer tx.Rollback() //nolint:errcheck

	// Inside a transaction, use tx.Exec/tx.Query — NOT db.Exec, which would
	// grab a different connection and run outside the transaction.
	if _, err := tx.ExecContext(ctx, `UPDATE books SET pages = pages - ? WHERE id = ?`, pages, fromID); err != nil {
		return fmt.Errorf("debit: %w", err)
	}

	res, err := tx.ExecContext(ctx, `UPDATE books SET pages = pages + ? WHERE id = ?`, pages, toID)
	if err != nil {
		return fmt.Errorf("credit: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Returning here triggers the deferred Rollback — the debit above
		// is undone.
		return fmt.Errorf("credit: book %d does not exist", toID)
	}

	return tx.Commit()
}

// ----------------------------------------------------------------------------
// HANDLING NULL
//   A SQL NULL cannot be scanned into a plain Go string/int — you get
//   "converting NULL to string is unsupported". Your options:
//     sql.NullString / NullInt64 / NullTime / NullBool  (.Valid + .String)
//     *string, *int                                     (nil == NULL)
//     sql.Null[T]                                       (generic, Go 1.22+)
//     COALESCE(publisher, '') in the SQL                (simplest, if '' is fine)
//
// RULES TO INTERNALISE
//   1. One *sql.DB for the whole program. It is a pool and it is
//      goroutine-safe. Never open one per request.
//   2. sql.Open does not connect. Ping to find out if the DB is reachable.
//   3. Always defer rows.Close() and always check rows.Err().
//   4. Always use placeholders. Never Sprintf a value into SQL.
//   5. sql.ErrNoRows is normal — translate it, don't log it as a 500.
//   6. Use the ...Context variants everywhere so queries respect timeouts
//      and client disconnects.
//   7. defer tx.Rollback() immediately after BeginTx.
//
// NEXT: lesson 37 turns this into a proper repository layer with
// migrations, which is what you'd actually ship.
// ----------------------------------------------------------------------------
