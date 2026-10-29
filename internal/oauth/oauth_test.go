package oauth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestValidRedirectURI(t *testing.T) {
	ok := []string{
		"https://claude.ai/api/mcp/auth_callback",
		"http://127.0.0.1:33418/callback",
		"http://localhost/callback",
		"http://[::1]:8000/cb",
		"cursor://anysphere.cursor-retrieval/oauth/callback",
		"com.example.app:/oauth2redirect",
	}
	bad := []string{
		"", "http://example.com/cb", "https://ex.com/cb#frag", "https://user:pw@ex.com/cb", "javascript:alert(1)",
		"data:text/html,x", "file:///etc/passwd", "myapp:/cb", "https:///nohost", "http://127.0.0.1.evil.com/cb",
		"https://ex.com/a b", "vbscript:x", strings.Repeat("https://a.b/", 300),
	}
	for _, u := range ok {
		if !validRedirectURI(u) {
			t.Errorf("rejected %q", u)
		}
	}
	for _, u := range bad {
		if validRedirectURI(u) {
			t.Errorf("accepted %q", u)
		}
	}
}

func TestRedirectAllowed(t *testing.T) {
	reg := []string{"http://127.0.0.1/callback", "https://app.example/cb"}
	cases := map[string]bool{
		"http://127.0.0.1:5555/callback":   true, // ephemeral port (RFC 8252)
		"http://127.0.0.1/callback":        true,
		"https://app.example/cb":           true,
		"https://app.example:8443/cb":      false, // port only relaxed on loopback http
		"http://127.0.0.1:5555/other":      false,
		"http://localhost:5555/callback":   false, // host must match exactly
		"http://127.0.0.1:5555/callback?x": false,
		"https://app.example/cb/../evil":   false,
	}
	for u, want := range cases {
		if got := redirectAllowed(reg, u); got != want {
			t.Errorf("%s: got %v", u, got)
		}
	}
}

func TestPublicAddr(t *testing.T) {
	for addr, want := range map[string]bool{
		"8.8.8.8": true, "2606:4700:4700::1111": true,
		"127.0.0.1": false, "10.1.2.3": false, "172.20.0.5": false, "192.168.1.1": false,
		"169.254.169.254": false, "100.64.0.1": false, "0.0.0.0": false, "::1": false, "fd00::1": false,
		"fe80::1": false, "::ffff:10.0.0.1": false, "64:ff9b::a00:1": false, "224.0.0.1": false, "198.18.0.1": false,
	} {
		if got := PublicAddr(netip.MustParseAddr(addr)); got != want {
			t.Errorf("%s: got %v", addr, got)
		}
	}
	if dialControl("tcp", "8.8.8.8:80", nil) == nil {
		t.Error("non-443 port allowed")
	}
	if dialControl("tcp", "169.254.169.254:443", nil) == nil {
		t.Error("metadata service allowed")
	}
	if dialControl("tcp", "8.8.8.8:443", nil) != nil {
		t.Error("public address refused")
	}
}

func TestIsCIMD(t *testing.T) {
	for id, want := range map[string]bool{
		"https://claude.ai/oauth/claude-code-client-metadata": true,
		"https://example.com":             false, // needs a path
		"https://example.com:8443/client": false,
		"http://example.com/client":       false,
		"https://example.com/client?x=1":  false,
		"hilo_c_abc":                      false,
	} {
		if got := isCIMD(id); got != want {
			t.Errorf("%s: got %v", id, got)
		}
	}
}

func TestFetchCIMD(t *testing.T) {
	var doc string
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, doc)
	}))
	defer ts.Close()
	id := ts.URL + "/client.json"
	cases := map[string]bool{
		`{"client_id":"` + id + `","client_name":"Claude Code","redirect_uris":["http://localhost/callback"],"token_endpoint_auth_method":"none"}`: true,
		`{"client_id":"https://impostor.example/client.json","redirect_uris":["http://localhost/callback"]}`:                                       false,
		`{"client_id":"` + id + `","redirect_uris":["javascript:alert(1)"]}`:                                                                       false,
		`{"client_id":"` + id + `","redirect_uris":[]}`:                                                                                            false,
		`{"client_id":"` + id + `","redirect_uris":["http://localhost/cb"],"token_endpoint_auth_method":"client_secret_basic"}`:                    false,
		`not json`: false,
		`{"client_id":"` + id + `","redirect_uris":["http://localhost/cb"],"x":"` + strings.Repeat("a", cimdMaxBody) + `"}`: false,
	}
	for body, want := range cases {
		doc = body
		d, err := fetchCIMD(context.Background(), ts.Client(), id)
		if (err == nil) != want {
			t.Errorf("doc %.80s…: err=%v", body, err)
		}
		if want && d.name != "Claude Code" {
			t.Errorf("name %q", d.name)
		}
	}
	// The production client never follows redirects and refuses loopback.
	if _, err := fetchCIMD(context.Background(), newCIMDClient(), id); err == nil {
		t.Error("production CIMD client reached a loopback server")
	}
}

func TestNormalizeScope(t *testing.T) {
	for in, want := range map[string]string{
		"":                               "crm:read crm:write crm:admin",
		"crm:read":                       "crm:read",
		"openid crm:read offline_access": "crm:read",
		"crm:read crm:read crm:write":    "crm:read crm:write",
		"mcp":                            "crm:read crm:write crm:admin",
	} {
		if got, ok := normalizeScope(in); !ok || got != want {
			t.Errorf("%q: got %q %v", in, got, ok)
		}
	}
	if _, ok := normalizeScope("crm:read admin:everything"); ok {
		t.Error("unknown scope accepted")
	}
	if !subset("crm:read", "crm:read crm:write") || subset("crm:admin", "crm:read") {
		t.Error("subset")
	}
}
