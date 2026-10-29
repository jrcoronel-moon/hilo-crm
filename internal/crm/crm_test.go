package crm_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/jrcoronel-moon/hilo-crm/internal/crm"
	"github.com/jrcoronel-moon/hilo-crm/internal/db"
	"github.com/jrcoronel-moon/hilo-crm/internal/identity"
	"github.com/jrcoronel-moon/hilo-crm/internal/testdb"
)

type fixture struct {
	t   *testing.T
	ctx context.Context
	db  *db.DB
	crm *crm.Service
	id  *identity.Service
}

func setup(t *testing.T) *fixture {
	t.Helper()
	curT = t
	d := testdb.New(t)
	log := slog.New(slog.DiscardHandler)
	return &fixture{t: t, ctx: context.Background(), db: d, crm: crm.New(d, log), id: identity.New(d, log, map[string]bool{"root@hilo.test": true})}
}

// workspace creates a workspace owned by a fresh superadmin user and returns
// an actor with the given role and scopes for a member of it.
func (f *fixture) workspace(name string) (owner crm.Actor, wsID uuid.UUID) {
	f.t.Helper()
	u, err := f.id.Login(f.ctx, identity.ExternalIdentity{Provider: "dev", Subject: "root", Email: "root@hilo.test", EmailVerified: true})
	if err != nil {
		f.t.Fatal(err)
	}
	ws, err := f.id.CreateWorkspace(f.ctx, u.ID, name)
	if err != nil {
		f.t.Fatal(err)
	}
	return crm.NewActor(u.ID, ws.ID, "owner", "session", "", ""), ws.ID
}

func (f *fixture) member(ws uuid.UUID, owner crm.Actor, email, role, scopes string) crm.Actor {
	f.t.Helper()
	c := identity.Caller{UserID: owner.UserID, WorkspaceID: ws, Role: "owner"}
	if err := f.id.Invite(f.ctx, c, email, map[string]string{"owner": "admin"}[role]+strings.TrimPrefix(role, "owner")); err != nil {
		f.t.Fatal(err)
	}
	u, err := f.id.Login(f.ctx, identity.ExternalIdentity{Provider: "dev", Subject: email, Email: email, EmailVerified: true})
	if err != nil {
		f.t.Fatal(err)
	}
	return crm.NewActor(u.ID, ws, role, "oauth", "test-agent", scopes)
}

// curT is the running test; tests in this package are not parallel, so the
// helpers below can report through it (Go does not allow passing t together
// with a multi-value call).
var curT *testing.T

func must[T any](v T, err error) T {
	if err != nil {
		curT.Helper()
		curT.Fatal(err)
	}
	return v
}

func must2[T any](v T, _ bool, err error) T {
	if err != nil {
		curT.Helper()
		curT.Fatal(err)
	}
	return v
}

func s(v string) *string { return &v }

func isValidation(err error) bool {
	var ve *crm.ValidationError
	return errors.As(err, &ve)
}

func TestSeededPipeline(t *testing.T) {
	f := setup(t)
	a, _ := f.workspace("Acme")
	ps := must(f.crm.ListPipelines(f.ctx, a))
	if len(ps) != 1 || !ps[0].IsDefault || len(ps[0].Stages) != 6 {
		t.Fatalf("unexpected seed: %+v", ps)
	}
	cats := []string{}
	for _, st := range ps[0].Stages {
		cats = append(cats, st.Category)
	}
	if strings.Join(cats, ",") != "initial,in_progress,in_progress,in_progress,won,lost" {
		t.Fatalf("categories %v", cats)
	}
	if ch := must(f.crm.ListChannels(f.ctx, a)); len(ch) != len(crm.DefaultChannels) {
		t.Fatalf("channels %d", len(ch))
	}
}

func TestUpsertsAreIdempotent(t *testing.T) {
	f := setup(t)
	a, _ := f.workspace("Acme")
	c1, created, err := f.crm.UpsertCompany(f.ctx, a, crm.CompanyInput{Name: s("Globex"), Domain: s("https://www.Globex.com/about")})
	if err != nil || !created || c1.Domain != "globex.com" {
		t.Fatalf("create: %v %v %+v", err, created, c1)
	}
	c2, created, err := f.crm.UpsertCompany(f.ctx, a, crm.CompanyInput{Domain: s("globex.com"), Industry: s("Energy")})
	if err != nil || created || c2.ID != c1.ID || c2.Name != "Globex" || c2.Industry != "Energy" {
		t.Fatalf("update by domain: %v %v %+v", err, created, c2)
	}
	ct := must2(f.crm.UpsertContact(f.ctx, a, crm.ContactInput{Email: s("Hank@Globex.com"), FirstName: s("Hank"), Company: s("globex.com")}))
	ct2 := must2(f.crm.UpsertContact(f.ctx, a, crm.ContactInput{Email: s("hank@globex.com"), Title: s("CEO")}))
	if ct.ID != ct2.ID || ct2.FirstName != "Hank" || ct2.Title != "CEO" || *ct2.CompanyID != c1.ID {
		t.Fatalf("contact upsert: %+v", ct2)
	}
	o1 := must2(f.crm.UpsertOpportunity(f.ctx, a, crm.OpportunityInput{ExternalID: s("ext-1"), Title: s("Deal"), Company: s("globex.com")}))
	o2 := must2(f.crm.UpsertOpportunity(f.ctx, a, crm.OpportunityInput{ExternalID: s("ext-1"), Title: s("Deal v2")}))
	if o1.ID != o2.ID || o2.Title != "Deal v2" || o1.StageCategory != crm.CatInitial || o1.Status != "open" {
		t.Fatalf("opportunity upsert: %+v / %+v", o1, o2)
	}
	// Clearing a reference with "".
	o3 := must2(f.crm.UpsertOpportunity(f.ctx, a, crm.OpportunityInput{ID: s(o1.ID.String()), Company: s("")}))
	if o3.CompanyID != nil {
		t.Fatal("empty string must clear the company")
	}
}

func TestStageMovesAndStatus(t *testing.T) {
	f := setup(t)
	a, _ := f.workspace("Acme")
	amount := 1234.56
	o := must2(f.crm.UpsertOpportunity(f.ctx, a, crm.OpportunityInput{Title: s("Big deal"), Amount: &amount, Currency: s("eur")}))
	if o.Amount != 1234.56 || o.Currency != "EUR" || o.Probability != 10 {
		t.Fatalf("amount/currency/probability: %+v", o)
	}
	if _, err := f.crm.MoveOpportunityStage(f.ctx, a, crm.MoveInput{Opportunity: o.ID.String(), Stage: "perdido"}); !isValidation(err) {
		t.Fatalf("lost without reason must fail, got %v", err)
	}
	if _, err := f.crm.MoveOpportunityStage(f.ctx, a, crm.MoveInput{Opportunity: o.ID.String(), Stage: "Nope"}); !isValidation(err) || !strings.Contains(err.Error(), "Propuesta") {
		t.Fatalf("unknown stage should list valid ones, got %v", err)
	}
	lost := must(f.crm.MoveOpportunityStage(f.ctx, a, crm.MoveInput{Opportunity: o.ID.String(), Stage: "Perdido", Reason: "budget"}))
	if lost.Status != "lost" || lost.ClosedAt == nil || lost.LostReason != "budget" {
		t.Fatalf("lost: %+v", lost)
	}
	reopened := must(f.crm.MoveOpportunityStage(f.ctx, a, crm.MoveInput{Opportunity: o.ID.String(), Stage: "Propuesta"}))
	if reopened.Status != "open" || reopened.ClosedAt != nil || reopened.LostReason != "" {
		t.Fatalf("reopen: %+v", reopened)
	}
	won := must(f.crm.MoveOpportunityStage(f.ctx, a, crm.MoveInput{Opportunity: o.ID.String(), Stage: "Ganado"}))
	if won.Status != "won" || won.ClosedAt == nil {
		t.Fatalf("won: %+v", won)
	}
	tl := must(f.crm.Timeline(f.ctx, a, crm.TimelineInput{Opportunity: o.ID.String()}))
	if len(tl) != 4 || tl[0].Label != "Ganado" || tl[len(tl)-1].Label != "Lead" {
		t.Fatalf("history: %+v", tl)
	}
	if _, err := f.crm.MoveOpportunityStage(f.ctx, a, crm.MoveInput{Opportunity: "no-such-external-id", Stage: "Lead"}); !errors.Is(err, crm.ErrNotFound) {
		t.Fatalf("move of unknown opportunity must be not-found (never a create), got %v", err)
	}
}

func TestConfigurePipelineRules(t *testing.T) {
	f := setup(t)
	a, _ := f.workspace("Acme")
	p := func(v int) *int { return &v }
	bad := [][]crm.StageInput{
		{{Name: "A", Category: "initial"}, {Name: "W", Category: "won"}, {Name: "L", Category: "lost"}, {Name: "L2", Category: "lost"}}, // no in_progress
		{{Name: "A", Category: "initial"}, {Name: "B", Category: "initial"}, {Name: "W", Category: "won"}, {Name: "L", Category: "lost"}, {Name: "P", Category: "in_progress"}},
		{{Name: "A", Category: "initial"}, {Name: "P", Category: "in_progress"}, {Name: "L", Category: "lost"}, {Name: "L2", Category: "lost"}}, // no won
		{{Name: "A", Category: "initial"}, {Name: "a", Category: "in_progress"}, {Name: "W", Category: "won"}, {Name: "L", Category: "lost"}},   // dup name
		{{Name: "A", Category: "initial"}, {Name: "P", Category: "doing"}, {Name: "W", Category: "won"}, {Name: "L", Category: "lost"}},         // bad category
		{{Name: "A", Category: "initial"}, {Name: "P", Category: "in_progress", Probability: p(101)}, {Name: "W", Category: "won"}, {Name: "L", Category: "lost"}},
	}
	for i, st := range bad {
		if _, err := f.crm.ConfigurePipeline(f.ctx, a, crm.PipelineInput{Name: "X", Stages: st}); !isValidation(err) {
			t.Fatalf("case %d: expected validation error, got %v", i, err)
		}
	}
	// A minimal valid custom pipeline: 1 initial, 1 in_progress, 1 won, 1 lost.
	pv := must(f.crm.ConfigurePipeline(f.ctx, a, crm.PipelineInput{Name: "Proyectos", Stages: []crm.StageInput{
		{Name: "Brief", Category: "initial"}, {Name: "Ejecución", Category: "in_progress"},
		{Name: "Entregado", Category: "won"}, {Name: "Cancelado", Category: "lost"},
	}}))
	o := must2(f.crm.UpsertOpportunity(f.ctx, a, crm.OpportunityInput{Title: s("Web"), Kind: s("project"), Pipeline: s("proyectos")}))
	if o.StageName != "Brief" || o.PipelineID != pv.ID {
		t.Fatalf("opportunity should start in Brief: %+v", o)
	}
	exec := pv.Stages[1].ID.String()
	o = must(f.crm.MoveOpportunityStage(f.ctx, a, crm.MoveInput{Opportunity: o.ID.String(), Stage: exec}))

	// Removing a stage that holds opportunities requires a reassignment.
	newStages := []crm.StageInput{
		{ID: pv.Stages[0].ID.String(), Name: "Brief", Category: "initial"},
		{Name: "Diseño", Category: "in_progress"}, {Name: "Desarrollo", Category: "in_progress"},
		{ID: pv.Stages[2].ID.String(), Name: "Entregado", Category: "won"},
		{ID: pv.Stages[3].ID.String(), Name: "Cancelado", Category: "lost"},
	}
	if _, err := f.crm.ConfigurePipeline(f.ctx, a, crm.PipelineInput{ID: pv.ID.String(), Name: "Proyectos", Stages: newStages}); !isValidation(err) {
		t.Fatalf("expected reassign error, got %v", err)
	}
	pv2 := must(f.crm.ConfigurePipeline(f.ctx, a, crm.PipelineInput{ID: pv.ID.String(), Name: "Proyectos", Stages: newStages,
		Reassign: map[string]string{exec: "desarrollo"}}))
	if len(pv2.Stages) != 5 || pv2.Stages[2].Name != "Desarrollo" {
		t.Fatalf("reconfigured: %+v", pv2.Stages)
	}
	got := must(f.crm.GetOpportunity(f.ctx, a, o.ID.String()))
	if got.StageName != "Desarrollo" {
		t.Fatalf("opportunity not reassigned: %s", got.StageName)
	}
	// Re-categorising the stage it sits in updates its status.
	newStages[2] = crm.StageInput{ID: pv2.Stages[2].ID.String(), Name: "Desarrollo", Category: "won"}
	must(f.crm.ConfigurePipeline(f.ctx, a, crm.PipelineInput{ID: pv.ID.String(), Name: "Proyectos", Stages: newStages}))
	if got = must(f.crm.GetOpportunity(f.ctx, a, o.ID.String())); got.Status != "won" || got.ClosedAt == nil {
		t.Fatalf("status should follow the stage category: %+v", got)
	}
	// Swapping two stage names in one go must not trip unique indexes.
	newStages[1].Name, newStages[2].Name = "Desarrollo", "Diseño"
	newStages[1].ID = ""
	must(f.crm.ConfigurePipeline(f.ctx, a, crm.PipelineInput{ID: pv.ID.String(), Name: "Proyectos", Stages: newStages}))
	if err := f.crm.ArchivePipeline(f.ctx, a, "Ventas"); !isValidation(err) {
		t.Fatalf("default pipeline must not be archivable, got %v", err)
	}
}

func TestPermissions(t *testing.T) {
	f := setup(t)
	owner, ws := f.workspace("Acme")
	viewer := f.member(ws, owner, "viewer@x.com", "viewer", "")
	member := f.member(ws, owner, "member@x.com", "member", "")
	readToken := f.member(ws, owner, "admin@x.com", "admin", "crm:read")

	if _, _, err := f.crm.UpsertCompany(f.ctx, viewer, crm.CompanyInput{Name: s("X")}); !errors.Is(err, crm.ErrForbidden) {
		t.Fatalf("viewer wrote: %v", err)
	}
	if _, _, err := f.crm.UpsertCompany(f.ctx, readToken, crm.CompanyInput{Name: s("X")}); !errors.Is(err, crm.ErrForbidden) {
		t.Fatalf("read-scoped token of an admin wrote: %v", err)
	}
	if _, _, err := f.crm.UpsertCompany(f.ctx, member, crm.CompanyInput{Name: s("X")}); err != nil {
		t.Fatalf("member write: %v", err)
	}
	if _, err := f.crm.ConfigurePipeline(f.ctx, member, crm.PipelineInput{Name: "P", Stages: crm.DefaultStages()}); !errors.Is(err, crm.ErrForbidden) {
		t.Fatalf("member configured a pipeline: %v", err)
	}
	if _, err := f.crm.ListPipelines(f.ctx, viewer); err != nil {
		t.Fatalf("viewer read: %v", err)
	}
	// Owner set via the owner field must be a member of the workspace.
	if _, _, err := f.crm.UpsertCompany(f.ctx, member, crm.CompanyInput{Name: s("Y"), Owner: s("stranger@x.com")}); !errors.Is(err, crm.ErrNotFound) {
		t.Fatalf("non-member owner accepted: %v", err)
	}
	if c := must2(f.crm.UpsertCompany(f.ctx, member, crm.CompanyInput{Name: s("Z"), Owner: s("viewer@x.com")})); c.OwnerID == nil || *c.OwnerID != viewer.UserID {
		t.Fatalf("owner by email: %+v", c)
	}
}

func TestTenantIsolation(t *testing.T) {
	f := setup(t)
	a, _ := f.workspace("A")
	b, _ := f.workspace("B")
	ca := must2(f.crm.UpsertCompany(f.ctx, a, crm.CompanyInput{Name: s("Secret A"), Domain: s("secret-a.com")}))
	oa := must2(f.crm.UpsertOpportunity(f.ctx, a, crm.OpportunityInput{Title: s("A deal"), ExternalID: s("shared-ext")}))

	if _, err := f.crm.GetCompany(f.ctx, b, ca.ID.String()); !errors.Is(err, crm.ErrNotFound) {
		t.Fatalf("B read A's company by id: %v", err)
	}
	if _, err := f.crm.GetCompany(f.ctx, b, "secret-a.com"); !errors.Is(err, crm.ErrNotFound) {
		t.Fatalf("B read A's company by domain: %v", err)
	}
	if _, err := f.crm.GetOpportunity(f.ctx, b, oa.ID.String()); !errors.Is(err, crm.ErrNotFound) {
		t.Fatalf("B read A's opportunity: %v", err)
	}
	if _, _, err := f.crm.UpsertContact(f.ctx, b, crm.ContactInput{Email: s("x@y.com"), Company: s(ca.ID.String())}); !errors.Is(err, crm.ErrNotFound) {
		t.Fatalf("B linked A's company: %v", err)
	}
	if _, _, err := f.crm.UpsertCompany(f.ctx, b, crm.CompanyInput{ID: s(ca.ID.String()), Name: s("pwned")}); !errors.Is(err, crm.ErrNotFound) {
		t.Fatalf("B updated A's company: %v", err)
	}
	// Same external_id in B creates B's own record, never touches A's.
	ob := must2(f.crm.UpsertOpportunity(f.ctx, b, crm.OpportunityInput{Title: s("B deal"), ExternalID: s("shared-ext")}))
	if ob.ID == oa.ID {
		t.Fatal("external_id matched across workspaces")
	}
	if _, err := f.crm.Timeline(f.ctx, b, crm.TimelineInput{Opportunity: oa.ID.String()}); err != nil {
		t.Fatal(err)
	} else if tl := must(f.crm.Timeline(f.ctx, b, crm.TimelineInput{Opportunity: oa.ID.String()})); len(tl) != 0 {
		t.Fatalf("B saw A's timeline: %+v", tl)
	}
	if r := must(f.crm.Search(f.ctx, b, "secret", 10)); len(r) != 0 {
		t.Fatalf("B found A's data: %+v", r)
	}
	if err := f.crm.Archive(f.ctx, b, crm.ArchiveInput{Type: "company", ID: ca.ID.String()}); !errors.Is(err, crm.ErrNotFound) {
		t.Fatalf("B archived A's company: %v", err)
	}
	if _, err := f.crm.LogInteraction(f.ctx, b, crm.InteractionInput{Type: "note", Opportunity: oa.ID.String()}); !errors.Is(err, crm.ErrNotFound) {
		t.Fatalf("B logged on A's opportunity: %v", err)
	}
}

func TestInteractionsAndSearch(t *testing.T) {
	f := setup(t)
	a, _ := f.workspace("Acme")
	co := must2(f.crm.UpsertCompany(f.ctx, a, crm.CompanyInput{Name: s("100% Real_Co"), Domain: s("real.co")}))
	ct := must2(f.crm.UpsertContact(f.ctx, a, crm.ContactInput{Email: s("ana@real.co"), Company: s("real.co")}))
	o := must2(f.crm.UpsertOpportunity(f.ctx, a, crm.OpportunityInput{Title: s("Deal")}))
	in := crm.InteractionInput{Type: "email", Direction: "inbound", Contact: ct.Email, Opportunity: o.ID.String(), ExternalID: "msg-1", Channel: "Email", Summary: "hola"}
	i1 := must(f.crm.LogInteraction(f.ctx, a, in))
	i2 := must(f.crm.LogInteraction(f.ctx, a, in))
	if i1.ID != i2.ID || !i2.Duplicate || i1.CompanyID == nil || *i1.CompanyID != co.ID {
		t.Fatalf("dedupe/company inference: %+v %+v", i1, i2)
	}
	if got := must(f.crm.GetOpportunity(f.ctx, a, o.ID.String())); got.LastInteractionAt == nil || got.Interactions != 1 {
		t.Fatalf("last_interaction_at not set: %+v", got)
	}
	for _, typ := range []string{"social", "form", "message", "call", "meeting", "note"} {
		must(f.crm.LogInteraction(f.ctx, a, crm.InteractionInput{Type: typ, Company: "real.co"}))
	}
	if _, err := f.crm.LogInteraction(f.ctx, a, crm.InteractionInput{Type: "fax", Company: "real.co"}); !isValidation(err) {
		t.Fatalf("bad type accepted: %v", err)
	}
	if _, err := f.crm.LogInteraction(f.ctx, a, crm.InteractionInput{Type: "note"}); !isValidation(err) {
		t.Fatalf("unlinked interaction accepted: %v", err)
	}
	// LIKE wildcards are matched literally: unescaped, "0%" would also match
	// "Plain 0 Co" and "l_co" would match "lxco".
	must2(f.crm.UpsertCompany(f.ctx, a, crm.CompanyInput{Name: s("Plain 0 Co lxco")}))
	if r := must(f.crm.Search(f.ctx, a, "0%", 10)); len(r) != 1 || r[0].ID != co.ID {
		t.Fatalf("%% should match only the literal: %+v", r)
	}
	if r := must(f.crm.Search(f.ctx, a, "l_co", 10)); len(r) != 1 || r[0].ID != co.ID {
		t.Fatalf("_ should match literally: %+v", r)
	}
}

func TestMetrics(t *testing.T) {
	f := setup(t)
	a, _ := f.workspace("Acme")
	amt := func(v float64) *float64 { return &v }
	o1 := must2(f.crm.UpsertOpportunity(f.ctx, a, crm.OpportunityInput{Title: s("1"), Amount: amt(1000), Stage: s("Propuesta"), SourceChannel: s("Web")}))
	must2(f.crm.UpsertOpportunity(f.ctx, a, crm.OpportunityInput{Title: s("2"), Amount: amt(500), Probability: func() *int { v := 20; return &v }()}))
	o3 := must2(f.crm.UpsertOpportunity(f.ctx, a, crm.OpportunityInput{Title: s("3"), Amount: amt(300)}))
	must(f.crm.MoveOpportunityStage(f.ctx, a, crm.MoveInput{Opportunity: o3.ID.String(), Stage: "Ganado"}))
	o4 := must2(f.crm.UpsertOpportunity(f.ctx, a, crm.OpportunityInput{Title: s("4")}))
	must(f.crm.MoveOpportunityStage(f.ctx, a, crm.MoveInput{Opportunity: o4.ID.String(), Stage: "Perdido", Reason: "x"}))
	_ = o1
	m := must(f.crm.Metrics(f.ctx, a, crm.MetricsInput{}))
	if m.OpenCount != 2 || m.OpenAmount["USD"] != 1500 || m.Weighted["USD"] != 600 { // 1000*50% + 500*20%
		t.Fatalf("open: %+v", m)
	}
	if m.WonCount != 1 || m.LostCount != 1 || m.WinRate != 0.5 || m.WonAmount["USD"] != 300 {
		t.Fatalf("closed: %+v", m)
	}
	found := false
	for _, c := range m.LeadsByChan {
		found = found || (c.Channel == "Web" && c.Count == 1)
	}
	if !found {
		t.Fatalf("channel breakdown: %+v", m.LeadsByChan)
	}
	if m.StaleCount != 0 {
		t.Fatalf("nothing should be stale yet: %d", m.StaleCount)
	}
	if _, cols, err := f.crm.Board(f.ctx, a, crm.BoardFilter{}); err != nil || len(cols) != 6 || len(cols[2].Cards) != 1 {
		t.Fatalf("board: %v %+v", err, cols)
	}
}

func TestCaptureLeadNeverOverwrites(t *testing.T) {
	f := setup(t)
	a, _ := f.workspace("Acme")
	must2(f.crm.UpsertContact(f.ctx, a, crm.ContactInput{Email: s("ceo@bigcorp.com"), FirstName: s("Real"), LastName: s("Name")}))
	form := must(f.crm.CreateLeadForm(f.ctx, a, crm.LeadFormInput{Name: "Web", Channel: "Formulario", AllowedOrigins: []string{"https://www.acme.com/"}}))
	info := must(f.crm.LookupForm(f.ctx, form.PublicKey))
	if info.WorkspaceID != a.WorkspaceID {
		t.Fatal("lookup returned the wrong workspace")
	}
	oppID := must(f.crm.CaptureLead(f.ctx, info, crm.LeadSubmission{
		Email: "CEO@bigcorp.com", FirstName: "Attacker", LastName: "Overwrite", Company: "BigCorp", Message: "Quiero una demo",
		Extra: map[string]string{"budget": "10k"},
	}))
	c := must(f.crm.GetContact(f.ctx, a, "ceo@bigcorp.com"))
	if c.FirstName != "Real" || c.LastName != "Name" {
		t.Fatalf("form overwrote an existing contact: %+v", c)
	}
	o := must(f.crm.GetOpportunity(f.ctx, a, oppID.String()))
	if o.StageCategory != crm.CatInitial || o.CompanyName != "BigCorp" || o.PrimaryContactID == nil || *o.PrimaryContactID != c.ID || o.Interactions != 1 {
		t.Fatalf("lead opportunity: %+v", o)
	}
	tl := must(f.crm.Timeline(f.ctx, a, crm.TimelineInput{Opportunity: oppID.String()}))
	if !strings.Contains(tl[0].Summary, "budget: 10k") || tl[0].Detail != "inbound" {
		t.Fatalf("form interaction: %+v", tl[0])
	}
	// Personal email domains never become companies.
	must(f.crm.CaptureLead(f.ctx, info, crm.LeadSubmission{Email: "someone@gmail.com"}))
	if _, err := f.crm.GetCompany(f.ctx, a, "gmail.com"); !errors.Is(err, crm.ErrNotFound) {
		t.Fatalf("gmail.com became a company: %v", err)
	}
	if err := f.crm.SetLeadFormActive(f.ctx, a, form.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.crm.LookupForm(f.ctx, form.PublicKey); !errors.Is(err, crm.ErrNotFound) {
		t.Fatalf("disabled form still resolves: %v", err)
	}
	if _, err := f.crm.LookupForm(f.ctx, "lf_doesnotexist000000000000"); !errors.Is(err, crm.ErrNotFound) {
		t.Fatalf("unknown form: %v", err)
	}
}

func TestValidation(t *testing.T) {
	f := setup(t)
	a, _ := f.workspace("Acme")
	cases := []crm.CompanyInput{
		{Name: s("")},
		{Name: s("X"), Domain: s("not a domain")},
		{Name: s("X"), Website: s("javascript:alert(1)")},
		{Name: s("X"), Country: s("Argentina")},
		{Name: s("X"), SizeRange: s("lots")},
		{Name: s("X"), Tags: &[]string{"<script>"}},
		{Name: s(strings.Repeat("x", 201))},
		{Name: s("X"), Custom: &map[string]any{strings.Repeat("k", 65): 1}},
	}
	for i, in := range cases {
		if _, _, err := f.crm.UpsertCompany(f.ctx, a, in); !isValidation(err) {
			t.Fatalf("case %d accepted: %v", i, err)
		}
	}
	neg := -1.0
	if _, _, err := f.crm.UpsertOpportunity(f.ctx, a, crm.OpportunityInput{Title: s("x"), Amount: &neg}); !isValidation(err) {
		t.Fatalf("negative amount: %v", err)
	}
	if _, _, err := f.crm.UpsertContact(f.ctx, a, crm.ContactInput{Phone: s("123")}); !isValidation(err) {
		t.Fatalf("contact without name/email: %v", err)
	}
	// Control characters are stripped, not stored.
	c := must2(f.crm.UpsertCompany(f.ctx, a, crm.CompanyInput{Name: s("Bad\x00\x07Name")}))
	if c.Name != "BadName" {
		t.Fatalf("control chars kept: %q", c.Name)
	}
}

func TestBoardFiltersBulkAndGoal(t *testing.T) {
	f := setup(t)
	a, _ := f.workspace("Acme")
	amt := func(v float64) *float64 { return &v }
	tags := func(v ...string) *[]string { return &v }
	o1 := must2(f.crm.UpsertOpportunity(f.ctx, a, crm.OpportunityInput{Title: s("Alpha"), Amount: amt(100), SourceChannel: s("Web"), Tags: tags("pyme")}))
	o2 := must2(f.crm.UpsertOpportunity(f.ctx, a, crm.OpportunityInput{Title: s("Beta"), Amount: amt(200), SourceChannel: s("LinkedIn")}))
	o3 := must2(f.crm.UpsertOpportunity(f.ctx, a, crm.OpportunityInput{Title: s("Gamma"), Amount: amt(300)}))
	must(f.crm.MoveOpportunityStage(f.ctx, a, crm.MoveInput{Opportunity: o3.ID.String(), Stage: "Ganado"}))

	count := func(cols []crm.BoardColumn) int {
		n := 0
		for _, c := range cols {
			n += len(c.Cards)
		}
		return n
	}
	for name, tc := range map[string]struct {
		f    crm.BoardFilter
		want int
	}{
		"all":          {crm.BoardFilter{}, 3},
		"open only":    {crm.BoardFilter{OpenOnly: true}, 2},
		"by channel":   {crm.BoardFilter{Channel: "web"}, 1},
		"by tag":       {crm.BoardFilter{Tag: "PYME"}, 1},
		"by text":      {crm.BoardFilter{Query: "bet"}, 1},
		"wildcard lit": {crm.BoardFilter{Query: "%"}, 0},
	} {
		_, cols, err := f.crm.Board(f.ctx, a, tc.f)
		if err != nil || count(cols) != tc.want {
			t.Errorf("%s: got %d (%v), want %d", name, count(cols), err, tc.want)
		}
	}

	// Bulk: tag both open ones, move them, then mark one lost; a missing id
	// is reported, not fatal.
	ids := []string{o1.ID.String(), o2.ID.String()}
	if r := must(f.crm.Bulk(f.ctx, a, crm.BulkInput{IDs: ids, Action: "tag", Tag: "Campaña"})); r.Done != 2 {
		t.Fatalf("bulk tag: %+v", r)
	}
	if got := must(f.crm.GetOpportunity(f.ctx, a, o1.ID.String())); strings.Join(got.Tags, ",") != "pyme,campaña" {
		t.Fatalf("tags after bulk: %v", got.Tags)
	}
	if r := must(f.crm.Bulk(f.ctx, a, crm.BulkInput{IDs: ids, Action: "move", Stage: "Propuesta"})); r.Done != 2 {
		t.Fatalf("bulk move: %+v", r)
	}
	r := must(f.crm.Bulk(f.ctx, a, crm.BulkInput{IDs: []string{o1.ID.String(), uuid.NewString()}, Action: "lost", Reason: "precio"}))
	if r.Done != 1 || len(r.Failed) != 1 {
		t.Fatalf("bulk lost with a missing id: %+v", r)
	}
	if r := must(f.crm.Bulk(f.ctx, a, crm.BulkInput{IDs: []string{o2.ID.String()}, Action: "lost"})); r.Done != 0 || len(r.Failed) != 1 {
		t.Fatalf("bulk lost without reason must fail per item: %+v", r)
	}
	if _, err := f.crm.Bulk(f.ctx, a, crm.BulkInput{IDs: ids, Action: "explode"}); !isValidation(err) {
		t.Fatalf("unknown bulk action: %v", err)
	}

	// Monthly goal and won-by-month.
	ps := must(f.crm.ListPipelines(f.ctx, a))
	target := 1000.0
	in := crm.PipelineInput{ID: ps[0].ID.String(), Name: ps[0].Name, MonthlyTarget: &target, TargetCurrency: "usd"}
	for _, st := range ps[0].Stages {
		in.Stages = append(in.Stages, crm.StageInput{ID: st.ID.String(), Name: st.Name, Category: st.Category})
	}
	pv := must(f.crm.ConfigurePipeline(f.ctx, a, in))
	if pv.MonthlyTarget != 1000 || pv.TargetCurrency != "USD" {
		t.Fatalf("goal not stored: %+v", pv)
	}
	m := must(f.crm.Metrics(f.ctx, a, crm.MetricsInput{}))
	if m.Goal == nil || m.Goal.Won != 300 || m.Goal.Pct != 30 || m.Goal.Remaining != 700 {
		t.Fatalf("goal progress: %+v", m.Goal)
	}
	if len(m.WonByMonth) != 6 || m.WonByMonth[5].Amount != 300 {
		t.Fatalf("won by month: %+v", m.WonByMonth)
	}
	if len(m.Activity) == 0 || m.Activity[0].Kind != "session" || m.Activity[0].Created != 3 {
		t.Fatalf("activity: %+v", m.Activity)
	}
	bad := -5.0
	in.MonthlyTarget = &bad
	if _, err := f.crm.ConfigurePipeline(f.ctx, a, in); !isValidation(err) {
		t.Fatalf("negative goal accepted: %v", err)
	}
}

func TestFindOrCreateCompanyAndInteractionList(t *testing.T) {
	f := setup(t)
	a, _ := f.workspace("Acme")
	id1 := must(f.crm.FindOrCreateCompany(f.ctx, a, "Globex", ""))
	id2 := must(f.crm.FindOrCreateCompany(f.ctx, a, "globex", ""))
	id3 := must(f.crm.FindOrCreateCompany(f.ctx, a, "Globex Corp", "https://globex.com"))
	id4 := must(f.crm.FindOrCreateCompany(f.ctx, a, "", "globex.com"))
	if id1 != id2 || id3 == id1 || id3 != id4 {
		t.Fatalf("find or create: %v %v %v %v", id1, id2, id3, id4)
	}
	if id := must(f.crm.FindOrCreateCompany(f.ctx, a, "", "")); id != uuid.Nil {
		t.Fatal("empty input must not create a company")
	}
	must(f.crm.LogInteraction(f.ctx, a, crm.InteractionInput{Type: "call", Direction: "inbound", Company: "globex.com", Subject: "Llamada entrante"}))
	must(f.crm.LogInteraction(f.ctx, a, crm.InteractionInput{Type: "note", Company: "globex.com", Summary: "nota interna"}))
	if p := must(f.crm.ListInteractions(f.ctx, a, crm.InteractionFilter{Direction: "inbound"})); len(p.Items) != 1 || p.Items[0].Company != "Globex Corp" {
		t.Fatalf("interactions by direction: %+v", p.Items)
	}
	if p := must(f.crm.ListInteractions(f.ctx, a, crm.InteractionFilter{Query: "interna"})); len(p.Items) != 1 || p.Items[0].Contact != "" {
		t.Fatalf("interactions by text (no contact must not break): %+v", p.Items)
	}
	if _, err := f.crm.ListInteractions(f.ctx, a, crm.InteractionFilter{Type: "fax"}); !isValidation(err) {
		t.Fatalf("bad type filter: %v", err)
	}
}

func TestInteractionsOrderedByWhenTheyHappened(t *testing.T) {
	f := setup(t)
	a, _ := f.workspace("Acme")
	must2(f.crm.UpsertCompany(f.ctx, a, crm.CompanyInput{Name: s("Globex"), Domain: s("globex.com")}))
	// Logged in this order, but they happened in a different one.
	for _, d := range []string{"2026-03-01T10:00:00Z", "2026-05-01T10:00:00Z", "2026-01-01T10:00:00Z", "2026-04-01T10:00:00Z"} {
		must(f.crm.LogInteraction(f.ctx, a, crm.InteractionInput{Type: "email", Company: "globex.com", Subject: d, OccurredAt: d}))
	}
	var got []string
	cursor := ""
	for {
		p := must(f.crm.ListInteractions(f.ctx, a, crm.InteractionFilter{Limit: 1, Cursor: cursor}))
		for _, it := range p.Items {
			got = append(got, it.Subject[:7])
		}
		if p.NextCursor == "" {
			break
		}
		cursor = p.NextCursor
	}
	if strings.Join(got, ",") != "2026-05,2026-04,2026-03,2026-01" {
		t.Fatalf("order/pagination: %v", got)
	}
	if _, err := f.crm.ListInteractions(f.ctx, a, crm.InteractionFilter{Cursor: "garbage"}); !isValidation(err) {
		t.Fatalf("bad cursor: %v", err)
	}
}

func TestCompanyAndContactRows(t *testing.T) {
	f := setup(t)
	a, _ := f.workspace("Acme")
	must2(f.crm.UpsertCompany(f.ctx, a, crm.CompanyInput{Name: s("Globex"), Domain: s("globex.com"), Tags: &[]string{"vip"}}))
	must2(f.crm.UpsertCompany(f.ctx, a, crm.CompanyInput{Name: s("Initech")})) // no interactions, no domain
	ana := must2(f.crm.UpsertContact(f.ctx, a, crm.ContactInput{Email: s("ana@globex.com"), FirstName: s("Ana"), Company: s("globex.com")}))
	bob := must2(f.crm.UpsertContact(f.ctx, a, crm.ContactInput{Email: s("bob@globex.com"), Company: s("globex.com")}))
	o1 := must2(f.crm.UpsertOpportunity(f.ctx, a, crm.OpportunityInput{Title: s("Primary"), PrimaryContact: s(ana.Email), Company: s("globex.com")}))
	must2(f.crm.UpsertOpportunity(f.ctx, a, crm.OpportunityInput{Title: s("Linked"), Contacts: []crm.ContactRole{{Contact: ana.Email, Role: "champion"}}}))
	must2(f.crm.UpsertOpportunity(f.ctx, a, crm.OpportunityInput{Title: s("Other"), PrimaryContact: s(bob.Email)}))
	must(f.crm.LogInteraction(f.ctx, a, crm.InteractionInput{Type: "call", Contact: ana.Email, Opportunity: o1.ID.String()}))

	cs := must(f.crm.CompanyRows(f.ctx, a, crm.ListFilter{}))
	if len(cs.Items) != 2 {
		t.Fatalf("companies: %+v", cs.Items)
	}
	for _, c := range cs.Items {
		switch c.Name {
		case "Globex":
			if c.Contacts != 2 || c.OpenOpps != 1 || c.LastActivity == nil {
				t.Fatalf("globex stats: %+v", c)
			}
		case "Initech":
			if c.LastActivity != nil || c.Contacts != 0 {
				t.Fatalf("initech stats: %+v", c)
			}
		}
	}
	if r := must(f.crm.CompanyRows(f.ctx, a, crm.ListFilter{Tag: "vip"})); len(r.Items) != 1 {
		t.Fatalf("company tag filter: %+v", r.Items)
	}
	if r := must(f.crm.ContactRows(f.ctx, a, crm.ListFilter{Query: "globex"})); len(r.Items) != 2 || r.Items[0].Company != "Globex" {
		t.Fatalf("contacts by company name: %+v", r.Items)
	}
	opps := must(f.crm.ListOpportunities(f.ctx, a, crm.OpportunityFilter{Contact: ana.Email}))
	if len(opps.Items) != 2 {
		t.Fatalf("opportunities of a contact (primary + linked): %+v", opps.Items)
	}
}
