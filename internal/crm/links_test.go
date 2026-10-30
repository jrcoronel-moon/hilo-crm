package crm_test

import (
	"strings"
	"testing"

	"github.com/jrcoronel-moon/hilo-crm/internal/crm"
)

func TestOpportunityLinks(t *testing.T) {
	f := setup(t)
	owner, _ := f.workspace("Acme")
	links := []crm.Link{{URL: "https://drive.example.test/x", Label: "Propuesta"}, {URL: "https://drive.example.test/x"}, {URL: "https://docs.example.test/y"}}
	o := must2(f.crm.UpsertOpportunity(f.ctx, owner, crm.OpportunityInput{Title: s("Planta"), Links: &links}))
	if len(o.Links) != 2 || o.Links[0].Label != "Propuesta" || o.Links[1].URL != "https://docs.example.test/y" {
		t.Fatalf("links: %+v", o.Links)
	}
	id := o.ID.String()
	// Omitted: unchanged. Empty list: cleared. Unsafe scheme: refused.
	if o = must2(f.crm.UpsertOpportunity(f.ctx, owner, crm.OpportunityInput{ID: &id, Title: s("Planta 2")})); len(o.Links) != 2 {
		t.Fatalf("links lost on update: %+v", o.Links)
	}
	bad := []crm.Link{{URL: "javascript:alert(1)"}}
	if _, _, err := f.crm.UpsertOpportunity(f.ctx, owner, crm.OpportunityInput{ID: &id, Links: &bad}); err == nil || !strings.Contains(err.Error(), "links") {
		t.Fatalf("unsafe link accepted: %v", err)
	}
	none := []crm.Link{}
	if o = must2(f.crm.UpsertOpportunity(f.ctx, owner, crm.OpportunityInput{ID: &id, Links: &none})); len(o.Links) != 0 {
		t.Fatalf("links not cleared: %+v", o.Links)
	}
	// Export carries them.
	links = links[:1]
	must2(f.crm.UpsertOpportunity(f.ctx, owner, crm.OpportunityInput{ID: &id, Links: &links}))
	out := must(f.crm.Export(f.ctx, owner))
	if len(out.Opportunities) != 1 || out.Opportunities[0].Links == nil || (*out.Opportunities[0].Links)[0].Label != "Propuesta" {
		t.Fatalf("export: %+v", out.Opportunities)
	}
}
