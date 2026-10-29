package web

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/jrcoronel-moon/hilo-crm/internal/crm"
	"github.com/jrcoronel-moon/hilo-crm/internal/store"
)

type settingsData struct {
	CSRF         string
	Pipelines    []crm.PipelineView
	Channels     []crm.ChannelView
	ChannelKinds []string
	Forms        []crm.LeadFormView
	Members      []store.ListMembersRow
	Invitations  []store.Invitation
	Domains      []store.WorkspaceDomain
	PATs         []store.ListPATsRow
	Connections  []store.ListConnectionsRow
	Audit        []crm.AuditEntry
	NewToken     string
	MCPURL       string
	Categories   []string
	BlankStages  []int
}

var flashes = map[string]string{
	"saved":   "Cambios guardados.",
	"invited": "Invitación enviada. La persona entra con su cuenta de Google o Microsoft.",
	"revoked": "Acceso revocado.",
	"removed": "Eliminado.",
}

func (h *Handler) settings(w http.ResponseWriter, r *http.Request, v view) {
	h.renderSettings(w, r, v, flashes[r.URL.Query().Get("ok")], "")
}

func (h *Handler) renderSettings(w http.ResponseWriter, r *http.Request, v view, flash, newToken string) {
	ctx, a, c := r.Context(), v.Actor, v.caller()
	d := settingsData{CSRF: v.Sess.CSRFToken, ChannelKinds: crm.ChannelKinds, NewToken: newToken, MCPURL: h.cfg.MCPResource(),
		Categories: []string{crm.CatInitial, crm.CatInProgress, crm.CatWon, crm.CatLost}, BlankStages: []int{1, 2, 3}}
	var err error
	collect := func(e error) {
		if err == nil {
			err = e
		}
	}
	d.Pipelines, err = h.crm.ListPipelines(ctx, a)
	var e error
	d.Channels, e = h.crm.ListChannels(ctx, a)
	collect(e)
	d.Members, e = h.id.Members(ctx, c)
	collect(e)
	d.PATs, e = h.id.PATs(ctx, c)
	collect(e)
	d.Connections, e = h.id.Connections(ctx, c)
	collect(e)
	if v.IsAdmin() {
		d.Forms, e = h.crm.ListLeadForms(ctx, a)
		collect(e)
		d.Invitations, e = h.id.Invitations(ctx, c)
		collect(e)
		d.Domains, e = h.id.WorkspaceDomains(ctx, c)
		collect(e)
		d.Audit, e = h.crm.ListAudit(ctx, a, "", 50)
		collect(e)
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.render(w, http.StatusOK, "settings", page{Title: "Ajustes", View: &v, Nav: "settings", Flash: flash, Data: d})
}

func (h *Handler) settingsAction(w http.ResponseWriter, r *http.Request, v view) {
	ctx, a, c := r.Context(), v.Actor, v.caller()
	f := r.PostFormValue
	ok := "saved"
	var err error
	switch r.PathValue("action") {
	case "pipeline":
		_, err = h.crm.ConfigurePipeline(ctx, a, pipelineFromForm(r))
	case "pipeline-archive":
		err = h.crm.ArchivePipeline(ctx, a, f("id"))
	case "channel":
		_, err = h.crm.UpsertChannel(ctx, a, crm.ChannelInput{ID: f("id"), Name: f("name"), Kind: f("kind")})
	case "channel-archive":
		err = h.crm.ArchiveChannel(ctx, a, f("id"))
	case "form":
		_, err = h.crm.CreateLeadForm(ctx, a, crm.LeadFormInput{Name: f("name"), Pipeline: f("pipeline"), Channel: f("channel"),
			AllowedOrigins: strings.Fields(f("origins")), RedirectURL: f("redirect_url")})
	case "form-toggle":
		err = withID(f("id"), func(id uuid.UUID) error { return h.crm.SetLeadFormActive(ctx, a, id, f("active") == "1") })
	case "invite":
		err, ok = h.id.Invite(ctx, c, f("email"), f("role")), "invited"
	case "role":
		err = withID(f("user"), func(id uuid.UUID) error { return h.id.SetRole(ctx, c, id, f("role")) })
	case "remove":
		err, ok = withID(f("user"), func(id uuid.UUID) error { return h.id.RemoveMember(ctx, c, id) }), "removed"
	case "invitation-revoke":
		err, ok = withID(f("id"), func(id uuid.UUID) error { return h.id.RevokeInvitation(ctx, c, id) }), "revoked"
	case "domain":
		err = h.id.AddWorkspaceDomain(ctx, c, f("domain"), f("role"))
	case "domain-remove":
		err, ok = h.id.RemoveWorkspaceDomain(ctx, c, f("domain")), "removed"
	case "token":
		days, _ := strconv.Atoi(f("days"))
		tok, _, terr := h.id.CreatePAT(ctx, c, f("name"), f("read_only") == "1", time.Duration(max(days, 0))*24*time.Hour)
		if terr != nil {
			h.fail(w, r, terr)
			return
		}
		h.renderSettings(w, r, v, "Token creado. Copialo ahora: no se vuelve a mostrar.", tok)
		return
	case "token-revoke":
		err, ok = withID(f("id"), func(id uuid.UUID) error { return h.id.RevokePAT(ctx, c, id) }), "revoked"
	case "connection-revoke":
		err, ok = h.id.RevokeConnection(ctx, c, f("client")), "revoked"
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/settings?ok="+ok, http.StatusSeeOther)
}

func withID(s string, fn func(uuid.UUID) error) error {
	id, err := uuid.Parse(s)
	if err != nil {
		return &crm.ValidationError{Field: "id", Msg: "invalid id"}
	}
	return fn(id)
}

// pipelineFromForm reads the pipeline editor: parallel stage_* arrays (blank
// names are ignored), ordered by the position inputs, plus reassign_<id>
// selects for removed stages.
func pipelineFromForm(r *http.Request) crm.PipelineInput {
	f := r.PostForm
	in := crm.PipelineInput{ID: f.Get("id"), Name: f.Get("name"), IsDefault: f.Get("is_default") == "1", Reassign: map[string]string{}}
	ids, names, cats, probs, pos := f["stage_id"], f["stage_name"], f["stage_category"], f["stage_probability"], f["stage_position"]
	type row struct {
		pos int
		st  crm.StageInput
	}
	var rows []row
	for i := range names {
		if strings.TrimSpace(names[i]) == "" || i >= len(cats) {
			continue
		}
		st := crm.StageInput{Name: names[i], Category: cats[i]}
		if i < len(ids) {
			st.ID = ids[i]
		}
		if i < len(probs) && probs[i] != "" {
			if p, err := strconv.Atoi(probs[i]); err == nil {
				st.Probability = &p
			}
		}
		p := i * 100
		if i < len(pos) {
			if v, err := strconv.Atoi(pos[i]); err == nil {
				p = v*100 + i
			}
		}
		rows = append(rows, row{pos: p, st: st})
	}
	for i := 1; i < len(rows); i++ { // insertion sort: tiny lists, stable
		for j := i; j > 0 && rows[j].pos < rows[j-1].pos; j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
	for _, rw := range rows {
		in.Stages = append(in.Stages, rw.st)
	}
	for k, vals := range f {
		if id, ok := strings.CutPrefix(k, "reassign_"); ok && len(vals) > 0 && vals[0] != "" {
			in.Reassign[id] = vals[0]
		}
	}
	return in
}
