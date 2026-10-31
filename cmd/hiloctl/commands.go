package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/jrcoronel-moon/hilo-crm/internal/events"
	"github.com/jrcoronel-moon/hilo-crm/internal/i18n"
	"github.com/jrcoronel-moon/hilo-crm/internal/identity"
	"github.com/jrcoronel-moon/hilo-crm/internal/store"
)

const day = 24 * time.Hour

func date(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.Format(time.DateOnly)
}

// workspace resolves the <workspace> argument and returns the operator on it.
func (c *cli) workspace(ref string) (store.Workspace, identity.Caller, error) {
	ws, err := c.id.FindWorkspace(c.ctx, ref)
	if err != nil {
		return ws, identity.Caller{}, fmt.Errorf("workspace %q: %w", ref, err)
	}
	return ws, identity.Operator(ws.ID), nil
}

// member resolves "<workspace> <email>" to that member, as a Caller.
func (c *cli) member(ref, email string) (identity.Caller, error) {
	ws, _, err := c.workspace(ref)
	if err != nil {
		return identity.Caller{}, err
	}
	m, err := c.id.MemberCaller(c.ctx, ws.ID, email)
	if err != nil {
		return m, fmt.Errorf("%s in %s: %w", email, ws.Slug, err)
	}
	return m, nil
}

// ───── workspaces ─────

func workspaceCreate(c *cli, in input) error {
	owner, ok := in.flags["owner"]
	if !ok {
		return fmt.Errorf("workspace create needs --owner <email>")
	}
	u, err := c.id.EnsureUser(c.ctx, owner)
	if err != nil {
		return err
	}
	lang := in.flags["lang"]
	if lang == "" {
		lang = "es"
	}
	if !i18n.Valid(lang) {
		return fmt.Errorf("--lang must be one of %s", strings.Join(i18n.Supported, ", "))
	}
	ws, err := c.id.CreateWorkspaceIn(c.ctx, u.ID, in.pos[0], lang)
	if err != nil {
		return err
	}
	return c.done(fmt.Sprintf("created workspace %s (%s), owner %s", ws.Slug, ws.ID, u.Email),
		map[string]any{"id": ws.ID, "slug": ws.Slug, "name": ws.Name, "owner": u.Email})
}

func workspaceList(c *cli, _ input) error {
	all, err := c.id.AllWorkspaces(c.ctx, true)
	if err != nil {
		return err
	}
	var live []store.ListAllWorkspacesRow
	rows := make([][]string, 0, len(all))
	for _, w := range all {
		if w.ArchivedAt == nil {
			live = append(live, w)
			rows = append(rows, []string{w.Slug, w.Name, strconv.Itoa(int(w.MemberCount)), date(&w.CreatedAt), w.ID.String()})
		}
	}
	return c.emit(live, "SLUG\tNAME\tMEMBERS\tCREATED\tID", rows)
}

// ───── members ─────

func memberList(c *cli, in input) error {
	_, op, err := c.workspace(in.pos[0])
	if err != nil {
		return err
	}
	members, err := c.id.Members(c.ctx, op)
	if err != nil {
		return err
	}
	invites, err := c.id.Invitations(c.ctx, op)
	if err != nil {
		return err
	}
	rows := make([][]string, 0, len(members)+len(invites))
	for _, m := range members {
		rows = append(rows, []string{m.Email, m.Role, "member", m.Name})
	}
	for _, i := range invites {
		rows = append(rows, []string{i.Email, i.Role, "invited until " + date(&i.ExpiresAt), ""})
	}
	return c.emit(map[string]any{"members": members, "invitations": invites}, "EMAIL\tROLE\tSTATUS\tNAME", rows)
}

// memberAdd adds the user now if they ever signed in, or invites them.
func memberAdd(c *cli, in input) error {
	ws, op, err := c.workspace(in.pos[0])
	if err != nil {
		return err
	}
	if err := c.id.Invite(c.ctx, op, in.pos[1], in.pos[2]); err != nil {
		return err
	}
	status := "added"
	if _, err := c.id.MemberCaller(c.ctx, ws.ID, in.pos[1]); err != nil {
		status = "invited"
	}
	return c.done(fmt.Sprintf("%s %s to %s as %s", status, in.pos[1], ws.Slug, in.pos[2]),
		map[string]any{"status": status})
}

func memberRole(c *cli, in input) error {
	m, err := c.member(in.pos[0], in.pos[1])
	if err != nil {
		return err
	}
	if err := c.id.SetRole(c.ctx, identity.Operator(m.WorkspaceID), m.UserID, in.pos[2]); err != nil {
		return err
	}
	return c.done(fmt.Sprintf("%s is now %s", in.pos[1], in.pos[2]), nil)
}

func memberRemove(c *cli, in input) error {
	m, err := c.member(in.pos[0], in.pos[1])
	if err != nil {
		return err
	}
	if err := c.id.RemoveMember(c.ctx, identity.Operator(m.WorkspaceID), m.UserID); err != nil {
		return err
	}
	return c.done("removed "+in.pos[1], nil)
}

// ───── personal access tokens ─────

// tokenCreate issues a PAT for a member. The plaintext is shown once.
func tokenCreate(c *cli, in input) error {
	ttl, err := parseTTL(in.flags["ttl"])
	if err != nil {
		return err
	}
	m, err := c.member(in.pos[0], in.pos[1])
	if err != nil {
		return err
	}
	tok, row, err := c.id.CreatePAT(c.ctx, m, in.pos[2], in.has("read-only"), ttl)
	if err != nil {
		return err
	}
	return c.done(fmt.Sprintf("token %s (%s, scope %s, expires %s). Shown only once:\n%s", row.ID, row.Name, row.Scope, date(row.ExpiresAt), tok),
		map[string]any{"token": tok, "id": row.ID, "name": row.Name, "scope": row.Scope, "expires_at": row.ExpiresAt})
}

// parseTTL accepts whole days, 1-365 ("90d"); empty means no expiry.
func parseTTL(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
	if err != nil || !strings.HasSuffix(s, "d") || n < 1 || n > 365 {
		return 0, fmt.Errorf("--ttl must be whole days between 1d and 365d, got %q", s)
	}
	return time.Duration(n) * day, nil
}

func tokenList(c *cli, in input) error {
	m, err := c.member(in.pos[0], in.pos[1])
	if err != nil {
		return err
	}
	pats, err := c.id.PATs(c.ctx, m)
	if err != nil {
		return err
	}
	rows := make([][]string, 0, len(pats))
	for _, p := range pats {
		rows = append(rows, []string{p.ID.String(), p.Name, p.Prefix + "…", p.Scope, date(p.ExpiresAt), date(p.LastUsedAt)})
	}
	return c.emit(pats, "ID\tNAME\tPREFIX\tSCOPE\tEXPIRES\tLAST USED", rows)
}

func tokenRevoke(c *cli, in input) error {
	id, err := uuid.Parse(in.pos[2])
	if err != nil {
		return fmt.Errorf("token id: %w", err)
	}
	m, err := c.member(in.pos[0], in.pos[1])
	if err != nil {
		return err
	}
	if err := c.id.RevokePAT(c.ctx, m, id); err != nil {
		return err
	}
	return c.done("revoked token "+id.String(), nil)
}

// ───── preapproved agents ─────

func clientPreapprove(c *cli, in input) error {
	ws, op, err := c.workspace(in.pos[0])
	if err != nil {
		return err
	}
	if err := c.id.Preapprove(c.ctx, op, in.pos[1], in.has("read-only")); err != nil {
		return err
	}
	return c.done(fmt.Sprintf("preapproved %s in %s: its users skip the consent screen", in.pos[1], ws.Slug), nil)
}

func clientUnapprove(c *cli, in input) error {
	_, op, err := c.workspace(in.pos[0])
	if err != nil {
		return err
	}
	if err := c.id.Unpreapprove(c.ctx, op, in.pos[1]); err != nil {
		return err
	}
	return c.done("no longer preapproved: "+in.pos[1], nil)
}

func clientList(c *cli, in input) error {
	_, op, err := c.workspace(in.pos[0])
	if err != nil {
		return err
	}
	list, err := c.id.Preapprovals(c.ctx, op)
	if err != nil {
		return err
	}
	rows := make([][]string, 0, len(list))
	for _, p := range list {
		rows = append(rows, []string{p.ClientID, p.Scope, date(&p.CreatedAt)})
	}
	return c.emit(list, "CLIENT\tSCOPE\tSINCE", rows)
}

// ───── live activity ─────

// watch prints every change in a workspace as it happens, until Ctrl-C.
func watch(c *cli, in input) error {
	ws, _, err := c.workspace(in.pos[0])
	if err != nil {
		return err
	}
	if !c.json {
		if _, err := fmt.Fprintf(c.out, "watching %s (Ctrl-C to stop)\n", ws.Slug); err != nil {
			return err
		}
	}
	enc := json.NewEncoder(c.out)
	events.Listen(c.ctx, c.d, slog.New(slog.NewTextHandler(os.Stderr, nil)), func(e events.Event) {
		if e.Workspace != ws.ID {
			return
		}
		if c.json {
			_ = enc.Encode(e)
			return
		}
		_, _ = fmt.Fprintf(c.out, "%s  %s  %-40s %s  [%s]\n", e.At.Local().Format(time.TimeOnly), ws.Slug, e.Who(), e.Summary, e.Via)
	})
	return nil
}

// ───── platform ─────

func superadminSet(on bool) func(*cli, input) error {
	return func(c *cli, in input) error {
		if err := c.id.SetSuperadmin(c.ctx, in.pos[0], on); err != nil {
			return err
		}
		return c.done(fmt.Sprintf("superadmin %t: %s", on, in.pos[0]), nil)
	}
}

func superadminList(c *cli, _ input) error {
	users, err := c.d.Platform().ListSuperadmins(c.ctx)
	if err != nil {
		return err
	}
	rows := make([][]string, 0, len(users))
	emails := make([]string, 0, len(users))
	for _, u := range users {
		rows = append(rows, []string{u.Email})
		emails = append(emails, u.Email)
	}
	return c.emit(emails, "EMAIL", rows)
}

func domainAllow(c *cli, in input) error {
	if err := c.id.AddPlatformDomain(c.ctx, true, uuid.Nil, in.pos[0]); err != nil {
		return err
	}
	return c.done("allowed: "+in.pos[0], nil)
}

func domainDeny(c *cli, in input) error {
	if err := c.id.RemovePlatformDomain(c.ctx, true, in.pos[0]); err != nil {
		return err
	}
	return c.done("removed: "+in.pos[0], nil)
}

func domainList(c *cli, _ input) error {
	doms, err := c.id.PlatformDomains(c.ctx, true)
	if err != nil {
		return err
	}
	rows := make([][]string, 0, len(doms))
	names := make([]string, 0, len(doms))
	for _, d := range doms {
		rows = append(rows, []string{d.Domain})
		names = append(names, d.Domain)
	}
	return c.emit(names, "DOMAIN", rows)
}
