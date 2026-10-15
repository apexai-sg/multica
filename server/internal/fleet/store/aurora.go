package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/fleet/model"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// auroraDeleteIdempotencyKey scopes a destroy intent to one control
// generation. fleet_node_operations is UNIQUE on
// (namespace, owner_id, idempotency_key) and the row is never reused, so a key
// that is fixed per node makes every teardown after the first collide with its
// own history: the insert raises a raw unique violation, the Fleet answers 503,
// and the reaper fails on every sweep forever (#259). A workspace re-arms the
// same node id across its lifetime cycles, so the generation the destroy
// advances is what tells the intents apart.
func auroraDeleteIdempotencyKey(nodeID pgtype.UUID, generation int64) string {
	return "aurora-delete:" + util.UUIDToString(nodeID) + ":" + strconv.FormatInt(generation, 10)
}

// auroraFingerprint binds one idempotency key to the immutable identity of an
// Aurora node intent. The one-time enrollment secret is deliberately excluded:
// a rotated secret must still replay the original node and operation.
func auroraFingerprint(nodeID, workspaceID, runtimeID pgtype.UUID, req model.AuroraNodeRequest) string {
	raw, _ := json.Marshal(struct {
		NodeID, WorkspaceID, RuntimeID    pgtype.UUID
		DaemonID, ImageDigest, Name, Spec string
	}{nodeID, workspaceID, runtimeID, req.DaemonID, req.ImageDigest, req.Name, req.Spec})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// validAurora admits only an administrator-approved image and declared spec. The
// caller cannot smuggle resources, a profile path or another image through the
// request. DaemonID must be a canonical UUID; its text form is persisted.
func (s *Store) validAurora(nodeID pgtype.UUID, req model.AuroraNodeRequest) bool {
	// The Aurora profile gate lives at the HTTP/Service layer (the route only
	// exists under Config.Aurora); the store still validates the administrator's
	// approved image and declared spec so a caller cannot smuggle either.
	if !validOwner(nodeID) || !validOwner(req.WorkspaceID) || !validOwner(req.RuntimeID) || !s.validProvisioning() {
		return false
	}
	if req.ImageDigest != s.provisioning.Image {
		return false
	}
	if _, err := util.ParseUUID(req.DaemonID); err != nil {
		return false
	}
	if strings.TrimSpace(req.IdempotencyKey) == "" || len(req.IdempotencyKey) > 128 {
		return false
	}
	return model.ValidateCreate(model.CreateRequest{Name: req.Name, Spec: req.Spec}, s.provisioning) == nil
}

// auroraBootstrapDead reports whether the persisted create intent cannot make
// progress. A failed/non-retryable operation or a revoked node is dead state;
// a healthy in-flight or completed bootstrap is not.
func auroraBootstrapDead(node model.Node, op model.Operation) bool {
	return node.Revoked || op.Phase == "failed" || op.NonRetryable
}

// resetAuroraIntent re-arms the same node identity for a fresh bootstrap. It
// advances the control generation, applies the current deployment image, clears
// the failed/revoked state, and rewinds the original create operation onto the
// new generation so it is claimable again. The idempotency key stays unchanged:
// this is still one create intent, never a second node or operation. Any
// unfinished non-create intent for the old generation (for example a queued
// destroy) is retired first so it cannot strand or later fire against the
// re-created identity.
func (s *Store) resetAuroraIntent(ctx context.Context, q *db.Queries, owner, nodeID pgtype.UUID, node model.Node, op model.Operation, req model.AuroraNodeRequest, fingerprint string) (model.Node, model.Operation, error) {
	next := node.Generation + 1
	if _, err := q.FleetRetireAuroraLifecycleOperations(ctx, db.FleetRetireAuroraLifecycleOperationsParams{
		Namespace:  s.namespace,
		OwnerID:    owner,
		NodeID:     nodeID,
		Generation: node.Generation,
	}); err != nil {
		return model.Node{}, model.Operation{}, err
	}
	row, err := q.FleetResetAuroraNode(ctx, db.FleetResetAuroraNodeParams{
		NextGeneration: next,
		Image:          req.ImageDigest,
		Namespace:      s.namespace,
		OwnerID:        owner,
		NodeID:         nodeID,
		Generation:     node.Generation,
		OperationID:    op.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Node{}, model.Operation{}, model.ErrConflict
	}
	if err != nil {
		return model.Node{}, model.Operation{}, err
	}
	resetNode, err := nodeFromRow(row)
	if err != nil {
		return model.Node{}, model.Operation{}, err
	}
	opRow, err := q.FleetResetAuroraCreateOperation(ctx, db.FleetResetAuroraCreateOperationParams{
		NextGeneration: next,
		RequestHash:    fingerprint,
		Namespace:      s.namespace,
		OwnerID:        owner,
		NodeID:         nodeID,
		OperationID:    op.ID,
		Generation:     op.Generation,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Node{}, model.Operation{}, model.ErrConflict
	}
	if err != nil {
		return model.Node{}, model.Operation{}, err
	}
	return resetNode, operationFromRow(opRow), nil
}

// auroraSameIdentity reports whether an existing Aurora node row was created
// for the same durable identity as req. The approved image is deliberately
// excluded: it is deployment-scoped, not identity.
func auroraSameIdentity(row db.FleetNode, req model.AuroraNodeRequest) bool {
	daemon, err := util.ParseUUID(req.DaemonID)
	if err != nil {
		return false
	}
	return row.WorkspaceID == req.WorkspaceID && row.RuntimeID == req.RuntimeID &&
		row.DaemonID == daemon && row.Name == req.Name && row.Spec == req.Spec
}

func (s *Store) lookupAuroraIntent(ctx context.Context, q *db.Queries, owner, nodeID pgtype.UUID, req model.AuroraNodeRequest, fingerprint string) (model.Node, model.Operation, bool, error) {
	existing, err := q.GetFleetIntentByKey(ctx, db.GetFleetIntentByKeyParams{Namespace: s.namespace, OwnerID: owner, IdempotencyKey: req.IdempotencyKey})
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Node{}, model.Operation{}, false, nil
	}
	if err != nil {
		return model.Node{}, model.Operation{}, false, err
	}
	// The key must not have been used for another node or action.
	if existing.Action != "create" || existing.NodeID != nodeID {
		return model.Node{}, model.Operation{}, false, model.ErrConflict
	}
	row, err := q.GetFleetNode(ctx, db.GetFleetNodeParams{Namespace: s.namespace, OwnerID: owner, NodeID: existing.NodeID})
	if err != nil {
		return model.Node{}, model.Operation{}, false, err
	}
	node, err := nodeFromRow(row)
	if err != nil {
		return model.Node{}, model.Operation{}, false, err
	}
	op := operationFromRow(existing)
	if existing.RequestHash == fingerprint {
		if !auroraBootstrapDead(node, op) {
			return node, op, true, nil
		}
		node, op, err = s.resetAuroraIntent(ctx, q, owner, nodeID, node, op, req, fingerprint)
		if err != nil {
			return model.Node{}, model.Operation{}, false, err
		}
		return node, op, true, nil
	}
	// The hash differs. A deployment-scoped image change on the same durable
	// identity re-arms the single create intent; every other difference means the
	// key is being reused for a different caller payload and stays a hard
	// conflict. The requested image must still be the administrator-approved one,
	// so a replay cannot smuggle an arbitrary image onto an existing node.
	if !auroraSameIdentity(row, req) || req.ImageDigest != s.provisioning.Image {
		return model.Node{}, model.Operation{}, false, model.ErrConflict
	}
	node, op, err = s.resetAuroraIntent(ctx, q, owner, nodeID, node, op, req, fingerprint)
	if err != nil {
		return model.Node{}, model.Operation{}, false, err
	}
	return node, op, true, nil
}

// CreateAuroraIntent is the single Aurora workspace-node admission point. The
// caller owns the node and daemon UUIDs; Fleet validates owner/namespace/image/
// spec, then commits the node and create operation atomically under the owner
// lock. A healthy matching replay returns the original node/operation even
// after quota or configuration changes; a replay of a failed/revoked create
// resets that same identity for a fresh bootstrap.
func (s *Store) CreateAuroraIntent(ctx context.Context, owner, nodeID pgtype.UUID, req model.AuroraNodeRequest) (model.Node, model.Operation, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, databaseTimeout)
	defer cancel()
	var node model.Node
	var op model.Operation
	replayed := false
	if !validOwner(owner) || !validOwner(nodeID) || strings.TrimSpace(req.IdempotencyKey) == "" {
		return node, op, false, model.ErrInvalidRequest
	}
	fingerprint := auroraFingerprint(nodeID, req.WorkspaceID, req.RuntimeID, req)
	err := s.WithTx(ctx, func(q *db.Queries) error {
		admission := CheckNamespaceAdmission(ctx, q, s.namespace)
		if admission != nil && !errors.Is(admission, model.ErrBusy) {
			return admission
		}
		if err := q.FleetOwnerExclusiveLock(ctx, db.FleetOwnerExclusiveLockParams{Namespace: s.namespace, OwnerID: owner}); err != nil {
			return err
		}
		var err error
		node, op, replayed, err = s.lookupAuroraIntent(ctx, q, owner, nodeID, req, fingerprint)
		if err != nil || replayed {
			return err
		}
		if admission != nil {
			return admission
		}
		if !s.validAurora(nodeID, req) {
			return model.ErrInvalidRequest
		}
		exists, err := q.FleetOwnerExists(ctx, owner)
		if err != nil {
			return err
		}
		if !exists {
			return model.ErrInvalidRequest
		}
		count, err := q.CountFleetProvisionedNodes(ctx, db.CountFleetProvisionedNodesParams{Namespace: s.namespace, OwnerID: owner})
		if err != nil {
			return err
		}
		if count >= int64(s.maxNodes) {
			return model.ErrConflict
		}
		snapshot, err := json.Marshal(s.provisioning.Specs[req.Spec])
		if err != nil {
			return model.ErrInvalidRequest
		}
		daemonID, _ := util.ParseUUID(req.DaemonID)
		nodeText := util.UUIDToString(nodeID)
		row, err := q.InsertFleetAuroraNode(ctx, db.InsertFleetAuroraNodeParams{NodeID: nodeID, DaemonID: daemonID, Namespace: s.namespace, OwnerID: owner, WorkspaceID: req.WorkspaceID, RuntimeID: req.RuntimeID, Name: req.Name, Spec: req.Spec, Image: req.ImageDigest, SpecConfig: snapshot, DataVolume: "multica-fleet-" + nodeText + "-data", SecretsVolume: "multica-fleet-" + nodeText + "-secrets"})
		if err != nil {
			return err
		}
		node, err = nodeFromRow(row)
		if err != nil {
			return err
		}
		operation, err := q.InsertFleetCreateOperation(ctx, db.InsertFleetCreateOperationParams{Namespace: s.namespace, OwnerID: owner, NodeID: node.ID, IdempotencyKey: req.IdempotencyKey, RequestHash: fingerprint})
		if err != nil {
			return err
		}
		op = operationFromRow(operation)
		return nil
	})
	if err != nil {
		return model.Node{}, model.Operation{}, false, err
	}
	return node, op, replayed, nil
}

// GetAuroraNode reads one owner- and namespace-scoped node. A missing, foreign
// owner or foreign namespace is pgx.ErrNoRows, never another owner's node.
func (s *Store) GetAuroraNode(ctx context.Context, owner, nodeID pgtype.UUID) (model.Node, error) {
	ctx, cancel := context.WithTimeout(ctx, databaseTimeout)
	defer cancel()
	if !validOwner(owner) || !validOwner(nodeID) {
		return model.Node{}, model.ErrForbidden
	}
	row, err := db.New(s.pool).GetFleetNode(ctx, db.GetFleetNodeParams{Namespace: s.namespace, OwnerID: owner, NodeID: nodeID})
	if err != nil {
		return model.Node{}, err
	}
	return nodeFromRow(row)
}

// DeleteAuroraIntent records one approved destroy intent and lets the
// Reconciler perform the physical deletion. It is idempotent: a node that is
// already gone or already terminating is success, because Aurora retries
// cleanup. The node lock serializes concurrent deletes under one key.
func (s *Store) DeleteAuroraIntent(ctx context.Context, owner, nodeID pgtype.UUID) error {
	ctx, cancel := context.WithTimeout(ctx, databaseTimeout)
	defer cancel()
	if !validOwner(owner) || !validOwner(nodeID) {
		return model.ErrForbidden
	}
	return s.WithTx(ctx, func(q *db.Queries) error {
		admission := CheckNamespaceAdmission(ctx, q, s.namespace)
		if admission != nil && !errors.Is(admission, model.ErrBusy) {
			return admission
		}
		n, err := s.lockedNode(ctx, q, owner, nodeID)
		if errors.Is(err, model.ErrForbidden) {
			return nil
		}
		if err != nil {
			return err
		}
		if n.Desired == "terminating" || n.Desired == "terminated" || n.Status == "terminated" {
			return nil
		}
		if admission != nil {
			return admission
		}
		// An Aurora node may still hold an unfinished create operation when its
		// daemon never enrolled; delete is the cleanup for exactly that case, so
		// quiescence is not required. An already-revoked node is also deletable.
		if n.Maintenance || n.Generation == math.MaxInt64 {
			return model.ErrConflict
		}
		next := n.Generation + 1
		advanced, err := q.FleetAdvanceIntent(ctx, db.FleetAdvanceIntentParams{Namespace: s.namespace, OwnerID: owner, NodeID: nodeID, Generation: n.Generation, NextGeneration: next, Desired: "terminating", Maintenance: true})
		if err != nil {
			return err
		}
		if advanced != 1 {
			return model.ErrConflict
		}
		approved, err := q.FleetApproveDelete(ctx, db.FleetApproveDeleteParams{Namespace: s.namespace, OwnerID: owner, NodeID: nodeID, Generation: next})
		if err != nil {
			return err
		}
		if approved != 1 {
			return model.ErrConflict
		}
		if err := q.RevokeFleetCredentials(ctx, db.RevokeFleetCredentialsParams{Namespace: s.namespace, OwnerID: owner, NodeID: nodeID}); err != nil {
			return err
		}
		_, err = q.InsertFleetLifecycleOperation(ctx, db.InsertFleetLifecycleOperationParams{Namespace: s.namespace, OwnerID: owner, NodeID: nodeID, Action: string(model.Delete), IdempotencyKey: auroraDeleteIdempotencyKey(nodeID, next), RequestHash: lifecycleFingerprint(nodeID, model.Delete), Phase: "queued", PriorDesired: n.Desired, Generation: next, Approved: true})
		return err
	})
}
