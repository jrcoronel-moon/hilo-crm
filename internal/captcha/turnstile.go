// Package captcha verifies Cloudflare Turnstile tokens for the public lead
// forms. The widget runs on the website that hosts the form; it adds a
// cf-turnstile-response field that Hilo checks here, server side, before
// anything is saved.
package captcha

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Field is the form field the Turnstile widget fills in.
const Field = "cf-turnstile-response"

// VerifyURL is Cloudflare's siteverify endpoint.
const VerifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"

var (
	// ErrMissing means the submission has no token (the widget is not on the
	// page, or the visitor did not complete it).
	ErrMissing = errors.New("captcha token missing")
	// ErrRejected means Cloudflare found the token invalid, expired or reused.
	ErrRejected = errors.New("captcha rejected")
)

// Turnstile checks tokens against Cloudflare. The secret comes with each
// call: every workspace has its own keys.
type Turnstile struct {
	Endpoint string       // VerifyURL unless overridden (tests)
	HTTP     *http.Client // short timeout: a visitor is waiting
}

// NewTurnstile returns a verifier for endpoint ("" means Cloudflare's).
func NewTurnstile(endpoint string) *Turnstile {
	if endpoint == "" {
		endpoint = VerifyURL
	}
	return &Turnstile{Endpoint: endpoint, HTTP: &http.Client{Timeout: 8 * time.Second}}
}

// Result is what Cloudflare reports about a valid token.
type Result struct {
	Hostname string // the site the widget was solved on
}

// Verify validates a token once (tokens are single use) with the workspace's
// secret. remoteIP is the visitor's address, sent as an extra signal.
func (t *Turnstile) Verify(ctx context.Context, secret, token, remoteIP string) (Result, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return Result{}, ErrMissing
	}
	if len(token) > 2048 { // Cloudflare's documented maximum
		return Result{}, ErrRejected
	}
	form := url.Values{"secret": {secret}, "response": {token}}
	if remoteIP != "" {
		form.Set("remoteip", remoteIP)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.Endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := t.HTTP.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("captcha verify: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("captcha verify: status %d", res.StatusCode)
	}
	var body struct {
		Success    bool     `json:"success"`
		Hostname   string   `json:"hostname"`
		ErrorCodes []string `json:"error-codes"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(nil, res.Body, 64<<10)).Decode(&body); err != nil {
		return Result{}, fmt.Errorf("captcha verify: %w", err)
	}
	if !body.Success {
		// A bad secret is our misconfiguration, not the visitor's fault.
		for _, c := range body.ErrorCodes {
			if c == "invalid-input-secret" || c == "missing-input-secret" {
				return Result{}, fmt.Errorf("captcha verify: %s", c)
			}
		}
		return Result{}, ErrRejected
	}
	return Result{Hostname: strings.ToLower(body.Hostname)}, nil
}
