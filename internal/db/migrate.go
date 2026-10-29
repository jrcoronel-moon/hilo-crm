package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// migrationLockKey is an arbitrary constant for pg_advisory_xact_lock, so two
// migrate runs (two deploys at once) can never interleave.
const migrationLockKey = 0x48494c4f // "HILO"

// Migrations lists the embedded migration files in apply order.
func Migrations() []string {
	entries, _ := fs.Glob(migrationsFS, "migrations/*.sql")
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, strings.TrimPrefix(e, "migrations/"))
	}
	sort.Strings(names)
	return names
}

// LatestMigration is the name of the newest embedded migration (for the
// startup banner).
func LatestMigration() string {
	m := Migrations()
	if len(m) == 0 {
		return ""
	}
	return strings.TrimSuffix(m[len(m)-1], ".sql")
}

// Migrate applies every pending migration, each in its own transaction, under
// an advisory lock. It must run as the migrator role. It returns the names
// applied in this run.
func (d *DB) Migrate(ctx context.Context) ([]string, error) {
	var applied []string
	if _, err := d.Pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return nil, fmt.Errorf("create schema_migrations: %w", err)
	}
	for _, name := range Migrations() {
		did, err := d.applyOne(ctx, name)
		if err != nil {
			return applied, err
		}
		if did {
			applied = append(applied, name)
		}
	}
	return applied, nil
}

func (d *DB) applyOne(ctx context.Context, name string) (bool, error) {
	body, err := migrationsFS.ReadFile("migrations/" + name)
	if err != nil {
		return false, err
	}
	did := false
	err = pgx.BeginFunc(ctx, d.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", migrationLockKey); err != nil {
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE name = $1)", name).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return nil
		}
		if _, err := tx.Exec(ctx, string(body)); err != nil {
			return fmt.Errorf("apply %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (name) VALUES ($1)", name); err != nil {
			return err
		}
		did = true
		return nil
	})
	return did, err
}

// AppliedLatest returns the newest migration recorded in the database, so a
// service can refuse to start against a schema older than its code.
func (d *DB) AppliedLatest(ctx context.Context) (string, error) {
	var name string
	err := d.Pool.QueryRow(ctx, "SELECT COALESCE(max(name), '') FROM schema_migrations").Scan(&name)
	return strings.TrimSuffix(name, ".sql"), err
}
