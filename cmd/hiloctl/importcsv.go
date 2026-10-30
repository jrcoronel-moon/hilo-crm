package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/jrcoronel-moon/hilo-crm/internal/crm"
	"github.com/jrcoronel-moon/hilo-crm/internal/importer"
)

// importCSV loads one kind of record from a CSV on stdin, the terminal twin
// of Ajustes → Importar: columns are guessed from the header (--map fixes
// them), values normalised, and every record goes through the same upserts
// as bulk_upsert. Without --dry-run it saves; the mapping is always shown so
// the first run can be checked.
const csvMaxBytes = 256 << 20

func importCSV(c *cli, in input) error {
	a, err := c.operator(in.pos[0])
	if err != nil {
		return err
	}
	kind := in.pos[1]
	if !importer.ValidKind(kind) {
		return fmt.Errorf("kind must be one of %s", strings.Join(importer.Kinds, ", "))
	}
	t, err := importer.Parse(c.in, csvMaxBytes, crm.MaxImportFile)
	if err != nil {
		return fmt.Errorf("CSV on stdin: %w", err)
	}
	m := importer.Guess(kind, t.Header)
	if err := applyMap(t.Header, m, in.flags["map"]); err != nil {
		return err
	}
	opts := importer.Options{Source: in.flags["source"], DateOrder: in.flags["dates"], Defaults: map[string]string{}}
	if d := in.flags["dates"]; d != "" && d != "dmy" && d != "mdy" {
		return fmt.Errorf("--dates must be dmy or mdy")
	}
	for _, kv := range splitList(in.flags["set"]) {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return fmt.Errorf("--set wants field=value, got %q", kv)
		}
		opts.Defaults[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	opts.Tags = splitList(in.flags["tags"])
	if err := importer.Validate(kind, t.Header, m, opts.Defaults); err != nil {
		return fmt.Errorf("mapping: %w (use --map <Col=field,...>; fields: %s)", err, fieldKeys(kind))
	}
	built := importer.Build(kind, t, m, opts)
	built.Input.DryRun = in.has("dry-run")
	res := crm.ImportResult{DryRun: built.Input.DryRun}
	if len(built.Rows) > 0 {
		if res, err = c.crm.ImportFile(c.ctx, a, built.Input); err != nil {
			return err
		}
	}
	s := importer.Summarize(len(t.Rows), built, res)
	if c.json {
		return c.emit(struct {
			Mapping map[string]string `json:"mapping"`
			importer.Summary
		}{mappingByColumn(t.Header, m), s}, "", nil)
	}
	rows := make([][]string, 0, len(t.Header))
	for i, h := range t.Header {
		target := m[i]
		if target == "" {
			target = "(ignored)"
		}
		rows = append(rows, []string{h, target})
	}
	if err := c.emit(nil, "COLUMN\tFIELD", rows); err != nil {
		return err
	}
	mode := "imported"
	if s.DryRun {
		mode = "dry run (nothing saved)"
	}
	if _, err := fmt.Fprintf(c.out, "\n%s: %d rows, %d ok (%d new, %d updated), %d rejected, %d not converted\n",
		mode, s.Rows, s.OK, s.Created, s.Updated, s.Failed, s.Skipped); err != nil || len(s.Errors) == 0 {
		return err
	}
	errRows := make([][]string, 0, len(s.Errors))
	for _, e := range s.Errors {
		errRows = append(errRows, []string{strconv.Itoa(e.Row), e.Field, e.Error})
	}
	return c.emit(nil, "ROW\tFIELD\tERROR", errRows)
}

// applyMap overrides guessed targets: "Col=field,Other=-,Notes=custom". A
// column is named by its header (case-insensitive) or its 1-based number;
// "-" or an empty field ignores it.
func applyMap(header []string, m importer.Mapping, spec string) error {
	for _, kv := range splitList(spec) {
		col, target, ok := strings.Cut(kv, "=")
		if !ok {
			return fmt.Errorf("--map wants Column=field, got %q", kv)
		}
		idx := -1
		if n, err := strconv.Atoi(col); err == nil && n >= 1 && n <= len(header) {
			idx = n - 1
		}
		for i, h := range header {
			if idx < 0 && strings.EqualFold(strings.TrimSpace(h), strings.TrimSpace(col)) {
				idx = i
			}
		}
		if idx < 0 {
			return fmt.Errorf("--map: no column %q in the file", col)
		}
		if target = strings.TrimSpace(target); target == "-" {
			target = ""
		}
		m[idx] = target
	}
	return nil
}

func mappingByColumn(header []string, m importer.Mapping) map[string]string {
	out := make(map[string]string, len(header))
	for i, h := range header {
		out[h] = m[i]
	}
	return out
}

func fieldKeys(kind string) string {
	var keys []string
	for _, f := range importer.Fields(kind) {
		keys = append(keys, f.Key)
	}
	if kind != importer.KindInteraction {
		keys = append(keys, importer.Custom)
	}
	return strings.Join(keys, ", ")
}

func splitList(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
