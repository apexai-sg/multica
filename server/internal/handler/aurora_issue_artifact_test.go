package handler

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/aurora"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// attachmentSelectByIssue reads the issue-scoped projection rows a report
// writes. comment_id is cast to text so a comment-less row scans as NULL.
const attachmentSelectByIssue = "SELECT filename, content_type, size_bytes, comment_id::text, task_id::text, issue_id::text, uploader_type FROM attachment WHERE issue_id = $1"

// auroraIssueReportFixture is the issue-round shape: the generation is bound to
// the issue through issue_id.
func auroraIssueReportFixture(t *testing.T, skillID string) (taskID, generationID, issueID string) {
	t.Helper()
	issueID = dbfx.Issue(t, "aurora issue artifact projection")
	agentID := dbfx.Agent(t, "AuroraIssueArtifactAgent "+uuid.NewString(), handlerTestRuntimeID(t))
	taskID = dbfx.Task(t, agentID, testutil.Cols{
		"runtime_id": handlerTestRuntimeID(t),
		"status":     "running",
		"issue_id":   issueID,
	})
	generationID = dbfx.Insert(t, "aurora_generation", testutil.Cols{
		"workspace_id":     testWorkspaceID,
		"user_id":          testUserID,
		"skill_id":         skillID,
		"prompt":           "issue artifact projection",
		"status":           "running",
		"task_id":          taskID,
		"issue_id":         issueID,
		"credits_reserved": int64(62000000),
	})
	return taskID, generationID, issueID
}

// TestReportTaskArtifactsProjectsIssueAttachment proves a committed artifact is
// projected once onto the issue as a task-tagged, comment-less attachment. The
// round's reply comment binds it later, so comment_id must start NULL.
func TestReportTaskArtifactsProjectsIssueAttachment(t *testing.T) {
	store := &artifactTestStorage{}
	withArtifactStorage(t, store)
	withAuroraModeration(t, &stubModerator{assetDecision: aurora.Decision{Allowed: true}})

	taskID, generationID, issueID := auroraIssueReportFixture(t, "xhs-image")
	fixture := seedAuroraStaging(t, store, taskID, generationID, "art-1", "xhs-image.png",
		"primary", []byte("png-bytes"), "{}")

	reportAuroraArtifacts(t, taskID, fixture.payload("primary")).Want(http.StatusOK)

	if n := dbfx.Count(t, "SELECT count(*) FROM attachment WHERE issue_id = $1", issueID); n != 1 {
		t.Fatalf("attachment rows = %d, want exactly 1 after one report", n)
	}

	var filename, contentType, uploaderType, taskCol, issueCol string
	var size int64
	var commentID *string
	dbfx.QueryRow(t, attachmentSelectByIssue, issueID).
		Scan(&filename, &contentType, &size, &commentID, &taskCol, &issueCol, &uploaderType)

	if filename != "xhs-image.png" || contentType != "image/png" || size != 9 {
		t.Fatalf("attachment = %s/%s/%d, want xhs-image.png/image/png/9", filename, contentType, size)
	}
	if commentID != nil {
		t.Fatalf("comment_id = %v, want NULL before the round ends", *commentID)
	}
	if taskCol != taskID || issueCol != issueID || uploaderType != "agent" {
		t.Fatalf("binding = task %s issue %s uploader %s", taskCol, issueCol, uploaderType)
	}
}

// TestReportTaskArtifactsProjectionIsIdempotentAndIssueScoped pins both halves
// of the projection guard: a replayed report reuses the existing row, and a
// generation with no issue_id projects nothing even though its task carries one.
func TestReportTaskArtifactsProjectionIsIdempotentAndIssueScoped(t *testing.T) {
	store := &artifactTestStorage{}
	withArtifactStorage(t, store)
	withAuroraModeration(t, &stubModerator{assetDecision: aurora.Decision{Allowed: true}})

	taskID, generationID, issueID := auroraIssueReportFixture(t, "xhs-image")
	fixture := seedAuroraStaging(t, store, taskID, generationID, "art-1", "xhs-image.png",
		"primary", []byte("png-bytes"), "{}")
	reportAuroraArtifacts(t, taskID, fixture.payload("primary")).Want(http.StatusOK)
	reportAuroraArtifacts(t, taskID, fixture.payload("primary")).Want(http.StatusOK)

	if n := dbfx.Count(t, "SELECT count(*) FROM attachment WHERE issue_id = $1", issueID); n != 1 {
		t.Fatalf("attachment rows = %d, want 1 after a replayed report", n)
	}

	plainTask, plainGeneration := auroraReportFixture(t, "xhs-image")
	plain := seedAuroraStaging(t, store, plainTask, plainGeneration, "art-2", "plain.png",
		"primary", []byte("png-bytes"), "{}")
	reportAuroraArtifacts(t, plainTask, plain.payload("primary")).Want(http.StatusOK)
	if n := dbfx.Count(t, "SELECT count(*) FROM attachment WHERE task_id = $1", plainTask); n != 0 {
		t.Fatalf("issue-less generation wrote %d attachments, want 0", n)
	}
}
