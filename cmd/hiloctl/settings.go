package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/jrcoronel-moon/hilo-crm/internal/branding"
	"github.com/jrcoronel-moon/hilo-crm/internal/crm"
	"github.com/jrcoronel-moon/hilo-crm/internal/events"
	"github.com/jrcoronel-moon/hilo-crm/internal/store"
)

// Workspace configuration and platform branding, as the web settings and
// superadmin pages do. Writes go through the crm and branding services, and
// the audit log records them as "hiloctl".

// operator is the CLI acting in the workspace's CRM with every permission.
func (c *cli) operator(ref string) (crm.Actor, error) {
	ws, _, err := c.workspace(ref)
	if err != nil {
		return crm.Actor{}, err
	}
	return crm.NewActor(uuid.Nil, ws.ID, "owner", "system", "hiloctl", crm.AllScopes), nil
}

// ───── pipelines and channels ─────

func pipelineList(c *cli, in input) error {
	a, err := c.operator(in.pos[0])
	if err != nil {
		return err
	}
	pipes, err := c.crm.ListPipelines(c.ctx, a)
	if err != nil {
		return err
	}
	rows := make([][]string, 0, len(pipes))
	for _, p := range pipes {
		stages := make([]string, 0, len(p.Stages))
		for _, st := range p.Stages {
			stages = append(stages, st.Name+" ("+st.Category+")")
		}
		goal := "-"
		if p.MonthlyTarget > 0 {
			goal = strconv.FormatFloat(p.MonthlyTarget, 'f', -1, 64) + " " + p.TargetCurrency
		}
		def := ""
		if p.IsDefault {
			def = "default"
		}
		rows = append(rows, []string{p.Name, def, goal, strings.Join(stages, " → ")})
	}
	return c.emit(pipes, "NAME\tDEFAULT\tMONTHLY GOAL\tSTAGES", rows)
}

// pipelineApply creates or reconfigures a pipeline from JSON on stdin, in the
// shape of the configure_pipeline tool (see `hiloctl --json pipeline list`).
func pipelineApply(c *cli, in input) error {
	a, err := c.operator(in.pos[0])
	if err != nil {
		return err
	}
	var p crm.PipelineInput
	dec := json.NewDecoder(io.LimitReader(c.in, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return fmt.Errorf("pipeline JSON on stdin: %w", err)
	}
	v, err := c.crm.ConfigurePipeline(c.ctx, a, p)
	if err != nil {
		return err
	}
	return c.done(fmt.Sprintf("pipeline %s saved with %d stages", v.Name, len(v.Stages)), map[string]any{"pipeline": v})
}

func channelList(c *cli, in input) error {
	a, err := c.operator(in.pos[0])
	if err != nil {
		return err
	}
	chans, err := c.crm.ListChannels(c.ctx, a)
	if err != nil {
		return err
	}
	rows := make([][]string, 0, len(chans))
	for _, ch := range chans {
		rows = append(rows, []string{ch.Name, ch.Kind, ch.ID.String()})
	}
	return c.emit(chans, "NAME\tKIND\tID", rows)
}

func channelAdd(c *cli, in input) error {
	a, err := c.operator(in.pos[0])
	if err != nil {
		return err
	}
	v, err := c.crm.UpsertChannel(c.ctx, a, crm.ChannelInput{Name: in.pos[1], Kind: in.pos[2]})
	if err != nil {
		return err
	}
	return c.done(fmt.Sprintf("channel %s (%s) saved", v.Name, v.Kind), map[string]any{"channel": v})
}

// ───── audit ─────

func auditList(c *cli, in input) error {
	a, err := c.operator(in.pos[0])
	if err != nil {
		return err
	}
	limit := 50
	if v, ok := in.flags["limit"]; ok {
		if limit, err = strconv.Atoi(v); err != nil || limit < 1 {
			return fmt.Errorf("--limit must be a positive number")
		}
	}
	entries, err := c.crm.ListAudit(c.ctx, a, "", limit)
	if err != nil {
		return err
	}
	rows := make([][]string, 0, len(entries))
	for _, e := range entries {
		ev := events.FromAudit(store.GetAuditEntryRow(e))
		rows = append(rows, []string{ev.At.Local().Format(time.DateTime), ev.Actor, ev.Via, ev.Summary})
	}
	return c.emit(entries, "WHEN\tWHO\tVIA\tWHAT", rows)
}

// ───── branding (platform-wide) ─────

func (c *cli) branding() *branding.Service { return branding.New(c.d) }

func brandingShow(c *cli, _ input) error {
	b := c.branding().Get(c.ctx)
	logo := "default"
	if b.HasLogo {
		logo = "custom"
	}
	return c.emit(map[string]any{"name": b.Name, "custom_logo": b.HasLogo}, "NAME\tLOGO", [][]string{{b.Name, logo}})
}

func brandingName(c *cli, in input) error {
	if err := c.branding().SetName(c.ctx, true, uuid.Nil, in.pos[0]); err != nil {
		return err
	}
	return c.done("application name: "+in.pos[0], nil)
}

// brandingLogo reads the image from stdin; its type is checked by content.
func brandingLogo(c *cli, _ input) error {
	data, err := io.ReadAll(io.LimitReader(c.in, branding.MaxLogo+1))
	if err != nil {
		return err
	}
	if err := c.branding().SetLogo(c.ctx, true, uuid.Nil, data); err != nil {
		return err
	}
	return c.done(fmt.Sprintf("logo updated (%d bytes)", len(data)), nil)
}

func brandingResetLogo(c *cli, _ input) error {
	if err := c.branding().ClearLogo(c.ctx, true, uuid.Nil); err != nil {
		return err
	}
	return c.done("logo reset to the default", nil)
}
