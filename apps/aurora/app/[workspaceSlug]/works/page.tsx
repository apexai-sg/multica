"use client";

import { use } from "react";
import { WorksList } from "@multica/views/aurora";
import { auroraRoutes } from "@/lib/routes";
import { auroraIssueHrefFor } from "@/lib/issue-link";

/**
 * The library.
 *
 * A client route so it can hand the shared view a route builder: the view owns
 * the row markup, the app owns where a generation's detail lives.
 */
export default function WorksPage({
  params,
}: {
  params: Promise<{ workspaceSlug: string }>;
}) {
  const { workspaceSlug } = use(params);
  const routes = auroraRoutes(workspaceSlug);
  return (
    <WorksList
      generationHref={(id) => routes.work(id)}
      issueHref={auroraIssueHrefFor(workspaceSlug)}
    />
  );
}
