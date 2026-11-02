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
	"github.com/jrcoronel-moon/hilo-crm/internal/i18n"
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
	Actor     string     `json:"actor"`          // the agent's name, the person's email, or the public form
	User      string     `json:"user,omitempty"` // when an agent acted: the email of the person it acts for
	Via       string     `json:"via"`            // session | oauth | pat | form | system
	Summary   string     `json:"summary"`
	Self      bool       `json:"self,omitempty"` // set per browser: the viewer did it

	actorUser *uuid.UUID
	name      string // the record's title or name, for SummaryIn
	stage     string // opportunity.move: the stage it went to
	itype     string // interaction.create: email, call…
}

// SummaryIn is Summary in the given interface language.
func (e Event) SummaryIn(lang string) string {
	verb, ok := verbs[e.Action]
	if !ok {
		return e.Summary
	}
	parts := []string{i18n.T(lang, verb)}
	if e.name != "" {
		parts = append(parts, "«"+e.name+"»")
	}
	if e.stage != "" {
		parts = append(parts, i18n.T(lang, "a %s", e.stage))
	}
	if e.itype != "" {
		parts = append(parts, "("+e.itype+")")
	}
	return strings.Join(parts, " ")
}

// ActorIn is Actor in the given language (the public form and the system have
// generic names; agents and people keep theirs).
func (e Event) ActorIn(lang string) string {
	if e.Actor == actorForm || e.Actor == actorSystem {
		return i18n.T(lang, e.Actor)
	}
	return strings.TrimPrefix(e.Actor, "PAT: ") // the agent's name, as the dashboard shows it
}

const (
	actorForm   = "Formulario público" // i18n.M("Formulario público")
	actorSystem = "Sistema"            // i18n.M("Sistema")
)

// Who names the actor and, for an agent, the person it acts for: "Cursor (ana@acme.com)".
func (e Event) Who() string {
	actor := strings.TrimPrefix(e.Actor, "PAT: ")
	if e.User != "" && e.User != e.Actor {
		return actor + " (" + e.User + ")"
	}
	return actor
}

// By reports whether the person signed in as user made this change in the web.
func (e Event) By(user uuid.UUID) bool {
	return e.Via == "session" && e.actorUser != nil && *e.actorUser == user
}

var verbs = map[string]string{
	"company.create": i18n.M("creó la empresa"), "company.update": i18n.M("actualizó la empresa"), "company.archive": i18n.M("archivó la empresa"),
	"contact.create": i18n.M("creó el contacto"), "contact.update": i18n.M("actualizó el contacto"), "contact.archive": i18n.M("archivó el contacto"),
	"opportunity.create": i18n.M("creó la oportunidad"), "opportunity.update": i18n.M("actualizó la oportunidad"),
	"opportunity.move": i18n.M("movió"), "opportunity.archive": i18n.M("archivó la oportunidad"),
	"interaction.create": i18n.M("registró una interacción"), "lead.capture": i18n.M("recibió un lead"),
	"pipeline.configure": i18n.M("configuró el pipeline"), "pipeline.archive": i18n.M("archivó el pipeline"),
	"channel.create": i18n.M("creó el canal"), "channel.update": i18n.M("actualizó el canal"), "channel.archive": i18n.M("archivó el canal"),
	"form.create": i18n.M("creó el formulario"), "form.update": i18n.M("actualizó el formulario"),
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
		e.Actor = actorForm
	case r.ActorClient != "" && r.ActorKind != "session":
		e.Actor, e.User = r.ActorClient, r.ActorEmail
	case r.ActorEmail != "":
		e.Actor = r.ActorEmail
	default:
		e.Actor = actorSystem
	}
	var d map[string]any
	_ = json.Unmarshal(r.Details, &d)
	str := func(k string) string { s, _ := d[k].(string); return s }
	e.name = firstNonEmpty(str("title"), str("name"), str("form"))
	switch r.Action {
	case "opportunity.move":
		e.stage = str("stage")
	case "interaction.create":
		e.itype = str("type")
	}
	if _, ok := verbs[r.Action]; ok {
		e.Summary = e.SummaryIn("es")
	} else {
		e.Summary = r.Action
	}
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
