package handler

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/aurora"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// attachmentSelectByIssue reads the issue-scoped projection rows a report
// writes. comment_id is cast to text so a comment-less row scans as NULL.
const attachmentSelectByIssue = "SELECT filename, content_type, size_bytes, comment_id::text, task_id::text, issue_id::text, uploader_type, url, uploader_id::text FROM attachment WHERE issue_id = $1"

// auroraIssueReportFixture is the issue-round shape: the generation is bound to
// the issue through issue_id. It reuses auroraReportFixture for everything the
// two shapes share.
func auroraIssueReportFixture(t *testing.T, skillID string) (taskID, generationID, issueID string) {
	t.Helper()
	issueID = dbfx.Issue(t, "aurora issue artifact projection")
	taskID, generationID = auroraReportFixture(t, skillID, testutil.Cols{"issue_id": issueID})
	return taskID, generationID, issueID
}

// roundTaskAgent reads the agent that owns a seeded task.
func roundTaskAgent(t *testing.T, taskID string) string {
	t.Helper()
	var agentID string
	dbfx.QueryRow(t, "SELECT agent_id::text FROM agent_task_queue WHERE id = $1", taskID).Scan(&agentID)
	return agentID
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

	var filename, contentType, uploaderType, taskCol, issueCol, url, uploaderID string
	var size int64
	var commentID *string
	dbfx.QueryRow(t, attachmentSelectByIssue, issueID).
		Scan(&filename, &contentType, &size, &commentID, &taskCol, &issueCol, &uploaderType, &url, &uploaderID)

	if filename != "xhs-image.png" || contentType != "image/png" || size != 9 {
		t.Fatalf("attachment = %s/%s/%d, want xhs-image.png/image/png/9", filename, contentType, size)
	}
	if commentID != nil {
		t.Fatalf("comment_id = %v, want NULL before the round ends", *commentID)
	}
	if taskCol != taskID || issueCol != issueID || uploaderType != "agent" {
		t.Fatalf("binding = task %s issue %s uploader %s", taskCol, issueCol, uploaderType)
	}
	if uploaderID != roundTaskAgent(t, taskID) {
		t.Fatalf("uploader_id = %s, want the task's own agent", uploaderID)
	}
	// The row points at the object the content library recorded, so one stored
	// file backs both views.
	var assetURL string
	dbfx.QueryRow(t, "SELECT media_url::text FROM aurora_asset WHERE generation_id = $1", generationID).Scan(&assetURL)
	if url == "" || url != assetURL {
		t.Fatalf("url = %q, want the library's media_url %q", url, assetURL)
	}
}

// TestReportTaskArtifactsProjectionIsIdempotentAndIssueScoped pins both halves
// of the projection guard: a replayed report reuses the existing row even after
// the round bound it, and a generation with no issue_id projects nothing even
// though its task carries one.
func TestReportTaskArtifactsProjectionIsIdempotentAndIssueScoped(t *testing.T) {
	store := &artifactTestStorage{}
	withArtifactStorage(t, store)
	withAuroraModeration(t, &stubModerator{assetDecision: aurora.Decision{Allowed: true}})

	taskID, generationID, issueID := auroraIssueReportFixture(t, "xhs-image")
	fixture := seedAuroraStaging(t, store, taskID, generationID, "art-1", "xhs-image.png",
		"primary", []byte("png-bytes"), "{}")
	reportAuroraArtifacts(t, taskID, fixture.payload("primary")).Want(http.StatusOK)

	// A bound row is the production replay shape: the round has already linked
	// its files, and a retried report must still find the row it wrote rather
	// than inserting a second one.
	commentID := dbfx.Insert(t, "comment", testutil.Cols{
		"issue_id":     issueID,
		"workspace_id": testWorkspaceID,
		"author_type":  "agent",
		"author_id":    roundTaskAgent(t, taskID),
		"content":      "Produced xhs-image.png",
		"type":         "comment",
	})
	dbfx.Exec(t, "UPDATE attachment SET comment_id = $1 WHERE issue_id = $2 AND task_id = $3",
		commentID, issueID, taskID)

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

// TestPublishCommentWithAttachmentsCarriesTheFiles pins the completion-path
// rebroadcast: a comment that gained files after it was first broadcast must be
// re-sent with those files, because clients replace a cached comment with the
// snapshot they receive.
func TestPublishCommentWithAttachmentsCarriesTheFiles(t *testing.T) {
	updates := make(chan CommentResponse, 4)
	testHandler.Bus.Subscribe(protocol.EventCommentUpdated, func(e events.Event) {
		payload, ok := e.Payload.(map[string]any)
		if !ok {
			return
		}
		if comment, ok := payload["comment"].(CommentResponse); ok {
			updates <- comment
		}
	})

	taskID, generationID, issueID := auroraIssueReportFixture(t, "xhs-image")
	assetURL := "https://cdn.example.test/aurora/xhs-image.png"
	insertAsset(t, generationID, testutil.Cols{"media_url": assetURL})
	commentID := dbfx.Insert(t, "comment", testutil.Cols{
		"issue_id":     issueID,
		"workspace_id": testWorkspaceID,
		"author_type":  "agent",
		"author_id":    roundTaskAgent(t, taskID),
		"content":      "Produced it",
		"type":         "comment",
	})
	dbfx.Insert(t, "attachment", testutil.Cols{
		"workspace_id": testWorkspaceID, "issue_id": issueID, "comment_id": commentID,
		"uploader_type": "agent", "uploader_id": roundTaskAgent(t, taskID),
		"filename": "xhs-image.png", "url": assetURL,
		"content_type": "image/png", "size_bytes": int64(9),
	})

	testHandler.publishCommentWithAttachments(context.Background(),
		parseUUID(testWorkspaceID), parseUUID(commentID), "system", "")

	select {
	case comment := <-updates:
		if len(comment.Attachments) != 1 || comment.Attachments[0].Filename != "xhs-image.png" {
			t.Fatalf("attachments = %+v", comment.Attachments)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no comment:updated broadcast carrying the round's files")
	}
}
