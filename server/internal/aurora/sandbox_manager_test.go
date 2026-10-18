package aurora_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/aurora"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// fakeProvisioner is the manager's view of the provider-neutral workspace-node
// provisioner without HTTP. It records every ensure request and answers with a
// configured node or error, so the manager's ordering and rollback can be
// asserted directly.
type fakeProvisioner struct {
	mu        sync.Mutex
	calls     int
	lastOwner string
	last      aurora.FleetEnsureRequest
	requests  []aurora.FleetEnsureRequest
	node      aurora.FleetNode
	err       error
	// firstErr applies to the first failFirst ensure calls, which lets a test
	// exercise an identity rejection followed by a successful replacement.
	firstErr  error
	failFirst int
	deletes   []provisionerDelete
	deleteErr error
}

type provisionerDelete struct {
	owner  string
	nodeID string
}

func (f *fakeProvisioner) EnsureWorkspaceNode(_ context.Context, ownerID string, req aurora.FleetEnsureRequest) (aurora.FleetNode, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastOwner = ownerID
	f.last = req
	f.requests = append(f.requests, req)
	if f.failFirst > 0 {
		f.failFirst--
		return aurora.FleetNode{}, f.firstErr
	}
	if f.err != nil {
		return aurora.FleetNode{}, f.err
	}
	node := f.node
	if node.ID == "" {
		node = aurora.FleetNode{ID: "fleet-" + req.NodeID, State: "launching", BackendID: "container-" + req.NodeID}
	}
	return node, nil
}

func (f *fakeProvisioner) DeleteWorkspaceNode(_ context.Context, ownerID, nodeID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deletes = append(f.deletes, provisionerDelete{owner: ownerID, nodeID: nodeID})
	return nil
}

func (f *fakeProvisioner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// sandboxNodeOwner reads the owner the manager must derive from the locked
// managed-runtime row.
func sandboxNodeOwner(t *testing.T, q *db.Queries, ws pgtype.UUID) string {
	t.Helper()
	managed, err := q.GetAuroraManagedRuntime(context.Background(), db.GetAuroraManagedRuntimeParams{
		WorkspaceID: ws,
		Provider:    "aurora_managed",
	})
	if err != nil {
		t.Fatalf("read managed runtime owner: %v", err)
	}
	return util.UUIDToString(managed.OwnerID)
}

// TestSandboxManagerReturnsHealthyOnlineNodeWithoutFleetCall pins the no-op
// path: an online node whose runtime and image still match is already the
// desired state, so Ensure must not disturb it or call the provisioner.
func TestSandboxManagerReturnsHealthyOnlineNodeWithoutFleetCall(t *testing.T) {
	pool := auroraTestPool(t)
	q := db.New(pool)
	ws, runtimeID := newSandboxNodeWorkspace(t, q, pool)
	ctx := context.Background()

	params := sandboxNodeParams(t, ws, runtimeID, uuid.NewString())
	params.ImageDigest = validSandboxImageDigest
	created, err := q.CreateAuroraSandboxNode(ctx, params)
	if err != nil {
		t.Fatalf("create starting node: %v", err)
	}
	if _, err := q.ConsumeAuroraSandboxEnrollment(ctx, params.EnrollmentTokenHash); err != nil {
		t.Fatalf("consume enrollment to online: %v", err)
	}

	fleet := &fakeProvisioner{}
	mgr := aurora.NewSandboxManager(q, pool, fleet, validSandboxImageDigest, nil)
	node, err := mgr.Ensure(ctx, ws, runtimeID)
	if err != nil {
		t.Fatalf("ensure healthy node: %v", err)
	}
	if node.ID != created.ID {
		t.Fatalf("ensured node id = %v, want %v", node.ID, created.ID)
	}
	if node.State != "online" {
		t.Fatalf("ensured node state = %q, want online", node.State)
	}
	if fleet.callCount() != 0 {
		t.Fatalf("provisioner ensure calls = %d, want 0 for a healthy online node", fleet.callCount())
	}
	if node.EnrollmentTokenHash.Valid {
		t.Fatal("returned node carries an enrollment token hash")
	}
}

// TestSandboxManagerIssuesEnrollmentAndCallsFleetOnce pins first provision:
// Ensure mints one single-use enrollment, sends exactly one provision request
// carrying that secret, the locked runtime owner and the node identity, and
// persists the backend id the Fleet returns.
func TestSandboxManagerIssuesEnrollmentAndCallsFleetOnce(t *testing.T) {
	pool := auroraTestPool(t)
	q := db.New(pool)
	ws, runtimeID := newSandboxNodeWorkspace(t, q, pool)
	ctx := context.Background()

	fleet := &fakeProvisioner{node: aurora.FleetNode{ID: "backend-node-7", State: "launching", BackendID: "container-7"}}
	mgr := aurora.NewSandboxManager(q, pool, fleet, validSandboxImageDigest, nil)
	node, err := mgr.Ensure(ctx, ws, runtimeID)
	if err != nil {
		t.Fatalf("ensure new node: %v", err)
	}
	if node.State != "starting" {
		t.Fatalf("ensured node state = %q, want starting (the daemon has not enrolled yet)", node.State)
	}
	if fleet.callCount() != 1 {
		t.Fatalf("provisioner ensure calls = %d, want 1", fleet.callCount())
	}
	if got, want := fleet.last.NodeID, util.UUIDToString(node.ID); got != want {
		t.Fatalf("provisioner node id = %q, want %q", got, want)
	}
	if got, want := fleet.last.WorkspaceID, util.UUIDToString(ws); got != want {
		t.Fatalf("provisioner workspace id = %q, want %q", got, want)
	}
	if got, want := fleet.last.RuntimeID, util.UUIDToString(runtimeID); got != want {
		t.Fatalf("provisioner runtime id = %q, want %q", got, want)
	}
	if fleet.last.DaemonID != node.DaemonID {
		t.Fatalf("provisioner daemon id = %q, want %q", fleet.last.DaemonID, node.DaemonID)
	}
	if !strings.HasPrefix(fleet.last.EnrollmentToken, "mse_") {
		t.Fatalf("provisioner enrollment token %q is not a single-use mse_ secret", fleet.last.EnrollmentToken)
	}
	if got, want := fleet.last.ImageDigest, validSandboxImageDigest; got != want {
		t.Fatalf("provisioner image digest = %q, want %q", got, want)
	}
	if fleet.last.Name == "" || fleet.last.Spec != "sandbox" {
		t.Fatalf("provisioner name/spec = %q/%q, want a name and the sandbox spec", fleet.last.Name, fleet.last.Spec)
	}
	if got, want := fleet.lastOwner, sandboxNodeOwner(t, q, ws); got != want {
		t.Fatalf("provisioner owner = %q, want the locked runtime owner %q", got, want)
	}

	stored := sandboxNodeByWorkspace(t, pool, ws)
	if stored.State != "starting" {
		t.Fatalf("stored node state = %q, want starting", stored.State)
	}
	if !stored.BackendNodeID.Valid || stored.BackendNodeID.String != "backend-node-7" {
		t.Fatalf("stored backend node id = %+v, want backend-node-7", stored.BackendNodeID)
	}
	if !stored.EnrollmentTokenHash.Valid || stored.EnrollmentTokenHash.String != auth.HashToken(fleet.last.EnrollmentToken) {
		t.Fatalf("stored enrollment hash = %+v, want the shipped token's hash", stored.EnrollmentTokenHash)
	}
}

// TestSandboxManagerConcurrentEnsureCreatesOneNode proves the workspace
// advisory lock serialises first provision: racing callers converge on one node
// row and exactly one provision request, and later callers adopt the node the
// winner armed instead of minting a competing secret.
func TestSandboxManagerConcurrentEnsureCreatesOneNode(t *testing.T) {
	pool := auroraTestPool(t)
	q := db.New(pool)
	ws, runtimeID := newSandboxNodeWorkspace(t, q, pool)
	ctx := context.Background()

	fleet := &fakeProvisioner{}
	mgr := aurora.NewSandboxManager(q, pool, fleet, validSandboxImageDigest, nil)

	const workers = 8
	var wg sync.WaitGroup
	nodes := make([]db.AuroraSandboxNode, workers)
	errs := make([]error, workers)
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			nodes[i], errs[i] = mgr.Ensure(ctx, ws, runtimeID)
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("ensure %d: %v", i, err)
		}
	}
	for i := 1; i < workers; i++ {
		if nodes[i].ID != nodes[0].ID {
			t.Fatalf("ensure %d node id = %v, want %v", i, nodes[i].ID, nodes[0].ID)
		}
	}
	if fleet.callCount() != 1 {
		t.Fatalf("provisioner ensure calls = %d, want exactly 1", fleet.callCount())
	}
	var rows int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM aurora_sandbox_node WHERE workspace_id = $1", ws).Scan(&rows); err != nil {
		t.Fatalf("count sandbox nodes: %v", err)
	}
	if rows != 1 {
		t.Fatalf("sandbox nodes = %d, want exactly 1", rows)
	}
}

// TestSandboxManagerDoesNotAdoptUnconfirmedStartingNode pins the fail-closed
// adoption gate: a starting node with a live enrollment but no Fleet backend id
// is either mid-provision by another caller or abandoned, and neither is safe to
// adopt. Ensure must not return success and must not call the Fleet, so the
// handler's runtime-unavailable 503 leaves no reservation behind.
func TestSandboxManagerDoesNotAdoptUnconfirmedStartingNode(t *testing.T) {
	pool := auroraTestPool(t)
	q := db.New(pool)
	ws, runtimeID := newSandboxNodeWorkspace(t, q, pool)
	ctx := context.Background()

	// A row exactly as arm() leaves it after committing: starting, one live
	// single-use enrollment, and no backend id yet.
	if _, err := q.CreateAuroraSandboxNode(ctx, sandboxNodeParams(t, ws, runtimeID, uuid.NewString())); err != nil {
		t.Fatalf("create starting node: %v", err)
	}

	fleet := &fakeProvisioner{}
	mgr := aurora.NewSandboxManager(q, pool, fleet, validSandboxImageDigest, nil)
	if _, err := mgr.Ensure(ctx, ws, runtimeID); err == nil {
		t.Fatal("Ensure adopted a starting node with no Fleet backend id")
	}
	if fleet.callCount() != 0 {
		t.Fatalf("provisioner ensure calls = %d, want 0 for an unconfirmed starting node", fleet.callCount())
	}

	// The unconfirmed row is left for the arming caller or the fail-mark; the
	// adopter must not have re-armed it with a competing secret.
	stored := sandboxNodeByWorkspace(t, pool, ws)
	if stored.State != "starting" || !stored.EnrollmentTokenHash.Valid || stored.BackendNodeID.Valid {
		t.Fatalf("adopter mutated the unconfirmed node: %+v", stored)
	}
}

// TestSandboxManagerMarksNodeFailedWhenFleetRejects pins rollback: a failed
// provision leaves no live enrollment and records the node as failed with the
// provisioner's error.
func TestSandboxManagerMarksNodeFailedWhenFleetRejects(t *testing.T) {
	pool := auroraTestPool(t)
	q := db.New(pool)
	ws, runtimeID := newSandboxNodeWorkspace(t, q, pool)
	ctx := context.Background()

	fleet := &fakeProvisioner{err: errors.New("fleet exploded")}
	mgr := aurora.NewSandboxManager(q, pool, fleet, validSandboxImageDigest, nil)
	if _, err := mgr.Ensure(ctx, ws, runtimeID); err == nil {
		t.Fatal("ensure succeeded, want the provisioner error")
	}

	stored := sandboxNodeByWorkspace(t, pool, ws)
	if stored.State != "failed" {
		t.Fatalf("stored node state = %q, want failed", stored.State)
	}
	if !stored.FailureReason.Valid || !strings.Contains(stored.FailureReason.String, "fleet exploded") {
		t.Fatalf("stored failure reason = %+v, want the provisioner error", stored.FailureReason)
	}
	if stored.EnrollmentTokenHash.Valid || stored.EnrollmentExpiresAt.Valid || stored.EnrollmentConsumedAt.Valid {
		t.Fatalf("failed node kept enrollment fields: hash=%+v expires=%+v consumed=%+v",
			stored.EnrollmentTokenHash, stored.EnrollmentExpiresAt, stored.EnrollmentConsumedAt)
	}
	if stored.BackendNodeID.Valid {
		t.Fatalf("failed node kept backend node id %q", stored.BackendNodeID.String)
	}
}

// TestSandboxManagerReplacesIdentityWhenFleetRejectsIt pins the identity
// recovery: a Fleet conflict/forbidden is not retried against the same node
// forever. The manager deletes the rejected Fleet node and local row, then
// mints a fresh id and ensures it once.
func TestSandboxManagerReplacesIdentityWhenFleetRejectsIt(t *testing.T) {
	pool := auroraTestPool(t)
	q := db.New(pool)
	ws, runtimeID := newSandboxNodeWorkspace(t, q, pool)
	ctx := context.Background()

	fleet := &fakeProvisioner{firstErr: aurora.ErrNodeConflict, failFirst: 1}
	mgr := aurora.NewSandboxManager(q, pool, fleet, validSandboxImageDigest, nil)
	node, err := mgr.Ensure(ctx, ws, runtimeID)
	if err != nil {
		t.Fatalf("ensure with identity replacement: %v", err)
	}
	if fleet.callCount() != 2 {
		t.Fatalf("fleet ensure calls = %d, want 2 (reject + fresh id)", fleet.callCount())
	}
	if len(fleet.requests) != 2 {
		t.Fatalf("recorded requests = %d, want 2", len(fleet.requests))
	}
	if fleet.requests[0].NodeID == fleet.requests[1].NodeID {
		t.Fatalf("replacement reused node id %q", fleet.requests[0].NodeID)
	}
	if len(fleet.deletes) != 1 || fleet.deletes[0].nodeID != fleet.requests[0].NodeID {
		t.Fatalf("fleet deletes = %+v, want the rejected node %q", fleet.deletes, fleet.requests[0].NodeID)
	}
	stored := sandboxNodeByWorkspace(t, pool, ws)
	if util.UUIDToString(stored.ID) != fleet.requests[1].NodeID {
		t.Fatalf("stored node id = %s, want the replacement %s", util.UUIDToString(stored.ID), fleet.requests[1].NodeID)
	}
	if util.UUIDToString(node.ID) != fleet.requests[1].NodeID {
		t.Fatalf("returned node id = %s, want the replacement %s", util.UUIDToString(node.ID), fleet.requests[1].NodeID)
	}
}

// TestSandboxManagerDoesNotReturnEnrollmentToken pins the secret boundary: the
// raw enrollment secret is handed to the provisioner only and never leaks
// through the value Ensure returns, while the database still holds its hash.
func TestSandboxManagerDoesNotReturnEnrollmentToken(t *testing.T) {
	pool := auroraTestPool(t)
	q := db.New(pool)
	ws, runtimeID := newSandboxNodeWorkspace(t, q, pool)
	ctx := context.Background()

	fleet := &fakeProvisioner{}
	mgr := aurora.NewSandboxManager(q, pool, fleet, validSandboxImageDigest, nil)
	node, err := mgr.Ensure(ctx, ws, runtimeID)
	if err != nil {
		t.Fatalf("ensure new node: %v", err)
	}
	if node.EnrollmentTokenHash.Valid {
		t.Fatalf("returned node carries enrollment hash %q", node.EnrollmentTokenHash.String)
	}
	if node.EnrollmentExpiresAt.Valid {
		t.Fatalf("returned node carries enrollment expiry %v", node.EnrollmentExpiresAt.Time)
	}

	stored := sandboxNodeByWorkspace(t, pool, ws)
	if !stored.EnrollmentTokenHash.Valid || !stored.EnrollmentExpiresAt.Valid {
		t.Fatal("Ensure returned before issuing a live enrollment")
	}
}

// TestSandboxManagerEnsureResolvesOwner pins the binding owner ruling: the node
// owner the manager ships to the provisioner is the OwnerID of the
// aurora_managed runtime row read under the per-workspace lock, never a value
// derived from the caller or the node row.
func TestSandboxManagerEnsureResolvesOwner(t *testing.T) {
	pool := auroraTestPool(t)
	q := db.New(pool)
	ws, runtimeID := newSandboxNodeWorkspace(t, q, pool)
	ctx := context.Background()

	owner := sandboxNodeOwner(t, q, ws)
	if owner == "" || owner == util.UUIDToString(ws) {
		t.Fatalf("fixture owner = %q, want a real user distinct from the workspace", owner)
	}

	fleet := &fakeProvisioner{}
	mgr := aurora.NewSandboxManager(q, pool, fleet, validSandboxImageDigest, nil)
	if _, err := mgr.Ensure(ctx, ws, runtimeID); err != nil {
		t.Fatalf("ensure new node: %v", err)
	}
	if fleet.callCount() != 1 {
		t.Fatalf("provisioner ensure calls = %d, want 1", fleet.callCount())
	}
	if fleet.lastOwner != owner {
		t.Fatalf("provisioner owner = %q, want the managed runtime owner %q", fleet.lastOwner, owner)
	}
}

// TestSandboxManagerRejectsCrossOwnerRuntime pins cross-owner rejection: a
// runtime that belongs to another workspace is refused before any provisioner
// call, so one workspace can never assert another owner's node.
func TestSandboxManagerRejectsCrossOwnerRuntime(t *testing.T) {
	pool := auroraTestPool(t)
	q := db.New(pool)
	wsA, _ := newSandboxNodeWorkspace(t, q, pool)
	_, runtimeB := newSandboxNodeWorkspace(t, q, pool)
	ctx := context.Background()

	fleet := &fakeProvisioner{}
	mgr := aurora.NewSandboxManager(q, pool, fleet, validSandboxImageDigest, nil)
	if _, err := mgr.Ensure(ctx, wsA, runtimeB); err == nil {
		t.Fatal("ensure accepted another workspace's managed runtime")
	}
	if fleet.callCount() != 0 {
		t.Fatalf("provisioner ensure calls = %d, want 0 for a foreign runtime", fleet.callCount())
	}
}

// TestSandboxManagerReprovisionsAStaleOnlineNode pins the fix for #223. An
// online row whose last_active_at has gone quiet is not evidence that the node
// still exists: its container can be removed while the row keeps
// state = 'online', and adopting it skips the Fleet call entirely — so the
// generation is accepted and then sits in queued with no error and no timeout
// until the reaper's 15-minute idle sweep happens to retire the row.
func TestSandboxManagerReprovisionsAStaleOnlineNode(t *testing.T) {
	pool := auroraTestPool(t)
	q := db.New(pool)
	ws, runtimeID := newSandboxNodeWorkspace(t, q, pool)
	ctx := context.Background()

	params := sandboxNodeParams(t, ws, runtimeID, uuid.NewString())
	params.ImageDigest = validSandboxImageDigest
	created, err := q.CreateAuroraSandboxNode(ctx, params)
	if err != nil {
		t.Fatalf("create starting node: %v", err)
	}
	if _, err := q.ConsumeAuroraSandboxEnrollment(ctx, params.EnrollmentTokenHash); err != nil {
		t.Fatalf("consume enrollment to online: %v", err)
	}
	// A daemon that stopped heartbeating long enough ago to stop counting as
	// alive, with the reaper not yet having retired the row.
	if _, err := pool.Exec(ctx,
		`UPDATE aurora_sandbox_node SET last_active_at = now() - interval '10 minutes' WHERE id = $1`,
		created.ID,
	); err != nil {
		t.Fatalf("backdate last_active_at: %v", err)
	}

	fleet := &fakeProvisioner{node: aurora.FleetNode{ID: "backend-node-9", State: "launching", BackendID: "container-9"}}
	mgr := aurora.NewSandboxManager(q, pool, fleet, validSandboxImageDigest, nil)
	node, err := mgr.Ensure(ctx, ws, runtimeID)
	if err != nil {
		t.Fatalf("ensure stale node: %v", err)
	}
	if fleet.callCount() != 1 {
		t.Fatalf("provisioner ensure calls = %d, want 1 for a stale online node", fleet.callCount())
	}
	if node.State != "starting" {
		t.Fatalf("ensured node state = %q, want starting (re-armed for a fresh node)", node.State)
	}
	if node.BackendNodeID.String != "backend-node-9" {
		t.Fatalf("backend node id = %q, want the fleet's new node", node.BackendNodeID.String)
	}
}
