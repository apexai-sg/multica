-- Aurora system-agent seeding (Plan 3 Task 1). These queries back
-- aurora.EnsureAuroraExpert (generation creation: the requested expert only) and
-- aurora.EnsureSystemAgents (tests and backfill: every expert). Each expert gets
-- one managed runtime row, one kind='user' agent carrying its upstream
-- quick-start cards as conversation_starters, its primary skill row, its
-- sub-skill rows, and the agent_skill junctions.

-- name: GetAuroraManagedRuntime :one
-- The workspace's server-hosted (managed) runtime, the idempotency anchor for
-- seeding. It may be unbound (daemon_id still NULL) or already bound to the
-- sandbox daemon that enrolled it, so the lookup does not filter on daemon_id;
-- migration 526's partial unique index enforces one such runtime per workspace,
-- so this ordering can never choose between duplicates.
SELECT * FROM agent_runtime
WHERE workspace_id = $1
  AND runtime_mode = 'cloud'
  AND provider = $2
ORDER BY created_at ASC, id ASC
LIMIT 1;

-- name: CreateAuroraManagedRuntime :one
INSERT INTO agent_runtime (
    workspace_id, daemon_id, name, runtime_mode, provider, status,
    device_info, metadata, owner_id, visibility
) VALUES (
    $1, NULL, $2, 'cloud', $3, 'offline', '', '{}'::jsonb, $4, 'private'
)
RETURNING *;

-- name: UpsertAuroraSystemAgent :one
-- Like Mika's CreateSystemUserAgent, this product-defined agent is deliberately
-- kind='user': members can see it, chat with it and assign issues to it.
-- Idempotency rides
-- migration 172's partial unique index on
-- (workspace_id, owner_id, runtime_id, system_key) WHERE system_key IS NOT
-- NULL; the arbiter must name all four columns and repeat the predicate, or
-- Postgres will not match the partial index. DO UPDATE refreshes
-- name/instructions so a later seed enriches the prompt without creating a
-- second row.
INSERT INTO agent (
    workspace_id, owner_id, runtime_id, kind, system_key, name, instructions,
    conversation_starters, runtime_mode, visibility, permission_mode, runtime_config
) VALUES (
    $1, $2, $3, 'user', $4, $5, $6, $7, 'cloud', 'workspace', 'private', '{}'::jsonb
)
ON CONFLICT (workspace_id, owner_id, runtime_id, system_key) WHERE system_key IS NOT NULL
-- Recover archived carriers in place on generation's pre-enqueue seed. Both
-- archive fields are cleared; system_key/runtime identity is unchanged.
DO UPDATE SET name = EXCLUDED.name, instructions = EXCLUDED.instructions,
    conversation_starters = EXCLUDED.conversation_starters,
    kind = EXCLUDED.kind, archived_at = NULL, archived_by = NULL
RETURNING *;

-- name: UpsertAuroraSkill :one
-- Inserts or refreshes the skill row a system agent's skill resolves to. The
-- name equals the agent's display name, and skill's UNIQUE(workspace_id, name)
-- is the idempotency key. content is the minimal SKILL.md workflow text,
-- refreshed on every seed so Plan 3.5's enrichment reaches already-seeded
-- workspaces.
INSERT INTO skill (workspace_id, name, description, content, config, created_by)
VALUES ($1, $2, '', $3, '{}'::jsonb, $4)
ON CONFLICT (workspace_id, name)
DO UPDATE SET content = EXCLUDED.content, updated_at = now()
RETURNING *;

-- name: AdoptAuroraSystemAgent :one
-- Adopts the workspace's existing Aurora carrier for one system_key and rebinds
-- it to the current managed runtime, whatever runtime or archive state it is in.
--
-- The seed must key on system_key alone. Migration 172's partial unique index
-- includes runtime_id, so once a runtime teardown unbinds a carrier
-- (UnbindUserAgentsFromRuntime sets runtime_id = NULL for kind = 'user'), an
-- insert-first seed sees a different tuple, inserts a second row, and leaves
-- GetAgentBySystemKey (archived_at IS NULL, ORDER BY created_at ASC) resolving
-- the older, unbound one: the workspace shows duplicate Aurora agents and a
-- generation binds to a carrier with no runtime. Adopting the earliest row keeps
-- the seed's identity identical to the one that lookup resolves.
UPDATE agent
SET owner_id = @owner_id,
    runtime_id = @runtime_id,
    kind = 'user',
    name = @name,
    conversation_starters = @conversation_starters,
    instructions = @instructions,
    runtime_mode = 'cloud',
    visibility = 'workspace',
    permission_mode = 'private',
    archived_at = NULL,
    archived_by = NULL,
    updated_at = now()
WHERE id = (
    SELECT a.id FROM agent a
    WHERE a.workspace_id = @workspace_id AND a.system_key = @system_key
    ORDER BY a.created_at ASC, a.id ASC
    LIMIT 1
)
RETURNING *;
