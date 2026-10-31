package web

import (
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/jrcoronel-moon/hilo-crm/internal/crm"
	"github.com/jrcoronel-moon/hilo-crm/internal/i18n"
	"github.com/jrcoronel-moon/hilo-crm/internal/store"
)

// Companies and contacts: lists, record pages, create/edit and archive. All
// writes go through the crm service, exactly like the MCP tools.

type peopleList[T any] struct {
	CSRF string
	Page crm.Page[T]
	Q    string
	Tag  string
	Next string
}

func listNext(r *http.Request, cursor string) string {
	if cursor == "" {
		return ""
	}
	q := r.URL.Query()
	q.Set("cursor", cursor)
	return "?" + q.Encode()
}

func (h *Handler) companies(w http.ResponseWriter, r *http.Request, v view) {
	g := r.URL.Query().Get
	f := crm.ListFilter{Query: strings.TrimSpace(g("q")), Tag: g("tag"), Cursor: g("cursor"), Limit: 50}
	p, err := h.crm.CompanyRows(r.Context(), v.Actor, f)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.render(w, r, http.StatusOK, "companies", page{Title: i18n.M("Empresas"), View: &v, Nav: "companies", Flash: flashes[g("ok")],
		Data: peopleList[crm.CompanyRow]{CSRF: v.Sess.CSRFToken, Page: p, Q: f.Query, Tag: f.Tag, Next: listNext(r, p.NextCursor)}})
}

func (h *Handler) contacts(w http.ResponseWriter, r *http.Request, v view) {
	g := r.URL.Query().Get
	f := crm.ListFilter{Query: strings.TrimSpace(g("q")), Tag: g("tag"), Company: g("company"), Cursor: g("cursor"), Limit: 50}
	p, err := h.crm.ContactRows(r.Context(), v.Actor, f)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.render(w, r, http.StatusOK, "contacts", page{Title: i18n.M("Contactos"), View: &v, Nav: "contacts", Flash: flashes[g("ok")],
		Data: peopleList[crm.ContactRow]{CSRF: v.Sess.CSRFToken, Page: p, Q: f.Query, Tag: f.Tag, Next: listNext(r, p.NextCursor)}})
}

type companyPage struct {
	CSRF          string
	C             crm.CompanyDetail
	Contacts      []crm.ContactRow
	Opportunities []crm.OpportunityView
	Timeline      []crm.TimelineEntry
	Members       []store.ListMembersRow
	Sizes         []string
	Suggest       []crm.TagCount
}

var companySizes = []string{"1-10", "11-50", "51-200", "201-1000", "1001-5000", "5000+"}

func (h *Handler) company(w http.ResponseWriter, r *http.Request, v view) {
	ctx, a, id := r.Context(), v.Actor, r.PathValue("id")
	c, err := h.crm.GetCompany(ctx, a, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	d := companyPage{CSRF: v.Sess.CSRFToken, C: c, Sizes: companySizes}
	ref := c.ID.String()
	contacts, err := h.crm.ContactRows(ctx, a, crm.ListFilter{Company: ref, Limit: 100})
	if err == nil {
		d.Contacts = contacts.Items
		var opps crm.Page[crm.OpportunityView]
		if opps, err = h.crm.ListOpportunities(ctx, a, crm.OpportunityFilter{Company: ref, Limit: 100}); err == nil {
			d.Opportunities = opps.Items
			if d.Timeline, err = h.crm.Timeline(ctx, a, crm.TimelineInput{Company: ref, Limit: 50}); err == nil {
				if d.Members, err = h.id.Members(ctx, v.caller()); err == nil {
					d.Suggest, err = h.crm.ListAllTags(ctx, a)
				}
			}
		}
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.render(w, r, http.StatusOK, "company", page{Title: i18n.M("Empresas"), Crumb: c.Name, View: &v, Nav: "companies", Flash: flashes[r.URL.Query().Get("ok")], Data: d})
}

type contactPage struct {
	CSRF          string
	C             crm.ContactDetail
	Company       string
	Opportunities []crm.OpportunityView
	Timeline      []crm.TimelineEntry
	Members       []store.ListMembersRow
	Suggest       []crm.TagCount
}

func (h *Handler) contact(w http.ResponseWriter, r *http.Request, v view) {
	ctx, a := r.Context(), v.Actor
	c, err := h.crm.GetContact(ctx, a, r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	d := contactPage{CSRF: v.Sess.CSRFToken, C: c}
	if c.CompanyID != nil {
		if co, err := h.crm.GetCompany(ctx, a, c.CompanyID.String()); err == nil {
			d.Company = co.Name
		}
	}
	ref := c.ID.String()
	opps, err := h.crm.ListOpportunities(ctx, a, crm.OpportunityFilter{Contact: ref, Limit: 100})
	if err == nil {
		d.Opportunities = opps.Items
		if d.Timeline, err = h.crm.Timeline(ctx, a, crm.TimelineInput{Contact: ref, Limit: 50}); err == nil {
			if d.Members, err = h.id.Members(ctx, v.caller()); err == nil {
				d.Suggest, err = h.crm.ListAllTags(ctx, a)
			}
		}
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	name := strings.TrimSpace(c.FirstName + " " + c.LastName)
	if name == "" {
		name = c.Email
	}
	h.render(w, r, http.StatusOK, "contact", page{Title: i18n.M("Contactos"), Crumb: name, View: &v, Nav: "contacts", Flash: flashes[r.URL.Query().Get("ok")], Data: d})
}

// formStr returns a pointer to a trimmed form value, so an empty field
// clears it (the service's "" = clear semantics) — used by edit forms that
// always send every field.
func formStr(r *http.Request, key string) *string {
	s := strings.TrimSpace(r.PostFormValue(key))
	return &s
}

func formTags(r *http.Request) *[]string {
	t := splitTags(r.PostFormValue("tags"))
	if t == nil {
		t = []string{}
	}
	return &t
}

// saveCompany creates (no id in the path) or updates a company.
func (h *Handler) saveCompany(w http.ResponseWriter, r *http.Request, v view) {
	in := crm.CompanyInput{Name: formStr(r, "name"), Domain: formStr(r, "domain")}
	if id := r.PathValue("id"); id != "" {
		in.ID = &id
		in.Industry, in.SizeRange, in.Country = formStr(r, "industry"), formStr(r, "size_range"), formStr(r, "country")
		in.Website, in.Description, in.Owner = formStr(r, "website"), formStr(r, "description"), formStr(r, "owner")
		in.Tags = formTags(r)
	} else if strings.TrimSpace(*in.Domain) == "" {
		in.Domain = nil // creating without a domain: never match an existing one by it
	}
	c, created, err := h.crm.UpsertCompany(r.Context(), v.Actor, in)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ok := "saved"
	if created {
		ok = "company"
	}
	http.Redirect(w, r, "/companies/"+c.ID.String()+"?ok="+ok, http.StatusSeeOther)
}

// saveContact creates or updates a contact. The company field takes a
// domain (contains a dot) or a name, found or created.
func (h *Handler) saveContact(w http.ResponseWriter, r *http.Request, v view) {
	ctx, a := r.Context(), v.Actor
	in := crm.ContactInput{FirstName: formStr(r, "first_name"), LastName: formStr(r, "last_name"), Email: formStr(r, "email")}
	if id := r.PathValue("id"); id != "" {
		in.ID = &id
		in.Phone, in.Title, in.LinkedInURL, in.Owner = formStr(r, "phone"), formStr(r, "title"), formStr(r, "linkedin_url"), formStr(r, "owner")
		in.Tags = formTags(r)
	} else if *in.Email == "" {
		in.Email = nil
	}
	if co := strings.TrimSpace(r.PostFormValue("company")); co != "" {
		name, domain := co, ""
		if strings.Contains(co, ".") && !strings.Contains(co, " ") {
			name, domain = "", co
		}
		cid, err := h.crm.FindOrCreateCompany(ctx, a, name, domain)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		s := cid.String()
		in.Company = &s
	} else if r.PathValue("id") != "" {
		empty := ""
		in.Company = &empty
	}
	c, created, err := h.crm.UpsertContact(ctx, a, in)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ok := "saved"
	if created {
		ok = "contact"
	}
	http.Redirect(w, r, "/contacts/"+c.ID.String()+"?ok="+ok, http.StatusSeeOther)
}

// archiveRecord archives a company or contact and returns to its list.
func (h *Handler) archiveRecord(kind string) func(http.ResponseWriter, *http.Request, view) {
	return func(w http.ResponseWriter, r *http.Request, v view) {
		id := r.PathValue("id")
		if _, err := uuid.Parse(id); err != nil {
			http.NotFound(w, r)
			return
		}
		if err := h.crm.Archive(r.Context(), v.Actor, crm.ArchiveInput{Type: kind, ID: id}); err != nil {
			h.fail(w, r, err)
			return
		}
		list := map[string]string{"company": "/companies", "contact": "/contacts"}[kind]
		http.Redirect(w, r, list+"?ok=archived", http.StatusSeeOther)
	}
}
