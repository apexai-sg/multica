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
export function useAuroraAgentDirectory() {
  const wsId = useWorkspaceId();
  const query = useQuery(agentListOptions(wsId));
  const agents = useMemo(() => visibleAgents(query.data ?? []), [query.data]);
  return { agents, query };
}
