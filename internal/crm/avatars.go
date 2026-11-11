package crm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jrcoronel-moon/hilo-crm/internal/store"
)

// Record pictures: a company's logo_url and a contact's photo_url. Browsers
// never load those URLs; Avatar fetches them here (safehttp: public https
// hosts only, redirects re-checked), keeps raster images only, and caches the
// bytes per workspace, so pages show them from their own origin.

const (
	avatarTimeout  = 5 * time.Second
	avatarMaxBytes = 512 << 10
	avatarFresh    = 7 * 24 * time.Hour // refetch after a week (the source may change the image)
	avatarRetry    = 6 * time.Hour      // after a failed fetch, wait before trying again
)

// avatarTypes are the formats served; SVG is not one of them (it can carry script).
var avatarTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

// Avatar returns the picture of a company or contact and its content type;
// ErrNotFound when it has none or it cannot be fetched.
func (s *Service) Avatar(ctx context.Context, a Actor, kind string, id uuid.UUID) ([]byte, string, error) {
	var src string
	var cached store.GetAvatarRow
	have := false
	err := s.tx(ctx, a, PermRead, func(q *store.Queries) error {
		switch kind {
		case KindCompany:
			c, err := q.GetCompany(ctx, store.GetCompanyParams{WorkspaceID: a.WorkspaceID, ID: id})
			if err != nil {
				return notFound("company", err)
			}
			src = c.LogoUrl
		case KindContact:
			c, err := q.GetContact(ctx, store.GetContactParams{WorkspaceID: a.WorkspaceID, ID: id})
			if err != nil {
				return notFound("contact", err)
			}
			src = c.PhotoUrl
		default:
			return ErrNotFound
		}
		if src == "" {
			return ErrNotFound
		}
		row, err := q.GetAvatar(ctx, store.GetAvatarParams{WorkspaceID: a.WorkspaceID, Url: src})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		cached, have = row, err == nil
		return err
	})
	if err != nil {
		return nil, "", err
	}
	if have {
		age := s.now().Sub(cached.FetchedAt)
		if cached.Data != nil && age < avatarFresh {
			return cached.Data, cached.ContentType, nil
		}
		if cached.Data == nil && age < avatarRetry {
			return nil, "", ErrNotFound
		}
	}
	data, ctype, ferr := fetchImage(ctx, s.img, src)
	if ferr != nil && have && cached.Data != nil {
		return cached.Data, cached.ContentType, nil // keep showing the last good one
	}
	if err := s.tx(ctx, a, PermRead, func(q *store.Queries) error {
		return q.SaveAvatar(ctx, store.SaveAvatarParams{WorkspaceID: a.WorkspaceID, Url: src, ContentType: ctype, Data: data})
	}); err != nil {
		s.log.Warn("avatar cache", "err", err)
	}
	if ferr != nil {
		s.log.Info("avatar fetch failed", "kind", kind, "id", id, "err", ferr)
		return nil, "", ErrNotFound
	}
	return data, ctype, nil
}

// fetchImage downloads src and checks, from its bytes, that it is a raster
// image no larger than avatarMaxBytes.
func fetchImage(ctx context.Context, hc *http.Client, src string) ([]byte, string, error) {
	// A caller-chosen URL by design; hc is a safehttp client (public https only).
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "image/png,image/jpeg,image/webp,image/gif")
	res, err := hc.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("status %d", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, avatarMaxBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) > avatarMaxBytes {
		return nil, "", errors.New("image too large")
	}
	ctype := http.DetectContentType(data)
	if !avatarTypes[ctype] {
		return nil, "", fmt.Errorf("not a PNG, JPEG, GIF or WebP image (%s)", ctype)
	}
	return data, ctype, nil
}
