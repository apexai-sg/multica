package service

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/aurora"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// newAuroraCompletionService returns a TaskService wired with the credit
// service, plus the fixture ids its Aurora settlement needs. The credit
// service shares the service's pool/query so reserve/refund runs in one
// accounting instance, exactly like the production wiring.
func newAuroraCompletionService(t *testing.T) (svc *TaskService, pool *pgxpool.Pool, workspaceID, userID, agentID string) {
	t.Helper()
	pool = sharedTestPool(t)
	workspaceID, userID, agentID, _ = seedAttributionFixture(t, pool)
	// credit_balance / credit_ledger carry no FK to user, so the fixture's own
	// user cleanup would leave them behind. Remove them here, before the user
	// row is deleted (cleanups run last-registered-first).
	t.Cleanup(func() {
		ctx := context.Background()
		pool.Exec(ctx, `DELETE FROM credit_ledger WHERE user_id = $1`, userID)
		pool.Exec(ctx, `DELETE FROM credit_balance WHERE user_id = $1`, userID)
	})
	q := db.New(pool)
	svc = NewTaskService(q, pool, nil, events.New())
	svc.Credit = aurora.NewCreditService(q, pool)
	return svc, pool, workspaceID, userID, agentID
}

// seedAuroraTask inserts a queued task on agentID and a generation row that
// reverse-links to it via task_id, returning both ids. The rows are removed on
// cleanup (there is no FK, so the fixture's own cleanup would leave them).
func seedAuroraTask(t *testing.T, pool *pgxpool.Pool, workspaceID, userID, agentID string, reserved int64) (generationID, taskID pgtype.UUID) {
	t.Helper()
	ctx := context.Background()
	// An active (queued/running) task must carry a runtime_id (check constraint
	// agent_task_queue_active_requires_runtime); reuse the agent's runtime.
	var runtimeID string
	if err := pool.QueryRow(ctx, `SELECT runtime_id::text FROM agent WHERE id = $1`, agentID).Scan(&runtimeID); err != nil {
		t.Fatalf("load agent runtime: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority)
		VALUES ($1, $2, 'queued', 0) RETURNING id`, agentID, runtimeID).Scan(&taskID); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	t.Cleanup(func() { pool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID) })

	if err := pool.QueryRow(ctx, `
		INSERT INTO aurora_generation (workspace_id, user_id, skill_id, prompt, status, task_id, credits_reserved)
		VALUES ($1, $2, 'xhs-image', 'test prompt', 'queued', $3, $4) RETURNING id`,
		workspaceID, userID, taskID, reserved).Scan(&generationID); err != nil {
		t.Fatalf("seed generation: %v", err)
	}
	t.Cleanup(func() { pool.Exec(context.Background(), `DELETE FROM aurora_generation WHERE id = $1`, generationID) })
	return generationID, taskID
}

func auroraGenRow(t *testing.T, pool *pgxpool.Pool, id pgtype.UUID) db.AuroraGeneration {
	t.Helper()
	// Read by id directly: GetAuroraGeneration filters by workspace_id, which the
	// settlement path does not carry separately, so the raw read mirrors what the
	// settlement actually observed.
	var gen db.AuroraGeneration
	if err := pool.QueryRow(context.Background(), `
		SELECT id, workspace_id, user_id, skill_id, prompt, status, task_id,
		       credits_reserved, credits_charged, error, created_at, updated_at
		FROM aurora_generation WHERE id = $1`, id).Scan(
		&gen.ID, &gen.WorkspaceID, &gen.UserID, &gen.SkillID, &gen.Prompt, &gen.Status,
		&gen.TaskID, &gen.CreditsReserved, &gen.CreditsCharged, &gen.Error,
		&gen.CreatedAt, &gen.UpdatedAt); err != nil {
		t.Fatalf("read generation: %v", err)
	}
	return gen
}

// seedAuroraAsset inserts the committed asset a completed generation must have
// before settlement charges it.
func seedAuroraAsset(t *testing.T, pool *pgxpool.Pool, generationID pgtype.UUID, workspaceID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO aurora_asset (
			generation_id, workspace_id, kind, media_url, format, manifest_artifact_id,
			name, mime_type, size_bytes, sha256, role, metadata
		) VALUES ($1, $2, 'image', 'https://cdn.example/out.png', 'png', 'primary-1',
			'out.png', 'image/png', 12, 'sha256:' || repeat('a', 64), 'primary', '{}'::jsonb)`,
		generationID, workspaceID); err != nil {
		t.Fatalf("seed asset: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM aurora_asset WHERE generation_id = $1`, generationID)
	})
}

func TestAuroraCompletionChargesReserved(t *testing.T) {
	svc, pool, workspaceID, userID, agentID := newAuroraCompletionService(t)
	genID, taskID := seedAuroraTask(t, pool, workspaceID, userID, agentID, 62_000_000)
	seedAuroraAsset(t, pool, genID, workspaceID)

	svc.settleAuroraOnCompleted(context.Background(), db.AgentTaskQueue{ID: taskID})

	gen := auroraGenRow(t, pool, genID)
	if gen.Status != "completed" {
		t.Fatalf("status = %q, want completed", gen.Status)
	}
	if gen.CreditsCharged != 62_000_000 {
		t.Fatalf("credits_charged = %d, want 62000000", gen.CreditsCharged)
	}
	// Completion charges what was already reserved; the wallet must not move.
	bal, err := svc.Credit.Balance(context.Background(), gen.UserID)
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if bal != 0 {
		t.Fatalf("balance = %d, want 0 (reservation is the only debit)", bal)
	}
}

func TestAuroraCompletionFailureRefunds(t *testing.T) {
	svc, pool, workspaceID, userID, agentID := newAuroraCompletionService(t)
	ctx := context.Background()
	user := util.MustParseUUID(userID)
	ws := util.MustParseUUID(workspaceID)

	// Fund the wallet so the reservation can be modelled as already spent, then
	// reserve (simulating creation) so the refund has something to return. The
	// reference is namespaced by user id: the idempotency key is globally
	// unique, and a constant would satisfy a later run's fast path against a
	// row left by an abnormally killed run.
	if err := svc.Credit.Grant(ctx, user, ws, 1_000_000_000, aurora.LedgerKindAdjustment, "settle-failed-seed-"+userID); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	genID, taskID := seedAuroraTask(t, pool, workspaceID, userID, agentID, 62_000_000)
	if err := svc.Credit.Reserve(ctx, user, ws, 62_000_000, util.UUIDToString(genID)); err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	svc.settleAuroraOnFailed(context.Background(), db.AgentTaskQueue{ID: taskID}, "agent_error")

	gen := auroraGenRow(t, pool, genID)
	if gen.Status != "failed" {
		t.Fatalf("status = %q, want failed", gen.Status)
	}
	if !gen.Error.Valid || gen.Error.String != "agent_error" {
		t.Fatalf("error = %q, want agent_error", gen.Error.String)
	}
	if gen.CreditsCharged != 0 {
		t.Fatalf("credits_charged = %d, want 0", gen.CreditsCharged)
	}
	// The reservation is fully returned.
	bal, err := svc.Credit.Balance(ctx, user)
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if bal != 1_000_000_000 {
		t.Fatalf("balance after refund = %d, want 1000000000", bal)
	}

	// Idempotency: a second settlement of the same terminal event is a no-op.
	svc.settleAuroraOnFailed(context.Background(), db.AgentTaskQueue{ID: taskID}, "agent_error")
	bal, err = svc.Credit.Balance(ctx, user)
	if err != nil {
		t.Fatalf("Balance (retry): %v", err)
	}
	if bal != 1_000_000_000 {
		t.Fatalf("balance after idempotent retry = %d, want 1000000000", bal)
	}
}

// TestAuroraCompletionWithoutAssetsFails pins the server-side guard: a
// completion callback with no committed asset is a failed, refunded run, not a
// charge for nothing.
func TestAuroraCompletionWithoutAssetsFails(t *testing.T) {
	svc, pool, workspaceID, userID, agentID := newAuroraCompletionService(t)
	ctx := context.Background()
	user := util.MustParseUUID(userID)
	ws := util.MustParseUUID(workspaceID)

	if err := svc.Credit.Grant(ctx, user, ws, 1_000_000_000, aurora.LedgerKindAdjustment, "settle-noasset-seed-"+userID); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	genID, taskID := seedAuroraTask(t, pool, workspaceID, userID, agentID, 62_000_000)
	if err := svc.Credit.Reserve(ctx, user, ws, 62_000_000, util.UUIDToString(genID)); err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	svc.settleAuroraOnCompleted(ctx, db.AgentTaskQueue{ID: taskID})

	gen := auroraGenRow(t, pool, genID)
	if gen.Status != "failed" {
		t.Fatalf("status = %q, want failed", gen.Status)
	}
	if !gen.Error.Valid || gen.Error.String != "no artifact reported" {
		t.Fatalf("error = %q, want no artifact reported", gen.Error.String)
	}
	if gen.CreditsCharged != 0 {
		t.Fatalf("credits_charged = %d, want 0", gen.CreditsCharged)
	}
	bal, err := svc.Credit.Balance(ctx, user)
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if bal != 1_000_000_000 {
		t.Fatalf("balance after refund = %d, want 1000000000", bal)
	}
}

func TestAuroraCompletionNoOpForNonAuroraTask(t *testing.T) {
	svc, pool, _, _, agentID := newAuroraCompletionService(t)
	ctx := context.Background()

	// A task with no generation must settle to a silent no-op.
	var runtimeID string
	if err := pool.QueryRow(ctx, `SELECT runtime_id::text FROM agent WHERE id = $1`, agentID).Scan(&runtimeID); err != nil {
		t.Fatalf("load agent runtime: %v", err)
	}
	var taskID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority)
		VALUES ($1, $2, 'queued', 0) RETURNING id`, agentID, runtimeID).Scan(&taskID); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	t.Cleanup(func() { pool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID) })

	svc.settleAuroraOnCompleted(ctx, db.AgentTaskQueue{ID: taskID})
	svc.settleAuroraOnFailed(ctx, db.AgentTaskQueue{ID: taskID}, "agent_error")

	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM aurora_generation WHERE task_id = $1`, taskID).Scan(&n); err != nil {
		t.Fatalf("count generations: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected no generation for a non-Aurora task, found %d", n)
	}
}

// TestAuroraFailureReasonPrefersTheDaemonsMessage pins the fix for #220. The
// generation used to carry only the classified taxonomy code, and
// "agent_error.unknown" tells the person who just lost their output nothing
// while the daemon's own message names the cause.
func TestAuroraFailureReasonPrefersTheDaemonsMessage(t *testing.T) {
	const reported = "artifact validation failed: aurora: artifact manifest is missing: open /workspace/output: no such file or directory"
	if got := auroraFailureReason(reported, "agent_error.unknown"); got != reported {
		t.Fatalf("reason = %q, want the daemon's message", got)
	}
	// The provider-failure shape from the same sweep.
	const provider = "artifact report failed: POST /api/daemon/tasks/01a12454/artifacts returned 500: content moderation unavailable"
	if got := auroraFailureReason(provider, "agent_error.provider_server_error"); got != provider {
		t.Fatalf("reason = %q, want the daemon's message", got)
	}
}

func TestAuroraFailureReasonFallsBackToTheClassifiedCode(t *testing.T) {
	for _, message := range []string{"", "   ", "\n\t "} {
		got := auroraFailureReason(message, "agent_error.provider_server_error")
		if got != "agent_error.provider_server_error" {
			t.Errorf("for message %q the reason = %q, want the classified code", message, got)
		}
	}
}

func TestAuroraFailureReasonHasALastResort(t *testing.T) {
	if got := auroraFailureReason("", ""); got != "task failed" {
		t.Errorf("reason = %q, want %q", got, "task failed")
	}
}

func TestAuroraFailureReasonTrimsTheMessage(t *testing.T) {
	if got := auroraFailureReason("  artifact missing  ", "agent_error.unknown"); got != "artifact missing" {
		t.Errorf("reason = %q, want it trimmed", got)
	}
}

// TestAuroraFailureReasonBoundsTheMessage covers the column's real reader: the
// generation detail view, not a log browser. A provider response body embedded
// in the daemon's message can be arbitrarily long.
func TestAuroraFailureReasonBoundsTheMessage(t *testing.T) {
	got := auroraFailureReason(strings.Repeat("x", 2000), "agent_error.unknown")
	if n := utf8.RuneCountInString(got); n != auroraFailureReasonMaxRunes+1 {
		t.Fatalf("reason is %d runes, want %d plus the ellipsis", n, auroraFailureReasonMaxRunes)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatal("a truncated reason must say so")
	}
}

// TestTruncateRunesKeepsTheStringValid guards the rune boundary. The
// byte-based truncate used elsewhere in this package would split a multi-byte
// rune and write invalid UTF-8 into a column the API returns as JSON, and these
// messages carry Chinese.
func TestTruncateRunesKeepsTheStringValid(t *testing.T) {
	const message = "图片是 85% 肤色占比，已达到 60% 的显式内容阈值"
	got := truncateRunes(message, 6)
	if !utf8.ValidString(got) {
		t.Fatalf("truncation produced invalid UTF-8: %q", got)
	}
	if n := utf8.RuneCountInString(got); n != 7 { // six runes plus the ellipsis
		t.Fatalf("truncated to %d runes, want 6 plus the ellipsis: %q", n, got)
	}
}

func TestTruncateRunesLeavesShortStringsAlone(t *testing.T) {
	const message = "short enough"
	if got := truncateRunes(message, 100); got != message {
		t.Fatalf("truncateRunes(%q, 100) = %q, want it unchanged", message, got)
	}
	if got := truncateRunes(message, 0); got != message {
		t.Fatalf("a non-positive limit must not truncate: %q", got)
	}
}

// TestSettleSkipsNeedsInputGeneration pins the question-round exit at the
// settlement boundary: once the round is marked needs_input, neither the
// completion nor the failure settlement moves a ledger row or changes the
// status, so the reservation stays held.
func TestSettleSkipsNeedsInputGeneration(t *testing.T) {
	svc, pool, workspaceID, userID, agentID := newAuroraCompletionService(t)
	ctx := context.Background()
	user := util.MustParseUUID(userID)
	ws := util.MustParseUUID(workspaceID)

	if err := svc.Credit.Grant(ctx, user, ws, 1_000_000_000, aurora.LedgerKindAdjustment, "needs-input-seed-"+userID); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	genID, taskID := seedAuroraTask(t, pool, workspaceID, userID, agentID, 62_000_000)
	if err := svc.Credit.Reserve(ctx, user, ws, 62_000_000, util.UUIDToString(genID)); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE aurora_generation SET status = 'needs_input' WHERE id = $1`, genID); err != nil {
		t.Fatalf("mark needs_input: %v", err)
	}

	var ledgerBefore int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM credit_ledger WHERE user_id = $1`, userID).Scan(&ledgerBefore); err != nil {
		t.Fatalf("count ledger: %v", err)
	}

	svc.settleAuroraOnCompleted(ctx, db.AgentTaskQueue{ID: taskID})
	svc.settleAuroraOnFailed(ctx, db.AgentTaskQueue{ID: taskID}, "agent_error")

	gen := auroraGenRow(t, pool, genID)
	if gen.Status != "needs_input" {
		t.Fatalf("status = %q, want needs_input", gen.Status)
	}
	if gen.CreditsCharged != 0 {
		t.Fatalf("credits_charged = %d, want 0", gen.CreditsCharged)
	}
	if gen.Error.Valid {
		t.Fatalf("error = %q, want unset", gen.Error.String)
	}

	var ledgerAfter int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM credit_ledger WHERE user_id = $1`, userID).Scan(&ledgerAfter); err != nil {
		t.Fatalf("count ledger: %v", err)
	}
	if ledgerAfter != ledgerBefore {
		t.Fatalf("ledger rows = %d after settling a needs_input round, want %d", ledgerAfter, ledgerBefore)
	}
	bal, err := svc.Credit.Balance(ctx, user)
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if bal != 1_000_000_000-62_000_000 {
		t.Fatalf("balance = %d, want the reservation held (%d)", bal, 1_000_000_000-62_000_000)
	}
}
