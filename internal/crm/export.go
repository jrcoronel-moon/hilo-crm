package crm

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/jrcoronel-moon/hilo-crm/internal/store"
)

// Export returns the whole workspace (live records) as an import batch, the
// shape of bulk_upsert and hiloctl import, so it can be loaded into another
// workspace or another Hilo: nobody is locked in. Records without an
// external_id get "hilo:<id>", and references between records use those ids.
// Pipelines and channels are referenced by name: the target workspace needs
// the same ones (hiloctl pipeline apply / channel add).
func (s *Service) Export(ctx context.Context, a Actor) (ImportInput, error) {
	var x exporter
	err := s.tx(ctx, a, PermAdmin, func(q *store.Queries) error {
		x = newExporter(ctx, q, a.WorkspaceID)
		return x.run(x.lookups, x.exportCompanies, x.exportContacts, x.exportOpportunities, x.exportInteractions)
	})
	return x.out, err
}

type exporter struct {
	ctx context.Context
	q   *store.Queries
	ws  uuid.UUID
	out ImportInput

	owners, channels          map[uuid.UUID]string // id → member email / channel name
	companies, contacts, opps map[uuid.UUID]string // id → external_id in the export
}

func newExporter(ctx context.Context, q *store.Queries, ws uuid.UUID) exporter {
	return exporter{ctx: ctx, q: q, ws: ws, owners: map[uuid.UUID]string{}, channels: map[uuid.UUID]string{},
		companies: map[uuid.UUID]string{}, contacts: map[uuid.UUID]string{}, opps: map[uuid.UUID]string{}}
}

// run takes the steps in order: later ones reference what earlier ones exported.
func (x *exporter) run(steps ...func() error) error {
	for _, step := range steps {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}

func exportID(ext *string, id uuid.UUID) string {
	if ext != nil && *ext != "" {
		return *ext
	}
	return "hilo:" + id.String()
}

// ref maps a reference through m; nil when unset or not exported (archived).
func ref(m map[uuid.UUID]string, id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	if v, ok := m[*id]; ok {
		return &v
	}
	return nil
}

// opt omits empty values: in an upsert an empty string would clear a field.
func opt(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func optTags(t []string) *[]string {
	if len(t) == 0 {
		return nil
	}
	return &t
}

func optLinks(raw json.RawMessage) *[]Link {
	l := linksOf(raw)
	if l == nil {
		return nil
	}
	return &l
}

func optCustom(raw json.RawMessage) *map[string]any {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil || len(m) == 0 {
		return nil
	}
	return &m
}

func optTime(t *time.Time) *string {
	if t == nil {
		return nil
	}
	v := t.UTC().Format(time.RFC3339)
	return &v
}

func (x *exporter) lookups() error {
	members, err := x.q.ListMembers(x.ctx, x.ws)
	if err != nil {
		return err
	}
	for _, m := range members {
		x.owners[m.ID] = m.Email
	}
	chans, err := x.q.ExportChannelNames(x.ctx, x.ws)
	for _, c := range chans {
		x.channels[c.ID] = c.Name
	}
	return err
}

func (x *exporter) exportCompanies() error {
	rows, err := x.q.ExportCompanies(x.ctx, x.ws)
	for _, c := range rows {
		id := exportID(c.ExternalID, c.ID)
		x.companies[c.ID] = id
		x.out.Companies = append(x.out.Companies, CompanyInput{ExternalID: &id, Name: &c.Name, Domain: c.Domain,
			Industry: opt(c.Industry), SizeRange: opt(c.SizeRange), Country: opt(c.Country), Website: opt(c.Website), LogoURL: opt(c.LogoUrl),
			Description: opt(c.Description), Owner: ref(x.owners, c.OwnerID), SourceChannel: ref(x.channels, c.SourceChannelID),
			Tags: optTags(c.Tags), Custom: optCustom(c.Custom)})
	}
	return err
}

func (x *exporter) exportContacts() error {
	rows, err := x.q.ExportContacts(x.ctx, x.ws)
	for _, c := range rows {
		id := exportID(c.ExternalID, c.ID)
		x.contacts[c.ID] = id
		x.out.Contacts = append(x.out.Contacts, ContactInput{ExternalID: &id, Email: c.Email, FirstName: opt(c.FirstName),
			LastName: opt(c.LastName), Phone: opt(c.Phone), Title: opt(c.Title), LinkedInURL: opt(c.LinkedinUrl), PhotoURL: opt(c.PhotoUrl),
			Company: ref(x.companies, c.CompanyID), Owner: ref(x.owners, c.OwnerID), SourceChannel: ref(x.channels, c.SourceChannelID),
			Tags: optTags(c.Tags), Custom: optCustom(c.Custom)})
	}
	return err
}

func (x *exporter) exportOpportunities() error {
	links, err := x.q.ExportOpportunityContacts(x.ctx, x.ws)
	if err != nil {
		return err
	}
	roles := map[uuid.UUID][]ContactRole{}
	for _, l := range links {
		if c, ok := x.contacts[l.ContactID]; ok {
			roles[l.OpportunityID] = append(roles[l.OpportunityID], ContactRole{Contact: c, Role: l.Role})
		}
	}
	for _, r := range roles {
		slices.SortFunc(r, func(a, b ContactRole) int { return strings.Compare(a.Contact, b.Contact) })
	}
	rows, err := x.q.ExportOpportunities(x.ctx, x.ws)
	for _, o := range rows {
		id := exportID(o.ExternalID, o.ID)
		x.opps[o.ID] = id
		amount := MinorToAmount(o.AmountMinor)
		in := OpportunityInput{ExternalID: &id, Title: &o.Title, Kind: &o.Kind, Pipeline: &o.PipelineName, Stage: &o.StageName,
			Company: ref(x.companies, o.CompanyID), PrimaryContact: ref(x.contacts, o.PrimaryContactID), Amount: &amount,
			Currency: &o.Currency, SourceChannel: ref(x.channels, o.SourceChannelID), Owner: ref(x.owners, o.OwnerID),
			NextStep: opt(o.NextStep), NextStepAt: optTime(o.NextStepAt), Description: opt(o.Description),
			LostReason: opt(o.LostReason), Tags: optTags(o.Tags), Custom: optCustom(o.Custom), Links: optLinks(o.Links), Contacts: roles[o.ID],
			CreatedAt: optTime(&o.CreatedAt), ClosedAt: optTime(o.ClosedAt)}
		if o.Probability != nil {
			p := int(*o.Probability)
			in.Probability = &p
		}
		if o.ExpectedCloseDate != nil {
			d := o.ExpectedCloseDate.Format(time.DateOnly)
			in.ExpectedCloseDate = &d
		}
		x.out.Opportunities = append(x.out.Opportunities, in)
	}
	return err
}

func (x *exporter) exportInteractions() error {
	rows, err := x.q.ExportInteractions(x.ctx, x.ws)
	for _, i := range rows {
		in := InteractionInput{ExternalID: exportID(i.ExternalID, i.ID), Type: i.Type, Direction: i.Direction,
			Subject: i.Subject, Summary: i.Summary, OccurredAt: i.OccurredAt.UTC().Format(time.RFC3339)}
		for dst, v := range map[*string]*string{&in.Channel: ref(x.channels, i.ChannelID), &in.Company: ref(x.companies, i.CompanyID),
			&in.Contact: ref(x.contacts, i.ContactID), &in.Opportunity: ref(x.opps, i.OpportunityID)} {
			if v != nil {
				*dst = *v
			}
		}
		if i.DurationMinutes != nil {
			d := int(*i.DurationMinutes)
			in.DurationMinutes = &d
		}
		x.out.Interactions = append(x.out.Interactions, in)
	}
	return err
}
