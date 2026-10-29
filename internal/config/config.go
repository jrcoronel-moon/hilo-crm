// Package config loads and validates settings from the environment. Invalid or
// unsafe combinations stop the process at startup instead of running degraded.
package config

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"strings"
)

// Config is the runtime configuration shared by every service.
type Config struct {
	DatabaseURL string
	ListenAddr  string

	// BaseURL is the public origin (scheme://host[:port], no path), e.g.
	// https://crm.example.com. OAuth issuer, redirect URIs and cookie security
	// all derive from it.
	BaseURL string

	// SessionSecret signs short-lived state (pending OAuth authorizations).
	SessionSecret []byte

	GoogleClientID     string
	GoogleClientSecret string

	MicrosoftClientID     string
	MicrosoftClientSecret string
	MicrosoftTenant       string

	// SuperadminEmails are promoted to superadmin on login (bootstrap).
	SuperadminEmails map[string]bool

	// TrustedProxies are the only peers whose X-Forwarded-For is honoured.
	TrustedProxies []netip.Prefix

	// DevLogin enables a password-less login form for local development. It is
	// refused unless BaseURL is a localhost URL.
	DevLogin bool
}

const minSecretLen = 32

// Load reads the environment. getenv is injectable for tests.
func Load(getenv func(string) string) (*Config, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	c := &Config{
		DatabaseURL:           getenv("DATABASE_URL"),
		ListenAddr:            orDefault(getenv("LISTEN_ADDR"), ":8080"),
		BaseURL:               strings.TrimRight(getenv("APP_BASE_URL"), "/"),
		SessionSecret:         []byte(getenv("SESSION_SECRET")),
		GoogleClientID:        getenv("GOOGLE_CLIENT_ID"),
		GoogleClientSecret:    getenv("GOOGLE_CLIENT_SECRET"),
		MicrosoftClientID:     getenv("MICROSOFT_CLIENT_ID"),
		MicrosoftClientSecret: getenv("MICROSOFT_CLIENT_SECRET"),
		MicrosoftTenant:       orDefault(getenv("MICROSOFT_TENANT"), "common"),
		SuperadminEmails:      map[string]bool{},
		DevLogin:              getenv("DEV_LOGIN_ENABLED") == "true",
	}
	for _, e := range strings.Split(getenv("SUPERADMIN_EMAILS"), ",") {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			c.SuperadminEmails[e] = true
		}
	}
	proxies := orDefault(getenv("TRUSTED_PROXY_CIDRS"), "127.0.0.1/32,::1/128,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16")
	for _, p := range strings.Split(proxies, ",") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		pfx, err := netip.ParsePrefix(p)
		if err != nil {
			return nil, fmt.Errorf("TRUSTED_PROXY_CIDRS: %w", err)
		}
		c.TrustedProxies = append(c.TrustedProxies, pfx)
	}
	return c, c.validate()
}

func (c *Config) validate() error {
	var errs []error
	if c.DatabaseURL == "" {
		errs = append(errs, errors.New("DATABASE_URL is required"))
	}
	u, err := url.Parse(c.BaseURL)
	switch {
	case c.BaseURL == "" || err != nil || u.Host == "":
		errs = append(errs, errors.New("APP_BASE_URL must be an absolute URL such as https://crm.example.com"))
	case u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil:
		errs = append(errs, errors.New("APP_BASE_URL must be an origin only (no path, query, fragment or credentials)"))
	case u.Scheme != "https" && (u.Scheme != "http" || !c.IsLocalhost()):
		errs = append(errs, errors.New("APP_BASE_URL must use https (plain http is only allowed for localhost)"))
	}
	if len(c.SessionSecret) < minSecretLen {
		errs = append(errs, fmt.Errorf("SESSION_SECRET must be at least %d bytes (generate one with: openssl rand -base64 48)", minSecretLen))
	}
	if (c.GoogleClientID == "") != (c.GoogleClientSecret == "") {
		errs = append(errs, errors.New("GOOGLE_CLIENT_ID and GOOGLE_CLIENT_SECRET must be set together"))
	}
	if (c.MicrosoftClientID == "") != (c.MicrosoftClientSecret == "") {
		errs = append(errs, errors.New("MICROSOFT_CLIENT_ID and MICROSOFT_CLIENT_SECRET must be set together"))
	}
	if c.DevLogin && !c.IsLocalhost() {
		errs = append(errs, errors.New("DEV_LOGIN_ENABLED is only allowed when APP_BASE_URL points to localhost"))
	}
	return errors.Join(errs...)
}

// IsLocalhost reports whether BaseURL points at the local machine.
func (c *Config) IsLocalhost() bool {
	u, err := url.Parse(c.BaseURL)
	if err != nil {
		return false
	}
	h := u.Hostname()
	return h == "localhost" || h == "127.0.0.1" || h == "::1" || strings.HasSuffix(h, ".localhost")
}

// SecureCookies reports whether cookies must carry the Secure flag.
func (c *Config) SecureCookies() bool { return strings.HasPrefix(c.BaseURL, "https://") }

// GoogleEnabled reports whether Google login is configured.
func (c *Config) GoogleEnabled() bool { return c.GoogleClientID != "" }

// MicrosoftEnabled reports whether Microsoft login is configured.
func (c *Config) MicrosoftEnabled() bool { return c.MicrosoftClientID != "" }

// MCPResource is the canonical URI of the MCP endpoint, used as the OAuth
// resource / token audience (RFC 8707).
func (c *Config) MCPResource() string { return c.BaseURL + "/mcp" }

// Providers lists the enabled login providers (for the banner and login page).
func (c *Config) Providers() []string {
	var p []string
	if c.GoogleEnabled() {
		p = append(p, "google")
	}
	if c.MicrosoftEnabled() {
		p = append(p, "microsoft")
	}
	if c.DevLogin {
		p = append(p, "dev-login")
	}
	return p
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}
