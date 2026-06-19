package store

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/yx3728/lab-monitor/server/migrations"
)

// Migrate applies every embedded *.sql migration that has not yet been recorded
// in schema_migrations, in lexical filename order. Each file runs inside its own
// transaction, so a partially-applied migration never leaves the schema in a
// half-state. The runner is idempotent: re-running it is a no-op once all
// migrations are recorded, which makes "apply migrations on every boot" safe.
func (s *Store) Migrate(ctx context.Context) error {
	// The bookkeeping table itself is created outside the migration set so the
	// runner can always query it.
	if _, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			name       TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied, err := s.appliedMigrations(ctx)
	if err != nil {
		return err
	}

	names, err := migrationNames()
	if err != nil {
		return err
	}

	for _, name := range names {
		if applied[name] {
			continue
		}
		sqlBytes, err := migrations.FS.ReadFile(name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		if err := s.applyOne(ctx, name, string(sqlBytes)); err != nil {
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
	}
	return nil
}

// noTxMarker opts a migration out of the wrapping transaction. Some TimescaleDB
// DDL — notably CREATE MATERIALIZED VIEW ... WITH (timescaledb.continuous) —
// cannot run inside a transaction block, so those files start with this marker
// and we execute their statements one at a time, auto-committed.
const noTxMarker = "-- migrate:no-transaction"

// applyOne runs a single migration and records it. Normally this is atomic (DDL
// + the schema_migrations insert in one transaction); no-transaction migrations
// run statement-by-statement outside a transaction (see noTxMarker).
func (s *Store) applyOne(ctx context.Context, name, sqlText string) error {
	if strings.HasPrefix(strings.TrimSpace(sqlText), noTxMarker) {
		return s.applyOneNoTx(ctx, name, sqlText)
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, sqlText); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO schema_migrations (name) VALUES ($1)`, name)
		return err
	})
}

// applyOneNoTx executes each statement separately (no surrounding transaction),
// then records the migration. Statements must be idempotent (IF NOT EXISTS),
// since a mid-file failure cannot be rolled back.
func (s *Store) applyOneNoTx(ctx context.Context, name, sqlText string) error {
	for _, stmt := range splitStatements(sqlText) {
		if _, err := s.pool.Exec(ctx, stmt); err != nil {
			return err
		}
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO schema_migrations (name) VALUES ($1)`, name)
	return err
}

// splitStatements splits a SQL file into individual statements on semicolons,
// dropping blanks and comment-only fragments. Adequate for our hand-written
// migration files (no semicolons inside string literals or function bodies).
func splitStatements(sqlText string) []string {
	var out []string
	for _, part := range strings.Split(sqlText, ";") {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		// Skip fragments that are only line comments.
		onlyComments := true
		for _, line := range strings.Split(trimmed, "\n") {
			if l := strings.TrimSpace(line); l != "" && !strings.HasPrefix(l, "--") {
				onlyComments = false
				break
			}
		}
		if !onlyComments {
			out = append(out, trimmed)
		}
	}
	return out
}

func (s *Store) appliedMigrations(ctx context.Context) (map[string]bool, error) {
	rows, err := s.pool.Query(ctx, `SELECT name FROM schema_migrations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	applied := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		applied[name] = true
	}
	return applied, rows.Err()
}

// migrationNames returns the embedded *.sql filenames in lexical order.
func migrationNames() ([]string, error) {
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && len(e.Name()) > 4 && e.Name()[len(e.Name())-4:] == ".sql" {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}
