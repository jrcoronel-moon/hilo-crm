package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jrcoronel-moon/hilo-crm/internal/config"
	"github.com/jrcoronel-moon/hilo-crm/internal/crm"
	"github.com/jrcoronel-moon/hilo-crm/internal/identity"
	"github.com/jrcoronel-moon/hilo-crm/internal/testdb"
)

// TestSearch covers the header's search box: the results page and the JSON
// quick list find companies, contacts and opportunities of the current
// workspace only, link to their record pages, escape what they print, and
// need a session.
func TestSearch(t *testing.T) {
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
	id := identity.New(d, log, map[string]bool{"ana@acme.test": true, "bob@other.test": true})
	svc := crm.New(d, log)
	login := func(email, ws string) (crm.Actor, string) {
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
		return crm.NewActor(u.ID, w.ID, "owner", "session", "", ""), token
	}
	ana, token := login("ana@acme.test", "Acme")
	bob, _ := login("bob@other.test", "Other")
	must := func(_ any, _ bool, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(svc.UpsertCompany(ctx, ana, crm.CompanyInput{Name: ptr("Globex <b>Andina</b>"), Domain: ptr("globex.test")}))
	must(svc.UpsertContact(ctx, ana, crm.ContactInput{FirstName: ptr("Gloria"), LastName: ptr("Paz"), Email: ptr("gloria@globex.test")}))
	must(svc.UpsertOpportunity(ctx, ana, crm.OpportunityInput{Title: ptr("Globex renovación")}))
	must(svc.UpsertCompany(ctx, bob, crm.CompanyInput{Name: ptr("Globex del otro workspace"), Domain: ptr("globex-other.test")}))

	get := func(path, accept, cookie string) (int, string) {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+path, nil)
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: "hilo_session", Value: cookie})
		}
		req.Header.Set("Accept-Language", "es")
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		res, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}

	st, body := get("/search?q=glo", "", token)
	if st != http.StatusOK || !strings.Contains(body, "3 resultados para") || !strings.Contains(body, `href="/contacts/`) ||
		!strings.Contains(body, "Globex &lt;b&gt;Andina&lt;/b&gt;") || strings.Contains(body, "otro workspace") {
		t.Fatalf("results page: %d\n%s", st, body)
	}

	st, body = get("/search?q=glo", "application/json", token)
	var out struct {
		Results []searchHit `json:"results"`
	}
	if st != http.StatusOK || json.Unmarshal([]byte(body), &out) != nil || len(out.Results) != 3 {
		t.Fatalf("json: %d %s", st, body)
	}
	for _, r := range out.Results {
		if r.Title == "Globex del otro workspace" || !strings.HasPrefix(r.URL, map[string]string{
			"company": "/companies/", "contact": "/contacts/", "opportunity": "/opportunities/"}[r.Type]) || r.Label == "" {
			t.Fatalf("bad hit %+v", r)
		}
		if r.Type == "opportunity" && r.Subtitle != "abierta" {
			t.Fatalf("status not translated: %+v", r)
		}
	}

	// Too short, or LIKE wildcards only: an empty result, not an error.
	for _, q := range []string{"g", "%25%25"} {
		if st, body = get("/search?q="+q, "application/json", token); st != http.StatusOK || body != "{\"results\":[]}\n" {
			t.Fatalf("q=%s: %d %s", q, st, body)
		}
	}
	if st, _ = get("/search?q=glo", "application/json", ""); st == http.StatusOK {
		t.Fatal("search answered without a session")
	}
}
