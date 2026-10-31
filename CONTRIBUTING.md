# Cómo contribuir

1. `make hooks` una vez: el pre-commit corre formato, búsqueda de secretos en lo staged y lint.
2. `make test` levanta un Postgres descartable (puerto 54329) y corre todos los tests con `-race`.
3. `make check` antes de abrir un PR: es exactamente lo que corre la CI.

Reglas:

- **SQL solo en `internal/db/queries`** (sqlc). Nada de SQL armado con strings. Después de cambiar consultas o
  migraciones: `make gen` y commiteá `internal/store`.
- **Reglas de negocio solo en `internal/crm`**: MCP, web y formularios son adaptadores finos.
- **Toda consulta del CRM corre en `db.InWorkspace`** y filtra por `workspace_id`. Un cambio que toque datos de
  tenants necesita un test de aislamiento.
- **Las migraciones nunca se editan una vez publicadas**: se agrega una nueva.
- Los mensajes de error que ve el usuario o el agente no exponen detalles internos; esos van al log.
- `//nolint` solo con el linter concreto y la razón.
- Los commits mencionan la asistencia de IA cuando la hubo, con un trailer `Assisted-by:`.
- **Textos de la interfaz traducibles**: en las plantillas, `{{t "Texto"}}` (o `{{t "Archivar %s?" .Name}}` con
  valores); en Go, `i18n.M("Texto")` para lo que se muestra al renderizar o `i18n.T(lang, "Texto")` para traducir en
  el momento. El texto en español es la clave. Cada mensaje nuevo necesita su traducción en
  `internal/i18n/locales/{en,fr,it,de,pt}.json`: `go test ./internal/i18n` falla si falta alguna, si cambian los `%s`
  o `%d`, o si queda una entrada sin uso.
