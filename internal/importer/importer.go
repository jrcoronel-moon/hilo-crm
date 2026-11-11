// Package importer turns a CSV file into an import batch (crm.ImportInput).
// It is the adapter behind Ajustes → Importar and hiloctl import-csv: it
// parses the file, guesses which column is which, normalises values (dates,
// amounts, tags, references) and hands the records to internal/crm, which
// creates them through the same upserts, rules and audit as any other write.
// Nothing here touches the database.
package importer

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"

	"github.com/jrcoronel-moon/hilo-crm/internal/i18n"
)

// Kinds of records a CSV can hold: one kind per file.
const (
	KindCompany     = "company"
	KindContact     = "contact"
	KindOpportunity = "opportunity"
	KindInteraction = "interaction"
)

// Kinds lists the record kinds in load order.
var Kinds = []string{KindCompany, KindContact, KindOpportunity, KindInteraction}

// Custom is the mapping target that stores a column under custom[<header>],
// without the customPrefix of a Hilo export ("custom:segment" → segment).
const (
	Custom       = "custom"
	customPrefix = "custom:"
)

// Table is a parsed CSV: the header and the data rows, all of the same width.
type Table struct {
	Header []string
	Rows   [][]string
}

// Parse reads a CSV (comma, semicolon or tab separated; UTF-8 with or without
// BOM) of at most maxBytes and maxRows data rows. Empty rows are skipped and
// short rows padded. Cells are data: nothing is ever evaluated.
func Parse(r io.Reader, maxBytes int64, maxRows int) (Table, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return Table{}, err
	}
	if int64(len(data)) > maxBytes {
		return Table{}, fmt.Errorf("the file is larger than %d KB", maxBytes>>10)
	}
	data = bytes.TrimPrefix(data, []byte("\uFEFF"))
	cr := csv.NewReader(bytes.NewReader(data))
	cr.Comma = delimiter(data)
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	cr.TrimLeadingSpace = true
	records, err := cr.ReadAll()
	if err != nil {
		return Table{}, fmt.Errorf("not a valid CSV: %w", err)
	}
	if len(records) == 0 {
		return Table{}, errors.New("the file is empty")
	}
	t := Table{Header: headers(records[0])}
	for _, rec := range records[1:] {
		row := make([]string, len(t.Header))
		empty := true
		for i := range row {
			if i < len(rec) {
				row[i] = unformula(strings.TrimSpace(rec[i]))
				empty = empty && row[i] == ""
			}
		}
		if empty {
			continue
		}
		t.Rows = append(t.Rows, row)
		if len(t.Rows) > maxRows {
			return Table{}, fmt.Errorf("more than %d rows", maxRows)
		}
	}
	if len(t.Rows) == 0 {
		return Table{}, errors.New("the file has a header but no rows")
	}
	return t, nil
}

// unformula removes the apostrophe that neutralises a formula in an export
// (crm.SafeCSV): "'+54 11 5555" is the phone "+54 11 5555".
func unformula(c string) string {
	if len(c) > 1 && c[0] == '\'' && strings.ContainsRune("=+-@", rune(c[1])) {
		return c[1:]
	}
	return c
}

// delimiter picks the separator that appears most in the first line.
func delimiter(data []byte) rune {
	line, _, _ := bytes.Cut(data, []byte("\n"))
	best, n := ',', bytes.Count(line, []byte(","))
	for _, c := range []rune{';', '\t'} {
		if k := bytes.Count(line, []byte(string(c))); k > n {
			best, n = c, k
		}
	}
	return best
}

// headers trims the header cells, names the blank ones and makes them unique
// (they become custom field keys).
func headers(cells []string) []string {
	out := make([]string, len(cells))
	seen := map[string]int{}
	for i, c := range cells {
		c = strings.TrimSpace(c)
		if c == "" {
			c = fmt.Sprintf("columna %d", i+1)
		}
		if k := seen[c]; k > 0 {
			c = fmt.Sprintf("%s (%d)", c, k+1)
		}
		seen[c]++
		out[i] = c
	}
	return out
}

// Field is a Hilo field a column can map to.
type Field struct {
	Key      string   // the JSON field of the crm input, e.g. "email"
	Label    string   // for the UI
	Aliases  []string // header names recognised when guessing, normalised
	Required bool     // one of the required fields must be mapped or set
}

var common = struct{ owner, channel, tags, external Field }{
	owner:    Field{Key: "owner", Label: i18n.M("Dueño (email)"), Aliases: []string{"owner", "dueno", "propietario", "responsable", "owner email", "assigned to", "asignado a"}},
	channel:  Field{Key: "source_channel", Label: i18n.M("Canal de origen"), Aliases: []string{"source", "origen", "canal", "channel", "lead source", "original source", "fuente"}},
	tags:     Field{Key: "tags", Label: i18n.M("Etiquetas"), Aliases: []string{"tags", "etiquetas", "labels"}},
	external: Field{Key: "external_id", Label: i18n.M("Id en el origen"), Aliases: []string{"id", "external id", "record id", "identificador"}},
}

var fields = map[string][]Field{
	KindCompany: {
		with(common.external, "company id", "id de empresa"),
		{Key: "name", Label: i18n.M("Nombre"), Required: true, Aliases: []string{"name", "company", "company name", "empresa", "nombre", "razon social", "cuenta", "account", "account name", "organization", "organizacion"}},
		{Key: "domain", Label: i18n.M("Dominio"), Aliases: []string{"domain", "dominio", "company domain name", "website domain"}},
		{Key: "industry", Label: i18n.M("Industria"), Aliases: []string{"industry", "industria", "sector", "rubro"}},
		{Key: "size_range", Label: i18n.M("Tamaño"), Aliases: []string{"size", "size range", "tamano", "employees", "empleados", "number of employees", "company size"}},
		{Key: "country", Label: i18n.M("País"), Aliases: []string{"country", "pais", "country/region"}},
		{Key: "website", Label: i18n.M("Sitio web"), Aliases: []string{"website", "web", "sitio web", "url", "website url"}},
		{Key: "logo_url", Label: i18n.M("Logo (URL)"), Aliases: []string{"logo", "logo url", "company logo", "imagen", "image"}},
		{Key: "description", Label: i18n.M("Descripción"), Aliases: []string{"description", "descripcion", "notes", "notas", "about"}},
		common.owner, common.channel, common.tags,
	},
	KindContact: {
		with(common.external, "contact id", "id de contacto"),
		{Key: "first_name", Label: i18n.M("Nombre"), Required: true, Aliases: []string{"first name", "nombre", "firstname", "given name"}},
		{Key: "last_name", Label: i18n.M("Apellido"), Required: true, Aliases: []string{"last name", "apellido", "apellidos", "lastname", "surname", "family name"}},
		{Key: "name", Label: i18n.M("Nombre completo"), Required: true, Aliases: []string{"name", "full name", "nombre completo", "contact", "contacto", "contact name"}},
		{Key: "email", Label: i18n.M("Email"), Required: true, Aliases: []string{"email", "e-mail", "correo", "correo electronico", "mail", "email address"}},
		{Key: "phone", Label: i18n.M("Teléfono"), Aliases: []string{"phone", "telefono", "phone number", "mobile", "celular", "movil", "tel"}},
		{Key: "title", Label: i18n.M("Cargo"), Aliases: []string{"title", "job title", "cargo", "puesto", "position", "role"}},
		{Key: "linkedin_url", Label: i18n.M("LinkedIn"), Aliases: []string{"linkedin", "linkedin url", "linkedin profile"}},
		{Key: "photo_url", Label: i18n.M("Foto (URL)"), Aliases: []string{"photo", "photo url", "foto", "avatar", "picture", "profile picture"}},
		{Key: "company", Label: i18n.M("Empresa (dominio o id)"), Aliases: []string{"company", "empresa", "company domain", "company id", "associated company", "account", "organization", "dominio de la empresa"}},
		common.owner, common.channel, common.tags,
	},
	KindOpportunity: {
		with(common.external, "deal id", "opportunity id"),
		{Key: "title", Label: i18n.M("Título"), Required: true, Aliases: []string{"title", "titulo", "name", "nombre", "deal name", "opportunity name", "deal", "oportunidad", "negocio", "subject"}},
		{Key: "kind", Label: i18n.M("Tipo (deal/project)"), Aliases: []string{"kind", "tipo", "type"}},
		{Key: "company", Label: i18n.M("Empresa (dominio o id)"), Aliases: []string{"company", "empresa", "account", "company domain", "company id", "associated company", "organization"}},
		{Key: "primary_contact", Label: i18n.M("Contacto (email o id)"), Aliases: []string{"contact", "contacto", "primary contact", "contact email", "person", "associated contact"}},
		{Key: "pipeline", Label: i18n.M("Pipeline"), Aliases: []string{"pipeline", "embudo"}},
		{Key: "stage", Label: i18n.M("Etapa"), Aliases: []string{"stage", "etapa", "deal stage", "status", "estado", "fase", "phase"}},
		{Key: "lost_reason", Label: i18n.M("Motivo de pérdida"), Aliases: []string{"lost reason", "motivo de perdida", "closed lost reason", "razon"}},
		{Key: "amount", Label: i18n.M("Monto"), Aliases: []string{"amount", "monto", "value", "valor", "importe", "deal value", "revenue"}},
		{Key: "currency", Label: i18n.M("Moneda"), Aliases: []string{"currency", "moneda", "deal currency", "currency code"}},
		{Key: "probability", Label: i18n.M("Probabilidad"), Aliases: []string{"probability", "probabilidad", "deal probability", "win probability"}},
		{Key: "expected_close_date", Label: i18n.M("Cierre esperado"), Aliases: []string{"close date", "expected close date", "fecha de cierre", "cierre", "cierre esperado", "fecha estimada de cierre", "closing date", "expected close"}},
		{Key: "next_step", Label: i18n.M("Próximo paso"), Aliases: []string{"next step", "proximo paso", "siguiente paso"}},
		{Key: "next_step_at", Label: i18n.M("Fecha del próximo paso"), Aliases: []string{"next step date", "next activity date", "fecha del proximo paso", "next activity"}},
		{Key: "description", Label: i18n.M("Descripción"), Aliases: []string{"description", "descripcion", "notes", "notas"}},
		{Key: "created_at", Label: i18n.M("Fecha de creación (histórica)"), Aliases: []string{"created", "created at", "create date", "creation date", "fecha de creacion", "creada", "add time", "created date"}},
		{Key: "closed_at", Label: i18n.M("Fecha de cierre real (histórica)"), Aliases: []string{"closed", "closed at", "close time", "won time", "lost time", "fecha de cierre real", "actual close date", "closed date"}},
		common.owner, common.channel, common.tags,
	},
	KindInteraction: {
		with(common.external, "activity id", "engagement id", "message id"),
		{Key: "type", Label: i18n.M("Tipo"), Required: true, Aliases: []string{"type", "tipo", "activity type", "kind"}},
		{Key: "direction", Label: i18n.M("Dirección"), Aliases: []string{"direction", "direccion", "sentido"}},
		{Key: "channel", Label: i18n.M("Canal"), Aliases: []string{"channel", "canal"}},
		{Key: "subject", Label: i18n.M("Asunto"), Aliases: []string{"subject", "asunto", "title", "titulo"}},
		{Key: "summary", Label: i18n.M("Resumen"), Aliases: []string{"summary", "resumen", "body", "notes", "notas", "description", "descripcion", "note", "content", "cuerpo"}},
		{Key: "occurred_at", Label: i18n.M("Fecha"), Aliases: []string{"date", "fecha", "occurred at", "activity date", "timestamp", "when", "datetime", "fecha y hora"}},
		{Key: "duration_minutes", Label: i18n.M("Duración (min)"), Aliases: []string{"duration", "duracion", "duration minutes", "minutes", "minutos"}},
		{Key: "company", Label: i18n.M("Empresa (dominio o id)"), Aliases: []string{"company", "empresa", "company domain", "company id"}},
		{Key: "contact", Label: i18n.M("Contacto (email o id)"), Aliases: []string{"contact", "contacto", "contact email", "person"}},
		{Key: "opportunity", Label: i18n.M("Oportunidad (id)"), Aliases: []string{"opportunity", "oportunidad", "deal", "negocio", "deal id", "opportunity id"}},
	},
}

func with(f Field, aliases ...string) Field {
	f.Aliases = append(append([]string{}, f.Aliases...), aliases...)
	return f
}

// Fields lists the fields of a kind, in display order; nil for an unknown kind.
func Fields(kind string) []Field { return fields[kind] }

// ValidKind reports whether kind is one of Kinds.
func ValidKind(kind string) bool { return fields[kind] != nil }

// Mapping is the target of each column: a field key, Custom, or "" to ignore.
type Mapping []string

// Guess maps the header to fields by name: "Email", "correo", "E-mail
// address" all map to email, and "custom:<key>" (a Hilo export) to a custom
// field. Columns it does not recognise stay unmapped.
func Guess(kind string, header []string) Mapping {
	m := make(Mapping, len(header))
	taken := map[string]bool{}
	for i, h := range header {
		if strings.HasPrefix(h, customPrefix) && kind != KindInteraction {
			m[i] = Custom
			continue
		}
		n := normalize(h)
		for _, f := range fields[kind] {
			if taken[f.Key] || (normalize(f.Key) != n && !contains(f.Aliases, n)) {
				continue
			}
			m[i], taken[f.Key] = f.Key, true
			break
		}
	}
	return m
}

// Validate checks a mapping against the header: known targets only, each
// field at most once, and the required field present (mapped or set as a
// default). Custom fields are not available for interactions.
func Validate(kind string, header []string, m Mapping, defaults map[string]string) error {
	if !ValidKind(kind) {
		return fmt.Errorf("unknown kind %q", kind)
	}
	if len(m) != len(header) {
		return fmt.Errorf("the mapping has %d entries for %d columns", len(m), len(header))
	}
	seen := map[string]bool{}
	for i, target := range m {
		switch {
		case target == "":
		case target == Custom:
			if kind == KindInteraction {
				return fmt.Errorf("column %q: interactions have no custom fields", header[i])
			}
		case field(kind, target) == nil:
			return fmt.Errorf("column %q: unknown field %q", header[i], target)
		case seen[target]:
			return fmt.Errorf("field %q is mapped twice", target)
		default:
			seen[target] = true
		}
	}
	for k := range defaults {
		if field(kind, k) == nil {
			return fmt.Errorf("unknown field %q in the defaults", k)
		}
	}
	var required []string
	for _, f := range fields[kind] {
		if f.Required {
			if seen[f.Key] || defaults[f.Key] != "" {
				return nil
			}
			required = append(required, f.Key)
		}
	}
	return fmt.Errorf("one of %s must be mapped", strings.Join(required, ", "))
}

func field(kind, key string) *Field {
	for i := range fields[kind] {
		if fields[kind][i].Key == key {
			return &fields[kind][i]
		}
	}
	return nil
}

// contains compares normalised forms, so "E-Mail" meets the alias "e-mail".
func contains(list []string, s string) bool {
	for _, v := range list {
		if normalize(v) == s {
			return true
		}
	}
	return false
}

var unaccent = transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)

// normalize lower-cases a header, drops accents and treats "_", "-" and
// repeated spaces as one space: "Correo_Electrónico" → "correo electronico".
func normalize(s string) string {
	if out, _, err := transform.String(unaccent, s); err == nil {
		s = out
	}
	s = strings.NewReplacer("_", " ", "-", " ").Replace(strings.ToLower(s))
	return strings.Join(strings.Fields(s), " ")
}
