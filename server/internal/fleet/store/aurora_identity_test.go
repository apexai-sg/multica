package store

import (
	"context"
	"errors"
	"strings"
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
// namespace was invisible to that lookup yet still blocked the insert, so the
// arm answered 503 forever and only manual SQL could unblock the workspace.
// The row must instead be adopted into the caller's namespace.
func TestFleetAuroraIntentAdoptsIdentityFromAnEarlierNamespace(t *testing.T) {
	s, f, ns := auroraStoreFixture(t, 2)
	ctx := context.Background()
	owner := uuid(t, f.UserID)
	nodeID := uuid(t, guuid.NewString())
	req := model.AuroraNodeRequest{
		WorkspaceID:     uuid(t, f.WorkspaceID),
		RuntimeID:       uuid(t, guuid.NewString()),
		DaemonID:        guuid.NewString(),
		ImageDigest:     "aurora-test-image",
		Name:            "aurora-node",
		Spec:            "sandbox",
		IdempotencyKey:  "aurora-identity-key",
		EnrollmentToken: "mse_" + strings.Repeat("a", 40),
	}
	stale := "stale-" + ns
	f.Cleanup(t, "DELETE FROM fleet_nodes WHERE id=$1", util.UUIDToString(nodeID))
	seedFleetNode(t, f, stale, f.UserID, nodeID, req)

	node, _, replayed, err := s.CreateAuroraIntent(ctx, owner, nodeID, req)
	if err != nil {
		t.Fatalf("adopt identity from an earlier namespace: %v", err)
	}
	if replayed || node.ID != nodeID || node.OwnerID != owner || node.Namespace != ns {
		t.Fatalf("adopted node=%+v replayed=%v", node, replayed)
	}
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
// foreign namespace is adoptable only for the same owner, and the refusal is a
// typed conflict rather than the unique violation that used to leak as a 503.
func TestFleetAuroraIntentRejectsIdentityOwnedByAnotherOwner(t *testing.T) {
	s, f, ns := auroraStoreFixture(t, 2)
	ctx := context.Background()
	owner := uuid(t, f.UserID)
	stranger := f.User(t, "Stranger", "stranger-"+f.UserID+"@example.test")
	nodeID := uuid(t, guuid.NewString())
	req := model.AuroraNodeRequest{
		WorkspaceID:     uuid(t, f.WorkspaceID),
		RuntimeID:       uuid(t, guuid.NewString()),
		DaemonID:        guuid.NewString(),
		ImageDigest:     "aurora-test-image",
		Name:            "aurora-node",
		Spec:            "sandbox",
		IdempotencyKey:  "aurora-owner-key",
		EnrollmentToken: "mse_" + strings.Repeat("a", 40),
	}
	f.Cleanup(t, "DELETE FROM fleet_nodes WHERE id=$1", util.UUIDToString(nodeID))
	seedFleetNode(t, f, ns, stranger, nodeID, req)

	if _, _, _, err := s.CreateAuroraIntent(ctx, owner, nodeID, req); !errors.Is(err, model.ErrConflict) {
		t.Fatalf("foreign owner identity err=%v want ErrConflict", err)
	}
}

// TestFleetAuroraIntentRejectsReusedDaemonIdentity covers the second global
// unique index: a node id nobody has used yet must not slip past an existing row
// that already owns the daemon UUID.
func TestFleetAuroraIntentRejectsReusedDaemonIdentity(t *testing.T) {
	s, f, ns := auroraStoreFixture(t, 2)
	ctx := context.Background()
	owner := uuid(t, f.UserID)
	otherNode := uuid(t, guuid.NewString())
	nodeID := uuid(t, guuid.NewString())
	req := model.AuroraNodeRequest{
		WorkspaceID:     uuid(t, f.WorkspaceID),
		RuntimeID:       uuid(t, guuid.NewString()),
		DaemonID:        guuid.NewString(),
		ImageDigest:     "aurora-test-image",
		Name:            "aurora-node",
		Spec:            "sandbox",
		IdempotencyKey:  "aurora-daemon-key",
		EnrollmentToken: "mse_" + strings.Repeat("a", 40),
	}
	f.Cleanup(t, "DELETE FROM fleet_nodes WHERE id=$1", util.UUIDToString(otherNode))
	f.Cleanup(t, "DELETE FROM fleet_nodes WHERE id=$1", util.UUIDToString(nodeID))
	seedFleetNode(t, f, ns, f.UserID, otherNode, req)

	if _, _, _, err := s.CreateAuroraIntent(ctx, owner, nodeID, req); !errors.Is(err, model.ErrConflict) {
		t.Fatalf("reused daemon identity err=%v want ErrConflict", err)
	}
}

// seedFleetNode writes a node row directly, standing in for one left behind by an
// earlier Fleet namespace or owner.
func seedFleetNode(t *testing.T, f *testutil.Fixture, namespace, ownerID string, nodeID pgtype.UUID, req model.AuroraNodeRequest) {
	t.Helper()
	f.Exec(t,
		"INSERT INTO fleet_nodes (id, daemon_id, namespace, owner_id, workspace_id, runtime_id, name, spec, image, spec_config, data_volume, secrets_volume, status) "+
			"VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'{}',$10,$11,'running')",
		util.UUIDToString(nodeID), req.DaemonID, namespace, ownerID,
		util.UUIDToString(req.WorkspaceID), util.UUIDToString(req.RuntimeID), req.Name, req.Spec, req.ImageDigest,
		"multica-fleet-"+util.UUIDToString(nodeID)+"-data",
		"multica-fleet-"+util.UUIDToString(nodeID)+"-secrets",
	)
}
