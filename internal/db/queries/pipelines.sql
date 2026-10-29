-- Pipelines, stages and channels. Run inside a workspace transaction (RLS);
-- every query also filters by workspace_id explicitly.

-- name: ListPipelines :many
SELECT * FROM pipelines
WHERE workspace_id = $1 AND archived_at IS NULL
ORDER BY position, name;

-- name: GetPipeline :one
SELECT * FROM pipelines WHERE workspace_id = $1 AND id = $2 AND archived_at IS NULL;

-- name: GetPipelineByName :one
SELECT * FROM pipelines WHERE workspace_id = $1 AND lower(name) = lower(sqlc.arg(name)) AND archived_at IS NULL;

-- name: GetDefaultPipeline :one
SELECT * FROM pipelines WHERE workspace_id = $1 AND is_default AND archived_at IS NULL;

-- name: CreatePipeline :one
INSERT INTO pipelines (workspace_id, name, is_default, position)
VALUES ($1, $2, $3, $4) RETURNING *;

-- name: RenamePipeline :exec
UPDATE pipelines SET name = $3, updated_at = now() WHERE workspace_id = $1 AND id = $2;

-- name: SetPipelineTarget :exec
UPDATE pipelines SET monthly_target_minor = $3, target_currency = $4, updated_at = now() WHERE workspace_id = $1 AND id = $2;

-- name: ClearDefaultPipeline :exec
UPDATE pipelines SET is_default = false WHERE workspace_id = $1 AND is_default;

-- name: SetDefaultPipeline :exec
UPDATE pipelines SET is_default = true, updated_at = now() WHERE workspace_id = $1 AND id = $2;

-- name: ArchivePipeline :exec
UPDATE pipelines SET archived_at = now(), is_default = false, updated_at = now() WHERE workspace_id = $1 AND id = $2;

-- name: CountPipelines :one
SELECT count(*)::int FROM pipelines WHERE workspace_id = $1 AND archived_at IS NULL;

-- name: ListStages :many
SELECT * FROM pipeline_stages WHERE workspace_id = $1 AND pipeline_id = $2 ORDER BY position;

-- name: ListAllStages :many
SELECT s.* FROM pipeline_stages s
JOIN pipelines p ON p.id = s.pipeline_id
WHERE s.workspace_id = $1 AND p.archived_at IS NULL
ORDER BY p.position, s.position;

-- name: GetStage :one
SELECT * FROM pipeline_stages WHERE workspace_id = $1 AND id = $2;

-- name: GetInitialStage :one
SELECT * FROM pipeline_stages WHERE workspace_id = $1 AND pipeline_id = $2 AND category = 'initial';

-- name: InsertStage :one
INSERT INTO pipeline_stages (workspace_id, pipeline_id, name, category, position, probability)
VALUES ($1, $2, $3, $4, $5, $6) RETURNING *;

-- name: UpdateStage :exec
UPDATE pipeline_stages SET name = $3, category = $4, position = $5, probability = $6
WHERE workspace_id = $1 AND id = $2;

-- ParkStages moves every stage of a pipeline out of the way (negative position,
-- temporary unique name) so a reconfiguration can reassign names and positions
-- without tripping the unique indexes halfway through.
-- name: ParkStages :exec
UPDATE pipeline_stages SET position = -1 - position, name = '~' || id::text
WHERE workspace_id = $1 AND pipeline_id = $2;

-- name: DeleteStage :exec
DELETE FROM pipeline_stages WHERE workspace_id = $1 AND id = $2;

-- name: CountOpportunitiesInStage :one
SELECT count(*)::int FROM opportunities WHERE workspace_id = $1 AND stage_id = $2;

-- name: ReassignStage :exec
UPDATE opportunities SET stage_id = sqlc.arg(to_stage), updated_at = now()
WHERE workspace_id = $1 AND stage_id = sqlc.arg(from_stage);

-- name: ListChannels :many
SELECT * FROM channels WHERE workspace_id = $1 AND archived_at IS NULL ORDER BY name;

-- name: GetChannel :one
SELECT * FROM channels WHERE workspace_id = $1 AND id = $2 AND archived_at IS NULL;

-- name: GetChannelByName :one
SELECT * FROM channels WHERE workspace_id = $1 AND lower(name) = lower(sqlc.arg(name)) AND archived_at IS NULL;

-- name: CreateChannel :one
INSERT INTO channels (workspace_id, name, kind) VALUES ($1, $2, $3) RETURNING *;

-- name: UpdateChannel :exec
UPDATE channels SET name = $3, kind = $4 WHERE workspace_id = $1 AND id = $2;

-- name: ArchiveChannel :execrows
UPDATE channels SET archived_at = now() WHERE workspace_id = $1 AND id = $2 AND archived_at IS NULL;

-- SyncOpportunityStatuses re-derives status/closed_at from stage categories
-- after a pipeline reconfiguration changed what a stage means.
-- name: SyncOpportunityStatuses :exec
UPDATE opportunities o SET
    status = CASE s.category WHEN 'won' THEN 'won' WHEN 'lost' THEN 'lost' ELSE 'open' END,
    closed_at = CASE WHEN s.category IN ('won', 'lost') THEN COALESCE(o.closed_at, now()) ELSE NULL END,
    updated_at = now()
FROM pipeline_stages s
WHERE s.id = o.stage_id AND o.workspace_id = $1 AND o.pipeline_id = $2
  AND o.status IS DISTINCT FROM CASE s.category WHEN 'won' THEN 'won' WHEN 'lost' THEN 'lost' ELSE 'open' END;
