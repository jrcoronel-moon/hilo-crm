package banner

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPrintPlain(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var buf bytes.Buffer
	Print(&buf, Info{Service: "mcp", Commit: "abc1234", BuiltAt: "2026-10-29T10:00Z", Migration: "0001_init", Addr: ":8080", Integrations: []string{"google", "microsoft"}})
	out := buf.String()
	if strings.Contains(out, "\x1b[") {
		t.Fatal("NO_COLOR must strip ANSI codes")
	}
	for _, want := range []string{"mcp", "abc1234", "0001_init", "google · microsoft"} {
		if !strings.Contains(out, want) {
			t.Fatalf("banner missing %q:\n%s", want, out)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if n := utf8.RuneCountInString(line); n > 100 {
			t.Fatalf("line too wide (%d): %q", n, line)
		}
	}
}

func TestPrintDefaults(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var buf bytes.Buffer
	Print(&buf, Info{})
	if !strings.Contains(buf.String(), "commit    dev") {
		t.Fatalf("empty commit should read dev:\n%s", buf.String())
	}
}
