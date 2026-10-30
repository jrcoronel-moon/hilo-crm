package identity

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jrcoronel-moon/hilo-crm/internal/crm"
	"github.com/jrcoronel-moon/hilo-crm/internal/store"
)

// Operator is the CLI acting on a workspace with owner rights. Whoever can
// run hiloctl already holds the database credentials, so it is not a user:
// it has no user id, and the last-owner rules still apply.
func Operator(workspaceID uuid.UUID) Caller {
	return Caller{WorkspaceID: workspaceID, Role: "owner"}
}

// by is the user recorded as the author of an action, nil for the operator.
func (c Caller) by() *uuid.UUID {
	if c.UserID == uuid.Nil {
		return nil
	}
	return &c.UserID
}

// FindWorkspace resolves a workspace by id or slug.
func (s *Service) FindWorkspace(ctx context.Context, ref string) (store.Workspace, error) {
	q := s.db.Platform()
	var ws store.Workspace
	var err error
	if id, perr := uuid.Parse(ref); perr == nil {
		ws, err = q.GetWorkspace(ctx, id)
	} else {
		ws, err = q.GetWorkspaceBySlug(ctx, ref)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ws, ErrNotFound
	}
	return ws, err
}

// EnsureUser returns the user with this email, creating it if nobody has
// signed in with it yet. On their first Google/Microsoft login the identity
// is linked by verified email, exactly as for an accepted invitation.
func (s *Service) EnsureUser(ctx context.Context, email string) (store.User, error) {
	email, err := crm.NormalizeEmail("email", email)
	if err != nil || email == "" {
		return store.User{}, &crm.ValidationError{Field: "email", Msg: "not a valid email address"}
	}
	q := s.db.Platform()
	u, err := q.GetUserByEmail(ctx, email)
	if errors.Is(err, pgx.ErrNoRows) {
		return q.CreateUser(ctx, store.CreateUserParams{Email: email, IsSuperadmin: s.superadmins[email]})
	}
	return u, err
}

// MemberCaller is the member with this email in a workspace, as a Caller
// (for issuing tokens on their behalf, or targeting them by email).
func (s *Service) MemberCaller(ctx context.Context, workspaceID uuid.UUID, email string) (Caller, error) {
	email, err := crm.NormalizeEmail("email", email)
	if err != nil || email == "" {
		return Caller{}, &crm.ValidationError{Field: "email", Msg: "not a valid email address"}
	}
	m, err := s.db.Platform().GetMemberByEmail(ctx, store.GetMemberByEmailParams{WorkspaceID: workspaceID, Email: email})
	if errors.Is(err, pgx.ErrNoRows) {
		return Caller{}, ErrNotFound
	}
	return Caller{UserID: m.ID, WorkspaceID: workspaceID, Role: m.Role}, err
}
