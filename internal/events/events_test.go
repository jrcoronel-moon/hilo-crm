package events

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/jrcoronel-moon/hilo-crm/internal/store"
)

func TestSummaries(t *testing.T) {
	ws, user := uuid.New(), uuid.New()
	row := func(kind, client, email, action, details string) store.GetAuditEntryRow {
		return store.GetAuditEntryRow{WorkspaceID: &ws, ActorUserID: &user, ActorKind: kind, ActorClient: client,
			ActorEmail: email, Action: action, Details: json.RawMessage(details)}
	}
	for _, c := range []struct {
		row            store.GetAuditEntryRow
		actor, summary string
	}{
		{row("oauth", "Claude", "ana@acme.test", "opportunity.move", `{"title":"Planta solar","stage":"Propuesta"}`), "Claude", "movió «Planta solar» a Propuesta"},
		{row("pat", "grok", "ana@acme.test", "interaction.create", `{"type":"call"}`), "grok", "registró una interacción (call)"},
		{row("session", "web", "ana@acme.test", "contact.create", `{"name":"Hank Scorpio"}`), "ana@acme.test", "creó el contacto «Hank Scorpio»"},
		{row("form", "", "", "lead.capture", `{"form":"Web","stage":"Lead"}`), "Formulario público", "recibió un lead «Web»"},
		{row("system", "", "", "something.new", `{}`), "Sistema", "something.new"},
	} {
		e := fromAudit(c.row)
		if e.Actor != c.actor || e.Summary != c.summary || e.Workspace != ws {
			t.Errorf("%s: got %q %q", c.row.Action, e.Actor, e.Summary)
		}
	}
	if e := fromAudit(row("session", "web", "a@b.c", "company.update", `{}`)); !e.By(user) || e.By(uuid.New()) {
		t.Error("By must match only the signed-in author")
	}
	if e := fromAudit(row("oauth", "Claude", "a@b.c", "company.update", `{}`)); e.By(user) {
		t.Error("an agent acting for the user is not the user in the browser")
	}
}

func TestHub(t *testing.T) {
	h := NewHub()
	wsA, wsB, user := uuid.New(), uuid.New(), uuid.New()
	a, err := h.Subscribe(wsA, user)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := h.Subscribe(wsB, uuid.New())
	h.Publish(Event{ID: 1, Workspace: wsA})
	if got := <-a.C; got.ID != 1 {
		t.Fatalf("subscriber of A got %+v", got)
	}
	select {
	case e := <-b.C:
		t.Fatalf("event of workspace A leaked to B: %+v", e)
	default:
	}
	// A slow subscriber drops events instead of blocking the publisher.
	for i := range buffer + 10 {
		h.Publish(Event{ID: int64(i), Workspace: wsA})
	}
	// Per-user limit, released on Close.
	subs := []*Sub{a}
	for range MaxPerUser - 1 {
		s, err := h.Subscribe(wsA, user)
		if err != nil {
			t.Fatal(err)
		}
		subs = append(subs, s)
	}
	if _, err := h.Subscribe(wsA, user); !errors.Is(err, ErrTooMany) {
		t.Fatalf("limit per user: %v", err)
	}
	subs[0].Close()
	subs[0].Close() // idempotent
	if _, err := h.Subscribe(wsA, user); err != nil {
		t.Fatalf("after close: %v", err)
	}
}
