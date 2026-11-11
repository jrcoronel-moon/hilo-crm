package web

import (
	"net/http"
	"slices"
	"strconv"

	"github.com/jrcoronel-moon/hilo-crm/internal/crm"
)

// Web tables: sort by a column (?sort=&dir=), filters in the toolbar, offset
// paging (?offset=). Every link keeps the rest of the query, so sorting,
// filtering and paging combine.

// tableOptions reads the sort and offset of a table whose sortable columns are cols.
func tableOptions(r *http.Request, cols ...string) crm.TableOptions {
	q := r.URL.Query()
	var t crm.TableOptions
	if s := q.Get("sort"); slices.Contains(cols, s) {
		t.Sort, t.Desc = s, q.Get("dir") == "desc"
	}
	t.Offset, _ = strconv.Atoi(q.Get("offset"))
	return t
}

// sortCol is a sortable header: where a click goes and the current direction.
type sortCol struct{ URL, Dir string }

// sortLinks cycles each column: ascending, descending, back to the default.
func sortLinks(r *http.Request, t crm.TableOptions, cols ...string) map[string]sortCol {
	out := make(map[string]sortCol, len(cols))
	for _, c := range cols {
		q := r.URL.Query()
		q.Del("offset")
		q.Del("cursor")
		q.Del("ok")
		dir := ""
		switch {
		case t.Sort == c && !t.Desc:
			dir = "asc"
			q.Set("sort", c)
			q.Set("dir", "desc")
		case t.Sort == c:
			dir = "desc"
			q.Del("sort")
			q.Del("dir")
		default:
			q.Set("sort", c)
			q.Del("dir")
		}
		out[c] = sortCol{URL: "?" + q.Encode(), Dir: dir}
	}
	return out
}

// pager holds the previous and next page links of an offset-paged table.
type pager struct{ Prev, Next string }

func pagerFor(r *http.Request, t crm.TableOptions, shown, nextOffset int) pager {
	at := func(off int) string {
		q := r.URL.Query()
		q.Del("cursor")
		q.Del("ok")
		if off > 0 {
			q.Set("offset", strconv.Itoa(off))
		} else {
			q.Del("offset")
		}
		return "?" + q.Encode()
	}
	var p pager
	if t.Offset > 0 {
		p.Prev = at(max(0, t.Offset-max(shown, 50)))
	}
	if nextOffset > 0 {
		p.Next = at(nextOffset)
	}
	return p
}

// without is the current URL minus some query keys (a filter chip's ×).
func without(r *http.Request, keys ...string) string {
	q := r.URL.Query()
	for _, k := range append(keys, "offset", "cursor", "ok") {
		q.Del(k)
	}
	if len(q) == 0 {
		return r.URL.Path
	}
	return r.URL.Path + "?" + q.Encode()
}

// thData feeds the "th" partial: a header that sorts its column.
type thData struct {
	Label, Class, URL, Dir, Aria string
}

func th(links map[string]sortCol, col, label, class string) thData {
	l := links[col]
	aria := map[string]string{"asc": "ascending", "desc": "descending"}[l.Dir]
	return thData{Label: label, Class: class, URL: l.URL, Dir: l.Dir, Aria: aria}
}
