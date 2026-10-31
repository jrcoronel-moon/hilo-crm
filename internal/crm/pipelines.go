package crm

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/jrcoronel-moon/hilo-crm/internal/i18n"
	"github.com/jrcoronel-moon/hilo-crm/internal/store"
)

// Stage categories. Every pipeline has exactly one initial stage and at least
// one of each of the others (enforced here and, at commit, by the database).
const (
	CatInitial    = "initial"
	CatInProgress = "in_progress"
	CatWon        = "won"
	CatLost       = "lost"
)

const maxStages = 20

// StageView is a stage as returned to callers.
type StageView struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Category    string    `json:"category"`
	Position    int       `json:"position"`
	Probability int       `json:"probability"`
}

// PipelineView is a pipeline with its ordered stages.
type PipelineView struct {
	ID             uuid.UUID   `json:"id"`
	Name           string      `json:"name"`
	IsDefault      bool        `json:"is_default"`
	MonthlyTarget  float64     `json:"monthly_target,omitempty"`
	TargetCurrency string      `json:"target_currency,omitempty"`
	Stages         []StageView `json:"stages"`
}

func pipelineView(p store.Pipeline, stages []StageView) PipelineView {
	v := PipelineView{ID: p.ID, Name: p.Name, IsDefault: p.IsDefault, Stages: stages}
	if p.MonthlyTargetMinor > 0 {
		v.MonthlyTarget, v.TargetCurrency = MinorToAmount(p.MonthlyTargetMinor), p.TargetCurrency
	}
	return v
}

func stageView(s store.PipelineStage) StageView {
	return StageView{ID: s.ID, Name: s.Name, Category: s.Category, Position: int(s.Position), Probability: int(s.Probability)}
}

// statusFor derives an opportunity status from its stage category.
func statusFor(category string) string {
	switch category {
	case CatWon:
		return "won"
	case CatLost:
		return "lost"
	default:
		return "open"
	}
}

// ListPipelines returns every active pipeline with its stages.
func (s *Service) ListPipelines(ctx context.Context, a Actor) ([]PipelineView, error) {
	var out []PipelineView
	err := s.tx(ctx, a, PermRead, func(q *store.Queries) error {
		ps, err := q.ListPipelines(ctx, a.WorkspaceID)
		if err != nil {
			return err
		}
		stages, err := q.ListAllStages(ctx, a.WorkspaceID)
		if err != nil {
			return err
		}
		byPipe := map[uuid.UUID][]StageView{}
		for _, st := range stages {
			byPipe[st.PipelineID] = append(byPipe[st.PipelineID], stageView(st))
		}
		out = make([]PipelineView, 0, len(ps))
		for _, p := range ps {
			out = append(out, pipelineView(p, byPipe[p.ID]))
		}
		return nil
	})
	return out, err
}

// resolvePipeline finds a pipeline by id or name; empty means the default.
func resolvePipeline(ctx context.Context, q *store.Queries, ws uuid.UUID, ref string) (store.Pipeline, error) {
	ref = strings.TrimSpace(ref)
	var p store.Pipeline
	var err error
	if ref == "" {
		p, err = q.GetDefaultPipeline(ctx, ws)
	} else if id, perr := uuid.Parse(ref); perr == nil {
		p, err = q.GetPipeline(ctx, store.GetPipelineParams{WorkspaceID: ws, ID: id})
	} else {
		p, err = q.GetPipelineByName(ctx, store.GetPipelineByNameParams{WorkspaceID: ws, Name: ref})
	}
	return p, notFound("pipeline", err)
}

// resolveStage finds a stage of pipeline p by id or (case-insensitive) name.
func resolveStage(ctx context.Context, q *store.Queries, ws uuid.UUID, pipelineID uuid.UUID, ref string) (store.PipelineStage, error) {
	stages, err := q.ListStages(ctx, store.ListStagesParams{WorkspaceID: ws, PipelineID: pipelineID})
	if err != nil {
		return store.PipelineStage{}, err
	}
	ref = strings.TrimSpace(ref)
	for _, st := range stages {
		if st.ID.String() == ref || strings.EqualFold(st.Name, ref) {
			return st, nil
		}
	}
	names := make([]string, 0, len(stages))
	for _, st := range stages {
		names = append(names, st.Name)
	}
	return store.PipelineStage{}, invalid("stage", "unknown stage %q; this pipeline's stages are: %s", ref, strings.Join(names, ", "))
}

// StageInput describes one stage in a pipeline configuration, in order.
type StageInput struct {
	ID          string `json:"id,omitempty"` // existing stage to keep (rename/re-categorise)
	Name        string `json:"name"`
	Category    string `json:"category"`
	Probability *int   `json:"probability,omitempty"`
}

// PipelineInput creates (ID empty) or reconfigures a pipeline. Stages is the
// complete ordered list: stages of the pipeline that are not listed are
// deleted, and if they still hold opportunities Reassign must say where those
// go (old stage id → name or id of a stage in the new list).
type PipelineInput struct {
	ID             string            `json:"id,omitempty"`
	Name           string            `json:"name"`
	IsDefault      bool              `json:"is_default,omitempty"`
	Stages         []StageInput      `json:"stages"`
	Reassign       map[string]string `json:"reassign,omitempty"`
	MonthlyTarget  *float64          `json:"monthly_target,omitempty" jsonschema:"amount to win per calendar month; 0 removes the goal"`
	TargetCurrency string            `json:"target_currency,omitempty" jsonschema:"ISO 4217 currency of the goal, default USD"`
}

var defaultProbability = map[string]int{CatInitial: 10, CatInProgress: 50, CatWon: 100, CatLost: 0}

func validateStages(in []StageInput) ([]StageInput, error) {
	if len(in) < 4 || len(in) > maxStages {
		return nil, invalid("stages", "a pipeline needs between 4 and %d stages", maxStages)
	}
	counts := map[string]int{}
	names := map[string]bool{}
	out := make([]StageInput, len(in))
	for i, st := range in {
		name, err := requiredName("stages.name", st.Name)
		if err != nil {
			return nil, err
		}
		key := strings.ToLower(name)
		if names[key] {
			return nil, invalid("stages", "duplicate stage name %q", name)
		}
		names[key] = true
		if _, ok := defaultProbability[st.Category]; !ok {
			return nil, invalid("stages.category", "must be one of initial, in_progress, won, lost (got %q)", st.Category)
		}
		counts[st.Category]++
		p := defaultProbability[st.Category]
		if st.Probability != nil {
			if *st.Probability < 0 || *st.Probability > 100 {
				return nil, invalid("stages.probability", "must be between 0 and 100")
			}
			p = *st.Probability
		}
		out[i] = StageInput{ID: strings.TrimSpace(st.ID), Name: name, Category: st.Category, Probability: &p}
	}
	if counts[CatInitial] != 1 || counts[CatInProgress] < 1 || counts[CatWon] < 1 || counts[CatLost] < 1 {
		return nil, invalid("stages", "need exactly 1 initial stage and at least 1 in_progress, 1 won and 1 lost (got %d/%d/%d/%d)",
			counts[CatInitial], counts[CatInProgress], counts[CatWon], counts[CatLost])
	}
	return out, nil
}

// ConfigurePipeline creates or reconfigures a pipeline atomically.
func (s *Service) ConfigurePipeline(ctx context.Context, a Actor, in PipelineInput) (PipelineView, error) {
	name, err := requiredName("name", in.Name)
	if err != nil {
		return PipelineView{}, err
	}
	stages, err := validateStages(in.Stages)
	if err != nil {
		return PipelineView{}, err
	}
	var pipeID uuid.UUID
	err = s.tx(ctx, a, PermAdmin, func(q *store.Queries) error {
		ws := a.WorkspaceID
		var existing []store.PipelineStage
		if in.ID == "" {
			n, err := q.CountPipelines(ctx, ws)
			if err != nil {
				return err
			}
			p, err := q.CreatePipeline(ctx, store.CreatePipelineParams{WorkspaceID: ws, Name: name, Position: n})
			if err != nil {
				return err
			}
			pipeID = p.ID
			in.IsDefault = in.IsDefault || n == 0
		} else {
			p, err := resolvePipeline(ctx, q, ws, in.ID)
			if err != nil {
				return err
			}
			pipeID = p.ID
			if err := q.RenamePipeline(ctx, store.RenamePipelineParams{WorkspaceID: ws, ID: pipeID, Name: name}); err != nil {
				return err
			}
			if existing, err = q.ListStages(ctx, store.ListStagesParams{WorkspaceID: ws, PipelineID: pipeID}); err != nil {
				return err
			}
		}
		if err := applyStages(ctx, q, ws, pipeID, existing, stages, in.Reassign); err != nil {
			return err
		}
		if in.IsDefault {
			if err := q.ClearDefaultPipeline(ctx, ws); err != nil {
				return err
			}
			if err := q.SetDefaultPipeline(ctx, store.SetDefaultPipelineParams{WorkspaceID: ws, ID: pipeID}); err != nil {
				return err
			}
		}
		if err := q.SyncOpportunityStatuses(ctx, store.SyncOpportunityStatusesParams{WorkspaceID: ws, PipelineID: pipeID}); err != nil {
			return err
		}
		if in.MonthlyTarget != nil {
			target, cur, err := cleanTarget(*in.MonthlyTarget, in.TargetCurrency)
			if err != nil {
				return err
			}
			if err := q.SetPipelineTarget(ctx, store.SetPipelineTargetParams{WorkspaceID: ws, ID: pipeID, MonthlyTargetMinor: target, TargetCurrency: cur}); err != nil {
				return err
			}
		}
		return s.audit(ctx, q, a, "pipeline.configure", "pipeline", pipeID, map[string]any{"name": name, "stages": len(stages)})
	})
	if err != nil {
		return PipelineView{}, err
	}
	return s.pipelineView(ctx, a, pipeID)
}

func cleanTarget(amount float64, currency string) (int64, string, error) {
	minor, err := AmountToMinor("monthly_target", amount)
	if err != nil {
		return 0, "", err
	}
	if currency == "" {
		currency = "USD"
	}
	cur, err := cleanCurrency("target_currency", currency)
	return minor, cur, err
}

// applyStages reconciles the stored stages with the requested list.
func applyStages(ctx context.Context, q *store.Queries, ws, pipeID uuid.UUID, existing []store.PipelineStage, stages []StageInput, reassign map[string]string) error {
	byID := map[string]store.PipelineStage{}
	for _, st := range existing {
		byID[st.ID.String()] = st
	}
	if len(existing) > 0 {
		if err := q.ParkStages(ctx, store.ParkStagesParams{WorkspaceID: ws, PipelineID: pipeID}); err != nil {
			return err
		}
	}
	kept := map[string]bool{}
	final := map[string]uuid.UUID{} // lower(name) and id → new stage id
	for i, st := range stages {
		pos, prob := int32(i), int32(*st.Probability) //nolint:gosec // bounded: ≤20 stages, 0..100
		if st.ID != "" {
			old, ok := byID[st.ID]
			if !ok || kept[st.ID] {
				return invalid("stages.id", "stage %q does not belong to this pipeline", st.ID)
			}
			kept[st.ID] = true
			if err := q.UpdateStage(ctx, store.UpdateStageParams{WorkspaceID: ws, ID: old.ID, Name: st.Name, Category: st.Category, Position: pos, Probability: prob}); err != nil {
				return err
			}
			final[strings.ToLower(st.Name)], final[old.ID.String()] = old.ID, old.ID
			continue
		}
		ns, err := q.InsertStage(ctx, store.InsertStageParams{WorkspaceID: ws, PipelineID: pipeID, Name: st.Name, Category: st.Category, Position: pos, Probability: prob})
		if err != nil {
			return err
		}
		final[strings.ToLower(st.Name)], final[ns.ID.String()] = ns.ID, ns.ID
	}
	for _, old := range existing {
		id := old.ID.String()
		if kept[id] {
			continue
		}
		n, err := q.CountOpportunitiesInStage(ctx, store.CountOpportunitiesInStageParams{WorkspaceID: ws, StageID: old.ID})
		if err != nil {
			return err
		}
		if n > 0 {
			target, ok := final[strings.ToLower(strings.TrimSpace(reassign[id]))]
			if !ok {
				return invalid("reassign", "stage %q still has %d opportunities: set reassign[%q] to the name or id of a stage in the new list", old.Name, n, id)
			}
			if err := q.ReassignStage(ctx, store.ReassignStageParams{WorkspaceID: ws, FromStage: old.ID, ToStage: target}); err != nil {
				return err
			}
		}
		if err := q.DeleteStage(ctx, store.DeleteStageParams{WorkspaceID: ws, ID: old.ID}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) pipelineView(ctx context.Context, a Actor, id uuid.UUID) (PipelineView, error) {
	all, err := s.ListPipelines(ctx, a)
	if err != nil {
		return PipelineView{}, err
	}
	for _, p := range all {
		if p.ID == id {
			return p, nil
		}
	}
	return PipelineView{}, &notFoundError{what: "pipeline"}
}

// DefaultStages is the pipeline every new workspace starts with.
func DefaultStages() []StageInput {
	p := func(v int) *int { return &v }
	return []StageInput{
		{Name: i18n.M("Lead"), Category: CatInitial, Probability: p(10)},
		{Name: i18n.M("Calificado"), Category: CatInProgress, Probability: p(25)},
		{Name: i18n.M("Propuesta"), Category: CatInProgress, Probability: p(50)},
		{Name: i18n.M("Negociación"), Category: CatInProgress, Probability: p(75)},
		{Name: i18n.M("Ganado"), Category: CatWon, Probability: p(100)},
		{Name: i18n.M("Perdido"), Category: CatLost, Probability: p(0)},
	}
}

// DefaultChannels are created with every new workspace.
var DefaultChannels = []struct{ Name, Kind string }{
	{i18n.M("Web"), "web"}, {i18n.M("Formulario"), "form"}, {i18n.M("Email"), "email"}, {i18n.M("Teléfono"), "phone"},
	{i18n.M("LinkedIn"), "social"}, {i18n.M("Referido"), "referral"}, {i18n.M("Evento"), "event"}, {i18n.M("Outbound"), "outbound"},
	{i18n.M("Videoconferencia"), "videocall"}, {i18n.M("Video"), "video"},
}

// SeedWorkspace creates the default pipeline and channels, named in lang. It
// runs inside the transaction that created the workspace, with RLS already
// bound to it.
func SeedWorkspace(ctx context.Context, q *store.Queries, ws uuid.UUID, lang string) error {
	p, err := q.CreatePipeline(ctx, store.CreatePipelineParams{WorkspaceID: ws, Name: i18n.T(lang, "Ventas"), IsDefault: true})
	if err != nil {
		return err
	}
	defaults := DefaultStages()
	for i := range defaults {
		defaults[i].Name = i18n.T(lang, defaults[i].Name)
	}
	stages, err := validateStages(defaults)
	if err != nil {
		return err
	}
	if err := applyStages(ctx, q, ws, p.ID, nil, stages, nil); err != nil {
		return err
	}
	for _, ch := range DefaultChannels {
		if _, err := q.CreateChannel(ctx, store.CreateChannelParams{WorkspaceID: ws, Name: i18n.T(lang, ch.Name), Kind: ch.Kind}); err != nil {
			return err
		}
	}
	return nil
}

// ArchivePipeline hides a pipeline. The default pipeline cannot be archived.
func (s *Service) ArchivePipeline(ctx context.Context, a Actor, ref string) error {
	return s.tx(ctx, a, PermAdmin, func(q *store.Queries) error {
		p, err := resolvePipeline(ctx, q, a.WorkspaceID, ref)
		if err != nil {
			return err
		}
		if p.IsDefault {
			return invalid("pipeline", "the default pipeline cannot be archived; make another pipeline the default first")
		}
		if err := q.ArchivePipeline(ctx, store.ArchivePipelineParams{WorkspaceID: a.WorkspaceID, ID: p.ID}); err != nil {
			return err
		}
		return s.audit(ctx, q, a, "pipeline.archive", "pipeline", p.ID, nil)
	})
}

// isNoRows reports a missing row.
func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
