package web

import (
	"net/http"

	"github.com/jrcoronel-moon/hilo-crm/internal/i18n"
)

// profile is the signed-in person's page (the badge at the foot of the
// sidebar): who they are, their workspaces, and their interface preferences.
func (h *Handler) profile(w http.ResponseWriter, r *http.Request, v view) {
	h.render(w, r, http.StatusOK, "profile", page{Title: i18n.M("Tu perfil"), View: &v, Nav: "profile", Data: v.Sess.CSRFToken})
}
