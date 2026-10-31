package postgres

import (
	"context"
	"embed"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Open creates a small single-machine pool. The caller owns Close.
func Open(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, fmt.Errorf("postgres: empty DATABASE_URL")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: parse dsn: %w", err)
	}
	cfg.MaxConns = 4
	cfg.MinConns = 1
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.ConnConfig.RuntimeParams["application_name"] = "bountygrid"

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("postgres: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: ping: %w", err)
	}
	return pool, nil
}

// Migrate applies embedded SQL migrations in lexical order. It is safe for a
// single-machine process and uses an advisory transaction lock to avoid races.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("postgres: create schema_migrations: %w", err)
	}

	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("postgres: read migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") || strings.HasSuffix(e.Name(), ".down.sql") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		if err := applyOne(ctx, pool, name); err != nil {
			return err
		}
	}
	return nil
}

// RollbackLast applies the .down.sql for the newest applied migration and
// removes its schema_migrations row. It is intentionally one-at-a-time.
func RollbackLast(ctx context.Context, pool *pgxpool.Pool) (string, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("postgres: begin rollback: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(0x42474644)`); err != nil {
		return "", fmt.Errorf("postgres: lock rollback: %w", err)
	}

	var version string
	if err := tx.QueryRow(ctx,
		`SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1`,
	).Scan(&version); err != nil {
		if err == pgx.ErrNoRows {
			return "", fmt.Errorf("postgres: no migrations to roll back")
		}
		return "", fmt.Errorf("postgres: find latest migration: %w", err)
	}

	downName := strings.TrimSuffix(version, ".sql") + ".down.sql"
	down, err := migrationFS.ReadFile("migrations/" + downName)
	if err != nil {
		return "", fmt.Errorf("postgres: missing down migration %s: %w", downName, err)
	}
	if _, err := tx.Exec(ctx, string(down)); err != nil {
		return "", fmt.Errorf("postgres: apply %s: %w", downName, err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM schema_migrations WHERE version = $1`, version); err != nil {
		return "", fmt.Errorf("postgres: remove %s: %w", version, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("postgres: commit rollback: %w", err)
	}
	return version, nil
}

// RollbackAll rolls back every applied migration, newest first.
func RollbackAll(ctx context.Context, pool *pgxpool.Pool) error {
	for {
		version, err := RollbackLast(ctx, pool)
		if err != nil {
			if strings.Contains(err.Error(), "no migrations") {
				return nil
			}
			return err
		}
		if version == "" {
			return nil
		}
	}
}

func applyOne(ctx context.Context, pool *pgxpool.Pool, name string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin %s: %w", name, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Single global migration lock; released at commit/rollback.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(0x42474644)`); err != nil {
		return fmt.Errorf("postgres: lock %s: %w", name, err)
	}

	var applied bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, name,
	).Scan(&applied); err != nil {
		return fmt.Errorf("postgres: check %s: %w", name, err)
	}
	if applied {
		return tx.Commit(ctx)
	}

	sql, err := migrationFS.ReadFile("migrations/" + name)
	if err != nil {
		return fmt.Errorf("postgres: read %s: %w", name, err)
	}
	if _, err := tx.Exec(ctx, string(sql)); err != nil {
		return fmt.Errorf("postgres: apply %s: %w", name, err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO schema_migrations (version) VALUES ($1)`, name,
	); err != nil {
		return fmt.Errorf("postgres: record %s: %w", name, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit %s: %w", name, err)
	}
	return nil
}
