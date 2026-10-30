package crm

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/jrcoronel-moon/hilo-crm/internal/store"
)

// ContactInput creates or updates a contact. Matching order for an update:
// id, then external_id, then email.
type ContactInput struct {
	ID            *string         `json:"id,omitempty" jsonschema:"existing contact id to update"`
	ExternalID    *string         `json:"external_id,omitempty" jsonschema:"your own stable id for idempotent upserts"`
	Company       *string         `json:"company,omitempty" jsonschema:"company id, domain or external_id; empty string clears"`
	FirstName     *string         `json:"first_name,omitempty"`
	LastName      *string         `json:"last_name,omitempty"`
	Email         *string         `json:"email,omitempty" jsonschema:"unique per workspace"`
	Phone         *string         `json:"phone,omitempty"`
	Title         *string         `json:"title,omitempty" jsonschema:"job title"`
	LinkedInURL   *string         `json:"linkedin_url,omitempty"`
	Owner         *string         `json:"owner,omitempty" jsonschema:"member id or email; empty string clears"`
	SourceChannel *string         `json:"source_channel,omitempty" jsonschema:"channel id or name; empty string clears"`
	Tags          *[]string       `json:"tags,omitempty" jsonschema:"replaces all tags"`
	Custom        *map[string]any `json:"custom,omitempty" jsonschema:"free-form fields; replaces the whole object"`
}

// ContactView is a contact as returned to callers.
type ContactView struct {
	ID              uuid.UUID       `json:"id"`
	CompanyID       *uuid.UUID      `json:"company_id,omitempty"`
	FirstName       string          `json:"first_name,omitempty"`
	LastName        string          `json:"last_name,omitempty"`
	Email           string          `json:"email,omitempty"`
	Phone           string          `json:"phone,omitempty"`
	Title           string          `json:"title,omitempty"`
	LinkedInURL     string          `json:"linkedin_url,omitempty"`
	OwnerID         *uuid.UUID      `json:"owner_id,omitempty"`
	SourceChannelID *uuid.UUID      `json:"source_channel_id,omitempty"`
	Tags            []string        `json:"tags,omitempty"`
	Custom          json.RawMessage `json:"custom,omitempty"`
	ExternalID      string          `json:"external_id,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

func contactView(c store.Contact) ContactView {
	return ContactView{
		ID: c.ID, CompanyID: c.CompanyID, FirstName: c.FirstName, LastName: c.LastName, Email: deref(c.Email),
		Phone: c.Phone, Title: c.Title, LinkedInURL: c.LinkedinUrl, OwnerID: c.OwnerID,
		SourceChannelID: c.SourceChannelID, Tags: c.Tags, Custom: emptyObjNil(c.Custom),
		ExternalID: deref(c.ExternalID), CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
}

//nolint:dupl // per-entity adapter for findRecord, where the shared logic lives
func findContact(ctx context.Context, q *store.Queries, ws uuid.UUID, in ContactInput) (*store.Contact, error) {
	return findRecord("contact", in.ID, in.ExternalID,
		func(id uuid.UUID) (store.Contact, error) {
			return q.GetContact(ctx, store.GetContactParams{WorkspaceID: ws, ID: id})
		},
		func(ext *string) (store.Contact, error) {
			return q.GetContactByExternalID(ctx, store.GetContactByExternalIDParams{WorkspaceID: ws, ExternalID: ext})
		},
		naturalKey(in.Email, func(v string) (string, error) { return NormalizeEmail("email", v) },
			func(e *string) (store.Contact, error) {
				return q.GetContactByEmail(ctx, store.GetContactByEmailParams{WorkspaceID: ws, Email: e})
			}))
}

// UpsertContact creates or updates a contact and reports whether it was created.
func (s *Service) UpsertContact(ctx context.Context, a Actor, in ContactInput) (ContactView, bool, error) {
	var out store.Contact
	created := false
	err := s.tx(ctx, a, PermWrite, func(q *store.Queries) error {
		var err error
		out, created, err = upsertContact(ctx, q, a.WorkspaceID, in)
		if err != nil {
			return err
		}
		action := "contact.update"
		if created {
			action = "contact.create"
		}
		name := strings.TrimSpace(out.FirstName + " " + out.LastName)
		if name == "" && out.Email != nil {
			name = *out.Email
		}
		return s.audit(ctx, q, a, action, "contact", out.ID, map[string]any{"name": name})
	})
	return contactView(out), created, err
}

func upsertContact(ctx context.Context, q *store.Queries, ws uuid.UUID, in ContactInput) (store.Contact, bool, error) {
	cur, err := findContact(ctx, q, ws, in)
	if err != nil {
		return store.Contact{}, false, err
	}
	created := cur == nil
	c := store.Contact{ID: newID(), Tags: []string{}, Custom: json.RawMessage(`{}`)}
	if cur != nil {
		c = *cur
	}
	r := resolvers{ctx, q, ws}
	var p patcher
	p.ref(&c.CompanyID, in.Company, r.company)
	p.line(&c.FirstName, "first_name", in.FirstName, 100)
	p.line(&c.LastName, "last_name", in.LastName, 100)
	if in.Email != nil {
		e, err := NormalizeEmail("email", *in.Email)
		p.set(err)
		c.Email = nilIfEmpty(e)
	}
	p.line(&c.Phone, "phone", in.Phone, 40)
	p.line(&c.Title, "title", in.Title, 150)
	p.with(&c.LinkedinUrl, in.LinkedInURL, func(v string) (string, error) { return cleanURL("linkedin_url", v) })
	p.ref(&c.OwnerID, in.Owner, r.owner)
	p.ref(&c.SourceChannelID, in.SourceChannel, r.channel)
	p.tags(&c.Tags, "tags", in.Tags)
	p.custom(&c.Custom, "custom", in.Custom)
	p.externalID(&c.ExternalID, in.ExternalID)
	if p.err != nil {
		return store.Contact{}, false, p.err
	}
	if c.FirstName == "" && c.LastName == "" && c.Email == nil {
		return store.Contact{}, false, invalid("contact", "needs at least a name or an email")
	}
	c, err = q.SaveContact(ctx, store.SaveContactParams{
		ID: c.ID, WorkspaceID: ws, CompanyID: c.CompanyID, FirstName: c.FirstName, LastName: c.LastName, Email: c.Email,
		Phone: c.Phone, Title: c.Title, LinkedinUrl: c.LinkedinUrl, OwnerID: c.OwnerID,
		SourceChannelID: c.SourceChannelID, Tags: c.Tags, Custom: c.Custom, ExternalID: c.ExternalID,
	})
	return c, created, err
}

// ContactDetail is a contact with its interaction count.
type ContactDetail struct {
	ContactView
	Interactions int `json:"interaction_count"`
}

// GetContact returns one contact by id or email.
func (s *Service) GetContact(ctx context.Context, a Actor, ref string) (ContactDetail, error) {
	var out ContactDetail
	err := s.tx(ctx, a, PermRead, func(q *store.Queries) error {
		id, err := resolvers{ctx, q, a.WorkspaceID}.contact(ref)
		if err != nil {
			return err
		}
		c, err := q.GetContact(ctx, store.GetContactParams{WorkspaceID: a.WorkspaceID, ID: id})
		if err != nil {
			return notFound("contact", err)
		}
		n, err := q.CountInteractions(ctx, store.CountInteractionsParams{WorkspaceID: a.WorkspaceID, ContactID: &id})
		out = ContactDetail{ContactView: contactView(c), Interactions: int(n)}
		return err
	})
	return out, err
}

// ListContacts returns a page of contacts, newest first.
func (s *Service) ListContacts(ctx context.Context, a Actor, f ListFilter) (Page[ContactView], error) {
	var out Page[ContactView]
	err := s.tx(ctx, a, PermRead, func(q *store.Queries) error {
		la, err := f.args(resolvers{ctx, q, a.WorkspaceID})
		if err != nil {
			return err
		}
		rows, err := q.ListContacts(ctx, store.ListContactsParams{
			WorkspaceID: a.WorkspaceID, Query: la.query, CompanyID: la.company, OwnerID: la.owner, Tag: la.tag,
			Cursor: la.cur, Lim: la.limit,
		})
		if err != nil {
			return err
		}
		out.Items = make([]ContactView, 0, len(rows))
		for _, c := range rows {
			out.Items = append(out.Items, contactView(c))
		}
		out.NextCursor = nextCursor(rows, la.limit, func(c store.Contact) uuid.UUID { return c.ID })
		return nil
	})
	return out, err
}
