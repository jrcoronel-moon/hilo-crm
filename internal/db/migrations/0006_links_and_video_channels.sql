-- Opportunities carry links to things outside Hilo (proposals, shared folders,
-- recordings…), so an agent can leave every external file noted on the deal.
ALTER TABLE opportunities ADD COLUMN links jsonb NOT NULL DEFAULT '[]'
    CHECK (jsonb_typeof(links) = 'array' AND jsonb_array_length(links) <= 50 AND pg_column_size(links) <= 32768);

-- Channels gain video kinds (video calls, and video as a lead source).
ALTER TABLE channels DROP CONSTRAINT channels_kind_check;
ALTER TABLE channels ADD CONSTRAINT channels_kind_check CHECK (kind IN ('web', 'form', 'email', 'phone', 'social',
    'referral', 'event', 'outbound', 'partner', 'ads', 'videocall', 'video', 'other'));

-- Existing workspaces get the two new default channels (new ones get them
-- from SeedWorkspace). Row-level security is forced on channels, so the
-- workspace is bound for each insert.
DO $$
DECLARE w record;
BEGIN
    FOR w IN SELECT id FROM workspaces WHERE archived_at IS NULL LOOP
        PERFORM set_config('app.workspace_id', w.id::text, true);
        INSERT INTO channels (workspace_id, name, kind)
        SELECT w.id, v.name, v.kind
        FROM (VALUES ('Videoconferencia', 'videocall'), ('Video', 'video')) AS v (name, kind)
        WHERE NOT EXISTS (SELECT 1 FROM channels c WHERE c.workspace_id = w.id AND lower(c.name) = lower(v.name));
    END LOOP;
    PERFORM set_config('app.workspace_id', '', true);
END $$;
