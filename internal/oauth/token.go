package oauth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jrcoronel-moon/hilo-crm/internal/httpx"
	"github.com/jrcoronel-moon/hilo-crm/internal/secure"
	"github.com/jrcoronel-moon/hilo-crm/internal/store"
)

// authenticateClient applies the client's registered auth method: public
// clients send only client_id (PKCE is their proof); confidential clients
// must present the matching secret (basic or post).
func (s *Server) authenticateClient(ctx context.Context, r *http.Request) (*client, error) {
	id, secret := r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	if u, p, ok := r.BasicAuth(); ok {
		id, secret = u, p
	}
	c, err := s.resolveClient(ctx, id, "")
	if err != nil {
		return nil, err
	}
	if c.SecretHash != "" && !secure.Equal(secure.Hash(secret), c.SecretHash) {
		return nil, errors.New("bad client secret")
	}
	return c, nil
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	cors(w)
	if !s.tokenLimit.Allow(s.ips.Of(r)) {
		oauthError(w, http.StatusTooManyRequests, "slow_down", "")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "body must be application/x-www-form-urlencoded")
		return
	}
	c, err := s.authenticateClient(r.Context(), r)
	if err != nil {
		w.Header().Set("WWW-Authenticate", `Basic realm="hilo"`)
		oauthError(w, http.StatusUnauthorized, "invalid_client", "")
		return
	}
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		s.grantCode(w, r, c)
	case "refresh_token":
		s.grantRefresh(w, r, c)
	default:
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type", "")
	}
}

func (s *Server) grantCode(w http.ResponseWriter, r *http.Request, c *client) {
	ctx := r.Context()
	f := r.PostForm
	code := f.Get("code")
	q := s.db.Platform()
	row, err := q.ConsumeOAuthCode(ctx, secure.Hash(code))
	if errors.Is(err, pgx.ErrNoRows) {
		// Replaying a redeemed code means it leaked: revoke what it minted.
		if fam, ferr := q.GetUsedOAuthCodeFamily(ctx, secure.Hash(code)); ferr == nil {
			_ = q.RevokeTokenFamily(ctx, fam)
			s.log.Warn("oauth: authorization code replayed; token family revoked", "client_id", c.ID)
		}
		oauthError(w, http.StatusBadRequest, "invalid_grant", "invalid or expired authorization code")
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	switch {
	case row.ClientID != c.ID:
		oauthError(w, http.StatusBadRequest, "invalid_grant", "code was issued to another client")
	case row.RedirectUri != f.Get("redirect_uri"):
		oauthError(w, http.StatusBadRequest, "invalid_grant", "redirect_uri does not match")
	case !secure.Equal(secure.PKCES256(f.Get("code_verifier")), row.CodeChallenge) || len(f.Get("code_verifier")) < 43 || len(f.Get("code_verifier")) > 128:
		oauthError(w, http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
	case f.Get("resource") != "" && !s.validResource(f.Get("resource")):
		oauthError(w, http.StatusBadRequest, "invalid_target", "")
	default:
		s.issue(ctx, w, issueParams{family: row.FamilyID, clientID: c.ID, userID: row.UserID, workspaceID: row.WorkspaceID,
			scope: row.Scope, resource: row.Resource, grantedAt: s.now()})
	}
}

func (s *Server) grantRefresh(w http.ResponseWriter, r *http.Request, c *client) {
	ctx := r.Context()
	refresh := r.PostForm.Get("refresh_token")
	q := s.db.Platform()
	row, err := q.ConsumeRefresh(ctx, store.ConsumeRefreshParams{RefreshHash: secure.Hash(refresh), GraceSeconds: int32(RefreshGrace / time.Second)})
	if errors.Is(err, pgx.ErrNoRows) {
		if fam, ferr := q.GetRefreshFamily(ctx, secure.Hash(refresh)); ferr == nil {
			_ = q.RevokeTokenFamily(ctx, fam)
			s.log.Warn("oauth: refresh token reused after rotation; token family revoked", "client_id", c.ID)
		}
		oauthError(w, http.StatusBadRequest, "invalid_grant", "invalid, expired or revoked refresh token")
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	if row.ClientID != c.ID {
		_ = q.RevokeTokenFamily(ctx, row.FamilyID)
		oauthError(w, http.StatusBadRequest, "invalid_grant", "refresh token was issued to another client")
		return
	}
	scope := row.Scope
	if req, ok := normalizeScope(r.PostForm.Get("scope")); ok && r.PostForm.Get("scope") != "" {
		// A refresh may narrow the scope, never widen it.
		if !subset(req, row.Scope) {
			oauthError(w, http.StatusBadRequest, "invalid_scope", "cannot widen the scope of a grant")
			return
		}
		scope = req
	}
	s.issue(ctx, w, issueParams{family: row.FamilyID, clientID: c.ID, userID: row.UserID, workspaceID: row.WorkspaceID,
		scope: scope, resource: row.Resource, grantedAt: row.GrantedAt})
}

func subset(a, b string) bool {
	have := map[string]bool{}
	for _, s := range strings.Fields(b) {
		have[s] = true
	}
	for _, s := range strings.Fields(a) {
		if !have[s] {
			return false
		}
	}
	return true
}

type issueParams struct {
	family              uuid.UUID
	clientID            string
	userID, workspaceID uuid.UUID
	scope, resource     string
	grantedAt           time.Time
}

func (s *Server) issue(ctx context.Context, w http.ResponseWriter, p issueParams) {
	access, refresh := secure.Token(accessPrefix, 32), secure.Token(refreshPrefix, 32)
	now := s.now()
	err := s.db.Platform().CreateOAuthToken(ctx, store.CreateOAuthTokenParams{
		FamilyID: p.family, ClientID: p.clientID, UserID: p.userID, WorkspaceID: p.workspaceID,
		AccessHash: secure.Hash(access), RefreshHash: secure.Hash(refresh), Scope: p.scope, Resource: p.resource,
		ExpiresAt: now.Add(AccessTTL), RefreshExpiresAt: now.Add(RefreshTTL), GrantedAt: p.grantedAt,
	})
	if err != nil {
		s.serverError(w, err)
		return
	}
	_ = s.db.Platform().TouchOAuthClient(ctx, p.clientID)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"access_token":  access,
		"token_type":    "Bearer",
		"expires_in":    int(AccessTTL / time.Second),
		"refresh_token": refresh,
		"scope":         p.scope,
	})
}

// revoke implements RFC 7009. It always answers 200 (no token oracle).
func (s *Server) revoke(w http.ResponseWriter, r *http.Request) {
	cors(w)
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "")
		return
	}
	c, err := s.authenticateClient(r.Context(), r)
	if err != nil {
		oauthError(w, http.StatusUnauthorized, "invalid_client", "")
		return
	}
	tok := r.PostForm.Get("token")
	q := s.db.Platform()
	h := secure.Hash(tok)
	if fam, err := q.GetRefreshFamily(r.Context(), h); err == nil {
		_ = q.RevokeTokenFamily(r.Context(), fam)
	} else if p, err := q.ResolveAccessToken(r.Context(), h); err == nil && p.ClientID == c.ID {
		_, _ = q.RevokeConnection(r.Context(), store.RevokeConnectionParams{UserID: p.UserID, WorkspaceID: p.WorkspaceID, ClientID: c.ID})
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) serverError(w http.ResponseWriter, err error) {
	s.log.Error("oauth", "err", err)
	oauthError(w, http.StatusInternalServerError, "server_error", "")
}
