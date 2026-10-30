package crm

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"github.com/jrcoronel-moon/hilo-crm/internal/store"
)

// ChannelKinds are the allowed channel kinds.
var ChannelKinds = []string{"web", "form", "email", "phone", "social", "referral", "event", "outbound", "partner", "ads", "videocall", "video", "other"}

// ChannelView is a lead/interaction channel.
type ChannelView struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
	Kind string    `json:"kind"`
}

// ListChannels returns the active channels.
func (s *Service) ListChannels(ctx context.Context, a Actor) ([]ChannelView, error) {
	var out []ChannelView
	err := s.tx(ctx, a, PermRead, func(q *store.Queries) error {
		rows, err := q.ListChannels(ctx, a.WorkspaceID)
		out = make([]ChannelView, 0, len(rows))
		for _, c := range rows {
			out = append(out, ChannelView{ID: c.ID, Name: c.Name, Kind: c.Kind})
		}
		return err
	})
	return out, err
}

// ChannelInput creates (no ID) or updates a channel.
type ChannelInput struct {
	ID   string `json:"id,omitempty" jsonschema:"existing channel id or name to update"`
	Name string `json:"name"`
	Kind string `json:"kind" jsonschema:"web, form, email, phone, social, referral, event, outbound, partner, ads, videocall, video or other"`
}

// UpsertChannel creates or updates a channel (admin).
func (s *Service) UpsertChannel(ctx context.Context, a Actor, in ChannelInput) (ChannelView, error) {
	name, err := requiredName("name", in.Name)
	if err != nil {
		return ChannelView{}, err
	}
	if err := oneOf("kind", in.Kind, ChannelKinds); err != nil {
		return ChannelView{}, err
	}
	var out ChannelView
	err = s.tx(ctx, a, PermAdmin, func(q *store.Queries) error {
		ws := a.WorkspaceID
		if in.ID == "" {
			c, err := q.CreateChannel(ctx, store.CreateChannelParams{WorkspaceID: ws, Name: name, Kind: in.Kind})
			if err != nil {
				return err
			}
			out = ChannelView{ID: c.ID, Name: c.Name, Kind: c.Kind}
			return s.audit(ctx, q, a, "channel.create", "channel", c.ID, map[string]any{"name": name})
		}
		id, err := resolvers{ctx, q, ws}.channel(in.ID)
		if err != nil {
			return err
		}
		if err := q.UpdateChannel(ctx, store.UpdateChannelParams{WorkspaceID: ws, ID: id, Name: name, Kind: in.Kind}); err != nil {
			return err
		}
		out = ChannelView{ID: id, Name: name, Kind: in.Kind}
		return s.audit(ctx, q, a, "channel.update", "channel", id, map[string]any{"name": name})
	})
	return out, err
}

// ArchiveChannel hides a channel; existing references are kept.
func (s *Service) ArchiveChannel(ctx context.Context, a Actor, ref string) error {
	return s.tx(ctx, a, PermAdmin, func(q *store.Queries) error {
		id, err := resolvers{ctx, q, a.WorkspaceID}.channel(ref)
		if err != nil {
			return err
		}
		if _, err := q.ArchiveChannel(ctx, store.ArchiveChannelParams{WorkspaceID: a.WorkspaceID, ID: id}); err != nil {
			return err
		}
		return s.audit(ctx, q, a, "channel.archive", "channel", id, nil)
	})
}

// ArchiveInput archives one record.
type ArchiveInput struct {
	Type string `json:"type" jsonschema:"company, contact or opportunity"`
	ID   string `json:"id"`
}

// Archive soft-deletes a company, contact or opportunity. Archived records
// disappear from lists and lookups but stay in the database and audit log.
func (s *Service) Archive(ctx context.Context, a Actor, in ArchiveInput) error {
	id, err := parseID("id", in.ID)
	if err != nil {
		return err
	}
	return s.tx(ctx, a, PermWrite, func(q *store.Queries) error {
		var n int64
		var err error
		switch in.Type {
		case "company":
			n, err = q.ArchiveCompany(ctx, store.ArchiveCompanyParams{WorkspaceID: a.WorkspaceID, ID: id})
		case "contact":
			n, err = q.ArchiveContact(ctx, store.ArchiveContactParams{WorkspaceID: a.WorkspaceID, ID: id})
		case "opportunity":
			n, err = q.ArchiveOpportunity(ctx, store.ArchiveOpportunityParams{WorkspaceID: a.WorkspaceID, ID: id})
		default:
			return invalid("type", "must be company, contact or opportunity")
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return &notFoundError{what: in.Type}
		}
		return s.audit(ctx, q, a, in.Type+".archive", in.Type, id, nil)
	})
}

// SearchResult is one hit of a global search.
type SearchResult struct {
	Type     string    `json:"type"`
	ID       uuid.UUID `json:"id"`
	Title    string    `json:"title"`
	Subtitle string    `json:"subtitle,omitempty"`
}

// Search looks for companies, contacts and opportunities matching query.
func (s *Service) Search(ctx context.Context, a Actor, query string, limit int) ([]SearchResult, error) {
	pat, err := likePattern(query)
	if err != nil {
		return nil, err
	}
	if pat == nil || len([]rune(strings.TrimSpace(query))) < 2 {
		return nil, invalid("query", "at least 2 characters")
	}
	var out []SearchResult
	err = s.tx(ctx, a, PermRead, func(q *store.Queries) error {
		rows, err := q.Search(ctx, store.SearchParams{WorkspaceID: a.WorkspaceID, Query: *pat, Lim: clampLimit(limit, 10, 50)})
		out = make([]SearchResult, 0, len(rows))
		for _, r := range rows {
			out = append(out, SearchResult{Type: r.Type, ID: r.ID, Title: r.Title, Subtitle: r.Subtitle})
		}
		return err
	})
	return out, err
}

// AuditEntry is one audit log row.
type AuditEntry = store.ListAuditRow

// ListAudit returns recent audit entries (admin), optionally for one record.
func (s *Service) ListAudit(ctx context.Context, a Actor, entityID string, limit int) ([]AuditEntry, error) {
	var out []AuditEntry
	err := s.tx(ctx, a, PermAdmin, func(q *store.Queries) error {
		p := store.ListAuditParams{WorkspaceID: &a.WorkspaceID, Lim: clampLimit(limit, 50, 200)}
		var pt patcher
		pt.ref(&p.EntityID, strPtrIf(entityID), func(v string) (uuid.UUID, error) { return parseID("entity_id", v) })
		if pt.err != nil {
			return pt.err
		}
		var err error
		out, err = q.ListAudit(ctx, p)
		return err
	})
	return out, err
}
