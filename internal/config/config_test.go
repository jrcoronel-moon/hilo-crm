package config

import (
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func base() map[string]string {
	return map[string]string{
		"DATABASE_URL":   "postgres://x",
		"APP_BASE_URL":   "https://crm.example.com",
		"SESSION_SECRET": strings.Repeat("s", 40),
	}
}

func TestLoadValid(t *testing.T) {
	c, err := Load(env(base()))
	if err != nil {
		t.Fatal(err)
	}
	if !c.SecureCookies() || c.MCPResource() != "https://crm.example.com/mcp" {
		t.Fatalf("unexpected derived values: %+v", c)
	}
}

func TestLoadRejectsUnsafe(t *testing.T) {
	cases := map[string]map[string]string{
		"short secret":        {"SESSION_SECRET": "short"},
		"http public host":    {"APP_BASE_URL": "http://crm.example.com"},
		"base url with path":  {"APP_BASE_URL": "https://crm.example.com/app"},
		"base url with creds": {"APP_BASE_URL": "https://u:p@crm.example.com"},
		"dev login on public": {"DEV_LOGIN_ENABLED": "true"},
		"google half set":     {"GOOGLE_CLIENT_ID": "id"},
		"microsoft half set":  {"MICROSOFT_CLIENT_SECRET": "s"},
		"missing database":    {"DATABASE_URL": ""},
		"bad proxy cidr":      {"TRUSTED_PROXY_CIDRS": "not-a-cidr"},
		"localhost lookalike": {"APP_BASE_URL": "http://localhost.evil.com", "DEV_LOGIN_ENABLED": "true"},
	}
	for name, override := range cases {
		t.Run(name, func(t *testing.T) {
			m := base()
			for k, v := range override {
				m[k] = v
			}
			if _, err := Load(env(m)); err == nil {
				t.Fatalf("expected an error for %s", name)
			}
		})
	}
}

func TestDevLoginAllowedOnLocalhost(t *testing.T) {
	m := base()
	m["APP_BASE_URL"] = "http://localhost:8080"
	m["DEV_LOGIN_ENABLED"] = "true"
	c, err := Load(env(m))
	if err != nil {
		t.Fatal(err)
	}
	if c.SecureCookies() {
		t.Fatal("http localhost must not force Secure cookies")
	}
}

func TestSuperadminEmailsNormalized(t *testing.T) {
	m := base()
	m["SUPERADMIN_EMAILS"] = " Admin@Example.com , ,b@x.io"
	c, err := Load(env(m))
	if err != nil {
		t.Fatal(err)
	}
	if !c.SuperadminEmails["admin@example.com"] || !c.SuperadminEmails["b@x.io"] || len(c.SuperadminEmails) != 2 {
		t.Fatalf("got %v", c.SuperadminEmails)
	}
}
