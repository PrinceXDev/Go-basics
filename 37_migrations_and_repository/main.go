package main

// ============================================================================
// LESSON 37: MIGRATIONS + THE REPOSITORY PATTERN
//
// Lesson 36 showed the raw database/sql API. Real services don't scatter
// SQL through their handlers — they put it behind a repository, and they
// evolve the schema with versioned migrations. This lesson builds both.
//
// FILES
//   migrate.go            a ~60-line migration runner over embedded SQL
//   migrations/*.sql      the versioned, immutable migration files
//   store.go              the TaskRepository interface + SQL and fake impls
//   main.go               (this file) wiring and a demo
//
// Run:  go run ./37_migrations_and_repository
// Run it TWICE — the second run should skip both migrations.
// ============================================================================

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

func main() {
	if err := realMain(); err != nil {
		// Keeping main tiny and putting the work in a function that returns
		// an error means every `defer` below actually runs. log.Fatal calls
		// os.Exit, which SKIPS defers — a subtle and common resource leak.
		log.Fatal(err)
	}
}

func realMain() error {
	ctx := context.Background()

	db, err := openDB(ctx, filepath.Join("37_migrations_and_repository", "tasks.db"))
	if err != nil {
		return err
	}
	defer db.Close()

	// ---------- step 1: migrate ----------
	fmt.Println("== migrations ==")
	n, err := Migrate(ctx, db)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	fmt.Printf("applied %d new migration(s)\n", n)

	// ---------- step 2: use the repository ----------
	// `repo` is typed as the INTERFACE. Nothing below this line knows or
	// cares that there's a SQLite database underneath.
	var repo TaskRepository = NewSQLTaskRepo(db)

	if err := demo(ctx, repo, "SQL repo"); err != nil {
		return err
	}

	// ---------- step 3: the exact same demo, zero database ----------
	// This is the payoff of the interface: identical code, in-memory fake.
	fmt.Println()
	if err := demo(ctx, NewMemTaskRepo(), "in-memory fake"); err != nil {
		return err
	}

	return nil
}

// openDB centralises pool configuration so every entry point (server, CLI,
// tests) gets the same settings.
func openDB(ctx context.Context, path string) (*sql.DB, error) {
	// SQLite-specific pragmas, passed as DSN query params:
	//   _pragma=busy_timeout  wait instead of instantly returning SQLITE_BUSY
	//   _pragma=journal_mode(WAL)  readers don't block the writer
	//   _pragma=foreign_keys(1)    SQLite has FKs OFF by default (!)
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(time.Hour)

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}
	return db, nil
}

// demo exercises the repository. Its parameter is the INTERFACE, so it runs
// unchanged against SQLite or the fake — which is exactly how your real
// tests will be written.
func demo(ctx context.Context, repo TaskRepository, label string) error {
	fmt.Printf("== exercising the repository (%s) ==\n", label)

	// Create a few tasks. Ignore duplicate-run noise by just creating more.
	for _, t := range []struct {
		title    string
		priority int
	}{
		{"write the migration runner", 1},
		{"review the repository interface", 2},
		{"ship it", 3},
	} {
		created, err := repo.Create(ctx, t.title, t.priority)
		if err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
		fmt.Printf("  created #%d %q (p%d)\n", created.ID, created.Title, created.Priority)
	}

	// Validation errors come back as our OWN domain error, checkable with
	// errors.Is — no SQL constraint message leaking to the caller.
	if _, err := repo.Create(ctx, "", 1); errors.Is(err, ErrInvalidTask) {
		fmt.Println("  rejected empty title:", err)
	}

	// Read one.
	t, err := repo.GetByID(ctx, 1)
	if err != nil {
		return err
	}
	fmt.Printf("  GetByID(1): %q done=%v\n", t.Title, t.Done)

	// Not-found is a domain error, so an HTTP handler can map it to 404
	// without importing database/sql.
	if _, err := repo.GetByID(ctx, 9999); errors.Is(err, ErrTaskNotFound) {
		fmt.Println("  GetByID(9999) ->", err, "(handler maps this to 404)")
	}

	// Mutate.
	if err := repo.SetDone(ctx, 1, true); err != nil {
		return err
	}
	fmt.Println("  marked #1 done")

	// Filtered list. Taking the address of a literal via a helper keeps
	// the optional-filter call sites readable.
	pending := false
	list, err := repo.List(ctx, TaskFilter{Done: &pending, Limit: 10})
	if err != nil {
		return err
	}
	fmt.Printf("  %d task(s) still pending\n", len(list))
	for _, x := range list {
		fmt.Printf("    p%d #%d %s\n", x.Priority, x.ID, x.Title)
	}

	return nil
}

// ----------------------------------------------------------------------------
// WHAT TO TAKE AWAY
//   1. Migrations are numbered, immutable, embedded in the binary, applied
//      in one transaction each, and recorded in schema_migrations.
//   2. A repository is the SQL boundary. Above it: domain types and domain
//      errors. Below it: database/sql, drivers, NULL handling.
//   3. Depend on the interface, construct the struct. That single decision
//      is what makes the HTTP layer in lessons 39-43 testable.
//   4. Every method takes ctx as its first parameter, and every query uses
//      the ...Context variant.
//   5. Keep main() to "call a function, log the error" so defers run.
//
// IN PRODUCTION you'd replace the hand-rolled runner with golang-migrate or
// goose, and probably generate the store with sqlc — see lesson 38.
// ----------------------------------------------------------------------------
