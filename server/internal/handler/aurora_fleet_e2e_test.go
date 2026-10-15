package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/aurora"
	"github.com/multica-ai/multica/server/internal/cloudruntime"
	"github.com/multica-ai/multica/server/internal/fleet"
	"github.com/multica-ai/multica/server/internal/fleet/model"
	"github.com/multica-ai/multica/server/internal/fleet/store"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TestAuroraFleetProvisionToClaim is the Fleet acceptance test: the server's
// real SandboxManager provisions a workspace node through the Task 3 Fleet
// route (over the production cloudruntime client and an in-process Fleet
// service, so no Docker is needed), the sandbox daemon exchanges the
// enrollment secret the manager shipped for a daemon credential, and it claims
// a queued task as the identity it just enrolled. The daemon itself is
// simulated with the server's own HTTP endpoints - the exact calls a real
// daemon inside the node would make.
func TestAuroraFleetProvisionToClaim(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	withSandboxEnrollment(t, newSandboxEnrollmentService())
	ctx := context.Background()

	// Clear any managed runtime a previous handler test left in the shared
	// workspace: the one-managed-runtime-per-workspace index makes the seed
	// below collide otherwise.
	dbfx.Exec(t, `DELETE FROM aurora_sandbox_node WHERE workspace_id = $1`, testWorkspaceID)
	dbfx.Exec(t, `DELETE FROM daemon_token WHERE workspace_id = $1`, testWorkspaceID)
	dbfx.Exec(t, `DELETE FROM agent_runtime WHERE workspace_id = $1 AND provider = 'aurora_managed'`, testWorkspaceID)

	// Seed the workspace's managed runtime in the offline state a fresh fleet
	// node starts from, a system agent bound to it, and a queued task.
	runtimeID := dbfx.Runtime(t, "Aurora fleet e2e managed runtime", testutil.Cols{
		"provider":     "aurora_managed",
		"status":       "offline",
		"last_seen_at": nil,
	})
	agentID := dbfx.Agent(t, "Aurora fleet e2e agent", runtimeID, testutil.Cols{"kind": "system"})
	issueID := dbfx.Issue(t, "Aurora fleet e2e issue")
	taskID := dbfx.Task(t, agentID, testutil.Cols{
		"runtime_id": runtimeID,
		"issue_id":   issueID,
		"status":     "queued",
	})
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM daemon_token WHERE workspace_id = $1`, testWorkspaceID)
		testPool.Exec(ctx, `DELETE FROM aurora_sandbox_node WHERE workspace_id = $1`, testWorkspaceID)
	})

	// Run an in-process Fleet service with the Aurora profile and drive it
	// through the production provisioner, exactly as main.go wires it. The
	// enrollment secret travels only in the exported header and stays in the
	// Fleet's process-local handoff.
	namespace := "aurora-e2e-" + testUserID
	secret := []byte("test-only-012345678901234567890123456789")
	cfg := model.Config{
		Namespace: namespace,
		FleetID:   "aurora-e2e",
		Image:     auroraEnrollmentImageDigest,
		APIURL:    "http://127.0.0.1:1",
		Specs:     map[string]model.Spec{"sandbox": {CPUs: 2, MemoryBytes: 4294967296, Pids: 256, MaxRuns: 1}},
		MaxNodes:  2,
		Aurora:    &model.AuroraConfig{ServerURL: "http://api.test"},
	}
	repo := store.New(testPool, namespace, store.WithProvisioningConfig(cfg))
	svc := fleet.NewService(repo, cfg, nil)
	fleetServer := httptest.NewServer(svc.Handler(secret))
	defer fleetServer.Close()
	t.Cleanup(func() {
		for _, table := range []string{"fleet_node_credentials", "fleet_node_operations", "fleet_nodes", "fleet_credential_profiles"} {
			testPool.Exec(ctx, "DELETE FROM "+table+" WHERE namespace=$1 AND owner_id=$2", namespace, testUserID)
		}
	})

	client := cloudruntime.NewClient(cloudruntime.Config{BaseURL: fleetServer.URL, ServiceSecret: secret})
	mgr := aurora.NewSandboxManager(testHandler.Queries, testPool, aurora.NewFleetProvisioner(client), auroraEnrollmentImageDigest, nil)
	node, err := mgr.Ensure(ctx, parseUUID(testWorkspaceID), parseUUID(runtimeID))
	if err != nil {
		t.Fatalf("ensure workspace sandbox: %v", err)
	}
	if node.State != "starting" {
		t.Fatalf("ensured node state = %q, want starting", node.State)
	}
	if !node.BackendNodeID.Valid || node.BackendNodeID.String == "" {
		t.Fatalf("fleet backend node id was not recorded: %+v", node.BackendNodeID)
	}
	if node.BackendNodeID.String != uuidToString(node.ID) {
		t.Fatalf("backend node id = %q, want the Fleet node identity %q", node.BackendNodeID.String, uuidToString(node.ID))
	}

	// The Fleet holds the single-use secret the manager shipped. Handing it to
	// the daemon here mirrors the reconciler taking it at bootstrap.
	issued, ok := svc.TakeAuroraEnrollment(node.ID)
	if !ok || !strings.HasPrefix(issued, "mse_") {
		t.Fatalf("fleet enrollment handoff = %q ok=%v, want a single-use mse_ secret", issued, ok)
	}

	// The sandbox daemon exchanges the workspace-scoped enrollment secret for
	// an mdt_ credential and learns the runtime and daemon identity it serves.
	enrolled := testutil.Decode[struct {
		DaemonID    string `json:"daemon_id"`
		DaemonToken string `json:"daemon_token"`
		Runtime     struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"runtime"`
	}](t, testHandler.ManagedRuntimeEnroll, managedEnrollRequest(issued, nil), http.StatusOK)
	if enrolled.Runtime.ID != runtimeID {
		t.Fatalf("enrolled runtime id = %q, want %q", enrolled.Runtime.ID, runtimeID)
	}
	if enrolled.Runtime.Status != "online" {
		t.Fatalf("enrolled runtime status = %q, want online", enrolled.Runtime.Status)
	}
	if enrolled.DaemonID == "" || enrolled.DaemonToken == "" {
		t.Fatalf("enrollment response missing daemon identity: %+v", enrolled)
	}

	// The daemon claims the queued task under the identity it just enrolled.
	claim := testutil.Decode[batchClaimResponse](t, testHandler.ClaimTasksByRuntime,
		newDaemonTokenRequest(http.MethodPost, "/api/daemon/tasks/claim", map[string]any{
			"daemon_id":   enrolled.DaemonID,
			"runtime_ids": []string{runtimeID},
			"max_tasks":   1,
		}, testWorkspaceID, enrolled.DaemonID),
		http.StatusOK)
	if len(claim.Tasks) != 1 || claim.Tasks[0].ID != taskID {
		t.Fatalf("claimed tasks = %+v, want task %s", claim.Tasks, taskID)
	}

	// Every available skill route is materialized on the fleet's managed
	// runtime: the enrolled daemon's claim set therefore reaches each skill's
	// system agent, not just the generic task above. This is the fleet-level
	// half of "prove all 13 routes" - the handler matrix owns the lifecycle.
	if err := aurora.EnsureSystemAgents(ctx, testHandler.Queries, parseUUID(testWorkspaceID), parseUUID(testUserID)); err != nil {
		t.Fatalf("seed aurora system agents: %v", err)
	}
	names := make([]string, 0, len(aurora.Catalog()))
	for _, entry := range aurora.Catalog() {
		names = append(names, entry.Name)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE agent_id IN (SELECT id FROM agent WHERE workspace_id = $1 AND system_key LIKE 'aurora:%')`, testWorkspaceID)
		testPool.Exec(ctx, `DELETE FROM agent_skill WHERE agent_id IN (SELECT id FROM agent WHERE workspace_id = $1 AND system_key LIKE 'aurora:%')`, testWorkspaceID)
		testPool.Exec(ctx, `DELETE FROM agent WHERE workspace_id = $1 AND system_key LIKE 'aurora:%'`, testWorkspaceID)
		testPool.Exec(ctx, `DELETE FROM skill WHERE workspace_id = $1 AND name = ANY($2::text[])`, testWorkspaceID, names)
		testPool.Exec(ctx, `DELETE FROM agent_runtime WHERE workspace_id = $1 AND provider = 'aurora_managed'`, testWorkspaceID)
	})
	available := 0
	for _, entry := range aurora.Catalog() {
		if !entry.Available {
			continue
		}
		available++
		systemAgent, err := testHandler.Queries.GetAgentBySystemKey(ctx, db.GetAgentBySystemKeyParams{
			WorkspaceID: parseUUID(testWorkspaceID),
			SystemKey:   pgtype.Text{String: "aurora:" + entry.ID, Valid: true},
		})
		if err != nil {
			t.Fatalf("load %s system agent: %v", entry.ID, err)
		}
		if systemAgent.RuntimeID != parseUUID(runtimeID) {
			t.Fatalf("%s system agent runtime = %s, want the fleet runtime %s", entry.ID, uuidToString(systemAgent.RuntimeID), runtimeID)
		}
		if _, ok := aurora.Workflow(entry.ID); !ok {
			t.Fatalf("skill %s has no reviewed workflow", entry.ID)
		}
	}
	if available != len(aurora.Catalog()) {
		t.Fatalf("available skills = %d, want %d", available, len(aurora.Catalog()))
	}
}
