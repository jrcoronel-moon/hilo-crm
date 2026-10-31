package web

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/jrcoronel-moon/hilo-crm/internal/branding"
	"github.com/jrcoronel-moon/hilo-crm/internal/config"
	"github.com/jrcoronel-moon/hilo-crm/internal/crm"
	"github.com/jrcoronel-moon/hilo-crm/internal/db"
	"github.com/jrcoronel-moon/hilo-crm/internal/events"
	"github.com/jrcoronel-moon/hilo-crm/internal/identity"
	"github.com/jrcoronel-moon/hilo-crm/internal/oauth"
)

// Mount wires the web UI and the OAuth authorization server onto one mux, and
// starts relaying live updates until ctx ends.
func Mount(ctx context.Context, cfg *config.Config, d *db.DB, log *slog.Logger) (*http.ServeMux, error) {
	oa := oauth.New(cfg, d, log)
	h, err := New(cfg, branding.New(d), crm.New(d, log).WithSecrets(cfg.SessionSecret), identity.New(d, log, cfg.SuperadminEmails), oa, log)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	if cfg.UIMode() != config.UIOff { // without sign-in there is no OAuth: personal access tokens only
		oa.Routes(mux)
	}
	if cfg.UIMode() == config.UIFull { // live updates feed the full UI only
		h.hub, h.done = events.NewHub(), ctx.Done()
		go events.Listen(ctx, d, log, h.hub.Publish)
	}
	h.Routes(mux)
	return mux, nil
}
