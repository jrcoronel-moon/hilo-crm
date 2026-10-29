package secure

import (
	"strings"
	"testing"
	"time"
)

var key = []byte(strings.Repeat("k", 32))

func TestTokenUniqueAndPrefixed(t *testing.T) {
	seen := map[string]bool{}
	for range 1000 {
		tok := Token("hilo_x_", 32)
		if !strings.HasPrefix(tok, "hilo_x_") || len(tok) != len("hilo_x_")+43 {
			t.Fatalf("bad token %q", tok)
		}
		if seen[tok] {
			t.Fatal("duplicate token")
		}
		seen[tok] = true
	}
}

func TestPKCEKnownVector(t *testing.T) {
	// RFC 7636 appendix B.
	if got := PKCES256("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"); got != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" {
		t.Fatalf("got %s", got)
	}
}

func TestSignVerify(t *testing.T) {
	type data struct{ A string }
	blob, err := Sign(key, "authz", data{A: "x"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var out data
	if err := Verify(key, "authz", blob, &out); err != nil || out.A != "x" {
		t.Fatalf("verify: %v %+v", err, out)
	}
	if Verify(key, "other-purpose", blob, &out) == nil {
		t.Fatal("blob accepted for a different purpose")
	}
	if Verify([]byte(strings.Repeat("z", 32)), "authz", blob, &out) == nil {
		t.Fatal("blob accepted with a different key")
	}
	tampered := "x" + blob[1:]
	if Verify(key, "authz", tampered, &out) == nil {
		t.Fatal("tampered blob accepted")
	}
	expired, _ := Sign(key, "authz", data{}, -time.Second)
	if Verify(key, "authz", expired, &out) == nil {
		t.Fatal("expired blob accepted")
	}
	for _, junk := range []string{"", ".", "abc", "abc.def", blob + "."} {
		if Verify(key, "authz", junk, &out) == nil {
			t.Fatalf("junk %q accepted", junk)
		}
	}
}

func FuzzVerify(f *testing.F) {
	good, _ := Sign(key, "authz", map[string]int{"a": 1}, time.Hour)
	f.Add(good)
	f.Add("")
	f.Add("a.b")
	f.Fuzz(func(t *testing.T, blob string) {
		var v map[string]any
		if err := Verify(key, "authz", blob, &v); err == nil && blob != good {
			t.Fatalf("unexpected acceptance of %q", blob)
		}
	})
}
