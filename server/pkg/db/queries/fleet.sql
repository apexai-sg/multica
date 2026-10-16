-- name: FleetSchemaVersion :one
SELECT current_setting('server_version_num')::integer >= 170000 AS supported;

-- name: FleetSchemaProbe :exec
-- Parse and permission-check required fields and query dependencies without reading rows.
SELECT n.id,
 n.namespace,
 n.owner_id,
 n.created_at,
 n.updated_at,
 n.container_id,
 n.daemon_id,
 n.name,
 n.spec,
 n.image,
 n.profile_ref,
 n.start_epoch,
 n.data_volume,
 n.secrets_volume,
 n.desired,
 n.status,
 n.generation,
 n.ready,
 n.health_at,
 n.active_runs,
 n.pending_reports,
 n.failed_reports,
 n.maintenance,
 n.revoked,
 n.error_code,
 n.error_message,
 n.spec_config,
 n.observation,
 n.workspace_id,
 n.runtime_id,
 o.id,
 o.namespace,
 o.owner_id,
 o.created_at,
 o.updated_at,
 o.node_id,
 o.action,
 o.action_claimed_at,
 o.action_start_epoch,
 o.idempotency_key,
 o.request_hash,
 o.phase,
 o.prior_desired,
 o.generation,
 o.approved,
 o.attempts,
 o.bootstrap_claimed_at,
 o.bootstrap_minted,
 o.non_retryable,
 o.next_attempt_at,
 o.error_code,
 o.error_message,
 c.id,
 c.namespace,
 c.owner_id,
 c.created_at,
 c.updated_at,
 c.node_id,
 c.token_hash,
 c.revoked_at,
 c.generation,
 p.id,
 p.namespace,
 p.owner_id,
 p.created_at,
 p.updated_at,
 p.profile_ref,
 p.config_version,
 u.id,
 r.id,
 r.owner_id,
 r.metadata,
 t.runtime_id,
 t.status,
 f.namespace, f.fleet_id, f.closed, f.generation, f.operation_key, f.finalized, f.completion_manifest
FROM fleet_nodes n CROSS JOIN fleet_node_operations o
 CROSS JOIN fleet_node_credentials c CROSS JOIN fleet_credential_profiles p
 CROSS JOIN "user" u CROSS JOIN agent_runtime r CROSS JOIN agent_task_queue t
 CROSS JOIN fleet_namespace_fences f
WHERE false;

-- name: FleetPendingDelete :one
-- The current control generation is independent of credential generations.
SELECT EXISTS (SELECT 1 FROM fleet_node_operations o
 WHERE o.namespace = @namespace AND o.owner_id = @owner_id AND o.node_id = @node_id
 AND o.generation = @generation AND o.action='delete'
 AND o.phase IN ('queued','preparing','prepared','applying')) AS pending;

-- name: FleetLockCopiedTaskWorkspaces :exec
SELECT w.id FROM workspace w WHERE w.id IN (
 SELECT r.workspace_id FROM agent_runtime r WHERE r.id = ANY(@runtime_ids::uuid[])
 UNION SELECT a.workspace_id FROM agent a WHERE a.id = @agent_id
 UNION SELECT i.workspace_id FROM issue i WHERE i.id = @issue_id
) ORDER BY w.id FOR KEY SHARE;

-- name: FleetFenceTaskOwners :one
-- Preserve the historical workspace -> agent -> issue -> runtime order.
SELECT lock_task_owner_rows(@agent_id, @issue_id, @runtime_id)::boolean AS valid;

-- name: FleetLockRuntimeBinding :one
-- KEY SHARE alone does not conflict with trusted metadata upgrades.
SELECT * FROM agent_runtime WHERE id = @runtime_id FOR SHARE;

-- name: FleetReclaimBindings :many
-- Nonlocking candidates precede node locks; agent bindings are rechecked after locks.
SELECT DISTINCT a.id AS agent_id, a.runtime_id AS bound_runtime_id,
 atq.runtime_id AS task_runtime_id
FROM agent_task_queue atq JOIN agent a ON a.id=atq.agent_id
WHERE atq.runtime_id=ANY(@runtime_ids::uuid[]) AND atq.status='dispatched';

-- name: FleetLockClaimAgents :many
SELECT * FROM agent WHERE id=ANY(@agent_ids::uuid[]) ORDER BY id FOR UPDATE;

-- name: FleetLockTaskWorkspaces :exec
-- Early managed admission mirrors migration 284 before runtime and agent locks.
SELECT 1 FROM workspace w WHERE w.id IN (
 SELECT a.workspace_id FROM agent a WHERE a.id = @agent_id
 UNION SELECT i.workspace_id FROM issue i WHERE i.id = @issue_id
 UNION SELECT r.workspace_id FROM agent_runtime r WHERE r.id = @runtime_id
) ORDER BY w.id FOR KEY SHARE;

-- name: FleetAdmissionLocksHeld :one
-- Caller-owned enqueue must prove its early locks on THIS backend/transaction;
-- it may not acquire a newly discovered node after chat/workspace rows.
WITH keys AS (
 SELECT hashtextextended(sqlc.arg(namespace)::text || ':' || sqlc.arg(node_id)::uuid::text,0) AS key,'ShareLock'::text AS mode
 UNION ALL SELECT hashtextextended('namespace:' || sqlc.arg(namespace)::text,0),'ShareLock'
 UNION ALL SELECT hashtextextended('capacity:' || sqlc.arg(namespace)::text || ':' || sqlc.arg(node_id)::uuid::text,0),'ExclusiveLock'
)
SELECT bool_and(EXISTS (
 SELECT 1 FROM pg_catalog.pg_lock_status() l WHERE to_jsonb(l)->>'locktype'='advisory'
 AND (to_jsonb(l)->>'pid')::integer=pg_backend_pid()
 AND (to_jsonb(l)->>'granted')::boolean AND (to_jsonb(l)->>'objsubid')::integer=1
 AND (to_jsonb(l)->>'classid')::bigint=((keys.key >> 32) & 4294967295)
 AND (to_jsonb(l)->>'objid')::bigint=(keys.key & 4294967295)
 AND (to_jsonb(l)->>'mode'=keys.mode OR (keys.mode='ShareLock' AND to_jsonb(l)->>'mode'='ExclusiveLock'))
)) FROM keys;

-- name: FleetNamespaceSharedLock :exec
SELECT pg_advisory_xact_lock_shared(hashtextextended('namespace:' || sqlc.arg(namespace)::text,0));

-- name: FleetNamespaceExclusiveLock :exec
SELECT pg_advisory_xact_lock(hashtextextended('namespace:' || sqlc.arg(namespace)::text,0));

-- name: GetFleetNamespaceFence :one
SELECT * FROM fleet_namespace_fences WHERE namespace = @namespace;

-- name: InsertFleetNamespaceFence :one
INSERT INTO fleet_namespace_fences (namespace,fleet_id,closed,generation,operation_key)
VALUES (@namespace,@fleet_id,true,1,@operation_key) RETURNING *;

-- name: CloseFleetNamespaceFence :one
UPDATE fleet_namespace_fences SET closed=true,generation=generation+1,operation_key= @operation_key
WHERE namespace= @namespace AND fleet_id= @fleet_id AND generation= @generation AND NOT closed
RETURNING *;

-- name: OpenFleetNamespaceFence :execrows
UPDATE fleet_namespace_fences SET closed=false,finalized=false,completion_manifest=NULL
WHERE namespace= @namespace AND fleet_id= @fleet_id AND operation_key= @operation_key AND generation= @generation AND closed;

-- name: FinalizeFleetNamespaceFence :execrows
UPDATE fleet_namespace_fences SET finalized=true,completion_manifest= @completion_manifest::jsonb
WHERE namespace = @namespace AND fleet_id = @fleet_id AND operation_key = @operation_key
AND generation = @generation AND closed AND NOT finalized;

-- name: BeginFleetNamespaceDestroy :one
UPDATE fleet_namespace_fences SET finalized=false,generation=generation+1,completion_manifest=NULL
WHERE namespace = @namespace AND fleet_id = @fleet_id AND operation_key = @operation_key
AND generation = @generation AND closed AND finalized RETURNING *;

-- name: ListFleetNamespaceNodes :many
SELECT * FROM fleet_nodes WHERE namespace= @namespace
AND (@after_id::uuid = '00000000-0000-0000-0000-000000000000'::uuid OR id > @after_id::uuid)
ORDER BY id LIMIT @page_limit;

-- name: FleetNodeSharedLock :exec
SELECT pg_advisory_xact_lock_shared(hashtextextended(sqlc.arg(namespace)::text || ':' || sqlc.arg(node_id)::uuid::text, 0));

-- name: FleetNodeExclusiveLock :exec
SELECT pg_advisory_xact_lock(hashtextextended(sqlc.arg(namespace)::text || ':' || sqlc.arg(node_id)::uuid::text, 0));

-- name: FleetNodeCapacityLock :exec
SELECT pg_advisory_xact_lock(hashtextextended('capacity:' || sqlc.arg(namespace)::text || ':' || sqlc.arg(node_id)::uuid::text, 0));

-- name: GetFleetNode :one
SELECT * FROM fleet_nodes WHERE namespace = @namespace AND owner_id = @owner_id AND id = @node_id;

-- name: ListFleetNodesByOwner :many
-- Fully terminated tombstones stay durable but are not provisioned nodes, so the
-- owner-facing list hides them exactly like the provisioned-node count does.
SELECT * FROM fleet_nodes WHERE namespace = @namespace AND owner_id = @owner_id
 AND NOT (desired = 'terminated' AND status = 'terminated')
ORDER BY created_at, id LIMIT @page_limit OFFSET @page_offset;

-- name: ResolveFleetNodeReference :one
SELECT id FROM fleet_nodes WHERE namespace = @namespace AND owner_id = @owner_id
 AND (id::text = @reference::text OR (container_id <> '' AND container_id = @reference::text))
ORDER BY (id::text = @reference::text) DESC LIMIT 1;

-- name: GetFleetOperation :one
SELECT * FROM fleet_node_operations WHERE namespace = @namespace AND owner_id = @owner_id AND id = @operation_id;

-- name: GetFleetNodeForRuntime :one
SELECT n.* FROM fleet_nodes n JOIN agent_runtime r ON r.metadata->>'fleet_node_id' = n.id::text
WHERE n.namespace = @namespace AND n.owner_id = @owner_id AND r.owner_id = n.owner_id
 AND r.id = @runtime_id AND r.metadata->>'managed_by' = 'local_fleet';

-- name: CountFleetActiveRuns :one
-- Safety counts deliberately span all associated workspaces, not a request header.
SELECT count(*) FROM agent_task_queue t
JOIN agent_runtime r ON r.id = t.runtime_id
JOIN fleet_nodes n ON r.metadata->>'fleet_node_id' = n.id::text
WHERE n.namespace = @namespace AND n.owner_id = @owner_id AND n.id = @node_id
 AND r.owner_id = n.owner_id AND r.metadata->>'managed_by' = 'local_fleet'
 AND t.status IN ('dispatched', 'running', 'waiting_local_directory');

-- name: CountFleetQueuedRuns :one
SELECT count(*) FROM agent_task_queue t
JOIN agent_runtime r ON r.id = t.runtime_id
JOIN fleet_nodes n ON r.metadata->>'fleet_node_id' = n.id::text
WHERE n.namespace = @namespace AND n.owner_id = @owner_id AND n.id = @node_id
 AND r.owner_id = n.owner_id AND r.metadata->>'managed_by' = 'local_fleet'
 AND t.status IN ('queued', 'deferred');

-- name: FleetOwnerExclusiveLock :exec
-- Creation and profile projection share this protocol; batch owners sort by UUID.
-- Acquire owner before any node/capacity/runtime locks if combining protocols.
SELECT pg_advisory_xact_lock(hashtextextended('fleet-owner:' || sqlc.arg(namespace)::text || ':' || sqlc.arg(owner_id)::uuid::text, 0));

-- name: FleetOwnerExists :one
SELECT EXISTS (SELECT 1 FROM "user" WHERE id = @owner_id);

-- name: GetFleetProfile :one
SELECT * FROM fleet_credential_profiles WHERE namespace = @namespace AND owner_id = @owner_id;

-- name: UpsertFleetProfile :exec
INSERT INTO fleet_credential_profiles (namespace, owner_id, profile_ref, config_version)
VALUES (@namespace, @owner_id, @profile_ref, @config_version)
ON CONFLICT (namespace, owner_id) DO UPDATE SET profile_ref = EXCLUDED.profile_ref,
 config_version = EXCLUDED.config_version, updated_at = now();

-- name: GetFleetIntentByKey :one
SELECT * FROM fleet_node_operations WHERE namespace = @namespace AND owner_id = @owner_id AND idempotency_key = @idempotency_key;

-- name: CountFleetProvisionedNodes :one
SELECT count(*) FROM fleet_nodes WHERE namespace = @namespace AND owner_id = @owner_id
 AND NOT (desired = 'terminated' AND status = 'terminated');

-- name: InsertFleetNode :one
-- Volume names are durable node identity: they are derived from the generated
-- node UUID so a retried create/adopt finds the same owned volumes.
WITH new_node AS (SELECT gen_random_uuid() AS id)
INSERT INTO fleet_nodes (id, namespace, owner_id, name, spec, image, profile_ref, spec_config, data_volume, secrets_volume)
SELECT n.id, @namespace, @owner_id, @name, @spec, @image, @profile_ref, @spec_config,
       'multica-fleet-' || n.id::text || '-data',
       'multica-fleet-' || n.id::text || '-secrets'
FROM new_node n
RETURNING *;

-- name: InsertFleetAuroraNode :one
-- Aurora owns the node and daemon UUIDs. Persist them verbatim with the
-- workspace/runtime dimensions and the administrator's approved image, so the
-- SSOT identity is never a generated default and no enrollment secret is written.
INSERT INTO fleet_nodes (id, daemon_id, namespace, owner_id, workspace_id, runtime_id, name, spec, image, profile_ref, spec_config, data_volume, secrets_volume)
VALUES (@node_id, @daemon_id, @namespace, @owner_id, @workspace_id, @runtime_id, @name, @spec, @image, '', @spec_config, @data_volume, @secrets_volume)
RETURNING *;

-- name: GetFleetAuroraIdentityClaim :many
-- The node id and the daemon id are both globally unique (fleet_nodes_id_idx,
-- fleet_nodes_daemon_idx) while every scoped lookup in this file filters by
-- namespace, so one statement resolves both claims in a single round trip: a row
-- left by an earlier Fleet namespace is invisible to the scoped lookups yet still
-- blocks the insert. At most one row can match -- a row owning this daemon id
-- would have to carry this same node id to match both, and the unique indexes
-- forbid a second row for either key.
SELECT * FROM fleet_nodes WHERE id = @node_id OR daemon_id = @daemon_id;

-- name: AdoptFleetAuroraNode :one
-- Move a row left by an earlier Fleet namespace onto the caller's namespace and
-- rewind it to a fresh bootstrap on the next control generation, mirroring
-- FleetResetAuroraNode's fresh-bootstrap state. The approved image and the spec
-- config are deployment-scoped, not identity, so both are rewritten from the
-- current deployment. Owner, daemon id and the workspace/runtime dimensions are
-- durable identity the caller has already verified; the volumes are named from
-- the node id and stay in place.
UPDATE fleet_nodes SET namespace= @namespace, generation= @next_generation, image= @image,
 spec_config= @spec_config, desired='running', status='creating', container_id='', start_epoch='', ready=false,
 maintenance=false, revoked=false, error_code='', error_message='', observation='{}'::jsonb,
 updated_at=now()
WHERE id= @node_id
RETURNING *;

-- name: InsertFleetCreateOperation :one
INSERT INTO fleet_node_operations (namespace, owner_id, node_id, action, idempotency_key, request_hash)
VALUES (@namespace, @owner_id, @node_id, 'create', @idempotency_key, @request_hash) RETURNING *;

-- name: FleetResetAuroraNode :one
-- Keep the fresh-bootstrap column list below in step with AdoptFleetAuroraNode,
-- which rewinds a row left by an earlier namespace to the same state.
-- Re-arm the same Aurora node identity after its create failed, it was revoked,
-- or the deployed image digest changed. Volumes, daemon id, owner and
-- workspace/runtime dimensions are durable identity and must survive; only the
-- control generation, the approved deployment image, and the fresh-bootstrap
-- state change. An image change is deployment-scoped, not identity: reusing the
-- same row keeps the same idempotency key and create intent. The old observation
-- may carry a bootstrap success receipt bound to the prior generation, so it is
-- cleared.
UPDATE fleet_nodes n SET generation= @next_generation,image= @image,desired='running',status='creating',
 container_id='',start_epoch='',ready=false,maintenance=false,revoked=false,
 error_code='',error_message='',observation='{}'::jsonb,updated_at=now()
WHERE n.namespace= @namespace AND n.owner_id= @owner_id AND n.id= @node_id AND n.generation= @generation
 AND (n.revoked OR n.image<> @image OR EXISTS (SELECT 1 FROM fleet_node_operations o
  WHERE o.namespace=n.namespace AND o.owner_id=n.owner_id AND o.node_id=n.id AND o.id= @operation_id
   AND (o.phase='failed' OR o.non_retryable)))
RETURNING n.*;

-- name: FleetResetAuroraCreateOperation :one
-- Make the original create operation claimable again at the reset generation.
-- The idempotency key is untouched: this is the same create intent, not a second
-- one. request_hash is rewritten to the current deployment fingerprint so the
-- re-armed intent replays under the new image instead of conflicting forever.
UPDATE fleet_node_operations o SET generation= @next_generation,phase='queued',
 request_hash= @request_hash,
 bootstrap_minted=false,non_retryable=false,bootstrap_claimed_at=NULL,attempts=0,
 next_attempt_at=NULL,error_code='',error_message='',updated_at=now()
WHERE o.namespace= @namespace AND o.owner_id= @owner_id AND o.node_id= @node_id AND o.id= @operation_id
 AND o.generation= @generation AND o.action='create'
RETURNING o.*;

-- name: FleetRetireAuroraLifecycleOperations :execrows
-- Re-arming the one create intent supersedes any unfinished non-create intent
-- recorded for the same node (for example a queued destroy from an earlier
-- workspace teardown). Retiring it here keeps the identity re-creatable instead
-- of leaving an operation that can never match the re-created generation.
UPDATE fleet_node_operations SET phase='failed',non_retryable=true,error_code='superseded',
 error_message='',next_attempt_at=NULL,updated_at=now()
WHERE namespace= @namespace AND owner_id= @owner_id AND node_id= @node_id
 AND generation<= @generation AND action<>'create' AND phase NOT IN ('completed','failed');

-- name: FleetUnfinishedOperations :one
SELECT count(*) FROM fleet_node_operations WHERE namespace = @namespace AND owner_id = @owner_id AND node_id = @node_id
 AND phase NOT IN ('completed','failed');

-- name: InsertFleetLifecycleOperation :one
INSERT INTO fleet_node_operations(namespace,owner_id,node_id,action,idempotency_key,request_hash,phase,prior_desired,generation,approved)
VALUES(@namespace,@owner_id,@node_id,@action,@idempotency_key,@request_hash,@phase,@prior_desired,@generation,@approved) RETURNING *;

-- name: FleetAdvanceIntent :execrows
UPDATE fleet_nodes SET generation = @next_generation,desired = @desired,maintenance = @maintenance,ready=false,updated_at=now()
WHERE namespace = @namespace AND owner_id = @owner_id AND id = @node_id AND generation = @generation;

-- name: FleetApproveOperation :execrows
UPDATE fleet_node_operations SET approved=true,phase='queued',updated_at=now()
WHERE namespace = @namespace AND owner_id = @owner_id AND id = @operation_id AND generation = @generation AND phase='preparing' AND NOT approved;

-- name: FleetAbortOperation :execrows
UPDATE fleet_node_operations SET approved=false,phase='failed',error_code='busy',updated_at=now()
WHERE namespace = @namespace AND owner_id = @owner_id AND id = @operation_id AND generation = @generation AND phase='preparing' AND NOT approved;

-- name: FleetRestoreMaintenance :execrows
UPDATE fleet_nodes SET desired = @desired,maintenance=false,ready=false,updated_at=now()
WHERE namespace = @namespace AND owner_id = @owner_id AND id = @node_id AND generation = @generation AND maintenance;

-- name: FleetSetMaintenanceDesired :execrows
UPDATE fleet_nodes SET desired = @desired,ready=false,updated_at=now()
WHERE namespace = @namespace AND owner_id = @owner_id AND id = @node_id AND generation = @generation AND maintenance;

-- name: FleetApproveDelete :execrows
UPDATE fleet_nodes SET desired='terminating',status='terminating',revoked=true,ready=false,updated_at=now()
WHERE namespace = @namespace AND owner_id = @owner_id AND id = @node_id AND generation = @generation AND maintenance;

-- name: FleetOperationLocator :one
SELECT * FROM fleet_node_operations WHERE namespace = @namespace AND id = @operation_id;

-- name: GetFleetNodeIdentity :one
-- The globally unique node ID locates its trusted namespace, never caller metadata.
SELECT * FROM fleet_nodes WHERE id = @node_id AND owner_id = @owner_id;

-- name: GetFleetNodeByID :one
SELECT * FROM fleet_nodes WHERE namespace = @namespace AND id = @node_id;

-- name: MaxFleetCredentialGeneration :one
SELECT COALESCE(max(generation), 0)::bigint FROM fleet_node_credentials
WHERE namespace = @namespace AND node_id = @node_id AND owner_id = @owner_id;

-- name: RevokeFleetCredentials :exec
UPDATE fleet_node_credentials SET revoked_at = now(), updated_at = now()
WHERE namespace = @namespace AND node_id = @node_id AND owner_id = @owner_id AND revoked_at IS NULL;

-- name: InsertFleetCredential :exec
INSERT INTO fleet_node_credentials (namespace, node_id, owner_id, token_hash, generation)
VALUES (@namespace, @node_id, @owner_id, @token_hash, @generation);

-- name: VerifyFleetCredential :one
SELECT n.* FROM fleet_node_credentials c
JOIN fleet_nodes n ON n.id = c.node_id AND n.owner_id = c.owner_id AND n.namespace = c.namespace
JOIN "user" u ON u.id = n.owner_id
WHERE c.namespace = @namespace AND c.token_hash = @token_hash AND c.revoked_at IS NULL
 AND NOT n.revoked AND n.desired <> 'terminated' AND n.status <> 'terminated'
 AND c.generation = (SELECT max(h.generation) FROM fleet_node_credentials h
   WHERE h.namespace = n.namespace AND h.node_id = n.id AND h.owner_id = n.owner_id);

-- name: FleetRecoveryClock :one
SELECT clock_timestamp()::timestamptz AS sql_now;

-- name: FleetObservable :many
SELECT n.* FROM fleet_nodes n
WHERE n.namespace= @namespace AND n.container_id<>'' AND NOT n.revoked AND NOT n.maintenance AND n.desired='running'
 AND n.id> @after_id::uuid
 AND NOT EXISTS (SELECT 1 FROM fleet_node_operations o WHERE o.namespace=n.namespace AND o.node_id=n.id
  AND o.generation=n.generation AND o.phase IN ('queued','preparing','prepared','applying'))
ORDER BY n.id LIMIT 100;

-- name: ListFleetRecoverable :many
-- Projections are scheduling candidates, not physical authority.
SELECT o.* FROM fleet_node_operations o JOIN fleet_nodes n
 ON n.namespace=o.namespace AND n.owner_id=o.owner_id AND n.id=o.node_id AND n.generation=o.generation
WHERE o.namespace= @namespace AND o.phase IN ('queued','preparing','prepared','applying')
 AND NOT o.non_retryable AND o.attempts<5
 AND (o.next_attempt_at IS NULL OR o.next_attempt_at<=clock_timestamp())
 AND (o.action<>'create' OR n.container_id<>'' OR o.bootstrap_claimed_at IS NULL
  OR (o.bootstrap_claimed_at<=clock_timestamp() AND o.bootstrap_claimed_at+interval '5 minutes'<=clock_timestamp()))
 AND o.id> @after_id::uuid
ORDER BY o.id LIMIT 100;

-- name: FleetClaimLifecycle :one
UPDATE fleet_node_operations o SET action_claimed_at=clock_timestamp(),action_start_epoch= @start_epoch,phase='applying',updated_at=now()
WHERE o.namespace= @namespace AND o.owner_id= @owner_id AND o.node_id= @node_id AND o.id= @operation_id
 AND o.generation= @generation AND o.action= @action AND o.approved= @approved
 AND o.action IN ('start','stop','reboot') AND o.phase IN ('queued','prepared')
 AND o.action_claimed_at IS NULL AND NOT o.non_retryable AND o.attempts<5
 AND (o.next_attempt_at IS NULL OR o.next_attempt_at<=clock_timestamp())
 AND EXISTS (SELECT 1 FROM fleet_nodes n WHERE n.namespace=o.namespace AND n.owner_id=o.owner_id AND n.id=o.node_id
  AND n.generation=o.generation AND n.container_id= @container_id AND n.container_id<>'' AND n.start_epoch= @start_epoch
  AND NOT n.revoked AND ((o.action='start' AND NOT n.maintenance AND n.desired='running')
   OR (o.action='stop' AND o.approved AND n.maintenance AND n.desired='stopped')
   OR (o.action='reboot' AND o.approved AND n.maintenance AND n.desired='running')))
RETURNING o.*;

-- name: FleetClaimBootstrap :one
UPDATE fleet_node_operations o SET bootstrap_claimed_at=clock_timestamp(),phase='applying',updated_at=now()
WHERE o.namespace= @namespace AND o.owner_id= @owner_id AND o.node_id= @node_id AND o.id= @operation_id
 AND o.generation= @generation AND o.action='create' AND o.phase IN ('queued','prepared')
 AND o.bootstrap_claimed_at IS NULL AND NOT o.bootstrap_minted AND NOT o.non_retryable AND o.attempts<5
 AND (o.next_attempt_at IS NULL OR o.next_attempt_at<=clock_timestamp())
 AND EXISTS (SELECT 1 FROM fleet_nodes n WHERE n.namespace=o.namespace AND n.owner_id=o.owner_id AND n.id=o.node_id
  AND n.generation=o.generation AND n.container_id='' AND NOT n.revoked AND NOT n.maintenance AND n.desired='running')
RETURNING o.*;

-- name: FleetMarkBootstrapMinted :execrows
UPDATE fleet_node_operations o SET bootstrap_minted=true,updated_at=now()
WHERE o.namespace= @namespace AND o.owner_id= @owner_id AND o.node_id= @node_id AND o.id= @operation_id
 AND o.generation= @generation AND o.action='create' AND o.phase='applying'
 AND o.bootstrap_claimed_at= @claimed_at AND o.bootstrap_claimed_at<=clock_timestamp()
 AND o.bootstrap_claimed_at+interval '5 minutes'>clock_timestamp() AND NOT o.bootstrap_minted AND NOT o.non_retryable
 AND EXISTS (SELECT 1 FROM fleet_nodes n WHERE n.namespace=o.namespace AND n.owner_id=o.owner_id AND n.id=o.node_id
  AND n.generation=o.generation AND n.container_id='' AND NOT n.revoked AND NOT n.maintenance AND n.desired='running');

-- name: FleetConfirmBootstrap :execrows
UPDATE fleet_nodes n SET container_id= @container_id,start_epoch= @start_epoch,status= @status,ready=false,observation= @observation,updated_at=now()
FROM fleet_node_operations o
WHERE n.namespace= @namespace AND n.owner_id= @owner_id AND n.id= @node_id AND n.generation= @generation
 AND n.container_id='' AND NOT n.revoked AND NOT n.maintenance AND n.desired='running'
 AND o.namespace=n.namespace AND o.owner_id=n.owner_id AND o.node_id=n.id AND o.generation=n.generation
 AND o.id= @operation_id AND o.action='create' AND o.phase='applying' AND o.bootstrap_minted AND NOT o.non_retryable
 AND o.bootstrap_claimed_at= @claimed_at AND o.bootstrap_claimed_at<=clock_timestamp()
 AND o.bootstrap_claimed_at+interval '5 minutes'>clock_timestamp();

-- name: FleetExpireBootstrapNode :execrows
UPDATE fleet_nodes n SET revoked=true,ready=false,error_code='bootstrap-unrecoverable',error_message='',updated_at=now()
FROM fleet_node_operations o
WHERE n.namespace= @namespace AND n.owner_id= @owner_id AND n.id= @node_id AND n.generation= @generation
 AND n.container_id='' AND NOT n.revoked AND NOT n.maintenance AND n.desired='running'
 AND o.namespace=n.namespace AND o.owner_id=n.owner_id AND o.node_id=n.id AND o.generation=n.generation
 AND o.id= @operation_id AND o.action='create' AND o.phase='applying' AND NOT o.non_retryable
 AND o.bootstrap_claimed_at= @claimed_at AND o.bootstrap_claimed_at<=clock_timestamp()
 AND o.bootstrap_claimed_at+interval '5 minutes'<=clock_timestamp();

-- name: FleetExpireBootstrapOperation :execrows
UPDATE fleet_node_operations SET non_retryable=true,error_code='bootstrap-unrecoverable',error_message='',updated_at=now()
WHERE namespace= @namespace AND owner_id= @owner_id AND node_id= @node_id AND id= @operation_id
 AND generation= @generation AND action='create' AND phase='applying' AND NOT non_retryable
 AND bootstrap_claimed_at= @claimed_at AND bootstrap_claimed_at<=clock_timestamp()
 AND bootstrap_claimed_at+interval '5 minutes'<=clock_timestamp();

-- name: FleetExpireConfirmedCreateNode :execrows
UPDATE fleet_nodes n SET revoked=true,ready=false,error_code='initialization-timeout',error_message='',updated_at=now()
FROM fleet_node_operations o
WHERE n.namespace= @namespace AND n.owner_id= @owner_id AND n.id= @node_id AND n.generation= @generation
 AND n.container_id= @container_id AND n.container_id<>'' AND NOT n.revoked AND NOT n.maintenance AND n.desired='running'
 AND NOT (n.observation ? 'bootstrap_success')
 AND o.namespace=n.namespace AND o.owner_id=n.owner_id AND o.node_id=n.id AND o.generation=n.generation
 AND o.id= @operation_id AND o.action='create' AND o.phase='applying' AND o.bootstrap_minted AND NOT o.non_retryable
 AND o.bootstrap_claimed_at= @claimed_at AND o.bootstrap_claimed_at<=clock_timestamp()
 AND o.bootstrap_claimed_at+interval '5 minutes'<=clock_timestamp();

-- name: FleetExpireConfirmedCreateOperation :execrows
UPDATE fleet_node_operations SET non_retryable=true,error_code='initialization-timeout',error_message='',next_attempt_at=NULL,updated_at=now()
WHERE namespace= @namespace AND owner_id= @owner_id AND node_id= @node_id AND id= @operation_id
 AND generation= @generation AND action='create' AND phase='applying' AND bootstrap_minted AND NOT non_retryable
 AND bootstrap_claimed_at= @claimed_at AND bootstrap_claimed_at<=clock_timestamp()
 AND bootstrap_claimed_at+interval '5 minutes'<=clock_timestamp();

-- name: FleetRecordObservation :execrows
UPDATE fleet_nodes SET observation= @observation::jsonb ||
 CASE WHEN observation ? 'bootstrap_success' THEN jsonb_build_object('bootstrap_success',observation->'bootstrap_success') ELSE '{}'::jsonb END,
 status=CASE WHEN desired IN ('terminating','terminated') THEN status ELSE @status END,
 start_epoch=CASE WHEN @start_epoch::text<>'' THEN @start_epoch ELSE start_epoch END,
 ready=(@ready::boolean AND NOT maintenance AND NOT revoked AND desired='running'),
 health_at= @observed_at,active_runs= @active_runs,pending_reports= @pending_reports,failed_reports= @failed_reports,updated_at=now()
WHERE namespace= @namespace AND owner_id= @owner_id AND id= @node_id AND generation= @generation
 AND @observed_at::timestamptz<=clock_timestamp() AND @observed_at::timestamptz>=clock_timestamp()-interval '30 seconds'
 AND (health_at IS NULL OR health_at<= @observed_at);

-- name: FleetCompleteOperation :execrows
UPDATE fleet_node_operations o SET phase='completed',error_code='',error_message='',next_attempt_at=NULL,updated_at=now()
WHERE o.namespace= @namespace AND o.owner_id= @owner_id AND o.node_id= @node_id AND o.id= @operation_id
 AND o.generation= @generation AND o.action= @action AND o.approved= @approved AND o.phase= @phase
 AND o.phase IN ('queued','prepared','applying') AND NOT o.non_retryable
 AND (o.action<>'create' OR o.bootstrap_claimed_at IS NULL
  OR (o.bootstrap_claimed_at<=clock_timestamp() AND o.bootstrap_claimed_at+interval '5 minutes'>clock_timestamp())
   OR EXISTS (SELECT 1 FROM fleet_nodes n WHERE n.namespace=o.namespace
    AND n.owner_id=o.owner_id AND n.id=o.node_id
    AND n.generation=o.generation AND NOT n.revoked AND NOT n.maintenance
    AND n.desired='running' AND n.status='running' AND n.ready
    AND n.health_at<=clock_timestamp() AND n.health_at>=clock_timestamp()-interval '30 seconds'
    AND n.observation->'bootstrap_success'=sqlc.narg(success_receipt)::jsonb));

-- name: FleetFinishLifecycleNode :execrows
UPDATE fleet_nodes SET status= @status,start_epoch= @start_epoch,maintenance=false,ready= @ready,updated_at=now()
WHERE namespace= @namespace AND owner_id= @owner_id AND id= @node_id AND generation= @generation AND NOT revoked
 AND desired= @desired;

-- name: FleetScheduleOperation :execrows
-- Unknown defers do not spend attempts; actual errors do. No phase/approval/claim mutation.
UPDATE fleet_node_operations SET attempts=LEAST(5,attempts+ sqlc.arg(attempt_increment)::integer),
 non_retryable=(@permanent::boolean OR attempts+ sqlc.arg(attempt_increment)::integer>=5),error_code= @error_code,error_message='',
 next_attempt_at=clock_timestamp()+ sqlc.arg(delay_seconds)::integer*interval '1 second',updated_at=now()
WHERE namespace= @namespace AND owner_id= @owner_id AND node_id= @node_id AND id= @operation_id
 AND generation= @generation AND action= @action AND phase= @phase AND approved= @approved AND attempts= @attempts
 AND phase IN ('queued','preparing','prepared','applying') AND NOT non_retryable;

-- name: FleetDeleteRuntimes :many
SELECT r.* FROM agent_runtime r WHERE r.owner_id= @owner_id
 AND r.metadata->>'managed_by'='local_fleet' AND r.metadata->>'fleet_node_id'= @node_text::text ORDER BY r.id;

-- name: FleetOfflineDeletedRuntime :execrows
UPDATE agent_runtime SET status='offline',metadata=metadata || '{"fleet_tombstone":true}'::jsonb,updated_at=now()
WHERE id= @runtime_id AND owner_id= @owner_id AND metadata->>'managed_by'='local_fleet' AND metadata->>'fleet_node_id'= @node_text::text;

-- name: FleetTombstoneNode :execrows
UPDATE fleet_nodes n SET desired='terminated',status='terminated',ready=false,revoked=true,updated_at=now()
FROM fleet_node_operations o WHERE n.namespace= @namespace AND n.owner_id= @owner_id AND n.id= @node_id AND n.generation= @generation
 AND n.maintenance AND n.revoked AND n.desired='terminating'
 AND o.namespace=n.namespace AND o.owner_id=n.owner_id AND o.node_id=n.id AND o.generation=n.generation
 AND o.id= @operation_id AND o.action='delete' AND o.approved AND o.phase IN ('queued','prepared','applying') AND NOT o.non_retryable;

-- name: FleetFailBootstrapNode :execrows
UPDATE fleet_nodes n SET revoked=true,ready=false,error_code= @error_code,error_message='',updated_at=now()
FROM fleet_node_operations o
WHERE n.namespace= @namespace AND n.owner_id= @owner_id AND n.id= @node_id AND n.generation= @generation
 AND n.container_id='' AND NOT n.revoked AND NOT n.maintenance AND n.desired='running'
 AND o.namespace=n.namespace AND o.owner_id=n.owner_id AND o.node_id=n.id AND o.generation=n.generation
 AND o.id= @operation_id AND o.action='create' AND o.phase='applying' AND NOT o.non_retryable
 AND o.bootstrap_claimed_at= @claimed_at AND o.bootstrap_claimed_at<=clock_timestamp()
 AND o.bootstrap_claimed_at+interval '5 minutes'>clock_timestamp();

-- name: FleetFailBootstrapOperation :execrows
UPDATE fleet_node_operations SET non_retryable=true,error_code= @error_code,error_message='',updated_at=now()
WHERE namespace= @namespace AND owner_id= @owner_id AND node_id= @node_id AND id= @operation_id
 AND generation= @generation AND action='create' AND phase='applying' AND NOT non_retryable
 AND bootstrap_claimed_at= @claimed_at AND bootstrap_claimed_at<=clock_timestamp()
 AND bootstrap_claimed_at+interval '5 minutes'>clock_timestamp();
