package safehttp

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

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

// A local server (loopback) is exactly what the client must never reach.
func TestClientRefusesLocal(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("secret")) }))
	defer ts.Close()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL, nil)
	if res, err := Client(2*time.Second, 2).Do(req); err == nil {
		_ = res.Body.Close()
		t.Fatal("loopback address reached")
	}
}
