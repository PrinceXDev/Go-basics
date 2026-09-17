package database

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	_ "github.com/lib/pq"
)

// Connect opens a Postgres connection pool and verifies it with a ping.
func Connect(dsn string) (*sql.DB, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping db: %w", err)
	}
	return db, nil
}

/* ***** Mental model *****
Migrations are a to-do list stapled to your .sql files;
the bookkeeping table is the "done" pile; and every item is completed inside a transaction so a crash mid-migration can never leave a half-finished checkbox.
*/

// Migrate applies every *.sql file in migrationsDir that hasn't run yet, in
// filename order, tracking progress in a schema_migrations table.
func Migrate(db *sql.DB, migrationsDir string) (applied int, err error) {
	/*
			BEFORE this line runs (fresh DB):        AFTER this line runs:
		┌─────────────┐                          ┌─────────────────────┐
		│  (empty DB) │      ────────────►       │ schema_migrations   │
		└─────────────┘                          │   (empty, 0 rows)   │
		                                         └─────────────────────┘
	*/

	// Step 1 — Create the clipboard itself, if it's not there yet
	const bookkeeping = `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`
	if _, err := db.Exec(bookkeeping); err != nil {
		return 0, fmt.Errorf("create schema_migrations: %w", err)
	}

	// Step 2 — Read the clipboard into memory: "who's already on the list?"
	done := map[string]bool{}
	rows, err := db.Query(`SELECT version FROM schema_migrations`)

	/*
		Query result (rows):          done map (after the loop):
		┌───────────────────────┐     {
			│ 0001_create_users      │       "0001_create_users": true,
			│ 0002_create_products   │       "0002_create_products": true,
			└───────────────────────┘     }
	*/

	if err != nil {
		return 0, fmt.Errorf("read schema_migrations: %w", err)
	}

	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan version: %w", err)
		}
		done[v] = true
	}

	/* Why rows.Close() matters?
	while rows is open, it holds a connection from the pool hostage.
	Forgetting to close it is one of the most common Go database bugs — eventually you exhaust your connection pool and everything hangs.
	Always close your rows.
	*/
	rows.Close()

	/*
		List the files on disk, and put them in the RIGHT order

			filepath.Glob is like running ls migrations/*.sql — it returns every filename matching that pattern.
			But filesystems don't guarantee any particular order, so we explicitly slices.Sort them — alphabetically,
			which is exactly why we zero-pad the numbers (0001, not 1):

			WITHOUT zero-padding, sorted alphabetically:   WITH zero-padding:
		  "10_x.sql"                                     "0001_x.sql"
		  "2_y.sql"     <- 2 sorts AFTER 10!  ❌         "0002_y.sql"   ✅ correct order
		  "1_z.sql"                                       "0003_z.sql"

		  If migration order is wrong, you might try to CREATE TABLE order_items referencing a products table that doesn't exist yet — order is everything here.
	*/

	// Step 3 — List the files on disk, and put them in the RIGHT order
	names, err := filepath.Glob(filepath.Join(migrationsDir, "*.sql"))
	if err != nil {
		return 0, fmt.Errorf("glob migrations: %w", err)
	}
	slices.Sort(names)

	// Step 4: The loop: for each file, "are you on the list already?"
	for _, name := range names {
		version := strings.TrimSuffix(filepath.Base(name), ".sql")
		if done[version] {
			continue
		}

		/* filepath.Base("migrations/0003_create_orders.sql") → "0003_create_orders.sql".
		strings.TrimSuffix(..., ".sql") chops the extension off → "0003_create_orders".
		That string is the version name — it's exactly what we compare against the clipboard map.
		If it's already true in done, continue skips straight to the next file — this migration has already run, nothing to do. */

		// For a NEW migration: run it inside a transaction
		body, err := os.ReadFile(name)
		if err != nil {
			return applied, fmt.Errorf("read %s: %w", name, err)
		}

		/* ***** IMPORTANT *****
				Why wrap this in a transaction (tx)? This is the single most important idea in this function. Two things need to happen together: (1) actually run the SQL (e.g. CREATE TABLE orders...), and (2) write the "I did this" entry on the clipboard. If we did these as two separate, un-transactioned statements and the app crashed between them, you'd get a nightmare scenario:

		WITHOUT a transaction — the danger:

		  Step A: CREATE TABLE orders   ✅ succeeds
		  ⚡ app crashes / power cut here ⚡
		  Step B: INSERT INTO schema_migrations  ❌ never runs

		  Next restart: "0003_create_orders" is NOT on the clipboard,
		  so we try to run it AGAIN -> "ERROR: relation orders already exists" -> CRASH LOOP

		With a transaction, tx.Exec (run the migration) and the bookkeeping INSERT are staged together,
		invisible to the rest of the database, until tx.Commit() makes both permanent atomically — as one indivisible unit.
		If anything fails at any point, tx.Rollback() throws away everything staged in that transaction, as if it never happened:

		WITH a transaction — safe:

		  tx.Begin()
		     ├─ CREATE TABLE orders          (staged, not yet visible/permanent)
		     ├─ INSERT INTO schema_migrations (staged, not yet visible/permanent)
		  tx.Commit()  ── both become permanent together, or...
		  tx.Rollback() ── BOTH vanish together, DB looks exactly as if we never tried

		This is called atomicity — the "A" in the classic database ACID guarantee. Either the whole migration + its bookkeeping entry happens,
		or none of it does. No in-between broken state is ever possible.
		*/
		tx, err := db.Begin()
		if err != nil {
			return applied, fmt.Errorf("begin %s: %w", version, err)
		}
		if _, err := tx.Exec(string(body)); err != nil {
			tx.Rollback()
			return applied, fmt.Errorf("apply %s: %w", version, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (version) VALUES ($1)`, version); err != nil {
			tx.Rollback()
			return applied, fmt.Errorf("record %s: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			return applied, fmt.Errorf("commit %s: %w", version, err)
		}
		applied++
	}

	// Step 6 — Return how many were actually applied
	return applied, nil
}
