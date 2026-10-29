// Package db owns the Postgres connection pool, the embedded migrations and
// the transaction helpers that bind Row-Level Security to a workspace.
package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jrcoronel-moon/hilo-crm/internal/store"
)

// DB wraps the pool. All CRM access goes through InWorkspace so RLS is always
// bound; platform tables (users, sessions, tokens) use Platform.
type DB struct {
	Pool *pgxpool.Pool
}

// Open connects and verifies the connection.
func Open(ctx context.Context, url string) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnIdleTime = 10 * time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return &DB{Pool: pool}, nil
}

// Close releases the pool.
func (d *DB) Close() { d.Pool.Close() }

// Platform returns queries for tables without RLS (users, sessions, tokens…).
func (d *DB) Platform() *store.Queries { return store.New(d.Pool) }

// InWorkspace runs fn in a transaction with app.workspace_id bound, so every
// RLS-protected table only shows that workspace's rows. The setting is
// transaction-local (set_config(..., true)) and therefore can never leak to the
// next user of the pooled connection.
func (d *DB) InWorkspace(ctx context.Context, workspaceID uuid.UUID, fn func(q *store.Queries) error) error {
	if workspaceID == uuid.Nil {
		return errors.New("db: workspace id required")
	}
	return d.inTx(ctx, "app.workspace_id", workspaceID.String(), fn)
}

// InFormLookup runs fn with app.form_key bound: the only lead_forms row it
// can see is the active one with that public key (policy form_lookup).
func (d *DB) InFormLookup(ctx context.Context, publicKey string, fn func(q *store.Queries) error) error {
	if publicKey == "" {
		return errors.New("db: form key required")
	}
	return d.inTx(ctx, "app.form_key", publicKey, fn)
}

// InTx runs fn in a plain transaction (platform tables only).
func (d *DB) InTx(ctx context.Context, fn func(q *store.Queries) error) error {
	return d.inTx(ctx, "", "", fn)
}

func (d *DB) inTx(ctx context.Context, setting, value string, fn func(q *store.Queries) error) (err error) {
	tx, err := d.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()
	if setting != "" {
		if _, err = tx.Exec(ctx, "SELECT set_config($1, $2, true)", setting, value); err != nil {
			return fmt.Errorf("bind %s: %w", setting, err)
		}
	}
	if err = fn(store.New(tx)); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}
