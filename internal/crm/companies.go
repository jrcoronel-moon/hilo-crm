package crm

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/jrcoronel-moon/hilo-crm/internal/store"
)

// CompanyInput creates or updates a company. Matching order for an update:
// id, then external_id, then domain. Without a match a new company is created
// (name required).
type CompanyInput struct {
	ID            *string         `json:"id,omitempty" jsonschema:"existing company id to update"`
	ExternalID    *string         `json:"external_id,omitempty" jsonschema:"your own stable id for idempotent upserts"`
	Name          *string         `json:"name,omitempty"`
	Domain        *string         `json:"domain,omitempty" jsonschema:"web domain, e.g. acme.com; unique per workspace"`
	Industry      *string         `json:"industry,omitempty"`
	SizeRange     *string         `json:"size_range,omitempty" jsonschema:"one of 1-10, 11-50, 51-200, 201-1000, 1001-5000, 5000+"`
	Country       *string         `json:"country,omitempty" jsonschema:"ISO 3166-1 alpha-2, e.g. AR"`
	Website       *string         `json:"website,omitempty"`
	Description   *string         `json:"description,omitempty"`
	Owner         *string         `json:"owner,omitempty" jsonschema:"member id or email; empty string clears"`
	SourceChannel *string         `json:"source_channel,omitempty" jsonschema:"channel id or name; empty string clears"`
	Tags          *[]string       `json:"tags,omitempty" jsonschema:"replaces all tags"`
	Custom        *map[string]any `json:"custom,omitempty" jsonschema:"free-form fields; replaces the whole object"`
}

// CompanyView is a company as returned to callers.
type CompanyView struct {
	ID              uuid.UUID       `json:"id"`
	Name            string          `json:"name"`
	Domain          string          `json:"domain,omitempty"`
	Industry        string          `json:"industry,omitempty"`
	SizeRange       string          `json:"size_range,omitempty"`
	Country         string          `json:"country,omitempty"`
	Website         string          `json:"website,omitempty"`
	Description     string          `json:"description,omitempty"`
	OwnerID         *uuid.UUID      `json:"owner_id,omitempty"`
	SourceChannelID *uuid.UUID      `json:"source_channel_id,omitempty"`
	Tags            []string        `json:"tags,omitempty"`
	Custom          json.RawMessage `json:"custom,omitempty"`
	ExternalID      string          `json:"external_id,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

func companyView(c store.Company) CompanyView {
	return CompanyView{
		ID: c.ID, Name: c.Name, Domain: deref(c.Domain), Industry: c.Industry, SizeRange: c.SizeRange,
		Country: c.Country, Website: c.Website, Description: c.Description, OwnerID: c.OwnerID,
		SourceChannelID: c.SourceChannelID, Tags: c.Tags, Custom: emptyObjNil(c.Custom),
		ExternalID: deref(c.ExternalID), CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func emptyObjNil(b json.RawMessage) json.RawMessage {
	if len(b) == 0 || string(b) == "{}" {
		return nil
	}
	return b
}

// findCompany locates the company an input refers to, if any.
//
//nolint:dupl // per-entity adapter for findRecord, where the shared logic lives
func findCompany(ctx context.Context, q *store.Queries, ws uuid.UUID, in CompanyInput) (*store.Company, error) {
	return findRecord("company", in.ID, in.ExternalID,
		func(id uuid.UUID) (store.Company, error) {
			return q.GetCompany(ctx, store.GetCompanyParams{WorkspaceID: ws, ID: id})
		},
		func(ext *string) (store.Company, error) {
			return q.GetCompanyByExternalID(ctx, store.GetCompanyByExternalIDParams{WorkspaceID: ws, ExternalID: ext})
		},
		naturalKey(in.Domain, func(v string) (string, error) { return NormalizeDomain("domain", v) },
			func(d *string) (store.Company, error) {
				return q.GetCompanyByDomain(ctx, store.GetCompanyByDomainParams{WorkspaceID: ws, Domain: d})
			}))
}

// UpsertCompany creates or updates a company and reports whether it was created.
func (s *Service) UpsertCompany(ctx context.Context, a Actor, in CompanyInput) (CompanyView, bool, error) {
	var out store.Company
	created := false
	err := s.tx(ctx, a, PermWrite, func(q *store.Queries) error {
		var err error
		out, created, err = upsertCompany(ctx, q, a.WorkspaceID, in)
		if err != nil {
			return err
		}
		action := "company.update"
		if created {
			action = "company.create"
		}
		return s.audit(ctx, q, a, action, "company", out.ID, map[string]any{"name": out.Name})
	})
	return companyView(out), created, err
}

func upsertCompany(ctx context.Context, q *store.Queries, ws uuid.UUID, in CompanyInput) (store.Company, bool, error) {
	cur, err := findCompany(ctx, q, ws, in)
	if err != nil {
		return store.Company{}, false, err
	}
	created := cur == nil
	c := store.Company{ID: newID(), Tags: []string{}, Custom: json.RawMessage(`{}`)}
	if cur != nil {
		c = *cur
	}
	r := resolvers{ctx, q, ws}
	var p patcher
	p.line(&c.Name, "name", in.Name, 200)
	if in.Domain != nil {
		d, err := NormalizeDomain("domain", *in.Domain)
		p.set(err)
		c.Domain = nilIfEmpty(d)
	}
	p.line(&c.Industry, "industry", in.Industry, 100)
	p.with(&c.SizeRange, in.SizeRange, func(v string) (string, error) {
		if !sizeRanges[v] {
			return "", invalid("size_range", "must be one of 1-10, 11-50, 51-200, 201-1000, 1001-5000, 5000+")
		}
		return v, nil
	})
	p.with(&c.Country, in.Country, func(v string) (string, error) { return cleanCountry("country", v) })
	p.with(&c.Website, in.Website, func(v string) (string, error) { return cleanURL("website", v) })
	p.text(&c.Description, "description", in.Description, 5000)
	p.ref(&c.OwnerID, in.Owner, r.owner)
	p.ref(&c.SourceChannelID, in.SourceChannel, r.channel)
	p.tags(&c.Tags, "tags", in.Tags)
	p.custom(&c.Custom, "custom", in.Custom)
	p.externalID(&c.ExternalID, in.ExternalID)
	if p.err != nil {
		return store.Company{}, false, p.err
	}
	if c.Name == "" {
		return store.Company{}, false, invalid("name", "is required")
	}
	c, err = q.SaveCompany(ctx, store.SaveCompanyParams{
		ID: c.ID, WorkspaceID: ws, Name: c.Name, Domain: c.Domain, Industry: c.Industry, SizeRange: c.SizeRange,
		Country: c.Country, Website: c.Website, Description: c.Description, OwnerID: c.OwnerID,
		SourceChannelID: c.SourceChannelID, Tags: c.Tags, Custom: c.Custom, ExternalID: c.ExternalID,
	})
	return c, created, err
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// CompanyDetail is a company with its counts, for get_company.
type CompanyDetail struct {
	CompanyView
	Interactions int `json:"interaction_count"`
}

// GetCompany returns one company by id or domain.
func (s *Service) GetCompany(ctx context.Context, a Actor, ref string) (CompanyDetail, error) {
	var out CompanyDetail
	err := s.tx(ctx, a, PermRead, func(q *store.Queries) error {
		id, err := resolvers{ctx, q, a.WorkspaceID}.company(ref)
		if err != nil {
			return err
		}
		c, err := q.GetCompany(ctx, store.GetCompanyParams{WorkspaceID: a.WorkspaceID, ID: id})
		if err != nil {
			return notFound("company", err)
		}
		n, err := q.CountInteractions(ctx, store.CountInteractionsParams{WorkspaceID: a.WorkspaceID, CompanyID: &id})
		out = CompanyDetail{CompanyView: companyView(c), Interactions: int(n)}
		return err
	})
	return out, err
}

// ListFilter is shared by the list operations.
type ListFilter struct {
	Query   string `json:"query,omitempty" jsonschema:"case-insensitive substring match"`
	Owner   string `json:"owner,omitempty" jsonschema:"member id or email"`
	Tag     string `json:"tag,omitempty"`
	Company string `json:"company,omitempty" jsonschema:"company id or domain (contacts only)"`
	Cursor  string `json:"cursor,omitempty" jsonschema:"next_cursor from a previous page"`
	Limit   int    `json:"limit,omitempty" jsonschema:"page size, default 25, max 100"`
}

// Page is a page of results with an opaque cursor for the next one.
type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

type listArgs struct {
	query, tag *string
	owner, cur *uuid.UUID
	company    *uuid.UUID
	limit      int32
}

func (f ListFilter) args(r resolvers) (listArgs, error) {
	var la listArgs
	var err error
	if la.query, err = likePattern("query", f.Query); err != nil {
		return la, err
	}
	if f.Tag != "" {
		t := f.Tag
		la.tag = &t
	}
	var p patcher
	p.ref(&la.owner, strPtrIf(f.Owner), r.owner)
	p.ref(&la.company, strPtrIf(f.Company), r.company)
	p.ref(&la.cur, strPtrIf(f.Cursor), func(s string) (uuid.UUID, error) { return parseID("cursor", s) })
	la.limit = clampLimit(f.Limit, 25, 100)
	return la, p.err
}

func strPtrIf(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nextCursor[T any](items []T, limit int32, id func(T) uuid.UUID) string {
	if len(items) < int(limit) || len(items) == 0 {
		return ""
	}
	return id(items[len(items)-1]).String()
}

// ListCompanies returns a page of companies, newest first.
func (s *Service) ListCompanies(ctx context.Context, a Actor, f ListFilter) (Page[CompanyView], error) {
	var out Page[CompanyView]
	err := s.tx(ctx, a, PermRead, func(q *store.Queries) error {
		la, err := f.args(resolvers{ctx, q, a.WorkspaceID})
		if err != nil {
			return err
		}
		rows, err := q.ListCompanies(ctx, store.ListCompaniesParams{
			WorkspaceID: a.WorkspaceID, Query: la.query, OwnerID: la.owner, Tag: la.tag, Cursor: la.cur, Lim: la.limit,
		})
		if err != nil {
			return err
		}
		out.Items = make([]CompanyView, 0, len(rows))
		for _, c := range rows {
			out.Items = append(out.Items, companyView(c))
		}
		out.NextCursor = nextCursor(rows, la.limit, func(c store.Company) uuid.UUID { return c.ID })
		return nil
	})
	return out, err
}
