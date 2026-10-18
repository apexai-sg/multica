# Aurora Backend-Agent Directory Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the Aurora consumer directory a projection of the workspace's backend-configured agents, using only each agent's own name, description, avatar, skills, and conversation starters, and delete the hardcoded skill-display layer.

**Architecture:** A thin TanStack Query hook reads `GET /api/agents` through the shared `agentListOptions`. Pure helpers in `aurora-agents.ts` filter archived agents, search the displayed fields, order submittable agents first, and decide which agents map to an Aurora execution skill (`system_key = aurora:<skillId>` plus an available catalog entry). The directory and composer render agent fields; the skill catalog survives only as execution metadata for those agents. Ordinary agents render read-only with no price and no submit path. History, works, and generation detail prefer the agent name and fall back to the catalog name.

**Tech Stack:** TypeScript, React, TanStack Query, Next.js (Aurora app), Vitest + Testing Library, pnpm/turbo.

**Spec:** `docs/superpowers/specs/2026-10-18-aurora-agent-directory-design.md`

## Global Constraints

- No backend, daemon, migration, billing, or execution changes. `server/` must stay untouched.
- Every displayed agent value comes from the agent row. Never invent a name, description, avatar, or example in the client.
- A read-only agent shows no price and has no submit affordance.
- Submittable means `agent.system_key` starts with `aurora:` and the suffix names an available entry in `GET /api/aurora/skills`.
- Repository text (code, comments, commits, docs) is English. UI copy is localized in all five locale bundles: `en`, `zh-Hans`, `fr`, `ja`, `ko`.
- Run commands from the repository root. This worktree is a git worktree; do not `cd` into another checkout.
- Report the commands actually run and any skipped checks.

## Before You Start

The branch must include the P1 consumer-workbench commit, because this plan deletes files that commit adds.

- [ ] **Step 1: Confirm the workbench base is present**

Run:
```bash
git merge-base --is-ancestor 7d9a5ce16 HEAD && echo "base present" || echo "base missing"
ls packages/views/aurora/tool-guidance.ts
```
Expected: `base present` and the file path printed.

- [ ] **Step 2: Merge the base commit when missing**

Run:
```bash
git merge --no-ff origin/codex/apex-consumer-workbench -m "merge: bring in the P1 consumer workbench base"
```
Expected: merge succeeds. If conflicts appear, resolve them by keeping the codex branch version of the files this plan changes (`packages/views/aurora/*`, `packages/views/locales/*/aurora.json`, `apps/aurora/*`), then `git add` and `git commit --no-edit`.

- [ ] **Step 3: Confirm a clean test baseline**

Run:
```bash
pnpm --filter @multica/views test
```
Expected: all suites pass. If the baseline is already red, record the failing suites and continue; do not fix unrelated failures.

## File Map

| File | Responsibility |
| --- | --- |
| `packages/views/aurora/aurora-agents.ts` | Pure agent-directory rules: system key, visibility, chips, submittability, search, order, name map |
| `packages/views/aurora/use-aurora-agent-directory.ts` | Reads and filters the workspace agent list |
| `packages/views/aurora/use-aurora-agent-names.ts` | `skillId -> agent name` for history/works/detail |
| `packages/views/aurora/skill-directory.tsx` | Agent grid, search, hero, read-only cards, composer trigger |
| `packages/views/aurora/generation-composer.tsx` | Agent header, description, conversation-starter examples |
| `packages/views/aurora/history-list.tsx` | Prefer agent name over catalog name |
| `packages/views/aurora/works-list.tsx` | Prefer agent name over catalog name |
| `packages/views/aurora/generation-detail.tsx` | Prefer agent name over catalog name |
| `packages/views/aurora/labels.ts` | Catalog name fallback only; remove hardcoded display helpers |
| `packages/views/aurora/aurora.css` | Neutral hover/cursor for read-only cards |
| `packages/views/locales/*/aurora.json` | Agent-oriented directory copy; remove obsolete keys |
| `packages/views/locales/parity.test.ts` | Plural-family expectations for the new copy |
| `packages/views/aurora/tool-guidance.ts` | Deleted: the hardcoded name/summary/example tables |

---

### Task 1: Pure agent-directory helpers

**Files:**
- Create: `packages/views/aurora/aurora-agents.ts`
- Create: `packages/views/aurora/aurora-agents.test.ts`

**Interfaces:**
- Produces:
  - `AURORA_SYSTEM_KEY_PREFIX: "aurora:"`
  - `agentSkillId(agent: Agent): string | null`
  - `visibleAgents(agents: Agent[]): Agent[]`
  - `agentSkillChips(agent: Agent): AgentSkillSummary[]`
  - `agentSubmissionSkill(agent: Agent, catalog: AuroraSkill[]): AuroraSkill | null`
  - `matchesAgentQuery(agent: Agent, query: string): boolean`
  - `orderDirectoryAgents(agents: Agent[], catalog: AuroraSkill[]): Agent[]`
  - `agentNamesBySkillId(agents: Agent[]): Map<string, string>`

- [ ] **Step 1: Write the failing tests**

Create `packages/views/aurora/aurora-agents.test.ts`:

```ts
// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { AuroraSkill } from "@multica/core/aurora";
import type { Agent } from "@multica/core/types";
import {
  agentNamesBySkillId,
  agentSkillChips,
  agentSkillId,
  agentSubmissionSkill,
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run:
```bash
pnpm -C packages/views exec vitest run aurora/aurora-agents.test.ts
```
Expected: FAIL — `Failed to resolve import "./aurora-agents"`.

- [ ] **Step 3: Write the implementation**

Create `packages/views/aurora/aurora-agents.ts`:

```ts
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
```

- [ ] **Step 4: Run the tests to verify they pass**

Run:
```bash
pnpm -C packages/views exec vitest run aurora/aurora-agents.test.ts
```
Expected: PASS, all suites green.

- [ ] **Step 5: Commit**

```bash
git add packages/views/aurora/aurora-agents.ts packages/views/aurora/aurora-agents.test.ts
git commit -m "feat(aurora): add backend-agent directory rules"
```

---

### Task 2: Agent-driven directory, hook, and copy

**Files:**
- Create: `packages/views/aurora/use-aurora-agent-directory.ts`
- Modify: `packages/views/aurora/skill-directory.tsx` (full replacement)
- Modify: `packages/views/aurora/aurora.css` (read-only card rules)
- Modify: `packages/views/aurora/skill-directory.test.tsx` (full replacement)
- Modify: `packages/views/locales/en/aurora.json`
- Modify: `packages/views/locales/zh-Hans/aurora.json`
- Modify: `packages/views/locales/fr/aurora.json`
- Modify: `packages/views/locales/ja/aurora.json`
- Modify: `packages/views/locales/ko/aurora.json`
- Modify: `packages/views/locales/parity.test.ts` (the plural-family expectations)

**Interfaces:**
- Consumes: Task 1 helpers.
- Produces: `useAuroraAgentDirectory(): { agents: Agent[]; query: UseQueryResult<Agent[]> }`.

- [ ] **Step 1: Write the hook**

Create `packages/views/aurora/use-aurora-agent-directory.ts`:

```ts
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
```

- [ ] **Step 2: Rewrite the directory test**

Replace `packages/views/aurora/skill-directory.test.tsx` with:

```tsx
import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { AuroraSkill } from "@multica/core/aurora";
import type { Agent } from "@multica/core/types";
import { renderWithI18n } from "../test/i18n";
import { stubNavigationAdapter } from "../test/navigation";
import { NavigationProvider } from "../navigation";

const mocks = vi.hoisted(() => ({
  directory: vi.fn(),
  skills: vi.fn(),
  create: vi.fn(),
  detail: vi.fn(),
  balance: vi.fn(),
}));

vi.mock("./use-aurora-agent-directory", () => ({
  useAuroraAgentDirectory: () => mocks.directory(),
}));

vi.mock("@multica/core/aurora", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/aurora")>();
  return {
    ...actual,
    useAuroraSkills: () => mocks.skills(),
    useCreateAuroraGeneration: () => ({
      mutateAsync: mocks.create,
      isPending: false,
    }),
    useAuroraGenerationDetail: (id: string) => mocks.detail(id),
    useAuroraBalance: () => mocks.balance(),
  };
});

vi.mock("@multica/core/workspace/avatar-url", () => ({
  resolvePublicFileUrl: (url: string | null | undefined) => url ?? null,
}));

import { SkillDirectory } from "./skill-directory";

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

const LYRICS: AuroraSkill = {
  id: "content-creation-lyrics-songwriting",
  name: "歌词创作专家",
  nameEn: "Lyrics expert",
  category: "content-creation",
  credits: 60,
  input: ["text", "document"],
  output: ["text"],
  featured: false,
  available: true,
  attachments: [],
  avatarUrl: "",
};

const OPS = agent({
  id: "a-ops",
  name: "Ops assistant",
  description: "Answers operations questions.",
  skills: [{ id: "s-ops", name: "Runbooks", description: "", enabled: true }],
});

const LYRICS_AGENT = agent({
  id: "a-lyrics",
  name: "Lyrics helper",
  description: "Writes short lyrics.",
  avatar_url: "/uploads/lyrics.png",
  system_key: "aurora:content-creation-lyrics-songwriting",
  skills: [{ id: "s-lyrics", name: "歌词创作", description: "", enabled: true }],
  conversation_starters: [
    { label: "Try an example", prompt: "A song about courage" },
  ],
});

// Archived agents are removed by `useAuroraAgentDirectory` (covered by the
// `visibleAgents` unit test), so this fixture is the hook's visible output.
const AGENTS = [OPS, LYRICS_AGENT];

function directory(
  agents: Agent[],
  query: Record<string, unknown> = {},
) {
  mocks.directory.mockReturnValue({
    agents,
    query: { isPending: false, isError: false, refetch: vi.fn(), ...query },
  });
}

function catalog(skills: AuroraSkill[] = [LYRICS]) {
  mocks.skills.mockReturnValue({
    data: { value: skills, degraded: false },
    isPending: false,
  });
}

function renderDirectory() {
  return renderWithI18n(
    <NavigationProvider value={stubNavigationAdapter()}>
      <SkillDirectory />
    </NavigationProvider>,
  );
}

function cards() {
  const grid = document.querySelector<HTMLElement>(".aurora-card-grid");
  if (!grid) return [];
  return Array.from(grid.querySelectorAll<HTMLElement>(".aurora-card"));
}

describe("SkillDirectory", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    directory(AGENTS);
    catalog();
    mocks.detail.mockReturnValue({ data: undefined, isPending: true });
    mocks.balance.mockReturnValue({
      data: { value: { availableMicro: 0 }, degraded: false },
      isPending: false,
    });
  });

  it("lists backend agents with their own name, description, avatar, and skills", () => {
    renderDirectory();
    expect(cards()).toHaveLength(2);
    const first = cards()[0]!;
    expect(first).toHaveTextContent("Lyrics helper");
    expect(first).toHaveTextContent("Writes short lyrics.");
    expect(first).toHaveTextContent("歌词创作");
    expect(first.querySelector(".aurora-avatar-img")).toHaveAttribute(
      "src",
      "/uploads/lyrics.png",
    );
  });

  it("puts submittable agents first and counts what the reader sees", async () => {
    const user = userEvent.setup();
    renderDirectory();
    expect(screen.getByText("2 assistants shown")).toBeVisible();
    expect(cards()[0]).toHaveTextContent("Lyrics helper");
    await user.type(screen.getByRole("searchbox"), "Runbooks");
    expect(cards()).toHaveLength(1);
    expect(cards()[0]).toHaveTextContent("Ops assistant");
  });

  it("searches names, descriptions, skills, and conversation starters", async () => {
    const user = userEvent.setup();
    renderDirectory();
    const search = screen.getByRole("searchbox");
    await user.type(search, "courage");
    expect(cards()).toHaveLength(1);
    expect(cards()[0]).toHaveTextContent("Lyrics helper");
    await user.clear(search);
    await user.type(search, "operations questions");
    expect(cards()).toHaveLength(1);
    expect(cards()[0]).toHaveTextContent("Ops assistant");
  });

  it("opens the composer only for an agent with an Aurora identity", async () => {
    const user = userEvent.setup();
    renderDirectory();
    expect(cards()[0]!.tagName).toBe("BUTTON");
    expect(cards()[1]!.tagName).toBe("DIV");

    await user.click(cards()[0]!);
    expect(await screen.findByRole("dialog")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Generate" })).toBeDisabled();
  });

  it("shows no price outside the composer for a read-only agent", () => {
    renderDirectory();
    expect(cards()[1]).toHaveTextContent("View only");
    expect(cards()[1]).not.toHaveTextContent(/\d+ credits/);
    expect(cards()[0]).toHaveTextContent("60 credits");
  });

  it("treats a pending or failed catalog as nothing submittable", () => {
    mocks.skills.mockReturnValue({ data: undefined, isPending: true });
    renderDirectory();
    expect(cards()[0]!.tagName).toBe("DIV");
    expect(screen.queryByText("60 credits")).not.toBeInTheDocument();
  });

  it.each([
    { isPending: true },
    { isPending: false, isError: true, refetch: vi.fn() },
  ])("does not present an unread agent list as zero assistants", (query) => {
    directory([], query);
    renderDirectory();
    expect(screen.queryByText("0 assistants shown")).not.toBeInTheDocument();
  });

  it("reports a successfully read empty agent list honestly", () => {
    directory([]);
    renderDirectory();
    expect(screen.getByText("0 assistants shown")).toBeVisible();
    expect(screen.getByText("No matching assistants")).toBeVisible();
  });

  it("offers retry for a failed agent list", async () => {
    const user = userEvent.setup();
    const refetch = vi.fn();
    directory([], { isPending: false, isError: true, refetch });
    renderDirectory();
    await user.click(screen.getByRole("button", { name: "Try again" }));
    expect(refetch).toHaveBeenCalledOnce();
  });

  it("clears the search from the empty state", async () => {
    const user = userEvent.setup();
    renderDirectory();
    await user.type(screen.getByRole("searchbox"), "nothing matches");
    expect(screen.getByText("No matching assistants")).toBeVisible();
    await user.click(screen.getByRole("button", { name: "Clear search" }));
    expect(screen.getByRole("searchbox")).toHaveValue("");
    expect(cards()).toHaveLength(2);
  });
});
```

- [ ] **Step 3: Run the directory tests to verify they fail**

Run:
```bash
pnpm -C packages/views exec vitest run aurora/skill-directory.test.tsx
```
Expected: FAIL — the component still reads the skill catalog and the hook does not exist.

- [ ] **Step 4: Replace the directory component**

Replace `packages/views/aurora/skill-directory.tsx` with:

```tsx
"use client";

import { useMemo, useState } from "react";
import {
  ArrowUpRight,
  LayoutGrid,
  Search,
  SearchX,
  Sparkles,
} from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import {
  InputGroup,
  InputGroupAddon,
  InputGroupInput,
} from "@multica/ui/components/ui/input-group";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { useAuroraSkills, type AuroraSkill } from "@multica/core/aurora";
import type { Agent } from "@multica/core/types";
import { AuroraAvatar } from "./aurora-avatar";
import { AuroraPageState } from "./aurora-page-header";
import { useLocale, useT } from "../i18n";
import { GenerationComposer } from "./generation-composer";
import { formatCredits } from "./format";
import { AuroraLoadFailed } from "./load-failed";
import {
  agentSkillChips,
  agentSubmissionSkill,
  matchesAgentQuery,
  orderDirectoryAgents,
} from "./aurora-agents";
import { useAuroraAgentDirectory } from "./use-aurora-agent-directory";

/**
 * The consumer directory: every agent the backend shows this member, with the
 * skill catalog consulted only to decide which agents can submit.
 *
 * The directory owns which agent's drawer is open. That stays component state
 * rather than a URL so a half-written prompt does not survive a refresh as a
 * linkable address.
 */
export interface SkillDirectoryProps {
  /** The app's library route, offered on a finished generation. */
  worksHref?: string;
  /** The app's checkout route, offered when a generation costs more than the wallet holds. */
  topUpHref?: string;
  /** The app's route for the issue a generation runs on, forwarded to the drawer. */
  issueHref?: (identifier: string) => string | null;
}

// The grid's own geometry, shared by the cards and the skeleton so the page
// does not jump while the agent list resolves.
const GRID_CLASS = "aurora-card-grid";

// A stable reference for "the catalog has not loaded yet", so the memos below
// are not handed a fresh array on every render.
const EMPTY_SKILLS: AuroraSkill[] = [];

interface SelectedAgent {
  agent: Agent;
  skill: AuroraSkill;
}

export function SkillDirectory({
  worksHref,
  topUpHref,
  issueHref,
}: SkillDirectoryProps = {}) {
  const { t } = useT("aurora");
  const { agents, query: agentsQuery } = useAuroraAgentDirectory();
  const skillsQuery = useAuroraSkills();
  const skills = skillsQuery.data?.value ?? EMPTY_SKILLS;
  const [query, setQuery] = useState("");
  const [selected, setSelected] = useState<SelectedAgent | null>(null);

  // Submittable agents first; the backend's order holds inside each group.
  const ordered = useMemo(
    () => orderDirectoryAgents(agents, skills),
    [agents, skills],
  );
  const visible = useMemo(
    () => ordered.filter((agent) => matchesAgentQuery(agent, query)),
    [ordered, query],
  );

  const agentsKnown = !agentsQuery.isPending && !agentsQuery.isError;

  return (
    <div className="aurora-page">
      <DirectoryHero
        agents={agentsKnown ? ordered : undefined}
        skills={skills}
      />
      <ol
        className="aurora-quick-steps"
        aria-label={t(($) => $.directory.guide_label)}
      >
        <li>
          <span>1</span>
          {t(($) => $.directory.step_choose)}
        </li>
        <li>
          <span>2</span>
          {t(($) => $.directory.step_describe)}
        </li>
        <li>
          <span>3</span>
          {t(($) => $.directory.step_download)}
        </li>
      </ol>

      <div className="aurora-section-head">
        <div>
          <p className="aurora-eyebrow">
            <LayoutGrid aria-hidden="true" className="size-3.5" />
            {t(($) => $.directory.eyebrow)}
          </p>
          <h2>{t(($) => $.directory.title)}</h2>
        </div>
        {agentsKnown ? (
          <span>
            {t(($) => $.directory.visible_count, { count: visible.length })}
          </span>
        ) : null}
      </div>

      <div className="aurora-toolbar">
        <InputGroup className="w-full max-w-[280px] bg-card">
          <InputGroupAddon>
            <Search aria-hidden="true" />
          </InputGroupAddon>
          <InputGroupInput
            type="search"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder={t(($) => $.directory.search_placeholder)}
            aria-label={t(($) => $.directory.search_placeholder)}
          />
        </InputGroup>
      </div>

      {agentsQuery.isPending ? (
        <DirectorySkeleton />
      ) : agentsQuery.isError ? (
        <AuroraLoadFailed
          title={t(($) => $.directory.load_failed_title)}
          onRetry={() => void agentsQuery.refetch()}
        />
      ) : visible.length === 0 ? (
        <AuroraPageState
          icon={SearchX}
          title={t(($) => $.directory.empty_title)}
          description={t(($) => $.directory.empty_description)}
          actions={
            query ? (
              <Button
                type="button"
                variant="outline"
                onClick={() => setQuery("")}
              >
                {t(($) => $.directory.clear_search)}
              </Button>
            ) : undefined
          }
        />
      ) : (
        <ul className={GRID_CLASS}>
          {visible.map((agent) => {
            const skill = agentSubmissionSkill(agent, skills);
            return (
              <li key={agent.id}>
                <AgentCard
                  agent={agent}
                  skill={skill}
                  onSelect={() => {
                    if (skill) setSelected({ agent, skill });
                  }}
                />
              </li>
            );
          })}
        </ul>
      )}

      <GenerationComposer
        skill={selected?.skill ?? null}
        open={selected !== null}
        onOpenChange={(open) => {
          if (!open) setSelected(null);
        }}
        worksHref={worksHref}
        topUpHref={topUpHref}
        issueHref={issueHref}
      />
    </div>
  );
}

function DirectoryHero({
  agents,
  skills,
}: {
  agents: Agent[] | undefined;
  skills: AuroraSkill[];
}) {
  const { t } = useT("aurora");
  return (
    <section className="aurora-hero">
      <div>
        <span className="aurora-hero-label">
          {t(($) => $.directory.hero_tag)}
        </span>
        <h1>{t(($) => $.directory.hero_title)}</h1>
        <p>{t(($) => $.directory.hero_subtitle)}</p>
      </div>
      {/* Decorative: the first four agents as tiles. It carries no fact the
          list below does not, so it is hidden from assistive tech. */}
      <div className="aurora-hero-orbit" aria-hidden="true">
        <div className="aurora-hero-center">
          <Sparkles className="size-6" />
        </div>
        {(agents ?? []).slice(0, 4).map((agent, index) => (
          <span
            key={agent.id}
            className={"aurora-orbit-avatar aurora-orbit-avatar--" + (index + 1)}
          >
            <AuroraAvatar
              name={agent.name}
              id={agent.id}
              avatarUrl={agent.avatar_url}
              available={Boolean(agentSubmissionSkill(agent, skills))}
            />
          </span>
        ))}
      </div>
    </section>
  );
}

function AgentCard({
  agent,
  skill,
  onSelect,
}: {
  agent: Agent;
  skill: AuroraSkill | null;
  onSelect: () => void;
}) {
  const { t } = useT("aurora");
  const locale = useLocale();
  const chips = agentSkillChips(agent);

  const content = (
    <>
      <span className="aurora-card-top">
        <AuroraAvatar
          name={agent.name}
          id={agent.id}
          avatarUrl={agent.avatar_url}
          size="lg"
          available={Boolean(skill)}
        />
        <span className="aurora-card-name">
          <span className="aurora-card-title">{agent.name}</span>
        </span>
        {skill ? (
          <span aria-hidden="true" className="aurora-card-arrow">
            <ArrowUpRight className="size-4" />
          </span>
        ) : null}
      </span>
      {agent.description ? (
        <span className="aurora-card-description">{agent.description}</span>
      ) : null}
      {skill && skill.output.length === 1 && skill.output[0] === "text" ? (
        <span className="aurora-delivery-note">
          {t(($) => $.directory.text_output)}
        </span>
      ) : null}
      {chips.length > 0 ? (
        <span className="aurora-chips">
          {chips.map((chip) => (
            <span key={chip.id}>{chip.name}</span>
          ))}
        </span>
      ) : null}
      <span className="aurora-card-footer">
        {skill ? (
          <>
            <span className="aurora-card-state">
              <span aria-hidden="true" className="aurora-dot" />
              {t(($) => $.directory.ready)}
            </span>
            <strong>
              {t(($) => $.credits, {
                credits: formatCredits(skill.credits, locale),
              })}
            </strong>
          </>
        ) : (
          <span className="aurora-status-pill aurora-status-pill--muted">
            {t(($) => $.directory.read_only)}
          </span>
        )}
      </span>
    </>
  );

  if (!skill) {
    return (
      <div className="aurora-card aurora-card--read-only">{content}</div>
    );
  }
  return (
    <button type="button" onClick={onSelect} className="aurora-card">
      {content}
    </button>
  );
}

/** Placeholder cards at the grid's own geometry, so the page does not jump. */
function DirectorySkeleton() {
  return (
    <ul className={GRID_CLASS}>
      {Array.from({ length: 8 }, (_, index) => (
        <li key={index}>
          <Skeleton className="h-20 w-full rounded-xl" />
        </li>
      ))}
    </ul>
  );
}
```

- [ ] **Step 5: Neutralise the read-only card interaction**

Append to `packages/views/aurora/aurora.css` after the `.aurora-card--unavailable` rule:

```css
.aurora-card--read-only {
  cursor: default;
}

.aurora-card--read-only:hover {
  border-color: var(--aurora-line);
  box-shadow: none;
  transform: none;
}
```

- [ ] **Step 6: Update the English copy**

In `packages/views/locales/en/aurora.json`, replace the whole `directory` object with:

```json
"directory": {
  "title": "Start creating",
  "search_placeholder": "Search assistants, e.g. social posts",
  "load_failed_title": "Could not load assistants",
  "load_failed_description": "The agent list is temporarily unavailable.",
  "empty_title": "No matching assistants",
  "empty_description": "Try another keyword.",
  "eyebrow": "AI ASSISTANTS",
  "hero_title": "What would you like to create?",
  "hero_subtitle": "Pick an assistant. Add your details. Get a result you can use.",
  "hero_tag": "YOUR EVERYDAY AI TEAM",
  "visible_count_one": "{{count}} assistant shown",
  "visible_count_other": "{{count}} assistants shown",
  "ready": "Open assistant",
  "clear_search": "Clear search",
  "text_output": "Written content · Markdown (.md)",
  "read_only": "View only",
  "guide_label": "Start in three steps",
  "step_choose": "Choose an assistant",
  "step_describe": "Add your details",
  "step_download": "Review & download",
  "categories": {
    "all": "All tools",
    "ecommerce": "E-commerce",
    "content-creation": "Content creation",
    "marketing": "Marketing",
    "legal": "Legal",
    "media": "Media",
    "design": "Design",
    "other": "Other"
  }
}
```

- [ ] **Step 7: Update the Chinese copy**

In `packages/views/locales/zh-Hans/aurora.json`, replace the whole `directory` object with:

```json
"directory": {
  "title": "开始创作",
  "search_placeholder": "搜索助手，例如：小红书",
  "load_failed_title": "无法加载助手列表",
  "load_failed_description": "助手列表暂时不可用。",
  "empty_title": "没有匹配的助手",
  "empty_description": "换个关键词试试。",
  "eyebrow": "AI 助手",
  "hero_title": "你今天想做什么？",
  "hero_subtitle": "选一个助手，填入你的需求，把想法变成可用的内容。",
  "hero_tag": "APEX AI · 你的创作助手",
  "visible_count_other": "{{count}} 个助手",
  "ready": "开始使用",
  "clear_search": "清除搜索",
  "text_output": "文字内容 · Markdown（.md）",
  "read_only": "暂不可提交",
  "guide_label": "三步完成创作",
  "step_choose": "选择助手",
  "step_describe": "填写需求",
  "step_download": "查看与下载",
  "categories": {
    "all": "全部功能",
    "ecommerce": "电商",
    "content-creation": "故事与写作",
    "marketing": "推广与文案",
    "legal": "合同与资料",
    "media": "视频脚本",
    "design": "产品与网页",
    "other": "其他"
  }
}
```

- [ ] **Step 8: Update the French copy**

In `packages/views/locales/fr/aurora.json`, replace the whole `directory` object with:

```json
"directory": {
  "title": "Commencer à créer",
  "search_placeholder": "Rechercher un assistant",
  "load_failed_title": "Impossible de charger les assistants",
  "load_failed_description": "La liste des assistants est temporairement indisponible.",
  "empty_title": "Aucun assistant correspondant",
  "empty_description": "Essayez un autre mot-clé.",
  "eyebrow": "ASSISTANTS IA",
  "hero_title": "Que souhaitez-vous créer ?",
  "hero_subtitle": "Choisissez un assistant, précisez votre besoin et récupérez le résultat.",
  "hero_tag": "VOTRE ÉQUIPE IA AU QUOTIDIEN",
  "visible_count_one": "{{count}} assistant affiché",
  "visible_count_many": "{{count}} assistants affichés",
  "visible_count_other": "{{count}} assistants affichés",
  "ready": "Ouvrir l'assistant",
  "clear_search": "Effacer la recherche",
  "text_output": "Contenu écrit · Markdown (.md)",
  "read_only": "Lecture seule",
  "guide_label": "Créer en trois étapes",
  "step_choose": "Choisir un assistant",
  "step_describe": "Préciser votre besoin",
  "step_download": "Vérifier et télécharger",
  "categories": {
    "all": "Tous les outils",
    "ecommerce": "E-commerce",
    "content-creation": "Création de contenu",
    "marketing": "Marketing",
    "legal": "Juridique",
    "media": "Médias",
    "design": "Design",
    "other": "Autre"
  }
}
```

- [ ] **Step 9: Update the Japanese copy**

In `packages/views/locales/ja/aurora.json`, replace the whole `directory` object with:

```json
"directory": {
  "title": "作成を始める",
  "search_placeholder": "アシスタントを検索",
  "load_failed_title": "アシスタント一覧を読み込めませんでした",
  "load_failed_description": "アシスタント一覧を一時的に利用できません。",
  "empty_title": "一致するアシスタントがありません",
  "empty_description": "別のキーワードをお試しください。",
  "eyebrow": "AI アシスタント",
  "hero_title": "今日は何を作りますか？",
  "hero_subtitle": "アシスタントを選び、内容を入力して、成果物を受け取りましょう。",
  "hero_tag": "毎日の AI アシスタント",
  "visible_count_other": "アシスタント {{count}} 件",
  "ready": "使ってみる",
  "clear_search": "検索をクリア",
  "text_output": "文章 · Markdown（.md）",
  "read_only": "閲覧のみ",
  "guide_label": "3ステップで作成",
  "step_choose": "アシスタントを選択",
  "step_describe": "内容を入力",
  "step_download": "確認・ダウンロード",
  "categories": {
    "all": "すべてのツール",
    "ecommerce": "EC",
    "content-creation": "コンテンツ制作",
    "marketing": "マーケティング",
    "legal": "法務",
    "media": "メディア",
    "design": "デザイン",
    "other": "その他"
  }
}
```

- [ ] **Step 10: Update the Korean copy**

In `packages/views/locales/ko/aurora.json`, replace the whole `directory` object with:

```json
"directory": {
  "title": "만들기 시작",
  "search_placeholder": "어시스턴트 검색",
  "load_failed_title": "어시스턴트 목록을 불러오지 못했습니다",
  "load_failed_description": "어시스턴트 목록을 일시적으로 사용할 수 없습니다.",
  "empty_title": "일치하는 어시스턴트가 없습니다",
  "empty_description": "다른 키워드를 시도해 보세요.",
  "eyebrow": "AI 어시스턴트",
  "hero_title": "오늘 무엇을 만들까요?",
  "hero_subtitle": "어시스턴트를 선택하고 내용을 입력해 결과물을 받아 보세요.",
  "hero_tag": "일상 속 AI 도우미",
  "visible_count_other": "어시스턴트 {{count}}개",
  "ready": "사용하기",
  "clear_search": "검색 지우기",
  "text_output": "텍스트 · Markdown (.md)",
  "read_only": "보기 전용",
  "guide_label": "세 단계로 만들기",
  "step_choose": "어시스턴트 선택",
  "step_describe": "요청 입력",
  "step_download": "확인 및 다운로드",
  "categories": {
    "all": "모든 도구",
    "ecommerce": "이커머스",
    "content-creation": "콘텐츠 제작",
    "marketing": "마케팅",
    "legal": "법률",
    "media": "미디어",
    "design": "디자인",
    "other": "기타"
  }
}
```

- [ ] **Step 11: Update the locale parity test**

`packages/views/locales/parity.test.ts` still asserts the pre-agent copy and the
`stats_runnable` / `stats_total` families this task removes. Its typed `$`
references would also fail typecheck once those keys leave the English bundle.

Replace the whole `describe("Aurora directory plural families", ...)` block
(from `describe("Aurora directory plural families", () => {` through its
closing `});` that sits immediately before `describe("locale bundle parity"`)
with:

```ts
describe("Aurora directory plural families", () => {
  it.each([
    ["en", 0, "0 assistants shown"],
    ["en", 1, "1 assistant shown"],
    ["en", 3, "3 assistants shown"],
    ["zh-Hans", 0, "0 个助手"],
    ["zh-Hans", 1, "1 个助手"],
    ["zh-Hans", 3, "3 个助手"],
    ["ja", 0, "アシスタント 0 件"],
    ["ja", 1, "アシスタント 1 件"],
    ["ja", 3, "アシスタント 3 件"],
    ["ko", 0, "어시스턴트 0개"],
    ["ko", 1, "어시스턴트 1개"],
    ["ko", 3, "어시스턴트 3개"],
    ["fr", 0, "0 assistant affiché"],
    ["fr", 1, "1 assistant affiché"],
    ["fr", 3, "3 assistants affichés"],
    ["fr", 1000000, "1000000 assistants affichés"],
  ] as const)("renders %s count %i without fallback or duplicated numerals", async (locale, count, visible) => {
    const i18n = createInstance();
    await i18n.init({ lng: locale, fallbackLng: false, resources: RESOURCES });
    expect(i18n.t(($) => $.directory.visible_count, { ns: "aurora", count })).toBe(visible);
  });
  const families = ["visible_count"];
  const categories = { en: ["one", "other"], "zh-Hans": ["other"], ja: ["other"], ko: ["other"], fr: ["many", "one", "other"] };
  for (const [locale, suffixes] of Object.entries(categories)) {
    it(`${locale} supplies exactly its grammatical count forms`, () => {
      const keys = flattenKeys(RESOURCES[locale as keyof typeof RESOURCES].aurora);
      for (const family of families) {
        const base = `directory.${family}`;
        expect(keys.filter((key) => key === base || key.startsWith(base + "_")).sort())
          .toEqual(suffixes.map((suffix) => base + "_" + suffix).sort());
      }
    });
  }
});
```

- [ ] **Step 12: Run the directory and locale tests, then the typecheck**

Run:
```bash
pnpm -C packages/views exec vitest run \
  aurora/skill-directory.test.tsx \
  aurora/labels.test.ts \
  locales/parity.test.ts
pnpm --filter @multica/views typecheck
```
Expected: all pass. The parity suite is red on the base branch because the
consumer-workbench copy changed without this test; this step is what makes it
green again.

- [ ] **Step 13: Commit**

```bash
git add packages/views/aurora/use-aurora-agent-directory.ts \
  packages/views/aurora/skill-directory.tsx \
  packages/views/aurora/skill-directory.test.tsx \
  packages/views/aurora/aurora.css \
  packages/views/locales/en/aurora.json \
  packages/views/locales/zh-Hans/aurora.json \
  packages/views/locales/fr/aurora.json \
  packages/views/locales/ja/aurora.json \
  packages/views/locales/ko/aurora.json \
  packages/views/locales/parity.test.ts
git commit -m "feat(aurora): drive the directory from backend agents"
```

---

### Task 3: Composer renders the agent and its starters

**Files:**
- Modify: `packages/views/aurora/generation-composer.tsx`
- Modify: `packages/views/aurora/generation-composer.test.tsx`
- Modify: `packages/views/aurora/skill-directory.tsx` (pass the selected agent)

**Interfaces:**
- Consumes: `Agent` from `@multica/core/types`.
- Produces: `GenerationComposerProps.agent?: Agent | null`; the component renders nothing unless both `agent` and `skill` are present.

- [ ] **Step 1: Update the composer tests first**

In `packages/views/aurora/generation-composer.test.tsx`:

1. Add `Agent` to the type imports:

```ts
import type { Agent } from "@multica/core/types";
```

2. Add an agent fixture after the `skill()` fixture:

```ts
function agent(overrides: Partial<Agent> = {}): Agent {
  return {
    id: "agent-lyrics",
    workspace_id: "ws-1",
    runtime_id: "rt-1",
    name: "Write lyrics",
    description: "Short lyrics for songs.",
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
    skills: [{ id: "s-lyrics", name: "歌词创作", description: "", enabled: true }],
    archived_by: null,
    conversation_starters: [
      {
        label: "Try an example",
        prompt: "A warm song about a family trip to Singapore",
      },
    ],
    ...overrides,
  };
}

const DEFAULT_AGENT = agent();
```

3. Replace `renderComposer` with:

```ts
function renderComposer(
  props: Partial<Parameters<typeof GenerationComposer>[0]>,
) {
  return renderWithI18n(
    <NavigationProvider value={stubNavigationAdapter()}>
      <GenerationComposer
        agent={DEFAULT_AGENT}
        skill={skill()}
        open
        onOpenChange={vi.fn()}
        {...props}
      />
    </NavigationProvider>,
  );
}
```

4. In the test `"groups the labelled prompt and submit action in the Aurora drawer"`, delete this assertion:

```ts
expect(screen.getByText("Content creation")).toHaveClass("aurora-drawer-head-category");
```

The rest of that test stays (the heading is still "Write lyrics", now from the agent).

5. Add two tests at the end of the `describe` block:

```ts
it("renders the agent's own description and conversation starters", () => {
  renderComposer({});
  expect(screen.getByText("Short lyrics for songs.")).toBeInTheDocument();
  expect(
    screen.getByRole("button", { name: "Try an example" }),
  ).toBeInTheDocument();
});

it("omits the deliverable and examples when the agent has neither", () => {
  renderComposer({
    agent: agent({ description: "", conversation_starters: [] }),
  });
  expect(
    screen.queryByText("What you will receive"),
  ).not.toBeInTheDocument();
  expect(
    screen.queryByRole("button", { name: "Try an example" }),
  ).not.toBeInTheDocument();
});
```

- [ ] **Step 2: Run the composer tests to verify they fail**

Run:
```bash
pnpm -C packages/views exec vitest run aurora/generation-composer.test.tsx
```
Expected: FAIL — the composer does not accept an `agent` prop, and it still reads `toolGuidance`.

- [ ] **Step 3: Add the agent prop and use it for display**

In `packages/views/aurora/generation-composer.tsx`:

1. Add the type import:

```ts
import type { Agent } from "@multica/core/types";
```

2. Remove the hardcoded-copy import:

```ts
import { toolGuidance } from "./tool-guidance";
```

3. Replace the labels import:

```ts
import {
  auroraCategoryLabel,
  generationStatusLabel,
  skillDisplayName,
} from "./labels";
```

with:

```ts
import { generationStatusLabel } from "./labels";
```

4. Add `agent` to the props interface, before `skill`:

```ts
  /** The backend agent this drawer presents. Required with `skill`. */
  agent?: Agent | null;
```

5. In `GenerationComposer`, require both and key the body on the agent:

```ts
export function GenerationComposer({
  agent,
  skill,
  open,
  onOpenChange,
  worksHref,
  topUpHref,
  issueHref,
}: GenerationComposerProps) {
  if (!agent || !skill) return null;
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="aurora-drawer">
        {/* Keyed on the agent so switching agents remounts the body: the
            draft, the in-flight generation and any failure all belong to the
            agent that was open. */}
        <ComposerBody
          key={agent.id}
          agent={agent}
          skill={skill}
          worksHref={worksHref}
          topUpHref={topUpHref}
          issueHref={issueHref}
        />
      </SheetContent>
    </Sheet>
  );
}
```

6. Add `agent` to `ComposerBody`'s parameters:

```ts
function ComposerBody({
  agent,
  skill,
  worksHref,
  topUpHref,
  issueHref,
}: {
  agent: Agent;
  skill: AuroraSkill;
  worksHref?: string;
  topUpHref?: string;
  issueHref?: (identifier: string) => string | null;
}) {
```

7. Replace the guidance derivation:

```ts
  const guidance = toolGuidance(skill.id, locale);
```

with:

```ts
  const starters = (agent.conversation_starters ?? [])
    .filter((starter) => starter.label.trim() && starter.prompt.trim())
    .slice(0, 3);
  const description = agent.description.trim();
```

8. Replace the drawer header block. The existing block starts at `<SheetHeader` and ends before `{!skill.available ? (`. Replace it with:

```tsx
      <SheetHeader className="aurora-drawer-head flex-row">
        <AuroraAvatar
          name={agent.name}
          id={agent.id}
          avatarUrl={agent.avatar_url}
          size="lg"
          available={skill.available}
        />
        <div>
          <SheetTitle>{agent.name}</SheetTitle>
          <SheetDescription>
            {t(($) => $.composer.cost, {
              credits: formatCredits(skill.credits, locale),
            })}
          </SheetDescription>
        </div>
      </SheetHeader>
```

9. Replace the deliverable block:

```tsx
      {guidance ? (
        <section className="aurora-deliverable" aria-label={t(($) => $.composer.deliverable_title)}>
          <strong>{t(($) => $.composer.deliverable_title)}</strong>
          <p>{guidance.summary}</p>
          {skill.output.length === 1 && skill.output[0] === "text" ? <span>{t(($) => $.directory.text_output)}</span> : null}
        </section>
      ) : null}
```

with:

```tsx
      {description ? (
        <section
          className="aurora-deliverable"
          aria-label={t(($) => $.composer.deliverable_title)}
        >
          <strong>{t(($) => $.composer.deliverable_title)}</strong>
          <p>{description}</p>
          {skill.output.length === 1 && skill.output[0] === "text" ? (
            <span>{t(($) => $.directory.text_output)}</span>
          ) : null}
        </section>
      ) : null}
```

10. Replace the textarea placeholder:

```tsx
            placeholder={guidance?.example ?? t(($) => $.composer.prompt_placeholder)}
```

with:

```tsx
            placeholder={t(($) => $.composer.prompt_placeholder)}
```

11. Replace the guidance-gated block (the `{guidance ? (<> ... </>) : null}` that contains the sample button and the audience/preferences fields) with:

```tsx
        {starters.length > 0 ? (
          <div className="flex flex-wrap gap-2">
            {starters.map((starter, index) => (
              <Button
                key={`${starter.label}-${index}`}
                type="button"
                variant="outline"
                size="sm"
                disabled={
                  !skill.available ||
                  prompt.trim().length > 0 ||
                  create.isPending ||
                  outcome.kind === "started" ||
                  outcome.kind === "unreadable"
                }
                onClick={() => setPrompt(starter.prompt)}
              >
                <Sparkles aria-hidden="true" />
                {starter.label}
              </Button>
            ))}
          </div>
        ) : null}
        <div className="aurora-field">
          <Label htmlFor="aurora-audience">
            {t(($) => $.composer.audience_label)}
          </Label>
          <Input
            id="aurora-audience"
            value={audience}
            onChange={(event) => setAudience(event.target.value)}
            disabled={!skill.available}
            placeholder={t(($) => $.composer.audience_placeholder)}
          />
        </div>
        <div className="aurora-field">
          <Label htmlFor="aurora-preferences">
            {t(($) => $.composer.preferences_label)}
          </Label>
          <Input
            id="aurora-preferences"
            value={preferences}
            onChange={(event) => setPreferences(event.target.value)}
            disabled={!skill.available}
            placeholder={t(($) => $.composer.preferences_placeholder)}
          />
        </div>
```

12. Pass the selected agent from the directory. In `packages/views/aurora/skill-directory.tsx`, change:

```tsx
      <GenerationComposer
        skill={selected?.skill ?? null}
```

to:

```tsx
      <GenerationComposer
        agent={selected?.agent ?? null}
        skill={selected?.skill ?? null}
```

- [ ] **Step 4: Run the composer tests**

Run:
```bash
pnpm -C packages/views exec vitest run aurora/generation-composer.test.tsx aurora/skill-directory.test.tsx
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/views/aurora/generation-composer.tsx \
  packages/views/aurora/generation-composer.test.tsx \
  packages/views/aurora/skill-directory.tsx
git commit -m "feat(aurora): render agent identity and starters in the composer"
```

---

### Task 4: Delete the hardcoded display layer

**Files:**
- Delete: `packages/views/aurora/tool-guidance.ts`
- Delete: `packages/views/aurora/tool-guidance.test.ts`
- Modify: `packages/views/aurora/labels.ts`
- Modify: `packages/views/aurora/labels.test.ts`
- Modify: all five `packages/views/locales/*/aurora.json` files

**Interfaces:**
- `skillDisplayName(skill, locale)` keeps its signature and now returns the catalog's `name`/`nameEn` only.

- [ ] **Step 1: Delete the hardcoded tables**

Run:
```bash
git rm packages/views/aurora/tool-guidance.ts packages/views/aurora/tool-guidance.test.ts
```

- [ ] **Step 2: Simplify the catalog name helper**

In `packages/views/aurora/labels.ts`:

1. Delete the import:

```ts
import { toolGuidance } from "./tool-guidance";
```

2. Replace `skillDisplayName` (including its doc comment) with:

```ts
/**
 * The name to put on a generation that only carries a skill id.
 *
 * The consumer surfaces show agent names; this is the fallback for an id no
 * visible agent owns. The catalog carries a Chinese `name` and English
 * `nameEn`, so a Chinese reader gets the Chinese name and every other reader
 * gets the English one. `nameEn` defaults to `""` in the schema, so a Chinese
 * name is still the better answer than an empty label.
 */
export function skillDisplayName(skill: AuroraSkill, locale: string): string {
  if (locale.startsWith("zh")) return skill.name;
  return skill.nameEn || skill.name;
}
```

3. Delete the entire `auroraInputLabel` block: the `AuroraInputLabel` type, `INPUT_LABELS`, and the function.

4. Delete the entire `auroraCategoryLabel` block: the `AuroraCategoryLabel` type, `CATEGORY_LABELS`, and the function.

- [ ] **Step 3: Update the label tests**

In `packages/views/aurora/labels.test.ts`:

1. Remove `auroraCategoryLabel` and `auroraInputLabel` from the import.
2. Delete the `describe("auroraInputLabel", ...)` block.
3. Delete the `describe("auroraCategoryLabel", ...)` block at the end of the file.

The remaining suites (`generationStatusLabel`, `ledgerKindLabel`, `skillDisplayName`, `skillDisplayNamesById`) stay unchanged.

- [ ] **Step 4: Remove the copy keys the refactor made unreachable**

Task 2 already dropped the directory-only keys (`unavailable`, `inputs`,
`stats_*`, `recommended`, `upcoming_*`, `planned`, `clear_filters`,
`output_label`) when it replaced the `directory` object. Two keys deliberately
survived because code still read them at that point:

- `directory.categories` — the composer's category line used it until Task 3.
- `composer.example` — the composer's single example button used it until Task 3.

In every file under `packages/views/locales/{en,zh-Hans,fr,ja,ko}/aurora.json`, delete the whole `directory.categories` object and the `composer.example` line.

Keep `directory.text_output`, `directory.ready`, and every key the components still read.

- [ ] **Step 5: Prove no reference survives**

Run:
```bash
grep -rn "toolGuidance\|RECOMMENDED_TOOL_IDS\|orderTools\|auroraCategoryLabel\|auroraInputLabel\|directory\.categories\|directory\.planned" packages/views apps/aurora
```
Expected: no output.

- [ ] **Step 6: Run the views tests and typecheck**

Run:
```bash
pnpm --filter @multica/views test
pnpm --filter @multica/views typecheck
```
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add -A packages/views/aurora packages/views/locales
git commit -m "refactor(aurora): remove the hardcoded skill display layer"
```

---

### Task 5: Prefer agent names in history, works, and detail

**Files:**
- Create: `packages/views/aurora/use-aurora-agent-names.ts`
- Modify: `packages/views/aurora/history-list.tsx`
- Modify: `packages/views/aurora/works-list.tsx`
- Modify: `packages/views/aurora/generation-detail.tsx`
- Modify: `packages/views/aurora/history-list.test.tsx`
- Modify: `packages/views/aurora/works-list.test.tsx`
- Modify: `packages/views/aurora/generation-detail.test.tsx`

**Interfaces:**
- Produces: `useAuroraAgentNames(): Map<string, string>`.

- [ ] **Step 1: Create the hook**

Create `packages/views/aurora/use-aurora-agent-names.ts`:

```ts
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
```

- [ ] **Step 2: Update history**

In `packages/views/aurora/history-list.tsx`:

1. Add the import:

```ts
import { useAuroraAgentNames } from "./use-aurora-agent-names";
```

2. After `const skillNames = useMemo(...)`, add:

```ts
  const agentNames = useAuroraAgentNames();
```

3. In the skill filter options, change both labels from `skillDisplayName(skill, locale)` to:

```ts
agentNames.get(skill.id) ?? skillDisplayName(skill, locale)
```

4. In the row construction, change:

```tsx
                  skillName={
                    skillNames.get(generation.skillId) ??
                    (catalogLoaded ? t(($) => $.works.skill_unknown) : null)
                  }
```

to:

```tsx
                  skillName={
                    agentNames.get(generation.skillId) ??
                    skillNames.get(generation.skillId) ??
                    (catalogLoaded ? t(($) => $.works.skill_unknown) : null)
                  }
```

- [ ] **Step 3: Update works**

In `packages/views/aurora/works-list.tsx`:

1. Add the import:

```ts
import { useAuroraAgentNames } from "./use-aurora-agent-names";
```

2. After `const skills = skillsQuery.data?.value;`, add:

```ts
  const agentNames = useAuroraAgentNames();
```

3. Replace the `skillNames` memo with:

```ts
  const skillNames = useMemo(() => {
    const names = skillDisplayNamesById(skills ?? [], locale);
    for (const [skillId, agentName] of agentNames) names.set(skillId, agentName);
    return names;
  }, [skills, locale, agentNames]);
```

- [ ] **Step 4: Update generation detail**

In `packages/views/aurora/generation-detail.tsx`:

1. Add the import:

```ts
import { useAuroraAgentNames } from "./use-aurora-agent-names";
```

2. In `GenerationFields`, after `const skills = skillsQuery.data?.value ?? EMPTY_SKILLS;`, add:

```ts
  const agentNames = useAuroraAgentNames();
```

3. Replace:

```ts
  const skillName = skill
    ? skillDisplayName(skill, locale)
    : catalogLoaded
      ? t(($) => $.works.skill_unknown)
      : generation.skillId;
```

with:

```ts
  const skillName =
    agentNames.get(generation.skillId) ??
    (skill
      ? skillDisplayName(skill, locale)
      : catalogLoaded
        ? t(($) => $.works.skill_unknown)
        : generation.skillId);
```

- [ ] **Step 5: Add the test mocks and one precedence test per suite**

In each of `history-list.test.tsx`, `works-list.test.tsx`, and `generation-detail.test.tsx`:

1. Add this hoisted state near the other `vi.hoisted` declarations:

```ts
const agentNames = vi.hoisted(() => ({ current: new Map<string, string>() }));
```

2. Add this module mock after the existing `@multica/core/aurora` mock:

```ts
vi.mock("./use-aurora-agent-names", () => ({
  useAuroraAgentNames: () => agentNames.current,
}));
```

3. Reset it in `beforeEach`:

```ts
agentNames.current = new Map();
```

4. Add one test to each describe block. Use the suite's existing fixtures and render helper; the skill fixture id is `poster` in all three suites.

For `history-list.test.tsx` (its helpers are `install(...)` and `renderHistory()`):

```ts
it("prefers the backend agent's name over the catalog name", () => {
  agentNames.current = new Map([["poster", "Poster helper"]]);
  install([generation()]);
  renderHistory();
  expect(screen.getByText("Poster helper")).toBeInTheDocument();
  expect(screen.queryByText("Poster")).not.toBeInTheDocument();
});
```

For `works-list.test.tsx` (its helper is `renderWorks()`; the `beforeEach` already installs the generation, asset, and `POSTER` catalog):

```ts
it("prefers the backend agent's name over the catalog name", () => {
  agentNames.current = new Map([["poster", "Poster helper"]]);
  renderWorks();
  expect(screen.getByText("Poster helper")).toBeInTheDocument();
  expect(screen.queryByText("Poster")).not.toBeInTheDocument();
});
```

For `generation-detail.test.tsx` (its helpers are `read(...)` and `renderDetail()`; the `beforeEach` already installs `read(detail())` and `read([POSTER])`):

```ts
it("prefers the backend agent's name over the catalog name", () => {
  agentNames.current = new Map([["poster", "Poster helper"]]);
  renderDetail();
  expect(screen.getByText("Poster helper")).toBeInTheDocument();
  expect(screen.queryByText("Poster")).not.toBeInTheDocument();
});
```

- [ ] **Step 6: Run the three suites**

Run:
```bash
pnpm -C packages/views exec vitest run \
  aurora/history-list.test.tsx \
  aurora/works-list.test.tsx \
  aurora/generation-detail.test.tsx
```
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add packages/views/aurora/use-aurora-agent-names.ts \
  packages/views/aurora/history-list.tsx \
  packages/views/aurora/works-list.tsx \
  packages/views/aurora/generation-detail.tsx \
  packages/views/aurora/history-list.test.tsx \
  packages/views/aurora/works-list.test.tsx \
  packages/views/aurora/generation-detail.test.tsx
git commit -m "feat(aurora): prefer backend agent names in history and works"
```

---

### Task 6: Final verification

**Files:** none expected; this task only verifies and fixes stragglers.

- [ ] **Step 1: Run the full views checks**

```bash
pnpm --filter @multica/views typecheck
pnpm --filter @multica/views lint
pnpm --filter @multica/views test
```
Expected: all pass. If lint reports an unused import left by the refactor, remove it and re-run.

- [ ] **Step 2: Run the Aurora app checks**

```bash
pnpm --filter @multica/aurora typecheck
pnpm --filter @multica/aurora test
```
Expected: pass.

- [ ] **Step 3: Prove the backend is untouched**

```bash
git diff --stat main -- server
```
Expected: no output.

- [ ] **Step 4: Prove the hardcoded layer is gone**

```bash
grep -rn "toolGuidance\|RECOMMENDED_TOOL_IDS\|orderTools\|auroraCategoryLabel\|auroraInputLabel" packages/views apps/aurora || true
git diff --check
```
Expected: no grep hits and no whitespace errors.

- [ ] **Step 5: Commit any verification fixes**

If Step 1 or 2 required a source fix:
```bash
git add -A
git commit -m "chore(aurora): clean up after the agent-directory refactor"
```
If nothing changed, skip this step.

---

## Manual Acceptance (not automated)

After the plan is implemented and the app runs against a backend with both Aurora carriers and ordinary agents:

1. Open `/<workspace>/skills`.
2. Confirm every non-archived agent the backend shows to you appears, with the same name, description, avatar, and enabled skills as the agents page.
3. Confirm an ordinary agent renders read-only with no price and cannot open the composer.
4. Confirm an Aurora carrier opens the composer, shows its own name/description, and its conversation starters fill the prompt.
5. Confirm archiving an agent in the backend removes it from the directory after a refetch.
