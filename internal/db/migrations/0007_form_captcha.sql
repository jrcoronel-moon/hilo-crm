-- Public lead forms can require a Cloudflare Turnstile captcha. Off for
-- existing forms: their websites do not have the widget yet.
ALTER TABLE lead_forms ADD COLUMN require_captcha boolean NOT NULL DEFAULT false;
