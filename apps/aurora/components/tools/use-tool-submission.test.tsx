import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { NavigationProvider, type NavigationAdapter } from "@multica/views/navigation";
import type { Agent, Issue } from "@multica/core/types";

import { apexTools } from "./tool-definitions";
import { useToolSubmission } from "./use-tool-submission";

const mocks = vi.hoisted(() => ({
  uploadFile: vi.fn(),
  createIssue: vi.fn(),
  listAgents: vi.fn(),
}));

vi.mock("@multica/core/hooks", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/hooks")>()),
  useWorkspaceId: () => "ws-1",
}));

vi.mock("@multica/core/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/api")>();
  return {
    ...actual,
    api: { ...actual.api, ...mocks },
  };
});

const tool = apexTools.find((candidate) => candidate.id === "poster")!;

const agent = (overrides: Partial<Agent> = {}): Agent =>
  ({
    id: "agent-1",
    name: "Mika",
    system_key: "mika",
    archived_at: null,
    runtime_id: "runtime-1",
    runtime_bound: true,
    ...overrides,
  }) as Agent;

const issue = (identifier = "MUL-42"): Issue => ({ identifier }) as Issue;

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
  mocks.createIssue.mockReset();
  mocks.listAgents.mockReset();
  mocks.listAgents.mockResolvedValue([agent()]);
  mocks.uploadFile.mockResolvedValue({ id: "att-1" });
  mocks.createIssue.mockResolvedValue(issue());
  push.mockReset();
});

afterEach(() => {
  vi.unstubAllEnvs();
});

describe("useToolSubmission", () => {
  it("opens the issue it created, assigned to the workspace assistant", async () => {
    const { result } = await render();

    await act(async () => {
      await result.current.submit({ source: "秋季新品海报", files: [file("product.png")] });
    });

    expect(mocks.createIssue).toHaveBeenCalledWith(
      expect.objectContaining({
        title: expect.stringContaining("海报制作"),
        description: expect.stringContaining("秋季新品海报"),
        assignee_type: "agent",
        assignee_id: "agent-1",
        attachment_ids: ["att-1"],
        allow_duplicate: true,
      }),
    );
    expect(push).toHaveBeenCalledWith("/acme/issues/MUL-42");
  });

  it("lists every uploaded file as an attachment, in the order the user chose them", async () => {
    mocks.uploadFile.mockResolvedValueOnce({ id: "att-1" }).mockResolvedValueOnce({ id: "att-2" });
    const { result } = await render();

    await act(async () => {
      await result.current.submit({ files: [file("scope.png"), file("face.png")] });
    });

    expect(mocks.createIssue.mock.calls[0]![0].attachment_ids).toEqual(["att-1", "att-2"]);
  });

  it("writes the tool, the requirement, the settings and the media limitation into the description", async () => {
    const { result } = await render();

    await act(async () => {
      await result.current.submit({ source: "  做一张秋季海报  ", values: { ratio: "1:1" } });
    });

    const description: string = mocks.createIssue.mock.calls[0]![0].description;
    expect(description).toContain("海报制作");
    expect(description).toContain("做一张秋季海报");
    expect(description).toContain("ratio: 1:1");
    expect(description).toContain("无法生成真实媒体文件");
  });

  it("stays locked after the issue exists so a second click cannot create another one", async () => {
    const { result } = await render();

    await act(async () => {
      await result.current.submit({ source: "海报" });
    });

    // The route has not replaced this page yet; re-enabling here would let the
    // user create a duplicate issue and run the agent twice.
    expect(result.current.pending).toBe(true);

    await act(async () => {
      await result.current.submit({ source: "海报" });
    });

    expect(mocks.createIssue).toHaveBeenCalledTimes(1);
    expect(push).toHaveBeenCalledTimes(1);
  });

  it("reports a failed upload without creating an issue or navigating", async () => {
    mocks.uploadFile.mockRejectedValueOnce(new Error("File exceeds 100 MB limit"));
    const { result } = await render();

    await act(async () => {
      await result.current.submit({ files: [file("clip.mp4")] });
    });

    expect(mocks.createIssue).not.toHaveBeenCalled();
    expect(push).not.toHaveBeenCalled();
    expect(result.current.error).toBe("File exceeds 100 MB limit");
    expect(result.current.pending).toBe(false);
  });

  it("reports a failed issue creation and releases the button", async () => {
    mocks.createIssue.mockRejectedValueOnce(new Error("workspace has reached its issue limit"));
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
    mocks.createIssue.mockResolvedValueOnce(issue("#42"));
    const { result } = await render();

    await act(async () => {
      await result.current.submit({ source: "海报" });
    });

    expect(push).not.toHaveBeenCalled();
    expect(result.current.createdIdentifier).toBe("#42");
    expect(result.current.pending).toBe(false);
  });

  it("clears the previous error when the user submits again", async () => {
    mocks.createIssue.mockRejectedValueOnce(new Error("boom"));
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

  it("reports that the workspace has no assistant instead of ignoring the click", async () => {
    mocks.listAgents.mockResolvedValue([]);
    const { result } = await render();

    expect(result.current.status).toBe("no-agent");
    expect(result.current.canSubmit).toBe(false);

    await act(async () => {
      await result.current.submit({ source: "海报" });
    });

    expect(result.current.error).not.toBeNull();
    expect(mocks.createIssue).not.toHaveBeenCalled();
  });

  it("prefers the workspace assistant over the oldest agent", async () => {
    mocks.listAgents.mockResolvedValue([
      agent({ id: "old-agent", name: "Older", system_key: undefined }),
      agent({ id: "mika-agent", name: "Mika", system_key: "mika" }),
    ]);
    const { result } = await render();

    expect(result.current.agent?.id).toBe("mika-agent");
  });

  it("never assigns work to an archived assistant", async () => {
    mocks.listAgents.mockResolvedValue([
      agent({ id: "archived", archived_at: "2026-09-01T00:00:00Z" }),
      agent({ id: "live", system_key: undefined }),
    ]);
    const { result } = await render();

    expect(result.current.agent?.id).toBe("live");
  });

  it("skips an assistant whose runtime was deleted, because the issue would never run", async () => {
    mocks.listAgents.mockResolvedValue([
      agent({ id: "unbound", system_key: "mika", runtime_id: "", runtime_bound: false }),
      agent({ id: "bound", system_key: undefined }),
    ]);
    const { result } = await render();

    expect(result.current.agent?.id).toBe("bound");
  });

  it("reports that every assistant lost its runtime", async () => {
    mocks.listAgents.mockResolvedValue([
      agent({ id: "unbound", runtime_id: "", runtime_bound: false }),
    ]);
    const { result } = await render();

    expect(result.current.status).toBe("no-runtime");
    expect(result.current.canSubmit).toBe(false);
  });

  it("reports an assistant-list failure and reloads it on demand", async () => {
    mocks.listAgents.mockRejectedValueOnce(new Error("503"));
    const { result } = await render();

    expect(result.current.status).toBe("error");

    await act(async () => {
      result.current.reloadAgents();
    });

    await waitFor(() => expect(result.current.status).toBe("ready"));
    expect(mocks.listAgents).toHaveBeenCalledTimes(2);
  });

  it("keeps the status loading until the assistant list answers", async () => {
    mocks.listAgents.mockReturnValue(new Promise(() => undefined));
    const view = renderHook(() => useToolSubmission(tool, "acme"), { wrapper });

    expect(view.result.current.status).toBe("loading");
    expect(view.result.current.canSubmit).toBe(false);
  });

  it("does not start a second submission while one is still uploading", async () => {
    let release!: (value: { id: string }) => void;
    mocks.uploadFile.mockReturnValueOnce(new Promise((resolve) => { release = resolve; }));
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
    expect(mocks.createIssue).not.toHaveBeenCalled();

    await act(async () => {
      release({ id: "att-1" });
      await first;
    });
    expect(mocks.createIssue).toHaveBeenCalledTimes(1);
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
});
