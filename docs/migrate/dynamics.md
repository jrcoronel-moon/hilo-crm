# Migrar desde Microsoft Dynamics 365 Sales

Seguí primero el método común de [README.md](README.md). Acá está lo específico de Dynamics 365 (Dataverse).

`external_id`: `d365:<guid>`.

## Acceso

Web API (OData): `https://<org>.crm.dynamics.com/api/data/v9.2/`. Para un agente, lo más simple es un token
delegado con la CLI de Azure (`az account get-access-token --resource https://<org>.crm.dynamics.com`) de un usuario
con permisos de lectura, o un usuario de aplicación registrado en Entra ID con un rol de solo lectura.

Pedí las etiquetas legibles de los conjuntos de opciones con el encabezado
`Prefer: odata.include-annotations="OData.Community.Display.V1.FormattedValue"`. Paginá con `@odata.nextLink`.

## Qué leer

| Dato | Entidad |
|---|---|
| Empresas | `accounts` |
| Contactos | `contacts` |
| Oportunidades | `opportunities` (abiertas, ganadas y perdidas) |
| Cierres (motivo, fecha real) | `opportunitycloses` |
| Usuarios | `systemusers` (`internalemailaddress`) |
| Interacciones | `phonecalls`, `emails`, `appointments`, `annotations` (notas) |

## Mapeo

| Dynamics | Hilo |
|---|---|
| account `name`, `websiteurl`, `numberofemployees`, `address1_country`, `description`, `industrycode` (etiqueta) | `name`, `website` (+ `domain`), `size_range`, `country`, `description`, `industry` |
| contact `firstname`, `lastname`, `emailaddress1`, `telephone1`, `jobtitle`, `_parentcustomerid_value` | `first_name`, `last_name`, `email`, `phone`, `title`, `company` |
| opportunity `name`, `estimatedvalue`, `estimatedclosedate`, `closeprobability`, `description` | `title`, `amount`, `expected_close_date`, `probability`, `description` |
| opportunity `transactioncurrencyid` (código ISO) | `currency` |
| opportunity `_parentaccountid_value`, `_parentcontactid_value` | `company`, `primary_contact` |
| `_ownerid_value` → email | `owner` |

## Etapas

El estado de la oportunidad está en `statecode`: 0 abierta, 1 ganada, 2 perdida. La etapa de venta está en
`stepname` (o en el flujo de proceso de negocio).

- Etapas de `stepname` en orden → la primera `initial`, el resto `in_progress`.
- Agregá una etapa `won` y una `lost` al pipeline de Hilo; `statecode` 1 y 2 van a esas etapas.
- El motivo de pérdida está en el `opportunityclose` asociado (o en `statuscode`, con su etiqueta).

## Interacciones

| Dynamics | `type` | `direction` |
|---|---|---|
| `phonecalls` (`directioncode`: true = saliente) | `call` | `outbound` / `inbound` |
| `emails` (`directioncode`) | `email` | `outbound` / `inbound` |
| `appointments` (`scheduledstart`) | `meeting` | `outbound` |
| `annotations` (`notetext`, `_objectid_value`) | `note` | `internal` |

Usá `actualend` (o `createdon`) como `occurred_at`. El registro relacionado está en `_regardingobjectid_value`.

## Trampas

- Los cuerpos de email vienen en HTML: pasalos a texto y resumilos.
- `_parentcustomerid_value` de un contacto puede apuntar a una cuenta o a otro contacto.
