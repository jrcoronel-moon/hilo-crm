-- 0001_init.sql — Hilo CRM base schema.
--
-- Tenant isolation is enforced three times over:
--   1. every CRM row carries workspace_id and every cross-entity reference is a
--      composite FK (workspace_id, id), so a row can never point at another
--      workspace's data, whatever the application does;
--   2. Row-Level Security filters every CRM table by app.workspace_id, which the
--      application sets per transaction (see internal/db);
--   3. the application layer scopes every query by workspace (tested by the
--      tenant-isolation harness).
--
-- Runs as the migrator role (table owner). The app role only gets DML.

CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- ───────────────────────────── platform ─────────────────────────────

CREATE TABLE users (
    id             uuid PRIMARY KEY DEFAULT uuidv7(),
    email          text NOT NULL CHECK (email = lower(email) AND email LIKE '%_@_%'),
    name           text NOT NULL DEFAULT '' CHECK (length(name) <= 200),
    avatar_url     text NOT NULL DEFAULT '' CHECK (length(avatar_url) <= 2000),
    is_superadmin  boolean NOT NULL DEFAULT false,
    disabled_at    timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now(),
    last_login_at  timestamptz
);
CREATE UNIQUE INDEX users_email_key ON users (email);

-- One row per external identity. subject is the IdP's stable id: Google "sub",
-- Microsoft "tid:oid" (never the email, which Microsoft does not verify).
CREATE TABLE user_identities (
    id          uuid PRIMARY KEY DEFAULT uuidv7(),
    user_id     uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    provider    text NOT NULL CHECK (provider IN ('google', 'microsoft', 'dev')),
    subject     text NOT NULL CHECK (length(subject) BETWEEN 1 AND 300),
    email       text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (provider, subject)
);
CREATE INDEX user_identities_user_idx ON user_identities (user_id);

-- Email domains allowed to sign in to the platform at all (superadmin-managed).
-- Closed by default: an empty table means only superadmins and invited emails.
CREATE TABLE platform_allowed_domains (
    domain      text PRIMARY KEY CHECK (domain = lower(domain) AND domain ~ '^[a-z0-9.-]+\.[a-z]{2,}$'),
    created_by  uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE workspaces (
    id           uuid PRIMARY KEY DEFAULT uuidv7(),
    name         text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    slug         text NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9][a-z0-9-]{1,62}$'),
    created_by   uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    archived_at  timestamptz
);

CREATE TABLE workspace_members (
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    user_id       uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role          text NOT NULL CHECK (role IN ('owner', 'admin', 'member', 'viewer')),
    created_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, user_id)
);
CREATE INDEX workspace_members_user_idx ON workspace_members (user_id);

-- Users whose verified email is on one of these domains may join the workspace
-- on their own, with default_role.
CREATE TABLE workspace_domains (
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    domain        text NOT NULL CHECK (domain = lower(domain) AND domain ~ '^[a-z0-9.-]+\.[a-z]{2,}$'),
    default_role  text NOT NULL DEFAULT 'member' CHECK (default_role IN ('admin', 'member', 'viewer')),
    created_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, domain)
);
CREATE INDEX workspace_domains_domain_idx ON workspace_domains (domain);

CREATE TABLE invitations (
    id            uuid PRIMARY KEY DEFAULT uuidv7(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    email         text NOT NULL CHECK (email = lower(email) AND email LIKE '%_@_%'),
    role          text NOT NULL CHECK (role IN ('admin', 'member', 'viewer')),
    invited_by    uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    expires_at    timestamptz NOT NULL,
    accepted_at   timestamptz,
    revoked_at    timestamptz
);
CREATE UNIQUE INDEX invitations_pending_key ON invitations (workspace_id, email)
    WHERE accepted_at IS NULL AND revoked_at IS NULL;
CREATE INDEX invitations_email_idx ON invitations (email);

-- Server-side web sessions. Only the SHA-256 of the cookie value is stored.
CREATE TABLE sessions (
    token_hash    text PRIMARY KEY,
    user_id       uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    csrf_token    text NOT NULL,
    workspace_id  uuid REFERENCES workspaces (id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    expires_at    timestamptz NOT NULL,
    last_seen_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sessions_user_idx ON sessions (user_id);
CREATE INDEX sessions_expires_idx ON sessions (expires_at);

-- ───────────────────────────── OAuth / tokens ─────────────────────────────

CREATE TABLE oauth_clients (
    client_id      text PRIMARY KEY CHECK (length(client_id) BETWEEN 1 AND 2000),
    kind           text NOT NULL CHECK (kind IN ('dcr', 'cimd')),
    secret_hash    text NOT NULL DEFAULT '',
    name           text NOT NULL DEFAULT '' CHECK (length(name) <= 200),
    redirect_uris  text[] NOT NULL CHECK (cardinality(redirect_uris) BETWEEN 1 AND 10),
    created_at     timestamptz NOT NULL DEFAULT now(),
    last_used_at   timestamptz
);

CREATE TABLE oauth_codes (
    code_hash       text PRIMARY KEY,
    -- Tokens minted from this code join this family, so replaying an already
    -- redeemed code can revoke them all (RFC 9700 §4.5).
    family_id       uuid NOT NULL DEFAULT uuidv7(),
    client_id       text NOT NULL REFERENCES oauth_clients (client_id) ON DELETE CASCADE,
    user_id         uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    workspace_id    uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    redirect_uri    text NOT NULL,
    code_challenge  text NOT NULL CHECK (length(code_challenge) = 43),
    scope           text NOT NULL,
    resource        text NOT NULL,
    expires_at      timestamptz NOT NULL,
    used_at         timestamptz
);

-- A token family is one consent: every refresh rotates the pair inside the same
-- family, and presenting an already-rotated refresh token (outside the grace
-- window) revokes the whole family (RFC 9700 §4.14.2).
CREATE TABLE oauth_tokens (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    family_id           uuid NOT NULL,
    client_id           text NOT NULL REFERENCES oauth_clients (client_id) ON DELETE CASCADE,
    user_id             uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    workspace_id        uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    access_hash         text NOT NULL UNIQUE,
    refresh_hash        text NOT NULL UNIQUE,
    scope               text NOT NULL,
    resource            text NOT NULL,
    expires_at          timestamptz NOT NULL,
    refresh_expires_at  timestamptz NOT NULL,
    refresh_used_at     timestamptz,
    granted_at          timestamptz NOT NULL,
    revoked_at          timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX oauth_tokens_family_idx ON oauth_tokens (family_id);
CREATE INDEX oauth_tokens_user_idx ON oauth_tokens (user_id, workspace_id);

CREATE TABLE personal_access_tokens (
    id            uuid PRIMARY KEY DEFAULT uuidv7(),
    user_id       uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    name          text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    token_hash    text NOT NULL UNIQUE,
    prefix        text NOT NULL,
    scope         text NOT NULL,
    expires_at    timestamptz,
    last_used_at  timestamptz,
    revoked_at    timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX pat_user_idx ON personal_access_tokens (user_id, workspace_id);

-- ───────────────────────────── CRM ─────────────────────────────

CREATE TABLE pipelines (
    id            uuid PRIMARY KEY DEFAULT uuidv7(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    name          text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    is_default    boolean NOT NULL DEFAULT false,
    position      int NOT NULL DEFAULT 0,
    archived_at   timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, id)
);
CREATE UNIQUE INDEX pipelines_name_key ON pipelines (workspace_id, lower(name)) WHERE archived_at IS NULL;
CREATE UNIQUE INDEX pipelines_default_key ON pipelines (workspace_id) WHERE is_default AND archived_at IS NULL;

CREATE TABLE pipeline_stages (
    id            uuid PRIMARY KEY DEFAULT uuidv7(),
    workspace_id  uuid NOT NULL,
    pipeline_id   uuid NOT NULL,
    name          text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    category      text NOT NULL CHECK (category IN ('initial', 'in_progress', 'won', 'lost')),
    position      int NOT NULL,
    probability   int NOT NULL CHECK (probability BETWEEN 0 AND 100),
    created_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, id),
    UNIQUE (pipeline_id, id),
    FOREIGN KEY (workspace_id, pipeline_id) REFERENCES pipelines (workspace_id, id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX pipeline_stages_name_key ON pipeline_stages (pipeline_id, lower(name));
CREATE UNIQUE INDEX pipeline_stages_position_key ON pipeline_stages (pipeline_id, position) ;
CREATE UNIQUE INDEX pipeline_stages_one_initial ON pipeline_stages (pipeline_id) WHERE category = 'initial';

-- Pipeline shape invariant, checked at COMMIT so a reconfiguration can move
-- stages around freely inside one transaction: exactly one initial stage, at
-- least one in_progress, one won and one lost.
CREATE FUNCTION check_pipeline_shape() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    pid uuid;
    n_initial int; n_progress int; n_won int; n_lost int;
BEGIN
    IF TG_TABLE_NAME = 'pipelines' THEN
        pid := NEW.id;
    ELSE
        pid := COALESCE(NEW.pipeline_id, OLD.pipeline_id);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pipelines WHERE id = pid) THEN
        RETURN NULL; -- pipeline deleted in this transaction
    END IF;
    SELECT count(*) FILTER (WHERE category = 'initial'),
           count(*) FILTER (WHERE category = 'in_progress'),
           count(*) FILTER (WHERE category = 'won'),
           count(*) FILTER (WHERE category = 'lost')
      INTO n_initial, n_progress, n_won, n_lost
      FROM pipeline_stages WHERE pipeline_id = pid;
    IF n_initial <> 1 OR n_progress < 1 OR n_won < 1 OR n_lost < 1 THEN
        RAISE EXCEPTION 'pipeline % must have exactly 1 initial and at least 1 in_progress, 1 won and 1 lost stage (has %/%/%/%)',
            pid, n_initial, n_progress, n_won, n_lost
            USING ERRCODE = 'check_violation', CONSTRAINT = 'pipeline_shape';
    END IF;
    RETURN NULL;
END $$;

CREATE CONSTRAINT TRIGGER pipeline_stages_shape
    AFTER INSERT OR UPDATE OR DELETE ON pipeline_stages
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION check_pipeline_shape();

-- A pipeline created without stages must fail too.
CREATE CONSTRAINT TRIGGER pipelines_shape
    AFTER INSERT ON pipelines
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION check_pipeline_shape();

CREATE TABLE channels (
    id            uuid PRIMARY KEY DEFAULT uuidv7(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    name          text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    kind          text NOT NULL CHECK (kind IN ('web', 'form', 'email', 'phone', 'social', 'referral', 'event', 'outbound', 'partner', 'ads', 'other')),
    archived_at   timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, id)
);
CREATE UNIQUE INDEX channels_name_key ON channels (workspace_id, lower(name)) WHERE archived_at IS NULL;

CREATE TABLE companies (
    id                 uuid PRIMARY KEY DEFAULT uuidv7(),
    workspace_id       uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    name               text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    domain             text CHECK (domain = lower(domain) AND length(domain) <= 253),
    industry           text NOT NULL DEFAULT '' CHECK (length(industry) <= 100),
    size_range         text NOT NULL DEFAULT '' CHECK (size_range IN ('', '1-10', '11-50', '51-200', '201-1000', '1001-5000', '5000+')),
    country            text NOT NULL DEFAULT '' CHECK (country = '' OR country ~ '^[A-Z]{2}$'),
    website            text NOT NULL DEFAULT '' CHECK (length(website) <= 500),
    description        text NOT NULL DEFAULT '' CHECK (length(description) <= 5000),
    owner_id           uuid,
    source_channel_id  uuid,
    tags               text[] NOT NULL DEFAULT '{}' CHECK (cardinality(tags) <= 30),
    custom             jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(custom) = 'object' AND pg_column_size(custom) <= 16384),
    external_id        text CHECK (length(external_id) <= 200),
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    archived_at        timestamptz,
    UNIQUE (workspace_id, id),
    FOREIGN KEY (workspace_id, owner_id) REFERENCES workspace_members (workspace_id, user_id) ON DELETE SET NULL (owner_id),
    FOREIGN KEY (workspace_id, source_channel_id) REFERENCES channels (workspace_id, id) ON DELETE SET NULL (source_channel_id)
);
CREATE UNIQUE INDEX companies_domain_key ON companies (workspace_id, domain) WHERE domain IS NOT NULL AND archived_at IS NULL;
CREATE UNIQUE INDEX companies_external_key ON companies (workspace_id, external_id) WHERE external_id IS NOT NULL;
CREATE INDEX companies_name_trgm ON companies USING gin (name gin_trgm_ops);

CREATE TABLE contacts (
    id                 uuid PRIMARY KEY DEFAULT uuidv7(),
    workspace_id       uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    company_id         uuid,
    first_name         text NOT NULL DEFAULT '' CHECK (length(first_name) <= 100),
    last_name          text NOT NULL DEFAULT '' CHECK (length(last_name) <= 100),
    email              text CHECK (email = lower(email) AND length(email) <= 320),
    phone              text NOT NULL DEFAULT '' CHECK (length(phone) <= 40),
    title              text NOT NULL DEFAULT '' CHECK (length(title) <= 150),
    linkedin_url       text NOT NULL DEFAULT '' CHECK (length(linkedin_url) <= 500),
    owner_id           uuid,
    source_channel_id  uuid,
    tags               text[] NOT NULL DEFAULT '{}' CHECK (cardinality(tags) <= 30),
    custom             jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(custom) = 'object' AND pg_column_size(custom) <= 16384),
    external_id        text CHECK (length(external_id) <= 200),
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    archived_at        timestamptz,
    UNIQUE (workspace_id, id),
    CHECK (first_name <> '' OR last_name <> '' OR email IS NOT NULL),
    FOREIGN KEY (workspace_id, company_id) REFERENCES companies (workspace_id, id) ON DELETE SET NULL (company_id),
    FOREIGN KEY (workspace_id, owner_id) REFERENCES workspace_members (workspace_id, user_id) ON DELETE SET NULL (owner_id),
    FOREIGN KEY (workspace_id, source_channel_id) REFERENCES channels (workspace_id, id) ON DELETE SET NULL (source_channel_id)
);
CREATE UNIQUE INDEX contacts_email_key ON contacts (workspace_id, email) WHERE email IS NOT NULL AND archived_at IS NULL;
CREATE UNIQUE INDEX contacts_external_key ON contacts (workspace_id, external_id) WHERE external_id IS NOT NULL;
CREATE INDEX contacts_company_idx ON contacts (workspace_id, company_id);
CREATE INDEX contacts_name_trgm ON contacts USING gin ((first_name || ' ' || last_name) gin_trgm_ops);

CREATE TABLE opportunities (
    id                   uuid PRIMARY KEY DEFAULT uuidv7(),
    workspace_id         uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    title                text NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
    kind                 text NOT NULL DEFAULT 'deal' CHECK (kind IN ('deal', 'project')),
    company_id           uuid,
    primary_contact_id   uuid,
    pipeline_id          uuid NOT NULL,
    stage_id             uuid NOT NULL,
    status               text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'won', 'lost')),
    amount_minor         bigint NOT NULL DEFAULT 0 CHECK (amount_minor >= 0),
    currency             text NOT NULL DEFAULT 'USD' CHECK (currency ~ '^[A-Z]{3}$'),
    probability          int CHECK (probability BETWEEN 0 AND 100),
    expected_close_date  date,
    closed_at            timestamptz,
    lost_reason          text NOT NULL DEFAULT '' CHECK (length(lost_reason) <= 1000),
    source_channel_id    uuid,
    owner_id             uuid,
    next_step            text NOT NULL DEFAULT '' CHECK (length(next_step) <= 500),
    next_step_at         timestamptz,
    description          text NOT NULL DEFAULT '' CHECK (length(description) <= 5000),
    tags                 text[] NOT NULL DEFAULT '{}' CHECK (cardinality(tags) <= 30),
    custom               jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(custom) = 'object' AND pg_column_size(custom) <= 16384),
    external_id          text CHECK (length(external_id) <= 200),
    stage_entered_at     timestamptz NOT NULL DEFAULT now(),
    last_interaction_at  timestamptz,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now(),
    archived_at          timestamptz,
    UNIQUE (workspace_id, id),
    FOREIGN KEY (workspace_id, pipeline_id) REFERENCES pipelines (workspace_id, id),
    FOREIGN KEY (pipeline_id, stage_id) REFERENCES pipeline_stages (pipeline_id, id),
    FOREIGN KEY (workspace_id, company_id) REFERENCES companies (workspace_id, id) ON DELETE SET NULL (company_id),
    FOREIGN KEY (workspace_id, primary_contact_id) REFERENCES contacts (workspace_id, id) ON DELETE SET NULL (primary_contact_id),
    FOREIGN KEY (workspace_id, owner_id) REFERENCES workspace_members (workspace_id, user_id) ON DELETE SET NULL (owner_id),
    FOREIGN KEY (workspace_id, source_channel_id) REFERENCES channels (workspace_id, id) ON DELETE SET NULL (source_channel_id),
    CHECK ((status = 'open') = (closed_at IS NULL))
);
CREATE UNIQUE INDEX opportunities_external_key ON opportunities (workspace_id, external_id) WHERE external_id IS NOT NULL;
CREATE INDEX opportunities_board_idx ON opportunities (workspace_id, pipeline_id, stage_id) WHERE archived_at IS NULL;
CREATE INDEX opportunities_company_idx ON opportunities (workspace_id, company_id);
CREATE INDEX opportunities_title_trgm ON opportunities USING gin (title gin_trgm_ops);

CREATE TABLE opportunity_contacts (
    workspace_id    uuid NOT NULL,
    opportunity_id  uuid NOT NULL,
    contact_id      uuid NOT NULL,
    role            text NOT NULL DEFAULT '' CHECK (length(role) <= 50),
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (opportunity_id, contact_id),
    FOREIGN KEY (workspace_id, opportunity_id) REFERENCES opportunities (workspace_id, id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id, contact_id) REFERENCES contacts (workspace_id, id) ON DELETE CASCADE
);

CREATE TABLE interactions (
    id                uuid PRIMARY KEY DEFAULT uuidv7(),
    workspace_id      uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    type              text NOT NULL CHECK (type IN ('email', 'call', 'meeting', 'note', 'message', 'social', 'form')),
    direction         text NOT NULL CHECK (direction IN ('inbound', 'outbound', 'internal')),
    channel_id        uuid,
    subject           text NOT NULL DEFAULT '' CHECK (length(subject) <= 300),
    summary           text NOT NULL DEFAULT '' CHECK (length(summary) <= 10000),
    occurred_at       timestamptz NOT NULL DEFAULT now(),
    duration_minutes  int CHECK (duration_minutes BETWEEN 0 AND 1440),
    company_id        uuid,
    contact_id        uuid,
    opportunity_id    uuid,
    external_id       text CHECK (length(external_id) <= 200),
    created_by        uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, id),
    CHECK (company_id IS NOT NULL OR contact_id IS NOT NULL OR opportunity_id IS NOT NULL),
    FOREIGN KEY (workspace_id, channel_id) REFERENCES channels (workspace_id, id) ON DELETE SET NULL (channel_id),
    FOREIGN KEY (workspace_id, company_id) REFERENCES companies (workspace_id, id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id, contact_id) REFERENCES contacts (workspace_id, id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id, opportunity_id) REFERENCES opportunities (workspace_id, id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX interactions_external_key ON interactions (workspace_id, external_id) WHERE external_id IS NOT NULL;
CREATE INDEX interactions_opp_idx ON interactions (workspace_id, opportunity_id, occurred_at DESC);
CREATE INDEX interactions_contact_idx ON interactions (workspace_id, contact_id, occurred_at DESC);
CREATE INDEX interactions_company_idx ON interactions (workspace_id, company_id, occurred_at DESC);
CREATE INDEX interactions_time_idx ON interactions (workspace_id, occurred_at DESC);

CREATE TABLE stage_changes (
    id              uuid PRIMARY KEY DEFAULT uuidv7(),
    workspace_id    uuid NOT NULL,
    opportunity_id  uuid NOT NULL,
    -- Stage names are snapshotted so history survives a stage being deleted.
    from_stage_id    uuid,
    from_stage_name  text NOT NULL DEFAULT '',
    to_stage_id      uuid,
    to_stage_name    text NOT NULL,
    to_category      text NOT NULL CHECK (to_category IN ('initial', 'in_progress', 'won', 'lost')),
    reason           text NOT NULL DEFAULT '' CHECK (length(reason) <= 1000),
    changed_by       uuid REFERENCES users (id) ON DELETE SET NULL,
    changed_at       timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (workspace_id, opportunity_id) REFERENCES opportunities (workspace_id, id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id, from_stage_id) REFERENCES pipeline_stages (workspace_id, id) ON DELETE SET NULL (from_stage_id),
    FOREIGN KEY (workspace_id, to_stage_id) REFERENCES pipeline_stages (workspace_id, id) ON DELETE SET NULL (to_stage_id)
);
CREATE INDEX stage_changes_opp_idx ON stage_changes (workspace_id, opportunity_id, changed_at);

CREATE TABLE lead_forms (
    id               uuid PRIMARY KEY DEFAULT uuidv7(),
    workspace_id     uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    name             text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    public_key       text NOT NULL UNIQUE CHECK (public_key ~ '^[A-Za-z0-9_-]{20,64}$'),
    pipeline_id      uuid NOT NULL,
    channel_id       uuid NOT NULL,
    allowed_origins  text[] NOT NULL DEFAULT '{}' CHECK (cardinality(allowed_origins) <= 20),
    redirect_url     text NOT NULL DEFAULT '' CHECK (length(redirect_url) <= 500),
    is_active        boolean NOT NULL DEFAULT true,
    created_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, id),
    FOREIGN KEY (workspace_id, pipeline_id) REFERENCES pipelines (workspace_id, id),
    FOREIGN KEY (workspace_id, channel_id) REFERENCES channels (workspace_id, id)
);

-- Append-only record of every write, and of who (user + which agent) made it.
CREATE TABLE audit_log (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id  uuid REFERENCES workspaces (id) ON DELETE CASCADE,
    actor_user_id uuid REFERENCES users (id) ON DELETE SET NULL,
    actor_kind    text NOT NULL CHECK (actor_kind IN ('session', 'oauth', 'pat', 'form', 'system')),
    actor_client  text NOT NULL DEFAULT '' CHECK (length(actor_client) <= 200),
    action        text NOT NULL CHECK (length(action) <= 64),
    entity_type   text NOT NULL CHECK (length(entity_type) <= 32),
    entity_id     uuid,
    details       jsonb NOT NULL DEFAULT '{}',
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_log_ws_idx ON audit_log (workspace_id, created_at DESC);
CREATE INDEX audit_log_entity_idx ON audit_log (workspace_id, entity_type, entity_id);

-- ───────────────────────────── Row-Level Security ─────────────────────────────
-- current_setting(..., true) yields NULL when unset, and NULL = x is never true,
-- so a connection that forgot to set the workspace sees nothing.

DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['pipelines', 'pipeline_stages', 'channels', 'companies', 'contacts',
                             'opportunities', 'opportunity_contacts', 'interactions', 'stage_changes',
                             'lead_forms']
    LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
        EXECUTE format($p$CREATE POLICY workspace_isolation ON %I
                          USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
                          WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)$p$, t);
    END LOOP;
END $$;

-- The audit log is shared: workspace rows are isolated, platform rows
-- (workspace_id IS NULL) are only reachable without a workspace bound.
ALTER TABLE audit_log ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_log FORCE ROW LEVEL SECURITY;
CREATE POLICY workspace_isolation ON audit_log
    USING (workspace_id IS NOT DISTINCT FROM NULLIF(current_setting('app.workspace_id', true), '')::uuid)
    WITH CHECK (workspace_id IS NOT DISTINCT FROM NULLIF(current_setting('app.workspace_id', true), '')::uuid);

-- The audit log is append-only for the application.
REVOKE UPDATE, DELETE, TRUNCATE ON audit_log FROM hilo_app;

-- The public form endpoint must find a form before it knows the workspace.
-- This permissive policy exposes exactly one row: the one whose public_key the
-- request bound to app.form_key. Nothing else becomes visible.
CREATE POLICY form_lookup ON lead_forms FOR SELECT
    USING (is_active AND public_key = NULLIF(current_setting('app.form_key', true), ''));
