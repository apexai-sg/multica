/**
 * The Multica web URL for an Aurora generation's issue.
 *
 * After submit, Aurora hands the user to the existing issue detail on whichever
 * app serves it: the Multica web app named by the host's NEXT_PUBLIC_WEB_ORIGIN,
 * or Aurora itself (an empty origin yields the relative path). The host supplies
 * the origin; core never reads process.env.
 */

/**
 * The PREFIX-N shape service.IssueIdentifier produces (a workspace prefix, a
 * dash, the issue number). A workspace whose stored prefix is missing degrades
 * to #N, which the helpers below treat as "no issue link" rather than guessing
 * a URL that would 404.
 */
const ISSUE_IDENTIFIER_PATTERN = /^[A-Za-z0-9]+-[0-9]+$/;

/**
 * /{slug}/issues/{identifier} with both segments percent-encoded, or null when
 * identifier is not the PREFIX-N shape.
 */
export function auroraIssuePath(
  slug: string,
  identifier: string,
): string | null {
  if (!ISSUE_IDENTIFIER_PATTERN.test(identifier)) return null;
  return (
    "/" +
    encodeURIComponent(slug) +
    "/issues/" +
    encodeURIComponent(identifier)
  );
}

/**
 * The absolute issue URL when origin is configured, otherwise the relative path
 * above. A missing origin means the host renders the issue itself, so the two
 * cases stay one link.
 */
export function auroraIssueHref(
  slug: string,
  identifier: string,
  origin: string | undefined,
): string | null {
  const path = auroraIssuePath(slug, identifier);
  if (path === null) return null;
  const base = (origin ?? "").replace(/\/+$/, "");
  return base ? base + path : path;
}
