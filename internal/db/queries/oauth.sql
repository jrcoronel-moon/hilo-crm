-- OAuth 2.1 authorization server + personal access tokens.
-- Every secret (client secret, code, access/refresh token, PAT) is stored only
-- as its SHA-256 hash.

-- name: GetOAuthClient :one
SELECT * FROM oauth_clients WHERE client_id = $1;

-- name: CreateOAuthClient :exec
INSERT INTO oauth_clients (client_id, kind, secret_hash, name, redirect_uris)
VALUES ($1, $2, $3, $4, $5);

-- name: UpsertCIMDClient :exec
INSERT INTO oauth_clients (client_id, kind, name, redirect_uris)
VALUES ($1, 'cimd', $2, $3)
ON CONFLICT (client_id) DO UPDATE SET name = EXCLUDED.name, redirect_uris = EXCLUDED.redirect_uris
WHERE oauth_clients.kind = 'cimd';

-- name: CreateOAuthCode :one
INSERT INTO oauth_codes (code_hash, client_id, user_id, workspace_id, redirect_uri, code_challenge, scope, resource, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING family_id;

-- ConsumeOAuthCode marks a code used, atomically, and returns it only on its
-- first, unexpired use.
-- name: ConsumeOAuthCode :one
UPDATE oauth_codes SET used_at = now()
WHERE code_hash = $1 AND used_at IS NULL AND expires_at > now()
RETURNING *;

-- name: GetUsedOAuthCodeFamily :one
SELECT family_id FROM oauth_codes WHERE code_hash = $1 AND used_at IS NOT NULL;

-- name: CreateOAuthToken :exec
INSERT INTO oauth_tokens (family_id, client_id, user_id, workspace_id, access_hash, refresh_hash, scope, resource,
                          expires_at, refresh_expires_at, granted_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11);

-- ConsumeRefresh rotates a refresh token. A token already used is still
-- accepted inside the grace window (agents fire tool calls in parallel and
-- they all refresh with the same token at once).
-- name: ConsumeRefresh :one
UPDATE oauth_tokens SET refresh_used_at = COALESCE(refresh_used_at, now())
WHERE refresh_hash = sqlc.arg(refresh_hash)
  AND revoked_at IS NULL
  AND refresh_expires_at > now()
  AND (refresh_used_at IS NULL OR refresh_used_at > now() - make_interval(secs => sqlc.arg(grace_seconds)::int))
RETURNING *;

-- name: GetRefreshFamily :one
SELECT family_id FROM oauth_tokens WHERE refresh_hash = $1;

-- name: RevokeTokenFamily :exec
UPDATE oauth_tokens SET revoked_at = now() WHERE family_id = $1 AND revoked_at IS NULL;

-- name: ResolveAccessToken :one
SELECT t.id, t.user_id, t.workspace_id, t.scope, t.resource, t.client_id,
       c.name AS client_name, m.role, u.email, u.is_superadmin
FROM oauth_tokens t
JOIN oauth_clients c ON c.client_id = t.client_id
JOIN users u ON u.id = t.user_id
JOIN workspace_members m ON m.workspace_id = t.workspace_id AND m.user_id = t.user_id
JOIN workspaces w ON w.id = t.workspace_id
WHERE t.access_hash = $1 AND t.revoked_at IS NULL AND t.expires_at > now()
  AND u.disabled_at IS NULL AND w.archived_at IS NULL;

-- name: TouchOAuthClient :exec
UPDATE oauth_clients SET last_used_at = now() WHERE client_id = $1;

-- name: ListConnections :many
SELECT c.client_id, c.name, c.kind, min(t.granted_at)::timestamptz AS granted_at,
       max(t.created_at)::timestamptz AS last_refresh
FROM oauth_tokens t JOIN oauth_clients c ON c.client_id = t.client_id
WHERE t.user_id = $1 AND t.workspace_id = $2 AND t.revoked_at IS NULL AND t.refresh_expires_at > now()
GROUP BY c.client_id, c.name, c.kind
ORDER BY c.name;

-- name: RevokeConnection :execrows
UPDATE oauth_tokens SET revoked_at = now()
WHERE user_id = $1 AND workspace_id = $2 AND client_id = $3 AND revoked_at IS NULL;

-- name: CreatePAT :one
INSERT INTO personal_access_tokens (user_id, workspace_id, name, token_hash, prefix, scope, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, name, prefix, scope, expires_at, created_at;

-- name: ResolvePAT :one
SELECT p.id, p.user_id, p.workspace_id, p.scope, p.name, m.role, u.email, u.is_superadmin
FROM personal_access_tokens p
JOIN users u ON u.id = p.user_id
JOIN workspace_members m ON m.workspace_id = p.workspace_id AND m.user_id = p.user_id
JOIN workspaces w ON w.id = p.workspace_id
WHERE p.token_hash = $1 AND p.revoked_at IS NULL AND (p.expires_at IS NULL OR p.expires_at > now())
  AND u.disabled_at IS NULL AND w.archived_at IS NULL;

-- name: TouchPAT :exec
UPDATE personal_access_tokens SET last_used_at = now() WHERE id = $1;

-- name: ListPATs :many
SELECT id, name, prefix, scope, expires_at, last_used_at, created_at
FROM personal_access_tokens
WHERE user_id = $1 AND workspace_id = $2 AND revoked_at IS NULL
ORDER BY created_at DESC;

-- name: RevokePAT :execrows
UPDATE personal_access_tokens SET revoked_at = now()
WHERE id = $1 AND user_id = $2 AND workspace_id = $3 AND revoked_at IS NULL;
