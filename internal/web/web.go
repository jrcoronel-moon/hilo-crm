// Package web is the human-facing side of Hilo: sign-in (Google, Microsoft),
// the OAuth consent screen, the pipeline board, a light dashboard, workspace
// settings and the superadmin page, plus the public lead-form endpoint.
//
// Pages are server-rendered with html/template (context-aware escaping) and
// served under a strict CSP with no inline script. Every state-changing
// request needs a session, a matching per-session CSRF token and, when the
// browser sends one, a same-origin Origin header.
package web

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"github.com/jrcoronel-moon/hilo-crm/internal/branding"
	"github.com/jrcoronel-moon/hilo-crm/internal/captcha"
	"github.com/jrcoronel-moon/hilo-crm/internal/config"
	"github.com/jrcoronel-moon/hilo-crm/internal/crm"
	"github.com/jrcoronel-moon/hilo-crm/internal/events"
	"github.com/jrcoronel-moon/hilo-crm/internal/httpx"
	"github.com/jrcoronel-moon/hilo-crm/internal/i18n"
	"github.com/jrcoronel-moon/hilo-crm/internal/identity"
	"github.com/jrcoronel-moon/hilo-crm/internal/oauth"
	"github.com/jrcoronel-moon/hilo-crm/internal/secure"
	"github.com/jrcoronel-moon/hilo-crm/internal/store"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// Handler serves the web UI.
type Handler struct {
	cfg   *config.Config
	brand *branding.Service
	crm   *crm.Service
	id    *identity.Service
	oauth *oauth.Server
	log   *slog.Logger
	ips   *httpx.ClientIP
	tpl   map[string]map[string]*template.Template // language → page

	loginLimit *httpx.RateLimiter
	formLimit  *httpx.RateLimiter
	// authorizeLimit caps OAuth authorization requests, which anyone can send
	// and which may fetch a client's metadata document from the internet.
	authorizeLimit *httpx.RateLimiter
	captcha        captchaVerifier // Cloudflare Turnstile (keys are per workspace)
	http           *http.Client    // to the identity providers

	hub  *events.Hub     // live updates (nil: disabled)
	done <-chan struct{} // closed when the process shuts down
}

// New builds the handler and parses the templates.
func New(cfg *config.Config, brand *branding.Service, crmSvc *crm.Service, idSvc *identity.Service, oa *oauth.Server, log *slog.Logger) (*Handler, error) {
	h := &Handler{
		cfg: cfg, brand: brand, crm: crmSvc, id: idSvc, oauth: oa, log: log, ips: httpx.NewClientIP(cfg.TrustedProxies),
		loginLimit: httpx.NewRateLimiter(30, 30), formLimit: httpx.NewRateLimiter(10, 10),
		authorizeLimit: httpx.NewRateLimiter(60, 30),
		http:           &http.Client{Timeout: 15 * time.Second},
	}
	h.captcha = captcha.NewTurnstile(cfg.TurnstileVerifyURL)
	return h, h.parseTemplates()
}

func (h *Handler) parseTemplates() error {
	pages, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil {
		return err
	}
	h.tpl = map[string]map[string]*template.Template{}
	for _, lang := range i18n.Supported {
		fm := funcsFor(lang)
		h.tpl[lang] = map[string]*template.Template{}
		for _, p := range pages {
			name := strings.TrimSuffix(strings.TrimPrefix(p, "templates/"), ".html")
			if name == "layout" || name == "partials" {
				continue
			}
			t, err := template.New("layout.html").Funcs(fm).ParseFS(templateFS, "templates/layout.html", "templates/partials.html", p)
			if err != nil {
				return err
			}
			h.tpl[lang][name] = t
		}
	}
	return nil
}

// Routes mounts the web endpoints.
func (h *Handler) Routes(mux *http.ServeMux) {
	static, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", cacheStatic(http.FileServerFS(static))))
	mux.HandleFunc("GET /branding/logo", h.logo)
	mux.HandleFunc("GET /lang", h.setLang)
	mux.HandleFunc("POST /f/{key}", h.submitForm)
	mux.HandleFunc("OPTIONS /f/{key}", h.formPreflight)
	if h.cfg.UIMode() != config.UIFull {
		mux.HandleFunc("GET /{$}", h.headless)
	}
	if h.cfg.UIMode() == config.UIOff {
		return // no sign-in at all: agents use personal access tokens
	}
	// Sign-in and the agents' OAuth consent: every mode with a UI.
	mux.HandleFunc("GET /login", h.loginPage)
	mux.HandleFunc("GET /auth/{provider}", h.authStart)
	mux.HandleFunc("GET /auth/{provider}/callback", h.authCallback)
	mux.HandleFunc("POST /auth/dev", h.devLogin)
	mux.HandleFunc("POST /logout", h.session(h.logout))
	mux.HandleFunc("GET /oauth/authorize", h.authorize)
	mux.HandleFunc("POST /oauth/authorize", h.session(h.consent))
	mux.HandleFunc("GET /workspaces/new", h.session(h.newWorkspacePage))
	mux.HandleFunc("POST /workspaces", h.session(h.createWorkspace))
	mux.HandleFunc("POST /workspaces/switch", h.session(h.switchWorkspace))
	mux.HandleFunc("POST /invitations/accept", h.session(h.acceptInvitation))
	mux.HandleFunc("POST /invitations/decline", h.session(h.declineInvitation))
	if h.cfg.UIMode() == config.UIMinimal {
		return
	}
	mux.HandleFunc("GET /{$}", h.home)
	mux.HandleFunc("GET /events", h.session(h.live))
	mux.HandleFunc("GET /events/last", h.member(h.lastChange))
	mux.HandleFunc("GET /board", h.member(h.board))
	mux.HandleFunc("POST /board/move", h.member(h.boardMove))
	mux.HandleFunc("POST /opportunities", h.member(h.createOpportunity))
	mux.HandleFunc("GET /opportunities", h.member(h.opportunities))
	mux.HandleFunc("POST /opportunities/bulk", h.member(h.bulk))
	mux.HandleFunc("GET /opportunities.csv", h.member(h.exportCSV))
	mux.HandleFunc("GET /opportunities/{id}", h.member(h.opportunity))
	mux.HandleFunc("POST /opportunities/{id}", h.member(h.saveOpportunity))
	mux.HandleFunc("POST /opportunities/{id}/move", h.member(h.moveOpportunity))
	mux.HandleFunc("POST /opportunities/{id}/contacts", h.member(h.linkContact))
	mux.HandleFunc("POST /opportunities/{id}/archive", h.member(h.archiveOpportunity))
	mux.HandleFunc("GET /interactions", h.member(h.interactions))
	mux.HandleFunc("GET /companies", h.member(h.companies))
	mux.HandleFunc("POST /companies", h.member(h.saveCompany))
	mux.HandleFunc("GET /companies/{id}", h.member(h.company))
	mux.HandleFunc("POST /companies/{id}", h.member(h.saveCompany))
	mux.HandleFunc("POST /companies/{id}/archive", h.member(h.archiveRecord("company")))
	mux.HandleFunc("GET /contacts", h.member(h.contacts))
	mux.HandleFunc("POST /contacts", h.member(h.saveContact))
	mux.HandleFunc("GET /contacts/{id}", h.member(h.contact))
	mux.HandleFunc("POST /contacts/{id}", h.member(h.saveContact))
	mux.HandleFunc("POST /contacts/{id}/archive", h.member(h.archiveRecord("contact")))
	mux.HandleFunc("GET /dashboard", h.member(h.dashboard))
	mux.HandleFunc("GET /settings", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/settings/agents", http.StatusSeeOther)
	})
	mux.HandleFunc("GET /settings/import", h.member(h.importForm))
	mux.HandleFunc("POST /settings/import", h.member(h.importCSV))
	mux.HandleFunc("POST /settings/export", h.member(h.exportRecords))
	mux.HandleFunc("GET /settings/{section}", h.member(h.settings))
	mux.HandleFunc("POST /settings/{action}", h.member(h.settingsAction))
	mux.HandleFunc("GET /admin", h.session(h.adminPage))
	mux.HandleFunc("POST /admin/{action}", h.session(h.adminAction))
}

// assetVersions maps each static file to a short hash of its content. Pages
// link "/static/app.js?v=<hash>", so a deploy that changes a file changes its
// URL and browsers fetch it again instead of using a cached copy.
var assetVersions = func() map[string]string {
	out := map[string]string{}
	_ = fs.WalkDir(staticFS, "static", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := staticFS.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		out[strings.TrimPrefix(p, "static/")] = hex.EncodeToString(sum[:6])
		return nil
	})
	return out
}()

// asset is the versioned URL of a static file (templates).
func asset(name string) string {
	return "/static/" + name + "?v=" + assetVersions[name]
}

func cacheStatic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		next.ServeHTTP(w, r)
	})
}

// ───────────── sessions ─────────────

func (h *Handler) cookieName() string {
	if h.cfg.SecureCookies() {
		return "__Host-hilo_session"
	}
	return "hilo_session"
}

func (h *Handler) setCookie(w http.ResponseWriter, name, value string, maxAge int) {
	// Secure is on whenever the base URL is https; config only allows plain
	// http for localhost development.
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", MaxAge: maxAge, HttpOnly: true, //nolint:gosec // G124: see above
		Secure: h.cfg.SecureCookies(), SameSite: http.SameSiteLaxMode})
}

// view is what a page handler gets about the signed-in user.
type view struct {
	Sess       identity.Session
	Workspaces []workspaceItem
	Current    *workspaceItem
	Actor      crm.Actor
	Side       string // sidebar layout class from the hilo_side cookie
	AgentCount int    // OAuth agents connected (sidebar)
	TokenCount int    // active personal access tokens (sidebar)
	// Invitations wait for the user's answer: nobody joins a workspace
	// without accepting.
	Invitations []store.PendingInvitationsForEmailRow
}

type workspaceItem struct {
	ID   uuid.UUID
	Name string
	Role string
}

// IsAdmin reports whether the user administers the current workspace.
func (v view) IsAdmin() bool {
	return v.Current != nil && (v.Current.Role == "owner" || v.Current.Role == "admin")
}

// CanWrite reports whether the user may change CRM data here (templates).
func (v view) CanWrite() bool { return v.Current != nil && v.Actor.Can(crm.PermWrite) }

func (v view) caller() identity.Caller {
	c := identity.Caller{UserID: v.Sess.UserID, IsSuperadmin: v.Sess.IsSuperadmin}
	if v.Current != nil {
		c.WorkspaceID, c.Role = v.Current.ID, v.Current.Role
	}
	return c
}

// session requires a signed-in user; POSTs also need a valid CSRF token and a
// same-origin Origin header.
func (h *Handler) session(next func(http.ResponseWriter, *http.Request, view)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(h.cookieName())
		var sess identity.Session
		if err == nil {
			sess, err = h.id.ResolveSession(r.Context(), c.Value)
		}
		if err != nil {
			if r.Method == http.MethodGet {
				http.Redirect(w, r, "/login?return_to="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
				return
			}
			http.Error(w, "sign in required", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet && !h.validPost(r, sess) {
			h.log.Warn("csrf check failed", "path", r.URL.Path, "ip", h.ips.Of(r))
			http.Error(w, "invalid or expired form, reload the page and try again", http.StatusForbidden)
			return
		}
		v, err := h.loadView(r.Context(), sess)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		v.Side = sideClass(r)
		w.Header().Set("Cache-Control", "no-store")
		next(w, r, v)
	}
}

// member additionally requires a current workspace.
func (h *Handler) member(next func(http.ResponseWriter, *http.Request, view)) http.HandlerFunc {
	return h.session(func(w http.ResponseWriter, r *http.Request, v view) {
		if v.Current == nil {
			http.Redirect(w, r, "/workspaces/new", http.StatusSeeOther)
			return
		}
		next(w, r, v)
	})
}

func (h *Handler) validPost(r *http.Request, sess identity.Session) bool {
	if o := r.Header.Get("Origin"); o != "" && o != h.cfg.BaseURL {
		return false
	}
	return secure.Equal(r.PostFormValue("csrf"), sess.CSRFToken)
}

func (h *Handler) loadView(ctx context.Context, sess identity.Session) (view, error) {
	list, err := h.id.Workspaces(ctx, sess.UserID)
	if err != nil {
		return view{}, err
	}
	v := view{Sess: sess}
	if inv, err := h.id.MyInvitations(ctx, sess.Email); err == nil {
		v.Invitations = inv
	}
	for _, w := range list {
		v.Workspaces = append(v.Workspaces, workspaceItem{ID: w.ID, Name: w.Name, Role: w.Role})
	}
	for i := range v.Workspaces {
		if sess.WorkspaceID != nil && v.Workspaces[i].ID == *sess.WorkspaceID {
			v.Current = &v.Workspaces[i]
		}
	}
	if v.Current == nil && len(v.Workspaces) > 0 {
		v.Current = &v.Workspaces[0]
	}
	if v.Current != nil {
		v.Actor = crm.NewActor(sess.UserID, v.Current.ID, v.Current.Role, "session", "web", "")
		c := v.caller()
		if conns, err := h.id.Connections(ctx, c); err == nil {
			v.AgentCount = len(conns)
		}
		if pats, err := h.id.PATs(ctx, c); err == nil {
			v.TokenCount = len(pats)
		}
	}
	return v, nil
}

// ───────────── rendering ─────────────

type page struct {
	Title   string
	Crumb   string // secondary title ("Funnel · Ventas")
	CrumbUI bool   // Crumb is interface text (a settings section), not data: translate it
	Bare    bool   // full-page screens without the sidebar (consent)
	View    *view
	Nav     string
	Flash   string
	Error   string
	Data    any
	Cfg     *config.Config
	Brand   branding.Brand
	Lang    string       // interface language
	Langs   []langOption // for the language selector
	Path    string       // current URL, so the selector comes back here
	JSi18n  template.JS  // the strings app.js shows, in this language (JSON)
}

type langOption struct{ Code, Name string }

var langOptions = func() []langOption {
	out := make([]langOption, 0, len(i18n.Supported))
	for _, l := range i18n.Supported {
		out = append(out, langOption{l, i18n.Names[l]})
	}
	return out
}()

// langCookie holds the language picked in the selector.
const langCookie = "hilo_lang"

// lang is the request's interface language: the selector's choice, else the
// browser's Accept-Language, else English.
func (h *Handler) lang(r *http.Request) string {
	choice := ""
	if c, err := r.Cookie(langCookie); err == nil {
		choice = c.Value
	}
	return i18n.Match(choice, r.Header.Get("Accept-Language"))
}

// setLang stores the selector's choice and goes back to the page it came from.
func (h *Handler) setLang(w http.ResponseWriter, r *http.Request) {
	l := r.URL.Query().Get("l")
	if i18n.Valid(l) {
		http.SetCookie(w, &http.Cookie{Name: langCookie, Value: l, Path: "/", MaxAge: 365 * 24 * 3600, //nolint:gosec // G124: Secure follows APP_BASE_URL (https in production), like every cookie here
			HttpOnly: true, Secure: h.cfg.SecureCookies(), SameSite: http.SameSiteLaxMode})
	}
	next := localPath(r.URL.Query().Get("next"))
	http.Redirect(w, r, next, http.StatusSeeOther) //nolint:gosec // G710: localPath keeps only a same-origin path
}

// localPath returns p when it is a path on this site, "/" otherwise: no
// scheme, no host, no protocol-relative or backslash tricks.
func localPath(p string) string {
	u, err := url.Parse(p)
	if err != nil || u.Scheme != "" || u.Host != "" || !strings.HasPrefix(p, "/") ||
		strings.HasPrefix(p, "//") || strings.Contains(p, "\\") {
		return "/"
	}
	return u.RequestURI()
}

func (h *Handler) render(w http.ResponseWriter, r *http.Request, status int, name string, p page) {
	lang := h.lang(r)
	t, ok := h.tpl[lang][name]
	if !ok {
		h.log.Error("unknown template", "name", name)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	p.Cfg = h.cfg
	p.Brand = h.brand.Get(context.Background())
	p.Lang, p.Langs, p.Path = lang, langOptions, r.URL.RequestURI()
	p.Title, p.Flash, p.Error = i18n.T(lang, p.Title), i18n.T(lang, p.Flash), i18n.T(lang, p.Error)
	if p.CrumbUI {
		p.Crumb = i18n.T(lang, p.Crumb)
	}
	p.JSi18n = jsStrings(lang)
	w.Header().Set("Content-Language", lang)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := t.Execute(w, p); err != nil {
		h.log.Error("render", "template", name, "err", err)
	}
}

// fail renders a safe error page; details go to the log only.
func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	lang := h.lang(r)
	status, msg := http.StatusInternalServerError, i18n.M("Algo salió mal. Probá de nuevo en un momento.")
	var ve *crm.ValidationError
	switch {
	case errors.As(err, &ve):
		status, msg = http.StatusBadRequest, ve.Field+": "+i18n.T(lang, ve.Msg)
	case errors.Is(err, crm.ErrNotFound), errors.Is(err, identity.ErrNotFound):
		status, msg = http.StatusNotFound, i18n.M("No encontrado.")
	case errors.Is(err, crm.ErrForbidden), errors.Is(err, identity.ErrForbidden):
		status, msg = http.StatusForbidden, i18n.M("No tenés permiso para hacer esto.")
	case errors.Is(err, branding.ErrName), errors.Is(err, branding.ErrLogo):
		status, msg = http.StatusBadRequest, err.Error()
	case errors.Is(err, branding.ErrForbidden):
		status, msg = http.StatusForbidden, i18n.M("Solo un superadmin puede cambiar la marca.")
	case errors.Is(err, crm.ErrConflict), errors.Is(err, identity.ErrLastOwner):
		status, msg = http.StatusConflict, err.Error()
	default:
		h.log.Error("web", "path", r.URL.Path, "err", err)
	}
	h.render(w, r, status, "error", page{Title: i18n.M("Error"), Error: msg})
}

// headless is the home page when the UI is minimal or off: where to point an
// agent, and how it authenticates.
func (h *Handler) headless(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, http.StatusOK, "headless", page{Title: i18n.M("Modo headless"), Data: struct {
		MCP   string
		OAuth bool
	}{h.cfg.MCPResource(), h.cfg.UIMode() != config.UIOff}})
}

func (h *Handler) home(w http.ResponseWriter, r *http.Request) {
	h.member(func(w http.ResponseWriter, r *http.Request, _ view) {
		http.Redirect(w, r, "/board", http.StatusSeeOther)
	})(w, r)
}

// sideClass maps the sidebar preference cookie (set by app.js) to a CSS
// class. Only known values are accepted: the cookie is user-controlled.
func sideClass(r *http.Request) string {
	c, err := r.Cookie("hilo_side")
	if err != nil {
		return ""
	}
	if c.Value == "c" {
		return "collapsed"
	}
	if w, err := strconv.Atoi(c.Value); err == nil && w >= 200 && w <= 360 && w%20 == 0 {
		return "sw-" + c.Value
	}
	return ""
}

// safeReturn accepts only same-site absolute paths (no //host, no \). Spaces
// and control characters are refused too: browsers drop tabs and newlines
// from a Location, so "/\t/evil.com" would become "//evil.com".
func safeReturn(s string) string {
	if s == "" || !strings.HasPrefix(s, "/") || strings.HasPrefix(s, "//") || strings.Contains(s, "\\") || len(s) > 4000 ||
		strings.ContainsFunc(s, func(r rune) bool { return r <= ' ' || r == 0x7f || unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return "/"
	}
	return s
}
