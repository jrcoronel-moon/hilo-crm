# Despliegue

Hilo corre como un conjunto de contenedores, uno por componente, con `docker compose`. Hay tres formas de ponerlo
en producción, de la más simple a la más robusta:

| Camino | Cuándo | Base de datos | Backups |
|---|---|---|---|
| [A. Docker en cualquier servidor](#a-docker-en-cualquier-servidor) | ya tenés un servidor Linux (VPS, on-premise) | contenedor `db` (Postgres 18) | contenedor `backup` (diarios, en un volumen) |
| [B. AWS Lightsail](#b-aws-lightsail) | querés algo fijo, barato y con snapshots del disco | contenedor `db` | `backup` + snapshots automáticos de la instancia |
| [C. EC2 + Aurora PostgreSQL](#c-ec2--aurora-postgresql) | querés base gestionada (alta disponibilidad, backups continuos, escalar) | Aurora, fuera del host | los de Aurora (continuos, hasta 35 días) + snapshots |

Las secciones [Configuración](#configuración-env), [Arrancar y actualizar](#arrancar-y-actualizar) y
[Operación](#operación) valen para los tres.

## Qué se despliega

```
                 ┌─────────┐   /mcp    ┌─────┐
 internet ─443─► │  caddy  │ ────────► │ mcp │ ─┐
                 │  (TLS)  │   resto   ├─────┤  │   ┌────────┐
                 └─────────┘ ────────► │ web │ ─┼─► │   db   │  (o Aurora)
                                       └─────┘  │   └────────┘
                               migrate (una vez, antes que web y mcp) ─┘
                               backup  (pg_dump diario; solo con db local)
```

- `caddy`: único contenedor con puertos publicados; obtiene y renueva el certificado TLS solo.
- `web`: interfaz, login con Google/Microsoft, OAuth para los agentes, `hiloctl`.
- `mcp`: el servidor MCP que usan los agentes.
- `migrate`: aplica las migraciones y termina; `web` y `mcp` no arrancan contra un esquema viejo.
- Todos los contenedores corren sin root, con sistema de archivos de solo lectura y sin capacidades; la red `data`
  no tiene salida a internet.

Recursos: 2 GB de RAM alcanzan para un equipo (1 GB para probar). El disco crece con los backups: con 14 días de
retención, unas 20 veces el tamaño de la base.

## Requisitos comunes

1. Un dominio (`crm.tudominio.com`) con un registro `A` hacia la IP pública del servidor.
2. Puertos abiertos hacia el servidor: **80 y 443** (Caddy) y **22** solo desde tu IP.
3. Docker con el plugin compose (`docker compose version` ≥ 2.24).
4. Credenciales OAuth de Google y/o Microsoft (una sola es suficiente; con una sola, los agentes se conectan sin
   pantalla intermedia de Hilo).

## A. Docker en cualquier servidor

Ubuntu 24.04 o Debian 12, con acceso SSH:

```sh
sudo apt-get update && sudo apt-get -y upgrade
curl -fsSL https://get.docker.com | sudo sh
sudo usermod -aG docker "$USER" && newgrp docker
sudo apt-get -y install unattended-upgrades git make

git clone https://github.com/jrcoronel-moon/hilo-crm.git && cd hilo-crm
cp .env.example .env && chmod 600 .env       # completar: ver "Configuración"
```

Firewall del host (si no hay uno delante):

```sh
sudo apt-get -y install ufw
sudo ufw allow from <tu IP> to any port 22 proto tcp
sudo ufw allow 80,443/tcp
sudo ufw enable
```

Después, [Arrancar y actualizar](#arrancar-y-actualizar). Los backups quedan en el volumen `backups` del host:
copialos afuera con regularidad (`docker compose cp backup:/var/lib/postgresql/backups ./backups-copia` y de ahí a
otro lugar, o un `rsync` programado).

## B. AWS Lightsail

1. Lightsail → *Create instance* → Linux/Unix → **Ubuntu 24.04 LTS**. Plan de 2 GB de RAM.
2. *Networking*: asigná una **IP estática**; en el firewall de la instancia dejá **22 (limitado a tu IP), 80 y
   443**.
3. DNS: registro `A` de `crm.tudominio.com` hacia la IP estática.
4. Activá los **snapshots automáticos** (backup del disco completo, incluidos los volúmenes de Docker).
5. Entrá por SSH y seguí los mismos pasos que en [A](#a-docker-en-cualquier-servidor) (Docker, clonar, `.env`); el
   firewall lo maneja Lightsail, no hace falta `ufw`.

Después, [Arrancar y actualizar](#arrancar-y-actualizar).

Para restaurar un desastre: creá una instancia desde el último snapshot, asignale la IP estática y arrancá el
stack; o una instancia nueva y restaurá el último `hilo-*.sql.gz` del volumen `backups` (ver Operación).

## C. EC2 + Aurora PostgreSQL

La aplicación corre en una instancia EC2 y la base en un clúster Aurora PostgreSQL. Los contenedores `db` y
`backup` no se levantan.

### C.1 Red

- Una VPC con al menos dos subredes privadas (Aurora las exige) y una pública para la EC2.
- Security group **`hilo-app`** (EC2): entrada 80 y 443 desde `0.0.0.0/0`, 22 desde tu IP.
- Security group **`hilo-db`** (Aurora): entrada 5432 **solo desde `hilo-app`**. Sin acceso público.

### C.2 Aurora

1. RDS → *Create database* → **Aurora PostgreSQL-Compatible**, versión 16 o superior.
2. Capacidad: **Serverless v2** (mínimo 0.5 ACU) para empezar, o una instancia `db.t4g.medium`. Para alta
   disponibilidad, agregá una réplica en otra zona (Aurora conmuta sola).
3. Nombre de la base inicial: `hilo`. Usuario maestro: `hilo_master` con contraseña en Secrets Manager.
4. Conectividad: la VPC de arriba, subredes privadas, security group `hilo-db`, **sin** acceso público.
5. Backups: retención **14 días** o más; *deletion protection* activada; cifrado en reposo (por defecto).
6. Anotá el **writer endpoint** (`hilo.cluster-xxxx.<región>.rds.amazonaws.com`).

### C.3 EC2

1. Ubuntu 24.04, `t4g.small` (ARM) o `t3.small`, en la subred pública con IP elástica, security group `hilo-app`.
2. DNS: registro `A` hacia la IP elástica.
3. Docker, clonar y `.env` como en [A](#a-docker-en-cualquier-servidor), más el cliente de Postgres y el
   certificado raíz de RDS:

```sh
sudo apt-get -y install postgresql-client
cd hilo-crm
curl -fsSL https://truststore.pki.rds.amazonaws.com/global/global-bundle.pem -o deploy/rds-ca.pem
```

### C.4 Roles en Aurora

Una sola vez, como usuario maestro, se crean los dos roles de Hilo (el equivalente a lo que hace el contenedor
`db` al inicializarse). Generá dos contraseñas y guardalas para el `.env`:

```sh
MIG_PW=$(openssl rand -base64 48 | tr -d '/+=' | cut -c1-48)
APP_PW=$(openssl rand -base64 48 | tr -d '/+=' | cut -c1-48)
psql "host=<writer endpoint> dbname=hilo user=hilo_master sslmode=verify-full sslrootcert=deploy/rds-ca.pem" \
     -v migrator_pw="$MIG_PW" -v app_pw="$APP_PW" -f deploy/postgres/roles-external.sql
echo "HILO_MIGRATOR_PASSWORD=$MIG_PW  HILO_APP_PASSWORD=$APP_PW"   # al .env, y después `history -c`
```

### C.5 `.env` para Aurora

Además de lo común, dos URLs completas (las del `docker-compose.yml` apuntan al contenedor `db` y acá no se usan):

```sh
DATABASE_URL=postgres://hilo_app:<HILO_APP_PASSWORD>@<writer endpoint>:5432/hilo?sslmode=verify-full&sslrootcert=/etc/ssl/rds-ca.pem&pool_max_conns=20
MIGRATOR_DATABASE_URL=postgres://hilo_migrator:<HILO_MIGRATOR_PASSWORD>@<writer endpoint>:5432/hilo?sslmode=verify-full&sslrootcert=/etc/ssl/rds-ca.pem
```

Si la contraseña tiene caracteres especiales, codificalos para URL (`@` → `%40`, etc.); las generadas con el
comando de arriba no los tienen.

### C.6 Arrancar

```sh
export GIT_SHA=$(git rev-parse --short HEAD)
docker compose -f docker-compose.yml -f deploy/compose.prod.yml -f deploy/compose.aurora.yml up -d --build --wait
```

`deploy/compose.aurora.yml` deja afuera `db` y `backup`, monta el certificado de RDS y abre la red `data` hacia
afuera. Para actualizar, el mismo comando después de `git pull`.

### C.7 Backups y recuperación

- Aurora hace backups continuos: *point-in-time recovery* dentro de la retención, y snapshots manuales antes de
  cada actualización grande (`aws rds create-db-cluster-snapshot …`).
- Para una copia lógica fuera de AWS: desde la EC2, `pg_dump "host=<writer endpoint> dbname=hilo user=hilo_master
  sslmode=verify-full sslrootcert=deploy/rds-ca.pem" | gzip > hilo-$(date +%F).sql.gz`, o por workspace con
  `hiloctl export` (legible, reimportable).
- Si cambia el writer endpoint (restauración a un clúster nuevo), actualizá las dos URLs del `.env` y recreá los
  contenedores.

### C.8 Crecer

`web` y `mcp` no guardan estado: se pueden correr en más de una EC2 detrás de un Application Load Balancer (TLS en
el ALB, Caddy ya no hace falta). El tiempo real usa `LISTEN/NOTIFY` de Postgres, así que cada instancia ve todos los
cambios sin nada extra; el ALB no necesita sesiones pegajosas. Las migraciones las corre una sola instancia.

## Configuración (`.env`)

| Variable | Valor |
|---|---|
| `APP_BASE_URL` | `https://crm.tudominio.com` |
| `SITE_ADDRESS` | `crm.tudominio.com` |
| `ACME_EMAIL` | tu email (avisos de Let's Encrypt) |
| `SESSION_SECRET`, `POSTGRES_PASSWORD`, `HILO_MIGRATOR_PASSWORD`, `HILO_APP_PASSWORD` | `openssl rand -base64 48 \| tr -d '/+=' \| cut -c1-48`, uno distinto por variable (con Aurora, las dos últimas son las del paso C.4 y `POSTGRES_PASSWORD` puede ser cualquiera) |
| `GOOGLE_CLIENT_ID` / `GOOGLE_CLIENT_SECRET` | OAuth client tipo *Web*; redirect `https://crm.tudominio.com/auth/google/callback` |
| `MICROSOFT_CLIENT_ID` / `MICROSOFT_CLIENT_SECRET` | App registration; redirect `https://crm.tudominio.com/auth/microsoft/callback`; optional claims `email` y `xms_edov` en el ID token |
| `SUPERADMIN_EMAILS` | tu email |
| `DEV_LOGIN_ENABLED` | `false` (el override de producción lo fuerza igual) |
| `HILO_UI` | `full` (por defecto), `minimal` (solo login y consentimiento de agentes) u `off` (solo tokens) |
| `BACKUP_KEEP_DAYS` | días de backups locales (14); sin efecto con Aurora |

## Arrancar y actualizar

```sh
export GIT_SHA=$(git rev-parse --short HEAD)
docker compose -f docker-compose.yml -f deploy/compose.prod.yml up -d --build --wait     # A y B
# C: agregar -f deploy/compose.aurora.yml
docker logs hilo-web-1 2>&1 | head -15     # banner: commit, migración aplicada, integraciones activas
```

Entrá a `https://crm.tudominio.com`, iniciá sesión con tu cuenta (superadmin) y en **Superadmin** habilitá el
dominio de email de tu equipo. O todo por terminal: ver [HILOCTL.md](HILOCTL.md), sección "Primeros pasos".

Actualizar:

```sh
git pull
export GIT_SHA=$(git rev-parse --short HEAD)
docker compose -f docker-compose.yml -f deploy/compose.prod.yml up -d --build --wait     # (+ aurora en C)
```

`migrate` corre antes que `web` y `mcp`. Si una migración falla, los servicios viejos siguen corriendo; mirá
`docker logs hilo-migrate-1`.

## Operación

```sh
docker compose exec web hiloctl                                    # todos los comandos (ver docs/HILOCTL.md)
docker compose exec web hiloctl watch <workspace>                  # actividad de los agentes en vivo
docker compose exec -T web hiloctl export <workspace> > ws.json    # copia legible de un workspace
docker logs hilo-web-1 --since 1h                                  # logs sin query strings (no necesitan el .env)

# Backups locales (A y B):
docker compose exec backup ls -l /var/lib/postgresql/backups
docker compose exec -T backup sh -c 'gunzip -c /var/lib/postgresql/backups/hilo-AAAAMMDD-HHMM.sql.gz' \
  | docker compose exec -T db psql -U postgres -d hilo             # restaurar
```

Lista de control después de desplegar:

- [ ] `https://crm.tudominio.com` carga con candado; `http://` redirige.
- [ ] El banner de `web` muestra el commit esperado y "migración aplicada".
- [ ] Login con Google o Microsoft funciona y el dominio del equipo está habilitado.
- [ ] Un agente se conecta a `https://crm.tudominio.com/mcp` (ver [AGENTS.md](AGENTS.md)).
- [ ] Hay un backup reciente (`backup` o Aurora) y probaste restaurarlo una vez.
- [ ] `.env` con permisos `600`, SSH solo con llaves y desde tus IPs, actualizaciones automáticas del sistema
      activas.
