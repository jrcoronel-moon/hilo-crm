package web

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jrcoronel-moon/hilo-crm/internal/crm"
	"github.com/jrcoronel-moon/hilo-crm/internal/httpx"
	"github.com/jrcoronel-moon/hilo-crm/internal/i18n"
)

// The header's search box: companies, contacts and opportunities at once,
// through the same crm.Search as the search_crm tool. GET /search renders the
// results page; with Accept: application/json it answers the quick list the
// box shows while typing.

type searchHit struct {
	Type     string `json:"type"`
	Label    string `json:"label"` // the type, in the page's language
	Title    string `json:"title"`
	Subtitle string `json:"subtitle,omitempty"`
	URL      string `json:"url"`
}

type searchGroup struct {
	Label string
	Icon  string
	Hits  []searchHit
}

type searchData struct {
	Q      string
	Groups []searchGroup
	Total  int
}

var searchKinds = []struct{ typ, path, label, icon string }{
	{"company", "/companies/", i18n.M("Empresas"), "building"},
	{"contact", "/contacts/", i18n.M("Contactos"), "users"},
	{"opportunity", "/opportunities/", i18n.M("Oportunidades"), "brief"},
}

var oppStatus = map[string]string{"open": i18n.M("abierta"), "won": i18n.M("ganada"), "lost": i18n.M("perdida")}

func (h *Handler) search(w http.ResponseWriter, r *http.Request, v view) {
	lang, q := h.lang(r), strings.TrimSpace(r.URL.Query().Get("q"))
	var found []crm.SearchResult
	if len([]rune(q)) >= 2 {
		var err error
		var ve *crm.ValidationError
		if found, err = h.crm.Search(r.Context(), v.Actor, q, 20); err != nil && !errors.As(err, &ve) {
			h.fail(w, r, err)
			return
		}
	}
	d := searchData{Q: q, Total: len(found)}
	var flat []searchHit
	for _, k := range searchKinds {
		g := searchGroup{Label: i18n.T(lang, k.label), Icon: k.icon}
		for _, f := range found {
			if f.Type != k.typ {
				continue
			}
			sub := f.Subtitle
			if s, ok := oppStatus[sub]; ok && k.typ == "opportunity" {
				sub = i18n.T(lang, s)
			}
			g.Hits = append(g.Hits, searchHit{Type: k.typ, Label: g.Label, Title: f.Title, Subtitle: sub, URL: k.path + f.ID.String()})
		}
		flat = append(flat, g.Hits...)
		if len(g.Hits) > 0 {
			d.Groups = append(d.Groups, g)
		}
	}
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		if flat == nil {
			flat = []searchHit{}
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"results": flat})
		return
	}
	h.render(w, r, http.StatusOK, "search", page{Title: i18n.M("Buscar"), Crumb: q, View: &v, Nav: "search", Data: d})
}
