package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/fleet/model"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func sameRecoveryNode(a, b model.Node) bool {
	return a.Namespace == b.Namespace && a.ID == b.ID && a.OwnerID == b.OwnerID && a.Generation == b.Generation && a.ContainerID == b.ContainerID && a.StartEpoch == b.StartEpoch && a.DaemonID == b.DaemonID && a.DataVolume == b.DataVolume && a.SecretsVolume == b.SecretsVolume && a.Resources == b.Resources && a.Image == b.Image && a.ProfileRef == b.ProfileRef && a.Desired == b.Desired && a.Maintenance == b.Maintenance && a.Revoked == b.Revoked
}

const bootstrapLease = 5 * time.Minute

// observationClockSkew bounds the comparison between an observation stamped by
// the caller's clock and a timestamp read from PostgreSQL. The two are separate
// clock domains; a skew of up to two seconds is not evidence of a forged
// observation, while a timestamp further ahead is still rejected. The bound is
// small against the 30-second staleness window and the five-minute lease.
const observationClockSkew = 2 * time.Second

func bootstrapState(n model.Node, op model.Operation, now time.Time) string {
	if n.Generation != op.Generation || op.Action != model.Create || n.Revoked || n.Maintenance || n.Desired != "running" || op.NonRetryable {
		return "denied"
	}
	if n.ContainerID != "" {
		return "confirmed"
	}
	if op.Phase != "queued" && op.Phase != "prepared" && op.Phase != "applying" {
		return "denied"
	}
	if op.BootstrapClaimedAt.IsZero() {
		if op.BootstrapMinted || op.Attempts >= 5 {
			return "denied"
		}
		return "new"
	}
	if op.BootstrapClaimedAt.After(now) {
		return "denied"
	}
	if now.Before(op.BootstrapClaimedAt.Add(bootstrapLease)) {
		return "live"
	}
	return "expired"
}

func validateObservation(n model.Node, generation int64, o model.Observation, now time.Time) error {
	if generation < 1 || n.Generation != generation {
		return model.ErrConflict
	}
	if o.ObservedAt.IsZero() || o.ObservedAt.After(now.Add(observationClockSkew)) || now.Sub(o.ObservedAt) > 30*time.Second || o.ObservedAt.Before(n.HealthAt) || o.RuntimeCount < 0 || o.ActiveRuns < 0 || o.PendingReports < 0 || o.FailedReports < 0 || o.ActiveRuns > math.MaxInt32 || o.PendingReports > math.MaxInt32 || o.FailedReports > math.MaxInt32 {
		return model.ErrUnknownHealth
	}
	if o.DataVolume != "" && o.DataVolume != n.DataVolume {
		return model.ErrUnknownHealth
	}
	if o.Offline {
		if (o.Status != "stopped" && o.Status != "missing") || (n.Status != "stopped" && n.Status != "missing" && n.Status != "terminating") || o.Ready || o.ContainerID != "" || o.DaemonID != "" || o.StartEpoch != "" || n.DataVolume == "" || o.DataVolume != n.DataVolume || o.LayoutVersion != "1" {
			return model.ErrUnknownHealth
		}
		return nil
	}
	if o.Status != "running" && o.Status != "starting" && o.Status != "stopped" && o.Status != "missing" && o.Status != "unknown" {
		return model.ErrUnknownHealth
	}
	if o.Status == "missing" {
		if o.ContainerID != "" || o.StartEpoch != "" || o.DaemonID != "" || o.Ready || o.ReportStatsKnown {
			return model.ErrUnknownHealth
		}
		return nil
	}
	if n.ContainerID == "" || o.ContainerID != n.ContainerID || (o.DaemonID != "" && o.DaemonID != n.DaemonID) || (o.StartEpoch != "" && n.StartEpoch != "" && o.StartEpoch != n.StartEpoch) {
		return model.ErrUnknownHealth
	}
	if (o.Ready || o.ReportStatsKnown) && (o.Status != "running" || o.DaemonID != n.DaemonID || o.StartEpoch == "") {
		return model.ErrUnknownHealth
	}
	if o.Ready && (n.Revoked || n.Maintenance || n.Desired == "terminating" || n.Desired == "terminated") {
		return model.ErrConflict
	}
	return nil
}

// RecoverySnapshot is a current SQL binding, not a lease or maintenance proof.
// Re-read immediately before each external action; never use discovery as authority.
type RecoverySnapshot struct {
	Node      model.Node
	Operation model.Operation
	SQLNow    time.Time
	// Discovery projection only. Store recomputes from the private receipt under current locks.
	BootstrapSucceeded bool
	bootstrapSuccess   *bootstrapSuccess
}

func (s RecoverySnapshot) Ref() model.OperationRef {
	return model.OperationRef{Namespace: s.Node.Namespace, NodeID: s.Node.ID, OperationID: s.Operation.ID, Generation: s.Operation.Generation, Action: s.Operation.Action}
}

// BootstrapClaim is returned only to the initial winner; it cannot be reconstructed
// from a ListRecoverable projection. Its local deadline includes SQL round-trip time.
type BootstrapClaim struct {
	snapshot  RecoverySnapshot
	claimedAt time.Time
	deadline  time.Time
}

func (c BootstrapClaim) Snapshot() RecoverySnapshot { return c.snapshot }
func (c BootstrapClaim) Deadline() time.Time        { return c.deadline }
func (c BootstrapClaim) Context(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithDeadline(parent, c.deadline)
}

func affectedOne(count int64, e error) error {
	if e != nil {
		return e
	}
	if count != 1 {
		return model.ErrConflict
	}
	return nil
}
func timestamp(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: !t.IsZero()}
}

func (s *Store) recoverySnapshot(ctx context.Context, q *db.Queries, owner pgtype.UUID, ref model.OperationRef) (RecoverySnapshot, error) {
	var out RecoverySnapshot
	if ref.Namespace != s.namespace || !validOwner(owner) || !validOwner(ref.NodeID) || !validOwner(ref.OperationID) || ref.Generation < 1 {
		return out, model.ErrForbidden
	}
	n, e := s.lockedNode(ctx, q, owner, ref.NodeID)
	if e != nil {
		return out, e
	}
	exists, e := q.FleetOwnerExists(ctx, owner)
	if e != nil {
		return out, e
	}
	if !exists {
		return out, model.ErrForbidden
	}
	row, e := q.GetFleetOperation(ctx, db.GetFleetOperationParams{Namespace: s.namespace, OwnerID: owner, OperationID: ref.OperationID})
	if errors.Is(e, pgx.ErrNoRows) {
		return out, model.ErrForbidden
	}
	if e != nil {
		return out, e
	}
	op := operationFromRow(row)
	if op.NodeID != n.ID || op.OwnerID != n.OwnerID || op.Generation != ref.Generation || n.Generation != ref.Generation || op.Action != ref.Action {
		return out, model.ErrConflict
	}
	now, e := q.FleetRecoveryClock(ctx)
	if e != nil {
		return out, e
	}
	if !now.Valid || now.InfinityModifier != pgtype.Finite {
		return out, model.ErrUnavailable
	}
	out = RecoverySnapshot{Node: n, Operation: op, SQLNow: now.Time}
	if op.Action == model.Create {
		row, e := q.GetFleetNode(ctx, db.GetFleetNodeParams{Namespace: s.namespace, OwnerID: owner, NodeID: n.ID})
		if e != nil {
			return out, e
		}
		out.bootstrapSuccess, e = decodeBootstrapSuccess(row.Observation)
		if e != nil {
			return out, e
		}
		if out.bootstrapSuccess != nil {
			d, _, e := decodeDurableObservation(row.Observation)
			if e != nil || d.Generation != n.Generation {
				return out, model.ErrUnavailable
			}
		}
		if out.bootstrapSuccess != nil && !validBootstrapSuccess(out) {
			return out, model.ErrUnavailable
		}
		out.BootstrapSucceeded = validBootstrapSuccess(out)
	}
	return out, nil
}
func currentRecovery(s RecoverySnapshot) error {
	n, op := s.Node, s.Operation
	if op.NonRetryable || op.Attempts >= 5 || (op.Phase != "queued" && op.Phase != "preparing" && op.Phase != "prepared" && op.Phase != "applying") {
		return model.ErrConflict
	}
	if !op.NextAttemptAt.IsZero() && op.NextAttemptAt.After(s.SQLNow) {
		return model.ErrBusy
	}
	switch op.Action {
	case model.Create, model.Start:
		if n.Revoked || n.Maintenance || n.Desired != "running" {
			return model.ErrConflict
		}
	case model.Stop, model.Reboot, model.Delete:
		if !n.Maintenance {
			return model.ErrConflict
		}
		if !op.Approved {
			if op.Phase != "preparing" || n.Revoked {
				return model.ErrConflict
			}
			return nil
		}
		if op.Action == model.Delete {
			if !n.Revoked || n.Desired != "terminating" {
				return model.ErrConflict
			}
		} else if n.Revoked || (op.Action == model.Stop && n.Desired != "stopped") || (op.Action == model.Reboot && n.Desired != "running") {
			return model.ErrConflict
		}
	default:
		return model.ErrConflict
	}
	return nil
}
func sameRecoverySnapshot(a, b RecoverySnapshot) bool {
	return sameRecoveryNode(a.Node, b.Node) && a.Operation.ID == b.Operation.ID && a.Operation.NodeID == b.Operation.NodeID && a.Operation.OwnerID == b.Operation.OwnerID && a.Operation.Action == b.Operation.Action && a.Operation.Generation == b.Operation.Generation && a.Operation.Phase == b.Operation.Phase && a.Operation.Approved == b.Operation.Approved && a.Operation.Attempts == b.Operation.Attempts && a.Operation.NonRetryable == b.Operation.NonRetryable && a.Operation.BootstrapClaimedAt.Equal(b.Operation.BootstrapClaimedAt) && a.Operation.ActionClaimedAt.Equal(b.Operation.ActionClaimedAt) && a.Operation.ActionStartEpoch == b.Operation.ActionStartEpoch
}

// ListObservable is bounded read-only confirmed-idle discovery, never execution authority.
func (s *Store) ListObservable(ctx context.Context, after pgtype.UUID) ([]model.Node, error) {
	ctx, cancel := context.WithTimeout(ctx, databaseTimeout)
	defer cancel()
	if !after.Valid {
		after = pgtype.UUID{Valid: true}
	}
	rows, e := db.New(s.pool).FleetObservable(ctx, db.FleetObservableParams{Namespace: s.namespace, AfterID: after})
	if e != nil {
		return nil, e
	}
	out := make([]model.Node, 0, len(rows))
	for _, row := range rows {
		n, e := nodeFromRow(row)
		if e != nil {
			return nil, e
		}
		out = append(out, n)
	}
	return out, nil
}

func (s *Store) ListRecoverable(ctx context.Context, after pgtype.UUID) ([]model.Operation, error) {
	ctx, cancel := context.WithTimeout(ctx, databaseTimeout)
	defer cancel()
	if !after.Valid {
		after = pgtype.UUID{Valid: true}
	}
	rows, e := db.New(s.pool).ListFleetRecoverable(ctx, db.ListFleetRecoverableParams{Namespace: s.namespace, AfterID: after})
	if e != nil {
		return nil, e
	}
	out := make([]model.Operation, 0, len(rows))
	for _, row := range rows {
		out = append(out, operationFromRow(row))
	}
	return out, nil
}
func (s *Store) CurrentOperation(ctx context.Context, owner pgtype.UUID, ref model.OperationRef) (RecoverySnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, databaseTimeout)
	defer cancel()
	var out RecoverySnapshot
	e := s.WithTx(ctx, func(q *db.Queries) error {
		var e error
		out, e = s.recoverySnapshot(ctx, q, owner, ref)
		if e != nil {
			return e
		}
		return currentRecovery(out)
	})
	return out, e
}

func (s *Store) ClaimBootstrap(ctx context.Context, owner pgtype.UUID, ref model.OperationRef) (BootstrapClaim, error) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, databaseTimeout)
	defer cancel()
	var out BootstrapClaim
	e := s.WithTx(ctx, func(q *db.Queries) error {
		snap, e := s.recoverySnapshot(ctx, q, owner, ref)
		if e != nil {
			return e
		}
		if e = currentRecovery(snap); e != nil {
			return e
		}
		if bootstrapState(snap.Node, snap.Operation, snap.SQLNow) != "new" {
			return model.ErrConflict
		}
		row, e := q.FleetClaimBootstrap(ctx, db.FleetClaimBootstrapParams{Namespace: s.namespace, OwnerID: owner, NodeID: ref.NodeID, OperationID: ref.OperationID, Generation: ref.Generation})
		if errors.Is(e, pgx.ErrNoRows) {
			return model.ErrConflict
		}
		if e != nil {
			return e
		}
		snap.Operation = operationFromRow(row)
		now, e := q.FleetRecoveryClock(ctx)
		if e != nil {
			return e
		}
		if !now.Valid || now.InfinityModifier != pgtype.Finite {
			return model.ErrUnavailable
		}
		snap.SQLNow = now.Time
		if bootstrapState(snap.Node, snap.Operation, snap.SQLNow) != "live" {
			return model.ErrConflict
		}
		out = BootstrapClaim{snapshot: snap, claimedAt: snap.Operation.BootstrapClaimedAt, deadline: started.Add(snap.Operation.BootstrapClaimedAt.Add(bootstrapLease).Sub(now.Time))}
		return nil
	})
	return out, e
}
func validBootstrapClaim(c BootstrapClaim, snap RecoverySnapshot, localNow time.Time) bool {
	return !c.claimedAt.IsZero() && c.claimedAt.Equal(snap.Operation.BootstrapClaimedAt) && sameRecoverySnapshot(c.snapshot, snap) && bootstrapState(snap.Node, snap.Operation, snap.SQLNow) == "live" && localNow.Before(c.deadline)
}

func (s *Store) currentBootstrap(ctx context.Context, q *db.Queries, c BootstrapClaim) (RecoverySnapshot, error) {
	snap, e := s.recoverySnapshot(ctx, q, c.snapshot.Node.OwnerID, c.snapshot.Ref())
	if e != nil {
		return snap, e
	}
	if !validBootstrapClaim(c, snap, time.Now()) {
		return snap, model.ErrConflict
	}
	return snap, nil
}

// CheckBootstrapClaim must precede mint, private profile reads, and each Ensure call.
// The Worker must also run Provider work with claim.Context, not a fresh five-minute context.
func (s *Store) CheckBootstrapClaim(ctx context.Context, c BootstrapClaim) (model.Node, error) {
	ctx, leaseCancel := c.Context(ctx)
	defer leaseCancel()
	ctx, cancel := context.WithTimeout(ctx, databaseTimeout)
	defer cancel()
	var n model.Node
	e := s.WithTx(ctx, func(q *db.Queries) error { snap, e := s.currentBootstrap(ctx, q, c); n = snap.Node; return e })
	return n, e
}
func (s *Store) MintBootstrapToken(ctx context.Context, c BootstrapClaim) (string, int64, error) {
	ctx, leaseCancel := c.Context(ctx)
	defer leaseCancel()
	ctx, cancel := context.WithTimeout(ctx, databaseTimeout)
	defer cancel()
	var token string
	var generation int64
	e := s.WithTx(ctx, func(q *db.Queries) error {
		snap, e := s.currentBootstrap(ctx, q, c)
		if e != nil {
			return e
		}
		n, op := snap.Node, snap.Operation
		if op.BootstrapMinted {
			return model.ErrConflict
		}
		count, e := q.FleetMarkBootstrapMinted(ctx, db.FleetMarkBootstrapMintedParams{Namespace: s.namespace, OwnerID: n.OwnerID, NodeID: n.ID, OperationID: op.ID, Generation: op.Generation, ClaimedAt: timestamp(c.claimedAt)})
		if e = affectedOne(count, e); e != nil {
			return e
		}
		generation, e = q.MaxFleetCredentialGeneration(ctx, db.MaxFleetCredentialGenerationParams{Namespace: s.namespace, OwnerID: n.OwnerID, NodeID: n.ID})
		if e != nil {
			return e
		}
		if generation == math.MaxInt64 {
			return model.ErrUnavailable
		}
		generation++
		var hash string
		token, hash, e = NewNodeToken()
		if e != nil {
			return e
		}
		if e = q.RevokeFleetCredentials(ctx, db.RevokeFleetCredentialsParams{Namespace: s.namespace, OwnerID: n.OwnerID, NodeID: n.ID}); e != nil {
			return e
		}
		return q.InsertFleetCredential(ctx, db.InsertFleetCredentialParams{Namespace: s.namespace, OwnerID: n.OwnerID, NodeID: n.ID, TokenHash: hash, Generation: generation})
	})
	if e != nil {
		return "", 0, e
	}
	return token, generation, nil
}

// MarkBootstrapMinted records that the live bootstrap winner has started
// provisioning without minting a Fleet node token. Aurora nodes authenticate
// with their one-time enrollment secret, so there is no credential to write,
// but ConfirmBootstrap's SQL/lease fence still requires the operation to be
// marked. Reusing FleetMarkBootstrapMinted keeps the same claim, generation and
// lease authority: it writes no fleet_node_credentials row and returns
// ErrConflict when the operation was already marked.
func (s *Store) MarkBootstrapMinted(ctx context.Context, c BootstrapClaim) error {
	ctx, leaseCancel := c.Context(ctx)
	defer leaseCancel()
	ctx, cancel := context.WithTimeout(ctx, databaseTimeout)
	defer cancel()
	return s.WithTx(ctx, func(q *db.Queries) error {
		snap, e := s.currentBootstrap(ctx, q, c)
		if e != nil {
			return e
		}
		n, op := snap.Node, snap.Operation
		if op.BootstrapMinted {
			return model.ErrConflict
		}
		count, e := q.FleetMarkBootstrapMinted(ctx, db.FleetMarkBootstrapMintedParams{Namespace: s.namespace, OwnerID: n.OwnerID, NodeID: n.ID, OperationID: op.ID, Generation: op.Generation, ClaimedAt: timestamp(c.claimedAt)})
		return affectedOne(count, e)
	})
}

func bootstrapObservation(snap RecoverySnapshot, o model.Observation) (model.Node, error) {
	n := snap.Node
	if !snap.Operation.BootstrapMinted || o.ContainerID == "" || o.Offline || o.Status == "missing" || o.ObservedAt.Add(observationClockSkew).Before(snap.Operation.BootstrapClaimedAt) {
		return n, model.ErrConflict
	}
	n.ContainerID = o.ContainerID
	return n, validateObservation(n, n.Generation, o, snap.SQLNow)
}

func (s *Store) ConfirmBootstrap(ctx context.Context, c BootstrapClaim, o model.Observation) error {
	ctx, leaseCancel := c.Context(ctx)
	defer leaseCancel()
	ctx, cancel := context.WithTimeout(ctx, databaseTimeout)
	defer cancel()
	return s.WithTx(ctx, func(q *db.Queries) error {
		snap, e := s.currentBootstrap(ctx, q, c)
		if e != nil {
			return e
		}
		n, op := snap.Node, snap.Operation
		n, e = bootstrapObservation(snap, o)
		if e != nil {
			return e
		}
		proof := confirmedBootstrapSuccess(snap, o)
		raw, e := encodeObservation(n.Generation, o, proof)
		if e != nil {
			return e
		}
		count, e := q.FleetConfirmBootstrap(ctx, db.FleetConfirmBootstrapParams{Observation: raw, Namespace: s.namespace, OwnerID: n.OwnerID, NodeID: n.ID, OperationID: op.ID, Generation: op.Generation, ClaimedAt: timestamp(c.claimedAt), ContainerID: o.ContainerID, StartEpoch: o.StartEpoch, Status: o.Status})
		if e = affectedOne(count, e); e != nil {
			return e
		}
		return persistObservation(ctx, q, n, o)
	})
}

// ExpireBootstrap is restart disposition only. A live/future claim or confirmed
// container cannot be revoked; the original phase and volumes remain untouched.
func (s *Store) ExpireBootstrap(ctx context.Context, baseline RecoverySnapshot) error {
	ctx, cancel := context.WithTimeout(ctx, databaseTimeout)
	defer cancel()
	return s.WithTx(ctx, func(q *db.Queries) error {
		snap, e := s.recoverySnapshot(ctx, q, baseline.Node.OwnerID, baseline.Ref())
		if e != nil {
			return e
		}
		n, op := snap.Node, snap.Operation
		prior := n
		prior.Revoked = baseline.Node.Revoked
		if op.Action == model.Create && op.Phase == "applying" && !op.BootstrapClaimedAt.IsZero() && n.ContainerID == "" && n.Revoked && op.NonRetryable && op.ErrorCode == "bootstrap-unrecoverable" && op.BootstrapClaimedAt.Equal(baseline.Operation.BootstrapClaimedAt) && sameRecoveryNode(prior, baseline.Node) {
			return nil
		}
		if !sameRecoverySnapshot(baseline, snap) || bootstrapState(n, op, snap.SQLNow) != "expired" {
			return model.ErrConflict
		}
		count, e := q.FleetExpireBootstrapNode(ctx, db.FleetExpireBootstrapNodeParams{Namespace: s.namespace, OwnerID: n.OwnerID, NodeID: n.ID, OperationID: op.ID, Generation: op.Generation, ClaimedAt: timestamp(op.BootstrapClaimedAt)})
		if e = affectedOne(count, e); e != nil {
			return e
		}
		count, e = q.FleetExpireBootstrapOperation(ctx, db.FleetExpireBootstrapOperationParams{Namespace: s.namespace, OwnerID: n.OwnerID, NodeID: n.ID, OperationID: op.ID, Generation: op.Generation, ClaimedAt: timestamp(op.BootstrapClaimedAt)})
		if e = affectedOne(count, e); e != nil {
			return e
		}
		return q.RevokeFleetCredentials(ctx, db.RevokeFleetCredentialsParams{Namespace: s.namespace, OwnerID: n.OwnerID, NodeID: n.ID})
	})
}

// confirmedCreateExpired uses the immutable SQL checkpoint, never updated_at or local scheduling time.
func confirmedCreateExpired(s RecoverySnapshot) bool {
	n, op := s.Node, s.Operation
	return s.bootstrapSuccess == nil && n.Generation == op.Generation && currentRecovery(s) == nil && op.Action == model.Create && op.Phase == "applying" && n.ContainerID != "" && op.BootstrapMinted && !op.BootstrapClaimedAt.IsZero() && !op.BootstrapClaimedAt.After(s.SQLNow) && !s.SQLNow.Before(op.BootstrapClaimedAt.Add(bootstrapLease))
}

// ExpireConfirmedCreate is a current-bound creation-failure disposition. No identity/data cleanup,
// phase clearing, renewed claim or replacement is permitted; credentials are revoked atomically.
func (s *Store) ExpireConfirmedCreate(ctx context.Context, baseline RecoverySnapshot) error {
	ctx, cancel := context.WithTimeout(ctx, databaseTimeout)
	defer cancel()
	return s.WithTx(ctx, func(q *db.Queries) error {
		snap, e := s.recoverySnapshot(ctx, q, baseline.Node.OwnerID, baseline.Ref())
		if e != nil {
			return e
		}
		if !sameRecoverySnapshot(baseline, snap) || !confirmedCreateExpired(snap) {
			return model.ErrConflict
		}
		n, op := snap.Node, snap.Operation
		count, e := q.FleetExpireConfirmedCreateNode(ctx, db.FleetExpireConfirmedCreateNodeParams{Namespace: s.namespace, OwnerID: n.OwnerID, NodeID: n.ID, OperationID: op.ID, Generation: op.Generation, ContainerID: n.ContainerID, ClaimedAt: timestamp(op.BootstrapClaimedAt)})
		if e = affectedOne(count, e); e != nil {
			return e
		}
		count, e = q.FleetExpireConfirmedCreateOperation(ctx, db.FleetExpireConfirmedCreateOperationParams{Namespace: s.namespace, OwnerID: n.OwnerID, NodeID: n.ID, OperationID: op.ID, Generation: op.Generation, ClaimedAt: timestamp(op.BootstrapClaimedAt)})
		if e = affectedOne(count, e); e != nil {
			return e
		}
		return q.RevokeFleetCredentials(ctx, db.RevokeFleetCredentialsParams{Namespace: s.namespace, OwnerID: n.OwnerID, NodeID: n.ID})
	})
}

// bootstrapSuccess is historical proof, not a lease, current health, or public permission.
// Only live private ConfirmBootstrap writes it, atomically with the in-window confirmation CAS.
type bootstrapSuccess struct {
	Ref           model.OperationRef `json:"ref"`
	OwnerID       pgtype.UUID        `json:"owner_id"`
	ClaimedAt     time.Time          `json:"claimed_at"`
	ContainerID   string             `json:"container_id"`
	DaemonID      string             `json:"daemon_id"`
	StartEpoch    string             `json:"start_epoch"`
	DataVolume    string             `json:"data_volume"`
	SecretsVolume string             `json:"secrets_volume"`
	Image         string             `json:"image"`
	ProfileRef    string             `json:"profile_ref"`
	Resources     model.Spec         `json:"resources"`
	LayoutVersion string             `json:"layout_version"`
	Observation   model.Observation  `json:"observation"`
}

// Online layout is established by the accepted SDK mount/bootstrap/daemon protocol.
// Its real producer omits data/layout fields; supplied mismatches still fail closed.
// Report queue Known/counts are separate maintenance gates, not initialization health.
func bootstrapHealthy(n model.Node, o model.Observation, now time.Time) bool {
	claude := false
	for _, a := range o.Agents {
		claude = claude || strings.EqualFold(a, "claude")
	}
	return validateObservation(n, n.Generation, o, now) == nil && o.Ready && !o.Offline && o.Status == "running" && o.ContainerID == n.ContainerID && o.DaemonID == n.DaemonID && o.StartEpoch == n.StartEpoch && claude && o.RuntimeCount > 0 && (o.LayoutVersion == "" || o.LayoutVersion == "1")
}
func confirmedBootstrapSuccess(s RecoverySnapshot, o model.Observation) *bootstrapSuccess {
	if s.Operation.Phase != "applying" || bootstrapState(s.Node, s.Operation, s.SQLNow) != "live" {
		return nil
	}
	n, e := bootstrapObservation(s, o)
	if e != nil {
		return nil
	}
	n.StartEpoch = o.StartEpoch
	if !bootstrapHealthy(n, o, s.SQLNow) || !s.SQLNow.Before(s.Operation.BootstrapClaimedAt.Add(bootstrapLease)) {
		return nil
	}
	p := &bootstrapSuccess{Ref: s.Ref(), OwnerID: n.OwnerID, ClaimedAt: s.Operation.BootstrapClaimedAt, ContainerID: o.ContainerID, DaemonID: n.DaemonID, StartEpoch: o.StartEpoch, DataVolume: n.DataVolume, SecretsVolume: n.SecretsVolume, Image: n.Image, ProfileRef: n.ProfileRef, Resources: n.Resources, LayoutVersion: "1", Observation: o}
	s.Node = n
	s.bootstrapSuccess = p
	if !validBootstrapSuccess(s) {
		return nil
	}
	return p
}
func validBootstrapSuccess(s RecoverySnapshot) bool {
	p, n, op := s.bootstrapSuccess, s.Node, s.Operation
	if p == nil || !validOwner(n.ID) || !validOwner(n.OwnerID) || !validOwner(op.ID) || n.Namespace == "" || op.NodeID != n.ID || op.OwnerID != n.OwnerID || op.Action != model.Create || !op.BootstrapMinted || op.BootstrapClaimedAt.IsZero() || n.Generation != op.Generation || n.Revoked || n.Maintenance || n.Desired != "running" || op.NonRetryable || (op.Phase != "applying" && op.Phase != "completed") {
		return false
	}
	if p.Ref != s.Ref() || p.OwnerID != n.OwnerID || !p.ClaimedAt.Equal(op.BootstrapClaimedAt) || p.ContainerID == "" || p.ContainerID != n.ContainerID || p.DaemonID != n.DaemonID || p.StartEpoch != n.StartEpoch || p.DataVolume != n.DataVolume || p.SecretsVolume != n.SecretsVolume || p.Image != n.Image || p.ProfileRef != n.ProfileRef || p.Resources != n.Resources || p.LayoutVersion != "1" {
		return false
	}
	o := p.Observation
	if p.ClaimedAt.After(s.SQLNow) || o.ObservedAt.After(s.SQLNow.Add(observationClockSkew)) || o.ObservedAt.Before(p.ClaimedAt) || !o.ObservedAt.Before(p.ClaimedAt.Add(bootstrapLease)) || o.ObservedAt.Before(op.CreatedAt) {
		return false
	}
	epoch, e := time.Parse(time.RFC3339Nano, p.StartEpoch)
	if e != nil || epoch.Before(p.ClaimedAt) || epoch.After(o.ObservedAt) {
		return false
	}
	// Ignore current lease age only for the historical fact, never for completion.
	n.HealthAt = time.Time{}
	return bootstrapHealthy(n, o, o.ObservedAt)
}
func decodeDurableObservation(raw []byte) (durableObservation, map[string]json.RawMessage, error) {
	var d durableObservation
	var fields map[string]json.RawMessage
	_, e := model.DecodeStrictObject(raw, &fields)
	if e != nil || (len(fields) != 2 && len(fields) != 3) || fields["generation"] == nil || fields["observation"] == nil || (len(fields) == 3 && fields["bootstrap_success"] == nil) {
		return d, fields, model.ErrUnavailable
	}
	if e = json.Unmarshal(raw, &d); e != nil {
		return d, fields, model.ErrUnavailable
	}
	return d, fields, nil
}
func decodeBootstrapSuccess(raw []byte) (*bootstrapSuccess, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("{}")) {
		return nil, nil
	}
	_, fields, e := decodeDurableObservation(raw)
	if e != nil {
		return nil, model.ErrUnavailable
	}
	if proof, ok := fields["bootstrap_success"]; ok {
		var p bootstrapSuccess
		f, e := model.DecodeStrictObject(proof, &p)
		if e != nil || len(f) != 13 {
			return nil, model.ErrUnavailable
		}
		for key, required := range map[string][]string{
			"ref":         {"Namespace", "NodeID", "OperationID", "Generation", "Action"},
			"observation": {"ContainerID", "Status", "DaemonID", "StartEpoch", "Ready", "Agents", "RuntimeCount", "ActiveRuns", "PendingReports", "FailedReports", "ReportStatsKnown", "Offline", "DataVolume", "LayoutVersion", "ObservedAt"},
		} {
			var nested map[string]json.RawMessage
			_, e := model.DecodeStrictObject(f[key], &nested)
			if e != nil || len(nested) != len(required) {
				return nil, model.ErrUnavailable
			}
			for _, field := range required {
				if nested[field] == nil {
					return nil, model.ErrUnavailable
				}
			}
		}
		var resources model.Spec
		if fields, e := model.DecodeStrictObject(f["resources"], &resources); e != nil || len(fields) != 4 {
			return nil, model.ErrUnavailable
		}
		return &p, nil
	}
	return nil, nil
}
func encodeObservation(generation int64, o model.Observation, p *bootstrapSuccess) ([]byte, error) {
	if o.Agents == nil {
		o.Agents = []string{}
	}
	if p == nil {
		return json.Marshal(struct {
			Generation  int64             `json:"generation"`
			Observation model.Observation `json:"observation"`
		}{generation, o})
	}
	return json.Marshal(durableObservation{Generation: generation, Observation: o, BootstrapSuccess: p})
}

type durableObservation struct {
	Generation       int64             `json:"generation"`
	Observation      model.Observation `json:"observation"`
	BootstrapSuccess *bootstrapSuccess `json:"bootstrap_success,omitempty"`
}

func decodeObservation(raw []byte, n model.Node) (model.Observation, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("{}")) || len(raw) == 0 {
		return model.Observation{}, nil
	}
	d, fields, e := decodeDurableObservation(raw)
	if e != nil || (len(fields) != 2 && len(fields) != 3) {
		return model.Observation{}, model.ErrUnavailable
	}
	var observed map[string]json.RawMessage
	observed, e = model.DecodeStrictObject(fields["observation"], &observed)
	required := []string{"ContainerID", "Status", "DaemonID", "StartEpoch", "Ready", "Agents", "RuntimeCount", "ActiveRuns", "PendingReports", "FailedReports", "ReportStatsKnown", "Offline", "DataVolume", "LayoutVersion", "ObservedAt"}
	if e != nil || len(observed) != len(required) {
		return model.Observation{}, model.ErrUnavailable
	}
	for _, key := range required {
		if _, ok := observed[key]; !ok {
			return model.Observation{}, model.ErrUnavailable
		}
	}
	if d.Generation != n.Generation {
		return model.Observation{}, nil
	}
	// Historic diagnostics are not current execution proof. Validate structure/binding,
	// not wall-clock freshness here; write/approval paths perform the fresh clock check.
	check := n
	check.HealthAt = time.Time{}
	check.Maintenance = false
	check.Revoked = false
	check.Desired = "running"
	if d.Observation.Offline {
		check.Status = d.Observation.Status
	}
	if e = validateObservation(check, n.Generation, d.Observation, d.Observation.ObservedAt); e != nil {
		return model.Observation{}, model.ErrUnavailable
	}
	return d.Observation, nil
}
func persistObservation(ctx context.Context, q *db.Queries, n model.Node, o model.Observation) error {
	if o.Agents == nil {
		o.Agents = []string{}
	}
	raw, e := encodeObservation(n.Generation, o, nil)
	if e != nil {
		return model.ErrUnknownHealth
	}
	count, e := q.FleetRecordObservation(ctx, db.FleetRecordObservationParams{Namespace: n.Namespace, OwnerID: n.OwnerID, NodeID: n.ID, Generation: n.Generation, Observation: raw, Status: o.Status, StartEpoch: o.StartEpoch, Ready: o.Ready, ObservedAt: timestamp(o.ObservedAt), ActiveRuns: int32(o.ActiveRuns), PendingReports: int32(o.PendingReports), FailedReports: int32(o.FailedReports)})
	return affectedOne(count, e)
}
func (s *Store) RecordObservation(ctx context.Context, nodeID pgtype.UUID, generation int64, o model.Observation) error {
	ctx, cancel := context.WithTimeout(ctx, databaseTimeout)
	defer cancel()
	return s.WithTx(ctx, func(q *db.Queries) error {
		row, e := s.credentialNode(ctx, q, nodeID)
		if e != nil {
			return e
		}
		n, e := nodeFromRow(row)
		if e != nil {
			return e
		}
		now, e := q.FleetRecoveryClock(ctx)
		if e != nil {
			return e
		}
		if !now.Valid || now.InfinityModifier != pgtype.Finite {
			return model.ErrUnavailable
		}
		if e = validateObservation(n, generation, o, now.Time); e != nil {
			return e
		}
		return persistObservation(ctx, q, n, o)
	})
}

func safeRecoveryCode(code string) bool {
	switch code {
	case "unavailable", "configuration", "forbidden", "instance-lost", "bootstrap-unrecoverable":
		return true
	}
	return false
}

// FailBootstrap is available only to the live initial winner. Preserve all resources;
// there is no inferred cleanup permission and no retry with an unrecoverable hash.
func (s *Store) FailBootstrap(ctx context.Context, c BootstrapClaim, code string) error {
	if !safeRecoveryCode(code) {
		return model.ErrInvalidRequest
	}
	ctx, leaseCancel := c.Context(ctx)
	defer leaseCancel()
	ctx, cancel := context.WithTimeout(ctx, databaseTimeout)
	defer cancel()
	return s.WithTx(ctx, func(q *db.Queries) error {
		snap, e := s.currentBootstrap(ctx, q, c)
		if e != nil {
			return e
		}
		n, op := snap.Node, snap.Operation
		count, e := q.FleetFailBootstrapNode(ctx, db.FleetFailBootstrapNodeParams{Namespace: s.namespace, OwnerID: n.OwnerID, NodeID: n.ID, OperationID: op.ID, Generation: op.Generation, ClaimedAt: timestamp(c.claimedAt), ErrorCode: code})
		if e = affectedOne(count, e); e != nil {
			return e
		}
		count, e = q.FleetFailBootstrapOperation(ctx, db.FleetFailBootstrapOperationParams{Namespace: s.namespace, OwnerID: n.OwnerID, NodeID: n.ID, OperationID: op.ID, Generation: op.Generation, ClaimedAt: timestamp(c.claimedAt), ErrorCode: code})
		if e = affectedOne(count, e); e != nil {
			return e
		}
		return q.RevokeFleetCredentials(ctx, db.RevokeFleetCredentialsParams{Namespace: s.namespace, OwnerID: n.OwnerID, NodeID: n.ID})
	})
}

// DeferOperation records expected unknown proof without spending actual-error attempts.
func (s *Store) DeferOperation(ctx context.Context, baseline RecoverySnapshot, delay time.Duration) error {
	if delay < 5*time.Second || delay > 30*time.Second {
		return model.ErrInvalidRequest
	}
	return s.scheduleRecovery(ctx, baseline, "unknown", false, 0, int32((delay+time.Second-1)/time.Second))
}

// RecordOperationError accepts sanitized dispositions only, never arbitrary error text.
// Unconfirmed bootstrap failures must use FailBootstrap/ExpireBootstrap instead.
func (s *Store) RecordOperationError(ctx context.Context, baseline RecoverySnapshot, code string, permanent bool) error {
	if !safeRecoveryCode(code) {
		return model.ErrInvalidRequest
	}
	return s.scheduleRecovery(ctx, baseline, code, permanent, 1, 5)
}
func (s *Store) scheduleRecovery(ctx context.Context, baseline RecoverySnapshot, code string, permanent bool, increment, delay int32) error {
	ctx, cancel := context.WithTimeout(ctx, databaseTimeout)
	defer cancel()
	return s.WithTx(ctx, func(q *db.Queries) error {
		snap, e := s.recoverySnapshot(ctx, q, baseline.Node.OwnerID, baseline.Ref())
		if e != nil {
			return e
		}
		if e = currentRecovery(snap); e != nil {
			return e
		}
		if !sameRecoverySnapshot(baseline, snap) {
			return model.ErrConflict
		}
		n, op := snap.Node, snap.Operation
		if op.Action == model.Create && n.ContainerID == "" && !op.BootstrapClaimedAt.IsZero() {
			return model.ErrConflict
		}
		count, e := q.FleetScheduleOperation(ctx, db.FleetScheduleOperationParams{Namespace: s.namespace, OwnerID: n.OwnerID, NodeID: n.ID, OperationID: op.ID, Generation: op.Generation, Action: string(op.Action), Phase: op.Phase, Approved: op.Approved, Attempts: int32(op.Attempts), AttemptIncrement: increment, Permanent: permanent, DelaySeconds: delay, ErrorCode: code})
		return affectedOne(count, e)
	})
}

// RecordOperationResult never adopts an unconfirmed create. Start/reboot may publish
// a newer SDK epoch only under their approved original-operation/current-resource fence.
// Non-ready results remain operative; the Worker must inspect, not repeat Ensure/Apply.
func (s *Store) RecordOperationResult(ctx context.Context, baseline RecoverySnapshot, o model.Observation) error {
	ctx, cancel := context.WithTimeout(ctx, databaseTimeout)
	defer cancel()
	return s.WithTx(ctx, func(q *db.Queries) error {
		snap, e := s.recoverySnapshot(ctx, q, baseline.Node.OwnerID, baseline.Ref())
		if e != nil {
			return e
		}
		if e = currentRecovery(snap); e != nil {
			return e
		}
		if !sameRecoverySnapshot(baseline, snap) {
			return model.ErrConflict
		}
		return s.persistOperationResult(ctx, q, snap, o, false)
	})
}

// FinishDelete is SQL completion only. The Worker must first obtain fresh successful
// Provider.Delete proof for this exact current operation; no resource I/O occurs here.
func (s *Store) FinishDelete(ctx context.Context, operationID pgtype.UUID, generation int64) error {
	ctx, cancel := context.WithTimeout(ctx, databaseTimeout)
	defer cancel()
	return s.WithTx(ctx, func(q *db.Queries) error {
		locator, e := q.FleetOperationLocator(ctx, db.FleetOperationLocatorParams{Namespace: s.namespace, OperationID: operationID})
		if errors.Is(e, pgx.ErrNoRows) {
			return model.ErrForbidden
		}
		if e != nil {
			return e
		}
		ref := model.OperationRef{Namespace: s.namespace, NodeID: locator.NodeID, OperationID: operationID, Generation: generation, Action: model.Delete}
		snap, e := s.recoverySnapshot(ctx, q, locator.OwnerID, ref)
		if e != nil {
			return e
		}
		n, op := snap.Node, snap.Operation
		if op.Action != model.Delete || !op.Approved || !n.Revoked || !n.Maintenance {
			return model.ErrConflict
		}
		if op.Phase == "completed" && n.Desired == "terminated" && n.Status == "terminated" {
			return nil
		}
		if e = currentRecovery(snap); e != nil {
			return e
		}
		if e = q.FleetNodeCapacityLock(ctx, db.FleetNodeCapacityLockParams{Namespace: s.namespace, NodeID: n.ID}); e != nil {
			return e
		}
		candidates, e := q.FleetDeleteRuntimes(ctx, db.FleetDeleteRuntimesParams{OwnerID: n.OwnerID, NodeText: util.UUIDToString(n.ID)})
		if e != nil {
			return e
		}
		ids := make([]pgtype.UUID, 0, len(candidates))
		for _, rt := range candidates {
			ids = append(ids, rt.ID)
		}
		if e = q.LockWorkspaceForRuntimeMerge(ctx, ids); e != nil {
			return e
		}
		locked, e := q.LockRuntimesForMerge(ctx, ids)
		if e != nil {
			return e
		}
		if len(candidates) != len(locked) {
			return model.ErrConflict
		}
		for i, rt := range locked {
			if rt.ID != candidates[i].ID || rt.OwnerID != candidates[i].OwnerID || rt.WorkspaceID != candidates[i].WorkspaceID || !bytes.Equal(rt.Metadata, candidates[i].Metadata) {
				return model.ErrConflict
			}
		}
		again, e := q.FleetDeleteRuntimes(ctx, db.FleetDeleteRuntimesParams{OwnerID: n.OwnerID, NodeText: util.UUIDToString(n.ID)})
		if e != nil {
			return e
		}
		if len(again) != len(locked) {
			return model.ErrConflict
		}
		for i, rt := range again {
			if rt.ID != locked[i].ID {
				return model.ErrConflict
			}
		}
		if e = s.idleSQL(ctx, q, n, model.Delete); e != nil {
			return e
		}
		for _, rt := range locked {
			count, e := q.FleetOfflineDeletedRuntime(ctx, db.FleetOfflineDeletedRuntimeParams{RuntimeID: rt.ID, OwnerID: n.OwnerID, NodeText: util.UUIDToString(n.ID)})
			if e = affectedOne(count, e); e != nil {
				return e
			}
		}
		count, e := q.FleetTombstoneNode(ctx, db.FleetTombstoneNodeParams{Namespace: s.namespace, OwnerID: n.OwnerID, NodeID: n.ID, OperationID: op.ID, Generation: op.Generation})
		if e = affectedOne(count, e); e != nil {
			return e
		}
		count, e = q.FleetCompleteOperation(ctx, db.FleetCompleteOperationParams{Namespace: s.namespace, OwnerID: n.OwnerID, NodeID: n.ID, OperationID: op.ID, Generation: op.Generation, Action: string(op.Action), Phase: op.Phase, Approved: op.Approved})
		return affectedOne(count, e)
	})
}
