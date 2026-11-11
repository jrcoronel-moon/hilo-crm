package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/jrcoronel-moon/hilo-crm/internal/config"
	"github.com/jrcoronel-moon/hilo-crm/internal/crm"
	"github.com/jrcoronel-moon/hilo-crm/internal/identity"
	"github.com/jrcoronel-moon/hilo-crm/internal/testdb"
)

// TestTablesProfileAndJoin covers the pieces around the tables: sorting and
// filtering companies, the theme switch, the profile page, record pictures,
// and a superadmin entering a workspace (only them, and on the record).
func TestTablesProfileAndJoin(t *testing.T) {
	d := testdb.New(t)
	log := slog.New(slog.DiscardHandler)
	ts := httptest.NewUnstartedServer(nil)
	cfg := &config.Config{BaseURL: "http://" + ts.Listener.Addr().String(), SessionSecret: []byte(strings.Repeat("s", 40))}
	mux, err := Mount(t.Context(), cfg, d, log)
	if err != nil {
		t.Fatal(err)
	}
	ts.Config.Handler = mux
	ts.Start()
	t.Cleanup(ts.Close)

	ctx := context.Background()
	id := identity.New(d, log, map[string]bool{"root@acme.test": true, "bob@other.test": true})
	svc := crm.New(d, log)
	type user struct {
		actor       crm.Actor
		token, csrf string
		ws          string
	}
	login := func(email, ws string) user {
		t.Helper()
		u, err := id.Login(ctx, identity.ExternalIdentity{Provider: "dev", Subject: email, Email: email, EmailVerified: true})
		if err != nil {
			t.Fatal(err)
		}
		w, err := id.CreateWorkspace(ctx, u.ID, ws)
		if err != nil {
			t.Fatal(err)
		}
		token, err := id.CreateSession(ctx, u.ID)
		if err != nil {
			t.Fatal(err)
		}
		sess, err := id.ResolveSession(ctx, token)
		if err != nil {
			t.Fatal(err)
		}
		return user{crm.NewActor(u.ID, w.ID, "owner", "session", "", ""), token, sess.CSRFToken, w.ID.String()}
	}
	root, bob := login("root@acme.test", "Acme"), login("bob@other.test", "Other")
	if err := id.SetSuperadmin(ctx, "bob@other.test", false); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(u user, method, path string, form url.Values) (*http.Response, string) {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequestWithContext(ctx, method, ts.URL+path, body)
		req.AddCookie(&http.Cookie{Name: "hilo_session", Value: u.token})
		req.Header.Set("Accept-Language", "es")
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		b, _ := io.ReadAll(res.Body)
		return res, string(b)
	}
	must := func(_ any, _ bool, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct{ name, industry, country string }{{"Zeta", "Software", "AR"}, {"Alfa", "Retail", "CL"}, {"Mitad", "Software", "AR"}} {
		must(svc.UpsertCompany(ctx, root.actor, crm.CompanyInput{Name: ptr(c.name), Industry: ptr(c.industry), Country: ptr(c.country)}))
	}

	// Sorted by name, both ways; filtered by industry and country.
	order := func(body string, names ...string) bool {
		last := -1
		for _, n := range names {
			i := strings.Index(body, "<b>"+n+"</b>")
			if i < last {
				return false
			}
			last = i
		}
		return true
	}
	if _, body := do(root, "GET", "/companies?sort=name", nil); !order(body, "Alfa", "Mitad", "Zeta") || !strings.Contains(body, `aria-sort="ascending"`) {
		t.Fatalf("ascending sort:\n%s", body)
	}
	if _, body := do(root, "GET", "/companies?sort=name&dir=desc", nil); !order(body, "Zeta", "Mitad", "Alfa") {
		t.Fatal("descending sort")
	}
	if _, body := do(root, "GET", "/companies?industry=Software&country=AR", nil); strings.Contains(body, "<b>Alfa</b>") || !strings.Contains(body, "<b>Zeta</b>") {
		t.Fatal("filters not applied")
	}
	if res, _ := do(root, "GET", "/companies?sort=name;drop", nil); res.StatusCode != http.StatusOK {
		t.Fatalf("unknown sort column: %d", res.StatusCode)
	}

	// Theme: a cookie the server turns into data-theme; "system" clears it.
	res, _ := do(root, "GET", "/theme?t=dark&next=/profile", nil)
	if c := res.Cookies(); res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/profile" || len(c) != 1 || c[0].Value != "dark" {
		t.Fatalf("theme: %d %v %v", res.StatusCode, res.Header, c)
	}
	if res, _ = do(root, "GET", "/theme?t=dark&next=https://evil.test", nil); res.Header.Get("Location") != "/" {
		t.Fatalf("open redirect: %s", res.Header.Get("Location"))
	}
	if _, body := do(root, "GET", "/profile", nil); !strings.Contains(body, "root@acme.test") || !strings.Contains(body, "Tus workspaces") {
		t.Fatalf("profile:\n%s", body)
	}

	// A company without a logo has no picture.
	co, _, err := svc.UpsertCompany(ctx, root.actor, crm.CompanyInput{Name: ptr("Sin logo")})
	if err != nil {
		t.Fatal(err)
	}
	if res, _ = do(root, "GET", "/avatars/company/"+co.ID.String(), nil); res.StatusCode != http.StatusNotFound {
		t.Fatalf("avatar without logo: %d", res.StatusCode)
	}

	// Entering a workspace: not for a regular user; a superadmin becomes an
	// admin member, on the audit log.
	if res, _ = do(bob, "POST", "/admin/join", url.Values{"csrf": {bob.csrf}, "workspace": {root.ws}}); res.StatusCode != http.StatusForbidden {
		t.Fatalf("non-superadmin joined: %d", res.StatusCode)
	}
	if err := id.SetSuperadmin(ctx, "bob@other.test", true); err != nil {
		t.Fatal(err)
	}
	if res, _ = do(bob, "POST", "/admin/join", url.Values{"csrf": {bob.csrf}, "workspace": {root.ws}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("join: %d", res.StatusCode)
	}
	if _, body := do(bob, "GET", "/companies", nil); !strings.Contains(body, "<b>Zeta</b>") {
		t.Fatal("after joining, the workspace is not the current one")
	}
	if role, err := id.Membership(ctx, bob.actor.UserID, root.actor.WorkspaceID); err != nil || role != "admin" {
		t.Fatalf("membership: %q %v", role, err)
	}
	audit, err := svc.ListAudit(ctx, root.actor, "", 10)
	if err != nil || len(audit) == 0 || audit[0].Action != "member.superadmin_join" {
		t.Fatalf("audit: %v %+v", err, audit)
	}
}
