package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/multica-ai/multica/server/internal/fleet/model"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Confirmed timeout eligibility uses the SQL clock and immutable checkpoint, not mutable errors/time.
func TestRecoveryConfirmedCreateSQLDeadlineMatrix(t *testing.T) {
	now := time.Date(2026, 10, 5, 0, 5, 0, 0, time.UTC)
	good := RecoverySnapshot{SQLNow: now, Node: model.Node{Generation: 3, Desired: "running", ContainerID: "cid", DataVolume: "data"}, Operation: model.Operation{Generation: 3, Action: model.Create, Phase: "applying", BootstrapMinted: true, BootstrapClaimedAt: now.Add(-5 * time.Minute)}}
	if !confirmedCreateExpired(good) {
		t.Fatal("original deadline not eligible")
	}
	for _, name := range []string{"before-deadline", "future-checkpoint", "no-checkpoint", "unminted", "unconfirmed", "new-generation", "completed", "nonretryable", "revoked", "maintenance", "not-running"} {
		t.Run(name, func(t *testing.T) {
			s := good
			switch name {
			case "before-deadline":
				s.SQLNow = now.Add(-time.Nanosecond)
			case "future-checkpoint":
				s.Operation.BootstrapClaimedAt = now.Add(time.Second)
			case "no-checkpoint":
				s.Operation.BootstrapClaimedAt = time.Time{}
			case "unminted":
				s.Operation.BootstrapMinted = false
			case "unconfirmed":
				s.Node.ContainerID = ""
			case "new-generation":
				s.Node.Generation++
			case "completed":
				s.Operation.Phase = "completed"
			case "nonretryable":
				s.Operation.NonRetryable = true
			case "revoked":
				s.Node.Revoked = true
			case "maintenance":
				s.Node.Maintenance = true
			case "not-running":
				s.Node.Desired = "stopped"
			}
			if confirmedCreateExpired(s) {
				t.Fatal("invalid timeout eligible")
			}
		})
	}
	good.Operation.UpdatedAt = now.Add(time.Hour)
	good.Operation.ErrorCode = "unavailable"
	if !confirmedCreateExpired(good) {
		t.Fatal("mutable error refreshed deadline")
	}
}
func TestRecoveryConfirmedCreateLateResultNoSQLWrites(t *testing.T) {
	now := time.Date(2026, 10, 5, 0, 5, 0, 0, time.UTC)
	s := RecoverySnapshot{SQLNow: now, Node: model.Node{Generation: 3, Desired: "running", ContainerID: "cid", DaemonID: "daemon", StartEpoch: "epoch"}, Operation: model.Operation{Generation: 3, Action: model.Create, Phase: "applying", BootstrapMinted: true, BootstrapClaimedAt: now.Add(-5 * time.Minute), CreatedAt: now.Add(-6 * time.Minute)}}
	o := model.Observation{ContainerID: "cid", DaemonID: "daemon", StartEpoch: "epoch", Status: "running", Ready: true, Agents: []string{"claude"}, RuntimeCount: 1, ObservedAt: now}
	c := &recoveryCapture{}
	if e := New(nil, "owned").persistOperationResult(context.Background(), db.New(c), s, o, false); !errors.Is(e, model.ErrConflict) || c.sql != "" {
		t.Fatal("late healthy create performed SQL writes")
	}
	s.SQLNow = now.Add(-time.Nanosecond)
	o.ObservedAt = s.SQLNow
	if e := New(nil, "owned").persistOperationResult(context.Background(), db.New(c), s, o, false); e != nil {
		t.Fatalf("predeadline completion denied: %v", e)
	}
}
func TestRecoveryConfirmedTimeoutCompiledCAS(t *testing.T) {
	c := &recoveryCapture{}
	q := db.New(c)
	owner := uuid(t, "10000000-0000-0000-0000-000000000001")
	node := uuid(t, "20000000-0000-0000-0000-000000000001")
	op := uuid(t, "30000000-0000-0000-0000-000000000001")
	stamp := timestamp(time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC))
	_, e := q.FleetExpireConfirmedCreateNode(context.Background(), db.FleetExpireConfirmedCreateNodeParams{Namespace: "owned", OwnerID: owner, NodeID: node, OperationID: op, Generation: 3, ContainerID: "cid", ClaimedAt: stamp})
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(c.args, []any{"owned", owner, node, int64(3), "cid", op, stamp}) {
		t.Fatalf("unbound current timeout: %v", c.args)
	}
	for _, guard := range []string{"n.container_id=", "n.generation=", "NOT n.revoked", "NOT n.maintenance", "o.id=", "o.action='create'", "o.phase='applying'", "o.bootstrap_minted", "NOT o.non_retryable", "o.bootstrap_claimed_at=", "interval '5 minutes'<=clock_timestamp()", "initialization-timeout"} {
		if !strings.Contains(c.sql, guard) {
			t.Fatalf("missing timeout guard %s", guard)
		}
	}
	for _, forbidden := range []string{"container_id='',", "data_volume=", "phase='failed'", "phase='completed'"} {
		if strings.Contains(c.sql, forbidden) {
			t.Fatal("timeout cleared identity/data/barrier")
		}
	}
	_, e = q.FleetExpireConfirmedCreateOperation(context.Background(), db.FleetExpireConfirmedCreateOperationParams{Namespace: "owned", OwnerID: owner, NodeID: node, OperationID: op, Generation: 3, ClaimedAt: stamp})
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(c.args, []any{"owned", owner, node, op, int64(3), stamp}) || !strings.Contains(c.sql, "non_retryable=true") || !strings.Contains(c.sql, "bootstrap_minted") {
		t.Fatal("timeout operation binding lost")
	}
}

// Even a transaction beginning before expiry must not commit completion after SQL-clock expiry.
func TestRecoveryConfirmedCompletionClockCrossingCAS(t *testing.T) {
	c := &recoveryCapture{}
	_, _ = db.New(c).FleetCompleteOperation(context.Background(), db.FleetCompleteOperationParams{Namespace: "owned", Action: "create", Phase: "applying", Generation: 3})
	if !strings.Contains(c.sql, "bootstrap_claimed_at+interval '5 minutes'>clock_timestamp()") {
		t.Fatal("completion CAS can cross original SQL initialization deadline")
	}
}
func TestRecoveryProcessedCursorCompiledQuery(t *testing.T) {
	c := &lifecycleSQLCapture{}
	after := uuid(t, "30000000-0000-0000-0000-000000000099")
	_, _ = db.New(c).ListFleetRecoverable(context.Background(), db.ListFleetRecoverableParams{Namespace: "owned", AfterID: after})
	if !reflect.DeepEqual(c.args, []any{"owned", after}) {
		t.Fatal("processed cursor/namespace not bound")
	}
	for _, guard := range []string{"o.id>", "ORDER BY o.id LIMIT 100", "n.generation=o.generation", "NOT o.non_retryable", "o.attempts<5", "o.next_attempt_at", "bootstrap_claimed_at"} {
		if !strings.Contains(c.sql, guard) {
			t.Fatalf("missing recovery discovery guard %s", guard)
		}
	}
}

// Confirmation requires a post-checkpoint provider result, not merely a token or cached identity.
func TestRecoveryBootstrapResultValidation(t *testing.T) {
	now := time.Date(2026, 10, 4, 0, 1, 0, 0, time.UTC)
	snap := RecoverySnapshot{SQLNow: now, Node: model.Node{Generation: 1, Desired: "running", DaemonID: "daemon", DataVolume: "data"}, Operation: model.Operation{BootstrapMinted: true, BootstrapClaimedAt: now.Add(-time.Second)}}
	good := model.Observation{ContainerID: "cid", Status: "running", DaemonID: "daemon", StartEpoch: "epoch", Ready: true, ObservedAt: now, ReportStatsKnown: true}
	if n, e := bootstrapObservation(snap, good); e != nil || n.ContainerID != "cid" {
		t.Fatalf("fresh confirmation=%+v err=%v", n, e)
	}
	for _, tc := range []struct {
		name   string
		change func(*RecoverySnapshot, *model.Observation)
	}{
		{"before-mint", func(s *RecoverySnapshot, o *model.Observation) { s.Operation.BootstrapMinted = false }},
		{"cached-before-checkpoint", func(s *RecoverySnapshot, o *model.Observation) { o.ObservedAt = now.Add(-4 * time.Second) }},
		{"offline", func(s *RecoverySnapshot, o *model.Observation) { o.Offline = true }},
		{"missing", func(s *RecoverySnapshot, o *model.Observation) { o.Status = "missing" }},
		{"no-confirmed-id", func(s *RecoverySnapshot, o *model.Observation) { o.ContainerID = "" }},
		{"wrong-daemon", func(s *RecoverySnapshot, o *model.Observation) { o.DaemonID = "other" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, o := snap, good
			tc.change(&s, &o)
			if _, e := bootstrapObservation(s, o); e == nil {
				t.Fatal("unsafe bootstrap result accepted")
			}
		})
	}
}

// Recovery snapshots are not claimant authority: only the live, original winner handle can continue.
func TestRecoveryBootstrapClaimAuthority(t *testing.T) {
	now := time.Date(2026, 10, 4, 0, 1, 0, 0, time.UTC)
	n := model.Node{Generation: 3, Desired: "running", ContainerID: ""}
	op := model.Operation{Generation: 3, Action: model.Create, Phase: "applying", BootstrapClaimedAt: now.Add(-time.Minute)}
	snap := RecoverySnapshot{Node: n, Operation: op, SQLNow: now}
	claim := BootstrapClaim{snapshot: snap, claimedAt: op.BootstrapClaimedAt, deadline: now.Add(4 * time.Minute)}
	if !validBootstrapClaim(claim, snap, now) {
		t.Fatal("winner not authorized")
	}
	for _, tc := range []struct {
		name   string
		change func(*BootstrapClaim, *RecoverySnapshot)
		local  time.Time
	}{
		{"reconstructed-projection", func(c *BootstrapClaim, s *RecoverySnapshot) { c.claimedAt = time.Time{} }, now},
		{"different-claim", func(c *BootstrapClaim, s *RecoverySnapshot) {
			s.Operation.BootstrapClaimedAt = now.Add(-30 * time.Second)
		}, now},
		{"new-generation", func(c *BootstrapClaim, s *RecoverySnapshot) { s.Node.Generation = 4 }, now},
		{"confirmed", func(c *BootstrapClaim, s *RecoverySnapshot) { s.Node.ContainerID = "confirmed" }, now},
		{"sql-expired", func(c *BootstrapClaim, s *RecoverySnapshot) { s.SQLNow = now.Add(4 * time.Minute) }, now},
		{"local-deadline", func(c *BootstrapClaim, s *RecoverySnapshot) {}, now.Add(4 * time.Minute)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, s := claim, snap
			tc.change(&c, &s)
			if validBootstrapClaim(c, s, tc.local) {
				t.Fatal("stale or competing claimant accepted")
			}
		})
	}
	ctx, cancel := claim.Context(context.Background())
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok || !deadline.Equal(claim.deadline) {
		t.Fatal("claim context extended deadline")
	}
}

// Durable read/write must round-trip known diagnostics; malformed or partial payloads never become known-zero.
func TestRecoveryDurableObservationRoundTrip(t *testing.T) {
	now := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	n := model.Node{Namespace: "owned", Generation: 3, ContainerID: "cid", DaemonID: "daemon", StartEpoch: "epoch", DataVolume: "data", Status: "stopped"}
	o := model.Observation{Offline: true, Status: "stopped", DataVolume: "data", LayoutVersion: "1", ReportStatsKnown: true, ObservedAt: now}
	c := &recoveryCapture{}
	if e := persistObservation(context.Background(), db.New(c), n, o); e != nil {
		t.Fatal(e)
	}
	raw, ok := c.args[0].([]byte)
	if !ok {
		t.Fatal("missing observation statement binding")
	}
	got, e := decodeObservation(raw, n)
	if e != nil || !got.ReportStatsKnown || !got.Offline || got.DataVolume != "data" || got.LayoutVersion != "1" || !got.ObservedAt.Equal(now) {
		t.Fatalf("round-trip=%+v err=%v", got, e)
	}
	var d map[string]json.RawMessage
	if e = json.Unmarshal(raw, &d); e != nil {
		t.Fatal(e)
	}
	var nested map[string]json.RawMessage
	if e = json.Unmarshal(d["observation"], &nested); e != nil {
		t.Fatal(e)
	}
	delete(nested, "PendingReports")
	d["observation"], _ = json.Marshal(nested)
	broken, _ := json.Marshal(d)
	if got, e = decodeObservation(broken, n); e == nil || got.ReportStatsKnown {
		t.Fatal("missing required count became known-zero")
	}
	for _, raw := range []string{"{}", ""} {
		got, e = decodeObservation([]byte(raw), n)
		if e != nil || got.ReportStatsKnown || got.Offline {
			t.Fatal("legacy row not unknown")
		}
	}
	n.Generation = 4
	got, e = decodeObservation(c.args[0].([]byte), n)
	if e != nil || got.ReportStatsKnown {
		t.Fatal("stale generation diagnostic reused")
	}
	want := []any{n.Namespace, n.OwnerID, n.ID, int64(3)}
	if !reflect.DeepEqual(c.args[8:], want) {
		t.Fatalf("observation identity arguments=%v", c.args[8:])
	}
}

// Omitting immutable image/volume/epoch/owner fences lets a stale physical result change newer SQL truth.
func TestRecoveryResultBinding(t *testing.T) {
	n := model.Node{ID: uuid(t, "10000000-0000-0000-0000-000000000001"), OwnerID: uuid(t, "20000000-0000-0000-0000-000000000001"), Namespace: "owned", Generation: 3, ContainerID: "cid", StartEpoch: "epoch", DaemonID: "daemon", DataVolume: "data", SecretsVolume: "secret", Image: "image", ProfileRef: "profile", Desired: "running"}
	if !sameRecoveryNode(n, n) {
		t.Fatal("identical binding rejected")
	}
	for _, mutate := range []func(*model.Node){func(n *model.Node) { n.Generation++ }, func(n *model.Node) { n.StartEpoch = "new" }, func(n *model.Node) { n.ContainerID = "new" }, func(n *model.Node) { n.DataVolume = "new" }, func(n *model.Node) { n.SecretsVolume = "new" }, func(n *model.Node) { n.Image = "new" }, func(n *model.Node) { n.ProfileRef = "new" }, func(n *model.Node) { n.Revoked = true }, func(n *model.Node) { n.Maintenance = true }, func(n *model.Node) { n.Namespace = "other" }, func(n *model.Node) { n.OwnerID = n.ID }, func(n *model.Node) { n.Resources.MaxRuns = 2 }} {
		other := n
		mutate(&other)
		if sameRecoveryNode(n, other) {
			t.Fatal("accepted competing binding")
		}
	}
}

// Arithmetic adjacent to sqlc parameters must be bound, not sent as PostgreSQL operators.
type recoveryCapture struct {
	sql  string
	args []any
	tag  string
	err  error
}

func (c *recoveryCapture) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	c.sql = sql
	c.args = args
	tag := c.tag
	if tag == "" {
		tag = "UPDATE 1"
	}
	return pgconn.NewCommandTag(tag), c.err
}
func (c *recoveryCapture) Query(context.Context, string, ...any) (pgx.Rows, error) {
	panic("unexpected query")
}
func (c *recoveryCapture) QueryRow(context.Context, string, ...any) pgx.Row { panic("unexpected row") }
func TestRecoveryStatementBindings(t *testing.T) {
	c := &recoveryCapture{}
	if _, e := db.New(c).FleetScheduleOperation(context.Background(), db.FleetScheduleOperationParams{Namespace: "owned", Phase: "preparing", Action: "delete", Attempts: 2, AttemptIncrement: 1, DelaySeconds: 5, ErrorCode: "unavailable", Generation: 3}); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(c.sql, "@") || len(c.args) != 13 {
		t.Fatalf("unbound statement args=%d SQL=%s", len(c.args), c.sql)
	}
	if !reflect.DeepEqual(c.args[:5], []any{int32(1), false, "unavailable", int32(5), "owned"}) || !reflect.DeepEqual(c.args[8:], []any{int64(3), "delete", "preparing", false, int32(2)}) {
		t.Fatalf("scheduled disposition binding=%v", c.args)
	}
	now := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	stamp := timestamp(now)
	owner := uuid(t, "10000000-0000-0000-0000-000000000001")
	node := uuid(t, "20000000-0000-0000-0000-000000000001")
	op := uuid(t, "30000000-0000-0000-0000-000000000001")
	if _, e := db.New(c).FleetConfirmBootstrap(context.Background(), db.FleetConfirmBootstrapParams{ContainerID: "cid", StartEpoch: "epoch", Status: "starting", Namespace: "owned", OwnerID: owner, NodeID: node, Generation: 3, OperationID: op, ClaimedAt: stamp}); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(c.args, []any{"cid", "epoch", "starting", []byte(nil), "owned", owner, node, int64(3), op, stamp}) {
		t.Fatalf("confirmation fence binding=%v", c.args)
	}
	if _, e := db.New(c).FleetExpireBootstrapNode(context.Background(), db.FleetExpireBootstrapNodeParams{Namespace: "owned", OwnerID: owner, NodeID: node, Generation: 3, OperationID: op, ClaimedAt: stamp}); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(c.args, []any{"owned", owner, node, int64(3), op, stamp}) {
		t.Fatalf("revocation fence binding=%v", c.args)
	}
}
func TestRecoveryObservationCASFailure(t *testing.T) {
	n := model.Node{Namespace: "owned", Generation: 1}
	o := model.Observation{Status: "missing", ObservedAt: time.Now()}
	if e := persistObservation(context.Background(), db.New(&recoveryCapture{tag: "UPDATE 0"}), n, o); !errors.Is(e, model.ErrConflict) {
		t.Fatalf("lost observation CAS accepted: %v", e)
	}
	sentinel := errors.New("owned SQL failure")
	if e := persistObservation(context.Background(), db.New(&recoveryCapture{err: sentinel}), n, o); !errors.Is(e, sentinel) {
		t.Fatalf("SQL error ignored: %v", e)
	}
}

// A live claimant must not be recovered; updates never renew its immutable deadline.
func TestRecoveryBootstrapLeaseClock(t *testing.T) {
	now := time.Date(2026, 10, 4, 0, 5, 0, 0, time.UTC)
	n := model.Node{Generation: 3, Desired: "running"}
	op := model.Operation{Action: model.Create, Generation: 3, Phase: "applying", BootstrapClaimedAt: now.Add(-time.Minute)}
	cases := []struct {
		name      string
		node      func(*model.Node)
		operation func(*model.Operation)
		want      string
	}{
		{"live", func(n *model.Node) {}, func(o *model.Operation) {}, "live"},
		{"expiry", func(n *model.Node) {}, func(o *model.Operation) { o.BootstrapClaimedAt = now.Add(-5 * time.Minute) }, "expired"},
		{"error-does-not-renew", func(n *model.Node) {}, func(o *model.Operation) {
			o.BootstrapClaimedAt = now.Add(-6 * time.Minute)
			o.UpdatedAt = now
			o.ErrorCode = "unavailable"
		}, "expired"},
		{"future-clock-failclosed", func(n *model.Node) {}, func(o *model.Operation) { o.BootstrapClaimedAt = now.Add(time.Second) }, "denied"},
		{"new-generation", func(n *model.Node) { n.Generation = 4 }, func(o *model.Operation) {}, "denied"},
		{"confirmation-wins", func(n *model.Node) { n.ContainerID = "confirmed" }, func(o *model.Operation) { o.BootstrapClaimedAt = now.Add(-6 * time.Minute) }, "confirmed"},
		{"already-revoked", func(n *model.Node) { n.Revoked = true }, func(o *model.Operation) {}, "denied"},
		{"nonretryable", func(n *model.Node) {}, func(o *model.Operation) { o.NonRetryable = true }, "denied"},
		{"fresh", func(n *model.Node) {}, func(o *model.Operation) { o.Phase = "queued"; o.BootstrapClaimedAt = time.Time{} }, "new"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nn, oo := n, op
			tc.node(&nn)
			tc.operation(&oo)
			if got := bootstrapState(nn, oo, now); got != tc.want {
				t.Fatalf("got=%s want=%s", got, tc.want)
			}
		})
	}
}

// Accepting stale/wrong-identity/offline-ready observations would publish unsafe SQL health.
func TestRecoveryObservationValidation(t *testing.T) {
	now := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	n := model.Node{Generation: 3, ContainerID: "cid", DaemonID: "daemon", StartEpoch: "epoch", DataVolume: "data", Status: "running", HealthAt: now.Add(-time.Second)}
	good := model.Observation{ContainerID: "cid", DaemonID: "daemon", StartEpoch: "epoch", Status: "running", Ready: true, ReportStatsKnown: true, ObservedAt: now}
	cases := []struct {
		name   string
		mutate func(*model.Observation)
		valid  bool
	}{
		{"current", func(o *model.Observation) {}, true},
		{"future", func(o *model.Observation) { o.ObservedAt = now.Add(3 * time.Second) }, false},
		{"future-within-clock-skew", func(o *model.Observation) { o.ObservedAt = now.Add(1500 * time.Millisecond) }, true},
		{"stale", func(o *model.Observation) { o.ObservedAt = now.Add(-31 * time.Second) }, false},
		{"older-than-recorded", func(o *model.Observation) { o.ObservedAt = now.Add(-2 * time.Second) }, false},
		{"wrong-container", func(o *model.Observation) { o.ContainerID = "other" }, false},
		{"old-epoch", func(o *model.Observation) { o.StartEpoch = "old" }, false},
		{"wrong-daemon", func(o *model.Observation) { o.DaemonID = "other" }, false},
		{"wrong-volume", func(o *model.Observation) { o.DataVolume = "other" }, false},
		{"negative", func(o *model.Observation) { o.PendingReports = -1 }, false},
		{"SQL-counter-overflow", func(o *model.Observation) { o.ActiveRuns = 1 << 31 }, false},
		{"unknown-not-zero", func(o *model.Observation) { o.ReportStatsKnown = false; o.Ready = false }, true},
		{"missing-preserves-confirmed-identity", func(o *model.Observation) { *o = model.Observation{Status: "missing", ObservedAt: now} }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := good
			tc.mutate(&o)
			if got := validateObservation(n, 3, o, now) == nil; got != tc.valid {
				t.Fatalf("accepted=%v want=%v", got, tc.valid)
			}
		})
	}
	if validateObservation(n, 2, good, now) == nil {
		t.Fatal("accepted old generation")
	}
	n.Status = "stopped"
	offline := model.Observation{Offline: true, Status: "stopped", DataVolume: "data", LayoutVersion: "1", ReportStatsKnown: true, ObservedAt: now}
	if e := validateObservation(n, 3, offline, now); e != nil {
		t.Fatal(e)
	}
	for _, mutate := range []func(*model.Observation){func(o *model.Observation) { o.Ready = true }, func(o *model.Observation) { o.Status = "running" }, func(o *model.Observation) { o.LayoutVersion = "2" }, func(o *model.Observation) { o.StartEpoch = "epoch" }, func(o *model.Observation) { o.DataVolume = "wrong" }} {
		o := offline
		mutate(&o)
		if validateObservation(n, 3, o, now) == nil {
			t.Fatal("accepted invalid offline proof")
		}
	}
}

// Explicit execution gate precedes every fixture or DB lookup. Schema execution
// needs controller permission; these assertions are prepared, not SQL acceptance.
func requireRecoverySQL(t *testing.T) {
	t.Helper()
	if os.Getenv("MULTICA_RUN_FLEET_RECOVERY_DB") != "1" {
		t.Skip("UNEXECUTED: additive577 schema execution not authorized")
	}
}
func TestRecoverySQLClaimRaceAndConfirmation(t *testing.T) {
	requireRecoverySQL(t)
	pool, f := testutil.NewFleetFixture(t)
	s := New(pool, "recovery-claim")
	ctx := context.Background()
	id := f.FleetNode(t, "recovery-claim", testutil.Cols{"container_id": "", "status": "creating", "ready": false, "health_at": nil, "data_volume": "data", "secrets_volume": "secrets"})
	opid := f.Insert(t, "fleet_node_operations", testutil.Cols{"namespace": "recovery-claim", "owner_id": f.UserID, "node_id": id, "action": "create", "idempotency_key": "claim", "request_hash": "hash"})
	ref := model.OperationRef{Namespace: "recovery-claim", NodeID: uuid(t, id), OperationID: uuid(t, opid), Generation: 1, Action: model.Create}
	owner := uuid(t, f.UserID)
	var wg sync.WaitGroup
	results := make(chan BootstrapClaim, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, e := s.ClaimBootstrap(ctx, owner, ref)
			if e == nil {
				results <- c
			} else {
				errs <- e
			}
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	if len(results) != 1 || len(errs) != 1 {
		t.Fatalf("winners=%d errors=%d", len(results), len(errs))
	}
	claim := <-results
	snap, e := s.CurrentOperation(ctx, owner, ref)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.ExpireBootstrap(ctx, snap); !errors.Is(e, model.ErrConflict) {
		t.Fatalf("live claimant revoked: %v", e)
	}
	token, _, e := s.MintBootstrapToken(ctx, claim)
	if e != nil || token == "" {
		t.Fatal("initial mint failed", e)
	}
	if _, _, e = s.MintBootstrapToken(ctx, claim); !errors.Is(e, model.ErrConflict) {
		t.Fatalf("remint allowed: %v", e)
	}
	o := model.Observation{ContainerID: "cid", Status: "running", DaemonID: claim.Snapshot().Node.DaemonID, StartEpoch: time.Now().UTC().Format(time.RFC3339Nano), ObservedAt: time.Now().UTC(), Ready: true, ReportStatsKnown: true}
	if e = s.ConfirmBootstrap(ctx, claim, o); e != nil {
		t.Fatal(e)
	}
	if e = s.ExpireBootstrap(ctx, snap); !errors.Is(e, model.ErrConflict) {
		t.Fatalf("confirmation overwritten: %v", e)
	}
	if e = s.ConfirmBootstrap(ctx, claim, o); !errors.Is(e, model.ErrConflict) {
		t.Fatalf("late duplicate result accepted: %v", e)
	}
	current, e := s.CurrentOperation(ctx, owner, ref)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.RecordOperationResult(ctx, current, o); e != nil {
		t.Fatal(e)
	}
	n, e := s.GetNode(ctx, owner, ref.NodeID)
	if e != nil || !n.Ready || n.ContainerID != "cid" || n.Revoked || !n.Observation.ReportStatsKnown {
		t.Fatalf("confirmed node=%+v err=%v", n, e)
	}
	op, e := s.GetOperation(ctx, owner, ref.OperationID)
	if e != nil || op.Phase != "completed" || !op.BootstrapClaimedAt.Equal(claim.claimedAt) {
		t.Fatalf("operation=%+v err=%v", op, e)
	}
}
func TestRecoverySQLExpiredBootstrapCrashWindows(t *testing.T) {
	requireRecoverySQL(t)
	for _, window := range []string{"before-mint", "after-mint", "after-install-bootstrap", "before-sql-confirmation"} {
		t.Run(window, func(t *testing.T) {
			pool, f := testutil.NewFleetFixture(t)
			ctx := context.Background()
			s := New(pool, "recovery-expired")
			id := f.FleetNode(t, "recovery-expired", testutil.Cols{"status": "creating", "ready": false, "health_at": nil, "data_volume": "keep-data", "secrets_volume": "keep-secrets"})
			at := time.Now().UTC().Add(-6 * time.Minute).Truncate(time.Microsecond)
			opid := f.Insert(t, "fleet_node_operations", testutil.Cols{"namespace": "recovery-expired", "owner_id": f.UserID, "node_id": id, "action": "create", "idempotency_key": window, "request_hash": "hash", "phase": "applying", "bootstrap_claimed_at": at, "bootstrap_minted": window != "before-mint", "updated_at": time.Now().UTC()})
			ref := model.OperationRef{Namespace: "recovery-expired", NodeID: uuid(t, id), OperationID: uuid(t, opid), Generation: 1, Action: model.Create}
			credential := f.Insert(t, "fleet_node_credentials", testutil.Cols{"namespace": "recovery-expired", "owner_id": f.UserID, "node_id": id, "token_hash": "owned-fake-" + id})
			snap, e := s.CurrentOperation(ctx, uuid(t, f.UserID), ref)
			if e != nil {
				t.Fatal(e)
			}
			if e = s.ExpireBootstrap(ctx, snap); e != nil {
				t.Fatal(e)
			}
			if e = s.ExpireBootstrap(ctx, snap); e != nil {
				t.Fatal("repeat recovery", e)
			}
			if _, e = s.ClaimBootstrap(ctx, uuid(t, f.UserID), ref); !errors.Is(e, model.ErrConflict) {
				t.Fatalf("reminted claim=%v", e)
			}
			n, e := s.GetNode(ctx, uuid(t, f.UserID), ref.NodeID)
			if e != nil || !n.Revoked || n.DataVolume != "keep-data" || n.SecretsVolume != "keep-secrets" || n.ContainerID != "" {
				t.Fatalf("preservation node=%+v err=%v", n, e)
			}
			op, e := s.GetOperation(ctx, uuid(t, f.UserID), ref.OperationID)
			if e != nil || !op.NonRetryable || op.Phase != "applying" || op.ErrorCode != "bootstrap-unrecoverable" || !op.BootstrapClaimedAt.Equal(at) {
				t.Fatalf("guarded operation=%+v err=%v", op, e)
			}
			var revoked bool
			if e = pool.QueryRow(ctx, "SELECT revoked_at IS NOT NULL FROM fleet_node_credentials WHERE id=$1", credential).Scan(&revoked); e != nil || !revoked {
				t.Fatalf("credential revoke=%v err=%v", revoked, e)
			}
		})
	}
}
func TestRecoverySQLDeletePreservesBusinessRows(t *testing.T) {
	requireRecoverySQL(t)
	pool, f := testutil.NewFleetFixture(t)
	ctx := context.Background()
	s := New(pool, "recovery-delete")
	id := f.FleetNode(t, "recovery-delete", testutil.Cols{"desired": "terminating", "status": "terminating", "maintenance": true, "revoked": true, "ready": false})
	opid := f.Insert(t, "fleet_node_operations", testutil.Cols{"namespace": "recovery-delete", "owner_id": f.UserID, "node_id": id, "action": "delete", "idempotency_key": "delete", "request_hash": "hash", "approved": true, "phase": "queued"})
	rt := f.Runtime(t, "owned", testutil.Cols{"runtime_mode": "local", "metadata": []byte("{\"managed_by\":\"local_fleet\",\"fleet_node_id\":\"" + id + "\"}")})
	ag := f.Agent(t, "keep", rt)
	issue := f.Issue(t, "keep business issue")
	task := f.Task(t, ag, testutil.Cols{"runtime_id": rt, "status": "completed", "issue_id": issue})
	if e := s.FinishDelete(ctx, uuid(t, opid), 1); e != nil {
		t.Fatal(e)
	}
	if e := s.FinishDelete(ctx, uuid(t, opid), 1); e != nil {
		t.Fatal("repeat completion", e)
	}
	var status string
	var tombstone bool
	if e := pool.QueryRow(ctx, "SELECT status,(metadata->>'fleet_tombstone')::boolean FROM agent_runtime WHERE id=$1", rt).Scan(&status, &tombstone); e != nil || status != "offline" || !tombstone {
		t.Fatalf("runtime=%s tombstone=%v err=%v", status, tombstone, e)
	}
	for _, row := range []struct{ table, id string }{{"agent", ag}, {"issue", issue}, {"agent_task_queue", task}} {
		var count int
		if e := pool.QueryRow(ctx, "SELECT count(*) FROM "+row.table+" WHERE id=$1", row.id).Scan(&count); e != nil || count != 1 {
			t.Fatalf("business row %s deleted: %v", row.table, e)
		}
	}
	n, e := s.GetNode(ctx, uuid(t, f.UserID), uuid(t, id))
	if e != nil || n.Desired != "terminated" || n.Status != "terminated" || !n.Revoked {
		t.Fatalf("node=%+v err=%v", n, e)
	}
	if e = s.FinishDelete(ctx, uuid(t, opid), 2); !errors.Is(e, model.ErrConflict) {
		t.Fatalf("wrong generation completion: %v", e)
	}
}

// Additional authorization matrix: approval projections and due scheduling must
// preserve their original operative barrier, never become execution permission.
func TestRecoveryCurrentOperationMatrix(t *testing.T) {
	now := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name                                     string
		action                                   model.Action
		phase, desired                           string
		approved, maintenance, revoked, nonretry bool
		attempts                                 int
		next                                     time.Time
		want                                     bool
	}{
		{name: "create", action: model.Create, phase: "queued", desired: "running", want: true},
		{name: "start", action: model.Start, phase: "queued", desired: "running", want: true},
		{name: "unapproved-review", action: model.Delete, phase: "preparing", desired: "stopped", maintenance: true, want: true},
		{name: "unapproved-cannot-apply", action: model.Delete, phase: "applying", desired: "terminating", maintenance: true, revoked: true, want: false},
		{name: "approved-delete", action: model.Delete, phase: "queued", desired: "terminating", approved: true, maintenance: true, revoked: true, want: true},
		{name: "delete-not-revoked", action: model.Delete, phase: "queued", desired: "terminating", approved: true, maintenance: true, want: false},
		{name: "wrong-desired", action: model.Delete, phase: "queued", desired: "running", approved: true, maintenance: true, revoked: true, want: false},
		{name: "approved-stop", action: model.Stop, phase: "applying", desired: "stopped", approved: true, maintenance: true, want: true},
		{name: "approved-reboot", action: model.Reboot, phase: "prepared", desired: "running", approved: true, maintenance: true, want: true},
		{name: "deferred", action: model.Delete, phase: "preparing", desired: "stopped", maintenance: true, next: now.Add(5 * time.Second), want: false},
		{name: "error-budget", action: model.Start, phase: "applying", desired: "running", attempts: 5, want: false},
		{name: "permanent", action: model.Create, phase: "applying", desired: "running", nonretry: true, want: false},
		{name: "completed", action: model.Start, phase: "completed", desired: "running", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snap := RecoverySnapshot{SQLNow: now, Node: model.Node{Desired: tc.desired, Maintenance: tc.maintenance, Revoked: tc.revoked}, Operation: model.Operation{Action: tc.action, Phase: tc.phase, Approved: tc.approved, NonRetryable: tc.nonretry, Attempts: tc.attempts, NextAttemptAt: tc.next}}
			if got := currentRecovery(snap) == nil; got != tc.want {
				t.Fatalf("authority=%v want=%v", got, tc.want)
			}
		})
	}
	s := New(nil, "owned")
	for _, delay := range []time.Duration{0, 4 * time.Second, 31 * time.Second} {
		if e := s.DeferOperation(context.Background(), RecoverySnapshot{}, delay); !errors.Is(e, model.ErrInvalidRequest) {
			t.Fatalf("unsafe defer delay=%v err=%v", delay, e)
		}
	}
	if e := s.RecordOperationError(context.Background(), RecoverySnapshot{}, "private-marker-value", true); !errors.Is(e, model.ErrInvalidRequest) {
		t.Fatal("arbitrary error text accepted")
	}
}
func TestRecoverySQLObservationAndUnknownScheduling(t *testing.T) {
	requireRecoverySQL(t)
	pool, f := testutil.NewFleetFixture(t)
	ctx := context.Background()
	s := New(pool, "recovery-observe")
	id := f.FleetNode(t, "recovery-observe", testutil.Cols{"status": "stopped", "desired": "stopped", "maintenance": true, "ready": false, "health_at": nil, "container_id": "cid", "start_epoch": "old", "data_volume": "data"})
	opid := f.Insert(t, "fleet_node_operations", testutil.Cols{"namespace": "recovery-observe", "owner_id": f.UserID, "node_id": id, "action": "delete", "idempotency_key": "unknown", "request_hash": "hash", "phase": "preparing"})
	ref := model.OperationRef{Namespace: "recovery-observe", NodeID: uuid(t, id), OperationID: uuid(t, opid), Generation: 1, Action: model.Delete}
	owner := uuid(t, f.UserID)
	o := model.Observation{Offline: true, Status: "stopped", DataVolume: "data", LayoutVersion: "1", ObservedAt: time.Now().UTC(), ReportStatsKnown: true, PendingReports: 1}
	if e := s.RecordObservation(ctx, ref.NodeID, 1, o); e != nil {
		t.Fatal(e)
	}
	n, e := s.GetNode(ctx, owner, ref.NodeID)
	if e != nil || !n.Observation.Offline || !n.Observation.ReportStatsKnown || n.Ready || n.Observation.PendingReports != 1 {
		t.Fatalf("durable diagnostic=%+v err=%v", n.Observation, e)
	}
	if e = s.RecordObservation(ctx, ref.NodeID, 2, o); !errors.Is(e, model.ErrConflict) {
		t.Fatalf("wrong generation: %v", e)
	}
	for i := 0; i < 7; i++ {
		snap, e := s.CurrentOperation(ctx, owner, ref)
		if e != nil {
			t.Fatal(e)
		}
		if e = s.DeferOperation(ctx, snap, 5*time.Second); e != nil {
			t.Fatal(e)
		}
		if _, e = pool.Exec(ctx, "UPDATE fleet_node_operations SET next_attempt_at=clock_timestamp()-interval '1 second' WHERE id=$1", opid); e != nil {
			t.Fatal(e)
		}
	}
	op, e := s.GetOperation(ctx, owner, ref.OperationID)
	if e != nil || op.Attempts != 0 || op.NonRetryable || op.Approved || op.Phase != "preparing" || !op.BootstrapClaimedAt.IsZero() {
		t.Fatalf("unknown spent errors or lost barrier=%+v err=%v", op, e)
	}
	for i := 0; i < 5; i++ {
		snap, e := s.CurrentOperation(ctx, owner, ref)
		if e != nil {
			t.Fatal(e)
		}
		if e = s.RecordOperationError(ctx, snap, "unavailable", false); e != nil {
			t.Fatal(e)
		}
		if _, e = pool.Exec(ctx, "UPDATE fleet_node_operations SET next_attempt_at=clock_timestamp()-interval '1 second' WHERE id=$1", opid); e != nil {
			t.Fatal(e)
		}
	}
	op, e = s.GetOperation(ctx, owner, ref.OperationID)
	if e != nil || op.Attempts != 5 || !op.NonRetryable || op.Phase != "preparing" || op.Approved {
		t.Fatalf("error budget disposition=%+v err=%v", op, e)
	}
	if _, e = s.CurrentOperation(ctx, owner, ref); !errors.Is(e, model.ErrConflict) {
		t.Fatalf("exhausted authority: %v", e)
	}
}
func TestRecoverySQLLiveFailureAndNewGenerationFence(t *testing.T) {
	requireRecoverySQL(t)
	pool, f := testutil.NewFleetFixture(t)
	s := New(pool, "recovery-failure")
	ctx := context.Background()
	id := f.FleetNode(t, "recovery-failure", testutil.Cols{"status": "creating", "ready": false, "health_at": nil, "data_volume": "data", "secrets_volume": "secrets"})
	opid := f.Insert(t, "fleet_node_operations", testutil.Cols{"namespace": "recovery-failure", "owner_id": f.UserID, "node_id": id, "action": "create", "idempotency_key": "failure", "request_hash": "hash"})
	ref := model.OperationRef{Namespace: "recovery-failure", NodeID: uuid(t, id), OperationID: uuid(t, opid), Generation: 1, Action: model.Create}
	owner := uuid(t, f.UserID)
	claim, e := s.ClaimBootstrap(ctx, owner, ref)
	if e != nil {
		t.Fatal(e)
	}
	token, _, e := s.MintBootstrapToken(ctx, claim)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = pool.Exec(ctx, "UPDATE fleet_nodes SET generation=2 WHERE id=$1", id); e != nil {
		t.Fatal(e)
	}
	if e = s.FailBootstrap(ctx, claim, "configuration"); !errors.Is(e, model.ErrConflict) {
		t.Fatalf("stale claimant revoked new generation: %v", e)
	}
	var revoked bool
	if e = pool.QueryRow(ctx, "SELECT revoked FROM fleet_nodes WHERE id=$1", id).Scan(&revoked); e != nil || revoked {
		t.Fatalf("new-generation revoke=%v err=%v", revoked, e)
	}
	if _, e = pool.Exec(ctx, "UPDATE fleet_nodes SET generation=1 WHERE id=$1", id); e != nil {
		t.Fatal(e)
	}
	if e = s.FailBootstrap(ctx, claim, "configuration"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.VerifyNodeToken(ctx, token); !errors.Is(e, model.ErrForbidden) {
		t.Fatal("failed bootstrap token not revoked")
	}
	op, e := s.GetOperation(ctx, owner, ref.OperationID)
	if e != nil || !op.NonRetryable || op.Phase != "applying" || !op.BootstrapClaimedAt.Equal(claim.claimedAt) {
		t.Fatalf("failure barrier=%+v err=%v", op, e)
	}
}
