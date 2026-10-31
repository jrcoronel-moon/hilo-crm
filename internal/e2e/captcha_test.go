package e2e_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/jrcoronel-moon/hilo-crm/internal/config"
)

const (
	testSiteKey = "0x4AAAAAAAsite"
	testSecret  = "0x4AAAAAAAtest-secret-9xyz"
)

// fakeTurnstile stands in for Cloudflare's siteverify: "good" was solved on
// the allowed site, "elsewhere" on another site, anything else is invalid.
func fakeTurnstile(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch {
		case r.PostForm.Get("secret") != testSecret:
			_, _ = w.Write([]byte(`{"success":false,"error-codes":["invalid-input-secret"]}`))
		case r.PostForm.Get("response") == "good":
			_, _ = w.Write([]byte(`{"success":true,"hostname":"www.acme.test"}`))
		case r.PostForm.Get("response") == "elsewhere":
			_, _ = w.Write([]byte(`{"success":true,"hostname":"evil.example"}`))
		default:
			_, _ = w.Write([]byte(`{"success":false,"error-codes":["invalid-input-response"]}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestLeadFormCaptcha(t *testing.T) {
	cf := fakeTurnstile(t)
	s := startWith(t, func(c *config.Config) { c.TurnstileVerifyURL = cf.URL })
	csrf := s.signIn()
	status := func(path string, v url.Values) int {
		t.Helper()
		v.Set("csrf", csrf)
		res, _ := s.do("POST", path, v)
		return res.StatusCode
	}
	_, page := s.do("GET", "/settings/forms", nil)
	if !strings.Contains(page, "sin configurar") {
		t.Fatal("missing keys not shown")
	}
	ch := regexp.MustCompile(`<option value="([0-9a-f-]{36})" selected>Formulario</option>`).FindStringSubmatch(page)
	if ch == nil {
		t.Fatal("form channel not found")
	}
	newForm := url.Values{"name": {"Web"}, "channel": {ch[1]}, "origins": {"https://www.acme.test"}, "captcha": {"1"}}
	if c := status("/settings/form", newForm); c != http.StatusBadRequest {
		t.Fatalf("captcha form without keys must be refused: %d", c)
	}

	// The admin saves the keys in the UI; the secret is required the first time.
	if c := status("/settings/captcha-keys", url.Values{"site_key": {testSiteKey}}); c != http.StatusBadRequest {
		t.Fatalf("first save without secret: %d", c)
	}
	if c := status("/settings/captcha-keys", url.Values{"site_key": {"<script>"}, "secret_key": {testSecret}}); c != http.StatusBadRequest {
		t.Fatalf("malformed site key: %d", c)
	}
	if c := status("/settings/captcha-keys", url.Values{"site_key": {"0x4AAAAAAAold"}, "secret_key": {testSecret}}); c != http.StatusSeeOther {
		t.Fatalf("save keys: %d", c)
	}
	// Changing only the site key keeps the stored secret.
	if c := status("/settings/captcha-keys", url.Values{"site_key": {testSiteKey}, "secret_key": {""}}); c != http.StatusSeeOther {
		t.Fatalf("update site key: %d", c)
	}
	_, page = s.do("GET", "/settings/forms", nil)
	if !strings.Contains(page, "claves guardadas") || !strings.Contains(page, "<code>9xyz</code>") {
		t.Fatal("saved state or secret hint not shown")
	}
	if strings.Contains(page, testSecret) || strings.Contains(page, "test-secret") {
		t.Fatal("the secret reached the page")
	}

	if c := status("/settings/form", newForm); c != http.StatusSeeOther {
		t.Fatalf("create captcha form: %d", c)
	}
	_, page = s.do("GET", "/settings/forms", nil)
	if !strings.Contains(page, `data-sitekey="`+testSiteKey+`"`) || !strings.Contains(page, "turnstile/v0/api.js") {
		t.Fatal("snippet lacks the Turnstile widget")
	}
	key := regexp.MustCompile(`/f/(lf_[A-Za-z0-9_-]+)`).FindStringSubmatch(page)[1]
	id := regexp.MustCompile(`action="/settings/form-captcha"[^>]*>.*?name="id" value="([0-9a-f-]{36})"`).FindStringSubmatch(page)[1]

	post := func(token string) int {
		v := url.Values{"email": {"ana@cliente.test"}, "name": {"Ana Pérez"}}
		if token != "" {
			v.Set("cf-turnstile-response", token)
		}
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, s.base+"/f/"+key, strings.NewReader(v.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = r.Body.Close()
		return r.StatusCode
	}
	for _, tc := range []struct {
		token string
		want  int
	}{
		{"", http.StatusBadRequest},         // widget missing or not completed
		{"forged", http.StatusForbidden},    // Cloudflare rejects it
		{"elsewhere", http.StatusForbidden}, // solved on a site outside the allow-list
		{"good", http.StatusOK},
	} {
		if got := post(tc.token); got != tc.want {
			t.Fatalf("token %q: got %d, want %d", tc.token, got, tc.want)
		}
	}
	_, page = s.do("GET", "/board", nil)
	if !strings.Contains(page, "Ana Pérez") {
		t.Fatal("verified lead not on the board")
	}

	// Keys cannot be removed while a form depends on them.
	if c := status("/settings/captcha-keys-delete", url.Values{}); c != http.StatusBadRequest {
		t.Fatalf("delete keys in use: %d", c)
	}
	if c := status("/settings/form-captcha", url.Values{"id": {id}, "captcha": {"0"}}); c != http.StatusSeeOther {
		t.Fatalf("captcha off: %d", c)
	}
	if c := status("/settings/captcha-keys-delete", url.Values{}); c != http.StatusSeeOther {
		t.Fatalf("delete keys: %d", c)
	}
	if c := status("/settings/form-captcha", url.Values{"id": {id}, "captcha": {"1"}}); c != http.StatusBadRequest {
		t.Fatalf("captcha on without keys: %d", c)
	}
	if got := post(""); got != http.StatusOK {
		t.Fatalf("no captcha required any more: %d", got)
	}
}
