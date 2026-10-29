// Package oauth is Hilo's OAuth 2.1 authorization server for MCP clients
// (Claude, ChatGPT, Grok, Cursor…), following the MCP authorization spec:
// RFC 9728 protected-resource metadata, RFC 8414 server metadata, RFC 7591
// dynamic client registration, Client ID Metadata Documents, mandatory PKCE
// (S256), RFC 8707 resource indicators and rotating refresh tokens with
// reuse detection (RFC 9700). It also resolves bearer tokens (OAuth access
// tokens and personal access tokens) for the MCP service.
//
// The human steps (sign-in and consent) are rendered by the web package; this
// package validates requests and issues credentials.
package oauth

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/jrcoronel-moon/hilo-crm/internal/config"
	"github.com/jrcoronel-moon/hilo-crm/internal/crm"
	"github.com/jrcoronel-moon/hilo-crm/internal/db"
	"github.com/jrcoronel-moon/hilo-crm/internal/httpx"
)

// Lifetimes.
const (
	AccessTTL  = time.Hour
	RefreshTTL = 30 * 24 * time.Hour // sliding: every refresh starts a new window
	CodeTTL    = 2 * time.Minute
	PendingTTL = 10 * time.Minute // signed consent request

	// RefreshGrace accepts an already-rotated refresh token for a few seconds,
	// because agents fire tool calls in parallel and they all refresh at once
	// with the same token. Outside it, reuse revokes the whole token family.
	RefreshGrace = 30 * time.Second
)

// Token prefixes make leaked credentials recognisable to secret scanners.
const (
	accessPrefix  = "hilo_at_"
	refreshPrefix = "hilo_rt_"
	clientPrefix  = "hilo_c_"
)

// Scopes this server issues.
var supportedScopes = []string{string(crm.PermRead), string(crm.PermWrite), string(crm.PermAdmin)}

// Server implements the OAuth endpoints.
type Server struct {
	cfg *config.Config
	db  *db.DB
	log *slog.Logger
	now func() time.Time

	// cimd fetches Client ID Metadata Documents (replaceable in tests).
	cimd *http.Client

	registerLimit *httpx.RateLimiter
	tokenLimit    *httpx.RateLimiter
	ips           *httpx.ClientIP
}

// New builds the server.
func New(cfg *config.Config, d *db.DB, log *slog.Logger) *Server {
	return &Server{
		cfg: cfg, db: d, log: log, now: time.Now, cimd: newCIMDClient(),
		registerLimit: httpx.NewRateLimiter(10, 20), // per IP: 10/min, burst 20
		tokenLimit:    httpx.NewRateLimiter(60, 60),
		ips:           httpx.NewClientIP(cfg.TrustedProxies),
	}
}

// Issuer is the authorization server identifier (RFC 8414).
func (s *Server) Issuer() string { return s.cfg.BaseURL }

// Routes mounts the public OAuth endpoints. The authorize/consent pages are
// mounted by the web package.
func (s *Server) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", s.protectedResource)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", s.protectedResource)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", s.serverMetadata)
	mux.HandleFunc("GET /.well-known/openid-configuration", s.serverMetadata)
	mux.HandleFunc("POST /oauth/register", s.register)
	mux.HandleFunc("POST /oauth/token", s.token)
	mux.HandleFunc("POST /oauth/revoke", s.revoke)
	for _, p := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp",
		"/.well-known/oauth-authorization-server", "/.well-known/openid-configuration",
		"/oauth/register", "/oauth/token", "/oauth/revoke"} {
		mux.HandleFunc("OPTIONS "+p, preflight)
	}
}

// cors lets browser-based MCP clients call the discovery, registration and
// token endpoints. These endpoints use no cookies, so a wildcard origin is
// safe (credentials are never allowed).
func cors(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, MCP-Protocol-Version")
	h.Set("Access-Control-Max-Age", "600")
}

func preflight(w http.ResponseWriter, _ *http.Request) {
	cors(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) protectedResource(w http.ResponseWriter, _ *http.Request) {
	cors(w)
	w.Header().Set("Cache-Control", "public, max-age=3600")
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"resource":                 s.cfg.MCPResource(),
		"authorization_servers":    []string{s.Issuer()},
		"bearer_methods_supported": []string{"header"},
		"scopes_supported":         supportedScopes,
		"resource_name":            "Hilo CRM",
		"resource_documentation":   "https://github.com/jrcoronel-moon/hilo-crm#connect-an-agent",
	})
}

func (s *Server) serverMetadata(w http.ResponseWriter, _ *http.Request) {
	cors(w)
	w.Header().Set("Cache-Control", "public, max-age=3600")
	b := s.Issuer()
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"issuer":                                         b,
		"authorization_endpoint":                         b + "/oauth/authorize",
		"token_endpoint":                                 b + "/oauth/token",
		"registration_endpoint":                          b + "/oauth/register",
		"revocation_endpoint":                            b + "/oauth/revoke",
		"response_types_supported":                       []string{"code"},
		"response_modes_supported":                       []string{"query"},
		"grant_types_supported":                          []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":               []string{"S256"},
		"token_endpoint_auth_methods_supported":          []string{"none", "client_secret_basic", "client_secret_post"},
		"revocation_endpoint_auth_methods_supported":     []string{"none", "client_secret_basic", "client_secret_post"},
		"scopes_supported":                               supportedScopes,
		"client_id_metadata_document_supported":          true,
		"authorization_response_iss_parameter_supported": true,
	})
}

// oauthError writes an RFC 6749 §5.2 error response.
func oauthError(w http.ResponseWriter, status int, code, desc string) {
	w.Header().Set("Cache-Control", "no-store")
	m := map[string]string{"error": code}
	if desc != "" {
		m["error_description"] = desc
	}
	httpx.WriteJSON(w, status, m)
}
