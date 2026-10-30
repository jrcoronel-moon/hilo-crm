// Package app is the shared bootstrap of the long-running services (web, mcp):
// configuration, logging, database, schema check, startup banner, hardened
// HTTP server and graceful shutdown.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/jrcoronel-moon/hilo-crm/internal/banner"
	"github.com/jrcoronel-moon/hilo-crm/internal/config"
	"github.com/jrcoronel-moon/hilo-crm/internal/db"
	"github.com/jrcoronel-moon/hilo-crm/internal/httpx"
)

// Build identifies the binary (set by -ldflags in the Dockerfile).
type Build struct {
	Service string
	Commit  string
	Time    string
}

// Env is what a service gets to build its handler.
type Env struct {
	Ctx context.Context // canceled on shutdown
	Cfg *config.Config
	DB  *db.DB
	Log *slog.Logger
	IPs *httpx.ClientIP
}

// Run loads config, opens the database, prints the banner and serves the
// handler returned by mount until SIGINT/SIGTERM.
func Run(b Build, mount func(Env) (http.Handler, []string, error)) {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})).With("service", b.Service)
	if err := run(b, log, mount); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(b Build, log *slog.Logger, mount func(Env) (http.Handler, []string, error)) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg, err := config.Load(nil)
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	d, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer d.Close()
	applied, err := d.AppliedLatest(ctx)
	if err != nil {
		return fmt.Errorf("read schema version (did the migrate container run?): %w", err)
	}
	if applied < db.LatestMigration() {
		return fmt.Errorf("database schema %q is older than this build (%q): run the migrate container first", applied, db.LatestMigration())
	}
	env := Env{Ctx: ctx, Cfg: cfg, DB: d, Log: log, IPs: httpx.NewClientIP(cfg.TrustedProxies)}
	h, integrations, err := mount(env)
	if err != nil {
		return err
	}
	banner.Print(os.Stdout, banner.Info{Service: b.Service, Commit: b.Commit, BuiltAt: b.Time, Migration: applied,
		Addr: cfg.ListenAddr, Integrations: integrations})

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           Middleware(env, h),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	go purgeLoop(ctx, d, log)
	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// Middleware wraps every service handler with the common chain.
func Middleware(env Env, h http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := env.DB.Pool.Ping(r.Context()); err != nil {
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok"))
	})
	mux.Handle("/", h)
	return httpx.Recover(env.Log)(httpx.Logger(env.Log, env.IPs)(httpx.SecurityHeaders(httpx.LimitBody(httpx.MaxBody)(mux))))
}

// healthcheck is the container probe (distroless images have no curl):
// `<binary> healthcheck` exits 0 when /healthz answers 200.
func healthcheck() int {
	port := "8080"
	if _, p, err := net.SplitHostPort(os.Getenv("LISTEN_ADDR")); err == nil && p != "" {
		if _, err := strconv.Atoi(p); err == nil {
			port = p
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// Always loopback; only the numeric port comes from configuration.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+port+"/healthz", nil) //nolint:gosec // G704: fixed loopback host, numeric port
	if err != nil {
		return 1
	}
	res, err := http.DefaultClient.Do(req) //nolint:gosec // G704: fixed loopback host, numeric port
	if err != nil {
		return 1
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

// purgeLoop deletes expired sessions, codes and tokens hourly.
func purgeLoop(ctx context.Context, d *db.DB, log *slog.Logger) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := d.Platform().PurgeExpired(ctx); err != nil {
				log.Warn("purge expired", "err", err)
			}
		}
	}
}
