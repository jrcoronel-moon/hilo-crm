package web

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/jrcoronel-moon/hilo-crm/internal/i18n"
	"github.com/jrcoronel-moon/hilo-crm/internal/identity"
	"github.com/jrcoronel-moon/hilo-crm/internal/secure"
)

type loginData struct {
	Google, Microsoft, Dev bool
	ReturnTo               string
	CSRF                   string
}

// The dev login form is protected against login CSRF with a double-submit
// token: a random value in a SameSite=Strict cookie that must match the form.
func (h *Handler) preLoginCookie() string {
	if h.cfg.SecureCookies() {
		return "__Host-hilo_prelogin"
	}
	return "hilo_prelogin"
}

func (h *Handler) loginPage(w http.ResponseWriter, r *http.Request) {
	msg := ""
	switch r.URL.Query().Get("error") {
	case "":
	case "not_allowed":
		msg = identity.ErrNotAllowed.Error()
	case "unverified":
		msg = i18n.M("Tu proveedor no verificó este email. Si usás Microsoft con una cuenta de trabajo, tu organización tiene que verificar el dominio.")
	case "disabled":
		msg = identity.ErrDisabled.Error()
	default:
		msg = i18n.M("No pudimos completar el inicio de sesión. Probá de nuevo.")
	}
	d := loginData{Google: h.cfg.GoogleEnabled(), Microsoft: h.cfg.MicrosoftEnabled(), Dev: h.devLoginAllowed(r),
		ReturnTo: safeReturn(r.URL.Query().Get("return_to"))}
	// An agent's authorization with a single provider: nothing to choose, go
	// straight to it. Only for /oauth/authorize, so signing out does not bounce
	// the user back in through the provider's remembered session.
	if msg == "" && !d.Dev && d.Google != d.Microsoft && strings.HasPrefix(d.ReturnTo, "/oauth/authorize?") {
		provider := "google"
		if d.Microsoft {
			provider = "microsoft"
		}
		http.Redirect(w, r, "/auth/"+provider+"?return_to="+url.QueryEscape(d.ReturnTo), http.StatusSeeOther)
		return
	}
	if d.Dev {
		d.CSRF = secure.Token("", 24)
		http.SetCookie(w, &http.Cookie{Name: h.preLoginCookie(), Value: d.CSRF, Path: "/", MaxAge: 600, HttpOnly: true, //nolint:gosec // G124: Secure follows the base URL scheme
			Secure: h.cfg.SecureCookies(), SameSite: http.SameSiteStrictMode})
	}
	w.Header().Set("Cache-Control", "no-store")
	h.render(w, r, http.StatusOK, "login", page{Title: i18n.M("Entrar"), Error: msg, Data: d})
}

func (h *Handler) authStart(w http.ResponseWriter, r *http.Request) {
	p, ok := h.provider(r.PathValue("provider"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !h.loginLimit.Allow(h.ips.Key(r)) {
		http.Error(w, "too many attempts", http.StatusTooManyRequests)
		return
	}
	u, err := h.startURL(w, p, r.URL.Query().Get("return_to"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	http.Redirect(w, r, u, http.StatusFound) //nolint:gosec // G710: u is the provider's fixed authorization endpoint plus our parameters
}

func (h *Handler) authCallback(w http.ResponseWriter, r *http.Request) {
	p, ok := h.provider(r.PathValue("provider"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	h.setCookie(w, h.loginCookieName(), "", -1)
	if r.URL.Query().Get("error") != "" {
		http.Redirect(w, r, "/login?error=provider", http.StatusSeeOther)
		return
	}
	ext, returnTo, err := h.finish(r.Context(), r, p)
	if err != nil {
		h.log.Warn("login callback rejected", "provider", p.name, "err", err, "ip", h.ips.Of(r))
		http.Redirect(w, r, "/login?error=failed", http.StatusSeeOther)
		return
	}
	h.signIn(w, r, ext, returnTo)
}

// devLoginAllowed: the configuration enables it (localhost base URL only)
// AND this request was addressed to that local host. A tunnel (cloudflared,
// ngrok…) forwards requests with its own public Host, so the password-less
// form can never be reached from outside even if a tunnel is left open.
func (h *Handler) devLoginAllowed(r *http.Request) bool {
	if !h.cfg.DevLogin || r.Header.Get("Cf-Connecting-Ip") != "" || r.Header.Get("Ngrok-Trace-Id") != "" {
		return false
	}
	u, err := url.Parse(h.cfg.BaseURL)
	if err != nil || !strings.EqualFold(r.Host, u.Host) {
		return false
	}
	fh := r.Header.Get("X-Forwarded-Host")
	return fh == "" || strings.EqualFold(fh, u.Host)
}

// devLogin is a password-less login for local development only (config
// refuses DEV_LOGIN_ENABLED unless the base URL is localhost).
func (h *Handler) devLogin(w http.ResponseWriter, r *http.Request) {
	if !h.devLoginAllowed(r) {
		if h.cfg.DevLogin {
			h.log.Warn("dev login refused: request did not come through the local base URL", "host", r.Host, "ip", h.ips.Of(r))
		}
		http.NotFound(w, r)
		return
	}
	c, err := r.Cookie(h.preLoginCookie())
	if (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != h.cfg.BaseURL) || err != nil || !secure.Equal(c.Value, r.PostFormValue("csrf")) {
		http.Error(w, "invalid or expired form, reload the page and try again", http.StatusForbidden)
		return
	}
	h.setCookie(w, h.preLoginCookie(), "", -1)
	email := strings.ToLower(strings.TrimSpace(r.PostFormValue("email")))
	h.signIn(w, r, identity.ExternalIdentity{Provider: "dev", Subject: email, Email: email, EmailVerified: true, Name: email},
		r.PostFormValue("return_to"))
}

func (h *Handler) signIn(w http.ResponseWriter, r *http.Request, ext identity.ExternalIdentity, returnTo string) {
	u, err := h.id.Login(r.Context(), ext)
	if err != nil {
		code := "failed"
		switch {
		case errors.Is(err, identity.ErrNotAllowed):
			code = "not_allowed"
		case errors.Is(err, identity.ErrEmailUnverified):
			code = "unverified"
		case errors.Is(err, identity.ErrDisabled):
			code = "disabled"
		default:
			h.log.Error("login", "err", err)
		}
		h.log.Info("login refused", "provider", ext.Provider, "reason", code, "ip", h.ips.Of(r))
		http.Redirect(w, r, "/login?error="+code, http.StatusSeeOther)
		return
	}
	token, err := h.id.CreateSession(r.Context(), u.ID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.setCookie(w, h.cookieName(), token, int(identity.SessionTTL.Seconds()))
	h.log.Info("login", "user_id", u.ID, "provider", ext.Provider)
	http.Redirect(w, r, safeReturn(returnTo), http.StatusSeeOther)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request, v view) {
	_ = h.id.Logout(r.Context(), v.Sess)
	h.setCookie(w, h.cookieName(), "", -1)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
