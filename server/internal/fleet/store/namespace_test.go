package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/fleet/model"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type admissionDB struct {
	closed  bool
	execs   int
	readErr error
}

func (f *admissionDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	f.execs++
	return pgconn.CommandTag{}, nil
}
func (f *admissionDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("unexpected query")
}
func (f *admissionDB) QueryRow(context.Context, string, ...any) pgx.Row { return admissionRow{f} }

type admissionRow struct{ f *admissionDB }

func (r admissionRow) Scan(dest ...any) error {
	if r.f.readErr != nil {
		return r.f.readErr
	}
	*dest[0].(*string) = "fixture"
	*dest[1].(*string) = "fleet"
	*dest[2].(*bool) = r.f.closed
	*dest[3].(*int64) = 1
	*dest[4].(*string) = "shutdown"
	*dest[5].(*bool) = false
	*dest[6].(*[]byte) = nil
	return nil
}

func TestNamespaceSQLListingAllOwnersAndUnknownIdentity(t *testing.T) {
	s, f, ns := namespaceFixture(t)
	ctx := context.Background()
	ids := []string{"00000000-0000-0000-0000-000000000000", "00000000-0000-0000-0000-000000000001", "00000000-0000-0000-0000-000000000002"}
	other := f.User(t, "other", "listing-"+f.UserID+"@test.invalid")
	for i, id := range ids {
		owner := f.UserID
		if i == 2 {
			owner = other
		}
		f.FleetNode(t, ns, testutil.Cols{"id": id, "owner_id": owner})
	}
	nodes, e := s.ListNamespaceNodes(ctx, pgtype.UUID{}, 2)
	if e != nil || len(nodes) != 2 || nodes[0].ID.Bytes != [16]byte{} || nodes[1].ID.Bytes[15] != 1 {
		t.Fatalf("root page silently omitted unknown node: %v %v", nodes, e)
	}
	nodes, e = s.ListNamespaceNodes(ctx, nodes[1].ID, 2)
	if e != nil || len(nodes) != 1 || nodes[0].ID.Bytes[15] != 2 || nodes[0].OwnerID != ownerUUID(t, other) {
		t.Fatalf("stable all-owner next page failed: %v %v", nodes, e)
	}
}

func TestNamespaceSQLSchemaShape(t *testing.T) {
	pool, _ := testutil.NewFleetFixture(t)
	ctx := context.Background()
	var database string
	var version int
	var validIndex, notNull bool
	err := pool.QueryRow(ctx, `SELECT current_database(),current_setting('server_version_num')::int,
 (SELECT indisunique AND indisvalid AND indisready FROM pg_index WHERE indexrelid='fleet_namespace_fences_namespace_uidx'::regclass),
 (SELECT count(*) FILTER (WHERE attnotnull)=6 AND count(*)=7 AND count(*) FILTER (WHERE attname='completion_manifest' AND NOT attnotnull)=1 FROM pg_attribute WHERE attrelid='fleet_namespace_fences'::regclass AND attnum>0 AND NOT attisdropped)`).Scan(&database, &version, &validIndex, &notNull)
	if err != nil || version < 170000 || !validIndex || !notNull {
		t.Fatalf("namespace physical schema proof database=%s PG=%d unique/valid/ready=%v NOTNULL=%v error=%v", database, version, validIndex, notNull, err)
	}
	t.Logf("NamespacePhysicalSchema database=%s PG=%d unique/valid/ready=%v sixNOTNULL=%v", database, version, validIndex, notNull)
}

func namespaceFixture(t *testing.T) (*Store, *testutil.Fixture, string) {
	pool, f := testutil.NewFleetFixture(t)
	ns := "task1-namespace-" + f.UserID
	cleanProduced(t, f, ns)
	f.Cleanup(t, "DELETE FROM fleet_namespace_fences WHERE namespace=$1", ns)
	return New(pool, ns, WithProvisioningConfig(fakeConfig(ns))), f, ns
}
func TestNamespaceSQLAllOwnerProducersAndReplay(t *testing.T) {
	s, f, ns := namespaceFixture(t)
	ctx := context.Background()
	owner := ownerUUID(t, f.UserID)
	f.FleetProfile(t, ns)
	req := model.CreateRequest{Name: "first", Spec: "local-small", IdempotencyKey: "before"}
	first, op, _, err := s.CreateIntent(ctx, owner, req)
	if err != nil {
		t.Fatal(err)
	}
	fence, err := s.CloseNamespace(ctx, "fixture-fleet", "shutdown-key")
	if err != nil || !fence.Closed {
		t.Fatalf("close=%+v %v", fence, err)
	}
	again, op2, replay, err := s.CreateIntent(ctx, owner, req)
	if err != nil || !replay || again.ID != first.ID || op2.ID != op.ID {
		t.Fatalf("closed durable replay=%v %v", replay, err)
	}
	other := f.User(t, "other", "other-"+f.UserID+"@test.invalid")
	for _, id := range []pgtype.UUID{owner, ownerUUID(t, other)} {
		if _, _, _, err := s.CreateIntent(ctx, id, model.CreateRequest{Name: "new", Spec: "local-small", IdempotencyKey: "after"}); !errors.Is(err, model.ErrBusy) {
			t.Errorf("owner create crossed closed namespace: %v", err)
		}
	}
	for _, action := range []model.Action{model.Start, model.Reboot} {
		status := "running"
		if action == model.Start {
			status = "stopped"
		}
		node := ownerUUID(t, f.FleetNode(t, ns, testutil.Cols{"status": status}))
		var e error
		if action == model.Start {
			_, e = s.CreateStartIntent(ctx, owner, node, "start-after")
		} else {
			_, e = s.PrepareMaintenance(ctx, owner, node, action, "reboot-after")
		}
		if !errors.Is(e, model.ErrBusy) {
			t.Errorf("%s crossed closure: %v", action, e)
		}
	}
	for _, field := range []string{"fleet", "key", "epoch"} {
		bad := fence
		switch field {
		case "fleet":
			bad.FleetID += "x"
		case "key":
			bad.OperationKey += "x"
		case "epoch":
			bad.Generation++
		}
		if e := s.OpenNamespace(ctx, bad); !errors.Is(e, model.ErrConflict) {
			t.Errorf("bad %s resume=%v", field, e)
		}
	}
	retry, e := s.CloseNamespace(ctx, "fixture-fleet", "shutdown-key")
	if e != nil || retry != fence {
		t.Fatalf("close retry changed epoch: %+v %v", retry, e)
	}
	if e := s.OpenNamespace(ctx, fence); e != nil {
		t.Fatal(e)
	}
	if e := s.OpenNamespace(ctx, fence); !errors.Is(e, model.ErrConflict) {
		t.Fatalf("stale CAS reopened=%v", e)
	}
}

// A producer transaction keeps its shared prefix through commit; Close cannot
// win early or mint a marker while that transaction is still admitted.
func TestNamespaceSQLProducerCommitAndCloseBudget(t *testing.T) {
	s, _, ns := namespaceFixture(t)
	ctx := context.Background()
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	if e = CheckNamespaceAdmission(ctx, db.New(tx), ns); e != nil {
		t.Fatal(e)
	}
	started := make(chan struct{})
	finished := make(chan error, 1)
	go func() { close(started); _, e := s.CloseNamespace(ctx, "fixture-fleet", "close"); finished <- e }()
	<-started
	// Server lock introspection makes waiting deterministic instead of sleeping.
	waitCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	for {
		var waiting bool
		e = s.pool.QueryRow(waitCtx, "SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND NOT granted AND classid::bigint=((hashtextextended('namespace:'||$1,0)>>32)&4294967295) AND objid::bigint=(hashtextextended('namespace:'||$1,0)&4294967295))", ns).Scan(&waiting)
		if e != nil {
			t.Fatal(e)
		}
		if waiting {
			break
		}
	}
	select {
	case e := <-finished:
		t.Fatalf("close crossed held producer: %v", e)
	default:
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	if e = <-finished; e != nil {
		t.Fatal(e)
	}
	fence, e := s.GetNamespaceFence(ctx)
	if e != nil || !fence.Closed {
		t.Fatalf("commit then close=%+v %v", fence, e)
	}
	if e = s.OpenNamespace(ctx, fence); e != nil {
		t.Fatal(e)
	}
	tx, e = s.pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	if e = CheckNamespaceAdmission(ctx, db.New(tx), ns); e != nil {
		t.Fatal(e)
	}
	start := time.Now()
	_, e = s.CloseNamespace(ctx, "fixture-fleet", "timeout")
	if e == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("close budget elapsed=%v error=%v", time.Since(start), e)
	}
}

func TestNamespaceManifestExcludesDiagnostics(t *testing.T) {
	s := &Store{namespace: "fixture"}
	n := model.Node{Namespace: "fixture", ID: pgtype.UUID{Bytes: [16]byte{15: 1}, Valid: true}, OwnerID: pgtype.UUID{Bytes: [16]byte{15: 2}, Valid: true}, Generation: 1, ErrorMessage: "synthetic-model-secret", Observation: model.Observation{Agents: []string{"synthetic-observation"}}}
	c := NamespaceCompletion{Node: n, Ref: model.OperationRef{Namespace: "fixture", NodeID: n.ID, OperationID: pgtype.UUID{Bytes: [16]byte{15: 3}, Valid: true}, Generation: 1, Action: model.Stop}, Key: "original"}
	raw, e := encodeCompletionManifest(NamespaceFence{Namespace: s.namespace, FleetID: "fleet", OperationKey: "key", Generation: 1}, model.Stop, []NamespaceCompletion{c})
	if e != nil {
		t.Fatal(e)
	}
	for _, forbidden := range []string{"synthetic-model-secret", "synthetic-observation", "Observation", "ErrorMessage", "ErrorCode", "HealthAt", "UpdatedAt", "ActiveRuns"} {
		if bytes.Contains(raw, []byte(forbidden)) {
			t.Fatalf("manifest included non-proof field %s", forbidden)
		}
	}
}

func TestNamespaceSQLManifestEmptyAndLegacy(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprint(legacy), func(t *testing.T) {
			s, f, ns := namespaceFixture(t)
			ctx := context.Background()
			fence, e := s.CloseNamespace(ctx, "fixture-fleet", "shutdown")
			if e != nil {
				t.Fatal(e)
			}
			if legacy {
				f.Exec(t, "UPDATE fleet_namespace_fences SET finalized=true WHERE namespace=$1", ns)
			} else if e = s.CheckNamespaceCompletions(ctx, fence, model.Stop, nil); e != nil {
				t.Fatal(e)
			}
			fence, e = s.GetNamespaceFence(ctx)
			if e != nil {
				t.Fatal(e)
			}
			cs, found, e := s.GetNamespaceCompletions(ctx, fence, model.Stop)
			if legacy {
				if e == nil || found {
					t.Fatal("legacy missing proof adopted empty namespace")
				}
				if e = s.CheckNamespaceCompletions(ctx, fence, model.Stop, nil); e == nil {
					t.Fatal("legacy proof backfilled")
				}
				if _, e = s.BeginNamespaceDestroy(ctx, fence); e == nil {
					t.Fatal("legacy proof authorized destroy")
				}
				var absent bool
				if e = s.pool.QueryRow(ctx, "SELECT completion_manifest IS NULL FROM fleet_namespace_fences WHERE namespace=$1", ns).Scan(&absent); e != nil || !absent {
					t.Fatal("missing proof was backfilled")
				}
			} else {
				if e != nil || !found || cs == nil || len(cs) != 0 {
					t.Fatalf("original empty proof unavailable: %v", e)
				}
				if e = s.CheckNamespaceCompletions(ctx, fence, model.Stop, cs); e != nil {
					t.Fatal(e)
				}
				if _, e = s.BeginNamespaceDestroy(ctx, fence); e != nil {
					t.Fatal(e)
				}
			}
		})
	}
}

func TestNamespaceSQLFinalizedManifestRejectsDrift(t *testing.T) {
	for _, change := range []string{"remove", "image"} {
		t.Run(change, func(t *testing.T) {
			s, f, ns := namespaceFixture(t)
			ctx := context.Background()
			owner := ownerUUID(t, f.UserID)
			// PostgreSQL stores timestamptz at microsecond precision while Go keeps
			// nanoseconds on Linux; the observation JSON and the health_at column must
			// round-trip to the same instant, or the completion check reads the node as
			// unknown health.
			now := time.Now().UTC().Truncate(time.Microsecond)
			raw, err := encodeObservation(1, model.Observation{Status: "stopped", ContainerID: "owned", ObservedAt: now}, nil)
			if err != nil {
				t.Fatal(err)
			}
			var proof []NamespaceCompletion
			for _, suffix := range []string{"a", "b"} {
				id := ownerUUID(t, f.FleetNode(t, ns, testutil.Cols{"status": "stopped", "desired": "stopped", "ready": false, "container_id": "owned", "observation": raw, "health_at": now}))
				key := "shutdown-" + suffix
				op := ownerUUID(t, f.Insert(t, "fleet_node_operations", testutil.Cols{"namespace": ns, "owner_id": f.UserID, "node_id": id, "action": "stop", "phase": "completed", "approved": true, "generation": int64(1), "idempotency_key": key, "request_hash": lifecycleFingerprint(id, model.Stop), "action_claimed_at": now.Add(-time.Second)}))
				node, e := s.GetNode(ctx, owner, id)
				if e != nil {
					t.Fatal(e)
				}
				proof = append(proof, NamespaceCompletion{Node: node, Ref: model.OperationRef{Namespace: ns, NodeID: id, OperationID: op, Generation: 1, Action: model.Stop}, Key: key})
			}
			sort.Slice(proof, func(i, j int) bool { return bytes.Compare(proof[i].Node.ID.Bytes[:], proof[j].Node.ID.Bytes[:]) < 0 })
			fence, e := s.CloseNamespace(ctx, "fixture-fleet", "shutdown")
			if e != nil {
				t.Fatal(e)
			}
			if e = s.CheckNamespaceCompletions(ctx, fence, model.Stop, proof); e != nil {
				t.Fatal(e)
			}
			fence, e = s.GetNamespaceFence(ctx)
			if e != nil {
				t.Fatal(e)
			}
			b := proof[1].Node
			if change == "remove" {
				f.Exec(t, "DELETE FROM fleet_nodes WHERE namespace=$1 AND id=$2", ns, b.ID)
			} else {
				f.Exec(t, "UPDATE fleet_nodes SET image='substituted' WHERE namespace=$1 AND id=$2", ns, b.ID)
			}
			current, e := s.ListNamespaceNodes(ctx, pgtype.UUID{}, 100)
			if e != nil {
				t.Fatal(e)
			}
			var reduced []NamespaceCompletion
			for _, n := range current {
				for _, c := range proof {
					if c.Node.ID == n.ID {
						c.Node = n
						reduced = append(reduced, c)
					}
				}
			}
			if e = s.CheckNamespaceCompletions(ctx, fence, model.Stop, reduced); e == nil {
				t.Error("current subset/baseline replaced original finalized proof")
			}
			if _, e = s.BeginNamespaceDestroy(ctx, fence); e == nil {
				t.Error("trusted destroy CAS crossed original proof drift")
			}
			after, e := s.GetNamespaceFence(ctx)
			if e != nil || after != fence {
				t.Fatalf("drift changed barrier: %v", e)
			}
		})
	}
}

func TestNamespaceSQLCompletionRequiresOriginalReceipt(t *testing.T) {
	for _, kind := range []string{"safe", "safe-delete", "unapproved", "missing-observation", "wrong-key", "wrong-generation", "opened", "altered-snapshot", "unexpected-node"} {
		t.Run(kind, func(t *testing.T) {
			s, f, ns := namespaceFixture(t)
			ctx := context.Background()
			owner := ownerUUID(t, f.UserID)
			// PostgreSQL stores timestamptz at microsecond precision while Go keeps
			// nanoseconds on Linux; the observation JSON and the health_at column must
			// round-trip to the same instant, or the completion check reads the node as
			// unknown health.
			now := time.Now().UTC().Truncate(time.Microsecond)
			o := model.Observation{Status: "stopped", ContainerID: "owned", ObservedAt: now}
			raw, e := encodeObservation(1, o, nil)
			if e != nil {
				t.Fatal(e)
			}
			if kind == "missing-observation" {
				raw = []byte(`{}`)
			}
			cols := testutil.Cols{"status": "stopped", "desired": "stopped", "ready": false, "container_id": "owned", "observation": raw, "health_at": now}
			action := model.Stop
			if kind == "safe-delete" {
				action = model.Delete
				cols["status"] = "terminated"
				cols["desired"] = "terminated"
				cols["revoked"] = true
				cols["maintenance"] = true
				cols["data_volume"] = "owned-data"
				cols["secrets_volume"] = "owned-secrets"
			}
			node := ownerUUID(t, f.FleetNode(t, ns, cols))
			approved := kind != "unapproved"
			key := "shutdown-stop"
			op := ownerUUID(t, f.Insert(t, "fleet_node_operations", testutil.Cols{"namespace": ns, "owner_id": f.UserID, "node_id": node, "action": string(action), "phase": "completed", "approved": approved, "generation": int64(1), "idempotency_key": key, "request_hash": lifecycleFingerprint(node, action), "action_claimed_at": now.Add(-time.Second)}))
			fence, e := s.CloseNamespace(ctx, "fixture-fleet", "shutdown")
			if e != nil {
				t.Fatal(e)
			}
			ref := model.OperationRef{Namespace: ns, NodeID: node, OperationID: op, Generation: 1, Action: action}
			expected, e := s.GetNode(ctx, owner, node)
			if e != nil {
				t.Fatal(e)
			}
			switch kind {
			case "altered-snapshot":
				expected.Image = "foreign-image"
			case "wrong-key":
				key += "wrong"
			case "wrong-generation":
				ref.Generation++
			case "opened":
				if e = s.OpenNamespace(ctx, fence); e != nil {
					t.Fatal(e)
				}
			}
			e = s.CheckNamespaceCompletion(ctx, fence, expected, ref, key)
			if kind == "unexpected-node" {
				f.FleetNode(t, ns)
			}
			if kind == "safe" || kind == "safe-delete" || kind == "unexpected-node" {
				if e != nil {
					t.Fatal(e)
				}
			} else if e == nil {
				t.Fatalf("%s forged completion proof authorized cleanup", kind)
			}
			whole := s.CheckNamespaceCompletions(ctx, fence, action, []NamespaceCompletion{{Node: expected, Ref: ref, Key: key}})
			if kind == "safe" || kind == "safe-delete" {
				if whole != nil {
					t.Fatal(whole)
				}
				final, e := s.GetNamespaceFence(ctx)
				if e != nil || !final.Finalized {
					t.Errorf("whole proof did not persist finalized barrier: %+v %v", final, e)
				}
				current, e := s.GetNode(ctx, owner, node)
				if e != nil {
					t.Fatal(e)
				}
				receipt, found, e := s.LookupNamespaceOperation(ctx, current, action, key)
				if e != nil || !found || receipt.ID != op {
					t.Fatalf("readonly original receipt lookup=%v %v", found, e)
				}
				if _, found, e = s.LookupNamespaceOperation(ctx, current, action, "missing-key"); e != nil || found {
					t.Fatalf("missing receipt minted/adopted: %v %v", found, e)
				}
				for _, a := range []model.Action{model.Stop, model.Delete} {
					if _, e = s.PrepareMaintenance(ctx, owner, node, a, "post-final-"+string(a)); !errors.Is(e, model.ErrBusy) {
						t.Errorf("new %s crossed finalized barrier: %v", a, e)
					}
				}
				if replay, e := s.PrepareMaintenance(ctx, owner, node, action, "shutdown-stop"); e != nil || replay.ID != op {
					t.Errorf("finalized durable replay rejected: %v", e)
				}
				foreign := final
				foreign.OperationKey = "foreign-workflow"
				if _, e = s.BeginNamespaceDestroy(ctx, foreign); !errors.Is(e, model.ErrConflict) {
					t.Errorf("foreign workflow destroy CAS accepted: %v", e)
				}
				bad := final
				bad.Generation++
				if _, e = s.BeginNamespaceDestroy(ctx, bad); !errors.Is(e, model.ErrConflict) {
					t.Errorf("stale destroy cycle CAS accepted: %v", e)
				}
				next, e := s.BeginNamespaceDestroy(ctx, final)
				if e != nil || !next.Closed || next.Finalized || next.Generation != final.Generation+1 || next.FleetID != final.FleetID || next.OperationKey != final.OperationKey {
					t.Errorf("trusted destroy cycle lost closure/epoch identity: %+v %v", next, e)
				}

			} else if whole == nil {
				t.Fatalf("%s bypassed whole namespace completion", kind)
			}
		})
	}
}

// Omitting the gate read, or treating read errors as open, crosses closure.
func TestNamespaceCloseRejectsNewWork(t *testing.T) {
	f := &admissionDB{closed: true}
	if err := CheckNamespaceAdmission(context.Background(), db.New(f), "fixture"); !errors.Is(err, model.ErrBusy) {
		t.Fatalf("new work crossed closed namespace: %v", err)
	}
	if f.execs != 1 {
		t.Fatal("shared prefix lock was not acquired")
	}
	f.closed = false
	if err := CheckNamespaceAdmission(context.Background(), db.New(f), "fixture"); err != nil {
		t.Fatalf("open namespace denied: %v", err)
	}
	f.readErr = errors.New("missing table")
	if err := CheckNamespaceAdmission(context.Background(), db.New(f), "fixture"); !errors.Is(err, model.ErrUnavailable) {
		t.Fatalf("missing schema opened namespace: %v", err)
	}
	f.readErr = pgx.ErrNoRows
	if err := CheckNamespaceAdmission(context.Background(), db.New(f), "fixture"); err != nil {
		t.Fatalf("initial open denied: %v", err)
	}
}
