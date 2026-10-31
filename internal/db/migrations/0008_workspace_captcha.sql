-- Cloudflare Turnstile keys per workspace, entered by an admin in the UI.
-- The secret is sealed (AES-256-GCM, key derived from SESSION_SECRET, bound to
-- the workspace id); only its last characters are kept in clear as a hint.
CREATE TABLE workspace_captcha (
    workspace_id uuid PRIMARY KEY REFERENCES workspaces (id) ON DELETE CASCADE,
    site_key     text NOT NULL CHECK (site_key ~ '^[0-9A-Za-z_-]{10,100}$'),
    secret_enc   text NOT NULL CHECK (length(secret_enc) BETWEEN 10 AND 1000),
    secret_hint  text NOT NULL CHECK (length(secret_hint) <= 8),
    updated_by   uuid REFERENCES users (id) ON DELETE SET NULL,
    updated_at   timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE workspace_captcha ENABLE ROW LEVEL SECURITY;
ALTER TABLE workspace_captcha FORCE ROW LEVEL SECURITY;
CREATE POLICY workspace_isolation ON workspace_captcha
    USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
    WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

-- The public form endpoint sees only the keys of the workspace that owns the
-- active form bound to app.form_key (the lead_forms form_lookup policy limits
-- the subquery to that one form).
CREATE POLICY form_lookup ON workspace_captcha FOR SELECT
    USING (EXISTS (SELECT 1 FROM lead_forms f
                   WHERE f.workspace_id = workspace_captcha.workspace_id
                     AND f.is_active
                     AND f.public_key = NULLIF(current_setting('app.form_key', true), '')));
