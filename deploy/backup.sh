#!/bin/sh
# Daily logical backup of the whole cluster into the `backups` volume, keeping
# BACKUP_KEEP_DAYS days. Copy the volume off the host (or snapshot the
# Lightsail instance) for off-site retention. Restore with:
#   docker compose exec -T db psql -U postgres -d hilo < hilo-YYYYmmdd-HHMM.sql
set -eu
umask 077
dir="${BACKUP_DIR:-/backups}"
mkdir -p "$dir"
while true; do
  f="$dir/hilo-$(date -u +%Y%m%d-%H%M).sql.gz"
  if pg_dump --no-owner --clean --if-exists | gzip -9 > "$f.tmp"; then
    mv "$f.tmp" "$f"
    echo "backup ok: $f"
  else
    rm -f "$f.tmp"
    echo "backup FAILED" >&2
  fi
  find "$dir" -name 'hilo-*.sql.gz' -mtime +"${BACKUP_KEEP_DAYS:-14}" -delete
  sleep 86400
done
