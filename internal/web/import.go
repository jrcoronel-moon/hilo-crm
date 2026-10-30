package web

import (
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/jrcoronel-moon/hilo-crm/internal/crm"
	"github.com/jrcoronel-moon/hilo-crm/internal/httpx"
	"github.com/jrcoronel-moon/hilo-crm/internal/importer"
)

// Ajustes → Importar: upload a CSV, map its columns to Hilo fields, validate
// (a dry run through the same path as bulk_upsert) and load it. Admins only.
// The file never touches the disk: between steps it travels in a hidden field
// of the form, and every step re-validates everything. Bigger files go
// through hiloctl import-csv, which has no request limits.
const (
	importMaxCSV  = 700 << 10 // under the 1 MiB request limit, re-posted at each step
	importMaxRows = 3000      // keeps a dry run well inside the request timeout
	importMaxErrs = 200       // rows listed on the page
)

type importData struct {
	CSRF      string
	Kinds     []importKind
	Pipelines []crm.PipelineView
	Types     []string // interaction types, for the default
	MaxKB     int
	MaxRows   int

	// After the file is analysed:
	Kind      string
	Source    string
	DateOrder string
	Tags      string
	Defaults  map[string]string
	Fields    []importer.Field
	Columns   []importColumn
	CSV       string // the file, re-posted with the mapping
	Rows      int
	Summary   *importer.Summary
	More      int // errors beyond importMaxErrs
	Done      bool
}

type importKind struct{ Key, Label, List string }

var importKinds = []importKind{
	{importer.KindCompany, "Empresas", "/companies"},
	{importer.KindContact, "Contactos", "/contacts"},
	{importer.KindOpportunity, "Oportunidades", "/opportunities"},
	{importer.KindInteraction, "Interacciones", "/interactions"},
}

type importColumn struct {
	Index  int
	Name   string
	Sample []string
	Target string
}

// Label is the Spanish name of the chosen kind (template).
func (d importData) Label() string {
	for _, k := range d.Kinds {
		if k.Key == d.Kind {
			return k.Label
		}
	}
	return ""
}

// List is the page that shows the imported records (template).
func (d importData) List() string {
	for _, k := range d.Kinds {
		if k.Key == d.Kind {
			return k.List
		}
	}
	return "/board"
}

func (h *Handler) importPage(r *http.Request, v view) (importData, error) {
	pipes, err := h.crm.ListPipelines(r.Context(), v.Actor)
	return importData{CSRF: v.Sess.CSRFToken, Kinds: importKinds, Pipelines: pipes, Types: crm.InteractionTypes, MaxKB: importMaxCSV >> 10,
		MaxRows: importMaxRows, Kind: importer.KindContact, DateOrder: "dmy", Defaults: map[string]string{}}, err
}

// importForm is GET /settings/import: the upload form.
func (h *Handler) importForm(w http.ResponseWriter, r *http.Request, v view) {
	if !v.IsAdmin() {
		h.fail(w, r, crm.ErrForbidden)
		return
	}
	d, err := h.importPage(r, v)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.renderImport(w, v, d, "")
}

// importCSV handles every step: a fresh upload (multipart with "file") gets a
// guessed mapping and a dry run; a re-post of the hidden CSV with the mapping
// gets a dry run, or the real import when action=import.
func (h *Handler) importCSV(w http.ResponseWriter, r *http.Request, v view) {
	if !v.IsAdmin() {
		h.fail(w, r, crm.ErrForbidden)
		return
	}
	d, err := h.importPage(r, v)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if r.ContentLength > httpx.MaxBody {
		h.renderImport(w, v, d, "El archivo supera "+strconv.Itoa(importMaxCSV>>10)+" KB; para archivos grandes usá hiloctl import-csv.")
		return
	}
	if k := r.PostFormValue("kind"); importer.ValidKind(k) {
		d.Kind = k
	}
	d.Source = strings.TrimSpace(r.PostFormValue("source"))
	if r.PostFormValue("dates") == "mdy" {
		d.DateOrder = "mdy"
	}
	d.Tags = strings.TrimSpace(r.PostFormValue("tags"))
	for _, k := range []string{"pipeline", "currency", "type"} {
		if val := strings.TrimSpace(r.PostFormValue("default_" + k)); val != "" {
			d.Defaults[k] = val
		}
	}
	csvText, fresh, err := importFile(r)
	if err != nil {
		h.renderImport(w, v, d, err.Error())
		return
	}
	t, err := importer.Parse(strings.NewReader(csvText), importMaxCSV, importMaxRows)
	if err != nil {
		h.renderImport(w, v, d, "No se pudo leer el archivo: "+err.Error())
		return
	}
	d.CSV, d.Rows, d.Fields = csvText, len(t.Rows), importer.Fields(d.Kind)
	var m importer.Mapping
	if fresh {
		m = importer.Guess(d.Kind, t.Header)
	} else {
		m = make(importer.Mapping, len(t.Header))
		for i := range t.Header {
			m[i] = r.PostFormValue("col_" + strconv.Itoa(i))
		}
	}
	for i, name := range t.Header {
		col := importColumn{Index: i, Name: name, Target: m[i]}
		for _, row := range t.Rows[:min(3, len(t.Rows))] {
			if row[i] != "" {
				col.Sample = append(col.Sample, row[i])
			}
		}
		d.Columns = append(d.Columns, col)
	}
	if err := importer.Validate(d.Kind, t.Header, m, d.Defaults); err != nil {
		h.renderImport(w, v, d, "Revisá el mapeo: "+err.Error())
		return
	}
	opts := importer.Options{Source: d.Source, DateOrder: d.DateOrder, Defaults: d.Defaults, Tags: splitTags(d.Tags)}
	built := importer.Build(d.Kind, t, m, opts)
	built.Input.DryRun = r.PostFormValue("action") != "import"
	res := crm.ImportResult{DryRun: built.Input.DryRun}
	if len(built.Rows) > 0 { // otherwise every row was skipped by the adapter
		if res, err = h.crm.ImportFile(r.Context(), v.Actor, built.Input); err != nil {
			h.fail(w, r, err)
			return
		}
	}
	s := importer.Summarize(d.Rows, built, res)
	if len(s.Errors) > importMaxErrs {
		d.More, s.Errors = len(s.Errors)-importMaxErrs, s.Errors[:importMaxErrs]
	}
	d.Summary, d.Done = &s, !s.DryRun
	if d.Done {
		h.log.Info("csv import", "workspace", v.Current.ID, "user_id", v.Sess.UserID, "kind", d.Kind, "ok", s.OK, "failed", s.Failed, "skipped", s.Skipped)
	}
	h.renderImport(w, v, d, "")
}

// importFile returns the CSV text: the uploaded file on a fresh upload (fresh
// = true), or the hidden field on later steps.
func importFile(r *http.Request) (text string, fresh bool, err error) {
	f, _, ferr := r.FormFile("file")
	if ferr != nil {
		if text = r.PostFormValue("csv"); text == "" {
			return "", false, &crm.ValidationError{Field: "file", Msg: "elegí un archivo CSV"}
		}
		return text, false, nil
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, importMaxCSV+1))
	if err != nil {
		return "", false, err
	}
	if len(data) > importMaxCSV {
		return "", false, &crm.ValidationError{Field: "file", Msg: "el archivo supera " + strconv.Itoa(importMaxCSV>>10) + " KB; para archivos grandes usá hiloctl import-csv"}
	}
	return string(data), true, nil
}

func (h *Handler) renderImport(w http.ResponseWriter, v view, d importData, errMsg string) {
	status := http.StatusOK
	if errMsg != "" {
		status = http.StatusBadRequest
	}
	h.render(w, status, "settings_import", page{Title: "Ajustes", Crumb: "Importar", View: &v, Nav: "import", Error: errMsg, Data: d})
}
