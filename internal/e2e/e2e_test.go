// Package e2e runs the whole stack (web + OAuth + MCP behind one origin, as
// Caddy serves it) against a real database, driving it like a browser and an
// MCP client would: sign-in, workspace creation, dynamic client registration,
// consent, PKCE token exchange, MCP tool calls, refresh rotation, and the
// attacks each step must withstand.
package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jrcoronel-moon/hilo-crm/internal/app"
	"github.com/jrcoronel-moon/hilo-crm/internal/config"
	"github.com/jrcoronel-moon/hilo-crm/internal/crm"
	"github.com/jrcoronel-moon/hilo-crm/internal/db"
	"github.com/jrcoronel-moon/hilo-crm/internal/httpx"
	"github.com/jrcoronel-moon/hilo-crm/internal/mcpserver"
	"github.com/jrcoronel-moon/hilo-crm/internal/secure"
	"github.com/jrcoronel-moon/hilo-crm/internal/testdb"
	"github.com/jrcoronel-moon/hilo-crm/internal/web"
)

const redirectURI = "http://127.0.0.1:43123/callback"

type stack struct {
	t    *testing.T
	db   *db.DB
	base string
	web  *http.Client // browser: cookie jar, no auto-redirects
}

func start(t *testing.T) *stack { return startWith(t, nil) }

// startWith starts the stack, letting the test adjust the configuration.
func startWith(t *testing.T, tweak func(*config.Config)) *stack {
	t.Helper()
	d := testdb.New(t)
	log := slog.New(slog.DiscardHandler)
	if os.Getenv("HILO_E2E_LOG") != "" { // debugging: see server-side errors
		log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	ts := httptest.NewUnstartedServer(nil)
	base := "http://" + ts.Listener.Addr().String()
	cfg := &config.Config{BaseURL: base, SessionSecret: []byte(strings.Repeat("s", 40)), DevLogin: true,
		SuperadminEmails: map[string]bool{"root@acme.test": true}}
	if tweak != nil {
		tweak(cfg)
	}
	mux, err := web.Mount(t.Context(), cfg, d, log)
	if err != nil {
		t.Fatal(err)
	}
	mux.Handle("/mcp", mcpserver.New(cfg, d, crm.New(d, log), log).Handler())
	ts.Config.Handler = app.Middleware(app.Env{Cfg: cfg, DB: d, Log: log, IPs: httpx.NewClientIP(nil)}, mux)
	ts.Start()
	t.Cleanup(ts.Close)
	jar, _ := cookiejar.New(nil)
	return &stack{t: t, db: d, base: base, web: &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (s *stack) do(method, path string, form url.Values, hdr ...string) (*http.Response, string) {
	s.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	u := path
	if strings.HasPrefix(path, "/") {
		u = s.base + path
	}
	req, _ := http.NewRequestWithContext(context.Background(), method, u, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		if hdr[i] == "Host" {
			req.Host = hdr[i+1]
			continue
		}
		req.Header.Set(hdr[i], hdr[i+1])
	}
	res, err := s.web.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	return res, string(b)
}

var (
	csrfRe = regexp.MustCompile(`name="csrf" value="([^"]+)"`)
	reqRe  = regexp.MustCompile(`name="req" value="([^"]+)"`)
	wsRe   = regexp.MustCompile(`name="workspace"[^>]*><option value="([0-9a-f-]{36})"`)
	hrefRe = regexp.MustCompile(`data-navigate href="([^"]+)"`)
)

func find(t *testing.T, re *regexp.Regexp, body string) string {
	t.Helper()
	m := re.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("pattern %s not found in:\n%s", re, body)
	}
	return strings.ReplaceAll(m[1], "&amp;", "&")
}

var loginCSRFRe = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

func (s *stack) devLogin(email, returnTo string) (*http.Response, string) {
	s.t.Helper()
	_, page := s.do("GET", "/login", nil)
	return s.do("POST", "/auth/dev", url.Values{"email": {email}, "return_to": {returnTo}, "csrf": {find(s.t, loginCSRFRe, page)}})
}

// signIn logs in as the superadmin with the dev provider and creates a
// workspace; returns the CSRF token.
func (s *stack) signIn() string {
	s.t.Helper()
	res, _ := s.devLogin("root@acme.test", "/")
	if res.StatusCode != http.StatusSeeOther || strings.Contains(res.Header.Get("Location"), "error") {
		s.t.Fatalf("dev login: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	_, page := s.do("GET", "/workspaces/new", nil)
	csrf := find(s.t, csrfRe, page)
	if res, _ = s.do("POST", "/workspaces", url.Values{"csrf": {csrf}, "name": {"Acme"}}); res.StatusCode != http.StatusSeeOther {
		s.t.Fatalf("create workspace: %d", res.StatusCode)
	}
	return csrf
}

type tokens struct {
	Access  string `json:"access_token"`
	Refresh string `json:"refresh_token"`
	Scope   string `json:"scope"`
	Error   string `json:"error"`
}

func (s *stack) token(form url.Values) (int, tokens) {
	s.t.Helper()
	res, body := s.do("POST", "/oauth/token", form)
	var tk tokens
	_ = json.Unmarshal([]byte(body), &tk)
	return res.StatusCode, tk
}

func mcpSession(t *testing.T, base, tok string) (*mcp.ClientSession, error) {
	c := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "1"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	hc := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.Header.Set("Authorization", "Bearer "+tok)
		return http.DefaultTransport.RoundTrip(r)
	})}
	cs, err := c.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: base + "/mcp", HTTPClient: hc, MaxRetries: -1}, nil)
	if err == nil {
		t.Cleanup(func() { _ = cs.Close() })
	}
	return cs, err
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOAuthAgentFlow(t *testing.T) {
	s := start(t)

	// 1. Discovery, exactly as an MCP client does it.
	res, _ := s.do("POST", "/mcp", nil)
	if res.StatusCode != http.StatusUnauthorized || !strings.Contains(res.Header.Get("WWW-Authenticate"), "/.well-known/oauth-protected-resource/mcp") {
		t.Fatalf("challenge: %d %v", res.StatusCode, res.Header)
	}
	_, body := s.do("GET", "/.well-known/oauth-protected-resource/mcp", nil)
	if !strings.Contains(body, `"resource":"`+s.base+`/mcp"`) {
		t.Fatalf("protected resource metadata: %s", body)
	}
	_, body = s.do("GET", "/.well-known/oauth-authorization-server", nil)
	if !strings.Contains(body, `"code_challenge_methods_supported":["S256"]`) || !strings.Contains(body, `"client_id_metadata_document_supported":true`) {
		t.Fatalf("server metadata: %s", body)
	}

	// 2. Dynamic client registration (public client) — and a rejected one.
	reg := func(uris ...string) (int, map[string]any) {
		b, _ := json.Marshal(map[string]any{"client_name": "Test Agent", "redirect_uris": uris, "token_endpoint_auth_method": "none"})
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, s.base+"/oauth/register", strings.NewReader(string(b)))
		req.Header.Set("Content-Type", "application/json")
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = r.Body.Close() }()
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		return r.StatusCode, m
	}
	for _, bad := range []string{"javascript:alert(1)", "http://evil.example/cb", "https://ok.example/cb#frag", "data:text/html,x"} {
		if code, _ := reg(bad); code != http.StatusBadRequest {
			t.Fatalf("redirect_uri %q accepted (%d)", bad, code)
		}
	}
	code, client := reg("http://127.0.0.1/callback")
	if code != http.StatusCreated || client["client_secret"] != nil {
		t.Fatalf("register: %d %v", code, client)
	}
	clientID := client["client_id"].(string)

	// 3. Sign in and authorize. Unregistered redirect URIs are never followed.
	s.signIn()
	verifier := secure.Token("", 40)
	authz := url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {redirectURI},
		"code_challenge": {secure.PKCES256(verifier)}, "code_challenge_method": {"S256"}, "state": {"xyz"},
		"resource": {s.base + "/mcp"}}
	bad := url.Values{}
	for k, v := range authz {
		bad[k] = v
	}
	bad.Set("redirect_uri", "http://127.0.0.1:43123/other")
	if res, _ = s.do("GET", "/oauth/authorize?"+bad.Encode(), nil); res.StatusCode != http.StatusBadRequest || res.Header.Get("Location") != "" {
		t.Fatalf("unregistered redirect_uri: %d → %q", res.StatusCode, res.Header.Get("Location"))
	}
	bad = url.Values{}
	for k, v := range authz {
		bad[k] = v
	}
	bad.Set("code_challenge_method", "plain")
	if res, _ = s.do("GET", "/oauth/authorize?"+bad.Encode(), nil); !strings.Contains(res.Header.Get("Location"), "error=invalid_request") {
		t.Fatalf("plain PKCE should be refused via redirect: %d %q", res.StatusCode, res.Header.Get("Location"))
	}
	res, page := s.do("GET", "/oauth/authorize?"+authz.Encode(), nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(page, "Test Agent") || !strings.Contains(page, "no está verificado") {
		t.Fatalf("consent page: %d\n%s", res.StatusCode, page)
	}
	csrf, blob, ws := find(t, csrfRe, page), find(t, reqRe, page), find(t, wsRe, page)

	// Consent without CSRF token, or from another origin, is refused.
	if res, _ = s.do("POST", "/oauth/authorize", url.Values{"req": {blob}, "workspace": {ws}, "decision": {"approve"}}); res.StatusCode != http.StatusForbidden {
		t.Fatalf("consent without csrf: %d", res.StatusCode)
	}
	if res, _ = s.do("POST", "/oauth/authorize", url.Values{"csrf": {csrf}, "req": {blob}, "workspace": {ws}, "decision": {"approve"}}, "Origin", "https://evil.example"); res.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin consent: %d", res.StatusCode)
	}
	// Tampered request blob.
	if res, _ = s.do("POST", "/oauth/authorize", url.Values{"csrf": {csrf}, "req": {"x" + blob}, "workspace": {ws}, "decision": {"approve"}}); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("tampered blob: %d", res.StatusCode)
	}
	_, page = s.do("POST", "/oauth/authorize", url.Values{"csrf": {csrf}, "req": {blob}, "workspace": {ws}, "decision": {"approve"}})
	back, _ := url.Parse(find(t, hrefRe, page))
	authCode := back.Query().Get("code")
	if back.Query().Get("state") != "xyz" || back.Query().Get("iss") != s.base || authCode == "" || !strings.HasPrefix(back.String(), redirectURI) {
		t.Fatalf("hand-back URL: %s", back)
	}

	// 4. Token exchange: wrong verifier fails; right one works once.
	exchange := url.Values{"grant_type": {"authorization_code"}, "client_id": {clientID}, "code": {authCode}, "redirect_uri": {redirectURI}, "code_verifier": {secure.Token("", 40)}}
	if st, tk := s.token(exchange); st != http.StatusBadRequest || tk.Error != "invalid_grant" {
		t.Fatalf("wrong verifier: %d %+v", st, tk)
	}
	// The failed attempt consumed the code (single use, RFC 6749 §4.1.2), so
	// authorize again for the real exchange.
	_, page = s.do("GET", "/oauth/authorize?"+authz.Encode(), nil)
	_, page = s.do("POST", "/oauth/authorize", url.Values{"csrf": {csrf}, "req": {find(t, reqRe, page)}, "workspace": {ws}, "decision": {"approve"}})
	back, _ = url.Parse(find(t, hrefRe, page))
	exchange.Set("code", back.Query().Get("code"))
	exchange.Set("code_verifier", verifier)
	st, tk := s.token(exchange)
	if st != http.StatusOK || !strings.HasPrefix(tk.Access, "hilo_at_") || !strings.HasPrefix(tk.Refresh, "hilo_rt_") {
		t.Fatalf("token exchange: %d %+v", st, tk)
	}

	// 5. The agent works through MCP.
	cs, err := mcpSession(t, s.base, tk.Access)
	if err != nil {
		t.Fatal(err)
	}
	out, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_pipelines", Arguments: map[string]any{}})
	if err != nil || out.IsError || !strings.Contains(out.Content[0].(*mcp.TextContent).Text, "Negociación") {
		t.Fatalf("list_pipelines: %v %+v", err, out)
	}

	// 6. Refresh rotates the pair.
	st, tk2 := s.token(url.Values{"grant_type": {"refresh_token"}, "client_id": {clientID}, "refresh_token": {tk.Refresh}})
	if st != http.StatusOK || tk2.Access == tk.Access || tk2.Refresh == tk.Refresh {
		t.Fatalf("refresh: %d %+v", st, tk2)
	}
	// Refresh may narrow but never widen the scope.
	if st, _ = s.token(url.Values{"grant_type": {"refresh_token"}, "client_id": {clientID}, "refresh_token": {tk2.Refresh}, "scope": {"crm:read"}}); st != http.StatusOK {
		t.Fatalf("narrowing refresh: %d", st)
	}

	// 7. Replaying the redeemed code revokes everything minted from it.
	if st, _ = s.token(exchange); st != http.StatusBadRequest {
		t.Fatalf("code replay accepted: %d", st)
	}
	if _, err := mcpSession(t, s.base, tk2.Access); err == nil {
		t.Fatal("token family should be revoked after code replay")
	}
}

func TestWebSecurity(t *testing.T) {
	s := start(t)

	// Through a tunnel (public Host or tunnel headers) the dev login does not
	// exist: no form on the page, 404 on the endpoint.
	_, page := s.do("GET", "/login", nil)
	tok := find(t, loginCSRFRe, page)
	for _, h := range [][]string{{"Host", "keys-kick.trycloudflare.com"}, {"Cf-Connecting-Ip", "203.0.113.7"}, {"X-Forwarded-Host", "evil.example"}} {
		if _, p := s.do("GET", "/login", nil, h...); strings.Contains(p, `action="/auth/dev"`) {
			t.Fatalf("dev form shown via %v", h)
		}
		if res, _ := s.do("POST", "/auth/dev", url.Values{"email": {"root@acme.test"}, "csrf": {tok}}, h...); res.StatusCode != http.StatusNotFound {
			t.Fatalf("dev login via %v: %d", h, res.StatusCode)
		}
	}
	// Login CSRF: the dev form needs the pre-login token.
	if res, _ := s.do("POST", "/auth/dev", url.Values{"email": {"root@acme.test"}}); res.StatusCode != http.StatusForbidden {
		t.Fatalf("dev login without token: %d", res.StatusCode)
	}
	// Login policy: unknown domains are refused (closed by default).
	res, _ := s.devLogin("mallory@evil.test", "")
	if !strings.Contains(res.Header.Get("Location"), "error=not_allowed") {
		t.Fatalf("unlisted domain signed in: %s", res.Header.Get("Location"))
	}
	// Open redirect via return_to is neutralised.
	for _, rt := range []string{"//evil.example", "https://evil.example", "/\\evil.example"} {
		res, _ = s.devLogin("root@acme.test", rt)
		if loc := res.Header.Get("Location"); loc != "/" {
			t.Fatalf("return_to %q redirected to %q", rt, loc)
		}
	}
	csrf := s.signIn()

	// Security headers on every page.
	res, page = s.do("GET", "/board", nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(page, "Negociación") {
		t.Fatalf("board: %d", res.StatusCode)
	}
	csp := res.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "unsafe-inline") || res.Header.Get("X-Frame-Options") != "DENY" {
		t.Fatalf("headers: %v", res.Header)
	}
	if strings.Contains(page, "<script>") || strings.Contains(page, " style=") || strings.Contains(page, "onclick") {
		t.Fatal("inline script or style in page")
	}
	// Settings writes need the CSRF token.
	if res, _ = s.do("POST", "/settings/channel", url.Values{"name": {"X"}, "kind": {"web"}}); res.StatusCode != http.StatusForbidden {
		t.Fatalf("settings without csrf: %d", res.StatusCode)
	}
	// Stored XSS is escaped: a channel name with markup renders inert.
	if res, _ = s.do("POST", "/settings/channel", url.Values{"csrf": {csrf}, "name": {`<img src=x onerror=alert(1)>`}, "kind": {"web"}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("create channel: %d", res.StatusCode)
	}
	_, page = s.do("GET", "/settings/channels", nil)
	if strings.Contains(page, "<img src=x") || !strings.Contains(page, "&lt;img src=x onerror=alert(1)&gt;") {
		t.Fatal("channel name not escaped")
	}
	// Dashboard renders.
	if res, _ = s.do("GET", "/dashboard?days=30", nil); res.StatusCode != http.StatusOK {
		t.Fatalf("dashboard: %d", res.StatusCode)
	}
	// PAT is shown once and works against MCP.
	_, page = s.do("POST", "/settings/token", url.Values{"csrf": {csrf}, "name": {"grok"}, "days": {"30"}})
	pat := regexp.MustCompile(`hilo_pat_[A-Za-z0-9_-]{43}`).FindString(page)
	if pat == "" {
		t.Fatal("new PAT not shown")
	}
	if _, err := mcpSession(t, s.base, pat); err != nil {
		t.Fatalf("PAT against MCP: %v", err)
	}
	_, page = s.do("GET", "/settings/agents", nil)
	if strings.Contains(page, pat) {
		t.Fatal("PAT shown again after creation")
	}
	// Logout kills the session.
	_, _ = s.do("POST", "/logout", url.Values{"csrf": {csrf}})
	if res, _ = s.do("GET", "/board", nil); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("board after logout: %d", res.StatusCode)
	}
}

func TestLeadFormEndpoint(t *testing.T) {
	s := start(t)
	csrf := s.signIn()
	res, _ := s.do("POST", "/settings/form", url.Values{"csrf": {csrf}, "name": {"Web"}, "channel": {""}, "origins": {"https://www.acme.test"}})
	if res.StatusCode != http.StatusBadRequest && res.StatusCode != http.StatusNotFound {
		t.Fatalf("form with empty channel should fail: %d", res.StatusCode)
	}
	_, page := s.do("GET", "/settings/forms", nil)
	ch := regexp.MustCompile(`<option value="([0-9a-f-]{36})" selected>Formulario</option>`).FindStringSubmatch(page)
	if ch == nil {
		t.Fatal("form channel not found")
	}
	if res, _ = s.do("POST", "/settings/form", url.Values{"csrf": {csrf}, "name": {"Web"}, "channel": {ch[1]}, "origins": {"https://www.acme.test"}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("create form: %d", res.StatusCode)
	}
	_, page = s.do("GET", "/settings/forms", nil)
	key := regexp.MustCompile(`/f/(lf_[A-Za-z0-9_-]+)`).FindStringSubmatch(page)[1]

	anon := &http.Client{}
	post := func(origin string, v url.Values) int {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, s.base+"/f/"+key, strings.NewReader(v.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		r, err := anon.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = r.Body.Close()
		return r.StatusCode
	}
	lead := url.Values{"email": {"ana@cliente.test"}, "name": {"Ana Pérez"}, "company": {"Cliente SA"}, "message": {"Hola"}, "presupuesto": {"10k"}}
	if c := post("https://evil.example", lead); c != http.StatusForbidden {
		t.Fatalf("disallowed origin: %d", c)
	}
	if c := post("https://www.acme.test", lead); c != http.StatusOK {
		t.Fatalf("submission: %d", c)
	}
	if c := post("", url.Values{"email": {"not-an-email"}}); c != http.StatusBadRequest {
		t.Fatalf("invalid email: %d", c)
	}
	if c := post("", url.Values{"email": {"bot@spam.test"}, "website": {"http://spam"}}); c != http.StatusOK {
		t.Fatalf("honeypot should look like success: %d", c)
	}
	_, page = s.do("GET", "/board", nil)
	if !strings.Contains(page, "Lead: Cliente SA · Ana Pérez") || strings.Contains(page, "spam.test") {
		t.Fatal("lead not on the board (or the honeypot one is)")
	}
	if c := post("", url.Values{"email": {"x@y.test"}}); c != http.StatusOK {
		t.Fatalf("no-origin post (plain HTML form): %d", c)
	}
	for range 20 {
		post("", url.Values{"email": {"flood@y.test"}})
	}
	if c := post("", url.Values{"email": {"flood@y.test"}}); c != http.StatusTooManyRequests {
		t.Fatalf("rate limit not enforced: %d", c)
	}
}

// connectAgent runs registration + consent + exchange and returns tokens.
func (s *stack) connectAgent(csrf string) (string, tokens) {
	s.t.Helper()
	b := `{"client_name":"Agent","redirect_uris":["` + redirectURI + `"],"token_endpoint_auth_method":"none"}`
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, s.base+"/oauth/register", strings.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	var c struct {
		ClientID string `json:"client_id"`
	}
	_ = json.NewDecoder(res.Body).Decode(&c)
	_ = res.Body.Close()
	verifier := secure.Token("", 40)
	q := url.Values{"response_type": {"code"}, "client_id": {c.ClientID}, "redirect_uri": {redirectURI},
		"code_challenge": {secure.PKCES256(verifier)}, "code_challenge_method": {"S256"}}
	_, page := s.do("GET", "/oauth/authorize?"+q.Encode(), nil)
	_, page = s.do("POST", "/oauth/authorize", url.Values{"csrf": {csrf}, "req": {find(s.t, reqRe, page)}, "workspace": {find(s.t, wsRe, page)}, "decision": {"approve"}})
	back, _ := url.Parse(find(s.t, hrefRe, page))
	st, tk := s.token(url.Values{"grant_type": {"authorization_code"}, "client_id": {c.ClientID}, "code": {back.Query().Get("code")},
		"redirect_uri": {redirectURI}, "code_verifier": {verifier}})
	if st != http.StatusOK {
		s.t.Fatalf("exchange: %d %+v", st, tk)
	}
	return c.ClientID, tk
}

func TestRefreshReuseRevokesFamily(t *testing.T) {
	s := start(t)
	clientID, tk := s.connectAgent(s.signIn())
	st, tk2 := s.token(url.Values{"grant_type": {"refresh_token"}, "client_id": {clientID}, "refresh_token": {tk.Refresh}})
	if st != http.StatusOK {
		t.Fatal(st)
	}
	// Within the grace window the old refresh still works (parallel tool calls).
	if st, _ = s.token(url.Values{"grant_type": {"refresh_token"}, "client_id": {clientID}, "refresh_token": {tk.Refresh}}); st != http.StatusOK {
		t.Fatalf("grace window: %d", st)
	}
	// Age the rotation past the grace window: the old token is now a replay.
	if _, err := s.db.Pool.Exec(context.Background(), "UPDATE oauth_tokens SET refresh_used_at = now() - interval '1 hour' WHERE refresh_hash = $1", secure.Hash(tk.Refresh)); err != nil {
		t.Fatal(err)
	}
	if st, _ = s.token(url.Values{"grant_type": {"refresh_token"}, "client_id": {clientID}, "refresh_token": {tk.Refresh}}); st != http.StatusBadRequest {
		t.Fatalf("replayed refresh accepted: %d", st)
	}
	if _, err := mcpSession(t, s.base, tk2.Access); err == nil {
		t.Fatal("tokens of a replayed family must be revoked")
	}
	// Refresh with another client's id fails.
	cid2, tk3 := s.connectAgent(find(t, csrfRe, func() string { _, p := s.do("GET", "/settings/agents", nil); return p }()))
	_ = cid2
	if st, _ = s.token(url.Values{"grant_type": {"refresh_token"}, "client_id": {clientID}, "refresh_token": {tk3.Refresh}}); st != http.StatusBadRequest {
		t.Fatalf("cross-client refresh: %d", st)
	}
	// RFC 7009 revocation.
	res, _ := s.do("POST", "/oauth/revoke", url.Values{"client_id": {cid2}, "token": {tk3.Refresh}})
	if res.StatusCode != http.StatusOK {
		t.Fatal(res.StatusCode)
	}
	if _, err := mcpSession(t, s.base, tk3.Access); err == nil {
		t.Fatal("revoked token still works")
	}
}

func TestSettingsAndAdmin(t *testing.T) {
	s := start(t)
	csrf := s.signIn()
	post := func(action string, v url.Values) int {
		v.Set("csrf", csrf)
		res, _ := s.do("POST", action, v)
		return res.StatusCode
	}
	// Superadmin page and platform domains.
	if c := post("/admin/domain", url.Values{"domain": {"acme.test"}}); c != http.StatusSeeOther {
		t.Fatalf("admin domain: %d", c)
	}
	_, page := s.do("GET", "/admin", nil)
	if !strings.Contains(page, "<code>acme.test</code>") {
		t.Fatal("platform domain not listed")
	}
	// Invite a member and give them a role.
	if c := post("/settings/invite", url.Values{"email": {"ana@acme.test"}, "role": {"member"}}); c != http.StatusSeeOther {
		t.Fatalf("invite: %d", c)
	}
	if c := post("/settings/domain", url.Values{"domain": {"acme.test"}, "role": {"viewer"}}); c != http.StatusSeeOther {
		t.Fatalf("workspace domain: %d", c)
	}
	// Pipeline editor: rename a stage, add one, delete one with reassignment.
	_, page = s.do("GET", "/settings/pipelines", nil)
	pid := regexp.MustCompile(`name="id" value="([0-9a-f-]{36})">\s*<div class="row"><label class="field grow">Nombre<input name="name" value="Ventas"`).FindStringSubmatch(page)
	stages := regexp.MustCompile(`name="stage_id" value="([0-9a-f-]{36})">\s*<input type="text" name="stage_name" value="([^"]+)"`).FindAllStringSubmatch(page, -1)
	if pid == nil || len(stages) != 6 {
		t.Fatalf("pipeline editor not rendered (%d stages)", len(stages))
	}
	// Put an opportunity in "Calificado" so deleting it needs a reassignment.
	_, tk := s.connectAgent(csrf)
	cs, err := mcpSession(t, s.base, tk.Access)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "upsert_opportunity", Arguments: map[string]any{"title": "X", "stage": "Calificado"}}); err != nil || out.IsError {
		t.Fatalf("seed: %v", err)
	}
	form := url.Values{"id": {pid[1]}, "name": {"Ventas B2B"}, "is_default": {"1"}}
	cats := []string{"initial", "in_progress", "in_progress", "in_progress", "won", "lost"}
	for i, st := range stages {
		name := st[2]
		if name == "Calificado" {
			name = "" // delete
		}
		if name == "Lead" {
			name = "Prospecto"
		}
		form.Add("stage_id", st[1])
		form.Add("stage_name", name)
		form.Add("stage_category", cats[i])
		form.Add("stage_probability", "")
		form.Add("stage_position", fmt.Sprint(i))
	}
	form.Add("stage_id", "")
	form.Add("stage_name", "Demo")
	form.Add("stage_category", "in_progress")
	form.Add("stage_probability", "40")
	form.Add("stage_position", "1")
	if c := post("/settings/pipeline", form); c != http.StatusBadRequest {
		t.Fatalf("deleting a stage with opportunities must require reassign: %d", c)
	}
	form.Set("reassign_"+stages[1][1], "Demo")
	if c := post("/settings/pipeline", form); c != http.StatusSeeOther {
		t.Fatalf("pipeline save: %d", c)
	}
	_, page = s.do("GET", "/board", nil)
	if !strings.Contains(page, "Ventas B2B") || !strings.Contains(page, "Prospecto") || strings.Contains(page, "Calificado") {
		t.Fatal("pipeline changes not on the board")
	}
	out, _ := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_opportunities", Arguments: map[string]any{"stage": "Demo"}})
	if !strings.Contains(out.Content[0].(*mcp.TextContent).Text, `"title":"X"`) {
		t.Fatal("opportunity not reassigned to Demo")
	}
	// The connected agent shows up and can be revoked from the web.
	_, page = s.do("GET", "/settings/agents", nil)
	client := regexp.MustCompile(`name="client" value="([^"]+)"`).FindStringSubmatch(page)
	if client == nil {
		t.Fatal("connected agent not listed")
	}
	if c := post("/settings/connection-revoke", url.Values{"client": {client[1]}}); c != http.StatusSeeOther {
		t.Fatalf("revoke: %d", c)
	}
	if _, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_pipelines", Arguments: map[string]any{}}); err == nil {
		t.Fatal("revoked agent still works")
	}
	// A second workspace and switching between them.
	if c := post("/workspaces", url.Values{"name": {"Otra"}}); c != http.StatusSeeOther {
		t.Fatalf("second workspace: %d", c)
	}
	_, page = s.do("GET", "/board", nil)
	if !strings.Contains(page, `name="workspace" data-autosubmit`) {
		t.Fatal("workspace switcher missing")
	}
	// A non-superadmin cannot reach /admin.
	if c := post("/logout", url.Values{}); c != http.StatusSeeOther {
		t.Fatal(c)
	}
	res, _ := s.devLogin("ana@acme.test", "")
	if strings.Contains(res.Header.Get("Location"), "error") {
		t.Fatalf("invited member login: %s", res.Header.Get("Location"))
	}
	if res, _ = s.do("GET", "/admin", nil); res.StatusCode != http.StatusForbidden {
		t.Fatalf("member reached /admin: %d", res.StatusCode)
	}
	_, page = s.do("GET", "/settings/pipelines", nil)
	if strings.Contains(page, `action="/settings/pipeline"`) || strings.Contains(page, `href="/settings/audit"`) {
		t.Fatal("member sees admin controls")
	}
}

func TestBoardDragAndDrop(t *testing.T) {
	s := start(t)
	csrf := s.signIn()
	_, tk := s.connectAgent(csrf)
	cs, err := mcpSession(t, s.base, tk.Access)
	if err != nil {
		t.Fatal(err)
	}
	out, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "upsert_opportunity", Arguments: map[string]any{"title": "Arrastrable"}})
	if err != nil || out.IsError {
		t.Fatalf("seed: %v", err)
	}
	var created struct {
		Record crm.OpportunityView `json:"record"`
	}
	_ = json.Unmarshal([]byte(out.Content[0].(*mcp.TextContent).Text), &created)
	_, page := s.do("GET", "/board", nil)
	stage := func(name string) string {
		m := regexp.MustCompile(`data-stage="([0-9a-f-]{36})" data-stage-name="` + name + `"`).FindStringSubmatch(page)
		if m == nil {
			t.Fatalf("column %s not on the board", name)
		}
		return m[1]
	}
	move := func(stageID, reason string, withCSRF bool) (int, string) {
		v := url.Values{"opportunity": {created.Record.ID.String()}, "stage": {stageID}, "reason": {reason}}
		if withCSRF {
			v.Set("csrf", csrf)
		}
		res, body := s.do("POST", "/board/move", v, "Accept", "application/json")
		return res.StatusCode, body
	}
	if c, _ := move(stage("Propuesta"), "", false); c != http.StatusForbidden {
		t.Fatalf("move without csrf: %d", c)
	}
	if c, body := move(stage("Propuesta"), "", true); c != http.StatusOK || !strings.Contains(body, `"stage":"Propuesta"`) {
		t.Fatalf("move: %d %s", c, body)
	}
	if c, body := move(stage("Perdido"), "", true); c != http.StatusBadRequest || !strings.Contains(body, "lost_reason") {
		t.Fatalf("lost without reason: %d %s", c, body)
	}
	if c, body := move(stage("Perdido"), "Eligió a la competencia", true); c != http.StatusOK || !strings.Contains(body, `"status":"lost"`) {
		t.Fatalf("lost with reason: %d %s", c, body)
	}
	tl, _ := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_timeline", Arguments: map[string]any{"opportunity": created.Record.ID.String()}})
	if txt := tl.Content[0].(*mcp.TextContent).Text; !strings.Contains(txt, "Eligió a la competencia") || !strings.Contains(txt, "Propuesta") {
		t.Fatalf("web moves missing from history: %s", txt)
	}
	// A viewer cannot move cards.
	if c := func() int {
		v := url.Values{"csrf": {csrf}, "email": {"lector@acme.test"}, "role": {"viewer"}}
		res, _ := s.do("POST", "/settings/invite", v)
		return res.StatusCode
	}(); c != http.StatusSeeOther {
		t.Fatalf("invite viewer: %d", c)
	}
	_, _ = s.do("POST", "/logout", url.Values{"csrf": {csrf}})
	res, _ := s.devLogin("lector@acme.test", "/")
	if strings.Contains(res.Header.Get("Location"), "error") {
		t.Fatalf("viewer login: %s", res.Header.Get("Location"))
	}
	_, page = s.do("GET", "/board", nil)
	viewerCSRF := find(t, csrfRe, page)
	v := url.Values{"csrf": {viewerCSRF}, "opportunity": {created.Record.ID.String()}, "stage": {stage("Lead")}}
	if res, _ := s.do("POST", "/board/move", v); res.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer moved a card: %d", res.StatusCode)
	}
}

func TestWebDataFeatures(t *testing.T) {
	s := start(t)
	csrf := s.signIn()
	post := func(path string, v url.Values) (*http.Response, string) {
		v.Set("csrf", csrf)
		return s.do("POST", path, v)
	}
	// New opportunity from the web, with a company created by name.
	_, page := s.do("GET", "/board", nil)
	pipe := regexp.MustCompile(`name="pipeline" value="([0-9a-f-]{36})"`).FindStringSubmatch(page)[1]
	lead := regexp.MustCompile(`data-stage="([0-9a-f-]{36})" data-stage-name="Lead"`).FindStringSubmatch(page)[1]
	res, _ := post("/opportunities", url.Values{"pipeline": {pipe}, "title": {`=HYPERLINK("http://evil","x")`}, "company": {"Globex"},
		"amount": {"1.500,50"}, "currency": {"usd"}, "stage": {lead}, "channel": {"Web"}, "tags": {"pyme, inbound"}, "next_step": {"Llamar"}})
	if res.StatusCode != http.StatusSeeOther || !strings.Contains(res.Header.Get("Location"), "ok=created") {
		t.Fatalf("create from web: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if res, _ = post("/opportunities", url.Values{"pipeline": {pipe}, "title": {"Otra"}, "company": {"Globex"}, "amount": {"abc"}}); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad amount accepted: %d", res.StatusCode)
	}
	post("/opportunities", url.Values{"pipeline": {pipe}, "title": {"Segunda"}, "company": {"globex"}, "amount": {"10"}})
	_, page = s.do("GET", "/board?q=globex", nil)
	if !strings.Contains(page, "Segunda") || strings.Count(page, "<span class=\"co\">Globex</span>") != 2 {
		t.Fatalf("company should be reused by name and the search should find both:\n%s", page)
	}
	if _, page = s.do("GET", "/board?tag=pyme&open=1", nil); !strings.Contains(page, "HYPERLINK") || strings.Contains(page, "Segunda") || !strings.Contains(page, "Tag: pyme") {
		t.Fatal("tag filter / chip")
	}

	// List + bulk: tag and move both, then a lost without reason is refused per row.
	_, page = s.do("GET", "/opportunities", nil)
	ids := regexp.MustCompile(`name="id" value="([0-9a-f-]{36})"`).FindAllStringSubmatch(page, -1)
	if len(ids) != 2 {
		t.Fatalf("list rows: %d", len(ids))
	}
	prop := regexp.MustCompile(`<option value="([0-9a-f-]{36})">Propuesta</option>`).FindStringSubmatch(page)[1]
	_, page = post("/opportunities/bulk", url.Values{"id": {ids[0][1], ids[1][1]}, "action": {"move"}, "stage": {prop}})
	if !strings.Contains(page, "2 oportunidades actualizadas") {
		t.Fatal("bulk move summary missing")
	}
	_, page = post("/opportunities/bulk", url.Values{"id": {ids[0][1]}, "action": {"lost"}})
	if !strings.Contains(page, "No se pudieron cambiar") {
		t.Fatal("bulk lost without reason should report the failure")
	}

	// CSV export neutralises formula injection.
	res, body := s.do("GET", "/opportunities.csv", nil)
	if res.Header.Get("Content-Type") != "text/csv; charset=utf-8" || !strings.Contains(body, `'=HYPERLINK`) || strings.Contains(body, "\n=HYPERLINK") {
		t.Fatalf("csv export: %s\n%s", res.Header.Get("Content-Type"), body)
	}

	// Interactions page and dashboard with a monthly goal set from the editor.
	if res, page = s.do("GET", "/interactions", nil); res.StatusCode != http.StatusOK || !strings.Contains(page, "Sin interacciones") {
		t.Fatalf("interactions page: %d", res.StatusCode)
	}
	_, page = s.do("GET", "/settings/pipelines", nil)
	form := url.Values{"id": {regexp.MustCompile(`name="id" value="([0-9a-f-]{36})"`).FindStringSubmatch(page)[1]}, "name": {"Ventas"}, "is_default": {"1"},
		"monthly_target": {"50000"}, "target_currency": {"USD"}}
	for _, m := range regexp.MustCompile(`name="stage_id" value="([0-9a-f-]{36})">\s*<input type="text" name="stage_name" value="([^"]+)"`).FindAllStringSubmatch(page, -1) {
		form.Add("stage_id", m[1])
		form.Add("stage_name", m[2])
	}
	for _, c := range []string{"initial", "in_progress", "in_progress", "in_progress", "won", "lost"} {
		form.Add("stage_category", c)
		form.Add("stage_probability", "")
	}
	if res, _ = post("/settings/pipeline", form); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("save goal: %d", res.StatusCode)
	}
	form.Set("monthly_target", "mucho")
	if res, _ = post("/settings/pipeline", form); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("non-numeric goal: %d", res.StatusCode)
	}
	_, page = s.do("GET", "/dashboard", nil)
	if !strings.Contains(page, "USD 50.000 restantes") || !strings.Contains(page, "Actividad por agente") || !strings.Contains(page, "Monto ganado por mes") {
		t.Fatal("dashboard goal / activity / monthly chart missing")
	}
}

func TestCompaniesAndContactsPages(t *testing.T) {
	s := start(t)
	csrf := s.signIn()
	post := func(path string, v url.Values) *http.Response {
		v.Set("csrf", csrf)
		res, _ := s.do("POST", path, v)
		return res
	}
	res := post("/companies", url.Values{"name": {"Globex"}, "domain": {"https://www.globex.com"}})
	if res.StatusCode != http.StatusSeeOther || !strings.Contains(res.Header.Get("Location"), "ok=company") {
		t.Fatalf("create company: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	companyURL := strings.Split(res.Header.Get("Location"), "?")[0]
	res = post("/contacts", url.Values{"first_name": {"Ana"}, "email": {"ana@globex.com"}, "company": {"globex.com"}})
	contactURL := strings.Split(res.Header.Get("Location"), "?")[0]
	if res.StatusCode != http.StatusSeeOther || !strings.HasPrefix(contactURL, "/contacts/") {
		t.Fatalf("create contact: %d", res.StatusCode)
	}
	_, page := s.do("GET", companyURL, nil)
	if !strings.Contains(page, "ana@globex.com") || !strings.Contains(page, `value="globex.com"`) {
		t.Fatal("company page should list its contact and show the normalised domain")
	}
	// Edit: industry, country, tags; an invalid country is rejected.
	edit := url.Values{"name": {"Globex Corp"}, "domain": {"globex.com"}, "industry": {"Energía"}, "country": {"ar"}, "tags": {"vip, energía"}, "size_range": {"51-200"}}
	if res = post(companyURL, edit); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("edit company: %d", res.StatusCode)
	}
	edit.Set("country", "Argentina")
	if res = post(companyURL, edit); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid country accepted: %d", res.StatusCode)
	}
	if _, page = s.do("GET", "/companies?q=corp", nil); !strings.Contains(page, "Globex Corp") || !strings.Contains(page, "Energía") {
		t.Fatal("companies list after edit")
	}
	if _, page = s.do("GET", "/contacts?q=globex", nil); !strings.Contains(page, "Globex Corp") {
		t.Fatal("contacts list should show the company")
	}
	// The contact page shows the opportunities they are on.
	_, page = s.do("GET", "/board", nil)
	pipe := regexp.MustCompile(`name="pipeline" value="([0-9a-f-]{36})"`).FindStringSubmatch(page)[1]
	post("/opportunities", url.Values{"pipeline": {pipe}, "title": {"Planta solar"}, "company": {"Globex Corp"}})
	if _, page = s.do("GET", companyURL, nil); !strings.Contains(page, "Planta solar") {
		t.Fatal("company page should list its opportunities")
	}
	// Archive the contact: gone from the list, still reachable by nobody.
	if res = post(contactURL+"/archive", url.Values{}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("archive contact: %d", res.StatusCode)
	}
	if _, page = s.do("GET", "/contacts", nil); strings.Contains(page, `href="`+contactURL+`"`) {
		t.Fatal("archived contact still listed")
	}
	if res, _ = s.do("GET", contactURL, nil); res.StatusCode != http.StatusNotFound {
		t.Fatalf("archived contact page: %d", res.StatusCode)
	}
	// Sidebar preference cookie: valid values become a class, anything else is ignored.
	for value, want := range map[string]string{"c": `class="shell collapsed"`, "280": `class="shell sw-280"`, "290": `class="shell "`, `"><script>`: `class="shell "`} {
		res, page := s.do("GET", "/companies", nil, "Cookie", "hilo_side="+url.QueryEscape(value)+"; "+cookieHeader(s))
		if res.StatusCode != http.StatusOK || !strings.Contains(page, want) {
			t.Fatalf("hilo_side=%q: want %s", value, want)
		}
	}
}

// cookieHeader returns the session cookie of the browser jar (tests that set
// an extra cookie by hand must resend it).
func cookieHeader(s *stack) string {
	u, _ := url.Parse(s.base)
	var parts []string
	for _, c := range s.web.Jar.Cookies(u) {
		parts = append(parts, c.Name+"="+c.Value)
	}
	return strings.Join(parts, "; ")
}

func TestOpportunityPageAndTags(t *testing.T) {
	s := start(t)
	csrf := s.signIn()
	post := func(path string, v url.Values) (*http.Response, string) {
		v.Set("csrf", csrf)
		return s.do("POST", path, v)
	}
	_, page := s.do("GET", "/board", nil)
	pipe := regexp.MustCompile(`name="pipeline" value="([0-9a-f-]{36})"`).FindStringSubmatch(page)[1]
	post("/contacts", url.Values{"email": {"ana@globex.com"}, "first_name": {"Ana"}, "company": {"globex.com"}})
	post("/contacts", url.Values{"email": {"bob@globex.com"}, "first_name": {"Bob"}})
	post("/opportunities", url.Values{"pipeline": {pipe}, "title": {"Planta"}, "company": {"globex.com"}, "tags": {"pyme"}})
	_, page = s.do("GET", "/board", nil)
	oppURL := regexp.MustCompile(`href="(/opportunities/[0-9a-f-]{36})"`).FindStringSubmatch(page)[1]
	var res *http.Response
	// Edit every field, including tags (comma list, normalised) and the primary contact.
	edit := url.Values{"title": {"Planta solar"}, "kind": {"project"}, "amount": {"2.500,75"}, "currency": {"EUR"}, "probability": {"40"},
		"expected_close_date": {"2027-03-31"}, "company": {"globex.com"}, "primary_contact": {"ana@globex.com"}, "next_step": {"Visita técnica"},
		"next_step_at": {"2027-01-15"}, "tags": {"PyME, energía, pyme"}, "description": {"Línea 1\nLínea 2"}, "channel": {""}, "owner": {""}}
	if res, _ = post(oppURL, edit); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("save opportunity: %d", res.StatusCode)
	}
	_, page = s.do("GET", oppURL, nil)
	for _, want := range []string{"Planta solar", "EUR 2.500,75", "40%", `value="pyme, energía"`, `value="ana@globex.com"`, "Visita técnica", `id="tag-suggest"`} {
		if !strings.Contains(page, want) {
			t.Fatalf("opportunity page missing %q", want)
		}
	}
	edit.Set("probability", "150")
	if res, _ = post(oppURL, edit); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("probability 150 accepted: %d", res.StatusCode)
	}
	// Link a second contact with a role; move to lost needs a reason.
	if res, _ = post(oppURL+"/contacts", url.Values{"contact": {"bob@globex.com"}, "role": {"champion"}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("link contact: %d", res.StatusCode)
	}
	lost := regexp.MustCompile(`<option value="([0-9a-f-]{36})" data-category="lost"`).FindStringSubmatch(page)[1]
	if res, _ = post(oppURL+"/move", url.Values{"stage": {lost}}); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("lost without reason: %d", res.StatusCode)
	}
	if res, _ = post(oppURL+"/move", url.Values{"stage": {lost}, "reason": {"precio"}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("move to lost: %d", res.StatusCode)
	}
	_, page = s.do("GET", oppURL, nil)
	if !strings.Contains(page, "Perdida: precio") || !strings.Contains(page, "champion") {
		t.Fatal("lost reason / linked contact not shown")
	}
	// Contact tags are editable from the contact page.
	_, page = s.do("GET", "/contacts?q=bob", nil)
	contactURL := regexp.MustCompile(`href="(/contacts/[0-9a-f-]{36})"`).FindStringSubmatch(page)[1]
	post(contactURL, url.Values{"first_name": {"Bob"}, "email": {"bob@globex.com"}, "tags": {"Decisor, técnico"}})
	if _, page = s.do("GET", "/contacts?tag=decisor", nil); !strings.Contains(page, contactURL) {
		t.Fatal("contact tag not saved or not filterable")
	}
	if res, _ = post(oppURL+"/archive", url.Values{}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("archive: %d", res.StatusCode)
	}
	if res, _ = s.do("GET", oppURL, nil); res.StatusCode != http.StatusNotFound {
		t.Fatalf("archived opportunity page: %d", res.StatusCode)
	}
}

func TestBranding(t *testing.T) {
	s := start(t)
	csrf := s.signIn()
	multipart := func(path, field, filename string, data []byte) int {
		var b strings.Builder
		boundary := "hiloboundary"
		b.WriteString("--" + boundary + "\r\nContent-Disposition: form-data; name=\"csrf\"\r\n\r\n" + csrf + "\r\n")
		b.WriteString("--" + boundary + "\r\nContent-Disposition: form-data; name=\"" + field + "\"; filename=\"" + filename + "\"\r\nContent-Type: image/png\r\n\r\n")
		b.Write(data)
		b.WriteString("\r\n--" + boundary + "--\r\n")
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, s.base+path, strings.NewReader(b.String()))
		req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
		res, err := s.web.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		return res.StatusCode
	}
	if res, _ := s.do("POST", "/admin/brand-name", url.Values{"csrf": {csrf}, "name": {"Acme CRM"}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("set name: %d", res.StatusCode)
	}
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89\x00\x00\x00\rIDATx\x9cc\x00\x01\x00\x00\x05\x00\x01\r\n-\xb4\x00\x00\x00\x00IEND\xaeB`\x82")
	if c := multipart("/admin/brand-logo", "logo", "logo.png", png); c != http.StatusSeeOther {
		t.Fatalf("upload png: %d", c)
	}
	if c := multipart("/admin/brand-logo", "logo", "evil.png", []byte("<html><script>alert(1)</script></html>")); c != http.StatusBadRequest {
		t.Fatalf("html disguised as png accepted: %d", c)
	}
	_, page := s.do("GET", "/board", nil)
	if !strings.Contains(page, "<title>Funnel · Acme CRM</title>") || !strings.Contains(page, "/branding/logo?v=") {
		t.Fatal("branding not applied to pages")
	}
	res, body := s.do("GET", "/branding/logo", nil)
	if res.Header.Get("Content-Type") != "image/png" || !strings.HasPrefix(body, "\x89PNG") || !strings.Contains(res.Header.Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("logo response: %v", res.Header)
	}
	// An SVG is accepted but always served sandboxed.
	if c := multipart("/admin/brand-logo", "logo", "l.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)); c != http.StatusSeeOther {
		t.Fatalf("svg upload: %d", c)
	}
	res, _ = s.do("GET", "/branding/logo", nil)
	if res.Header.Get("Content-Type") != "image/svg+xml" || !strings.Contains(res.Header.Get("Content-Security-Policy"), "sandbox") || res.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("svg must be sandboxed: %v", res.Header)
	}
	// The login page uses the brand too; non-superadmins cannot change it.
	_, _ = s.do("POST", "/logout", url.Values{"csrf": {csrf}})
	if _, page = s.do("GET", "/login", nil); !strings.Contains(page, "Entrar a Acme CRM") {
		t.Fatal("login page without brand")
	}
	// Non-superadmins cannot change the brand: sign back in as root, invite a
	// member, and try as them.
	s.devLogin("root@acme.test", "/")
	_, page = s.do("GET", "/settings/members", nil)
	csrf = find(t, csrfRe, page)
	_, _ = s.do("POST", "/settings/invite", url.Values{"csrf": {csrf}, "email": {"m@acme.test"}, "role": {"member"}})
	_, _ = s.do("POST", "/logout", url.Values{"csrf": {csrf}})
	s.devLogin("m@acme.test", "/")
	_, page = s.do("GET", "/board", nil)
	mcsrf := find(t, csrfRe, page)
	if res, _ = s.do("POST", "/admin/brand-name", url.Values{"csrf": {mcsrf}, "name": {"pwned"}}); res.StatusCode != http.StatusForbidden {
		t.Fatalf("member renamed the app: %d", res.StatusCode)
	}
}
