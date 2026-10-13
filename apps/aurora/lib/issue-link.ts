import { auroraIssueHref } from "@multica/core/aurora";
import { resolveWebOrigin } from "@/config/runtime-urls";

/**
 * Builds the issueHref the shared Aurora views take, bound to one workspace.
 *
 * The literal process.env.NEXT_PUBLIC_WEB_ORIGIN read is what lets Next inline
 * the origin into the client bundle; resolveWebOrigin keeps the parsing
 * (http(s) only, trailing slash stripped) in the app's runtime-urls module.
 * Unset, the core helper returns Aurora's own relative issue path and the issue
 * renders in this app.
 */
export function auroraIssueHrefFor(
  workspaceSlug: string,
): (identifier: string) => string | null {
  const origin = resolveWebOrigin({
    NEXT_PUBLIC_WEB_ORIGIN: process.env.NEXT_PUBLIC_WEB_ORIGIN,
  });
  return (identifier) => auroraIssueHref(workspaceSlug, identifier, origin);
}
