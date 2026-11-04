package crm_test

import (
	"strings"
	"testing"
	"time"

	"github.com/jrcoronel-moon/hilo-crm/internal/crm"
)

func migrationBatch(dry bool) crm.ImportInput {
	return crm.ImportInput{
		DryRun:    dry,
		Companies: []crm.CompanyInput{{ExternalID: s("hs:1"), Name: s("Globex")}}, // no domain: only reachable by external_id
		Contacts:  []crm.ContactInput{{ExternalID: s("hs:c1"), Email: s("hank@globex.test"), Company: s("hs:1")}},
		Opportunities: []crm.OpportunityInput{
			{ExternalID: s("hs:d1"), Title: s("Planta solar"), Company: s("hs:1"), PrimaryContact: s("hs:c1"),
				Stage: s("Ganado"), CreatedAt: s("2025-01-10"), ClosedAt: s("2025-03-05T15:00:00Z")},
			{ExternalID: s("hs:d2"), Title: s("Sin motivo"), Stage: s("Perdido")}, // fails: lost needs a reason
		},
		Interactions: []crm.InteractionInput{{ExternalID: "hs:e1", Type: "call", Opportunity: "hs:d1", Contact: "hs:c1",
			OccurredAt: "2025-02-01T10:00:00Z", Summary: "Llamada de cierre"}},
	}
}

func TestImportBatch(t *testing.T) {
	f := setup(t)
	owner, _ := f.workspace("Acme")

	// Dry run: validates everything, references between items included, saves nothing.
	res, err := f.crm.Import(f.ctx, owner, migrationBatch(true))
	if err != nil || !res.DryRun || res.OK != 4 || res.Failed != 1 {
		t.Fatalf("dry run: %+v %v", res, err)
	}
	if !strings.Contains(res.Items[3].Error, "lost_reason") {
		t.Fatalf("failed item should explain why: %+v", res.Items[3])
	}
	if page := must(f.crm.ListCompanies(f.ctx, owner, crm.ListFilter{})); len(page.Items) != 0 {
		t.Fatalf("dry run saved %d companies", len(page.Items))
	}

	// For real: the failure does not undo the rest.
	res, err = f.crm.Import(f.ctx, owner, migrationBatch(false))
	if err != nil || res.OK != 4 || res.Failed != 1 || !res.Items[0].Created {
		t.Fatalf("import: %+v %v", res, err)
	}
	o := must(f.crm.GetOpportunity(f.ctx, owner, "hs:d1"))
	day := func(t time.Time) string { return t.UTC().Format(time.DateOnly) }
	if o.Status != "won" || day(o.CreatedAt) != "2025-01-10" || o.ClosedAt == nil || day(*o.ClosedAt) != "2025-03-05" ||
		day(o.StageEnteredAt) != "2025-03-05" || o.CompanyName != "Globex" {
		t.Fatalf("historical opportunity: %+v", o)
	}
	tl := must(f.crm.Timeline(f.ctx, owner, crm.TimelineInput{Opportunity: "hs:d1"}))
	if len(tl) != 2 {
		t.Fatalf("timeline: %+v", tl)
	}
	for _, e := range tl {
		if !strings.HasPrefix(e.At.UTC().Format(time.DateOnly), "2025-") {
			t.Fatalf("history must keep the original dates: %+v", e)
		}
	}

	// Again: idempotent, nothing new, and the same outcome for every record.
	res, _ = f.crm.Import(f.ctx, owner, migrationBatch(false))
	if res.OK != 4 || res.Failed != 1 {
		t.Fatalf("re-run: %+v", res)
	}
	for _, it := range res.Items {
		if it.Created {
			t.Fatalf("second run created %+v", it)
		}
	}
	if page := must(f.crm.ListCompanies(f.ctx, owner, crm.ListFilter{})); len(page.Items) != 1 {
		t.Fatalf("companies after re-run: %d", len(page.Items))
	}

	if _, err := f.crm.Import(f.ctx, owner, crm.ImportInput{Companies: make([]crm.CompanyInput, crm.MaxImport+1)}); !isValidation(err) {
		t.Fatalf("oversized batch: %v", err)
	}
	if _, err := f.crm.Import(f.ctx, owner, crm.ImportInput{}); !isValidation(err) {
		t.Fatalf("empty batch: %v", err)
	}
}

func TestHistoricalDatesRules(t *testing.T) {
	f := setup(t)
	owner, ws := f.workspace("Acme")
	writer := f.member(ws, "w@acme.test", "member", crm.AllScopes)
	future := time.Now().Add(48 * time.Hour).Format(time.DateOnly)
	for name, c := range map[string]struct {
		a  crm.Actor
		in crm.OpportunityInput
	}{
		"needs admin":           {writer, crm.OpportunityInput{Title: s("A"), CreatedAt: s("2024-01-01")}},
		"future":                {owner, crm.OpportunityInput{Title: s("B"), CreatedAt: s(future)}},
		"too old":               {owner, crm.OpportunityInput{Title: s("C"), CreatedAt: s("1980-01-01")}},
		"closed before created": {owner, crm.OpportunityInput{Title: s("D"), Stage: s("Ganado"), CreatedAt: s("2024-05-01"), ClosedAt: s("2024-04-01")}},
		"closed_at on open":     {owner, crm.OpportunityInput{Title: s("E"), ClosedAt: s("2024-04-01")}},
		"not a date":            {owner, crm.OpportunityInput{Title: s("F"), CreatedAt: s("ayer")}},
	} {
		if _, _, err := f.crm.UpsertOpportunity(f.ctx, c.a, c.in); !isValidation(err) {
			t.Errorf("%s: want a validation error, got %v", name, err)
		}
	}
	o := must2(f.crm.UpsertOpportunity(f.ctx, owner, crm.OpportunityInput{Title: s("G"), ExternalID: s("x:1"), CreatedAt: s("2024-01-01")}))
	if o.CreatedAt.UTC().Format(time.DateOnly) != "2024-01-01" || o.Status != "open" {
		t.Fatalf("open historical: %+v", o)
	}
	// On updates the dates are ignored (re-running a migration), never rewritten.
	if o = must2(f.crm.UpsertOpportunity(f.ctx, owner, crm.OpportunityInput{ExternalID: s("x:1"), CreatedAt: s("2023-01-01")})); o.CreatedAt.UTC().Format(time.DateOnly) != "2024-01-01" {
		t.Fatalf("update rewrote created_at: %+v", o)
	}
	// Only the close date known: created then too.
	o = must2(f.crm.UpsertOpportunity(f.ctx, owner, crm.OpportunityInput{Title: s("H"), Stage: s("Perdido"), LostReason: s("precio"), ClosedAt: s("2024-06-01")}))
	if o.CreatedAt.UTC().Format(time.DateOnly) != "2024-06-01" || o.Status != "lost" {
		t.Fatalf("closed only: %+v", o)
	}
}
