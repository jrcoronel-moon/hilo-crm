package importer

import (
	"strings"
	"testing"

	"github.com/jrcoronel-moon/hilo-crm/internal/crm"
)

func TestParseDetectsDelimiterBOMAndBlankRows(t *testing.T) {
	in := "\uFEFFNombre;Email;Notas\nAna;ana@acme.test;\"dijo; hola\"\n;;\nBob;bob@acme.test\n"
	tb, err := Parse(strings.NewReader(in), 1<<20, 100)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(tb.Header, "|") != "Nombre|Email|Notas" || len(tb.Rows) != 2 {
		t.Fatalf("%+v", tb)
	}
	if tb.Rows[0][2] != "dijo; hola" || tb.Rows[1][2] != "" {
		t.Fatalf("cells: %+v", tb.Rows)
	}
	if _, err := Parse(strings.NewReader(in), 10, 100); err == nil || !strings.Contains(err.Error(), "larger") {
		t.Fatalf("size limit: %v", err)
	}
	if _, err := Parse(strings.NewReader(in), 1<<20, 1); err == nil || !strings.Contains(err.Error(), "rows") {
		t.Fatalf("row limit: %v", err)
	}
	if _, err := Parse(strings.NewReader("a,b\n"), 1<<20, 10); err == nil {
		t.Fatal("header only must fail")
	}
	tb, err = Parse(strings.NewReader("a,,a\n1,2,3\n"), 1<<20, 10)
	if err != nil || strings.Join(tb.Header, "|") != "a|columna 2|a (2)" {
		t.Fatalf("header naming: %+v %v", tb.Header, err)
	}
}

func TestGuessAndValidate(t *testing.T) {
	header := []string{"Record ID", "Deal Name", "Deal Stage", "Amount", "Close Date", "Correo del contacto", "Propietario", "Deal Stage"}
	m := Guess(KindOpportunity, header)
	want := Mapping{"external_id", "title", "stage", "amount", "expected_close_date", "", "owner", ""}
	if strings.Join(m, ",") != strings.Join(want, ",") {
		t.Fatalf("guess: %v", m)
	}
	if err := Validate(KindOpportunity, header, m, nil); err != nil {
		t.Fatal(err)
	}
	m[7] = "stage"
	if err := Validate(KindOpportunity, header, m, nil); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("duplicate: %v", err)
	}
	m[7], m[1] = "", ""
	if err := Validate(KindOpportunity, header, m, nil); err == nil || !strings.Contains(err.Error(), "title") {
		t.Fatalf("required: %v", err)
	}
	if err := Validate(KindOpportunity, header, m, map[string]string{"title": "x"}); err != nil {
		t.Fatalf("default satisfies required: %v", err)
	}
	m[5] = "custom"
	if err := Validate(KindOpportunity, header, m, map[string]string{"title": "x", "bogus": "1"}); err == nil {
		t.Fatal("unknown default accepted")
	}
	if err := Validate(KindInteraction, []string{"a"}, Mapping{"custom"}, map[string]string{"type": "note"}); err == nil {
		t.Fatal("interactions have no custom fields")
	}
	if err := Validate(KindContact, []string{"Nombre completo"}, Mapping{"name"}, nil); err != nil {
		t.Fatalf("any of the contact fields is enough: %v", err)
	}
	if c := Guess(KindContact, []string{"E-Mail", "Teléfono", "Empresa"}); strings.Join(c, ",") != "email,phone,company" {
		t.Fatalf("contact guess: %v", c)
	}
}

func TestBuildOpportunities(t *testing.T) {
	tb := Table{
		Header: []string{"Id", "Título", "Empresa", "Etapa", "Monto", "Cierre", "Creada", "Tags", "Región", "Tipo"},
		Rows: [][]string{
			{"42", "Planta solar", "7", "Ganado", "$ 1.500,50", "03/04/2025", "2025-01-10 09:30", "grande; urgente", "Norte", "Proyecto"},
			{"43", "Sin fecha", "globex.com", "Nuevo", "abc", "", "", "", "", ""},
			{"44", "Fecha rara", "", "Nuevo", "", "31/02/2025", "", "", "", ""},
			{"hilo:0199b7fa-0000-7000-8000-000000000001", "Exportada", "", "Nuevo", "1,500.50", "2025-12-01", "", "", "", ""},
		},
	}
	m := Guess(KindOpportunity, tb.Header)
	m[8] = Custom
	res := Build(KindOpportunity, tb, m, Options{Source: "hs", DateOrder: "dmy", Defaults: map[string]string{"pipeline": "Ventas", "currency": "USD"}, Tags: []string{"importado"}})
	if len(res.Input.Opportunities) != 2 || len(res.Skip) != 2 || len(res.Rows) != 2 {
		t.Fatalf("%+v", res)
	}
	o := res.Input.Opportunities[0]
	if *o.ExternalID != "hs:42" || *o.Company != "hs:7" || *o.Amount != 1500.5 || *o.ExpectedCloseDate != "2025-04-03" ||
		*o.CreatedAt != "2025-01-10T09:30:00Z" || *o.Pipeline != "Ventas" || *o.Currency != "USD" || *o.Kind != "project" ||
		strings.Join(*o.Tags, ",") != "grande,urgente,importado" || (*o.Custom)["Región"] != "Norte" || res.Rows[0] != 2 {
		t.Fatalf("row 2: %+v", o)
	}
	x := res.Input.Opportunities[1]
	if *x.ExternalID != "hilo:0199b7fa-0000-7000-8000-000000000001" || *x.Amount != 1500.5 || res.Rows[1] != 5 {
		t.Fatalf("row 5: %+v", x)
	}
	if res.Skip[0].Row != 3 || res.Skip[0].Field != "amount" || res.Skip[1].Row != 4 || !strings.Contains(res.Skip[1].Error, "date") {
		t.Fatalf("skipped: %+v", res.Skip)
	}
	// A domain reference is left alone even with a source prefix.
	res = Build(KindOpportunity, Table{Header: tb.Header, Rows: tb.Rows[1:2]}, m, Options{Source: "hs"})
	if len(res.Skip) != 1 { // the amount
		t.Fatalf("%+v", res)
	}
	res = Build(KindContact, Table{Header: []string{"Name", "Company", "Email"}, Rows: [][]string{{"Ana María López", "globex.com", "ANA@Globex.com"}, {"Solo", "", ""}}},
		Guess(KindContact, []string{"Name", "Company", "Email"}), Options{Source: "hs"})
	c := res.Input.Contacts
	if len(c) != 2 || *c[0].FirstName != "Ana" || *c[0].LastName != "María López" || *c[0].Company != "globex.com" || c[1].LastName != nil {
		t.Fatalf("contacts: %+v %+v", c[0], c[1])
	}
}

func TestBuildInteractions(t *testing.T) {
	tb := Table{Header: []string{"Tipo", "Fecha", "Contacto", "Notas", "Duración", "Dirección"},
		Rows: [][]string{{"Llamada", "04/03/2025 15:00", "ana@acme.test", "Cierre", "45 min", "saliente"}, {"", "2025-03-04", "ana@acme.test", "Sin tipo", "", ""}}}
	m := Guess(KindInteraction, tb.Header)
	res := Build(KindInteraction, tb, m, Options{DateOrder: "mdy", Defaults: map[string]string{"type": "note"}})
	if len(res.Input.Interactions) != 2 || len(res.Skip) != 0 {
		t.Fatalf("%+v", res)
	}
	i := res.Input.Interactions[0]
	if i.Type != "call" || i.OccurredAt != "2025-04-03T15:00:00Z" || *i.DurationMinutes != 45 || i.Direction != "outbound" || i.Contact != "ana@acme.test" {
		t.Fatalf("%+v", i)
	}
	if res.Input.Interactions[1].Type != "note" {
		t.Fatalf("default type: %+v", res.Input.Interactions[1])
	}
}

func TestParseDate(t *testing.T) {
	cases := map[string]string{
		"2025-04-03": "2025-04-03", "2025/04/03": "2025-04-03", "03/04/2025": "2025-04-03", "3-4-25": "2025-04-03", "13/04/2025": "2025-04-13",
		"2025-04-03T10:00:00Z": "2025-04-03T10:00:00Z", "2025-04-03 10:00": "2025-04-03T10:00:00Z", "03/04/2025 10:00:30": "2025-04-03T10:00:30Z",
		"2025-04-03T12:00:00-03:00": "2025-04-03T15:00:00Z",
	}
	for in, want := range cases {
		if got, err := ParseDate(in, ""); err != nil || got != want {
			t.Errorf("%q: got %q %v want %q", in, got, err, want)
		}
	}
	if got, _ := ParseDate("03/04/2025", "mdy"); got != "2025-03-04" {
		t.Errorf("mdy: %q", got)
	}
	if got, _ := ParseDate("13/04/2025", "mdy"); got != "2025-04-13" {
		t.Errorf("mdy with an unambiguous day: %q", got)
	}
	for _, bad := range []string{"", "ayer", "13/13/2025", "31/02/2025", "2025-04-03 25:00", "1/2"} {
		if _, err := ParseDate(bad, ""); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestParseAmount(t *testing.T) {
	for in, want := range map[string]float64{"1500": 1500, "1.500,50": 1500.5, "1,500.50": 1500.5, "$ 1500": 1500, "USD 12,5": 12.5, "-3": -3, "€1.200": 1.2} {
		if got, err := ParseAmount(in); err != nil || got != want {
			t.Errorf("%q: got %v %v want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "abc", "1.2.3"} {
		if _, err := ParseAmount(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestSummarize(t *testing.T) {
	b := Result{Rows: []int{2, 4}, Skip: []RowError{{Row: 3, Field: "amount", Error: "bad"}}}
	r := crm.ImportResult{DryRun: true, OK: 1, Failed: 1, Items: []crm.ImportItem{{Index: 0, OK: true, Created: true}, {Index: 1, Error: "title: required"}}}
	s := Summarize(3, b, r)
	if !s.DryRun || s.Rows != 3 || s.OK != 1 || s.Created != 1 || s.Updated != 0 || s.Failed != 1 || s.Skipped != 1 || len(s.Errors) != 2 || s.Errors[0].Row != 3 || s.Errors[1].Row != 4 {
		t.Fatalf("%+v", s)
	}
}

// A Hilo export reads back as it was written: the apostrophe that neutralises
// a formula goes away, and "custom:<key>" columns are custom fields.
func TestReadsHiloExport(t *testing.T) {
	tb, err := Parse(strings.NewReader("email,phone,custom:segment,note\nana@x.test,'+54 11 5555,'=SUM(A1),'quoted\n"), 1<<20, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(tb.Rows[0], "|"); got != "ana@x.test|+54 11 5555|=SUM(A1)|'quoted" {
		t.Fatalf("cells: %q", got)
	}
	m := Guess(KindContact, tb.Header)
	if strings.Join(m, ",") != "email,phone,custom," {
		t.Fatalf("mapping: %q", m)
	}
	if got := Guess(KindInteraction, []string{"custom:x"}); got[0] != "" {
		t.Fatalf("interactions have no custom fields: %q", got)
	}
	res := Build(KindContact, tb, m, Options{})
	if c := res.Input.Contacts[0]; c.Custom == nil || (*c.Custom)["segment"] != "=SUM(A1)" || *c.Phone != "+54 11 5555" {
		t.Fatalf("contact: %+v", c)
	}
}
