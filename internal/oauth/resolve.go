package oauth

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jrcoronel-moon/hilo-crm/internal/crm"
	"github.com/jrcoronel-moon/hilo-crm/internal/identity"
	"github.com/jrcoronel-moon/hilo-crm/internal/secure"
	"github.com/jrcoronel-moon/hilo-crm/internal/store"
)

// Principal is who a bearer token acts for.
type Principal struct {
	UserID      uuid.UUID
	Email       string
	WorkspaceID uuid.UUID
	Role        string
	Scope       string
	Kind        string // oauth | pat
	Client      string // OAuth client name or PAT name
	TokenID     uuid.UUID
}

// Actor converts the principal into the CRM actor (role ∩ scope).
func (p Principal) Actor() crm.Actor {
	return crm.NewActor(p.UserID, p.WorkspaceID, p.Role, p.Kind, p.Client, p.Scope)
}

// ErrInvalidToken means the bearer token is unknown, expired, revoked, meant
// for another resource, or its user lost access to the workspace.
var ErrInvalidToken = errors.New("invalid token")

// ResolveBearer validates an OAuth access token or a personal access token.
// Membership is re-checked on every call (it is part of the lookup join), so
// removing someone from a workspace cuts their agents off immediately.
func ResolveBearer(ctx context.Context, q *store.Queries, token, wantResource string) (Principal, error) {
	if token == "" || len(token) > 200 {
		return Principal{}, ErrInvalidToken
	}
	h := secure.Hash(token)
	switch {
	case strings.HasPrefix(token, accessPrefix):
		row, err := q.ResolveAccessToken(ctx, h)
		if err != nil {
			return Principal{}, notFoundAsInvalid(err)
		}
		if row.Resource != wantResource {
			return Principal{}, ErrInvalidToken
		}
		return Principal{UserID: row.UserID, Email: row.Email, WorkspaceID: row.WorkspaceID, Role: row.Role,
			Scope: row.Scope, Kind: "oauth", Client: row.ClientName, TokenID: row.ID}, nil
	case strings.HasPrefix(token, identity.PATPrefix):
		row, err := q.ResolvePAT(ctx, h)
		if err != nil {
			return Principal{}, notFoundAsInvalid(err)
		}
		_ = q.TouchPAT(ctx, row.ID)
		return Principal{UserID: row.UserID, Email: row.Email, WorkspaceID: row.WorkspaceID, Role: row.Role,
			Scope: row.Scope, Kind: "pat", Client: "PAT: " + row.Name, TokenID: row.ID}, nil
	default:
		return Principal{}, ErrInvalidToken
	}
}

func notFoundAsInvalid(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrInvalidToken
	}
	return err
}
