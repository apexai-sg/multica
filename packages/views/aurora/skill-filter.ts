/**
 * What the directory shows for a given search box and category tab.
 *
 * Extracted from the component for the same reason the `skills` domain keeps
 * its own `skill-list-filter.ts`: the matching rules are the part worth testing
 * exhaustively, and they do not need a DOM to be exercised.
 */

import type { AuroraSkill } from "@multica/core/aurora";
import { toolSearchText } from "./tool-guidance";

/** The pseudo-category behind the "All" tab; never a catalog value. */
export const AURORA_CATEGORY_ALL = "all";

/**
 * Skills matching a query and a category.
 *
 * The query is matched against the catalog's Chinese and English names and
 * against tool-guidance's consumer copy. The catalog pairs a Chinese name with
 * an English one, and tool-guidance carries a consumer name and summary in both
 * languages; which of them a reader is looking at depends on their UI language,
 * not on what they know how to spell — someone reading "Poster" may still
 * search 海报, and vice versa. Matching is case-insensitive on a trimmed query
 * so a trailing space does not empty the grid.
 *
 * Availability is deliberately not a filter: an unavailable skill is the
 * "coming soon" half of the directory, and dropping it would hide the roadmap.
 */
export function filterAuroraSkills(
  skills: AuroraSkill[],
  query: string,
  category: string,
): AuroraSkill[] {
  const needle = query.trim().toLowerCase();
  return skills.filter((skill) => {
    if (category !== AURORA_CATEGORY_ALL && skill.category !== category) {
      return false;
    }
    if (!needle) return true;
    return (
      skill.name.toLowerCase().includes(needle) ||
      skill.nameEn.toLowerCase().includes(needle) ||
      toolSearchText(skill.id).toLowerCase().includes(needle)
    );
  });
}
