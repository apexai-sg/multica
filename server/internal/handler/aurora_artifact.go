package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/aurora"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// errAuroraAssetConflict marks an upsert that matched an existing asset with a
// different hash or metadata. The report fails closed rather than overwriting a
// committed deliverable.
var errAuroraAssetConflict = errors.New("aurora: asset conflicts with an existing row")

// auroraGenerationOutcomeNeedsInput is the artifact report's declarative
// question-round ending: the round stopped to ask a question and staged no
// artifacts. It is the only outcome an empty artifact list may carry.
const auroraGenerationOutcomeNeedsInput = "needs_input"

// AuroraArtifactPayload is one staged content artifact the sandbox daemon
// produced and reported for a completed generation. StagingID names the object
// the task-token upload/import endpoints minted; the server resolves the
// storage URL and object ownership from it, so no caller-supplied URL is ever
// trusted. Name/kind/role/format/mime/size/sha256/metadata are the manifest
// identity and are re-validated against the staging row before any asset write.
type AuroraArtifactPayload struct {
	ManifestArtifactID string         `json:"manifest_artifact_id"`
	StagingID          string         `json:"staging_id"`
	Name               string         `json:"name"`
	Kind               string         `json:"kind"`
	Role               string         `json:"role"`
	Format             string         `json:"format"`
	MIMEType           string         `json:"mime_type"`
	SizeBytes          int64          `json:"size_bytes"`
	SHA256             string         `json:"sha256"`
	Metadata           map[string]any `json:"metadata,omitempty"`
}

// normalizedAuroraReportArtifact is a validated payload bound to its staging
// row and resolved storage URL.
type normalizedAuroraReportArtifact struct {
	payload   AuroraArtifactPayload
	staging   db.AuroraArtifactStaging
	objectURL string
}

// ReportTaskArtifacts records the artifacts a completed Aurora task produced.
// The daemon reports only staging ids, after it has validated the broker-written
// manifest and uploaded the local files through Task 6's staging endpoint. The
// report is all-or-nothing and idempotent:
//
//   - every reported artifact is matched against a staging row owned by the
//     same task/generation/workspace, and the manifest metadata must match the
//     row exactly;
//   - every stored object is moderated before any row is written;
//   - one transaction upserts every asset by (generation_id,
//     manifest_artifact_id), marks the reported staging rows committed, and
//     rejects an existing row with a different hash or metadata;
//   - if moderation or the transaction fails, the uncommitted storage objects
//     are deleted and the generation is failed and refunded exactly once.
func (h *Handler) ReportTaskArtifacts(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")
	task, ok := h.requireDaemonTaskAccess(w, r, taskID)
	if !ok {
		return
	}

	var req struct {
		Artifacts []AuroraArtifactPayload `json:"artifacts"`
		Outcome   string                  `json:"outcome"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	outcome := strings.TrimSpace(req.Outcome)
	if len(req.Artifacts) == 0 && outcome != auroraGenerationOutcomeNeedsInput {
		writeError(w, http.StatusBadRequest, "artifacts are required")
		return
	}

	gen, err := h.Queries.GetAuroraGenerationByTaskID(r.Context(), task.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "not an aurora generation")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load generation")
		return
	}
	if _, ok := aurora.Lookup(gen.SkillID); !ok {
		// A generation always references a catalog skill; a missing entry is a
		// data inconsistency. Fail rather than write assets with an empty kind.
		writeError(w, http.StatusInternalServerError, "unknown skill")
		return
	}

	if len(req.Artifacts) == 0 {
		// outcome is needs_input here. The CAS on status='queued' is idempotent
		// and refuses to overwrite a row another settlement already moved; no
		// credits move and no asset is written. A non-empty list keeps the
		// existing validation and commit path below unchanged.
		if _, err := h.Queries.MarkAuroraGenerationNeedsInput(r.Context(), db.MarkAuroraGenerationNeedsInputParams{
			ID:          gen.ID,
			WorkspaceID: gen.WorkspaceID,
		}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			slog.Error("aurora needs_input mark failed", "generation_id", uuidToString(gen.ID), "error", err)
			writeError(w, http.StatusInternalServerError, "failed to mark generation")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"recorded": 0})
		return
	}

	payloads, ok := h.validateAuroraReportBatch(w, gen.SkillID, req.Artifacts)
	if !ok {
		return
	}

	if h.Storage == nil {
		writeFeatureDisabled(w, "aurora_artifact_storage_not_configured", "artifact storage not configured")
		return
	}
	stagingRows, err := h.listAuroraArtifactStaging(r.Context(), task.ID, gen.ID, gen.WorkspaceID)
	if err != nil {
		slog.Error("aurora artifact staging load failed", "generation_id", uuidToString(gen.ID), "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load staged artifacts")
		return
	}
	stagingByID := make(map[string]db.AuroraArtifactStaging, len(stagingRows))
	for _, row := range stagingRows {
		stagingByID[uuidToString(row.ID)] = row
	}

	bound := make([]normalizedAuroraReportArtifact, 0, len(payloads))
	for _, payload := range payloads {
		row, found := stagingByID[payload.StagingID]
		if !found {
			writeError(w, http.StatusBadRequest, "unknown staging artifact")
			return
		}
		if !auroraStagingMatchesPayload(row, payload) {
			writeError(w, http.StatusBadRequest, "artifact metadata does not match its staged object")
			return
		}
		bound = append(bound, normalizedAuroraReportArtifact{
			payload:   payload,
			staging:   row,
			objectURL: h.Storage.ObjectURL(row.StorageKey),
		})
	}

	// Screen every stored object before writing a single row. An artifact the
	// moderator rejects must not leave its siblings stored: the generation is
	// about to be failed, and a library that still showed output from it would
	// contradict the generation's own status.
	for _, artifact := range bound {
		decision, err := h.Moderation.ScreenAsset(r.Context(), artifact.objectURL, artifact.payload.Kind)
		if err != nil {
			slog.Error("aurora asset moderation failed", "generation_id", uuidToString(gen.ID), "error", err)
			h.deleteUncommittedAuroraStaging(r.Context(), stagingRows)
			h.blockGenerationForModeration(r.Context(), gen, "moderation adapter error: "+err.Error())
			writeError(w, http.StatusInternalServerError, "content moderation unavailable")
			return
		}
		if !decision.Allowed {
			h.deleteUncommittedAuroraStaging(r.Context(), stagingRows)
			h.blockGenerationForModeration(r.Context(), gen, decision.Reason)
			writeError(w, http.StatusUnprocessableEntity, decision.Reason)
			return
		}
	}

	if err := h.commitAuroraArtifacts(r.Context(), gen, task.ID, task.AgentID, bound); err != nil {
		h.deleteUncommittedAuroraStaging(r.Context(), stagingRows)
		if errors.Is(err, errAuroraAssetConflict) {
			h.failGenerationAndRefund(r.Context(), gen.UserID, gen.WorkspaceID, gen.ID, gen.CreditsReserved, "artifact conflict")
			writeError(w, http.StatusConflict, "artifact conflicts with an existing asset")
			return
		}
		slog.Error("aurora artifact report transaction failed", "generation_id", uuidToString(gen.ID), "error", err)
		h.failGenerationAndRefund(r.Context(), gen.UserID, gen.WorkspaceID, gen.ID, gen.CreditsReserved, "artifact commit failed")
		writeError(w, http.StatusInternalServerError, "failed to record artifacts")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"recorded": len(bound)})
}

// validateAuroraReportBatch enforces the server-owned artifact policy over the
// whole batch before any storage or DB read: unique identities, accepted
// kind/format/MIME, bounded size, a valid staging id, and exactly the outputs
// the skill may produce (including a primary one).
func (h *Handler) validateAuroraReportBatch(w http.ResponseWriter, skillID string, artifacts []AuroraArtifactPayload) ([]AuroraArtifactPayload, bool) {
	policy, ok := aurora.ExecutionPolicy(skillID)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unknown skill")
		return nil, false
	}
	if len(artifacts) > 20 {
		writeError(w, http.StatusBadRequest, "too many artifacts")
		return nil, false
	}
	ids := make(map[string]bool, len(artifacts))
	stagingIDs := make(map[string]bool, len(artifacts))
	hasPrimary := false
	normalized := make([]AuroraArtifactPayload, 0, len(artifacts))

	for _, artifact := range artifacts {
		artifact.ManifestArtifactID = strings.TrimSpace(artifact.ManifestArtifactID)
		artifact.StagingID = strings.TrimSpace(artifact.StagingID)
		artifact.Name = strings.TrimSpace(artifact.Name)
		artifact.Kind = strings.ToLower(strings.TrimSpace(artifact.Kind))
		artifact.Role = strings.ToLower(strings.TrimSpace(artifact.Role))
		artifact.Format = strings.ToLower(strings.TrimSpace(artifact.Format))
		artifact.MIMEType = normalizeArtifactMIME(artifact.MIMEType)

		if !auroraArtifactIDPattern.MatchString(artifact.ManifestArtifactID) || ids[artifact.ManifestArtifactID] {
			writeError(w, http.StatusBadRequest, "invalid or duplicate manifest artifact id")
			return nil, false
		}
		ids[artifact.ManifestArtifactID] = true
		if _, err := util.ParseUUID(artifact.StagingID); err != nil || stagingIDs[artifact.StagingID] {
			writeError(w, http.StatusBadRequest, "invalid or duplicate staging id")
			return nil, false
		}
		stagingIDs[artifact.StagingID] = true
		if !auroraArtifactNamePattern.MatchString(artifact.Name) || strings.Contains(artifact.Name, "..") {
			writeError(w, http.StatusBadRequest, "invalid artifact name")
			return nil, false
		}
		if !auroraArtifactRoles[artifact.Role] {
			writeError(w, http.StatusBadRequest, "invalid artifact role")
			return nil, false
		}
		spec, ok := auroraArtifactKinds[artifact.Kind]
		if !ok {
			writeError(w, http.StatusBadRequest, "unsupported artifact kind")
			return nil, false
		}
		formatKind, ok := auroraArtifactFormatKinds[artifact.Format]
		if !ok || formatKind != artifact.Kind {
			writeError(w, http.StatusBadRequest, "artifact format does not match its kind")
			return nil, false
		}
		if !auroraArtifactReportExtensionMatches(artifact.Name, artifact.Kind) {
			writeError(w, http.StatusBadRequest, "artifact name extension does not match its kind")
			return nil, false
		}
		if !slices.Contains(policy.OutputKinds, artifact.Kind) {
			// A transcript is route metadata, not the skill's declared output:
			// video-captions publishes the ASR transcript beside the primary
			// video, and the catalog only advertises the video.
			if artifact.Role != "transcript" || artifact.Kind != "text" {
				writeError(w, http.StatusBadRequest, "artifact kind is not produced by this skill")
				return nil, false
			}
		}
		if !slices.Contains(spec.MIMEs, artifact.MIMEType) {
			writeError(w, http.StatusUnsupportedMediaType, "unsupported artifact MIME type")
			return nil, false
		}
		if artifact.SizeBytes < 0 || artifact.SizeBytes > spec.MaxBytes {
			writeError(w, http.StatusRequestEntityTooLarge, "artifact exceeds the per-kind size limit")
			return nil, false
		}
		digest, ok := normalizeAuroraArtifactDigest(artifact.SHA256)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid artifact sha256")
			return nil, false
		}
		artifact.SHA256 = digest
		if artifact.Metadata == nil {
			artifact.Metadata = map[string]any{}
		}
		if encoded, err := json.Marshal(artifact.Metadata); err != nil || len(encoded) > auroraArtifactMaxMetadataBytes {
			writeError(w, http.StatusBadRequest, "artifact metadata is too large")
			return nil, false
		}
		if artifact.Role == "primary" && slices.Contains(policy.OutputKinds, artifact.Kind) {
			hasPrimary = true
		}
		normalized = append(normalized, artifact)
	}
	if !hasPrimary {
		writeError(w, http.StatusBadRequest, "a primary artifact matching the skill output is required")
		return nil, false
	}
	return normalized, true
}

// listAuroraArtifactStaging loads every staging row a report may name, scoped to
// the task, generation, and workspace on the verified daemon token.
func (h *Handler) listAuroraArtifactStaging(ctx context.Context, taskID, generationID, workspaceID pgtype.UUID) ([]db.AuroraArtifactStaging, error) {
	rows, err := h.DB.Query(ctx, `
		SELECT id, task_id, generation_id, workspace_id, manifest_artifact_id, storage_key, name,
		       kind, role, format, mime_type, size_bytes, sha256, metadata, source_type, status,
		       created_at, updated_at
		FROM aurora_artifact_staging
		WHERE task_id = $1 AND generation_id = $2 AND workspace_id = $3 AND status <> 'deleted'
		ORDER BY created_at ASC`, taskID, generationID, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var staging []db.AuroraArtifactStaging
	for rows.Next() {
		var row db.AuroraArtifactStaging
		if err := rows.Scan(
			&row.ID, &row.TaskID, &row.GenerationID, &row.WorkspaceID, &row.ManifestArtifactID,
			&row.StorageKey, &row.Name, &row.Kind, &row.Role, &row.Format, &row.MimeType,
			&row.SizeBytes, &row.Sha256, &row.Metadata, &row.SourceType, &row.Status,
			&row.CreatedAt, &row.UpdatedAt,
		); err != nil {
			return nil, err
		}
		staging = append(staging, row)
	}
	return staging, rows.Err()
}

// auroraStagingMatchesPayload proves the reported manifest identity is exactly
// what the object was staged as. Provider imports leave manifest id/role/format
// NULL until the report assigns them, so those three compare only when present.
func auroraStagingMatchesPayload(row db.AuroraArtifactStaging, payload AuroraArtifactPayload) bool {
	if row.Name != payload.Name || row.Kind != payload.Kind {
		return false
	}
	if row.MimeType != payload.MIMEType || row.SizeBytes != payload.SizeBytes || row.Sha256 != payload.SHA256 {
		return false
	}
	if row.ManifestArtifactID.Valid && row.ManifestArtifactID.String != payload.ManifestArtifactID {
		return false
	}
	if row.Role.Valid && row.Role.String != payload.Role {
		return false
	}
	if row.Format.Valid && row.Format.String != payload.Format {
		return false
	}
	return auroraMetadataEqual(row.Metadata, payload.Metadata)
}

func auroraMetadataEqual(stored []byte, reported map[string]any) bool {
	var storedMap map[string]any
	if len(bytes.TrimSpace(stored)) == 0 {
		storedMap = map[string]any{}
	} else if err := json.Unmarshal(stored, &storedMap); err != nil {
		return false
	}
	if reported == nil {
		reported = map[string]any{}
	}
	storedJSON, err := json.Marshal(storedMap)
	if err != nil {
		return false
	}
	reportedJSON, err := json.Marshal(reported)
	if err != nil {
		return false
	}
	return bytes.Equal(storedJSON, reportedJSON)
}

// insertAuroraProjectionAttachment projects one committed artifact onto its
// issue as a task-tagged, comment-less attachment. The round's reply comment
// binds the row later, so comment_id is deliberately NULL here. The NOT EXISTS
// guard keys on the producing task and object URL, which makes a replayed report
// idempotent without looking at comment_id.
const insertAuroraProjectionAttachment = `
	INSERT INTO attachment (
		id, workspace_id, issue_id, comment_id, task_id,
		uploader_type, uploader_id, filename, url, content_type, size_bytes
	)
	SELECT $1, $2, $3, NULL, $4, 'agent', $5, $6, $7, $8, $9
	WHERE NOT EXISTS (
		SELECT 1 FROM attachment
		WHERE workspace_id = $2 AND task_id = $4 AND url = $7
	)`

// commitAuroraArtifacts writes every asset and marks the reported staging rows
// committed in one transaction. It also projects each artifact onto the issue as
// a task-tagged, comment-less attachment, which the round's reply comment binds
// later. The stored object is shared: the content library (aurora_asset) owns
// the bytes, and the attachment row is only a reference to them. An upsert that
// finds an existing row with a different hash or metadata returns
// errAuroraAssetConflict and rolls the whole batch back.
func (h *Handler) commitAuroraArtifacts(ctx context.Context, gen db.AuroraGeneration, taskID, agentID pgtype.UUID, artifacts []normalizedAuroraReportArtifact) error {
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, artifact := range artifacts {
		metadata, err := json.Marshal(artifact.payload.Metadata)
		if err != nil {
			return err
		}
		var assetID pgtype.UUID
		err = tx.QueryRow(ctx, `
			INSERT INTO aurora_asset (
				generation_id, workspace_id, kind, media_url, format, manifest_artifact_id,
				name, mime_type, size_bytes, sha256, role, metadata
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
			ON CONFLICT (generation_id, manifest_artifact_id) WHERE manifest_artifact_id IS NOT NULL
			DO UPDATE SET metadata = aurora_asset.metadata
			WHERE aurora_asset.sha256 = EXCLUDED.sha256 AND aurora_asset.metadata = EXCLUDED.metadata
			RETURNING id`,
			gen.ID, gen.WorkspaceID, artifact.payload.Kind,
			pgtype.Text{String: artifact.objectURL, Valid: artifact.objectURL != ""},
			pgtype.Text{String: artifact.payload.Format, Valid: artifact.payload.Format != ""},
			pgtype.Text{String: artifact.payload.ManifestArtifactID, Valid: true},
			pgtype.Text{String: artifact.payload.Name, Valid: true},
			pgtype.Text{String: artifact.payload.MIMEType, Valid: true},
			pgtype.Int8{Int64: artifact.payload.SizeBytes, Valid: true},
			pgtype.Text{String: artifact.payload.SHA256, Valid: true},
			pgtype.Text{String: artifact.payload.Role, Valid: artifact.payload.Role != ""},
			metadata,
		).Scan(&assetID)
		if errors.Is(err, pgx.ErrNoRows) {
			return errAuroraAssetConflict
		}
		if err != nil {
			return err
		}

		tag, err := tx.Exec(ctx, `
			UPDATE aurora_artifact_staging
			SET status = 'committed',
			    manifest_artifact_id = COALESCE(manifest_artifact_id, $2),
			    role = COALESCE(role, $3),
			    format = COALESCE(format, $4),
			    updated_at = now()
			WHERE id = $1 AND task_id = $5 AND generation_id = $6 AND workspace_id = $7 AND status <> 'deleted'`,
			artifact.staging.ID, artifact.payload.ManifestArtifactID, artifact.payload.Role,
			artifact.payload.Format, artifact.staging.TaskID, artifact.staging.GenerationID,
			artifact.staging.WorkspaceID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errors.New("aurora: staging row disappeared before commit")
		}

		// Project the committed artifact onto its issue. The generation owns
		// the issue binding: a generation without an issue_id has no thread to
		// project onto, even when its task carries one.
		if gen.IssueID.Valid {
			attachmentID, err := uuid.NewV7()
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, insertAuroraProjectionAttachment,
				attachmentID, gen.WorkspaceID, gen.IssueID, taskID, agentID,
				artifact.payload.Name, artifact.objectURL,
				artifact.payload.MIMEType, artifact.payload.SizeBytes,
			); err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}

// deleteUncommittedAuroraStaging removes the storage objects of staging rows
// that were never committed. Committed rows are left alone: their assets are
// durable, and a retry must not destroy them.
func (h *Handler) deleteUncommittedAuroraStaging(ctx context.Context, rows []db.AuroraArtifactStaging) {
	if h.Storage == nil {
		return
	}
	for _, row := range rows {
		if row.Status == "committed" {
			continue
		}
		if err := h.Storage.DeleteObject(ctx, row.StorageKey); err != nil {
			slog.Error("failed to delete uncommitted aurora staging object", "key", row.StorageKey, "error", err)
		}
	}
}

// auroraArtifactReportExtensionMatches proves the declared name carries the
// extension its kind implies, so a report cannot label a video as a PNG.
func auroraArtifactReportExtensionMatches(name, kind string) bool {
	dot := strings.LastIndexByte(name, '.')
	if dot <= 0 || dot == len(name)-1 {
		return false
	}
	extensionKind, ok := auroraArtifactFormatKinds[strings.ToLower(name[dot+1:])]
	return ok && extensionKind == kind
}

func normalizeAuroraArtifactDigest(raw string) (string, bool) {
	match := auroraArtifactSHA256Regex.FindStringSubmatch(strings.ToLower(strings.TrimSpace(raw)))
	if match == nil {
		return "", false
	}
	return "sha256:" + match[1], true
}

// blockGenerationForModeration fails a generation whose artifact did not pass
// screening: the audit row, the refund, and the terminal status. The caller
// then answers the daemon with a non-2xx, which is what makes the task itself
// fail — the settlement that follows finds the generation already terminal and
// is a no-op, so the reservation is refunded exactly once.
//
// auditReason is the adapter's own account of the rejection and goes to the
// audit trail; the generation's error field gets the fixed "moderation blocked"
// so the client-facing status does not vary with the adapter's wording.
func (h *Handler) blockGenerationForModeration(ctx context.Context, gen db.AuroraGeneration, auditReason string) {
	h.recordModerationBlock(ctx, gen.ID, gen.WorkspaceID, aurora.ModerationScopeAsset, auditReason)
	h.failGenerationAndRefund(ctx, gen.UserID, gen.WorkspaceID, gen.ID, gen.CreditsReserved, "moderation blocked")
}
