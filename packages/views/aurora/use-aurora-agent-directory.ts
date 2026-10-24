"use client";

import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import { agentListOptions } from "@multica/core/workspace/queries";
import { visibleAgents } from "./aurora-agents";

/**
 * The workspace's configured agents, as the directory sees them.
 *
 * The list query includes archived rows for the management surfaces, so the
 * view filters them here. Permission filtering is the backend's: the response
 * is exactly what the agents page shows this member.
 */
export function useAuroraAgentDirectory(options: { enabled?: boolean } = {}) {
  const wsId = useWorkspaceId();
  // The catalog read seeds the workspace's carriers on the server, so a
  // directory that lists agents before it answers can read an empty list for a
  // workspace that is about to have all of them. Callers that render the
  // directory pass `enabled: false` until the catalog settles.
  const query = useQuery({
    ...agentListOptions(wsId),
    enabled: options.enabled ?? true,
  });
  const agents = useMemo(() => visibleAgents(query.data ?? []), [query.data]);
  return { agents, query };
}
