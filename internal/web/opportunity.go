package web

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/jrcoronel-moon/hilo-crm/internal/crm"
	"github.com/jrcoronel-moon/hilo-crm/internal/store"
)

// Opportunity record page: edit, move, link contacts, archive, timeline.

type opportunityPage struct {
	CSRF     string
	O        crm.OpportunityDetail
	Stages   []crm.StageView
	Channels []crm.ChannelView
	Members  []store.ListMembersRow
	Timeline []crm.TimelineEntry
	Suggest  []crm.TagCount
	Company  string // company name or domain, for the edit field
	Contact  string // primary contact email, for the edit field
}

func (h *Handler) opportunity(w http.ResponseWriter, r *http.Request, v view) {
	ctx, a := r.Context(), v.Actor
	o, err := h.crm.GetOpportunity(ctx, a, r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	d := opportunityPage{CSRF: v.Sess.CSRFToken, O: o, Company: o.CompanyName}
	pipes, err := h.crm.ListPipelines(ctx, a)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	for _, p := range pipes {
		if p.ID == o.PipelineID {
			d.Stages = p.Stages
		}
	}
	if o.PrimaryContactID != nil {
		if c, err := h.crm.GetContact(ctx, a, o.PrimaryContactID.String()); err == nil {
			d.Contact = c.Email
		}
	}
	if d.Channels, err = h.crm.ListChannels(ctx, a); err == nil {
		if d.Members, err = h.id.Members(ctx, v.caller()); err == nil {
			if d.Timeline, err = h.crm.Timeline(ctx, a, crm.TimelineInput{Opportunity: o.ID.String(), Limit: 100}); err == nil {
				d.Suggest, err = h.crm.ListAllTags(ctx, a)
			}
		}
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.render(w, http.StatusOK, "opportunity", page{Title: "Oportunidades", Crumb: o.Title, View: &v, Nav: "opportunities",
		Flash: flashes[r.URL.Query().Get("ok")], Data: d})
}

// saveOpportunity updates every editable field from the record form.
func (h *Handler) saveOpportunity(w http.ResponseWriter, r *http.Request, v view) {
	ctx, a := r.Context(), v.Actor
	id := r.PathValue("id")
	in := crm.OpportunityInput{ID: &id, Title: formStr(r, "title"), Kind: formStr(r, "kind"), Currency: formStr(r, "currency"),
		ExpectedCloseDate: formStr(r, "expected_close_date"), NextStep: formStr(r, "next_step"), NextStepAt: formStr(r, "next_step_at"),
		Description: formStr(r, "description"), Owner: formStr(r, "owner"), SourceChannel: formStr(r, "channel"),
		PrimaryContact: formStr(r, "primary_contact"), Tags: formTags(r), Links: formLinks(r)}
	amount := 0.0
	if s := strings.TrimSpace(r.PostFormValue("amount")); s != "" {
		var err error
		if amount, err = parseAmount(s); err != nil {
			h.fail(w, r, &crm.ValidationError{Field: "amount", Msg: "must be a number"})
			return
		}
	}
	in.Amount = &amount
	if p := strings.TrimSpace(r.PostFormValue("probability")); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil {
			h.fail(w, r, &crm.ValidationError{Field: "probability", Msg: "must be a whole number 0-100"})
			return
		}
		in.Probability = &n
	}
	company := strings.TrimSpace(r.PostFormValue("company"))
	if company == "" {
		empty := ""
		in.Company = &empty
	} else {
		name, domain := company, ""
		if strings.Contains(company, ".") && !strings.Contains(company, " ") {
			name, domain = "", company
		}
		cid, err := h.crm.FindOrCreateCompany(ctx, a, name, domain)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		s := cid.String()
		in.Company = &s
	}
	if _, _, err := h.crm.UpsertOpportunity(ctx, a, in); err != nil {
		h.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/opportunities/"+id+"?ok=saved", http.StatusSeeOther)
}

// moveOpportunity changes the stage from the record page.
func (h *Handler) moveOpportunity(w http.ResponseWriter, r *http.Request, v view) {
	id := r.PathValue("id")
	if _, err := h.crm.MoveOpportunityStage(r.Context(), v.Actor, crm.MoveInput{Opportunity: id, Stage: r.PostFormValue("stage"), Reason: r.PostFormValue("reason")}); err != nil {
		h.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/opportunities/"+id+"?ok=saved", http.StatusSeeOther)
}

// linkContact adds a contact (by email) with a role.
func (h *Handler) linkContact(w http.ResponseWriter, r *http.Request, v view) {
	id := r.PathValue("id")
	in := crm.OpportunityInput{ID: &id, Contacts: []crm.ContactRole{{Contact: r.PostFormValue("contact"), Role: r.PostFormValue("role")}}}
	if _, _, err := h.crm.UpsertOpportunity(r.Context(), v.Actor, in); err != nil {
		h.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/opportunities/"+id+"?ok=saved", http.StatusSeeOther)
}

// archiveOpportunity soft-deletes from the record page.
func (h *Handler) archiveOpportunity(w http.ResponseWriter, r *http.Request, v view) {
	if err := h.crm.Archive(r.Context(), v.Actor, crm.ArchiveInput{Type: "opportunity", ID: r.PathValue("id")}); err != nil {
		h.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/opportunities?ok=archived", http.StatusSeeOther)
}

// formLinks reads the links textarea: one per line, "URL | label". An empty
// textarea clears the links.
func formLinks(r *http.Request) *[]crm.Link {
	out := []crm.Link{}
	for _, line := range strings.Split(r.PostFormValue("links"), "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		u, label, _ := strings.Cut(line, "|")
		out = append(out, crm.Link{URL: strings.TrimSpace(u), Label: strings.TrimSpace(label)})
	}
	return &out
}
