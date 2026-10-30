# Migrar desde Pipedrive

Seguí primero el método común de [README.md](README.md). Acá está lo específico de Pipedrive. Confirmá en
developers.pipedrive.com qué endpoints están en v1 y cuáles en v2.

`external_id`: `pipedrive:<tipo>:<id>` (`pipedrive:deal:123`): los ids de Pipedrive se repiten entre tipos.

## Acceso

El token personal de API (Configuración personal → API). En v2 va como encabezado `x-api-token`; en v1, como
parámetro `api_token`. Base: `https://<empresa>.pipedrive.com/api/`.

## Qué leer

| Dato | Endpoint |
|---|---|
| Organizaciones | `/organizations` |
| Personas | `/persons` |
| Negocios (todos los estados) | `/deals` (pedí abiertos, ganados y perdidos; por defecto no vienen todos) |
| Pipelines y etapas | `/pipelines`, `/stages` |
| Usuarios | `/users` (id → email) |
| Actividades | `/activities` |
| Notas | `/notes` |
| Campos personalizados | `/organizationFields`, `/personFields`, `/dealFields` |

## Mapeo

| Pipedrive | Hilo |
|---|---|
| organization `name`, `address_country` | `name`, `country` |
| organization: sitio web (suele ser campo personalizado) | `website`, `domain` |
| person `first_name`, `last_name`, `job_title` | `first_name`, `last_name`, `title` |
| person `emails` / `phones` (listas) | el marcado `primary` → `email` / `phone` |
| person `org_id` | `company` |
| deal `title`, `value`, `currency`, `expected_close_date`, `probability` | `title`, `amount`, `currency`, `expected_close_date`, `probability` |
| deal `stage_id`, `status`, `lost_reason` | `stage` (ver abajo), `lost_reason` |
| deal `person_id` + participantes | `primary_contact`, `contacts` |
| `owner_id` / `user_id` → email | `owner` |
| deal `label`s | `tags` |
| deal `add_time`; `won_time` o `lost_time` | `created_at`; `closed_at` |

Los campos personalizados vienen con claves de 40 caracteres (hash): traducilas con `/…Fields` y guardalas en
`custom` con su nombre legible.

## Etapas

En Pipedrive todas las etapas son "en curso": ganado y perdido son el **estado** del negocio, no una etapa.

- Primera etapa del pipeline → `initial`; el resto → `in_progress`.
- Agregá al pipeline de Hilo una etapa `won` (por ejemplo "Ganado") y una `lost` ("Perdido").
- `status = open` → la etapa mapeada; `won` → "Ganado"; `lost` → "Perdido" con `lost_reason`. En el caso de
  perdidos, guardá la etapa donde se perdió en `custom.origen_etapa`.

## Interacciones

| Pipedrive | `type` | `direction` |
|---|---|---|
| activity `call` (hecha) | `call` | `outbound` salvo que el asunto diga lo contrario |
| activity `meeting` (hecha) | `meeting` | `outbound` |
| activity `email` (hecha) | `email` | `outbound` |
| notas | `note` | `internal` |

Fecha: `due_date` + `due_time` (o `marked_as_done_time`); duración: `duration`; texto: `subject` y `note`. Las
actividades pendientes no son interacciones: la próxima de cada negocio va en `next_step` / `next_step_at`.

## Trampas

- Las organizaciones no tienen dominio por defecto. No lo deduzcas del email de una persona si es un proveedor
  gratuito.
- Los textos de notas vienen en HTML: pasalos a texto.
- Respetá el rate limit (Pipedrive devuelve 429 y encabezados con el cupo restante).
