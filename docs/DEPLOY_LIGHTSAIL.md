# Despliegue en AWS Lightsail

Una instancia Linux con Docker y `docker compose`. Caddy obtiene y renueva el certificado TLS.

## 1. Instancia

1. Lightsail → *Create instance* → Linux/Unix → **Ubuntu 24.04 LTS**. Plan recomendado: 2 GB de RAM (1 GB alcanza
   para pruebas).
2. *Networking*: asigná una **IP estática** y dejá abiertos solo **22 (SSH, idealmente limitado a tu IP), 80 y 443**.
3. DNS: un registro `A` de `crm.tudominio.com` hacia la IP estática.
4. Activá **snapshots automáticos** de la instancia (backup del disco completo).

## 2. Docker

```sh
sudo apt-get update && sudo apt-get -y upgrade
curl -fsSL https://get.docker.com | sudo sh
sudo usermod -aG docker ubuntu && newgrp docker
sudo apt-get -y install unattended-upgrades git make
```

## 3. Código y configuración

```sh
git clone https://github.com/jrcoronel-moon/hilo-crm.git && cd hilo-crm
cp .env.example .env && chmod 600 .env
```

Completá `.env`:

| Variable | Valor |
|---|---|
| `APP_BASE_URL` | `https://crm.tudominio.com` |
| `SITE_ADDRESS` | `crm.tudominio.com` |
| `ACME_EMAIL` | tu email (avisos de Let's Encrypt) |
| `SESSION_SECRET`, `POSTGRES_PASSWORD`, `HILO_MIGRATOR_PASSWORD`, `HILO_APP_PASSWORD` | `openssl rand -base64 48 \| tr -d '/+=' \| cut -c1-48` (uno distinto por variable) |
| `GOOGLE_CLIENT_ID/SECRET` | OAuth client tipo *Web*; redirect `https://crm.tudominio.com/auth/google/callback` |
| `MICROSOFT_CLIENT_ID/SECRET` | App registration; redirect `https://crm.tudominio.com/auth/microsoft/callback`; agregá los optional claims `email` y `xms_edov` al ID token |
| `SUPERADMIN_EMAILS` | tu email |
| `DEV_LOGIN_ENABLED` | `false` (el override de producción lo fuerza igual) |

## 4. Arrancar

```sh
export GIT_SHA=$(git rev-parse --short HEAD)
docker compose -f docker-compose.yml -f deploy/compose.prod.yml up -d --build --wait
docker logs hilo-web-1 2>&1 | head -15     # banner: commit, migración, integraciones activas
```

Entrá a `https://crm.tudominio.com`, iniciá sesión con tu cuenta (superadmin) y en **Superadmin** habilitá el
dominio de email de tu equipo.

## 5. Actualizar

```sh
git pull
export GIT_SHA=$(git rev-parse --short HEAD)
docker compose -f docker-compose.yml -f deploy/compose.prod.yml up -d --build --wait
```

`migrate` corre antes que `web` y `mcp`; los servicios se niegan a arrancar contra un esquema más viejo que su código.

## 6. Operación

```sh
docker compose exec web hiloctl superadmin grant ana@tudominio.com
docker compose exec web hiloctl domain allow tudominio.com
docker compose exec backup ls -l /var/lib/postgresql/backups     # backups diarios
# restaurar un backup:
docker compose exec -T backup sh -c 'gunzip -c /var/lib/postgresql/backups/hilo-AAAAMMDD-HHMM.sql.gz' \
  | docker compose exec -T db psql -U postgres -d hilo
```

Para ver los logs, `docker logs <contenedor>` no necesita el `.env` (a diferencia de `docker compose logs`).
