package oauth_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/jrcoronel-moon/hilo-crm/internal/identity"
	"github.com/jrcoronel-moon/hilo-crm/internal/oauth"
	"github.com/jrcoronel-moon/hilo-crm/internal/testdb"
)

func TestResolveBearerReasons(t *testing.T) {
	ctx := context.Background()
	d := testdb.New(t)
	id := identity.New(d, slog.New(slog.DiscardHandler), map[string]bool{"root@acme.test": true})
	u, err := id.Login(ctx, identity.ExternalIdentity{Provider: "dev", Subject: "r", Email: "root@acme.test", EmailVerified: true})
	if err != nil {
		t.Fatal(err)
	}
	ws, _ := id.CreateWorkspace(ctx, u.ID, "Acme")
	c := identity.Caller{UserID: u.ID, WorkspaceID: ws.ID, Role: "owner"}
	tok, row, err := id.CreatePAT(ctx, c, "bot", false, 0)
	if err != nil {
		t.Fatal(err)
	}
	const res = "http://localhost/mcp"
	tampered := tok[:len(tok)-1] + "A"
	if strings.HasSuffix(tok, "A") {
		tampered = tok[:len(tok)-1] + "B"
	}
	if p, err := oauth.ResolveBearer(ctx, d.Platform(), tok, res); err != nil || p.UserID != u.ID {
		t.Fatalf("valid token: %v", err)
	}
	reason := func(token string) string {
		_, err := oauth.ResolveBearer(ctx, d.Platform(), token, res)
		var it *oauth.InvalidTokenError
		if !errors.As(err, &it) || !errors.Is(err, oauth.ErrInvalidToken) {
			t.Fatalf("%q: unexpected error %v", token, err)
		}
		if strings.Contains(it.Prefix, tok[len(identity.PATPrefix)+6:]) {
			t.Fatal("diagnostic leaks the secret part of the token")
		}
		return it.Reason
	}
	cases := map[string]string{
		"":                   "missing",
		"Bearer " + tok:      "malformed",
		"ghp_something":      "malformed",
		tok + "%":            "malformed", // the copy-paste accident
		tampered:             "unknown",
		"hilo_at_" + tok[9:]: "unknown",
	}
	for in, want := range cases {
		if got := reason(in); got != want {
			t.Errorf("%.20q…: got %s want %s", in, got, want)
		}
	}
	if err := id.RevokePAT(ctx, c, row.ID); err != nil {
		t.Fatal(err)
	}
	if got := reason(tok); got != "revoked" {
		t.Errorf("revoked token: %s", got)
	}
}
