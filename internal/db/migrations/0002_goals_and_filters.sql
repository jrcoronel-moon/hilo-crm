-- 0002: monthly goal per pipeline, and indexes for the web filters.

ALTER TABLE pipelines
    ADD COLUMN monthly_target_minor bigint NOT NULL DEFAULT 0 CHECK (monthly_target_minor >= 0),
    ADD COLUMN target_currency text NOT NULL DEFAULT 'USD' CHECK (target_currency ~ '^[A-Z]{3}$');

CREATE INDEX opportunities_tags_idx ON opportunities USING gin (tags);
CREATE INDEX companies_lower_name_idx ON companies (workspace_id, lower(name)) WHERE archived_at IS NULL;
