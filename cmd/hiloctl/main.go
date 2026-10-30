// Command hiloctl is the operator CLI, run inside the web container:
//
//	docker compose exec web hiloctl workspace create "Acme" --owner ana@acme.com
//	docker compose exec web hiloctl token create acme ana@acme.com claude --read-only
//	docker compose exec web hiloctl --json member list acme
//
// Run it without arguments for the full command list. It uses the same
// DATABASE_URL (app role) as the service and acts as the operator: owner
// rights on any workspace, through the same rules as the web.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/jrcoronel-moon/hilo-crm/internal/db"
	"github.com/jrcoronel-moon/hilo-crm/internal/identity"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := start(ctx, os.Args[1:])
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func start(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errUsage
	}
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return errors.New("DATABASE_URL is required")
	}
	d, err := db.Open(ctx, url)
	if err != nil {
		return err
	}
	defer d.Close()
	return run(ctx, d, os.Stdout, args)
}

// command is one command, e.g. "token create" or "watch".
type command struct {
	name  string
	args  []string // positional arguments, for usage and arity
	flags []string // accepted flags besides --json; optional ones in brackets
	run   func(c *cli, in input) error
}

// commands is the whole CLI, in the order the usage lists it.
var commands = []command{
	{"workspace create", []string{"<name>"}, []string{"--owner <email>"}, workspaceCreate},
	{"workspace list", nil, nil, workspaceList},
	{"member list", []string{"<workspace>"}, nil, memberList},
	{"member add", []string{"<workspace>", "<email>", "<admin|member|viewer>"}, nil, memberAdd},
	{"member role", []string{"<workspace>", "<email>", "<owner|admin|member|viewer>"}, nil, memberRole},
	{"member remove", []string{"<workspace>", "<email>"}, nil, memberRemove},
	{"token create", []string{"<workspace>", "<email>", "<name>"}, []string{"[--read-only]", "[--ttl <days>d]"}, tokenCreate},
	{"token list", []string{"<workspace>", "<email>"}, nil, tokenList},
	{"token revoke", []string{"<workspace>", "<email>", "<token-id>"}, nil, tokenRevoke},
	{"client preapprove", []string{"<workspace>", "<client-id-url>"}, []string{"[--read-only]"}, clientPreapprove},
	{"client unapprove", []string{"<workspace>", "<client-id-url>"}, nil, clientUnapprove},
	{"client list", []string{"<workspace>"}, nil, clientList},
	{"superadmin grant", []string{"<email>"}, nil, superadminSet(true)},
	{"superadmin revoke", []string{"<email>"}, nil, superadminSet(false)},
	{"superadmin list", nil, nil, superadminList},
	{"domain allow", []string{"<domain>"}, nil, domainAllow},
	{"domain deny", []string{"<domain>"}, nil, domainDeny},
	{"domain list", nil, nil, domainList},
	{"watch", []string{"<workspace>"}, nil, watch},
}

var errUsage = errors.New(usage())

func usage() string {
	var b strings.Builder
	b.WriteString("usage: hiloctl [--json] <command>\n\n<workspace> is a slug or id.\n")
	for _, c := range commands {
		b.WriteString("\n  hiloctl " + strings.Join(append(append([]string{c.name}, c.args...), c.flags...), " "))
	}
	return b.String()
}

// valueFlags take the next argument as their value; the rest are booleans.
var valueFlags = map[string]bool{"owner": true, "ttl": true}

type input struct {
	pos   []string          // positional arguments after the command name
	flags map[string]string // name without dashes → value ("" for booleans)
}

// parse splits flags (anywhere) from the words: the command name and its
// positional arguments.
func parse(args []string) ([]string, input, error) {
	in := input{flags: map[string]string{}}
	var words []string
	for i := 0; i < len(args); i++ {
		name, ok := strings.CutPrefix(args[i], "--")
		if !ok {
			words = append(words, args[i])
			continue
		}
		if valueFlags[name] {
			if i+1 >= len(args) {
				return nil, in, fmt.Errorf("--%s needs a value", name)
			}
			i++
			in.flags[name] = args[i]
		} else {
			in.flags[name] = ""
		}
	}
	return words, in, nil
}

func (in input) has(flag string) bool {
	_, ok := in.flags[flag]
	return ok
}

func run(ctx context.Context, d *db.DB, out io.Writer, args []string) error {
	words, in, err := parse(args)
	if err != nil {
		return err
	}
	for _, cmd := range commands {
		n := len(strings.Fields(cmd.name))
		if len(words) < n || strings.Join(words[:n], " ") != cmd.name {
			continue
		}
		name := cmd.name
		in.pos = words[n:]
		if len(in.pos) != len(cmd.args) {
			return errUsage
		}
		for f := range in.flags {
			if f != "json" && !accepts(cmd, f) {
				return fmt.Errorf("%s does not take --%s", name, f)
			}
		}
		c := &cli{ctx: ctx, d: d, id: identity.New(d, slog.New(slog.DiscardHandler), nil), out: out, json: in.has("json")}
		return cmd.run(c, in)
	}
	return errUsage
}

func accepts(cmd command, flag string) bool {
	for _, f := range cmd.flags {
		if strings.Trim(strings.Fields(f)[0], "[]") == "--"+flag {
			return true
		}
	}
	return false
}

type cli struct {
	ctx  context.Context
	d    *db.DB
	id   *identity.Service
	out  io.Writer
	json bool
}

// emit prints v as indented JSON with --json, or as a table otherwise: one
// header row and one row per record, tab-separated cells.
func (c *cli) emit(v any, header string, rows [][]string) error {
	if c.json {
		e := json.NewEncoder(c.out)
		e.SetIndent("", "  ")
		return e.Encode(v)
	}
	var b strings.Builder
	b.WriteString(header + "\n")
	for _, r := range rows {
		b.WriteString(strings.Join(r, "\t") + "\n")
	}
	w := tabwriter.NewWriter(c.out, 0, 0, 2, ' ', 0)
	if _, err := io.WriteString(w, b.String()); err != nil {
		return err
	}
	return w.Flush()
}

// done reports a completed action: a sentence, or {"ok": true, ...} with --json.
func (c *cli) done(msg string, fields map[string]any) error {
	if !c.json {
		_, err := fmt.Fprintln(c.out, msg)
		return err
	}
	if fields == nil {
		fields = map[string]any{}
	}
	fields["ok"] = true
	return c.emit(fields, "", nil)
}
