// Command migrate applies the embedded database migrations and exits. It runs
// as the one-shot `migrate` container, connected as the hilo_migrator role.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/jrcoronel-moon/hilo-crm/internal/banner"
	"github.com/jrcoronel-moon/hilo-crm/internal/db"
)

// Set at build time with -ldflags "-X main.gitSHA=… -X main.buildTime=…".
var (
	gitSHA    = "dev"
	buildTime = ""
)

func main() {
	if err := run(); err != nil {
		slog.Error("migrate failed", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	url := os.Getenv("MIGRATOR_DATABASE_URL")
	if url == "" {
		return fmt.Errorf("MIGRATOR_DATABASE_URL is required")
	}
	banner.Print(os.Stdout, banner.Info{Service: "migrate", Commit: gitSHA, BuiltAt: buildTime, Migration: db.LatestMigration()})
	d, err := db.Open(ctx, url)
	if err != nil {
		return err
	}
	defer d.Close()
	applied, err := d.Migrate(ctx)
	if err != nil {
		return err
	}
	if len(applied) == 0 {
		slog.Info("schema up to date", "latest", db.LatestMigration())
		return nil
	}
	slog.Info("migrations applied", "names", applied)
	return nil
}
