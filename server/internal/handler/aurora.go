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
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
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

// auroraCheckoutIntentFresh reports whether an intent was last handed a
// checkout URL inside the TTL. The read path (a stale intent is not reported as
// pending) and the begin path (a fresh one is reused instead of replaced) both
// judge freshness here, so the two cannot drift apart.
func auroraCheckoutIntentFresh(intentUpdatedAt pgtype.Timestamptz) bool {
	return intentUpdatedAt.Valid && time.Since(intentUpdatedAt.Time) < auroraCheckoutIntentTTL
}

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
//
// The catalog itself is a constant; what a workspace owns is the avatar its
// members set on the experts' carrier agents, so this read joins the two.
func (h *Handler) ListAuroraSkills(w http.ResponseWriter, r *http.Request) {
	// Opening the directory is what "using Aurora" looks like, so it is where a
	// workspace gets every expert's carrier at once — the Agents settings and the
	// avatar join must not depend on which experts a generation happened to
	// touch. A failed seed is logged and swallowed: the catalog is a constant and
	// the carriers are its decoration plus CreateAuroraGeneration's own
	// prerequisite, so a 500 here would take down the directory for nothing.
	if workspaceID, err := util.ParseUUID(h.resolveWorkspaceID(r)); err == nil {
		if err := h.ensureAuroraCatalogSeed(r.Context(), workspaceID); err != nil {
			slog.Warn("aurora catalog seed", "workspace_id", uuidToString(workspaceID), "error", err)
		}
	}
	skills := aurora.Catalog()
	h.attachAuroraExpertAvatars(r, skills)
	writeJSON(w, http.StatusOK, map[string]any{"skills": skills})
}

// attachAuroraExpertAvatars decorates the catalog with the workspace's carrier
// avatars, in place.
//
// Each expert's carrier is an ordinary agent whose system_key is
// aurora.SystemKey(skillID), which is how a portrait the member uploaded in the
// Agents settings reaches the directory. Nothing else about the catalog is
// workspace-specific.
//
// No carrier seeded, no avatar set, an unresolved workspace, and a failed
// lookup all leave the entry untouched — that is the initials tile the
// directory already drew, so every one of them degrades to the previous
// rendering. The failure path is deliberately not fatal: the directory is the
// content and the portrait is decoration, and a 500 here would take the whole
// Create page down over a thumbnail.
func (h *Handler) attachAuroraExpertAvatars(r *http.Request, skills []aurora.SkillCatalogEntry) {
	workspaceID, err := util.ParseUUID(h.resolveWorkspaceID(r))
	if err != nil {
		return
	}
	rows, err := h.Queries.ListAuroraExpertAvatars(r.Context(), db.ListAuroraExpertAvatarsParams{
		WorkspaceID:     workspaceID,
		SystemKeyPrefix: strToText(aurora.SystemKeyPrefix + "%"),
	})
	if err != nil {
		slog.Warn("list aurora expert avatars", "error", err)
		return
	}
	avatars := make(map[string]string, len(rows))
	for _, row := range rows {
		// The query already narrowed to live carriers of this prefix, so the only
		// skip left is a carrier with no portrait — the common case.
		if row.AvatarUrl.String == "" {
			continue
		}
		skillID, ok := aurora.SkillIDFromSystemKey(row.SystemKey.String)
		if !ok {
			continue
		}
		avatars[skillID] = h.resolveAvatarURL(row.AvatarUrl.String)
	}
	for i := range skills {
		if avatar, ok := avatars[skills[i].ID]; ok {
			skills[i].AvatarURL = avatar
		}
	}
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

	// Provision order matters: seed the requested expert and the managed
	// runtime, then make the workspace sandbox accepted by the fleet, and only
	// then run the entitlement check and insert the generation row. Only the
	// requested expert is seeded: the vendored library is ~7 MiB per workspace,
	// and a workspace should pay for the expert it actually runs.
	// The fleet call is deliberately outside every database transaction, and a
	// node may be created for a request entitlement later rejects; the idle
	// reaper removes it. That trade-off avoids reserving credits for
	// infrastructure that could not start.
	if err := h.ensureAuroraExpertSeed(r.Context(), workspaceID, entry.ID); err != nil {
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
		SystemKey:   pgtype.Text{String: aurora.SystemKey(entry.ID), Valid: true},
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
			// The 402 names the shortfall so the client can show what a top-up
			// has to cover. Reading the wallet can fail independently of the
			// refusal; the wallet state is then unknown, so fail loudly rather
			// than answer with a shortfall derived from nothing.
			balance, balanceErr := h.Credit.Balance(r.Context(), userUUID)
			if balanceErr != nil {
				slog.Error("failed to load aurora balance after insufficient credits", "user_id", userID, "error", balanceErr)
				writeError(w, http.StatusInternalServerError, "failed to load balance")
				return
			}
			requiredCredits := float64(amountMicro) / microCreditsPerCredit
			availableCredits := float64(balance) / microCreditsPerCredit
			// Count the refusal here, not at the Reserve error: only this branch
			// actually answers 402, and the alert is on the 402 rate.
			h.Metrics.RecordAuroraCreditShortfall()
			writeJSON(w, http.StatusPaymentRequired, map[string]any{
				"error":            "insufficient credits",
				"requiredCredits":  requiredCredits,
				"availableCredits": availableCredits,
				"shortfallCredits": max(0, requiredCredits-availableCredits),
			})
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

// loadAuroraAttachments resolves attachment ids to rows in the order the caller
// listed them. A missing or foreign id is invalid input, never a silently
// dropped attachment. It reads only; policy checks stay with each caller.
func (h *Handler) loadAuroraAttachments(ctx context.Context, workspaceID pgtype.UUID, ids []pgtype.UUID) ([]db.Attachment, error) {
	rows, err := h.Queries.ListAttachmentsByIDs(ctx, db.ListAttachmentsByIDsParams{
		AttachmentIds: ids,
		WorkspaceID:   workspaceID,
	})
	if err != nil {
		return nil, fmt.Errorf("load attachments: %w", err)
	}
	byID := make(map[pgtype.UUID]db.Attachment, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}
	files := make([]db.Attachment, 0, len(ids))
	for _, id := range ids {
		row, found := byID[id]
		if !found {
			return nil, invalidAuroraInput("attachment not found")
		}
		files = append(files, row)
	}
	return files, nil
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

	files, err := h.loadAuroraAttachments(ctx, workspaceID, ids)
	if err != nil {
		return err
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

// ensureAuroraCatalogSeed gives a workspace the carriers it has never had: the
// experts whose aurora:<slug> key is absent from the workspace entirely. It
// writes nothing once every catalog expert has a key.
//
// It is a key-set diff, not a row count, and archived rows count as present.
// The difference matters when a member archives a carrier: the generation path
// calls AdoptAuroraSystemAgent, which clears archived_at to recover a stale
// runtime binding, and that is right there — but a directory load must not undo
// the member's archive, so a present-but-archived key is not missing.
//
// The diff is the guard rather than a "seeded" flag: a workspace seeded by a
// build with a smaller catalog is missing the newer slugs and converges here,
// and a complete workspace pays one indexed key query per directory load. The
// common case therefore takes no transaction and no lock.
//
// The seed itself runs under the per-workspace advisory lock generation
// creation uses, and re-diffs inside it: the managed-runtime lookup-then-insert
// below has no ON CONFLICT arbiter (see ensureAuroraExpertSeed), so two
// concurrent first entries would otherwise mint two managed runtimes.
func (h *Handler) ensureAuroraCatalogSeed(ctx context.Context, workspaceID pgtype.UUID) error {
	missing, err := auroraMissingCatalogExperts(ctx, h.Queries, workspaceID)
	if err != nil || len(missing) == 0 {
		return err
	}
	return h.withAuroraSeedLock(ctx, workspaceID, func(ctx context.Context, q *db.Queries) error {
		// The request that held the lock before this one may have just seeded
		// these experts, which the pre-check above ran too early to see.
		missing, err := auroraMissingCatalogExperts(ctx, q, workspaceID)
		if err != nil || len(missing) == 0 {
			return err
		}
		ownerID, err := q.GetWorkspaceOwnerUserID(ctx, workspaceID)
		if err != nil {
			return fmt.Errorf("get workspace owner: %w", err)
		}
		for _, skillID := range missing {
			if err := aurora.EnsureAuroraExpert(ctx, q, workspaceID, ownerID, skillID); err != nil {
				return fmt.Errorf("seed aurora expert %q: %w", skillID, err)
			}
		}
		return nil
	})
}

// withAuroraSeedLock runs fn inside the per-workspace seed transaction, holding
// the advisory lock that serialises concurrent first seeds. The lock lives here
// so a new seed entry point cannot forget it.
//
// It does not resolve the carrier's owner: each caller does that inside the
// closure, because only the caller knows which experts it is about to seed. The
// owner must be the workspace owner — runtimeVerdict blocks a private runtime
// whose owner differs from the agent's.
//
// The lock is not decorum: the managed-runtime lookup-then-insert in
// aurora.EnsureAuroraExpert has no ON CONFLICT arbiter (its NULL daemon_id sits
// outside migration 121's partial unique index), so this lock — not the queries
// — is what makes two concurrent first seeds produce one runtime row.
func (h *Handler) withAuroraSeedLock(
	ctx context.Context,
	workspaceID pgtype.UUID,
	fn func(context.Context, *db.Queries) error,
) error {
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", "aurora:"+uuidToString(workspaceID)); err != nil {
		return err
	}
	if err := fn(ctx, h.Queries.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// auroraMissingCatalogExperts returns the catalog skills whose carrier key the
// workspace does not have at all. An archived carrier still has its key, so it
// is not missing; that is what keeps a directory load from resurrecting an
// expert the member archived.
func auroraMissingCatalogExperts(ctx context.Context, q *db.Queries, workspaceID pgtype.UUID) ([]string, error) {
	keys, err := q.ListAuroraSystemAgentKeys(ctx, db.ListAuroraSystemAgentKeysParams{
		WorkspaceID:     workspaceID,
		SystemKeyPrefix: strToText(aurora.SystemKeyPrefix + "%"),
	})
	if err != nil {
		return nil, fmt.Errorf("list aurora carrier keys: %w", err)
	}
	present := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if key.Valid {
			present[key.String] = struct{}{}
		}
	}
	missing := make([]string, 0, len(aurora.Catalog()))
	for _, entry := range aurora.Catalog() {
		if _, ok := present[aurora.SystemKey(entry.ID)]; !ok {
			missing = append(missing, entry.ID)
		}
	}
	return missing, nil
}

// ensureAuroraExpertSeed seeds one expert behind the shared seed lock. It
// resolves the owner itself rather than taking the request's actor: this path
// runs for whoever creates the generation, and the carrier and its managed
// runtime must belong to the workspace owner for any member to invoke them
// afterwards. Task 10's tool-run endpoint seeds through here for the same
// reason.
func (h *Handler) ensureAuroraExpertSeed(ctx context.Context, workspaceID pgtype.UUID, skillID string) error {
	return h.withAuroraSeedLock(ctx, workspaceID, func(ctx context.Context, q *db.Queries) error {
		ownerID, err := q.GetWorkspaceOwnerUserID(ctx, workspaceID)
		if err != nil {
			return fmt.Errorf("get workspace owner: %w", err)
		}
		return aurora.EnsureAuroraExpert(ctx, q, workspaceID, ownerID, skillID)
	})
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
	mediaURL := auroraAssetObjectURL(asset)
	if h.Storage != nil && mediaURL != "" {
		if err := h.Storage.DeleteObject(r.Context(), h.Storage.KeyFromURL(mediaURL)); err != nil {
			slog.Error("failed to delete aurora asset object", "asset_id", uuidToString(asset.ID), "error", err)
			writeError(w, http.StatusInternalServerError, "failed to delete asset file")
			return
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

	// The reply that showed this file must stop claiming to have it: drop the
	// reference rows, bump each affected comment's revision and rebroadcast it,
	// so no client keeps a broken thumbnail. An empty media_url matches
	// nothing, and a row with no comment bound to it has nobody to tell.
	if mediaURL != "" {
		removed, err := h.Queries.DeleteAttachmentsByURL(r.Context(), db.DeleteAttachmentsByURLParams{
			Url: mediaURL, WorkspaceID: asset.WorkspaceID,
		})
		if err != nil {
			slog.Error("failed to unlink deleted asset from comments", "asset_id", uuidToString(asset.ID), "error", err)
		} else {
			seen := make(map[string]struct{}, len(removed))
			for _, commentUUID := range removed {
				if !commentUUID.Valid {
					continue
				}
				commentID := uuidToString(commentUUID)
				if _, ok := seen[commentID]; ok {
					continue
				}
				seen[commentID] = struct{}{}
				if _, err := h.Queries.BumpCommentRevision(r.Context(), db.BumpCommentRevisionParams{
					ID: commentUUID, WorkspaceID: asset.WorkspaceID,
				}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
					slog.Warn("failed to bump comment revision after asset delete", "comment_id", commentID, "error", err)
				}
				h.publishCommentWithAttachments(r.Context(), asset.WorkspaceID, commentUUID, "system", "")
			}
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Billing — checkout, the Stripe webhook, and the subscription read
// (Plan 5 Tasks 3 and 7)
// ---------------------------------------------------------------------------

// The payment channels a subscription checkout can start on. "stripe" also
// answers a request that names no channel, which is what every client predating
// Antom sends.
const (
	paymentProviderStripe = "stripe"
	paymentProviderAntom  = "antom"
)

// Antom notification endpoints, appended to the public API origin. The router
// serves both; the checkout hands them to Antom so a later payment or state
// change can be matched back to the row.
const (
	antomNotifyPathPayment      = "/api/aurora/billing/antom/notify/payment"
	antomNotifyPathSubscription = "/api/aurora/billing/antom/notify/subscription"
)

// CreateAuroraCheckout starts a subscription checkout for the caller on the
// provider the request names (Stripe when it names none).
//
// Fail-closed by construction: with a channel unconfigured the endpoint answers
// 503 rather than letting a client believe it bought something, and a Stripe
// tier whose price id is unset is a 503 too, because the alternative is
// charging whatever price the default happens to be. Both checks live inside
// their own branch, after the provider dispatch: a deployment that configures
// only Antom must still be able to sell through it.
func (h *Handler) CreateAuroraCheckout(w http.ResponseWriter, r *http.Request) {
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
		Provider     string `json:"provider"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	provider := req.Provider
	if provider == "" {
		provider = paymentProviderStripe
	}
	if provider != paymentProviderStripe && provider != paymentProviderAntom {
		// Guessing would charge the user through a channel they did not pick.
		writeError(w, http.StatusBadRequest, "unknown provider")
		return
	}
	// Channel availability is a deployment fact, so it is answered before the
	// request body is judged. Each check names its own channel: an Antom-only
	// deployment must not be refused by the Stripe precondition, and vice versa.
	switch provider {
	case paymentProviderStripe:
		if h.Payments == nil {
			writeError(w, http.StatusServiceUnavailable, "payments not configured")
			return
		}
	case paymentProviderAntom:
		if h.Antom == nil {
			writeError(w, http.StatusServiceUnavailable, "antom not configured")
			return
		}
	}
	// The client supplies the workspace route because it knows the slug, while the
	// server pins that route to a configured app origin before handing it to the
	// payment channel. Antom ignores cancelUrl: closing its page sends no
	// callback, so the row simply stays pending and the return page polls.
	if !h.validCheckoutURL(req.SuccessURL) || !h.validCheckoutURL(req.CancelURL) {
		writeError(w, http.StatusBadRequest, "successUrl and cancelUrl must use a trusted app origin")
		return
	}
	tier, ok := h.Tiers.Lookup(req.Tier)
	if !ok || tier.Tier == "free" {
		writeError(w, http.StatusBadRequest, "unknown tier")
		return
	}
	// Channel price and period preconditions run before the intent is persisted:
	// a checkout that cannot be created must not leave a pending row behind.
	var priceID string
	switch provider {
	case paymentProviderStripe:
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
	case paymentProviderAntom:
		if req.BillingCycle != "monthly" {
			// The Antom contract created here is monthly-only; there is no yearly
			// period rule to fall back to.
			writeError(w, http.StatusBadRequest, "billingCycle must be monthly for this provider")
			return
		}
		if tier.AntomAmountMinorMonthly == "" {
			writeError(w, http.StatusServiceUnavailable, "tier price not configured")
			return
		}
	}
	intent, err := h.beginAuroraSubscriptionCheckout(
		r.Context(), userUUID, tier.Tier, req.BillingCycle, provider,
	)
	if err != nil {
		if errors.Is(err, errAuroraSubscriptionConflict) {
			writeError(w, http.StatusConflict, errAuroraSubscriptionConflict.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to start checkout")
		return
	}
	var checkoutURL string
	switch provider {
	case paymentProviderStripe:
		checkoutURL, err = h.Payments.CreateSubscriptionCheckout(r.Context(), priceID,
			req.SuccessURL, req.CancelURL, userID, tier.Tier, intent.key)
	case paymentProviderAntom:
		checkoutURL, err = h.Antom.CreateSubscription(r.Context(), aurora.AntomSubscriptionRequest{
			// Antom's create response carries no subscription id, so the request id
			// is the only way the later notification can find this row: it must be
			// exactly the persisted checkout key, not a similar prefix.
			RequestID:             intent.key,
			Tier:                  tier.Tier,
			Amount:                tier.AntomAmountMinorMonthly,
			Currency:              aurora.AntomCurrency,
			RedirectURL:           req.SuccessURL,
			PaymentNotifyURL:      h.antomNotifyURL(antomNotifyPathPayment),
			SubscriptionNotifyURL: h.antomNotifyURL(antomNotifyPathSubscription),
			// A zero time serialises as year 0001, which Antom would reject.
			StartTime: time.Now().UTC(),
		})
	}
	// Both outcomes below write back to the intent, and the request context is
	// already canceled in the common failure case (the user closed the tab while
	// the channel call was in flight), so those writes detach from it.
	intentCtx, cancelIntent := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancelIntent()
	if err != nil {
		slog.Error("failed to create aurora subscription checkout",
			"provider", provider, "tier", tier.Tier,
			"checkout_key", intent.key, "user_id", userID, "error", err)
		// An intent this request created and then failed to turn into a returned
		// URL cannot be completed by the caller, so it must not block the next
		// attempt. It is expired rather than deleted: a timeout cannot prove the
		// channel created nothing, and a notification for this key must still
		// find the row. A reused intent is left alone — its creator may already
		// have returned a URL for it. The update is guarded on the pending state
		// and on this request's updated_at, so an intent that was marked used in
		// the meantime is untouched.
		if intent.created {
			if _, expireErr := h.Queries.ExpireAuroraSubscriptionCheckoutIntent(
				intentCtx, db.ExpireAuroraSubscriptionCheckoutIntentParams{
					UserID:                 userUUID,
					CheckoutIdempotencyKey: strToText(intent.key),
					IntentUpdatedAt:        intent.updatedAt,
					ExpiredAt: pgtype.Timestamptz{
						Time:  time.Now().Add(-auroraCheckoutIntentTTL),
						Valid: true,
					},
				}); expireErr != nil {
				// The original failure is what the caller must see; this line is
				// the operator's only signal that a row was left behind.
				slog.Error("failed to expire aurora checkout intent",
					"provider", provider, "tier", tier.Tier,
					"checkout_key", intent.key, "user_id", userID, "error", expireErr)
			}
		}
		writeError(w, http.StatusInternalServerError, "failed to create checkout")
		return
	}
	// A returned URL is what makes this intent real, and what a failing sibling
	// must not expire. Recording it here rather than on reuse keeps a run of
	// failed attempts from refreshing an intent that never produced anything.
	if _, err := h.Queries.MarkAuroraSubscriptionCheckoutIntentUsed(
		intentCtx, db.MarkAuroraSubscriptionCheckoutIntentUsedParams{
			UserID:                 userUUID,
			CheckoutIdempotencyKey: strToText(intent.key),
		}); err != nil {
		// The URL is valid and the browser is about to use it; a stale timestamp
		// only costs the intent its protection from a later failing sibling.
		slog.Error("failed to mark aurora checkout intent used",
			"provider", provider, "tier", tier.Tier,
			"checkout_key", intent.key, "user_id", userID, "error", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"checkoutUrl": checkoutURL})
}

// antomNotifyURL builds one Antom notification endpoint on the public API
// origin. The channel is only constructed when PublicURL is configured, so this
// never has to invent an address.
func (h *Handler) antomNotifyURL(path string) string {
	return strings.TrimRight(h.cfg.PublicURL, "/") + path
}

// auroraCheckoutIntent is this request's reservation of the user's single
// subscription row. created reports whether the request made it, and updatedAt
// is the ownership token a failing request must present to expire it. The token
// only moves when a channel call returns a checkout URL, so a failing sibling
// can no longer expire a row whose URL the browser already has, and failed
// attempts cannot keep an intent alive past the checkout TTL.
type auroraCheckoutIntent struct {
	key       string
	created   bool
	updatedAt pgtype.Timestamptz
}

// beginAuroraSubscriptionCheckout persists or reuses one pending checkout per
// user before any provider request leaves the process. The user advisory lock
// closes the two-tab race; the provider's idempotency key closes retries after
// commit.
func (h *Handler) beginAuroraSubscriptionCheckout(
	ctx context.Context,
	userID pgtype.UUID,
	tier, billingCycle, provider string,
) (auroraCheckoutIntent, error) {
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return auroraCheckoutIntent{}, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx,
		"SELECT pg_advisory_xact_lock(hashtextextended($1, 0))",
		"aurora-subscription:"+uuidToString(userID),
	); err != nil {
		return auroraCheckoutIntent{}, err
	}

	qtx := h.Queries.WithTx(tx)
	sub, err := qtx.GetAuroraSubscriptionByUser(ctx, userID)
	switch {
	case err == nil && sub.Status == "pending":
		fresh := auroraCheckoutIntentFresh(sub.UpdatedAt)
		if fresh {
			// A pending intent on another channel is never replaced: the user may
			// still complete the session that channel holds, and a replaced Stripe
			// session's webhook would never match this row (Stripe retries it for
			// days). Rows created before Antom existed carry the migration's
			// 'stripe' default, so they are judged on that channel.
			if sub.PaymentProvider != provider {
				return auroraCheckoutIntent{}, errAuroraSubscriptionConflict
			}
			sameProduct := sub.Tier == tier && sub.CheckoutBillingCycle.Valid && sub.CheckoutBillingCycle.String == billingCycle
			if sameProduct && sub.CheckoutIdempotencyKey.Valid && sub.CheckoutIdempotencyKey.String != "" {
				if err := tx.Commit(ctx); err != nil {
					return auroraCheckoutIntent{}, err
				}
				return auroraCheckoutIntent{key: sub.CheckoutIdempotencyKey.String}, nil
			}
			return auroraCheckoutIntent{}, errAuroraSubscriptionConflict
		}
	case err == nil && sub.Status != "canceled":
		// Active, past_due, and unknown future states all fail closed. Only a
		// canceled plan or an expired pending intent may start another checkout.
		return auroraCheckoutIntent{}, errAuroraSubscriptionConflict
	case err != nil && !errors.Is(err, pgx.ErrNoRows):
		return auroraCheckoutIntent{}, err
	}

	checkoutID := "aurora-sub-" + uuid.NewString()
	row, err := qtx.StartAuroraSubscriptionCheckout(ctx, db.StartAuroraSubscriptionCheckoutParams{
		UserID:                 userID,
		Tier:                   tier,
		CheckoutIdempotencyKey: strToText(checkoutID),
		CheckoutBillingCycle:   strToText(billingCycle),
		PaymentProvider:        provider,
	})
	if err != nil {
		return auroraCheckoutIntent{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return auroraCheckoutIntent{}, err
	}
	return auroraCheckoutIntent{key: checkoutID, created: true, updatedAt: row.UpdatedAt}, nil
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

// CreateAuroraBillingPortal opens Stripe's Customer Portal for the caller's
// subscription, where a subscriber can cancel or change their payment method
// without contacting support.
//
// Fail-closed like checkout: no payment provider is a 503, an untrusted return
// URL is a 400, and a caller with no subscription row (or a row created before
// checkout attached a Stripe customer) is a 409 rather than a portal session
// that would show Stripe's own error page.
func (h *Handler) CreateAuroraBillingPortal(w http.ResponseWriter, r *http.Request) {
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
		ReturnURL string `json:"returnUrl"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// Like the checkout return URLs, this is pinned to a configured app origin
	// before it is handed to Stripe (and from Stripe back to the browser).
	if !h.validCheckoutURL(req.ReturnURL) {
		writeError(w, http.StatusBadRequest, "returnUrl must use a trusted app origin")
		return
	}
	sub, err := h.Queries.GetAuroraSubscriptionByUser(r.Context(), userUUID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusConflict, "no active subscription")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load subscription")
		return
	}
	// A pending checkout that never completed has no Stripe customer yet; the
	// portal has nothing to manage, so it is the same conflict as no row.
	if !sub.StripeCustomerID.Valid || sub.StripeCustomerID.String == "" {
		writeError(w, http.StatusConflict, "no active subscription")
		return
	}
	portalURL, err := h.Payments.CreateBillingPortalSession(
		r.Context(), sub.StripeCustomerID.String, req.ReturnURL,
	)
	if err != nil {
		slog.Error("failed to create aurora billing portal session", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create portal session")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"portalUrl": portalURL})
}

// StripeWebhook receives Stripe's billing events. It is mounted on the public
// route group — Stripe cannot hold a session cookie — so the signature is the
// only thing authenticating the request, and it is verified before the body is
// read as anything but bytes.
func (h *Handler) StripeWebhook(w http.ResponseWriter, r *http.Request) {
	if h.Payments == nil {
		h.Metrics.RecordAuroraWebhookEvent(obsmetrics.AuroraWebhookEventUnknown, obsmetrics.AuroraWebhookResultNotConfigured)
		writeError(w, http.StatusServiceUnavailable, "payments not configured")
		return
	}
	// Match the existing public Stripe ingress: reject abusive senders before
	// reading or hashing a body, and reject requests that cannot be Stripe at all.
	if h.WebhookIPRateLimiter != nil {
		if ip := h.clientIPForRateLimit(r); ip != "" && !h.WebhookIPRateLimiter.Allow(r.Context(), ip) {
			h.Metrics.RecordAuroraWebhookEvent(obsmetrics.AuroraWebhookEventUnknown, obsmetrics.AuroraWebhookResultRateLimited)
			writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
	}
	if len(r.Header.Values(stripeSignatureHeader)) == 0 {
		h.Metrics.RecordAuroraWebhookEvent(obsmetrics.AuroraWebhookEventUnknown, obsmetrics.AuroraWebhookResultInvalidSignature)
		writeError(w, http.StatusUnauthorized, "missing Stripe-Signature header")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxStripeWebhookBodySize)
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		// Both the size cap and an unreadable body mean the request never
		// reached signature verification.
		h.Metrics.RecordAuroraWebhookEvent(obsmetrics.AuroraWebhookEventUnknown, obsmetrics.AuroraWebhookResultBadRequest)
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
		h.Metrics.RecordAuroraWebhookEvent(obsmetrics.AuroraWebhookEventUnknown, obsmetrics.AuroraWebhookResultInvalidSignature)
		writeError(w, http.StatusBadRequest, "invalid signature")
		return
	}
	if err := h.handleStripeEvent(r.Context(), event); err != nil {
		// A failure here is retryable by Stripe (a transient database error,
		// say), so it must not be acked — the ledger's idempotency keys make
		// the retry safe. It is also the outcome ops alert on: a sustained
		// non-2xx rate means the grant is not landing.
		h.Metrics.RecordAuroraWebhookEvent(event.Type, obsmetrics.AuroraWebhookResultHandlerError)
		slog.Error("stripe webhook handling failed", "event_id", event.ID, "type", event.Type, "error", err)
		writeError(w, http.StatusInternalServerError, "webhook failed")
		return
	}
	h.Metrics.RecordAuroraWebhookEvent(event.Type, obsmetrics.AuroraWebhookResultOK)
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

// ---------------------------------------------------------------------------
// Antom notifications — the charge result and the subscription lifecycle
// ---------------------------------------------------------------------------

// maxAntomNotifyBodySize caps a notification body. Both payloads are small
// JSON documents, so the cap only bounds what a public route buffers before
// the signature is checked.
const maxAntomNotifyBodySize = 64 << 10

// antomAckBody is the acknowledgement Antom requires, byte for byte. Antom
// stops retrying on it, so it is written only once a notification has been
// applied or deliberately discarded — never for a 401 or a 500.
const antomAckBody = `{"result":{"resultCode":"SUCCESS","resultStatus":"S","resultMessage":"success"}}`

func writeAntomAck(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(antomAckBody)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(antomAckBody))
}

// serveAntomNotification runs the part both notification handlers must share:
// fail closed without a channel, rate limit, cap the body, and verify the
// signature over the exact bytes received. event is the route's metric label,
// so a failure is attributed to the path Antom actually called.
//
// The helper takes the label rather than deriving it because the failures it
// reports happen before the body is trusted, when the notification carries no
// type to attribute them to.
func (h *Handler) serveAntomNotification(w http.ResponseWriter, r *http.Request, event string) (aurora.AntomNotification, bool) {
	if h.Antom == nil {
		h.Metrics.RecordAuroraWebhookEvent(event, obsmetrics.AuroraWebhookResultNotConfigured)
		writeError(w, http.StatusServiceUnavailable, "antom not configured")
		return aurora.AntomNotification{}, false
	}
	if h.WebhookIPRateLimiter != nil {
		if ip := h.clientIPForRateLimit(r); ip != "" && !h.WebhookIPRateLimiter.Allow(r.Context(), ip) {
			h.Metrics.RecordAuroraWebhookEvent(event, obsmetrics.AuroraWebhookResultRateLimited)
			writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return aurora.AntomNotification{}, false
		}
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAntomNotifyBodySize)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		// Both the size cap and an unreadable body mean the request never
		// reached signature verification.
		h.Metrics.RecordAuroraWebhookEvent(event, obsmetrics.AuroraWebhookResultBadRequest)
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body is too large")
			return aurora.AntomNotification{}, false
		}
		writeError(w, http.StatusBadRequest, "invalid body")
		return aurora.AntomNotification{}, false
	}
	notification, err := h.Antom.VerifyNotification(r.Method, r.URL.Path, r.Header, body)
	if err != nil {
		// Logged even though Antom retries: a retry cannot fix a wrong key, so
		// this line is the only signal an operator gets.
		h.Metrics.RecordAuroraWebhookEvent(event, obsmetrics.AuroraWebhookResultInvalidSignature)
		slog.Error("antom notification failed verification", "event", event, "error", err)
		writeError(w, http.StatusUnauthorized, "invalid signature")
		return aurora.AntomNotification{}, false
	}
	return notification, true
}

// findAntomSubscription resolves the local row a verified notification belongs
// to, stopping at the first match: the checkout key (so the first charge can
// claim its pending intent), then the Antom subscription id (the fallback for
// a row whose checkout key is gone). found=false means this channel knows no
// such subscription; a caller acks that rather than retrying.
func (h *Handler) findAntomSubscription(ctx context.Context, notification aurora.AntomNotification) (db.AuroraSubscription, bool, error) {
	if notification.RequestID != "" {
		row, err := h.Queries.GetAuroraSubscriptionByCheckoutKey(ctx, strToText(notification.RequestID))
		switch {
		case err == nil:
			return row, true, nil
		case !errors.Is(err, pgx.ErrNoRows):
			return db.AuroraSubscription{}, false, err
		}
	}
	if notification.SubscriptionID == "" {
		return db.AuroraSubscription{}, false, nil
	}
	row, err := h.Queries.GetAuroraSubscriptionByAntomID(ctx, strToText(notification.SubscriptionID))
	switch {
	case err == nil:
		return row, true, nil
	case errors.Is(err, pgx.ErrNoRows):
		return db.AuroraSubscription{}, false, nil
	default:
		return db.AuroraSubscription{}, false, err
	}
}

// applyAntomSubscription reports false when the terminal-state guard declined
// the write. pgx.ErrNoRows therefore means "nothing applied" — the row is
// canceled under the same Antom subscription — not a failure worth retrying.
// The parameters carry no payment provider: the channel is stamped at checkout
// and a notification never moves a subscription to another one.
func (h *Handler) applyAntomSubscription(ctx context.Context, params db.UpsertAuroraAntomSubscriptionParams) (bool, error) {
	_, err := h.Queries.UpsertAuroraAntomSubscription(ctx, params)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// failAntomNotification reports a retryable failure: no ack, so Antom's retry
// schedule applies. Every write on these paths is idempotent, which is what
// makes a retry safe. The notification's identifiers are logged so a retried
// failure can be traced back to the subscription and charge it was about.
func (h *Handler) failAntomNotification(w http.ResponseWriter, event, message string, notification aurora.AntomNotification, err error) {
	h.Metrics.RecordAuroraWebhookEvent(event, obsmetrics.AuroraWebhookResultHandlerError)
	slog.Error(message, "event", event,
		"request_id", notification.RequestID,
		"subscription_id", notification.SubscriptionID,
		"payment_id", notification.PaymentID,
		"error", err)
	writeError(w, http.StatusInternalServerError, "notification handling failed")
}

// AntomPaymentNotification applies one charge result. A success records the
// period Antom reports, marks the subscription active and grants that month's
// allowance; any other result marks it past_due and grants nothing. Mounted on
// the public route group, so serveAntomNotification is the only thing
// authenticating the request.
func (h *Handler) AntomPaymentNotification(w http.ResponseWriter, r *http.Request) {
	event := obsmetrics.AuroraWebhookEventAntomPayment
	notification, ok := h.serveAntomNotification(w, r, event)
	if !ok {
		return
	}
	if notification.Type != "payment" {
		// A lifecycle payload posted to the charge route would read as a failed
		// charge ("ACTIVE" is not "S") and downgrade a paying user to past_due.
		// Retrying cannot reroute it, so it is logged and acked.
		h.Metrics.RecordAuroraWebhookEvent(event, obsmetrics.AuroraWebhookResultUnknown)
		slog.Warn("antom payment notification carried a non-payment payload", "type", notification.Type)
		writeAntomAck(w)
		return
	}
	ctx := r.Context()
	row, found, err := h.findAntomSubscription(ctx, notification)
	if err != nil {
		h.failAntomNotification(w, event, "antom payment notification lookup failed", notification, err)
		return
	}
	if !found {
		// Retrying cannot create a local row, so this is acked and logged.
		h.Metrics.RecordAuroraWebhookEvent(event, obsmetrics.AuroraWebhookResultUnknown)
		slog.Warn("antom payment notification for an unknown subscription",
			"request_id", notification.RequestID,
			"subscription_id", notification.SubscriptionID,
			"payment_id", notification.PaymentID)
		writeAntomAck(w)
		return
	}
	paid := notification.Status == "S"
	if paid && (notification.PeriodEnd.IsZero() || !notification.PeriodEnd.After(notification.PeriodStart)) {
		// A success without a usable period is not a success: acking it would
		// stop Antom's retries and persist a wrong next-charge date. Not acked,
		// so Antom retries a body it may fix.
		h.failAntomNotification(w, event, "antom payment notification has no usable billing period", notification,
			fmt.Errorf("period start %s, period end %s", notification.PeriodStart, notification.PeriodEnd))
		return
	}
	status := "past_due"
	periodEnd := row.CurrentPeriodEnd
	if paid {
		status = "active"
		// Antom notifications carry no ordering timestamp, so a very late
		// retry can move the period end backwards; the next charge's period
		// corrects it. Nothing better than "apply what arrived" exists here.
		periodEnd = pgtype.Timestamptz{Time: notification.PeriodEnd, Valid: true}
	}
	antomID := row.AntomSubscriptionID
	if notification.SubscriptionID != "" {
		antomID = strToText(notification.SubscriptionID)
	}
	applied, err := h.applyAntomSubscription(ctx, db.UpsertAuroraAntomSubscriptionParams{
		UserID:              row.UserID,
		Tier:                row.Tier,
		Status:              status,
		AntomSubscriptionID: antomID,
		CurrentPeriodEnd:    periodEnd,
		// A charge result never changes whether renewals are being stopped.
		CancelAtPeriodEnd: row.CancelAtPeriodEnd,
	})
	if err != nil {
		h.failAntomNotification(w, event, "antom payment notification write failed", notification, err)
		return
	}
	result := obsmetrics.AuroraWebhookResultOK
	// applied=false is the terminal-state guard: the subscription was
	// terminated under the same Antom id, so there is nothing to grant.
	if applied && paid {
		// The charge is recorded either way. A tier this deployment no longer
		// sells cannot be granted, and returning before the write would leave a
		// paid row pending with no period, which nothing repairs until the next
		// cycle; so only the grant is skipped, and the outcome stays visible as
		// "unknown" rather than being reported as ok.
		tier, knownTier := h.Tiers.Lookup(row.Tier)
		switch {
		case !knownTier || tier.Tier == "free":
			slog.Warn("antom subscription allowance skipped for an unknown tier",
				"tier", row.Tier, "subscription_id", notification.SubscriptionID)
			result = obsmetrics.AuroraWebhookResultUnknown
		default:
			if err := h.grantSubscriptionAllowance(ctx, row.UserID, tier); err != nil {
				h.failAntomNotification(w, event, "antom subscription allowance grant failed", notification, err)
				return
			}
		}
	}
	h.Metrics.RecordAuroraWebhookEvent(event, result)
	writeAntomAck(w)
}

// AntomSubscriptionNotification applies one lifecycle transition. Antom sends
// no ordering timestamp, so the states are written explicitly and any arrival
// order converges; the terminal-state guard in the upsert keeps a late CREATE
// from resurrecting a terminated subscription.
func (h *Handler) AntomSubscriptionNotification(w http.ResponseWriter, r *http.Request) {
	event := obsmetrics.AuroraWebhookEventAntomSubscription
	notification, ok := h.serveAntomNotification(w, r, event)
	if !ok {
		return
	}
	// cancelAtPeriodEnd stays nil for the status-only transitions: only CANCEL
	// sets the flag and only TERMINATE clears it, so an UPDATE cannot silently
	// drop a cancel the user already asked for. R22 trade-off: a subscription
	// reactivated without a TERMINATE keeps rendering "cancellation scheduled".
	var status string
	var cancelAtPeriodEnd *bool
	switch {
	case notification.NotificationType == "TERMINATE" || notification.Status == "TERMINATED":
		// Termination is final whichever field carries it: an explicit
		// TERMINATE is authoritative, and a body that says TERMINATED is a
		// termination however it is labelled. Anything else would let an acked
		// termination leave a canceled subscription granting the paid tier.
		status = "canceled"
		terminate := false
		cancelAtPeriodEnd = &terminate
	case notification.NotificationType == "CANCEL":
		// Antom's cancel stops renewals at the end of the paid period; access
		// continues until then, which is what cancel_at_period_end expresses.
		// It applies whether or not the status string says ACTIVE.
		status = "active"
		cancel := true
		cancelAtPeriodEnd = &cancel
	case (notification.NotificationType == "CREATE" ||
		notification.NotificationType == "UPDATE" ||
		notification.NotificationType == "CHANGE") &&
		notification.Status == "ACTIVE":
		status = "active"
	default:
		// Retrying cannot turn an unhandled type into a handled one, nor an
		// unhandled status into one this channel stores.
		h.Metrics.RecordAuroraWebhookEvent(event, obsmetrics.AuroraWebhookResultUnknown)
		slog.Warn("antom subscription notification with an unhandled type",
			"notification_type", notification.NotificationType,
			"status", notification.Status,
			"subscription_id", notification.SubscriptionID)
		writeAntomAck(w)
		return
	}
	ctx := r.Context()
	row, found, err := h.findAntomSubscription(ctx, notification)
	if err != nil {
		h.failAntomNotification(w, event, "antom subscription notification lookup failed", notification, err)
		return
	}
	if !found {
		h.Metrics.RecordAuroraWebhookEvent(event, obsmetrics.AuroraWebhookResultUnknown)
		slog.Warn("antom subscription notification for an unknown subscription",
			"notification_type", notification.NotificationType,
			"subscription_id", notification.SubscriptionID)
		writeAntomAck(w)
		return
	}
	cancel := row.CancelAtPeriodEnd
	if cancelAtPeriodEnd != nil {
		cancel = *cancelAtPeriodEnd
	}
	antomID := row.AntomSubscriptionID
	if notification.SubscriptionID != "" {
		antomID = strToText(notification.SubscriptionID)
	}
	// A lifecycle transition carries no period, so the stored period end is
	// what the last charge reported and is left alone. A declined write is the
	// guard refusing a late CREATE on a terminated subscription: the terminal
	// state stands and the notification is still acked.
	if _, err := h.applyAntomSubscription(ctx, db.UpsertAuroraAntomSubscriptionParams{
		UserID:              row.UserID,
		Tier:                row.Tier,
		Status:              status,
		AntomSubscriptionID: antomID,
		CurrentPeriodEnd:    row.CurrentPeriodEnd,
		CancelAtPeriodEnd:   cancel,
	}); err != nil {
		h.failAntomNotification(w, event, "antom subscription notification write failed", notification, err)
		return
	}
	h.Metrics.RecordAuroraWebhookEvent(event, obsmetrics.AuroraWebhookResultOK)
	writeAntomAck(w)
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
	Tier   string `json:"tier"`
	Status string `json:"status"`
	// Provider is the channel the stored row is on, empty when there is no
	// row. It decides whether the plan screen offers the in-app Antom cancel
	// or the Stripe portal.
	Provider string `json:"provider"`
	// AvailableProviders lists the channels this deployment can sell through,
	// Stripe first and Antom second. It is always an array — an empty one when
	// neither is configured — so the client never has to handle null.
	AvailableProviders []string                 `json:"availableProviders"`
	CurrentPeriodEnd   *string                  `json:"currentPeriodEnd"`
	CancelAtPeriodEnd  bool                     `json:"cancelAtPeriodEnd"`
	Limits             AuroraSubscriptionLimits `json:"limits"`
	Usage              AuroraSubscriptionUsage  `json:"usage"`
}

// availableSubscriptionProviders reports the configured subscription channels
// in a fixed order. The order is part of the response contract, not a
// preference: a client renders the buttons in list order.
func (h *Handler) availableSubscriptionProviders() []string {
	providers := make([]string, 0, 2)
	if h.Payments != nil {
		providers = append(providers, "stripe")
	}
	if h.Antom != nil {
		providers = append(providers, "antom")
	}
	return providers
}

// buildAuroraSubscriptionResponse assembles the plan screen's whole state for
// one user. The read and the cancel endpoint both answer with this shape, so
// they are built here once and cannot drift apart.
func (h *Handler) buildAuroraSubscriptionResponse(ctx context.Context, userUUID pgtype.UUID) (AuroraSubscriptionResponse, error) {
	limits, err := aurora.LimitsForUser(ctx, h.Queries, h.Tiers, userUUID)
	if err != nil {
		return AuroraSubscriptionResponse{}, fmt.Errorf("load limits: %w", err)
	}
	usedThisMonth, err := h.Queries.CountGenerationsThisMonth(ctx, userUUID)
	if err != nil {
		return AuroraSubscriptionResponse{}, fmt.Errorf("load usage: %w", err)
	}
	activeGenerations, err := h.Queries.CountActiveGenerations(ctx, userUUID)
	if err != nil {
		return AuroraSubscriptionResponse{}, fmt.Errorf("load usage: %w", err)
	}

	response := AuroraSubscriptionResponse{
		Tier:               limits.Tier,
		AvailableProviders: h.availableSubscriptionProviders(),
		Limits: AuroraSubscriptionLimits{
			GenerationsPerMonth: limits.GenerationsPerMonth,
			Concurrency:         limits.Concurrency,
		},
		Usage: AuroraSubscriptionUsage{
			GenerationsUsedThisMonth: usedThisMonth,
			ActiveGenerations:        activeGenerations,
		},
	}

	sub, err := h.Queries.GetAuroraSubscriptionByUser(ctx, userUUID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// No plan row: the free tier with no period. Not an error — a user who
		// has never subscribed is the common case.
	case err != nil:
		return AuroraSubscriptionResponse{}, fmt.Errorf("load subscription: %w", err)
	default:
		// A pending intent older than the checkout TTL is no longer an intent:
		// the next checkout may replace it, so reporting it would describe a plan
		// the user never completed. The row is kept, not deleted — a notification
		// that arrives late still finds it by its key and activates it.
		if sub.Status == "pending" && !auroraCheckoutIntentFresh(sub.UpdatedAt) {
			break
		}
		response.Status = sub.Status
		response.Provider = sub.PaymentProvider
		response.CancelAtPeriodEnd = sub.CancelAtPeriodEnd
		if sub.CurrentPeriodEnd.Valid {
			end := sub.CurrentPeriodEnd.Time.UTC().Format(time.RFC3339Nano)
			response.CurrentPeriodEnd = &end
		}
	}

	return response, nil
}

// GetAuroraSubscription reports the caller's plan, its limits and this month's
// usage. Like the other billing reads it is account-scoped: the plan belongs to
// the user, not to the workspace the request is made from.
func (h *Handler) GetAuroraSubscription(w http.ResponseWriter, r *http.Request) {
	userUUID, ok := auroraBillingUser(w, r)
	if !ok {
		return
	}
	response, err := h.buildAuroraSubscriptionResponse(r.Context(), userUUID)
	if err != nil {
		slog.Error("failed to load aurora subscription", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load subscription")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"subscription": response})
}

// CancelAuroraSubscription stops renewals for the caller's Antom subscription.
// Antom has no billing portal, so this endpoint is the whole cancellation path
// for that channel; a Stripe row is pointed at the portal instead. The channel
// and the subscription id both come from the stored row, never from the
// request, so a caller can neither cancel another account's subscription nor
// choose a channel the row is not on.
//
// Antom's CANCEL leaves the paid period active, so only cancel_at_period_end
// changes here. A TERMINATE notification is the one thing that clears the flag
// (R22); this endpoint and the CREATE/UPDATE/CHANGE notifications never do.
func (h *Handler) CancelAuroraSubscription(w http.ResponseWriter, r *http.Request) {
	userUUID, ok := auroraBillingUser(w, r)
	if !ok {
		return
	}
	sub, err := h.Queries.GetAuroraSubscriptionByUser(r.Context(), userUUID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, http.StatusNotFound, "no subscription to cancel")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "failed to load subscription")
		return
	}
	// The check comes before the update so the caller gets the specific
	// reason; the write itself is also guarded, which covers a termination
	// arriving between this read and that write.
	if sub.PaymentProvider != "antom" {
		writeError(w, http.StatusConflict, "this subscription is managed by Stripe; use the billing portal")
		return
	}
	// A row that already reached canceled has nothing left to stop, and
	// submitting it to Antom again would reset cancel_at_period_end on a
	// subscription the termination already ended.
	if sub.Status == "canceled" {
		writeError(w, http.StatusConflict, "subscription already canceled")
		return
	}
	if h.Antom == nil {
		writeError(w, http.StatusServiceUnavailable, "antom not configured")
		return
	}
	if !sub.AntomSubscriptionID.Valid || sub.AntomSubscriptionID.String == "" {
		// A pending row has no subscription at Antom yet, so there is nothing
		// to stop renewing and an empty id must not reach the provider.
		writeError(w, http.StatusConflict, "subscription has no Antom id")
		return
	}
	// The provider call comes first, and a failure changes nothing locally:
	// the user can retry, and the server never claims a cancellation Antom did
	// not accept.
	if err := h.Antom.CancelSubscription(r.Context(), sub.AntomSubscriptionID.String); err != nil {
		slog.Error("failed to cancel aurora antom subscription", "error", err)
		writeError(w, http.StatusBadGateway, "failed to cancel subscription")
		return
	}
	if _, err := h.Queries.MarkAuroraSubscriptionCancelAtPeriodEnd(r.Context(), userUUID); err != nil {
		// Antom already accepted the cancel; only the local flag is pending.
		// A retry self-heals if Antom's cancel is idempotent, so this is not
		// the "nothing happened" failure the 5xx might suggest. A declined
		// update (no row) means a termination won the race since the read
		// above: the row is already canceled, which is the same answer the
		// pre-read gives.
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusConflict, "subscription already canceled")
			return
		}
		slog.Error("failed to record aurora subscription cancellation", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to update subscription")
		return
	}
	response, err := h.buildAuroraSubscriptionResponse(r.Context(), userUUID)
	if err != nil {
		slog.Error("failed to load aurora subscription after cancel", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load subscription")
		return
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
