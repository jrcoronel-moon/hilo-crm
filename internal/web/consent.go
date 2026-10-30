package web

import (
	"errors"
	"html/template"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/jrcoronel-moon/hilo-crm/internal/oauth"
)

type consentData struct {
	Pending oauth.Pending
	Blob    string
	CSRF    string
	Scopes  []scopeInfo
}

type scopeInfo struct{ Name, Label, Desc, Icon string }

var scopeInfos = map[string]scopeInfo{
	"crm:read":  {"crm:read", "Leer", "Empresas, contactos, oportunidades, interacciones y métricas.", "i-eye"},
	"crm:write": {"crm:write", "Escribir", "Crear y mover oportunidades, registrar interacciones.", "i-pencil"},
	"crm:admin": {"crm:admin", "Administrar", "Pipelines y canales. Pocos agentes lo necesitan.", "i-shield"},
}

func describeScopes(scope string) []scopeInfo {
	var out []scopeInfo
	for _, s := range strings.Fields(scope) {
		if info, ok := scopeInfos[s]; ok {
			out = append(out, info)
		}
	}
	return out
}

// authorize validates the request, makes sure the user is signed in, and
// shows the consent screen, unless a workspace admin preapproved the client.
func (h *Handler) authorize(w http.ResponseWriter, r *http.Request) {
	p, err := h.oauth.ParseAuthorize(r.Context(), r.URL.Query())
	if err != nil {
		var ae *oauth.AuthorizeError
		if errors.As(err, &ae) && ae.Redirect != "" {
			http.Redirect(w, r, ae.Redirect, http.StatusFound)
			return
		}
		h.render(w, http.StatusBadRequest, "error", page{Title: "Solicitud inválida",
			Error: "La aplicación que te trajo aquí envió una solicitud de autorización inválida (cliente o redirect_uri no registrados)."})
		return
	}
	h.session(func(w http.ResponseWriter, _ *http.Request, v view) {
		if v.Current == nil {
			http.Redirect(w, r, "/workspaces/new", http.StatusSeeOther)
			return
		}
		if dest, ws, ok, err := h.oauth.Preapproved(r.Context(), p, v.Sess.UserID); err != nil {
			h.fail(w, r, err)
			return
		} else if ok {
			h.log.Info("oauth consent preapproved", "user_id", v.Sess.UserID, "workspace_id", ws, "client", p.ClientName)
			h.handBack(w, "Agente autorizado", dest)
			return
		}
		blob, err := h.oauth.SignPending(p)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		w.Header().Set("X-Frame-Options", "DENY")
		h.render(w, http.StatusOK, "consent", page{Title: "Autorizar acceso", View: &v, Bare: true,
			Data: consentData{Pending: p, Blob: blob, CSRF: v.Sess.CSRFToken, Scopes: describeScopes(p.Scope)}})
	})(w, r)
}

// consent handles the user's decision.
func (h *Handler) consent(w http.ResponseWriter, r *http.Request, v view) {
	p, err := h.oauth.VerifyPending(r.PostFormValue("req"))
	if err != nil {
		h.render(w, http.StatusBadRequest, "error", page{Title: "Solicitud vencida", Error: "La solicitud de autorización venció. Volvé a conectar desde tu agente."})
		return
	}
	if r.PostFormValue("decision") != "approve" {
		h.handBack(w, "Acceso cancelado", h.oauth.Deny(p))
		return
	}
	ws, err := uuid.Parse(r.PostFormValue("workspace"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	dest, err := h.oauth.Approve(r.Context(), p, v.Sess.UserID, ws, r.PostFormValue("read_only") == "1")
	if err != nil {
		if errors.Is(err, oauth.ErrNoWorkspaceAccess) {
			h.render(w, http.StatusForbidden, "error", page{Title: "Sin acceso", Error: "No sos miembro de ese workspace."})
			return
		}
		h.fail(w, r, err)
		return
	}
	h.log.Info("oauth consent granted", "user_id", v.Sess.UserID, "workspace_id", ws, "client", p.ClientName, "verified", p.Verified)
	h.handBack(w, "Agente autorizado", dest)
}

// handBack returns the browser to the agent's redirect URI through a page
// that navigates by script (see static/app.js).
func (h *Handler) handBack(w http.ResponseWriter, title, dest string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	h.render(w, http.StatusOK, "redirect", page{Title: title, Data: template.URL(dest)}) //nolint:gosec // dest is the client's registered redirect_uri, validated by oauth.Approve/Deny
}
