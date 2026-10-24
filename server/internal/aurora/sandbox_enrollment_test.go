package aurora_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/aurora"
	"github.com/multica-ai/multica/server/internal/auth"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// newSandboxNodeWorkspace builds a throwaway workspace with the managed runtime
// Task 1's sandbox-node rows point at. Nodes carry no foreign key, so their
// rows are removed by workspace id before the workspace cascades away.
func newSandboxNodeWorkspace(t *testing.T, q *db.Queries, pool *pgxpool.Pool) (pgtype.UUID, pgtype.UUID) {
	t.Helper()
	ctx := context.Background()
	owner := newAuroraTestUser(t, pool)
	ws := newAuroraTestWorkspace(t, pool, owner)
	if err := aurora.EnsureSystemAgents(ctx, q, ws, owner); err != nil {
		t.Fatalf("ensure system agents: %v", err)
	}
	runtimeID, err := aurora.ManagedRuntimeID(ctx, q, ws)
	if err != nil {
		t.Fatalf("managed runtime id: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM aurora_sandbox_node WHERE workspace_id = $1`, ws)
		_, _ = pool.Exec(ctx, `DELETE FROM agent_skill WHERE agent_id IN (SELECT id FROM agent WHERE workspace_id = $1)`, ws)
		_, _ = pool.Exec(ctx, `DELETE FROM agent WHERE workspace_id = $1`, ws)
		_, _ = pool.Exec(ctx, `DELETE FROM skill WHERE workspace_id = $1`, ws)
		_, _ = pool.Exec(ctx, `DELETE FROM agent_runtime WHERE workspace_id = $1`, ws)
	})
	return ws, runtimeID
}

// sandboxNodeParams is a valid starting node row. The enrollment hash is unique
// per call so an intended duplicate is the only constraint it can hit.
func sandboxNodeParams(t *testing.T, ws, runtimeID pgtype.UUID, daemonID string) db.CreateAuroraSandboxNodeParams {
	t.Helper()
	raw := "mse_" + uuid.NewString()
	return db.CreateAuroraSandboxNodeParams{
		ID:                  mustUUID(t, uuid.NewString()),
		WorkspaceID:         ws,
		RuntimeID:           runtimeID,
		DaemonID:            daemonID,
		ImageDigest:         "ghcr.io/eanfs/multica-aurora@sha256:" + strings.Repeat("a", 64),
		State:               "starting",
		EnrollmentTokenHash: pgtype.Text{String: auth.HashToken(raw), Valid: true},
		EnrollmentExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Minute), Valid: true},
	}
}

func requireUniqueViolation(t *testing.T, err error, indexName string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("want unique violation on %s, got %v", indexName, err)
	}
	if pgErr.Code != "23505" {
		t.Fatalf("want unique violation on %s, got SQLSTATE %s: %v", indexName, pgErr.Code, err)
	}
	if pgErr.ConstraintName != indexName {
		t.Fatalf("unique violation on %s, want %s", pgErr.ConstraintName, indexName)
	}
}

// TestAuroraSandboxNodeUniquenessAndConsumption pins the Task 1 schema
// invariants: one node per workspace, runtime, and daemon identity, and an
// enrollment hash that is consumable exactly once and only before it expires.
func TestAuroraSandboxNodeUniquenessAndConsumption(t *testing.T) {
	pool := auroraTestPool(t)
	q := db.New(pool)
	ctx := context.Background()

	t.Run("identity uniqueness", func(t *testing.T) {
		wsA, rtA := newSandboxNodeWorkspace(t, q, pool)
		daemonA := uuid.NewString()
		if _, err := q.CreateAuroraSandboxNode(ctx, sandboxNodeParams(t, wsA, rtA, daemonA)); err != nil {
			t.Fatalf("create first node: %v", err)
		}

		// A second row for the same workspace collides on workspace_id even
		// though its runtime and daemon identities are fresh.
		_, err := q.CreateAuroraSandboxNode(ctx, sandboxNodeParams(t, wsA, mustUUID(t, uuid.NewString()), uuid.NewString()))
		requireUniqueViolation(t, err, "aurora_sandbox_node_workspace_uidx")

		// A second workspace may not reuse the first row's runtime.
		wsB, _ := newSandboxNodeWorkspace(t, q, pool)
		_, err = q.CreateAuroraSandboxNode(ctx, sandboxNodeParams(t, wsB, rtA, uuid.NewString()))
		requireUniqueViolation(t, err, "aurora_sandbox_node_runtime_uidx")

		// A second workspace may not reuse the first row's daemon identity.
		wsC, rtC := newSandboxNodeWorkspace(t, q, pool)
		_, err = q.CreateAuroraSandboxNode(ctx, sandboxNodeParams(t, wsC, rtC, daemonA))
		requireUniqueViolation(t, err, "aurora_sandbox_node_daemon_uidx")
	})

	t.Run("consumable exactly once before expiry", func(t *testing.T) {
		ws, rt := newSandboxNodeWorkspace(t, q, pool)
		raw := "mse_" + uuid.NewString()
		hash := auth.HashToken(raw)
		params := sandboxNodeParams(t, ws, rt, uuid.NewString())
		params.EnrollmentTokenHash = pgtype.Text{String: hash, Valid: true}
		params.EnrollmentExpiresAt = pgtype.Timestamptz{Time: time.Now().Add(time.Minute), Valid: true}
		if _, err := q.CreateAuroraSandboxNode(ctx, params); err != nil {
			t.Fatalf("create starting node: %v", err)
		}

		consumed, err := q.ConsumeAuroraSandboxEnrollment(ctx, pgtype.Text{String: hash, Valid: true})
		if err != nil {
			t.Fatalf("consume enrollment: %v", err)
		}
		if consumed.State != "online" {
			t.Errorf("consumed state = %q, want online", consumed.State)
		}
		if consumed.EnrollmentTokenHash.Valid {
			t.Errorf("consumed hash = %q, want NULL", consumed.EnrollmentTokenHash.String)
		}
		if !consumed.EnrollmentConsumedAt.Valid {
			t.Error("consumed_at is NULL, want a timestamp")
		}
		if !consumed.StartedAt.Valid {
			t.Error("started_at is NULL, want a timestamp")
		}

		// Replay: the hash was cleared, so the same token returns no row.
		if _, err := q.ConsumeAuroraSandboxEnrollment(ctx, pgtype.Text{String: hash, Valid: true}); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("replay consume = %v, want no rows", err)
		}

		// Expiry: a token past its expiry is not returned either.
		wsExpired, rtExpired := newSandboxNodeWorkspace(t, q, pool)
		expiredHash := auth.HashToken("mse_" + uuid.NewString())
		expired := sandboxNodeParams(t, wsExpired, rtExpired, uuid.NewString())
		expired.EnrollmentTokenHash = pgtype.Text{String: expiredHash, Valid: true}
		expired.EnrollmentExpiresAt = pgtype.Timestamptz{Time: time.Now().Add(-time.Minute), Valid: true}
		if _, err := q.CreateAuroraSandboxNode(ctx, expired); err != nil {
			t.Fatalf("create expired node: %v", err)
		}
		if _, err := q.ConsumeAuroraSandboxEnrollment(ctx, pgtype.Text{String: expiredHash, Valid: true}); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("expired consume = %v, want no rows", err)
		}
	})
}

// validSandboxImageDigest is a well-formed image reference: the registry host,
// the repository, and a 64-character lowercase sha256 digest.
var validSandboxImageDigest = "ghcr.io/eanfs/multica-aurora@sha256:" + strings.Repeat("a", 64)

// newSandboxEnrollmentService wires the service under test on the fixture pool.
// now is injected so token lifetimes and expiry are asserted exactly.
func newSandboxEnrollmentService(t *testing.T, pool *pgxpool.Pool, now func() time.Time) (*aurora.SandboxEnrollmentService, *db.Queries) {
	t.Helper()
	q := db.New(pool)
	return aurora.NewSandboxEnrollmentService(pool, q, now), q
}

// sandboxNodeByWorkspace loads the workspace's single node row, failing the
// test when it is missing.
func sandboxNodeByWorkspace(t *testing.T, pool *pgxpool.Pool, ws pgtype.UUID) db.AuroraSandboxNode {
	t.Helper()
	node, err := db.New(pool).GetAuroraSandboxNodeByWorkspace(context.Background(), ws)
	if err != nil {
		t.Fatalf("get sandbox node: %v", err)
	}
	return node
}

func TestSandboxEnrollmentIssueCreatesStartingNode(t *testing.T) {
	pool := auroraTestPool(t)
	q := db.New(pool)
	ws, runtimeID := newSandboxNodeWorkspace(t, q, pool)
	ctx := context.Background()
	current := time.Now().UTC().Truncate(time.Microsecond)
	svc, _ := newSandboxEnrollmentService(t, pool, func() time.Time { return current })

	issued, err := svc.Issue(ctx, ws, runtimeID, validSandboxImageDigest)
	if err != nil {
		t.Fatalf("issue enrollment: %v", err)
	}
	if issued.Identity.WorkspaceID != ws {
		t.Errorf("issued workspace = %v, want %v", issued.Identity.WorkspaceID, ws)
	}
	if issued.Identity.RuntimeID != runtimeID {
		t.Errorf("issued runtime = %v, want %v", issued.Identity.RuntimeID, runtimeID)
	}
	if !issued.Identity.NodeID.Valid {
		t.Error("issued node id is NULL")
	}
	if _, err := uuid.Parse(issued.Identity.DaemonID); err != nil {
		t.Errorf("issued daemon id = %q, want a canonical UUID", issued.Identity.DaemonID)
	}
	if !strings.HasPrefix(issued.Token, "mse_") || len(issued.Token) != len("mse_")+40 {
		t.Errorf("issued token %q is not mse_ + 40 hex chars", issued.Token)
	}
	if want := current.Add(5 * time.Minute); !issued.ExpiresAt.Equal(want) {
		t.Errorf("issued expiry = %v, want %v", issued.ExpiresAt, want)
	}

	node := sandboxNodeByWorkspace(t, pool, ws)
	if node.State != "starting" {
		t.Errorf("node state = %q, want starting", node.State)
	}
	if node.ID != issued.Identity.NodeID {
		t.Errorf("stored node id = %v, want %v", node.ID, issued.Identity.NodeID)
	}
	if !node.EnrollmentTokenHash.Valid || node.EnrollmentTokenHash.String != auth.HashToken(issued.Token) {
		t.Errorf("stored enrollment hash = %+v, want the issued token's hash", node.EnrollmentTokenHash)
	}
	if !node.EnrollmentExpiresAt.Valid || !node.EnrollmentExpiresAt.Time.Equal(issued.ExpiresAt) {
		t.Errorf("stored expiry = %+v, want %v", node.EnrollmentExpiresAt, issued.ExpiresAt)
	}
	if node.StartedAt.Valid {
		t.Error("a freshly issued node already has started_at")
	}

	t.Run("rejects an active node without minting a second token", func(t *testing.T) {
		wsActive, rtActive := newSandboxNodeWorkspace(t, q, pool)
		activeSvc, _ := newSandboxEnrollmentService(t, pool, func() time.Time { return current })
		active, err := activeSvc.Issue(ctx, wsActive, rtActive, validSandboxImageDigest)
		if err != nil {
			t.Fatalf("issue active enrollment: %v", err)
		}
		if _, _, err := activeSvc.Consume(ctx, active.Token); err != nil {
			t.Fatalf("consume to activate: %v", err)
		}
		if _, err := activeSvc.Issue(ctx, wsActive, rtActive, validSandboxImageDigest); !errors.Is(err, aurora.ErrSandboxNodeAlreadyActive) {
			t.Fatalf("issue on online node = %v, want ErrSandboxNodeAlreadyActive", err)
		}
		if node := sandboxNodeByWorkspace(t, pool, wsActive); node.EnrollmentTokenHash.Valid {
			t.Error("online node was re-armed with a second secret")
		}

		if _, err := q.MarkAuroraSandboxNodeDraining(ctx, db.MarkAuroraSandboxNodeDrainingParams{
			WorkspaceID: wsActive,
			DaemonID:    active.Identity.DaemonID,
		}); err != nil {
			t.Fatalf("mark draining: %v", err)
		}
		if _, err := activeSvc.Issue(ctx, wsActive, rtActive, validSandboxImageDigest); !errors.Is(err, aurora.ErrSandboxNodeAlreadyActive) {
			t.Fatalf("issue on draining node = %v, want ErrSandboxNodeAlreadyActive", err)
		}
	})

	t.Run("rejects a malformed image reference", func(t *testing.T) {
		wsBad, rtBad := newSandboxNodeWorkspace(t, q, pool)
		badSvc, _ := newSandboxEnrollmentService(t, pool, func() time.Time { return current })
		for _, digest := range []string{
			"ghcr.io/eanfs/multica-aurora:latest",
			"ghcr.io/eanfs/multica-aurora@sha256:" + strings.Repeat("A", 64),
			"ghcr.io/eanfs/multica-aurora@sha256:" + strings.Repeat("a", 63),
			"ghcr.io/eanfs/multica-aurora@sha512:" + strings.Repeat("a", 64),
			"ghcr.io/eanfs/multica-aurora@sha256:" + strings.Repeat("a", 64) + "x",
		} {
			if _, err := badSvc.Issue(ctx, wsBad, rtBad, digest); err == nil {
				t.Errorf("Issue accepted malformed image reference %q", digest)
			}
		}
		if _, err := q.GetAuroraSandboxNodeByWorkspace(ctx, wsBad); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("malformed image reference left a node row: %v", err)
		}
	})
}

func TestSandboxEnrollmentIssueRotatesStoppedNode(t *testing.T) {
	pool := auroraTestPool(t)
	q := db.New(pool)
	ws, runtimeID := newSandboxNodeWorkspace(t, q, pool)
	ctx := context.Background()
	current := time.Now().UTC().Truncate(time.Microsecond)
	svc, _ := newSandboxEnrollmentService(t, pool, func() time.Time { return current })

	first, err := svc.Issue(ctx, ws, runtimeID, validSandboxImageDigest)
	if err != nil {
		t.Fatalf("first issue: %v", err)
	}
	// Simulate a node whose previous launch stopped, leaving the lifecycle
	// markers a real stop writes.
	if _, err := q.MarkAuroraSandboxNodeStopped(ctx, db.MarkAuroraSandboxNodeStoppedParams{
		WorkspaceID: ws,
		DaemonID:    first.Identity.DaemonID,
	}); err != nil {
		t.Fatalf("mark stopped: %v", err)
	}
	var priorStoppedAt pgtype.Timestamptz
	if err := pool.QueryRow(ctx,
		`UPDATE aurora_sandbox_node
		 SET started_at = now(), stopped_at = now(), drain_started_at = now(),
		     backend_node_id = $2, failure_reason = 'previous launch failed'
		 WHERE workspace_id = $1
		 RETURNING stopped_at`,
		ws, "backend-node-1").Scan(&priorStoppedAt); err != nil {
		t.Fatalf("seed stopped lifecycle markers: %v", err)
	}

	second, err := svc.Issue(ctx, ws, runtimeID, validSandboxImageDigest)
	if err != nil {
		t.Fatalf("rotate issue: %v", err)
	}
	if second.Token == first.Token {
		t.Error("rotation reused the previous enrollment secret")
	}
	if second.Identity != first.Identity {
		t.Errorf("rotation changed node identity: got %+v, want %+v", second.Identity, first.Identity)
	}
	if want := current.Add(5 * time.Minute); !second.ExpiresAt.Equal(want) {
		t.Errorf("rotated expiry = %v, want %v", second.ExpiresAt, want)
	}

	node := sandboxNodeByWorkspace(t, pool, ws)
	if node.State != "starting" {
		t.Errorf("rotated state = %q, want starting", node.State)
	}
	if node.ID != first.Identity.NodeID || node.DaemonID != first.Identity.DaemonID || node.RuntimeID != runtimeID {
		t.Errorf("rotation changed node identity: node=%+v", node)
	}
	if !node.EnrollmentTokenHash.Valid || node.EnrollmentTokenHash.String != auth.HashToken(second.Token) {
		t.Errorf("rotated enrollment hash = %+v, want the new secret's hash", node.EnrollmentTokenHash)
	}
	if node.StartedAt.Valid {
		t.Error("rotation did not clear the previous launch's started_at")
	}
	if node.StoppedAt.Valid {
		t.Error("rotation did not clear stopped_at")
	}
	if node.DrainStartedAt.Valid {
		t.Error("rotation did not clear drain_started_at")
	}
	if node.BackendNodeID.Valid {
		t.Error("rotation did not clear backend_node_id")
	}
	if node.FailureReason.Valid {
		t.Error("rotation did not clear failure_reason")
	}
	if node.EnrollmentConsumedAt.Valid {
		t.Error("rotation did not clear enrollment_consumed_at")
	}

	// The rotated secret starts a brand-new launch: its started_at is stamped
	// after the previous stop rather than reused from the previous launch.
	if _, _, err := svc.Consume(ctx, second.Token); err != nil {
		t.Fatalf("consume rotated secret: %v", err)
	}
	node = sandboxNodeByWorkspace(t, pool, ws)
	if !node.StartedAt.Valid {
		t.Fatal("consumed node has no started_at")
	}
	if priorStoppedAt.Valid && node.StartedAt.Time.Before(priorStoppedAt.Time) {
		t.Errorf("new launch started_at %v is before the prior stop %v", node.StartedAt.Time, priorStoppedAt.Time)
	}
}

func TestSandboxEnrollmentConsumeMintsScopedDaemonTokenAndBindsRuntime(t *testing.T) {
	pool := auroraTestPool(t)
	q := db.New(pool)
	ws, runtimeID := newSandboxNodeWorkspace(t, q, pool)
	ctx := context.Background()
	current := time.Now().UTC().Truncate(time.Microsecond)
	svc, _ := newSandboxEnrollmentService(t, pool, func() time.Time { return current })

	issued, err := svc.Issue(ctx, ws, runtimeID, validSandboxImageDigest)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	consumed, bound, err := svc.Consume(ctx, issued.Token)
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	if consumed.Identity != issued.Identity {
		t.Errorf("consumed identity = %+v, want %+v", consumed.Identity, issued.Identity)
	}
	if !strings.HasPrefix(consumed.DaemonToken, "mdt_") || len(consumed.DaemonToken) != len("mdt_")+40 {
		t.Errorf("daemon token %q is not mdt_ + 40 hex chars", consumed.DaemonToken)
	}
	if want := current.Add(8 * time.Hour); !consumed.DaemonTokenExpiresAt.Equal(want) {
		t.Errorf("daemon token expiry = %v, want %v", consumed.DaemonTokenExpiresAt, want)
	}

	stored, err := q.GetDaemonTokenByHash(ctx, auth.HashToken(consumed.DaemonToken))
	if err != nil {
		t.Fatalf("load stored daemon token: %v", err)
	}
	if stored.WorkspaceID != ws {
		t.Errorf("stored daemon token workspace = %v, want %v", stored.WorkspaceID, ws)
	}
	if stored.DaemonID != consumed.Identity.DaemonID {
		t.Errorf("stored daemon token daemon = %q, want %q", stored.DaemonID, consumed.Identity.DaemonID)
	}
	if !stored.ExpiresAt.Valid || !stored.ExpiresAt.Time.Equal(consumed.DaemonTokenExpiresAt) {
		t.Errorf("stored daemon expiry = %+v, want %v", stored.ExpiresAt, consumed.DaemonTokenExpiresAt)
	}
	// MUL-8154: the daemon register gate refuses a token with no owner binding.
	if !stored.OwnerID.Valid {
		t.Fatal("stored daemon token has no owner binding")
	}
	if stored.OwnerID != bound.OwnerID {
		t.Errorf("stored daemon token owner = %v, want the runtime owner %v", stored.OwnerID, bound.OwnerID)
	}

	if bound.ID != runtimeID {
		t.Errorf("bound runtime = %v, want %v", bound.ID, runtimeID)
	}
	if bound.Provider != "aurora_managed" {
		t.Errorf("bound runtime provider = %q, want aurora_managed", bound.Provider)
	}
	if !bound.DaemonID.Valid || bound.DaemonID.String != consumed.Identity.DaemonID {
		t.Errorf("bound runtime daemon = %+v, want %q", bound.DaemonID, consumed.Identity.DaemonID)
	}
	if bound.Status != "online" {
		t.Errorf("bound runtime status = %q, want online", bound.Status)
	}

	persisted, err := q.GetAuroraManagedRuntime(ctx, db.GetAuroraManagedRuntimeParams{
		WorkspaceID: ws,
		Provider:    "aurora_managed",
	})
	if err != nil {
		t.Fatalf("load managed runtime: %v", err)
	}
	if persisted.Provider != "aurora_managed" {
		t.Errorf("persisted provider = %q, want aurora_managed", persisted.Provider)
	}

	node := sandboxNodeByWorkspace(t, pool, ws)
	if node.State != "online" {
		t.Errorf("consumed node state = %q, want online", node.State)
	}
	if node.EnrollmentTokenHash.Valid {
		t.Error("consumed node retained an enrollment hash")
	}
	if !node.StartedAt.Valid {
		t.Error("consumed node has no started_at")
	}

	t.Run("rejects malformed tokens", func(t *testing.T) {
		for _, token := range []string{
			"",
			"mse_",
			"mdt_" + strings.Repeat("a", 40),
			"mse_" + strings.Repeat("A", 40),
			"mse_" + strings.Repeat("a", 39),
		} {
			if _, _, err := svc.Consume(ctx, token); !errors.Is(err, aurora.ErrInvalidManagedEnrollment) {
				t.Errorf("Consume(%q) = %v, want ErrInvalidManagedEnrollment", token, err)
			}
		}
	})
}

func TestSandboxEnrollmentConsumeRejectsExpiredToken(t *testing.T) {
	pool := auroraTestPool(t)
	q := db.New(pool)
	ws, runtimeID := newSandboxNodeWorkspace(t, q, pool)
	ctx := context.Background()
	// An enrollment minted an hour ago expired five minutes after it was
	// issued, so the stored secret is already dead when Consume runs.
	current := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	svc, _ := newSandboxEnrollmentService(t, pool, func() time.Time { return current })

	issued, err := svc.Issue(ctx, ws, runtimeID, validSandboxImageDigest)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, _, err := svc.Consume(ctx, issued.Token); !errors.Is(err, aurora.ErrInvalidManagedEnrollment) {
		t.Fatalf("consume expired = %v, want ErrInvalidManagedEnrollment", err)
	}
	node := sandboxNodeByWorkspace(t, pool, ws)
	if node.State != "starting" {
		t.Errorf("expired consume changed state to %q, want starting", node.State)
	}
	if node.EnrollmentConsumedAt.Valid {
		t.Error("expired consume marked the secret consumed")
	}
	if !node.EnrollmentTokenHash.Valid || node.EnrollmentTokenHash.String != auth.HashToken(issued.Token) {
		t.Error("expired consume cleared the enrollment hash")
	}
}

func TestSandboxEnrollmentConsumeRejectsReplay(t *testing.T) {
	pool := auroraTestPool(t)
	q := db.New(pool)
	ws, runtimeID := newSandboxNodeWorkspace(t, q, pool)
	ctx := context.Background()
	current := time.Now().UTC().Truncate(time.Microsecond)
	svc, _ := newSandboxEnrollmentService(t, pool, func() time.Time { return current })

	issued, err := svc.Issue(ctx, ws, runtimeID, validSandboxImageDigest)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, _, err := svc.Consume(ctx, issued.Token); err != nil {
		t.Fatalf("first consume: %v", err)
	}
	if _, _, err := svc.Consume(ctx, issued.Token); !errors.Is(err, aurora.ErrInvalidManagedEnrollment) {
		t.Fatalf("replay consume = %v, want ErrInvalidManagedEnrollment", err)
	}

	var tokens int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM daemon_token WHERE workspace_id = $1`, ws).Scan(&tokens); err != nil {
		t.Fatalf("count daemon tokens: %v", err)
	}
	if tokens != 1 {
		t.Errorf("daemon token count = %d, want 1 (replay must not mint another)", tokens)
	}
}

func TestSandboxEnrollmentConsumeRollsBackWhenRuntimeBindingFails(t *testing.T) {
	pool := auroraTestPool(t)
	q := db.New(pool)
	ws, runtimeID := newSandboxNodeWorkspace(t, q, pool)
	ctx := context.Background()
	current := time.Now().UTC().Truncate(time.Microsecond)
	svc, _ := newSandboxEnrollmentService(t, pool, func() time.Time { return current })

	issued, err := svc.Issue(ctx, ws, runtimeID, validSandboxImageDigest)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	// Break the carrier invariant the bind predicate checks. The enrollment
	// UPDATE still matches, so only a rolled-back transaction can leave the
	// node starting with its secret intact and no daemon token behind.
	if _, err := pool.Exec(ctx, `UPDATE agent_runtime SET provider = 'user_managed' WHERE id = $1`, runtimeID); err != nil {
		t.Fatalf("break managed runtime: %v", err)
	}

	if _, _, err := svc.Consume(ctx, issued.Token); err == nil {
		t.Fatal("consume with an unbindable runtime succeeded")
	}

	node := sandboxNodeByWorkspace(t, pool, ws)
	if node.State != "starting" {
		t.Errorf("rollback left state = %q, want starting", node.State)
	}
	if !node.EnrollmentTokenHash.Valid || node.EnrollmentTokenHash.String != auth.HashToken(issued.Token) {
		t.Error("rollback lost the enrollment secret")
	}
	if node.EnrollmentConsumedAt.Valid {
		t.Error("rollback left the secret marked consumed")
	}
	var tokens int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM daemon_token WHERE workspace_id = $1`, ws).Scan(&tokens); err != nil {
		t.Fatalf("count daemon tokens: %v", err)
	}
	if tokens != 0 {
		t.Errorf("daemon token count = %d, want 0 after rollback", tokens)
	}
}
