package web

import (
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/jrcoronel-moon/hilo-crm/internal/crm"
)

type boardData struct {
	Pipelines []crm.PipelineView
	Pipeline  crm.PipelineView
	Columns   []crm.BoardColumn
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
	h.render(w, http.StatusOK, "board", page{Title: "Funnel", View: &v, Nav: "board", Data: boardData{Pipelines: pipes, Pipeline: pv, Columns: cols}})
}

type dashboardData struct {
	Pipelines []crm.PipelineView
	Metrics   crm.Metrics
	Days      int
	MaxStage  int
	MaxChan   int
	Weeks     []weekBar
	MaxWeek   int
}

type weekBar struct {
	Week     string
	Inbound  int
	Outbound int
	Internal int
	Total    int
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
	d := dashboardData{Pipelines: pipes, Metrics: m, Days: days}
	for _, s := range m.Stages {
		d.MaxStage = max(d.MaxStage, s.Count)
	}
	for _, c := range m.LeadsByChan {
		d.MaxChan = max(d.MaxChan, c.Count)
	}
	idx := map[string]int{}
	for _, wc := range m.Interactions {
		i, ok := idx[wc.Week]
		if !ok {
			i = len(d.Weeks)
			idx[wc.Week] = i
			d.Weeks = append(d.Weeks, weekBar{Week: wc.Week})
		}
		b := &d.Weeks[i]
		switch wc.Direction {
		case "inbound":
			b.Inbound += wc.Count
		case "outbound":
			b.Outbound += wc.Count
		default:
			b.Internal += wc.Count
		}
		b.Total += wc.Count
		d.MaxWeek = max(d.MaxWeek, b.Total)
	}
	h.render(w, http.StatusOK, "dashboard", page{Title: "Dashboard", View: &v, Nav: "dashboard", Data: d})
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
