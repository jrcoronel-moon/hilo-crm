# Migrar desde HubSpot

Seguí primero el método común de [README.md](README.md). Acá está lo específico de HubSpot. Confirmá en
developers.hubspot.com la versión vigente de la API y los nombres exactos de los permisos.

`external_id`: `hubspot:<id>` (los ids de HubSpot son numéricos y únicos por tipo de objeto).

## Acceso

Un token de solo lectura de una app privada (o de servicio) de HubSpot, con permisos de lectura de empresas,
contactos, negocios (deals), propietarios (owners) y, si se migran emails, de emails de ventas. Se manda como
`Authorization: Bearer <token>` a `https://api.hubapi.com`.

## Qué leer

| Dato | Endpoint (CRM v3) |
|---|---|
| Empresas | `GET /crm/v3/objects/companies?properties=…&limit=100&after=…` |
| Contactos | `GET /crm/v3/objects/contacts?properties=…&associations=companies` |
| Negocios | `GET /crm/v3/objects/deals?properties=…&associations=companies,contacts` |
| Pipelines y etapas | `GET /crm/v3/pipelines/deals` |
| Propietarios | `GET /crm/v3/owners` (id → email) |
| Interacciones | `GET /crm/v3/objects/{emails,calls,meetings,notes,communications}?associations=…` |

Paginá con `after`. Para volúmenes grandes, usá los endpoints `batch/read`.

## Mapeo

| HubSpot | Hilo |
|---|---|
| company `name`, `domain`, `industry`, `country`, `website`, `description` | `upsert_company` con los mismos nombres |
| company `numberofemployees` | `size_range` (agrupá: 1-10, 11-50, 51-200, 201-1000, 1001-5000, 5000+) |
| contact `firstname`, `lastname`, `email`, `phone`, `jobtitle` | `first_name`, `last_name`, `email`, `phone`, `title` |
| contact asociado a empresa | `company` (dominio o id de Hilo) |
| deal `dealname`, `amount`, `deal_currency_code`, `closedate` | `title`, `amount`, `currency`, `expected_close_date` (solo la fecha) |
| deal `pipeline`, `dealstage` | `pipeline`, `stage` según el mapeo aprobado |
| deal `closed_lost_reason` | `lost_reason` |
| deal asociado a contactos | `primary_contact` (el primero) y `contacts` |
| `hubspot_owner_id` (en cualquier objeto) | `owner` = email del propietario |
| `hs_lead_status`, `lifecyclestage` | `tags` (si la persona lo quiere) |
| `createdate`, `closedate` de deals cerrados | `custom` (`origen_creado`, `origen_cerrado`) |

## Etapas

Cada etapa de `/crm/v3/pipelines/deals` trae `metadata.isClosed` y `metadata.probability`:

- `isClosed = "false"` → `in_progress` (la primera del pipeline → `initial`).
- `isClosed = "true"` y `probability = "1.0"` → `won`.
- `isClosed = "true"` y `probability = "0.0"` → `lost`.

## Interacciones

| HubSpot | `type` | `direction` | Fecha y texto |
|---|---|---|---|
| emails (`hs_email_direction`: `EMAIL` / `INCOMING_EMAIL`) | `email` | `outbound` / `inbound` | `hs_timestamp`, `hs_email_subject`, resumen de `hs_email_text` |
| calls (`hs_call_direction`) | `call` | `inbound` / `outbound` | `hs_timestamp`, `hs_call_body`, `hs_call_duration` (ms → minutos) |
| meetings | `meeting` | `outbound` | `hs_meeting_start_time`, `hs_meeting_title`, `hs_meeting_body` |
| notes | `note` | `internal` | `hs_timestamp`, `hs_note_body` |
| communications (WhatsApp, SMS, LinkedIn) | `message` o `social` | según el caso | `hs_timestamp`, `hs_communication_body` |

Vinculá cada interacción con la empresa, el contacto y la oportunidad asociados (ids de Hilo del mapa).

## Trampas

- Los cuerpos vienen en HTML: pasalos a texto y resumilos.
- Un negocio puede estar asociado a varias empresas: usá la principal (la primera asociación) y anotá las otras en
  `custom`.
- La API de búsqueda tiene un tope de resultados por consulta: para listar todo, usá los endpoints de listado
  paginados, no la búsqueda.
- Respetá los encabezados de rate limit; HubSpot devuelve 429 al excederlos.
