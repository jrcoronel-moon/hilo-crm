package crm

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/jrcoronel-moon/hilo-crm/internal/store"
)

// InteractionFilter narrows the interactions list.
type InteractionFilter struct {
	Type      string
	Direction string
	Channel   string
	Query     string
	Cursor    string
	Limit     int
}

// InteractionRow is one line of the interactions list.
type InteractionRow struct {
	ID               uuid.UUID
	Type, Direction  string
	Subject, Summary string
	OccurredAt       time.Time
	OpportunityID    *uuid.UUID
	Opportunity      string
	Company          string
	Contact          string
	Channel          string
}

// ListInteractions returns recent interactions, newest first.
func (s *Service) ListInteractions(ctx context.Context, a Actor, f InteractionFilter) (Page[InteractionRow], error) {
	var out Page[InteractionRow]
	if f.Type != "" {
		if err := oneOf("type", f.Type, InteractionTypes); err != nil {
			return out, err
		}
	}
	if f.Direction != "" {
		if err := oneOf("direction", f.Direction, InteractionDirections); err != nil {
			return out, err
		}
	}
	err := s.tx(ctx, a, PermRead, func(q *store.Queries) error {
		p := store.ListInteractionsParams{WorkspaceID: a.WorkspaceID, Type: strPtrIf(f.Type), Direction: strPtrIf(f.Direction), Lim: clampLimit(f.Limit, 50, 200)}
		var err error
		if p.Query, err = likePattern(f.Query); err != nil {
			return err
		}
		var pt patcher
		r := resolvers{ctx, q, a.WorkspaceID}
		pt.ref(&p.ChannelID, strPtrIf(f.Channel), r.channel)
		if pt.err != nil {
			return pt.err
		}
		if f.Cursor != "" {
			at, id, err := parseTimeCursor(f.Cursor)
			if err != nil {
				return err
			}
			p.CursorAt, p.CursorID = &at, &id
		}
		rows, err := q.ListInteractions(ctx, p)
		if err != nil {
			return err
		}
		out.Items = make([]InteractionRow, 0, len(rows))
		for _, r := range rows {
			contact := r.ContactName
			if contact == "" {
				contact = deref(r.ContactEmail)
			}
			out.Items = append(out.Items, InteractionRow{ID: r.ID, Type: r.Type, Direction: r.Direction, Subject: r.Subject, Summary: r.Summary,
				OccurredAt: r.OccurredAt, OpportunityID: r.OpportunityID, Opportunity: deref(r.OpportunityTitle), Company: deref(r.CompanyName),
				Contact: contact, Channel: deref(r.ChannelName)})
		}
		if len(rows) == int(p.Lim) {
			last := rows[len(rows)-1]
			out.NextCursor = last.OccurredAt.UTC().Format(time.RFC3339Nano) + "_" + last.ID.String()
		}
		return nil
	})
	return out, err
}

// parseTimeCursor reads the "occurred_at_id" keyset cursor of the
// interactions list (ordered by when things happened, not when logged).
func parseTimeCursor(c string) (time.Time, uuid.UUID, error) {
	ts, id, ok := strings.Cut(c, "_")
	at, err1 := time.Parse(time.RFC3339Nano, ts)
	uid, err2 := uuid.Parse(id)
	if !ok || err1 != nil || err2 != nil {
		return time.Time{}, uuid.Nil, invalid("cursor", "invalid cursor")
	}
	return at, uid, nil
}

// TagCount is a tag in use and how many opportunities carry it.
type TagCount struct {
	Tag   string
	Count int
}

// ListTags returns the tags in use, most used first.
func (s *Service) ListTags(ctx context.Context, a Actor) ([]TagCount, error) {
	var out []TagCount
	err := s.tx(ctx, a, PermRead, func(q *store.Queries) error {
		rows, err := q.ListTags(ctx, a.WorkspaceID)
		for _, r := range rows {
			out = append(out, TagCount{Tag: r.Tag, Count: int(r.N)})
		}
		return err
	})
	return out, err
}

// BulkInput applies one action to several opportunities.
type BulkInput struct {
	IDs    []string
	Action string // move | tag | lost
	Stage  string // move: target stage id or name
	Tag    string // tag: tag to add
	Reason string // lost: why
}

// BulkResult reports what happened per opportunity.
type BulkResult struct {
	Done   int
	Failed []string // "title: reason"
}

const maxBulk = 200

// Bulk runs the action on each opportunity through the regular single-item
// operations, so every rule (permissions, lost reason, history, audit)
// applies exactly as for one.
func (s *Service) Bulk(ctx context.Context, a Actor, in BulkInput) (BulkResult, error) {
	var res BulkResult
	if len(in.IDs) == 0 {
		return res, invalid("ids", "select at least one opportunity")
	}
	if len(in.IDs) > maxBulk {
		return res, invalid("ids", "at most %d at a time", maxBulk)
	}
	if err := a.require(PermWrite); err != nil {
		return res, err
	}
	tag := strings.ToLower(strings.TrimSpace(in.Tag))
	if in.Action == "tag" {
		if _, err := cleanTags("tag", []string{tag}); err != nil || tag == "" {
			return res, invalid("tag", "invalid tag")
		}
	}
	for _, id := range in.IDs {
		opp, err := s.GetOpportunity(ctx, a, id)
		if err != nil {
			res.Failed = append(res.Failed, id+": "+err.Error())
			continue
		}
		switch in.Action {
		case "move":
			_, err = s.MoveOpportunityStage(ctx, a, MoveInput{Opportunity: id, Stage: in.Stage, Reason: in.Reason})
		case "lost":
			err = s.markLost(ctx, a, opp, in.Reason)
		case "tag":
			if !slices.Contains(opp.Tags, tag) {
				tags := append(slices.Clone(opp.Tags), tag)
				_, _, err = s.UpsertOpportunity(ctx, a, OpportunityInput{ID: &id, Tags: &tags})
			}
		default:
			return res, invalid("action", "must be move, tag or lost")
		}
		if err != nil {
			var ve *ValidationError
			if !errors.As(err, &ve) && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrConflict) {
				return res, err
			}
			res.Failed = append(res.Failed, opp.Title+": "+err.Error())
			continue
		}
		res.Done++
	}
	return res, nil
}

// markLost moves an opportunity to the first lost stage of its pipeline.
func (s *Service) markLost(ctx context.Context, a Actor, opp OpportunityDetail, reason string) error {
	pipes, err := s.ListPipelines(ctx, a)
	if err != nil {
		return err
	}
	for _, p := range pipes {
		if p.ID != opp.PipelineID {
			continue
		}
		for _, st := range p.Stages {
			if st.Category == CatLost {
				_, err := s.MoveOpportunityStage(ctx, a, MoveInput{Opportunity: opp.ID.String(), Stage: st.ID.String(), Reason: reason})
				return err
			}
		}
	}
	return &notFoundError{what: "lost stage"}
}

// FindOrCreateCompany returns the company with that domain (preferred) or
// exact name, creating it when neither exists. Used by the web "new
// opportunity" form, where people type a name rather than an id.
func (s *Service) FindOrCreateCompany(ctx context.Context, a Actor, name, domain string) (uuid.UUID, error) {
	name, err := cleanLine("company", name, 200)
	if err != nil {
		return uuid.Nil, err
	}
	if domain != "" {
		if domain, err = NormalizeDomain("domain", domain); err != nil {
			return uuid.Nil, err
		}
	}
	if name == "" && domain == "" {
		return uuid.Nil, nil
	}
	var id uuid.UUID
	err = s.tx(ctx, a, PermWrite, func(q *store.Queries) error {
		// Find first, and never modify what is found: this is a lookup that
		// creates only when nothing matches.
		var c store.Company
		var err error
		if domain != "" {
			c, err = q.GetCompanyByDomain(ctx, store.GetCompanyByDomainParams{WorkspaceID: a.WorkspaceID, Domain: &domain})
		} else {
			c, err = q.GetCompanyByName(ctx, store.GetCompanyByNameParams{WorkspaceID: a.WorkspaceID, Name: name})
		}
		if err == nil {
			id = c.ID
			return nil
		} else if !isNoRows(err) {
			return err
		}
		in := CompanyInput{Name: &name}
		if domain != "" {
			in.Domain = &domain
			if name == "" {
				in.Name = &domain
			}
		}
		c, _, err = upsertCompany(ctx, q, a.WorkspaceID, in)
		if err != nil {
			return err
		}
		id = c.ID
		return s.audit(ctx, q, a, "company.create", "company", c.ID, map[string]any{"name": c.Name})
	})
	return id, err
}

// CompanyRow is one line of the companies list.
type CompanyRow struct {
	ID                     uuid.UUID
	Name, Domain, Industry string
	Country                string
	Tags                   []string
	Contacts, OpenOpps     int
	LastActivity           *time.Time
}

// CompanyRows lists companies with their counts (web table).
func (s *Service) CompanyRows(ctx context.Context, a Actor, f ListFilter) (Page[CompanyRow], error) {
	var out Page[CompanyRow]
	err := s.tx(ctx, a, PermRead, func(q *store.Queries) error {
		la, err := f.args(resolvers{ctx, q, a.WorkspaceID})
		if err != nil {
			return err
		}
		rows, err := q.CompaniesWithStats(ctx, store.CompaniesWithStatsParams{WorkspaceID: a.WorkspaceID, Query: la.query, Tag: la.tag, Cursor: la.cur, Lim: la.limit})
		if err != nil {
			return err
		}
		out.Items = make([]CompanyRow, 0, len(rows))
		for _, r := range rows {
			out.Items = append(out.Items, CompanyRow{ID: r.ID, Name: r.Name, Domain: deref(r.Domain), Industry: r.Industry, Country: r.Country,
				Tags: r.Tags, Contacts: int(r.Contacts), OpenOpps: int(r.OpenOpps), LastActivity: notEpoch(r.LastActivity)})
		}
		out.NextCursor = nextCursor(rows, la.limit, func(r store.CompaniesWithStatsRow) uuid.UUID { return r.ID })
		return nil
	})
	return out, err
}

// ContactRow is one line of the contacts list.
type ContactRow struct {
	ID                        uuid.UUID
	Name, Email, Title, Phone string
	CompanyID                 *uuid.UUID
	Company                   string
	Tags                      []string
	LastActivity              *time.Time
}

// ContactRows lists contacts with their company (web table).
func (s *Service) ContactRows(ctx context.Context, a Actor, f ListFilter) (Page[ContactRow], error) {
	var out Page[ContactRow]
	err := s.tx(ctx, a, PermRead, func(q *store.Queries) error {
		la, err := f.args(resolvers{ctx, q, a.WorkspaceID})
		if err != nil {
			return err
		}
		rows, err := q.ContactsWithStats(ctx, store.ContactsWithStatsParams{WorkspaceID: a.WorkspaceID, Query: la.query, CompanyID: la.company,
			Tag: la.tag, Cursor: la.cur, Lim: la.limit})
		if err != nil {
			return err
		}
		out.Items = make([]ContactRow, 0, len(rows))
		for _, r := range rows {
			out.Items = append(out.Items, ContactRow{ID: r.ID, Name: strings.TrimSpace(r.FirstName + " " + r.LastName), Email: deref(r.Email),
				Title: r.Title, Phone: r.Phone, CompanyID: r.CompanyID, Company: deref(r.CompanyName), Tags: r.Tags, LastActivity: notEpoch(r.LastActivity)})
		}
		out.NextCursor = nextCursor(rows, la.limit, func(r store.ContactsWithStatsRow) uuid.UUID { return r.ID })
		return nil
	})
	return out, err
}

// notEpoch maps the SQL "no activity" sentinel (epoch) to nil.
func notEpoch(t time.Time) *time.Time {
	if t.Unix() == 0 {
		return nil
	}
	return &t
}
