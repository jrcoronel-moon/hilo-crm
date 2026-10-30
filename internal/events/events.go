// Package events relays every committed change in a workspace, as it happens,
// to whoever is watching: browsers (server-sent events) and hiloctl watch.
//
// The source is the audit log: each audited write notifies the hilo_events
// channel on commit (migration 0005) with references only, and the listener
// reads the row back with the workspace bound, so row-level security applies.
package events

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/jrcoronel-moon/hilo-crm/internal/db"
	"github.com/jrcoronel-moon/hilo-crm/internal/store"
)

// Event is one change, as watchers see it.
type Event struct {
	ID        int64      `json:"id"`
	At        time.Time  `json:"at"`
	Workspace uuid.UUID  `json:"workspace_id"`
	Action    string     `json:"action"`
	Entity    string     `json:"entity"`
	EntityID  *uuid.UUID `json:"entity_id,omitempty"`
	Actor     string     `json:"actor"` // the agent's name, the person's email, or the public form
	Via       string     `json:"via"`   // session | oauth | pat | form | system
	Summary   string     `json:"summary"`
	Self      bool       `json:"self,omitempty"` // set per browser: the viewer did it

	actorUser *uuid.UUID
}

// By reports whether the person signed in as user made this change in the web.
func (e Event) By(user uuid.UUID) bool {
	return e.Via == "session" && e.actorUser != nil && *e.actorUser == user
}

var verbs = map[string]string{
	"company.create": "creó la empresa", "company.update": "actualizó la empresa", "company.archive": "archivó la empresa",
	"contact.create": "creó el contacto", "contact.update": "actualizó el contacto", "contact.archive": "archivó el contacto",
	"opportunity.create": "creó la oportunidad", "opportunity.update": "actualizó la oportunidad",
	"opportunity.move": "movió", "opportunity.archive": "archivó la oportunidad",
	"interaction.create": "registró una interacción", "lead.capture": "recibió un lead",
	"pipeline.configure": "configuró el pipeline", "pipeline.archive": "archivó el pipeline",
	"channel.create": "creó el canal", "channel.update": "actualizó el canal", "channel.archive": "archivó el canal",
	"form.create": "creó el formulario", "form.update": "actualizó el formulario",
}

// FromAudit builds the event of an audit row.
func FromAudit(r store.GetAuditEntryRow) Event {
	e := Event{ID: r.ID, At: r.CreatedAt, Action: r.Action, Entity: r.EntityType, EntityID: r.EntityID,
		Via: r.ActorKind, actorUser: r.ActorUserID}
	if r.WorkspaceID != nil {
		e.Workspace = *r.WorkspaceID
	}
	switch {
	case r.ActorKind == "form":
		e.Actor = "Formulario público"
	case r.ActorClient != "" && r.ActorKind != "session":
		e.Actor = r.ActorClient
	case r.ActorEmail != "":
		e.Actor = r.ActorEmail
	default:
		e.Actor = "Sistema"
	}
	var d map[string]any
	_ = json.Unmarshal(r.Details, &d)
	str := func(k string) string { s, _ := d[k].(string); return s }
	verb, ok := verbs[r.Action]
	if !ok {
		verb = r.Action
	}
	parts := []string{verb}
	if name := firstNonEmpty(str("title"), str("name"), str("form")); name != "" {
		parts = append(parts, "«"+name+"»")
	}
	switch {
	case r.Action == "opportunity.move" && str("stage") != "":
		parts = append(parts, "a "+str("stage"))
	case r.Action == "interaction.create" && str("type") != "":
		parts = append(parts, "("+str("type")+")")
	}
	e.Summary = strings.Join(parts, " ")
	return e
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// ───── listener ─────

const retryEvery = 2 * time.Second

// Listen relays committed changes to publish until ctx ends. It holds one
// database connection and reconnects if it drops.
func Listen(ctx context.Context, d *db.DB, log *slog.Logger, publish func(Event)) {
	for ctx.Err() == nil {
		err := listenOnce(ctx, d, log, publish)
		if ctx.Err() != nil {
			return
		}
		log.Warn("live updates: listener stopped, reconnecting", "err", err)
		select {
		case <-ctx.Done():
		case <-time.After(retryEvery):
		}
	}
}

type notice struct {
	ID int64     `json:"id"`
	WS uuid.UUID `json:"ws"`
}

func listenOnce(ctx context.Context, d *db.DB, log *slog.Logger, publish func(Event)) error {
	pc, err := d.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	conn := pc.Hijack() // a listening connection must not go back to the pool
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	if _, err := conn.Exec(ctx, "LISTEN hilo_events"); err != nil {
		return err
	}
	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		var ref notice
		if err := json.Unmarshal([]byte(n.Payload), &ref); err != nil {
			log.Warn("live updates: bad notification", "err", err)
			continue
		}
		var row store.GetAuditEntryRow
		err = d.InWorkspace(ctx, ref.WS, func(q *store.Queries) error {
			var err error
			row, err = q.GetAuditEntry(ctx, store.GetAuditEntryParams{WorkspaceID: &ref.WS, ID: ref.ID})
			return err
		})
		if err != nil {
			log.Warn("live updates: read audit entry", "err", err)
			continue
		}
		publish(FromAudit(row))
	}
}

// ───── hub ─────

// Limits on browsers watching, so one user or a flood cannot exhaust the
// server: per user (several tabs) and in total.
const (
	MaxPerUser = 8
	MaxTotal   = 2000
	buffer     = 32
)

// ErrTooMany is returned when a subscription would exceed the limits.
var ErrTooMany = errors.New("too many live connections")

// Hub fans events out to the subscribers of each workspace.
type Hub struct {
	mu      sync.Mutex
	subs    map[*Sub]struct{}
	perUser map[uuid.UUID]int
}

// NewHub returns an empty hub.
func NewHub() *Hub { return &Hub{subs: map[*Sub]struct{}{}, perUser: map[uuid.UUID]int{}} }

// Sub is one watcher of one workspace.
type Sub struct {
	C    chan Event
	ws   uuid.UUID
	user uuid.UUID
	hub  *Hub
}

// Subscribe starts watching a workspace on behalf of user.
func (h *Hub) Subscribe(ws, user uuid.UUID) (*Sub, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.subs) >= MaxTotal || h.perUser[user] >= MaxPerUser {
		return nil, ErrTooMany
	}
	s := &Sub{C: make(chan Event, buffer), ws: ws, user: user, hub: h}
	h.subs[s] = struct{}{}
	h.perUser[user]++
	return s, nil
}

// Close stops the subscription.
func (s *Sub) Close() {
	h := s.hub
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.subs[s]; !ok {
		return
	}
	delete(h.subs, s)
	if h.perUser[s.user]--; h.perUser[s.user] == 0 {
		delete(h.perUser, s.user)
	}
}

// Publish delivers e to the workspace's subscribers. A subscriber that is not
// keeping up misses events rather than slowing everyone down; browsers
// refresh what they show, so a missed event is caught up by the next one.
func (h *Hub) Publish(e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		if s.ws != e.Workspace {
			continue
		}
		select {
		case s.C <- e:
		default:
		}
	}
}
