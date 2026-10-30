package crm_test

import (
	"slices"
	"testing"
	"time"

	"github.com/jrcoronel-moon/hilo-crm/internal/crm"
)

func TestListChangesCursor(t *testing.T) {
	f := setup(t)
	owner, _ := f.workspace("Acme")
	first := must(f.crm.ListChanges(f.ctx, owner, crm.ChangesInput{}))
	if len(first.Changes) != 0 || first.NextCursor == "" {
		t.Fatalf("empty workspace: %+v", first)
	}
	must2(f.crm.UpsertCompany(f.ctx, owner, crm.CompanyInput{Name: s("Globex")}))
	must2(f.crm.UpsertOpportunity(f.ctx, owner, crm.OpportunityInput{Title: s("Planta"), Stage: s("Propuesta")}))
	page := must(f.crm.ListChanges(f.ctx, owner, crm.ChangesInput{Cursor: first.NextCursor, Limit: 1}))
	if len(page.Changes) != 1 || page.Changes[0].Summary != "creó la empresa «Globex»" {
		t.Fatalf("page 1: %+v", page)
	}
	page = must(f.crm.ListChanges(f.ctx, owner, crm.ChangesInput{Cursor: page.NextCursor}))
	if len(page.Changes) != 1 || page.Changes[0].Action != "opportunity.create" {
		t.Fatalf("page 2: %+v", page)
	}
	if again := must(f.crm.ListChanges(f.ctx, owner, crm.ChangesInput{Cursor: page.NextCursor})); len(again.Changes) != 0 || again.NextCursor != page.NextCursor {
		t.Fatalf("nothing new: %+v", again)
	}
	if _, err := f.crm.ListChanges(f.ctx, owner, crm.ChangesInput{Cursor: "abc"}); !isValidation(err) {
		t.Fatalf("bad cursor: %v", err)
	}
	// Another workspace's changes never show.
	other, _ := f.workspace("Other")
	if p := must(f.crm.ListChanges(f.ctx, other, crm.ChangesInput{Cursor: "0"})); len(p.Changes) != 0 {
		t.Fatalf("leak: %+v", p)
	}
}

func TestNeedsAttention(t *testing.T) {
	f := setup(t)
	owner, _ := f.workspace("Acme")
	yesterday := time.Now().AddDate(0, 0, -1)
	amt := 5000.0
	must2(f.crm.UpsertOpportunity(f.ctx, owner, crm.OpportunityInput{Title: s("Atrasada"), Amount: &amt, Owner: s("root@hilo.test"),
		NextStep: s("Llamar"), NextStepAt: s(yesterday.Format(time.RFC3339)), ExpectedCloseDate: s(yesterday.Format(time.DateOnly))}))
	must2(f.crm.UpsertOpportunity(f.ctx, owner, crm.OpportunityInput{Title: s("Al día"), Owner: s("root@hilo.test"), NextStep: s("Demo"),
		NextStepAt: s(time.Now().AddDate(0, 0, 3).Format(time.RFC3339))}))
	must2(f.crm.UpsertOpportunity(f.ctx, owner, crm.OpportunityInput{Title: s("Huérfana")}))
	must2(f.crm.UpsertOpportunity(f.ctx, owner, crm.OpportunityInput{Title: s("Vieja"), CreatedAt: s("2024-01-01"), Owner: s("root@hilo.test"), NextStep: s("x")}))
	must2(f.crm.UpsertOpportunity(f.ctx, owner, crm.OpportunityInput{Title: s("Ganada"), Stage: s("Ganado")}))

	items := must(f.crm.NeedsAttention(f.ctx, owner, crm.AttentionInput{}))
	got := map[string][]string{}
	for _, it := range items {
		got[it.Opportunity.Title] = it.Reasons
	}
	want := map[string][]string{
		"Atrasada": {"next_step_overdue", "past_expected_close"},
		"Huérfana": {"no_owner", "no_next_step"},
		"Vieja":    {"stale"},
	}
	if len(got) != len(want) {
		t.Fatalf("items: %+v", got)
	}
	for title, reasons := range want {
		if !slices.Equal(got[title], reasons) {
			t.Errorf("%s: got %v, want %v", title, got[title], reasons)
		}
	}
	if items[0].Opportunity.Title != "Atrasada" { // two reasons and the largest amount
		t.Fatalf("order: %s first", items[0].Opportunity.Title)
	}
}
