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

/**
 * One agent the directory can run, classified once.
 *
 * A current Aurora carrier runs the generation pipeline; a user-created agent
 * (empty system key) runs an ordinary issue task. Every other system agent
 * (Mika, a retired carrier) is omitted: it is system-generated and either has
 * no Aurora artifact contract or no current catalog entry.
 */
export type DirectoryEntry =
  | { mode: "generation"; agent: Agent; skill: AuroraSkill }
  | { mode: "issue"; agent: Agent };

/**
 * The directory's one classification pass, generation entries first.
 *
 * Backend order holds inside each group, so the grid never reorders agents the
 * backend returned in a deliberate order. The catalog is consulted once per
 * agent here; callers read the decision from the entry instead of re-deriving
 * it in a filter, a comparator, and again in a card.
 */
export function directoryEntries(
  agents: Agent[],
  catalog: AuroraSkill[],
): DirectoryEntry[] {
  const generation: DirectoryEntry[] = [];
  const issue: DirectoryEntry[] = [];
  for (const agent of agents) {
    const skill = agentSubmissionSkill(agent, catalog);
    if (skill) {
      generation.push({ mode: "generation", agent, skill });
    } else if (!(agent.system_key ?? "").trim()) {
      issue.push({ mode: "issue", agent });
    }
  }
  return [...generation, ...issue];
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

/** skillId -> agent name, for surfaces that hold a generation's skill id. */
export function agentNamesBySkillId(agents: Agent[]): Map<string, string> {
  const names = new Map<string, string>();
  for (const agent of agents) {
    const skillId = agentSkillId(agent);
    if (skillId && !names.has(skillId)) names.set(skillId, agent.name);
  }
  return names;
}
