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

// links replaces the stored links; an empty list clears them.
func (p *patcher) links(dst *json.RawMessage, field string, v *[]Link) {
	if v == nil || p.err != nil {
		return
	}
	b, err := cleanLinks(field, *v)
	p.set(err)
	*dst = b
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

// hiloID recognises the external_id hiloctl export gives records that had
// none ("hilo:<id>"), so a re-import into the same workspace finds them.
func hiloID(ext string) (uuid.UUID, bool) {
	rest, ok := strings.CutPrefix(ext, "hilo:")
	if !ok {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(rest)
	return id, err == nil
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
		if uid, ok := hiloID(*externalID); ok { // exported from this workspace
			if r, err := byID(uid); err == nil {
				return &r, nil
			}
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

// refKey splits a reference into the one column it can match: an id, a
// natural key (when normalize accepts it: a domain, an email) or an external_id.
type refKey struct {
	id            *uuid.UUID
	key, external *string
}

func splitRef(field, ref string, normalize func(string, string) (string, error)) refKey {
	ref = strings.TrimSpace(ref)
	if id, err := uuid.Parse(ref); err == nil {
		return refKey{id: &id}
	}
	if n, err := normalize(field, ref); err == nil && n != "" {
		return refKey{key: &n}
	}
	if id, ok := hiloID(ref); ok {
		return refKey{id: &id, external: &ref} // the record exported from here, or its copy elsewhere
	}
	return refKey{external: &ref}
}

// company accepts a company id, domain or external_id ("hubspot:123").
func (r resolvers) company(ref string) (uuid.UUID, error) {
	k := splitRef("company", ref, NormalizeDomain)
	id, err := r.q.ResolveCompanyRef(r.ctx, store.ResolveCompanyRefParams{WorkspaceID: r.ws, RefID: k.id, RefKey: k.key, RefExternal: k.external})
	return id, notFound("company", err)
}

// contact accepts a contact id, email or external_id.
func (r resolvers) contact(ref string) (uuid.UUID, error) {
	k := splitRef("contact", ref, NormalizeEmail)
	id, err := r.q.ResolveContactRef(r.ctx, store.ResolveContactRefParams{WorkspaceID: r.ws, RefID: k.id, RefKey: k.key, RefExternal: k.external})
	return id, notFound("contact", err)
}

// opportunity accepts an opportunity id or external_id.
func (r resolvers) opportunity(ref string) (uuid.UUID, error) {
	return byIDOr("opportunity", ref,
		func(id uuid.UUID) error {
			_, err := r.q.GetOpportunity(r.ctx, store.GetOpportunityParams{WorkspaceID: r.ws, ID: id})
			return err
		},
		func(key string) (uuid.UUID, error) {
			o, err := r.q.GetOpportunityByExternalID(r.ctx, store.GetOpportunityByExternalIDParams{WorkspaceID: r.ws, ExternalID: &key})
			if id, ok := hiloID(key); ok && isNoRows(err) {
				o, err = r.q.GetOpportunity(r.ctx, store.GetOpportunityParams{WorkspaceID: r.ws, ID: id})
			}
			return o.ID, err
		})
}
