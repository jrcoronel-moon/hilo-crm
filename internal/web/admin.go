package web

import (
	"net/http"

	"github.com/jrcoronel-moon/hilo-crm/internal/identity"
	"github.com/jrcoronel-moon/hilo-crm/internal/store"
)

type adminData struct {
	CSRF       string
	Domains    []store.PlatformAllowedDomain
	Workspaces []store.ListAllWorkspacesRow
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
	h.render(w, http.StatusOK, "admin", page{Title: "Superadmin", View: &v, Nav: "admin", Flash: flashes[r.URL.Query().Get("ok")],
		Data: adminData{CSRF: v.Sess.CSRFToken, Domains: doms, Workspaces: wss}})
}

func (h *Handler) adminAction(w http.ResponseWriter, r *http.Request, v view) {
	var err error
	switch r.PathValue("action") {
	case "domain":
		err = h.id.AddPlatformDomain(r.Context(), v.Sess.IsSuperadmin, v.Sess.UserID, r.PostFormValue("domain"))
	case "domain-remove":
		err = h.id.RemovePlatformDomain(r.Context(), v.Sess.IsSuperadmin, r.PostFormValue("domain"))
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
