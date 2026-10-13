package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/aurora"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/storage"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/stripe/stripe-go/v86"
)

// maxAuroraGenerationBodyBytes caps the request body for generation creation.
// A content prompt is small; the cap keeps an unbounded write from bloating
// the TEXT column.
const maxAuroraGenerationBodyBytes = 256 * 1024

// maxAuroraAttachments caps one generation request's attachment list. The
// widest skill (poster, xhs-image, product-image, image-edit) accepts four.
const maxAuroraAttachments = 4

// microCreditsPerCredit converts a catalog credit into the ledger's
// micro-credit unit (1 credit = 1e6 micro). Reservation and refund both move
// the amount derived with this same constant, so one write reverses the other
// exactly.
const microCreditsPerCredit = 1_000_000

// Stripe Checkout Sessions expire after at most 24 hours. Keep the local intent
// for an extra hour so request latency or modest clock skew cannot let a new
// checkout replace one Stripe may still complete.
const auroraCheckoutIntentTTL = 25 * time.Hour

// auroraRuntimeUnavailableCode is the stable error code for a workspace whose
// sandbox the fleet cannot make ready. New generation creation fails closed
// with it rather than reserving credits for work no node can run.
const auroraRuntimeUnavailableCode = "aurora_runtime_unavailable"

var (
	errAuroraMonthlyGenerationLimit = errors.New("monthly generation limit reached")
	errAuroraConcurrencyLimit       = errors.New("concurrency limit reached")
	errAuroraAllowanceUnavailable   = errors.New("monthly allowance unavailable")
	errAuroraSubscriptionConflict   = errors.New("subscription already exists")
	errAuroraRuntimeUnavailable     = errors.New("aurora runtime unavailable")
)

// ListAuroraSkills returns the full catalog, including unavailable phase-2 skills.
// The router requires authentication and workspace membership.
func (h *Handler) ListAuroraSkills(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"skills": aurora.Catalog()})
}

// AuroraGenerationResponse is the consumer-facing shape of a generation row.
type AuroraGenerationResponse struct {
	ID              string `json:"id"`
	SkillID         string `json:"skillId"`
	Prompt          string `json:"prompt"`
	Status          string `json:"status"`
	CreditsReserved int64  `json:"creditsReserved"`
	// IssueIdentifier is the "PREFIX-N" key of the issue this generation runs
	// on. Omitted entirely when the workspace prefix could not be resolved:
	// the degraded "#N" form carries no prefix, and the client reads the
	// absence as "no issue link to offer".
	IssueIdentifier string `json:"issueIdentifier,omitempty"`
}

// CreateAuroraGeneration validates the skill, lazily seeds the workspace's
// Aurora system agents, inserts a queued generation row, reserves its credits,
// and enqueues the execution task. The router guards this route with
// RequireHumanActor and RequireWorkspaceMember: machine credentials cannot
// create generations, and workspace membership is re-validated on every
// request.
func (h *Handler) CreateAuroraGeneration(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace_id")
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	userUUID, ok := parseUUIDOrBadRequest(w, userID, "user_id")
	if !ok {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxAuroraGenerationBodyBytes)
	var req struct {
		SkillID       string   `json:"skillId"`
		Prompt        string   `json:"prompt"`
		AttachmentIDs []string `json:"attachmentIds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.SkillID == "" || strings.TrimSpace(req.Prompt) == "" {
		writeError(w, http.StatusBadRequest, "skillId and prompt are required")
		return
	}
	entry, ok := aurora.Lookup(req.SkillID)
	if !ok {
		writeError(w, http.StatusBadRequest, "unknown skill")
		return
	}
	if !entry.Available {
		writeError(w, http.StatusBadRequest, "skill not available")
		return
	}

	// Resolve and validate the skill's input attachments before moderation,
	// provisioning, the generation insert, or any credit reservation. A
	// rejected file must not create a sandbox, a row, or a charge.
	attachmentIDs, ok := parseAuroraAttachmentIDs(w, req.AttachmentIDs)
	if !ok {
		return
	}
	if err := h.validateAuroraAttachments(r.Context(), workspaceID, userUUID, req.SkillID, attachmentIDs); err != nil {
		var invalid auroraInputError
		if errors.As(err, &invalid) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		slog.Error("failed to validate aurora attachments", "workspace_id", uuidToString(workspaceID), "error", err)
		writeError(w, http.StatusInternalServerError, "failed to validate attachments")
		return
	}

	// Screen before anything is written. A rejected prompt must not seed
	// agents, reserve credits, or leave a generation row behind, so the screen
	// runs ahead of every side effect rather than merely ahead of the insert.
	decision, err := h.Moderation.ScreenPrompt(r.Context(), req.Prompt)
	if err != nil {
		// Fail closed: a moderator that cannot reach a verdict must not be read
		// as approval (spec §10 red line). The failed screen is recorded — an
		// outage that silently rejects is a thing an operator has to be able to
		// see.
		slog.Error("aurora prompt moderation failed", "workspace_id", uuidToString(workspaceID), "error", err)
		h.recordModerationBlock(r.Context(), pgtype.UUID{}, workspaceID, aurora.ModerationScopePrompt, "moderation adapter error: "+err.Error())
		writeError(w, http.StatusInternalServerError, "content moderation unavailable")
		return
	}
	if !decision.Allowed {
		h.recordModerationBlock(r.Context(), pgtype.UUID{}, workspaceID, aurora.ModerationScopePrompt, decision.Reason)
		writeError(w, http.StatusUnprocessableEntity, decision.Reason)
		return
	}

	// Provision order matters: seed the workspace's Aurora system agents and
	// managed runtime, then make the workspace sandbox accepted by the fleet,
	// and only then run the entitlement check and insert the generation row.
	// The fleet call is deliberately outside every database transaction, and a
	// node may be created for a request entitlement later rejects; the idle
	// reaper removes it. That trade-off avoids reserving credits for
	// infrastructure that could not start.
	if err := h.ensureAuroraSystemAgents(r.Context(), workspaceID, userUUID); err != nil {
		slog.Error("failed to prepare aurora workspace", "workspace_id", uuidToString(workspaceID), "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create generation")
		return
	}
	runtimeID, err := aurora.ManagedRuntimeID(r.Context(), h.Queries, workspaceID)
	if err != nil {
		slog.Error("failed to load aurora managed runtime", "workspace_id", uuidToString(workspaceID), "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create generation")
		return
	}
	if err := h.ensureWorkspaceSandbox(r.Context(), workspaceID, runtimeID); err != nil {
		slog.Error("aurora sandbox unavailable", "workspace_id", uuidToString(workspaceID), "error", err)
		writeErrorCode(w, http.StatusServiceUnavailable, auroraRuntimeUnavailableCode, errAuroraRuntimeUnavailable.Error())
		return
	}

	// Serialize the entitlement decision and queued-row insertion per user. The
	// row itself occupies concurrency before its task id is attached, closing the
	// check-then-act race between simultaneous requests.
	row, err := h.createAuroraGenerationWithEntitlement(
		r.Context(), userUUID, workspaceID, req.SkillID, req.Prompt,
	)
	if err != nil {
		switch {
		case errors.Is(err, errAuroraMonthlyGenerationLimit):
			writeError(w, http.StatusTooManyRequests, err.Error())
		case errors.Is(err, errAuroraConcurrencyLimit):
			writeError(w, http.StatusTooManyRequests, err.Error())
		case errors.Is(err, errAuroraAllowanceUnavailable):
			writeError(w, http.StatusServiceUnavailable, errAuroraAllowanceUnavailable.Error())
		default:
			slog.Error("failed to create aurora generation", "user_id", userID, "error", err)
			writeError(w, http.StatusInternalServerError, "failed to create generation")
		}
		return
	}

	amountMicro := int64(entry.Credits) * microCreditsPerCredit
	reference := uuidToString(row.ID)

	// Bind the issue before reserving or enqueuing. attributionForIssueTask
	// resolves the run's accountable human from the issue creator, so a create
	// without CreatorID would fall back and, in a fail-closed workspace, refuse
	// the enqueue. The assignee-driven auto-enqueue is suppressed: the Aurora
	// enqueue below is the only task this issue gets.
	agent, err := h.Queries.GetAgentBySystemKey(r.Context(), db.GetAgentBySystemKeyParams{
		WorkspaceID: workspaceID,
		SystemKey:   pgtype.Text{String: "aurora:" + entry.ID, Valid: true},
	})
	if err != nil {
		// Nothing has been reserved yet, so there is nothing to refund.
		h.markGenerationFailed(r.Context(), workspaceID, row.ID, "system agent unavailable")
		writeError(w, http.StatusInternalServerError, "failed to create generation")
		return
	}

	created, err := h.IssueService.Create(r.Context(), service.IssueCreateParams{
		WorkspaceID:   workspaceID,
		Title:         aurora.IssueTitle(entry.Name, req.Prompt),
		Description:   pgtype.Text{String: req.Prompt, Valid: true},
		Status:        "todo",
		Priority:      "none",
		AssigneeType:  pgtype.Text{String: "agent", Valid: true},
		AssigneeID:    agent.ID,
		CreatorType:   "member",
		CreatorID:     userUUID,
		AttachmentIDs: attachmentIDs,
		// Aurora titles are "skill：prompt", so the same prompt submitted twice
		// deliberately produces the same title. The duplicate guard is for
		// human-authored issues and must not block a second generation.
		AllowDuplicate: true,
	}, service.IssueCreateOpts{SkipAssignedAgentEnqueue: true})
	if err != nil {
		slog.Error("failed to create aurora issue", "workspace_id", uuidToString(workspaceID), "error", err)
		h.markGenerationFailed(r.Context(), workspaceID, row.ID, "issue create failed")
		writeError(w, http.StatusInternalServerError, "failed to create generation")
		return
	}
	issue := created.Issue

	// Reserve before enqueue: a short wallet must never leave behind a queued
	// task for work no one was charged for.
	if err := h.Credit.Reserve(r.Context(), userUUID, workspaceID, amountMicro, reference); err != nil {
		if errors.Is(err, aurora.ErrInsufficientCredits) {
			h.abandonAuroraIssue(r.Context(), issue)
			h.markGenerationFailed(r.Context(), workspaceID, row.ID, "insufficient credits")
			writeError(w, http.StatusPaymentRequired, "insufficient credits")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to reserve credits")
		return
	}

	attachmentIDStrings := make([]string, 0, len(attachmentIDs))
	for _, id := range attachmentIDs {
		attachmentIDStrings = append(attachmentIDStrings, uuidToString(id))
	}
	task, err := h.TaskService.EnqueueAuroraIssueTask(r.Context(), issue, reference, req.Prompt, attachmentIDStrings)
	if err != nil {
		h.abandonAuroraIssue(r.Context(), issue)
		h.failGenerationAndRefund(r.Context(), userUUID, workspaceID, row.ID, amountMicro, "enqueue failed")
		writeError(w, http.StatusInternalServerError, "failed to enqueue generation")
		return
	}

	// The generation row has no task id until this write, so cancelling the
	// task on failure resolves no generation and cannot refund twice.
	updated, err := h.Queries.UpdateAuroraGenerationBound(r.Context(), db.UpdateAuroraGenerationBoundParams{
		ID:              row.ID,
		TaskID:          task.ID,
		CreditsReserved: amountMicro,
		IssueID:         issue.ID,
		WorkspaceID:     workspaceID,
	})
	if err != nil {
		if _, cancelErr := h.TaskService.CancelTaskWithReason(r.Context(), task.ID, "failed to bind generation", "aurora_bind_failed"); cancelErr != nil {
			slog.Warn("aurora generation task cancel failed", "task_id", uuidToString(task.ID), "error", cancelErr)
		}
		h.abandonAuroraIssue(r.Context(), issue)
		h.failGenerationAndRefund(r.Context(), userUUID, workspaceID, row.ID, amountMicro, "failed to record task")
		writeError(w, http.StatusInternalServerError, "failed to record generation task")
		return
	}

	resp := AuroraGenerationResponse{
		ID:              uuidToString(updated.ID),
		SkillID:         updated.SkillID,
		Prompt:          updated.Prompt,
		Status:          updated.Status,
		CreditsReserved: updated.CreditsReserved,
	}
	if prefix := h.getIssuePrefix(r.Context(), issue.WorkspaceID); prefix != "" {
		resp.IssueIdentifier = service.IssueIdentifier(prefix, issue.Number)
	}
	writeJSON(w, http.StatusCreated, map[string]any{"generation": resp})
}

// auroraInputError marks an attachment rejection the caller caused. The
// handler answers it with 400; a storage or query failure stays a 500.
type auroraInputError struct{ message string }

func (e auroraInputError) Error() string { return e.message }

func invalidAuroraInput(format string, args ...any) error {
	return auroraInputError{message: fmt.Sprintf(format, args...)}
}

// parseAuroraAttachmentIDs enforces the request-shape rules — at most four ids,
// no duplicates, every id a UUID — before any row is read.
func parseAuroraAttachmentIDs(w http.ResponseWriter, rawIDs []string) ([]pgtype.UUID, bool) {
	if len(rawIDs) > maxAuroraAttachments {
		writeError(w, http.StatusBadRequest, "at most four attachmentIds are allowed")
		return nil, false
	}
	ids := make([]pgtype.UUID, 0, len(rawIDs))
	seen := make(map[pgtype.UUID]struct{}, len(rawIDs))
	for _, raw := range rawIDs {
		id, ok := parseUUIDOrBadRequest(w, raw, "attachmentIds")
		if !ok {
			return nil, false
		}
		if _, duplicate := seen[id]; duplicate {
			writeError(w, http.StatusBadRequest, "duplicate attachmentIds")
			return nil, false
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, true
}

// validateAuroraAttachments proves every requested id resolves to a row in this
// workspace uploaded by this user, then applies the skill's input rules. It
// reads only; it never provisions or writes.
func (h *Handler) validateAuroraAttachments(ctx context.Context, workspaceID, userID pgtype.UUID, skillID string, ids []pgtype.UUID) error {
	policy, ok := aurora.ExecutionPolicy(skillID)
	if !ok {
		return invalidAuroraInput("skill not available")
	}
	if len(ids) == 0 {
		if err := aurora.ValidateSkillInputs(policy, nil); err != nil {
			return invalidAuroraInput("%s", err)
		}
		return nil
	}

	rows, err := h.Queries.ListAttachmentsByIDs(ctx, db.ListAttachmentsByIDsParams{
		AttachmentIds: ids,
		WorkspaceID:   workspaceID,
	})
	if err != nil {
		return fmt.Errorf("load attachments: %w", err)
	}
	byID := make(map[pgtype.UUID]db.Attachment, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}
	files := make([]db.Attachment, 0, len(ids))
	for _, id := range ids {
		row, found := byID[id]
		if !found {
			return invalidAuroraInput("attachment not found")
		}
		files = append(files, row)
	}
	if err := aurora.ValidateSkillAttachmentScope(files, workspaceID, userID); err != nil {
		return invalidAuroraInput("%s", err)
	}
	if err := aurora.ValidateSkillInputs(policy, files); err != nil {
		return invalidAuroraInput("%s", err)
	}
	return nil
}

// createAuroraGenerationWithEntitlement owns the atomic entitlement boundary.
// A transaction-scoped advisory lock serializes all creates for one user; the
// counters and queued-row insertion then share one transaction, so concurrent
// requests cannot both observe spare capacity and consume the same slot.
func (h *Handler) createAuroraGenerationWithEntitlement(
	ctx context.Context,
	userID, workspaceID pgtype.UUID,
	skillID, prompt string,
) (db.AuroraGeneration, error) {
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return db.AuroraGeneration{}, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx,
		"SELECT pg_advisory_xact_lock(hashtextextended($1, 0))",
		"aurora-entitlement:"+uuidToString(userID),
	); err != nil {
		return db.AuroraGeneration{}, err
	}
	qtx := h.Queries.WithTx(tx)
	limits, err := aurora.LimitsForUser(ctx, qtx, h.Tiers, userID)
	if err != nil {
		return db.AuroraGeneration{}, fmt.Errorf("load limits: %w", err)
	}
	usedThisMonth, err := qtx.CountGenerationsThisMonth(ctx, userID)
	if err != nil {
		return db.AuroraGeneration{}, fmt.Errorf("load monthly usage: %w", err)
	}
	if usedThisMonth >= int64(limits.GenerationsPerMonth) {
		return db.AuroraGeneration{}, errAuroraMonthlyGenerationLimit
	}
	activeGenerations, err := qtx.CountActiveGenerations(ctx, userID)
	if err != nil {
		return db.AuroraGeneration{}, fmt.Errorf("load active usage: %w", err)
	}
	if activeGenerations >= int64(limits.Concurrency) {
		return db.AuroraGeneration{}, errAuroraConcurrencyLimit
	}

	if limits.Tier == "free" {
		// The allowance must exist before Reserve runs. Unlike the previous
		// best-effort path, an infrastructure failure remains a 503 rather than
		// masquerading as an insufficient-credit 402.
		if err := h.ensureFreeMonthlyGrant(ctx, userID); err != nil {
			return db.AuroraGeneration{}, fmt.Errorf("%w: %v", errAuroraAllowanceUnavailable, err)
		}
	}

	row, err := qtx.CreateAuroraGeneration(ctx, db.CreateAuroraGenerationParams{
		WorkspaceID: workspaceID,
		UserID:      userID,
		SkillID:     skillID,
		Prompt:      prompt,
	})
	if err != nil {
		return db.AuroraGeneration{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return db.AuroraGeneration{}, err
	}
	return row, nil
}

// ensureFreeMonthlyGrant tops the user's natural-month allowance up to the Free
// tier. The CreditService serializes this with paid webhook/settlement grants,
// so a same-month upgrade receives only the difference and a late Free request
// cannot add credits after the paid allowance.
func (h *Handler) ensureFreeMonthlyGrant(ctx context.Context, userID pgtype.UUID) error {
	wsID, err := h.Queries.GetPersonalWorkspaceForUser(ctx, userID)
	if err != nil {
		return err
	}
	return h.Credit.EnsureMonthlyAllowance(ctx, userID, wsID, h.Tiers.FreeMonthlyMicro(), time.Now())
}

// ensureAuroraSystemAgents serialises the idempotent system-agent seed behind a
// per-workspace advisory lock. The managed-runtime lookup-then-insert inside
// aurora.EnsureSystemAgents has no ON CONFLICT arbiter (its NULL daemon_id sits
// outside migration 121's partial unique index), so this lock — not the queries
// — is what makes two concurrent first seeds produce one runtime row.
func (h *Handler) ensureAuroraSystemAgents(ctx context.Context, workspaceID, ownerID pgtype.UUID) error {
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", "aurora:"+uuidToString(workspaceID)); err != nil {
		return err
	}
	if err := aurora.EnsureSystemAgents(ctx, h.Queries.WithTx(tx), workspaceID, ownerID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ensureWorkspaceSandbox makes the workspace's sandbox accepted by the fleet.
// A nil manager is the fail-closed configuration: with no usable fleet client
// the catalog and library still work, but new generation creation is rejected
// before any reservation rather than queued without a runtime.
func (h *Handler) ensureWorkspaceSandbox(ctx context.Context, workspaceID, runtimeID pgtype.UUID) error {
	if h.SandboxManager == nil {
		return errAuroraRuntimeUnavailable
	}
	if _, err := h.SandboxManager.Ensure(ctx, workspaceID, runtimeID); err != nil {
		return fmt.Errorf("%w: %v", errAuroraRuntimeUnavailable, err)
	}
	return nil
}

// markGenerationFailed moves a generation to the terminal failed state with a
// reason and no credits charged. Used when the request aborts before any
// credits changed hands (an insufficient wallet).
func (h *Handler) markGenerationFailed(ctx context.Context, workspaceID, generationID pgtype.UUID, reason string) {
	if _, err := h.Queries.UpdateAuroraGenerationTerminal(ctx, db.UpdateAuroraGenerationTerminalParams{
		ID:             generationID,
		Status:         "failed",
		Error:          pgtype.Text{String: reason, Valid: true},
		CreditsCharged: 0,
		WorkspaceID:    workspaceID,
	}); err != nil {
		slog.Warn("aurora generation mark-failed error", "generation_id", uuidToString(generationID), "error", err)
	}
}

// recordModerationBlock appends one rejection to the moderation audit trail.
//
// The write is best-effort: the content is already rejected by the time it
// runs, and failing the request afterwards would not produce the missing row.
// A warning is logged instead, because a log that silently stops recording is
// worse than one that says so.
//
// generationID is the zero UUID when a prompt was rejected before its
// generation row existed. The row is then keyed by workspace alone, which is
// the most specific identity available at that point.
func (h *Handler) recordModerationBlock(ctx context.Context, generationID, workspaceID pgtype.UUID, scope, reason string) {
	if _, err := h.Queries.CreateAuroraModerationLog(ctx, db.CreateAuroraModerationLogParams{
		GenerationID: generationID,
		WorkspaceID:  workspaceID,
		Scope:        scope,
		Verdict:      aurora.ModerationVerdictBlocked,
		Reason:       reason,
	}); err != nil {
		slog.Warn("aurora moderation log write failed",
			"scope", scope, "workspace_id", uuidToString(workspaceID), "error", err)
	}
}

// failGenerationAndRefund rolls a post-reservation failure back: refund the
// reserved micro-credits (idempotent via the generation-id reference) and mark
// the generation failed. Both writes are best-effort — the request path is
// already returning an error — but a failed refund is logged so a customer is
// never silently left charged for work that never started.
func (h *Handler) failGenerationAndRefund(ctx context.Context, userID, workspaceID, generationID pgtype.UUID, amountMicro int64, reason string) {
	// A generation that never reached the reservation has nothing to give back,
	// and Refund rejects a non-positive amount rather than treating it as a
	// no-op — so skip it instead of logging a warning for a non-event.
	if amountMicro > 0 {
		if err := h.Credit.Refund(ctx, userID, workspaceID, amountMicro, uuidToString(generationID)); err != nil {
			slog.Warn("aurora generation refund failed", "generation_id", uuidToString(generationID), "error", err)
		}
	}
	h.markGenerationFailed(ctx, workspaceID, generationID, reason)
}

// abandonAuroraIssue cancels the issue Aurora created when the generation fails
// before its round can run. The omitted assignee nargs write NULL, unassigning
// the skill's system agent so nothing is left waiting on it; the write is
// best-effort because the caller is already returning an error.
func (h *Handler) abandonAuroraIssue(ctx context.Context, issue db.Issue) {
	if !issue.ID.Valid {
		return
	}
	if _, err := h.Queries.UpdateIssue(ctx, db.UpdateIssueParams{
		ID:     issue.ID,
		Status: pgtype.Text{String: "cancelled", Valid: true},
	}); err != nil {
		slog.Warn("aurora issue abandon failed", "issue_id", uuidToString(issue.ID), "error", err)
	}
}

// Aurora list paging bounds. The ledger and generation lists are bounded by
// what a screen can show, not by what one query can carry.
const (
	defaultAuroraListLimit = 50
	maxAuroraListLimit     = 200
)

// auroraBillingUser resolves the authenticated caller's UUID for the two
// account-scoped billing reads. These endpoints report the *caller's* wallet,
// so they deliberately do not consult X-Workspace-ID: a balance that changed
// with the selected workspace would be the wrong number in every workspace but
// one.
func auroraBillingUser(w http.ResponseWriter, r *http.Request) (pgtype.UUID, bool) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return pgtype.UUID{}, false
	}
	return parseUUIDOrBadRequest(w, userID, "user_id")
}

// GetAuroraBillingBalance returns the caller's spendable balance in
// micro-credits (1 credit = 1e6 micro; 1 USD = 1000 credit). A user who has
// never transacted has no wallet row, which reads as 0 rather than an error —
// the field is always present so the client never has to distinguish "no
// balance" from "no answer".
func (h *Handler) GetAuroraBillingBalance(w http.ResponseWriter, r *http.Request) {
	userUUID, ok := auroraBillingUser(w, r)
	if !ok {
		return
	}
	balance, err := h.Credit.Balance(r.Context(), userUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load balance")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"availableMicro": balance})
}

// AuroraBillingTransactionResponse is one ledger row as the billing screen
// consumes it. amountMicro is signed (deduction negative), balanceAfterMicro is
// the wallet after that row, and reference names the row's subject — a
// generation id for reservations and refunds, or a grant key — so the UI can
// label it.
type AuroraBillingTransactionResponse struct {
	ID                string `json:"id"`
	Kind              string `json:"kind"`
	AmountMicro       int64  `json:"amountMicro"`
	BalanceAfterMicro int64  `json:"balanceAfterMicro"`
	Reference         string `json:"reference"`
	CreatedAt         string `json:"createdAt"`
}

// ListAuroraBillingTransactions returns the caller's ledger, newest first.
// `limit` is optional and junk-tolerant: a missing, unparseable, or
// out-of-range value falls back to the default instead of erroring, so a
// client bug cannot turn a billing screen into a 400.
func (h *Handler) ListAuroraBillingTransactions(w http.ResponseWriter, r *http.Request) {
	userUUID, ok := auroraBillingUser(w, r)
	if !ok {
		return
	}
	rows, err := h.Queries.ListCreditTransactions(r.Context(), db.ListCreditTransactionsParams{
		UserID: userUUID,
		Limit:  auroraListLimit(r.URL.Query().Get("limit")),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load transactions")
		return
	}
	// Non-nil even when empty: `null` here would reach the client's zod
	// `.default([])` as a present-but-null value, and the billing list would
	// render against it.
	transactions := make([]AuroraBillingTransactionResponse, 0, len(rows))
	for _, row := range rows {
		transactions = append(transactions, AuroraBillingTransactionResponse{
			ID:                uuidToString(row.ID),
			Kind:              row.Kind,
			AmountMicro:       row.AmountMicro,
			BalanceAfterMicro: row.BalanceAfterMicro,
			Reference:         row.Reference,
			CreatedAt:         row.CreatedAt.Time.UTC().Format(time.RFC3339Nano),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"transactions": transactions})
}

// auroraListLimit reads a ?limit query param, falling back to the default for
// anything missing, unparseable, or outside 1..maxAuroraListLimit.
func auroraListLimit(raw string) int32 {
	if raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= maxAuroraListLimit {
			return int32(n)
		}
	}
	return defaultAuroraListLimit
}

// auroraListOffset reads a ?offset query param, falling back to 0 for anything
// unparseable or negative.
func auroraListOffset(raw string) int32 {
	if raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 0 {
			return int32(n)
		}
	}
	return 0
}

// AuroraGenerationSummaryResponse is the consumer-facing shape of a generation
// row in the list and detail endpoints. It extends the create response with the
// fields the progress screen reads: charged credits, an optional error, the
// creation timestamp, and the enqueued agent task the generation runs as.
type AuroraGenerationSummaryResponse struct {
	ID              string  `json:"id"`
	SkillID         string  `json:"skillId"`
	Prompt          string  `json:"prompt"`
	Status          string  `json:"status"`
	CreditsReserved int64   `json:"creditsReserved"`
	CreditsCharged  int64   `json:"creditsCharged"`
	Error           *string `json:"error"`
	CreatedAt       string  `json:"createdAt"`
	// The enqueued agent task, or "" for a generation that never enqueued one.
	// The client uses it to link to the task; the wire stays a plain string so
	// an absent value is unambiguous.
	TaskID string `json:"taskId"`
	// IssueIdentifier is the "PREFIX-N" key of the issue this generation runs
	// on. Omitted when the generation has no issue, or when the workspace
	// prefix cannot be resolved: the degraded "#N" form carries no prefix, and
	// the client reads its absence as "no issue link to offer".
	IssueIdentifier string `json:"issueIdentifier,omitempty"`
}

// AuroraAssetResponse is one content asset a generation produced. The library
// list and the generation detail endpoint emit the same shape, so one client
// schema parses either surface.
type AuroraAssetResponse struct {
	ID           string  `json:"id"`
	GenerationID string  `json:"generationId"`
	Kind         string  `json:"kind"`
	MediaURL     *string `json:"mediaUrl"`
	Format       *string `json:"format"`
	CreatedAt    string  `json:"createdAt"`
}

// AuroraGenerationDetailResponse wraps the summary with the generation's assets.
type AuroraGenerationDetailResponse struct {
	AuroraGenerationSummaryResponse
	Assets []AuroraAssetResponse `json:"assets"`
}

// assetResponse projects an asset row into its consumer shape.
func assetResponse(row db.AuroraAsset) AuroraAssetResponse {
	return AuroraAssetResponse{
		ID:           uuidToString(row.ID),
		GenerationID: uuidToString(row.GenerationID),
		Kind:         row.Kind,
		MediaURL:     textToPtr(row.MediaUrl),
		Format:       textToPtr(row.Format),
		CreatedAt:    row.CreatedAt.Time.UTC().Format(time.RFC3339Nano),
	}
}

// generationSummary projects a generation row into its consumer shape. The
// status is the row's stored value; the detail endpoint replaces it with the
// task-derived status for in-flight generations.
func generationSummary(row db.AuroraGeneration, issueIdentifier string) AuroraGenerationSummaryResponse {
	return AuroraGenerationSummaryResponse{
		ID:              uuidToString(row.ID),
		SkillID:         row.SkillID,
		Prompt:          row.Prompt,
		Status:          row.Status,
		CreditsReserved: row.CreditsReserved,
		CreditsCharged:  row.CreditsCharged,
		Error:           textToPtr(row.Error),
		CreatedAt:       row.CreatedAt.Time.UTC().Format(time.RFC3339Nano),
		TaskID:          uuidToString(row.TaskID),
		IssueIdentifier: issueIdentifier,
	}
}

// auroraIssueIdentifiers resolves the "PREFIX-N" keys of the issues the given
// generations run on, for the list and detail endpoints. It is best-effort: a
// missing issue, an unreadable workspace prefix or a failed lookup yields no
// entry, so a page still renders without a link instead of failing.
func (h *Handler) auroraIssueIdentifiers(ctx context.Context, workspaceID pgtype.UUID, issueIDs []pgtype.UUID) map[string]string {
	identifiers := make(map[string]string, len(issueIDs))
	unique := make([]pgtype.UUID, 0, len(issueIDs))
	seen := make(map[string]struct{}, len(issueIDs))
	for _, issueID := range issueIDs {
		if !issueID.Valid {
			continue
		}
		key := uuidToString(issueID)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, issueID)
	}
	if len(unique) == 0 {
		return identifiers
	}
	prefix := h.getIssuePrefix(ctx, workspaceID)
	if prefix == "" {
		return identifiers
	}
	refs, err := h.Queries.ListIssueRefsInWorkspace(ctx, db.ListIssueRefsInWorkspaceParams{
		WorkspaceID: workspaceID,
		Ids:         unique,
	})
	if err != nil {
		slog.Warn("aurora issue identifiers load failed", "workspace_id", uuidToString(workspaceID), "error", err)
		return identifiers
	}
	for _, ref := range refs {
		identifiers[uuidToString(ref.ID)] = service.IssueIdentifier(prefix, ref.Number)
	}
	return identifiers
}

// ListAuroraGenerations returns the workspace's generations, newest first.
// `limit` and `offset` are optional and junk-tolerant so a client bug cannot
// turn the progress screen into a 400. Status here is the row's stored value —
// in-flight generations stay "queued" until the execution layer writes the
// terminal state back; live progress reads the detail endpoint, which derives
// the status from the enqueued task.
func (h *Handler) ListAuroraGenerations(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace_id")
	if !ok {
		return
	}
	rows, err := h.Queries.ListAuroraGenerations(r.Context(), db.ListAuroraGenerationsParams{
		WorkspaceID: workspaceID,
		Limit:       auroraListLimit(r.URL.Query().Get("limit")),
		Offset:      auroraListOffset(r.URL.Query().Get("offset")),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list generations")
		return
	}
	issueIDs := make([]pgtype.UUID, 0, len(rows))
	for _, row := range rows {
		issueIDs = append(issueIDs, row.IssueID)
	}
	identifiers := h.auroraIssueIdentifiers(r.Context(), workspaceID, issueIDs)

	generations := make([]AuroraGenerationSummaryResponse, 0, len(rows))
	for _, row := range rows {
		identifier := ""
		if row.IssueID.Valid {
			identifier = identifiers[uuidToString(row.IssueID)]
		}
		generations = append(generations, generationSummary(row, identifier))
	}
	writeJSON(w, http.StatusOK, map[string]any{"generations": generations})
}

// GetAuroraGeneration returns one generation with its assets. A generation
// still on the queue has its status derived from the enqueued agent task so the
// progress screen reflects in-flight work without a separate status sync
// pipeline — the terminal state is written back to the row by the execution
// layer.
func (h *Handler) GetAuroraGeneration(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace_id")
	if !ok {
		return
	}
	generationID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "generation id")
	if !ok {
		return
	}

	row, err := h.Queries.GetAuroraGeneration(r.Context(), db.GetAuroraGenerationParams{
		ID:          generationID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "generation not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load generation")
		return
	}

	issueIdentifier := ""
	if row.IssueID.Valid {
		issueIdentifier = h.auroraIssueIdentifiers(r.Context(), workspaceID, []pgtype.UUID{row.IssueID})[uuidToString(row.IssueID)]
	}
	summary := generationSummary(row, issueIdentifier)
	summary.Status = h.deriveGenerationStatus(r.Context(), row)

	assets, err := h.Queries.ListAuroraAssets(r.Context(), db.ListAuroraAssetsParams{
		GenerationID: generationID,
		WorkspaceID:  workspaceID,
		Limit:        maxAuroraListLimit,
		Offset:       0,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load assets")
		return
	}
	assetResponses := make([]AuroraAssetResponse, 0, len(assets))
	for _, a := range assets {
		assetResponses = append(assetResponses, assetResponse(a))
	}

	writeJSON(w, http.StatusOK, map[string]any{"generation": AuroraGenerationDetailResponse{
		AuroraGenerationSummaryResponse: summary,
		Assets:                          assetResponses,
	}})
}

// deriveGenerationStatus returns the generation's effective status. Terminal
// states are stored on the row; a queued row's status is derived from its
// enqueued agent task so the progress screen can show in-flight work. A missing
// task row or a transient read failure leaves the stored status untouched —
// derivation is best-effort and read-side only.
func (h *Handler) deriveGenerationStatus(ctx context.Context, row db.AuroraGeneration) string {
	if row.Status != "queued" || !row.TaskID.Valid {
		return row.Status
	}
	task, err := h.Queries.GetAgentTaskStatus(ctx, row.TaskID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("aurora generation status derive failed", "generation_id", uuidToString(row.ID), "error", err)
		}
		return row.Status
	}
	switch task.Status {
	case "completed":
		return "completed"
	case "failed", "cancelled":
		return "failed"
	case "queued":
		return "queued"
	default: // dispatched, running, waiting_local_directory, deferred
		return "running"
	}
}

// ---------------------------------------------------------------------------
// Asset library — list, download, delete (spec §9.1 #6)
// ---------------------------------------------------------------------------

// ListAuroraAssets returns the workspace's content assets, newest first,
// optionally narrowed to one generation. `limit` and `offset` are junk-tolerant
// like the generation list, but a malformed `generationId` is a 400: a bad page
// size still shows the right list, a bad filter would show the wrong one.
func (h *Handler) ListAuroraAssets(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace_id")
	if !ok {
		return
	}
	// Absent generationId leaves the filter unset, so the list spans the whole
	// workspace library; the workspace id is always bound, which is what keeps
	// another workspace's generation from widening the result.
	generationID := pgtype.UUID{}
	if raw := r.URL.Query().Get("generationId"); raw != "" {
		generationID, ok = parseUUIDOrBadRequest(w, raw, "generationId")
		if !ok {
			return
		}
	}

	rows, err := h.Queries.ListAuroraAssets(r.Context(), db.ListAuroraAssetsParams{
		GenerationID: generationID,
		WorkspaceID:  workspaceID,
		Limit:        auroraListLimit(r.URL.Query().Get("limit")),
		Offset:       auroraListOffset(r.URL.Query().Get("offset")),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list assets")
		return
	}
	// Non-nil even when empty, so the client's `.default([])` never sees a
	// present-but-null list.
	assets := make([]AuroraAssetResponse, 0, len(rows))
	for _, row := range rows {
		assets = append(assets, assetResponse(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{"assets": assets})
}

// loadAuroraAssetInWorkspace resolves one asset by path id inside the caller's
// workspace. Workspace membership is validated by the route's middleware; the
// query re-scopes by workspace_id so another workspace's asset id resolves to
// the same 404 a nonexistent one does.
func (h *Handler) loadAuroraAssetInWorkspace(w http.ResponseWriter, r *http.Request) (db.AuroraAsset, bool) {
	workspaceID, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace_id")
	if !ok {
		return db.AuroraAsset{}, false
	}
	assetID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "asset id")
	if !ok {
		return db.AuroraAsset{}, false
	}
	asset, err := h.Queries.GetAuroraAsset(r.Context(), db.GetAuroraAssetParams{
		ID:          assetID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusInternalServerError, "failed to load asset")
			return db.AuroraAsset{}, false
		}
		writeError(w, http.StatusNotFound, "asset not found")
		return db.AuroraAsset{}, false
	}
	return asset, true
}

// auroraAssetObjectURL returns the URL of the object backing one asset, or ""
// when the row has none. media_url is nullable, and an empty one is no more
// usable than a missing one, so both read as "nothing to serve".
func auroraAssetObjectURL(asset db.AuroraAsset) string {
	if !asset.MediaUrl.Valid {
		return ""
	}
	return asset.MediaUrl.String
}

// DownloadAuroraAsset hands back one asset's file. It follows the attachment
// download contract: a CloudFront or presign deployment gets a short-lived
// signed URL to follow, and a deployment with no signable URL (local disk,
// private object host) has the object streamed through the API instead. The
// asset is resolved inside the caller's workspace first, so another
// workspace's asset id is a 404 rather than a redirect.
func (h *Handler) DownloadAuroraAsset(w http.ResponseWriter, r *http.Request) {
	asset, ok := h.loadAuroraAssetInWorkspace(w, r)
	if !ok {
		return
	}
	// media_url is the only pointer to the stored object. A generation
	// completes with assets that always carry one, but a row without it has
	// nothing to serve and nothing to guess at.
	mediaURL := auroraAssetObjectURL(asset)
	if mediaURL == "" {
		writeError(w, http.StatusNotFound, "asset has no file")
		return
	}
	if h.Storage == nil {
		writeFeatureDisabled(w, "storage_not_configured", "storage not configured")
		return
	}

	key := h.Storage.KeyFromURL(mediaURL)
	// Every mode names the file the same way, so it is resolved once here and
	// handed to whichever branch serves it.
	filename := auroraAssetFilename(key, asset.Format)
	disposition := storage.AttachmentContentDisposition(filename)

	switch h.resolveAttachmentDownloadMode(mediaURL) {
	case attachmentDownloadModeCloudFront:
		if h.CFSigner == nil {
			writeError(w, http.StatusInternalServerError, "cloudfront asset downloads are not configured")
			return
		}
		h.setAttachmentPreviewSecurityHeaders(w)
		http.Redirect(w, r, h.CFSigner.SignedURLWithContentDisposition(
			mediaURL,
			disposition,
			time.Now().Add(h.attachmentDownloadURLTTL()),
		), http.StatusFound)
	case attachmentDownloadModePresign:
		presigner, ok := h.Storage.(storage.DownloadPresigner)
		if !ok {
			writeError(w, http.StatusInternalServerError, "asset storage does not support presigned downloads")
			return
		}
		signedURL, err := presigner.PresignGetWithContentDisposition(
			r.Context(),
			key,
			h.attachmentDownloadURLTTL(),
			disposition,
		)
		if err != nil {
			slog.Error("failed to presign aurora asset download", "asset_id", uuidToString(asset.ID), "key", key, "error", err)
			writeError(w, http.StatusBadGateway, "failed to create download URL")
			return
		}
		h.setAttachmentPreviewSecurityHeaders(w)
		http.Redirect(w, r, signedURL, http.StatusFound)
	case attachmentDownloadModeProxy:
		h.proxyAuroraAssetDownload(w, r, asset, key, filename)
	default:
		writeError(w, http.StatusInternalServerError, "invalid asset download mode")
	}
}

// proxyAuroraAssetDownload streams an asset through the API for deployments
// with no signable storage URL — the branch DownloadAttachment falls into for
// the same reason. It is deliberately simpler than that handler's proxy path:
// aurora_asset records no size, so there is nothing to range over and a
// forward-only reader is served whole, without the Accept-Ranges/206/416
// handling the attachment stream implements. The caller resolves the download
// name; the content type is derived from the recorded format.
func (h *Handler) proxyAuroraAssetDownload(w http.ResponseWriter, r *http.Request, asset db.AuroraAsset, key, filename string) {
	reader, err := h.Storage.GetReader(r.Context(), key)
	if err != nil {
		slog.Warn("aurora asset object missing", "asset_id", uuidToString(asset.ID), "key", key, "error", err)
		writeError(w, http.StatusNotFound, "asset file not found")
		return
	}
	defer reader.Close()

	contentType := "application/octet-stream"
	if asset.Format.Valid && asset.Format.String != "" {
		if resolved := mime.TypeByExtension("." + strings.ToLower(asset.Format.String)); resolved != "" {
			contentType = resolved
		}
	}
	w.Header().Set("Content-Type", contentType)
	// The endpoint exists to save the file, so the disposition is forced even
	// for the media types a browser would otherwise render inline.
	w.Header().Set("Content-Disposition", storage.AttachmentContentDisposition(filename))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	h.setAttachmentPreviewSecurityHeaders(w)

	// A seekable backend (local disk) gets Range and resume handling from the
	// standard library; a forward-only one (object store body) streams whole.
	if seeker, ok := reader.(io.ReadSeeker); ok {
		http.ServeContent(w, r, filename, time.Time{}, seeker)
		return
	}
	if _, err := io.Copy(w, reader); err != nil {
		slog.Warn("aurora asset stream interrupted", "asset_id", uuidToString(asset.ID), "key", key, "error", err)
	}
}

// auroraAssetFilename names the file a download saves. The object key's
// basename is the best name available; the recorded format is the fallback for
// a key that carries none. path.Base reports a key with no basename as "." or
// "..", and a bare slash as "/", so those three are what "carries none" means.
func auroraAssetFilename(key string, format pgtype.Text) string {
	if base := path.Base(key); base != "." && base != ".." && base != "/" {
		return base
	}
	if format.Valid && format.String != "" {
		return "asset." + format.String
	}
	return "asset"
}

// DeleteAuroraAsset removes one asset from the workspace's library. The stored
// object goes first, then the row. There is no cascade between the two, and the
// failure orders are not symmetric: an object delete is idempotent, so a row
// that briefly outlives its object is repaired by simply retrying this request,
// while a row deleted before its object strands a file that nothing points at
// and nothing can find again. A storage failure therefore fails the request and
// leaves the row in place. Removing an asset the caller's workspace does not
// hold is a 404 either way, so the endpoint is no existence oracle for other
// workspaces' assets.
func (h *Handler) DeleteAuroraAsset(w http.ResponseWriter, r *http.Request) {
	asset, ok := h.loadAuroraAssetInWorkspace(w, r)
	if !ok {
		return
	}

	// No storage backend, or no object on the row, means nothing to reclaim;
	// the row still goes.
	if h.Storage != nil {
		if mediaURL := auroraAssetObjectURL(asset); mediaURL != "" {
			if err := h.Storage.DeleteObject(r.Context(), h.Storage.KeyFromURL(mediaURL)); err != nil {
				slog.Error("failed to delete aurora asset object", "asset_id", uuidToString(asset.ID), "error", err)
				writeError(w, http.StatusInternalServerError, "failed to delete asset file")
				return
			}
		}
	}

	// A row already removed by a concurrent delete leaves the same end state
	// this request asked for, so it is not an error.
	if _, err := h.Queries.DeleteAuroraAsset(r.Context(), db.DeleteAuroraAssetParams{
		ID:          asset.ID,
		WorkspaceID: asset.WorkspaceID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete asset")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Billing — checkout, the Stripe webhook, and the subscription read
// (Plan 5 Tasks 3 and 7)
// ---------------------------------------------------------------------------

// CreateAuroraCheckout starts a subscription checkout for the caller.
//
// Fail-closed by construction: with no configured payment provider the
// endpoint answers 503 rather than letting a client believe it bought
// something, and a tier whose price id is unset is a 503 too, because the
// alternative is charging whatever price the default happens to be.
func (h *Handler) CreateAuroraCheckout(w http.ResponseWriter, r *http.Request) {
	if h.Payments == nil {
		writeError(w, http.StatusServiceUnavailable, "payments not configured")
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	userUUID, ok := parseUUIDOrBadRequest(w, userID, "user_id")
	if !ok {
		return
	}
	var req struct {
		Tier         string `json:"tier"`
		BillingCycle string `json:"billingCycle"`
		SuccessURL   string `json:"successUrl"`
		CancelURL    string `json:"cancelUrl"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// The client supplies the workspace route because it knows the slug, while the
	// server pins that route to a configured app origin before giving it to Stripe.
	if !h.validCheckoutURL(req.SuccessURL) || !h.validCheckoutURL(req.CancelURL) {
		writeError(w, http.StatusBadRequest, "successUrl and cancelUrl must use a trusted app origin")
		return
	}
	tier, ok := h.Tiers.Lookup(req.Tier)
	if !ok || tier.Tier == "free" {
		writeError(w, http.StatusBadRequest, "unknown tier")
		return
	}
	var priceID string
	switch req.BillingCycle {
	case "monthly":
		priceID = tier.StripePriceMonthly
	case "yearly":
		priceID = tier.StripePriceYearly
	default:
		// Billing period changes what Stripe charges, so an unknown value is
		// never safe to coerce to the monthly default.
		writeError(w, http.StatusBadRequest, "billingCycle must be monthly or yearly")
		return
	}
	if priceID == "" {
		writeError(w, http.StatusServiceUnavailable, "tier price not configured")
		return
	}
	checkoutID, err := h.beginAuroraSubscriptionCheckout(
		r.Context(), userUUID, tier.Tier, req.BillingCycle,
	)
	if err != nil {
		if errors.Is(err, errAuroraSubscriptionConflict) {
			writeError(w, http.StatusConflict, errAuroraSubscriptionConflict.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to start checkout")
		return
	}
	checkoutURL, err := h.Payments.CreateSubscriptionCheckout(r.Context(), priceID,
		req.SuccessURL, req.CancelURL, userID, tier.Tier, checkoutID)
	if err != nil {
		slog.Error("failed to create aurora subscription checkout", "tier", tier.Tier, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create checkout")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"checkoutUrl": checkoutURL})
}

// beginAuroraSubscriptionCheckout persists or reuses one pending checkout per
// user before any Stripe request leaves the process. The user advisory lock
// closes the two-tab race; Stripe's idempotency key closes retries after commit.
func (h *Handler) beginAuroraSubscriptionCheckout(
	ctx context.Context,
	userID pgtype.UUID,
	tier, billingCycle string,
) (string, error) {
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx,
		"SELECT pg_advisory_xact_lock(hashtextextended($1, 0))",
		"aurora-subscription:"+uuidToString(userID),
	); err != nil {
		return "", err
	}

	qtx := h.Queries.WithTx(tx)
	sub, err := qtx.GetAuroraSubscriptionByUser(ctx, userID)
	switch {
	case err == nil && sub.Status == "pending":
		fresh := sub.UpdatedAt.Valid && time.Since(sub.UpdatedAt.Time) < auroraCheckoutIntentTTL
		sameProduct := sub.Tier == tier && sub.CheckoutBillingCycle.Valid && sub.CheckoutBillingCycle.String == billingCycle
		if fresh && sameProduct && sub.CheckoutIdempotencyKey.Valid && sub.CheckoutIdempotencyKey.String != "" {
			if err := tx.Commit(ctx); err != nil {
				return "", err
			}
			return sub.CheckoutIdempotencyKey.String, nil
		}
		if fresh {
			return "", errAuroraSubscriptionConflict
		}
	case err == nil && sub.Status != "canceled":
		// Active, past_due, and unknown future states all fail closed. Only a
		// canceled plan or an expired pending intent may start another checkout.
		return "", errAuroraSubscriptionConflict
	case err != nil && !errors.Is(err, pgx.ErrNoRows):
		return "", err
	}

	checkoutID := "aurora-sub-" + uuid.NewString()
	if _, err := qtx.StartAuroraSubscriptionCheckout(ctx, db.StartAuroraSubscriptionCheckoutParams{
		UserID:                 userID,
		Tier:                   tier,
		CheckoutIdempotencyKey: strToText(checkoutID),
		CheckoutBillingCycle:   strToText(billingCycle),
	}); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return checkoutID, nil
}

// checkoutOrigin normalizes an absolute HTTP(S) URL to a scheme, hostname, and
// effective port. Userinfo is rejected even when the hostname is trusted: it is
// misleading in a Stripe redirect and browsers render it inconsistently.
func checkoutOrigin(raw string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Opaque != "" || parsed.User != nil {
		return "", false
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "https" && scheme != "http" {
		return "", false
	}
	hostname := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if hostname == "" {
		return "", false
	}
	port := parsed.Port()
	if port == "" {
		if scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return scheme + "://" + net.JoinHostPort(hostname, port), true
}

// validCheckoutURL accepts paths only on browser origins explicitly trusted by
// this deployment. Stripe follows these values after payment, so accepting an
// arbitrary absolute URL would turn Checkout into an open redirect.
func (h *Handler) validCheckoutURL(raw string) bool {
	origin, ok := checkoutOrigin(raw)
	if !ok {
		return false
	}
	if appOrigin, valid := checkoutOrigin(h.cfg.AppURL); valid && origin == appOrigin {
		return true
	}
	for _, candidate := range h.cfg.CheckoutReturnOrigins {
		allowed, valid := checkoutOrigin(candidate)
		if valid && origin == allowed {
			return true
		}
	}
	return false
}

// CreateAuroraTopupCheckout starts a one-time credit purchase. Same
// fail-closed shape as the subscription checkout.
func (h *Handler) CreateAuroraTopupCheckout(w http.ResponseWriter, r *http.Request) {
	if h.Payments == nil {
		writeError(w, http.StatusServiceUnavailable, "payments not configured")
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req struct {
		TopupID    string `json:"topupId"`
		SuccessURL string `json:"successUrl"`
		CancelURL  string `json:"cancelUrl"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !h.validCheckoutURL(req.SuccessURL) || !h.validCheckoutURL(req.CancelURL) {
		writeError(w, http.StatusBadRequest, "successUrl and cancelUrl must use a trusted app origin")
		return
	}
	topup, ok := h.Tiers.LookupTopup(req.TopupID)
	if !ok {
		writeError(w, http.StatusBadRequest, "unknown topup")
		return
	}
	if topup.PriceID == "" {
		writeError(w, http.StatusServiceUnavailable, "topup price not configured")
		return
	}
	checkoutURL, err := h.Payments.CreateTopupCheckout(r.Context(), topup.PriceID,
		req.SuccessURL, req.CancelURL, userID, topup.ID)
	if err != nil {
		slog.Error("failed to create aurora topup checkout", "topup_id", topup.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create checkout")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"checkoutUrl": checkoutURL})
}

// StripeWebhook receives Stripe's billing events. It is mounted on the public
// route group — Stripe cannot hold a session cookie — so the signature is the
// only thing authenticating the request, and it is verified before the body is
// read as anything but bytes.
func (h *Handler) StripeWebhook(w http.ResponseWriter, r *http.Request) {
	if h.Payments == nil {
		writeError(w, http.StatusServiceUnavailable, "payments not configured")
		return
	}
	// Match the existing public Stripe ingress: reject abusive senders before
	// reading or hashing a body, and reject requests that cannot be Stripe at all.
	if h.WebhookIPRateLimiter != nil {
		if ip := h.clientIPForRateLimit(r); ip != "" && !h.WebhookIPRateLimiter.Allow(r.Context(), ip) {
			writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
	}
	if len(r.Header.Values(stripeSignatureHeader)) == 0 {
		writeError(w, http.StatusUnauthorized, "missing Stripe-Signature header")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxStripeWebhookBodySize)
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body is too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	event, err := h.Payments.ConstructEvent(payload, r.Header.Get(stripeSignatureHeader))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid signature")
		return
	}
	if err := h.handleStripeEvent(r.Context(), event); err != nil {
		// A failure here is retryable by Stripe (a transient database error,
		// say), so it must not be acked — the ledger's idempotency keys make
		// the retry safe.
		slog.Error("stripe webhook handling failed", "event_id", event.ID, "type", event.Type, "error", err)
		writeError(w, http.StatusInternalServerError, "webhook failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"received": true})
}

// handleStripeEvent routes a verified Stripe event to the right ledger write.
// Every write is idempotent: subscriptions upsert by user_id, grants key on the
// month (subscriptions) or the Checkout Session id (topups).
//
// Bad metadata is logged and acked (a nil error): retrying cannot fix a
// permanently-malformed event, and a non-2xx would make Stripe redeliver it
// forever. The metadata userId is request-boundary input — it arrives from a
// third party — so it is parsed with util.ParseUUID, not the panicking
// parseUUID the handler's own trusted round-trips use.
func (h *Handler) handleStripeEvent(ctx context.Context, ev aurora.Event) error {
	switch ev.Type {
	case string(stripe.EventTypeCheckoutSessionCompleted),
		string(stripe.EventTypeCheckoutSessionAsyncPaymentSucceeded):
		var session stripe.CheckoutSession
		if err := json.Unmarshal(ev.Raw, &session); err != nil {
			return err
		}
		userUUID, err := util.ParseUUID(session.Metadata["userId"])
		if err != nil {
			slog.Error("stripe event with invalid userId metadata", "event_id", ev.ID)
			return nil // ack: retrying cannot fix bad metadata
		}
		// checkout.session.completed can precede payment for delayed methods.
		// Fulfil only a paid/no-payment-required session; the later async success
		// event enters this same path and uses the session id for idempotency.
		if !checkoutSessionPaid(&session) {
			return nil
		}
		if session.Mode == stripe.CheckoutSessionModePayment {
			return h.handleTopupCompleted(ctx, ev, &session, userUUID)
		}
		return h.handleSubscriptionCheckoutCompleted(ctx, ev, &session, userUUID)
	case string(stripe.EventTypeCustomerSubscriptionUpdated):
		return h.handleSubscriptionUpsert(ctx, ev)
	case string(stripe.EventTypeCustomerSubscriptionDeleted):
		return h.handleSubscriptionDeleted(ctx, ev)
	default:
		// Unknown event types are ignored: Stripe sends far more than this
		// product consumes, and a 4xx would make it retry them forever.
		return nil
	}
}

func checkoutSessionPaid(session *stripe.CheckoutSession) bool {
	if session == nil {
		return false
	}
	return session.PaymentStatus == stripe.CheckoutSessionPaymentStatusPaid ||
		session.PaymentStatus == stripe.CheckoutSessionPaymentStatusNoPaymentRequired
}

// handleTopupCompleted credits a one-time purchase, keyed by the Stripe
// Checkout Session id. completed and async_payment_succeeded are separate event
// ids for the same delayed purchase, so the event id cannot be the grant key.
// Topups never expire and stay out of the monthly expiry sum.
func (h *Handler) handleTopupCompleted(ctx context.Context, ev aurora.Event, session *stripe.CheckoutSession, userUUID pgtype.UUID) error {
	if session.ID == "" {
		return errors.New("stripe topup event without a checkout session id")
	}
	topup, ok := h.Tiers.LookupTopup(session.Metadata["topupId"])
	if !ok {
		return fmt.Errorf("stripe topup event %s has unknown topupId %q", ev.ID, session.Metadata["topupId"])
	}
	wsID, err := h.Queries.GetPersonalWorkspaceForUser(ctx, userUUID)
	if err != nil {
		return err
	}
	return h.Credit.Grant(ctx, userUUID, wsID, topup.Credits*microCreditsPerCredit, aurora.LedgerKindTopup, session.ID)
}

// handleSubscriptionCheckoutCompleted reconciles the paid Checkout Session with
// its durable pending intent, reads Stripe's canonical subscription state, and
// grants the allowance only when that state is active. Legacy sessions created
// before checkout intents existed are accepted when they carry no checkoutId.
func (h *Handler) handleSubscriptionCheckoutCompleted(ctx context.Context, ev aurora.Event, session *stripe.CheckoutSession, userUUID pgtype.UUID) error {
	tier, ok := h.Tiers.Lookup(session.Metadata["tier"])
	if !ok || tier.Tier == "free" {
		return fmt.Errorf("stripe subscription event %s has unknown tier %q", ev.ID, session.Metadata["tier"])
	}
	if session.Subscription == nil || session.Subscription.ID == "" {
		return errors.New("stripe subscription event without a subscription")
	}

	checkoutID := session.Metadata["checkoutId"]
	if checkoutID != "" {
		existing, err := h.Queries.GetAuroraSubscriptionByUser(ctx, userUUID)
		if err != nil {
			return fmt.Errorf("load subscription checkout intent: %w", err)
		}
		pendingMatches := existing.Status == "pending" &&
			existing.Tier == tier.Tier &&
			existing.CheckoutIdempotencyKey.Valid &&
			existing.CheckoutIdempotencyKey.String == checkoutID
		sameSubscription := existing.StripeSubscriptionID.Valid &&
			existing.StripeSubscriptionID.String == session.Subscription.ID
		if !pendingMatches && !sameSubscription {
			return fmt.Errorf("stripe checkout %s does not match the pending subscription", checkoutID)
		}
		if sameSubscription && existing.StripeEventCreatedAt > ev.Created {
			return nil // a newer lifecycle event already settled this subscription
		}
	}

	state, err := h.Payments.GetSubscriptionState(ctx, session.Subscription.ID)
	if err != nil {
		return err
	}
	customerID := stripeCustomerID(session)
	if state.CustomerID != "" {
		customerID = &state.CustomerID
	}
	status := stripeSubscriptionStatus(stripe.SubscriptionStatus(state.Status))
	applied, err := h.upsertAuroraSubscriptionEvent(ctx, db.UpsertAuroraSubscriptionParams{
		UserID:               userUUID,
		Tier:                 tier.Tier,
		Status:               status,
		StripeCustomerID:     ptrToText(customerID),
		StripeSubscriptionID: ptrToText(&session.Subscription.ID),
		CurrentPeriodEnd:     pgtype.Timestamptz{Time: state.CurrentPeriodEnd, Valid: true},
		CancelAtPeriodEnd:    state.CancelAtPeriodEnd,
		StripeEventCreatedAt: ev.Created,
	})
	if err != nil || !applied || status != "active" {
		return err
	}
	return h.grantSubscriptionAllowance(ctx, userUUID, tier)
}

// resolveSubscriptionLifecycleOwner finds an existing Stripe subscription row,
// or attributes an early lifecycle event through metadata copied onto the
// Subscription by Checkout. The latter closes deleted/updated-before-completed
// delivery races without accepting arbitrary third-party subscription ids.
func (h *Handler) resolveSubscriptionLifecycleOwner(
	ctx context.Context,
	ev aurora.Event,
	sub *stripe.Subscription,
) (db.AuroraSubscription, bool, error) {
	existing, err := h.Queries.GetAuroraSubscriptionByStripeID(ctx, ptrToText(&sub.ID))
	if err == nil {
		return existing, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return db.AuroraSubscription{}, false, err
	}

	checkoutID := sub.Metadata["checkoutId"]
	userID, parseErr := util.ParseUUID(sub.Metadata["userId"])
	if checkoutID == "" || parseErr != nil {
		slog.Warn("stripe lifecycle event for an unknown subscription", "event_id", ev.ID, "subscription_id", sub.ID)
		return db.AuroraSubscription{}, false, nil
	}
	pending, err := h.Queries.GetAuroraSubscriptionByUser(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("stripe lifecycle event has no pending subscription", "event_id", ev.ID, "subscription_id", sub.ID)
			return db.AuroraSubscription{}, false, nil
		}
		return db.AuroraSubscription{}, false, err
	}
	if pending.Status != "pending" ||
		!pending.CheckoutIdempotencyKey.Valid ||
		pending.CheckoutIdempotencyKey.String != checkoutID ||
		pending.Tier != sub.Metadata["tier"] {
		return db.AuroraSubscription{}, false, fmt.Errorf("stripe lifecycle event does not match pending checkout %s", checkoutID)
	}
	return pending, true, nil
}

func (h *Handler) handleSubscriptionUpsert(ctx context.Context, ev aurora.Event) error {
	var sub stripe.Subscription
	if err := json.Unmarshal(ev.Raw, &sub); err != nil {
		return err
	}
	existing, ok, err := h.resolveSubscriptionLifecycleOwner(ctx, ev, &sub)
	if err != nil || !ok {
		return err
	}
	state, err := h.Payments.GetSubscriptionState(ctx, sub.ID)
	if err != nil {
		return err
	}
	periodEnd := existing.CurrentPeriodEnd
	if !state.CurrentPeriodEnd.IsZero() {
		periodEnd = pgtype.Timestamptz{Time: state.CurrentPeriodEnd, Valid: true}
	}
	customerID := stripeCustomerIDFromSubscription(&sub)
	if state.CustomerID != "" {
		customerID = &state.CustomerID
	}
	status := stripeSubscriptionStatus(stripe.SubscriptionStatus(state.Status))
	applied, err := h.upsertAuroraSubscriptionEvent(ctx, db.UpsertAuroraSubscriptionParams{
		UserID:               existing.UserID,
		Tier:                 existing.Tier,
		Status:               status,
		StripeCustomerID:     ptrToText(customerID),
		StripeSubscriptionID: ptrToText(&sub.ID),
		CurrentPeriodEnd:     periodEnd,
		CancelAtPeriodEnd:    state.CancelAtPeriodEnd,
		StripeEventCreatedAt: ev.Created,
	})
	if err != nil || !applied || status != "active" {
		return err
	}
	tier, found := h.Tiers.Lookup(existing.Tier)
	if !found || tier.Tier == "free" {
		return fmt.Errorf("unknown subscription tier %q", existing.Tier)
	}
	return h.grantSubscriptionAllowance(ctx, existing.UserID, tier)
}

// handleSubscriptionDeleted marks the subscription canceled. Already-granted
// credits stay spendable until their monthly window expires — there is no
// clawback, which is the documented MVP semantic.
func (h *Handler) handleSubscriptionDeleted(ctx context.Context, ev aurora.Event) error {
	var sub stripe.Subscription
	if err := json.Unmarshal(ev.Raw, &sub); err != nil {
		return err
	}
	existing, ok, err := h.resolveSubscriptionLifecycleOwner(ctx, ev, &sub)
	if err != nil || !ok {
		return err
	}
	_, err = h.upsertAuroraSubscriptionEvent(ctx, db.UpsertAuroraSubscriptionParams{
		UserID:               existing.UserID,
		Tier:                 existing.Tier,
		Status:               "canceled",
		StripeCustomerID:     ptrToText(stripeCustomerIDFromSubscription(&sub)),
		StripeSubscriptionID: ptrToText(&sub.ID),
		CurrentPeriodEnd:     existing.CurrentPeriodEnd,
		CancelAtPeriodEnd:    false,
		StripeEventCreatedAt: ev.Created,
	})
	return err
}

// upsertAuroraSubscriptionEvent reports false when SQL rejected a stale event.
func (h *Handler) upsertAuroraSubscriptionEvent(ctx context.Context, params db.UpsertAuroraSubscriptionParams) (bool, error) {
	_, err := h.Queries.UpsertAuroraSubscription(ctx, params)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (h *Handler) grantSubscriptionAllowance(ctx context.Context, userID pgtype.UUID, tier aurora.Tier) error {
	wsID, err := h.Queries.GetPersonalWorkspaceForUser(ctx, userID)
	if err != nil {
		return err
	}
	return h.Credit.EnsureMonthlyAllowance(ctx, userID, wsID, tier.MonthlyCreditsMicro(), time.Now())
}

// stripeSubscriptionStatus maps Stripe's lifecycle onto the three states this
// product stores. Anything that is not in good standing becomes "past_due" and
// therefore stops granting; the entitlement gate already treats a non-active
// row as the free tier, so a past-due user keeps working on free limits rather
// than losing access outright.
func stripeSubscriptionStatus(status stripe.SubscriptionStatus) string {
	switch status {
	case stripe.SubscriptionStatusActive, stripe.SubscriptionStatusTrialing:
		return "active"
	case stripe.SubscriptionStatusCanceled:
		return "canceled"
	default:
		return "past_due"
	}
}

// stripeCustomerID reads the customer id off a checkout session, which carries
// an expanded customer object rather than a bare id.
func stripeCustomerID(session *stripe.CheckoutSession) *string {
	if session == nil || session.Customer == nil || session.Customer.ID == "" {
		return nil
	}
	id := session.Customer.ID
	return &id
}

// stripeCustomerIDFromSubscription is stripeCustomerID for a subscription
// object, whose customer is a bare id string.
func stripeCustomerIDFromSubscription(sub *stripe.Subscription) *string {
	if sub == nil || sub.Customer == nil || sub.Customer.ID == "" {
		return nil
	}
	id := sub.Customer.ID
	return &id
}

// AuroraSubscriptionLimits is the caller's effective entitlement: the gates the
// server actually applies, from the tier catalog.
type AuroraSubscriptionLimits struct {
	GenerationsPerMonth int `json:"generationsPerMonth"`
	Concurrency         int `json:"concurrency"`
}

// AuroraSubscriptionUsage is how much of the month's allowance the caller has
// spent, so the plan screen can show a progress bar rather than a bare number.
type AuroraSubscriptionUsage struct {
	GenerationsUsedThisMonth int64 `json:"generationsUsedThisMonth"`
	ActiveGenerations        int64 `json:"activeGenerations"`
}

// AuroraSubscriptionResponse is the plan screen's whole state.
//
// Tier is the *effective* tier — the one Limits carries — not the tier named on
// a subscription row. The two differ for a canceled or past-due plan, and a
// response that named the stored tier beside free-tier limits would describe a
// plan the user does not have. Status is the stored subscription status, empty
// when there is no row at all, which is what tells a canceled subscriber apart
// from one who never subscribed.
type AuroraSubscriptionResponse struct {
	Tier              string                   `json:"tier"`
	Status            string                   `json:"status"`
	CurrentPeriodEnd  *string                  `json:"currentPeriodEnd"`
	CancelAtPeriodEnd bool                     `json:"cancelAtPeriodEnd"`
	Limits            AuroraSubscriptionLimits `json:"limits"`
	Usage             AuroraSubscriptionUsage  `json:"usage"`
}

// GetAuroraSubscription reports the caller's plan, its limits and this month's
// usage. Like the other billing reads it is account-scoped: the plan belongs to
// the user, not to the workspace the request is made from.
func (h *Handler) GetAuroraSubscription(w http.ResponseWriter, r *http.Request) {
	userUUID, ok := auroraBillingUser(w, r)
	if !ok {
		return
	}
	limits, err := aurora.LimitsForUser(r.Context(), h.Queries, h.Tiers, userUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load subscription")
		return
	}
	usedThisMonth, err := h.Queries.CountGenerationsThisMonth(r.Context(), userUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load usage")
		return
	}
	activeGenerations, err := h.Queries.CountActiveGenerations(r.Context(), userUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load usage")
		return
	}

	response := AuroraSubscriptionResponse{
		Tier: limits.Tier,
		Limits: AuroraSubscriptionLimits{
			GenerationsPerMonth: limits.GenerationsPerMonth,
			Concurrency:         limits.Concurrency,
		},
		Usage: AuroraSubscriptionUsage{
			GenerationsUsedThisMonth: usedThisMonth,
			ActiveGenerations:        activeGenerations,
		},
	}

	sub, err := h.Queries.GetAuroraSubscriptionByUser(r.Context(), userUUID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// No plan row: the free tier with no period. Not an error — a user who
		// has never subscribed is the common case.
	case err != nil:
		writeError(w, http.StatusInternalServerError, "failed to load subscription")
		return
	default:
		response.Status = sub.Status
		response.CancelAtPeriodEnd = sub.CancelAtPeriodEnd
		if sub.CurrentPeriodEnd.Valid {
			end := sub.CurrentPeriodEnd.Time.UTC().Format(time.RFC3339Nano)
			response.CurrentPeriodEnd = &end
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"subscription": response})
}

// AuroraTopupResponse is one purchasable credit pack as the plan screen lists
// it. The Stripe price id is deliberately absent: it is deployment
// configuration, the client only ever passes the id back, and leaking it would
// let a caller probe which Stripe mode the server is in.
type AuroraTopupResponse struct {
	ID      string `json:"id"`
	Credits int64  `json:"credits"`
}

// ListAuroraTopups returns the credit packs this deployment sells, in catalog
// order.
func (h *Handler) ListAuroraTopups(w http.ResponseWriter, r *http.Request) {
	topups := make([]AuroraTopupResponse, 0, len(h.Tiers.Topups))
	for _, topup := range h.Tiers.Topups {
		topups = append(topups, AuroraTopupResponse{ID: topup.ID, Credits: topup.Credits})
	}
	writeJSON(w, http.StatusOK, map[string]any{"topups": topups})
}
