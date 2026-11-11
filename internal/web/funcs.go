package web

import (
	"fmt"
	"hash/fnv"
	"html/template"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"github.com/jrcoronel-moon/hilo-crm/internal/crm"
	"github.com/jrcoronel-moon/hilo-crm/internal/i18n"
	"github.com/jrcoronel-moon/hilo-crm/internal/store"
)

// funcsFor returns the template functions for one language: translation
// ("t"), and every formatter whose output depends on the language.
func funcsFor(lang string) template.FuncMap {
	f := template.FuncMap{
		"t":          func(msg string, args ...any) string { return i18n.T(lang, msg, args...) },
		"lang":       func() string { return lang },
		"money":      func(v float64, cur string) string { return money(lang, v, cur) },
		"minor":      func(m int64, cur string) string { return money(lang, crm.MinorToAmount(m), cur) },
		"moneyMap":   func(m crm.Money) string { return moneyMap(lang, m) },
		"moneyShort": func(m crm.Money) string { return moneyShort(lang, m) },
		"short":      func(v float64) string { return short(lang, v) },
		"date":       func(t time.Time) string { return t.Format(i18n.DateLayout(lang)) },
		"datep":      func(t *time.Time) string { return datep(lang, t) },
		"ago":        func(t *time.Time) string { return ago(lang, t) },
		"agoT":       func(t time.Time) string { return ago(lang, &t) },
		"catLabel":   func(c string) string { return i18n.T(lang, catLabel(c)) },
		"roleLabel":  func(r string) string { return i18n.T(lang, roleLabel(r)) },
		"typeLabel":  func(t string) string { return i18n.T(lang, orSelf(typeLabels, t)) },
		"kindLabel":  func(k string) string { return i18n.T(lang, orSelf(kindLabels, k)) },
	}
	for k, v := range funcs {
		f[k] = v
	}
	return f
}

// funcs are the language-independent template functions.
var funcs = template.FuncMap{
	"pct":        func(f float64) string { return fmt.Sprintf("%.0f%%", f*100) },
	"bar":        bar,
	"currencies": crm.SortedCurrencies,
	"deref":      derefStr,
	"add":        func(a, b int) int { return a + b },
	"isClosing":  func(cat string) bool { return cat == crm.CatWon || cat == crm.CatLost },
	"initials":   initials,
	"tagClass":   tagClass,
	"avClass":    avClass,
	"av":         av,
	"th":         th,
	"pg": func(shown int, p pager) any {
		return struct {
			Shown int
			P     pager
		}{shown, p}
	},
	"typeIcon":   func(t string) string { return orSelf(typeIcons, t) },
	"stageClass": stageClass,
	"firstTags":  firstTags,
	"scopeClass": scopeClass,
	"stale":      func(t time.Time) bool { return time.Since(t) > staleAfter },
	"fresh":      func(t time.Time) bool { return time.Since(t) < 2*time.Minute },
	"addf":       func(a, b float64) float64 { return a + b },
	"divf":       func(a, b float64) float64 { return a / b },
	"mulf":       func(a, b float64) float64 { return a * b },
	"join":       strings.Join,
	"upper":      strings.ToUpper,
	"trim":       strings.TrimSpace,
	"ownerSel":   ownerSel,
	"ownerName":  ownerName,
	"tagField":   tagField,
	"host":       urlHost,
	"asset":      asset,
}

// Stored values are English (agents and the API see those); people see these.
var (
	typeLabels = map[string]string{"email": i18n.M("Email"), "call": i18n.M("Llamada"), "meeting": i18n.M("Reunión"), "note": i18n.M("Nota"), "message": i18n.M("Mensaje"),
		"social": i18n.M("Red social"), "form": i18n.M("Formulario")}
	kindLabels = map[string]string{"web": i18n.M("Web"), "form": i18n.M("Formulario"), "email": i18n.M("Email"), "phone": i18n.M("Teléfono"), "social": i18n.M("Red social"),
		"referral": i18n.M("Referido"), "event": i18n.M("Evento"), "outbound": i18n.M("Outbound"), "partner": i18n.M("Partner"), "ads": i18n.M("Publicidad"),
		"videocall": i18n.M("Videoconferencia"), "video": i18n.M("Video"), "other": i18n.M("Otro")}
)

func orSelf(m map[string]string, k string) string {
	if v, ok := m[k]; ok {
		return v
	}
	return k
}

// urlHost is the label of a link without one.
func urlHost(s string) string {
	if u, err := url.Parse(s); err == nil && u.Host != "" {
		return strings.TrimPrefix(u.Host, "www.")
	}
	return s
}

// staleAfter matches the dashboard's "estancadas" KPI.
const staleAfter = 14 * 24 * time.Hour

// initials for avatars: "Ana Torres" → "AT", falling back to the email.
func initials(name, email string) string {
	src := strings.TrimSpace(name)
	if src == "" || strings.Contains(src, "@") {
		src = strings.Split(email, "@")[0]
		src = strings.NewReplacer(".", " ", "_", " ", "-", " ").Replace(src)
	}
	var out []rune
	for _, w := range strings.Fields(src) {
		if r := []rune(w); len(r) > 0 {
			out = append(out, unicode.ToUpper(r[0]))
		}
		if len(out) == 2 {
			break
		}
	}
	if len(out) == 0 {
		return "?"
	}
	return string(out)
}

// tagClass gives every tag a stable colour from the kit palette.
func tagClass(tag string) string {
	palette := []string{"indigo", "amber", "red", "teal", ""}
	h := fnv.New32a()
	_, _ = h.Write([]byte(strings.ToLower(tag)))
	return palette[h.Sum32()%uint32(len(palette))] //nolint:gosec // bounded index
}

// avClass gives an avatar (a company, a person, a workspace) a stable hue
// from its name, so the same record looks the same on every page.
func avClass(name string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(strings.ToLower(strings.TrimSpace(name))))
	return "h" + strconv.Itoa(int(h.Sum32()%6))
}

// typeIcons is the sprite symbol of each interaction type (and of a stage
// change in a timeline).
var typeIcons = map[string]string{"email": "mail", "call": "phone", "meeting": "video", "note": "note",
	"message": "msg", "social": "hash", "form": "file", "stage_change": "flag"}

// stageClass colours a stage dot: amber initial, green won, grey lost, and a
// rotating hue for in-progress stages by position.
func stageClass(category string, position int) string {
	switch category {
	case crm.CatInitial:
		return "c-initial"
	case crm.CatWon:
		return "c-won"
	case crm.CatLost:
		return "c-lost"
	}
	return fmt.Sprintf("c-p%d", (max(position, 1)-1)%4+1)
}

// short abbreviates large numbers: 530800 → "530,8k", 1250000 → "1,3M".
func short(lang string, v float64) string {
	switch {
	case math.Abs(v) >= 1e6:
		return strings.Replace(fmt.Sprintf("%.1fM", v/1e6), ".", i18n.Decimal(lang), 1)
	case math.Abs(v) >= 1e4:
		return strings.Replace(fmt.Sprintf("%.1fk", v/1e3), ".", i18n.Decimal(lang), 1)
	default:
		return money(lang, v, "")
	}
}

// moneyShort renders a per-currency map abbreviated ("USD 530,8k").
func moneyShort(lang string, m crm.Money) string {
	if len(m) == 0 {
		return "—"
	}
	parts := make([]string, 0, len(m))
	for _, c := range crm.SortedCurrencies(m) {
		parts = append(parts, c+" "+short(lang, m[c]))
	}
	return strings.Join(parts, " · ")
}

// firstTags keeps cards compact: at most n tags plus a "+k" counter.
func firstTags(tags []string, n int) struct {
	Shown []string
	More  int
} {
	if len(tags) <= n {
		return struct {
			Shown []string
			More  int
		}{tags, 0}
	}
	return struct {
		Shown []string
		More  int
	}{tags[:n], len(tags) - n}
}

func scopeClass(scope string) string {
	switch scope {
	case "crm:read":
		return "teal"
	case "crm:write":
		return "amber"
	case "crm:admin":
		return "red"
	}
	return ""
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// money formats 1234567.5 as "USD 1.234.567,50" (separators per language),
// dropping decimals on whole amounts.
func money(lang string, v float64, cur string) string {
	neg := v < 0
	v = math.Abs(v)
	whole := int64(v)
	cents := int64(math.Round((v - float64(whole)) * 100))
	if cents == 100 {
		whole, cents = whole+1, 0
	}
	out := i18n.Number(lang, whole)
	if cents != 0 {
		out += fmt.Sprintf("%s%02d", i18n.Decimal(lang), cents)
	}
	if neg {
		out = "-" + out
	}
	return strings.TrimSpace(cur + " " + out)
}

func moneyMap(lang string, m crm.Money) string {
	if len(m) == 0 {
		return "—"
	}
	parts := make([]string, 0, len(m))
	for _, c := range crm.SortedCurrencies(m) {
		parts = append(parts, money(lang, m[c], c))
	}
	return strings.Join(parts, " · ")
}

// bar returns a CSS width percentage for a horizontal bar (min 2% if >0).
func bar(n, maxN int) string {
	if maxN <= 0 || n <= 0 {
		return "0%"
	}
	return fmt.Sprintf("%d%%", max(2, n*100/maxN))
}

func datep(lang string, t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(i18n.DateLayout(lang))
}

func ago(lang string, t *time.Time) string {
	if t == nil {
		return i18n.T(lang, "sin actividad")
	}
	d := time.Since(*t)
	switch {
	case d < time.Hour:
		return i18n.T(lang, "hace minutos")
	case d < 24*time.Hour:
		return i18n.T(lang, "hace %d h", int(d.Hours()))
	default:
		return i18n.T(lang, "hace %d d", int(d.Hours()/24))
	}
}

func catLabel(c string) string {
	switch c {
	case crm.CatInitial:
		return i18n.M("Inicial")
	case crm.CatInProgress:
		return i18n.M("En progreso")
	case crm.CatWon:
		return i18n.M("Ganado")
	case crm.CatLost:
		return i18n.M("Perdido")
	}
	return c
}

func roleLabel(r string) string {
	switch r {
	case "owner":
		return i18n.M("Propietario")
	case "admin":
		return i18n.M("Admin")
	case "member":
		return i18n.M("Miembro")
	case "viewer":
		return i18n.M("Lector")
	}
	return r
}

// ownerName is how a record's owner reads on its page: the member's name,
// else their email; "" when there is no owner (or they left).
func ownerName(members []store.ListMembersRow, cur *uuid.UUID) string {
	for _, m := range members {
		if cur != nil && m.ID == *cur {
			if m.Name != "" {
				return m.Name
			}
			return m.Email
		}
	}
	return ""
}

// ownerSel feeds the "owner-select" partial: members plus the current owner.
func ownerSel(members []store.ListMembersRow, cur *uuid.UUID) struct {
	Members []store.ListMembersRow
	Cur     string
} {
	c := ""
	if cur != nil {
		c = cur.String()
	}
	return struct {
		Members []store.ListMembersRow
		Cur     string
	}{members, c}
}

// tagField feeds the "tag-field" partial.
func tagField(tags []string, suggest []crm.TagCount) struct {
	Tags    []string
	Suggest []crm.TagCount
} {
	return struct {
		Tags    []string
		Suggest []crm.TagCount
	}{tags, suggest}
}
