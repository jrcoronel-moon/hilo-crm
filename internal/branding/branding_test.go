package branding

import (
	"strings"
	"testing"
)

func TestSniff(t *testing.T) {
	png := "\x89PNG\r\n\x1a\n" + strings.Repeat("\x00", 20)
	cases := map[string]string{
		png: "image/png",
		"\xff\xd8\xff\xe0" + strings.Repeat("x", 20): "image/jpeg",
		"RIFF\x00\x00\x00\x00WEBPVP8 ":               "image/webp",
		`<svg xmlns="http://www.w3.org/2000/svg"/>`:  "image/svg+xml",
		"\xef\xbb\xbf<?xml version=\"1.0\"?><svg/>":  "image/svg+xml",
		"<html><body>hi</body></html>":               "",
		"GIF89a" + strings.Repeat("x", 20):           "",
		"<?xml version=\"1.0\"?><html/>":             "",
		"":                                           "",
		png + strings.Repeat("x", MaxLogo):           "",
	}
	for in, want := range cases {
		got, ok := sniff([]byte(in))
		if got != want || ok != (want != "") {
			t.Errorf("%.30q: got %q %v want %q", in, got, ok, want)
		}
	}
}
