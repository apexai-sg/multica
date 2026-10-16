package store

import (
	"context"
	"errors"
	"testing"

	guuid "github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/fleet/model"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
)

// TestFleetAuroraIntentAdoptsIdentityFromAnEarlierNamespace is the regression for
// the identity-scope mismatch: the node and daemon UUIDs are Aurora-owned and
// globally unique (fleet_nodes_id_idx, fleet_nodes_daemon_idx), while the replay
// lookup is scoped to (namespace, owner_id). A row left by an earlier Fleet
// namespace was invisible to that lookup yet still blocked the insert, so the arm
// answered 503 forever and only manual SQL unblocked the workspace. The row must
// instead be adopted into the caller's namespace.
func TestFleetAuroraIntentAdoptsIdentityFromAnEarlierNamespace(t *testing.T) {
	s, f, ns := auroraStoreFixture(t, 2)
	ctx := context.Background()
	owner := uuid(t, f.UserID)
	nodeID := uuid(t, guuid.NewString())
	req := auroraRebootstrapRequest(t, f, "aurora-identity-key")
	seedSurvivingNode(t, f, "stale-"+ns, f.UserID, nodeID, req)

	node, _, replayed, err := s.CreateAuroraIntent(ctx, owner, nodeID, req)
	if err != nil {
		t.Fatalf("adopt identity from an earlier namespace: %v", err)
	}
	if replayed || node.ID != nodeID || node.OwnerID != owner || node.Namespace != ns {
		t.Fatalf("adopted node=%+v replayed=%v", node, replayed)
	}
	// Adoption moves the surviving row; it never adds a second one for the id.
	if n := f.Count(t, "SELECT count(*) FROM fleet_nodes WHERE id=$1", util.UUIDToString(nodeID)); n != 1 {
		t.Fatalf("rows for the adopted identity = %d, want 1", n)
	}
	var got string
	f.QueryRow(t, "SELECT namespace FROM fleet_nodes WHERE id=$1", util.UUIDToString(nodeID)).Scan(&got)
	if got != ns {
		t.Fatalf("adopted namespace=%q want %q", got, ns)
	}
}

// TestFleetAuroraIntentRejectsIdentityOwnedByAnotherOwner pins the refusal: a
// surviving row is adoptable only for the same owner, and the refusal is a typed
// conflict rather than the unique violation that used to leak as a 503.
func TestFleetAuroraIntentRejectsIdentityOwnedByAnotherOwner(t *testing.T) {
	s, f, ns := auroraStoreFixture(t, 2)
	ctx := context.Background()
	owner := uuid(t, f.UserID)
	stranger := f.User(t, "Stranger", "stranger-"+f.UserID+"@example.test")
	nodeID := uuid(t, guuid.NewString())
	req := auroraRebootstrapRequest(t, f, "aurora-owner-key")
	seedSurvivingNode(t, f, ns, stranger, nodeID, req)

	if _, _, _, err := s.CreateAuroraIntent(ctx, owner, nodeID, req); !errors.Is(err, model.ErrConflict) {
		t.Fatalf("foreign owner identity err=%v want ErrConflict", err)
	}
}

// TestFleetAuroraIntentRejectsReusedDaemonIdentity covers the second global
// unique index: an unused node id must not be admitted under a daemon another
// node already owns.
func TestFleetAuroraIntentRejectsReusedDaemonIdentity(t *testing.T) {
	s, f, ns := auroraStoreFixture(t, 2)
	ctx := context.Background()
	owner := uuid(t, f.UserID)
	nodeID := uuid(t, guuid.NewString())
	req := auroraRebootstrapRequest(t, f, "aurora-daemon-key")
	seedSurvivingNode(t, f, ns, f.UserID, uuid(t, guuid.NewString()), req)

	if _, _, _, err := s.CreateAuroraIntent(ctx, owner, nodeID, req); !errors.Is(err, model.ErrConflict) {
		t.Fatalf("reused daemon identity err=%v want ErrConflict", err)
	}
}

// seedSurvivingNode writes the row an earlier Fleet namespace (or another owner)
// left behind: the caller-supplied ids and durable dimensions, its own namespace.
// Fixture.Insert registers the matching delete itself, so adoption moving the row
// into another namespace needs no caller-side teardown.
func seedSurvivingNode(t *testing.T, f *testutil.Fixture, namespace, ownerID string, nodeID pgtype.UUID, req model.AuroraNodeRequest) {
	t.Helper()
	text := util.UUIDToString(nodeID)
	f.FleetNode(t, namespace, testutil.Cols{
		"id":             text,
		"owner_id":       ownerID,
		"daemon_id":      req.DaemonID,
		"workspace_id":   util.UUIDToString(req.WorkspaceID),
		"runtime_id":     util.UUIDToString(req.RuntimeID),
		"name":           req.Name,
		"spec":           req.Spec,
		"image":          req.ImageDigest,
		"data_volume":    "multica-fleet-" + text + "-data",
		"secrets_volume": "multica-fleet-" + text + "-secrets",
	})
}
