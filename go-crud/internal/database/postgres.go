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

// Migrate applies every *.sql file in migrationsDir that hasn't run yet, in
// filename order, tracking progress in a schema_migrations table.
func Migrate(db *sql.DB, migrationsDir string) (applied int, err error) {
	const bookkeeping = `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`
	if _, err := db.Exec(bookkeeping); err != nil {
		return 0, fmt.Errorf("create schema_migrations: %w", err)
	}

	done := map[string]bool{}
	rows, err := db.Query(`SELECT version FROM schema_migrations`)
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
	rows.Close()

	names, err := filepath.Glob(filepath.Join(migrationsDir, "*.sql"))
	if err != nil {
		return 0, fmt.Errorf("glob migrations: %w", err)
	}
	slices.Sort(names)

	for _, name := range names {
		version := strings.TrimSuffix(filepath.Base(name), ".sql")
		if done[version] {
			continue
		}

		body, err := os.ReadFile(name)
		if err != nil {
			return applied, fmt.Errorf("read %s: %w", name, err)
		}

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
	return applied, nil
}
