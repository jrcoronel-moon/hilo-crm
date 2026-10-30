# Migrar desde CSV o Excel

Seguí primero el método común de [README.md](README.md). Esta guía sirve para cualquier herramienta que exporte a
CSV o Excel: Monday, Freshsales, Notion, Airtable, Google Sheets, otros CRMs o planillas propias.

## Archivos

Pedile a la persona un archivo por tipo (empresas, contactos, oportunidades y, si hay, actividades), con todas las
columnas. Si el origen exporta todo en una sola tabla (una fila por oportunidad con la empresa y el contacto
adentro), también sirve: separá empresas y contactos únicos antes de cargar.

## Mapeo de columnas

Leé los encabezados y unas filas de ejemplo, proponé el mapeo a los campos de Hilo y **esperá la aprobación** de la
persona. Las columnas sin lugar propio van a `custom` con su nombre original (hasta 50 por registro).

## Normalización

| Dato | Regla |
|---|---|
| Email | minúsculas, sin espacios; descartá los inválidos y listalos en el informe |
| Dominio | el host del sitio web, sin `www.`; nunca un dominio de email gratuito |
| Montos | detectá el formato: `1.500,50` y `1,500.50` son el mismo número; sin símbolo de moneda |
| Moneda | código ISO de 3 letras (`USD`, `EUR`, `ARS`); si no hay columna, preguntá cuál es |
| Fechas | `AAAA-MM-DD`; confirmá con la persona si `03/04/2025` es 3 de abril o 4 de marzo |
| Nombre completo | separalo en `first_name` y `last_name` cuando no vengan separados |
| Tags | separá por coma o punto y coma; hasta 30 |

## `external_id` sin ids

- Si el archivo trae ids del origen: `<origen>:<id>` (por ejemplo `monday:1234567890`).
- Si no trae: empresas y contactos se identifican solos por dominio y email. Para oportunidades, usá un hash
  estable de título + empresa + fecha de creación: `csv:<primeros 12 caracteres del sha1>`. Nunca uses el número de
  fila: cambia si alguien reordena el archivo.

## Etapas

Listá los valores distintos de la columna de etapa o estado y proponé el mapeo a las categorías de Hilo
(`initial`, `in_progress`, `won`, `lost`). Valores como "Ganado", "Won", "Cerrado ganado" o "Closed Won" → `won`;
"Perdido", "Lost", "Descartado" → `lost`.

## Duplicados

Antes de cargar, buscá duplicados dentro de los archivos (mismo email, mismo dominio) y mostráselos a la persona.
Hilo no duplica por dominio ni por email, pero si dos filas del mismo contacto traen datos distintos, la última
gana.
