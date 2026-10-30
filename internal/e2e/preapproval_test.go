package e2e_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/jrcoronel-moon/hilo-crm/internal/config"
	"github.com/jrcoronel-moon/hilo-crm/internal/identity"
	"github.com/jrcoronel-moon/hilo-crm/internal/secure"
	"github.com/jrcoronel-moon/hilo-crm/internal/store"
)

const agentID = "https://agent.example/oauth/client.json"

// authorizeAs starts an authorization for the URL-identified test agent.
func (s *stack) authorizeAs(scope, verifier string) string {
	s.t.Helper()
	q := url.Values{"response_type": {"code"}, "client_id": {agentID}, "redirect_uri": {redirectURI},
		"code_challenge": {secure.PKCES256(verifier)}, "code_challenge_method": {"S256"}}
	if scope != "" {
		q.Set("scope", scope)
	}
	_, page := s.do("GET", "/oauth/authorize?"+q.Encode(), nil)
	return page
}

func TestPreapprovedAgentSkipsConsent(t *testing.T) {
	s := start(t)
	s.signIn()
	ctx := context.Background()
	// A client identified by its metadata URL, as if its document was fetched.
	if err := s.db.Platform().UpsertCIMDClient(ctx, store.UpsertCIMDClientParams{ClientID: agentID, Name: "Agent", RedirectUris: []string{redirectURI}}); err != nil {
		t.Fatal(err)
	}
	id := identity.New(s.db, slog.New(slog.DiscardHandler), nil)
	ws, err := id.FindWorkspace(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	op := identity.Operator(ws.ID)

	if page := s.authorizeAs("", "v"); !reqRe.MatchString(page) {
		t.Fatalf("without preapproval the consent screen must show:\n%s", page)
	}
	if err := id.Preapprove(ctx, op, "dcr-client-id", false); err == nil {
		t.Fatal("a client not identified by URL was preapproved")
	}
	if err := id.Preapprove(ctx, op, agentID, false); err != nil {
		t.Fatal(err)
	}

	// Asked for everything: granted read + write, never admin, no consent page.
	verifier := secure.Token("", 40)
	page := s.authorizeAs("", verifier)
	if reqRe.MatchString(page) {
		t.Fatal("preapproved agent still got the consent screen")
	}
	back, _ := url.Parse(find(t, hrefRe, page))
	st, tk := s.token(url.Values{"grant_type": {"authorization_code"}, "client_id": {agentID}, "code": {back.Query().Get("code")},
		"redirect_uri": {redirectURI}, "code_verifier": {verifier}})
	if st != http.StatusOK || tk.Scope != "crm:read crm:write" {
		t.Fatalf("preapproved grant: %d %+v", st, tk)
	}

	// Administration alone always needs consent.
	if page := s.authorizeAs("crm:admin", "v"); !reqRe.MatchString(page) {
		t.Fatal("crm:admin was granted without consent")
	}

	// Ambiguous (two workspaces trust it): ask which one.
	_, wsPage := s.do("GET", "/workspaces/new", nil)
	if res, _ := s.do("POST", "/workspaces", url.Values{"csrf": {find(t, csrfRe, wsPage)}, "name": {"Beta"}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("second workspace: %d", res.StatusCode)
	}
	beta, _ := id.FindWorkspace(ctx, "beta")
	if err := id.Preapprove(ctx, identity.Operator(beta.ID), agentID, true); err != nil {
		t.Fatal(err)
	}
	if page := s.authorizeAs("", "v"); !reqRe.MatchString(page) {
		t.Fatal("two preapproving workspaces must fall back to consent")
	}

	if err := id.Unpreapprove(ctx, identity.Operator(beta.ID), agentID); err != nil {
		t.Fatal(err)
	}
	if list, _ := id.Preapprovals(ctx, op); len(list) != 1 || list[0].Scope != "crm:read crm:write" {
		t.Fatalf("preapprovals: %+v", list)
	}
}

func TestSingleProviderGoesStraightToIt(t *testing.T) {
	s := startWith(t, func(c *config.Config) {
		c.DevLogin = false
		c.GoogleClientID, c.GoogleClientSecret = "test-client", "test-secret"
	})
	authz := "/oauth/authorize?client_id=x"
	res, _ := s.do("GET", "/login?return_to="+url.QueryEscape(authz), nil)
	if loc := res.Header.Get("Location"); res.StatusCode != http.StatusSeeOther || loc != "/auth/google?return_to="+url.QueryEscape(authz) {
		t.Fatalf("agent authorization: %d %q", res.StatusCode, loc)
	}
	// Plain sign-in, after sign-out, or with an error: the login page.
	for _, q := range []string{"", "?return_to=/board", "?error=not_allowed&return_to=" + url.QueryEscape(authz)} {
		if res, page := s.do("GET", "/login"+q, nil); res.StatusCode != http.StatusOK || !strings.Contains(page, "/auth/google") {
			t.Fatalf("/login%s: %d", q, res.StatusCode)
		}
	}
}
