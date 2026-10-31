package e2e_test

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jrcoronel-moon/hilo-crm/internal/crm"
	"github.com/jrcoronel-moon/hilo-crm/internal/events"
	"github.com/jrcoronel-moon/hilo-crm/internal/identity"
)

// TestLiveUpdates: a browser watching /events sees an agent's change as soon
// as it commits, and nothing from other workspaces.
func TestLiveUpdates(t *testing.T) {
	s := start(t)
	s.signIn()
	ctx := context.Background()
	id := identity.New(s.db, slog.New(slog.DiscardHandler), nil)
	ws, _ := id.FindWorkspace(ctx, "acme")
	root, err := id.MemberCaller(ctx, ws.ID, "root@acme.test")
	if err != nil {
		t.Fatal(err)
	}

	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, _ := http.NewRequestWithContext(sctx, http.MethodGet, s.base+"/events", nil)
	req.Header.Set("Accept-Language", "es") // summaries follow the viewer's language
	res, err := s.web.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("/events: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	got := make(chan events.Event, 8)
	go func() {
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				var e events.Event
				if json.Unmarshal([]byte(data), &e) == nil {
					got <- e
				}
			}
		}
	}()

	// Another workspace's change must not arrive; ours must, attributed to the agent.
	svc := crm.New(s.db, slog.New(slog.DiscardHandler))
	other, err := id.CreateWorkspace(ctx, root.UserID, "Other")
	if err != nil {
		t.Fatal(err)
	}
	name, domain := "Secreta", "globex.com"
	if _, _, err := svc.UpsertCompany(ctx, crm.NewActor(root.UserID, other.ID, "owner", "oauth", "Claude", crm.AllScopes), crm.CompanyInput{Name: &name}); err != nil {
		t.Fatal(err)
	}
	// Repeat the (idempotent) change until the listener has subscribed.
	name = "Globex"
	agent := crm.NewActor(root.UserID, ws.ID, "owner", "oauth", "Claude", crm.AllScopes)
	deadline := time.After(5 * time.Second)
	for e := (events.Event{}); e.ID == 0; {
		if _, _, err := svc.UpsertCompany(ctx, agent, crm.CompanyInput{Name: &name, Domain: &domain}); err != nil {
			t.Fatal(err)
		}
		select {
		case e = <-got:
			if e.Actor != "Claude" || !strings.HasSuffix(e.Summary, "la empresa «Globex»") || e.Self || e.Workspace != ws.ID {
				t.Fatalf("event: %+v", e)
			}
		case <-time.After(300 * time.Millisecond):
		case <-deadline:
			t.Fatal("no live event within 5s")
		}
	}

	// Signed out: no stream.
	anon, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.base+"/events", nil)
	r2, err := http.DefaultClient.Do(anon)
	if err != nil {
		t.Fatal(err)
	}
	_ = r2.Body.Close()
	if r2.StatusCode == http.StatusOK && r2.Header.Get("Content-Type") == "text/event-stream" {
		t.Fatal("anonymous request got the event stream")
	}
}
