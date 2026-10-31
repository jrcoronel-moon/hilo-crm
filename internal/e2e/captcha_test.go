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

// fakeTurnstile stands in for Cloudflare's siteverify: "good" was solved on
// the allowed site, "elsewhere" on another site, anything else is invalid.
func fakeTurnstile(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch {
		case r.PostForm.Get("secret") != "test-secret":
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
	s := startWith(t, func(c *config.Config) {
		c.TurnstileSiteKey, c.TurnstileSecretKey, c.TurnstileVerifyURL = "0x4AAAAAAAsite", "test-secret", cf.URL
	})
	csrf := s.signIn()
	_, page := s.do("GET", "/settings/forms", nil)
	ch := regexp.MustCompile(`<option value="([0-9a-f-]{36})" selected>Formulario</option>`).FindStringSubmatch(page)
	if ch == nil {
		t.Fatal("form channel not found")
	}
	res, _ := s.do("POST", "/settings/form", url.Values{"csrf": {csrf}, "name": {"Web"}, "channel": {ch[1]},
		"origins": {"https://www.acme.test"}, "captcha": {"1"}})
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("create form: %d", res.StatusCode)
	}
	_, page = s.do("GET", "/settings/forms", nil)
	if !strings.Contains(page, `data-sitekey="0x4AAAAAAAsite"`) || !strings.Contains(page, "turnstile/v0/api.js") {
		t.Fatal("snippet lacks the Turnstile widget")
	}
	if strings.Contains(page, "test-secret") {
		t.Fatal("the secret key reached the page")
	}
	key := regexp.MustCompile(`/f/(lf_[A-Za-z0-9_-]+)`).FindStringSubmatch(page)[1]
	id := regexp.MustCompile(`name="id" value="([0-9a-f-]{36})"`).FindStringSubmatch(page)[1]

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
	for token, want := range map[string]int{
		"":          http.StatusBadRequest, // widget missing or not completed
		"forged":    http.StatusForbidden,  // Cloudflare rejects it
		"elsewhere": http.StatusForbidden,  // solved on a site outside the allow-list
		"good":      http.StatusOK,
	} {
		if got := post(token); got != want {
			t.Fatalf("token %q: got %d, want %d", token, got, want)
		}
	}
	_, page = s.do("GET", "/board", nil)
	if !strings.Contains(page, "Ana Pérez") {
		t.Fatal("verified lead not on the board")
	}

	// Turning the captcha off accepts plain submissions again.
	if res, _ = s.do("POST", "/settings/form-captcha", url.Values{"csrf": {csrf}, "id": {id}, "captcha": {"0"}}); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("captcha off: %d", res.StatusCode)
	}
	if got := post(""); got != http.StatusOK {
		t.Fatalf("no captcha required any more: %d", got)
	}
}

func TestLeadFormCaptchaNeedsKeys(t *testing.T) {
	s := start(t)
	csrf := s.signIn()
	_, page := s.do("GET", "/settings/forms", nil)
	if !strings.Contains(page, "Captcha no disponible") {
		t.Fatal("missing keys not explained")
	}
	ch := regexp.MustCompile(`<option value="([0-9a-f-]{36})" selected>Formulario</option>`).FindStringSubmatch(page)
	res, _ := s.do("POST", "/settings/form", url.Values{"csrf": {csrf}, "name": {"Web"}, "channel": {ch[1]}, "captcha": {"1"}})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("captcha without keys must be refused: %d", res.StatusCode)
	}
}
