package mcpserver_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jrcoronel-moon/hilo-crm/internal/config"
	"github.com/jrcoronel-moon/hilo-crm/internal/crm"
	"github.com/jrcoronel-moon/hilo-crm/internal/identity"
	"github.com/jrcoronel-moon/hilo-crm/internal/mcpserver"
	"github.com/jrcoronel-moon/hilo-crm/internal/testdb"
)

type env struct {
	t   *testing.T
	ctx context.Context
	id  *identity.Service
	url string
}

func setup(t *testing.T) *env {
	t.Helper()
	d := testdb.New(t)
	log := slog.New(slog.DiscardHandler)
	cfg := &config.Config{BaseURL: "http://localhost:8080"}
	srv := mcpserver.New(cfg, d, crm.New(d, log), log)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &env{t: t, ctx: context.Background(), id: identity.New(d, log, map[string]bool{"root@hilo.test": true}), url: ts.URL}
}

// token creates a workspace member with role and returns a PAT for them.
func (e *env) token(ws identity.Caller, email, role string, readOnly bool) string {
	e.t.Helper()
	if role != "owner" {
		if err := e.id.Invite(e.ctx, ws, email, role); err != nil {
			e.t.Fatal(err)
		}
	}
	u, err := e.id.Login(e.ctx, identity.ExternalIdentity{Provider: "dev", Subject: email, Email: email, EmailVerified: true})
	if err != nil {
		e.t.Fatal(err)
	}
	tok, _, err := e.id.CreatePAT(e.ctx, identity.Caller{UserID: u.ID, WorkspaceID: ws.WorkspaceID, Role: role}, "test", readOnly, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	return tok
}

func (e *env) workspace(name string) identity.Caller {
	e.t.Helper()
	u, err := e.id.Login(e.ctx, identity.ExternalIdentity{Provider: "dev", Subject: "root", Email: "root@hilo.test", EmailVerified: true})
	if err != nil {
		e.t.Fatal(err)
	}
	ws, err := e.id.CreateWorkspace(e.ctx, u.ID, name)
	if err != nil {
		e.t.Fatal(err)
	}
	return identity.Caller{UserID: u.ID, WorkspaceID: ws.ID, Role: "owner"}
}

type bearer struct{ tok string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.tok)
	return http.DefaultTransport.RoundTrip(r)
}

func (e *env) connect(tok string) *mcp.ClientSession {
	e.t.Helper()
	c := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "1"}, nil)
	ctx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
	defer cancel()
	cs, err := c.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: e.url + "/mcp", HTTPClient: &http.Client{Transport: bearer{tok}}, MaxRetries: -1}, nil)
	if err != nil {
		e.t.Fatalf("connect: %v", err)
	}
	e.t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func toolNames(t *testing.T, cs *mcp.ClientSession) []string {
	t.Helper()
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range res.Tools {
		names = append(names, tl.Name)
	}
	return names
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res.Content[0].(*mcp.TextContent).Text, res.IsError
}

func TestUnauthenticated(t *testing.T) {
	e := setup(t)
	for _, auth := range []string{"", "Bearer nope", "Bearer hilo_pat_" + strings.Repeat("x", 43), "Basic abc"} {
		req, _ := http.NewRequestWithContext(e.ctx, http.MethodPost, e.url+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		wa := res.Header.Get("WWW-Authenticate")
		if res.StatusCode != http.StatusUnauthorized || !strings.Contains(wa, `resource_metadata="http://localhost:8080/.well-known/oauth-protected-resource/mcp"`) {
			t.Fatalf("%q: status %d, WWW-Authenticate %q", auth, res.StatusCode, wa)
		}
	}
}

func TestToolsFilteredByPermission(t *testing.T) {
	e := setup(t)
	ws := e.workspace("Acme")
	owner := toolNames(t, e.connect(e.token(ws, "root@hilo.test", "owner", false)))
	viewer := toolNames(t, e.connect(e.token(ws, "v@x.com", "viewer", false)))
	member := toolNames(t, e.connect(e.token(ws, "m@x.com", "member", false)))
	readOnly := toolNames(t, e.connect(e.token(ws, "a@x.com", "admin", true)))

	if len(owner) != 22 || !slices.Contains(owner, "bulk_upsert") || !slices.Contains(owner, "configure_pipeline") || !slices.Contains(owner, "list_interactions") || !slices.Contains(owner, "list_tags") {
		t.Fatalf("owner tools (%d): %v", len(owner), owner)
	}
	for _, list := range [][]string{viewer, readOnly} {
		if slices.Contains(list, "upsert_company") || slices.Contains(list, "configure_pipeline") || !slices.Contains(list, "get_metrics") {
			t.Fatalf("read-only caller sees write tools: %v", list)
		}
	}
	if !slices.Contains(member, "log_interaction") || slices.Contains(member, "configure_pipeline") {
		t.Fatalf("member tools: %v", member)
	}
}

func TestAgentFlow(t *testing.T) {
	e := setup(t)
	ws := e.workspace("Acme")
	cs := e.connect(e.token(ws, "root@hilo.test", "owner", false))

	out, isErr := call(t, cs, "upsert_company", map[string]any{"name": "Globex", "domain": "globex.com", "external_id": "c-1"})
	if isErr || !strings.Contains(out, `"created":true`) {
		t.Fatalf("upsert_company: %s", out)
	}
	if out, isErr = call(t, cs, "upsert_company", map[string]any{"external_id": "c-1", "industry": "Energy"}); isErr || !strings.Contains(out, `"created":false`) {
		t.Fatalf("idempotent upsert: %s", out)
	}
	if _, isErr = call(t, cs, "upsert_contact", map[string]any{"email": "hank@globex.com", "first_name": "Hank", "company": "globex.com"}); isErr {
		t.Fatal("upsert_contact failed")
	}
	out, isErr = call(t, cs, "upsert_opportunity", map[string]any{"title": "Planta solar", "company": "globex.com", "amount": 25000.5, "currency": "USD", "primary_contact": "hank@globex.com"})
	if isErr || !strings.Contains(out, `"stage":"Lead"`) {
		t.Fatalf("upsert_opportunity: %s", out)
	}
	var created struct {
		Record crm.OpportunityView `json:"record"`
	}
	_ = json.Unmarshal([]byte(out), &created)
	id := created.Record.ID.String()

	if out, isErr = call(t, cs, "move_opportunity_stage", map[string]any{"opportunity": id, "stage": "Perdido"}); !isErr || !strings.Contains(out, "lost_reason") {
		t.Fatalf("lost without reason should be a tool error: %s", out)
	}
	if out, isErr = call(t, cs, "move_opportunity_stage", map[string]any{"opportunity": id, "stage": "Propuesta"}); isErr || !strings.Contains(out, `"status":"open"`) {
		t.Fatalf("move: %s", out)
	}
	if out, isErr = call(t, cs, "log_interaction", map[string]any{"type": "meeting", "opportunity": id, "summary": "Demo", "channel": "Email"}); isErr {
		t.Fatalf("log_interaction: %s", out)
	}
	if out, isErr = call(t, cs, "get_timeline", map[string]any{"opportunity": id}); isErr || !strings.Contains(out, "Demo") || !strings.Contains(out, "Propuesta") {
		t.Fatalf("timeline: %s", out)
	}
	if out, isErr = call(t, cs, "get_metrics", map[string]any{}); isErr || !strings.Contains(out, `"open_count":1`) {
		t.Fatalf("metrics: %s", out)
	}
	if out, isErr = call(t, cs, "search_crm", map[string]any{"query": "glob"}); isErr || !strings.Contains(out, "Globex") {
		t.Fatalf("search: %s", out)
	}
	batch := map[string]any{"dry_run": true,
		"companies": []any{map[string]any{"name": "Initech", "external_id": "x:1"}},
		"contacts":  []any{map[string]any{"email": "peter@initech.test", "company": "x:1"}}}
	if out, isErr = call(t, cs, "bulk_upsert", batch); isErr || !strings.Contains(out, `"ok":2`) || !strings.Contains(out, `"dry_run":true`) {
		t.Fatalf("bulk_upsert dry run: %s", out)
	}
	if out, _ = call(t, cs, "search_crm", map[string]any{"query": "initech"}); strings.Contains(out, "Initech") {
		t.Fatalf("dry run saved: %s", out)
	}
	// Input-schema validation rejects ill-typed arguments before the service.
	res, err := cs.CallTool(e.ctx, &mcp.CallToolParams{Name: "log_interaction", Arguments: map[string]any{"type": 5}})
	if err == nil && !res.IsError {
		t.Fatal("ill-typed argument accepted")
	}
}

func TestCrossWorkspaceTokenSeesNothing(t *testing.T) {
	e := setup(t)
	a := e.workspace("A")
	b := e.workspace("B")
	ca := e.connect(e.token(a, "root@hilo.test", "owner", false))
	out, _ := call(t, ca, "upsert_company", map[string]any{"name": "Secret", "domain": "secret.com"})
	var created struct {
		Record crm.CompanyView `json:"record"`
	}
	_ = json.Unmarshal([]byte(out), &created)

	cb := e.connect(e.token(b, "bob@x.com", "member", false))
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"get_company", map[string]any{"ref": created.Record.ID.String()}},
		{"get_company", map[string]any{"ref": "secret.com"}},
		{"upsert_company", map[string]any{"id": created.Record.ID.String(), "name": "pwned"}},
		{"archive_record", map[string]any{"type": "company", "id": created.Record.ID.String()}},
	} {
		if out, isErr := call(t, cb, c.tool, c.args); !isErr || !strings.Contains(out, "not found") {
			t.Fatalf("%s from another workspace: %s", c.tool, out)
		}
	}
	if out, _ := call(t, cb, "search_crm", map[string]any{"query": "secret"}); out != "[]" {
		t.Fatalf("search leaked: %s", out)
	}
}

func TestRevokedTokenStopsWorking(t *testing.T) {
	e := setup(t)
	ws := e.workspace("Acme")
	tok := e.token(ws, "m@x.com", "member", false)
	cs := e.connect(tok)
	if _, isErr := call(t, cs, "list_pipelines", map[string]any{}); isErr {
		t.Fatal("list_pipelines failed")
	}
	members, _ := e.id.Members(e.ctx, ws)
	for _, m := range members {
		if m.Email == "m@x.com" {
			if err := e.id.RemoveMember(e.ctx, ws, m.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := cs.CallTool(e.ctx, &mcp.CallToolParams{Name: "list_pipelines", Arguments: map[string]any{}}); err == nil {
		t.Fatal("token of a removed member still works")
	}
}
