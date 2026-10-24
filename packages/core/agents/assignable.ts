import { canAssignAgentToIssue } from "../permissions";
import type { Agent, MemberRole } from "../types";

/**
 * The agents this viewer may assign work to: not archived, and admitted by
 * `canAssignAgentToIssue` — the same rule the AssigneePicker applies.
 *
 * Members are read for the role because visibility of `private` agents depends
 * on it; without them every private agent would look assignable.
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
