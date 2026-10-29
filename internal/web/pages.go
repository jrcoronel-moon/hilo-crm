package web

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/jrcoronel-moon/hilo-crm/internal/crm"
	"github.com/jrcoronel-moon/hilo-crm/internal/httpx"
)

type boardData struct {
	Pipelines []crm.PipelineView
	Pipeline  crm.PipelineView
	Columns   []crm.BoardColumn
	Metrics   crm.Metrics
	CSRF      string
}

func (h *Handler) board(w http.ResponseWriter, r *http.Request, v view) {
	pipes, err := h.crm.ListPipelines(r.Context(), v.Actor)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	pv, cols, err := h.crm.Board(r.Context(), v.Actor, r.URL.Query().Get("pipeline"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	m, err := h.crm.Metrics(r.Context(), v.Actor, crm.MetricsInput{Pipeline: pv.ID.String(), Days: 90})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.render(w, http.StatusOK, "board", page{Title: "Funnel", Crumb: pv.Name, View: &v, Nav: "board",
		Data: boardData{Pipelines: pipes, Pipeline: pv, Columns: cols, Metrics: m, CSRF: v.Sess.CSRFToken}})
}

// boardMove handles a card dropped on another column (fetch from app.js).
// Same rules as the MCP tool: write permission, lost stages need a reason,
// and the move is recorded in the stage history and audit log.
func (h *Handler) boardMove(w http.ResponseWriter, r *http.Request, v view) {
	out, err := h.crm.MoveOpportunityStage(r.Context(), v.Actor, crm.MoveInput{
		Opportunity: r.PostFormValue("opportunity"), Stage: r.PostFormValue("stage"), Reason: r.PostFormValue("reason"),
	})
	if err != nil {
		status, msg := http.StatusInternalServerError, "No se pudo mover la oportunidad."
		var ve *crm.ValidationError
		switch {
		case errors.As(err, &ve):
			status, msg = http.StatusBadRequest, ve.Error()
		case errors.Is(err, crm.ErrForbidden):
			status, msg = http.StatusForbidden, "Tu rol no permite mover oportunidades."
		case errors.Is(err, crm.ErrNotFound):
			status, msg = http.StatusNotFound, "La oportunidad ya no existe."
		default:
			h.log.Error("board move", "err", err)
		}
		httpx.WriteError(w, status, msg)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": out.Status, "stage": out.StageName})
}

type dashboardData struct {
	Pipelines []crm.PipelineView
	Metrics   crm.Metrics
	Days      int
	MaxStage  int
	Weeks     []vbar // interactions per week
	WeekPeak  vbar
	Channels  []slice
	Share     string // this view's URL, for "Compartir"
}

// vbar is one bar of a vertical SVG chart (viewBox 0 0 600 100).
type vbar struct {
	X, Y, W, H float64
	Label      string
	Value      int
	Peak       bool
}

// slice is one channel in the legend (and, later, the donut).
type slice struct {
	Name  string
	Count int
	Pct   int
	Class string
}

func weekBars(m crm.Metrics) ([]vbar, vbar) {
	idx, totals := map[string]int{}, []vbar{}
	for _, wc := range m.Interactions {
		i, ok := idx[wc.Week]
		if !ok {
			i = len(totals)
			idx[wc.Week] = i
			totals = append(totals, vbar{Label: wc.Week})
		}
		totals[i].Value += wc.Count
	}
	peak, maxV := vbar{}, 0
	for _, b := range totals {
		maxV = max(maxV, b.Value)
	}
	n := float64(len(totals))
	for i := range totals {
		b := &totals[i]
		slot := 600 / max(n, 1)
		b.W = slot * 0.78
		b.X = float64(i)*slot + (slot-b.W)/2
		b.H = 100 * float64(b.Value) / float64(max(maxV, 1))
		b.Y = 100 - b.H
		if b.Value == maxV && maxV > 0 && !peak.Peak {
			b.Peak = true
			peak = *b
		}
	}
	return totals, peak
}

var channelClasses = []string{"c-p3", "c-p1", "c-initial", "c-p2", "c-p4", "c-won"}

func channelSlices(m crm.Metrics) []slice {
	total := 0
	for _, c := range m.LeadsByChan {
		total += c.Count
	}
	out := make([]slice, 0, len(m.LeadsByChan))
	for i, c := range m.LeadsByChan {
		out = append(out, slice{Name: c.Channel, Count: c.Count, Pct: c.Count * 100 / max(total, 1), Class: channelClasses[i%len(channelClasses)]})
	}
	return out
}

func (h *Handler) dashboard(w http.ResponseWriter, r *http.Request, v view) {
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days != 30 && days != 90 && days != 180 && days != 365 {
		days = 90
	}
	pipes, err := h.crm.ListPipelines(r.Context(), v.Actor)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	m, err := h.crm.Metrics(r.Context(), v.Actor, crm.MetricsInput{Pipeline: r.URL.Query().Get("pipeline"), Days: days})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	d := dashboardData{Pipelines: pipes, Metrics: m, Days: days, Channels: channelSlices(m),
		Share: h.cfg.BaseURL + "/dashboard?pipeline=" + m.PipelineID.String() + "&days=" + strconv.Itoa(days)}
	for _, s := range m.Stages {
		d.MaxStage = max(d.MaxStage, s.Count)
	}
	d.Weeks, d.WeekPeak = weekBars(m)
	h.render(w, http.StatusOK, "dashboard", page{Title: "Dashboard", Crumb: m.PipelineName, View: &v, Nav: "dashboard", Data: d})
}

func (h *Handler) newWorkspacePage(w http.ResponseWriter, _ *http.Request, v view) {
	h.render(w, http.StatusOK, "workspace_new", page{Title: "Nuevo workspace", View: &v})
}

func (h *Handler) createWorkspace(w http.ResponseWriter, r *http.Request, v view) {
	ws, err := h.id.CreateWorkspace(r.Context(), v.Sess.UserID, r.PostFormValue("name"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if err := h.id.SwitchWorkspace(r.Context(), v.Sess, ws.ID); err != nil {
		h.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/board", http.StatusSeeOther)
}

func (h *Handler) switchWorkspace(w http.ResponseWriter, r *http.Request, v view) {
	id, err := uuid.Parse(r.PostFormValue("workspace"))
	if err == nil {
		err = h.id.SwitchWorkspace(r.Context(), v.Sess, id)
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/board", http.StatusSeeOther)
}
