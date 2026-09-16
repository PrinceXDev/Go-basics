package main

// ============================================================================
// PART 1 OF LESSON 37: SCHEMA MIGRATIONS
//
// A migration is a numbered, immutable SQL file. A migration RUNNER applies
// any files that haven't run yet, recording each one in a bookkeeping table
// so re-running is a no-op. That's the whole idea — every tool (golang-
// migrate, goose, atlas, Flyway, Prisma Migrate) is a fancier version of
// the ~60 lines below.
//
// Writing it by hand once means you'll understand what the tools do and be
// able to debug them at 2am.
// ============================================================================

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"slices"
	"strings"
)

// go:embed bakes files into the compiled binary at BUILD time. This is why
// a Go service can ship as a single file with no "don't forget to copy the
// migrations folder" deployment step.
//
// The directive must sit immediately above the var, with no blank line, and
// the comment must be exactly "//go:embed" (no space after //).
//
//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrate applies every migration that hasn't run yet, in filename order,
// and reports how many it applied.
func Migrate(ctx context.Context, db *sql.DB) (applied int, err error) {
	// The bookkeeping table. Creating it is itself idempotent.
	const bookkeeping = `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    TEXT PRIMARY KEY,
			applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`
	if _, err := db.ExecContext(ctx, bookkeeping); err != nil {
		return 0, fmt.Errorf("create schema_migrations: %w", err)
	}

	// Which versions have already run?
	done, err := appliedVersions(ctx, db)
	if err != nil {
		return 0, err
	}

	// Which migration files exist? fs.Glob over the embedded FS.
	names, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		return 0, fmt.Errorf("glob migrations: %w", err)
	}
	// Sort so 0001 runs before 0002. This is exactly why migrations are
	// zero-padded: "10" sorts before "2" as a string, but "0010" doesn't.
	slices.Sort(names)

	for _, name := range names {
		version := strings.TrimSuffix(strings.TrimPrefix(name, "migrations/"), ".sql")
		if slices.Contains(done, version) {
			fmt.Printf("  skip  %s (already applied)\n", version)
			continue
		}

		body, err := fs.ReadFile(migrationFiles, name)
		if err != nil {
			return applied, fmt.Errorf("read %s: %w", name, err)
		}

		// Each migration runs in its OWN transaction: the DDL and the
		// bookkeeping insert commit together. Without this you can end up
		// with a half-applied migration that the runner thinks succeeded —
		// the worst possible state.
		//
		// (Caveat: MySQL does not support transactional DDL, so there this
		// guarantee is weaker. Postgres and SQLite do.)
		if err := applyOne(ctx, db, version, string(body)); err != nil {
			return applied, err
		}
		fmt.Printf("  apply %s ✓\n", version)
		applied++
	}
	return applied, nil
}

func applyOne(ctx context.Context, db *sql.DB, version, body string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin %s: %w", version, err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit

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

// ----------------------------------------------------------------------------
// MIGRATION RULES THAT SAVE YOU LATER
//   1. Zero-pad version numbers (0001, not 1) so lexical sort == numeric.
//   2. Never edit a migration that has run outside your laptop. Add a new one.
//   3. Never `DROP` or rename a column in the same deploy that stops using
//      it. Ship "stop writing it" first, drop it a release later — otherwise
//      the old running instances break mid-rollout.
//   4. Run migrations as a separate step BEFORE the new code starts, not
//      from inside a request handler.
//   5. Real tools add: down-migrations, an advisory lock so two instances
//      can't migrate at once, and checksum verification. Use golang-migrate
//      or goose in production rather than this.
// ----------------------------------------------------------------------------
