package crm

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/jrcoronel-moon/hilo-crm/internal/store"
)

// Record kinds of ExportTable: the CSV importer's names for them.
const (
	KindCompany     = "company"
	KindContact     = "contact"
	KindOpportunity = "opportunity"
)

// ExportKinds are the kinds ExportTable accepts, in the order to load them.
var ExportKinds = []string{KindCompany, KindContact, KindOpportunity}

// Table is one kind of record as rows of text, ready for a CSV file.
type Table struct {
	Kind    string     `json:"kind"`
	Columns []string   `json:"columns"`
	Rows    [][]string `json:"rows"`
}

// ExportTable returns every live company, contact or opportunity as rows
// whose columns are the CSV importer's field names, so the file loads back as
// it is (Ajustes → Importar, hiloctl import-csv) into another workspace:
// companies, then contacts, then opportunities. Ids are those of Export (the
// external_id, or "hilo:<id>" when the record has none); references use the
// company's domain and the contact's email when there is one, which also
// match records the other workspace already has. company_name,
// primary_contact_name and hilo_id are there for people and are ignored on
// import; custom fields are "custom:<key>" columns. Cells a spreadsheet would
// run as a formula start with an apostrophe (SafeCSV), which the importer
// removes. Admins only, like Export, and audited.
func (s *Service) ExportTable(ctx context.Context, a Actor, kind string) (Table, error) {
	var t Table
	err := s.tx(ctx, a, PermAdmin, func(q *store.Queries) error {
		x := newExporter(ctx, q, a.WorkspaceID)
		switch kind {
		case KindCompany:
			if err := x.run(x.lookups, x.exportCompanies); err != nil {
				return err
			}
			t = companyTable(&x)
		case KindContact:
			if err := x.run(x.lookups, x.exportCompanies, x.exportContacts); err != nil {
				return err
			}
			t = contactTable(&x)
		case KindOpportunity:
			if err := x.run(x.lookups, x.exportCompanies, x.exportContacts, x.exportOpportunities); err != nil {
				return err
			}
			t = opportunityTable(&x)
		default:
			return invalid("kind", "must be one of %s", strings.Join(ExportKinds, ", "))
		}
		return s.audit(ctx, q, a, kind+".export", kind, uuid.Nil, map[string]any{"rows": len(t.Rows)})
	})
	return t, err
}

func companyTable(x *exporter) Table {
	ids := invert(x.companies)
	return table(KindCompany, []string{"external_id", "name", "domain", "industry", "size_range", "country", "website", "logo_url", "description",
		"owner", "source_channel", "tags", "hilo_id"}, len(x.out.Companies), func(i int) ([]string, *map[string]any) {
		c := x.out.Companies[i]
		return []string{val(c.ExternalID), val(c.Name), val(c.Domain), val(c.Industry), val(c.SizeRange), val(c.Country), val(c.Website), val(c.LogoURL),
			val(c.Description), val(c.Owner), val(c.SourceChannel), tagList(c.Tags), ids[val(c.ExternalID)]}, c.Custom
	})
}

func contactTable(x *exporter) Table {
	ids, companies := invert(x.contacts), companyRefs(x)
	return table(KindContact, []string{"external_id", "first_name", "last_name", "email", "phone", "title", "linkedin_url", "photo_url",
		"company", "company_name", "owner", "source_channel", "tags", "hilo_id"}, len(x.out.Contacts), func(i int) ([]string, *map[string]any) {
		c := x.out.Contacts[i]
		co := companies[val(c.Company)]
		return []string{val(c.ExternalID), val(c.FirstName), val(c.LastName), val(c.Email), val(c.Phone), val(c.Title), val(c.LinkedInURL), val(c.PhotoURL),
			co.ref, co.name, val(c.Owner), val(c.SourceChannel), tagList(c.Tags), ids[val(c.ExternalID)]}, c.Custom
	})
}

func opportunityTable(x *exporter) Table {
	ids, companies, contacts := invert(x.opps), companyRefs(x), map[string]named{}
	for _, c := range x.out.Contacts {
		ref := val(c.Email)
		if ref == "" {
			ref = val(c.ExternalID)
		}
		contacts[val(c.ExternalID)] = named{ref: ref, name: strings.TrimSpace(val(c.FirstName) + " " + val(c.LastName))}
	}
	return table(KindOpportunity, []string{"external_id", "title", "kind", "pipeline", "stage", "amount", "currency", "probability",
		"expected_close_date", "company", "company_name", "primary_contact", "primary_contact_name", "owner", "source_channel",
		"next_step", "next_step_at", "description", "lost_reason", "tags", "created_at", "closed_at", "hilo_id"},
		len(x.out.Opportunities), func(i int) ([]string, *map[string]any) {
			o := x.out.Opportunities[i]
			amount, prob := "", ""
			if o.Amount != nil {
				amount = strconv.FormatFloat(*o.Amount, 'f', -1, 64)
			}
			if o.Probability != nil {
				prob = strconv.Itoa(*o.Probability)
			}
			co, pc := companies[val(o.Company)], contacts[val(o.PrimaryContact)]
			return []string{val(o.ExternalID), val(o.Title), val(o.Kind), val(o.Pipeline), val(o.Stage), amount, val(o.Currency), prob,
				val(o.ExpectedCloseDate), co.ref, co.name, pc.ref, pc.name, val(o.Owner), val(o.SourceChannel), val(o.NextStep),
				val(o.NextStepAt), val(o.Description), val(o.LostReason), tagList(o.Tags), val(o.CreatedAt), val(o.ClosedAt),
				ids[val(o.ExternalID)]}, o.Custom
		})
}

// named is a reference to a record in a file, with a name people can read.
type named struct{ ref, name string }

// companyRefs maps each exported company id to its domain (or the id when it
// has none) and its name.
func companyRefs(x *exporter) map[string]named {
	out := make(map[string]named, len(x.out.Companies))
	for _, c := range x.out.Companies {
		ref := val(c.Domain)
		if ref == "" {
			ref = val(c.ExternalID)
		}
		out[val(c.ExternalID)] = named{ref: ref, name: val(c.Name)}
	}
	return out
}

// table builds the rows and appends one "custom:<key>" column per custom
// field found in any record, sorted by key.
func table(kind string, cols []string, n int, row func(int) ([]string, *map[string]any)) Table {
	t := Table{Kind: kind, Rows: make([][]string, 0, n)}
	customs := make([]map[string]any, n)
	var keys []string
	for i := range n {
		r, custom := row(i)
		t.Rows = append(t.Rows, r)
		if custom != nil {
			customs[i] = *custom
			for k := range *custom {
				if !slices.Contains(keys, k) {
					keys = append(keys, k)
				}
			}
		}
	}
	slices.Sort(keys)
	t.Columns = cols
	for _, k := range keys {
		t.Columns = append(t.Columns, "custom:"+k)
	}
	for i := range t.Rows {
		for _, k := range keys {
			t.Rows[i] = append(t.Rows[i], cell(customs[i][k]))
		}
		t.Rows[i] = SafeCSV(t.Rows[i])
	}
	return t
}

// invert maps each exported id back to the record's Hilo id.
func invert(m map[uuid.UUID]string) map[string]string {
	out := make(map[string]string, len(m))
	for id, ext := range m {
		out[ext] = id.String()
	}
	return out
}

func val(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func tagList(t *[]string) string {
	if t == nil {
		return ""
	}
	return strings.Join(*t, ", ")
}

// cell writes a custom value: text as it is, anything else as JSON.
func cell(v any) string {
	switch v := v.(type) {
	case nil:
		return ""
	case string:
		return v
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

// SafeCSV prefixes cells that start with a formula trigger with an
// apostrophe, so a spreadsheet shows them as text (OWASP CSV injection
// guidance). The CSV importer removes it again.
func SafeCSV(row []string) []string {
	for i, c := range row {
		if c != "" && strings.ContainsRune("=+-@\t\r", rune(c[0])) {
			row[i] = "'" + c
		}
	}
	return row
}
