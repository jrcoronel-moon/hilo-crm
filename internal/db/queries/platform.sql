-- Platform-level queries: users, identities, workspaces, membership, sessions.
-- None of these tables has RLS; every query here is keyed by an id the caller
-- has already been authorized for.

-- name: GetUser :one
SELECT * FROM users WHERE id = $1;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = $1;

-- name: GetUserByIdentity :one
SELECT u.* FROM users u
JOIN user_identities i ON i.user_id = u.id
WHERE i.provider = $1 AND i.subject = $2;

-- name: CreateUser :one
INSERT INTO users (email, name, avatar_url, is_superadmin)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: TouchUserLogin :exec
UPDATE users SET last_login_at = now(), name = $2, avatar_url = $3 WHERE id = $1;

-- name: SetSuperadmin :execrows
UPDATE users SET is_superadmin = $2 WHERE email = $1;

-- name: ListSuperadmins :many
SELECT * FROM users WHERE is_superadmin ORDER BY email;

-- name: ListUsers :many
SELECT * FROM users ORDER BY created_at DESC LIMIT $1;

-- name: CreateIdentity :exec
INSERT INTO user_identities (user_id, provider, subject, email)
VALUES ($1, $2, $3, $4)
ON CONFLICT (provider, subject) DO NOTHING;

-- name: IsPlatformDomainAllowed :one
SELECT EXISTS (SELECT 1 FROM platform_allowed_domains WHERE domain = $1);

-- name: ListPlatformDomains :many
SELECT * FROM platform_allowed_domains ORDER BY domain;

-- name: AddPlatformDomain :exec
INSERT INTO platform_allowed_domains (domain, created_by) VALUES ($1, $2)
ON CONFLICT (domain) DO NOTHING;

-- name: RemovePlatformDomain :execrows
DELETE FROM platform_allowed_domains WHERE domain = $1;

-- name: CreateWorkspace :one
INSERT INTO workspaces (name, slug, created_by) VALUES ($1, $2, $3) RETURNING *;

-- name: GetWorkspace :one
SELECT * FROM workspaces WHERE id = $1 AND archived_at IS NULL;

-- name: ListAllWorkspaces :many
SELECT w.*, (SELECT count(*) FROM workspace_members m WHERE m.workspace_id = w.id)::int AS member_count
FROM workspaces w ORDER BY w.created_at DESC;

-- name: SlugExists :one
SELECT EXISTS (SELECT 1 FROM workspaces WHERE slug = $1);

-- name: ListUserWorkspaces :many
SELECT w.id, w.name, w.slug, m.role
FROM workspace_members m JOIN workspaces w ON w.id = m.workspace_id
WHERE m.user_id = $1 AND w.archived_at IS NULL
ORDER BY w.name;

-- name: GetMembership :one
SELECT m.role FROM workspace_members m
JOIN workspaces w ON w.id = m.workspace_id
WHERE m.workspace_id = $1 AND m.user_id = $2 AND w.archived_at IS NULL;

-- name: AddMember :exec
INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ($1, $2, $3)
ON CONFLICT (workspace_id, user_id) DO NOTHING;

-- name: UpdateMemberRole :execrows
UPDATE workspace_members SET role = $3 WHERE workspace_id = $1 AND user_id = $2;

-- name: RemoveMember :execrows
DELETE FROM workspace_members WHERE workspace_id = $1 AND user_id = $2;

-- name: CountOwners :one
SELECT count(*)::int FROM workspace_members WHERE workspace_id = $1 AND role = 'owner';

-- name: ListMembers :many
SELECT u.id, u.email, u.name, m.role, m.created_at
FROM workspace_members m JOIN users u ON u.id = m.user_id
WHERE m.workspace_id = $1 ORDER BY u.email;

-- name: ListWorkspaceDomains :many
SELECT * FROM workspace_domains WHERE workspace_id = $1 ORDER BY domain;

-- name: AddWorkspaceDomain :exec
INSERT INTO workspace_domains (workspace_id, domain, default_role) VALUES ($1, $2, $3)
ON CONFLICT (workspace_id, domain) DO UPDATE SET default_role = EXCLUDED.default_role;

-- name: RemoveWorkspaceDomain :execrows
DELETE FROM workspace_domains WHERE workspace_id = $1 AND domain = $2;

-- name: WorkspacesForDomain :many
SELECT d.workspace_id, d.default_role FROM workspace_domains d
JOIN workspaces w ON w.id = d.workspace_id
WHERE d.domain = $1 AND w.archived_at IS NULL;

-- name: CreateInvitation :one
INSERT INTO invitations (workspace_id, email, role, invited_by, expires_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (workspace_id, email) WHERE accepted_at IS NULL AND revoked_at IS NULL
DO UPDATE SET role = EXCLUDED.role, expires_at = EXCLUDED.expires_at, invited_by = EXCLUDED.invited_by
RETURNING *;

-- name: PendingInvitationsForEmail :many
SELECT i.* FROM invitations i JOIN workspaces w ON w.id = i.workspace_id
WHERE i.email = $1 AND i.accepted_at IS NULL AND i.revoked_at IS NULL
  AND i.expires_at > now() AND w.archived_at IS NULL;

-- name: HasPendingInvitation :one
SELECT EXISTS (
  SELECT 1 FROM invitations
  WHERE email = $1 AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > now()
);

-- name: AcceptInvitation :exec
UPDATE invitations SET accepted_at = now() WHERE id = $1;

-- name: ListInvitations :many
SELECT * FROM invitations
WHERE workspace_id = $1 AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > now()
ORDER BY created_at DESC;

-- name: RevokeInvitation :execrows
UPDATE invitations SET revoked_at = now()
WHERE id = $1 AND workspace_id = $2 AND accepted_at IS NULL AND revoked_at IS NULL;

-- name: CreateSession :exec
INSERT INTO sessions (token_hash, user_id, csrf_token, workspace_id, expires_at)
VALUES ($1, $2, $3, $4, $5);

-- name: GetSession :one
SELECT s.*, u.email, u.name, u.is_superadmin
FROM sessions s JOIN users u ON u.id = s.user_id
WHERE s.token_hash = $1 AND s.expires_at > now() AND u.disabled_at IS NULL;

-- name: TouchSession :exec
UPDATE sessions SET last_seen_at = now() WHERE token_hash = $1;

-- name: SetSessionWorkspace :exec
UPDATE sessions SET workspace_id = $2 WHERE token_hash = $1;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE token_hash = $1;

-- name: DeleteUserSessions :exec
DELETE FROM sessions WHERE user_id = $1;

-- name: PurgeExpired :exec
WITH s AS (DELETE FROM sessions WHERE sessions.expires_at < now()),
     c AS (DELETE FROM oauth_codes WHERE oauth_codes.expires_at < now() - interval '1 day'),
     t AS (DELETE FROM oauth_tokens WHERE oauth_tokens.refresh_expires_at < now() - interval '1 day')
SELECT 1;

-- BindWorkspace binds RLS to a workspace inside an already open transaction
-- (used when a workspace is created and seeded in one transaction).
-- name: BindWorkspace :exec
SELECT set_config('app.workspace_id', sqlc.arg(workspace_id)::text, true);

-- name: GetMemberByEmail :one
SELECT u.id, m.role FROM users u
JOIN workspace_members m ON m.user_id = u.id
WHERE m.workspace_id = $1 AND u.email = $2;
