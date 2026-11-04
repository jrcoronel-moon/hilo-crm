# Manual de `hiloctl`

`hiloctl` es la consola de operación de Hilo. Hace todo lo que hace la web de Ajustes y de Superadmin, y algunas
cosas que la web no hace (exportar e importar, mirar la actividad en vivo). Vive dentro de la imagen `web`, habla
con la base directamente y no necesita la interfaz: funciona igual con `HILO_UI=full`, `minimal` u `off`.

## 1. Cómo se ejecuta

En el servidor (o en tu máquina con el stack local):

```sh
cd ~/hilo-crm                                   # donde está docker-compose.yml y el .env
docker compose exec web hiloctl                 # sin argumentos: lista todos los comandos
alias hc='docker compose exec web hiloctl'      # para el resto de este manual
```

Reglas que valen para todos los comandos:

| Regla | Detalle |
|---|---|
| `<workspace>` | el *slug* (`acme`, como aparece en `hc workspace list`) o su id |
| `--json` | salida JSON para scripts, en cualquier posición: `hc --json member list acme` |
| Flags en cualquier lugar | `hc token create acme ana@acme.com claude --read-only` o `hc --read-only token create …` |
| Entrada por stdin | los comandos que leen archivos (`pipeline apply`, `import`, `import-csv`, `branding logo`) los toman de stdin; con Docker hay que usar `docker compose exec -T web hiloctl …` para que el archivo pase |
| Salida a archivo | `export` escribe en stdout: `docker compose exec -T web hiloctl export acme > acme.json` |
| Quién actúa | `hiloctl` es el **operador**: derechos de propietario en cualquier workspace, con las mismas reglas que la web (nunca deja un workspace sin propietario, valida todo igual). En la auditoría aparece como `hiloctl` |
| Errores | mensaje en stderr y código de salida 1; con argumentos incorrectos imprime la lista de comandos |

> Quien tiene SSH al servidor puede ejecutar `hiloctl`, y por lo tanto tiene acceso total a todos los datos.
> Tratá el acceso SSH como la llave maestra (ver `SECURITY.md`).

## 2. Primeros pasos después de instalar

Paso a paso para dejar un Hilo recién desplegado listo para un equipo:

```sh
# 1. Quién administra la plataforma (también se puede con SUPERADMIN_EMAILS en el .env)
hc superadmin grant ana@acme.com

# 2. Qué dominios de email pueden iniciar sesión
hc domain allow acme.com

# 3. El workspace y su propietario (si nunca entró, igual queda creado como dueño)
hc workspace create "Acme" --owner ana@acme.com
hc workspace list

# 4. El equipo
hc member add acme bob@acme.com member          # "added" si ya entró alguna vez; si no, "invited"
hc member add acme cfo@acme.com viewer
hc member list acme

# 5. El pipeline y los canales (si los de fábrica no alcanzan)
hc pipeline list acme
hc --json pipeline list acme > pipeline.json    # editalo y volvé a aplicarlo:
docker compose exec -T web hiloctl pipeline apply acme < pipeline.json
hc channel add acme "Webinar" event

# 6. El primer agente (ver sección 5)
hc token create acme ana@acme.com claude-code --ttl 90d
```

## 3. Referencia de comandos

### Plataforma (superadmin)

| Comando | Qué hace |
|---|---|
| `hc superadmin grant <email>` / `revoke <email>` / `list` | Quién puede entrar a la página Superadmin y cambiar dominios y marca |
| `hc domain allow <dominio>` / `deny <dominio>` / `list` | Dominios de email que pueden iniciar sesión en la plataforma |
| `hc branding show` | Nombre de la aplicación y si tiene logo propio |
| `hc branding name "Acme CRM"` | Cambia el nombre que ven todos |
| `docker compose exec -T web hiloctl branding logo < logo.png` | Sube el logo (PNG, JPG, WebP o SVG; se verifica por contenido) |
| `hc branding reset-logo` | Vuelve al logo por defecto |

### Workspaces y miembros

| Comando | Qué hace |
|---|---|
| `hc workspace create "<nombre>" --owner <email> [--lang en]` | Crea el workspace con ese propietario; el slug se deriva del nombre. `--lang` (en, es, fr, it, de, pt; por defecto es) elige el idioma del pipeline y los canales iniciales |
| `hc workspace list` | Slug, nombre, miembros, fecha e id de cada workspace activo |
| `hc member list <ws>` | Miembros con rol, e invitaciones pendientes con su vencimiento |
| `hc member add <ws> <email> <admin\|member\|viewer>` | Agrega a quien ya entró alguna vez, o lo invita y queda adentro al entrar con su cuenta de Google o Microsoft (las invitaciones desde la web, en cambio, se aceptan explícitamente) |
| `hc member role <ws> <email> <owner\|admin\|member\|viewer>` | Cambia el rol. No permite dejar el workspace sin propietario |
| `hc member remove <ws> <email>` | Quita al miembro (sus tokens y conexiones de agentes dejan de servir) |

Roles: **owner** y **admin** administran (ajustes, miembros, importar, exportar); **member** lee y escribe datos;
**viewer** solo lee.

### Tokens personales (agentes sin OAuth)

| Comando | Qué hace |
|---|---|
| `hc token create <ws> <email> <nombre> [--read-only] [--ttl <días>d]` | Emite un token para ese miembro. **Se muestra una sola vez.** Sin `--ttl` no vence; `--ttl` acepta de `1d` a `365d` |
| `hc token list <ws> <email>` | Tokens de ese miembro: id, nombre, prefijo, alcance, vencimiento y último uso (nunca el secreto) |
| `hc token revoke <ws> <email> <token-id>` | Lo invalida al instante |

El token se usa como `Authorization: Bearer hilo_pat_…` contra `https://<tu-dominio>/mcp`. Hereda los permisos del
rol del miembro, acotados por `--read-only`.

> Un token es para clientes **sin OAuth**: la API de xAI (Grok por API), scripts, integraciones por encabezado. Los
> clientes con OAuth (Claude, Grokbot, Cursor, ChatGPT…) no lo necesitan: se conectan con la URL `/mcp` y autorizan
> en Hilo; su conexión se ve y se revoca en *Ajustes → Agentes*. Si un cliente entró por OAuth, el token que le
> habías creado no se usa: revocalo.

### Agentes pre-aprobados (OAuth sin pantalla de consentimiento)

| Comando | Qué hace |
|---|---|
| `hc client preapprove <ws> <client-id-url> [--read-only]` | Los usuarios de ese agente no ven la pantalla de consentimiento en este workspace. Solo agentes identificados por URL (CIMD); máximo lectura + escritura |
| `hc client unapprove <ws> <client-id-url>` | Vuelve a pedir consentimiento |
| `hc client list <ws>` | Agentes pre-aprobados, con alcance y fecha |

El `client_id` del agente aparece en *Ajustes → Agentes* después de que alguien lo conectó una vez.

### Configuración del workspace

| Comando | Qué hace |
|---|---|
| `hc pipeline list <ws>` | Pipelines con sus etapas (y categoría), meta mensual y cuál es el default |
| `docker compose exec -T web hiloctl pipeline apply <ws> < pipeline.json` | Crea o reconfigura un pipeline. El JSON tiene la forma de la herramienta `configure_pipeline`; el más fácil es partir de `hc --json pipeline list <ws>` |
| `hc channel list <ws>` | Canales activos con su tipo |
| `hc channel add <ws> "<nombre>" <tipo>` | Tipos: `web`, `form`, `email`, `phone`, `social`, `referral`, `event`, `outbound`, `partner`, `ads`, `videocall`, `video`, `other` |
| `hc audit list <ws> [--limit <n>]` | Últimos cambios (50 por defecto) en frases legibles: quién, por dónde, qué |

Ejemplo mínimo de `pipeline.json` (un pipeline nuevo):

```json
{"name": "Partners",
 "stages": [{"name": "Nuevo", "category": "initial"}, {"name": "Activo", "category": "in_progress"},
            {"name": "Firmado", "category": "won"}, {"name": "Caído", "category": "lost"}],
 "monthly_target": 5000, "target_currency": "USD"}
```

Para modificar uno existente, agregá su `"id"` (o el nombre en `"id"`); las etapas que ya existen van con su `id`
para conservar las oportunidades que tienen.

### Datos: exportar, importar, migrar

| Comando | Qué hace |
|---|---|
| `docker compose exec -T web hiloctl export <ws> > ws.json` | Todo el workspace (empresas, contactos, oportunidades con su historial de fechas, interacciones) en el formato de `bulk_upsert`. Los registros sin id externo reciben `hilo:<id>` |
| `docker compose exec -T web hiloctl import <ws> --dry-run < ws.json` | Valida el archivo completo (hasta 100 000 registros, referencias incluidas) sin guardar nada |
| `docker compose exec -T web hiloctl import <ws> < ws.json` | Carga. Cada registro sale bien o mal por su cuenta; se listan los fallidos con su posición. Volver a importar actualiza en lugar de duplicar |
| `docker compose exec -T web hiloctl import-csv <ws> <company\|contact\|opportunity\|interaction> [opciones] < archivo.csv` | Carga un CSV de otro CRM o planilla (un tipo de registro por archivo). Siempre imprime cómo mapeó las columnas |

Opciones de `import-csv`:

| Opción | Para qué |
|---|---|
| `--source <nombre>` | Prefijo de los ids del archivo: `--source hubspot` convierte `123` en `hubspot:123`, así se puede reimportar o sincronizar sin duplicar |
| `--map "<Columna>=<campo>,…"` | Corrige el mapeo automático. La columna se nombra por su encabezado o su número (desde 1); el campo es uno de los de Hilo, `custom` (guarda la columna en campos personalizados) o `-` (ignorar) |
| `--set "<campo>=<valor>,…"` | Valores fijos para campos sin columna: `--set pipeline=Ventas,currency=USD`, o `--set type=note` para interacciones |
| `--dates dmy\|mdy` | Cómo leer fechas ambiguas como `03/04/2025` (por defecto día/mes/año). Las fechas ISO siempre se entienden |
| `--tags <a,b>` | Etiquetas que se agregan a todo lo importado, p. ej. `importado-2026-10` |
| `--dry-run` | Valida y muestra el resultado sin guardar |

Campos reconocidos por tipo (los encabezados en español o inglés se detectan solos):

- **company**: `external_id`, `name`, `domain`, `industry`, `size_range`, `country`, `website`, `description`, `owner`, `source_channel`, `tags`, `custom`
- **contact**: `external_id`, `first_name`, `last_name`, `name` (nombre completo, se separa), `email`, `phone`, `title`, `linkedin_url`, `company` (dominio o id), `owner`, `source_channel`, `tags`, `custom`
- **opportunity**: `external_id`, `title`, `kind`, `company`, `primary_contact`, `pipeline`, `stage`, `lost_reason`, `amount`, `currency`, `probability`, `expected_close_date`, `next_step`, `next_step_at`, `description`, `created_at`, `closed_at`, `owner`, `source_channel`, `tags`, `custom`
- **interaction**: `external_id`, `type`, `direction`, `channel`, `subject`, `summary`, `occurred_at`, `duration_minutes`, `company`, `contact`, `opportunity`

Orden recomendado: empresas → contactos → oportunidades → interacciones, cada tipo con su archivo y el mismo
`--source`. Montos como `1.500,50`, `1,500.50` o `$ 1500` y tipos como "Llamada" o "Reunión" se normalizan solos.

### Actividad en vivo

| Comando | Qué hace |
|---|---|
| `hc watch <ws>` | Imprime cada cambio del workspace en el momento en que ocurre (hora, quién, qué, por dónde) hasta Ctrl-C |
| `hc --json watch <ws>` | Lo mismo, un JSON por línea, para encadenar con otras herramientas |

## 4. Ejemplos de salida

```
$ hc workspace list
SLUG  NAME  MEMBERS  CREATED     ID
acme  Acme  3        2026-10-30  01a18c86-4cb7-7ab3-99fc-48fd5a5914fd

$ hc token create acme ana@acme.com claude-code --ttl 90d
token 01a1…  (claude-code, scope crm:read crm:write crm:admin, expires 2027-01-28). Shown only once:
hilo_pat_…

$ docker compose exec -T web hiloctl import-csv acme contact --source hs --dry-run < contactos.csv
COLUMN   FIELD
Nombre   name
E-mail   email
Empresa  company
Cargo    title

dry run (nothing saved): 120 rows, 118 ok (118 new, 0 updated), 2 rejected, 0 not converted
ROW  FIELD  ERROR
57          company not found
98          email: not a valid email address

$ hc watch acme
watching acme (Ctrl-C to stop)
10:42:07  acme  Claude (ana@acme.com)    created the company «Globex»  [oauth]
10:42:09  acme  Claude (ana@acme.com)    moved «Planta solar» to Propuesta  [oauth]
```

## 5. Recetas

**Conectar un agente sin que el usuario vea ninguna pantalla de Hilo** (ideal con `HILO_UI=minimal`):

1. Que un usuario conecte el agente una vez por OAuth; en *Ajustes → Agentes* aparece su `client_id` (una URL).
2. `hc client preapprove acme https://…/client-metadata.json`
3. Con un solo proveedor de login configurado (solo Google o solo Microsoft), la próxima persona que conecte ese
   agente ve únicamente la pantalla del proveedor y vuelve al agente.

**Un agente que solo acepta un encabezado** (Grok por API, scripts, Cursor sin OAuth):

```sh
hc token create acme ana@acme.com grok --read-only --ttl 30d      # copiá el token
hc token list acme ana@acme.com                                   # ver "último uso"
hc token revoke acme ana@acme.com <id>                            # al terminar
```

**Migrar desde un CSV de otro CRM:**

```sh
docker compose exec -T web hiloctl import-csv acme company     --source pipedrive --dry-run < organizations.csv
docker compose exec -T web hiloctl import-csv acme company     --source pipedrive           < organizations.csv
docker compose exec -T web hiloctl import-csv acme contact     --source pipedrive           < persons.csv
docker compose exec -T web hiloctl import-csv acme opportunity --source pipedrive --set pipeline=Ventas --map "Status=stage,Value=amount" < deals.csv
docker compose exec -T web hiloctl import-csv acme interaction --source pipedrive --set type=note < notes.csv
```

Si el archivo supera lo que la pantalla *Ajustes → Importar* acepta (700 KB o 3 000 filas), este es el camino. Para
migraciones por API con un agente, ver `docs/migrate/`.

**Copia de un workspace** (clonar para pruebas, o mover a otro Hilo):

```sh
docker compose exec -T web hiloctl export acme > acme.json
hc workspace create "Acme pruebas" --owner ana@acme.com
docker compose exec -T web hiloctl import acme-pruebas --dry-run < acme.json
docker compose exec -T web hiloctl import acme-pruebas           < acme.json
shred -u acme.json        # el archivo tiene todos los datos en claro
```

El workspace destino necesita los mismos nombres de pipelines y canales (`pipeline apply`, `channel add`); el
export no los incluye.

**Dar de baja a alguien:**

```sh
hc member remove acme bob@acme.com      # sus tokens y conexiones de agentes dejan de servir al instante
hc audit list acme --limit 20           # ver qué hizo últimamente
```

**Operar sin interfaz:** poné `HILO_UI=minimal` (solo login y consentimiento de agentes) u `off` (ni eso: agentes
solo con tokens) en el `.env`, levantá de nuevo el stack, y administrá todo con `hiloctl`.

## 6. Errores frecuentes

| Mensaje | Causa y solución |
|---|---|
| `usage: hiloctl …` | Comando mal escrito o le faltan o sobran argumentos; compará con la lista |
| `workspace "x": not found` | El slug no existe o el workspace está archivado; `hc workspace list` |
| `ana@acme.com in acme: not found` | No es miembro de ese workspace; `hc member add` primero |
| `cannot remove or demote the last owner` | Nombrá otro propietario antes (`member role … owner`) |
| `--ttl must be whole days between 1d and 365d` | El formato es `30d` |
| `bundle JSON on stdin: unknown field "…"` | El archivo tiene un campo que Hilo no conoce; revisá el nombre |
| `mapping: one of title must be mapped` | `import-csv` no encontró la columna obligatoria; indicala con `--map "Mi columna=title"` o fijala con `--set` |
| `--map: no column "x" in the file` | El nombre no coincide con el encabezado; también podés usar el número de columna |
| Nada pasa con `import` / `pipeline apply` | Falta `-T` en `docker compose exec -T …`: sin él, stdin no llega al contenedor |
| `DATABASE_URL is required` | Se ejecutó fuera del contenedor; usá `docker compose exec web hiloctl` |

## 7. Seguridad

- `hiloctl` no pide contraseña: la protección es el acceso al servidor. Limitá SSH a quien deba operar, y usá llaves.
- Los tokens se muestran una sola vez y nunca se vuelven a listar enteros. No los pegues en chats ni en tickets.
- `export` produce todos los datos del workspace en claro. Tratá el archivo como un backup: cifrado en reposo y
  borrado cuando ya no haga falta.
- Todo lo que hace `hiloctl` queda en la auditoría del workspace con actor `hiloctl` (`hc audit list`), y los
  cambios de datos se ven en vivo en la web y en `hc watch`.
