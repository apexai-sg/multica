package handler

import (
	"context"
	"net/http"
	"sync"
	"testing"

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
	// A subscriber on this bus stays registered for the life of the test
	// binary and Publish is synchronous, so a bounded channel turns every later
	// publish in this package into a permanent block once it fills. Record into
	// a slice instead, and assert after the call: Publish has already delivered
	// by the time it returns.
	var mu sync.Mutex
	var updates []CommentResponse
	testHandler.Bus.Subscribe(protocol.EventCommentUpdated, func(e events.Event) {
		payload, ok := e.Payload.(map[string]any)
		if !ok {
			return
		}
		if comment, ok := payload["comment"].(CommentResponse); ok {
			mu.Lock()
			updates = append(updates, comment)
			mu.Unlock()
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

	mu.Lock()
	defer mu.Unlock()
	if len(updates) == 0 {
		t.Fatal("no comment:updated broadcast carrying the round's files")
	}
	comment := updates[len(updates)-1]
	if len(comment.Attachments) != 1 || comment.Attachments[0].Filename != "xhs-image.png" {
		t.Fatalf("attachments = %+v", comment.Attachments)
	}
}

// TestIssueAttachmentListExcludesRunProjections pins the read-side effect of the
// ListAttachmentsByIssue boundary: a run-produced projection must not appear in
// the issue's own attachment list (the mobile timeline builds its description
// image sequence from it), while a file a person gave the issue still does.
func TestIssueAttachmentListExcludesRunProjections(t *testing.T) {
	taskID, _, issueID := auroraIssueReportFixture(t, "xhs-image")

	// The projection the round produced: task-tagged and still comment-less.
	dbfx.Insert(t, "attachment", testutil.Cols{
		"workspace_id":  testWorkspaceID,
		"issue_id":      issueID,
		"task_id":       taskID,
		"uploader_type": "agent",
		"uploader_id":   roundTaskAgent(t, taskID),
		"filename":      "xhs-image.png",
		"url":           "https://cdn.example.test/xhs-image.png",
		"content_type":  "image/png",
		"size_bytes":    int64(9),
	})

	list := func() []AttachmentResponse {
		req := newRequest(http.MethodGet, "/api/issues/"+issueID+"/attachments", nil)
		req = withURLParam(req, "id", issueID)
		return testutil.Decode[[]AttachmentResponse](t, testHandler.ListAttachments, req, http.StatusOK)
	}

	if got := list(); len(got) != 0 {
		t.Fatalf("issue attachments = %+v, want none while the only row is a run projection", got)
	}

	// A file a person gave the issue has no task_id and must still be listed.
	dbfx.Insert(t, "attachment", testutil.Cols{
		"workspace_id":  testWorkspaceID,
		"issue_id":      issueID,
		"uploader_type": "member",
		"uploader_id":   testUserID,
		"filename":      "brief.png",
		"url":           "https://cdn.example.test/brief.png",
		"content_type":  "image/png",
		"size_bytes":    int64(5),
	})
	got := list()
	if len(got) != 1 || got[0].Filename != "brief.png" {
		t.Fatalf("issue attachments = %+v, want only the human-supplied brief.png", got)
	}
}

// reclaimTestStorage records every object deletion the reclamation helper
// requests, whichever storage call carries it.
type reclaimTestStorage struct {
	mockStorage
	deletedKeys []string
}

func (s *reclaimTestStorage) DeleteObject(_ context.Context, key string) error {
	s.deletedKeys = append(s.deletedKeys, key)
	return nil
}

func (s *reclaimTestStorage) DeleteKeys(_ context.Context, keys []string) {
	s.deletedKeys = append(s.deletedKeys, keys...)
}

func withReclaimStorage(t *testing.T, s *reclaimTestStorage) {
	t.Helper()
	previous := testHandler.Storage
	testHandler.Storage = s
	t.Cleanup(func() { testHandler.Storage = previous })
}

// TestReclaimAttachmentObjectsKeepsLibraryOwnedBytes proves the ownership rule:
// a comment or issue delete releases attachment rows, but an object the content
// library still points at must survive, because the library owns those bytes.
func TestReclaimAttachmentObjectsKeepsLibraryOwnedBytes(t *testing.T) {
	store := &reclaimTestStorage{}
	withReclaimStorage(t, store)
	_, generationID, _ := auroraIssueReportFixture(t, "xhs-image")
	assetURL := "https://cdn.example.test/aurora/xhs-image.png"
	insertAsset(t, generationID, testutil.Cols{"media_url": assetURL})

	testHandler.reclaimAttachmentObjects(context.Background(), []string{assetURL})
	if len(store.deletedKeys) != 0 {
		t.Fatalf("deleted keys = %v, want none: the library still owns this object", store.deletedKeys)
	}

	orphan := "https://cdn.example.test/upload/plain.png"
	testHandler.reclaimAttachmentObjects(context.Background(), []string{orphan})
	if len(store.deletedKeys) != 1 {
		t.Fatalf("deleted keys = %v, want exactly the orphan", store.deletedKeys)
	}
}

// TestDeleteAuroraAssetUnlinksTheReplyAndRebroadcastsIt proves the other half of
// the ownership rule: deleting a library asset reclaims the object, drops the
// attachment rows that showed it, bumps each affected comment's revision and
// rebroadcasts it so no client keeps a broken thumbnail.
func TestDeleteAuroraAssetUnlinksTheReplyAndRebroadcastsIt(t *testing.T) {
	store := &reclaimTestStorage{}
	withReclaimStorage(t, store)
	taskID, generationID, issueID := auroraIssueReportFixture(t, "xhs-image")
	assetURL := "https://cdn.example.test/aurora/xhs-image.png"
	assetID := insertAsset(t, generationID, testutil.Cols{"media_url": assetURL})
	commentID := dbfx.Insert(t, "comment", testutil.Cols{
		"issue_id":     issueID,
		"workspace_id": testWorkspaceID,
		"author_type":  "agent",
		"author_id":    roundTaskAgent(t, taskID),
		"content":      "Produced it",
		"type":         "comment",
		"revision":     int64(3),
	})
	dbfx.Insert(t, "attachment", testutil.Cols{
		"workspace_id": testWorkspaceID, "issue_id": issueID, "comment_id": commentID,
		"uploader_type": "agent", "uploader_id": roundTaskAgent(t, taskID),
		"filename": "xhs-image.png", "url": assetURL,
		"content_type": "image/png", "size_bytes": int64(9),
	})

	// See the note above: record broadcasts instead of pushing them through a
	// bounded channel that nothing drains.
	var mu sync.Mutex
	broadcasts := 0
	testHandler.Bus.Subscribe(protocol.EventCommentUpdated, func(events.Event) {
		mu.Lock()
		broadcasts++
		mu.Unlock()
	})

	req := withURLParam(newRequest(http.MethodDelete, "/api/aurora/assets/"+assetID, nil), "id", assetID)
	testutil.Call(t, testHandler.DeleteAuroraAsset, req).Want(http.StatusNoContent)

	if n := dbfx.Count(t, "SELECT count(*) FROM attachment WHERE url = $1", assetURL); n != 0 {
		t.Fatalf("attachment rows for the deleted asset = %d, want 0", n)
	}
	var revision int64
	dbfx.QueryRow(t, "SELECT revision FROM comment WHERE id = $1", commentID).Scan(&revision)
	if revision <= 3 {
		t.Fatalf("comment revision = %d, want bumped past 3", revision)
	}
	mu.Lock()
	defer mu.Unlock()
	if broadcasts == 0 {
		t.Fatal("deleting a library asset did not rebroadcast the reply")
	}
}
