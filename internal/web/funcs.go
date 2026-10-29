package web

import (
	"fmt"
	"html/template"
	"math"
	"strings"
	"time"

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
