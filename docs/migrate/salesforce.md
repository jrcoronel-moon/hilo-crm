# Migrar desde Salesforce

Seguí primero el método común de [README.md](README.md). Acá está lo específico de Salesforce. Confirmá en la
documentación de Salesforce la versión vigente de la API REST.

`external_id`: `sf:<Id>` (usá el Id de 18 caracteres).

## Acceso

Lo más simple para un agente es la CLI de Salesforce: `sf org login web` y después
`sf data query --query "<SOQL>" --json` (o `sf data export bulk` para volúmenes grandes). Usá un usuario con
permisos de solo lectura. Por API REST: `GET /services/data/vXX.X/query?q=<SOQL>`, paginando con `nextRecordsUrl`.

## Qué leer (SOQL)

```sql
SELECT Id, Name, Website, Industry, NumberOfEmployees, BillingCountry, Description, OwnerId FROM Account
SELECT Id, AccountId, FirstName, LastName, Email, Phone, Title, OwnerId FROM Contact
SELECT Id, Name, AccountId, StageName, Amount, CloseDate, Probability, IsClosed, IsWon, LeadSource,
       NextStep, Description, OwnerId, CreatedDate FROM Opportunity
SELECT OpportunityId, ContactId, Role, IsPrimary FROM OpportunityContactRole
SELECT MasterLabel, IsClosed, IsWon, DefaultProbability, SortOrder, IsActive FROM OpportunityStage
SELECT Id, Email FROM User
SELECT Id, Subject, Description, TaskSubtype, CallType, CallDurationInSeconds, ActivityDate, Status,
       WhoId, WhatId FROM Task
SELECT Id, Subject, Description, StartDateTime, DurationInMinutes, WhoId, WhatId FROM Event
```

Con multimoneda activada, sumá `CurrencyIsoCode` a Opportunity. Los campos personalizados terminan en `__c`
(el motivo de pérdida suele ser uno de ellos: preguntale a la persona cuál).

## Mapeo

| Salesforce | Hilo |
|---|---|
| Account `Name`, `Industry`, `BillingCountry`, `Description`, `Website` | `name`, `industry`, `country`, `description`, `website` |
| Account `Website` | `domain` (el host, sin `www.`) |
| Account `NumberOfEmployees` | `size_range` |
| Contact `FirstName`, `LastName`, `Email`, `Phone`, `Title`, `AccountId` | `first_name`, `last_name`, `email`, `phone`, `title`, `company` |
| Opportunity `Name`, `Amount`, `CloseDate`, `Probability`, `NextStep`, `Description` | `title`, `amount`, `expected_close_date`, `probability`, `next_step`, `description` |
| Opportunity `StageName` | `stage` según el mapeo aprobado |
| Opportunity `LeadSource` | `source_channel` |
| OpportunityContactRole | `contacts` (`role` = `Role`); el de `IsPrimary` → `primary_contact` |
| `OwnerId` → User `Email` | `owner` |
| `CreatedDate`, `CloseDate` de cerradas | `custom` (`origen_creado`, `origen_cerrado`) |

## Etapas

De `OpportunityStage` (solo las `IsActive`, en orden de `SortOrder`): `IsClosed = false` → `in_progress` (la
primera → `initial`); `IsWon = true` → `won`; `IsClosed = true` y `IsWon = false` → `lost`.

Si hay varios procesos de venta (record types de Opportunity con etapas distintas), proponé un pipeline de Hilo
por proceso.

## Interacciones

| Salesforce | `type` | `direction` |
|---|---|---|
| Task con `TaskSubtype = 'Call'` | `call` (`CallType`: `Inbound` / `Outbound`; duración en segundos → minutos) | `inbound` / `outbound` |
| Task con `TaskSubtype = 'Email'` (o EmailMessage, con `Incoming`) | `email` | `inbound` / `outbound` |
| Event | `meeting` | `outbound` |
| Task completada de otro tipo, notas | `note` | `internal` |

`WhoId` apunta a un Contact (o a un Lead) y `WhatId` a una Account u Opportunity: resolvelos con el mapa. Las Task
abiertas no se migran como interacción: la próxima va en `next_step` / `next_step_at`.

## Trampas

- **Leads sin convertir**: no son contactos todavía. Preguntale a la persona si se migran (como contacto + empresa
  + oportunidad en la etapa inicial) o se dejan afuera.
- Límite diario de llamadas a la API: para más de unas decenas de miles de registros, usá Bulk API 2.0
  (`sf data export bulk`).
- `CloseDate` de una oportunidad abierta es la fecha estimada; de una cerrada, la real.
