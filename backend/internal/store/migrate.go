package store

import (
	"context"
	"fmt"
	"io/fs"
	"sort"

	"github.com/jackc/pgx/v5"
)

// migrationLockID is an arbitrary advisory-lock key so two processes migrating
// at once take turns instead of racing.
const migrationLockID = 727001

// Migrate applies every unapplied *.sql file in fsys, in filename order, each in
// its own transaction. Applied versions are recorded in schema_migrations.
// It returns the versions it applied.
func (s *Store) Migrate(ctx context.Context, fsys fs.FS) ([]string, error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()

	// Session-level lock: must be taken and released on the same connection.
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLockID); err != nil {
		return nil, err
	}
	defer conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, migrationLockID)

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return nil, err
	}

	files, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return nil, err
	}
	sort.Strings(files)

	var applied []string
	for _, f := range files {
		var done bool
		if err := conn.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, f).Scan(&done); err != nil {
			return applied, err
		}
		if done {
			continue
		}
		sqlText, err := fs.ReadFile(fsys, f)
		if err != nil {
			return applied, err
		}
		if err := applyOne(ctx, conn, f, string(sqlText)); err != nil {
			return applied, fmt.Errorf("migration %s: %w", f, err)
		}
		applied = append(applied, f)
	}
	return applied, nil
}

func applyOne(ctx context.Context, conn interface {
	Begin(context.Context) (pgx.Tx, error)
}, version, sqlText string) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Simple protocol (no args) so a file may contain many statements.
	if _, err := tx.Exec(ctx, sqlText, pgx.QueryExecModeSimpleProtocol); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, version); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
