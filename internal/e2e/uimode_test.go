package e2e_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/jrcoronel-moon/hilo-crm/internal/config"
)

func TestMinimalUIKeepsOnlyAgentAuthorization(t *testing.T) {
	s := startWith(t, func(c *config.Config) { c.UI = config.UIMinimal })
	csrf := s.signIn()
	// The agent's OAuth flow still works end to end.
	if _, tk := s.connectAgent(csrf); tk.Access == "" {
		t.Fatal("no token in minimal mode")
	}
	for _, path := range []string{"/board", "/dashboard", "/companies", "/settings/agents", "/admin", "/events"} {
		if res, _ := s.do("GET", path, nil); res.StatusCode != http.StatusNotFound {
			t.Errorf("%s in minimal mode: %d", path, res.StatusCode)
		}
	}
	if res, page := s.do("GET", "/", nil); res.StatusCode != http.StatusOK || !strings.Contains(page, s.base+"/mcp") || !strings.Contains(page, "iniciar sesión") {
		t.Fatalf("home: %d", res.StatusCode)
	}
}

func TestUIOffServesNoPagesNorOAuth(t *testing.T) {
	s := startWith(t, func(c *config.Config) { c.UI = config.UIOff })
	for _, path := range []string{"/login", "/board", "/oauth/authorize", "/.well-known/oauth-authorization-server"} {
		if res, _ := s.do("GET", path, nil); res.StatusCode != http.StatusNotFound {
			t.Errorf("%s with the UI off: %d", path, res.StatusCode)
		}
	}
	if res, page := s.do("GET", "/", nil); res.StatusCode != http.StatusOK || !strings.Contains(page, "hiloctl token create") {
		t.Fatalf("home: %d", res.StatusCode)
	}
	// Public lead forms keep working (unknown key: not found, but routed).
	if res, _ := s.do("POST", "/f/nope", nil); res.StatusCode == http.StatusMethodNotAllowed {
		t.Fatal("lead form endpoint missing")
	}
}
