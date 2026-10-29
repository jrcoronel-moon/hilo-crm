package identity

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jrcoronel-moon/hilo-crm/internal/secure"
	"github.com/jrcoronel-moon/hilo-crm/internal/store"
)

// Session is an authenticated web session.
type Session struct {
	TokenHash    string
	UserID       uuid.UUID
	Email        string
	Name         string
	IsSuperadmin bool
	CSRFToken    string
	WorkspaceID  *uuid.UUID
}

// CreateSession starts a session and returns the cookie value (only its hash
// is stored).
func (s *Service) CreateSession(ctx context.Context, userID uuid.UUID) (string, error) {
	token := secure.Token("", 32)
	var ws *uuid.UUID
	if list, err := s.Workspaces(ctx, userID); err == nil && len(list) > 0 {
		ws = &list[0].ID
	}
	err := s.db.Platform().CreateSession(ctx, store.CreateSessionParams{
		TokenHash: secure.Hash(token), UserID: userID, CsrfToken: secure.Token("", 32), WorkspaceID: ws,
		ExpiresAt: s.now().Add(SessionTTL),
	})
	return token, err
}

// ResolveSession returns the session for a cookie value, or ErrNotFound.
func (s *Service) ResolveSession(ctx context.Context, token string) (Session, error) {
	if token == "" || len(token) > 100 {
		return Session{}, ErrNotFound
	}
	h := secure.Hash(token)
	row, err := s.db.Platform().GetSession(ctx, h)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, err
	}
	if s.now().Sub(row.LastSeenAt) > 5*time.Minute { // refresh last_seen at most every 5 minutes
		_ = s.db.Platform().TouchSession(ctx, h)
	}
	return Session{TokenHash: h, UserID: row.UserID, Email: row.Email, Name: row.Name, IsSuperadmin: row.IsSuperadmin,
		CSRFToken: row.CsrfToken, WorkspaceID: row.WorkspaceID}, nil
}

// SwitchWorkspace sets the session's current workspace (membership checked).
func (s *Service) SwitchWorkspace(ctx context.Context, sess Session, workspaceID uuid.UUID) error {
	if _, err := s.Membership(ctx, sess.UserID, workspaceID); err != nil {
		return err
	}
	return s.db.Platform().SetSessionWorkspace(ctx, store.SetSessionWorkspaceParams{TokenHash: sess.TokenHash, WorkspaceID: &workspaceID})
}

// Logout ends a session.
func (s *Service) Logout(ctx context.Context, sess Session) error {
	return s.db.Platform().DeleteSession(ctx, sess.TokenHash)
}

// PurgeExpired deletes expired sessions, codes and tokens.
func (s *Service) PurgeExpired(ctx context.Context) error {
	return s.db.Platform().PurgeExpired(ctx)
}
