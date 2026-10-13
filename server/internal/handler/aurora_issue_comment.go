package handler

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/aurora"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// auroraRoundDisposition is the comment gate's verdict for one accepted comment
// on an issue that belongs to an Aurora generation.
type auroraRoundDisposition int

const (
	// auroraRoundNotAurora: the issue has no generation. The ordinary
	// comment-trigger path runs unchanged.
	auroraRoundNotAurora auroraRoundDisposition = iota
	// auroraRoundBlock: the comment stays on the issue. Nothing is registered,
	// merged, or enqueued — the live round must not swallow it and no second
	// round may start.
	auroraRoundBlock
	// auroraRoundOpen: moderation passed on a needs_input generation, so the
	// caller opens the next round on the same generation.
	auroraRoundOpen
)

// auroraRoundDecision is the comment gate's answer. generation is the resolved
// generation whenever the issue belongs to one.
type auroraRoundDecision struct {
	disposition auroraRoundDisposition
	generation  db.AuroraGeneration
}

// isAuroraIssue reports whether the issue belongs to a generation, so the
// ordinary trigger path must not run.
func (d auroraRoundDecision) isAuroraIssue() bool {
	return d.disposition != auroraRoundNotAurora
}

// opensNextRound reports whether the caller must open the next round.
func (d auroraRoundDecision) opensNextRound() bool {
	return d.disposition == auroraRoundOpen
}

// auroraIssueRoundDecision is the comment gate (spec §1.5). It runs after the
// comment's triggers are computed and before anything folds the comment into a
// live round, and it decides between exactly two dispositions for an Aurora
// issue: block the comment, or open the next round.
//
//   - No generation for the issue: not Aurora — the caller keeps today's path.
//   - The current round is still queued/running or its task is active: block.
//   - The generation is needs_input: moderate the comment; pass opens the next
//     round, reject blocks.
//   - Any terminal status: block.
//
// An unreadable generation or a failed moderator fails closed to block: a
// comment that cannot be proven safe to run must never be folded into a round.
func (h *Handler) auroraIssueRoundDecision(ctx context.Context, issue db.Issue, commentBody string) auroraRoundDecision {
	generation, err := h.Queries.GetAuroraGenerationByIssueID(ctx, db.GetAuroraGenerationByIssueIDParams{
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return auroraRoundDecision{}
		}
		slog.Error("aurora comment gate lookup failed", "issue_id", uuidToString(issue.ID), "error", err)
		return auroraRoundDecision{disposition: auroraRoundBlock}
	}

	if h.auroraRoundInProgress(ctx, generation) {
		return auroraRoundDecision{disposition: auroraRoundBlock, generation: generation}
	}
	if generation.Status != "needs_input" {
		return auroraRoundDecision{disposition: auroraRoundBlock, generation: generation}
	}

	decision, err := h.Moderation.ScreenPrompt(ctx, commentBody)
	if err != nil {
		slog.Error("aurora comment moderation failed",
			"issue_id", uuidToString(issue.ID),
			"generation_id", uuidToString(generation.ID),
			"error", err)
		h.recordModerationBlock(ctx, generation.ID, issue.WorkspaceID, aurora.ModerationScopePrompt,
			"moderation adapter error: "+err.Error())
		return auroraRoundDecision{disposition: auroraRoundBlock, generation: generation}
	}
	if !decision.Allowed {
		h.recordModerationBlock(ctx, generation.ID, issue.WorkspaceID, aurora.ModerationScopePrompt, decision.Reason)
		return auroraRoundDecision{disposition: auroraRoundBlock, generation: generation}
	}
	return auroraRoundDecision{disposition: auroraRoundOpen, generation: generation}
}

// auroraRoundInProgress reports whether the generation's current round still
// owns the issue: the stored status is queued (a round that has not ended), or
// the current task is still queued/dispatched/running/waiting. The task check
// closes the window between a needs_input artifact report and its terminal
// callback, where the generation already reads needs_input but the run is not
// done yet.
func (h *Handler) auroraRoundInProgress(ctx context.Context, generation db.AuroraGeneration) bool {
	if generation.Status == "queued" {
		return true
	}
	if !generation.TaskID.Valid {
		return false
	}
	task, err := h.Queries.GetAgentTask(ctx, generation.TaskID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// The current task row is gone, so no run owns the issue.
			return false
		}
		slog.Warn("aurora comment gate task lookup failed",
			"task_id", uuidToString(generation.TaskID), "error", err)
		// Unreadable current task: treat the round as live so a second one
		// cannot start alongside it.
		return true
	}
	switch task.Status {
	case "queued", "dispatched", "running", "waiting_local_directory", "deferred":
		return true
	}
	return false
}

// openAuroraIssueRound enqueues the continuation round and points the
// generation at it. Enqueue first, write back second: a claim reads the whole
// context out of the task row, so the short window where the generation still
// names the old task is harmless. A write-back failure leaves the new round
// running but the generation stale; it is logged rather than hidden.
func (h *Handler) openAuroraIssueRound(ctx context.Context, issue db.Issue, generation db.AuroraGeneration, triggerCommentID pgtype.UUID) {
	task, err := h.TaskService.EnqueueAuroraIssueRound(ctx, issue, generation, triggerCommentID)
	if err != nil {
		slog.Error("aurora issue round enqueue failed",
			"issue_id", uuidToString(issue.ID),
			"generation_id", uuidToString(generation.ID),
			"error", err)
		return
	}
	if _, err := h.Queries.UpdateAuroraGenerationRound(ctx, db.UpdateAuroraGenerationRoundParams{
		ID:          generation.ID,
		TaskID:      task.ID,
		WorkspaceID: issue.WorkspaceID,
	}); err != nil {
		slog.Warn("aurora issue round write-back failed",
			"issue_id", uuidToString(issue.ID),
			"generation_id", uuidToString(generation.ID),
			"task_id", uuidToString(task.ID),
			"error", err)
	}
}
