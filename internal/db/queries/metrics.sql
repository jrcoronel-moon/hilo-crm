-- Metrics, lead forms and audit log. Workspace transaction (RLS) + explicit
-- workspace filter, as everywhere else.

-- ───────────── metrics ─────────────

-- Per-stage volume of open opportunities. Weighted value uses the
-- opportunity's own probability when set, else the stage's.
-- name: StageSummary :many
SELECT s.id AS stage_id, s.name, s.category, s.position, o.currency,
       count(o.id)::int AS count,
       COALESCE(sum(o.amount_minor), 0)::bigint AS amount_minor,
       COALESCE(sum(o.amount_minor * COALESCE(o.probability, s.probability) / 100), 0)::bigint AS weighted_minor
FROM pipeline_stages s
LEFT JOIN opportunities o ON o.stage_id = s.id AND o.archived_at IS NULL AND o.status = 'open'
WHERE s.workspace_id = $1 AND s.pipeline_id = $2
GROUP BY s.id, s.name, s.category, s.position, o.currency
ORDER BY s.position;

-- name: ClosedSummary :many
SELECT status, currency, count(*)::int AS count,
       COALESCE(sum(amount_minor), 0)::bigint AS amount_minor,
       COALESCE(avg(EXTRACT(EPOCH FROM closed_at - created_at) / 86400), 0)::float8 AS avg_cycle_days
FROM opportunities
WHERE workspace_id = $1 AND pipeline_id = $2 AND archived_at IS NULL
  AND status <> 'open' AND closed_at >= sqlc.arg(since)::timestamptz
GROUP BY status, currency;

-- name: LeadsByChannel :many
SELECT COALESCE(ch.name, '(none)') AS channel, count(o.id)::int AS count
FROM opportunities o
LEFT JOIN channels ch ON ch.id = o.source_channel_id
WHERE o.workspace_id = $1 AND o.pipeline_id = $2 AND o.archived_at IS NULL AND o.created_at >= sqlc.arg(since)::timestamptz
GROUP BY ch.name
ORDER BY count DESC;

-- name: InteractionsByWeek :many
SELECT date_trunc('week', occurred_at)::timestamptz AS week, type, direction, count(*)::int AS count
FROM interactions
WHERE workspace_id = $1 AND occurred_at >= sqlc.arg(since)::timestamptz
GROUP BY 1, 2, 3
ORDER BY 1;

-- Average days spent in each stage, from the stage-change history (time from
-- entering a stage until the next change of the same opportunity).
-- name: TimeInStage :many
WITH spans AS (
    SELECT sc.to_stage_id AS stage_id,
           EXTRACT(EPOCH FROM lead(sc.changed_at) OVER (PARTITION BY sc.opportunity_id ORDER BY sc.changed_at)
                   - sc.changed_at) / 86400 AS days
    FROM stage_changes sc
    JOIN opportunities o ON o.id = sc.opportunity_id
    WHERE sc.workspace_id = $1 AND o.pipeline_id = $2 AND sc.changed_at >= sqlc.arg(since)::timestamptz
)
SELECT s.id AS stage_id, s.name, COALESCE(avg(sp.days), 0)::float8 AS avg_days, count(sp.days)::int AS samples
FROM pipeline_stages s
LEFT JOIN spans sp ON sp.stage_id = s.id AND sp.days IS NOT NULL
WHERE s.workspace_id = $1 AND s.pipeline_id = $2
GROUP BY s.id, s.name, s.position
ORDER BY s.position;

-- name: CountStale :one
SELECT count(*)::int FROM opportunities
WHERE workspace_id = $1 AND pipeline_id = $2 AND archived_at IS NULL AND status = 'open'
  AND COALESCE(last_interaction_at, stage_entered_at) < sqlc.arg(stale_before)::timestamptz;

-- Won amount per calendar month (in the workspace timezone UTC) for the
-- last `months` months, per currency.
-- name: WonByMonth :many
SELECT date_trunc('month', closed_at)::timestamptz AS month, currency,
       COALESCE(sum(amount_minor), 0)::bigint AS amount_minor, count(*)::int AS count
FROM opportunities
WHERE workspace_id = $1 AND pipeline_id = $2 AND archived_at IS NULL AND status = 'won'
  AND closed_at >= date_trunc('month', now()) - make_interval(months => sqlc.arg(months)::int - 1)
GROUP BY 1, 2 ORDER BY 1;

-- Who changed what in the period: people (session), agents (OAuth client),
-- tokens (PAT name) and forms.
-- name: ActivityByActor :many
SELECT a.actor_kind, a.actor_client, COALESCE(u.email, '') AS email, COALESCE(u.name, '') AS name,
       count(*)::int AS changes,
       count(*) FILTER (WHERE a.action = 'opportunity.create')::int AS created
FROM audit_log a LEFT JOIN users u ON u.id = a.actor_user_id
WHERE a.workspace_id = $1 AND a.created_at >= sqlc.arg(since)::timestamptz
GROUP BY a.actor_kind, a.actor_client, u.email, u.name
ORDER BY changes DESC LIMIT 6;

-- ───────────── lead forms ─────────────

-- name: ListLeadForms :many
SELECT * FROM lead_forms WHERE workspace_id = $1 ORDER BY name;

-- name: GetLeadForm :one
SELECT * FROM lead_forms WHERE workspace_id = $1 AND id = $2;

-- name: CreateLeadForm :one
INSERT INTO lead_forms (workspace_id, name, public_key, pipeline_id, channel_id, allowed_origins, redirect_url)
VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING *;

-- name: SetLeadFormActive :execrows
UPDATE lead_forms SET is_active = $3 WHERE workspace_id = $1 AND id = $2;

-- Only visible through the form_lookup policy (app.form_key bound).
-- name: LookupLeadForm :one
SELECT * FROM lead_forms WHERE public_key = $1 AND is_active;

-- ───────────── audit ─────────────

-- name: InsertAudit :exec
INSERT INTO audit_log (workspace_id, actor_user_id, actor_kind, actor_client, action, entity_type, entity_id, details)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: ListAudit :many
SELECT a.*, COALESCE(u.email, '') AS actor_email
FROM audit_log a LEFT JOIN users u ON u.id = a.actor_user_id
WHERE a.workspace_id = $1
  AND (sqlc.narg(entity_id)::uuid IS NULL OR a.entity_id = sqlc.narg(entity_id))
ORDER BY a.id DESC
LIMIT sqlc.arg(lim);
