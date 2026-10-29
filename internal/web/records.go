package web

import (
	"encoding/csv"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/jrcoronel-moon/hilo-crm/internal/crm"
)

// ───────────── create from the web ─────────────

// createOpportunity handles the "Nueva oportunidad" dialog. The company is
// found by domain or exact name, or created.
func (h *Handler) createOpportunity(w http.ResponseWriter, r *http.Request, v view) {
	ctx, a := r.Context(), v.Actor
	f := r.PostFormValue
	in := crm.OpportunityInput{Title: ptr(f("title"))}
	// Empty form fields mean "use the default", never "set to empty".
	optional := func(dst **string, key string) {
		if v := strings.TrimSpace(f(key)); v != "" {
			*dst = &v
		}
	}
	optional(&in.Pipeline, "pipeline")
	optional(&in.Currency, "currency")
	optional(&in.Stage, "stage")
	optional(&in.SourceChannel, "channel")
	optional(&in.NextStep, "next_step")
	if amt := strings.TrimSpace(f("amount")); amt != "" {
		val, err := parseAmount(amt)
		if err != nil {
			h.fail(w, r, &crm.ValidationError{Field: "amount", Msg: "must be a number"})
			return
		}
		in.Amount = &val
	}
	if tags := splitTags(f("tags")); len(tags) > 0 {
		in.Tags = &tags
	}
	companyID, err := h.crm.FindOrCreateCompany(ctx, a, f("company"), f("domain"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if companyID != uuid.Nil {
		in.Company = ptr(companyID.String())
	}
	opp, _, err := h.crm.UpsertOpportunity(ctx, a, in)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/board?pipeline="+opp.PipelineID.String()+"&ok=created", http.StatusSeeOther)
}

// parseAmount accepts what people type: "1500.50", "1500,50", "1.500,50"
// or "1,500.50". With both separators, the last one is the decimal mark; a
// lone comma is decimal; spaces are ignored.
func parseAmount(s string) (float64, error) {
	s = strings.ReplaceAll(strings.TrimSpace(s), " ", "")
	dot, comma := strings.LastIndex(s, "."), strings.LastIndex(s, ",")
	switch {
	case dot >= 0 && comma >= 0 && comma > dot:
		s = strings.ReplaceAll(s, ".", "")
		s = strings.Replace(s, ",", ".", 1)
	case dot >= 0 && comma >= 0:
		s = strings.ReplaceAll(s, ",", "")
	case comma >= 0:
		s = strings.Replace(s, ",", ".", 1)
	}
	return strconv.ParseFloat(s, 64)
}

func splitTags(s string) []string {
	var out []string
	for _, t := range strings.Split(s, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func ptr(s string) *string { return &s }

// ───────────── opportunities list ─────────────

type oppListData struct {
	CSRF      string
	Page      crm.Page[crm.OpportunityView]
	F         oppQuery
	Pipelines []crm.PipelineView
	Stages    []crm.StageView // of the selected (or default) pipeline
	Channels  []crm.ChannelView
	Tags      []crm.TagCount
	Result    *crm.BulkResult
}

type oppQuery struct {
	Pipeline, Q, Stage, Status, Channel, Tag, Cursor string
}

// Encode keeps the filters (pagination, export).
func (q oppQuery) Encode() string {
	v := url.Values{}
	for k, val := range map[string]string{"pipeline": q.Pipeline, "q": q.Q, "stage": q.Stage, "status": q.Status, "channel": q.Channel, "tag": q.Tag} {
		if val != "" {
			v.Set(k, val)
		}
	}
	return v.Encode()
}

func readOppQuery(r *http.Request) oppQuery {
	g := r.URL.Query().Get
	return oppQuery{Pipeline: g("pipeline"), Q: strings.TrimSpace(g("q")), Stage: g("stage"), Status: g("status"),
		Channel: g("channel"), Tag: g("tag"), Cursor: g("cursor")}
}

func (q oppQuery) filter(limit int) crm.OpportunityFilter {
	return crm.OpportunityFilter{Pipeline: q.Pipeline, Stage: q.Stage, Status: q.Status, Channel: q.Channel, Tag: q.Tag, Query: q.Q, Cursor: q.Cursor, Limit: limit}
}

func (h *Handler) opportunities(w http.ResponseWriter, r *http.Request, v view) {
	h.renderOpportunities(w, r, v, nil)
}

func (h *Handler) renderOpportunities(w http.ResponseWriter, r *http.Request, v view, result *crm.BulkResult) {
	ctx, a := r.Context(), v.Actor
	fq := readOppQuery(r)
	d := oppListData{CSRF: v.Sess.CSRFToken, F: fq, Result: result}
	var err error
	if d.Pipelines, err = h.crm.ListPipelines(ctx, a); err != nil {
		h.fail(w, r, err)
		return
	}
	for _, p := range d.Pipelines {
		if p.ID.String() == fq.Pipeline || (fq.Pipeline == "" && p.IsDefault) {
			d.Stages = p.Stages
		}
	}
	if d.Page, err = h.crm.ListOpportunities(ctx, a, fq.filter(50)); err != nil {
		h.fail(w, r, err)
		return
	}
	if d.Channels, err = h.crm.ListChannels(ctx, a); err != nil {
		h.fail(w, r, err)
		return
	}
	if d.Tags, err = h.crm.ListTags(ctx, a); err != nil {
		h.fail(w, r, err)
		return
	}
	h.render(w, http.StatusOK, "opportunities", page{Title: "Oportunidades", View: &v, Nav: "opportunities", Data: d})
}

// bulk applies an action to the selected rows, then shows the list again
// with a summary (and the rows that could not be changed, with why).
func (h *Handler) bulk(w http.ResponseWriter, r *http.Request, v view) {
	res, err := h.crm.Bulk(r.Context(), v.Actor, crm.BulkInput{
		IDs: r.PostForm["id"], Action: r.PostFormValue("action"), Stage: r.PostFormValue("stage"),
		Tag: r.PostFormValue("tag"), Reason: r.PostFormValue("reason"),
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	r.URL.RawQuery = r.PostFormValue("back")
	h.renderOpportunities(w, r, v, &res)
}

// ───────────── CSV export ─────────────

const exportMax = 10000

// exportCSV streams the filtered opportunities. Cells that a spreadsheet
// would evaluate as a formula are neutralised (CSV injection).
func (h *Handler) exportCSV(w http.ResponseWriter, r *http.Request, v view) {
	fq := readOppQuery(r)
	fq.Cursor = ""
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="hilo-oportunidades-`+time.Now().Format("2006-01-02")+`.csv"`)
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"id", "titulo", "tipo", "empresa", "etapa", "categoria", "estado", "monto", "moneda", "probabilidad",
		"cierre_esperado", "tags", "proximo_paso", "ultima_actividad", "creada", "actualizada"})
	written := 0
	for written < exportMax {
		page, err := h.crm.ListOpportunities(r.Context(), v.Actor, fq.filter(100))
		if err != nil {
			h.log.Error("export", "err", err)
			break
		}
		for _, o := range page.Items {
			last := ""
			if o.LastInteractionAt != nil {
				last = o.LastInteractionAt.Format(time.RFC3339)
			}
			_ = cw.Write(safeCSV([]string{o.ID.String(), o.Title, o.Kind, o.CompanyName, o.StageName, o.StageCategory, o.Status,
				strconv.FormatFloat(o.Amount, 'f', 2, 64), o.Currency, strconv.Itoa(o.Probability), o.ExpectedCloseDate,
				strings.Join(o.Tags, ", "), o.NextStep, last, o.CreatedAt.Format(time.RFC3339), o.UpdatedAt.Format(time.RFC3339)}))
			written++
		}
		if page.NextCursor == "" {
			break
		}
		fq.Cursor = page.NextCursor
	}
	cw.Flush()
}

// safeCSV prefixes cells starting with a formula trigger with an apostrophe
// (OWASP CSV injection guidance).
func safeCSV(row []string) []string {
	for i, c := range row {
		if c != "" && strings.ContainsRune("=+-@\t\r", rune(c[0])) {
			row[i] = "'" + c
		}
	}
	return row
}

// ───────────── interactions ─────────────

type interactionsData struct {
	Page     crm.Page[crm.InteractionRow]
	F        crm.InteractionFilter
	Types    []string
	Channels []crm.ChannelView
	Next     string
}

func (h *Handler) interactions(w http.ResponseWriter, r *http.Request, v view) {
	g := r.URL.Query().Get
	f := crm.InteractionFilter{Type: g("type"), Direction: g("direction"), Channel: g("channel"), Query: strings.TrimSpace(g("q")), Cursor: g("cursor"), Limit: 50}
	d := interactionsData{F: f, Types: crm.InteractionTypes}
	var err error
	if d.Page, err = h.crm.ListInteractions(r.Context(), v.Actor, f); err != nil {
		h.fail(w, r, err)
		return
	}
	if d.Channels, err = h.crm.ListChannels(r.Context(), v.Actor); err != nil {
		h.fail(w, r, err)
		return
	}
	if d.Page.NextCursor != "" {
		q := r.URL.Query()
		q.Set("cursor", d.Page.NextCursor)
		d.Next = "?" + q.Encode()
	}
	h.render(w, http.StatusOK, "interactions", page{Title: "Interacciones", View: &v, Nav: "interactions", Data: d})
}
