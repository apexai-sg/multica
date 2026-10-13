package service

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/redact"
)

// auroraFailureReasonMaxRunes bounds the client-facing failure reason. The
// daemon's message can embed a whole provider response body, and this value is
// rendered in the generation detail view rather than read in a log browser.
const auroraFailureReasonMaxRunes = 400

// auroraFailureReason renders the reason a generation carries into its terminal
// failed state.
//
// The classified reason is a taxonomy code — "agent_error.unknown",
// "agent_error.provider_server_error" — which tells the person who just lost
// their output nothing at all. The daemon's own message is where the cause
// actually lives ("artifact validation failed: aurora: artifact manifest is
// missing: …"), so prefer it, redact it, bound it, and keep the code for a
// failure reported without one. Moderation is unaffected: its blocking path
// writes its own fixed phrase, not this.
func auroraFailureReason(errMsg, classified string) string {
	if message := strings.TrimSpace(redact.Text(errMsg)); message != "" {
		return truncateRunes(message, auroraFailureReasonMaxRunes)
	}
	if classified = strings.TrimSpace(classified); classified != "" {
		return classified
	}
	return "task failed"
}

// truncateRunes cuts on a rune boundary, unlike the byte-based truncate used for
// ASCII-only plugin-hook values: a failure message carries whatever the daemon
// and the provider wrote, Chinese included.
func truncateRunes(value string, limit int) string {
	if limit <= 0 || utf8.RuneCountInString(value) <= limit {
		return value
	}
	return string([]rune(value)[:limit]) + "…"
}

// Aurora settlement runs when an agent task that backs an Aurora generation
// reaches a terminal state. A generation is looked up by task_id (the reverse
// link written at enqueue), and only a non-terminal generation is settled:
// replaying the same terminal event is a no-op, which is what makes the
// daemon's durable complete/fail callbacks and the sweeper's failure passes
// safe to re-run.
//
// Success and failure are intentionally asymmetric:
//
//   - Success charges the amount already reserved at creation. No balance
//     moves here — the reservation deducted it — settling only records
//     credits_charged = credits_reserved and flips the status to completed.
//   - Failure refunds the reservation (idempotent via the generation-id
//     reference) and records the reason. The refund runs before the status
//     flip so a failed refund leaves the generation non-terminal and a retry
//     re-attempts it instead of being short-circuited by a terminal status.
const (
	auroraStatusCompleted = "completed"
	auroraStatusFailed    = "failed"
)

// settleableAuroraGeneration loads the generation a task backs and reports
// whether it is still eligible to settle: present (the task is an Aurora task)
// and still in flight. Only a stored status of queued settles. A lookup failure
// settles as a no-op — never a second charge or refund — so a transient DB
// error is logged, not acted on.
//
// Requiring queued is what makes a question round free: the round already moved
// the row to needs_input, so its terminal task settles to nothing and the
// reservation stays held. It also keeps the load-bearing failure path intact: a
// run that failed without an outcome never moved the row, stays queued, and is
// judged failed and refunded as before.
func (s *TaskService) settleableAuroraGeneration(ctx context.Context, taskID pgtype.UUID) (db.AuroraGeneration, bool) {
	gen, err := s.Queries.GetAuroraGenerationByTaskID(ctx, taskID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("aurora settlement: load generation failed",
				"task_id", util.UUIDToString(taskID), "error", err)
		}
		return db.AuroraGeneration{}, false
	}
	if gen.Status != "queued" {
		return db.AuroraGeneration{}, false
	}
	return gen, true
}

// settleAuroraOnCompleted settles a generation whose backing task completed.
// It is a best-effort, idempotent post-commit side effect: the credits were
// already deducted at reservation, so a failure here leaves the wallet correct
// and only the row status stale (re-armed by a daemon replay).
//
// The artifact is the deliverable, so a completion callback that arrives with
// no committed asset is failed and refunded rather than charged. The daemon
// already orders report-before-complete (Plan C Task 7); this is the server's
// independent guard against a buggy or malicious reporter, and it keeps the
// "successful provider process with no artifact fails" rule true even if that
// ordering is bypassed.
func (s *TaskService) settleAuroraOnCompleted(ctx context.Context, task db.AgentTaskQueue) {
	gen, ok := s.settleableAuroraGeneration(ctx, task.ID)
	if !ok {
		return
	}
	assets, err := s.Queries.ListAuroraAssets(ctx, db.ListAuroraAssetsParams{
		GenerationID: gen.ID,
		WorkspaceID:  gen.WorkspaceID,
		Limit:        1,
		Offset:       0,
	})
	if err != nil {
		// A read failure must not silently charge or refund: leave the
		// generation non-terminal so a durable replay can settle it correctly.
		slog.Warn("aurora settlement: load assets failed",
			"generation_id", util.UUIDToString(gen.ID), "error", err)
		return
	}
	if len(assets) == 0 {
		s.settleAuroraOnFailed(ctx, task, "no artifact reported")
		return
	}
	if _, err := s.Queries.UpdateAuroraGenerationTerminal(ctx, db.UpdateAuroraGenerationTerminalParams{
		ID:             gen.ID,
		Status:         auroraStatusCompleted,
		Error:          pgtype.Text{},
		CreditsCharged: gen.CreditsReserved,
		WorkspaceID:    gen.WorkspaceID,
	}); err != nil {
		slog.Warn("aurora settlement: mark completed failed",
			"generation_id", util.UUIDToString(gen.ID), "error", err)
	}
}

// settleAuroraOnFailed settles a generation whose backing task failed (or was
// swept into a terminal failed state). It refunds the reserved credits and
// records the reason. The refund is attempted first so a failed refund leaves
// the generation non-terminal for a later retry.
func (s *TaskService) settleAuroraOnFailed(ctx context.Context, task db.AgentTaskQueue, reason string) {
	gen, ok := s.settleableAuroraGeneration(ctx, task.ID)
	if !ok {
		return
	}
	if s.Credit != nil && gen.CreditsReserved > 0 {
		if err := s.Credit.Refund(ctx, gen.UserID, gen.WorkspaceID, gen.CreditsReserved, util.UUIDToString(gen.ID)); err != nil {
			slog.Warn("aurora settlement: refund failed",
				"generation_id", util.UUIDToString(gen.ID), "error", err)
			return
		}
	}
	if _, err := s.Queries.UpdateAuroraGenerationTerminal(ctx, db.UpdateAuroraGenerationTerminalParams{
		ID:             gen.ID,
		Status:         auroraStatusFailed,
		Error:          pgtype.Text{String: reason, Valid: true},
		CreditsCharged: 0,
		WorkspaceID:    gen.WorkspaceID,
	}); err != nil {
		slog.Warn("aurora settlement: mark failed failed",
			"generation_id", util.UUIDToString(gen.ID), "error", err)
	}
}
