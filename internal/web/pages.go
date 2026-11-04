package web

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/jrcoronel-moon/hilo-crm/internal/crm"
	"github.com/jrcoronel-moon/hilo-crm/internal/httpx"
	"github.com/jrcoronel-moon/hilo-crm/internal/i18n"
)

type boardData struct {
	Pipelines []crm.PipelineView
	Pipeline  crm.PipelineView
	Columns   []crm.BoardColumn
	Metrics   crm.Metrics
	CSRF      string
	F         boardQuery
	Channels  []crm.ChannelView
	Tags      []crm.TagCount
	AllTags   []crm.TagCount
	Stages    []crm.StageView
}

// boardQuery is the toolbar state, kept in the URL so views can be shared.
type boardQuery struct {
	Pipeline, Q, Channel, Tag string
	Days                      int
	Open                      bool
}

// Active reports whether any filter narrows the board.
func (q boardQuery) Active() bool { return q.Q != "" || q.Channel != "" || q.Tag != "" || q.Open }

// Without returns the query string with one filter removed (chip "×").
func (q boardQuery) Without(key string) string {
	v := q.values()
	v.Del(key)
	return "?" + v.Encode()
}

// Encode returns the full query string (export link).
func (q boardQuery) Encode() string { return q.values().Encode() }

func (q boardQuery) values() url.Values {
	v := url.Values{}
	set := func(k, val string) {
		if val != "" {
			v.Set(k, val)
		}
	}
	set("pipeline", q.Pipeline)
	set("q", q.Q)
	set("channel", q.Channel)
	set("tag", q.Tag)
	if q.Days != 30 {
		v.Set("days", strconv.Itoa(q.Days))
	}
	if q.Open {
		v.Set("open", "1")
	}
	return v
}

func readBoardQuery(r *http.Request) boardQuery {
	g := r.URL.Query().Get
	days, _ := strconv.Atoi(g("days"))
	if days != 7 && days != 30 && days != 90 {
		days = 30
	}
	return boardQuery{Pipeline: g("pipeline"), Q: strings.TrimSpace(g("q")), Channel: g("channel"), Tag: g("tag"), Days: days, Open: g("open") == "1"}
}

func (h *Handler) board(w http.ResponseWriter, r *http.Request, v view) {
	ctx, a := r.Context(), v.Actor
	fq := readBoardQuery(r)
	pipes, err := h.crm.ListPipelines(ctx, a)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	pv, cols, err := h.crm.Board(ctx, a, crm.BoardFilter{Pipeline: fq.Pipeline, Query: fq.Q, Channel: fq.Channel, Tag: fq.Tag, ClosedDays: fq.Days, OpenOnly: fq.Open})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	m, err := h.crm.Metrics(ctx, a, crm.MetricsInput{Pipeline: pv.ID.String(), Days: 90})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	d := boardData{Pipelines: pipes, Pipeline: pv, Columns: cols, Metrics: m, CSRF: v.Sess.CSRFToken, F: fq}
	if d.Channels, err = h.crm.ListChannels(ctx, a); err != nil {
		h.fail(w, r, err)
		return
	}
	if d.Tags, err = h.crm.ListTags(ctx, a); err != nil {
		h.fail(w, r, err)
		return
	}
	if d.AllTags, err = h.crm.ListAllTags(ctx, a); err != nil {
		h.fail(w, r, err)
		return
	}
	for _, c := range cols {
		d.Stages = append(d.Stages, c.Stage)
	}
	h.render(w, r, http.StatusOK, "board", page{Title: i18n.M("Funnel"), Crumb: pv.Name, View: &v, Nav: "board", Flash: flashes[r.URL.Query().Get("ok")], Data: d})
}

// boardMove handles a card dropped on another column (fetch from app.js).
// Same rules as the MCP tool: write permission, lost stages need a reason,
// and the move is recorded in the stage history and audit log.
func (h *Handler) boardMove(w http.ResponseWriter, r *http.Request, v view) {
	out, err := h.crm.MoveOpportunityStage(r.Context(), v.Actor, crm.MoveInput{
		Opportunity: r.PostFormValue("opportunity"), Stage: r.PostFormValue("stage"), Reason: r.PostFormValue("reason"),
	})
	if err != nil {
		status, msg := http.StatusInternalServerError, i18n.M("No se pudo mover la oportunidad.")
		var ve *crm.ValidationError
		switch {
		case errors.As(err, &ve):
			status, msg = http.StatusBadRequest, ve.Field+": "+ve.Msg
		case errors.Is(err, crm.ErrForbidden):
			status, msg = http.StatusForbidden, i18n.M("Tu rol no permite mover oportunidades.")
		case errors.Is(err, crm.ErrNotFound):
			status, msg = http.StatusNotFound, i18n.M("La oportunidad ya no existe.")
		default:
			h.log.Error("board move", "err", err)
		}
		httpx.WriteError(w, status, i18n.T(h.lang(r), msg))
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
	Months    []vbar // won per month
	GoalY     float64
	MonthPeak float64
	Donut     []arc
	NewTotal  int // new opportunities in the period (donut centre)
	Actors    []actorRow
}

// arc is a donut segment: an SVG circle of circumference 100 drawn with
// stroke-dasharray (attributes only, so the strict CSP holds).
type arc struct {
	Dash, Gap, Offset float64
	Class             string
}

type actorRow struct {
	Initials, Title, Sub, Class string
	Changes, Created            int
}

var monthNames = []string{i18n.M("Ene"), i18n.M("Feb"), i18n.M("Mar"), i18n.M("Abr"), i18n.M("May"), i18n.M("Jun"),
	i18n.M("Jul"), i18n.M("Ago"), i18n.M("Sep"), i18n.M("Oct"), i18n.M("Nov"), i18n.M("Dic")}

// monthBars draws the won-per-month chart; the goal line shares its scale.
func monthBars(m crm.Metrics) ([]vbar, float64, float64) {
	maxV := 0.0
	for _, x := range m.WonByMonth {
		maxV = max(maxV, x.Amount)
	}
	if m.Goal != nil {
		maxV = max(maxV, m.Goal.Target)
	}
	scale := max(maxV*1.15, 1)
	out := make([]vbar, 0, len(m.WonByMonth))
	slot := 600 / float64(max(len(m.WonByMonth), 1))
	for i, x := range m.WonByMonth {
		mm, _ := strconv.Atoi(x.Month[5:])
		b := vbar{W: slot * 0.62, Value: int(x.Amount), Label: monthNames[(mm+11)%12]}
		b.X = float64(i)*slot + (slot-b.W)/2
		b.H = 100 * x.Amount / scale
		b.Y = 100 - b.H
		b.Peak = i == len(m.WonByMonth)-1
		out = append(out, b)
	}
	goalY := -1.0
	if m.Goal != nil {
		goalY = 100 - 100*m.Goal.Target/scale
	}
	return out, goalY, maxV
}

func donut(ch []slice) []arc {
	out := make([]arc, 0, len(ch))
	offset := 25.0 // start at 12 o'clock
	total := 0
	for _, c := range ch {
		total += c.Count
	}
	for _, c := range ch {
		dash := 100 * float64(c.Count) / float64(max(total, 1))
		out = append(out, arc{Dash: dash, Gap: 100 - dash, Offset: offset, Class: c.Class})
		offset -= dash
	}
	return out
}

func actorRows(acts []crm.ActorCount) []actorRow {
	out := make([]actorRow, 0, len(acts))
	for _, a := range acts {
		r := actorRow{Changes: a.Changes, Created: a.Created}
		switch a.Kind {
		case "oauth":
			r.Title, r.Sub, r.Class = a.Client, i18n.M("agente · OAuth"), "agent2"
		case "pat":
			r.Title, r.Sub, r.Class = strings.TrimPrefix(a.Client, "PAT: "), i18n.M("token personal"), "agent"
		case "form":
			r.Title, r.Sub = a.Client, i18n.M("formulario público")
		default:
			r.Title, r.Sub = firstNonEmpty(a.Name, a.Email), i18n.M("sesión web")
		}
		r.Initials = initials(r.Title, a.Email)
		out = append(out, r)
	}
	return out
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
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
	d.Months, d.GoalY, d.MonthPeak = monthBars(m)
	d.Donut = donut(d.Channels)
	for _, c := range d.Channels {
		d.NewTotal += c.Count
	}
	d.Actors = actorRows(m.Activity)
	h.render(w, r, http.StatusOK, "dashboard", page{Title: i18n.M("Dashboard"), Crumb: m.PipelineName, View: &v, Nav: "dashboard", Data: d})
}

func (h *Handler) newWorkspacePage(w http.ResponseWriter, r *http.Request, v view) {
	title := i18n.M("Nuevo workspace")
	if len(v.Invitations) > 0 {
		title = i18n.M("Invitaciones")
	}
	h.render(w, r, http.StatusOK, "workspace_new", page{Title: title, View: &v, Nav: "invitations"})
}

// acceptInvitation joins the workspace the user was invited to and opens it.
func (h *Handler) acceptInvitation(w http.ResponseWriter, r *http.Request, v view) {
	err := withID(r.PostFormValue("id"), func(id uuid.UUID) error {
		ws, err := h.id.AcceptInvitation(r.Context(), v.Sess.UserID, v.Sess.Email, id)
		if err != nil {
			return err
		}
		return h.id.SwitchWorkspace(r.Context(), v.Sess, ws)
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// declineInvitation turns an invitation down.
func (h *Handler) declineInvitation(w http.ResponseWriter, r *http.Request, v view) {
	if err := withID(r.PostFormValue("id"), func(id uuid.UUID) error {
		return h.id.DeclineInvitation(r.Context(), v.Sess.Email, id)
	}); err != nil {
		h.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/workspaces/new", http.StatusSeeOther)
}

func (h *Handler) createWorkspace(w http.ResponseWriter, r *http.Request, v view) {
	ws, err := h.id.CreateWorkspaceIn(r.Context(), v.Sess.UserID, r.PostFormValue("name"), h.lang(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if err := h.id.SwitchWorkspace(r.Context(), v.Sess, ws.ID); err != nil {
		h.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther) // the board, or the headless page without the full UI
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
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
