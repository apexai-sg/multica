import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { AuroraSkill } from "@multica/core/aurora";
import type { Agent } from "@multica/core/types";
import { renderWithI18n } from "../test/i18n";
import { stubNavigationAdapter } from "../test/navigation";
import { NavigationProvider } from "../navigation";

const mocks = vi.hoisted(() => ({
  directory: vi.fn(),
  skills: vi.fn(),
  create: vi.fn(),
  createIssue: vi.fn(),
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

vi.mock("@multica/core/issues/mutations", () => ({
  useCreateIssue: () => ({
    mutateAsync: mocks.createIssue,
    isPending: false,
  }),
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

const MIKA = agent({
  id: "a-mika",
  name: "Mika",
  system_key: "mika",
});

const RETIRED = agent({
  id: "a-retired",
  name: "Retired carrier",
  system_key: "aurora:retired-skill",
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
    query: {
      // A successful read is the default; a first-load failure passes
      // `data: undefined` explicitly.
      data: agents,
      isPending: false,
      isError: false,
      refetch: vi.fn(),
      ...query,
    },
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
    <QueryClientProvider client={new QueryClient()}>
      <NavigationProvider value={stubNavigationAdapter()}>
        <SkillDirectory />
      </NavigationProvider>
    </QueryClientProvider>,
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
    expect(first).not.toHaveTextContent("歌词创作专家");
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
    expect(screen.getByText("1 assistant shown")).toBeVisible();
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

  it("filters by declared outputs while keeping ordinary agents discoverable", async () => {
    const user = userEvent.setup();
    renderDirectory();
    expect(screen.queryByRole("button", { name: "Image tools" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Writing tools" }));
    expect(cards()).toHaveLength(1);
    expect(cards()[0]).toHaveTextContent("Lyrics helper");
    await user.type(screen.getByRole("searchbox"), "Runbooks");
    expect(cards()).toHaveLength(0);
    await user.click(screen.getByRole("button", { name: "AI assistants", exact: true }));
    expect(cards()).toHaveLength(1);
    expect(cards()[0]).toHaveTextContent("Ops assistant");
    await user.clear(screen.getByRole("searchbox"));
    await user.click(screen.getByRole("button", { name: "All tools" }));
    expect(cards()).toHaveLength(2);
  });

  it("returns to all tools when a refetch removes the selected output", async () => {
    const user = userEvent.setup();
    const view = renderDirectory();
    await user.click(screen.getByRole("button", { name: "Writing tools" }));
    directory([OPS]);
    view.rerender(
      <QueryClientProvider client={new QueryClient()}>
        <NavigationProvider value={stubNavigationAdapter()}>
          <SkillDirectory />
        </NavigationProvider>
      </QueryClientProvider>,
    );
    expect(screen.getByRole("button", { name: "All tools" })).toHaveAttribute("aria-pressed", "true");
    expect(cards()).toHaveLength(1);
    expect(cards()[0]).toHaveTextContent("Ops assistant");
  });

  it("makes every listed agent actionable", async () => {
    const user = userEvent.setup();
    renderDirectory();
    expect(cards()[0]!.tagName).toBe("BUTTON");
    expect(cards()[1]!.tagName).toBe("BUTTON");

    await user.click(cards()[0]!);
    expect(await screen.findByRole("dialog")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Generate" })).toBeDisabled();
    await user.keyboard("{Escape}");

    await user.click(cards()[1]!);
    expect(await screen.findByRole("dialog")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Start task" })).toBeDisabled();
    expect(screen.queryByText("View only")).not.toBeInTheDocument();
  });

  it("shows no price for an ordinary agent but keeps it usable", () => {
    renderDirectory();
    expect(cards()[1]).not.toHaveTextContent(/\d+ credits/);
    expect(cards()[1]).not.toHaveTextContent("View only");
    expect(cards()[0]).toHaveTextContent("60 credits");
  });

  it("hides system-generated agents with no current catalog entry", () => {
    directory([...AGENTS, MIKA, RETIRED]);
    renderDirectory();
    expect(cards()).toHaveLength(2);
    expect(screen.queryByText("Mika")).not.toBeInTheDocument();
    expect(screen.queryByText("Retired carrier")).not.toBeInTheDocument();
  });

  it("shows the skeleton until both the agent list and the catalog resolve", () => {
    mocks.skills.mockReturnValue({ data: undefined, isPending: true });
    renderDirectory();
    expect(cards()).toHaveLength(0);
    expect(screen.queryByText("Ops assistant")).not.toBeInTheDocument();
  });

  it("keeps ordinary agents usable and reports an unreadable catalog", async () => {
    const user = userEvent.setup();
    const refetch = vi.fn();
    mocks.skills.mockReturnValue({
      data: undefined,
      isPending: false,
      isError: true,
      refetch,
    });
    renderDirectory();
    expect(cards()).toHaveLength(1);
    expect(cards()[0]!.tagName).toBe("BUTTON");
    expect(cards()[0]).toHaveTextContent("Ops assistant");
    expect(
      screen.getByText("Could not load Aurora assistants. Retry to see them."),
    ).toBeVisible();
    expect(screen.queryByText("0 assistants shown")).not.toBeInTheDocument();
    expect(screen.queryByText("No assistants yet")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Try again" }));
    expect(refetch).toHaveBeenCalledOnce();
  });

  it("does not claim an empty workspace when only carriers exist and the catalog failed", () => {
    directory([LYRICS_AGENT]);
    mocks.skills.mockReturnValue({
      data: undefined,
      isPending: false,
      isError: true,
    });
    renderDirectory();
    expect(cards()).toHaveLength(0);
    expect(
      screen.getByText("Could not load Aurora assistants. Retry to see them."),
    ).toBeVisible();
    expect(screen.queryByText("No assistants yet")).not.toBeInTheDocument();
    expect(screen.queryByText("0 assistants shown")).not.toBeInTheDocument();
  });

  it.each([
    { isPending: true, data: undefined },
    { isPending: false, isError: true, refetch: vi.fn(), data: undefined },
  ])(
    "does not present an unread agent list as zero assistants: %j",
    (query) => {
      directory([], query);
      renderDirectory();
      expect(screen.queryByText("0 assistants shown")).not.toBeInTheDocument();
    },
  );

  it("reports a successfully read empty agent list honestly", () => {
    directory([]);
    renderDirectory();
    expect(screen.getByText("0 assistants shown")).toBeVisible();
    expect(screen.getByText("No assistants yet")).toBeVisible();
  });

  it("offers retry for a failed agent list", async () => {
    const user = userEvent.setup();
    const refetch = vi.fn();
    directory([], { isPending: false, isError: true, refetch, data: undefined });
    renderDirectory();
    await user.click(screen.getByRole("button", { name: "Try again" }));
    expect(refetch).toHaveBeenCalledOnce();
  });

  it("keeps the last readable list when a background refetch fails", () => {
    directory(AGENTS, { isError: true });
    renderDirectory();
    expect(cards()).toHaveLength(2);
    expect(
      screen.queryByRole("button", { name: "Try again" }),
    ).not.toBeInTheDocument();
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
