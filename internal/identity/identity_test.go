package identity_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/jrcoronel-moon/hilo-crm/internal/identity"
	"github.com/jrcoronel-moon/hilo-crm/internal/store"
	"github.com/jrcoronel-moon/hilo-crm/internal/testdb"
)

var ctx = context.Background()

func setup(t *testing.T) *identity.Service {
	t.Helper()
	return identity.New(testdb.New(t), slog.New(slog.DiscardHandler), map[string]bool{"root@acme.test": true})
}

func login(t *testing.T, id *identity.Service, email string, verified bool) (store.User, error) {
	t.Helper()
	return id.Login(ctx, identity.ExternalIdentity{Provider: "google", Subject: "sub-" + email, Email: email, EmailVerified: verified, Name: email})
}

func owner(t *testing.T, id *identity.Service) identity.Caller {
	t.Helper()
	u, err := login(t, id, "root@acme.test", true)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := id.CreateWorkspace(ctx, u.ID, "Acme")
	if err != nil {
		t.Fatal(err)
	}
	return identity.Caller{UserID: u.ID, WorkspaceID: ws.ID, Role: "owner", IsSuperadmin: u.IsSuperadmin}
}

func TestLoginPolicy(t *testing.T) {
	id := setup(t)
	if _, err := login(t, id, "root@acme.test", false); !errors.Is(err, identity.ErrEmailUnverified) {
		t.Fatalf("unverified email: %v", err)
	}
	if _, err := login(t, id, "stranger@other.test", true); !errors.Is(err, identity.ErrNotAllowed) {
		t.Fatalf("closed by default: %v", err)
	}
	root, err := login(t, id, "root@acme.test", true)
	if err != nil || !root.IsSuperadmin {
		t.Fatalf("SUPERADMIN_EMAILS must promote on first login: %v %+v", err, root)
	}
	if err := id.AddPlatformDomain(ctx, false, root.ID, "acme.test"); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("non-superadmin added a platform domain: %v", err)
	}
	if err := id.AddPlatformDomain(ctx, true, root.ID, "https://www.Acme.test/"); err != nil {
		t.Fatal(err)
	}
	if _, err := login(t, id, "ana@acme.test", true); err != nil {
		t.Fatalf("allowed domain: %v", err)
	}
	// Subdomains and look-alikes are not the allowed domain.
	for _, e := range []string{"eve@evil-acme.test", "eve@acme.test.evil.com", "eve@sub.acme.test"} {
		if _, err := login(t, id, e, true); !errors.Is(err, identity.ErrNotAllowed) {
			t.Fatalf("%s: %v", e, err)
		}
	}
	// Same email through another provider links to the same user.
	u2, err := id.Login(ctx, identity.ExternalIdentity{Provider: "microsoft", Subject: "tid:oid", Email: "ANA@acme.test", EmailVerified: true})
	u1, _ := login(t, id, "ana@acme.test", true)
	if err != nil || u1.ID != u2.ID {
		t.Fatalf("identity linking: %v", err)
	}
}

func TestInvitationsAndDomainAutoJoin(t *testing.T) {
	id := setup(t)
	o := owner(t, id)
	if err := id.Invite(ctx, o, "guest@outside.test", "viewer"); err != nil {
		t.Fatal(err)
	}
	if err := id.Invite(ctx, o, "x@outside.test", "owner"); err == nil {
		t.Fatal("owner role cannot be invited")
	}
	g, err := login(t, id, "guest@outside.test", true)
	if err != nil {
		t.Fatalf("invited email must be able to sign in: %v", err)
	}
	// Signing in does not join: the person accepts the invitation.
	if _, err := id.Membership(ctx, g.ID, o.WorkspaceID); err == nil {
		t.Fatal("joined without accepting the invitation")
	}
	invs, err := id.MyInvitations(ctx, g.Email)
	if err != nil || len(invs) != 1 || invs[0].WorkspaceName != "Acme" || invs[0].InvitedByEmail != "root@acme.test" {
		t.Fatalf("pending invitations: %v %+v", err, invs)
	}
	if _, err := id.AcceptInvitation(ctx, g.ID, "someone@else.test", invs[0].ID); !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("accepted an invitation sent to another email: %v", err)
	}
	if ws, err := id.AcceptInvitation(ctx, g.ID, g.Email, invs[0].ID); err != nil || ws != o.WorkspaceID {
		t.Fatalf("accept: %v", err)
	}
	if role, err := id.Membership(ctx, g.ID, o.WorkspaceID); err != nil || role != "viewer" {
		t.Fatalf("invitation not accepted: %v %s", err, role)
	}
	if _, err := id.AcceptInvitation(ctx, g.ID, g.Email, invs[0].ID); !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("invitation accepted twice: %v", err)
	}

	// An existing user is invited too, never added directly by an admin...
	_ = id.AddPlatformDomain(ctx, true, uuid.Nil, "acme.test")
	b, _ := login(t, id, "bob@acme.test", true)
	if err := id.Invite(ctx, o, b.Email, "member"); err != nil {
		t.Fatal(err)
	}
	if _, err := id.Membership(ctx, b.ID, o.WorkspaceID); err == nil {
		t.Fatal("existing user added without accepting")
	}
	invs, _ = id.MyInvitations(ctx, b.Email)
	if len(invs) != 1 {
		t.Fatalf("bob's invitations: %+v", invs)
	}
	if err := id.DeclineInvitation(ctx, b.Email, invs[0].ID); err != nil {
		t.Fatal(err)
	}
	if invs, _ = id.MyInvitations(ctx, b.Email); len(invs) != 0 {
		t.Fatalf("declined invitation still pending: %+v", invs)
	}
	// ...only the operator (hiloctl) adds people directly.
	if err := id.Invite(ctx, identity.Operator(o.WorkspaceID), b.Email, "member"); err != nil {
		t.Fatal(err)
	}
	if role, _ := id.Membership(ctx, b.ID, o.WorkspaceID); role != "member" {
		t.Fatalf("operator add: %q", role)
	}

	// A workspace may only claim the domain of its admin's own email.
	plain := o
	plain.IsSuperadmin = false
	if err := id.AddWorkspaceDomain(ctx, plain, "partner.test", "member"); err == nil {
		t.Fatal("claimed a domain the admin's email does not prove")
	}
	if err := id.AddWorkspaceDomain(ctx, identity.Operator(o.WorkspaceID), "partner.test", "member"); err != nil {
		t.Fatal(err)
	}
	// Workspace auto-join requires the domain to be allowed on the platform too.
	if _, err := login(t, id, "p@partner.test", true); !errors.Is(err, identity.ErrNotAllowed) {
		t.Fatalf("workspace domain alone must not open sign-in: %v", err)
	}
	_ = id.AddPlatformDomain(ctx, true, uuid.Nil, "partner.test")
	p, err := login(t, id, "p@partner.test", true)
	if err != nil {
		t.Fatal(err)
	}
	// The domain offers an invitation; joining still needs acceptance.
	if _, err := id.Membership(ctx, p.ID, o.WorkspaceID); err == nil {
		t.Fatal("domain auto-joined without acceptance")
	}
	invs, _ = id.MyInvitations(ctx, p.Email)
	if len(invs) != 1 || invs[0].Role != "member" {
		t.Fatalf("domain invitation: %+v", invs)
	}
	if err := id.DeclineInvitation(ctx, p.Email, invs[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := login(t, id, "p@partner.test", true); err != nil {
		t.Fatal(err)
	}
	if invs, _ = id.MyInvitations(ctx, p.Email); len(invs) != 0 {
		t.Fatalf("declined domain invitation offered again: %+v", invs)
	}

	admin := identity.Caller{UserID: p.ID, WorkspaceID: o.WorkspaceID, Role: "admin"}
	if err := id.AddWorkspaceDomain(ctx, admin, "x.test", "admin"); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("only owners may auto-grant admin: %v", err)
	}
	member := identity.Caller{UserID: p.ID, WorkspaceID: o.WorkspaceID, Role: "member"}
	if err := id.Invite(ctx, member, "y@outside.test", "member"); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("member invited: %v", err)
	}
}

func TestRolesAndOwnership(t *testing.T) {
	id := setup(t)
	o := owner(t, id)
	_ = id.AddPlatformDomain(ctx, true, uuid.Nil, "acme.test")
	a, _ := login(t, id, "admin@acme.test", true)
	m, _ := login(t, id, "member@acme.test", true)
	for _, u := range []store.User{a, m} {
		if err := id.Invite(ctx, identity.Operator(o.WorkspaceID), u.Email, "member"); err != nil {
			t.Fatal(err)
		}
	}
	if err := id.SetRole(ctx, o, a.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	admin := identity.Caller{UserID: a.ID, WorkspaceID: o.WorkspaceID, Role: "admin"}
	if err := id.SetRole(ctx, admin, m.ID, "owner"); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("admin granted owner: %v", err)
	}
	if err := id.SetRole(ctx, admin, o.UserID, "viewer"); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("admin demoted the owner: %v", err)
	}
	if err := id.RemoveMember(ctx, admin, o.UserID); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("admin removed the owner: %v", err)
	}
	if err := id.SetRole(ctx, o, o.UserID, "admin"); !errors.Is(err, identity.ErrLastOwner) {
		t.Fatalf("last owner demoted: %v", err)
	}
	if err := id.RemoveMember(ctx, o, o.UserID); !errors.Is(err, identity.ErrLastOwner) {
		t.Fatalf("last owner removed: %v", err)
	}
	// Removing a member revokes their tokens for the workspace.
	mc := identity.Caller{UserID: m.ID, WorkspaceID: o.WorkspaceID, Role: "member"}
	if _, _, err := id.CreatePAT(ctx, mc, "bot", false, 0); err != nil {
		t.Fatal(err)
	}
	if err := id.RemoveMember(ctx, admin, m.ID); err != nil {
		t.Fatal(err)
	}
	if pats, _ := id.PATs(ctx, mc); len(pats) != 0 {
		t.Fatalf("tokens survived removal: %+v", pats)
	}
	// Members can leave on their own.
	self := identity.Caller{UserID: a.ID, WorkspaceID: o.WorkspaceID, Role: "admin"}
	if err := id.RemoveMember(ctx, self, a.ID); err != nil {
		t.Fatalf("leave: %v", err)
	}
}

func TestSessions(t *testing.T) {
	id := setup(t)
	o := owner(t, id)
	tok, err := id.CreateSession(ctx, o.UserID)
	if err != nil {
		t.Fatal(err)
	}
	s, err := id.ResolveSession(ctx, tok)
	if err != nil || s.UserID != o.UserID || s.CSRFToken == "" || s.WorkspaceID == nil || *s.WorkspaceID != o.WorkspaceID {
		t.Fatalf("session: %v %+v", err, s)
	}
	other := uuid.New()
	if err := id.SwitchWorkspace(ctx, s, other); !errors.Is(err, identity.ErrForbidden) {
		t.Fatalf("switched to a foreign workspace: %v", err)
	}
	for _, bad := range []string{"", "nope", tok + "x"} {
		if _, err := id.ResolveSession(ctx, bad); !errors.Is(err, identity.ErrNotFound) {
			t.Fatalf("bad session %q: %v", bad, err)
		}
	}
	if err := id.Logout(ctx, s); err != nil {
		t.Fatal(err)
	}
	if _, err := id.ResolveSession(ctx, tok); !errors.Is(err, identity.ErrNotFound) {
		t.Fatal("session alive after logout")
	}
	if err := id.SetSuperadmin(ctx, "nobody@acme.test", true); !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("superadmin for unknown user: %v", err)
	}
}
