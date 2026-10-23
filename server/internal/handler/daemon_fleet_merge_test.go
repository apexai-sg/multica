package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/fleet"
	"github.com/multica-ai/multica/server/internal/fleet/model"
	"github.com/multica-ai/multica/server/internal/fleet/store"
	"github.com/multica-ai/multica/server/internal/middleware"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestFleetLegacyMergeRejectsManagedSourceOrTarget(t *testing.T) {
	for _, direction := range []string{"managed-ordinary", "ordinary-managed", "managed-managed", "missing-node", "bad-marker"} {
		t.Run(direction, func(t *testing.T) {
			pool, f := testutil.NewFleetFixture(t)
			ns := "task6-merge-" + uuid.NewString()
			node := f.FleetNode(t, ns)
			managed := json.RawMessage(fmt.Sprintf(`{"managed_by":"local_fleet","fleet_node_id":"%s"}`, node))
			sourceMeta, targetMeta := json.RawMessage(`{}`), json.RawMessage(`{}`)
			switch direction {
			case "managed-ordinary":
				sourceMeta = managed
			case "ordinary-managed":
				targetMeta = managed
			case "managed-managed":
				sourceMeta = managed
				targetMeta = managed
			case "missing-node":
				sourceMeta = json.RawMessage(fmt.Sprintf(`{"managed_by":"local_fleet","fleet_node_id":"%s"}`, uuid.NewString()))
			case "bad-marker":
				sourceMeta = json.RawMessage(`{"managed_by":"local_fleet","fleet_node_id":"invalid"}`)
			}
			source := f.Runtime(t, "merge-source", testutil.Cols{"metadata": sourceMeta})
			target := f.Runtime(t, "merge-target", testutil.Cols{"metadata": targetMeta})
			ag := f.Agent(t, "merge-agent", source)
			task := f.Task(t, ag, testutil.Cols{"runtime_id": source})
			h := &Handler{Queries: db.New(pool), TxStarter: pool}
			if err := h.mergeLegacyRuntime(context.Background(), util.MustParseUUID(target), util.MustParseUUID(source), "old", "claude", util.MustParseUUID(f.UserID)); err == nil {
				t.Fatal("managed runtime merge was allowed")
			}
			a, err := h.Queries.GetAgent(context.Background(), util.MustParseUUID(ag))
			if err != nil {
				t.Fatal(err)
			}
			row, err := h.Queries.GetAgentTask(context.Background(), util.MustParseUUID(task))
			if err != nil {
				t.Fatal(err)
			}
			if a.RuntimeID != util.MustParseUUID(source) || row.RuntimeID != util.MustParseUUID(source) {
				t.Fatal("rejected merge reassigned agent or task")
			}
			for _, id := range []string{source, target} {
				if _, err := h.Queries.GetAgentRuntime(context.Background(), util.MustParseUUID(id)); err != nil {
					t.Fatalf("rejected merge removed runtime: %v", err)
				}
			}
		})
	}
}

type fleetDaemonStarter struct {
	pool    *pgxpool.Pool
	entered chan struct{}
	once    sync.Once
}

func (s *fleetDaemonStarter) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &fleetDaemonTx{Tx: tx, s: s}, nil
}

type fleetDaemonTx struct {
	pgx.Tx
	s *fleetDaemonStarter
}

func (t *fleetDaemonTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if strings.Contains(sql, "name: FleetNodeSharedLock") {
		t.s.once.Do(func() { close(t.s.entered) })
	}
	return t.Tx.Exec(ctx, sql, args...)
}

func TestFleetLegacyMergeMaintenanceRace(t *testing.T) {
	pool, f := testutil.NewFleetFixture(t)
	ns := "task6-merge-race-" + uuid.NewString()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	node := f.FleetNode(t, ns)
	source := f.Runtime(t, "race-source", testutil.Cols{"metadata": json.RawMessage(fmt.Sprintf(`{"managed_by":"local_fleet","fleet_node_id":"%s"}`, node))})
	target := f.Runtime(t, "race-target")
	ag := f.Agent(t, "race-source", source)
	task := f.Task(t, ag, testutil.Cols{"runtime_id": source})
	maint, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer maint.Rollback(context.Background())
	if err := db.New(maint).FleetNodeExclusiveLock(ctx, db.FleetNodeExclusiveLockParams{Namespace: ns, NodeID: util.MustParseUUID(node)}); err != nil {
		t.Fatal(err)
	}
	if _, err := maint.Exec(ctx, "UPDATE fleet_nodes SET maintenance=true WHERE id=$1 AND namespace=$2", node, ns); err != nil {
		t.Fatal(err)
	}
	starter := &fleetDaemonStarter{pool: pool, entered: make(chan struct{})}
	h := &Handler{Queries: db.New(pool), TxStarter: starter}
	done := make(chan error, 1)
	go func() {
		done <- h.mergeLegacyRuntime(ctx, util.MustParseUUID(target), util.MustParseUUID(source), "old", "claude", util.MustParseUUID(f.UserID))
	}()
	select {
	case <-starter.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	probe, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := probe.Exec(ctx, "SELECT id FROM workspace WHERE id=$1 FOR UPDATE NOWAIT", f.WorkspaceID); err != nil {
		t.Fatalf("merge workspace preceded node: %v", err)
	}
	if _, err := probe.Exec(ctx, "SELECT id FROM agent_runtime WHERE id=ANY($1::uuid[]) FOR UPDATE NOWAIT", []string{source, target}); err != nil {
		t.Fatalf("merge runtimes preceded node: %v", err)
	}
	_ = probe.Rollback(ctx)
	if err := maint.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("managed merge passed maintenance")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	agent, err := h.Queries.GetAgent(ctx, util.MustParseUUID(ag))
	if err != nil {
		t.Fatal(err)
	}
	row, err := h.Queries.GetAgentTask(ctx, util.MustParseUUID(task))
	if err != nil {
		t.Fatal(err)
	}
	if agent.RuntimeID != util.MustParseUUID(source) || row.RuntimeID != util.MustParseUUID(source) {
		t.Fatal("maintenance merge changed managed references")
	}
}

func TestFleetRegisterRechecksCredentialInInitialUpsertTransaction(t *testing.T) {
	pool, f := testutil.NewFleetFixture(t)
	ns := "task6-register-race-" + uuid.NewString()
	t.Logf("Task6FixScope namespace=%s owner=%s workspace=%s", ns, f.UserID, f.WorkspaceID)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	id := f.FleetNode(t, ns)
	repo := store.New(pool, ns)
	cleanupTask5Fleet(t, pool, ns, f.UserID, f.WorkspaceID)
	token, _, err := repo.MintNodeToken(ctx, util.MustParseUUID(id))
	if err != nil {
		t.Fatal(err)
	}
	node, err := db.New(pool).GetFleetNodeIdentity(ctx, db.GetFleetNodeIdentityParams{NodeID: util.MustParseUUID(id), OwnerID: util.MustParseUUID(f.UserID)})
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte("task6-test-only-012345678901234567890123456789")
	srv := httptest.NewServer(fleet.NewService(repo, model.Config{Namespace: ns}, nil).Handler(secret))
	defer srv.Close()
	verifier := auth.NewCloudPATVerifier(auth.CloudPATVerifierConfig{FleetBaseURL: srv.URL, ServiceSecret: secret})
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if err := db.New(blocker).FleetNodeExclusiveLock(ctx, db.FleetNodeExclusiveLockParams{Namespace: ns, NodeID: node.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := blocker.Exec(ctx, "UPDATE fleet_node_credentials SET revoked_at=now() WHERE namespace=$1 AND node_id=$2", ns, id); err != nil {
		t.Fatal(err)
	}
	starter := &fleetDaemonStarter{pool: pool, entered: make(chan struct{})}
	h := *testHandler
	h.Queries = db.New(pool)
	h.TxStarter = starter
	h.cfg.LocalFleetURL = srv.URL
	body := fmt.Sprintf(`{"workspace_id":"%s","daemon_id":"%s","runtimes":[{"type":"claude","name":"fixture"}]}`, f.WorkspaceID, util.UUIDToString(node.DaemonID))
	req := httptest.NewRequest("POST", "/api/daemon/register", strings.NewReader(body)).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+token)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		middleware.DaemonAuth(h.Queries, nil, nil, verifier)(http.HandlerFunc(h.DaemonRegister)).ServeHTTP(w, req)
		done <- w
	}()
	select {
	case <-starter.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	probe, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := probe.Exec(ctx, "SELECT id FROM workspace WHERE id=$1 FOR UPDATE NOWAIT", f.WorkspaceID); err != nil {
		t.Fatalf("initial registration workspace preceded node: %v", err)
	}
	_ = probe.Rollback(ctx)
	if err := blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case w := <-done:
		if w.Code != 403 {
			t.Fatalf("revoked registration=%d body=%s", w.Code, w.Body.String())
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM agent_runtime WHERE workspace_id=$1 AND owner_id=$2", f.WorkspaceID, f.UserID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("revoked token inserted %d initial runtimes", count)
	}
}

func TestFleetRegisterProfilesRecheckCredentialInInitialTransaction(t *testing.T) {
	for _, path := range []string{"custom", "failed"} {
		t.Run(path, func(t *testing.T) {
			pool, f := testutil.NewFleetFixture(t)
			ns := "task6-register-race-" + uuid.NewString()
			t.Logf("Task6FixScope namespace=%s owner=%s workspace=%s", ns, f.UserID, f.WorkspaceID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			id := f.FleetNode(t, ns)
			repo := store.New(pool, ns)
			cleanupTask5Fleet(t, pool, ns, f.UserID, f.WorkspaceID)
			token, _, err := repo.MintNodeToken(ctx, util.MustParseUUID(id))
			if err != nil {
				t.Fatal(err)
			}
			node, err := db.New(pool).GetFleetNodeIdentity(ctx, db.GetFleetNodeIdentityParams{NodeID: util.MustParseUUID(id), OwnerID: util.MustParseUUID(f.UserID)})
			if err != nil {
				t.Fatal(err)
			}
			secret := []byte("task6-test-only-012345678901234567890123456789")
			srv := httptest.NewServer(fleet.NewService(repo, model.Config{Namespace: ns}, nil).Handler(secret))
			defer srv.Close()
			verifier := auth.NewCloudPATVerifier(auth.CloudPATVerifierConfig{FleetBaseURL: srv.URL, ServiceSecret: secret})
			blocker, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback(context.Background())
			if err := db.New(blocker).FleetNodeExclusiveLock(ctx, db.FleetNodeExclusiveLockParams{Namespace: ns, NodeID: node.ID}); err != nil {
				t.Fatal(err)
			}
			if _, err := blocker.Exec(ctx, "UPDATE fleet_node_credentials SET revoked_at=now() WHERE namespace=$1 AND node_id=$2", ns, id); err != nil {
				t.Fatal(err)
			}
			starter := &fleetDaemonStarter{pool: pool, entered: make(chan struct{})}
			h := *testHandler
			h.Queries = db.New(pool)
			h.TxStarter = starter
			h.cfg.LocalFleetURL = srv.URL
			profile := f.Insert(t, "runtime_profile", testutil.Cols{"workspace_id": f.WorkspaceID, "display_name": "fake profile", "protocol_family": "claude", "command_name": "/test-only-missing/claude", "created_by": f.UserID})
			body := fmt.Sprintf(`{"workspace_id":"%s","daemon_id":"%s","runtimes":[{"type":"claude","name":"fixture","profile_id":"%s"}]}`, f.WorkspaceID, util.UUIDToString(node.DaemonID), profile)
			if path == "failed" {
				body = fmt.Sprintf(`{"workspace_id":"%s","daemon_id":"%s","failed_profiles":[{"profile_id":"%s","reason":"fixture missing"}]}`, f.WorkspaceID, util.UUIDToString(node.DaemonID), profile)
			}
			req := httptest.NewRequest("POST", "/api/daemon/register", strings.NewReader(body)).WithContext(ctx)
			req.Header.Set("Authorization", "Bearer "+token)
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				w := httptest.NewRecorder()
				middleware.DaemonAuth(h.Queries, nil, nil, verifier)(http.HandlerFunc(h.DaemonRegister)).ServeHTTP(w, req)
				done <- w
			}()
			select {
			case <-starter.entered:
			case w := <-done:
				t.Fatalf("profile registration bypassed node gate: status=%d", w.Code)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			probe, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := probe.Exec(ctx, "SELECT id FROM workspace WHERE id=$1 FOR UPDATE NOWAIT", f.WorkspaceID); err != nil {
				t.Fatalf("initial registration workspace preceded node: %v", err)
			}
			if _, err := probe.Exec(ctx, "SELECT id FROM runtime_profile WHERE id=$1 FOR UPDATE NOWAIT", profile); err != nil {
				t.Fatalf("profile row preceded node admission: %v", err)
			}
			_ = probe.Rollback(ctx)
			if err := blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case w := <-done:
				if w.Code != 403 {
					t.Fatalf("revoked registration=%d body=%s", w.Code, w.Body.String())
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			var count int
			if err := pool.QueryRow(ctx, "SELECT count(*) FROM agent_runtime WHERE workspace_id=$1 AND owner_id=$2", f.WorkspaceID, f.UserID).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("revoked token inserted %d initial runtimes", count)
			}
		})
	}
}
