import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { NavigationProvider, type NavigationAdapter } from "@multica/views/navigation";
import type { Agent } from "@multica/core/types";

import { apexTools } from "./tool-definitions";
import { useToolSubmission } from "./use-tool-submission";

const mocks = vi.hoisted(() => ({
  uploadFile: vi.fn(),
  listAgents: vi.fn(),
  listMembers: vi.fn(),
  listTools: vi.fn(),
  createToolRun: vi.fn(),
}));

const toolsResult = (tools: unknown[]) => ({ value: tools, degraded: false });

vi.mock("@multica/core/hooks", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/hooks")>()),
  useWorkspaceId: () => "ws-1",
}));

vi.mock("@multica/core/auth", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/auth")>()),
  useAuthStore: Object.assign(
    (selector: (state: { user: { id: string } }) => unknown) =>
      selector({ user: { id: "u-admin" } }),
    { getState: () => ({ user: { id: "u-admin" } }) },
  ),
}));

vi.mock("@multica/core/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/api")>();
  return {
    ...actual,
    api: { ...actual.api, ...mocks },
  };
});

// The real options object closes over the real listAuroraTools, so replacing
// only the client would still read through it. The query function is replaced
// too, and wraps the fixture in the ParseResult shape auroraToolsOptions
// resolves to.
vi.mock("@multica/core/aurora", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/aurora")>()),
  auroraToolsOptions: (wsId: string) => ({
    queryKey: ["aurora", wsId, "tools"],
    queryFn: async () => {
      const result = await mocks.listTools(wsId);
      // A bare array is the ordinary fixture; a ParseResult lets a test choose
      // the degraded path.
      return Array.isArray(result) ? toolsResult(result) : result;
    },
  }),
  createAuroraToolRun: mocks.createToolRun,
}));

const tool = apexTools.find((candidate) => candidate.id === "poster")!;

const mapping = (id: string, expertSlug: string, expertName = "广告文案专家") => ({
  id,
  expertSlug,
  expertName,
});

// A shared workspace agent, assignable by any member of `ws-1`.
const agent = (overrides: Partial<Agent> = {}): Agent =>
  ({
    id: "agent-1",
    name: "Mika",
    system_key: "mika",
    archived_at: null,
    runtime_id: "runtime-1",
    runtime_bound: true,
    owner_id: null,
    permission_mode: "public_to",
    invocation_targets: [{ target_type: "workspace", target_id: "ws-1" }],
    ...overrides,
  }) as Agent;

const carrier = (systemKey: string, overrides: Partial<Agent> = {}) =>
  agent({ system_key: systemKey, ...overrides });

const file = (name: string) => new File(["bytes"], name, { type: "image/png" });

const push = vi.fn();

function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const adapter: NavigationAdapter = {
    push,
    replace: vi.fn(),
    back: vi.fn(),
    pathname: "/acme/tools/poster",
    searchParams: new URLSearchParams(),
    hash: "",
    getShareableUrl: (path: string) => `https://aod.example.test${path}`,
  };
  return (
    <QueryClientProvider client={client}>
      <NavigationProvider value={adapter}>{children}</NavigationProvider>
    </QueryClientProvider>
  );
}

async function render(subject = tool) {
  const view = renderHook(() => useToolSubmission(subject, "acme"), { wrapper });
  await waitFor(() => expect(view.result.current.status).not.toBe("loading"));
  return view;
}

beforeEach(() => {
  vi.stubEnv("NEXT_PUBLIC_WEB_ORIGIN", "");
  mocks.uploadFile.mockReset();
  mocks.uploadFile.mockResolvedValue({ id: "att-1" });
  mocks.listAgents.mockReset();
  mocks.listAgents.mockResolvedValue([
    carrier("aurora:marketing-ad-copywriting"),
  ]);
  mocks.listMembers.mockReset();
  mocks.listMembers.mockResolvedValue([{ user_id: "u-admin", role: "admin" }]);
  mocks.listTools.mockReset();
  mocks.listTools.mockResolvedValue([
    mapping("poster", "marketing-ad-copywriting"),
    mapping("copywriting", "marketing-ad-copywriting"),
  ]);
  mocks.createToolRun.mockReset();
  mocks.createToolRun.mockResolvedValue({
    identifier: "MUL-42",
    issueId: "i-1",
    expert: { slug: "marketing-ad-copywriting", name: "广告文案专家" },
  });
  push.mockReset();
});

afterEach(() => {
  vi.unstubAllEnvs();
});

describe("useToolSubmission", () => {
  it("submits through the tool-run endpoint with the uploaded attachment ids", async () => {
    const { result } = await render();

    await act(async () => {
      await result.current.submit({ source: "海报", files: [file("p.png")] });
    });

    expect(mocks.createToolRun).toHaveBeenCalledWith(
      "poster",
      expect.objectContaining({
        source: "海报",
        fileNames: ["p.png"],
        attachmentIds: ["att-1"],
      }),
    );
    expect(push).toHaveBeenCalledWith("/acme/issues/MUL-42");
  });

  it("reports no-expert for a tool the server does not map", async () => {
    mocks.listTools.mockResolvedValue([
      mapping("other", "x", "X"),
    ]);
    const { result } = await render();

    expect(result.current.status).toBe("no-expert");
    expect(result.current.canSubmit).toBe(false);

    await act(async () => {
      await result.current.submit({ source: "海报" });
    });

    expect(mocks.createToolRun).not.toHaveBeenCalled();
  });

  it("reports an unreadable tool directory as an error, not as a missing executor", async () => {
    // A degraded body parses to an empty directory; calling that "no-expert"
    // would claim the server is unconfigured and offer no reload.
    mocks.listTools.mockResolvedValue({ value: [], degraded: true });
    const { result } = await render();

    expect(result.current.status).toBe("error");
    expect(result.current.canSubmit).toBe(false);
  });

  it("reads the agent list only after the tool directory has answered", async () => {
    // The tools read is what seeds the carriers; reading agents in parallel
    // would resolve "no-agent" from a list that predates the seed.
    let releaseTools!: (tools: unknown[]) => void;
    mocks.listTools.mockReturnValueOnce(
      new Promise((resolve) => {
        releaseTools = resolve;
      }),
    );
    const view = renderHook(() => useToolSubmission(tool, "acme"), { wrapper });

    expect(view.result.current.status).toBe("loading");
    expect(mocks.listAgents).not.toHaveBeenCalled();

    await act(async () => {
      releaseTools([mapping("poster", "marketing-ad-copywriting")]);
    });

    await waitFor(() => expect(view.result.current.status).toBe("ready"));
    expect(mocks.listAgents).toHaveBeenCalledTimes(1);
  });

  it("keeps the button disabled when the mapped carrier has no runtime", async () => {
    mocks.listAgents.mockResolvedValue([
      carrier("aurora:marketing-ad-copywriting", {
        runtime_bound: false,
        runtime_id: "",
      }),
    ]);
    const { result } = await render();

    expect(result.current.status).toBe("no-runtime");
    expect(result.current.canSubmit).toBe(false);
  });

  it("reports no-agent when the mapped carrier is not in the assignable list", async () => {
    mocks.listAgents.mockResolvedValue([carrier("aurora:media-voiceover")]);
    const { result } = await render();

    expect(result.current.status).toBe("no-agent");
  });

  it("skips another member's private carrier, which the list shows to admins but the assign gate refuses", async () => {
    mocks.listMembers.mockResolvedValue([{ user_id: "u-admin", role: "admin" }]);
    mocks.listAgents.mockResolvedValue([
      carrier("aurora:marketing-ad-copywriting", {
        owner_id: "u-other",
        permission_mode: "private",
        invocation_targets: [],
      }),
    ]);
    const { result } = await render();

    expect(result.current.status).toBe("no-agent");
  });

  it("stays locked after the run exists so a second click cannot create another one", async () => {
    const { result } = await render();

    await act(async () => {
      await result.current.submit({ source: "海报" });
    });

    // The route has not replaced this page yet; re-enabling here would let the
    // user create a duplicate issue and run the expert twice.
    expect(result.current.pending).toBe(true);

    await act(async () => {
      await result.current.submit({ source: "海报" });
    });

    expect(mocks.createToolRun).toHaveBeenCalledTimes(1);
    expect(push).toHaveBeenCalledTimes(1);
  });

  it("reports a failed upload without creating a run or navigating", async () => {
    mocks.uploadFile.mockRejectedValueOnce(new Error("File exceeds 100 MB limit"));
    const { result } = await render();

    await act(async () => {
      await result.current.submit({ files: [file("clip.mp4")] });
    });

    expect(mocks.createToolRun).not.toHaveBeenCalled();
    expect(push).not.toHaveBeenCalled();
    expect(result.current.error).toBe("File exceeds 100 MB limit");
    expect(result.current.pending).toBe(false);
  });

  it("reports a failed run creation and releases the button", async () => {
    mocks.createToolRun.mockRejectedValueOnce(
      new Error("workspace has reached its issue limit"),
    );
    const { result } = await render();

    await act(async () => {
      await result.current.submit({ source: "海报" });
    });

    expect(result.current.error).toBe("workspace has reached its issue limit");
    expect(result.current.pending).toBe(false);
    expect(result.current.canSubmit).toBe(true);
    expect(push).not.toHaveBeenCalled();
  });

  it("surfaces the issue instead of guessing a link when the identifier has no PREFIX-N shape", async () => {
    mocks.createToolRun.mockResolvedValueOnce({
      identifier: "#42",
      issueId: "i-1",
      expert: { slug: "marketing-ad-copywriting", name: "广告文案专家" },
    });
    const { result } = await render();

    await act(async () => {
      await result.current.submit({ source: "海报" });
    });

    expect(push).not.toHaveBeenCalled();
    expect(result.current.createdIdentifier).toBe("#42");
    expect(result.current.pending).toBe(false);
  });

  it("clears the previous error when the user submits again", async () => {
    mocks.createToolRun.mockRejectedValueOnce(new Error("boom"));
    const { result } = await render();

    await act(async () => {
      await result.current.submit({ source: "海报" });
    });
    expect(result.current.error).toBe("boom");

    await act(async () => {
      await result.current.submit({ source: "海报" });
    });

    expect(result.current.error).toBeNull();
  });

  it("reports an unreadable tool directory as an error and reloads all three lists on demand", async () => {
    mocks.listTools.mockRejectedValueOnce(new Error("503"));
    const { result } = await render();

    expect(result.current.status).toBe("error");

    await act(async () => {
      result.current.reloadAgents();
    });

    await waitFor(() => expect(result.current.status).toBe("ready"));
    expect(mocks.listTools).toHaveBeenCalledTimes(2);
    expect(mocks.listAgents).toHaveBeenCalledTimes(2);
  });

  it("keeps the status loading until the tool directory answers", async () => {
    mocks.listTools.mockReturnValue(new Promise(() => undefined));
    const view = renderHook(() => useToolSubmission(tool, "acme"), { wrapper });

    expect(view.result.current.status).toBe("loading");
    expect(view.result.current.canSubmit).toBe(false);
  });

  it("does not start a second submission while one is still uploading", async () => {
    let release!: (value: { id: string }) => void;
    mocks.uploadFile.mockReturnValueOnce(
      new Promise((resolve) => {
        release = resolve;
      }),
    );
    const { result } = await render();

    let first!: Promise<void>;
    act(() => {
      first = result.current.submit({ files: [file("poster.png")] });
    });
    await waitFor(() => expect(result.current.pending).toBe(true));

    await act(async () => {
      await result.current.submit({ files: [file("poster.png")] });
    });

    expect(mocks.uploadFile).toHaveBeenCalledTimes(1);
    expect(mocks.createToolRun).not.toHaveBeenCalled();

    await act(async () => {
      release({ id: "att-1" });
      await first;
    });
    expect(mocks.createToolRun).toHaveBeenCalledTimes(1);
  });
});

describe("apexTools", () => {
  it("ships no fabricated agent keys", () => {
    // The 14 keys this file used to carry were retired skill IDs; the retired
    // skill carriers are no longer created anywhere, so every lookup silently
    // fell through. Reintroduce a key only with a mapping the backend can honour.
    const keyed = apexTools.filter((candidate) =>
      "agentKey" in (candidate as unknown as Record<string, unknown>),
    );
    expect(keyed).toEqual([]);
  });

  it("keeps the shipped tool ids in step with the server mapping", () => {
    // The same 18-id list is asserted by TestEveryShippedToolHasACatalogExpert.
    expect(apexTools.map((t) => t.id).sort()).toEqual([
      "avatar-video", "copywriting", "face-swap", "image-retouch", "pdf-reader",
      "portraits", "poster", "presentation", "product-images", "rednote-images",
      "resume", "short-video", "spreadsheet", "video-edit", "video-transcript",
      "video-translate", "voiceover", "website",
    ]);
  });
});
