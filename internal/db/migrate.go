package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// migrationLockID is an arbitrary constant for the Postgres advisory lock that
// serialises migrations, so two instances starting at once cannot both apply them.
const migrationLockID int64 = 0x7a6f6e61 // "zona"

const schemaMigrationsDDL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INT PRIMARY KEY,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
)`

type migration struct {
	Version int
	Name    string
	SQL     string
}

// Migrate applies every embedded migration that has not been applied yet, in
// version order. It is idempotent: the second run is a no-op.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("migrate: acquire: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLockID); err != nil {
		return fmt.Errorf("migrate: lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", migrationLockID)
	}()

	if _, err := conn.Exec(ctx, schemaMigrationsDDL); err != nil {
		return fmt.Errorf("migrate: create schema_migrations: %w", err)
	}

	applied, err := appliedVersions(ctx, conn)
	if err != nil {
		return err
	}

	migrations, err := loadMigrations()
	if err != nil {
		return err
	}

	for _, m := range migrations {
		if applied[m.Version] {
			continue
		}
		if err := applyMigration(ctx, conn, m); err != nil {
			return err
		}
	}
	return nil
}

func appliedVersions(ctx context.Context, conn *pgxpool.Conn) (map[int]bool, error) {
	rows, err := conn.Query(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return nil, fmt.Errorf("migrate: read applied versions: %w", err)
	}
	defer rows.Close()

	applied := make(map[int]bool)
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("migrate: scan version: %w", err)
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("migrate: read applied versions: %w", err)
	}
	return applied, nil
}

func applyMigration(ctx context.Context, conn *pgxpool.Conn, m migration) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("migrate %s: begin: %w", m.Name, err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if _, err := tx.Exec(ctx, m.SQL); err != nil {
		return fmt.Errorf("migrate %s: %w", m.Name, err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", m.Version); err != nil {
		return fmt.Errorf("migrate %s: record version: %w", m.Name, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("migrate %s: commit: %w", m.Name, err)
	}
	return nil
}

// loadMigrations reads the embedded SQL files and orders them by the numeric
// prefix of the filename (0001_init.sql, 0002_....sql).
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("migrate: read embedded migrations: %w", err)
	}

	out := make([]migration, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		version, err := migrationVersion(e.Name())
		if err != nil {
			return nil, err
		}
		body, err := migrationsFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("migrate: read %s: %w", e.Name(), err)
		}
		out = append(out, migration{Version: version, Name: e.Name(), SQL: string(body)})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

func migrationVersion(name string) (int, error) {
	prefix, _, ok := strings.Cut(name, "_")
	if !ok {
		return 0, fmt.Errorf("migrate: %s must be named <version>_<name>.sql", name)
	}
	v, err := strconv.Atoi(prefix)
	if err != nil {
		return 0, fmt.Errorf("migrate: %s has a non-numeric version prefix: %w", name, err)
	}
	return v, nil
}
