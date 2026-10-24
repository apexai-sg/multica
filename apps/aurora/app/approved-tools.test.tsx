import { useState, type ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { NavigationProvider, type NavigationAdapter } from "@multica/views/navigation";
import SkillsPage from "./[workspaceSlug]/skills/page";
import ToolPage from "./[workspaceSlug]/tools/[toolId]/page";
import { WorkspaceChrome } from "../components/workspace-chrome";
import { apexTools } from "../components/tools/tool-definitions";

vi.mock("next/navigation", () => ({ notFound: () => { throw new Error("NEXT_NOT_FOUND"); } }));
vi.mock("../components/tools/use-tool-submission", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../components/tools/use-tool-submission")>()),
  useToolSubmission: () => ({
    submit: vi.fn(),
    pending: false,
    error: null,
    createdIdentifier: null,
    canSubmit: true,
    status: "ready" as const,
    reloadAgents: vi.fn(),
  }),
}));
vi.mock("../components/aurora-shell", () => ({
  AuroraShell: ({ children }: { children: ReactNode }) => <><aside aria-label="工作区导航" /><main>{children}</main></>,
}));
afterEach(cleanup);

function Harness({ initialPath, children }: { initialPath: string; children: ReactNode }) {
  const [path, setPath] = useState(initialPath);
  const url = new URL(path, "https://apex.example");
  const adapter: NavigationAdapter = {
    pathname: url.pathname, searchParams: url.searchParams, hash: "",
    push: setPath, replace: setPath, back: vi.fn(), getShareableUrl: (href) => href,
  };
  return <NavigationProvider value={adapter}>
    <WorkspaceChrome slug="acme" userName="测试用户">{children}</WorkspaceChrome>
  </NavigationProvider>;
}

describe("approved design in production routes", () => {
  it("serves the approved directory with all 18 production links in the agreed order", async () => {
    render(<Harness initialPath="/acme/skills">{await SkillsPage({ params: Promise.resolve({ workspaceSlug: "acme" }) })}</Harness>);
    const links = screen.getAllByRole("link", { name: /^打开/ });
    expect(links).toHaveLength(18);
    expect(links.slice(0, 3).map((link) => link.getAttribute("href"))).toEqual([
      "/acme/tools/face-swap", "/acme/tools/video-transcript", "/acme/tools/product-images",
    ]);
    for (const tool of apexTools) {
      expect(screen.getByRole("link", { name: `打开${tool.name}` })).toHaveAttribute("href", `/acme/tools/${tool.id}`);
    }
    expect(screen.getByRole("link", { name: "现有工具库" })).toHaveAttribute("href", "/acme/skills/connected");
    expect(document.querySelector('a[href^="/preview"]')).toBeNull();
  });

  it("keeps a filtered search in tool links and restores it from the URL", async () => {
    const user = userEvent.setup();
    const page = await SkillsPage({ params: Promise.resolve({ workspaceSlug: "acme" }) });
    render(<Harness initialPath="/acme/skills?category=video&q=%E6%96%87%E6%A1%88">{page}</Harness>);
    expect(screen.getByRole("searchbox", { name: "搜索工具" })).toHaveValue("文案");
    expect(screen.getByRole("button", { name: "视频工具" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("link", { name: "打开视频文案提取" })).toHaveAttribute("href", "/acme/tools/video-transcript?category=video&q=%E6%96%87%E6%A1%88");
    await user.clear(screen.getByRole("searchbox", { name: "搜索工具" }));
    await user.click(screen.getByRole("button", { name: "办公助手" }));
    expect(screen.getAllByRole("link", { name: /^打开/ })).toHaveLength(4);
    expect(screen.getByRole("link", { name: "打开PPT 制作" })).toHaveAttribute("href", "/acme/tools/presentation?category=office");
  });

  it.each(apexTools)("opens $id as a full-page workspace through its production route", async (tool) => {
    const page = await ToolPage({ params: Promise.resolve({ workspaceSlug: "acme", toolId: tool.id }) });
    render(<Harness initialPath={`/acme/tools/${tool.id}?category=video&q=test`}>{page}</Harness>);
    expect(screen.getByRole("heading", { level: 1, name: tool.id === "face-swap" ? "视频换脸" : tool.name })).toBeVisible();
    expect(screen.queryByRole("complementary", { name: "工作区导航" })).toBeNull();
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.getAllByRole("main")).toHaveLength(1);
    expect(screen.getByRole("link", { name: "返回工具列表" })).toHaveAttribute("href", "/acme/skills?category=video&q=test");
    expect(screen.getByLabelText("测试用户")).toBeVisible();
  });

  it("returns not-found for unknown tools instead of a generic or empty workspace", async () => {
    await expect(ToolPage({ params: Promise.resolve({ workspaceSlug: "acme", toolId: "not-a-tool" }) })).rejects.toThrow("NEXT_NOT_FOUND");
  });
});
