package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jrcoronel-moon/hilo-crm/internal/crm"
	"github.com/jrcoronel-moon/hilo-crm/internal/db"
	"github.com/jrcoronel-moon/hilo-crm/internal/identity"
	"github.com/jrcoronel-moon/hilo-crm/internal/oauth"
	"github.com/jrcoronel-moon/hilo-crm/internal/testdb"
)

type harness struct {
	t     *testing.T
	ctx   context.Context
	d     *db.DB
	stdin string
}

// ok runs a command that must succeed and returns its output.
func (h harness) ok(args ...string) string {
	h.t.Helper()
	var out bytes.Buffer
	if err := run(h.ctx, h.d, strings.NewReader(h.stdin), &out, args); err != nil {
		h.t.Fatalf("hiloctl %s: %v", strings.Join(args, " "), err)
	}
	return out.String()
}

// fails runs a command that must fail and returns the error text.
func (h harness) fails(args ...string) string {
	h.t.Helper()
	var out bytes.Buffer
	err := run(h.ctx, h.d, strings.NewReader(h.stdin), &out, args)
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

// syncBuffer is written by the watch goroutine and read by the test.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestWatchPrintsChangesLive(t *testing.T) {
	h := harness{t: t, ctx: context.Background(), d: testdb.New(t)}
	var ws struct{ ID string }
	h.json(&ws, "workspace", "create", "Acme", "--owner", "ana@acme.test")

	ctx, cancel := context.WithCancel(h.ctx)
	out := &syncBuffer{}
	done := make(chan error, 1)
	go func() { done <- run(ctx, h.d, strings.NewReader(""), out, []string{"watch", "acme"}) }()

	id := identity.New(h.d, slog.New(slog.DiscardHandler), nil)
	ana, err := id.MemberCaller(h.ctx, uuid.MustParse(ws.ID), "ana@acme.test")
	if err != nil {
		t.Fatal(err)
	}
	svc := crm.New(h.d, slog.New(slog.DiscardHandler))
	name := "Globex"
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.String(), "the company «Globex»") { // created, or updated by a retry
		if time.Now().After(deadline) {
			t.Fatalf("watch output: %q", out.String())
		}
		// Repeat the (idempotent) change until the listener has subscribed.
		if _, _, err := svc.UpsertCompany(h.ctx, crm.NewActor(ana.UserID, ana.WorkspaceID, "owner", "pat", "grok", crm.AllScopes), crm.CompanyInput{Name: &name, Domain: strPtr("globex.com")}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(out.String(), "grok") {
		t.Fatalf("actor missing: %q", out.String())
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func strPtr(s string) *string { return &s }

func TestSettingsAndBrandingCommands(t *testing.T) {
	h := harness{t: t, ctx: context.Background(), d: testdb.New(t)}
	h.ok("workspace", "create", "Acme", "--owner", "ana@acme.test")
	if out := h.ok("pipeline", "list", "acme"); !strings.Contains(out, "Lead (initial)") {
		t.Fatalf("pipeline list: %s", out)
	}
	h.stdin = `{"name":"Partners","stages":[{"name":"Nuevo","category":"initial"},{"name":"Activo","category":"in_progress"},
		{"name":"Firmado","category":"won"},{"name":"Caído","category":"lost"}],"monthly_target":5000,"target_currency":"USD"}`
	if out := h.ok("pipeline", "apply", "acme"); !strings.Contains(out, "Partners saved with 4 stages") {
		t.Fatalf("pipeline apply: %s", out)
	}
	h.stdin = `{"name":"Malo","stages":[{"name":"Solo","category":"won"}]}`
	if msg := h.fails("pipeline", "apply", "acme"); !strings.Contains(msg, "stages") {
		t.Fatalf("invalid pipeline: %s", msg)
	}
	h.stdin = `{"name":"X","bogus":1}`
	if msg := h.fails("pipeline", "apply", "acme"); !strings.Contains(msg, "unknown field") {
		t.Fatalf("unknown field: %s", msg)
	}
	h.stdin = ""
	h.ok("channel", "add", "acme", "Webinar", "event")
	if out := h.ok("channel", "list", "acme"); !strings.Contains(out, "Webinar") {
		t.Fatalf("channel list: %s", out)
	}
	if out := h.ok("audit", "list", "acme", "--limit", "5"); !strings.Contains(out, "hiloctl") || !strings.Contains(out, "configured the pipeline «Partners»") {
		t.Fatalf("audit list: %s", out)
	}
	if msg := h.fails("audit", "list", "acme", "--limit", "x"); !strings.Contains(msg, "--limit") {
		t.Fatalf("bad limit: %s", msg)
	}

	h.ok("branding", "name", "Acme CRM")
	var b struct {
		Name       string
		CustomLogo bool `json:"custom_logo"`
	}
	h.json(&b, "branding", "show")
	if b.Name != "Acme CRM" || b.CustomLogo {
		t.Fatalf("branding: %+v", b)
	}
	h.stdin = "not an image"
	h.fails("branding", "logo")
	h.stdin = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"><rect width="1" height="1"/></svg>`
	h.ok("branding", "logo")
	if h.json(&b, "branding", "show"); !b.CustomLogo {
		t.Fatal("logo not stored")
	}
	h.ok("branding", "reset-logo")
	if msg := h.fails("branding", "name", strings.Repeat("x", 61)); msg == "" {
		t.Fatal("long name accepted")
	}
}

// TestExportImportRoundTrip: a workspace exported and imported elsewhere
// exports identically; importing it again where it came from changes nothing.
func TestExportImportRoundTrip(t *testing.T) {
	h := harness{t: t, ctx: context.Background(), d: testdb.New(t)}
	var a struct{ ID string }
	h.json(&a, "workspace", "create", "Alpha", "--owner", "ana@acme.test")
	h.ok("workspace", "create", "Beta", "--owner", "ana@acme.test")

	id := identity.New(h.d, slog.New(slog.DiscardHandler), nil)
	ana, err := id.MemberCaller(h.ctx, uuid.MustParse(a.ID), "ana@acme.test")
	if err != nil {
		t.Fatal(err)
	}
	svc := crm.New(h.d, slog.New(slog.DiscardHandler))
	op := crm.NewActor(ana.UserID, ana.WorkspaceID, "owner", "pat", "seed", crm.AllScopes)
	tags, custom, amount := []string{"pyme"}, map[string]any{"segmento": "B2B"}, 1500.5
	steps := []error{}
	add := func(_ any, _ bool, err error) { steps = append(steps, err) }
	add(svc.UpsertCompany(h.ctx, op, crm.CompanyInput{Name: strPtr("Globex"), Domain: strPtr("globex.test"), Tags: &tags, Custom: &custom, Owner: strPtr("ana@acme.test")}))
	add(svc.UpsertCompany(h.ctx, op, crm.CompanyInput{Name: strPtr("Sin dominio"), ExternalID: strPtr("crm:7")}))
	add(svc.UpsertContact(h.ctx, op, crm.ContactInput{Email: strPtr("hank@globex.test"), FirstName: strPtr("Hank"), Company: strPtr("globex.test")}))
	add(svc.UpsertContact(h.ctx, op, crm.ContactInput{FirstName: strPtr("Sin email"), Company: strPtr("crm:7")}))
	add(svc.UpsertOpportunity(h.ctx, op, crm.OpportunityInput{Title: strPtr("Planta"), Company: strPtr("globex.test"), Amount: &amount,
		Stage: strPtr("Ganado"), CreatedAt: strPtr("2025-01-10"), ClosedAt: strPtr("2025-03-05"),
		Links:    &[]crm.Link{{URL: "https://drive.example.test/planta", Label: "Propuesta"}},
		Contacts: []crm.ContactRole{{Contact: "hank@globex.test", Role: "decisor"}}}))
	add(svc.UpsertOpportunity(h.ctx, op, crm.OpportunityInput{Title: strPtr("Perdida"), Stage: strPtr("Perdido"), LostReason: strPtr("precio"), NextStep: strPtr("nada")}))
	if _, err := svc.LogInteraction(h.ctx, op, crm.InteractionInput{Type: "call", Company: "globex.test", Contact: "hank@globex.test",
		Summary: "Llamada", OccurredAt: "2025-02-01T10:00:00Z", DurationMinutes: intPtr(15)}); err != nil {
		t.Fatal(err)
	}
	for _, err := range steps {
		if err != nil {
			t.Fatal(err)
		}
	}

	alpha := h.ok("export", "alpha")
	h.stdin = alpha
	if out := h.ok("import", "beta", "--dry-run"); !strings.HasPrefix(out, "dry run (nothing saved): 7 ok, 0 failed") {
		t.Fatalf("dry run: %s", out)
	}
	h.stdin = ""
	if out := h.ok("export", "beta"); strings.Contains(out, "Globex") {
		t.Fatal("dry run saved data")
	}
	h.stdin = alpha
	if out := h.ok("import", "beta"); !strings.HasPrefix(out, "imported: 7 ok, 0 failed") {
		t.Fatalf("import: %s", out)
	}
	h.stdin = ""
	if beta := h.ok("export", "beta"); beta != alpha {
		t.Fatalf("round trip differs:\nalpha: %s\nbeta:  %s", alpha, beta)
	}
	// Back where it came from: every record is an update, nothing is created.
	h.stdin = alpha
	var res crm.ImportResult
	h.json(&res, "import", "alpha")
	if res.Failed != 0 {
		t.Fatalf("re-import: %+v", res)
	}
	for _, it := range res.Items {
		if it.Created {
			t.Fatalf("re-import created %+v", it)
		}
	}
	h.stdin = `{"companies":[{"name":"X"}],"bogus":true}`
	if msg := h.fails("import", "alpha"); !strings.Contains(msg, "unknown field") {
		t.Fatalf("bad bundle: %s", msg)
	}
}

func intPtr(i int) *int { return &i }

func TestImportCSV(t *testing.T) {
	h := harness{t: t, ctx: context.Background(), d: testdb.New(t)}
	h.ok("workspace", "create", "Acme", "--owner", "ana@acme.test")

	// Spanish headers are recognised; "Región" has no field and is mapped to custom.
	h.stdin = "Id;Empresa;Dominio;Región\n1;Globex;globex.test;Norte\n2;Initech;;Sur\n"
	out := h.ok("import-csv", "acme", "company", "--source", "hs", "--map", "Región=custom", "--dry-run")
	if !regexp.MustCompile(`Empresa\s+name`).MatchString(out) || !strings.Contains(out, "dry run (nothing saved): 2 rows, 2 ok (2 new, 0 updated), 0 rejected, 0 not converted") {
		t.Fatalf("company dry run: %s", out)
	}
	if out := h.ok("import-csv", "acme", "company", "--source", "hs", "--map", "Región=custom"); !strings.Contains(out, "imported: 2 rows, 2 ok (2 new") {
		t.Fatalf("company import: %s", out)
	}
	// Again: nothing new, nothing duplicated.
	var res struct {
		Mapping              map[string]string
		OK, Created, Updated int
	}
	h.json(&res, "import-csv", "acme", "company", "--source", "hs", "--map", "Región=custom")
	if res.OK != 2 || res.Created != 0 || res.Updated != 2 || res.Mapping["Región"] != "custom" {
		t.Fatalf("re-import: %+v", res)
	}

	// Opportunities: references by source id and by domain, a default pipeline,
	// a bad amount (not converted) and a lost deal without a reason (rejected).
	h.stdin = "Deal ID,Deal Name,Company,Stage,Amount,Close Date,Created\n" +
		"d1,Planta,1,Ganado,\"1.500,50\",03/04/2025,2025-01-10\n" +
		"d2,Rara,globex.test,Nuevo,abc,,\n" +
		"d3,Perdida,2,Perdido,10,,\n"
	out = h.ok("import-csv", "acme", "opportunity", "--source", "hs", "--set", "currency=EUR", "--tags", "migrado")
	if !strings.Contains(out, "imported: 3 rows, 1 ok (1 new, 0 updated), 1 rejected, 1 not converted") ||
		!regexp.MustCompile(`3\s+amount\s+"abc" is not an amount`).MatchString(out) || !strings.Contains(out, "lost_reason") {
		t.Fatalf("opportunity import: %s", out)
	}
	svc := crm.New(h.d, slog.New(slog.DiscardHandler))
	id := identity.New(h.d, slog.New(slog.DiscardHandler), nil)
	ws, err := id.FindWorkspace(h.ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	op := crm.NewActor(uuid.Nil, ws.ID, "owner", "system", "test", crm.AllScopes)
	o, err := svc.GetOpportunity(h.ctx, op, "hs:d1")
	if err != nil || o.CompanyName != "Globex" || o.Amount != 1500.5 || o.Currency != "EUR" || o.Status != "won" ||
		o.CreatedAt.UTC().Format("2006-01-02") != "2025-01-10" || strings.Join(o.Tags, ",") != "migrado" {
		t.Fatalf("imported deal: %+v %v", o, err)
	}

	h.stdin = "a,b\n1,2\n"
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"import-csv", "acme", "thing"}, "kind must be"},
		{[]string{"import-csv", "acme", "company"}, "name must be mapped"},
		{[]string{"import-csv", "acme", "company", "--map", "zzz=name"}, "no column"},
		{[]string{"import-csv", "acme", "company", "--map", "a=name", "--dates", "ymd"}, "--dates"},
		{[]string{"import-csv", "acme", "company", "--map", "a=name", "--set", "nope"}, "--set"},
		{[]string{"import-csv", "acme", "interaction", "--map", "a=custom"}, "custom"},
	} {
		if msg := h.fails(c.args...); !strings.Contains(msg, c.want) {
			t.Errorf("%v: got %q, want %q", c.args, msg, c.want)
		}
	}
}
