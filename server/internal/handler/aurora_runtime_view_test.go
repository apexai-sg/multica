package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// auroraRuntimeView is the decoded GET /api/aurora/runtime body. It mirrors the
// handler's response shape so a rename on either side fails the test.
type auroraRuntimeView struct {
	WorkspaceID string `json:"workspaceId"`
	Node        *struct {
		ID          string  `json:"id"`
		Status      string  `json:"status"`
		Ready       bool    `json:"ready"`
		Provider    string  `json:"provider"`
		ErrorCode   *string `json:"errorCode"`
		OperationID *string `json:"operationId"`
		CreatedAt   string  `json:"createdAt"`
	} `json:"node"`
	RuntimeID *string `json:"runtimeId"`
	State     string  `json:"state"`
}

// runtimeViewWorkspace creates a throwaway workspace with one managed runtime.
// The node row is created per case, so a case that wants "no node" simply does
// not insert one.
func runtimeViewWorkspace(t *testing.T) (workspaceID, runtimeID string) {
	t.Helper()
	workspaceID = dbfx.Workspace(t, "Aurora runtime view", "aurora-runtime-view-"+uuid.NewString())
	runtimeID = dbfx.Runtime(t, "Aurora runtime view managed", testutil.Cols{
		"workspace_id": workspaceID,
		"provider":     auroraManagedRuntimeProvider,
		"status":       "offline",
		"last_seen_at": nil,
	})
	return workspaceID, runtimeID
}

// runtimeViewRequest scopes the request to workspaceID. The handler resolves the
// workspace from the header exactly as the membership middleware does in
// production.
func runtimeViewRequest(workspaceID string) *http.Request {
	req := newRequest(http.MethodGet, "/api/aurora/runtime", nil)
	req.Header.Set("X-Workspace-ID", workspaceID)
	return req
}

// insertRuntimeNode writes the workspace's single node row.
func insertRuntimeNode(t *testing.T, workspaceID, runtimeID string, cols testutil.Cols) string {
	t.Helper()
	base := testutil.Cols{
		"id":           uuid.NewString(),
		"workspace_id": workspaceID,
		"runtime_id":   runtimeID,
		"daemon_id":    uuid.NewString(),
		"image_digest": "ghcr.io/eanfs/multica-aurora@sha256:" + strings.Repeat("a", 64),
		"state":        "online",
	}
	for k, v := range cols {
		base[k] = v
	}
	return dbfx.Insert(t, "aurora_sandbox_node", base)
}

func decodeRuntimeView(t *testing.T, workspaceID string) auroraRuntimeView {
	t.Helper()
	return testutil.Decode[auroraRuntimeView](
		t, testHandler.GetAuroraRuntime, runtimeViewRequest(workspaceID), http.StatusOK,
	)
}

func TestAuroraRuntimeViewReportsAConcreteReasonWhenTheNodeIsNotReady(t *testing.T) {
	workspaceID, runtimeID := runtimeViewWorkspace(t)
	nodeID := insertRuntimeNode(t, workspaceID, runtimeID, testutil.Cols{"state": "online"})
	dbfx.FleetNode(t, "runtime-view-"+uuid.NewString(), testutil.Cols{
		"id": nodeID, "workspace_id": workspaceID, "runtime_id": runtimeID,
		"status": "failed", "ready": false, "error_code": "profile_missing",
		"error_message": "private credential at http://internal:9000",
	})
	resp := testutil.Call(t, testHandler.GetAuroraRuntime, runtimeViewRequest(workspaceID)).Want(http.StatusOK)
	var out auroraRuntimeView
	resp.JSON(&out)
	if out.State != "failed" || out.Node == nil || out.Node.Ready || out.Node.ErrorCode == nil || *out.Node.ErrorCode != "runtime_policy_unavailable" {
		t.Fatalf("projection = %#v, node = %#v; want failed/not-ready/runtime_policy_unavailable", out, out.Node)
	}
	if strings.Contains(resp.Text(), "private credential") || strings.Contains(resp.Text(), "internal:9000") {
		t.Fatal("projection leaked the private Fleet error")
	}
}

// These cases catch stale sandbox readiness, unscoped Fleet lookups, and raw error leaks.
func TestAuroraRuntimeViewFleetProjection(t *testing.T) {
	for _, tc := range []struct {
		name, sandbox, state, code string
		fleet                      testutil.Cols
	}{
		{"missing Fleet", "online", "offline", "runtime_offline", nil},
		{"Fleet not ready", "online", "offline", "runtime_offline", testutil.Cols{"ready": false}},
		{"Fleet stopped", "online", "offline", "runtime_offline", testutil.Cols{"status": "stopped"}},
		{"Fleet unknown", "online", "offline", "runtime_offline", testutil.Cols{"status": "future-state"}},
		{"Fleet starting", "online", "provisioning", "", testutil.Cols{"status": "starting", "ready": false}},
		{"Fleet failed during enrollment", "starting", "failed", "runtime_offline", testutil.Cols{"status": "failed", "error_code": "private-token-at-internal-host"}},
		{"Fleet policy", "online", "failed", "runtime_policy_unavailable", testutil.Cols{"status": "failed", "error_code": "profile_missing"}},
		{"Fleet unconfigured", "online", "failed", "runtime_unconfigured", testutil.Cols{"status": "failed", "error_code": "runtime_unconfigured"}},
		{"Fleet stale health", "online", "offline", "runtime_offline", testutil.Cols{"health_at": time.Now().Add(-time.Hour)}},
		{"Fleet revoked", "online", "offline", "runtime_offline", testutil.Cols{"revoked": true}},
		{"Fleet maintenance", "online", "offline", "runtime_offline", testutil.Cols{"maintenance": true}},
		{"Fleet stopping", "online", "offline", "runtime_offline", testutil.Cols{"desired": "stopped"}},
		{"sandbox stopped wins", "stopped", "offline", "runtime_offline", testutil.Cols{}},
		{"foreign workspace", "online", "offline", "runtime_offline", testutil.Cols{"workspace_id": uuid.NewString(), "error_code": "profile_missing", "status": "failed"}},
		{"foreign runtime", "online", "offline", "runtime_offline", testutil.Cols{"runtime_id": uuid.NewString()}},
		{"foreign node", "online", "offline", "runtime_offline", testutil.Cols{"id": uuid.NewString()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspaceID, runtimeID := runtimeViewWorkspace(t)
			nodeID := insertRuntimeNode(t, workspaceID, runtimeID, testutil.Cols{"state": tc.sandbox})
			if tc.fleet != nil {
				cols := testutil.Cols{"id": nodeID, "workspace_id": workspaceID, "runtime_id": runtimeID, "status": "running", "error_message": "private-token-at-internal-host"}
				for k, v := range tc.fleet {
					cols[k] = v
				}
				dbfx.FleetNode(t, "runtime-view-"+uuid.NewString(), cols)
			}
			resp := testutil.Call(t, testHandler.GetAuroraRuntime, runtimeViewRequest(workspaceID)).Want(http.StatusOK)
			var out auroraRuntimeView
			resp.JSON(&out)
			if out.State != tc.state || out.Node == nil || out.Node.Ready {
				t.Fatalf("want %s/not-ready, got %#v node %#v", tc.state, out, out.Node)
			}
			if tc.code == "" {
				if out.Node.ErrorCode != nil {
					t.Fatalf("unexpected error code: %s", *out.Node.ErrorCode)
				}
			} else if out.Node.ErrorCode == nil || *out.Node.ErrorCode != tc.code {
				t.Fatalf("want code %s, got %#v", tc.code, out.Node)
			}
			if strings.Contains(resp.Text(), "private-token-at-internal-host") {
				t.Fatal("private Fleet error leaked")
			}
		})
	}
}

func TestGetAuroraRuntime(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	t.Run("unconfigured and null node before any provisioning", func(t *testing.T) {
		workspaceID, runtimeID := runtimeViewWorkspace(t)

		out := decodeRuntimeView(t, workspaceID)

		if out.WorkspaceID != workspaceID {
			t.Fatalf("workspaceId = %q, want %q", out.WorkspaceID, workspaceID)
		}
		if out.State != "unconfigured" {
			t.Fatalf("state = %q, want unconfigured", out.State)
		}
		if out.Node != nil {
			t.Fatalf("node = %#v, want null", out.Node)
		}
		// The managed runtime is seeded lazily but may already exist; here it
		// does, and the view still reports the unconfigured node.
		if out.RuntimeID == nil || *out.RuntimeID != runtimeID {
			t.Fatalf("runtimeId = %v, want %q", out.RuntimeID, runtimeID)
		}
	})

	t.Run("provisioning while the node is starting", func(t *testing.T) {
		workspaceID, runtimeID := runtimeViewWorkspace(t)
		nodeID := insertRuntimeNode(t, workspaceID, runtimeID, testutil.Cols{"state": "starting"})

		out := decodeRuntimeView(t, workspaceID)

		if out.State != "provisioning" {
			t.Fatalf("state = %q, want provisioning", out.State)
		}
		if out.Node == nil {
			t.Fatal("node is null")
		}
		if out.Node.ID != nodeID || out.Node.Status != "starting" || out.Node.Ready {
			t.Fatalf("node = %#v, want id %q starting/not-ready", out.Node, nodeID)
		}
	})

	t.Run("online and ready once the node is online", func(t *testing.T) {
		workspaceID, runtimeID := runtimeViewWorkspace(t)
		nodeID := insertRuntimeNode(t, workspaceID, runtimeID, testutil.Cols{
			"state": "online", "backend_node_id": "fleet-node-1",
		})
		dbfx.FleetNode(t, "runtime-view-"+uuid.NewString(), testutil.Cols{
			"id": nodeID, "workspace_id": workspaceID, "runtime_id": runtimeID, "status": "running",
		})

		out := decodeRuntimeView(t, workspaceID)

		if out.State != "online" {
			t.Fatalf("state = %q, want online", out.State)
		}
		if out.Node == nil {
			t.Fatal("node is null")
		}
		// The Fleet node identity is the public handle once it exists.
		if out.Node.ID != "fleet-node-1" {
			t.Fatalf("node id = %q, want fleet-node-1", out.Node.ID)
		}
		if !out.Node.Ready || out.Node.Provider != "docker" {
			t.Fatalf("node = %#v, want ready docker", out.Node)
		}
		if out.Node.OperationID != nil {
			t.Fatalf("operationId = %v, want null", *out.Node.OperationID)
		}
		if out.Node.CreatedAt == "" {
			t.Fatal("createdAt is empty")
		}
	})

	t.Run("draining still reads as online", func(t *testing.T) {
		workspaceID, runtimeID := runtimeViewWorkspace(t)
		nodeID := insertRuntimeNode(t, workspaceID, runtimeID, testutil.Cols{"state": "draining"})
		dbfx.FleetNode(t, "runtime-view-"+uuid.NewString(), testutil.Cols{
			"id": nodeID, "workspace_id": workspaceID, "runtime_id": runtimeID, "status": "running",
		})

		if out := decodeRuntimeView(t, workspaceID); out.State != "online" {
			t.Fatalf("state = %q, want online", out.State)
		}
	})

	t.Run("stopped reads as offline", func(t *testing.T) {
		workspaceID, runtimeID := runtimeViewWorkspace(t)
		insertRuntimeNode(t, workspaceID, runtimeID, testutil.Cols{"state": "stopped"})

		out := decodeRuntimeView(t, workspaceID)

		if out.State != "offline" {
			t.Fatalf("state = %q, want offline", out.State)
		}
		if out.Node == nil || out.Node.Status != "stopped" {
			t.Fatalf("node = %#v, want stopped status", out.Node)
		}
	})

	t.Run("failed maps the raw reason onto a public code", func(t *testing.T) {
		workspaceID, runtimeID := runtimeViewWorkspace(t)
		reason := "ensure workspace sandbox: dial tcp 10.0.0.5:9000: connect: connection refused"
		insertRuntimeNode(t, workspaceID, runtimeID, testutil.Cols{
			"state":          "failed",
			"failure_reason": reason,
		})

		resp := testutil.Call(t, testHandler.GetAuroraRuntime, runtimeViewRequest(workspaceID)).Want(http.StatusOK)
		var out auroraRuntimeView
		resp.JSON(&out)

		if out.State != "failed" {
			t.Fatalf("state = %q, want failed", out.State)
		}
		if out.Node == nil || out.Node.ErrorCode == nil || *out.Node.ErrorCode != "runtime_offline" {
			t.Fatalf("errorCode = %#v, want runtime_offline", out.Node)
		}
		// The raw reason names internal addresses; it must not cross the API.
		body := resp.Text()
		if strings.Contains(body, "10.0.0.5") || strings.Contains(body, "connection refused") {
			t.Fatalf("response leaked the raw failure reason: %s", body)
		}
	})

	t.Run("failed classifies a missing profile", func(t *testing.T) {
		workspaceID, runtimeID := runtimeViewWorkspace(t)
		insertRuntimeNode(t, workspaceID, runtimeID, testutil.Cols{
			"state":          "failed",
			"failure_reason": "provider credential profile is missing",
		})

		out := decodeRuntimeView(t, workspaceID)
		if out.Node == nil || out.Node.ErrorCode == nil || *out.Node.ErrorCode != "runtime_policy_unavailable" {
			t.Fatalf("errorCode = %#v, want runtime_policy_unavailable", out.Node)
		}
	})

	t.Run("another workspace's node stays invisible", func(t *testing.T) {
		otherWorkspace, otherRuntime := runtimeViewWorkspace(t)
		insertRuntimeNode(t, otherWorkspace, otherRuntime, testutil.Cols{"state": "online"})

		workspaceID, _ := runtimeViewWorkspace(t)
		out := decodeRuntimeView(t, workspaceID)

		if out.State != "unconfigured" || out.Node != nil {
			t.Fatalf("foreign node leaked into view: %#v", out)
		}
	})

	t.Run("rejects a malformed workspace id", func(t *testing.T) {
		req := newRequest(http.MethodGet, "/api/aurora/runtime", nil)
		req.Header.Set("X-Workspace-ID", "not-a-uuid")

		testutil.Call(t, testHandler.GetAuroraRuntime, req).Want(http.StatusBadRequest)
	})
}

// TestAuroraRuntimeViewIsReadOnly pins that the view does not mutate the node:
// a read that rewrote last_active_at would make opening the screen a heartbeat
// and hide an idle node from the reaper.
func TestAuroraRuntimeViewIsReadOnly(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	workspaceID, runtimeID := runtimeViewWorkspace(t)
	nodeID := insertRuntimeNode(t, workspaceID, runtimeID, testutil.Cols{"state": "online"})

	dbfx.FleetNode(t, "runtime-view-"+uuid.NewString(), testutil.Cols{
		"id": nodeID, "workspace_id": workspaceID, "runtime_id": runtimeID, "status": "running",
	})
	// Snapshot complete rows, including activity timestamps, health and credentials.
	const readProjectionSQL = `SELECT json_build_array(
  (SELECT row_to_json(n) FROM aurora_sandbox_node n WHERE n.workspace_id = $1),
  (SELECT row_to_json(f) FROM fleet_nodes f WHERE f.id = $2),
  (SELECT row_to_json(r) FROM agent_runtime r WHERE r.id = $3),
  (SELECT count(*) FROM fleet_node_operations o WHERE o.node_id = $2),
  (SELECT count(*) FROM fleet_node_credentials c WHERE c.node_id = $2)
 )::text`

	var before string
	if err := testPool.QueryRow(context.Background(), readProjectionSQL, workspaceID, nodeID, runtimeID).Scan(&before); err != nil {
		t.Fatalf("read node before: %v", err)
	}

	decodeRuntimeView(t, workspaceID)

	var after string
	if err := testPool.QueryRow(context.Background(), readProjectionSQL, workspaceID, nodeID, runtimeID).Scan(&after); err != nil {
		t.Fatalf("read node after: %v", err)
	}
	if before != after {
		t.Fatal("GET mutated runtime projection rows or created lifecycle records")
	}
}

// recordingSandboxManager records the ensure calls the wake endpoint makes. It
// satisfies aurora.WorkspaceSandboxManager so the handler seam stays unchanged.
type recordingSandboxManager struct {
	ensured []string
	err     error
}

func (m *recordingSandboxManager) Ensure(_ context.Context, workspaceID, runtimeID pgtype.UUID) (db.AuroraSandboxNode, error) {
	m.ensured = append(m.ensured, uuidToString(workspaceID)+"/"+uuidToString(runtimeID))
	return db.AuroraSandboxNode{}, m.err
}

func (m *recordingSandboxManager) HandoffWorkspaceToFleet(_ context.Context, _ pgtype.UUID) error {
	return nil
}

func ensureRuntimeRequest(workspaceID string) *http.Request {
	req := newRequest(http.MethodPost, "/api/aurora/runtime/ensure", nil)
	req.Header.Set("X-Workspace-ID", workspaceID)
	return req
}

// TestEnsureAuroraRuntimeWakesTheManagedNode pins the explicit recovery control:
// the endpoint asks the manager to ensure exactly the workspace's managed
// runtime and returns the same projection the read uses.
func TestEnsureAuroraRuntimeWakesTheManagedNode(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	workspaceID, runtimeID := runtimeViewWorkspace(t)
	manager := &recordingSandboxManager{}
	withSandboxManager(t, manager)

	view := testutil.Decode[auroraRuntimeView](
		t, testHandler.EnsureAuroraRuntime, ensureRuntimeRequest(workspaceID), http.StatusOK,
	)
	if len(manager.ensured) != 1 || manager.ensured[0] != workspaceID+"/"+runtimeID {
		t.Fatalf("ensure calls = %v, want [%s/%s]", manager.ensured, workspaceID, runtimeID)
	}
	if view.WorkspaceID != workspaceID {
		t.Fatalf("view workspace = %q, want %q", view.WorkspaceID, workspaceID)
	}
}

// TestEnsureAuroraRuntimeFailsClosed pins the two refusals: no manager means the
// feature is off, and the ensure failure is reported as a runtime outage rather
// than a successful wake.
func TestEnsureAuroraRuntimeFailsClosed(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	workspaceID, _ := runtimeViewWorkspace(t)

	withSandboxManager(t, nil)
	testutil.Call(t, testHandler.EnsureAuroraRuntime, ensureRuntimeRequest(workspaceID)).
		Want(http.StatusServiceUnavailable)

	withSandboxManager(t, &recordingSandboxManager{err: errors.New("fleet down")})
	testutil.Call(t, testHandler.EnsureAuroraRuntime, ensureRuntimeRequest(workspaceID)).
		Want(http.StatusServiceUnavailable)
}
