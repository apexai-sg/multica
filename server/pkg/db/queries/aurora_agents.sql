-- Aurora system-agent seeding (Plan 3 Task 1). These queries back
-- aurora.EnsureAuroraExpert (generation creation: the requested expert only) and
-- aurora.EnsureSystemAgents (tests and backfill: every expert). Each expert gets
-- one managed runtime row, one kind='user' agent carrying its upstream
-- quick-start cards as conversation_starters, and its primary skill row.
-- Sub-skill files are imported onto the cloud runtime, not stored here.

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

-- name: ListAuroraManagedRuntimes :many
-- Every workspace that has a managed Aurora runtime. The reconciler keeps one
-- node online per such workspace; a workspace that never used Aurora has no row
-- and therefore no node to maintain.
SELECT * FROM agent_runtime
WHERE provider = sqlc.arg(provider)
ORDER BY workspace_id ASC, created_at ASC, id ASC;

-- name: CreateAuroraManagedRuntime :one
INSERT INTO agent_runtime (
    workspace_id, daemon_id, name, runtime_mode, provider, status,
    device_info, metadata, owner_id, visibility
) VALUES (
    $1, NULL, $2, 'cloud', $3, 'offline', '', '{}'::jsonb, $4, 'private'
)
RETURNING *;

-- name: SetAuroraManagedRuntimeOwner :exec
-- Re-homes the workspace's managed runtime onto whoever the seed resolved as
-- the workspace owner, so an ownership transfer heals on the next seed instead
-- of leaving the runtime on the previous owner. The daemon claim path refuses to
-- deliver to a private runtime whose owner differs from the agent's, and the
-- seed re-owns the carriers on every run, so this is the other half of that
-- invariant. The predicate repeats the carrier identity so only this managed
-- runtime can be re-homed, and the IS DISTINCT FROM test keeps a steady-state
-- seed to a no-op rather than a write.
UPDATE agent_runtime
SET owner_id = @owner_id, updated_at = now()
WHERE id = @id
  AND workspace_id = @workspace_id
  AND provider = @provider
  AND owner_id IS DISTINCT FROM @owner_id;

-- name: UpsertAuroraSystemAgent :one
-- Like Mika's CreateSystemUserAgent, this product-defined agent is deliberately
-- kind='user': members can see it, chat with it and assign issues to it.
--
-- permission_mode is 'public_to' because the carrier belongs to the workspace
-- owner, not to whoever runs a generation: 'private' would let only that owner
-- invoke it. The agent_invocation_target('workspace') row that makes public_to
-- mean "every member" is written by aurora.ensureExpert.
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
    $1, $2, $3, 'user', $4, $5, $6, $7, 'cloud', 'workspace', 'public_to', '{}'::jsonb
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
--
-- owner_id is the workspace owner, never the caller: the handler resolves it
-- once per seed (GetWorkspaceOwnerUserID), so a member's generation cannot take
-- the carrier from the owner. permission_mode mirrors UpsertAuroraSystemAgent.
UPDATE agent
SET owner_id = @owner_id,
    runtime_id = @runtime_id,
    kind = 'user',
    name = @name,
    conversation_starters = @conversation_starters,
    instructions = @instructions,
    runtime_mode = 'cloud',
    visibility = 'workspace',
    permission_mode = 'public_to',
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

-- name: ListAuroraExpertAvatars :many
-- The portrait each Aurora expert carrier carries in this workspace, keyed by
-- the agent's stable system_key. The skill directory decorates its catalog with
-- these: the catalog itself is a server-owned constant and stays avatar-free, so
-- only the workspace lookup can answer what the member set on the carrier agent
-- in the Agents settings.
--
-- system_key_prefix is aurora.SystemKeyPrefix plus the LIKE wildcard, passed in
-- rather than spelled here: the "<prefix><skillID>" format has one owner in Go.
--
-- DISTINCT ON keeps one row per skill, resolving the same way
-- GetAgentBySystemKey does (earliest live carrier first): migration 172's
-- partial unique index includes runtime_id, so a runtime teardown can leave two
-- live rows for one system_key and the directory must not double-count them.
SELECT DISTINCT ON (a.system_key)
    a.system_key,
    a.avatar_url
FROM agent a
WHERE a.workspace_id = @workspace_id
  AND a.system_key LIKE @system_key_prefix
  AND a.archived_at IS NULL
ORDER BY a.system_key ASC, a.created_at ASC, a.id ASC;

-- name: ListAuroraSystemAgentKeys :many
-- The carrier keys this workspace has ever had, archived rows included. The
-- first-entry seed diffs them against the catalog and seeds only the experts
-- whose key is absent entirely: a key that is present but archived is a carrier
-- the member archived, and loading the directory must not resurrect it.
-- Generation creation still adopts and un-archives, because that path owns
-- recovering a carrier whose runtime binding went stale.
--
-- DISTINCT because migration 172's partial unique index includes runtime_id, so
-- a runtime teardown can leave two rows (live or not) for one system_key.
SELECT DISTINCT a.system_key
FROM agent a
WHERE a.workspace_id = @workspace_id
  AND a.system_key LIKE @system_key_prefix;
