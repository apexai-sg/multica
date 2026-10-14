package handler

// The server-side half of Plan C Task 8's route proof. The Node broker matrix
// proves each available skill's exact tool chain; this matrix proves the server
// lifecycle every route shares: a skill creates a generation with its declared
// inputs, the validated attachments reach the queued task, the managed runner's
// staged artifacts become task-owned assets, and the generation settles with
// the exact catalog credit debit on success and the exact reserve refund on
// failure.
//
// The fake managed runner is matrixFakeRunner below plus the suite's
// fakeSandboxManager control plane; storage and moderation are the same fakes
// the artifact tests use. The canonical Go analogue of the E2E TestApiClient is
// the package's testutil.Call harness, which drives the production handlers
// against real database rows.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/aurora"
	"github.com/multica-ai/multica/server/internal/testutil"
)

var matrixTextBytes = []byte("# Matrix artifact\n\nbody\n")

type matrixAttachment struct {
	filename    string
	contentType string
	size        int64
}

// matrixArtifact is one expected staging artifact for a skill's output policy.
type matrixArtifact struct {
	id      string
	kind    string
	format  string
	mime    string
	name    string
	role    string
	content []byte
}

type matrixSkillCase struct {
	skillID    string
	attachment *matrixAttachment
	outputs    []matrixArtifact
}

func matrixTextArtifact(id, name, format, mime string) matrixArtifact {
	return matrixArtifact{id: id, kind: "text", format: format, mime: mime, name: name, role: "primary", content: matrixTextBytes}
}

// auroraMatrixCases is the fixed route table as the server sees it: one case
// per available expert. Every expert takes a written request plus at most one
// source document and reports a single Markdown artifact on the shared
// claude-text route.
func auroraMatrixCases() []matrixSkillCase {
	document := &matrixAttachment{filename: "notes.md", contentType: "text/markdown", size: 2048}
	cases := make([]matrixSkillCase, 0, len(aurora.Catalog()))
	for _, entry := range aurora.Catalog() {
		if !entry.Available {
			continue
		}
		cases = append(cases, matrixSkillCase{
			skillID:    entry.ID,
			attachment: document,
			outputs:    []matrixArtifact{matrixTextArtifact("primary-1", entry.ID+".md", "md", "text/markdown")},
		})
	}
	return cases
}

// matrixFakeRunner stands in for the managed sandbox daemon. It stages the
// artifacts the broker manifest would report, reports them through the real
// task-token endpoint, and drives the task to a terminal state.
type matrixFakeRunner struct {
	t            *testing.T
	store        *artifactTestStorage
	taskID       string
	generationID string
}

func (r *matrixFakeRunner) stage(artifact matrixArtifact) AuroraArtifactPayload {
	r.t.Helper()
	stagingID := uuid.NewString()
	storageKey := "workspaces/" + testWorkspaceID + "/aurora-artifacts/" + stagingID + "/" + artifact.name
	r.store.put(storageKey, artifact.content)
	sum := sha256.Sum256(artifact.content)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	dbfx.Insert(r.t, "aurora_artifact_staging", testutil.Cols{
		"id":                   stagingID,
		"task_id":              r.taskID,
		"generation_id":        r.generationID,
		"workspace_id":         testWorkspaceID,
		"manifest_artifact_id": artifact.id,
		"storage_key":          storageKey,
		"name":                 artifact.name,
		"kind":                 artifact.kind,
		"role":                 artifact.role,
		"format":               artifact.format,
		"mime_type":            artifact.mime,
		"size_bytes":           int64(len(artifact.content)),
		"sha256":               digest,
		"metadata":             "{}",
		"source_type":          "upload",
	})
	return AuroraArtifactPayload{
		ManifestArtifactID: artifact.id,
		StagingID:          stagingID,
		Name:               artifact.name,
		Kind:               artifact.kind,
		Role:               artifact.role,
		Format:             artifact.format,
		MIMEType:           artifact.mime,
		SizeBytes:          int64(len(artifact.content)),
		SHA256:             digest,
		Metadata:           map[string]any{},
	}
}

func (r *matrixFakeRunner) report(outputs []matrixArtifact) {
	r.t.Helper()
	payloads := make([]AuroraArtifactPayload, 0, len(outputs))
	for _, artifact := range outputs {
		payloads = append(payloads, r.stage(artifact))
	}
	got := reportAuroraArtifacts(r.t, r.taskID, payloads...)
	if got.Code != http.StatusOK {
		r.t.Fatalf("report artifacts: status %d: %s", got.Code, got.Body.String())
	}
}

func (r *matrixFakeRunner) markRunning() {
	r.t.Helper()
	dbfx.Exec(r.t, "UPDATE agent_task_queue SET status = 'running', started_at = now() WHERE id = $1", r.taskID)
}

func (r *matrixFakeRunner) complete() {
	r.t.Helper()
	req := newDaemonTokenRequest(http.MethodPost, "/api/daemon/tasks/"+r.taskID+"/complete",
		map[string]any{"output": "matrix complete"}, testWorkspaceID, "aurora-matrix-daemon")
	req = withURLParam(req, "taskId", r.taskID)
	testutil.Call(r.t, testHandler.CompleteTask, req).Want(http.StatusOK)
}

func (r *matrixFakeRunner) fail() {
	r.t.Helper()
	req := newDaemonTokenRequest(http.MethodPost, "/api/daemon/tasks/"+r.taskID+"/fail",
		map[string]any{"error": "matrix provider failure", "failure_reason": "agent_error"}, testWorkspaceID, "aurora-matrix-daemon")
	req = withURLParam(req, "taskId", r.taskID)
	testutil.Call(r.t, testHandler.FailTask, req).Want(http.StatusOK)
}

type matrixGeneration struct {
	id                  string
	taskID              string
	reserved            int64
	balanceAfterReserve int64
}

// createMatrixGeneration funds a wallet, creates one generation for the case,
// and proves the request's attachments reached the queued task. It installs a
// permissive prompt/asset moderator so the matrix owns the wiring, not the
// blocklist contents.
func createMatrixGeneration(t *testing.T, tc matrixSkillCase) matrixGeneration {
	t.Helper()
	creditTestReset(t)
	cleanupAuroraSystemAgents(t)
	cleanupAuroraWork(t)
	withAuroraModeration(t, &stubModerator{
		promptDecision: aurora.Decision{Allowed: true},
		assetDecision:  aurora.Decision{Allowed: true},
	})

	ctx := context.Background()
	if err := testHandler.Credit.Grant(ctx, parseUUID(testUserID), parseUUID(testWorkspaceID),
		int64(1_000_000_000_000), aurora.LedgerKindAdjustment, creditRef("matrix-"+tc.skillID)); err != nil {
		t.Fatalf("grant: %v", err)
	}

	body := map[string]any{"skillId": tc.skillID, "prompt": "matrix " + tc.skillID + " prompt"}
	var attachmentIDs []string
	if tc.attachment != nil {
		attachmentIDs = []string{insertAuroraAttachment(t, tc.attachment.filename, tc.attachment.contentType, tc.attachment.size, testUserID)}
		body["attachmentIds"] = attachmentIDs
	}

	out := testutil.Decode[struct {
		Generation struct {
			ID              string "json:\"id\""
			Status          string "json:\"status\""
			CreditsReserved int64  "json:\"creditsReserved\""
		} "json:\"generation\""
	}](t, testHandler.CreateAuroraGeneration, newRequest(http.MethodPost, "/api/aurora/generations", body), http.StatusCreated)

	if out.Generation.Status != "queued" {
		t.Fatalf("generation status = %q, want queued", out.Generation.Status)
	}
	entry, ok := aurora.Lookup(tc.skillID)
	if !ok {
		t.Fatalf("catalog entry %q not found", tc.skillID)
	}
	wantReserved := int64(entry.Credits) * microCreditsPerCredit
	if out.Generation.CreditsReserved != wantReserved {
		t.Fatalf("creditsReserved = %d, want %d", out.Generation.CreditsReserved, wantReserved)
	}

	var taskID pgtype.UUID
	if err := testPool.QueryRow(ctx, "SELECT task_id FROM aurora_generation WHERE id = $1", out.Generation.ID).Scan(&taskID); err != nil {
		t.Fatalf("load generation task: %v", err)
	}
	if !taskID.Valid {
		t.Fatalf("generation %s has no task id", out.Generation.ID)
	}

	ids := auroraTaskAttachmentIDs(t, out.Generation.ID)
	if !slices.Equal(ids, attachmentIDs) {
		t.Fatalf("task attachment_ids = %v, want %v", ids, attachmentIDs)
	}

	balance, err := testHandler.Credit.Balance(ctx, parseUUID(testUserID))
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	return matrixGeneration{
		id:                  out.Generation.ID,
		taskID:              taskID.String(),
		reserved:            wantReserved,
		balanceAfterReserve: balance,
	}
}

func matrixLedger(t *testing.T, kind string) (int64, int64) {
	t.Helper()
	var count, sum int64
	dbfx.QueryRow(t, "SELECT count(*), COALESCE(sum(amount_micro), 0) FROM credit_ledger WHERE user_id = $1 AND kind = $2", testUserID, kind).Scan(&count, &sum)
	return count, sum
}

func matrixBalance(t *testing.T) int64 {
	t.Helper()
	balance, err := testHandler.Credit.Balance(context.Background(), parseUUID(testUserID))
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	return balance
}

func matrixGenerationRow(t *testing.T, generationID string) (status string, charged int64, taskStatus string) {
	t.Helper()
	dbfx.QueryRow(t, "SELECT g.status, g.credits_charged, q.status FROM aurora_generation g JOIN agent_task_queue q ON q.id = g.task_id WHERE g.id = $1", generationID).Scan(&status, &charged, &taskStatus)
	return status, charged, taskStatus
}

// TestAuroraSkillMatrixSuccessLifecycle proves every available skill creates,
// routes its declared attachments, reports its expected staging artifacts, and
// settles with exactly the catalog debit already reserved.
func TestAuroraSkillMatrixSuccessLifecycle(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	for _, tc := range auroraMatrixCases() {
		tc := tc
		t.Run(tc.skillID, func(t *testing.T) {
			store := &artifactTestStorage{}
			withArtifactStorage(t, store)

			generation := createMatrixGeneration(t, tc)
			runner := &matrixFakeRunner{t: t, store: store, taskID: generation.taskID, generationID: generation.id}
			runner.markRunning()
			runner.report(tc.outputs)
			if got := auroraAssetCount(t, generation.id); got != len(tc.outputs) {
				t.Fatalf("asset count after report = %d, want %d", got, len(tc.outputs))
			}

			runner.complete()

			status, charged, taskStatus := matrixGenerationRow(t, generation.id)
			if status != "completed" {
				t.Fatalf("generation status = %q, want completed", status)
			}
			if charged != generation.reserved {
				t.Fatalf("credits_charged = %d, want the reserved %d", charged, generation.reserved)
			}
			if taskStatus != "completed" {
				t.Fatalf("task status = %q, want completed", taskStatus)
			}
			if count, sum := matrixLedger(t, aurora.LedgerKindRefund); count != 0 || sum != 0 {
				t.Fatalf("success refunded %d row(s) summing %d, want none", count, sum)
			}
			if balance := matrixBalance(t); balance != generation.balanceAfterReserve {
				t.Fatalf("success moved the wallet: %d -> %d", generation.balanceAfterReserve, balance)
			}
		})
	}
}

// TestAuroraSkillMatrixFailureRefund proves a failed route refunds exactly the
// reserve once and leaves no asset or charge behind.
func TestAuroraSkillMatrixFailureRefund(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	for _, tc := range auroraMatrixCases() {
		tc := tc
		t.Run(tc.skillID, func(t *testing.T) {
			generation := createMatrixGeneration(t, tc)
			runner := &matrixFakeRunner{t: t, store: &artifactTestStorage{}, taskID: generation.taskID, generationID: generation.id}
			runner.markRunning()
			runner.fail()

			status, charged, taskStatus := matrixGenerationRow(t, generation.id)
			if status != "failed" {
				t.Fatalf("generation status = %q, want failed", status)
			}
			if charged != 0 {
				t.Fatalf("credits_charged = %d, want 0 on failure", charged)
			}
			if taskStatus != "failed" {
				t.Fatalf("task status = %q, want failed", taskStatus)
			}
			if got := auroraAssetCount(t, generation.id); got != 0 {
				t.Fatalf("asset count after failure = %d, want 0", got)
			}
			count, sum := matrixLedger(t, aurora.LedgerKindRefund)
			if count != 1 || sum != generation.reserved {
				t.Fatalf("refund ledger = (%d row(s), %d), want (1, %d)", count, sum, generation.reserved)
			}
			if balance := matrixBalance(t); balance != generation.balanceAfterReserve+generation.reserved {
				t.Fatalf("failed wallet = %d, want the reserve returned (%d)", balance, generation.balanceAfterReserve+generation.reserved)
			}
		})
	}
}
