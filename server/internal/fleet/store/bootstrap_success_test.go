package store

import (
	"context"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/multica-ai/multica/server/internal/fleet/model"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"strings"
	"testing"
	"time"
)

func TestRecoveryHealthyConfirmationCrashAfterDeadline(t *testing.T) {
	now := time.Date(2026, 10, 5, 0, 5, 0, 0, time.UTC)
	s := RecoverySnapshot{SQLNow: now.Add(-time.Second), Node: model.Node{Namespace: "owned", ID: uuid(t, "10000000-0000-0000-0000-000000000001"), OwnerID: uuid(t, "20000000-0000-0000-0000-000000000001"), Generation: 3, Desired: "running", DaemonID: "daemon", DataVolume: "data", SecretsVolume: "secrets", Image: "image", ProfileRef: "profile"}, Operation: model.Operation{ID: uuid(t, "30000000-0000-0000-0000-000000000001"), Generation: 3, Action: model.Create, Phase: "applying", BootstrapMinted: true, BootstrapClaimedAt: now.Add(-5 * time.Minute), CreatedAt: now.Add(-6 * time.Minute)}}
	s.Operation.NodeID = s.Node.ID
	s.Operation.OwnerID = s.Node.OwnerID
	o := model.Observation{ContainerID: "cid", DaemonID: "daemon", StartEpoch: now.Add(-4 * time.Minute).Format(time.RFC3339Nano), Status: "running", Ready: true, Agents: []string{"claude"}, RuntimeCount: 1, ReportStatsKnown: true, ObservedAt: s.SQLNow}
	proof := confirmedBootstrapSuccess(s, o)
	if proof == nil {
		t.Fatal("valid live confirmation did not produce success receipt")
	}
	raw, e := encodeObservation(s.Node.Generation, o, proof)
	if e != nil {
		t.Fatal(e)
	}
	s.bootstrapSuccess, e = decodeBootstrapSuccess(raw)
	if e != nil {
		t.Fatal(e)
	}
	n, e := bootstrapObservation(s, o)
	if e != nil {
		t.Fatal(e)
	}
	s.Node = n
	s.Node.StartEpoch = o.StartEpoch
	s.Node.Observation = o
	s.Node.Ready = true
	s.Node.HealthAt = o.ObservedAt
	// Crash loses the separate operation-completion write, not the healthy confirmation.
	s.SQLNow = now.Add(time.Second)
	if confirmedCreateExpired(s) {
		t.Fatal("validated in-window healthy confirmation was classified never-ready and revoked")
	}
	o.ObservedAt = s.SQLNow
	c := &recoveryCapture{}
	if e := New(nil, "owned").persistOperationResult(context.Background(), db.New(c), s, o, false); e != nil {
		t.Fatalf("fresh same-generation crash recovery completion denied: %v", e)
	}
}
func TestRecoverySuccessReceiptCompiledTransactionGuards(t *testing.T) {
	c := &recoveryCapture{}
	q := db.New(c)
	_, _ = q.FleetConfirmBootstrap(context.Background(), db.FleetConfirmBootstrapParams{})
	if !strings.Contains(c.sql, "observation=") {
		t.Error("confirmation has no atomic in-window durable receipt write")
	}
	_, _ = q.FleetExpireConfirmedCreateNode(context.Background(), db.FleetExpireConfirmedCreateNodeParams{})
	if !strings.Contains(c.sql, "bootstrap_success") {
		t.Error("timeout CAS lacks durable-success exclusion")
	}
	_, _ = q.FleetCompleteOperation(context.Background(), db.FleetCompleteOperationParams{})
	if !strings.Contains(c.sql, "bootstrap_success") || !strings.Contains(c.sql, "health_at>=clock_timestamp()-interval '30 seconds'") {
		t.Error("completion CAS lacks exact success receipt plus fresh transaction-time health")
	}
	_, _ = q.FleetRecordObservation(context.Background(), db.FleetRecordObservationParams{})
	if !strings.Contains(c.sql, "bootstrap_success") {
		t.Error("observation writes erase durable success")
	}
}
func successFixture(t *testing.T) (RecoverySnapshot, model.Observation) {
	t.Helper()
	now := time.Date(2026, 10, 5, 0, 5, 0, 0, time.UTC)
	n := model.Node{Namespace: "owned", ID: uuid(t, "10000000-0000-0000-0000-000000000001"), OwnerID: uuid(t, "20000000-0000-0000-0000-000000000001"), Generation: 3, Desired: "running", DaemonID: "daemon", DataVolume: "data", SecretsVolume: "secrets", Image: "image", ProfileRef: "profile", Resources: model.Spec{CPUs: 2, MemoryBytes: 4 << 30, Pids: 256, MaxRuns: 1}}
	op := model.Operation{ID: uuid(t, "30000000-0000-0000-0000-000000000001"), NodeID: n.ID, OwnerID: n.OwnerID, Generation: 3, Action: model.Create, Phase: "applying", BootstrapMinted: true, BootstrapClaimedAt: now.Add(-5 * time.Minute), CreatedAt: now.Add(-6 * time.Minute)}
	s := RecoverySnapshot{Node: n, Operation: op, SQLNow: now.Add(-time.Second)}
	o := model.Observation{ContainerID: "cid", DaemonID: n.DaemonID, StartEpoch: now.Add(-4 * time.Minute).Format(time.RFC3339Nano), Status: "running", Ready: true, Agents: []string{"claude"}, RuntimeCount: 1, ReportStatsKnown: true, ObservedAt: s.SQLNow}
	s.bootstrapSuccess = confirmedBootstrapSuccess(s, o)
	if s.bootstrapSuccess == nil {
		t.Fatal("no valid private-confirmation receipt")
	}
	s.Node.ContainerID = o.ContainerID
	s.Node.StartEpoch = o.StartEpoch
	s.Node.Observation = o
	s.Node.HealthAt = o.ObservedAt
	s.Node.Ready = true
	return s, o
}
func TestRecoveryBootstrapSuccessHistoricalExclusionNotHealth(t *testing.T) {
	s, o := successFixture(t)
	s.SQLNow = s.SQLNow.Add(2 * time.Minute)
	if !validBootstrapSuccess(s) || confirmedCreateExpired(s) {
		t.Fatal("age converted initialized success into never-ready failure")
	}
	c := &recoveryCapture{}
	if e := New(nil, "owned").persistOperationResult(context.Background(), db.New(c), s, o, false); e == nil || c.sql != "" {
		t.Fatal("stale historical health completed or wrote")
	}
	o.ObservedAt = s.SQLNow
	if e := New(nil, "owned").persistOperationResult(context.Background(), db.New(c), s, o, false); e != nil {
		t.Fatalf("fresh same-binding receipt recovery denied: %v", e)
	}
	if len(c.args) != 9 || len(c.args[8].([]byte)) == 0 {
		t.Fatal("completion not bound to exact private success receipt")
	}
}
func TestRecoveryBootstrapSuccessNegativeMatrix(t *testing.T) {
	for _, name := range []string{"ready-only", "projection-only", "generation", "owner", "namespace", "node", "operation", "claim", "resources", "container", "epoch", "daemon", "data", "secrets", "image", "profile", "late-proof", "future-proof", "preclaim-proof", "old-epoch", "unknown-proof", "unhealthy-proof", "offline-proof", "unminted", "revoked", "maintenance", "foreign-epoch", "wrong-layout", "wrong-volume", "completed-race"} {
		t.Run(name, func(t *testing.T) {
			s, o := successFixture(t)
			copyProof := *s.bootstrapSuccess
			s.bootstrapSuccess = &copyProof
			s.SQLNow = s.SQLNow.Add(2 * time.Second)
			o.ObservedAt = s.SQLNow
			switch name {
			case "ready-only":
				s.bootstrapSuccess = nil
			case "projection-only":
				s.bootstrapSuccess = nil
				s.BootstrapSucceeded = true
			case "generation":
				s.Node.Generation++
			case "owner":
				s.Node.OwnerID = s.Node.ID
			case "namespace":
				s.Node.Namespace = "foreign"
			case "node":
				s.Node.ID = s.Node.OwnerID
			case "operation":
				s.Operation.ID = s.Node.ID
			case "claim":
				s.Operation.BootstrapClaimedAt = s.Operation.BootstrapClaimedAt.Add(time.Second)
			case "resources":
				s.Node.Resources.MaxRuns++
			case "container":
				s.Node.ContainerID = "foreign"
			case "epoch":
				s.Node.StartEpoch = s.SQLNow.Format(time.RFC3339Nano)
			case "daemon":
				s.Node.DaemonID = "foreign"
			case "data":
				s.Node.DataVolume = "foreign"
			case "secrets":
				s.Node.SecretsVolume = "foreign"
			case "image":
				s.Node.Image = "foreign"
			case "profile":
				s.Node.ProfileRef = "foreign"
			case "late-proof":
				s.bootstrapSuccess.Observation.ObservedAt = s.Operation.BootstrapClaimedAt.Add(5 * time.Minute)
			case "future-proof":
				s.bootstrapSuccess.Observation.ObservedAt = s.SQLNow.Add(3 * time.Second)
			case "preclaim-proof":
				s.bootstrapSuccess.Observation.ObservedAt = s.Operation.BootstrapClaimedAt.Add(-time.Second)
			case "old-epoch":
				s.bootstrapSuccess.StartEpoch = s.Operation.BootstrapClaimedAt.Add(-time.Second).Format(time.RFC3339Nano)
			case "unknown-proof":
				s.bootstrapSuccess.Observation.Ready = false
				s.bootstrapSuccess.Observation.Status = "unknown"
			case "unhealthy-proof":
				s.bootstrapSuccess.Observation.RuntimeCount = 0
			case "offline-proof":
				s.bootstrapSuccess.Observation.Offline = true
			case "unminted":
				s.Operation.BootstrapMinted = false
			case "revoked":
				s.Node.Revoked = true
			case "maintenance":
				s.Node.Maintenance = true
			case "foreign-epoch":
				s.bootstrapSuccess.Observation.StartEpoch = "foreign"
			case "wrong-layout":
				s.bootstrapSuccess.Observation.LayoutVersion = "2"
			case "wrong-volume":
				s.bootstrapSuccess.Observation.DataVolume = "foreign"
			case "completed-race":
				s.Operation.Phase = "completed"
			}
			if name != "completed-race" && validBootstrapSuccess(s) {
				t.Fatal("invalid history granted success")
			}
			c := &recoveryCapture{}
			if e := New(nil, "owned").persistOperationResult(context.Background(), db.New(c), s, o, false); e == nil || c.sql != "" {
				t.Fatal("invalid/current-completed history performed result writes")
			}
			if s.bootstrapSuccess != nil && confirmedCreateExpired(s) {
				t.Fatal("inconsistent receipt guessed never-ready revocation")
			}
		})
	}
}
func TestRecoveryBootstrapSuccessFreshHealthNegativeMatrix(t *testing.T) {
	for _, name := range []string{"stale", "future", "unknown", "no-runtime", "no-claude", "offline", "foreign-epoch", "wrong-layout", "wrong-volume", "missing", "stopped"} {
		t.Run(name, func(t *testing.T) {
			s, o := successFixture(t)
			s.SQLNow = s.SQLNow.Add(time.Minute)
			o.ObservedAt = s.SQLNow
			switch name {
			case "stale":
				o.ObservedAt = s.SQLNow.Add(-31 * time.Second)
			case "future":
				o.ObservedAt = s.SQLNow.Add(3 * time.Second)
			case "unknown":
				o.Ready = false
				o.Status = "unknown"
			case "no-runtime":
				o.RuntimeCount = 0
			case "no-claude":
				o.Agents = nil
			case "offline":
				o.Offline = true
			case "foreign-epoch":
				o.StartEpoch = "foreign"
			case "wrong-layout":
				o.LayoutVersion = "2"
			case "wrong-volume":
				o.DataVolume = "foreign"
			case "missing":
				o.Status = "missing"
				o.Ready = false
			case "stopped":
				o.Status = "stopped"
				o.Ready = false
			}
			c := &recoveryCapture{}
			if e := New(nil, "owned").persistOperationResult(context.Background(), db.New(c), s, o, false); e == nil || c.sql != "" {
				t.Fatal("history promoted invalid current health")
			}
			if confirmedCreateExpired(s) {
				t.Fatal("unknown current health revoked historical initialization success")
			}
		})
	}
}

// TestRecoveryBootstrapSuccessWithinClockSkew pins the receipt side of the
// cross-clock tolerance. A daemon clock ahead of SQL must still yield the
// durable proof: ConfirmBootstrap writes container_id regardless, so dropping
// the receipt would leave a confirmed node unprovable and lease expiry would
// later revoke it as never-ready.
func TestRecoveryBootstrapSuccessWithinClockSkew(t *testing.T) {
	now := time.Date(2026, 10, 5, 0, 5, 0, 0, time.UTC)
	n := model.Node{Namespace: "owned", ID: uuid(t, "10000000-0000-0000-0000-000000000001"), OwnerID: uuid(t, "20000000-0000-0000-0000-000000000001"), Generation: 3, Desired: "running", DaemonID: "daemon", DataVolume: "data", SecretsVolume: "secrets", Image: "image", ProfileRef: "profile", Resources: model.Spec{CPUs: 2, MemoryBytes: 4 << 30, Pids: 256, MaxRuns: 1}}
	op := model.Operation{ID: uuid(t, "30000000-0000-0000-0000-000000000001"), NodeID: n.ID, OwnerID: n.OwnerID, Generation: 3, Action: model.Create, Phase: "applying", BootstrapMinted: true, BootstrapClaimedAt: now.Add(-time.Minute), CreatedAt: now.Add(-6 * time.Minute)}
	s := RecoverySnapshot{Node: n, Operation: op, SQLNow: now}
	o := model.Observation{ContainerID: "cid", DaemonID: n.DaemonID, StartEpoch: now.Add(-30 * time.Second).Format(time.RFC3339Nano), Status: "running", Ready: true, Agents: []string{"claude"}, RuntimeCount: 1, ReportStatsKnown: true, ObservedAt: now.Add(1500 * time.Millisecond)}
	p := confirmedBootstrapSuccess(s, o)
	if p == nil {
		t.Fatal("confirmation within clock skew produced no durable receipt")
	}
	s.Node.ContainerID = o.ContainerID
	s.Node.StartEpoch = o.StartEpoch
	s.bootstrapSuccess = p
	if !validBootstrapSuccess(s) || confirmedCreateExpired(s) {
		t.Fatal("within-skew receipt did not survive validation")
	}
	raw, e := encodeObservation(n.Generation, o, p)
	if e != nil || !strings.Contains(string(raw), `"bootstrap_success"`) {
		t.Fatal("within-skew confirmation wrote the receipt-less form")
	}
}
func TestRecoveryBootstrapSuccessMintOnlyInWindowHealthyConfirmation(t *testing.T) {
	for _, name := range []string{"valid-online-omitted-layout", "late", "stale", "future", "wrong-layout", "wrong-volume", "offline", "unknown", "no-runtime", "no-claude", "foreign-epoch", "unminted", "completed", "new-generation"} {
		t.Run(name, func(t *testing.T) {
			s, o := successFixture(t)
			s.bootstrapSuccess = nil
			s.Node.ContainerID = ""
			s.Node.StartEpoch = ""
			s.Node.HealthAt = time.Time{}
			switch name {
			case "late":
				s.SQLNow = s.Operation.BootstrapClaimedAt.Add(5 * time.Minute)
				o.ObservedAt = s.SQLNow
			case "stale":
				o.ObservedAt = s.SQLNow.Add(-31 * time.Second)
			case "future":
				o.ObservedAt = s.SQLNow.Add(time.Second)
			case "wrong-layout":
				o.LayoutVersion = "2"
			case "wrong-volume":
				o.DataVolume = "foreign"
			case "offline":
				o.Offline = true
			case "unknown":
				o.Ready = false
				o.Status = "unknown"
			case "no-runtime":
				o.RuntimeCount = 0
			case "no-claude":
				o.Agents = nil
			case "foreign-epoch":
				o.StartEpoch = "foreign"
			case "unminted":
				s.Operation.BootstrapMinted = false
			case "completed":
				s.Operation.Phase = "completed"
			case "new-generation":
				s.Node.Generation++
			}
			got := confirmedBootstrapSuccess(s, o)
			if (got != nil) != (name == "valid-online-omitted-layout") {
				t.Fatal("invalid live confirmation created history or real producer shape rejected")
			}
		})
	}
}
func TestRecoveryBootstrapSuccessStrictReceiptDecode(t *testing.T) {
	s, o := successFixture(t)
	raw, e := encodeObservation(s.Node.Generation, o, s.bootstrapSuccess)
	if e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"duplicate-ref", "duplicate-observation", "missing-observation-field"} {
		t.Run(name, func(t *testing.T) {
			text := string(raw)
			switch name {
			case "duplicate-ref":
				text = strings.Replace(text, "\"Namespace\":\"owned\"", "\"Namespace\":\"foreign\",\"Namespace\":\"owned\"", 1)
			case "duplicate-observation":
				text = strings.ReplaceAll(text, "\"Ready\":true", "\"Ready\":false,\"Ready\":true")
			case "missing-observation-field":
				text = strings.ReplaceAll(text, "\"LayoutVersion\":\"\",", "")
			}
			if _, e := decodeBootstrapSuccess([]byte(text)); e == nil {
				t.Fatal("malformed nested receipt accepted")
			}
		})
	}
}
func TestRecoveryBootstrapSuccessReportStatsIndependent(t *testing.T) {
	for _, name := range []string{"unknown-reports", "pending-and-failed-reports"} {
		t.Run(name, func(t *testing.T) {
			s, o := successFixture(t)
			s.Node.ContainerID = ""
			s.Node.StartEpoch = ""
			s.Node.HealthAt = time.Time{}
			s.bootstrapSuccess = nil
			o.ReportStatsKnown = name != "unknown-reports"
			o.PendingReports = 2
			o.FailedReports = 3
			p := confirmedBootstrapSuccess(s, o)
			if p == nil {
				t.Fatal("valid initialization health lost success receipt because report queue was unknown/busy")
			}
			if p.Observation.ReportStatsKnown != o.ReportStatsKnown || p.Observation.PendingReports != 2 || p.Observation.FailedReports != 3 {
				t.Fatal("receipt fabricated or erased report statistics")
			}
			s.Node.ContainerID = o.ContainerID
			s.Node.StartEpoch = o.StartEpoch
			s.bootstrapSuccess = p
			s.Node.HealthAt = o.ObservedAt
			s.SQLNow = s.SQLNow.Add(time.Minute)
			if confirmedCreateExpired(s) || !validBootstrapSuccess(s) {
				t.Fatal("report statistics revoked historical initialization")
			}
			o.ObservedAt = s.SQLNow
			capture := &statsCapture{}
			if e := New(nil, "owned").persistOperationResult(context.Background(), db.New(capture), s, o, false); e != nil {
				t.Fatal(e)
			}
			got, e := decodeObservation(capture.observation, s.Node)
			if e != nil || got.ReportStatsKnown != o.ReportStatsKnown || got.PendingReports != 2 || got.FailedReports != 3 {
				t.Fatal("completion fabricated or erased report statistics")
			}
			if !o.ReportStatsKnown && knownMaintenanceProof(s.Node, s.Operation, o, s.SQLNow) {
				t.Fatal("unknown queue became dangerous-maintenance proof")
			}
		})
	}
}

type statsCapture struct {
	recoveryCapture
	observation []byte
}

func (c *statsCapture) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if strings.Contains(sql, "SET observation=") {
		c.observation = args[0].([]byte)
	}
	return c.recoveryCapture.Exec(ctx, sql, args...)
}
