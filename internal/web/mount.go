package web

import (
	"log/slog"
	"net/http"

	"github.com/jrcoronel-moon/hilo-crm/internal/config"
	"github.com/jrcoronel-moon/hilo-crm/internal/crm"
	"github.com/jrcoronel-moon/hilo-crm/internal/db"
	"github.com/jrcoronel-moon/hilo-crm/internal/identity"
	"github.com/jrcoronel-moon/hilo-crm/internal/oauth"
)

// Mount wires the web UI and the OAuth authorization server onto one mux.
func Mount(cfg *config.Config, d *db.DB, log *slog.Logger) (*http.ServeMux, error) {
	oa := oauth.New(cfg, d, log)
	h, err := New(cfg, crm.New(d, log), identity.New(d, log, cfg.SuperadminEmails), oa, log)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	oa.Routes(mux)
	h.Routes(mux)
	return mux, nil
}
