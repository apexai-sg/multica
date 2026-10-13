"use client";

import { use } from "react";
import { HistoryList } from "@multica/views/aurora";
import { auroraRoutes } from "@/lib/routes";
import { auroraIssueHrefFor } from "@/lib/issue-link";

/** Past generations and the results they produced. */
export default function HistoryPage({
  params,
}: {
  params: Promise<{ workspaceSlug: string }>;
}) {
  const { workspaceSlug } = use(params);
  const routes = auroraRoutes(workspaceSlug);
  return (
    <HistoryList
      generationHref={(id) => routes.work(id)}
      issueHref={auroraIssueHrefFor(workspaceSlug)}
    />
  );
}
