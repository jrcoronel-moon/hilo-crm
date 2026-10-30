package crm

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/jrcoronel-moon/hilo-crm/internal/store"
)

// Interaction types and directions.
var (
	InteractionTypes      = []string{"email", "call", "meeting", "note", "message", "social", "form"}
	InteractionDirections = []string{"inbound", "outbound", "internal"}
)

func oneOf(field, v string, allowed []string) error {
	for _, a := range allowed {
		if v == a {
			return nil
		}
	}
	return invalid(field, "must be one of %s", strings.Join(allowed, ", "))
}

// InteractionInput logs something that happened with a company, contact or
// opportunity (at least one is required).
type InteractionInput struct {
	Type            string `json:"type" jsonschema:"email, call, meeting, note, message, social or form"`
	Direction       string `json:"direction,omitempty" jsonschema:"inbound, outbound or internal (default outbound; internal for notes)"`
	Channel         string `json:"channel,omitempty" jsonschema:"channel id or name"`
	Subject         string `json:"subject,omitempty"`
	Summary         string `json:"summary,omitempty" jsonschema:"what happened; keep it a summary, max 10000 chars"`
	OccurredAt      string `json:"occurred_at,omitempty" jsonschema:"RFC 3339; default now"`
	DurationMinutes *int   `json:"duration_minutes,omitempty"`
	Company         string `json:"company,omitempty" jsonschema:"company id, domain or external_id"`
	Contact         string `json:"contact,omitempty" jsonschema:"contact id, email or external_id"`
	Opportunity     string `json:"opportunity,omitempty" jsonschema:"opportunity id or external_id"`
	ExternalID      string `json:"external_id,omitempty" jsonschema:"your id (e.g. email message id) so retries do not duplicate"`
}

// InteractionView is a logged interaction.
type InteractionView struct {
	ID            uuid.UUID  `json:"id"`
	Type          string     `json:"type"`
	Direction     string     `json:"direction"`
	ChannelID     *uuid.UUID `json:"channel_id,omitempty"`
	Subject       string     `json:"subject,omitempty"`
	Summary       string     `json:"summary,omitempty"`
	OccurredAt    time.Time  `json:"occurred_at"`
	CompanyID     *uuid.UUID `json:"company_id,omitempty"`
	ContactID     *uuid.UUID `json:"contact_id,omitempty"`
	OpportunityID *uuid.UUID `json:"opportunity_id,omitempty"`
	Duplicate     bool       `json:"duplicate,omitempty"`
}

func interactionView(i store.Interaction) InteractionView {
	return InteractionView{
		ID: i.ID, Type: i.Type, Direction: i.Direction, ChannelID: i.ChannelID, Subject: i.Subject,
		Summary: i.Summary, OccurredAt: i.OccurredAt, CompanyID: i.CompanyID, ContactID: i.ContactID,
		OpportunityID: i.OpportunityID,
	}
}

// LogInteraction records an interaction. When the contact belongs to a
// company and no company was given, the interaction is linked to it too, and
// the opportunity's last_interaction_at is advanced.
func (s *Service) LogInteraction(ctx context.Context, a Actor, in InteractionInput) (InteractionView, error) {
	var out InteractionView
	err := s.tx(ctx, a, PermWrite, func(q *store.Queries) error {
		i, dup, err := s.logInteraction(ctx, q, a, in)
		if err != nil {
			return err
		}
		out = interactionView(i)
		out.Duplicate = dup
		if dup {
			return nil
		}
		return s.audit(ctx, q, a, "interaction.create", "interaction", i.ID, map[string]any{"type": i.Type})
	})
	return out, err
}

func (s *Service) logInteraction(ctx context.Context, q *store.Queries, a Actor, in InteractionInput) (store.Interaction, bool, error) {
	ws := a.WorkspaceID
	if in.ExternalID != "" {
		if prev, err := q.GetInteractionByExternalID(ctx, store.GetInteractionByExternalIDParams{WorkspaceID: ws, ExternalID: &in.ExternalID}); err == nil {
			return prev, true, nil
		} else if !isNoRows(err) {
			return store.Interaction{}, false, err
		}
	}
	if err := oneOf("type", in.Type, InteractionTypes); err != nil {
		return store.Interaction{}, false, err
	}
	if in.Direction == "" {
		switch in.Type {
		case "note":
			in.Direction = "internal"
		case "form":
			in.Direction = "inbound"
		default:
			in.Direction = "outbound"
		}
	}
	if err := oneOf("direction", in.Direction, InteractionDirections); err != nil {
		return store.Interaction{}, false, err
	}
	p := store.InsertInteractionParams{WorkspaceID: ws, Type: in.Type, Direction: in.Direction, CreatedBy: a.userPtr()}
	r := resolvers{ctx, q, ws}
	var pt patcher
	pt.ref(&p.ChannelID, strPtrIf(in.Channel), r.channel)
	pt.ref(&p.CompanyID, strPtrIf(in.Company), r.company)
	pt.ref(&p.ContactID, strPtrIf(in.Contact), r.contact)
	pt.ref(&p.OpportunityID, strPtrIf(in.Opportunity), r.opportunity)
	pt.line(&p.Subject, "subject", &in.Subject, 300)
	pt.text(&p.Summary, "summary", &in.Summary, 10000)
	pt.externalID(&p.ExternalID, &in.ExternalID)
	if pt.err != nil {
		return store.Interaction{}, false, pt.err
	}
	if p.CompanyID == nil && p.ContactID == nil && p.OpportunityID == nil {
		return store.Interaction{}, false, invalid("interaction", "link it to a company, contact or opportunity")
	}
	occurred := s.now()
	if t, err := parseTime("occurred_at", in.OccurredAt); err != nil {
		return store.Interaction{}, false, err
	} else if t != nil {
		occurred = *t
	}
	p.OccurredAt = occurred
	if in.DurationMinutes != nil {
		if *in.DurationMinutes < 0 || *in.DurationMinutes > 1440 {
			return store.Interaction{}, false, invalid("duration_minutes", "must be between 0 and 1440")
		}
		d := int32(*in.DurationMinutes)
		p.DurationMinutes = &d
	}
	if p.CompanyID == nil && p.ContactID != nil {
		c, err := q.GetContact(ctx, store.GetContactParams{WorkspaceID: ws, ID: *p.ContactID})
		if err != nil {
			return store.Interaction{}, false, err
		}
		p.CompanyID = c.CompanyID
	}
	i, err := q.InsertInteraction(ctx, p)
	if err != nil {
		return store.Interaction{}, false, err
	}
	if p.OpportunityID != nil {
		if err := q.TouchOpportunityInteraction(ctx, store.TouchOpportunityInteractionParams{WorkspaceID: ws, ID: *p.OpportunityID, At: occurred}); err != nil {
			return store.Interaction{}, false, err
		}
	}
	return i, false, nil
}

// TimelineInput selects whose timeline to read (exactly one).
type TimelineInput struct {
	Opportunity string `json:"opportunity,omitempty" jsonschema:"opportunity id or external_id"`
	Contact     string `json:"contact,omitempty" jsonschema:"contact id, email or external_id"`
	Company     string `json:"company,omitempty" jsonschema:"company id, domain or external_id"`
	Limit       int    `json:"limit,omitempty" jsonschema:"default 30, max 200"`
}

// TimelineEntry is one interaction or stage change.
type TimelineEntry struct {
	Kind          string     `json:"kind"` // interaction | stage_change
	ID            uuid.UUID  `json:"id"`
	At            time.Time  `json:"at"`
	Label         string     `json:"label"`  // interaction type, or the stage entered
	Detail        string     `json:"detail"` // direction, or the stage category
	Subject       string     `json:"subject,omitempty"`
	Summary       string     `json:"summary,omitempty"`
	OpportunityID *uuid.UUID `json:"opportunity_id,omitempty"`
}

// Timeline returns the history of one record, newest first.
func (s *Service) Timeline(ctx context.Context, a Actor, in TimelineInput) ([]TimelineEntry, error) {
	set := 0
	for _, v := range []string{in.Opportunity, in.Contact, in.Company} {
		if v != "" {
			set++
		}
	}
	if set != 1 {
		return nil, invalid("timeline", "give exactly one of opportunity, contact or company")
	}
	var out []TimelineEntry
	err := s.tx(ctx, a, PermRead, func(q *store.Queries) error {
		r := resolvers{ctx, q, a.WorkspaceID}
		p := store.TimelineParams{WorkspaceID: a.WorkspaceID, Lim: clampLimit(in.Limit, 30, 200)}
		var pt patcher
		pt.ref(&p.CompanyID, strPtrIf(in.Company), r.company)
		pt.ref(&p.ContactID, strPtrIf(in.Contact), r.contact)
		pt.ref(&p.OpportunityID, strPtrIf(in.Opportunity), r.opportunity)
		if pt.err != nil {
			return pt.err
		}
		rows, err := q.Timeline(ctx, p)
		if err != nil {
			return err
		}
		out = make([]TimelineEntry, 0, len(rows))
		for _, r := range rows {
			out = append(out, TimelineEntry{Kind: r.Kind, ID: r.ID, At: r.At, Label: r.Label, Detail: r.Detail,
				Subject: r.Subject, Summary: r.Summary, OpportunityID: r.OpportunityID})
		}
		return nil
	})
	return out, err
}
