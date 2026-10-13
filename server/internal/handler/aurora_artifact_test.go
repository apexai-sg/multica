package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/aurora"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// The artifact report is all-or-nothing and idempotent. These tests pin both
// halves: a retried report must not duplicate a workspace asset, and a report
// that cannot fully commit must leave no partial asset behind while its
// uncommitted storage objects are removed and the generation is failed and
// refunded exactly once.

// auroraReportFixture seeds the task/generation pair a report acts on.
func auroraReportFixture(t *testing.T, skillID string) (taskID, generationID string) {
	t.Helper()
	issueID := dbfx.Issue(t, "aurora artifact report issue")
	agentID := dbfx.Agent(t, "AuroraArtifactReportAgent "+uuid.NewString(), handlerTestRuntimeID(t))
	taskID = dbfx.Task(t, agentID, testutil.Cols{
		"runtime_id": handlerTestRuntimeID(t),
		"status":     "running",
		"issue_id":   issueID,
	})
	generationID = dbfx.Insert(t, "aurora_generation", testutil.Cols{
		"workspace_id":     testWorkspaceID,
		"user_id":          testUserID,
		"skill_id":         skillID,
		"prompt":           "artifact report",
		"status":           "running",
		"task_id":          taskID,
		"credits_reserved": int64(62_000_000),
	})
	return taskID, generationID
}

// auroraStagingFixture is one staged object a report can name.
type auroraStagingFixture struct {
	StagingID          string
	ManifestArtifactID string
	Name               string
	StorageKey         string
	SizeBytes          int64
	SHA256             string
	Metadata           string
}

func (f auroraStagingFixture) payload(role string) AuroraArtifactPayload {
	return AuroraArtifactPayload{
		ManifestArtifactID: f.ManifestArtifactID,
		StagingID:          f.StagingID,
		Name:               f.Name,
		Kind:               "image",
		Role:               role,
		Format:             "png",
		MIMEType:           "image/png",
		SizeBytes:          f.SizeBytes,
		SHA256:             f.SHA256,
		Metadata:           map[string]any{},
	}
}

// seedAuroraStaging inserts one staged object and stores its bytes, mirroring
// what Task 6's upload endpoint leaves behind.
func seedAuroraStaging(t *testing.T, store *artifactTestStorage, taskID, generationID, manifestArtifactID, name, role string, content []byte, metadataJSON string) auroraStagingFixture {
	t.Helper()
	stagingID := uuid.NewString()
	storageKey := "workspaces/" + testWorkspaceID + "/aurora-artifacts/" + stagingID + "/" + name
	store.put(storageKey, content)
	sum := sha256.Sum256(content)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	if metadataJSON == "" {
		metadataJSON = "{}"
	}
	dbfx.Insert(t, "aurora_artifact_staging", testutil.Cols{
		"id":                   stagingID,
		"task_id":              taskID,
		"generation_id":        generationID,
		"workspace_id":         testWorkspaceID,
		"manifest_artifact_id": manifestArtifactID,
		"storage_key":          storageKey,
		"name":                 name,
		"kind":                 "image",
		"role":                 role,
		"format":               "png",
		"mime_type":            "image/png",
		"size_bytes":           int64(len(content)),
		"sha256":               digest,
		"metadata":             metadataJSON,
		"source_type":          "upload",
	})
	return auroraStagingFixture{
		StagingID:          stagingID,
		ManifestArtifactID: manifestArtifactID,
		Name:               name,
		StorageKey:         storageKey,
		SizeBytes:          int64(len(content)),
		SHA256:             digest,
		Metadata:           metadataJSON,
	}
}

func reportAuroraArtifacts(t *testing.T, taskID string, artifacts ...AuroraArtifactPayload) *testutil.Response {
	t.Helper()
	req := newDaemonTokenRequest(http.MethodPost, "/api/daemon/tasks/"+taskID+"/artifacts",
		map[string]any{"artifacts": artifacts}, testWorkspaceID, "aurora-report-daemon")
	req = withURLParam(req, "taskId", taskID)
	return testutil.Call(t, testHandler.ReportTaskArtifacts, req)
}

func auroraAssetCount(t *testing.T, generationID string) int {
	t.Helper()
	return dbfx.Count(t, `SELECT count(*) FROM aurora_asset WHERE generation_id = $1`, generationID)
}

func auroraStagingStatus(t *testing.T, stagingID string) string {
	t.Helper()
	var status string
	dbfx.QueryRow(t, `SELECT status FROM aurora_artifact_staging WHERE id = $1`, stagingID).Scan(&status)
	return status
}

// TestReportTaskArtifactsCommitsStagedAssets is the success path: the assets are
// written with the manifest identity and every staging row is committed.
func TestReportTaskArtifactsCommitsStagedAssets(t *testing.T) {
	store := &artifactTestStorage{}
	withArtifactStorage(t, store)
	withAuroraModeration(t, &stubModerator{assetDecision: aurora.Decision{Allowed: true}})

	taskID, generationID := auroraReportFixture(t, "xhs-image")
	first := seedAuroraStaging(t, store, taskID, generationID, "primary-1", "primary-1.png", "primary", []byte("\x89PNG\r\n\x1a\n\x00\x00"), "{}")
	second := seedAuroraStaging(t, store, taskID, generationID, "secondary-1", "secondary-1.png", "supporting", []byte("\x89PNG\r\n\x1a\n\x00\x01"), "{}")

	if got := reportAuroraArtifacts(t, taskID, first.payload("primary"), second.payload("supporting")); got.Code != http.StatusOK {
		t.Fatalf("report status = %d, want 200: %s", got.Code, got.Body.String())
	}

	rows, err := testHandler.Queries.ListAuroraAssets(context.Background(), db.ListAuroraAssetsParams{
		GenerationID: parseUUID(generationID),
		WorkspaceID:  parseUUID(testWorkspaceID),
		Limit:        10,
		Offset:       0,
	})
	if err != nil {
		t.Fatalf("ListAuroraAssets: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("asset count = %d, want 2", len(rows))
	}
	seen := map[string]bool{}
	for _, row := range rows {
		if !row.ManifestArtifactID.Valid {
			t.Errorf("asset %s has no manifest_artifact_id", row.ID)
			continue
		}
		seen[row.ManifestArtifactID.String] = true
		if row.Kind != "image" || row.MimeType.String != "image/png" || row.Format.String != "png" {
			t.Errorf("asset %+v has the wrong kind/mime/format", row)
		}
		if row.SizeBytes.Int64 == 0 || !row.Sha256.Valid {
			t.Errorf("asset %+v did not persist size/sha256", row)
		}
		if !row.MediaUrl.Valid || row.MediaUrl.String == "" {
			t.Errorf("asset %+v has no resolved media_url", row)
		}
	}
	if !seen["primary-1"] || !seen["secondary-1"] {
		t.Fatalf("asset manifest ids = %v, want primary-1 and secondary-1", seen)
	}
	if got := auroraStagingStatus(t, first.StagingID); got != "committed" {
		t.Errorf("primary staging status = %q, want committed", got)
	}
	if got := auroraStagingStatus(t, second.StagingID); got != "committed" {
		t.Errorf("secondary staging status = %q, want committed", got)
	}
}

// TestReportTaskArtifactsIdenticalRetryCreatesNoDuplicate proves the report is
// idempotent: replaying the daemon's report must not mint a second asset.
func TestReportTaskArtifactsIdenticalRetryCreatesNoDuplicate(t *testing.T) {
	store := &artifactTestStorage{}
	withArtifactStorage(t, store)
	withAuroraModeration(t, &stubModerator{assetDecision: aurora.Decision{Allowed: true}})

	taskID, generationID := auroraReportFixture(t, "xhs-image")
	staged := seedAuroraStaging(t, store, taskID, generationID, "primary-1", "primary-1.png", "primary", []byte("\x89PNG\r\n\x1a\n\x00\x00"), "{}")
	payload := staged.payload("primary")

	if got := reportAuroraArtifacts(t, taskID, payload); got.Code != http.StatusOK {
		t.Fatalf("first report status = %d, want 200: %s", got.Code, got.Body.String())
	}
	if got := reportAuroraArtifacts(t, taskID, payload); got.Code != http.StatusOK {
		t.Fatalf("retry report status = %d, want 200: %s", got.Code, got.Body.String())
	}
	if n := auroraAssetCount(t, generationID); n != 1 {
		t.Fatalf("asset count after retry = %d, want 1", n)
	}
	if got := auroraStagingStatus(t, staged.StagingID); got != "committed" {
		t.Errorf("staging status after retry = %q, want committed", got)
	}
}

// TestReportTaskArtifactsConflictingAssetFailsClosed pins the other idempotency
// half: the same (generation, manifest artifact id) with a different hash is a
// conflict, not an overwrite.
func TestReportTaskArtifactsConflictingAssetFailsClosed(t *testing.T) {
	store := &artifactTestStorage{}
	withArtifactStorage(t, store)
	withAuroraModeration(t, &stubModerator{assetDecision: aurora.Decision{Allowed: true}})

	taskID, generationID := auroraReportFixture(t, "xhs-image")
	staged := seedAuroraStaging(t, store, taskID, generationID, "primary-1", "primary-1.png", "primary", []byte("\x89PNG\r\n\x1a\n\x00\x00"), "{}")
	// A previous write already owns this manifest id with a different hash.
	dbfx.Insert(t, "aurora_asset", testutil.Cols{
		"generation_id":        generationID,
		"workspace_id":         testWorkspaceID,
		"kind":                 "image",
		"media_url":            "https://cdn.example.com/old",
		"format":               "png",
		"manifest_artifact_id": "primary-1",
		"name":                 "primary-1.png",
		"mime_type":            "image/png",
		"size_bytes":           int64(12),
		"sha256":               "sha256:" + strings.Repeat("f", 64),
		"role":                 "primary",
		"metadata":             "{}",
	})

	got := reportAuroraArtifacts(t, taskID, staged.payload("primary"))
	if got.Code != http.StatusConflict {
		t.Fatalf("conflicting report status = %d, want 409: %s", got.Code, got.Body.String())
	}
	if n := auroraAssetCount(t, generationID); n != 1 {
		t.Fatalf("asset count after conflict = %d, want the pre-existing 1", n)
	}
	if got := auroraStagingStatus(t, staged.StagingID); got != "staged" {
		t.Errorf("staging status after conflict = %q, want staged (the transaction rolled back)", got)
	}
}

// TestReportTaskArtifactsRejectsUnknownStagingID proves a report cannot name an
// object the task never staged.
func TestReportTaskArtifactsRejectsUnknownStagingID(t *testing.T) {
	store := &artifactTestStorage{}
	withArtifactStorage(t, store)
	withAuroraModeration(t, &stubModerator{assetDecision: aurora.Decision{Allowed: true}})

	taskID, generationID := auroraReportFixture(t, "xhs-image")
	payload := AuroraArtifactPayload{
		ManifestArtifactID: "primary-1",
		StagingID:          uuid.NewString(),
		Name:               "primary-1.png",
		Kind:               "image",
		Role:               "primary",
		Format:             "png",
		MIMEType:           "image/png",
		SizeBytes:          12,
		SHA256:             "sha256:" + strings.Repeat("a", 64),
		Metadata:           map[string]any{},
	}
	if got := reportAuroraArtifacts(t, taskID, payload); got.Code != http.StatusBadRequest {
		t.Fatalf("unknown staging id status = %d, want 400: %s", got.Code, got.Body.String())
	}
	if n := auroraAssetCount(t, generationID); n != 0 {
		t.Fatalf("asset count = %d, want 0", n)
	}
}

// TestReportTaskArtifactsRejectsForeignStagingID proves a staging row owned by a
// different task cannot be claimed by this one.
func TestReportTaskArtifactsRejectsForeignStagingID(t *testing.T) {
	store := &artifactTestStorage{}
	withArtifactStorage(t, store)
	withAuroraModeration(t, &stubModerator{assetDecision: aurora.Decision{Allowed: true}})

	foreignTaskID, foreignGenerationID := auroraReportFixture(t, "xhs-image")
	staged := seedAuroraStaging(t, store, foreignTaskID, foreignGenerationID, "primary-1", "primary-1.png", "primary", []byte("\x89PNG\r\n\x1a\n\x00\x00"), "{}")

	taskID, generationID := auroraReportFixture(t, "xhs-image")
	if got := reportAuroraArtifacts(t, taskID, staged.payload("primary")); got.Code != http.StatusBadRequest {
		t.Fatalf("foreign staging id status = %d, want 400: %s", got.Code, got.Body.String())
	}
	if n := auroraAssetCount(t, generationID); n != 0 {
		t.Fatalf("asset count = %d, want 0", n)
	}
}

// TestReportTaskArtifactsRejectsMetadataMismatch proves the report cannot move
// the goalposts a staged object was validated against at upload time.
func TestReportTaskArtifactsRejectsMetadataMismatch(t *testing.T) {
	store := &artifactTestStorage{}
	withArtifactStorage(t, store)
	withAuroraModeration(t, &stubModerator{assetDecision: aurora.Decision{Allowed: true}})

	taskID, generationID := auroraReportFixture(t, "xhs-image")
	staged := seedAuroraStaging(t, store, taskID, generationID, "primary-1", "primary-1.png", "primary", []byte("\x89PNG\r\n\x1a\n\x00\x00"), "{}")
	payload := staged.payload("primary")
	payload.SHA256 = "sha256:" + strings.Repeat("b", 64)

	if got := reportAuroraArtifacts(t, taskID, payload); got.Code != http.StatusBadRequest {
		t.Fatalf("metadata mismatch status = %d, want 400: %s", got.Code, got.Body.String())
	}
	if n := auroraAssetCount(t, generationID); n != 0 {
		t.Fatalf("asset count = %d, want 0", n)
	}
}

// TestReportTaskArtifactsModerationFailureDeletesStagingObjects is the
// fail-closed path: a rejected batch commits nothing, its staging objects are
// deleted, and the generation is failed and refunded once.
func TestReportTaskArtifactsModerationFailureDeletesStagingObjects(t *testing.T) {
	creditTestReset(t)
	resetAuroraGenerations(t)
	resetAuroraModerationLog(t)

	store := &artifactTestStorage{}
	withArtifactStorage(t, store)
	withAuroraModeration(t, &stubModerator{assetDecision: aurora.Decision{Allowed: false, Reason: "image is explicit"}})

	taskID, generationID := auroraReportFixture(t, "xhs-image")
	staged := seedAuroraStaging(t, store, taskID, generationID, "primary-1", "primary-1.png", "primary", []byte("\x89PNG\r\n\x1a\n\x00\x00"), "{}")

	if got := reportAuroraArtifacts(t, taskID, staged.payload("primary")); got.Code != http.StatusUnprocessableEntity {
		t.Fatalf("moderation failure status = %d, want 422: %s", got.Code, got.Body.String())
	}
	if n := auroraAssetCount(t, generationID); n != 0 {
		t.Fatalf("asset count after moderation failure = %d, want 0", n)
	}
	if len(store.deleted) == 0 {
		t.Fatal("moderation failure left the staging object in storage")
	}
	var status string
	dbfx.QueryRow(t, `SELECT status FROM aurora_generation WHERE id = $1`, generationID).Scan(&status)
	if status != "failed" {
		t.Errorf("generation status = %q, want failed", status)
	}
}

// TestReportTaskArtifactsTransactionFailureLeavesNoPartialAssets fails the
// commit itself: with the transaction refused, no asset may be visible and the
// uncommitted objects are cleaned up.
func TestReportTaskArtifactsTransactionFailureLeavesNoPartialAssets(t *testing.T) {
	creditTestReset(t)
	resetAuroraGenerations(t)
	resetAuroraModerationLog(t)

	store := &artifactTestStorage{}
	withArtifactStorage(t, store)
	withAuroraModeration(t, &stubModerator{assetDecision: aurora.Decision{Allowed: true}})
	withAuroraTxStarter(t, auroraRefusingTxStarter{})

	taskID, generationID := auroraReportFixture(t, "xhs-image")
	first := seedAuroraStaging(t, store, taskID, generationID, "primary-1", "primary-1.png", "primary", []byte("\x89PNG\r\n\x1a\n\x00\x00"), "{}")
	second := seedAuroraStaging(t, store, taskID, generationID, "secondary-1", "secondary-1.png", "supporting", []byte("\x89PNG\r\n\x1a\n\x00\x01"), "{}")

	if got := reportAuroraArtifacts(t, taskID, first.payload("primary"), second.payload("supporting")); got.Code != http.StatusInternalServerError {
		t.Fatalf("transaction failure status = %d, want 500: %s", got.Code, got.Body.String())
	}
	if n := auroraAssetCount(t, generationID); n != 0 {
		t.Fatalf("asset count after a failed transaction = %d, want 0", n)
	}
	for _, staged := range []auroraStagingFixture{first, second} {
		if got := auroraStagingStatus(t, staged.StagingID); got != "staged" {
			t.Errorf("staging status after a failed transaction = %q, want staged", got)
		}
	}
	if len(store.deleted) < 2 {
		t.Fatalf("deleted objects = %d, want both uncommitted objects removed", len(store.deleted))
	}
}

func TestReportTaskArtifactsRejectsNonAuroraTask(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	issueID := dbfx.Issue(t, "non-aurora artifact issue")
	agentID := dbfx.Agent(t, "NonAuroraArtifactAgent", handlerTestRuntimeID(t))
	taskID := dbfx.Task(t, agentID, testutil.Cols{
		"runtime_id": handlerTestRuntimeID(t),
		"status":     "running",
		"issue_id":   issueID,
	})

	req := newDaemonTokenRequest(http.MethodPost, "/api/daemon/tasks/"+taskID+"/artifacts",
		map[string]any{"artifacts": []AuroraArtifactPayload{{ManifestArtifactID: "primary-1", StagingID: uuid.NewString()}}},
		testWorkspaceID, "non-aurora-artifact-daemon")
	req = withURLParam(req, "taskId", taskID)

	testutil.Call(t, testHandler.ReportTaskArtifacts, req).Want(http.StatusNotFound)
}

// auroraRefusingTxStarter simulates a commit path that cannot start a
// transaction.
type auroraRefusingTxStarter struct{}

func (auroraRefusingTxStarter) Begin(context.Context) (pgx.Tx, error) {
	return nil, errors.New("aurora test: transaction refused")
}

func withAuroraTxStarter(t *testing.T, starter txStarter) {
	t.Helper()
	previous := testHandler.TxStarter
	testHandler.TxStarter = starter
	t.Cleanup(func() { testHandler.TxStarter = previous })
}

// genIDOfTask returns the generation id reverse-linked to taskID.
func genIDOfTask(t *testing.T, taskID string) string {
	t.Helper()
	var id pgtype.UUID
	if err := testPool.QueryRow(context.Background(),
		`SELECT id FROM aurora_generation WHERE task_id = $1`, taskID).Scan(&id); err != nil {
		t.Fatalf("load generation by task: %v", err)
	}
	return id.String()
}

// TestReportTaskArtifactsNeedsInputMarksGeneration pins the question-round
// exit on the server: an empty artifact batch is accepted only when it declares
// needs_input, moves the generation to needs_input by CAS, and leaves the
// reservation untouched.
func TestReportTaskArtifactsNeedsInputMarksGeneration(t *testing.T) {
	taskID, generationID := auroraReportFixture(t, "xhs-image")
	// A generation's stored status stays queued while its task runs; the
	// needs_input CAS is guarded on that value.
	dbfx.Exec(t, `UPDATE aurora_generation SET status = 'queued' WHERE id = $1`, generationID)

	report := func() *testutil.Response {
		t.Helper()
		req := newDaemonTokenRequest(http.MethodPost, "/api/daemon/tasks/"+taskID+"/artifacts",
			map[string]any{"outcome": "needs_input", "artifacts": []any{}}, testWorkspaceID, "aurora-needs-input-daemon")
		req = withURLParam(req, "taskId", taskID)
		return testutil.Call(t, testHandler.ReportTaskArtifacts, req)
	}

	report().Want(http.StatusOK)

	assertNeedsInput := func() {
		t.Helper()
		var status string
		var reserved, charged int64
		dbfx.QueryRow(t, `SELECT status, credits_reserved, credits_charged FROM aurora_generation WHERE id = $1`, generationID).
			Scan(&status, &reserved, &charged)
		if status != "needs_input" {
			t.Fatalf("status = %q, want needs_input", status)
		}
		if reserved != 62_000_000 {
			t.Errorf("credits_reserved = %d, want 62000000", reserved)
		}
		if charged != 0 {
			t.Errorf("credits_charged = %d, want 0", charged)
		}
	}
	assertNeedsInput()

	// The status='queued' CAS makes a replayed report a no-op, not a second
	// write.
	report().Want(http.StatusOK)
	assertNeedsInput()
}
