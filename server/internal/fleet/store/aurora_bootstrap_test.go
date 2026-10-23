package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	guuid "github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/fleet/model"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
)

// TestFleetAuroraBootstrapConfirm is the Task 3 fix regression. The Aurora create
// path carries no credential profile, so it has no Fleet node token to mint; it
// must still mark the operation bootstrapped so the existing SQL/lease fence in
// ConfirmBootstrap keeps working. The test drives the real store end to end:
// CreateAuroraIntent -> ClaimBootstrap -> MarkBootstrapMinted -> ConfirmBootstrap.
//
// It asserts the fence stays authoritative: an unmarked create fails closed with
// ErrConflict and mutates no node, the mark writes no credential row, and the
// marked create confirms the observed container.
func TestFleetAuroraBootstrapConfirm(t *testing.T) {
	s, f, ns := auroraStoreFixture(t, 2)
	ctx := context.Background()
	owner := uuid(t, f.UserID)
	nodeID := uuid(t, guuid.NewString())
	req := model.AuroraNodeRequest{
		WorkspaceID:     uuid(t, f.WorkspaceID),
		RuntimeID:       uuid(t, guuid.NewString()),
		DaemonID:        guuid.NewString(),
		ImageDigest:     "aurora-test-image",
		Name:            "aurora-bootstrap",
		Spec:            "sandbox",
		IdempotencyKey:  "aurora-bootstrap-key",
		EnrollmentToken: "mse_" + strings.Repeat("a", 40),
	}
	node, op, replayed, err := s.CreateAuroraIntent(ctx, owner, nodeID, req)
	if err != nil || replayed {
		t.Fatalf("create node=%+v replayed=%v err=%v", node, replayed, err)
	}
	ref := model.OperationRef{Namespace: ns, NodeID: node.ID, OperationID: op.ID, Generation: op.Generation, Action: model.Create}
	claim, err := s.ClaimBootstrap(ctx, owner, ref)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	obs := model.Observation{ContainerID: "aurora-cid", Status: "running", DaemonID: node.DaemonID, StartEpoch: time.Now().UTC().Format(time.RFC3339Nano), ObservedAt: time.Now().UTC(), Ready: true, ReportStatsKnown: true}

	// An unmarked create must fail closed: the fence stays authoritative and the
	// node is neither adopted nor revoked.
	if err = s.ConfirmBootstrap(ctx, claim, obs); !errors.Is(err, model.ErrConflict) {
		t.Fatalf("unmarked Confirm = %v, want %v", err, model.ErrConflict)
	}
	if got, err := s.GetAuroraNode(ctx, owner, nodeID); err != nil || got.ContainerID != "" || got.Revoked {
		t.Fatalf("unmarked node mutated: %+v err=%v", got, err)
	}

	// The Aurora branch marks the operation bootstrapped without a node token.
	if err = s.MarkBootstrapMinted(ctx, claim); err != nil {
		t.Fatalf("mark: %v", err)
	}
	if n := f.Count(t, "SELECT count(*) FROM fleet_node_credentials WHERE namespace=$1 AND owner_id=$2 AND node_id=$3", ns, f.UserID, util.UUIDToString(nodeID)); n != 0 {
		t.Fatalf("mark wrote %d credential rows", n)
	}
	if err = s.MarkBootstrapMinted(ctx, claim); !errors.Is(err, model.ErrConflict) {
		t.Fatalf("second mark = %v, want %v", err, model.ErrConflict)
	}

	// The mark satisfies the minted precondition; the fence still checks the
	// container/claim and the observed result is persisted.
	if err = s.ConfirmBootstrap(ctx, claim, obs); err != nil {
		t.Fatalf("marked Confirm: %v", err)
	}
	got, err := s.GetAuroraNode(ctx, owner, nodeID)
	if err != nil || got.ContainerID != obs.ContainerID || !got.Ready || got.Revoked {
		t.Fatalf("confirmed node = %+v err=%v", got, err)
	}
	current, err := s.GetOperation(ctx, owner, op.ID)
	if err != nil || !current.BootstrapMinted {
		t.Fatalf("confirmed operation = %+v err=%v", current, err)
	}
}

// auroraRebootstrapRequest is the one-node, one-key Aurora intent the re-arm
// regressions replay; only the enrollment secret differs on replay.
func auroraRebootstrapRequest(t *testing.T, f *testutil.Fixture, key string) model.AuroraNodeRequest {
	t.Helper()
	return model.AuroraNodeRequest{
		WorkspaceID:     uuid(t, f.WorkspaceID),
		RuntimeID:       uuid(t, guuid.NewString()),
		DaemonID:        guuid.NewString(),
		ImageDigest:     "aurora-test-image",
		Name:            "aurora-rebootstrap",
		Spec:            "sandbox",
		IdempotencyKey:  key,
		EnrollmentToken: "mse_" + strings.Repeat("a", 40),
	}
}

// requireAuroraRebootstrapReset asserts the replay reset the same node identity
// for a fresh bootstrap: durable identity is preserved, state is fresh, the
// generation advanced, and the original create operation is claimable again.
func requireAuroraRebootstrapReset(t *testing.T, f *testutil.Fixture, ns string, before model.Node, beforeOp model.Operation, after model.Node, afterOp model.Operation) {
	t.Helper()
	if after.ID != before.ID || after.OwnerID != before.OwnerID || after.DaemonID != before.DaemonID || after.Image != before.Image || after.DataVolume != before.DataVolume || after.SecretsVolume != before.SecretsVolume {
		t.Fatalf("reset changed durable identity: before=%+v after=%+v", before, after)
	}
	if after.Revoked || after.Maintenance || after.Desired != "running" || after.Status != "creating" || after.ContainerID != "" || after.Ready {
		t.Fatalf("reset did not restore fresh-bootstrap node state: %+v", after)
	}
	if after.Generation != before.Generation+1 {
		t.Fatalf("reset generation = %d, want %d", after.Generation, before.Generation+1)
	}
	if afterOp.ID != beforeOp.ID || afterOp.Generation != after.Generation || afterOp.Phase != "queued" || afterOp.BootstrapMinted || afterOp.NonRetryable || !afterOp.BootstrapClaimedAt.IsZero() || afterOp.Attempts != 0 {
		t.Fatalf("reset did not make the create operation claimable: %+v", afterOp)
	}
	if n := f.Count(t, "SELECT count(*) FROM fleet_nodes WHERE namespace=$1 AND owner_id=$2 AND id=$3", ns, f.UserID, util.UUIDToString(before.ID)); n != 1 {
		t.Fatalf("reset created a second node row: %d", n)
	}
	if n := f.Count(t, "SELECT count(*) FROM fleet_node_operations WHERE namespace=$1 AND owner_id=$2 AND idempotency_key=$3", ns, f.UserID, beforeOp.IdempotencyKey); n != 1 {
		t.Fatalf("reset created a second create operation: %d", n)
	}
}

// claimAuroraRebootstrap proves a re-armed create intent is claimable and
// minted at its new generation. It deliberately stops before ConfirmBootstrap.
func claimAuroraRebootstrap(t *testing.T, s *Store, ns string, owner, nodeID pgtype.UUID, node model.Node, op model.Operation) {
	t.Helper()
	ctx := context.Background()
	ref := model.OperationRef{Namespace: ns, NodeID: node.ID, OperationID: op.ID, Generation: op.Generation, Action: model.Create}
	claim, err := s.ClaimBootstrap(ctx, owner, ref)
	if err != nil {
		t.Fatalf("re-bootstrap claim: %v", err)
	}
	if err = s.MarkBootstrapMinted(ctx, claim); err != nil {
		t.Fatalf("re-bootstrap mark: %v", err)
	}
}

// confirmAuroraRebootstrap drives the reset generation through the real claim,
// mark, and confirm fence, proving the node is a live bootstrap again.
func confirmAuroraRebootstrap(t *testing.T, s *Store, ns string, owner, nodeID pgtype.UUID, node model.Node, op model.Operation) {
	t.Helper()
	ctx := context.Background()
	ref := model.OperationRef{Namespace: ns, NodeID: node.ID, OperationID: op.ID, Generation: op.Generation, Action: model.Create}
	claim, err := s.ClaimBootstrap(ctx, owner, ref)
	if err != nil {
		t.Fatalf("re-bootstrap claim: %v", err)
	}
	if err = s.MarkBootstrapMinted(ctx, claim); err != nil {
		t.Fatalf("re-bootstrap mark: %v", err)
	}
	obs := model.Observation{ContainerID: "aurora-rearm-cid", Status: "running", DaemonID: node.DaemonID, StartEpoch: time.Now().UTC().Format(time.RFC3339Nano), ObservedAt: time.Now().UTC(), Ready: true, ReportStatsKnown: true}
	if err = s.ConfirmBootstrap(ctx, claim, obs); err != nil {
		t.Fatalf("re-bootstrap confirm: %v", err)
	}
	got, err := s.GetAuroraNode(ctx, owner, nodeID)
	if err != nil || got.ContainerID != obs.ContainerID || !got.Ready || got.Revoked {
		t.Fatalf("re-bootstrapped node = %+v err=%v", got, err)
	}
}

// TestFleetAuroraReplayResetsFailedBootstrap covers the failure re-arm: after the
// live bootstrap attempt failed and revoked the node (non_retryable create), the
// same key and node UUID with a fresh secret must reset the same identity and let
// the pipeline run again.
func TestFleetAuroraReplayResetsFailedBootstrap(t *testing.T) {
	s, f, ns := auroraStoreFixture(t, 2)
	ctx := context.Background()
	owner := uuid(t, f.UserID)
	nodeID := uuid(t, guuid.NewString())
	req := auroraRebootstrapRequest(t, f, "aurora-rebootstrap-failed")
	node, op, replayed, err := s.CreateAuroraIntent(ctx, owner, nodeID, req)
	if err != nil || replayed {
		t.Fatalf("create node=%+v replayed=%v err=%v", node, replayed, err)
	}
	ref := model.OperationRef{Namespace: ns, NodeID: node.ID, OperationID: op.ID, Generation: op.Generation, Action: model.Create}
	claim, err := s.ClaimBootstrap(ctx, owner, ref)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err = s.MarkBootstrapMinted(ctx, claim); err != nil {
		t.Fatalf("mark: %v", err)
	}
	if err = s.FailBootstrap(ctx, claim, "unavailable"); err != nil {
		t.Fatalf("fail: %v", err)
	}
	dead, err := s.GetAuroraNode(ctx, owner, nodeID)
	if err != nil || !dead.Revoked {
		t.Fatalf("dead node = %+v err=%v", dead, err)
	}
	deadOp, err := s.GetOperation(ctx, owner, op.ID)
	if err != nil || !deadOp.NonRetryable {
		t.Fatalf("dead operation = %+v err=%v", deadOp, err)
	}

	rotated := req
	rotated.EnrollmentToken = "mse_" + strings.Repeat("b", 40)
	after, afterOp, replayed, err := s.CreateAuroraIntent(ctx, owner, nodeID, rotated)
	if err != nil || !replayed {
		t.Fatalf("replay after=%+v replayed=%v err=%v", after, replayed, err)
	}
	requireAuroraRebootstrapReset(t, f, ns, dead, deadOp, after, afterOp)
	confirmAuroraRebootstrap(t, s, ns, owner, nodeID, after, afterOp)
}

// TestFleetAuroraReplayResetsReapedNode covers the reaper re-arm: Fleet tombstoned
// the node (revoked, terminated) while the create operation stayed retryable, and
// the next PUT must reset that same node instead of returning dead state.
func TestFleetAuroraReplayResetsReapedNode(t *testing.T) {
	s, f, ns := auroraStoreFixture(t, 2)
	ctx := context.Background()
	owner := uuid(t, f.UserID)
	nodeID := uuid(t, guuid.NewString())
	req := auroraRebootstrapRequest(t, f, "aurora-rebootstrap-reaped")
	node, op, replayed, err := s.CreateAuroraIntent(ctx, owner, nodeID, req)
	if err != nil || replayed {
		t.Fatalf("create node=%+v replayed=%v err=%v", node, replayed, err)
	}
	// The Aurora reaper issues a Fleet delete, and Fleet completes the tombstone.
	if err = s.DeleteAuroraIntent(ctx, owner, nodeID); err != nil {
		t.Fatalf("delete intent: %v", err)
	}
	var deleteOpText string
	f.QueryRow(t, "SELECT id::text FROM fleet_node_operations WHERE namespace=$1 AND owner_id=$2 AND node_id=$3 AND action='delete'", ns, f.UserID, util.UUIDToString(nodeID)).Scan(&deleteOpText)
	if err = s.FinishDelete(ctx, uuid(t, deleteOpText), node.Generation+1); err != nil {
		t.Fatalf("finish delete: %v", err)
	}
	dead, err := s.GetAuroraNode(ctx, owner, nodeID)
	if err != nil || !dead.Revoked || dead.Desired != "terminated" || dead.Status != "terminated" {
		t.Fatalf("reaped node = %+v err=%v", dead, err)
	}
	deadOp, err := s.GetOperation(ctx, owner, op.ID)
	if err != nil || deadOp.NonRetryable {
		t.Fatalf("reaped create operation = %+v err=%v", deadOp, err)
	}

	rotated := req
	rotated.EnrollmentToken = "mse_" + strings.Repeat("b", 40)
	after, afterOp, replayed, err := s.CreateAuroraIntent(ctx, owner, nodeID, rotated)
	if err != nil || !replayed {
		t.Fatalf("replay after=%+v replayed=%v err=%v", after, replayed, err)
	}
	requireAuroraRebootstrapReset(t, f, ns, dead, deadOp, after, afterOp)
	confirmAuroraRebootstrap(t, s, ns, owner, nodeID, after, afterOp)
}

// auroraStoreWithImage builds a second store over the same test database and
// namespace with a different administrator-approved image, modelling a redeploy.
func auroraStoreWithImage(s *Store, ns, image string) *Store {
	return New(s.pool, ns, WithProvisioningConfig(model.Config{
		Namespace: ns,
		Image:     image,
		Specs:     map[string]model.Spec{"sandbox": {CPUs: 2, MemoryBytes: 4294967296, Pids: 256, MaxRuns: 1}},
		Aurora:    &model.AuroraConfig{ServerURL: "http://api.test"},
	}), WithMaxNodes(2))
}

// TestFleetAuroraImageChangeReArmsSameIdentity is the Task 27 regression for the
// fingerprint-change half of the deadlock. A redeploy changes only the
// deployment-scoped image digest; the Aurora owner replays the same stable
// idempotency key, so Fleet must re-arm the one create intent instead of
// returning ErrConflict forever. Durable identity (node/daemon UUIDs and the
// data/secrets volume names) is preserved and no second node or create
// operation appears.
func TestFleetAuroraImageChangeReArmsSameIdentity(t *testing.T) {
	s, f, ns := auroraStoreFixture(t, 2)
	ctx := context.Background()
	owner := uuid(t, f.UserID)
	nodeID := uuid(t, guuid.NewString())
	req := auroraRebootstrapRequest(t, f, "aurora-image-change")
	before, beforeOp, replayed, err := s.CreateAuroraIntent(ctx, owner, nodeID, req)
	if err != nil || replayed {
		t.Fatalf("create node=%+v replayed=%v err=%v", before, replayed, err)
	}

	next := auroraStoreWithImage(s, ns, "aurora-test-image-v2")
	req2 := req
	req2.ImageDigest = "aurora-test-image-v2"
	after, afterOp, replayed, err := next.CreateAuroraIntent(ctx, owner, nodeID, req2)
	if err != nil || !replayed {
		t.Fatalf("image-change replay after=%+v replayed=%v err=%v", after, replayed, err)
	}
	if after.ID != before.ID || after.OwnerID != before.OwnerID || after.DaemonID != before.DaemonID || after.DataVolume != before.DataVolume || after.SecretsVolume != before.SecretsVolume {
		t.Fatalf("image change altered durable identity: before=%+v after=%+v", before, after)
	}
	if after.Image != req2.ImageDigest || after.Generation != before.Generation+1 || after.Revoked || after.Maintenance || after.Desired != "running" || after.ContainerID != "" {
		t.Fatalf("image change did not re-arm the same identity: %+v", after)
	}
	if afterOp.ID != beforeOp.ID || afterOp.Generation != after.Generation || afterOp.Phase != "queued" || afterOp.RequestHash == beforeOp.RequestHash {
		t.Fatalf("image change did not rewind the one create intent: %+v", afterOp)
	}
	if n := f.Count(t, "SELECT count(*) FROM fleet_nodes WHERE namespace=$1 AND owner_id=$2 AND id=$3", ns, f.UserID, util.UUIDToString(nodeID)); n != 1 {
		t.Fatalf("image change created a second node row: %d", n)
	}
	if n := f.Count(t, "SELECT count(*) FROM fleet_node_operations WHERE namespace=$1 AND owner_id=$2 AND idempotency_key=$3", ns, f.UserID, req.IdempotencyKey); n != 1 {
		t.Fatalf("image change created a second create operation: %d", n)
	}

	// A different durable identity under the same key is still a hard conflict
	// even when the request also changes the approved image.
	foreign := req2
	foreign.RuntimeID = uuid(t, guuid.NewString())
	if _, _, _, err := next.CreateAuroraIntent(ctx, owner, nodeID, foreign); !errors.Is(err, model.ErrConflict) {
		t.Fatalf("foreign identity reuse err=%v, want conflict", err)
	}

	claimAuroraRebootstrap(t, next, ns, owner, nodeID, after, afterOp)
}

// TestFleetAuroraImageChangeRetiresQueuedDelete is the Task 27 regression for the
// admission half of the second deadlock: an unfinished destroy from an earlier
// teardown left the identity un-re-armable. A later deployment image change must
// retire that stale non-create intent deterministically, so the same create
// intent can bootstrap again and the retired delete can never fire.
func TestFleetAuroraImageChangeRetiresQueuedDelete(t *testing.T) {
	s, f, ns := auroraStoreFixture(t, 2)
	ctx := context.Background()
	owner := uuid(t, f.UserID)
	nodeID := uuid(t, guuid.NewString())
	req := auroraRebootstrapRequest(t, f, "aurora-queued-delete")
	node, op, _, err := s.CreateAuroraIntent(ctx, owner, nodeID, req)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	ref := model.OperationRef{Namespace: ns, NodeID: node.ID, OperationID: op.ID, Generation: op.Generation, Action: model.Create}
	claim, err := s.ClaimBootstrap(ctx, owner, ref)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err = s.MarkBootstrapMinted(ctx, claim); err != nil {
		t.Fatalf("mark: %v", err)
	}
	if err = s.FailBootstrap(ctx, claim, "unavailable"); err != nil {
		t.Fatalf("fail: %v", err)
	}
	if err = s.DeleteAuroraIntent(ctx, owner, nodeID); err != nil {
		t.Fatalf("delete intent: %v", err)
	}
	var deleteOpText string
	f.QueryRow(t, "SELECT id::text FROM fleet_node_operations WHERE namespace=$1 AND owner_id=$2 AND node_id=$3 AND action='delete'", ns, f.UserID, util.UUIDToString(nodeID)).Scan(&deleteOpText)

	next := auroraStoreWithImage(s, ns, "aurora-test-image-v2")
	req2 := req
	req2.ImageDigest = "aurora-test-image-v2"
	after, afterOp, replayed, err := next.CreateAuroraIntent(ctx, owner, nodeID, req2)
	if err != nil || !replayed {
		t.Fatalf("replay with queued delete after=%+v replayed=%v err=%v", after, replayed, err)
	}
	if after.Revoked || after.Maintenance || after.Desired != "running" || after.Image != req2.ImageDigest {
		t.Fatalf("queued delete not superseded: %+v", after)
	}
	stale, err := next.GetOperation(ctx, owner, uuid(t, deleteOpText))
	if err != nil || stale.Phase != "failed" || !stale.NonRetryable {
		t.Fatalf("stale delete not retired: %+v err=%v", stale, err)
	}
	ops, err := next.ListRecoverable(ctx, pgtype.UUID{})
	if err != nil {
		t.Fatalf("list recoverable: %v", err)
	}
	for _, o := range ops {
		if util.UUIDToString(o.NodeID) == util.UUIDToString(nodeID) && o.Action == model.Delete {
			t.Fatalf("retired delete still recoverable: %+v", o)
		}
	}
	if n := f.Count(t, "SELECT count(*) FROM fleet_node_operations WHERE namespace=$1 AND owner_id=$2 AND idempotency_key=$3", ns, f.UserID, req.IdempotencyKey); n != 1 {
		t.Fatalf("replay created a second create operation: %d", n)
	}
	claimAuroraRebootstrap(t, next, ns, owner, nodeID, after, afterOp)
}
