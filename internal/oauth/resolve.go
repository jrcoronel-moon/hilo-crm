package oauth

import (
	"context"
	"errors"
	"regexp"
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

// InvalidTokenError says why a token was rejected. The reason is for the
// server log only: clients always get the same generic 401, so the response
// is no oracle for attackers, while operators can still tell a mistyped token
// from a revoked one.
type InvalidTokenError struct {
	Reason string // missing | malformed | unknown | revoked | expired | not_member | wrong_resource
	Prefix string // public, non-secret prefix of the token (as shown in Settings), when known
}

func (e *InvalidTokenError) Error() string { return "invalid token: " + e.Reason }

// Unwrap makes errors.Is(err, ErrInvalidToken) hold.
func (e *InvalidTokenError) Unwrap() error { return ErrInvalidToken }

func invalidToken(reason, prefix string) error {
	return &InvalidTokenError{Reason: reason, Prefix: prefix}
}

// tokenBody is the shape of every Hilo token after its prefix: 32 random bytes
// in unpadded base64url.
var tokenBody = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// ResolveBearer validates an OAuth access token or a personal access token.
// Membership is re-checked on every call (it is part of the lookup join), so
// removing someone from a workspace cuts their agents off immediately.
func ResolveBearer(ctx context.Context, q *store.Queries, token, wantResource string) (Principal, error) {
	var prefix string
	switch {
	case token == "":
		return Principal{}, invalidToken("missing", "")
	case strings.HasPrefix(token, accessPrefix):
		prefix = accessPrefix
	case strings.HasPrefix(token, identity.PATPrefix):
		prefix = identity.PATPrefix
	default:
		return Principal{}, invalidToken("malformed", "")
	}
	// A stray character (a pasted trailing "%", quote or space) is the most
	// common copy-paste mistake; report it as such instead of "unknown".
	if !tokenBody.MatchString(token[len(prefix):]) {
		return Principal{}, invalidToken("malformed", publicPrefix(token, prefix))
	}
	h := secure.Hash(token)
	if prefix == accessPrefix {
		row, err := q.ResolveAccessToken(ctx, h)
		if err != nil {
			return Principal{}, diagnose(ctx, q, err, h, prefix, wantResource)
		}
		if row.Resource != wantResource {
			return Principal{}, invalidToken("wrong_resource", "")
		}
		return Principal{UserID: row.UserID, Email: row.Email, WorkspaceID: row.WorkspaceID, Role: row.Role,
			Scope: row.Scope, Kind: "oauth", Client: row.ClientName, TokenID: row.ID}, nil
	}
	row, err := q.ResolvePAT(ctx, h)
	if err != nil {
		return Principal{}, diagnose(ctx, q, err, h, prefix, wantResource)
	}
	_ = q.TouchPAT(ctx, row.ID)
	return Principal{UserID: row.UserID, Email: row.Email, WorkspaceID: row.WorkspaceID, Role: row.Role,
		Scope: row.Scope, Kind: "pat", Client: "PAT: " + row.Name, TokenID: row.ID}, nil
}

// publicPrefix is the part of a PAT shown in Settings (prefix + 6 chars);
// OAuth tokens have no public part beyond their type prefix.
func publicPrefix(token, prefix string) string {
	if prefix == identity.PATPrefix && len(token) >= len(prefix)+6 {
		return token[:len(prefix)+6]
	}
	return prefix
}

// diagnose explains a failed lookup: unknown, revoked, expired, or the user is
// no longer a member of the workspace.
func diagnose(ctx context.Context, q *store.Queries, lookupErr error, hash, prefix, wantResource string) error {
	if !errors.Is(lookupErr, pgx.ErrNoRows) {
		return lookupErr
	}
	var revoked, expired, member bool
	var pub string
	if prefix == identity.PATPrefix {
		d, err := q.DiagnosePAT(ctx, hash)
		if err != nil {
			return invalidToken("unknown", "")
		}
		revoked, expired, member, pub = d.Revoked, d.Expired, d.Member, d.Prefix
	} else {
		d, err := q.DiagnoseAccessToken(ctx, hash)
		if err != nil {
			return invalidToken("unknown", "")
		}
		revoked, expired, member, pub = d.Revoked, d.Expired, d.Member, prefix
		if d.Resource != wantResource {
			return invalidToken("wrong_resource", pub)
		}
	}
	switch {
	case revoked:
		return invalidToken("revoked", pub)
	case expired:
		return invalidToken("expired", pub)
	case !member:
		return invalidToken("not_member", pub)
	default:
		return invalidToken("unknown", pub) // e.g. the workspace or user was archived/disabled
	}
}
