package store

import (
	"context"
	"strings"
	"testing"

	guuid "github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
)

// TestFleetAuroraRepeatedTeardownKeepsItsOwnIntent is the #259 regression. The
// destroy intent key used to be fixed per node, while fleet_node_operations is
// UNIQUE on (namespace, owner_id, idempotency_key) and that row is never
// reused. A workspace re-arms the same node id across its lifetime cycles, so
// the second teardown collided with the first row: the insert raised a raw
// unique violation, the Fleet answered 503, and the reaper failed on every
// sweep instead of reaping the node. Each teardown now carries the control
// generation it advances.
func TestFleetAuroraRepeatedTeardownKeepsItsOwnIntent(t *testing.T) {
	s, f, ns := auroraStoreFixture(t, 2)
	ctx := context.Background()
	owner := uuid(t, f.UserID)
	nodeID := uuid(t, guuid.NewString())
	req := auroraRebootstrapRequest(t, f, "aurora-repeated-teardown")

	node, op, replayed, err := s.CreateAuroraIntent(ctx, owner, nodeID, req)
	if err != nil || replayed {
		t.Fatalf("create node=%+v replayed=%v err=%v", node, replayed, err)
	}

	// The first teardown always worked; the row it leaves behind is what used
	// to poison every later one.
	if err = s.DeleteAuroraIntent(ctx, owner, nodeID); err != nil {
		t.Fatalf("first delete intent: %v", err)
	}
	firstDelete := latestDeleteOperation(t, f, ns, nodeID)
	if err = s.FinishDelete(ctx, uuid(t, firstDelete), node.Generation+1); err != nil {
		t.Fatalf("finish first delete: %v", err)
	}
	dead, err := s.GetAuroraNode(ctx, owner, nodeID)
	if err != nil || !dead.Revoked || dead.Desired != "terminated" {
		t.Fatalf("first teardown = %+v err=%v", dead, err)
	}
	deadOp, err := s.GetOperation(ctx, owner, op.ID)
	if err != nil {
		t.Fatalf("first create operation: %v", err)
	}

	// Re-arm the same node identity, exactly as the next lifetime cycle does.
	rotated := req
	rotated.EnrollmentToken = "mse_" + strings.Repeat("b", 40)
	after, afterOp, replayed, err := s.CreateAuroraIntent(ctx, owner, nodeID, rotated)
	if err != nil || !replayed {
		t.Fatalf("re-arm after=%+v replayed=%v err=%v", after, replayed, err)
	}
	requireAuroraRebootstrapReset(t, f, ns, dead, deadOp, after, afterOp)
	confirmAuroraRebootstrap(t, s, ns, owner, nodeID, after, afterOp)

	// The regression: the first delete row is still in the table, so a
	// node-fixed key collides here and the whole teardown fails.
	if err = s.DeleteAuroraIntent(ctx, owner, nodeID); err != nil {
		t.Fatalf("second delete intent: %v", err)
	}
	secondDelete := latestDeleteOperation(t, f, ns, nodeID)
	if secondDelete == firstDelete {
		t.Fatalf("second teardown reused the first delete operation %s", firstDelete)
	}
	if err = s.FinishDelete(ctx, uuid(t, secondDelete), after.Generation+1); err != nil {
		t.Fatalf("finish second delete: %v", err)
	}
	reaped, err := s.GetAuroraNode(ctx, owner, nodeID)
	if err != nil || !reaped.Revoked || reaped.Desired != "terminated" || reaped.Status != "terminated" {
		t.Fatalf("second teardown = %+v err=%v", reaped, err)
	}
	deleteOps := "SELECT count(*) FROM fleet_node_operations WHERE namespace=$1 AND owner_id=$2 AND node_id=$3 AND action='delete'"
	if n := f.Count(t, deleteOps, ns, f.UserID, util.UUIDToString(nodeID)); n != 2 {
		t.Fatalf("delete operations = %d, want 2", n)
	}
	distinctKeys := "SELECT count(DISTINCT idempotency_key) FROM fleet_node_operations WHERE namespace=$1 AND owner_id=$2 AND node_id=$3 AND action='delete'"
	if n := f.Count(t, distinctKeys, ns, f.UserID, util.UUIDToString(nodeID)); n != 2 {
		t.Fatalf("distinct delete keys = %d, want 2", n)
	}
}

// latestDeleteOperation returns the newest destroy intent recorded for a node.
func latestDeleteOperation(t *testing.T, f *testutil.Fixture, ns string, nodeID pgtype.UUID) string {
	t.Helper()
	query := "SELECT id::text FROM fleet_node_operations WHERE namespace=$1 AND owner_id=$2 AND node_id=$3 AND action='delete' ORDER BY generation DESC LIMIT 1"
	var id string
	f.QueryRow(t, query, ns, f.UserID, util.UUIDToString(nodeID)).Scan(&id)
	if id == "" {
		t.Fatal("no delete operation recorded")
	}
	return id
}
