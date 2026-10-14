package service

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// seedAuroraRoundIssue creates the issue a round runs against.
const seedAuroraRoundIssue = `INSERT INTO issue (workspace_id, number, title, status, priority, creator_type, creator_id) VALUES ($1, (SELECT COALESCE(MAX(number), 0) + 1 FROM issue WHERE workspace_id = $1), 'aurora round reply', 'todo', 'none', 'member', $2) RETURNING id`

// seedAuroraRoundTask creates the running task that produced the projection.
const seedAuroraRoundTask = `INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority, issue_id, started_at) VALUES ($1, $2, 'running', 0, $3, now() - interval '1 minute') RETURNING id`

// seedIssueWithRound creates the issue, a running task on it, and one
// unbound projection attachment produced by that task.
func seedIssueWithRound(t *testing.T, svc *TaskService, pool *pgxpool.Pool, workspaceID, userID, agentID string) (issueID, taskID pgtype.UUID) {
	t.Helper()
	ctx := context.Background()
	if err := pool.QueryRow(ctx, seedAuroraRoundIssue, workspaceID, userID).Scan(&issueID); err != nil {
		t.Fatalf("seed aurora round issue: %v", err)
	}
	taskID = seedProjectionOnIssue(t, svc, pool, workspaceID, agentID, issueID)
	t.Cleanup(func() {
		ctx := context.Background()
		pool.Exec(ctx, `DELETE FROM attachment WHERE issue_id = $1`, issueID)
		pool.Exec(ctx, `DELETE FROM comment WHERE issue_id = $1`, issueID)
		pool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
		pool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, issueID)
	})
	return issueID, taskID
}

// seedProjectionOnIssue creates a running task on a caller-supplied issue plus
// the unbound projection attachment the artifact report would have written, and
// returns the new task id. The issue's own cleanup owns the rows.
func seedProjectionOnIssue(t *testing.T, svc *TaskService, pool *pgxpool.Pool, workspaceID, agentID string, issueID pgtype.UUID) (taskID pgtype.UUID) {
	t.Helper()
	ctx := context.Background()
	var runtimeID string
	if err := pool.QueryRow(ctx, `SELECT runtime_id::text FROM agent WHERE id = $1`, agentID).Scan(&runtimeID); err != nil {
		t.Fatalf("load agent runtime: %v", err)
	}
	if err := pool.QueryRow(ctx, seedAuroraRoundTask, agentID, runtimeID, issueID).Scan(&taskID); err != nil {
		t.Fatalf("seed round task: %v", err)
	}
	if _, err := svc.Queries.CreateAttachment(ctx, db.CreateAttachmentParams{
		ID:           dbid.NewV7(),
		WorkspaceID:  util.MustParseUUID(workspaceID),
		IssueID:      issueID,
		TaskID:       taskID,
		UploaderType: "agent",
		UploaderID:   util.MustParseUUID(agentID),
		Filename:     "xhs-image.png",
		Url:          "https://cdn.example.test/xhs-image.png",
		ContentType:  "image/png",
		SizeBytes:    9,
	}); err != nil {
		t.Fatalf("seed projection attachment: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		pool.Exec(ctx, `DELETE FROM attachment WHERE task_id = $1`, taskID)
		pool.Exec(ctx, `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
	})
	return taskID
}

// TestBindAuroraRoundArtifactsBindsToTheRoundsReply pins the happy path: the
// task-tagged attachment the round produced moves onto the reply the agent
// wrote, so it renders inside that comment and in the deliverables list.
func TestBindAuroraRoundArtifactsBindsToTheRoundsReply(t *testing.T) {
	svc, pool, workspaceID, userID, agentID := newAuroraCompletionService(t)
	ctx := context.Background()
	issueID, taskID := seedIssueWithRound(t, svc, pool, workspaceID, userID, agentID)

	reply, err := svc.Queries.CreateComment(ctx, db.CreateCommentParams{
		ID:           dbid.NewV7(),
		IssueID:      issueID,
		WorkspaceID:  util.MustParseUUID(workspaceID),
		AuthorType:   "agent",
		AuthorID:     util.MustParseUUID(agentID),
		Content:      "here is the image",
		Type:         "comment",
		SourceTaskID: taskID,
	})
	if err != nil {
		t.Fatalf("seed agent reply: %v", err)
	}

	task, err := svc.Queries.GetAgentTask(ctx, taskID)
	if err != nil {
		t.Fatalf("load task: %v", err)
	}
	svc.bindAuroraRoundArtifacts(ctx, task)

	var bound int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM attachment WHERE task_id = $1 AND comment_id = $2`, taskID, reply.ID).Scan(&bound); err != nil {
		t.Fatalf("count bound attachments: %v", err)
	}
	if bound != 1 {
		t.Fatalf("bound attachments = %d, want exactly 1 on the reply", bound)
	}
}

// TestBindAuroraRoundArtifactsNamesFilesWhenNoReplyExists pins the fallback: a
// run that produced files but wrote no reply still names them in a comment, so
// the output stays reachable and the attachment gets an owner.
func TestBindAuroraRoundArtifactsNamesFilesWhenNoReplyExists(t *testing.T) {
	svc, pool, workspaceID, userID, agentID := newAuroraCompletionService(t)
	ctx := context.Background()
	_, taskID := seedIssueWithRound(t, svc, pool, workspaceID, userID, agentID)

	task, err := svc.Queries.GetAgentTask(ctx, taskID)
	if err != nil {
		t.Fatalf("load task: %v", err)
	}
	svc.bindAuroraRoundArtifacts(ctx, task)

	var content string
	if err := pool.QueryRow(ctx, `
		SELECT c.content
		FROM comment c
		JOIN attachment a ON a.comment_id = c.id
		WHERE a.task_id = $1`, taskID).Scan(&content); err != nil {
		t.Fatalf("load synthesized round reply: %v", err)
	}
	if content != "xhs-image.png" {
		t.Fatalf("synthesized comment content = %q, want %q", content, "xhs-image.png")
	}
}

// TestBindAuroraRoundArtifactsLeavesOtherRoundsAlone pins the task scoping: a
// second run's projection on the same issue is not stolen by the first round's
// bind.
func TestBindAuroraRoundArtifactsLeavesOtherRoundsAlone(t *testing.T) {
	svc, pool, workspaceID, userID, agentID := newAuroraCompletionService(t)
	ctx := context.Background()
	issueID, firstTaskID := seedIssueWithRound(t, svc, pool, workspaceID, userID, agentID)
	otherTaskID := seedProjectionOnIssue(t, svc, pool, workspaceID, agentID, issueID)

	first, err := svc.Queries.GetAgentTask(ctx, firstTaskID)
	if err != nil {
		t.Fatalf("load first task: %v", err)
	}
	svc.bindAuroraRoundArtifacts(ctx, first)

	var otherUnbound int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM attachment WHERE task_id = $1 AND comment_id IS NULL`, otherTaskID).Scan(&otherUnbound); err != nil {
		t.Fatalf("count other round attachments: %v", err)
	}
	if otherUnbound != 1 {
		t.Fatalf("other round unbound attachments = %d, want 1", otherUnbound)
	}

	var firstBound int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM attachment WHERE task_id = $1 AND comment_id IS NOT NULL`, firstTaskID).Scan(&firstBound); err != nil {
		t.Fatalf("count first round attachments: %v", err)
	}
	if firstBound != 1 {
		t.Fatalf("first round bound attachments = %d, want 1", firstBound)
	}
}

// TestAuroraRoundInputsExcludeProducedFiles pins the shared query's boundary: a
// run-produced projection (task_id set) is not one of the issue's own input
// attachments, so a continuing round must not read its own deliverable back in.
func TestAuroraRoundInputsExcludeProducedFiles(t *testing.T) {
	svc, pool, workspaceID, userID, agentID := newAuroraCompletionService(t)
	issueID, _ := seedIssueWithRound(t, svc, pool, workspaceID, userID, agentID)

	// A file a person gave the issue: no task_id.
	human, err := svc.Queries.CreateAttachment(context.Background(), db.CreateAttachmentParams{
		ID: dbid.NewV7(), WorkspaceID: util.MustParseUUID(workspaceID), IssueID: issueID,
		UploaderType: "member", UploaderID: util.MustParseUUID(userID),
		Filename: "brief.png", Url: "https://cdn.example.test/brief.png",
		ContentType: "image/png", SizeBytes: 5,
	})
	if err != nil {
		t.Fatalf("seed human attachment: %v", err)
	}
	issue, err := svc.Queries.GetIssue(context.Background(), issueID)
	if err != nil {
		t.Fatalf("load issue: %v", err)
	}

	ids, err := svc.auroraRoundAttachmentIDs(context.Background(), issue)
	if err != nil {
		t.Fatalf("auroraRoundAttachmentIDs: %v", err)
	}
	if len(ids) != 1 || ids[0] != util.UUIDToString(human.ID) {
		t.Fatalf("round inputs = %v, want only the human-supplied %s", ids, util.UUIDToString(human.ID))
	}
}
