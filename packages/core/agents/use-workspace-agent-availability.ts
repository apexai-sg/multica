"use client";

import { useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "../hooks";
import { useCurrentMember } from "../permissions/use-current-member";
import { agentListOptions } from "../workspace/queries";
import { filterAssignableAgents } from "./assignable";

/**
 * Three-state availability for "does the current user have any agent
 * they can chat with in this workspace?".
 *
 * Why three states (not a boolean): the answer to "is there an agent?"
 * lives on the server. Until the agent-list query resolves, the answer
 * is genuinely *unknown*. Callers must distinguish "loading" from
 * "confirmed empty" — collapsing them to a boolean causes UIs to flash
 * disabled/empty states for the first few hundred ms after mount, even
 * when the workspace actually has agents.
 *
 *   "loading"   — agent or member list still in flight (be neutral in UI)
 *   "none"      — both queries resolved, user has zero assignable agents
 *   "available" — at least one agent passes archive + visibility filters
 */
export type WorkspaceAgentAvailability = "loading" | "none" | "available";

/**
 * Mirrors the per-agent visibility/archived filter used by AssigneePicker
 * and the chat agent dropdown, so the three pickers can never disagree on
 * "is this agent reachable?".
 *
 * Members are queried for the caller's role: `canAssignAgentToIssue` denies a
 * `public_to` agent's workspace grant when the role is unknown. A `private`
 * agent is owner-only regardless of role.
 */
export function useWorkspaceAgentAvailability(): WorkspaceAgentAvailability {
  const wsId = useWorkspaceId();
  const { userId, role, isFetched: memberFetched } = useCurrentMember(wsId);
  const { data: agents, isFetched: agentsFetched } = useQuery(
    agentListOptions(wsId),
  );

  if (!agentsFetched || !memberFetched) return "loading";

  const assignable = filterAssignableAgents(agents, { userId, role });

  return assignable.length > 0 ? "available" : "none";
}
