/**
 * What the history page shows for a given status tab and skill filter.
 *
 * Extracted from the component for the same reason the `skills` domain keeps
 * its own `skill-list-filter.ts`: the matching rules are the part worth testing
 * exhaustively, and they do not need a DOM to be exercised.
 */

import type { AuroraGeneration } from "@multica/core/aurora";
import { generationStatusLabel } from "./labels";

/** The pseudo-status behind the "All" tab; never a generation status. */
export const AURORA_HISTORY_STATUS_ALL = "all";

/** The pseudo-skill behind the "All skills" option; never a skill id. */
export const AURORA_HISTORY_SKILL_ALL = "all";

/**
 * The status tabs the page offers.
 *
 * Only the two terminal outcomes are selectable: a queued or running
 * generation belongs to "All" — the page is a history of results, and the
 * two named tabs answer "what finished" and "what failed".
 */
export type AuroraHistoryStatusFilter = "all" | "completed" | "failed";

/**
 * Generations matching a status tab and a skill.
 *
 * The status is read through `generationStatusLabel`, the same mapper the
 * row's badge uses, so a tab and the badge it filters cannot disagree about
 * whether a generation completed or failed.
 */
export function filterAuroraHistory(
  generations: AuroraGeneration[],
  status: AuroraHistoryStatusFilter,
  skillId: string,
): AuroraGeneration[] {
  return generations.filter((generation) => {
    if (
      status !== AURORA_HISTORY_STATUS_ALL &&
      generationStatusLabel(generation.status) !== status
    ) {
      return false;
    }
    if (
      skillId !== AURORA_HISTORY_SKILL_ALL &&
      generation.skillId !== skillId
    ) {
      return false;
    }
    return true;
  });
}
