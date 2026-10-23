-- Reverse 5000_aurora_carrier_invocation.up.sql.
--
-- The permission and target halves restore exactly what the previous seed
-- wrote: 'private' carriers with no invocation target. The ownership halves do
-- not: the migration overwrote owner_id with the workspace owner and no record
-- of the previous value survives, so carriers and managed runtimes stay owned by
-- the workspace owner. That is the state the pre-migration seed also converged
-- on for a workspace whose owner triggered it, and it is the safe one to keep —
-- a 'private' carrier on a 'private' runtime needs both owners equal.

UPDATE agent SET permission_mode = 'private', updated_at = now()
WHERE system_key LIKE 'aurora:%' AND permission_mode <> 'private';

DELETE FROM agent_invocation_target it
USING agent a
WHERE it.agent_id = a.id
  AND a.system_key LIKE 'aurora:%'
  AND it.target_type = 'workspace'
  AND it.target_id = a.workspace_id;
