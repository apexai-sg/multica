package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/fleet/model"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// The provider column the workspace's server-hosted Aurora runtime row carries.
// The aurora package's own constant is unexported, and this read must not import
// the sandbox lifecycle just to name a column value.
const auroraManagedRuntimeProvider = "aurora_managed"

// auroraExecutionNodeProvider is the only provider the local Docker Fleet
// serves Aurora sandbox nodes with.
const auroraExecutionNodeProvider = "docker"

// AuroraRuntimeNodeResponse is the public projection of the workspace's managed
// execution node. Identity and lifecycle state only: the enrollment secret,
// image digest, backend daemon id and raw failure reason stay server-side.
type AuroraRuntimeNodeResponse struct {
	ID          string  `json:"id"`
	Status      string  `json:"status"`
	Ready       bool    `json:"ready"`
	Provider    string  `json:"provider"`
	ErrorCode   *string `json:"errorCode,omitempty"`
	OperationID *string `json:"operationId,omitempty"`
	CreatedAt   string  `json:"createdAt"`
}

// AuroraExecutionTargetResponse is the workspace's execution target as the
// runtime view consumes it. State is unconfigured, provisioning, online, offline
// or failed.
type AuroraExecutionTargetResponse struct {
	WorkspaceID string                     `json:"workspaceId"`
	Node        *AuroraRuntimeNodeResponse `json:"node"`
	RuntimeID   *string                    `json:"runtimeId"`
	State       string                     `json:"state"`
}

// GetAuroraRuntime returns the workspace's managed execution node and managed
// runtime binding, read-only.
//
// The read never takes the node's lifecycle lock and never calls the Fleet: the
// runtime view is a projection, and a read that serialised against generation
// creation would turn opening a screen into a write-path dependency. It uses
// non-locking reads of the sandbox, Fleet node and managed runtime. It does not
// acquire lifecycle locks or refresh Fleet observations.
func (h *Handler) GetAuroraRuntime(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace_id")
	if !ok {
		return
	}
	target, err := h.auroraRuntimeTarget(r.Context(), workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, target)
}

// EnsureAuroraRuntime wakes (or re-arms) the workspace's long-lived runtime.
// GET is deliberately side-effect free; this write path is the explicit action a
// user or an operator takes when the projection says the node is offline. The
// server's ensure reconciler calls the same manager method in the background,
// so this endpoint is a recovery control rather than the only way back.
func (h *Handler) EnsureAuroraRuntime(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace_id")
	if !ok {
		return
	}
	if h.SandboxManager == nil {
		writeErrorCode(w, http.StatusServiceUnavailable, auroraRuntimeUnavailableCode, errAuroraRuntimeUnavailable.Error())
		return
	}
	managed, err := h.Queries.GetAuroraManagedRuntime(r.Context(), db.GetAuroraManagedRuntimeParams{
		WorkspaceID: workspaceID,
		Provider:    auroraManagedRuntimeProvider,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusConflict, "Aurora runtime is not configured for this workspace")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load managed runtime")
		return
	}
	if _, err := h.SandboxManager.Ensure(r.Context(), workspaceID, managed.ID); err != nil {
		slog.Warn("ensure aurora runtime failed",
			"workspace_id", uuidToString(workspaceID),
			"runtime_id", uuidToString(managed.ID),
			"error", err)
		writeErrorCode(w, http.StatusServiceUnavailable, auroraRuntimeUnavailableCode, errAuroraRuntimeUnavailable.Error())
		return
	}
	target, err := h.auroraRuntimeTarget(r.Context(), workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, target)
}

// auroraRuntimeTarget projects the workspace's node, Fleet row, and managed
// runtime into the runtime view. Both the read and the ensure endpoint settle on
// this one projection so their responses cannot disagree about the state.
func (h *Handler) auroraRuntimeTarget(ctx context.Context, workspaceID pgtype.UUID) (AuroraExecutionTargetResponse, error) {
	// No node row means the workspace has never provisioned a sandbox. That is
	// unconfigured, not failed: nothing has gone wrong yet.
	var node *db.AuroraSandboxNode
	row, err := h.Queries.GetAuroraSandboxNodeByWorkspace(ctx, workspaceID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return AuroraExecutionTargetResponse{}, errors.New("failed to load execution node")
	default:
		node = &row
	}

	// The managed runtime is seeded lazily on generation creation, so an
	// unseeded workspace legitimately has none.
	var runtimeID *string
	managed, err := h.Queries.GetAuroraManagedRuntime(ctx, db.GetAuroraManagedRuntimeParams{
		WorkspaceID: workspaceID,
		Provider:    auroraManagedRuntimeProvider,
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return AuroraExecutionTargetResponse{}, errors.New("failed to load managed runtime")
	default:
		id := uuidToString(managed.ID)
		runtimeID = &id
	}

	var fleet *db.FleetNode
	if node != nil {
		row, err := h.Queries.GetAuroraRuntimeFleetNode(ctx, db.GetAuroraRuntimeFleetNodeParams{
			NodeID: node.ID, WorkspaceID: workspaceID, RuntimeID: node.RuntimeID,
		})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return AuroraExecutionTargetResponse{}, errors.New("failed to load execution status")
		default:
			fleet = &row
		}
	}
	state := auroraExecutionState(node, fleet, time.Now())
	return AuroraExecutionTargetResponse{
		WorkspaceID: uuidToString(workspaceID),
		Node:        auroraRuntimeNodeResponse(node, fleet, state),
		RuntimeID:   runtimeID,
		State:       state,
	}, nil
}

// auroraRuntimeNodeResponse projects a node row into its consumer shape. It
// returns nil for a workspace with no row so the JSON node field is null.
func auroraRuntimeNodeResponse(node *db.AuroraSandboxNode, fleet *db.FleetNode, state string) *AuroraRuntimeNodeResponse {
	if node == nil {
		return nil
	}
	// Prefer the Fleet node identity once the admission call has recorded it.
	// Before that, the row's own id is the identity the Fleet was asked for, so
	// it is the same public handle either way.
	id := uuidToString(node.ID)
	if node.BackendNodeID.Valid && node.BackendNodeID.String != "" {
		id = node.BackendNodeID.String
	}
	return &AuroraRuntimeNodeResponse{
		ID:          id,
		Status:      node.State,
		Ready:       state == "online",
		Provider:    auroraExecutionNodeProvider,
		ErrorCode:   auroraRuntimeErrorCode(*node, fleet, state),
		OperationID: nil, // the local node row does not persist the create operation id
		CreatedAt:   node.CreatedAt.Time.UTC().Format(time.RFC3339Nano),
	}
}

// auroraRuntimeErrorCode only emits the public recovery vocabulary. Neither
// Fleet error messages nor the sandbox's raw failure reason cross this boundary.
func auroraRuntimeErrorCode(node db.AuroraSandboxNode, fleet *db.FleetNode, state string) *string {
	if state != "failed" && state != "offline" && state != "unconfigured" {
		return nil
	}
	code := "runtime_offline"
	if state == "unconfigured" {
		code = "runtime_unconfigured"
	}
	if fleet != nil {
		switch fleet.ErrorCode {
		case "runtime_unconfigured":
			code = "runtime_unconfigured"
		case "profile_missing", "runtime_policy_unavailable":
			code = "runtime_policy_unavailable"
		}
	}
	if node.State == "failed" && node.FailureReason.Valid {
		reason := strings.ToLower(node.FailureReason.String)
		if strings.Contains(reason, "profile") || strings.Contains(reason, "credential") || strings.Contains(reason, "secret") || strings.Contains(reason, "policy") {
			code = "runtime_policy_unavailable"
		}
	}
	return &code
}

// Enrollment alone is not readiness. Reuse Fleet's claimability policy so stale
// health, maintenance and revoked nodes cannot appear online. This is a pure
// projection of stored observations, not an attempt to refresh or start a node.
func auroraExecutionState(node *db.AuroraSandboxNode, fleet *db.FleetNode, now time.Time) string {
	if node == nil {
		return "unconfigured"
	}
	if node.State == "failed" {
		return "failed"
	}
	if node.State == "stopped" {
		return "offline"
	}
	if fleet != nil {
		if fleet.Status == "failed" {
			return "failed"
		}
		if fleet.Revoked || fleet.Maintenance || fleet.Desired != "running" {
			return "offline"
		}
		switch fleet.Status {
		case "creating", "starting":
			return "provisioning"
		case "running":
		default:
			return "offline"
		}
	}
	switch node.State {
	case "starting":
		return "provisioning"
	case "online", "draining":
		if fleet != nil && model.CanClaim(model.Node{
			Desired: fleet.Desired, Status: fleet.Status, Ready: fleet.Ready,
			Maintenance: fleet.Maintenance, Revoked: fleet.Revoked, HealthAt: fleet.HealthAt.Time,
		}, now) {
			return "online"
		}
	}
	return "offline"
}
