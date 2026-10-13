"use client";

import { use } from "react";
import { GenerationDetail } from "@multica/views/aurora";
import { auroraRoutes } from "@/lib/routes";
import { auroraIssueHrefFor } from "@/lib/issue-link";

/**
 * One generation's process and results.
 *
 * The shared view takes a task route as an optional prop; this app has no task
 * page, so it is omitted and the view shows the task id in a copyable form
 * instead of a link that would 404.
 */
export default function GenerationDetailPage({
  params,
}: {
  params: Promise<{ workspaceSlug: string; generationId: string }>;
}) {
  const { workspaceSlug, generationId } = use(params);
  return (
    <GenerationDetail
      generationId={generationId}
      backHref={auroraRoutes(workspaceSlug).works()}
      issueHref={auroraIssueHrefFor(workspaceSlug)}
    />
  );
}
