package httpx

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

func TestClientIP(t *testing.T) {
	c := NewClientIP([]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")})
	cases := []struct {
		name, remote, xff, want string
	}{
		{"direct client ignores spoofed header", "203.0.113.9:1234", "1.2.3.4", "203.0.113.9"},
		{"trusted proxy, single hop", "10.0.0.2:80", "198.51.100.7", "198.51.100.7"},
		{"trusted proxy, client-supplied prefix ignored", "10.0.0.2:80", "6.6.6.6, 198.51.100.7", "198.51.100.7"},
		{"chain of trusted proxies", "10.0.0.2:80", "198.51.100.7, 10.0.0.9", "198.51.100.7"},
		{"trusted proxy without header", "10.0.0.2:80", "", "10.0.0.2"},
		{"garbage header", "10.0.0.2:80", "not-an-ip", "10.0.0.2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
			r.RemoteAddr = tc.remote
			if tc.xff != "" {
				r.Header.Set("X-Forwarded-For", tc.xff)
			}
			if got := c.Of(r); got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}

func TestRateLimiter(t *testing.T) {
	now := time.Unix(0, 0)
	l := NewRateLimiter(60, 3)
	l.now = func() time.Time { return now }
	for i := range 3 {
		if !l.Allow("a") {
			t.Fatalf("request %d within burst denied", i)
		}
	}
	if l.Allow("a") {
		t.Fatal("burst exceeded but allowed")
	}
	if !l.Allow("b") {
		t.Fatal("other key must have its own bucket")
	}
	now = now.Add(time.Second)
	if !l.Allow("a") {
		t.Fatal("token should refill after 1s at 60/min")
	}
}

func TestSecurityHeadersAndRecover(t *testing.T) {
	h := SecurityHeaders(Recover(slog.New(slog.DiscardHandler))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("secret internal detail")
	})))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d", rec.Code)
	}
	if body := rec.Body.String(); body != "Internal Server Error\n" {
		t.Fatalf("panic detail leaked: %q", body)
	}
	for _, hdr := range []string{"Content-Security-Policy", "X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy"} {
		if rec.Header().Get(hdr) == "" {
			t.Fatalf("missing %s", hdr)
		}
	}
}

func TestBearerToken(t *testing.T) {
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	for hdr, want := range map[string]string{"Bearer abc": "abc", "bearer  abc ": "abc", "Basic abc": "", "Bearer": "", "": ""} {
		r.Header.Set("Authorization", hdr)
		if got := BearerToken(r); got != want {
			t.Fatalf("%q: got %q want %q", hdr, got, want)
		}
	}
}
