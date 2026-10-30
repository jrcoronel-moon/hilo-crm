package crm

import (
	"context"
	"slices"
	"strconv"
	"time"

	"github.com/jrcoronel-moon/hilo-crm/internal/events"
	"github.com/jrcoronel-moon/hilo-crm/internal/store"
)

// Tools for agents that work in the background: what changed since they last
// looked, and what needs someone's attention.

// ChangesInput pages through the workspace's changes.
type ChangesInput struct {
	Cursor string `json:"cursor,omitempty" jsonschema:"next_cursor of the previous call; omitted = the last 24 hours"`
	Limit  int    `json:"limit,omitempty" jsonschema:"default 50, max 200"`
}

// Changes is a page of changes, oldest first.
type Changes struct {
	Changes    []events.Event `json:"changes"`
	NextCursor string         `json:"next_cursor"` // pass it next time; always set
}

// ListChanges returns what changed (by people, agents and forms) after the
// cursor. The cursor is always returned, so a polling agent never misses or
// repeats a change.
func (s *Service) ListChanges(ctx context.Context, a Actor, in ChangesInput) (Changes, error) {
	out := Changes{Changes: []events.Event{}}
	p := store.ListChangesParams{WorkspaceID: &a.WorkspaceID, Lim: clampLimit(in.Limit, 50, 200), Since: s.now().Add(-24 * time.Hour)}
	if in.Cursor != "" {
		id, err := strconv.ParseInt(in.Cursor, 10, 64)
		if err != nil || id < 0 {
			return out, invalid("cursor", "must be a next_cursor returned by list_changes")
		}
		p.AfterID, p.Since = id, time.Time{}
	}
	err := s.tx(ctx, a, PermRead, func(q *store.Queries) error {
		rows, err := q.ListChanges(ctx, p)
		if err != nil {
			return err
		}
		last := p.AfterID
		for _, r := range rows {
			out.Changes = append(out.Changes, events.FromAudit(store.GetAuditEntryRow(r)))
			last = r.ID
		}
		if len(rows) == 0 && in.Cursor == "" {
			last, err = q.LastAuditID(ctx, &a.WorkspaceID)
		}
		out.NextCursor = strconv.FormatInt(last, 10)
		return err
	})
	return out, err
}

// AttentionInput tunes needs_attention.
type AttentionInput struct {
	StaleDays int `json:"stale_days,omitempty" jsonschema:"days without activity that make an open opportunity stale; default 14"`
	Limit     int `json:"limit,omitempty" jsonschema:"default 25, max 100"`
}

// AttentionItem is an open opportunity and why it needs attention.
type AttentionItem struct {
	Opportunity OpportunityView `json:"opportunity"`
	Reasons     []string        `json:"reasons"` // next_step_overdue, past_expected_close, stale, no_owner, no_next_step
}

// attentionScan caps how many open opportunities are examined.
const attentionScan = 1000

// NeedsAttention lists open opportunities that need someone: overdue next
// steps, expected close dates already passed, no recent activity, no owner,
// no next step. Most reasons first, then the largest amounts.
func (s *Service) NeedsAttention(ctx context.Context, a Actor, in AttentionInput) ([]AttentionItem, error) {
	days := in.StaleDays
	if days <= 0 {
		days = 14
	}
	now := s.now()
	today := now.Format(time.DateOnly)
	staleBefore := now.AddDate(0, 0, -days)
	items := []AttentionItem{}
	f := OpportunityFilter{Status: "open", Limit: 200}
	for scanned := 0; scanned < attentionScan; {
		page, err := s.ListOpportunities(ctx, a, f)
		if err != nil {
			return nil, err
		}
		for _, o := range page.Items {
			if r := attentionReasons(o, now, today, staleBefore); len(r) > 0 {
				items = append(items, AttentionItem{Opportunity: o, Reasons: r})
			}
		}
		scanned += len(page.Items)
		if page.NextCursor == "" {
			break
		}
		f.Cursor = page.NextCursor
	}
	slices.SortStableFunc(items, func(x, y AttentionItem) int {
		if d := len(y.Reasons) - len(x.Reasons); d != 0 {
			return d
		}
		switch {
		case x.Opportunity.Amount > y.Opportunity.Amount:
			return -1
		case x.Opportunity.Amount < y.Opportunity.Amount:
			return 1
		}
		return 0
	})
	return items[:min(len(items), int(clampLimit(in.Limit, 25, 100)))], nil
}

func attentionReasons(o OpportunityView, now time.Time, today string, staleBefore time.Time) []string {
	var r []string
	if o.NextStepAt != nil && o.NextStepAt.Before(now) {
		r = append(r, "next_step_overdue")
	}
	if o.ExpectedCloseDate != "" && o.ExpectedCloseDate < today {
		r = append(r, "past_expected_close")
	}
	last := o.StageEnteredAt
	if o.LastInteractionAt != nil && o.LastInteractionAt.After(last) {
		last = *o.LastInteractionAt
	}
	if last.Before(staleBefore) {
		r = append(r, "stale")
	}
	if o.OwnerID == nil {
		r = append(r, "no_owner")
	}
	if o.NextStep == "" {
		r = append(r, "no_next_step")
	}
	return r
}
