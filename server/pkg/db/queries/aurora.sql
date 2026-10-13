-- name: CreateAuroraGeneration :one
INSERT INTO aurora_generation (workspace_id, user_id, skill_id, prompt, status)
VALUES ($1, $2, $3, $4, 'queued')
RETURNING id, workspace_id, user_id, skill_id, prompt, status, task_id,
          credits_reserved, credits_charged, error, created_at, updated_at, issue_id;

-- name: GetAuroraGeneration :one
SELECT id, workspace_id, user_id, skill_id, prompt, status, task_id,
       credits_reserved, credits_charged, error, created_at, updated_at, issue_id
FROM aurora_generation
WHERE id = $1 AND workspace_id = $2;

-- name: UpdateAuroraGenerationTask :one
UPDATE aurora_generation
SET task_id = $2, credits_reserved = $3, updated_at = now()
WHERE id = $1 AND workspace_id = $4
RETURNING id, workspace_id, user_id, skill_id, prompt, status, task_id,
          credits_reserved, credits_charged, error, created_at, updated_at, issue_id;

-- name: UpdateAuroraGenerationTerminal :one
UPDATE aurora_generation
SET status = $2, error = $3, credits_charged = $4, updated_at = now()
WHERE id = $1 AND workspace_id = $5
RETURNING id, workspace_id, user_id, skill_id, prompt, status, task_id,
          credits_reserved, credits_charged, error, created_at, updated_at, issue_id;

-- name: GetAuroraGenerationByTaskID :one
SELECT id, workspace_id, user_id, skill_id, prompt, status, task_id,
       credits_reserved, credits_charged, error, created_at, updated_at, issue_id
FROM aurora_generation
WHERE task_id = $1;

-- name: ListAuroraGenerations :many
SELECT id, workspace_id, user_id, skill_id, prompt, status, task_id,
       credits_reserved, credits_charged, error, created_at, updated_at, issue_id
FROM aurora_generation
WHERE workspace_id = $1
ORDER BY created_at DESC
LIMIT $2 OFFSET $3;

-- name: CreateAuroraAsset :one
INSERT INTO aurora_asset (generation_id, workspace_id, kind, media_url, format)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, generation_id, workspace_id, kind, media_url, format, created_at,
          manifest_artifact_id, name, mime_type, size_bytes, sha256, role, metadata;

-- name: ListAuroraAssets :many
SELECT id, generation_id, workspace_id, kind, media_url, format, created_at,
       manifest_artifact_id, name, mime_type, size_bytes, sha256, role, metadata
FROM aurora_asset
WHERE (sqlc.narg('generation_id')::uuid IS NULL OR generation_id = sqlc.narg('generation_id')::uuid)
  AND (sqlc.narg('workspace_id')::uuid IS NULL OR workspace_id = sqlc.narg('workspace_id')::uuid)
ORDER BY created_at DESC
LIMIT sqlc.arg('limit')::int OFFSET sqlc.arg('offset')::int;

-- name: GetAuroraAsset :one
SELECT id, generation_id, workspace_id, kind, media_url, format, created_at,
       manifest_artifact_id, name, mime_type, size_bytes, sha256, role, metadata
FROM aurora_asset
WHERE id = $1 AND workspace_id = $2;

-- name: DeleteAuroraAsset :execrows
DELETE FROM aurora_asset
WHERE id = $1 AND workspace_id = $2;

-- name: CountWorkspacesForUser :one
SELECT count(*) FROM member WHERE user_id = $1;

-- name: CountGenerationsThisMonth :one
-- Monthly entitlement usage (Plan 5 Task 6). "This month" is the natural month
-- in the database's timezone, matching the settlement window (Task 4) so a
-- grant and the count that consumes it agree on where the month starts.
SELECT count(*) FROM aurora_generation
WHERE user_id = $1 AND created_at >= date_trunc('month', now());

-- name: CountActiveGenerations :one
-- Concurrency entitlement usage (Plan 5 Task 6). A newly-created queued row
-- occupies capacity before its task id is attached; once attached, the task's
-- non-terminal state remains authoritative.
SELECT count(*) FROM aurora_generation g
LEFT JOIN agent_task_queue t ON t.id = g.task_id
WHERE g.user_id = $1
  AND (
    (g.task_id IS NULL AND g.status = 'queued')
    OR t.status IN ('queued', 'dispatched', 'running', 'waiting_local_directory', 'deferred')
  );

-- name: GetPersonalWorkspaceForUser :one
-- The workspace a user-scoped credit write is attributed to. Aurora's ledger
-- rows carry a workspace_id, and a personal plan's grants belong to the
-- personal space — the workspace the user owns (Plan 1 provisions exactly one
-- at signup). Ordered by creation so the first-owned space wins if a user
-- somehow owns more than one.
SELECT w.id FROM workspace w
JOIN member m ON m.workspace_id = w.id
WHERE m.user_id = $1 AND m.role = 'owner'
ORDER BY w.created_at ASC
LIMIT 1;

-- name: GetAuroraGenerationByIssueID :one
-- 任务 4 的评论门闩用它判断这条 issue 属于哪次生成、是否终态。
SELECT id, workspace_id, user_id, skill_id, prompt, status, task_id,
       credits_reserved, credits_charged, error, created_at, updated_at, issue_id
FROM aurora_generation
WHERE issue_id = $1 AND workspace_id = $2;

-- name: UpdateAuroraGenerationBound :one
UPDATE aurora_generation
SET task_id = $2, credits_reserved = $3, issue_id = $4, updated_at = now()
WHERE id = $1 AND workspace_id = $5
RETURNING id, workspace_id, user_id, skill_id, prompt, status, task_id,
          credits_reserved, credits_charged, error, created_at, updated_at, issue_id;
