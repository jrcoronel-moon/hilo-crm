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

// settingsSection describes one settings page.
type settingsSection struct {
	title string
	admin bool // only owners/admins may open it
	load  func(h *Handler, r *http.Request, v view, d *settingsData) error
}

var settingsSections = map[string]settingsSection{
	"agents": {title: "Agentes (MCP)", load: func(h *Handler, r *http.Request, v view, d *settingsData) error {
		var err error
		if d.Connections, err = h.id.Connections(r.Context(), v.caller()); err != nil {
			return err
		}
		d.PATs, err = h.id.PATs(r.Context(), v.caller())
		return err
	}},
	"pipelines": {title: "Pipelines", load: func(h *Handler, r *http.Request, v view, d *settingsData) error {
		var err error
		d.Pipelines, err = h.crm.ListPipelines(r.Context(), v.Actor)
		return err
	}},
	"channels": {title: "Canales", load: func(h *Handler, r *http.Request, v view, d *settingsData) error {
		var err error
		d.Channels, err = h.crm.ListChannels(r.Context(), v.Actor)
		return err
	}},
	"forms": {title: "Formularios", admin: true, load: func(h *Handler, r *http.Request, v view, d *settingsData) error {
		var err error
		if d.Forms, err = h.crm.ListLeadForms(r.Context(), v.Actor); err != nil {
			return err
		}
		if d.Pipelines, err = h.crm.ListPipelines(r.Context(), v.Actor); err != nil {
			return err
		}
		d.Channels, err = h.crm.ListChannels(r.Context(), v.Actor)
		return err
	}},
	"members": {title: "Miembros", load: func(h *Handler, r *http.Request, v view, d *settingsData) error {
		var err error
		if d.Members, err = h.id.Members(r.Context(), v.caller()); err != nil || !v.IsAdmin() {
			return err
		}
		if d.Invitations, err = h.id.Invitations(r.Context(), v.caller()); err != nil {
			return err
		}
		d.Domains, err = h.id.WorkspaceDomains(r.Context(), v.caller())
		return err
	}},
	"audit": {title: "Auditoría", admin: true, load: func(h *Handler, r *http.Request, v view, d *settingsData) error {
		var err error
		d.Audit, err = h.crm.ListAudit(r.Context(), v.Actor, "", 100)
		return err
	}},
}

// actionSection maps a POST /settings/{action} to the page it returns to.
var actionSection = map[string]string{
	"pipeline": "pipelines", "pipeline-archive": "pipelines",
	"channel": "channels", "channel-archive": "channels",
	"form": "forms", "form-toggle": "forms",
	"invite": "members", "role": "members", "remove": "members", "invitation-revoke": "members",
	"domain": "members", "domain-remove": "members",
	"token": "agents", "token-revoke": "agents", "connection-revoke": "agents",
}

type settingsData struct {
	Section      string
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
}

var flashes = map[string]string{
	"saved":    "Cambios guardados.",
	"invited":  "Invitación enviada. La persona entra con su cuenta de Google o Microsoft.",
	"revoked":  "Acceso revocado.",
	"removed":  "Eliminado.",
	"created":  "Oportunidad creada.",
	"company":  "Empresa creada.",
	"contact":  "Contacto creado.",
	"archived": "Archivado. Ya no aparece en listas ni búsquedas; el historial se conserva.",
}

func (h *Handler) settings(w http.ResponseWriter, r *http.Request, v view) {
	h.renderSettings(w, r, v, r.PathValue("section"), flashes[r.URL.Query().Get("ok")], "")
}

func (h *Handler) renderSettings(w http.ResponseWriter, r *http.Request, v view, section, flash, newToken string) {
	sec, ok := settingsSections[section]
	if !ok {
		http.NotFound(w, r)
		return
	}
	if sec.admin && !v.IsAdmin() {
		h.fail(w, r, crm.ErrForbidden)
		return
	}
	d := settingsData{Section: section, CSRF: v.Sess.CSRFToken, ChannelKinds: crm.ChannelKinds, NewToken: newToken,
		MCPURL: h.cfg.MCPResource(), Categories: []string{crm.CatInitial, crm.CatInProgress, crm.CatWon, crm.CatLost}}
	if err := sec.load(h, r, v, &d); err != nil {
		h.fail(w, r, err)
		return
	}
	h.render(w, http.StatusOK, "settings_"+section, page{Title: "Ajustes", Crumb: sec.title, View: &v, Nav: section, Flash: flash, Data: d})
}

func (h *Handler) settingsAction(w http.ResponseWriter, r *http.Request, v view) {
	ctx, a, c := r.Context(), v.Actor, v.caller()
	f := r.PostFormValue
	action := r.PathValue("action")
	section, known := actionSection[action]
	if !known {
		http.NotFound(w, r)
		return
	}
	ok := "saved"
	var err error
	switch action {
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
		h.renderSettings(w, r, v, "agents", "Token creado. Copialo ahora: no se vuelve a mostrar.", tok)
		return
	case "token-revoke":
		err, ok = withID(f("id"), func(id uuid.UUID) error { return h.id.RevokePAT(ctx, c, id) }), "revoked"
	case "connection-revoke":
		err, ok = h.id.RevokeConnection(ctx, c, f("client")), "revoked"
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/settings/"+section+"?ok="+ok, http.StatusSeeOther)
}

func withID(s string, fn func(uuid.UUID) error) error {
	id, err := uuid.Parse(s)
	if err != nil {
		return &crm.ValidationError{Field: "id", Msg: "invalid id"}
	}
	return fn(id)
}

// pipelineFromForm reads the pipeline editor: parallel stage_* arrays in
// display order (rows can be reordered by dragging; blank names mean
// "delete this stage"), plus reassign_<id> selects for removed stages.
func pipelineFromForm(r *http.Request) crm.PipelineInput {
	f := r.PostForm
	in := crm.PipelineInput{ID: f.Get("id"), Name: f.Get("name"), IsDefault: f.Get("is_default") == "1", Reassign: map[string]string{}}
	ids, names, cats, probs := f["stage_id"], f["stage_name"], f["stage_category"], f["stage_probability"]
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
		in.Stages = append(in.Stages, st)
	}
	for k, vals := range f {
		if id, ok := strings.CutPrefix(k, "reassign_"); ok && len(vals) > 0 && vals[0] != "" {
			in.Reassign[id] = vals[0]
		}
	}
	if _, present := f["monthly_target"]; present {
		target := 0.0 // empty field removes the goal
		if t := strings.TrimSpace(f.Get("monthly_target")); t != "" {
			var err error
			if target, err = parseAmount(t); err != nil || target <= 0 {
				target = -1 // rejected by validation with a clear message
			}
		}
		in.MonthlyTarget, in.TargetCurrency = &target, f.Get("target_currency")
	}
	return in
}
