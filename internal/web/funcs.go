package web

import (
	"fmt"
	"hash/fnv"
	"html/template"
	"math"
	"strings"
	"time"
	"unicode"

	"github.com/jrcoronel-moon/hilo-crm/internal/crm"
)

var funcs = template.FuncMap{
	"money":      money,
	"minor":      func(m int64, cur string) string { return money(crm.MinorToAmount(m), cur) },
	"moneyMap":   moneyMap,
	"pct":        func(f float64) string { return fmt.Sprintf("%.0f%%", f*100) },
	"bar":        bar,
	"date":       func(t time.Time) string { return t.Format("02/01/2006") },
	"datep":      datep,
	"ago":        ago,
	"catLabel":   catLabel,
	"roleLabel":  roleLabel,
	"currencies": crm.SortedCurrencies,
	"deref":      derefStr,
	"add":        func(a, b int) int { return a + b },
	"isClosing":  func(cat string) bool { return cat == crm.CatWon || cat == crm.CatLost },
	"initials":   initials,
	"tagClass":   tagClass,
	"stageClass": stageClass,
	"short":      short,
	"moneyShort": moneyShort,
	"firstTags":  firstTags,
	"scopeClass": scopeClass,
	"stale":      func(t time.Time) bool { return time.Since(t) > staleAfter },
	"fresh":      func(t time.Time) bool { return time.Since(t) < 2*time.Minute },
	"agoT":       func(t time.Time) string { return ago(&t) },
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
func short(v float64) string {
	switch {
	case math.Abs(v) >= 1e6:
		return strings.Replace(fmt.Sprintf("%.1fM", v/1e6), ".", ",", 1)
	case math.Abs(v) >= 1e4:
		return strings.Replace(fmt.Sprintf("%.1fk", v/1e3), ".", ",", 1)
	default:
		return money(v, "")
	}
}

// moneyShort renders a per-currency map abbreviated ("USD 530,8k").
func moneyShort(m crm.Money) string {
	if len(m) == 0 {
		return "—"
	}
	parts := make([]string, 0, len(m))
	for _, c := range crm.SortedCurrencies(m) {
		parts = append(parts, c+" "+short(m[c]))
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

// money formats 1234567.5 as "USD 1.234.567,50" (es-AR style), dropping
// decimals on whole amounts.
func money(v float64, cur string) string {
	neg := v < 0
	v = math.Abs(v)
	whole := int64(v)
	cents := int64(math.Round((v - float64(whole)) * 100))
	if cents == 100 {
		whole, cents = whole+1, 0
	}
	s := fmt.Sprintf("%d", whole)
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(r)
	}
	out := b.String()
	if cents != 0 {
		out += fmt.Sprintf(",%02d", cents)
	}
	if neg {
		out = "-" + out
	}
	return strings.TrimSpace(cur + " " + out)
}

func moneyMap(m crm.Money) string {
	if len(m) == 0 {
		return "—"
	}
	parts := make([]string, 0, len(m))
	for _, c := range crm.SortedCurrencies(m) {
		parts = append(parts, money(m[c], c))
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

func datep(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format("02/01/2006")
}

func ago(t *time.Time) string {
	if t == nil {
		return "sin actividad"
	}
	d := time.Since(*t)
	switch {
	case d < time.Hour:
		return "hace minutos"
	case d < 24*time.Hour:
		return fmt.Sprintf("hace %d h", int(d.Hours()))
	default:
		return fmt.Sprintf("hace %d d", int(d.Hours()/24))
	}
}

func catLabel(c string) string {
	switch c {
	case crm.CatInitial:
		return "Inicial"
	case crm.CatInProgress:
		return "En progreso"
	case crm.CatWon:
		return "Ganado"
	case crm.CatLost:
		return "Perdido"
	}
	return c
}

func roleLabel(r string) string {
	switch r {
	case "owner":
		return "Propietario"
	case "admin":
		return "Admin"
	case "member":
		return "Miembro"
	case "viewer":
		return "Lector"
	}
	return r
}
