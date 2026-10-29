package crm

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/google/uuid"

	"github.com/jrcoronel-moon/hilo-crm/internal/store"
)

// patcher applies optional input fields onto a stored row, remembering the
// first validation error so call sites stay a flat list of assignments.
// Semantics shared by every upsert: a nil field leaves the value unchanged; an
// empty string clears it.
type patcher struct {
	err error
}

func (p *patcher) set(err error) {
	if p.err == nil && err != nil {
		p.err = err
	}
}

func (p *patcher) line(dst *string, field string, v *string, maxLen int) {
	if v == nil || p.err != nil {
		return
	}
	s, err := cleanLine(field, *v, maxLen)
	p.set(err)
	*dst = s
}

func (p *patcher) text(dst *string, field string, v *string, maxLen int) {
	if v == nil || p.err != nil {
		return
	}
	s, err := cleanText(field, *v, maxLen)
	p.set(err)
	*dst = s
}

// externalID sets the nullable external_id column ("" → NULL).
func (p *patcher) externalID(dst **string, v *string) {
	if v == nil || p.err != nil {
		return
	}
	s, err := cleanLine("external_id", *v, 200)
	p.set(err)
	if s == "" {
		*dst = nil
		return
	}
	*dst = &s
}

func (p *patcher) with(dst *string, v *string, fn func(string) (string, error)) {
	if v == nil || p.err != nil {
		return
	}
	s, err := fn(*v)
	p.set(err)
	*dst = s
}

func (p *patcher) tags(dst *[]string, field string, v *[]string) {
	if v == nil || p.err != nil {
		return
	}
	t, err := cleanTags(field, *v)
	p.set(err)
	*dst = t
}

func (p *patcher) custom(dst *json.RawMessage, field string, v *map[string]any) {
	if v == nil || p.err != nil {
		return
	}
	c, err := cleanCustom(field, *v)
	p.set(err)
	*dst = c
}

// ref resolves an optional reference ("" clears it) with resolve.
func (p *patcher) ref(dst **uuid.UUID, v *string, resolve func(string) (uuid.UUID, error)) {
	if v == nil || p.err != nil {
		return
	}
	if strings.TrimSpace(*v) == "" {
		*dst = nil
		return
	}
	id, err := resolve(*v)
	p.set(err)
	if err == nil {
		*dst = &id
	}
}

// findRecord locates the record an upsert refers to: by id (must exist), then
// external_id, then an optional natural key (domain, email). It returns nil
// when nothing matches, meaning "create".
func findRecord[T any](what string, id, externalID *string,
	byID func(uuid.UUID) (T, error), byExternal func(*string) (T, error), byNatural func() (T, bool, error),
) (*T, error) {
	if id != nil && *id != "" {
		uid, err := parseID("id", *id)
		if err != nil {
			return nil, err
		}
		r, err := byID(uid)
		if err != nil {
			return nil, notFound(what, err)
		}
		return &r, nil
	}
	if externalID != nil && *externalID != "" {
		r, err := byExternal(externalID)
		if err == nil {
			return &r, nil
		} else if !isNoRows(err) {
			return nil, err
		}
	}
	if byNatural == nil {
		return nil, nil
	}
	r, ok, err := byNatural()
	switch {
	case isNoRows(err):
		return nil, nil // no match: the caller creates
	case err != nil:
		return nil, err
	case !ok:
		return nil, nil
	}
	return &r, nil
}

// naturalKey adapts a "normalize then look up" pair for findRecord.
func naturalKey[T any](v *string, normalize func(string) (string, error), get func(*string) (T, error)) func() (T, bool, error) {
	return func() (T, bool, error) {
		var zero T
		if v == nil || *v == "" {
			return zero, false, nil
		}
		k, err := normalize(*v)
		if err != nil || k == "" {
			return zero, false, err
		}
		r, err := get(&k)
		return r, err == nil, err
	}
}

// newID returns a time-ordered id for a row about to be created.
func newID() uuid.UUID {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.New()
	}
	return id
}

// resolvers bundles reference lookups for one transaction.
type resolvers struct {
	ctx context.Context
	q   *store.Queries
	ws  uuid.UUID
}

func byIDOr(field, ref string, byID func(uuid.UUID) error, byKey func(string) (uuid.UUID, error)) (uuid.UUID, error) {
	ref = strings.TrimSpace(ref)
	if id, err := uuid.Parse(ref); err == nil {
		if err := byID(id); err != nil {
			return uuid.Nil, notFound(field, err)
		}
		return id, nil
	}
	id, err := byKey(ref)
	return id, notFound(field, err)
}

// owner accepts a user id or email; the user must be a workspace member.
func (r resolvers) owner(ref string) (uuid.UUID, error) {
	email := strings.ToLower(strings.TrimSpace(ref))
	if id, err := uuid.Parse(email); err == nil {
		if _, err := r.q.GetMembership(r.ctx, store.GetMembershipParams{WorkspaceID: r.ws, UserID: id}); err != nil {
			return uuid.Nil, notFound("owner", err)
		}
		return id, nil
	}
	m, err := r.q.GetMemberByEmail(r.ctx, store.GetMemberByEmailParams{WorkspaceID: r.ws, Email: email})
	if err != nil {
		return uuid.Nil, notFound("owner (must be a workspace member, by id or email)", err)
	}
	return m.ID, nil
}

// channel accepts a channel id or name.
func (r resolvers) channel(ref string) (uuid.UUID, error) {
	return byIDOr("channel", ref,
		func(id uuid.UUID) error {
			_, err := r.q.GetChannel(r.ctx, store.GetChannelParams{WorkspaceID: r.ws, ID: id})
			return err
		},
		func(name string) (uuid.UUID, error) {
			c, err := r.q.GetChannelByName(r.ctx, store.GetChannelByNameParams{WorkspaceID: r.ws, Name: name})
			return c.ID, err
		})
}

// company accepts a company id or domain.
func (r resolvers) company(ref string) (uuid.UUID, error) {
	return byIDOr("company", ref,
		func(id uuid.UUID) error {
			_, err := r.q.GetCompany(r.ctx, store.GetCompanyParams{WorkspaceID: r.ws, ID: id})
			return err
		},
		func(key string) (uuid.UUID, error) {
			d, err := NormalizeDomain("company", key)
			if err != nil || d == "" {
				return uuid.Nil, invalid("company", "must be a company id or domain")
			}
			c, err := r.q.GetCompanyByDomain(r.ctx, store.GetCompanyByDomainParams{WorkspaceID: r.ws, Domain: &d})
			return c.ID, err
		})
}

// contact accepts a contact id or email.
func (r resolvers) contact(ref string) (uuid.UUID, error) {
	return byIDOr("contact", ref,
		func(id uuid.UUID) error {
			_, err := r.q.GetContact(r.ctx, store.GetContactParams{WorkspaceID: r.ws, ID: id})
			return err
		},
		func(key string) (uuid.UUID, error) {
			e, err := NormalizeEmail("contact", key)
			if err != nil || e == "" {
				return uuid.Nil, invalid("contact", "must be a contact id or email")
			}
			c, err := r.q.GetContactByEmail(r.ctx, store.GetContactByEmailParams{WorkspaceID: r.ws, Email: &e})
			return c.ID, err
		})
}
