# Migrar desde Odoo CRM

Seguí primero el método común de [README.md](README.md). Acá está lo específico de Odoo. Confirmá en la
documentación de tu versión de Odoo qué API externa usar (XML-RPC/JSON-RPC o la API JSON más nueva).

`external_id`: `odoo:<modelo>:<id>` (`odoo:crm.lead:42`): los ids se repiten entre modelos.

## Acceso

Usuario + API key (Preferencias → Seguridad de la cuenta → Nueva API key) de un usuario con lectura del CRM. Con la
API externa: autenticá contra `/xmlrpc/2/common` y leé con `execute_kw` sobre `/xmlrpc/2/object`, usando
`search_read` con `fields`, `limit` y `offset`.

## Qué leer

| Dato | Modelo y filtro |
|---|---|
| Empresas | `res.partner` con `is_company = True` |
| Contactos | `res.partner` con `is_company = False` |
| Oportunidades | `crm.lead` con `type = 'opportunity'` (incluí las archivadas: contexto `active_test: False`) |
| Etapas | `crm.stage` |
| Motivos de pérdida | `crm.lost.reason` |
| Usuarios | `res.users` (`login` / email) |
| Historial | `mail.message` con `model = 'crm.lead'` y `message_type` en `email` o `comment` |
| Leads sin calificar | `crm.lead` con `type = 'lead'` (preguntale a la persona si entran) |

## Mapeo

| Odoo | Hilo |
|---|---|
| partner empresa `name`, `website`, `country_id`, `industry_id`, `comment` | `name`, `website` (+ `domain`), `country`, `industry`, `description` |
| partner contacto `name`, `email`, `phone`, `function`, `parent_id` | `first_name` + `last_name`, `email`, `phone`, `title`, `company` |
| lead `name`, `expected_revenue`, `probability`, `date_deadline`, `description` | `title`, `amount`, `probability`, `expected_close_date`, `description` |
| lead `partner_id` | `company` (o `primary_contact`, si es una persona con empresa) |
| lead `stage_id`, `lost_reason_id` | `stage`, `lost_reason` |
| lead `user_id` → email | `owner` |
| lead `tag_ids`, `source_id`, `medium_id` | `tags`, `source_channel` |
| lead `create_date`, `date_closed` | `custom` (`origen_creado`, `origen_cerrado`) |

## Etapas

- `crm.stage` con `is_won = True` → `won`.
- El resto, por `sequence` → la primera `initial`, las demás `in_progress`.
- Odoo no tiene etapa perdida: una oportunidad perdida queda **archivada** (`active = False`) con
  `lost_reason_id`. Agregá una etapa `lost` ("Perdido") en Hilo y mandá ahí las archivadas con motivo; guardá la
  etapa donde se perdió en `custom.origen_etapa`.
- Si hay varios equipos de venta con etapas propias (`team_id`), proponé un pipeline por equipo.

## Interacciones

| Odoo | `type` | `direction` |
|---|---|---|
| `mail.message` `message_type = 'email'` | `email` | `inbound` si el autor no es un usuario interno; si no, `outbound` |
| `mail.message` `message_type = 'comment'` (nota interna) | `note` | `internal` |

Usá `date` como `occurred_at` y `body` (HTML → texto) como resumen. Omití los mensajes de seguimiento automáticos
(cambios de campos, notificaciones). Las actividades pendientes (`mail.activity`) van en `next_step` /
`next_step_at`.
