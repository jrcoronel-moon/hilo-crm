# Hilo Headless CRM

**El CRM agentic-first: los agentes de IA operan el pipeline, las personas miran.**

Los CRM se diseñaron para que una persona haga clic en un tablero. Hilo está diseñado al revés: la vía principal es
un servidor [MCP](https://modelcontextprotocol.io) remoto y la web es una ventana para ver lo que los agentes hicieron.

Conectás Claude, ChatGPT, Grok o Cursor, y el agente carga empresas, contactos y oportunidades, registra cada
interacción y mueve el funnel. Entrás con Google o Microsoft cuando querés mirar.

Código libre, [Apache-2.0](LICENSE). Un `docker compose up` y anda.

## El problema

Darle un CRM a un agente suele terminar de dos maneras: un agente que scrapea una interfaz pensada para dedos, o una
persona copiando datos de un chat a un formulario.

Hilo expone el CRM como herramientas MCP tipadas, y los `upsert` son idempotentes, así que el agente puede reintentar
sin duplicar nada. Cada llamada pasa por las mismas reglas de permisos y la misma auditoría que si la hubiera hecho
una persona: no hay un camino "de agente" con menos control.

## Cómo se ve, en la práctica

Ejemplo ilustrativo.

```text
Vos:    Llegó un mail de Laura Méndez, de Acme, pidiendo una propuesta por 20 licencias.
        Cargala y movela a Propuesta.

Agente: (search_crm "Acme")         → la empresa existe, el contacto Laura Méndez no
        (upsert_contact)            → Laura Méndez, Acme, laura@acme.com
        (upsert_opportunity)        → "Acme / 20 licencias", external_id mail-83921
        (move_opportunity_stage)    → Propuesta
        (log_interaction)           → email entrante, con el resumen del pedido

        Listo: un contacto, una oportunidad en Propuesta y la interacción registrada.
```

Nada de eso necesitó abrir el navegador. Y si el agente se corta a la mitad y reintenta, los `upsert` resuelven por
`id`, `external_id`, dominio o email: no se duplica nada.

## Qué ve la persona

La web es deliberadamente chica y hace solo lo que un agente no debería hacer: que un humano mire y decida.

- **Funnel** con arrastre entre etapas, deshacer y motivo de pérdida.
- **Dashboard** con volumen y valor ponderado por etapa, win rate, ciclo, tiempo en etapa, oportunidades estancadas,
  leads por canal, interacciones por semana, monto ganado por mes y objetivo mensual por pipeline.
- **Empresas, contactos y oportunidades**: listas, fichas con timeline, edición, tags con sugerencias, búsqueda,
  filtros, acciones en lote y exportación CSV.
- **Interacciones y auditoría** de todo lo que pasó, venga de un agente o de una persona.
- **En tiempo real**: mientras un agente trabaja, el funnel, las listas, las fichas y el dashboard se actualizan
  solos, con un aviso de quién hizo qué ("Claude movió «Planta solar» a Propuesta").
- **Ajustes**: pipelines, canales, formularios, miembros, dominios habilitados y marca de la aplicación (nombre y
  logo, configurables por el superadmin).

## Empezar en local

Requisitos: Docker y Go 1.27+ (solo para desarrollar).

```sh
cp .env.example .env
# completá los secretos (el archivo explica cómo generarlos) y, para probar sin Google/Microsoft:
#   DEV_LOGIN_ENABLED=true   SUPERADMIN_EMAILS=vos@tuempresa.com
make up          # http://localhost:8080
```

Entrás con tu email (modo desarrollo), creás un workspace y en **Ajustes → Agentes** está la URL MCP.

## Conectar un agente

URL del servidor MCP: `https://<tu-dominio>/mcp`

- **Claude (web/desktop)**: Ajustes → Conectores → *Agregar conector personalizado* → pegá la URL. Se abre Hilo para
  iniciar sesión y autorizar; elegís el workspace y, si querés, acceso de solo lectura.
- **Claude Code**: `claude mcp add --transport http hilo https://<tu-dominio>/mcp`
- **Grok (API de xAI)**: herramienta `{"type": "mcp", "server_url": "https://<tu-dominio>/mcp", "authorization":
  "hilo_pat_…"}` en `/v1/responses` (el token solo: xAI agrega `Bearer`). Para probarlo contra tu stack local, con
  túnel incluido: `./scripts/try-grok.py`.
- **ChatGPT, Cursor, Duet, Muse y otros**: agregá la URL como servidor MCP remoto ([compatibilidad y guía por
  cliente](docs/AGENTS.md)). Si el cliente no soporta OAuth, creá un
  token personal en *Ajustes → Agentes* y mandalo como `Authorization: Bearer hilo_pat_…`.

### Herramientas MCP

| Lectura | Escritura | Administración |
|---|---|---|
| `search_crm`, `list_pipelines`, `list_channels`, `get_company`, `get_contact`, `get_opportunity`, `list_companies`, `list_contacts`, `list_opportunities`, `get_timeline`, `list_interactions`, `list_tags`, `list_changes`, `needs_attention`, `get_metrics` | `upsert_company`, `upsert_contact`, `upsert_opportunity`, `move_opportunity_stage`, `log_interaction`, `bulk_upsert`, `archive_record` | `configure_pipeline`, `upsert_channel` |

Las referencias entre registros aceptan el id de Hilo, la clave natural (dominio de la empresa, email del contacto) o
el `external_id` del sistema de origen, así que un agente que sincroniza o migra no necesita guardar ids.
`bulk_upsert` carga hasta 200 registros por llamada y tiene modo `dry_run` para validar sin guardar.

Cada agente ve solo las herramientas que su rol y su token permiten. Un token OAuth o personal puede restringirse a
solo lectura; nunca puede ampliar lo que permite el rol.

### Pipelines

Cada etapa es `initial` (exactamente una), `in_progress` (al menos una), `won` o `lost` (al menos una de cada). El
estado de una oportunidad sigue a la categoría de su etapa, así que las métricas no dependen de cómo nombraste las
columnas.

### Formularios de leads

Los formularios públicos de leads crean contacto + oportunidad + interacción, sin poder sobrescribir datos existentes.
Cada formulario puede exigir un captcha de Cloudflare Turnstile (gratis), que Hilo verifica en el servidor antes de
guardar. Si el formulario tiene orígenes permitidos, el captcha tiene que haberse resuelto en uno de esos sitios.

Para activarlo:

1. En [dash.cloudflare.com](https://dash.cloudflare.com), entrá a **Turnstile → Add widget** con los dominios de las
   webs donde van los formularios. Te da una clave pública (site key) y una secreta (secret key).
2. Cargalas en el `.env` del servidor y reiniciá:

   ```sh
   TURNSTILE_SITE_KEY=...
   TURNSTILE_SECRET_KEY=...
   ```

   ```sh
   docker compose up -d --wait
   ```

3. En **Ajustes → Formularios**, copiá el snippet a tu web (ya incluye el script y el widget de Turnstile) y después
   tocá **Exigir captcha**.

Respetá ese orden: si activás el captcha antes de que la web tenga el widget, el formulario rechaza todos los envíos.
Sin las dos claves cargadas, Hilo no deja activarlo. Los formularios nuevos traen el captcha activado; los que ya
existían siguen sin captcha hasta que lo actives.

## Quién puede entrar

Un email verificado por Google o Microsoft cuyo dominio esté habilitado por un superadmin, o con una invitación
pendiente. Sin dominios configurados, solo superadmins e invitados. Los superadmins se definen con
`SUPERADMIN_EMAILS` o con `hiloctl superadmin grant <email>`.

| Rol | Puede |
|---|---|
| Lector | leer |
| Miembro | leer y escribir datos del CRM |
| Admin | además, configurar pipelines, canales, formularios, miembros y dominios |
| Propietario | además, gestionar propietarios |
| Superadmin (plataforma) | dominios habilitados para entrar, marca de la aplicación y vista de todos los workspaces |

## Administrar desde la terminal

Todo lo de workspaces, miembros y tokens se puede hacer sin abrir la web, con `hiloctl` dentro del contenedor
`web` (en el servidor, por SSH):

```sh
docker compose exec web hiloctl workspace create "Acme" --owner ana@acme.com
docker compose exec web hiloctl member add acme bob@acme.com member      # si nunca entró, queda invitado
docker compose exec web hiloctl token create acme ana@acme.com grok --read-only --ttl 90d
docker compose exec web hiloctl --json member list acme
docker compose exec web hiloctl client preapprove acme <client-id-url>     # copiado de Ajustes → Agentes
docker compose exec web hiloctl watch acme                                # lo que hacen los agentes, en vivo
docker compose exec -T web hiloctl pipeline apply acme < pipeline.json    # pipelines, canales, auditoría, marca…
docker compose exec -T web hiloctl export acme > acme.json                 # todo el workspace, en JSON
docker compose exec -T web hiloctl import otro --dry-run < acme.json       # validar; sin --dry-run, cargar
docker compose exec -T web hiloctl import-csv acme contact --source hubspot --dry-run < contacts.csv   # un CSV de otro CRM
```

Manual completo, con pasos y recetas: [docs/HILOCTL.md](docs/HILOCTL.md).

`export` produce el mismo formato que lee `import` (y la herramienta `bulk_upsert`): sirve de backup legible, para
mover un workspace a otro Hilo y como formato de migraciones grandes sin agente. Importarlo de nuevo nunca duplica.

Un CSV de otro CRM o de una planilla se carga desde *Ajustes → Importar* (un tipo de registro por archivo: Hilo
reconoce las columnas en español o inglés, te deja ajustarlas y valida todo con una prueba antes de guardar) o con
`hiloctl import-csv` para archivos grandes. Ambos pasan por los mismos `upsert` que usan los agentes.

`hiloctl` sin argumentos lista todos los comandos. Actúa como operador del servidor, con las mismas reglas que la
web (por ejemplo, ningún workspace se queda sin propietario). El token se muestra una sola vez.

Con `HILO_UI=minimal` la web queda reducida a lo que necesita el OAuth de los agentes (iniciar sesión y
consentimiento) y todo lo demás se administra con `hiloctl`; con `HILO_UI=off` no hay páginas ni OAuth, y los agentes
entran con tokens personales emitidos por `hiloctl`.

Un agente pre-aprobado se conecta sin pantalla de consentimiento: la persona solo elige su cuenta de Google o
Microsoft (si hay un único proveedor configurado, Hilo la lleva directo). Se pre-aprueba con la URL que identifica al
agente (su `client_id`, visible en *Ajustes → Agentes* después de conectarlo una vez), con lectura y escritura o
solo lectura, y nunca con permisos de administración.

## Arquitectura

```
                 ┌──────────── edge ─────────────┐        ┌──────── data (interna) ────────┐
 navegador ─┐    │  caddy ──► web  (UI, OAuth,   │        │                                │
 agentes ───┴──► │   TLS       formularios)  ────┼────────┼──► postgres (RLS)  ◄── migrate │
   (MCP)         │        └─► mcp  (/mcp)    ────┼────────┼──►                 ◄── backup  │
                 └───────────────────────────────┘        └────────────────────────────────┘
```

| Contenedor | Rol |
|---|---|
| `caddy` | Único punto expuesto. TLS automático (Let's Encrypt), límite de cuerpo, compresión. |
| `web` | Login Google/Microsoft, consentimiento OAuth, tablero, dashboard, ajustes, superadmin, endpoint de formularios. |
| `mcp` | Servidor MCP sin estado. Solo valida tokens; nunca los emite. |
| `db` | Postgres 18. Sin puertos publicados, en una red sin salida a internet. |
| `migrate` | Aplica migraciones con el rol `hilo_migrator` y termina. |
| `backup` | `pg_dump` diario comprimido, con retención configurable. |

Stack: Go 1.27, `net/http`, pgx + sqlc (SQL tipado, sin SQL dinámico), el SDK oficial de MCP para Go,
`html/template`, Postgres 18, Caddy 2.

## Seguridad

Está pensado para correr expuesto a internet, porque un servidor MCP remoto tiene que estarlo.

- MCP remoto (Streamable HTTP) con **OAuth 2.1**: registro dinámico de clientes, Client ID Metadata Documents, PKCE
  obligatorio, rotación de refresh tokens y tokens personales para clientes sin OAuth.
- **Multi-workspace** con Postgres Row-Level Security y FKs compuestas por workspace.
- Roles de base de datos de mínimo privilegio, contenedores sin root y de solo lectura.
- CSP estricta.

## Desarrollo y harness de calidad

```sh
make hooks     # pre-commit: formato, secretos en lo staged, lint
make test      # tests unitarios, de integración y E2E contra Postgres real (con -race y cobertura)
make check     # todo lo de CI: formato, código generado, lint, tests, govulncheck, gitleaks, hadolint, Trivy
make dast      # OWASP ZAP baseline contra el stack levantado (make up)
```

| Capa | Herramienta |
|---|---|
| Lint y seguridad estática | golangci-lint: gosec, errcheck, dupl (código repetido), gocognit, staticcheck, revive… |
| Secretos | gitleaks (árbol y todo el historial) con reglas para los tokens de Hilo |
| Dependencias | govulncheck, Trivy, Dependabot |
| Contenedores | hadolint, Trivy sobre las imágenes |
| Tests | aislamiento entre tenants, matriz de permisos, flujo OAuth completo con ataques, CSRF, XSS, open redirect, fuzzing |
| DAST | OWASP ZAP baseline |

## Licencia

[Apache-2.0](LICENSE): uso libre, comercial y privado.

Documentación: [despliegue (Docker, Lightsail, EC2 + Aurora)](docs/DEPLOY.md) · [manual de hiloctl](docs/HILOCTL.md) · [seguridad y modelo de amenazas](SECURITY.md) ·
[banner de arranque](docs/STARTUP_BANNER.md) · [migrar desde otro CRM con un agente](docs/migrate/README.md) ·
[cómo contribuir](CONTRIBUTING.md) ·
[plan de próximos features](docs/PLAN_FEATURES.md).
