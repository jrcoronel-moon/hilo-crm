# Migrar desde Zoho CRM

Seguí primero el método común de [README.md](README.md). Acá está lo específico de Zoho CRM. Confirmá en la
documentación de Zoho la versión vigente de la API (`/crm/v{n}/`).

`external_id`: `zoho:<id>`.

## Acceso

OAuth con un "Self Client" de la consola de APIs de Zoho, con permisos de solo lectura de módulos
(`ZohoCRM.modules.READ`), configuración (`ZohoCRM.settings.READ`) y usuarios (`ZohoCRM.users.READ`). Usá el dominio
del centro de datos de la cuenta (`zohoapis.com`, `.eu`, `.in`, `.com.au`…). Encabezado:
`Authorization: Zoho-oauthtoken <token>`.

## Qué leer

| Dato | Módulo |
|---|---|
| Empresas | `Accounts` |
| Contactos | `Contacts` |
| Oportunidades | `Deals` |
| Etapas y pipelines | configuración de pipelines / valores de `Stage` |
| Usuarios | `users` (id → email) |
| Interacciones | `Calls`, `Events` (reuniones), `Notes` |
| Leads sin convertir | `Leads` (preguntale a la persona si entran) |

`GET /crm/v{n}/<Módulo>?fields=…&per_page=200`, paginando con `page` y, pasados los primeros miles, con
`page_token`. Para volúmenes grandes, la Bulk Read API.

## Mapeo

| Zoho | Hilo |
|---|---|
| Accounts `Account_Name`, `Website`, `Industry`, `Employees`, `Billing_Country`, `Description` | `name`, `website` (+ `domain`), `industry`, `size_range`, `country`, `description` |
| Contacts `First_Name`, `Last_Name`, `Email`, `Phone`, `Title`, `Account_Name.id` | `first_name`, `last_name`, `email`, `phone`, `title`, `company` |
| Deals `Deal_Name`, `Amount`, `Closing_Date`, `Probability`, `Description`, `Next_Step` | `title`, `amount`, `expected_close_date`, `probability`, `description`, `next_step` |
| Deals `Stage`, `Pipeline` | `stage`, `pipeline` según el mapeo |
| Deals `Contact_Name.id` | `primary_contact` |
| Deals `Lead_Source` | `source_channel` |
| motivo de pérdida (`Reason_For_Loss__s` o campo propio) | `lost_reason` |
| `Owner.email` | `owner` |
| `Tag` | `tags` |

## Etapas

Cada valor de etapa tiene un tipo de pronóstico: abierta, cerrada ganada o cerrada perdida. Abierta →
`in_progress` (la primera → `initial`); cerrada ganada → `won`; cerrada perdida → `lost`.

## Interacciones

| Zoho | `type` | `direction` |
|---|---|---|
| Calls (`Call_Type`: Inbound / Outbound) | `call` | `inbound` / `outbound` |
| Events | `meeting` | `outbound` |
| Notes (`Note_Title`, `Note_Content`, `Parent_Id`) | `note` | `internal` |

Fechas: `Call_Start_Time`, `Start_DateTime`, `Created_Time` de la nota. `Call_Duration` viene como `mm:ss`.

## Trampas

- El token de acceso dura poco: renovalo con el refresh token durante la migración.
- Los ids de referencia vienen como objetos (`{"name": …, "id": …}`): usá el `id`.
