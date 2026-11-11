package web

import (
	"io"
	"net/http"

	"github.com/google/uuid"

	"github.com/jrcoronel-moon/hilo-crm/internal/branding"
	"github.com/jrcoronel-moon/hilo-crm/internal/crm"
	"github.com/jrcoronel-moon/hilo-crm/internal/i18n"
	"github.com/jrcoronel-moon/hilo-crm/internal/identity"
	"github.com/jrcoronel-moon/hilo-crm/internal/store"
)

type adminData struct {
	CSRF       string
	Domains    []store.PlatformAllowedDomain
	Workspaces []store.ListAllWorkspacesRow
	Member     map[uuid.UUID]bool // workspaces the superadmin already belongs to
}

func (h *Handler) adminPage(w http.ResponseWriter, r *http.Request, v view) {
	if !v.Sess.IsSuperadmin {
		h.fail(w, r, identity.ErrForbidden)
		return
	}
	doms, err := h.id.PlatformDomains(r.Context(), true)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	wss, err := h.id.AllWorkspaces(r.Context(), true)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.render(w, r, http.StatusOK, "admin", page{Title: i18n.M("Superadmin"), View: &v, Nav: "admin", Flash: flashes[r.URL.Query().Get("ok")],
		Data: adminData{CSRF: v.Sess.CSRFToken, Domains: doms, Workspaces: wss, Member: v.memberOf()}})
}

func (h *Handler) adminAction(w http.ResponseWriter, r *http.Request, v view) {
	var err error
	switch r.PathValue("action") {
	case "domain":
		err = h.id.AddPlatformDomain(r.Context(), v.Sess.IsSuperadmin, v.Sess.UserID, r.PostFormValue("domain"))
	case "domain-remove":
		err = h.id.RemovePlatformDomain(r.Context(), v.Sess.IsSuperadmin, r.PostFormValue("domain"))
	case "brand-name":
		err = h.brand.SetName(r.Context(), v.Sess.IsSuperadmin, v.Sess.UserID, r.PostFormValue("name"))
	case "brand-logo":
		err = h.uploadLogo(r, v)
	case "brand-logo-reset":
		err = h.brand.ClearLogo(r.Context(), v.Sess.IsSuperadmin, v.Sess.UserID)
	case "join":
		h.adminJoin(w, r, v)
		return
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.log.Info("superadmin action", "action", r.PathValue("action"), "user_id", v.Sess.UserID)
	http.Redirect(w, r, "/admin?ok=saved", http.StatusSeeOther)
}

// adminJoin enters a workspace: the superadmin becomes an admin member (see
// identity.SuperadminJoin) and it becomes their current workspace.
func (h *Handler) adminJoin(w http.ResponseWriter, r *http.Request, v view) {
	ws, err := uuid.Parse(r.PostFormValue("workspace"))
	if err != nil {
		h.fail(w, r, identity.ErrNotFound)
		return
	}
	if err = h.id.SuperadminJoin(r.Context(), v.Sess.UserID, v.Sess.IsSuperadmin, ws); err == nil {
		err = h.id.SwitchWorkspace(r.Context(), v.Sess, ws)
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.log.Info("superadmin action", "action", "join", "user_id", v.Sess.UserID, "workspace_id", ws)
	http.Redirect(w, r, "/board", http.StatusSeeOther)
}

// uploadLogo reads the multipart file (size already capped by the body
// limit) and hands it to the branding service, which sniffs its real type.
func (h *Handler) uploadLogo(r *http.Request, v view) error {
	f, _, err := r.FormFile("logo")
	if err != nil {
		return &crm.ValidationError{Field: "logo", Msg: i18n.M("elegí un archivo de imagen")}
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, branding.MaxLogo+1))
	if err != nil {
		return err
	}
	return h.brand.SetLogo(r.Context(), v.Sess.IsSuperadmin, v.Sess.UserID, data)
}

// logo serves the uploaded logo. An SVG can carry script, so the response is
// sandboxed: even opened directly, nothing in it runs on this origin.
func (h *Handler) logo(w http.ResponseWriter, r *http.Request) {
	data, mime, at, err := h.brand.Logo(r.Context())
	if err != nil {
		http.Redirect(w, r, "/static/favicon.svg", http.StatusFound)
		return
	}
	hd := w.Header()
	hd.Set("Content-Type", mime)
	hd.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Cross-Origin-Resource-Policy", "same-origin")
	hd.Set("Cache-Control", "public, max-age=86400")
	hd.Set("Last-Modified", at.UTC().Format(http.TimeFormat))
	_, _ = w.Write(data)
}
