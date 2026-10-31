// Package i18n translates the web interface. Messages are identified by their
// Spanish text (the language the interface was written in, gettext style):
// templates call {{t "Guardar"}}, Go code calls i18n.T(lang, "Guardar"), and
// locales/<lang>.json maps each Spanish message to its translation. Spanish
// needs no catalog. A message missing from a catalog falls back to Spanish;
// the tests make sure none is missing.
package i18n

import (
	"embed"
	"encoding/json"
	"fmt"
	"strings"

	"golang.org/x/text/language"
)

// Default is used when the browser asks for nothing we support.
const Default = "en"

// Supported languages, in the order the selector shows them.
var Supported = []string{"en", "es", "fr", "it", "de", "pt"}

// Names are the languages' own names, for the selector.
var Names = map[string]string{"en": "English", "es": "Español", "fr": "Français", "it": "Italiano",
	"de": "Deutsch", "pt": "Português"}

//go:embed locales/*.json
var files embed.FS

var catalogs = map[string]map[string]string{}

func init() {
	for _, l := range Supported {
		if l == "es" {
			continue
		}
		raw, err := files.ReadFile("locales/" + l + ".json")
		if err != nil {
			panic("i18n: missing catalog " + l)
		}
		m := map[string]string{}
		if err := json.Unmarshal(raw, &m); err != nil {
			panic("i18n: bad catalog " + l + ": " + err.Error())
		}
		catalogs[l] = m
	}
}

// Valid reports whether lang is a supported language code.
func Valid(lang string) bool {
	_, ok := Names[lang]
	return ok
}

// T translates msg (Spanish) into lang; with args, msg is a fmt format.
func T(lang, msg string, args ...any) string {
	out := msg
	if c := catalogs[lang]; c != nil {
		if v := c[msg]; v != "" {
			out = v
		}
	}
	if len(args) > 0 {
		return fmt.Sprintf(out, args...)
	}
	return out
}

// Has reports whether lang has a translation for msg (Spanish always does).
func Has(lang, msg string) bool {
	if lang == "es" {
		return true
	}
	_, ok := catalogs[lang][msg]
	return ok
}

// Catalog returns lang's catalog (nil for Spanish). Callers must not modify it.
func Catalog(lang string) map[string]string { return catalogs[lang] }

var matcher = language.NewMatcher([]language.Tag{language.English, language.Spanish, language.French,
	language.Italian, language.German, language.Portuguese})

// Match picks the language: an explicit choice (the selector's cookie) when
// valid, otherwise the best match for the browser's Accept-Language.
func Match(choice, acceptLanguage string) string {
	if Valid(choice) {
		return choice
	}
	if strings.TrimSpace(acceptLanguage) == "" {
		return Default
	}
	tags, _, err := language.ParseAcceptLanguage(acceptLanguage)
	if err != nil || len(tags) == 0 {
		return Default
	}
	_, idx, conf := matcher.Match(tags...)
	if conf == language.No {
		return Default
	}
	return Supported[idx]
}

// Number formats an integer part with the language's thousands separator.
func Number(lang string, n int64) string {
	s := fmt.Sprintf("%d", n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	sep := thousands(lang)
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteString(sep)
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

func thousands(lang string) string {
	switch lang {
	case "en":
		return ","
	case "fr":
		return " " // narrow no-break space
	default:
		return "."
	}
}

// Decimal is the language's decimal mark.
func Decimal(lang string) string {
	if lang == "en" {
		return "."
	}
	return ","
}

// DateLayout is how dates are written (time.Format layout).
func DateLayout(lang string) string {
	switch lang {
	case "en":
		return "Jan 2, 2006"
	case "de":
		return "02.01.2006"
	default:
		return "02/01/2006"
	}
}

// M marks an interface message written in Go code and returns it unchanged:
// the page translates it when rendering (titles, notices, labels shown
// through templates). The catalog tests find every M("…") and T(lang, "…").
func M(msg string) string { return msg }
