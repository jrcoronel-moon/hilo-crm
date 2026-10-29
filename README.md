# Hilo CRM

**El CRM headless para que tus agentes de IA sigan cada oportunidad.**

Hilo es un CRM mínimo pensado para ser operado por agentes (Claude, ChatGPT, Grok, Cursor y cualquier cliente
[MCP](https://modelcontextprotocol.io)). Los agentes cargan empresas, contactos y oportunidades, registran cada
interacción y mueven el funnel; las personas entran con Google o Microsoft para ver el tablero, un dashboard liviano
y administrar el workspace.

- **MCP remoto** (Streamable HTTP) con **OAuth 2.1**: registro dinámico de clientes, Client ID Metadata Documents,
  PKCE obligatorio, rotación de refresh tokens y tokens personales para clientes sin OAuth.
- **Multi-workspace**, con dominios de email habilitados, invitaciones, roles y superadmins.
- **Pipelines configurables por workspace**: cada etapa es `initial` (exactamente una), `in_progress` (al menos una),
  `won` o `lost` (al menos una de cada). El estado de una oportunidad sigue a la categoría de su etapa.
- **Canales** y **interacciones** (email, llamada, reunión, nota, mensaje, social, formulario; entrante, saliente, interna).
- **Formularios de leads** públicos que crean contacto + oportunidad + interacción sin poder sobrescribir datos.
- **Métricas**: volumen y valor ponderado por etapa, win rate, ciclo, tiempo en etapa, estancadas, leads por canal,
  interacciones por semana.
- **Seguridad primero**: Postgres con Row-Level Security y FKs compuestas por workspace, roles de base de datos
  de mínimo privilegio, CSP estricta, contenedores sin root y de solo lectura, y un harness que corre lint de
  seguridad, secretos, vulnerabilidades, tests de aislamiento entre tenants y DAST en cada cambio.

Licencia [Apache-2.0](LICENSE): uso libre, comercial y privado.

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

## Empezar en local

Requisitos: Docker y Go 1.27+ (solo para desarrollar).

```sh
cp .env.example .env
# completá los secretos (el archivo explica cómo generarlos) y, para probar sin Google/Microsoft:
#   DEV_LOGIN_ENABLED=true   SUPERADMIN_EMAILS=vos@tuempresa.com
make up          # http://localhost:8080
```

Entrá con tu email (modo desarrollo), creá un workspace y en **Ajustes → Agentes** vas a ver la URL MCP.

## Conectar un agente

URL del servidor MCP: `https://<tu-dominio>/mcp`

- **Claude (web/desktop)**: Ajustes → Conectores → *Agregar conector personalizado* → pegá la URL. Se abre Hilo para
  iniciar sesión y autorizar; elegís el workspace y, si querés, acceso de solo lectura.
- **Claude Code**: `claude mcp add --transport http hilo https://<tu-dominio>/mcp`
- **ChatGPT, Grok, Cursor y otros**: agregá la URL como servidor MCP remoto. Si el cliente no soporta OAuth, creá un
  token personal en *Ajustes → Agentes* y envialo como `Authorization: Bearer hilo_pat_…`.

### Herramientas MCP

| Lectura | Escritura | Administración |
|---|---|---|
| `search_crm`, `list_pipelines`, `list_channels`, `get_company`, `get_contact`, `get_opportunity`, `list_companies`, `list_contacts`, `list_opportunities`, `get_timeline`, `get_metrics` | `upsert_company`, `upsert_contact`, `upsert_opportunity`, `move_opportunity_stage`, `log_interaction`, `archive_record` | `configure_pipeline`, `upsert_channel` |

Cada agente ve solo las herramientas que su rol y su token permiten. Los `upsert_*` son idempotentes (por `id`,
`external_id`, dominio o email), así que un agente puede reintentar sin duplicar.

## Permisos

| Rol | Puede |
|---|---|
| Lector | leer |
| Miembro | leer y escribir datos del CRM |
| Admin | además, configurar pipelines, canales, formularios, miembros y dominios |
| Propietario | además, gestionar propietarios |
| Superadmin (plataforma) | dominios habilitados para entrar y vista de todos los workspaces |

Un token OAuth o personal puede restringirse a solo lectura; nunca puede ampliar lo que permite el rol.

**Quién puede entrar**: un email verificado por Google o Microsoft cuyo dominio esté habilitado por un superadmin, o
con una invitación pendiente. Sin dominios configurados, solo superadmins e invitados. Los superadmins se definen con
`SUPERADMIN_EMAILS` o con `hiloctl superadmin grant <email>`.

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

Documentación: [despliegue en AWS Lightsail](docs/DEPLOY_LIGHTSAIL.md) · [seguridad y modelo de amenazas](SECURITY.md) ·
[banner de arranque](docs/STARTUP_BANNER.md) · [cómo contribuir](CONTRIBUTING.md).
