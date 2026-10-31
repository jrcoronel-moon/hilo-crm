package crm

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/jrcoronel-moon/hilo-crm/internal/secure"
	"github.com/jrcoronel-moon/hilo-crm/internal/store"
)

// Cloudflare Turnstile keys for the workspace's public lead forms. An admin
// enters them in the UI; the secret is sealed at rest (see secure.Seal) and
// never shown again, only its last characters.

const captchaPurpose = "turnstile-secret"

var captchaKeyRE = regexp.MustCompile(`^[0-9A-Za-z_-]{10,100}$`)

// WithSecrets gives the service the server's master secret (SESSION_SECRET),
// needed to seal and open third-party secrets stored in the database.
func (s *Service) WithSecrets(master []byte) *Service {
	s.master = master
	return s
}

// CaptchaView is what the admin UI shows: never the secret itself.
type CaptchaView struct {
	Configured bool
	SiteKey    string
	SecretHint string // last characters of the secret
	Readable   bool   // false: sealed under another SESSION_SECRET, enter it again
	UpdatedAt  time.Time
}

// CaptchaSettings returns the workspace's Turnstile keys, masked (admin).
func (s *Service) CaptchaSettings(ctx context.Context, a Actor) (CaptchaView, error) {
	var v CaptchaView
	err := s.tx(ctx, a, PermAdmin, func(q *store.Queries) error {
		row, err := q.GetWorkspaceCaptcha(ctx, a.WorkspaceID)
		if isNoRows(err) {
			return nil
		}
		if err != nil {
			return err
		}
		_, oerr := secure.Open(s.master, captchaPurpose, a.WorkspaceID.String(), row.SecretEnc)
		v = CaptchaView{Configured: true, SiteKey: row.SiteKey, SecretHint: row.SecretHint, Readable: oerr == nil, UpdatedAt: row.UpdatedAt}
		return nil
	})
	return v, err
}

// SetCaptchaKeys stores the workspace's Turnstile keys (admin). An empty
// secret keeps the stored one, so the site key can change without retyping it.
func (s *Service) SetCaptchaKeys(ctx context.Context, a Actor, siteKey, secret string) error {
	siteKey, secret = strings.TrimSpace(siteKey), strings.TrimSpace(secret)
	if !captchaKeyRE.MatchString(siteKey) {
		return invalid("site_key", "paste the site key from Cloudflare Turnstile")
	}
	if secret != "" && !captchaKeyRE.MatchString(secret) {
		return invalid("secret_key", "paste the secret key from Cloudflare Turnstile")
	}
	if len(s.master) == 0 {
		return errors.New("crm: captcha keys need the server secret (WithSecrets)")
	}
	return s.tx(ctx, a, PermAdmin, func(q *store.Queries) error {
		aad := a.WorkspaceID.String()
		var enc, hint string
		if secret == "" {
			row, err := q.GetWorkspaceCaptcha(ctx, a.WorkspaceID)
			if isNoRows(err) {
				return invalid("secret_key", "required")
			}
			if err != nil {
				return err
			}
			if _, err := secure.Open(s.master, captchaPurpose, aad, row.SecretEnc); err != nil {
				return invalid("secret_key", "the stored secret can no longer be read: enter it again")
			}
			enc, hint = row.SecretEnc, row.SecretHint
		} else {
			var err error
			if enc, err = secure.Seal(s.master, captchaPurpose, aad, secret); err != nil {
				return err
			}
			hint = secret[len(secret)-4:]
		}
		var by *uuid.UUID
		if a.UserID != uuid.Nil {
			by = &a.UserID
		}
		if err := q.UpsertWorkspaceCaptcha(ctx, store.UpsertWorkspaceCaptchaParams{
			WorkspaceID: a.WorkspaceID, SiteKey: siteKey, SecretEnc: enc, SecretHint: hint, UpdatedBy: by,
		}); err != nil {
			return err
		}
		return s.audit(ctx, q, a, "captcha.update", "workspace", a.WorkspaceID,
			map[string]any{"site_key": siteKey, "secret_changed": secret != ""})
	})
}

// DeleteCaptchaKeys removes the keys (admin). Refused while a form still
// requires the captcha: every submission to it would fail.
func (s *Service) DeleteCaptchaKeys(ctx context.Context, a Actor) error {
	return s.tx(ctx, a, PermAdmin, func(q *store.Queries) error {
		n, err := q.CountCaptchaForms(ctx, a.WorkspaceID)
		if err != nil {
			return err
		}
		if n > 0 {
			return invalid("captcha", "%d form(s) still require the captcha: turn it off there first", n)
		}
		if _, err := q.DeleteWorkspaceCaptcha(ctx, a.WorkspaceID); err != nil {
			return err
		}
		return s.audit(ctx, q, a, "captcha.delete", "workspace", a.WorkspaceID, nil)
	})
}

// captchaReady refuses to require a captcha the workspace has no usable keys for.
func (s *Service) captchaReady(ctx context.Context, q *store.Queries, ws uuid.UUID) error {
	row, err := q.GetWorkspaceCaptcha(ctx, ws)
	if isNoRows(err) {
		return invalid("captcha", "save the Turnstile keys first")
	}
	if err != nil {
		return err
	}
	if _, err := secure.Open(s.master, captchaPurpose, ws.String(), row.SecretEnc); err != nil {
		return invalid("captcha", "the stored Turnstile secret can no longer be read: enter it again")
	}
	return nil
}

// ErrCaptchaUnavailable means a form requires the captcha but its workspace
// has no readable keys (removed, or SESSION_SECRET rotated).
var ErrCaptchaUnavailable = errors.New("crm: captcha keys unavailable")

// formCaptchaSecret opens the workspace secret for the public endpoint, inside
// the form-lookup transaction (policy form_lookup on workspace_captcha).
func (s *Service) formCaptchaSecret(ctx context.Context, q *store.Queries, ws uuid.UUID) (string, error) {
	row, err := q.GetWorkspaceCaptcha(ctx, ws)
	if err != nil {
		return "", ErrCaptchaUnavailable
	}
	secret, err := secure.Open(s.master, captchaPurpose, ws.String(), row.SecretEnc)
	if err != nil {
		return "", ErrCaptchaUnavailable
	}
	return secret, nil
}
