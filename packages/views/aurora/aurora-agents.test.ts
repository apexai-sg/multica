// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { AuroraSkill } from "@multica/core/aurora";
import type { Agent } from "@multica/core/types";
import {
  agentDirectoryMode,
  agentNamesBySkillId,
  agentSkillChips,
  agentSkillId,
  agentSubmissionSkill,
  listableAgents,
  matchesAgentQuery,
  orderDirectoryAgents,
  visibleAgents,
} from "./aurora-agents";

function agent(overrides: Partial<Agent> = {}): Agent {
  return {
    id: "agent-1",
    workspace_id: "ws-1",
    runtime_id: "rt-1",
    name: "Agent",
    description: "",
    instructions: "",
    avatar_url: null,
    runtime_mode: "local",
    runtime_config: {},
    max_concurrent_tasks: 1,
    owner_id: "user-1",
    archived_at: null,
    custom_args: [],
    visibility: "private",
    permission_mode: "private",
    invocation_targets: [],
    model: "claude",
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    status: "idle",
    skills: [],
    archived_by: null,
    ...overrides,
  };
}

function skill(overrides: Partial<AuroraSkill> = {}): AuroraSkill {
  return {
    id: "lyrics",
    name: "歌词创作",
    nameEn: "Lyrics",
    category: "content-creation",
    credits: 60,
    input: ["text", "document"],
    output: ["text"],
    featured: false,
    available: true,
    attachments: [],
    avatarUrl: "",
    ...overrides,
  };
}

describe("agentSkillId", () => {
  it("reads the skill id from an Aurora system key", () => {
    expect(agentSkillId(agent({ system_key: "aurora:lyrics" }))).toBe("lyrics");
  });

  it("rejects other system agents, empty suffixes, and ordinary agents", () => {
    expect(agentSkillId(agent({ system_key: "mika" }))).toBeNull();
    expect(agentSkillId(agent({ system_key: "aurora:" }))).toBeNull();
    expect(agentSkillId(agent({ system_key: "aurora:   " }))).toBeNull();
    expect(agentSkillId(agent())).toBeNull();
  });
});

describe("visibleAgents", () => {
  it("keeps active agents and drops archived ones", () => {
    const active = agent({ id: "a", name: "Active" });
    const archived = agent({
      id: "b",
      name: "Archived",
      archived_at: "2026-01-01T00:00:00Z",
    });
    expect(visibleAgents([archived, active]).map((item) => item.id)).toEqual([
      "a",
    ]);
  });
});

describe("agentSkillChips", () => {
  it("shows every attached skill except the ones switched off", () => {
    const chips = agentSkillChips(
      agent({
        skills: [
          { id: "s1", name: "Runbooks", description: "", enabled: true },
          { id: "s2", name: "Drafts", description: "", enabled: false },
          { id: "s3", name: "Research", description: "", enabled: true },
        ],
      }),
    );
    expect(chips.map((item) => item.name)).toEqual(["Runbooks", "Research"]);
  });

  it("treats an omitted enabled flag as enabled", () => {
    expect(
      agentSkillChips(
        agent({
          skills: [{ id: "s1", name: "Runbooks", description: "" }],
        }),
      ),
    ).toHaveLength(1);
  });
});

describe("agentSubmissionSkill", () => {
  it("returns the available catalog entry the system key names", () => {
    const entry = skill();
    expect(
      agentSubmissionSkill(agent({ system_key: "aurora:lyrics" }), [entry]),
    ).toBe(entry);
  });

  it("refuses a missing, retired, or unavailable entry", () => {
    expect(
      agentSubmissionSkill(agent({ system_key: "aurora:retired" }), [skill()]),
    ).toBeNull();
    expect(
      agentSubmissionSkill(agent({ system_key: "aurora:lyrics" }), [
        skill({ available: false }),
      ]),
    ).toBeNull();
    expect(agentSubmissionSkill(agent(), [skill()])).toBeNull();
  });
});

describe("matchesAgentQuery", () => {
  const subject = agent({
    name: "Lyrics helper",
    description: "Writes short lyrics.",
    skills: [
      { id: "s1", name: "歌词创作", description: "Song drafts", enabled: true },
    ],
    conversation_starters: [
      { label: "Try an example", prompt: "A song about courage" },
    ],
  });

  it("matches an empty query", () => {
    expect(matchesAgentQuery(subject, "   ")).toBe(true);
  });

  it.each([
    "lyrics helper",
    "SHORT LYRICS",
    "歌词创作",
    "song drafts",
    "try an example",
    "courage",
  ])("matches %s across the displayed fields", (query) => {
    expect(matchesAgentQuery(subject, query)).toBe(true);
  });

  it("does not match an unrelated query", () => {
    expect(matchesAgentQuery(subject, "payroll")).toBe(false);
  });
});

describe("orderDirectoryAgents", () => {
  it("moves submittable agents first and preserves backend order inside each group", () => {
    const first = agent({ id: "a", name: "First" });
    const second = agent({
      id: "b",
      name: "Second",
      system_key: "aurora:lyrics",
    });
    const third = agent({ id: "c", name: "Third" });
    const fourth = agent({
      id: "d",
      name: "Fourth",
      system_key: "aurora:lyrics",
    });
    expect(
      orderDirectoryAgents([first, second, third, fourth], [skill()]).map(
        (item) => item.id,
      ),
    ).toEqual(["b", "d", "a", "c"]);
  });

  it("leaves the order alone when nothing is submittable", () => {
    const agents = [agent({ id: "a" }), agent({ id: "b" })];
    expect(
      orderDirectoryAgents(agents, [skill()]).map((item) => item.id),
    ).toEqual(["a", "b"]);
  });
});

describe("agentDirectoryMode", () => {
  it("runs a current Aurora carrier as a generation", () => {
    expect(
      agentDirectoryMode(agent({ system_key: "aurora:lyrics" }), [skill()]),
    ).toBe("generation");
  });

  it("runs a user-created agent as an ordinary issue", () => {
    expect(agentDirectoryMode(agent(), [skill()])).toBe("issue");
  });

  it("hides every other system agent", () => {
    expect(agentDirectoryMode(agent({ system_key: "mika" }), [skill()])).toBeNull();
    expect(
      agentDirectoryMode(agent({ system_key: "aurora:retired" }), [skill()]),
    ).toBeNull();
    expect(
      agentDirectoryMode(agent({ system_key: "aurora:lyrics" }), [
        skill({ available: false }),
      ]),
    ).toBeNull();
  });
});

describe("listableAgents", () => {
  it("keeps generation and issue agents and drops system-generated ones", () => {
    const generation = agent({ id: "g", system_key: "aurora:lyrics" });
    const issue = agent({ id: "i" });
    const mika = agent({ id: "m", system_key: "mika" });
    const retired = agent({ id: "r", system_key: "aurora:retired" });
    expect(
      listableAgents([generation, issue, mika, retired], [skill()]).map(
        (item) => item.id,
      ),
    ).toEqual(["g", "i"]);
  });
});

describe("agentNamesBySkillId", () => {
  it("maps an Aurora skill id to the first agent that carries it", () => {
    const names = agentNamesBySkillId([
      agent({ id: "a", name: "First", system_key: "aurora:lyrics" }),
      agent({ id: "b", name: "Second", system_key: "aurora:lyrics" }),
      agent({ id: "c", name: "Other" }),
    ]);
    expect(names.get("lyrics")).toBe("First");
    expect(names.size).toBe(1);
  });
});
