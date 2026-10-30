# Migrar desde Close

Seguí primero el método común de [README.md](README.md). Acá está lo específico de Close.

`external_id`: `close:<id>` (los ids de Close ya llevan prefijo de tipo: `lead_…`, `cont_…`, `oppo_…`, `acti_…`).

## Acceso

Una API key (Settings → Developer → API Keys), por HTTP Basic: la key como usuario y contraseña vacía. Base:
`https://api.close.com/api/v1/`. Paginá con `_skip` / `_limit`.

## Qué leer

| Dato | Endpoint |
|---|---|
| Leads (= empresas, con sus contactos) | `/lead/` |
| Contactos | `/contact/` |
| Oportunidades | `/opportunity/` |
| Pipelines y estados de oportunidad | `/pipeline/`, `/status/opportunity/` |
| Usuarios | `/user/` (id → email) |
| Actividades | `/activity/email/`, `/activity/call/`, `/activity/meeting/`, `/activity/note/`, `/activity/sms/` |

## Mapeo

| Close | Hilo |
|---|---|
| lead `display_name`, `url`, `description` | empresa `name`, `website` (+ `domain`), `description` |
| lead `status_label` | `tags` (es el estado del lead, no de una oportunidad) |
| contact `name`, `title`, `emails[0].email`, `phones[0].phone` | `first_name` + `last_name` (separá el nombre), `title`, `email`, `phone` |
| opportunity `note`, `value`, `value_currency`, `confidence`, `contact_id`, `user_id` | `title`/`description`, `amount`, `currency`, `probability`, `primary_contact`, `owner` |
| opportunity `status_id` | `stage` según el mapeo |
| opportunity `date_created`; `date_won` (o `date_lost`) | `created_at`; `closed_at` |

Las oportunidades de Close no tienen título propio: usá `"<lead> – <nota corta>"` como `title`.

## Etapas

Cada estado de oportunidad tiene `type`: `active` → `in_progress` (el primero → `initial`); `won` → `won`;
`lost` → `lost`.

## Interacciones

| Close | `type` | `direction` |
|---|---|---|
| email (`direction`: incoming / outgoing) | `email` | `inbound` / `outbound` |
| call (`direction`: inbound / outbound; `duration` en segundos) | `call` | `inbound` / `outbound` |
| meeting | `meeting` | `outbound` |
| note | `note` | `internal` |
| sms | `message` | según `direction` |

## Trampas

- **`value` viene en centavos**: dividí por 100.
- `value_period` puede ser `monthly` o `annual`: preguntale a la persona si se carga el valor tal cual o
  anualizado, y anotá el período en `custom`.
