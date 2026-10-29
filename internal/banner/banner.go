// Package banner prints the Hilo logo in ASCII at startup, next to what is
// needed to know WHAT is running: which service, the commit, when the image
// was built, the latest migration and which integrations are enabled. After a
// deploy, `docker logs <container> | head -20` answers "did it pick up the new
// code?" at a glance. NO_COLOR (no-color.org) turns the ANSI colours off.
//
// Recipe: docs/STARTUP_BANNER.md. Never pass secret values here, only names.
package banner

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

// A needle with its thread looping through the eye.
var needle = []string{
	`          _       `,
	`         (_)      `,
	`    .~~~~| |~~.   `,
	`   (     | |   )  `,
	`    '~~~~| |~~'   `,
	`         | |      `,
	`         | |      `,
	`         \ /      `,
	`          V       `,
}

// "HILO" in figlet's standard font, one entry per letter (5 rows each).
var letters = map[rune][]string{
	'H': {` _   _ `, `| | | |`, `| |_| |`, `|  _  |`, `|_| |_|`},
	'I': {` ___ `, `|_ _|`, ` | | `, ` | | `, `|___|`},
	'L': {` _     `, `| |    `, `| |    `, `| |___ `, `|_____|`},
	'O': {`  ___  `, ` / _ \ `, `| | | |`, `| |_| |`, ` \___/ `},
}

func word(s string) []string {
	out := make([]string, 5)
	for _, r := range s {
		for i, l := range letters[r] {
			out[i] += l
		}
	}
	return out
}

// Info is what is shown next to the logo.
type Info struct {
	Service      string // web, mcp, migrate…
	Commit       string
	BuiltAt      string
	Migration    string
	Addr         string
	Integrations []string // enabled integrations, names only
}

const (
	teal  = "\x1b[38;2;20;184;166m"
	amber = "\x1b[38;2;251;191;36m"
	grey  = "\x1b[38;2;139;149;184m"
	bold  = "\x1b[1m"
	reset = "\x1b[0m"
)

// Print writes the banner to w.
func Print(w io.Writer, in Info) {
	color := os.Getenv("NO_COLOR") == ""
	c := func(code, s string) string {
		if !color || s == "" {
			return s
		}
		return code + s + reset
	}
	val := func(s, def string) string {
		if strings.TrimSpace(s) == "" {
			return def
		}
		return s
	}
	right := []string{""}
	for _, l := range word("HILO") {
		right = append(right, c(teal+bold, l))
	}
	right = append(right,
		"",
		c(grey, "service   ")+c(amber+bold, val(in.Service, "?"))+c(grey, "  ·  listen ")+val(in.Addr, "—"),
		c(grey, "commit    ")+c(amber, val(in.Commit, "dev"))+c(grey, "  ·  build ")+val(in.BuiltAt, "local"),
		c(grey, "migration ")+val(in.Migration, "—"),
		c(grey, "enabled   ")+val(strings.Join(in.Integrations, " · "), "—"),
	)
	width := 0
	for _, l := range needle {
		width = max(width, utf8.RuneCountInString(l))
	}
	rows := max(len(needle), len(right))
	var b strings.Builder
	b.WriteString("\n")
	for i := range rows {
		left := ""
		if i < len(needle) {
			left = needle[i]
		}
		left += strings.Repeat(" ", width-utf8.RuneCountInString(left))
		r := ""
		if i < len(right) {
			r = right[i]
		}
		b.WriteString(c(teal, left) + "  " + r + "\n")
	}
	b.WriteString("\n")
	_, _ = fmt.Fprint(w, b.String())
}
