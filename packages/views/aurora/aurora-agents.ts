import type { AuroraSkill } from "@multica/core/aurora";
import type { Agent, AgentSkillSummary } from "@multica/core/types";

/** The system-key prefix Aurora's carrier agents carry. */
export const AURORA_SYSTEM_KEY_PREFIX = "aurora:";

/** The execution skill id an agent carries, or null for every other agent. */
export function agentSkillId(agent: Agent): string | null {
  const systemKey = agent.system_key ?? "";
  if (!systemKey.startsWith(AURORA_SYSTEM_KEY_PREFIX)) return null;
  const id = systemKey.slice(AURORA_SYSTEM_KEY_PREFIX.length).trim();
  return id || null;
}

/** The agents a consumer surface may list. */
export function visibleAgents(agents: Agent[]): Agent[] {
  return agents.filter((agent) => !agent.archived_at);
}

/** Attached skills that are actually switched on for the agent. */
export function agentSkillChips(agent: Agent): AgentSkillSummary[] {
  return agent.skills.filter((skill) => skill.enabled !== false);
}

/** The Aurora catalog entry this agent submits through, or null. */
export function agentSubmissionSkill(
  agent: Agent,
  catalog: AuroraSkill[],
): AuroraSkill | null {
  const skillId = agentSkillId(agent);
  if (!skillId) return null;
  const entry = catalog.find((candidate) => candidate.id === skillId);
  return entry && entry.available ? entry : null;
}

/** Whether the agent matches the directory's one search box. */
export function matchesAgentQuery(agent: Agent, query: string): boolean {
  const needle = query.trim().toLowerCase();
  if (!needle) return true;
  const fields = [
    agent.name,
    agent.description,
    ...agentSkillChips(agent).flatMap((skill) => [
      skill.name,
      skill.description,
    ]),
    ...(agent.conversation_starters ?? []).flatMap((starter) => [
      starter.label,
      starter.prompt,
    ]),
  ];
  return fields.some((field) => field.toLowerCase().includes(needle));
}

/**
 * Backend order, with the agents that can submit first.
 *
 * Array.prototype.sort is stable in every runtime this repository targets, so
 * the backend's creation order survives inside each group.
 */
export function orderDirectoryAgents(
  agents: Agent[],
  catalog: AuroraSkill[],
): Agent[] {
  return [...agents].sort((a, b) => {
    const rank = (agent: Agent) =>
      agentSubmissionSkill(agent, catalog) ? 1 : 0;
    return rank(b) - rank(a);
  });
}

/** skillId -> agent name, for surfaces that hold a generation's skill id. */
export function agentNamesBySkillId(agents: Agent[]): Map<string, string> {
  const names = new Map<string, string>();
  for (const agent of agents) {
    const skillId = agentSkillId(agent);
    if (skillId && !names.has(skillId)) names.set(skillId, agent.name);
  }
  return names;
}
