package aurora

import (
	"context"
	"errors"

	"github.com/multica-ai/multica/server/internal/cloudruntime"
	"github.com/multica-ai/multica/server/internal/fleet/model"
)

// ErrNodeNotFound reports that the Fleet does not know the node a delete
// targeted. The reaper treats it as success so repeated cleanup is a no-op.
var ErrNodeNotFound = errors.New("fleet node not found")

// ErrNodeConflict and ErrNodeForbidden are the provider-neutral forms of the
// Fleet's identity rejections. The manager replaces the node identity on these
// rather than retrying the same id forever: a row left under an older Fleet
// namespace or generation answers this way on every ensure.
var (
	ErrNodeConflict  = errors.New("fleet node identity conflict")
	ErrNodeForbidden = errors.New("fleet node identity forbidden")
)

// The Aurora profile admits every workspace node under one fixed name and one
// administrator-approved resource spec. The spec name must match a key in the
// Fleet's Config.Specs; the image is the deployed AURORA_RUNTIME_IMAGE.
const (
	auroraFleetNodeName = "aurora-sandbox"
	auroraFleetNodeSpec = "sandbox"
)

// FleetNode is the provider-neutral view of one provisioned workspace node. ID
// is the Fleet node identity, State its lifecycle status, and BackendID the
// running container the Fleet resolved (empty before launch).
type FleetNode struct {
	ID        string
	State     string
	BackendID string
}

// FleetEnsureRequest is one workspace node's identity as shipped to the
// provisioner. The enrollment secret is here because the Fleet hands it to the
// managed daemon; it is never persisted by Aurora or returned to a caller.
type FleetEnsureRequest struct {
	NodeID          string
	WorkspaceID     string
	RuntimeID       string
	DaemonID        string
	EnrollmentToken string
	ImageDigest     string
	Name            string
	Spec            string
}

// FleetProvisioner is the provider-neutral workspace-node seam the sandbox
// manager and reaper drive. ownerID is the OwnerID of the aurora_managed
// runtime row read under the per-workspace lock; it is never taken from the
// HTTP caller and reaches the Fleet only as its trusted X-User-ID header.
type FleetProvisioner interface {
	EnsureWorkspaceNode(ctx context.Context, ownerID string, req FleetEnsureRequest) (FleetNode, error)
	DeleteWorkspaceNode(ctx context.Context, ownerID, nodeID string) error
}

// fleetProvisioner is the production FleetProvisioner. It speaks the Fleet
// workspace-node route through the shared cloudruntime client, so the service
// key and the single-use enrollment header are written by exactly one
// implementation and the secret never enters a JSON body.
type fleetProvisioner struct {
	client *cloudruntime.Client
}

// NewFleetProvisioner wraps a local Fleet client. Callers build it only after
// the local Fleet URL and service-key file have been validated.
func NewFleetProvisioner(client *cloudruntime.Client) FleetProvisioner {
	return &fleetProvisioner{client: client}
}

// EnsureWorkspaceNode admits (or replays) one workspace node. The idempotency
// key is stable for the node identity, so a repeated admission returns the
// original Fleet node instead of creating a duplicate.
func (p *fleetProvisioner) EnsureWorkspaceNode(ctx context.Context, ownerID string, req FleetEnsureRequest) (FleetNode, error) {
	if p == nil || p.client == nil {
		return FleetNode{}, errors.New("aurora: Fleet provisioner is not configured")
	}
	node, err := p.client.EnsureAuroraWorkspaceNode(ctx, ownerID, cloudruntime.AuroraWorkspaceNodeRequest{
		NodeID:          req.NodeID,
		WorkspaceID:     req.WorkspaceID,
		RuntimeID:       req.RuntimeID,
		DaemonID:        req.DaemonID,
		EnrollmentToken: req.EnrollmentToken,
		ImageDigest:     req.ImageDigest,
		Name:            req.Name,
		Spec:            req.Spec,
		IdempotencyKey:  "aurora:" + req.NodeID,
	})
	if err != nil {
		switch {
		case errors.Is(err, model.ErrConflict):
			return FleetNode{}, ErrNodeConflict
		case errors.Is(err, model.ErrForbidden):
			return FleetNode{}, ErrNodeForbidden
		default:
			return FleetNode{}, err
		}
	}
	return FleetNode{ID: node.ID, State: node.Status, BackendID: node.InstanceID}, nil
}

// DeleteWorkspaceNode records one destroy intent for the owner's node. The
// client's not-found sentinel is translated so the reaper can treat a node the
// Fleet no longer knows as already clean.
func (p *fleetProvisioner) DeleteWorkspaceNode(ctx context.Context, ownerID, nodeID string) error {
	if p == nil || p.client == nil {
		return errors.New("aurora: Fleet provisioner is not configured")
	}
	if err := p.client.DeleteAuroraWorkspaceNode(ctx, ownerID, nodeID); err != nil {
		if errors.Is(err, cloudruntime.ErrNodeNotFound) {
			return ErrNodeNotFound
		}
		return err
	}
	return nil
}

var _ FleetProvisioner = (*fleetProvisioner)(nil)
