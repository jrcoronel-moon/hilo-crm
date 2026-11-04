package web

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jrcoronel-moon/hilo-crm/internal/config"
	"github.com/jrcoronel-moon/hilo-crm/internal/crm"
	"github.com/jrcoronel-moon/hilo-crm/internal/identity"
	"github.com/jrcoronel-moon/hilo-crm/internal/testdb"
)

// TestLiveStreamEndsWhenAccessEnds: signing out (or losing the workspace)
// closes an open /events stream within one heartbeat.
func TestLiveStreamEndsWhenAccessEnds(t *testing.T) {
	old := heartbeat
	heartbeat = 150 * time.Millisecond
	t.Cleanup(func() { heartbeat = old })

	d := testdb.New(t)
	log := slog.New(slog.DiscardHandler)
	ts := httptest.NewUnstartedServer(nil)
	cfg := &config.Config{BaseURL: "http://" + ts.Listener.Addr().String(), SessionSecret: []byte(strings.Repeat("s", 40))}
	mux, err := Mount(t.Context(), cfg, d, log)
	if err != nil {
		t.Fatal(err)
	}
	ts.Config.Handler = mux
	ts.Start()
	t.Cleanup(ts.Close)

	ctx := context.Background()
	id := identity.New(d, log, map[string]bool{"root@acme.test": true})
	u, err := id.Login(ctx, identity.ExternalIdentity{Provider: "dev", Subject: "r", Email: "root@acme.test", EmailVerified: true})
	if err != nil {
		t.Fatal(err)
	}
	ws, err := id.CreateWorkspace(ctx, u.ID, "Acme")
	if err != nil {
		t.Fatal(err)
	}
	owner := identity.Caller{UserID: u.ID, WorkspaceID: ws.ID, Role: "owner"}
	if err := id.Invite(ctx, identity.Operator(owner.WorkspaceID), "bob@acme.test", "member"); err != nil {
		t.Fatal(err)
	}
	bob, err := id.Login(ctx, identity.ExternalIdentity{Provider: "dev", Subject: "b", Email: "bob@acme.test", EmailVerified: true})
	if err != nil {
		t.Fatal(err)
	}
	// The polling fallback: the newest change id, which moves with each change.
	last := func(token string) int64 {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/events/last", nil)
		req.AddCookie(&http.Cookie{Name: "hilo_session", Value: token})
		res, err := http.DefaultClient.Do(req)
		if err != nil || res.StatusCode != http.StatusOK {
			t.Fatalf("events/last: %v %v", res, err)
		}
		defer func() { _ = res.Body.Close() }()
		var body struct{ ID int64 }
		if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return body.ID
	}
	tok, err := id.CreateSession(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	before := last(tok)
	if _, _, err := crm.New(d, log).UpsertCompany(ctx, crm.NewActor(u.ID, ws.ID, "owner", "session", "", ""), crm.CompanyInput{Name: ptr("Globex")}); err != nil {
		t.Fatal(err)
	}
	if after := last(tok); after <= before {
		t.Fatalf("events/last did not move: %d → %d", before, after)
	}

	for _, c := range []struct {
		name string
		user uuid.UUID
		end  func(token string) error
	}{
		{"sign out", u.ID, func(token string) error {
			sess, err := id.ResolveSession(ctx, token)
			if err != nil {
				return err
			}
			return id.Logout(ctx, sess)
		}},
		{"removed from the workspace", bob.ID, func(string) error { return id.RemoveMember(ctx, owner, bob.ID) }},
	} {
		token, err := id.CreateSession(ctx, c.user)
		if err != nil {
			t.Fatal(err)
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/events", nil)
		req.AddCookie(&http.Cookie{Name: "hilo_session", Value: token})
		res, err := http.DefaultClient.Do(req)
		if err != nil || res.StatusCode != http.StatusOK {
			t.Fatalf("%s: open stream: %v %v", c.name, res, err)
		}
		closed := make(chan struct{})
		go func() {
			sc := bufio.NewScanner(res.Body)
			for sc.Scan() {
			}
			close(closed)
		}()
		time.Sleep(2 * heartbeat) // still open while the session is valid
		select {
		case <-closed:
			t.Fatalf("%s: stream closed while access was valid", c.name)
		default:
		}
		if err := c.end(token); err != nil {
			t.Fatal(err)
		}
		select {
		case <-closed:
		case <-time.After(3 * heartbeat):
			t.Fatalf("%s: stream still open after access ended", c.name)
		}
		_ = res.Body.Close()
	}
}
