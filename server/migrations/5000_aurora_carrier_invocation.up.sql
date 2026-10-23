-- Aurora expert carriers become workspace-wide and owner-stable.
--
-- The carriers are product-defined agents (agent.system_key = 'aurora:<skill>')
-- that every member of the workspace may invoke, and whose owner — like their
-- managed runtime's — is always the workspace owner. The seed
-- (aurora.EnsureAuroraExpert) enforces both properties from now on; this
-- backfill brings the workspaces seeded before it in line:
--
--   * a 'private' carrier admitted only its owner, which is why a member's
--     generation could not use an expert another member had first triggered;
--   * a carrier whose owner_id had been stamped with the requesting member's id
--     (AdoptAuroraSystemAgent used to write the caller's id) cannot run on the
--     workspace's private managed runtime: the daemon claim path refuses to
--     deliver a task to a private runtime whose owner differs from the agent's.
--
-- Ownership comes from the same rule the seed resolves (GetWorkspaceOwnerUserID:
-- role = 'owner', oldest membership first, user_id as the tie-break). Both
-- backfills read one CTE rather than each joining member: nothing enforces one
-- owner row per workspace (promotion to 'owner' is unrestricted), and two
-- independent joins could pick different rows, leaving the carrier and runtime
-- owned by different people — the very split that blocks claims.

-- ----------------------------------------------------------------------------
-- 1. Every carrier admits the whole workspace.
-- ----------------------------------------------------------------------------
UPDATE agent SET permission_mode = 'public_to', updated_at = now()
WHERE system_key LIKE 'aurora:%' AND permission_mode <> 'public_to';

-- The target the mode above defers to: target_id holds the workspace id, so the
-- UNIQUE(agent_id, target_type, target_id) index dedups this against rows the
-- seed already wrote.
INSERT INTO agent_invocation_target (agent_id, target_type, target_id)
SELECT a.id, 'workspace', a.workspace_id FROM agent a
WHERE a.system_key LIKE 'aurora:%'
ON CONFLICT (agent_id, target_type, target_id) DO NOTHING;

-- ----------------------------------------------------------------------------
-- 2. Carrier and managed runtime belong to the workspace owner.
-- ----------------------------------------------------------------------------
WITH owner AS (
    SELECT DISTINCT ON (workspace_id) workspace_id, user_id
    FROM member
    WHERE role = 'owner'
    ORDER BY workspace_id, created_at ASC, user_id ASC
)
UPDATE agent a SET owner_id = o.user_id, updated_at = now()
FROM owner o
WHERE a.system_key LIKE 'aurora:%'
  AND o.workspace_id = a.workspace_id
  AND a.owner_id IS DISTINCT FROM o.user_id;

WITH owner AS (
    SELECT DISTINCT ON (workspace_id) workspace_id, user_id
    FROM member
    WHERE role = 'owner'
    ORDER BY workspace_id, created_at ASC, user_id ASC
)
UPDATE agent_runtime rt SET owner_id = o.user_id, updated_at = now()
FROM owner o
WHERE rt.provider = 'aurora_managed'
  AND o.workspace_id = rt.workspace_id
  AND rt.owner_id IS DISTINCT FROM o.user_id;
