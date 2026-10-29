// Package branding holds the platform's name and logo, editable by
// superadmins. Every page, the login screen, the OAuth consent screen and the
// MCP server info use it. Values are cached briefly; writes refresh the cache.
package branding

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jrcoronel-moon/hilo-crm/internal/db"
	"github.com/jrcoronel-moon/hilo-crm/internal/store"
)

// DefaultName is used until a superadmin sets one.
const DefaultName = "Hilo CRM"

// MaxLogo is the largest logo accepted (bytes).
const MaxLogo = 256 << 10

// Errors safe to show to the user.
var (
	ErrForbidden = errors.New("only superadmins can change the branding")
	ErrName      = errors.New("the name must be 1-60 printable characters")
	ErrLogo      = errors.New("the logo must be a PNG, JPEG, WebP or SVG image of at most 256 KB")
)

// Brand is what pages render.
type Brand struct {
	Name    string
	HasLogo bool
	Version int64 // changes whenever the logo changes (cache busting)
}

// LogoURL is the logo to show: the uploaded one or the bundled default.
func (b Brand) LogoURL() string {
	if b.HasLogo {
		return "/branding/logo?v=" + strconv.FormatInt(b.Version, 10)
	}
	return "/static/favicon.svg"
}

// Service reads and writes the branding.
type Service struct {
	db  *db.DB
	mu  sync.RWMutex
	at  time.Time
	cur Brand
}

// New builds the service.
func New(d *db.DB) *Service { return &Service{db: d} }

const ttl = 30 * time.Second

// Get returns the current branding (cached for a few seconds; on any error
// it falls back to the default so pages always render).
func (s *Service) Get(ctx context.Context) Brand {
	s.mu.RLock()
	if time.Since(s.at) < ttl {
		b := s.cur
		s.mu.RUnlock()
		return b
	}
	s.mu.RUnlock()
	row, err := s.db.Platform().GetBranding(ctx)
	b := Brand{Name: DefaultName}
	if err == nil {
		b = Brand{Name: row.AppName, HasLogo: row.LogoMime != "", Version: row.UpdatedAt.Unix()}
	}
	s.mu.Lock()
	s.cur, s.at = b, time.Now()
	s.mu.Unlock()
	return b
}

func (s *Service) invalidate() {
	s.mu.Lock()
	s.at = time.Time{}
	s.mu.Unlock()
}

// Logo returns the uploaded logo and its type.
func (s *Service) Logo(ctx context.Context) ([]byte, string, time.Time, error) {
	row, err := s.db.Platform().GetLogo(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", time.Time{}, err
	}
	return row.Logo, row.LogoMime, row.UpdatedAt, err
}

// SetName changes the application name (superadmin).
func (s *Service) SetName(ctx context.Context, superadmin bool, by uuid.UUID, name string) error {
	if !superadmin {
		return ErrForbidden
	}
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 60 || strings.IndexFunc(name, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0 {
		return ErrName
	}
	defer s.invalidate()
	return s.db.Platform().SetAppName(ctx, store.SetAppNameParams{AppName: name, UpdatedBy: &by})
}

// SetLogo stores a logo after checking its real type from its bytes.
func (s *Service) SetLogo(ctx context.Context, superadmin bool, by uuid.UUID, data []byte) error {
	if !superadmin {
		return ErrForbidden
	}
	mime, ok := sniff(data)
	if !ok {
		return ErrLogo
	}
	defer s.invalidate()
	return s.db.Platform().SetLogo(ctx, store.SetLogoParams{Logo: data, LogoMime: mime, UpdatedBy: &by})
}

// ClearLogo goes back to the default logo (superadmin).
func (s *Service) ClearLogo(ctx context.Context, superadmin bool, by uuid.UUID) error {
	if !superadmin {
		return ErrForbidden
	}
	defer s.invalidate()
	return s.db.Platform().ClearLogo(ctx, &by)
}

// sniff identifies the image by content, never by the declared type.
func sniff(data []byte) (string, bool) {
	if len(data) == 0 || len(data) > MaxLogo {
		return "", false
	}
	switch ct := http.DetectContentType(data); ct {
	case "image/png", "image/jpeg", "image/webp":
		return ct, true
	}
	head := bytes.ToLower(bytes.TrimSpace(data[:min(len(data), 1024)]))
	head = bytes.TrimPrefix(head, []byte("\xef\xbb\xbf"))
	if (bytes.HasPrefix(head, []byte("<svg")) || bytes.HasPrefix(head, []byte("<?xml"))) && bytes.Contains(bytes.ToLower(data), []byte("<svg")) {
		return "image/svg+xml", true
	}
	return "", false
}
