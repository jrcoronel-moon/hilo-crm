package crm

import (
	"context"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/jrcoronel-moon/hilo-crm/internal/store"
)

// MetricsInput selects the pipeline and period.
type MetricsInput struct {
	Pipeline  string `json:"pipeline,omitempty" jsonschema:"pipeline id or name (default pipeline if omitted)"`
	Days      int    `json:"days,omitempty" jsonschema:"look-back window for closed deals, leads and interactions; default 90, max 730"`
	StaleDays int    `json:"stale_days,omitempty" jsonschema:"an open opportunity is stale after this many days without activity; default 14"`
}

// Money is an amount per currency.
type Money map[string]float64

// StageMetrics is the open volume in one stage.
type StageMetrics struct {
	StageID  uuid.UUID `json:"stage_id"`
	Name     string    `json:"name"`
	Category string    `json:"category"`
	Count    int       `json:"count"`
	Amount   Money     `json:"amount"`
	Weighted Money     `json:"weighted"`
	AvgDays  float64   `json:"avg_days_in_stage"`
}

// Metrics is the pipeline dashboard.
type Metrics struct {
	PipelineID   uuid.UUID      `json:"pipeline_id"`
	PipelineName string         `json:"pipeline"`
	PeriodDays   int            `json:"period_days"`
	Stages       []StageMetrics `json:"stages"`
	OpenCount    int            `json:"open_count"`
	OpenAmount   Money          `json:"open_amount"`
	Weighted     Money          `json:"weighted_amount"`
	WonCount     int            `json:"won_count"`
	WonAmount    Money          `json:"won_amount"`
	LostCount    int            `json:"lost_count"`
	WinRate      float64        `json:"win_rate"` // won / (won + lost) in the period, 0..1
	AvgCycleDays float64        `json:"avg_won_cycle_days"`
	StaleCount   int            `json:"stale_count"`
	LeadsByChan  []ChannelCount `json:"new_by_channel"`
	Interactions []WeekCount    `json:"interactions_by_week"`
}

// ChannelCount is new opportunities per source channel.
type ChannelCount struct {
	Channel string `json:"channel"`
	Count   int    `json:"count"`
}

// WeekCount is interactions per week, type and direction.
type WeekCount struct {
	Week      string `json:"week"`
	Type      string `json:"type"`
	Direction string `json:"direction"`
	Count     int    `json:"count"`
}

func (m Money) add(cur string, minor int64) {
	if cur != "" && minor != 0 {
		m[cur] += MinorToAmount(minor)
	}
}

// Metrics computes the dashboard for one pipeline.
func (s *Service) Metrics(ctx context.Context, a Actor, in MetricsInput) (Metrics, error) {
	days := in.Days
	if days <= 0 {
		days = 90
	}
	staleDays := in.StaleDays
	if staleDays <= 0 {
		staleDays = 14
	}
	if days > 730 || staleDays > 3650 {
		return Metrics{}, invalid("days", "days max 730, stale_days max 3650")
	}
	now := s.now()
	since := now.AddDate(0, 0, -days)
	out := Metrics{PeriodDays: days, OpenAmount: Money{}, Weighted: Money{}, WonAmount: Money{}}
	err := s.tx(ctx, a, PermRead, func(q *store.Queries) error {
		ws := a.WorkspaceID
		pipe, err := resolvePipeline(ctx, q, ws, in.Pipeline)
		if err != nil {
			return err
		}
		out.PipelineID, out.PipelineName = pipe.ID, pipe.Name
		if err := fillStages(ctx, q, ws, pipe.ID, since, &out); err != nil {
			return err
		}
		closed, err := q.ClosedSummary(ctx, store.ClosedSummaryParams{WorkspaceID: ws, PipelineID: pipe.ID, Since: since})
		if err != nil {
			return err
		}
		var cycleSum float64
		for _, c := range closed {
			if c.Status == "won" {
				out.WonCount += int(c.Count)
				out.WonAmount.add(c.Currency, c.AmountMinor)
				cycleSum += c.AvgCycleDays * float64(c.Count)
			} else {
				out.LostCount += int(c.Count)
			}
		}
		if out.WonCount > 0 {
			out.AvgCycleDays = round1(cycleSum / float64(out.WonCount))
		}
		if total := out.WonCount + out.LostCount; total > 0 {
			out.WinRate = round3(float64(out.WonCount) / float64(total))
		}
		stale, err := q.CountStale(ctx, store.CountStaleParams{WorkspaceID: ws, PipelineID: pipe.ID, StaleBefore: now.AddDate(0, 0, -staleDays)})
		if err != nil {
			return err
		}
		out.StaleCount = int(stale)
		leads, err := q.LeadsByChannel(ctx, store.LeadsByChannelParams{WorkspaceID: ws, PipelineID: pipe.ID, Since: since})
		if err != nil {
			return err
		}
		out.LeadsByChan = make([]ChannelCount, 0, len(leads))
		for _, l := range leads {
			out.LeadsByChan = append(out.LeadsByChan, ChannelCount{Channel: l.Channel, Count: int(l.Count)})
		}
		weeks, err := q.InteractionsByWeek(ctx, store.InteractionsByWeekParams{WorkspaceID: ws, Since: since})
		if err != nil {
			return err
		}
		out.Interactions = make([]WeekCount, 0, len(weeks))
		for _, w := range weeks {
			out.Interactions = append(out.Interactions, WeekCount{Week: w.Week.Format(time.DateOnly), Type: w.Type, Direction: w.Direction, Count: int(w.Count)})
		}
		return nil
	})
	return out, err
}

func fillStages(ctx context.Context, q *store.Queries, ws, pipeID uuid.UUID, since time.Time, out *Metrics) error {
	rows, err := q.StageSummary(ctx, store.StageSummaryParams{WorkspaceID: ws, PipelineID: pipeID})
	if err != nil {
		return err
	}
	times, err := q.TimeInStage(ctx, store.TimeInStageParams{WorkspaceID: ws, PipelineID: pipeID, Since: since})
	if err != nil {
		return err
	}
	avg := map[uuid.UUID]float64{}
	for _, t := range times {
		avg[t.StageID] = round1(t.AvgDays)
	}
	idx := map[uuid.UUID]int{}
	for _, r := range rows {
		i, ok := idx[r.StageID]
		if !ok {
			i = len(out.Stages)
			idx[r.StageID] = i
			out.Stages = append(out.Stages, StageMetrics{StageID: r.StageID, Name: r.Name, Category: r.Category,
				Amount: Money{}, Weighted: Money{}, AvgDays: avg[r.StageID]})
		}
		st := &out.Stages[i]
		cur := deref(r.Currency)
		st.Count += int(r.Count)
		st.Amount.add(cur, r.AmountMinor)
		st.Weighted.add(cur, r.WeightedMinor)
		out.OpenCount += int(r.Count)
		out.OpenAmount.add(cur, r.AmountMinor)
		out.Weighted.add(cur, r.WeightedMinor)
	}
	return nil
}

func round1(f float64) float64 { return float64(int64(f*10+0.5)) / 10 }
func round3(f float64) float64 { return float64(int64(f*1000+0.5)) / 1000 }

// BoardColumn is one stage of the kanban board.
type BoardColumn struct {
	Stage StageView   `json:"stage"`
	Cards []BoardCard `json:"cards"`
	Total Money       `json:"total"`
}

// BoardCard is one opportunity on the board.
type BoardCard = store.BoardOpportunitiesRow

// Board returns the kanban board of a pipeline: one column per stage, open
// opportunities plus those closed in the last 30 days.
func (s *Service) Board(ctx context.Context, a Actor, pipelineRef string) (PipelineView, []BoardColumn, error) {
	var pv PipelineView
	var cols []BoardColumn
	err := s.tx(ctx, a, PermRead, func(q *store.Queries) error {
		ws := a.WorkspaceID
		pipe, err := resolvePipeline(ctx, q, ws, pipelineRef)
		if err != nil {
			return err
		}
		stages, err := q.ListStages(ctx, store.ListStagesParams{WorkspaceID: ws, PipelineID: pipe.ID})
		if err != nil {
			return err
		}
		cards, err := q.BoardOpportunities(ctx, store.BoardOpportunitiesParams{WorkspaceID: ws, PipelineID: pipe.ID})
		if err != nil {
			return err
		}
		pv = PipelineView{ID: pipe.ID, Name: pipe.Name, IsDefault: pipe.IsDefault}
		idx := map[uuid.UUID]int{}
		for i, st := range stages {
			pv.Stages = append(pv.Stages, stageView(st))
			cols = append(cols, BoardColumn{Stage: stageView(st), Cards: []BoardCard{}, Total: Money{}})
			idx[st.ID] = i
		}
		for _, c := range cards {
			if i, ok := idx[c.StageID]; ok {
				cols[i].Cards = append(cols[i].Cards, c)
				cols[i].Total.add(c.Currency, c.AmountMinor)
			}
		}
		return nil
	})
	return pv, cols, err
}

// SortedCurrencies returns m's currencies in a stable order (templates).
func SortedCurrencies(m Money) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
