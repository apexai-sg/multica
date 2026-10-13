"use client";

import { use } from "react";
import { SkillDirectory } from "@multica/views/aurora";
import { auroraRoutes } from "@/lib/routes";
import { auroraIssueHrefFor } from "@/lib/issue-link";

/**
 * The skill directory.
 *
 * `topUpHref` is withheld: buying credits is Plan 5's checkout route and does
 * not exist yet. The shared views take the destination as a prop precisely so
 * that an app which has not built it offers nothing to click, rather than a
 * link that 404s.
 */
export default function SkillsPage({
  params,
}: {
  params: Promise<{ workspaceSlug: string }>;
}) {
  const { workspaceSlug } = use(params);
  return (
    <SkillDirectory
      worksHref={auroraRoutes(workspaceSlug).works()}
      issueHref={auroraIssueHrefFor(workspaceSlug)}
    />
  );
}
