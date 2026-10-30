# Plan de nuevos features

Estado: en curso. Hecho: fase A, parte 1 (`hiloctl` para workspaces, miembros y tokens, con `--json`).

Punto de partida (lo que ya existe y se reutiliza):

- Toda escritura pasa por `internal/crm` (mismas reglas, validación y auditoría para web, MCP y lo que venga).
- Los `upsert_*` son idempotentes por `external_id`, la base de cualquier importación reanudable.
- `hiloctl` ya existe dentro de la imagen `web` con `superadmin` y `domain`.
- El servidor OAuth 2.1 (DCR, CIMD, PKCE, refresh con rotación) y los PATs ya funcionan con Claude, Grok (API) y Cursor.

Principios que se mantienen en los tres features:

1. Un solo camino de escritura (`internal/crm`). Ni CLI ni importador escriben SQL de negocio por su cuenta.
2. Cada superficie nueva entra al harness (`make check` + `make dast`) con tests e2e propios.
3. Credenciales de terceros nunca en argumentos de línea de comando, logs, base ni repo: van por env o stdin y se descartan al terminar.

---

## 1. Migración desde otras aplicaciones

**Deseo:** traer datos de otros CRMs. Prioridad: script de terminal, después por SSH, por último UI.

### Primero: guías de migración para agentes

Cada vez más gente va a migrar pidiéndoselo a un agente (Claude Code, Cursor, Claude, Duet…) en lugar de correr un
importador. Hilo ya tiene todo lo que ese agente necesita (los `upsert_*` idempotentes por `external_id`), así que lo
primero es escribirle las instrucciones:

- `docs/migrate/README.md`: el método común. Leer el origen con su API, export o conector; mapear etapas y dueños;
  usar `external_id = "<origen>:<id>"` para poder reintentar; cargar en orden empresas → contactos → oportunidades
  → interacciones; empezar con una muestra y verificar con `get_metrics` y `list_*`.
- Una guía por plataforma (`docs/migrate/hubspot.md`, `salesforce.md`, `pipedrive.md`, `zoho.md`,
  `dynamics.md`, `monday.md`, `close.md`, `freshsales.md`, `odoo.md`, `notion-airtable.md`, `csv.md`): cómo
  exportar o leer cada objeto, tabla de mapeo de campos a Hilo, cómo mapear sus etapas a las categorías
  `initial`/`in_progress`/`won`/`lost`, y sus trampas (límites de API, campos personalizados, duplicados).
- Escritas para que un agente las siga paso a paso: el usuario dice "migrá desde HubSpot siguiendo
  `docs/migrate/hubspot.md`" y listo.

Herramientas que lo harían más rápido, en este orden: `bulk_upsert` (lotes de hasta N registros por llamada) y un
modo `dry_run` en los upserts para que el agente muestre el plan antes de escribir.

El importador de abajo sigue teniendo sentido para volúmenes grandes o migraciones sin agente.

### Importador (volúmenes grandes)

Tres piezas separadas, para que cada origen nuevo solo agregue un adaptador:

```
origen (HubSpot, Pipedrive, CSV…) ──► adaptador ──► paquete Hilo (JSONL) ──► cargador ──► internal/crm
```

- **Paquete Hilo**: formato intermedio propio, un JSONL por entidad (`companies`, `contacts`, `opportunities`, `interactions`) más un `manifest.json` (origen, fecha, conteos). Se puede revisar, versionar y editar a mano antes de cargar.
- **Adaptadores** (`internal/importer/<origen>`): leen el origen y escriben el paquete. Primeros candidatos:
  - CSV/XLSX genérico con archivo de mapeo de columnas (cubre cualquier herramienta que exporte).
  - HubSpot y Pipedrive por API (token privado del usuario).
  - Después, según demanda: Salesforce, Zoho, Notion/Airtable.
- **Cargador**: recorre el paquete y llama a los `Upsert*` con `external_id = "<origen>:<id>"`. Así es reanudable e idempotente: correrlo dos veces no duplica.
- **Mapeo** (`mapping.yaml`): etapas del origen → etapas de Hilo (o crear pipeline nuevo), dueños del origen → emails de miembros (si no existen, quedan como invitación pendiente), campos extra → `custom` (jsonb).

### Superficies, en orden

1. **Terminal**: `hiloctl import <origen> --mapping m.yaml --workspace <slug> [--dry-run]` y `hiloctl export --workspace <slug>` (el mismo paquete al revés: sin lock-in, que es coherente con ser open source).
2. **SSH / contenedor**: el mismo comando, ejecutado en el servidor con `docker compose exec web hiloctl import …`. Lo único extra es poder pasar el paquete por stdin o por un volumen de solo lectura.
3. **UI**: subir un CSV, previsualizar el mapeo y confirmar. Reutiliza el adaptador CSV y el cargador; la UI solo arma el `mapping.yaml`.

Bonus casi gratis: un agente ya puede migrar hoy leyendo el CRM viejo con su propio conector y llamando a los `upsert_*` de Hilo. Se puede documentar como "migración asistida" y, si hace falta, sumar una herramienta MCP `bulk_upsert` para hacerlo en lotes.

### Seguridad

- `--dry-run` por defecto en la primera ejecución: informe de qué se crea, actualiza o descarta, sin escribir.
- Límites de tamaño por archivo y por fila; los CSV se parsean como datos (sin fórmulas).
- Auditoría con actor `import:<origen>` y un id de corrida para poder revisar o revertir (archivar todo lo de una corrida).
- Los tokens de HubSpot o Pipedrive se leen de env o stdin y no se guardan.

### Decisiones abiertas

- ¿Qué orígenes primero? (propuesta: CSV genérico + HubSpot).
- ¿Se importan cuerpos completos de emails o solo asunto y resumen?
- ¿Revertir una corrida = archivar, o borrado real?

---

## 2. Headless más serio

**Deseo:** administrar todo desde terminal, SSH o contenedor, y apagar la UI al máximo. El problema a resolver es cómo los usuarios autorizan a sus agentes por OAuth si no hay UI.

### Administración por terminal

Extender `hiloctl` hasta cubrir todo lo que hoy hace la web de ajustes y superadmin:

| Área | Comandos |
|---|---|
| Workspaces | `workspace create/list/rename` |
| Miembros | `member invite/list/role/remove` |
| Pipelines y canales | `pipeline apply -f pipeline.yaml`, `channel …` |
| Tokens | `token create/list/revoke` (PATs) |
| Clientes OAuth | `client list/revoke`, `client preapprove` (ver abajo) |
| Auditoría | `audit tail --workspace … --since …` |
| Marca, dominios, superadmins | `branding set`, `domain …`, `superadmin …` (ya existe) |
| Datos | `import`, `export`, `backup` |

- Salida legible por defecto y `--json` para scripts.
- Modo local: corre dentro del contenedor y habla con la base directamente. Quien tiene SSH al servidor ya es operador; la auditoría registra `cli:<usuario del sistema>`.
- Modo remoto (más adelante): el mismo `hiloctl` contra una API de administración con un PAT de superadmin, para no necesitar SSH.

### Apagar la UI: `HILO_UI=full | minimal | off`

- `full`: lo de hoy.
- `minimal`: solo lo imprescindible para OAuth (iniciar sesión con Google/Microsoft, consentimiento, elegir workspace, cerrar sesión). Tablero, dashboard y ajustes devuelven 404. Menos superficie de ataque.
- `off`: sin login humano; solo PATs emitidos por CLI.

### El problema de fondo: OAuth sin UI

El flujo OAuth que usan Claude, ChatGPT, Duet, etc. **siempre** abre un navegador: el usuario tiene que identificarse y aprobar. No hay forma estándar de evitar ese paso, pero se puede reducir a casi nada:

1. **UI mínima invisible** (recomendado): en `/oauth/authorize`, si hay un solo proveedor configurado, redirigir directo a Google o Microsoft, sin pantalla de Hilo. El usuario ve solo la pantalla de Google y vuelve al agente.
2. **Consentimiento pre-aprobado**: el admin declara por CLI qué clientes confía para un workspace (`hiloctl client preapprove https://claude.ai/... --workspace acme --scopes crm:read,crm:write`). Esos clientes se saltean la pantalla de consentimiento. Combinado con 1, el usuario no ve ninguna pantalla de Hilo.
3. **PATs por CLI** para clientes que aceptan un header (Claude Code, Cursor, API de Grok, conectores por API key): `hiloctl token create --user x@acme.com --workspace acme --read-only`. Funciona incluso con `HILO_UI=off`.
4. **Device flow** (RFC 8628, aprobar con `hiloctl oauth approve <código>`): elegante para servidores sin navegador, pero hoy casi ningún cliente MCP lo soporta. Queda anotado, no se implementa hasta que haya clientes.

Descartado por ahora: delegar todo en un proveedor externo (Google/Entra como servidor de autorización), porque no soportan DCR ni CIMD, que es lo que esperan los clientes MCP.

### Decisiones abiertas

- ¿`minimal` es el modo por defecto en producción, o se elige al desplegar?
- ¿La API de administración remota es necesaria, o alcanza con SSH?

---

## 3. Nuevos agentes (Claude Duet, Meta Muse, ChatGPT Dots, …)

**Deseo:** que Hilo funcione con los agentes personales y proactivos que van saliendo.

### Qué cambia con estos agentes

Son agentes que trabajan en segundo plano, vigilan las apps conectadas y piden aprobación antes de acciones sensibles. Para un CRM eso implica:

| Necesidad | Qué hay hoy | Qué agregar |
|---|---|---|
| Saber qué cambió sin releer todo | listas con cursor | herramienta `list_changes` (feed por cursor desde una fecha) |
| Saber qué requiere atención | `stale_days` en listas | herramienta `needs_attention`: estancadas, próximos pasos vencidos, leads sin dueño |
| Pedir aprobación en lo correcto | anotaciones readOnly / additive / overwriting | revisar `destructiveHint` (archivar, pasar a perdida) para que el agente pida confirmación |
| Seguir conectado sin el usuario | refresh con rotación | validar refresh largo y revocación desde CLI/web; política de duración configurable |
| Saber quién hizo qué | actor = cliente OAuth | sin cambios; mostrar el nombre del agente en todos lados |
| No obedecer contenido de terceros | resúmenes marcados como datos | mantener; sumar tests con textos de inyección en interacciones |
| Opcional: aprobación humana en Hilo | — | cola de "acciones pendientes" para operaciones destructivas, aprobables por web o CLI |

### Compatibilidad por cliente

Antes de implementar nada específico, una matriz verificada contra la documentación oficial de cada uno:

| Cliente | Transporte | Autenticación | Estado |
|---|---|---|---|
| Claude (web, desktop, Code) | Streamable HTTP | OAuth (DCR/CIMD) o header | probado |
| Claude Duet | usa los conectores de Claude | igual que Claude | verificar al tener acceso |
| Grok (API) | Streamable HTTP | header (PAT) | probado |
| Cursor | Streamable HTTP | OAuth o header | probado |
| ChatGPT / Dots | Streamable HTTP | OAuth | verificar |
| Meta Muse | por confirmar | por confirmar | verificar: las fuentes no coinciden en si acepta MCP con OAuth o solo con headers |

Si algún cliente solo acepta API key o header, el PAT ya lo cubre. Cada cliente verificado suma una guía corta "Conectar tu agente" en la documentación y un caso en `scripts/` como `try-grok.py`.

### Dependencia

Publicar en directorios (conectores de Claude, revisión de conectores de Meta) requiere URL pública estable y HTTPS propio: depende del despliegue en Lightsail.

---

## Orden propuesto

| Fase | Contenido | Tamaño |
|---|---|---|
| A.1 ✅ | `hiloctl` para workspaces, miembros y tokens, con `--json` | S |
| B.0 | Guías de migración para agentes (`docs/migrate/`) + `bulk_upsert` | S–M |
| A | Resto de `hiloctl` (pipelines, canales, auditoría, marca) + `HILO_UI` + redirect directo al proveedor + clientes pre-aprobados | M |
| B | Importador: paquete Hilo, cargador, CSV genérico, `export`, `--dry-run` | M |
| C | Herramientas para agentes proactivos (`list_changes`, `needs_attention`, anotaciones destructivas) + matriz de compatibilidad | S–M |
| D | Adaptadores HubSpot y Pipedrive | M |
| E | UI de importación CSV; cola de aprobaciones (si se decide) | M |
| — | Lightsail y publicación en directorios, cuando se decida | — |

A va primero porque B y C se apoyan en él: importar y emitir tokens desde terminal es justamente el modo headless.
