-- 0003: platform branding (superadmin): application name and logo.
CREATE TABLE platform_settings (
    id          boolean PRIMARY KEY DEFAULT true CHECK (id),
    app_name    text NOT NULL DEFAULT 'Hilo CRM' CHECK (length(app_name) BETWEEN 1 AND 60),
    logo        bytea CHECK (octet_length(logo) <= 262144),
    logo_mime   text NOT NULL DEFAULT '' CHECK (logo_mime IN ('', 'image/png', 'image/jpeg', 'image/webp', 'image/svg+xml')),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    updated_by  uuid REFERENCES users (id) ON DELETE SET NULL
);
INSERT INTO platform_settings DEFAULT VALUES;
