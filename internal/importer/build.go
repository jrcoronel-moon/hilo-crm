package importer

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/jrcoronel-moon/hilo-crm/internal/crm"
)

// Options tune how cells become values.
type Options struct {
	// Source names the system the file comes from ("hubspot"). Ids in the
	// file become "<source>:<id>", so the same records can be loaded again
	// (or synced later by an agent) without duplicates.
	Source string
	// DateOrder resolves ambiguous dates such as 03/04/2025: "dmy" (default)
	// or "mdy". ISO dates (2025-04-03) are always understood.
	DateOrder string
	// Defaults are values for fields the file has no column for, e.g.
	// pipeline, currency or (for interactions) type. A column wins over it.
	Defaults map[string]string
	// Tags are added to every record, e.g. "importado-2026-10".
	Tags []string
}

// RowError is a cell the adapter could not convert; the row is skipped.
type RowError struct {
	Row   int    `json:"row"` // line in the file; the header is line 1
	Field string `json:"field,omitempty"`
	Error string `json:"error"`
}

// Result is a batch ready for crm.Import, plus where each record came from.
type Result struct {
	Input crm.ImportInput
	Rows  []int      // Rows[i] is the file line of the i-th record of the batch
	Skip  []RowError // rows left out, with the reason
}

// Build converts the table into records of the given kind. Rows with a cell
// that cannot be converted are reported in Skip and left out; everything else
// is validated later by crm.Import (dry run or for real).
func Build(kind string, t Table, m Mapping, o Options) Result {
	var res Result
	for i, cells := range t.Rows {
		row := i + 2
		r := record{kind: kind, row: row, vals: map[string]string{}, o: o}
		for j, target := range m {
			if j >= len(cells) || cells[j] == "" {
				continue
			}
			switch target {
			case "":
			case Custom:
				if r.custom == nil {
					r.custom = map[string]any{}
				}
				r.custom[strings.TrimPrefix(t.Header[j], customPrefix)] = cells[j]
			default:
				r.vals[target] = cells[j]
			}
		}
		for k, v := range o.Defaults {
			if r.vals[k] == "" && v != "" {
				r.vals[k] = v
			}
		}
		switch kind {
		case KindCompany:
			res.Input.Companies = append(res.Input.Companies, r.company())
		case KindContact:
			res.Input.Contacts = append(res.Input.Contacts, r.contact())
		case KindOpportunity:
			res.Input.Opportunities = append(res.Input.Opportunities, r.opportunity())
		case KindInteraction:
			res.Input.Interactions = append(res.Input.Interactions, r.interaction())
		}
		if r.err != nil {
			res.Skip = append(res.Skip, *r.err)
			res.drop(kind)
			continue
		}
		res.Rows = append(res.Rows, row)
	}
	return res
}

// drop removes the record just appended for a row that failed to convert.
func (r *Result) drop(kind string) {
	switch kind {
	case KindCompany:
		r.Input.Companies = r.Input.Companies[:len(r.Input.Companies)-1]
	case KindContact:
		r.Input.Contacts = r.Input.Contacts[:len(r.Input.Contacts)-1]
	case KindOpportunity:
		r.Input.Opportunities = r.Input.Opportunities[:len(r.Input.Opportunities)-1]
	case KindInteraction:
		r.Input.Interactions = r.Input.Interactions[:len(r.Input.Interactions)-1]
	}
}

// record is one row being converted; the first conversion error wins.
type record struct {
	kind   string
	row    int
	vals   map[string]string
	custom map[string]any
	o      Options
	err    *RowError
}

func (r *record) fail(field, msg string) {
	if r.err == nil {
		r.err = &RowError{Row: r.row, Field: field, Error: msg}
	}
}

// str returns the cell as an optional string (nil when empty).
func (r *record) str(key string) *string {
	v, ok := r.vals[key]
	if !ok || v == "" {
		return nil
	}
	return &v
}

func (r *record) plain(key string) string { return r.vals[key] }

// externalID prefixes the id with the source ("hubspot:123") unless the cell
// already carries one, like the "hilo:<id>" of an export.
func (r *record) externalID() *string {
	v := r.vals["external_id"]
	if v == "" {
		return nil
	}
	if r.o.Source != "" && !strings.Contains(v, ":") {
		v = r.o.Source + ":" + v
	}
	return &v
}

// ref prepares a reference to another record: ids, emails and domains pass
// through; anything else is an id from the source and gets its prefix.
func (r *record) ref(key string) *string {
	v := r.vals[key]
	if v == "" {
		return nil
	}
	if _, err := uuid.Parse(v); err != nil && r.o.Source != "" && !strings.ContainsAny(v, "@:") && !looksLikeDomain(v) {
		v = r.o.Source + ":" + v
	}
	return &v
}

func looksLikeDomain(v string) bool {
	return strings.Contains(v, ".") && !strings.ContainsAny(v, " /")
}

var tagSplit = regexp.MustCompile(`[,;|]`)

func (r *record) tags() *[]string {
	var out []string
	for _, t := range tagSplit.Split(r.vals["tags"], -1) {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	out = append(out, r.o.Tags...)
	if len(out) == 0 {
		return nil
	}
	return &out
}

func (r *record) customFields() *map[string]any {
	if r.custom == nil {
		return nil
	}
	return &r.custom
}

func (r *record) date(key string) *string {
	v := r.vals[key]
	if v == "" {
		return nil
	}
	s, err := ParseDate(v, r.o.DateOrder)
	if err != nil {
		r.fail(key, err.Error())
		return nil
	}
	return &s
}

func (r *record) amount() *float64 {
	v := r.vals["amount"]
	if v == "" {
		return nil
	}
	f, err := ParseAmount(v)
	if err != nil {
		r.fail("amount", fmt.Sprintf("%q is not an amount", v))
		return nil
	}
	return &f
}

func (r *record) integer(key string) *int {
	v := r.vals[key]
	if v == "" {
		return nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(strings.TrimRight(strings.TrimSpace(v), "%min")))
	if err != nil {
		r.fail(key, fmt.Sprintf("%q is not a whole number", v))
		return nil
	}
	return &n
}

// vocab translates common Spanish and English labels to Hilo's values;
// unknown ones pass through and crm reports them.
func vocab(table map[string]string, v string) string {
	if t, ok := table[normalize(v)]; ok {
		return t
	}
	return v
}

var (
	kinds = map[string]string{"deal": "deal", "negocio": "deal", "venta": "deal", "oportunidad": "deal", "project": "project", "proyecto": "project"}
	types = map[string]string{"call": "call", "llamada": "call", "phone call": "call", "phone": "call", "meeting": "meeting", "reunion": "meeting",
		"note": "note", "nota": "note", "task": "note", "tarea": "note", "email": "email", "correo": "email", "e mail": "email", "mail": "email",
		"message": "message", "mensaje": "message", "whatsapp": "message", "sms": "message", "chat": "message", "social": "social", "linkedin": "social",
		"form": "form", "formulario": "form"}
	directions = map[string]string{"inbound": "inbound", "entrante": "inbound", "incoming": "inbound", "recibido": "inbound",
		"outbound": "outbound", "saliente": "outbound", "outgoing": "outbound", "enviado": "outbound", "internal": "internal", "interno": "internal", "interna": "internal"}
)

func (r *record) company() crm.CompanyInput {
	return crm.CompanyInput{ExternalID: r.externalID(), Name: r.str("name"), Domain: r.str("domain"), Industry: r.str("industry"),
		SizeRange: r.str("size_range"), Country: r.str("country"), Website: r.str("website"), Description: r.str("description"),
		Owner: r.str("owner"), SourceChannel: r.str("source_channel"), Tags: r.tags(), Custom: r.customFields()}
}

func (r *record) contact() crm.ContactInput {
	first, last := r.str("first_name"), r.str("last_name")
	if first == nil && last == nil && r.vals["name"] != "" {
		f, l, _ := strings.Cut(r.vals["name"], " ")
		first = &f
		if l = strings.TrimSpace(l); l != "" {
			last = &l
		}
	}
	return crm.ContactInput{ExternalID: r.externalID(), Company: r.ref("company"), FirstName: first, LastName: last, Email: r.str("email"),
		Phone: r.str("phone"), Title: r.str("title"), LinkedInURL: r.str("linkedin_url"), Owner: r.str("owner"),
		SourceChannel: r.str("source_channel"), Tags: r.tags(), Custom: r.customFields()}
}

func (r *record) opportunity() crm.OpportunityInput {
	var kind *string
	if v := r.vals["kind"]; v != "" {
		k := vocab(kinds, v)
		kind = &k
	}
	return crm.OpportunityInput{ExternalID: r.externalID(), Title: r.str("title"), Kind: kind, Company: r.ref("company"),
		PrimaryContact: r.ref("primary_contact"), Pipeline: r.str("pipeline"), Stage: r.str("stage"), LostReason: r.str("lost_reason"),
		Amount: r.amount(), Currency: r.str("currency"), Probability: r.integer("probability"), ExpectedCloseDate: r.date("expected_close_date"),
		SourceChannel: r.str("source_channel"), Owner: r.str("owner"), NextStep: r.str("next_step"), NextStepAt: r.date("next_step_at"),
		Description: r.str("description"), Tags: r.tags(), Custom: r.customFields(), CreatedAt: r.date("created_at"), ClosedAt: r.date("closed_at")}
}

func (r *record) interaction() crm.InteractionInput {
	deref := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	return crm.InteractionInput{ExternalID: deref(r.externalID()), Type: vocab(types, r.plain("type")), Direction: vocab(directions, r.plain("direction")),
		Channel: r.plain("channel"), Subject: r.plain("subject"), Summary: r.plain("summary"), OccurredAt: deref(r.date("occurred_at")),
		DurationMinutes: r.integer("duration_minutes"), Company: deref(r.ref("company")), Contact: deref(r.ref("contact")), Opportunity: deref(r.ref("opportunity"))}
}

// ParseDate normalises a date or timestamp to what crm accepts: YYYY-MM-DD
// for dates, RFC 3339 for timestamps (ones without a zone are taken as UTC).
// Besides ISO forms it reads 3/4/2025, 03-04-2025 and 03.04.25, resolving
// the day/month order with order ("dmy" by default, or "mdy") when both
// readings are possible, with an optional time after a space.
func ParseDate(v, order string) (string, error) {
	v = strings.TrimSpace(v)
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02 15:04", "2006/01/02 15:04:05", "2006/01/02 15:04"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t.UTC().Format(time.RFC3339), nil
		}
	}
	for _, layout := range []string{"2006-01-02", "2006/01/02", "2006.01.02"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t.Format(time.DateOnly), nil
		}
	}
	datePart, timePart, _ := strings.Cut(v, " ")
	parts := strings.FieldsFunc(datePart, func(r rune) bool { return r == '/' || r == '-' || r == '.' })
	if len(parts) != 3 {
		return "", fmt.Errorf("%q is not a date", v)
	}
	a, errA := strconv.Atoi(parts[0])
	b, errB := strconv.Atoi(parts[1])
	y, errY := strconv.Atoi(parts[2])
	if errA != nil || errB != nil || errY != nil {
		return "", fmt.Errorf("%q is not a date", v)
	}
	if len(parts[2]) == 2 {
		y += 2000
	}
	day, month := a, b // dmy
	switch {
	case a > 12 && b > 12:
		return "", fmt.Errorf("%q is not a date", v)
	case a > 12: // only one reading is possible
	case b > 12 || order == "mdy":
		day, month = b, a
	}
	t := time.Date(y, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	if t.Day() != day || t.Month() != time.Month(month) || y < 1900 || y > 2200 {
		return "", fmt.Errorf("%q is not a date", v)
	}
	if timePart == "" {
		return t.Format(time.DateOnly), nil
	}
	for _, layout := range []string{"15:04:05", "15:04"} {
		if tt, err := time.Parse(layout, strings.TrimSpace(timePart)); err == nil {
			return t.Add(time.Duration(tt.Hour())*time.Hour + time.Duration(tt.Minute())*time.Minute + time.Duration(tt.Second())*time.Second).Format(time.RFC3339), nil
		}
	}
	return "", fmt.Errorf("%q is not a date", v)
}

var notNumber = regexp.MustCompile(`[^0-9.,-]`)

// ParseAmount reads "1.500,50", "1,500.50", "$ 1500", "USD 1500" and "1500"
// as the same number.
func ParseAmount(v string) (float64, error) {
	s := notNumber.ReplaceAllString(v, "")
	dot, comma := strings.LastIndex(s, "."), strings.LastIndex(s, ",")
	switch {
	case dot >= 0 && comma >= 0 && comma > dot: // 1.500,50
		s = strings.ReplaceAll(s, ".", "")
		s = strings.Replace(s, ",", ".", 1)
	case dot >= 0 && comma >= 0: // 1,500.50
		s = strings.ReplaceAll(s, ",", "")
	case comma >= 0: // 1500,50 or 1,500 (treated as a decimal comma)
		s = strings.Replace(s, ",", ".", 1)
	}
	if s == "" {
		return 0, fmt.Errorf("%q is not an amount", v)
	}
	return strconv.ParseFloat(s, 64)
}

// Summary joins the adapter's skipped rows with the outcome of crm.Import,
// by file line, for people and scripts.
type Summary struct {
	DryRun  bool       `json:"dry_run"`
	Rows    int        `json:"rows"`    // data rows in the file
	OK      int        `json:"ok"`      // records saved (or valid, in a dry run)
	Created int        `json:"created"` // of those, new records
	Updated int        `json:"updated"` // of those, existing records
	Failed  int        `json:"failed"`  // rejected by crm (the row says why)
	Skipped int        `json:"skipped"` // not converted by the adapter
	Errors  []RowError `json:"errors"`
}

// Summarize builds the Summary of a Build followed by crm.Import.
func Summarize(rows int, b Result, r crm.ImportResult) Summary {
	s := Summary{DryRun: r.DryRun, Rows: rows, OK: r.OK, Failed: r.Failed, Skipped: len(b.Skip), Errors: append([]RowError{}, b.Skip...)}
	for _, it := range r.Items {
		if it.Created {
			s.Created++
		} else if it.OK {
			s.Updated++
		}
		if !it.OK && it.Index < len(b.Rows) {
			s.Errors = append(s.Errors, RowError{Row: b.Rows[it.Index], Error: it.Error})
		}
	}
	sort.Slice(s.Errors, func(i, j int) bool { return s.Errors[i].Row < s.Errors[j].Row })
	return s
}
