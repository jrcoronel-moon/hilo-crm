package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jrcoronel-moon/hilo-crm/internal/identity"
	"github.com/jrcoronel-moon/hilo-crm/internal/secure"
)

// provider is an OpenID Connect identity provider used for sign-in.
type provider struct {
	name         string
	authURL      string
	tokenURL     string
	clientID     string
	clientSecret string
	// issuerOK validates the id_token "iss" claim against the token's claims.
	issuerOK func(iss string, c idClaims) bool
	// identity turns verified claims into an external identity.
	identity func(c idClaims) (identity.ExternalIdentity, error)
}

// msConsumerTenant is the tenant of personal Microsoft accounts, whose email
// addresses Microsoft verifies.
const msConsumerTenant = "9188040d-6c67-4c5b-b112-36a304b66dad"

func (h *Handler) provider(name string) (*provider, bool) {
	switch {
	case name == "google" && h.cfg.GoogleEnabled():
		return &provider{ //nolint:gosec // G101: public endpoint URLs, not credentials
			name: "google", authURL: "https://accounts.google.com/o/oauth2/v2/auth", tokenURL: "https://oauth2.googleapis.com/token",
			clientID: h.cfg.GoogleClientID, clientSecret: h.cfg.GoogleClientSecret,
			issuerOK: func(iss string, _ idClaims) bool {
				return iss == "https://accounts.google.com" || iss == "accounts.google.com"
			},
			identity: func(c idClaims) (identity.ExternalIdentity, error) {
				return identity.ExternalIdentity{Provider: "google", Subject: c.Sub, Email: c.Email,
					EmailVerified: c.EmailVerified.bool(), Name: c.Name, AvatarURL: c.Picture}, nil
			},
		}, true
	case name == "microsoft" && h.cfg.MicrosoftEnabled():
		base := "https://login.microsoftonline.com/" + url.PathEscape(h.cfg.MicrosoftTenant) + "/oauth2/v2.0"
		return &provider{
			name: "microsoft", authURL: base + "/authorize", tokenURL: base + "/token",
			clientID: h.cfg.MicrosoftClientID, clientSecret: h.cfg.MicrosoftClientSecret,
			issuerOK: func(iss string, c idClaims) bool {
				return c.TID != "" && iss == "https://login.microsoftonline.com/"+c.TID+"/v2.0"
			},
			identity: microsoftIdentity,
		}, true
	}
	return nil, false
}

// microsoftIdentity defends against "nOAuth": Entra ID lets tenant admins set
// arbitrary, unverified email addresses. The stable subject is tid:oid, and
// the email is only trusted when Microsoft vouches for its domain (the
// optional xms_edov claim) or the account is a personal Microsoft account.
func microsoftIdentity(c idClaims) (identity.ExternalIdentity, error) {
	if c.TID == "" || c.OID == "" {
		return identity.ExternalIdentity{}, errors.New("microsoft: missing tid/oid")
	}
	email := c.Email
	if email == "" && c.TID == msConsumerTenant {
		email = c.PreferredUsername
	}
	verified := c.TID == msConsumerTenant || c.XmsEdov.bool()
	return identity.ExternalIdentity{Provider: "microsoft", Subject: c.TID + ":" + c.OID, Email: email,
		EmailVerified: verified, Name: c.Name}, nil
}

// idClaims are the id_token claims we use.
type idClaims struct {
	Iss               string   `json:"iss"`
	Aud               audience `json:"aud"`
	Exp               int64    `json:"exp"`
	Nonce             string   `json:"nonce"`
	Sub               string   `json:"sub"`
	Email             string   `json:"email"`
	EmailVerified     flexBool `json:"email_verified"`
	Name              string   `json:"name"`
	Picture           string   `json:"picture"`
	TID               string   `json:"tid"`
	OID               string   `json:"oid"`
	PreferredUsername string   `json:"preferred_username"`
	XmsEdov           flexBool `json:"xms_edov"`
}

// audience accepts the string or array forms of "aud".
type audience []string

func (a *audience) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*a = audience{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*a = many
	return nil
}

// flexBool accepts true/false and "true"/"false" (providers differ).
type flexBool bool

func (f *flexBool) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	*f = flexBool(s == "true" || s == "1")
	return nil
}

func (f flexBool) bool() bool { return bool(f) }

// loginState is kept in a signed, short-lived cookie between the redirect to
// the provider and the callback.
type loginState struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	Provider string `json:"p"`
	Return   string `json:"r"`
}

const (
	loginStatePurpose = "login-state"
	loginStateTTL     = 10 * time.Minute
)

func (h *Handler) loginCookieName() string {
	if h.cfg.SecureCookies() {
		return "__Host-hilo_login"
	}
	return "hilo_login"
}

func (h *Handler) redirectURI(p string) string { return h.cfg.BaseURL + "/auth/" + p + "/callback" }

// startURL builds the provider authorization URL and stores the state cookie.
func (h *Handler) startURL(w http.ResponseWriter, p *provider, returnTo string) (string, error) {
	st := loginState{State: secure.Token("", 24), Nonce: secure.Token("", 24), Verifier: secure.Token("", 48), Provider: p.name, Return: safeReturn(returnTo)}
	blob, err := secure.Sign(h.cfg.SessionSecret, loginStatePurpose, st, loginStateTTL)
	if err != nil {
		return "", err
	}
	h.setCookie(w, h.loginCookieName(), blob, int(loginStateTTL/time.Second))
	q := url.Values{
		"client_id": {p.clientID}, "redirect_uri": {h.redirectURI(p.name)}, "response_type": {"code"},
		"scope": {"openid email profile"}, "state": {st.State}, "nonce": {st.Nonce}, "prompt": {"select_account"},
		"code_challenge": {secure.PKCES256(st.Verifier)}, "code_challenge_method": {"S256"},
	}
	return p.authURL + "?" + q.Encode(), nil
}

// finish validates the callback and returns the verified identity.
func (h *Handler) finish(ctx context.Context, r *http.Request, p *provider) (identity.ExternalIdentity, string, error) {
	var st loginState
	c, err := r.Cookie(h.loginCookieName())
	if err != nil || secure.Verify(h.cfg.SessionSecret, loginStatePurpose, c.Value, &st) != nil {
		return identity.ExternalIdentity{}, "", errors.New("login state missing or expired")
	}
	if st.Provider != p.name || !secure.Equal(r.URL.Query().Get("state"), st.State) {
		return identity.ExternalIdentity{}, "", errors.New("state mismatch")
	}
	claims, err := h.exchange(ctx, p, r.URL.Query().Get("code"), st.Verifier)
	if err != nil {
		return identity.ExternalIdentity{}, "", err
	}
	switch {
	case !p.issuerOK(claims.Iss, claims):
		return identity.ExternalIdentity{}, "", fmt.Errorf("unexpected issuer %q", claims.Iss)
	case !slices.Contains(claims.Aud, p.clientID):
		return identity.ExternalIdentity{}, "", errors.New("id_token audience mismatch")
	case time.Now().Unix() > claims.Exp:
		return identity.ExternalIdentity{}, "", errors.New("id_token expired")
	case !secure.Equal(claims.Nonce, st.Nonce):
		return identity.ExternalIdentity{}, "", errors.New("nonce mismatch")
	case claims.Sub == "":
		return identity.ExternalIdentity{}, "", errors.New("id_token without subject")
	}
	ext, err := p.identity(claims)
	return ext, st.Return, err
}

// exchange redeems the code at the provider's token endpoint. The id_token is
// received directly from the provider over TLS in a back-channel call
// authenticated with our client secret, which OpenID Connect Core §3.1.3.7
// accepts in place of signature validation; iss/aud/exp/nonce are still
// checked by the caller.
func (h *Handler) exchange(ctx context.Context, p *provider, code, verifier string) (idClaims, error) {
	if code == "" || len(code) > 4096 {
		return idClaims{}, errors.New("missing code")
	}
	form := url.Values{
		"code": {code}, "client_id": {p.clientID}, "client_secret": {p.clientSecret},
		"redirect_uri": {h.redirectURI(p.name)}, "grant_type": {"authorization_code"}, "code_verifier": {verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return idClaims{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := h.http.Do(req)
	if err != nil {
		return idClaims{}, err
	}
	defer func() { _ = res.Body.Close() }()
	var tok struct {
		IDToken string `json:"id_token"`
		Error   string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&tok); err != nil {
		return idClaims{}, fmt.Errorf("token response: %w", err)
	}
	if res.StatusCode != http.StatusOK || tok.IDToken == "" {
		return idClaims{}, fmt.Errorf("token endpoint: status %d %s", res.StatusCode, tok.Error)
	}
	parts := strings.Split(tok.IDToken, ".")
	if len(parts) != 3 {
		return idClaims{}, errors.New("malformed id_token")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return idClaims{}, errors.New("malformed id_token payload")
	}
	var c idClaims
	if err := json.Unmarshal(raw, &c); err != nil {
		return idClaims{}, fmt.Errorf("id_token claims: %w", err)
	}
	return c, nil
}
