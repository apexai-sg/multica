package fleet

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/fleet/model"
	"github.com/multica-ai/multica/server/internal/fleet/store"
	"github.com/multica-ai/multica/server/internal/util"
)

// recoveryStore is a test seam over the exact durable Store contracts, not authority.
type recoveryStore interface {
	ListRecoverable(context.Context, pgtype.UUID) ([]model.Operation, error)
	ListObservable(context.Context, pgtype.UUID) ([]model.Node, error)
	CurrentOperation(context.Context, pgtype.UUID, model.OperationRef) (store.RecoverySnapshot, error)
	RecordObservation(context.Context, pgtype.UUID, int64, model.Observation) error
	RecordOperationResult(context.Context, store.RecoverySnapshot, model.Observation) error
	DeferOperation(context.Context, store.RecoverySnapshot, time.Duration) error
	RecordOperationError(context.Context, store.RecoverySnapshot, string, bool) error
	ClaimBootstrap(context.Context, pgtype.UUID, model.OperationRef) (store.BootstrapClaim, error)
	CheckBootstrapClaim(context.Context, store.BootstrapClaim) (model.Node, error)
	MintBootstrapToken(context.Context, store.BootstrapClaim) (string, int64, error)
	ConfirmBootstrap(context.Context, store.BootstrapClaim, model.Observation) error
	FailBootstrap(context.Context, store.BootstrapClaim, string) error
	ExpireBootstrap(context.Context, store.RecoverySnapshot) error
	ExpireConfirmedCreate(context.Context, store.RecoverySnapshot) error
	GetProfileForNode(context.Context, model.Node) (store.Profile, error)
	FinishDelete(context.Context, pgtype.UUID, int64) error
}

// OperationReviewer is supplied by the process composition using the existing API client.
// Its response is discovery only; the Worker reloads Store authority after review.
type OperationReviewer interface {
	ReviewOperation(context.Context, string, model.OperationRef) (model.Operation, error)
}

type Reconciler struct {
	repo           recoveryStore
	provider       model.Provider
	cfg            model.Config
	reviewer       OperationReviewer
	claimLifecycle func(context.Context, store.RecoverySnapshot) (lifecycleHandle, error)
	claimBootstrap func(context.Context, store.RecoverySnapshot) (bootstrapHandle, error)
	// auroraEnrollment is the process-local handoff supplied by the Fleet
	// composition when the Aurora profile is active. It is read at most once per
	// bootstrap and never persisted.
	auroraEnrollment func(pgtype.UUID) (string, bool)
	mu               sync.Mutex
	observeAfter     pgtype.UUID
	recoverAfter     pgtype.UUID
	initialization   *initializationSlot
	serviceContext   context.Context
	stopping         bool
	running          bool
	clock            func() time.Time
}

func NewReconciler(repo *store.Store, p model.Provider, cfg model.Config) *Reconciler {
	r := &Reconciler{provider: p, cfg: cfg}
	if repo != nil {
		r.repo = repo
		r.claimBootstrap = func(ctx context.Context, s store.RecoverySnapshot) (bootstrapHandle, error) {
			c, e := repo.ClaimBootstrap(ctx, s.Node.OwnerID, s.Ref())
			return sqlBootstrap{repo: repo, claim: c}, e
		}
		r.claimLifecycle = func(ctx context.Context, s store.RecoverySnapshot) (lifecycleHandle, error) {
			c, e := repo.ClaimLifecycle(ctx, s)
			return sqlLifecycle{repo: repo, claim: c}, e
		}
	}
	return r
}
func (r *Reconciler) SetReviewer(reviewer OperationReviewer) { r.reviewer = reviewer }

// SetAuroraEnrollment wires the process-local one-time enrollment handoff. It is
// only consulted when the Aurora managed-sandbox profile is active.
func (r *Reconciler) SetAuroraEnrollment(take func(pgtype.UUID) (string, bool)) {
	r.auroraEnrollment = take
}

// Tick serializes scheduling, not accepted physical I/O. Each lane has its own fixed budget.
func (r *Reconciler) Tick(parent context.Context) error {
	if !r.mu.TryLock() {
		return model.ErrBusy
	}
	defer r.mu.Unlock()
	if r.stopping {
		return context.Canceled
	}
	if r.serviceContext != nil {
		parent = r.serviceContext
	}
	if parent.Err() != nil {
		return parent.Err()
	}
	if r.repo == nil || r.provider == nil {
		return model.ErrUnavailable
	}
	r.reapInitialization()
	ctx, cancel := context.WithTimeout(parent, 24*time.Second)
	defer cancel()
	// Reserve health first. Supported steady load: two idle nodes at <=5s Inspect + <=2s write
	// each, <=2s discovery, <=2s scheduling overhead, <=5s recovery and <=5s cadence.
	// The 18s idle reservation includes slack; supported renewal age remains <30s.
	idle, stopIdle := context.WithTimeout(ctx, 18*time.Second)
	result := r.observeIdle(idle)
	stopIdle()
	recovery, stopRecovery := context.WithTimeout(ctx, 5*time.Second)
	defer stopRecovery()
	end := r.now().Add(5 * time.Second)
	ops, e := r.repo.ListRecoverable(recovery, r.recoverAfter)
	if e != nil {
		return model.ErrUnavailable
	}
	if len(ops) == 0 {
		r.recoverAfter = pgtype.UUID{}
		return result
	}
	for _, op := range ops {
		if recovery.Err() != nil || !r.now().Before(end) {
			break
		}
		if r.initialization != nil && r.initialization.ref.OperationID == op.ID {
			r.recoverAfter = op.ID
			continue
		}
		ref := model.OperationRef{Namespace: r.cfg.Namespace, NodeID: op.NodeID, OperationID: op.ID, Generation: op.Generation, Action: op.Action}
		snap, e := r.repo.CurrentOperation(recovery, op.OwnerID, ref)
		// A row whose lookup never finished remains ahead of the processed cursor.
		if recovery.Err() != nil {
			break
		}
		if errors.Is(e, model.ErrConflict) || errors.Is(e, model.ErrBusy) || errors.Is(e, model.ErrForbidden) {
			r.recoverAfter = op.ID
			continue
		}
		if e != nil {
			result = model.ErrUnavailable
			break
		}
		if snap.Operation.NonRetryable || snap.Operation.Attempts >= 5 {
			r.recoverAfter = op.ID
			continue
		}
		if snap.Operation.Action == model.Create && snap.Node.ContainerID == "" && snap.Operation.BootstrapClaimedAt.IsZero() {
			e = r.startInitialization(recovery, parent, snap)
		} else {
			// Only discovery/Inspect recovery uses the freshness reservation. Accepted physical
			// work shares the sole Worker slot and retains its independent action budgets.
			if snap.Operation.Action == model.Delete || (snap.Operation.Action != model.Create && snap.Operation.ActionClaimedAt.IsZero()) {
				e = r.startRecovery(parent, snap)
			} else {
				e = r.reconcile(recovery, snap)
			}
		}
		// The attempted row is processed even when its bounded I/O exhausted the pass.
		r.recoverAfter = op.ID
		if e != nil {
			result = e
		}
	}
	if len(ops) < 100 && r.recoverAfter == ops[len(ops)-1].ID && recovery.Err() == nil && r.now().Before(end) {
		r.recoverAfter = pgtype.UUID{}
	}
	return result
}

func (r *Reconciler) observeIdle(ctx context.Context) error {
	end := r.now().Add(18 * time.Second)
	nodes, e := r.repo.ListObservable(ctx, r.observeAfter)
	if e != nil {
		return model.ErrUnavailable
	}
	if len(nodes) == 0 {
		r.observeAfter = pgtype.UUID{}
		return nil
	}
	for _, n := range nodes {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !r.now().Before(end) {
			return nil
		}
		// Discovery narrows read-only work; it never authorizes replacing or starting an identity.
		if n.Namespace != r.cfg.Namespace || n.ContainerID == "" || n.Maintenance || n.Revoked || n.Desired != "running" {
			r.observeAfter = n.ID
			continue
		}
		c, cancel := context.WithTimeout(ctx, 5*time.Second)
		o, e := r.provider.Inspect(c, n)
		cancel()
		r.observeAfter = n.ID
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Only genuinely healthy results renew a healthy lease. Unknown running health ages out.
		// Ready writes are rejected by Store on intervening maintenance/revocation/generation changes.
		if e != nil {
			continue
		}
		if !workerNativeReady(n, o) {
			// Fresh native stopped/missing disables readiness; unknown running health is not renewed.
			if o.Offline || (o.Status != "stopped" && o.Status != "missing") {
				continue
			}
			o.Ready = false
		}
		if e = r.repo.RecordObservation(ctx, n.ID, n.Generation, o); e != nil && !errors.Is(e, model.ErrConflict) && !errors.Is(e, model.ErrUnknownHealth) {
			return model.ErrUnavailable
		}
	}
	// A fully processed short page reached the tail; wrap without spending a blank health Tick.
	if len(nodes) < 100 {
		r.observeAfter = pgtype.UUID{}
	}
	return nil
}

func (r *Reconciler) current(ctx context.Context, s store.RecoverySnapshot) (store.RecoverySnapshot, error) {
	fresh, e := r.repo.CurrentOperation(ctx, s.Node.OwnerID, s.Ref())
	if e != nil {
		return fresh, e
	}
	if !sameWorkerBinding(s, fresh) {
		return fresh, model.ErrConflict
	}
	return fresh, nil
}
func sameWorkerBinding(a, b store.RecoverySnapshot) bool {
	n, m := a.Node, b.Node
	x, y := a.Operation, b.Operation
	return a.Ref() == b.Ref() && n.OwnerID == m.OwnerID && n.Generation == m.Generation && n.ContainerID == m.ContainerID && n.StartEpoch == m.StartEpoch && n.DaemonID == m.DaemonID && n.Image == m.Image && n.Resources == m.Resources && n.ProfileRef == m.ProfileRef && n.DataVolume == m.DataVolume && n.SecretsVolume == m.SecretsVolume && n.Desired == m.Desired && n.Maintenance == m.Maintenance && n.Revoked == m.Revoked && x.Phase == y.Phase && x.Approved == y.Approved && x.Attempts == y.Attempts && x.NonRetryable == y.NonRetryable && x.BootstrapClaimedAt.Equal(y.BootstrapClaimedAt) && x.ActionClaimedAt.Equal(y.ActionClaimedAt) && x.ActionStartEpoch == y.ActionStartEpoch
}
func (r *Reconciler) reconcile(ctx context.Context, s store.RecoverySnapshot) error {
	op, n := s.Operation, s.Node
	if (op.Action == model.Stop || op.Action == model.Reboot || op.Action == model.Delete) && !op.Approved {
		if r.reviewer == nil {
			return r.recordError(ctx, s, model.ErrUnavailable)
		}
		c, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, e := r.reviewer.ReviewOperation(c, util.UUIDToString(n.OwnerID), s.Ref())
		cancel()
		// The response never authorizes execution. Reload the original SQL binding even on Unknown.
		fresh, loadErr := r.repo.CurrentOperation(ctx, n.OwnerID, s.Ref())
		if loadErr != nil {
			return nil
		}
		if e != nil {
			return r.recordError(ctx, fresh, e)
		}
		if !fresh.Operation.Approved {
			return r.repo.DeferOperation(ctx, fresh, 5*time.Second)
		}
		s = fresh
		op, n = s.Operation, s.Node
	}
	if !CanApplyOperation(op) {
		return nil
	}
	if op.Action == model.Create && n.ContainerID == "" {
		if !op.BootstrapClaimedAt.IsZero() {
			if op.BootstrapClaimedAt.After(s.SQLNow) || s.SQLNow.Before(op.BootstrapClaimedAt.Add(5*time.Minute)) {
				return nil
			}
			fresh, e := r.current(ctx, s)
			if e != nil {
				return nil
			}
			return r.repo.ExpireBootstrap(ctx, fresh)
		}
		return model.ErrBusy // Fresh initialization is admitted only by the owned scheduling slot.
	}
	if op.Action == model.Create && !s.BootstrapSucceeded && !op.BootstrapClaimedAt.IsZero() && !op.BootstrapClaimedAt.After(s.SQLNow) && !s.SQLNow.Before(op.BootstrapClaimedAt.Add(5*time.Minute)) {
		fresh, e := r.current(ctx, s)
		if e != nil {
			return nil
		}
		return r.repo.ExpireConfirmedCreate(ctx, fresh)
	}
	if op.Action == model.Delete {
		fresh, e := r.current(ctx, s)
		if e != nil {
			return nil
		}
		c, cancel := context.WithTimeout(ctx, 30*time.Second)
		e = r.provider.Delete(c, fresh.Node, fresh.Ref())
		cancel()
		if e != nil {
			return r.recordError(ctx, fresh, e)
		}
		// Canonical Delete proves all resources absent/data-last; no cached diagnostic substitutes.
		if _, e = r.current(ctx, fresh); e != nil {
			return nil
		}
		return r.repo.FinishDelete(ctx, fresh.Operation.ID, fresh.Operation.Generation)
	}
	if op.Action != model.Create {
		return r.lifecycle(ctx, s)
	}
	return r.inspect(ctx, s)
}

// lifecycleHandle keeps the actual private Store claim opaque. Fakes exercise orchestration,
// while Store tests own the SQL/clock binding; a projection cannot construct sqlLifecycle.
type lifecycleHandle interface {
	Context(context.Context) (context.Context, context.CancelFunc)
	Snapshot() store.RecoverySnapshot
	Check(context.Context) (model.Node, error)
	Result(context.Context, model.Observation) error
}
type sqlLifecycle struct {
	repo  *store.Store
	claim store.LifecycleClaim
}

func (c sqlLifecycle) Context(ctx context.Context) (context.Context, context.CancelFunc) {
	return c.claim.Context(ctx)
}
func (c sqlLifecycle) Snapshot() store.RecoverySnapshot { return c.claim.Snapshot() }
func (c sqlLifecycle) Check(ctx context.Context) (model.Node, error) {
	return c.repo.CheckLifecycleClaim(ctx, c.claim)
}
func (c sqlLifecycle) Result(ctx context.Context, o model.Observation) error {
	return c.repo.RecordLifecycleResult(ctx, c.claim, o)
}
func (r *Reconciler) lifecycle(ctx context.Context, s store.RecoverySnapshot) error {
	op := s.Operation
	if !op.ActionClaimedAt.IsZero() {
		if op.ActionClaimedAt.After(s.SQLNow) || s.SQLNow.Before(op.ActionClaimedAt.Add(5*time.Second)) {
			return nil
		}
		return r.inspect(ctx, s)
	}
	// A queued intent needs fresh proof before checkpoint; unknown proof never consumes a dispatch.
	if op.Action == model.Stop || op.Action == model.Reboot {
		fresh, e := r.current(ctx, s)
		if e != nil {
			return nil
		}
		c, cancel := context.WithTimeout(ctx, 5*time.Second)
		o, e := r.provider.Diagnose(c, fresh.Node, fresh.Ref())
		cancel()
		if e != nil {
			return r.recordError(ctx, fresh, e)
		}
		if !o.ReportStatsKnown || o.Offline || !o.Ready || o.ActiveRuns != 0 || o.PendingReports != 0 || o.FailedReports != 0 {
			return r.repo.DeferOperation(ctx, fresh, 5*time.Second)
		}
		s, e = r.current(ctx, fresh)
		if e != nil {
			return nil
		}
	}
	if r.claimLifecycle == nil {
		return model.ErrUnavailable
	}
	claim, e := r.claimLifecycle(ctx, s)
	if e != nil {
		if errors.Is(e, model.ErrConflict) {
			return nil
		}
		return r.recordError(ctx, s, e)
	}
	c, cancel := claim.Context(ctx)
	defer cancel()
	n, e := claim.Check(c)
	if e != nil || c.Err() != nil {
		return nil
	}
	o, e := r.provider.Apply(c, n, op.Action)
	if e != nil {
		return r.recordError(ctx, claim.Snapshot(), e)
	}
	if c.Err() != nil {
		return nil
	}
	// Result is checked using the same private live claimant, never a reconstructed snapshot.
	health := claim.Snapshot().Node
	health.StartEpoch = ""
	health.Maintenance = false
	o.Ready = workerNativeReady(health, o)
	if e = claim.Result(c, o); e != nil {
		return r.recordError(ctx, claim.Snapshot(), e)
	}
	return nil
}

func (r *Reconciler) inspect(ctx context.Context, s store.RecoverySnapshot) error {
	fresh, e := r.current(ctx, s)
	if e != nil {
		return nil
	}
	c, cancel := context.WithTimeout(ctx, 30*time.Second)
	readNode := fresh.Node
	// Only the approved original expired start/reboot checkpoint can discover a changed native epoch.
	// Keep the SQL snapshot/old epoch intact; RecordOperationResult alone can commit the proved new epoch.
	if fresh.Operation.Approved && fresh.Operation.Phase == "applying" && !fresh.Operation.ActionClaimedAt.IsZero() && !fresh.Operation.ActionClaimedAt.After(fresh.SQLNow) && !fresh.SQLNow.Before(fresh.Operation.ActionClaimedAt.Add(5*time.Second)) && (fresh.Operation.Action == model.Start || fresh.Operation.Action == model.Reboot) {
		readNode.StartEpoch = ""
	}
	o, e := r.provider.Inspect(c, readNode)
	cancel()
	if e != nil {
		return r.recordError(ctx, fresh, e)
	}
	current, e := r.current(ctx, fresh)
	if e != nil {
		return nil
	}
	if o.Status == "missing" {
		if current.Operation.Action == model.Stop {
			return r.repo.RecordOperationResult(ctx, current, o)
		}
		if e = r.repo.RecordObservation(ctx, current.Node.ID, current.Node.Generation, o); e != nil {
			return e
		}
		return r.repo.RecordOperationError(ctx, current, "instance-lost", true)
	}
	if current.Operation.Action == model.Create && !current.BootstrapSucceeded && !current.Operation.BootstrapClaimedAt.IsZero() && !current.Operation.BootstrapClaimedAt.After(current.SQLNow) && !current.SQLNow.Before(current.Operation.BootstrapClaimedAt.Add(5*time.Minute)) {
		return r.repo.ExpireConfirmedCreate(ctx, current)
	}
	health := current
	if current.Operation.Action == model.Start || current.Operation.Action == model.Reboot {
		health.Node.StartEpoch = ""
		health.Node.Maintenance = false
	}
	o = workerHealth(health, o)
	if e = r.repo.RecordOperationResult(ctx, current, o); e != nil {
		return r.recordError(ctx, current, e)
	}
	return nil
}
func workerNativeReady(n model.Node, o model.Observation) bool {
	claude := false
	for _, agent := range o.Agents {
		claude = claude || strings.EqualFold(agent, "claude")
	}
	return !n.Maintenance && !n.Revoked && n.Desired == "running" && o.Ready && o.Status == "running" && !o.Offline && o.ContainerID == n.ContainerID && o.DaemonID == n.DaemonID && o.StartEpoch != "" && (n.StartEpoch == "" || o.StartEpoch == n.StartEpoch) && claude && o.RuntimeCount > 0
}
func workerHealth(s store.RecoverySnapshot, o model.Observation) model.Observation {
	o.Ready = workerNativeReady(s.Node, o) && !o.ObservedAt.IsZero() && !o.ObservedAt.After(s.SQLNow) && s.SQLNow.Sub(o.ObservedAt) <= 30*time.Second
	return o
}
func (r *Reconciler) recordError(ctx context.Context, s store.RecoverySnapshot, e error) error {
	if errors.Is(e, model.ErrConflict) || errors.Is(e, context.Canceled) {
		return nil
	}
	if errors.Is(e, model.ErrUnknownHealth) || errors.Is(e, model.ErrBusy) {
		if maintenanceAction := s.Operation.Action == model.Delete || s.Operation.Action == model.Stop || s.Operation.Action == model.Reboot; maintenanceAction && errors.Is(e, model.ErrUnknownHealth) {
			fresh, err := r.current(ctx, s)
			if err != nil {
				return nil
			}
			c, cancel := context.WithTimeout(ctx, 5*time.Second)
			o, _ := r.provider.Diagnose(c, fresh.Node, fresh.Ref())
			cancel()
			if !o.ObservedAt.IsZero() {
				current, err := r.current(ctx, fresh)
				if err != nil {
					return nil
				}
				o = workerHealth(current, o)
				if err = r.repo.RecordObservation(ctx, current.Node.ID, current.Node.Generation, o); err != nil && !errors.Is(err, model.ErrConflict) {
					return err
				}
				s = current
			}
		}
		return r.repo.DeferOperation(ctx, s, 5*time.Second)
	}
	code, permanent := recoveryError(e)
	// This is the Fleet's only failure surface. The operation row keeps just the
	// code, so without a line here the cause -- a rejected image environment, a
	// missing image, a Docker error -- is lost and a failed node can only be
	// diagnosed by reconstructing state from the database. The default slog
	// handler writes to stderr, which the container's json-file driver captures.
	slog.WarnContext(ctx, "fleet operation failed",
		"namespace", r.cfg.Namespace,
		"node_id", util.UUIDToString(s.Node.ID),
		"action", string(s.Operation.Action),
		"generation", s.Operation.Generation,
		"error_code", code,
		"permanent", permanent,
		"error", e)
	return r.repo.RecordOperationError(ctx, s, code, permanent)
}
func recoveryError(e error) (string, bool) {
	if errors.Is(e, model.ErrForbidden) {
		return "forbidden", true
	}
	if errors.Is(e, model.ErrInvalidRequest) || errors.Is(e, model.ErrProfileMissing) {
		return "configuration", true
	}
	return "unavailable", false
}

// bootstrapHandle mocks choreography only; sqlBootstrap always carries the private ClaimBootstrap winner.
type bootstrapHandle interface {
	Context(context.Context) (context.Context, context.CancelFunc)
	Snapshot() store.RecoverySnapshot
	Check(context.Context) (model.Node, error)
	Mint(context.Context) (string, int64, error)
	Mark(context.Context) error
	Confirm(context.Context, model.Observation) error
	Fail(context.Context, string) error
}
type sqlBootstrap struct {
	repo  *store.Store
	claim store.BootstrapClaim
}

func (c sqlBootstrap) Context(ctx context.Context) (context.Context, context.CancelFunc) {
	return c.claim.Context(ctx)
}
func (c sqlBootstrap) Snapshot() store.RecoverySnapshot { return c.claim.Snapshot() }
func (c sqlBootstrap) Check(ctx context.Context) (model.Node, error) {
	return c.repo.CheckBootstrapClaim(ctx, c.claim)
}
func (c sqlBootstrap) Mint(ctx context.Context) (string, int64, error) {
	return c.repo.MintBootstrapToken(ctx, c.claim)
}
func (c sqlBootstrap) Mark(ctx context.Context) error {
	return c.repo.MarkBootstrapMinted(ctx, c.claim)
}
func (c sqlBootstrap) Confirm(ctx context.Context, o model.Observation) error {
	return c.repo.ConfirmBootstrap(ctx, c.claim, o)
}
func (c sqlBootstrap) Fail(ctx context.Context, code string) error {
	return c.repo.FailBootstrap(ctx, c.claim, code)
}

// auroraBootstrap takes the one-time enrollment secret the provision route
// handed to this process and builds the managed-sandbox bootstrap. The secret
// is consumed exactly once: a missing, foreign or malformed value is a normal
// bootstrap failure, never a Fleet-minted node token and never a retry loop.
func (r *Reconciler) auroraBootstrap(ctx context.Context, n model.Node) (model.Bootstrap, error) {
	if r.cfg.Aurora == nil || r.auroraEnrollment == nil {
		return model.Bootstrap{}, model.ErrInvalidRequest
	}
	token, ok := r.auroraEnrollment(n.ID)
	if !ok || !model.ValidEnrollmentToken(token) {
		return model.Bootstrap{}, model.ErrUnavailable
	}
	return model.Bootstrap{DaemonID: n.DaemonID, EnrollmentToken: token, ServerURL: r.cfg.Aurora.ServerURL}, nil
}

func (r *Reconciler) initialize(ctx context.Context, s store.RecoverySnapshot, claim bootstrapHandle) error {
	c, cancel := claim.Context(ctx)
	defer cancel()
	fail := func(e error) error {
		code, _ := recoveryError(e)
		if c.Err() != nil {
			return nil
		}
		return claim.Fail(c, code)
	}
	n, e := claim.Check(c)
	if e != nil {
		return nil
	}
	var b model.Bootstrap
	if r.cfg.Aurora != nil {
		// Aurora nodes carry no credential profile and never a Fleet-minted node
		// token. The only bootstrap input is the one-time enrollment secret from
		// the provision handoff. Recheck the claim immediately before consuming
		// it, because taking the secret is irreversible; a missing or malformed
		// secret fails the bootstrap the normal way (no guess, no retry timer).
		if _, e = claim.Check(c); e != nil {
			return nil
		}
		// Aurora has no Fleet node token to mint, but ConfirmBootstrap's SQL/lease
		// fence requires the operation to be marked before Ensure. Marking writes no
		// credential row and keeps the same claim/generation/lease authority.
		if e = claim.Mark(c); e != nil {
			return fail(e)
		}
		b, e = r.auroraBootstrap(c, n)
		if e != nil {
			return fail(e)
		}
	} else {
		profile, e := r.repo.GetProfileForNode(c, n)
		if e != nil {
			return fail(e)
		}
		// Check immediately before reading private bytes, not just before SQL routing lookup.
		n, e = claim.Check(c)
		if e != nil {
			return nil
		}
		b, e = LoadProfile(profile.Ref)
		if e != nil {
			return fail(e)
		}
		if _, e = claim.Check(c); e != nil {
			return nil
		}
		token, _, e := claim.Mint(c)
		if e != nil {
			return fail(e)
		}
		b.NodeToken = token
		b.DaemonID = n.DaemonID
		b.ServerURL = r.cfg.APIURL
	}
	n, e = claim.Check(c)
	if e != nil {
		return nil
	}
	o, e := r.provider.Ensure(c, n, b)
	if e != nil {
		return fail(e)
	}
	// There is exactly one Ensure per winning process. Confirmation is still SQL/current-claim fenced.
	health := claim.Snapshot().Node
	health.ContainerID = o.ContainerID
	o.Ready = workerNativeReady(health, o)
	if e = claim.Confirm(c, o); e != nil {
		return fail(e)
	}
	fresh, e := r.repo.CurrentOperation(ctx, n.OwnerID, s.Ref())
	if e != nil {
		return nil
	}
	o = workerHealth(fresh, o)
	return r.repo.RecordOperationResult(ctx, fresh, o)
}

// Run owns a service context; request/browser cancellation never owns accepted intents.
func (r *Reconciler) Run(parent context.Context) error {
	r.mu.Lock()
	if r.running || r.stopping {
		r.mu.Unlock()
		return model.ErrBusy
	}
	ctx, cancel := context.WithCancel(parent)
	r.running = true
	r.serviceContext = ctx
	r.mu.Unlock()
	defer func() { cancel(); r.closeInitialization() }()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		_ = r.Tick(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// CanApplyOperation classifies eligibility only. SQL current-binding checks supply authority.
func CanApplyOperation(op model.Operation) bool {
	if op.Phase != "queued" && op.Phase != "prepared" && op.Phase != "applying" {
		return false
	}
	switch op.Action {
	case model.Create, model.Start:
		return true
	case model.Stop, model.Reboot, model.Delete:
		return op.Approved
	default:
		return false
	}
}
