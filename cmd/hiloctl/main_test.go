package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/jrcoronel-moon/hilo-crm/internal/db"
	"github.com/jrcoronel-moon/hilo-crm/internal/identity"
	"github.com/jrcoronel-moon/hilo-crm/internal/oauth"
	"github.com/jrcoronel-moon/hilo-crm/internal/testdb"
)

type harness struct {
	t   *testing.T
	ctx context.Context
	d   *db.DB
}

// ok runs a command that must succeed and returns its output.
func (h harness) ok(args ...string) string {
	h.t.Helper()
	var out bytes.Buffer
	if err := run(h.ctx, h.d, &out, args); err != nil {
		h.t.Fatalf("hiloctl %s: %v", strings.Join(args, " "), err)
	}
	return out.String()
}

// fails runs a command that must fail and returns the error text.
func (h harness) fails(args ...string) string {
	h.t.Helper()
	var out bytes.Buffer
	err := run(h.ctx, h.d, &out, args)
	if err == nil {
		h.t.Fatalf("hiloctl %s: expected an error, got %q", strings.Join(args, " "), out.String())
	}
	return err.Error()
}

func (h harness) json(v any, args ...string) {
	h.t.Helper()
	if err := json.Unmarshal([]byte(h.ok(append([]string{"--json"}, args...)...)), v); err != nil {
		h.t.Fatal(err)
	}
}

func TestHeadlessWorkspaceMembersAndTokens(t *testing.T) {
	h := harness{t: t, ctx: context.Background(), d: testdb.New(t)}

	// A workspace for an owner who never signed in.
	var ws struct{ Slug, Owner string }
	h.json(&ws, "workspace", "create", "Acme Corp", "--owner", "Ana@Acme.test")
	if ws.Slug != "acme-corp" || ws.Owner != "ana@acme.test" {
		t.Fatalf("workspace create: %+v", ws)
	}
	if out := h.ok("workspace", "list"); !strings.Contains(out, "acme-corp") {
		t.Fatalf("workspace list: %s", out)
	}

	// Unknown emails are invited; they join on their first verified login.
	if out := h.ok("member", "add", "acme-corp", "bob@acme.test", "member"); !strings.HasPrefix(out, "invited") {
		t.Fatalf("member add: %s", out)
	}
	id := identity.New(h.d, slog.New(slog.DiscardHandler), nil)
	if _, err := id.Login(h.ctx, identity.ExternalIdentity{Provider: "google", Subject: "b", Email: "bob@acme.test", EmailVerified: true}); err != nil {
		t.Fatalf("invited login: %v", err)
	}
	// The owner created by the CLI can sign in too, and stays owner.
	if _, err := id.Login(h.ctx, identity.ExternalIdentity{Provider: "google", Subject: "a", Email: "ana@acme.test", EmailVerified: true}); err != nil {
		t.Fatalf("owner login: %v", err)
	}
	var members struct {
		Members []struct{ Email, Role string } `json:"members"`
	}
	h.json(&members, "member", "list", "acme-corp")
	if len(members.Members) != 2 {
		t.Fatalf("member list: %+v", members)
	}

	// Last-owner rule holds for the operator too.
	if msg := h.fails("member", "role", "acme-corp", "ana@acme.test", "admin"); !strings.Contains(msg, "owner") {
		t.Fatalf("demoting the last owner: %s", msg)
	}
	h.ok("member", "role", "acme-corp", "bob@acme.test", "admin")

	// Tokens: read-only, expiring, usable, listed and revocable.
	var tok struct {
		Token, Scope string
		ID           string `json:"id"`
	}
	h.json(&tok, "token", "create", "acme-corp", "bob@acme.test", "claude", "--read-only", "--ttl", "30d")
	p, err := oauth.ResolveBearer(h.ctx, h.d.Platform(), tok.Token, "http://localhost/mcp")
	if err != nil || p.Scope != "crm:read" || p.Role != "admin" {
		t.Fatalf("issued token: %+v %v", p, err)
	}
	if out := h.ok("token", "list", "acme-corp", "bob@acme.test"); !strings.Contains(out, tok.ID) || strings.Contains(out, tok.Token) {
		t.Fatalf("token list must show the id but never the secret: %s", out)
	}
	h.ok("token", "revoke", "acme-corp", "bob@acme.test", tok.ID)
	if _, err := oauth.ResolveBearer(h.ctx, h.d.Platform(), tok.Token, "http://localhost/mcp"); err == nil {
		t.Fatal("revoked token still resolves")
	}

	h.ok("member", "remove", "acme-corp", "bob@acme.test")
	if msg := h.fails("token", "create", "acme-corp", "bob@acme.test", "x"); !strings.Contains(msg, "not found") {
		t.Fatalf("token for a non-member: %s", msg)
	}
}

func TestUsageErrors(t *testing.T) {
	h := harness{t: t, ctx: context.Background(), d: testdb.New(t)}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"nope", "verb"}, "usage"},
		{[]string{"workspace", "list", "extra"}, "usage"},
		{[]string{"workspace", "create", "X"}, "--owner"},
		{[]string{"workspace", "list", "--read-only"}, "does not take --read-only"},
		{[]string{"token", "create", "a", "b@c.d", "n", "--ttl"}, "needs a value"},
		{[]string{"token", "create", "a", "b@c.d", "n", "--ttl", "2y"}, "--ttl"},
		{[]string{"token", "create", "a", "b@c.d", "n", "--ttl", "400d"}, "--ttl"},
		{[]string{"member", "list", "missing"}, "not found"},
		{[]string{"member", "add", "missing", "b@c.d", "member"}, "not found"},
	} {
		if msg := h.fails(c.args...); !strings.Contains(msg, c.want) {
			t.Errorf("%v: got %q, want it to mention %q", c.args, msg, c.want)
		}
	}
}
