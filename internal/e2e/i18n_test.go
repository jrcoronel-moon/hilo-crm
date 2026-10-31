package e2e_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// TestLanguages: the interface follows the browser's language, the selector
// overrides it with a cookie, and every page renders in every language.
func TestLanguages(t *testing.T) {
	s := start(t)
	csrf := s.signIn()

	cases := []struct{ accept, want, lang string }{
		{"en-US,en;q=0.9", "New opportunity", "en"},
		{"fr-FR", "Nouvelle opportunité", "fr"},
		{"it", "Nuova opportunità", "it"},
		{"de-DE", "Neue Opportunity", "de"},
		{"pt-BR", "Nova oportunidade", "pt"},
		{"ja", "New opportunity", "en"}, // unsupported: English
	}
	for _, tc := range cases {
		res, page := s.do("GET", "/board", nil, "Accept-Language", tc.accept)
		if res.StatusCode != http.StatusOK || !strings.Contains(page, tc.want) || !strings.Contains(page, `<html lang="`+tc.lang+`">`) {
			t.Fatalf("%s: status %d, want %q in lang %s", tc.accept, res.StatusCode, tc.want, tc.lang)
		}
		if strings.Contains(page, "Nueva oportunidad") {
			t.Fatalf("%s: Spanish text leaked", tc.accept)
		}
	}

	// The selector's choice beats the browser and survives navigation.
	res, _ := s.do("GET", "/lang?l=de&next=/dashboard", nil)
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/dashboard" {
		t.Fatalf("lang switch: %d %q", res.StatusCode, res.Header.Get("Location"))
	}
	if _, page := s.do("GET", "/board", nil, "Accept-Language", "fr"); !strings.Contains(page, "Neue Opportunity") {
		t.Fatal("cookie choice not applied")
	}
	// An open redirect is not possible through next.
	for _, next := range []string{"//evil.example", "https://evil.example", `/\evil.example`} {
		res, _ := s.do("GET", "/lang?l=en&next="+url.QueryEscape(next), nil)
		if loc := res.Header.Get("Location"); loc != "/" {
			t.Fatalf("next %q redirected to %q", next, loc)
		}
	}
	// An unknown language is ignored (the last valid choice, English, stays).
	s.do("GET", "/lang?l=xx&next=/", nil)

	// A workspace created in English starts with an English pipeline and channels.
	if res, _ := s.do("POST", "/workspaces", url.Values{"csrf": {csrf}, "name": {"Team EN"}}, "Accept-Language", "en"); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("create workspace: %d", res.StatusCode)
	}
	s.do("GET", "/lang?l=en&next=/", nil)
	if _, page := s.do("GET", "/board", nil); !strings.Contains(page, "Qualified") || !strings.Contains(page, "Sales") || strings.Contains(page, "Calificado") {
		t.Fatal("English workspace not seeded in English")
	}
	if _, page := s.do("GET", "/settings/channels", nil); !strings.Contains(page, "Video call") || strings.Contains(page, "Videoconferencia") {
		t.Fatal("English channels not seeded in English")
	}

	// Every page renders in every language (template functions, formats).
	for _, l := range []string{"en", "es", "fr", "it", "de", "pt"} {
		s.do("GET", "/lang?l="+l+"&next=/", nil)
		for _, p := range []string{"/board", "/dashboard", "/opportunities", "/companies", "/contacts", "/interactions",
			"/settings/agents", "/settings/pipelines", "/settings/channels", "/settings/forms", "/settings/members",
			"/settings/audit", "/settings/import", "/admin"} {
			res, page := s.do("GET", p, nil)
			if res.StatusCode != http.StatusOK {
				t.Fatalf("%s %s: %d", l, p, res.StatusCode)
			}
			if !strings.Contains(page, `id="i18n"`) {
				t.Fatalf("%s %s: no JS strings", l, p)
			}
		}
	}
}
