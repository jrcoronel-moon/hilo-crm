package secure

import (
	"errors"
	"strings"
	"testing"
)

func TestSealOpen(t *testing.T) {
	master := []byte("0123456789abcdef0123456789abcdef-master")
	sealed, err := Seal(master, "turnstile", "ws-1", "0x4AAA-secret")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, "secret") || !strings.HasPrefix(sealed, "v1.") {
		t.Fatalf("sealed value leaks or lacks version: %q", sealed)
	}
	if again, _ := Seal(master, "turnstile", "ws-1", "0x4AAA-secret"); again == sealed {
		t.Fatal("nonce reused: two seals are identical")
	}
	if got, err := Open(master, "turnstile", "ws-1", sealed); err != nil || got != "0x4AAA-secret" {
		t.Fatalf("open: %q %v", got, err)
	}
	other := []byte("another-master-secret-of-32-bytes!!")
	tampered := sealed[:len(sealed)-2] + "AA"
	for name, open := range map[string]func() (string, error){
		"other row":      func() (string, error) { return Open(master, "turnstile", "ws-2", sealed) },
		"other purpose":  func() (string, error) { return Open(master, "other", "ws-1", sealed) },
		"rotated master": func() (string, error) { return Open(other, "turnstile", "ws-1", sealed) },
		"tampered":       func() (string, error) { return Open(master, "turnstile", "ws-1", tampered) },
		"garbage":        func() (string, error) { return Open(master, "turnstile", "ws-1", "v1.!!") },
		"no version":     func() (string, error) { return Open(master, "turnstile", "ws-1", "plain") },
	} {
		if _, err := open(); !errors.Is(err, ErrUnsealable) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := Seal([]byte("short"), "turnstile", "ws-1", "x"); err == nil {
		t.Fatal("short master key accepted")
	}
}
