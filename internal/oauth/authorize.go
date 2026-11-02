package oauth

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/jrcoronel-moon/hilo-crm/internal/crm"
	"github.com/jrcoronel-moon/hilo-crm/internal/secure"
	"github.com/jrcoronel-moon/hilo-crm/internal/store"
)

// Pending is a validated authorization request waiting for the user's
// consent. It travels to the consent page as an HMAC-signed blob, so no
// server-side state is needed and nothing in it can be altered.
type Pending struct {
	ClientID      string `json:"c"`
	ClientName    string `json:"n"`
	Verified      bool   `json:"v"`
	RedirectURI   string `json:"r"`
	CodeChallenge string `json:"cc"`
	State         string `json:"s"`
	Scope         string `json:"sc"`
	Resource      string `json:"rs"`
}

// RedirectHost is what the consent page shows as the destination: the host
// the authorization code will be sent to.
func (p Pending) RedirectHost() string {
	u, err := url.Parse(p.RedirectURI)
	if err != nil {
		return p.RedirectURI
	}
	if u.Host == "" {
		return u.Scheme + "://"
	}
	return u.Host
}

// RedirectLocal reports whether the code goes to an app on the person's own
// machine (a loopback redirect, as desktop and CLI agents use).
func (p Pending) RedirectLocal() bool {
	u, err := url.Parse(p.RedirectURI)
	return err == nil && u.Scheme == "http" && isLoopback(u.Hostname())
}

// ReadOnly reports whether only crm:read was requested.
func (p Pending) ReadOnly() bool { return p.Scope == string(crm.PermRead) }

const pendingPurpose = "oauth-pending"

// AuthorizeError is a failed authorization request. When Redirect is set the
// error must be sent back to the client at that URL; otherwise the request
// could not be tied to a valid client and redirect URI, and the error is
// shown to the user instead (never redirect to an unvalidated URI).
type AuthorizeError struct {
	Code        string
	Description string
	Redirect    string
}

func (e *AuthorizeError) Error() string { return e.Code + ": " + e.Description }

var challengeRe = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// ParseAuthorize validates an authorization request (GET /oauth/authorize).
func (s *Server) ParseAuthorize(ctx context.Context, q url.Values) (Pending, error) {
	clientID, redirectURI := q.Get("client_id"), q.Get("redirect_uri")
	c, err := s.resolveClient(ctx, clientID, redirectURI)
	if err != nil {
		s.log.Info("oauth authorize: client rejected", "client_id", truncate(clientID, 200), "err", err)
		return Pending{}, &AuthorizeError{Code: "invalid_client", Description: "unknown client or unreadable client metadata document"}
	}
	if redirectURI == "" || !redirectAllowed(c.RedirectURIs, redirectURI) {
		return Pending{}, &AuthorizeError{Code: "invalid_request", Description: "redirect_uri is not registered for this client"}
	}
	fail := func(code, desc string) error {
		v := url.Values{"error": {code}, "error_description": {desc}, "iss": {s.Issuer()}}
		if st := q.Get("state"); st != "" {
			v.Set("state", st)
		}
		return &AuthorizeError{Code: code, Description: desc, Redirect: appendQuery(redirectURI, v)}
	}
	if q.Get("response_type") != "code" {
		return Pending{}, fail("unsupported_response_type", "only response_type=code is supported")
	}
	if q.Get("code_challenge_method") != "S256" || !challengeRe.MatchString(q.Get("code_challenge")) {
		return Pending{}, fail("invalid_request", "PKCE is required: code_challenge with code_challenge_method=S256")
	}
	scope, ok := normalizeScope(q.Get("scope"))
	if !ok {
		return Pending{}, fail("invalid_scope", "supported scopes: "+strings.Join(supportedScopes, " "))
	}
	resource := s.cfg.MCPResource()
	if rs := q.Get("resource"); rs != "" && !s.validResource(rs) {
		return Pending{}, fail("invalid_target", "resource must be "+resource)
	}
	if len(q.Get("state")) > 1000 {
		return Pending{}, fail("invalid_request", "state too long")
	}
	return Pending{
		ClientID: c.ID, ClientName: c.Name, Verified: c.Verified(), RedirectURI: redirectURI,
		CodeChallenge: q.Get("code_challenge"), State: q.Get("state"), Scope: scope, Resource: resource,
	}, nil
}

// validResource accepts the MCP endpoint (with or without trailing slash) and
// the bare origin, which some clients send.
func (s *Server) validResource(rs string) bool {
	rs = strings.TrimRight(rs, "/")
	return rs == s.cfg.MCPResource() || rs == s.cfg.BaseURL
}

// normalizeScope keeps only known scopes; empty means all of them.
func normalizeScope(raw string) (string, bool) {
	if strings.TrimSpace(raw) == "" {
		return crm.AllScopes, true
	}
	known := map[string]bool{}
	for _, s := range supportedScopes {
		known[s] = true
	}
	var out []string
	seen := map[string]bool{}
	for _, s := range strings.Fields(raw) {
		switch {
		case s == "openid" || s == "offline_access" || s == "mcp":
			// Requested by some generic clients; harmless to ignore.
		case !known[s]:
			return "", false
		case !seen[s]:
			seen[s] = true
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return crm.AllScopes, true
	}
	return strings.Join(out, " "), true
}

// SignPending serialises a pending request for the consent form.
func (s *Server) SignPending(p Pending) (string, error) {
	return secure.Sign(s.cfg.SessionSecret, pendingPurpose, p, PendingTTL)
}

// VerifyPending checks a blob from the consent form.
func (s *Server) VerifyPending(blob string) (Pending, error) {
	var p Pending
	err := secure.Verify(s.cfg.SessionSecret, pendingPurpose, blob, &p)
	return p, err
}

// ErrNoWorkspaceAccess is returned when the user is not a member of the
// workspace chosen on the consent page.
var ErrNoWorkspaceAccess = errors.New("not a member of that workspace")

// Approve issues an authorization code bound to (client, user, workspace,
// redirect URI, PKCE challenge, scope, resource) and returns the URL to send
// the browser to. readOnly narrows the grant to crm:read.
func (s *Server) Approve(ctx context.Context, p Pending, userID, workspaceID uuid.UUID, readOnly bool) (string, error) {
	scope := p.Scope
	if readOnly {
		scope = string(crm.PermRead)
	}
	return s.approve(ctx, p, userID, workspaceID, scope)
}

// Preapproved skips the consent screen when exactly one of the user's
// workspaces trusts this client (see identity.Preapprove). The grant is what
// the client asked for, narrowed to what the workspace allows; a request for
// crm:admin alone, or a client not identified by URL, gets the consent screen.
func (s *Server) Preapproved(ctx context.Context, p Pending, userID uuid.UUID) (string, uuid.UUID, bool, error) {
	if !p.Verified {
		return "", uuid.Nil, false, nil
	}
	rows, err := s.db.Platform().PreapprovedWorkspaces(ctx, store.PreapprovedWorkspacesParams{ClientID: p.ClientID, UserID: userID})
	if err != nil || len(rows) != 1 {
		return "", uuid.Nil, false, err
	}
	allowed := strings.Fields(rows[0].Scope)
	var scope []string
	for _, sc := range strings.Fields(p.Scope) {
		if slices.Contains(allowed, sc) {
			scope = append(scope, sc)
		}
	}
	if len(scope) == 0 {
		return "", uuid.Nil, false, nil
	}
	dest, err := s.approve(ctx, p, userID, rows[0].WorkspaceID, strings.Join(scope, " "))
	return dest, rows[0].WorkspaceID, err == nil, err
}

func (s *Server) approve(ctx context.Context, p Pending, userID, workspaceID uuid.UUID, scope string) (string, error) {
	// Re-check everything the blob claims against the store.
	c, err := s.resolveClient(ctx, p.ClientID, p.RedirectURI)
	if err != nil || !redirectAllowed(c.RedirectURIs, p.RedirectURI) {
		return "", errors.New("client no longer valid")
	}
	if _, err := s.db.Platform().GetMembership(ctx, store.GetMembershipParams{WorkspaceID: workspaceID, UserID: userID}); err != nil {
		return "", ErrNoWorkspaceAccess
	}
	code := secure.Token("hilo_code_", 32)
	if _, err := s.db.Platform().CreateOAuthCode(ctx, store.CreateOAuthCodeParams{
		CodeHash: secure.Hash(code), ClientID: p.ClientID, UserID: userID, WorkspaceID: workspaceID,
		RedirectUri: p.RedirectURI, CodeChallenge: p.CodeChallenge, Scope: scope, Resource: p.Resource,
		ExpiresAt: s.now().Add(CodeTTL),
	}); err != nil {
		return "", err
	}
	v := url.Values{"code": {code}, "iss": {s.Issuer()}}
	if p.State != "" {
		v.Set("state", p.State)
	}
	return appendQuery(p.RedirectURI, v), nil
}

// Deny returns the URL that tells the client the user refused.
func (s *Server) Deny(p Pending) string {
	v := url.Values{"error": {"access_denied"}, "iss": {s.Issuer()}}
	if p.State != "" {
		v.Set("state", p.State)
	}
	return appendQuery(p.RedirectURI, v)
}

func appendQuery(raw string, extra url.Values) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	q := u.Query()
	for k, vs := range extra {
		q[k] = vs
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
