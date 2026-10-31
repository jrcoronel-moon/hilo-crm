package i18n

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

var (
	// {{t "…"}} and (t "…") in templates, with "…" or `…` literals.
	tplMsg = regexp.MustCompile("(?:\\{\\{-?\\s*|\\(\\s*)t\\s+(\"(?:[^\"\\\\]|\\\\.)*\"|`[^`]*`)")
	// i18n.M("…") and i18n.T(lang, "…") in Go.
	goMsg = regexp.MustCompile(`i18n\.(?:M|T)\((?:\s*\w+\s*,)?\s*("(?:[^"\\]|\\.)*")`)
	verb  = regexp.MustCompile(`%[sdv]`)
)

// messages collects every interface message in the code base.
func messages(t *testing.T) []string {
	t.Helper()
	root := filepath.Join("..", "..")
	set := map[string]bool{}
	add := func(lit string) {
		s, err := strconv.Unquote(lit)
		if err != nil {
			t.Fatalf("bad literal %s: %v", lit, err)
		}
		set[s] = true
	}
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		switch {
		case strings.HasSuffix(path, ".html"):
			src, _ := os.ReadFile(path)
			for _, m := range tplMsg.FindAllStringSubmatch(string(src), -1) {
				add(m[1])
			}
		case strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go"):
			src, _ := os.ReadFile(path)
			for _, m := range goMsg.FindAllStringSubmatch(string(src), -1) {
				add(m[1])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(set))
	for m := range set {
		out = append(out, m)
	}
	slices.Sort(out)
	if len(out) < 300 {
		t.Fatalf("only %d messages found: the extraction is broken", len(out))
	}
	return out
}

// TestCatalogsComplete: every message has a translation in every language,
// with the same placeholders, and no catalog keeps stale entries.
func TestCatalogsComplete(t *testing.T) {
	msgs := messages(t)
	if dump := os.Getenv("I18N_DUMP"); dump != "" { // tooling: list messages to translate
		raw, _ := json.MarshalIndent(msgs, "", " ")
		if err := os.WriteFile(dump, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	used := map[string]bool{}
	for _, m := range msgs {
		used[m] = true
	}
	for _, lang := range Supported {
		if lang == "es" {
			continue
		}
		c := Catalog(lang)
		var missing []string
		for _, m := range msgs {
			v, ok := c[m]
			switch {
			case !ok || v == "":
				missing = append(missing, m)
			case !slices.Equal(verb.FindAllString(m, -1), verb.FindAllString(v, -1)):
				t.Errorf("%s: placeholders differ in %q → %q", lang, m, v)
			}
		}
		if len(missing) > 0 {
			t.Errorf("%s: %d messages without translation, e.g. %q", lang, len(missing), missing[:min(5, len(missing))])
		}
		for k := range c {
			if !used[k] {
				t.Errorf("%s: stale entry %q (no longer used)", lang, k)
			}
		}
	}
}

func TestMatch(t *testing.T) {
	for _, tc := range []struct{ choice, accept, want string }{
		{"", "", "en"},
		{"", "es-AR,es;q=0.9,en;q=0.8", "es"},
		{"", "pt-BR", "pt"},
		{"", "de-CH, fr;q=0.5", "de"},
		{"", "ja", "en"},
		{"fr", "es", "fr"},
		{"xx", "it", "it"},
		{"", "garbage;;;", "en"},
	} {
		if got := Match(tc.choice, tc.accept); got != tc.want {
			t.Errorf("Match(%q, %q) = %q, want %q", tc.choice, tc.accept, got, tc.want)
		}
	}
}

func TestFormats(t *testing.T) {
	if got := Number("en", 1234567); got != "1,234,567" {
		t.Errorf("en: %s", got)
	}
	if got := Number("es", -1234567); got != "-1.234.567" {
		t.Errorf("es: %s", got)
	}
	if got := Number("fr", 1234); got != "1 234" {
		t.Errorf("fr: %q", got)
	}
	if T("es", "Guardar") != "Guardar" || T("en", "hace %d h", 3) == "" {
		t.Error("T")
	}
}
