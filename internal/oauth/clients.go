package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/jrcoronel-moon/hilo-crm/internal/httpx"
	"github.com/jrcoronel-moon/hilo-crm/internal/identity"
	"github.com/jrcoronel-moon/hilo-crm/internal/secure"
	"github.com/jrcoronel-moon/hilo-crm/internal/store"
)

const maxRedirectURIs = 10

// validRedirectURI accepts what MCP clients actually use and nothing that
// could deliver a code somewhere unexpected:
//   - https URLs (web clients such as claude.ai);
//   - http on a loopback host (CLI clients such as Claude Code, RFC 8252 §7.3);
//   - private-use schemes in reverse-domain form (desktop apps, RFC 8252 §7.1),
//     e.g. cursor://anysphere.cursor-retrieval/oauth/callback.
//
// Fragments, credentials and dangerous schemes are always rejected.
func validRedirectURI(raw string) bool {
	if len(raw) > 2000 || strings.ContainsAny(raw, " \t\r\n") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Fragment != "" || u.User != nil || u.Opaque != "" {
		return false
	}
	switch u.Scheme {
	case "https":
		return u.Host != ""
	case "http":
		return isLoopback(u.Hostname())
	case "", "javascript", "data", "file", "vbscript", "blob", "about", "ftp", "ws", "wss":
		return false
	default:
		return strings.Contains(u.Scheme, ".") || u.Host != ""
	}
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	a, err := netip.ParseAddr(strings.Trim(host, "[]"))
	return err == nil && a.Unmap().IsLoopback()
}

// redirectAllowed compares a requested redirect_uri with the registered ones:
// exact match, except that the port of a loopback http URI may differ (native
// clients listen on an ephemeral port; RFC 8252 §7.3). Scheme, host, path and
// query must still match exactly.
func redirectAllowed(registered []string, requested string) bool {
	for _, r := range registered {
		if r == requested || loopbackMatch(r, requested) {
			return true
		}
	}
	return false
}

func loopbackMatch(registered, requested string) bool {
	a, err1 := url.Parse(registered)
	b, err2 := url.Parse(requested)
	if err1 != nil || err2 != nil || a.Scheme != "http" || b.Scheme != "http" {
		return false
	}
	return isLoopback(a.Hostname()) && a.Hostname() == b.Hostname() && a.Path == b.Path && a.RawQuery == b.RawQuery
}

// register implements Dynamic Client Registration (RFC 7591).
func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	cors(w)
	if !s.registerLimit.Allow(s.ips.Of(r)) {
		oauthError(w, http.StatusTooManyRequests, "slow_down", "too many registrations from this address")
		return
	}
	var req struct {
		ClientName              string   `json:"client_name"`
		RedirectURIs            []string `json:"redirect_uris"`
		TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
		GrantTypes              []string `json:"grant_types"`
		ResponseTypes           []string `json:"response_types"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "body must be a JSON client metadata document")
		return
	}
	uris, ok := cleanRedirectURIs(req.RedirectURIs)
	if !ok {
		oauthError(w, http.StatusBadRequest, "invalid_redirect_uri",
			"redirect_uris must be 1-10 https URLs, loopback http URLs or reverse-domain private-use schemes, without fragments")
		return
	}
	for _, g := range req.GrantTypes {
		if g != "authorization_code" && g != "refresh_token" {
			oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "unsupported grant_type "+g)
			return
		}
	}
	method := req.TokenEndpointAuthMethod
	switch method {
	case "":
		method = "client_secret_basic"
	case "none", "client_secret_basic", "client_secret_post":
	default:
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "unsupported token_endpoint_auth_method")
		return
	}
	name := cleanName(req.ClientName)
	clientID := secure.Token(clientPrefix, 18)
	secret, secretHash := "", ""
	if method != "none" {
		secret = secure.Token("hilo_cs_", 32)
		secretHash = secure.Hash(secret)
	}
	if err := s.db.Platform().CreateOAuthClient(r.Context(), store.CreateOAuthClientParams{
		ClientID: clientID, Kind: "dcr", SecretHash: secretHash, Name: name, RedirectUris: uris,
	}); err != nil {
		s.log.Error("oauth register", "err", err)
		oauthError(w, http.StatusInternalServerError, "server_error", "")
		return
	}
	resp := map[string]any{
		"client_id":                  clientID,
		"client_id_issued_at":        s.now().Unix(),
		"client_name":                name,
		"redirect_uris":              uris,
		"token_endpoint_auth_method": method,
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
	}
	if secret != "" {
		resp["client_secret"] = secret
		resp["client_secret_expires_at"] = 0
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusCreated, resp)
}

func cleanRedirectURIs(in []string) ([]string, bool) {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, u := range in {
		u = strings.TrimSpace(u)
		if u == "" || seen[u] {
			continue
		}
		if !validRedirectURI(u) {
			return nil, false
		}
		seen[u] = true
		out = append(out, u)
	}
	return out, len(out) > 0 && len(out) <= maxRedirectURIs
}

// cleanName keeps client names displayable: printable, single line, short.
func cleanName(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	if r := []rune(s); len(r) > 100 {
		s = string(r[:100])
	}
	return s
}

// client is a registered or metadata-document client.
type client struct {
	ID           string
	Kind         string // dcr | cimd
	SecretHash   string
	Name         string
	RedirectURIs []string
}

// Verified reports whether the client's identity is proven by its domain
// (CIMD), as opposed to a self-asserted DCR name.
func (c client) Verified() bool { return c.Kind == "cimd" }

func fromRow(r store.OauthClient) *client {
	return &client{ID: r.ClientID, Kind: r.Kind, SecretHash: r.SecretHash, Name: r.Name, RedirectURIs: r.RedirectUris}
}

// resolveClient loads a client, fetching its metadata document when the
// client_id is a CIMD URL not seen before. When wantRedirect is not allowed
// by a cached CIMD document, the document is re-fetched once (the client may
// have added a callback).
func (s *Server) resolveClient(ctx context.Context, clientID, wantRedirect string) (*client, error) {
	if clientID == "" || len(clientID) > 2000 {
		return nil, errors.New("missing client_id")
	}
	row, err := s.db.Platform().GetOAuthClient(ctx, clientID)
	switch {
	case err == nil:
		c := fromRow(row)
		if wantRedirect == "" || c.Kind != "cimd" || redirectAllowed(c.RedirectURIs, wantRedirect) {
			return c, nil
		}
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, err
	case !isCIMD(clientID):
		return nil, errors.New("unknown client")
	}
	doc, err := fetchCIMD(ctx, s.cimd, clientID)
	if err != nil {
		return nil, fmt.Errorf("client metadata document: %w", err)
	}
	if err := s.db.Platform().UpsertCIMDClient(ctx, store.UpsertCIMDClientParams{ClientID: clientID, Name: doc.name, RedirectUris: doc.redirectURIs}); err != nil {
		return nil, err
	}
	return &client{ID: clientID, Kind: "cimd", Name: doc.name, RedirectURIs: doc.redirectURIs}, nil
}

// ───────────── Client ID Metadata Documents ─────────────

const (
	cimdTimeout = 5 * time.Second
	cimdMaxBody = 64 << 10
)

// isCIMD: the client_id is a Client ID Metadata Document URL.
func isCIMD(clientID string) bool { return identity.IsClientIDURL(clientID) }

// blockedPrefixes are non-public ranges not covered by netip's predicates.
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), // carrier-grade NAT
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"), // NAT64 can reach IPv4 private space
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2001:db8::/32"),
}

// PublicAddr reports whether a is a globally routable unicast address.
func PublicAddr(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsValid() || !a.IsGlobalUnicast() || a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() ||
		a.IsMulticast() || a.IsUnspecified() || a.IsInterfaceLocalMulticast() || a.IsLinkLocalMulticast() {
		return false
	}
	for _, p := range blockedPrefixes {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

// dialControl runs after DNS resolution, right before connecting, so a name
// that resolves (or re-resolves) to an internal address is refused.
func dialControl(_, address string, _ syscall.RawConn) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	a, err := netip.ParseAddr(host)
	if err != nil || !PublicAddr(a) || port != "443" {
		return fmt.Errorf("refusing to connect to non-public address %s", address)
	}
	return nil
}

func newCIMDClient() *http.Client {
	return &http.Client{
		Timeout: cimdTimeout,
		Transport: &http.Transport{
			Proxy:                  nil,
			DialContext:            (&net.Dialer{Timeout: cimdTimeout, Control: dialControl}).DialContext,
			TLSHandshakeTimeout:    cimdTimeout,
			ResponseHeaderTimeout:  cimdTimeout,
			MaxResponseHeaderBytes: 16 << 10,
			DisableKeepAlives:      true,
		},
		// The document must come from exactly the client_id URL.
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirects not allowed") },
	}
}

type cimdDoc struct {
	name         string
	redirectURIs []string
}

func fetchCIMD(ctx context.Context, hc *http.Client, clientID string) (cimdDoc, error) {
	// Fetching a caller-chosen URL is the point of CIMD; dialControl refuses
	// every non-public address after DNS resolution and redirects are off.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, clientID, nil) //nolint:gosec // G704: guarded by dialControl (see above)
	if err != nil {
		return cimdDoc{}, err
	}
	req.Header.Set("Accept", "application/json")
	res, err := hc.Do(req) //nolint:gosec // G704: guarded by dialControl (see above)
	if err != nil {
		return cimdDoc{}, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return cimdDoc{}, fmt.Errorf("status %d", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, cimdMaxBody+1))
	if err != nil {
		return cimdDoc{}, err
	}
	if len(body) > cimdMaxBody {
		return cimdDoc{}, errors.New("document too large")
	}
	var doc struct {
		ClientID                string   `json:"client_id"`
		ClientName              string   `json:"client_name"`
		RedirectURIs            []string `json:"redirect_uris"`
		TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return cimdDoc{}, errors.New("not a JSON document")
	}
	// Without this equality anyone could publish a document impersonating
	// another client.
	if doc.ClientID != clientID {
		return cimdDoc{}, errors.New("client_id in the document does not match its URL")
	}
	if doc.TokenEndpointAuthMethod != "" && doc.TokenEndpointAuthMethod != "none" {
		return cimdDoc{}, errors.New("metadata-document clients must be public (token_endpoint_auth_method none)")
	}
	uris, ok := cleanRedirectURIs(doc.RedirectURIs)
	if !ok {
		return cimdDoc{}, errors.New("invalid redirect_uris")
	}
	name := cleanName(doc.ClientName)
	if name == "" {
		u, _ := url.Parse(clientID)
		name = u.Host
	}
	return cimdDoc{name: name, redirectURIs: uris}, nil
}
