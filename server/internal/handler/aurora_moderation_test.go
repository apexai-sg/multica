package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/aurora"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// stubModerator is a Moderator with scripted verdicts, so these tests own the
// wiring — status codes, refunds, audit rows — without depending on the
// contents of the repository blocklist. The default adapter's own behaviour
// (which terms fire, which images are rejected) is tested in
// server/internal/aurora, its canonical layer.
type stubModerator struct {
	promptDecision aurora.Decision
	promptErr      error
	assetDecision  aurora.Decision
	assetErr       error
	// blockAssetURL rejects only the artifacts whose media URL contains it, so
	// a batch can carry a clean artifact ahead of a rejected one.
	blockAssetURL string

	promptCalls int
	assetCalls  int
	lastKind    string
}

func (m *stubModerator) ScreenPrompt(context.Context, string) (aurora.Decision, error) {
	m.promptCalls++
	return m.promptDecision, m.promptErr
}

func (m *stubModerator) ScreenAsset(_ context.Context, mediaURL, kind string) (aurora.Decision, error) {
	m.assetCalls++
	m.lastKind = kind
	if m.assetErr != nil {
		return aurora.Decision{}, m.assetErr
	}
	if m.blockAssetURL != "" {
		if strings.Contains(mediaURL, m.blockAssetURL) {
			return aurora.Decision{Allowed: false, Reason: "image is explicit"}, nil
		}
		return aurora.Decision{Allowed: true}, nil
	}
	return m.assetDecision, nil
}

// withAuroraModeration installs a moderator on the shared test handler for one
// test and restores the previous one at cleanup.
func withAuroraModeration(t *testing.T, m aurora.Moderator) {
	t.Helper()
	original := testHandler.Moderation
	testHandler.Moderation = m
	t.Cleanup(func() { testHandler.Moderation = original })
}

// moderationLogRow is the subset of aurora_moderation_log the wiring asserts.
type moderationLogRow struct {
	GenerationID pgtype.UUID
	WorkspaceID  string
	Scope        string
	Verdict      string
	Reason       string
}

func auroraModerationLogRows(t *testing.T) []moderationLogRow {
	t.Helper()
	rows, err := testPool.Query(context.Background(), `
		SELECT generation_id, workspace_id::text, scope, verdict, reason
		FROM aurora_moderation_log
		WHERE workspace_id = $1
		ORDER BY created_at DESC`, testWorkspaceID)
	if err != nil {
		t.Fatalf("query aurora_moderation_log: %v", err)
	}
	defer rows.Close()

	var out []moderationLogRow
	for rows.Next() {
		var row moderationLogRow
		if err := rows.Scan(&row.GenerationID, &row.WorkspaceID, &row.Scope, &row.Verdict, &row.Reason); err != nil {
			t.Fatalf("scan aurora_moderation_log: %v", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate aurora_moderation_log: %v", err)
	}
	return out
}

func resetAuroraModerationLog(t *testing.T) {
	t.Helper()
	dbfx.Exec(t, `DELETE FROM aurora_moderation_log WHERE workspace_id = $1`, testWorkspaceID)
}

// cleanupAuroraWork removes what a test created through the create endpoint —
// the enqueued task, the generation, its assets and the audit rows — so the
// shared workspace is left as it was found. Task rows go first: the package's
// `LIMIT 1` agent-runtime heuristics can trip over a task left on an Aurora
// system agent.
func cleanupAuroraWork(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		if _, err := testPool.Exec(ctx, `
			DELETE FROM agent_task_queue
			WHERE agent_id IN (
				SELECT id FROM agent WHERE workspace_id = $1 AND system_key LIKE 'aurora:%'
			)`, testWorkspaceID); err != nil {
			t.Errorf("cleanup aurora tasks: %v", err)
		}
		resetAuroraGenerations(t)
		resetAuroraModerationLog(t)
	})
}

// TestAuroraModerationBlocksBlockedPrompt is the prompt pre-screen's happy
// path: a blocked prompt is refused before anything is written. It runs the
// real default adapter rather than a stub, because the one thing a stub cannot
// prove is that the repository blocklist and the handler agree on what
// "blocked" means.
func TestAuroraModerationBlocksBlockedPrompt(t *testing.T) {
	creditTestReset(t)
	resetAuroraGenerations(t)
	resetAuroraModerationLog(t)

	req := newRequest(http.MethodPost, "/api/aurora/generations", map[string]string{
		"skillId": "ecommerce-bidding-strategy",
		"prompt":  "帮我写一篇关于儿童色情的推广文案",
	})
	testutil.Call(t, testHandler.CreateAuroraGeneration, req).Want(http.StatusUnprocessableEntity)

	// No generation row: the screen runs before the insert, so a rejected
	// prompt leaves no queued work behind.
	if n := dbfx.Count(t, `SELECT count(*) FROM aurora_generation WHERE workspace_id = $1`, testWorkspaceID); n != 0 {
		t.Fatalf("generations after a blocked prompt = %d, want 0", n)
	}
	// Nothing enqueued and nothing charged: the screen also precedes the agent
	// seed and the reservation.
	if n := dbfx.Count(t, `
		SELECT count(*) FROM agent_task_queue atq
		JOIN agent a ON a.id = atq.agent_id
		WHERE a.workspace_id = $1 AND a.system_key LIKE 'aurora:%'`, testWorkspaceID); n != 0 {
		t.Fatalf("tasks enqueued for a blocked prompt = %d, want 0", n)
	}
	bal, err := testHandler.Credit.Balance(context.Background(), parseUUID(testUserID))
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if bal != 0 {
		t.Fatalf("balance after a blocked prompt = %d, want 0", bal)
	}

	rows := auroraModerationLogRows(t)
	if len(rows) != 1 {
		t.Fatalf("moderation log rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.WorkspaceID != testWorkspaceID {
		t.Errorf("log row workspace = %s, want %s", row.WorkspaceID, testWorkspaceID)
	}
	if row.Scope != aurora.ModerationScopePrompt || row.Verdict != aurora.ModerationVerdictBlocked {
		t.Errorf("log row = %+v, want scope=%s verdict=%s", row, aurora.ModerationScopePrompt, aurora.ModerationVerdictBlocked)
	}
	// The rejection is recorded against the workspace alone: there is no
	// generation to name, and inventing one would be a lie the audit trail
	// carries forever.
	if row.GenerationID.Valid {
		t.Errorf("log row generation_id = %s, want NULL for a prompt rejected before the insert", row.GenerationID.String())
	}
	if strings.TrimSpace(row.Reason) == "" {
		t.Error("log row has no reason")
	}
}

func TestAuroraModerationAllowsCleanPrompt(t *testing.T) {
	creditTestReset(t)
	resetAuroraModerationLog(t)
	cleanupAuroraSystemAgents(t)
	cleanupAuroraWork(t)

	ctx := context.Background()
	if err := testHandler.Credit.Grant(ctx, parseUUID(testUserID), parseUUID(testWorkspaceID),
		1_000_000_000, aurora.LedgerKindAdjustment, creditRef("moderation-seed")); err != nil {
		t.Fatalf("Grant: %v", err)
	}

	req := newRequest(http.MethodPost, "/api/aurora/generations", map[string]string{
		"skillId": "ecommerce-bidding-strategy",
		"prompt":  "生成一张新加坡亲子游封面",
	})
	testutil.Call(t, testHandler.CreateAuroraGeneration, req).Want(http.StatusCreated)

	// The default adapter records rejections only: an audit row per admitted
	// request would be a write on the hot path with nothing to review.
	if rows := auroraModerationLogRows(t); len(rows) != 0 {
		t.Fatalf("moderation log rows after an admitted prompt = %d, want 0", len(rows))
	}
}

// TestAuroraModerationFailsClosedWhenPromptScreenErrors is the prompt half of
// the spec §10 red line. A moderator that cannot reach a verdict must not be
// read as approval — the request fails and the failure is recorded.
func TestAuroraModerationFailsClosedWhenPromptScreenErrors(t *testing.T) {
	creditTestReset(t)
	resetAuroraGenerations(t)
	resetAuroraModerationLog(t)

	moderator := &stubModerator{promptErr: errors.New("screening service unreachable")}
	withAuroraModeration(t, moderator)

	req := newRequest(http.MethodPost, "/api/aurora/generations", map[string]string{
		"skillId": "ecommerce-bidding-strategy",
		"prompt":  "生成一张新加坡亲子游封面",
	})
	testutil.Call(t, testHandler.CreateAuroraGeneration, req).Want(http.StatusInternalServerError)

	if moderator.promptCalls != 1 {
		t.Fatalf("ScreenPrompt calls = %d, want 1", moderator.promptCalls)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM aurora_generation WHERE workspace_id = $1`, testWorkspaceID); n != 0 {
		t.Fatalf("generations after a failed screen = %d, want 0", n)
	}
	rows := auroraModerationLogRows(t)
	if len(rows) != 1 {
		t.Fatalf("moderation log rows = %d, want 1 recording the failed screen", len(rows))
	}
	if rows[0].Verdict != aurora.ModerationVerdictBlocked || rows[0].Scope != aurora.ModerationScopePrompt {
		t.Errorf("log row = %+v, want a blocked prompt row", rows[0])
	}
	if strings.TrimSpace(rows[0].Reason) == "" {
		t.Error("log row has no reason for the failed screen")
	}
}

// TestAuroraModerationBlocksUnsafeArtifact is the completion half: an artifact
// the moderator rejects is never stored, the generation fails with the
// documented reason, and the reservation is refunded.
// TestAuroraModerationBlocksUnsafeArtifact is the completion half: an artifact
// the moderator rejects is never stored, its staged object is deleted, the
// generation fails with the documented reason, and the reservation is refunded.
func TestAuroraModerationBlocksUnsafeArtifact(t *testing.T) {
	creditTestReset(t)
	resetAuroraGenerations(t)
	resetAuroraModerationLog(t)

	store := &artifactTestStorage{}
	withArtifactStorage(t, store)
	moderator := &stubModerator{assetDecision: aurora.Decision{Allowed: false, Reason: "image is explicit"}}
	withAuroraModeration(t, moderator)

	const reservedMicro = 62_000_000
	generationID, taskID := runningAuroraGeneration(t, reservedMicro)
	staged := seedAuroraStaging(t, store, taskID, generationID, "primary-1", "out.md", "primary", []byte(artifactTestText), "{}")

	got := reportAuroraArtifacts(t, taskID, staged.payload("primary"))
	if got.Code != http.StatusUnprocessableEntity {
		t.Fatalf("report status = %d, want 422: %s", got.Code, got.Body.String())
	}

	// The kind the catalog derived is what the moderator was asked about: a
	// screen that inspects the wrong surface would pass every other assertion.
	if moderator.assetCalls != 1 || moderator.lastKind != "text" {
		t.Fatalf("ScreenAsset calls/kind = %d/%q, want 1/%q", moderator.assetCalls, moderator.lastKind, "text")
	}

	assertAuroraGenerationFailedForModeration(t, generationID, reservedMicro)

	// No asset row: the screen runs before the insert, which is the whole point
	// of post-screening rather than deleting afterwards. The rejected object is
	// deleted so the batch leaves nothing behind.
	if n := dbfx.Count(t, `SELECT count(*) FROM aurora_asset WHERE generation_id = $1`, generationID); n != 0 {
		t.Fatalf("asset rows for a rejected artifact = %d, want 0", n)
	}
	if len(store.deleted) == 0 {
		t.Error("rejected staging object was not deleted")
	}

	rows := auroraModerationLogRows(t)
	if len(rows) != 1 {
		t.Fatalf("moderation log rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.Scope != aurora.ModerationScopeAsset || row.Verdict != aurora.ModerationVerdictBlocked {
		t.Errorf("log row = %+v, want scope=%s verdict=%s", row, aurora.ModerationScopeAsset, aurora.ModerationVerdictBlocked)
	}
	if !row.GenerationID.Valid || row.GenerationID.String() != generationID {
		t.Errorf("log row generation_id = %v, want %s", row.GenerationID, generationID)
	}
	if strings.TrimSpace(row.Reason) == "" {
		t.Error("log row has no reason")
	}
}

// TestAuroraModerationFailsClosedWhenAssetScreenErrors is the artifact half of
// the red line: bytes the moderator could not inspect are not stored, and the
// task fails rather than completing with unreviewed output.
func TestAuroraModerationFailsClosedWhenAssetScreenErrors(t *testing.T) {
	creditTestReset(t)
	resetAuroraGenerations(t)
	resetAuroraModerationLog(t)

	store := &artifactTestStorage{}
	withArtifactStorage(t, store)
	moderator := &stubModerator{assetErr: errors.New("screening service unreachable")}
	withAuroraModeration(t, moderator)

	const reservedMicro = 62_000_000
	generationID, taskID := runningAuroraGeneration(t, reservedMicro)
	staged := seedAuroraStaging(t, store, taskID, generationID, "primary-1", "out.md", "primary", []byte(artifactTestText), "{}")

	got := reportAuroraArtifacts(t, taskID, staged.payload("primary"))
	if got.Code != http.StatusInternalServerError {
		t.Fatalf("report status = %d, want 500: %s", got.Code, got.Body.String())
	}

	assertAuroraGenerationFailedForModeration(t, generationID, reservedMicro)

	if n := dbfx.Count(t, `SELECT count(*) FROM aurora_asset WHERE generation_id = $1`, generationID); n != 0 {
		t.Fatalf("asset rows for an unscreenable artifact = %d, want 0", n)
	}
	rows := auroraModerationLogRows(t)
	if len(rows) != 1 || rows[0].Verdict != aurora.ModerationVerdictBlocked || rows[0].Scope != aurora.ModerationScopeAsset {
		t.Fatalf("moderation log rows = %+v, want one blocked asset row", rows)
	}
	if strings.TrimSpace(rows[0].Reason) == "" {
		t.Error("log row has no reason for the failed screen")
	}
}

// TestAuroraModerationRejectsBeforeStoringAnyArtifact covers a batch whose
// first artifact is clean and whose second is not. The clean one must not be
// written either: a partially stored batch would leave the generation failed
// while its library shows output from it.
func TestAuroraModerationRejectsBeforeStoringAnyArtifact(t *testing.T) {
	creditTestReset(t)
	resetAuroraGenerations(t)
	resetAuroraModerationLog(t)

	store := &artifactTestStorage{}
	withArtifactStorage(t, store)
	moderator := &stubModerator{blockAssetURL: "second.md"}
	withAuroraModeration(t, moderator)

	const reservedMicro = 62_000_000
	generationID, taskID := runningAuroraGeneration(t, reservedMicro)
	first := seedAuroraStaging(t, store, taskID, generationID, "primary-1", "first.md", "primary", []byte(artifactTestText), "{}")
	second := seedAuroraStaging(t, store, taskID, generationID, "secondary-1", "second.md", "supporting", []byte(artifactTestText+"second"), "{}")

	got := reportAuroraArtifacts(t, taskID, first.payload("primary"), second.payload("supporting"))
	if got.Code != http.StatusUnprocessableEntity {
		t.Fatalf("report status = %d, want 422: %s", got.Code, got.Body.String())
	}

	if moderator.assetCalls != 2 {
		t.Fatalf("ScreenAsset calls = %d, want 2 (the whole batch is screened before any row is written)", moderator.assetCalls)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM aurora_asset WHERE generation_id = $1`, generationID); n != 0 {
		t.Fatalf("asset rows after a rejected batch = %d, want 0 (including the artifact that passed)", n)
	}
	if len(store.deleted) < 2 {
		t.Errorf("deleted objects = %d, want both uncommitted objects removed", len(store.deleted))
	}
}

// runningAuroraGeneration builds the state a generation is in while its task
// runs: a task on a workspace agent, a generation row reverse-linked to it with
// credits reserved, and the matching reservation in the ledger so the refund is
// observable rather than assumed.
func runningAuroraGeneration(t *testing.T, reservedMicro int64) (generationID, taskID string) {
	t.Helper()
	ctx := context.Background()

	issueID := dbfx.Issue(t, "aurora moderation issue")
	agentID := dbfx.Agent(t, "AuroraModerationAgent", handlerTestRuntimeID(t))
	taskID = dbfx.Task(t, agentID, testutil.Cols{
		"runtime_id": handlerTestRuntimeID(t),
		"status":     "running",
		"issue_id":   issueID,
	})
	generationID = dbfx.Insert(t, "aurora_generation", testutil.Cols{
		"workspace_id":     testWorkspaceID,
		"user_id":          testUserID,
		"skill_id":         "ecommerce-bidding-strategy",
		"prompt":           "artifact moderation",
		"status":           "running",
		"task_id":          taskID,
		"credits_reserved": reservedMicro,
	})

	user, ws := parseUUID(testUserID), parseUUID(testWorkspaceID)
	if err := testHandler.Credit.Grant(ctx, user, ws, reservedMicro*2, aurora.LedgerKindAdjustment, creditRef("moderation-seed")); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	// The reference has to be the generation id: that is the key the refund
	// derives, and a mismatch would let the refund credit a wallet the
	// reservation never debited.
	if err := testHandler.Credit.Reserve(ctx, user, ws, reservedMicro, uuidToString(parseUUID(generationID))); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	return generationID, taskID
}

// assertAuroraGenerationFailedForModeration pins the three post-conditions of a
// rejected artifact: the documented terminal reason, the reservation back in
// the wallet, and no charge.
func assertAuroraGenerationFailedForModeration(t *testing.T, generationID string, reservedMicro int64) {
	t.Helper()

	var status string
	var genErr pgtype.Text
	var charged int64
	dbfx.QueryRow(t, `SELECT status, error, credits_charged FROM aurora_generation WHERE id = $1`,
		generationID).Scan(&status, &genErr, &charged)
	if status != "failed" {
		t.Errorf("generation status = %q, want failed", status)
	}
	if !genErr.Valid || genErr.String != "moderation blocked" {
		t.Errorf("generation error = %q, want %q", genErr.String, "moderation blocked")
	}
	if charged != 0 {
		t.Errorf("credits_charged = %d, want 0", charged)
	}

	bal, err := testHandler.Credit.Balance(context.Background(), parseUUID(testUserID))
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if want := reservedMicro * 2; bal != want {
		t.Errorf("balance after the refund = %d, want %d (grant less the refunded reservation)", bal, want)
	}
}
