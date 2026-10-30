-- Agents an admin trusts for a workspace: their authorization requests skip
-- the consent screen. Only clients identified by a Client ID Metadata
-- Document (an https URL whose domain proves who they are) qualify, and never
-- with crm:admin: administration always needs the user's explicit consent.
-- client_id is not a foreign key: the client may not have connected yet.
CREATE TABLE oauth_preapprovals (
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    client_id     text NOT NULL CHECK (client_id LIKE 'https://%' AND length(client_id) <= 2000),
    scope         text NOT NULL CHECK (scope IN ('crm:read', 'crm:read crm:write')),
    created_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, client_id)
);
