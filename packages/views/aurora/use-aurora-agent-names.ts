"use client";

import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import { agentListOptions } from "@multica/core/workspace/queries";
import { agentNamesBySkillId, visibleAgents } from "./aurora-agents";

/**
 * skillId -> agent name for the screens that hold a generation's skill id.
 *
 * A generation records the execution skill, not the agent that presented it.
 * Preferring the agent's backend-configured name keeps history, works and the
 * detail view consistent with the card the generation was started from.
 */
export function useAuroraAgentNames(): Map<string, string> {
  const wsId = useWorkspaceId();
  const { data } = useQuery(agentListOptions(wsId));
  return useMemo(
    () => agentNamesBySkillId(visibleAgents(data ?? [])),
    [data],
  );
}
