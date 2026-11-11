package web

import (
	"encoding/json"
	"html/template"

	"github.com/jrcoronel-moon/hilo-crm/internal/i18n"
)

// jsMessages are the strings app.js shows. Each page carries them already
// translated (a JSON data block, which the CSP allows: it never runs), and
// app.js looks them up by their Spanish text.
var jsMessages = []string{
	i18n.M("¿Confirmar?"), i18n.M("Copiado"), i18n.M("%s (se elimina)"), i18n.M("No se pudo mover la oportunidad."), i18n.M("Deshacer"),
	i18n.M("Escribí el motivo de pérdida antes de marcar."), i18n.M("Escribí el motivo de pérdida."),
	i18n.M("Abrir el menú"), i18n.M("Cerrar el menú"), i18n.M("Quitar %s"),
	i18n.M("%s hizo %d cambios"), i18n.M("%d cambios de %d personas o agentes"),
	i18n.M("Sin resultados para “%s”"), i18n.M("Ver todos los resultados"),
}

var jsCache = func() map[string]template.JS {
	out := map[string]template.JS{}
	for _, lang := range i18n.Supported {
		m := make(map[string]string, len(jsMessages))
		for _, msg := range jsMessages {
			m[msg] = i18n.T(lang, msg)
		}
		raw, _ := json.Marshal(m)
		out[lang] = template.JS(raw) //nolint:gosec // G203: JSON from our own catalogs, marshalled (escapes </script>)
	}
	return out
}()

func jsStrings(lang string) template.JS { return jsCache[lang] }
