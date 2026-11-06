package web

import (
	"encoding/csv"
	"io"
	"net/http"
	"time"

	"github.com/jrcoronel-moon/hilo-crm/internal/crm"
)

// Ajustes → Exportar: every company, contact or opportunity as a CSV file in
// the columns Importar reads, so it loads back as it is into another
// workspace. Admins only. A POST with the CSRF token, not a link: each export
// is audited, and another site must not be able to trigger one.

// exportKinds are the kinds offered, in the order to load them back.
var exportKinds = importKinds[:3]

var exportFiles = map[string]string{crm.KindCompany: "companies", crm.KindContact: "contacts", crm.KindOpportunity: "opportunities"}

// exportRecords is POST /settings/export.
func (h *Handler) exportRecords(w http.ResponseWriter, r *http.Request, v view) {
	if !v.IsAdmin() {
		h.fail(w, r, crm.ErrForbidden)
		return
	}
	kind := r.PostFormValue("kind")
	t, err := h.crm.ExportTable(r.Context(), v.Actor, kind)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.log.Info("csv export", "workspace", v.Current.ID, "user_id", v.Sess.UserID, "kind", kind, "rows", len(t.Rows))
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="hilo-`+exportFiles[kind]+"-"+time.Now().Format(time.DateOnly)+`.csv"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, "\uFEFF") // without the BOM, Excel misreads accents
	cw := csv.NewWriter(w)
	_ = cw.Write(t.Columns)
	_ = cw.WriteAll(t.Rows)
}
