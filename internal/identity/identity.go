// Package identity owns who may sign in and what they belong to: users and
// their external identities, the login policy (verified email + allowed
// domains / invitations), web sessions, workspaces, members, invitations,
// domains, superadmins and personal access tokens.
package identity

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jrcoronel-moon/hilo-crm/internal/crm"
	"github.com/jrcoronel-moon/hilo-crm/internal/db"
	"github.com/jrcoronel-moon/hilo-crm/internal/secure"
	"github.com/jrcoronel-moon/hilo-crm/internal/store"
)

// Errors safe to show to the user.
var (
	ErrEmailUnverified = errors.New("the identity provider did not verify this email address")
	ErrNotAllowed      = errors.New("this email is not allowed to sign in; ask an administrator to invite you or to allow your domain")
	ErrDisabled        = errors.New("this account is disabled")
	ErrForbidden       = errors.New("forbidden")
	ErrNotFound        = errors.New("not found")
	ErrLastOwner       = errors.New("a workspace must keep at least one owner")
)

// SessionTTL is how long a web session lasts.
const SessionTTL = 7 * 24 * time.Hour

// Service implements the identity operations.
type Service struct {
	db          *db.DB
	log         *slog.Logger
	superadmins map[string]bool
	now         func() time.Time
}

// New builds the service. superadmins are emails promoted on login.
func New(d *db.DB, log *slog.Logger, superadmins map[string]bool) *Service {
	return &Service{db: d, log: log, superadmins: superadmins, now: time.Now}
}

// ExternalIdentity is what a login provider vouched for.
type ExternalIdentity struct {
	Provider      string // google | microsoft | dev
	Subject       string // stable id at the provider (never the email)
	Email         string
	EmailVerified bool
	Name          string
	AvatarURL     string
}

// Login applies the sign-in policy and returns the user.
func (s *Service) Login(ctx context.Context, ext ExternalIdentity) (store.User, error) {
	email, err := crm.NormalizeEmail("email", ext.Email)
	if err != nil || email == "" {
		return store.User{}, ErrEmailUnverified
	}
	if !ext.EmailVerified {
		return store.User{}, ErrEmailUnverified
	}
	name := truncate(strings.TrimSpace(ext.Name), 200)
	avatar := ""
	if strings.HasPrefix(ext.AvatarURL, "https://") && len(ext.AvatarURL) <= 2000 {
		avatar = ext.AvatarURL
	}
	var user store.User
	err = s.db.InTx(ctx, func(q *store.Queries) error {
		u, err := q.GetUserByIdentity(ctx, store.GetUserByIdentityParams{Provider: ext.Provider, Subject: ext.Subject})
		if errors.Is(err, pgx.ErrNoRows) {
			u, err = q.GetUserByEmail(ctx, email)
		}
		isNew := errors.Is(err, pgx.ErrNoRows)
		if err != nil && !isNew {
			return err
		}
		if !isNew && u.DisabledAt != nil {
			return ErrDisabled
		}
		allowed, err := s.mayLogin(ctx, q, email, u, isNew)
		if err != nil {
			return err
		}
		if !allowed {
			return ErrNotAllowed
		}
		if isNew {
			if u, err = q.CreateUser(ctx, store.CreateUserParams{Email: email, Name: name, AvatarUrl: avatar, IsSuperadmin: s.superadmins[email]}); err != nil {
				return err
			}
		} else {
			if err := q.TouchUserLogin(ctx, store.TouchUserLoginParams{ID: u.ID, Name: firstNonEmpty(name, u.Name), AvatarUrl: firstNonEmpty(avatar, u.AvatarUrl)}); err != nil {
				return err
			}
			if s.superadmins[email] && !u.IsSuperadmin {
				if _, err := q.SetSuperadmin(ctx, store.SetSuperadminParams{Email: email, IsSuperadmin: true}); err != nil {
					return err
				}
				u.IsSuperadmin = true
			}
		}
		if err := q.CreateIdentity(ctx, store.CreateIdentityParams{UserID: u.ID, Provider: ext.Provider, Subject: ext.Subject, Email: email}); err != nil {
			return err
		}
		if err := s.joinWorkspaces(ctx, q, u.ID, email); err != nil {
			return err
		}
		user = u
		return nil
	})
	return user, err
}

// mayLogin: superadmins, invited emails, allowed platform domains, and
// existing users who still belong to a workspace.
func (s *Service) mayLogin(ctx context.Context, q *store.Queries, email string, u store.User, isNew bool) (bool, error) {
	if s.superadmins[email] || (!isNew && u.IsSuperadmin) {
		return true, nil
	}
	if ok, err := q.IsPlatformDomainAllowed(ctx, crm.EmailDomain(email)); err != nil || ok {
		return ok, err
	}
	if ok, err := q.HasPendingInvitation(ctx, email); err != nil || ok {
		return ok, err
	}
	if isNew {
		return false, nil
	}
	ws, err := q.ListUserWorkspaces(ctx, u.ID)
	return len(ws) > 0, err
}

// joinWorkspaces runs on sign-in: it accepts the invitations the operator
// sent, and invites the user to the workspaces that list their email's
// domain. Nobody else joins a workspace without accepting (see
// AcceptInvitation): a workspace admin cannot pull people in.
func (s *Service) joinWorkspaces(ctx context.Context, q *store.Queries, userID uuid.UUID, email string) error {
	invs, err := q.AcceptPreacceptedInvitations(ctx, email)
	if err != nil {
		return err
	}
	for _, inv := range invs {
		if err := q.AddMember(ctx, store.AddMemberParams{WorkspaceID: inv.WorkspaceID, UserID: userID, Role: inv.Role}); err != nil {
			return err
		}
	}
	return q.OfferDomainInvitations(ctx, store.OfferDomainInvitationsParams{
		Email: email, ExpiresAt: s.now().Add(invitationTTL), Domain: crm.EmailDomain(email), UserID: userID,
	})
}

// Membership returns the user's role in a workspace, or ErrForbidden.
func (s *Service) Membership(ctx context.Context, userID, workspaceID uuid.UUID) (string, error) {
	role, err := s.db.Platform().GetMembership(ctx, store.GetMembershipParams{WorkspaceID: workspaceID, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrForbidden
	}
	return role, err
}

// Workspaces lists the user's workspaces.
func (s *Service) Workspaces(ctx context.Context, userID uuid.UUID) ([]store.ListUserWorkspacesRow, error) {
	return s.db.Platform().ListUserWorkspaces(ctx, userID)
}

var slugUnsafe = regexp.MustCompile(`[^a-z0-9]+`)

// CreateWorkspace creates a workspace owned by userID and seeds its default
// pipeline and channels, all in one transaction.
func (s *Service) CreateWorkspace(ctx context.Context, userID uuid.UUID, name string) (store.Workspace, error) {
	return s.CreateWorkspaceIn(ctx, userID, name, "es")
}

// CreateWorkspaceIn creates a workspace whose default pipeline and channels
// are named in lang (the creator's interface language).
func (s *Service) CreateWorkspaceIn(ctx context.Context, userID uuid.UUID, name, lang string) (store.Workspace, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 120 {
		return store.Workspace{}, &crm.ValidationError{Field: "name", Msg: "must be 1-120 characters"}
	}
	base := strings.Trim(slugUnsafe.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if len(base) < 2 {
		base = "ws"
	}
	if len(base) > 40 {
		base = strings.Trim(base[:40], "-")
	}
	var ws store.Workspace
	err := s.db.InTx(ctx, func(q *store.Queries) error {
		slug := base
		for i := 0; ; i++ {
			taken, err := q.SlugExists(ctx, slug)
			if err != nil {
				return err
			}
			if !taken {
				break
			}
			if i > 5 {
				return errors.New("could not allocate a workspace slug")
			}
			slug = base + "-" + strings.ToLower(secure.Token("", 3))
			slug = slugUnsafe.ReplaceAllString(slug, "")
		}
		var err error
		if ws, err = q.CreateWorkspace(ctx, store.CreateWorkspaceParams{Name: name, Slug: slug, CreatedBy: &userID}); err != nil {
			return err
		}
		if err := q.AddMember(ctx, store.AddMemberParams{WorkspaceID: ws.ID, UserID: userID, Role: "owner"}); err != nil {
			return err
		}
		if err := q.BindWorkspace(ctx, ws.ID.String()); err != nil {
			return err
		}
		return crm.SeedWorkspace(ctx, q, ws.ID, lang)
	})
	return ws, err
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// SetSuperadmin grants or revokes superadmin by email (CLI).
func (s *Service) SetSuperadmin(ctx context.Context, email string, on bool) error {
	email = strings.ToLower(strings.TrimSpace(email))
	n, err := s.db.Platform().SetSuperadmin(ctx, store.SetSuperadminParams{Email: email, IsSuperadmin: on})
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%w: no user with email %s has signed in yet (add it to SUPERADMIN_EMAILS to promote on first login)", ErrNotFound, email)
	}
	return nil
}
