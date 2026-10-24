import { canAssignAgentToIssue } from "../permissions";
import type { Agent, MemberRole } from "../types";

/**
 * The agents this viewer may assign work to: not archived, and admitted by
 * `canAssignAgentToIssue` — the same rule the AssigneePicker applies.
 *
 * The role matters only for a `public_to` agent carrying a workspace grant: an
 * unknown role denies it, so without member data every workspace-shared agent
 * would look unassignable. A `private` agent is owner-only whatever the role.
 */
export function filterAssignableAgents(
  agents: Agent[] | undefined,
  { userId, role }: { userId: string | null; role: MemberRole | null },
): Agent[] {
  return (agents ?? []).filter(
    (agent) =>
      !agent.archived_at && canAssignAgentToIssue(agent, { userId, role }).allowed,
  );
}
