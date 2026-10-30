package crm

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/jrcoronel-moon/hilo-crm/internal/store"
)

// OpportunityInput creates or updates an opportunity (deal or project).
// Matching order for an update: id, then external_id. On create the stage
// defaults to the pipeline's initial stage and the pipeline to the default one.
// Passing a different stage on update is a stage move (recorded in history).
type OpportunityInput struct {
	ID                *string         `json:"id,omitempty" jsonschema:"existing opportunity id to update"`
	ExternalID        *string         `json:"external_id,omitempty" jsonschema:"your own stable id for idempotent upserts"`
	Title             *string         `json:"title,omitempty"`
	Kind              *string         `json:"kind,omitempty" jsonschema:"deal (default) or project"`
	Company           *string         `json:"company,omitempty" jsonschema:"company id, domain or external_id; empty string clears"`
	PrimaryContact    *string         `json:"primary_contact,omitempty" jsonschema:"contact id, email or external_id; empty string clears"`
	Pipeline          *string         `json:"pipeline,omitempty" jsonschema:"pipeline id or name; only on create (default pipeline if omitted)"`
	Stage             *string         `json:"stage,omitempty" jsonschema:"stage id or name; omitted on create = the initial stage"`
	LostReason        *string         `json:"lost_reason,omitempty" jsonschema:"required when moving to a lost stage"`
	Amount            *float64        `json:"amount,omitempty" jsonschema:"decimal amount, up to 2 decimals"`
	Currency          *string         `json:"currency,omitempty" jsonschema:"ISO 4217 code, default USD"`
	Probability       *int            `json:"probability,omitempty" jsonschema:"0-100; overrides the stage probability"`
	ExpectedCloseDate *string         `json:"expected_close_date,omitempty" jsonschema:"YYYY-MM-DD; empty string clears"`
	SourceChannel     *string         `json:"source_channel,omitempty" jsonschema:"channel id or name; empty string clears"`
	Owner             *string         `json:"owner,omitempty" jsonschema:"member id or email; empty string clears"`
	NextStep          *string         `json:"next_step,omitempty" jsonschema:"what happens next"`
	NextStepAt        *string         `json:"next_step_at,omitempty" jsonschema:"RFC 3339 timestamp or YYYY-MM-DD; empty string clears"`
	Description       *string         `json:"description,omitempty"`
	Tags              *[]string       `json:"tags,omitempty" jsonschema:"replaces all tags"`
	Custom            *map[string]any `json:"custom,omitempty" jsonschema:"free-form fields; replaces the whole object"`
	Links             *[]Link         `json:"links,omitempty" jsonschema:"external files, folders or recordings about this opportunity; replaces all links"`
	CreatedAt         *string         `json:"created_at,omitempty" jsonschema:"for migrations (needs crm:admin): original creation date, RFC 3339 or YYYY-MM-DD, in the past; applied on create, ignored on updates"`
	ClosedAt          *string         `json:"closed_at,omitempty" jsonschema:"for migrations (needs crm:admin): original close date of an opportunity created in a won or lost stage, in the past; ignored on updates"`
	Contacts          []ContactRole   `json:"contacts,omitempty" jsonschema:"contacts to link (added, never removed)"`
}

// ContactRole links a contact to an opportunity.
type ContactRole struct {
	Contact string `json:"contact" jsonschema:"contact id, email or external_id"`
	Role    string `json:"role,omitempty" jsonschema:"e.g. decision maker, champion"`
}

// OpportunityView is an opportunity as returned to callers.
type OpportunityView struct {
	ID                uuid.UUID       `json:"id"`
	Title             string          `json:"title"`
	Kind              string          `json:"kind"`
	Status            string          `json:"status"`
	PipelineID        uuid.UUID       `json:"pipeline_id"`
	StageID           uuid.UUID       `json:"stage_id"`
	StageName         string          `json:"stage"`
	StageCategory     string          `json:"stage_category"`
	Amount            float64         `json:"amount"`
	Currency          string          `json:"currency"`
	Probability       int             `json:"probability"`
	ExpectedCloseDate string          `json:"expected_close_date,omitempty"`
	CompanyID         *uuid.UUID      `json:"company_id,omitempty"`
	CompanyName       string          `json:"company_name,omitempty"`
	PrimaryContactID  *uuid.UUID      `json:"primary_contact_id,omitempty"`
	OwnerID           *uuid.UUID      `json:"owner_id,omitempty"`
	SourceChannelID   *uuid.UUID      `json:"source_channel_id,omitempty"`
	NextStep          string          `json:"next_step,omitempty"`
	NextStepAt        *time.Time      `json:"next_step_at,omitempty"`
	LostReason        string          `json:"lost_reason,omitempty"`
	Description       string          `json:"description,omitempty"`
	Tags              []string        `json:"tags,omitempty"`
	Custom            json.RawMessage `json:"custom,omitempty"`
	Links             []Link          `json:"links,omitempty"`
	ExternalID        string          `json:"external_id,omitempty"`
	StageEnteredAt    time.Time       `json:"stage_entered_at"`
	LastInteractionAt *time.Time      `json:"last_interaction_at,omitempty"`
	ClosedAt          *time.Time      `json:"closed_at,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
}

func opportunityView(o store.Opportunity, st store.PipelineStage, companyName string) OpportunityView {
	prob := int(st.Probability)
	if o.Probability != nil {
		prob = int(*o.Probability)
	}
	v := OpportunityView{
		ID: o.ID, Title: o.Title, Kind: o.Kind, Status: o.Status, PipelineID: o.PipelineID, StageID: o.StageID,
		StageName: st.Name, StageCategory: st.Category, Amount: MinorToAmount(o.AmountMinor), Currency: o.Currency,
		Probability: prob, CompanyID: o.CompanyID, CompanyName: companyName, PrimaryContactID: o.PrimaryContactID,
		OwnerID: o.OwnerID, SourceChannelID: o.SourceChannelID, NextStep: o.NextStep, NextStepAt: o.NextStepAt,
		LostReason: o.LostReason, Description: o.Description, Tags: o.Tags, Custom: emptyObjNil(o.Custom), Links: linksOf(o.Links),
		ExternalID: deref(o.ExternalID), StageEnteredAt: o.StageEnteredAt, LastInteractionAt: o.LastInteractionAt,
		ClosedAt: o.ClosedAt, CreatedAt: o.CreatedAt, UpdatedAt: o.UpdatedAt,
	}
	if o.ExpectedCloseDate != nil {
		v.ExpectedCloseDate = o.ExpectedCloseDate.Format(time.DateOnly)
	}
	return v
}

func findOpportunity(ctx context.Context, q *store.Queries, ws uuid.UUID, in OpportunityInput) (*store.Opportunity, error) {
	return findRecord("opportunity", in.ID, in.ExternalID,
		func(id uuid.UUID) (store.Opportunity, error) {
			return q.GetOpportunity(ctx, store.GetOpportunityParams{WorkspaceID: ws, ID: id})
		},
		func(ext *string) (store.Opportunity, error) {
			return q.GetOpportunityByExternalID(ctx, store.GetOpportunityByExternalIDParams{WorkspaceID: ws, ExternalID: ext})
		}, nil)
}

// stageMove is a pending stage change, recorded after the row is written.
type stageMove struct {
	from   *store.PipelineStage
	to     store.PipelineStage
	reason string
	at     time.Time
}

// enterStage puts o into stage st, keeping status, closed_at and the lost
// reason consistent with the stage category.
func enterStage(o *store.Opportunity, st store.PipelineStage, reason string, now time.Time) error {
	if st.Category == CatLost && strings.TrimSpace(reason) == "" && strings.TrimSpace(o.LostReason) == "" {
		return invalid("lost_reason", "is required when moving to a lost stage (%s)", st.Name)
	}
	o.StageID = st.ID
	o.StageEnteredAt = now
	o.Status = statusFor(st.Category)
	switch o.Status {
	case "open":
		o.ClosedAt = nil
		o.LostReason = ""
	default:
		o.ClosedAt = &now
		if st.Category == CatLost && strings.TrimSpace(reason) != "" {
			o.LostReason = reason
		}
	}
	return nil
}

// UpsertOpportunity creates or updates an opportunity.
func (s *Service) UpsertOpportunity(ctx context.Context, a Actor, in OpportunityInput) (OpportunityView, bool, error) {
	return s.writeOpportunity(ctx, a, in, false)
}

// writeOpportunity is the shared write path; mustExist turns a would-be
// create into a not-found error (used by stage moves).
func (s *Service) writeOpportunity(ctx context.Context, a Actor, in OpportunityInput, mustExist bool) (OpportunityView, bool, error) {
	var out OpportunityView
	created := false
	err := s.tx(ctx, a, PermWrite, func(q *store.Queries) error {
		if mustExist {
			cur, err := findOpportunity(ctx, q, a.WorkspaceID, in)
			if err != nil {
				return err
			}
			if cur == nil {
				return &notFoundError{what: "opportunity"}
			}
		}
		w, err := s.upsertOpportunity(ctx, q, a, in)
		if err != nil {
			return err
		}
		created = w.created
		if err := s.recordMove(ctx, q, a, w.o.ID, w.move); err != nil {
			return err
		}
		action := "opportunity.update"
		switch {
		case created:
			action = "opportunity.create"
		case w.move != nil:
			action = "opportunity.move"
		}
		if err := s.audit(ctx, q, a, action, "opportunity", w.o.ID, map[string]any{"title": w.o.Title, "stage": w.stage.Name}); err != nil {
			return err
		}
		out, err = s.loadView(ctx, q, a.WorkspaceID, w.o, w.stage)
		return err
	})
	return out, created, err
}

// oppWrite is a prepared opportunity write.
type oppWrite struct {
	o       store.Opportunity
	stage   store.PipelineStage
	move    *stageMove
	created bool
}

func (s *Service) upsertOpportunity(ctx context.Context, q *store.Queries, a Actor, in OpportunityInput) (oppWrite, error) {
	ws := a.WorkspaceID
	cur, err := findOpportunity(ctx, q, ws, in)
	if err != nil {
		return oppWrite{}, err
	}
	w := oppWrite{created: cur == nil}
	hist, err := historicDates(a, in, w.created, s.now())
	if err != nil {
		return oppWrite{}, err
	}
	var curStage store.PipelineStage
	if w.created {
		w.o = store.Opportunity{ID: newID(), WorkspaceID: ws, Kind: "deal", Currency: "USD", Tags: []string{}, Custom: json.RawMessage(`{}`),
			Links: json.RawMessage(`[]`), Status: "open", CreatedAt: hist.created}
		pipe, err := resolvePipeline(ctx, q, ws, deref(in.Pipeline))
		if err != nil {
			return oppWrite{}, err
		}
		w.o.PipelineID = pipe.ID
	} else {
		w.o = *cur
		if err := switchPipeline(ctx, q, ws, &w.o, in); err != nil {
			return oppWrite{}, err
		}
		if curStage, err = q.GetStage(ctx, store.GetStageParams{WorkspaceID: ws, ID: w.o.StageID}); err != nil {
			return oppWrite{}, err
		}
	}
	r := resolvers{ctx, q, ws}
	reason, err := applyOpportunityFields(&w.o, in, r)
	if err != nil {
		return oppWrite{}, err
	}
	if err := s.applyStage(ctx, q, &w, in, curStage, reason, hist); err != nil {
		return oppWrite{}, err
	}
	if w.o, err = saveOpportunity(ctx, q, w.o); err != nil {
		return oppWrite{}, err
	}
	for _, cr := range in.Contacts {
		if err := linkContact(ctx, q, r, w.o.ID, cr); err != nil {
			return oppWrite{}, err
		}
	}
	return w, nil
}

// history holds the dates of an opportunity being created: now, or the
// original ones of a migrated record.
type history struct {
	created, closed time.Time
	closedGiven     bool
}

// earliest is the oldest date a migrated record may carry.
var earliest = time.Date(1990, 1, 1, 0, 0, 0, 0, time.UTC)

// historicDates validates created_at / closed_at: applied only when creating
// (ignored on updates), only for admins (they change the metrics), in the past,
// and closed after created.
func historicDates(a Actor, in OpportunityInput, creating bool, now time.Time) (history, error) {
	h := history{created: now, closed: now}
	if (in.CreatedAt == nil && in.ClosedAt == nil) || !creating {
		return h, nil // on updates they are ignored, so a migration batch can be re-run as is
	}
	if err := a.require(PermAdmin); err != nil {
		return h, invalid("created_at", "setting historical dates needs crm:admin")
	}
	parse := func(field string, v *string, into *time.Time) error {
		if v == nil {
			return nil
		}
		t, err := parseTime(field, *v)
		if err != nil {
			d, derr := parseDate(field, *v)
			if derr != nil || d == nil {
				return err
			}
			t = d
		}
		if t == nil || t.After(now) || t.Before(earliest) {
			return invalid(field, "must be a past date after 1990")
		}
		*into = *t
		return nil
	}
	if err := parse("created_at", in.CreatedAt, &h.created); err != nil {
		return h, err
	}
	if in.ClosedAt != nil {
		h.closedGiven = true
		if err := parse("closed_at", in.ClosedAt, &h.closed); err != nil {
			return h, err
		}
	} else if in.CreatedAt != nil {
		h.closed = h.created // closed without a known date: as old as the record
	}
	if in.CreatedAt == nil {
		h.created = h.closed // only the close date is known
	}
	if h.closed.Before(h.created) {
		return h, invalid("closed_at", "must not be before created_at")
	}
	return h, nil
}

// switchPipeline handles an explicit pipeline on update: moving to another
// pipeline is only possible together with a stage of that pipeline.
func switchPipeline(ctx context.Context, q *store.Queries, ws uuid.UUID, o *store.Opportunity, in OpportunityInput) error {
	if in.Pipeline == nil || strings.TrimSpace(*in.Pipeline) == "" {
		return nil
	}
	pipe, err := resolvePipeline(ctx, q, ws, *in.Pipeline)
	if err != nil || pipe.ID == o.PipelineID {
		return err
	}
	if in.Stage == nil {
		return invalid("stage", "moving to another pipeline requires a stage of that pipeline")
	}
	o.PipelineID = pipe.ID
	return nil
}

// applyOpportunityFields patches the plain fields and returns the cleaned
// lost reason (used by the stage logic).
func applyOpportunityFields(o *store.Opportunity, in OpportunityInput, r resolvers) (string, error) {
	var p patcher
	p.line(&o.Title, "title", in.Title, 200)
	p.with(&o.Kind, in.Kind, func(v string) (string, error) {
		if v != "deal" && v != "project" {
			return "", invalid("kind", "must be deal or project")
		}
		return v, nil
	})
	p.ref(&o.CompanyID, in.Company, r.company)
	p.ref(&o.PrimaryContactID, in.PrimaryContact, r.contact)
	if in.Amount != nil {
		m, err := AmountToMinor("amount", *in.Amount)
		p.set(err)
		o.AmountMinor = m
	}
	p.with(&o.Currency, in.Currency, func(v string) (string, error) { return cleanCurrency("currency", v) })
	if in.Probability != nil {
		v, err := cleanProbability("probability", in.Probability)
		p.set(err)
		o.Probability = v
	}
	if in.ExpectedCloseDate != nil {
		d, err := parseDate("expected_close_date", *in.ExpectedCloseDate)
		p.set(err)
		o.ExpectedCloseDate = d
	}
	if in.NextStepAt != nil {
		t, err := parseTime("next_step_at", *in.NextStepAt)
		p.set(err)
		o.NextStepAt = t
	}
	p.ref(&o.SourceChannelID, in.SourceChannel, r.channel)
	p.ref(&o.OwnerID, in.Owner, r.owner)
	p.line(&o.NextStep, "next_step", in.NextStep, 500)
	p.text(&o.Description, "description", in.Description, 5000)
	p.tags(&o.Tags, "tags", in.Tags)
	p.custom(&o.Custom, "custom", in.Custom)
	p.links(&o.Links, "links", in.Links)
	p.externalID(&o.ExternalID, in.ExternalID)
	var reason string
	p.text(&reason, "lost_reason", in.LostReason, 1000)
	if p.err == nil && o.Title == "" {
		p.err = invalid("title", "is required")
	}
	return reason, p.err
}

// applyStage resolves the target stage (explicit, or the initial stage on
// create) and, if it changes, enters it and prepares the history entry.
func (s *Service) applyStage(ctx context.Context, q *store.Queries, w *oppWrite, in OpportunityInput, curStage store.PipelineStage, reason string, hist history) error {
	ws := w.o.WorkspaceID
	w.stage = curStage
	var err error
	switch {
	case in.Stage != nil && strings.TrimSpace(*in.Stage) != "":
		w.stage, err = resolveStage(ctx, q, ws, w.o.PipelineID, *in.Stage)
	case w.created:
		w.stage, err = q.GetInitialStage(ctx, store.GetInitialStageParams{WorkspaceID: ws, PipelineID: w.o.PipelineID})
	}
	if err != nil {
		return err
	}
	if !w.created && w.stage.ID == curStage.ID {
		if reason != "" && w.o.Status == "lost" {
			w.o.LostReason = reason
		}
		return nil
	}
	at := s.now()
	if w.created {
		at = hist.created
		if w.stage.Category == CatWon || w.stage.Category == CatLost {
			at = hist.closed
		}
	}
	if hist.closedGiven && statusFor(w.stage.Category) == "open" {
		return invalid("closed_at", "only applies to an opportunity created in a won or lost stage")
	}
	if err := enterStage(&w.o, w.stage, reason, at); err != nil {
		return err
	}
	w.move = &stageMove{to: w.stage, reason: reason, at: at}
	if !w.created {
		w.move.from = &curStage
	}
	return nil
}

func saveOpportunity(ctx context.Context, q *store.Queries, o store.Opportunity) (store.Opportunity, error) {
	return q.SaveOpportunity(ctx, store.SaveOpportunityParams{
		ID: o.ID, WorkspaceID: o.WorkspaceID, Title: o.Title, Kind: o.Kind, CompanyID: o.CompanyID,
		PrimaryContactID: o.PrimaryContactID, PipelineID: o.PipelineID, StageID: o.StageID, Status: o.Status,
		AmountMinor: o.AmountMinor, Currency: o.Currency, Probability: o.Probability,
		ExpectedCloseDate: o.ExpectedCloseDate, ClosedAt: o.ClosedAt, LostReason: o.LostReason,
		SourceChannelID: o.SourceChannelID, OwnerID: o.OwnerID, NextStep: o.NextStep, NextStepAt: o.NextStepAt,
		Description: o.Description, Tags: o.Tags, Custom: o.Custom, Links: o.Links, ExternalID: o.ExternalID,
		StageEnteredAt: o.StageEnteredAt, CreatedAt: o.CreatedAt, // created_at is only written on insert
	})
}

func linkContact(ctx context.Context, q *store.Queries, r resolvers, oppID uuid.UUID, cr ContactRole) error {
	cid, err := r.contact(cr.Contact)
	if err != nil {
		return err
	}
	role, err := cleanLine("contacts.role", cr.Role, 50)
	if err != nil {
		return err
	}
	return q.AddOpportunityContact(ctx, store.AddOpportunityContactParams{WorkspaceID: r.ws, OpportunityID: oppID, ContactID: cid, Role: role})
}

func (s *Service) recordMove(ctx context.Context, q *store.Queries, a Actor, oppID uuid.UUID, m *stageMove) error {
	if m == nil {
		return nil
	}
	p := store.InsertStageChangeParams{
		WorkspaceID: a.WorkspaceID, OpportunityID: oppID, ToStageID: &m.to.ID, ToStageName: m.to.Name,
		ToCategory: m.to.Category, Reason: m.reason, ChangedBy: a.userPtr(), ChangedAt: m.at,
	}
	if m.from != nil {
		p.FromStageID, p.FromStageName = &m.from.ID, m.from.Name
	}
	return q.InsertStageChange(ctx, p)
}

func (s *Service) loadView(ctx context.Context, q *store.Queries, ws uuid.UUID, o store.Opportunity, st store.PipelineStage) (OpportunityView, error) {
	name := ""
	if o.CompanyID != nil {
		c, err := q.GetCompany(ctx, store.GetCompanyParams{WorkspaceID: ws, ID: *o.CompanyID})
		if err != nil && !isNoRows(err) {
			return OpportunityView{}, err
		}
		name = c.Name
	}
	return opportunityView(o, st, name), nil
}

// MoveInput moves an opportunity to another stage of its pipeline.
type MoveInput struct {
	Opportunity string `json:"opportunity" jsonschema:"opportunity id or external_id"`
	Stage       string `json:"stage" jsonschema:"target stage id or name (see list_pipelines)"`
	Reason      string `json:"reason,omitempty" jsonschema:"why; required when the target stage is a lost stage"`
}

// MoveOpportunityStage changes an opportunity's stage and records it.
func (s *Service) MoveOpportunityStage(ctx context.Context, a Actor, in MoveInput) (OpportunityView, error) {
	ref := strings.TrimSpace(in.Opportunity)
	oi := OpportunityInput{Stage: &in.Stage, LostReason: &in.Reason}
	if _, err := uuid.Parse(ref); err == nil {
		oi.ID = &ref
	} else {
		oi.ExternalID = &ref
	}
	if ref == "" || strings.TrimSpace(in.Stage) == "" {
		return OpportunityView{}, invalid("opportunity", "opportunity and stage are required")
	}
	v, _, err := s.writeOpportunity(ctx, a, oi, true)
	return v, err
}

// OpportunityDetail adds linked contacts and counts to the view.
type OpportunityDetail struct {
	OpportunityView
	Contacts     []store.ListOpportunityContactsRow `json:"contacts"`
	Interactions int                                `json:"interaction_count"`
}

// GetOpportunity returns one opportunity by id or external_id.
func (s *Service) GetOpportunity(ctx context.Context, a Actor, ref string) (OpportunityDetail, error) {
	var out OpportunityDetail
	err := s.tx(ctx, a, PermRead, func(q *store.Queries) error {
		in := OpportunityInput{}
		ref = strings.TrimSpace(ref)
		if _, err := uuid.Parse(ref); err == nil {
			in.ID = &ref
		} else {
			in.ExternalID = &ref
		}
		o, err := findOpportunity(ctx, q, a.WorkspaceID, in)
		if err != nil {
			return err
		}
		if o == nil {
			return &notFoundError{what: "opportunity"}
		}
		st, err := q.GetStage(ctx, store.GetStageParams{WorkspaceID: a.WorkspaceID, ID: o.StageID})
		if err != nil {
			return err
		}
		v, err := s.loadView(ctx, q, a.WorkspaceID, *o, st)
		if err != nil {
			return err
		}
		contacts, err := q.ListOpportunityContacts(ctx, store.ListOpportunityContactsParams{WorkspaceID: a.WorkspaceID, OpportunityID: o.ID})
		if err != nil {
			return err
		}
		n, err := q.CountInteractions(ctx, store.CountInteractionsParams{WorkspaceID: a.WorkspaceID, OpportunityID: &o.ID})
		out = OpportunityDetail{OpportunityView: v, Contacts: contacts, Interactions: int(n)}
		return err
	})
	return out, err
}

// OpportunityFilter narrows list_opportunities.
type OpportunityFilter struct {
	Pipeline    string `json:"pipeline,omitempty" jsonschema:"pipeline id or name (default pipeline if omitted and stage is a name)"`
	Stage       string `json:"stage,omitempty" jsonschema:"stage id or name"`
	Category    string `json:"category,omitempty" jsonschema:"initial, in_progress, won or lost"`
	Status      string `json:"status,omitempty" jsonschema:"open, won or lost"`
	Owner       string `json:"owner,omitempty" jsonschema:"member id or email"`
	Company     string `json:"company,omitempty" jsonschema:"company id, domain or external_id"`
	Contact     string `json:"contact,omitempty" jsonschema:"contact id, email or external_id (primary or linked)"`
	Channel     string `json:"channel,omitempty" jsonschema:"source channel id or name"`
	Query       string `json:"query,omitempty" jsonschema:"substring of the title"`
	Tag         string `json:"tag,omitempty" jsonschema:"only opportunities with this tag"`
	CloseBefore string `json:"close_before,omitempty" jsonschema:"expected close date on or before YYYY-MM-DD"`
	StaleDays   int    `json:"stale_days,omitempty" jsonschema:"open opportunities with no interaction or stage change in this many days"`
	Cursor      string `json:"cursor,omitempty"`
	Limit       int    `json:"limit,omitempty" jsonschema:"page size, default 25, max 100"`
}

// ListOpportunities returns a filtered page, newest first.
func (s *Service) ListOpportunities(ctx context.Context, a Actor, f OpportunityFilter) (Page[OpportunityView], error) {
	var out Page[OpportunityView]
	err := s.tx(ctx, a, PermRead, func(q *store.Queries) error {
		ws := a.WorkspaceID
		r := resolvers{ctx, q, ws}
		p := store.ListOpportunitiesParams{WorkspaceID: ws, Lim: clampLimit(f.Limit, 25, 100)}
		var pt patcher
		if f.Pipeline != "" || (f.Stage != "" && uuid.Validate(f.Stage) != nil) {
			pipe, err := resolvePipeline(ctx, q, ws, f.Pipeline)
			if err != nil {
				return err
			}
			p.PipelineID = &pipe.ID
			if f.Stage != "" {
				st, err := resolveStage(ctx, q, ws, pipe.ID, f.Stage)
				if err != nil {
					return err
				}
				p.StageID = &st.ID
			}
		} else if f.Stage != "" {
			pt.ref(&p.StageID, &f.Stage, func(v string) (uuid.UUID, error) { return parseID("stage", v) })
		}
		if f.Category != "" {
			if _, ok := defaultProbability[f.Category]; !ok {
				return invalid("category", "must be initial, in_progress, won or lost")
			}
			p.Category = &f.Category
		}
		if f.Status != "" {
			if f.Status != "open" && f.Status != "won" && f.Status != "lost" {
				return invalid("status", "must be open, won or lost")
			}
			p.Status = &f.Status
		}
		pt.ref(&p.OwnerID, strPtrIf(f.Owner), r.owner)
		pt.ref(&p.CompanyID, strPtrIf(f.Company), r.company)
		pt.ref(&p.ContactID, strPtrIf(f.Contact), r.contact)
		pt.ref(&p.ChannelID, strPtrIf(f.Channel), r.channel)
		pt.ref(&p.Cursor, strPtrIf(f.Cursor), func(v string) (uuid.UUID, error) { return parseID("cursor", v) })
		if pt.err != nil {
			return pt.err
		}
		var err error
		if p.Query, err = likePattern(f.Query); err != nil {
			return err
		}
		if t := strings.ToLower(strings.TrimSpace(f.Tag)); t != "" {
			p.Tag = &t
		}
		if p.CloseBefore, err = parseDate("close_before", f.CloseBefore); err != nil {
			return err
		}
		if f.StaleDays < 0 || f.StaleDays > 3650 {
			return invalid("stale_days", "must be between 1 and 3650")
		}
		if f.StaleDays > 0 {
			t := s.now().AddDate(0, 0, -f.StaleDays)
			p.StaleBefore = &t
		}
		rows, err := q.ListOpportunities(ctx, p)
		if err != nil {
			return err
		}
		out.Items = make([]OpportunityView, 0, len(rows))
		for _, row := range rows {
			st := store.PipelineStage{ID: row.StageID, Name: row.StageName, Category: row.StageCategory, Probability: row.StageProbability}
			out.Items = append(out.Items, opportunityView(listRowOpportunity(row), st, deref(row.CompanyName)))
		}
		out.NextCursor = nextCursor(rows, p.Lim, func(r store.ListOpportunitiesRow) uuid.UUID { return r.ID })
		return nil
	})
	return out, err
}

func listRowOpportunity(r store.ListOpportunitiesRow) store.Opportunity {
	return store.Opportunity{
		ID: r.ID, WorkspaceID: r.WorkspaceID, Title: r.Title, Kind: r.Kind, CompanyID: r.CompanyID,
		PrimaryContactID: r.PrimaryContactID, PipelineID: r.PipelineID, StageID: r.StageID, Status: r.Status,
		AmountMinor: r.AmountMinor, Currency: r.Currency, Probability: r.Probability,
		ExpectedCloseDate: r.ExpectedCloseDate, ClosedAt: r.ClosedAt, LostReason: r.LostReason,
		SourceChannelID: r.SourceChannelID, OwnerID: r.OwnerID, NextStep: r.NextStep, NextStepAt: r.NextStepAt,
		Description: r.Description, Tags: r.Tags, Custom: r.Custom, Links: r.Links, ExternalID: r.ExternalID,
		StageEnteredAt: r.StageEnteredAt, LastInteractionAt: r.LastInteractionAt, CreatedAt: r.CreatedAt,
		UpdatedAt: r.UpdatedAt, ArchivedAt: r.ArchivedAt,
	}
}
