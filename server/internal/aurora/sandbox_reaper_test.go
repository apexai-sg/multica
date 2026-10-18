package aurora_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/aurora"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// fakeNodeDeleter records fleet deletions without speaking HTTP. It implements
// the full FleetProvisioner seam because the reaper holds that interface.
type fakeNodeDeleter struct {
	mu      sync.Mutex
	deleted []string
	owners  []string
	err     error
}

func (f *fakeNodeDeleter) EnsureWorkspaceNode(context.Context, string, aurora.FleetEnsureRequest) (aurora.FleetNode, error) {
	return aurora.FleetNode{}, nil
}

func (f *fakeNodeDeleter) DeleteWorkspaceNode(_ context.Context, ownerID, nodeID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.deleted = append(f.deleted, nodeID)
	f.owners = append(f.owners, ownerID)
	return nil
}

func (f *fakeNodeDeleter) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.deleted)
}

// recordingSettler runs the real task transition so the reaper exercises the
// production SQL, and records every batch handed to the single settlement
// responder.
type recordingSettler struct {
	queries *db.Queries

	mu      sync.Mutex
	batches [][]db.AgentTaskQueue
	settled int
}

func (s *recordingSettler) FailAuroraSandboxTasksForRuntime(ctx context.Context, arg db.FailAuroraSandboxTasksForRuntimeParams) ([]db.AgentTaskQueue, error) {
	return s.queries.FailAuroraSandboxTasksForRuntime(ctx, arg)
}

func (s *recordingSettler) HandleFailedTasks(_ context.Context, tasks []db.AgentTaskQueue) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(tasks) == 0 {
		return 0
	}
	s.batches = append(s.batches, tasks)
	s.settled += len(tasks)
	return len(tasks)
}

func (s *recordingSettler) settledCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settled
}

func (s *recordingSettler) batchCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.batches)
}

// reaperFixture is a workspace with its managed runtime, a controllable node
// row, and a recording settler.
type reaperFixture struct {
	pool      *pgxpool.Pool
	queries   *db.Queries
	workspace pgtype.UUID
	runtimeID pgtype.UUID
	owner     pgtype.UUID
	now       time.Time
}

func newReaperFixture(t *testing.T) *reaperFixture {
	t.Helper()
	pool := auroraTestPool(t)
	q := db.New(pool)
	ctx := context.Background()

	owner := newAuroraTestUser(t, pool)
	ws := newAuroraTestWorkspace(t, pool, owner)
	if err := aurora.EnsureSystemAgents(ctx, q, ws, owner); err != nil {
		t.Fatalf("ensure system agents: %v", err)
	}
	runtimeID, err := aurora.ManagedRuntimeID(ctx, q, ws)
	if err != nil {
		t.Fatalf("managed runtime id: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM agent_task_queue WHERE runtime_id = $1", runtimeID)
		_, _ = pool.Exec(ctx, "DELETE FROM issue WHERE workspace_id = $1", ws)
		_, _ = pool.Exec(ctx, "DELETE FROM daemon_token WHERE workspace_id = $1", ws)
		_, _ = pool.Exec(ctx, "DELETE FROM aurora_sandbox_node WHERE workspace_id = $1", ws)
		_, _ = pool.Exec(ctx, "DELETE FROM agent_skill WHERE agent_id IN (SELECT id FROM agent WHERE workspace_id = $1)", ws)
		_, _ = pool.Exec(ctx, "DELETE FROM agent WHERE workspace_id = $1", ws)
		_, _ = pool.Exec(ctx, "DELETE FROM skill WHERE workspace_id = $1", ws)
		_, _ = pool.Exec(ctx, "DELETE FROM agent_runtime WHERE workspace_id = $1", ws)
	})
	return &reaperFixture{pool: pool, queries: q, workspace: ws, runtimeID: runtimeID, owner: owner, now: time.Now().UTC()}
}

// node writes a node row directly in the requested lifecycle position. The
// timestamps are relative to the fixture clock so the reaper's cutoffs are
// exercised, not the wall clock.
type nodeOptions struct {
	state          string
	daemonID       string
	backendNodeID  string
	createdAgo     time.Duration
	lastActiveAgo  time.Duration
	startedAgo     time.Duration
	drainAgo       time.Duration
	liveEnrollment bool
}

func (f *reaperFixture) node(t *testing.T, opts nodeOptions) db.AuroraSandboxNode {
	t.Helper()
	ctx := context.Background()
	params := sandboxNodeParams(t, f.workspace, f.runtimeID, opts.daemonID)
	if opts.state != "" {
		params.State = opts.state
	}
	created, err := f.queries.CreateAuroraSandboxNode(ctx, params)
	if err != nil {
		t.Fatalf("create sandbox node: %v", err)
	}

	optional := func(d time.Duration, set bool) pgtype.Timestamptz {
		if !set {
			return pgtype.Timestamptz{}
		}
		return pgtype.Timestamptz{Time: f.now.Add(-d).Truncate(time.Microsecond), Valid: true}
	}
	// last_active_at is NOT NULL: a node that was never active falls back to
	// its creation time, which is what "idle since it appeared" means.
	lastActiveAgo := opts.lastActiveAgo
	if lastActiveAgo <= 0 {
		lastActiveAgo = opts.createdAgo
	}
	backend := pgtype.Text{}
	if opts.backendNodeID != "" {
		backend = pgtype.Text{String: opts.backendNodeID, Valid: true}
	}
	_, err = f.pool.Exec(ctx, `
		UPDATE aurora_sandbox_node
		SET state = $2,
		    backend_node_id = $3,
		    created_at = $4,
		    last_active_at = $5,
		    started_at = $6,
		    drain_started_at = $7,
		    enrollment_token_hash = CASE WHEN $8 THEN enrollment_token_hash ELSE NULL END,
		    enrollment_expires_at = CASE WHEN $8 THEN enrollment_expires_at ELSE NULL END
		WHERE id = $1`,
		created.ID, opts.state, backend,
		pgtype.Timestamptz{Time: f.now.Add(-opts.createdAgo).Truncate(time.Microsecond), Valid: true},
		pgtype.Timestamptz{Time: f.now.Add(-lastActiveAgo).Truncate(time.Microsecond), Valid: true},
		optional(opts.startedAgo, opts.startedAgo > 0),
		optional(opts.drainAgo, opts.drainAgo > 0),
		opts.liveEnrollment,
	)
	if err != nil {
		t.Fatalf("position sandbox node: %v", err)
	}
	reloaded, err := f.queries.GetAuroraSandboxNodeByWorkspace(ctx, f.workspace)
	if err != nil {
		t.Fatalf("reload sandbox node: %v", err)
	}
	return reloaded
}

// queuedTask inserts a queued task bound to the workspace's managed runtime.
func (f *reaperFixture) queuedTask(t *testing.T) pgtype.UUID {
	t.Helper()
	ctx := context.Background()
	var agentID pgtype.UUID
	if err := f.pool.QueryRow(ctx, "SELECT id FROM agent WHERE workspace_id = $1 ORDER BY created_at, id LIMIT 1", f.workspace).Scan(&agentID); err != nil {
		t.Fatalf("select workspace agent: %v", err)
	}
	var issueID pgtype.UUID
	if err := f.pool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, creator_type, creator_id)
		VALUES ($1, 'sandbox reaper fixture', 'member', $2)
		RETURNING id`, f.workspace, f.owner).Scan(&issueID); err != nil {
		t.Fatalf("insert issue: %v", err)
	}
	var taskID pgtype.UUID
	if err := f.pool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, issue_id, runtime_id, status)
		VALUES ($1, $2, $3, 'queued')
		RETURNING id`, agentID, issueID, f.runtimeID).Scan(&taskID); err != nil {
		t.Fatalf("insert queued task: %v", err)
	}
	return taskID
}

func (f *reaperFixture) daemonToken(t *testing.T, daemonID string) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(),
		`INSERT INTO daemon_token (workspace_id, daemon_id, token_hash, expires_at)
		 VALUES ($1, $2, $3, now() + interval '1 hour')`,
		f.workspace, daemonID, "hash-"+daemonID); err != nil {
		t.Fatalf("insert daemon token: %v", err)
	}
}

func (f *reaperFixture) bindRuntime(t *testing.T, daemonID string) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(),
		"UPDATE agent_runtime SET daemon_id = $2, status = 'online' WHERE id = $1", f.runtimeID, daemonID); err != nil {
		t.Fatalf("bind runtime: %v", err)
	}
}

func (f *reaperFixture) reaper(fleet *fakeNodeDeleter, settler *recordingSettler) *aurora.SandboxReaper {
	return aurora.NewSandboxReaper(f.queries, f.pool, fleet, settler, func() time.Time { return f.now })
}

func (f *reaperFixture) nodeState(t *testing.T) db.AuroraSandboxNode {
	t.Helper()
	node, err := f.queries.GetAuroraSandboxNodeByWorkspace(context.Background(), f.workspace)
	if err != nil {
		t.Fatalf("reload node: %v", err)
	}
	return node
}

func (f *reaperFixture) taskStatus(t *testing.T, taskID pgtype.UUID) (string, string) {
	t.Helper()
	var status, reason string
	if err := f.pool.QueryRow(context.Background(),
		"SELECT status, COALESCE(failure_reason, '') FROM agent_task_queue WHERE id = $1", taskID).Scan(&status, &reason); err != nil {
		t.Fatalf("read task: %v", err)
	}
	return status, reason
}

func (f *reaperFixture) daemonTokenCount(t *testing.T, daemonID string) int {
	t.Helper()
	var count int
	if err := f.pool.QueryRow(context.Background(),
		"SELECT count(*) FROM daemon_token WHERE workspace_id = $1 AND daemon_id = $2", f.workspace, daemonID).Scan(&count); err != nil {
		t.Fatalf("count daemon tokens: %v", err)
	}
	return count
}

func (f *reaperFixture) runtimeBinding(t *testing.T) (string, string) {
	t.Helper()
	var status, daemonID string
	if err := f.pool.QueryRow(context.Background(),
		"SELECT status, COALESCE(daemon_id, '') FROM agent_runtime WHERE id = $1", f.runtimeID).Scan(&status, &daemonID); err != nil {
		t.Fatalf("read runtime: %v", err)
	}
	return status, daemonID
}

func (f *reaperFixture) sweep(t *testing.T, fleet *fakeNodeDeleter, settler *recordingSettler) aurora.SandboxReapStats {
	t.Helper()
	stats, err := f.reaper(fleet, settler).Sweep(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	return stats
}

// TestSandboxReaperFailsStartingNodeAfterTwoMinutes pins the stuck-start path:
// a node whose daemon never spent its enrollment past the grace window is
// failed and its queued work is settled.
func TestSandboxReaperFailsStartingNodeAfterTwoMinutes(t *testing.T) {
	f := newReaperFixture(t)
	f.node(t, nodeOptions{state: "starting", daemonID: "daemon-stuck", backendNodeID: "fleet-stuck", createdAgo: 3 * time.Minute})
	taskID := f.queuedTask(t)
	fleet := &fakeNodeDeleter{}
	settler := &recordingSettler{queries: f.queries}

	stats := f.sweep(t, fleet, settler)

	node := f.nodeState(t)
	if node.State != "failed" || node.FailureReason.String != aurora.SandboxRuntimeStartFailed {
		t.Fatalf("node state = %q reason = %q, want failed/%s", node.State, node.FailureReason.String, aurora.SandboxRuntimeStartFailed)
	}
	if stats.StartingFailed != 1 {
		t.Fatalf("starting failures = %d, want 1", stats.StartingFailed)
	}
	if fleet.callCount() != 1 {
		t.Fatalf("fleet deletes = %d, want 1", fleet.callCount())
	}
	status, reason := f.taskStatus(t, taskID)
	if status != "failed" || reason != aurora.SandboxRuntimeStartFailed {
		t.Fatalf("task = %q/%q, want failed/%s", status, reason, aurora.SandboxRuntimeStartFailed)
	}
	if settler.settledCount() != 1 {
		t.Fatalf("settled tasks = %d, want 1", settler.settledCount())
	}
}

// TestSandboxReaperKeepsIdleNodeBeforeFifteenMinutes pins the idle floor.
// TestSandboxReaperKeepsOnlineNode pins the long-lived runtime contract: an
// online node is not a reap candidate at all.
func TestSandboxReaperKeepsOnlineNode(t *testing.T) {
	f := newReaperFixture(t)
	f.node(t, nodeOptions{state: "online", daemonID: "daemon-busy", backendNodeID: "fleet-1", createdAgo: time.Minute, lastActiveAgo: 5 * time.Minute, startedAgo: time.Minute})
	fleet := &fakeNodeDeleter{}
	settler := &recordingSettler{queries: f.queries}

	f.sweep(t, fleet, settler)

	if node := f.nodeState(t); node.State != "online" {
		t.Fatalf("node state = %q, want online", node.State)
	}
	if fleet.callCount() != 0 {
		t.Fatalf("fleet deletes = %d, want 0", fleet.callCount())
	}
}

// TestSandboxReaperNeverStopsAnIdleOnlineNode pins the design change: idle and
// age are not lifecycle events for the long-lived workspace runtime.
func TestSandboxReaperNeverStopsAnIdleOnlineNode(t *testing.T) {
	f := newReaperFixture(t)
	f.node(t, nodeOptions{state: "online", daemonID: "daemon-idle", backendNodeID: "fleet-idle", createdAgo: 30 * time.Hour, lastActiveAgo: 20 * time.Minute, startedAgo: 30 * time.Hour})
	fleet := &fakeNodeDeleter{}
	settler := &recordingSettler{queries: f.queries}

	stats := f.sweep(t, fleet, settler)

	if node := f.nodeState(t); node.State != "online" {
		t.Fatalf("node state = %q, want online", node.State)
	}
	if stats.Stopped != 0 {
		t.Fatalf("stopped = %d, want 0", stats.Stopped)
	}
	if fleet.callCount() != 0 {
		t.Fatalf("fleet deletes = %d, want 0", fleet.callCount())
	}
}

// TestSandboxReaperKeepsDrainingNodeWithActiveTask pins the drain grace: work
// inside the window keeps the node alive.
func TestSandboxReaperKeepsDrainingNodeWithActiveTask(t *testing.T) {
	f := newReaperFixture(t)
	f.node(t, nodeOptions{state: "draining", daemonID: "daemon-draining", backendNodeID: "fleet-drain", createdAgo: 9 * time.Hour, lastActiveAgo: time.Minute, startedAgo: 9 * time.Hour, drainAgo: 5 * time.Minute})
	taskID := f.queuedTask(t)
	fleet := &fakeNodeDeleter{}
	settler := &recordingSettler{queries: f.queries}

	f.sweep(t, fleet, settler)

	if node := f.nodeState(t); node.State != "draining" {
		t.Fatalf("node state = %q, want draining", node.State)
	}
	if status, _ := f.taskStatus(t, taskID); status != "queued" {
		t.Fatalf("task status = %q, want queued", status)
	}
	if fleet.callCount() != 0 || settler.settledCount() != 0 {
		t.Fatalf("fleet deletes = %d settled = %d, want 0/0", fleet.callCount(), settler.settledCount())
	}
}

// TestSandboxReaperFailsTaskAndStopsAfterThirtyMinuteDrain pins the drain
// deadline: the task is failed with the drain reason and the node stops.
func TestSandboxReaperFailsTaskAndStopsAfterThirtyMinuteDrain(t *testing.T) {
	f := newReaperFixture(t)
	f.node(t, nodeOptions{state: "draining", daemonID: "daemon-expired", backendNodeID: "fleet-expired", createdAgo: 10 * time.Hour, lastActiveAgo: time.Minute, startedAgo: 10 * time.Hour, drainAgo: 40 * time.Minute})
	taskID := f.queuedTask(t)
	fleet := &fakeNodeDeleter{}
	settler := &recordingSettler{queries: f.queries}

	f.sweep(t, fleet, settler)

	status, reason := f.taskStatus(t, taskID)
	if status != "failed" || reason != aurora.SandboxDrainTimeout {
		t.Fatalf("task = %q/%q, want failed/%s", status, reason, aurora.SandboxDrainTimeout)
	}
	if node := f.nodeState(t); node.State != "stopped" {
		t.Fatalf("node state = %q, want stopped", node.State)
	}
	if settler.settledCount() != 1 {
		t.Fatalf("settled = %d, want 1", settler.settledCount())
	}
}

// TestSandboxReaperRevokesTokensAndMarksRuntimeOffline pins the stop
// transaction: the daemon loses its token and the runtime stops being bound.
func TestSandboxReaperRevokesTokensAndMarksRuntimeOffline(t *testing.T) {
	f := newReaperFixture(t)
	const daemonID = "daemon-revoke"
	f.node(t, nodeOptions{state: "draining", daemonID: daemonID, backendNodeID: "fleet-revoke", createdAgo: time.Hour, lastActiveAgo: 20 * time.Minute, startedAgo: 30 * time.Minute, drainAgo: 5 * time.Minute})
	f.daemonToken(t, daemonID)
	f.bindRuntime(t, daemonID)
	fleet := &fakeNodeDeleter{}
	settler := &recordingSettler{queries: f.queries}

	f.sweep(t, fleet, settler)

	if node := f.nodeState(t); node.State != "stopped" {
		t.Fatalf("node state = %q, want stopped", node.State)
	}
	if count := f.daemonTokenCount(t, daemonID); count != 0 {
		t.Fatalf("daemon tokens = %d, want 0", count)
	}
	status, boundDaemon := f.runtimeBinding(t)
	if status != "offline" || boundDaemon != "" {
		t.Fatalf("runtime = %q/%q, want offline with no daemon binding", status, boundDaemon)
	}
}

// TestSandboxReaperUsesAuroraFailureSettlementForRefund pins the single
// responder and its idempotency: exactly the tasks this sweep transitioned are
// handed to settlement once, and a repeated sweep settles nothing new.
func TestSandboxReaperUsesAuroraFailureSettlementForRefund(t *testing.T) {
	f := newReaperFixture(t)
	f.node(t, nodeOptions{state: "starting", daemonID: "daemon-settle", backendNodeID: "fleet-settle", createdAgo: 3 * time.Minute})
	taskID := f.queuedTask(t)
	fleet := &fakeNodeDeleter{}
	settler := &recordingSettler{queries: f.queries}
	r := f.reaper(fleet, settler)

	if _, err := r.Sweep(context.Background()); err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	if settler.batchCount() != 1 || settler.settledCount() != 1 {
		t.Fatalf("first sweep batches = %d settled = %d, want 1/1", settler.batchCount(), settler.settledCount())
	}
	if got := util.UUIDToString(settler.batches[0][0].ID); got != util.UUIDToString(taskID) {
		t.Fatalf("settled task = %s, want %s", got, util.UUIDToString(taskID))
	}

	if _, err := r.Sweep(context.Background()); err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if settler.batchCount() != 1 || settler.settledCount() != 1 {
		t.Fatalf("second sweep batches = %d settled = %d, want 1/1 (no duplicate refund)", settler.batchCount(), settler.settledCount())
	}
}

// TestSandboxReaperAddressesFleetNodeByWorkspaceNodeID pins the control API
// contract: deletion addresses the node UUID the server issued, not the fleet's
// backend container name, because the route validates a UUID and the backend
// resolves the container through the controlled node label.
func TestSandboxReaperAddressesFleetNodeByWorkspaceNodeID(t *testing.T) {
	f := newReaperFixture(t)
	node := f.node(t, nodeOptions{state: "draining", daemonID: "daemon-id", backendNodeID: "aurora-sbx-0123456789abcdef", createdAgo: time.Hour, lastActiveAgo: 20 * time.Minute, startedAgo: 30 * time.Minute, drainAgo: 5 * time.Minute})
	fleet := &fakeNodeDeleter{}
	settler := &recordingSettler{queries: f.queries}

	f.sweep(t, fleet, settler)

	if fleet.callCount() != 1 {
		t.Fatalf("fleet deletes = %d, want 1", fleet.callCount())
	}
	want := util.UUIDToString(node.ID)
	if fleet.deleted[0] != want {
		t.Fatalf("fleet delete address = %q, want the node UUID %q", fleet.deleted[0], want)
	}
	if want == node.BackendNodeID.String {
		t.Fatalf("delete address %q is the backend name, not the node UUID", fleet.deleted[0])
	}
	// The delete must carry the owner derived from the workspace's locked
	// aurora_managed runtime row, or the Fleet route treats it as a foreign
	// delete and silently succeeds without removing the node.
	if got, want := fleet.owners[0], util.UUIDToString(f.owner); got != want {
		t.Fatalf("fleet delete owner = %q, want the managed runtime owner %q", got, want)
	}
}
