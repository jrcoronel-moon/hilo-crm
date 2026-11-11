-- Pictures for records: a company's logo and a contact's photo, set by an
-- agent (or a person) as an https URL. Browsers never load that URL: the web
-- app fetches it server-side (public addresses only, size-capped, raster
-- images only), caches it here per workspace and serves it from its own
-- origin, so the CSP stays 'self' and no third party learns who looked.
ALTER TABLE companies ADD COLUMN logo_url text NOT NULL DEFAULT '' CHECK (logo_url = '' OR (logo_url LIKE 'https://%' AND length(logo_url) <= 1000));
ALTER TABLE contacts ADD COLUMN photo_url text NOT NULL DEFAULT '' CHECK (photo_url = '' OR (photo_url LIKE 'https://%' AND length(photo_url) <= 1000));

CREATE TABLE avatar_cache (
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    url          text NOT NULL CHECK (length(url) <= 1000),
    content_type text NOT NULL DEFAULT '' CHECK (content_type IN ('', 'image/png', 'image/jpeg', 'image/gif', 'image/webp')),
    data         bytea CHECK (data IS NULL OR length(data) <= 524288), -- NULL: the last fetch failed
    fetched_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, url)
);

ALTER TABLE avatar_cache ENABLE ROW LEVEL SECURITY;
ALTER TABLE avatar_cache FORCE ROW LEVEL SECURITY;
CREATE POLICY workspace_isolation ON avatar_cache
    USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
    WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
