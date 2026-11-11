package crm

import (
	"encoding/json"
	"math"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

// cleanText trims, strips control characters (keeping newlines and tabs) and
// enforces a maximum length in runes.
func cleanText(field, s string, maxLen int) (string, error) {
	s = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s))
	if !utf8.ValidString(s) {
		return "", invalid(field, "invalid UTF-8")
	}
	if utf8.RuneCountInString(s) > maxLen {
		return "", invalid(field, "too long (max %d characters)", maxLen)
	}
	return s, nil
}

// cleanLine is cleanText for single-line fields: newlines become spaces.
func cleanLine(field, s string, maxLen int) (string, error) {
	return cleanText(field, strings.NewReplacer("\r", " ", "\n", " ", "\t", " ").Replace(s), maxLen)
}

// requiredName validates a mandatory short name (max 100 characters).
func requiredName(field, s string) (string, error) {
	s, err := cleanLine(field, s, 100)
	if err == nil && s == "" {
		err = invalid(field, "is required")
	}
	return s, err
}

var domainRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

// NormalizeDomain accepts "Acme.com", "https://www.acme.com/about" or
// "www.acme.com" and returns "acme.com".
func NormalizeDomain(field, s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "", nil
	}
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil {
			return "", invalid(field, "not a valid domain")
		}
		s = u.Hostname()
	}
	s = strings.TrimSuffix(strings.TrimPrefix(s, "www."), ".")
	if len(s) > 253 || !domainRe.MatchString(s) {
		return "", invalid(field, "not a valid domain")
	}
	return s, nil
}

// NormalizeEmail lower-cases and validates an address (no display names).
func NormalizeEmail(field, s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "", nil
	}
	a, err := mail.ParseAddress(s)
	if err != nil || a.Address != s || len(s) > 320 {
		return "", invalid(field, "not a valid email address")
	}
	if _, err := NormalizeDomain(field, EmailDomain(s)); err != nil {
		return "", invalid(field, "not a valid email address")
	}
	return s, nil
}

// EmailDomain returns the part after the last "@".
func EmailDomain(email string) string {
	if i := strings.LastIndexByte(email, '@'); i >= 0 {
		return email[i+1:]
	}
	return ""
}

// cleanURL accepts only absolute http(s) URLs, so nothing like javascript: can
// ever be rendered as a link.
func cleanURL(field, s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || len(s) > 500 {
		return "", invalid(field, "must be an absolute http(s) URL")
	}
	return u.String(), nil
}

// cleanImageURL accepts an https URL for a record's picture. The web app
// fetches it server-side (see Avatar), so plain http and anything but a
// public host are refused there too; here only the shape is checked.
func cleanImageURL(field, s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || len(s) > 1000 {
		return "", invalid(field, "must be an absolute https URL of an image")
	}
	return u.String(), nil
}

var tagRe = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N} _.:/-]{0,39}$`)

// cleanTags lower-cases, dedupes and validates tags (max 30, 40 chars each).
func cleanTags(field string, tags []string) ([]string, error) {
	out := make([]string, 0, len(tags))
	seen := map[string]bool{}
	for _, t := range tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || seen[t] {
			continue
		}
		if !tagRe.MatchString(t) {
			return nil, invalid(field, "invalid tag %q", t)
		}
		seen[t] = true
		out = append(out, t)
	}
	if len(out) > 30 {
		return nil, invalid(field, "at most 30 tags")
	}
	return out, nil
}

// cleanCustom validates free-form custom fields: a flat-ish JSON object of at
// most 50 keys and 16 KiB.
func cleanCustom(field string, m map[string]any) (json.RawMessage, error) {
	if m == nil {
		return json.RawMessage(`{}`), nil
	}
	if len(m) > 50 {
		return nil, invalid(field, "at most 50 keys")
	}
	for k := range m {
		if k == "" || utf8.RuneCountInString(k) > 64 {
			return nil, invalid(field, "keys must be 1-64 characters")
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, invalid(field, "must be a JSON object")
	}
	if len(b) > 16*1024 {
		return nil, invalid(field, "too large (max 16 KiB)")
	}
	return b, nil
}

// Link points at something outside Hilo that belongs to a record: a proposal,
// a shared folder, a recording. Agents leave every external file here.
type Link struct {
	URL   string `json:"url" jsonschema:"absolute http(s) URL"`
	Label string `json:"label,omitempty" jsonschema:"short name, e.g. Propuesta v2; the host is shown when omitted"`
}

// cleanLinks validates links (absolute http(s) URLs, labels up to 100 chars,
// at most 50) and drops duplicates, keeping the first label.
func cleanLinks(field string, links []Link) (json.RawMessage, error) {
	out := make([]Link, 0, len(links))
	seen := map[string]bool{}
	for _, l := range links {
		u, err := cleanURL(field, l.URL)
		if err != nil {
			return nil, err
		}
		label, err := cleanLine(field, l.Label, 100)
		if err != nil {
			return nil, err
		}
		if u == "" || seen[u] {
			continue
		}
		seen[u] = true
		out = append(out, Link{URL: u, Label: label})
	}
	if len(out) > 50 {
		return nil, invalid(field, "at most 50 links")
	}
	b, _ := json.Marshal(out)
	return b, nil
}

// linksOf decodes the stored links column; nil when there are none.
func linksOf(raw json.RawMessage) []Link {
	var out []Link
	_ = json.Unmarshal(raw, &out)
	if len(out) == 0 {
		return nil
	}
	return out
}

var currencyRe = regexp.MustCompile(`^[A-Z]{3}$`)

func cleanCurrency(field, s string) (string, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	if !currencyRe.MatchString(s) {
		return "", invalid(field, "must be an ISO 4217 code such as USD, EUR, ARS")
	}
	return s, nil
}

// maxAmount keeps amount_minor far from int64 overflow in sums.
const maxAmount = 1e13

// AmountToMinor converts a decimal amount (up to 2 decimals) into minor units.
func AmountToMinor(field string, v float64) (int64, error) {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > maxAmount {
		return 0, invalid(field, "must be between 0 and 10,000,000,000,000")
	}
	return int64(math.Round(v * 100)), nil
}

// MinorToAmount is the inverse of AmountToMinor.
func MinorToAmount(m int64) float64 { return float64(m) / 100 }

func cleanProbability(field string, p *int) (*int32, error) {
	if p == nil {
		return nil, nil
	}
	if *p < 0 || *p > 100 {
		return nil, invalid(field, "must be between 0 and 100")
	}
	v := int32(*p)
	return &v, nil
}

var countryRe = regexp.MustCompile(`^[A-Z]{2}$`)

func cleanCountry(field, s string) (string, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	if s != "" && !countryRe.MatchString(s) {
		return "", invalid(field, "must be an ISO 3166-1 alpha-2 code such as AR, ES, US")
	}
	return s, nil
}

var sizeRanges = map[string]bool{"": true, "1-10": true, "11-50": true, "51-200": true, "201-1000": true, "1001-5000": true, "5000+": true}

// parseDate accepts YYYY-MM-DD.
func parseDate(field, s string) (*time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return nil, invalid(field, "must be a date as YYYY-MM-DD")
	}
	return &t, nil
}

// parseTime accepts RFC 3339 timestamps or a bare date (midnight UTC).
func parseTime(field, s string) (*time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return &t, nil
	}
	if t, err := time.Parse(time.DateOnly, s); err == nil {
		return &t, nil
	}
	return nil, invalid(field, "must be an RFC 3339 timestamp (2026-01-31T15:04:05Z) or a date (2026-01-31)")
}

func parseID(field, s string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(s))
	if err != nil {
		return uuid.Nil, invalid(field, "must be a valid id")
	}
	return id, nil
}

// likePattern escapes LIKE wildcards so a user-supplied query is matched
// literally (the SQL wraps it in %…%).
func likePattern(s string) (*string, error) {
	s, err := cleanLine("query", s, 200)
	if err != nil || s == "" {
		return nil, err
	}
	s = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
	return &s, nil
}

func clampLimit(n, def, maxN int) int32 {
	if n <= 0 {
		n = def
	}
	return int32(min(n, maxN)) //nolint:gosec // bounded by maxN
}
