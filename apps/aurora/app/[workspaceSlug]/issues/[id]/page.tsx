"use client";

import { use } from "react";
import { IssueDetailRoute } from "@multica/views/issues/components";

/**
 * The issue a generation runs on, rendered inside Aurora.
 *
 * This is the no-origin path: NEXT_PUBLIC_WEB_ORIGIN unset means the same
 * /{workspaceSlug}/issues/{id} address serves the existing issue detail here,
 * so a hand-off to the web app and an in-app render are the same page. The
 * workspace layout above already owns auth and slug resolution, which is why
 * this renders IssueDetailRoute directly rather than wrapping DashboardLayout.
 */
export default function IssueDetailPage({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = use(params);
  return <IssueDetailRoute routeId={id} />;
}
