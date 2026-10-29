// Package testdb gives integration tests a real, migrated, isolated Postgres
// database connected as the least-privileged app role, so tests exercise Row
// Level Security and grants exactly as production does.
//
// It expects the test cluster started by `make test-db` (deploy/compose.test.yml):
//
//	HILO_TEST_ADMIN_URL  superuser URL of the test cluster (to create/drop DBs)
//	HILO_TEST_APP_URL    app-role URL template; the database name is replaced
//
// The `hilo` database of that cluster must already be migrated (make does it);
// each test gets a fresh copy of it via CREATE DATABASE … TEMPLATE.
package testdb

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/jrcoronel-moon/hilo-crm/internal/db"
	"github.com/jrcoronel-moon/hilo-crm/internal/secure"
)

// New returns a migrated database dedicated to t, dropped at cleanup.
func New(t testing.TB) *db.DB {
	t.Helper()
	admin, app := os.Getenv("HILO_TEST_ADMIN_URL"), os.Getenv("HILO_TEST_APP_URL")
	if admin == "" || app == "" {
		if os.Getenv("HILO_TEST_REQUIRE_DB") == "1" {
			t.Fatal("HILO_TEST_ADMIN_URL / HILO_TEST_APP_URL not set (run `make test`)")
		}
		t.Skip("integration test: run `make test` to start the test database")
	}
	ctx := context.Background()
	name := "t_" + strings.ToLower(strings.NewReplacer("-", "", "_", "").Replace(secure.Token("", 9)))
	if err := createFromTemplate(ctx, admin, name); err != nil {
		t.Fatalf("create test database: %v", err)
	}
	u, err := url.Parse(app)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	d, err := db.Open(ctx, u.String())
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() {
		d.Close()
		if c, err := pgx.Connect(ctx, admin); err == nil {
			_, _ = c.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
			_ = c.Close(ctx)
		}
	})
	return d
}

// createFromTemplate copies the migrated `hilo` database. Postgres refuses to
// copy a template that has other sessions, which happens when test packages
// run in parallel, so it retries for a while.
func createFromTemplate(ctx context.Context, admin, name string) error {
	c, err := pgx.Connect(ctx, admin)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close(ctx) }()
	stmt := fmt.Sprintf("CREATE DATABASE %s TEMPLATE hilo", pgx.Identifier{name}.Sanitize())
	for attempt := 0; ; attempt++ {
		_, err = c.Exec(ctx, stmt)
		var pg *pgconn.PgError
		if err == nil || attempt > 50 || !errors.As(err, &pg) || pg.Code != "55006" { // object_in_use
			return err
		}
		time.Sleep(100 * time.Millisecond)
	}
}
