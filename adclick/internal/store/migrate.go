package store

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
)

// Migrate runs every embedded .sql file in name order. Files are idempotent
// (IF NOT EXISTS / ON CONFLICT), and an advisory lock stops two starting
// services from migrating at the same time.
func (s *Store) Migrate(ctx context.Context, fsys fs.FS) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(727001)`); err != nil {
		return err
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock(727001)`) //nolint:errcheck

	names, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, n := range names {
		sql, err := fs.ReadFile(fsys, n)
		if err != nil {
			return err
		}
		if _, err := conn.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("migration %s: %w", n, err)
		}
	}
	return nil
}
