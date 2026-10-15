package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/aurora"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// auroraCommentFixture creates an Aurora generation through the real create
// endpoint and returns its id, the issue the round runs on, the round-one task
// id, and the reserved micro-credits. It resets the wallet and registers the
// cleanups that return the shared workspace to its starting state.
func auroraCommentFixture(t *testing.T, prompt string) (generationID, issueID, taskID string, reserved int64) {
	t.Helper()
	creditTestReset(t)
	cleanupAuroraSystemAgents(t)
	cleanupAuroraWork(t)

	if err := testHandler.Credit.Grant(context.Background(), parseUUID(testUserID), parseUUID(testWorkspaceID),
		1_000_000_000, aurora.LedgerKindAdjustment, creditRef("aurora-comment-seed")); err != nil {
		t.Fatalf("Grant: %v", err)
	}

	out := testutil.Decode[struct {
		Generation struct {
			ID string `json:"id"`
		} `json:"generation"`
	}](t, testHandler.CreateAuroraGeneration, newRequest(http.MethodPost, "/api/aurora/generations", map[string]string{
		"skillId": "ecommerce-bidding-strategy",
		"prompt":  prompt,
	}), http.StatusCreated)
	generationID = out.Generation.ID
	if generationID == "" {
		t.Fatal("create returned an empty generation id")
	}

	var issueUUID, taskUUID pgtype.UUID
	dbfx.QueryRow(t,
		`SELECT issue_id, task_id, credits_reserved FROM aurora_generation WHERE id = $1`,
		generationID).Scan(&issueUUID, &taskUUID, &reserved)
	if !issueUUID.Valid || !taskUUID.Valid {
		t.Fatalf("generation %s missing issue_id/task_id", generationID)
	}
	return generationID, uuidToString(issueUUID), uuidToString(taskUUID), reserved
}

// TestAuroraIssueCommentDuringRoundIsNotMerged pins the gate's first
// disposition: while the current round is live, a comment stays on the issue
// but is never folded into that round — not as the coalesced trigger, not as
// the trigger summary — and never spawns a second task.
func TestAuroraIssueCommentDuringRoundIsNotMerged(t *testing.T) {
	_, issueID, taskID, _ := auroraCommentFixture(t, "进行中评论门闩")

	var agentID pgtype.UUID
	dbfx.QueryRow(t, `SELECT assignee_id FROM issue WHERE id = $1`, issueID).Scan(&agentID)
	if !agentID.Valid {
		t.Fatal("aurora issue has no assignee")
	}

	// Give round one a real thread: an agent-authored question the human
	// answers. Without the gate the reply is the newest trigger in that thread,
	// so the queued round folds it in.
	var questionID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO comment (workspace_id, issue_id, author_type, author_id, content)
		VALUES ($1, $2, 'agent', $3, '请问想要什么风格？')
		RETURNING id`, testWorkspaceID, issueID, agentID).Scan(&questionID); err != nil {
		t.Fatalf("insert agent question: %v", err)
	}
	dbfx.Exec(t, `UPDATE agent_task_queue SET trigger_comment_id = $1 WHERE id = $2`, questionID, taskID)

	content := "走暖色调吧"
	var created CommentResponse
	testutil.Call(t, testHandler.CreateComment, withURLParam(
		newRequest(http.MethodPost, "/api/issues/"+issueID+"/comments", map[string]any{
			"content":   content,
			"parent_id": questionID,
		}), "id", issueID)).Want(http.StatusCreated).JSON(&created)
	if created.ID == "" {
		t.Fatal("comment was not saved")
	}

	// The comment persists...
	if n := dbfx.Count(t, `SELECT count(*) FROM comment WHERE id = $1`, created.ID); n != 1 {
		t.Fatalf("comment rows = %d, want 1", n)
	}
	// ...no second task appears on the issue...
	if n := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1`, issueID); n != 1 {
		t.Fatalf("issue has %d tasks, want 1 (an in-progress round must not be multiplied)", n)
	}
	// ...and the running round is untouched.
	var triggerID pgtype.UUID
	var coalesced []pgtype.UUID
	var summary pgtype.Text
	dbfx.QueryRow(t, `SELECT trigger_comment_id, coalesced_comment_ids, trigger_summary FROM agent_task_queue WHERE id = $1`, taskID).
		Scan(&triggerID, &coalesced, &summary)
	if uuidToString(triggerID) != questionID {
		t.Fatalf("round trigger = %s, want %s (the comment was folded into the live round)", uuidToString(triggerID), questionID)
	}
	if len(coalesced) != 0 {
		t.Fatalf("coalesced_comment_ids = %v, want empty", coalesced)
	}
	if summary.Valid && strings.Contains(summary.String, content) {
		t.Fatalf("trigger_summary = %q contains the gated comment", summary.String)
	}
}

// TestAuroraIssueCommentOnNeedsInputOpensRound pins the gate's second
// disposition: a moderated reply on a needs_input generation opens the next
// round on the same generation, with no second reservation.
func TestAuroraIssueCommentOnNeedsInputOpensRound(t *testing.T) {
	generationID, issueID, taskID, reserved := auroraCommentFixture(t, "等你回复后续跑")
	// The round declared needs_input and its task finished; the reservation is
	// still held because needs_input never settles.
	dbfx.Exec(t, `UPDATE aurora_generation SET status = 'needs_input' WHERE id = $1`, generationID)
	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'completed' WHERE id = $1`, taskID)

	content := "参考图在海边，日落时分"
	var created CommentResponse
	testutil.Call(t, testHandler.CreateComment, withURLParam(
		newRequest(http.MethodPost, "/api/issues/"+issueID+"/comments", map[string]any{"content": content}), "id", issueID),
	).Want(http.StatusCreated).JSON(&created)
	if created.ID == "" {
		t.Fatal("comment was not saved")
	}

	// A second round is enqueued on the same issue...
	if n := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1`, issueID); n != 2 {
		t.Fatalf("issue has %d tasks, want 2 (one per round)", n)
	}
	var newTaskID pgtype.UUID
	var status string
	var stillReserved int64
	dbfx.QueryRow(t, `SELECT task_id, status, credits_reserved FROM aurora_generation WHERE id = $1`, generationID).
		Scan(&newTaskID, &status, &stillReserved)
	if !newTaskID.Valid || uuidToString(newTaskID) == taskID {
		t.Fatalf("generation task_id = %s, want the new round (old %s)", uuidToString(newTaskID), taskID)
	}
	if status != "queued" {
		t.Fatalf("generation status = %q, want queued", status)
	}
	if stillReserved != reserved {
		t.Fatalf("credits_reserved = %d, want unchanged %d (no second reservation)", stillReserved, reserved)
	}

	// ...carrying the continuation context and the trigger comment id.
	var triggerID pgtype.UUID
	var raw []byte
	dbfx.QueryRow(t, `SELECT trigger_comment_id, context FROM agent_task_queue WHERE id = $1`, uuidToString(newTaskID)).
		Scan(&triggerID, &raw)
	if uuidToString(triggerID) != created.ID {
		t.Fatalf("new round trigger = %s, want %s", uuidToString(triggerID), created.ID)
	}
	var ctx struct {
		Type          string   `json:"type"`
		GenerationID  string   `json:"generation_id"`
		Prompt        string   `json:"prompt"`
		AttachmentIDs []string `json:"attachment_ids"`
	}
	if err := json.Unmarshal(raw, &ctx); err != nil {
		t.Fatalf("unmarshal new round context: %v", err)
	}
	if ctx.Type != service.AuroraIssueContextType || ctx.GenerationID != generationID {
		t.Fatalf("new round context = %+v, want aurora_issue for generation %s", ctx, generationID)
	}
	if ctx.Prompt == "" || ctx.AttachmentIDs == nil {
		t.Fatalf("new round context missing prompt/attachments: %+v", ctx)
	}
}

// TestAuroraIssueCommentOnCompletedDoesNotEnqueue pins the terminal
// disposition: a finished generation never starts another run from a comment.
func TestAuroraIssueCommentOnCompletedDoesNotEnqueue(t *testing.T) {
	generationID, issueID, taskID, _ := auroraCommentFixture(t, "完成后评论不再入队")
	dbfx.Exec(t, `UPDATE aurora_generation SET status = 'completed' WHERE id = $1`, generationID)
	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'completed' WHERE id = $1`, taskID)

	testutil.Call(t, testHandler.CreateComment, withURLParam(
		newRequest(http.MethodPost, "/api/issues/"+issueID+"/comments", map[string]any{"content": "还能再来一版吗"}), "id", issueID),
	).Want(http.StatusCreated)

	if n := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1`, issueID); n != 1 {
		t.Fatalf("issue has %d tasks, want 1 (a completed generation must not start a run)", n)
	}
	var status string
	dbfx.QueryRow(t, `SELECT status FROM aurora_generation WHERE id = $1`, generationID).Scan(&status)
	if status != "completed" {
		t.Fatalf("generation status = %q, want completed", status)
	}
}

// TestAuroraIssueCommentBlockedByModeration pins that a rejected follow-up
// still persists, but starts nothing and leaves the generation and reservation
// untouched.
func TestAuroraIssueCommentBlockedByModeration(t *testing.T) {
	generationID, issueID, taskID, reserved := auroraCommentFixture(t, "审核拒绝的续跑")
	dbfx.Exec(t, `UPDATE aurora_generation SET status = 'needs_input' WHERE id = $1`, generationID)
	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'completed' WHERE id = $1`, taskID)
	resetAuroraModerationLog(t)
	withAuroraModeration(t, &stubModerator{promptDecision: aurora.Decision{Allowed: false, Reason: "content policy"}})

	var created CommentResponse
	testutil.Call(t, testHandler.CreateComment, withURLParam(
		newRequest(http.MethodPost, "/api/issues/"+issueID+"/comments", map[string]any{"content": "这条回复不会过审"}), "id", issueID),
	).Want(http.StatusCreated).JSON(&created)

	if n := dbfx.Count(t, `SELECT count(*) FROM comment WHERE id = $1`, created.ID); n != 1 {
		t.Fatalf("comment rows = %d, want 1 (a rejected reply still persists)", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1`, issueID); n != 1 {
		t.Fatalf("issue has %d tasks, want 1 (a rejected reply must not start a round)", n)
	}
	var status string
	var stillReserved int64
	dbfx.QueryRow(t, `SELECT status, credits_reserved FROM aurora_generation WHERE id = $1`, generationID).
		Scan(&status, &stillReserved)
	if status != "needs_input" {
		t.Fatalf("generation status = %q, want needs_input", status)
	}
	if stillReserved != reserved {
		t.Fatalf("credits_reserved = %d, want unchanged %d", stillReserved, reserved)
	}
}

// TestNonAuroraIssueCommentUnaffected is the byte-for-byte regression: an
// ordinary issue's comment still triggers its assigned agent exactly as before.
func TestNonAuroraIssueCommentUnaffected(t *testing.T) {
	agentID := createHandlerTestAgent(t, "Ordinary Comment Agent", nil)
	issueID := createCommentTriggerPreviewIssue(t, "ordinary comment path", "agent", agentID)

	var created CommentResponse
	testutil.Call(t, testHandler.CreateComment, withURLParam(
		newRequest(http.MethodPost, "/api/issues/"+issueID+"/comments", map[string]any{"content": "普通 issue 的评论照常触发"}), "id", issueID),
	).Want(http.StatusCreated).JSON(&created)

	if got := countQueuedCommentTriggerTasks(t, issueID, agentID); got != 1 {
		t.Fatalf("queued tasks for the ordinary issue = %d, want 1", got)
	}
	// The implicit assignee fallback is not an explicit mention, so it carries
	// no per-target outcome — the same response shape as before the gate.
	if len(created.TriggerOutcomes) != 0 {
		t.Fatalf("trigger outcomes = %+v, want none for an implicit assignee trigger", created.TriggerOutcomes)
	}
}
