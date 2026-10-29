package identity

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jrcoronel-moon/hilo-crm/internal/crm"
	"github.com/jrcoronel-moon/hilo-crm/internal/secure"
	"github.com/jrcoronel-moon/hilo-crm/internal/store"
)

// Caller is the user performing an administrative operation in a workspace.
type Caller struct {
	UserID       uuid.UUID
	WorkspaceID  uuid.UUID
	Role         string
	IsSuperadmin bool
}

func (c Caller) isAdmin() bool { return c.Role == "owner" || c.Role == "admin" }

func (c Caller) requireAdmin() error {
	if !c.isAdmin() {
		return ErrForbidden
	}
	return nil
}

const invitationTTL = 14 * 24 * time.Hour

// Members lists the workspace members (any member may see them).
func (s *Service) Members(ctx context.Context, c Caller) ([]store.ListMembersRow, error) {
	return s.db.Platform().ListMembers(ctx, c.WorkspaceID)
}

// Invitations lists pending invitations (admin).
func (s *Service) Invitations(ctx context.Context, c Caller) ([]store.Invitation, error) {
	if err := c.requireAdmin(); err != nil {
		return nil, err
	}
	return s.db.Platform().ListInvitations(ctx, c.WorkspaceID)
}

// Invite invites an email with a role. If the user already exists they are
// added immediately; otherwise they join on first login.
func (s *Service) Invite(ctx context.Context, c Caller, email, role string) error {
	if err := c.requireAdmin(); err != nil {
		return err
	}
	if role != "admin" && role != "member" && role != "viewer" {
		return &crm.ValidationError{Field: "role", Msg: "must be admin, member or viewer"}
	}
	email, err := crm.NormalizeEmail("email", email)
	if err != nil || email == "" {
		return &crm.ValidationError{Field: "email", Msg: "not a valid email address"}
	}
	return s.db.InTx(ctx, func(q *store.Queries) error {
		if u, err := q.GetUserByEmail(ctx, email); err == nil {
			return q.AddMember(ctx, store.AddMemberParams{WorkspaceID: c.WorkspaceID, UserID: u.ID, Role: role})
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		_, err := q.CreateInvitation(ctx, store.CreateInvitationParams{
			WorkspaceID: c.WorkspaceID, Email: email, Role: role, InvitedBy: &c.UserID, ExpiresAt: s.now().Add(invitationTTL),
		})
		return err
	})
}

// RevokeInvitation cancels a pending invitation (admin).
func (s *Service) RevokeInvitation(ctx context.Context, c Caller, id uuid.UUID) error {
	if err := c.requireAdmin(); err != nil {
		return err
	}
	n, err := s.db.Platform().RevokeInvitation(ctx, store.RevokeInvitationParams{ID: id, WorkspaceID: c.WorkspaceID})
	if err == nil && n == 0 {
		err = ErrNotFound
	}
	return err
}

// SetRole changes a member's role. Only owners can grant or take away the
// owner role, and the last owner cannot be demoted.
func (s *Service) SetRole(ctx context.Context, c Caller, userID uuid.UUID, role string) error {
	if err := c.requireAdmin(); err != nil {
		return err
	}
	if !crm.ValidRole(role) {
		return &crm.ValidationError{Field: "role", Msg: "must be owner, admin, member or viewer"}
	}
	return s.db.InTx(ctx, func(q *store.Queries) error {
		cur, err := q.GetMembership(ctx, store.GetMembershipParams{WorkspaceID: c.WorkspaceID, UserID: userID})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if (cur == "owner" || role == "owner") && c.Role != "owner" {
			return ErrForbidden
		}
		if cur == "owner" && role != "owner" {
			if err := keepAnOwner(ctx, q, c.WorkspaceID); err != nil {
				return err
			}
		}
		_, err = q.UpdateMemberRole(ctx, store.UpdateMemberRoleParams{WorkspaceID: c.WorkspaceID, UserID: userID, Role: role})
		return err
	})
}

// RemoveMember removes a member (admin; owners only by owners; never the
// last owner). Members may always remove themselves.
func (s *Service) RemoveMember(ctx context.Context, c Caller, userID uuid.UUID) error {
	if userID != c.UserID {
		if err := c.requireAdmin(); err != nil {
			return err
		}
	}
	return s.db.InTx(ctx, func(q *store.Queries) error {
		cur, err := q.GetMembership(ctx, store.GetMembershipParams{WorkspaceID: c.WorkspaceID, UserID: userID})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if cur == "owner" {
			if c.Role != "owner" {
				return ErrForbidden
			}
			if err := keepAnOwner(ctx, q, c.WorkspaceID); err != nil {
				return err
			}
		}
		if _, err := q.RemoveMember(ctx, store.RemoveMemberParams{WorkspaceID: c.WorkspaceID, UserID: userID}); err != nil {
			return err
		}
		// Their tokens for this workspace stop resolving (membership join), but
		// revoke explicitly so nothing lingers.
		return revokeWorkspaceAccess(ctx, q, userID, c.WorkspaceID)
	})
}

func keepAnOwner(ctx context.Context, q *store.Queries, ws uuid.UUID) error {
	n, err := q.CountOwners(ctx, ws)
	if err != nil {
		return err
	}
	if n <= 1 {
		return ErrLastOwner
	}
	return nil
}

func revokeWorkspaceAccess(ctx context.Context, q *store.Queries, userID, ws uuid.UUID) error {
	conns, err := q.ListConnections(ctx, store.ListConnectionsParams{UserID: userID, WorkspaceID: ws})
	if err != nil {
		return err
	}
	for _, c := range conns {
		if _, err := q.RevokeConnection(ctx, store.RevokeConnectionParams{UserID: userID, WorkspaceID: ws, ClientID: c.ClientID}); err != nil {
			return err
		}
	}
	pats, err := q.ListPATs(ctx, store.ListPATsParams{UserID: userID, WorkspaceID: ws})
	if err != nil {
		return err
	}
	for _, p := range pats {
		if _, err := q.RevokePAT(ctx, store.RevokePATParams{ID: p.ID, UserID: userID, WorkspaceID: ws}); err != nil {
			return err
		}
	}
	return nil
}

// WorkspaceDomains lists auto-join domains.
func (s *Service) WorkspaceDomains(ctx context.Context, c Caller) ([]store.WorkspaceDomain, error) {
	return s.db.Platform().ListWorkspaceDomains(ctx, c.WorkspaceID)
}

// AddWorkspaceDomain lets verified emails of a domain join with defaultRole
// (admin). It does not let anyone sign in who could not already (platform
// allowlist / invitations still apply).
func (s *Service) AddWorkspaceDomain(ctx context.Context, c Caller, domain, defaultRole string) error {
	if err := c.requireAdmin(); err != nil {
		return err
	}
	d, err := crm.NormalizeDomain("domain", domain)
	if err != nil || d == "" {
		return &crm.ValidationError{Field: "domain", Msg: "not a valid domain"}
	}
	if defaultRole != "admin" && defaultRole != "member" && defaultRole != "viewer" {
		return &crm.ValidationError{Field: "role", Msg: "must be admin, member or viewer"}
	}
	if defaultRole == "admin" && c.Role != "owner" {
		return ErrForbidden
	}
	return s.db.Platform().AddWorkspaceDomain(ctx, store.AddWorkspaceDomainParams{WorkspaceID: c.WorkspaceID, Domain: d, DefaultRole: defaultRole})
}

// RemoveWorkspaceDomain stops auto-join for a domain (admin).
func (s *Service) RemoveWorkspaceDomain(ctx context.Context, c Caller, domain string) error {
	if err := c.requireAdmin(); err != nil {
		return err
	}
	_, err := s.db.Platform().RemoveWorkspaceDomain(ctx, store.RemoveWorkspaceDomainParams{WorkspaceID: c.WorkspaceID, Domain: strings.ToLower(strings.TrimSpace(domain))})
	return err
}

// ───────────── superadmin ─────────────

// PlatformDomains lists the domains allowed to sign in (superadmin).
func (s *Service) PlatformDomains(ctx context.Context, superadmin bool) ([]store.PlatformAllowedDomain, error) {
	if !superadmin {
		return nil, ErrForbidden
	}
	return s.db.Platform().ListPlatformDomains(ctx)
}

// AddPlatformDomain allows verified emails of a domain to sign in (superadmin).
func (s *Service) AddPlatformDomain(ctx context.Context, superadmin bool, by uuid.UUID, domain string) error {
	if !superadmin {
		return ErrForbidden
	}
	d, err := crm.NormalizeDomain("domain", domain)
	if err != nil || d == "" {
		return &crm.ValidationError{Field: "domain", Msg: "not a valid domain"}
	}
	var byPtr *uuid.UUID
	if by != uuid.Nil {
		byPtr = &by
	}
	return s.db.Platform().AddPlatformDomain(ctx, store.AddPlatformDomainParams{Domain: d, CreatedBy: byPtr})
}

// RemovePlatformDomain stops new sign-ins from a domain (superadmin).
func (s *Service) RemovePlatformDomain(ctx context.Context, superadmin bool, domain string) error {
	if !superadmin {
		return ErrForbidden
	}
	_, err := s.db.Platform().RemovePlatformDomain(ctx, strings.ToLower(strings.TrimSpace(domain)))
	return err
}

// AllWorkspaces lists every workspace (superadmin).
func (s *Service) AllWorkspaces(ctx context.Context, superadmin bool) ([]store.ListAllWorkspacesRow, error) {
	if !superadmin {
		return nil, ErrForbidden
	}
	return s.db.Platform().ListAllWorkspaces(ctx)
}

// ───────────── personal access tokens ─────────────

// PATPrefix marks Hilo personal access tokens (recognisable by secret scanners).
const PATPrefix = "hilo_pat_"

// CreatePAT issues a token for the caller in their current workspace. The
// plaintext is returned once and never stored.
func (s *Service) CreatePAT(ctx context.Context, c Caller, name string, readOnly bool, ttl time.Duration) (string, store.CreatePATRow, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 100 {
		return "", store.CreatePATRow{}, &crm.ValidationError{Field: "name", Msg: "must be 1-100 characters"}
	}
	scope := crm.AllScopes
	if readOnly {
		scope = string(crm.PermRead)
	}
	var exp *time.Time
	if ttl > 0 {
		t := s.now().Add(ttl)
		exp = &t
	}
	token := secure.Token(PATPrefix, 32)
	row, err := s.db.Platform().CreatePAT(ctx, store.CreatePATParams{
		UserID: c.UserID, WorkspaceID: c.WorkspaceID, Name: name, TokenHash: secure.Hash(token),
		Prefix: token[:len(PATPrefix)+6], Scope: scope, ExpiresAt: exp,
	})
	return token, row, err
}

// PATs lists the caller's tokens in the workspace.
func (s *Service) PATs(ctx context.Context, c Caller) ([]store.ListPATsRow, error) {
	return s.db.Platform().ListPATs(ctx, store.ListPATsParams{UserID: c.UserID, WorkspaceID: c.WorkspaceID})
}

// RevokePAT revokes one of the caller's tokens.
func (s *Service) RevokePAT(ctx context.Context, c Caller, id uuid.UUID) error {
	n, err := s.db.Platform().RevokePAT(ctx, store.RevokePATParams{ID: id, UserID: c.UserID, WorkspaceID: c.WorkspaceID})
	if err == nil && n == 0 {
		err = ErrNotFound
	}
	return err
}

// Connections lists the OAuth apps (agents) the caller connected.
func (s *Service) Connections(ctx context.Context, c Caller) ([]store.ListConnectionsRow, error) {
	return s.db.Platform().ListConnections(ctx, store.ListConnectionsParams{UserID: c.UserID, WorkspaceID: c.WorkspaceID})
}

// RevokeConnection disconnects an OAuth app.
func (s *Service) RevokeConnection(ctx context.Context, c Caller, clientID string) error {
	n, err := s.db.Platform().RevokeConnection(ctx, store.RevokeConnectionParams{UserID: c.UserID, WorkspaceID: c.WorkspaceID, ClientID: clientID})
	if err == nil && n == 0 {
		err = ErrNotFound
	}
	return err
}
