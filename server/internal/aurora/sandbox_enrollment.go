package aurora

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	// ManagedExecutionProvider is the only execution provider a managed
	// enrollment may name. The persisted runtime stays aurora_managed; this
	// value is the in-daemon projection the enrollment response carries.
	ManagedExecutionProvider = "claude"
	// ManagedMaxConcurrency is the single claim slot a managed daemon may use.
	ManagedMaxConcurrency = 1
)

const (
	enrollmentTokenTTL = 5 * time.Minute
	daemonTokenTTL     = 8 * time.Hour
)

var (
	// ErrSandboxNodeAlreadyActive is returned by Issue when the workspace's
	// node is already online or draining: the existing launch owns the node,
	// and issuing again must not mint a competing secret.
	ErrSandboxNodeAlreadyActive = errors.New("sandbox node already active")
	// ErrInvalidManagedEnrollment collapses every failed secret lookup —
	// unknown, expired, and replayed — into one error so callers cannot tell
	// them apart.
	ErrInvalidManagedEnrollment = errors.New("invalid managed enrollment")
)

// imageDigestPattern requires the digest to be the tail of the reference: a
// registry may not append anything after "@sha256:<64 lowercase hex>".
var imageDigestPattern = regexp.MustCompile("@sha256:[0-9a-f]{64}$")

// SandboxNodeIdentity is one workspace's managed sandbox node: its own row id,
// the workspace and managed runtime it serves, and the daemon identity bound to
// it. Issue and Consume both speak it so a caller never reconstructs identity
// from the raw token.
type SandboxNodeIdentity struct {
	NodeID      pgtype.UUID
	WorkspaceID pgtype.UUID
	RuntimeID   pgtype.UUID
	DaemonID    string
}

// IssuedEnrollment is a freshly minted single-use secret plus the node it
// authorizes. Token is raw and is never persisted.
type IssuedEnrollment struct {
	Identity  SandboxNodeIdentity
	Token     string
	ExpiresAt time.Time
}

// ConsumedEnrollment is the daemon credential a managed daemon receives in
// exchange for a spent enrollment secret.
type ConsumedEnrollment struct {
	Identity             SandboxNodeIdentity
	DaemonToken          string
	DaemonTokenExpiresAt time.Time
}

// SandboxEnrollmentService owns the single-use enrollment protocol: it issues a
// five-minute mse_ secret for a workspace's managed runtime and exchanges that
// secret for a workspace- and daemon-scoped mdt_ credential.
type SandboxEnrollmentService struct {
	queries *db.Queries
	tx      TxBeginner
	now     func() time.Time
}

// NewSandboxEnrollmentService wires the service to its pool, queries, and clock.
// The pool is the transaction beginner; now must be supplied so expiry is
// deterministic in tests, and defaults to time.Now only when omitted.
func NewSandboxEnrollmentService(pool *pgxpool.Pool, q *db.Queries, now func() time.Time) *SandboxEnrollmentService {
	if now == nil {
		now = time.Now
	}
	return &SandboxEnrollmentService{queries: q, tx: pool, now: now}
}

// Issue creates or re-arms the workspace's single sandbox node and returns one
// raw enrollment secret. The advisory lock serialises issuance per workspace,
// and the locked node read serialises against Consume, so at most one live
// secret exists at a time.
func (s *SandboxEnrollmentService) Issue(ctx context.Context, workspaceID, runtimeID pgtype.UUID, imageDigest string) (IssuedEnrollment, error) {
	if !validImageDigest(imageDigest) {
		return IssuedEnrollment{}, fmt.Errorf("image digest %q must end with @sha256: and 64 lowercase hex characters", imageDigest)
	}

	tx, err := s.tx.Begin(ctx)
	if err != nil {
		return IssuedEnrollment{}, err
	}
	defer tx.Rollback(ctx)
	qtx := s.queries.WithTx(tx)

	if err := qtx.LockAuroraSandboxEnrollmentWorkspace(ctx, workspaceID); err != nil {
		return IssuedEnrollment{}, fmt.Errorf("lock workspace enrollment: %w", err)
	}
	managed, err := qtx.GetAuroraManagedRuntime(ctx, db.GetAuroraManagedRuntimeParams{
		WorkspaceID: workspaceID,
		Provider:    managedRuntimeProvider,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return IssuedEnrollment{}, fmt.Errorf("workspace %s has no managed runtime", util.UUIDToString(workspaceID))
		}
		return IssuedEnrollment{}, err
	}
	if managed.ID != runtimeID {
		return IssuedEnrollment{}, fmt.Errorf("runtime %s is not the workspace's managed runtime %s", util.UUIDToString(runtimeID), util.UUIDToString(managed.ID))
	}

	raw, err := auth.GenerateManagedEnrollmentToken()
	if err != nil {
		return IssuedEnrollment{}, err
	}
	expiresAt := s.now().Add(enrollmentTokenTTL)
	hash := pgtype.Text{String: auth.HashToken(raw), Valid: true}
	expiry := pgtype.Timestamptz{Time: expiresAt, Valid: true}

	existing, err := qtx.LockAuroraSandboxNodeByWorkspace(ctx, workspaceID)
	var node db.AuroraSandboxNode
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		nodeID := util.MustParseUUID(uuid.NewString())
		node, err = qtx.CreateAuroraSandboxNode(ctx, db.CreateAuroraSandboxNodeParams{
			ID:                  nodeID,
			WorkspaceID:         workspaceID,
			RuntimeID:           runtimeID,
			DaemonID:            newSandboxDaemonID(),
			ImageDigest:         imageDigest,
			State:               "starting",
			EnrollmentTokenHash: hash,
			EnrollmentExpiresAt: expiry,
		})
		if err != nil {
			return IssuedEnrollment{}, fmt.Errorf("create sandbox node: %w", err)
		}
	case err != nil:
		return IssuedEnrollment{}, err
	default:
		if existing.State == "online" || existing.State == "draining" {
			return IssuedEnrollment{}, ErrSandboxNodeAlreadyActive
		}
		node, err = qtx.RotateAuroraSandboxEnrollment(ctx, db.RotateAuroraSandboxEnrollmentParams{
			WorkspaceID:         workspaceID,
			EnrollmentTokenHash: hash,
			EnrollmentExpiresAt: expiry,
		})
		if err != nil {
			return IssuedEnrollment{}, fmt.Errorf("rotate sandbox enrollment: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return IssuedEnrollment{}, err
	}
	return IssuedEnrollment{
		Identity: SandboxNodeIdentity{
			NodeID:      node.ID,
			WorkspaceID: node.WorkspaceID,
			RuntimeID:   node.RuntimeID,
			DaemonID:    node.DaemonID,
		},
		Token:     raw,
		ExpiresAt: expiresAt,
	}, nil
}

// Consume spends an enrollment secret exactly once and returns the daemon
// credential it buys. Every write — clearing the secret, hashing the daemon
// token, stamping the launch, and binding the runtime — commits together, so a
// failure after the consume UPDATE leaves the secret spendable.
func (s *SandboxEnrollmentService) Consume(ctx context.Context, rawToken string) (ConsumedEnrollment, db.AgentRuntime, error) {
	if !validManagedEnrollmentToken(rawToken) {
		return ConsumedEnrollment{}, db.AgentRuntime{}, ErrInvalidManagedEnrollment
	}

	tx, err := s.tx.Begin(ctx)
	if err != nil {
		return ConsumedEnrollment{}, db.AgentRuntime{}, err
	}
	defer tx.Rollback(ctx)
	qtx := s.queries.WithTx(tx)

	node, err := qtx.ConsumeAuroraSandboxEnrollment(ctx, pgtype.Text{String: auth.HashToken(rawToken), Valid: true})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ConsumedEnrollment{}, db.AgentRuntime{}, ErrInvalidManagedEnrollment
		}
		return ConsumedEnrollment{}, db.AgentRuntime{}, err
	}

	rawDaemon, err := auth.GenerateDaemonToken()
	if err != nil {
		return ConsumedEnrollment{}, db.AgentRuntime{}, err
	}
	// The token must carry the runtime owner: the daemon register gate refuses
	// an unbound token (MUL-8154), so a managed node without this binding
	// enrolls, is pruned, and can never register its runtime.
	runtime, err := qtx.GetAgentRuntime(ctx, node.RuntimeID)
	if err != nil {
		return ConsumedEnrollment{}, db.AgentRuntime{}, fmt.Errorf("load managed runtime %s: %w", util.UUIDToString(node.RuntimeID), err)
	}
	expiresAt := s.now().Add(daemonTokenTTL)
	if _, err := qtx.CreateDaemonToken(ctx, db.CreateDaemonTokenParams{
		TokenHash:   auth.HashToken(rawDaemon),
		WorkspaceID: node.WorkspaceID,
		DaemonID:    node.DaemonID,
		ExpiresAt:   pgtype.Timestamptz{Time: expiresAt, Valid: true},
		OwnerID:     runtime.OwnerID,
	}); err != nil {
		return ConsumedEnrollment{}, db.AgentRuntime{}, fmt.Errorf("create daemon token: %w", err)
	}

	bound, err := qtx.BindAuroraManagedRuntime(ctx, db.BindAuroraManagedRuntimeParams{
		DaemonID:    pgtype.Text{String: node.DaemonID, Valid: true},
		ID:          node.RuntimeID,
		WorkspaceID: node.WorkspaceID,
	})
	if err != nil {
		return ConsumedEnrollment{}, db.AgentRuntime{}, fmt.Errorf("bind managed runtime %s: %w", util.UUIDToString(node.RuntimeID), err)
	}

	if err := tx.Commit(ctx); err != nil {
		return ConsumedEnrollment{}, db.AgentRuntime{}, err
	}
	return ConsumedEnrollment{
		Identity: SandboxNodeIdentity{
			NodeID:      node.ID,
			WorkspaceID: node.WorkspaceID,
			RuntimeID:   node.RuntimeID,
			DaemonID:    node.DaemonID,
		},
		DaemonToken:          rawDaemon,
		DaemonTokenExpiresAt: expiresAt,
	}, bound, nil
}

// validImageDigest reports whether ref ends with a pinned lowercase sha256
// digest.
func validImageDigest(ref string) bool {
	return imageDigestPattern.MatchString(ref)
}

// validManagedEnrollmentToken reports whether raw carries the mse_ prefix and
// exactly 40 lowercase hex characters. It is checked before any database call
// so junk credentials never reach the lookup.
func validManagedEnrollmentToken(raw string) bool {
	if !strings.HasPrefix(raw, "mse_") {
		return false
	}
	payload := strings.TrimPrefix(raw, "mse_")
	if len(payload) != 40 {
		return false
	}
	for i := 0; i < len(payload); i++ {
		c := payload[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
