-- Platform branding (single row).

-- name: GetBranding :one
SELECT app_name, logo_mime, updated_at FROM platform_settings WHERE id;

-- name: GetLogo :one
SELECT logo, logo_mime, updated_at FROM platform_settings WHERE id AND logo IS NOT NULL;

-- name: SetAppName :exec
UPDATE platform_settings SET app_name = $1, updated_by = $2, updated_at = now() WHERE id;

-- name: SetLogo :exec
UPDATE platform_settings SET logo = $1, logo_mime = $2, updated_by = $3, updated_at = now() WHERE id;

-- name: ClearLogo :exec
UPDATE platform_settings SET logo = NULL, logo_mime = '', updated_by = $1, updated_at = now() WHERE id;
