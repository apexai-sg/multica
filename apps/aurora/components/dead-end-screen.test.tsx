import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { NavigationProvider, type NavigationAdapter } from "@multica/views/navigation";
import { renderWithI18n } from "../test/render";

const { logout, state } = vi.hoisted(() => ({ logout: vi.fn(), state: { workspaces: [] as { slug: string }[] } }));
vi.mock("@multica/views/auth", () => ({ useLogout: () => logout }));
vi.mock("@multica/core/workspace", () => ({ useWorkspaceList: () => state }));

import { DeadEndScreen } from "./dead-end-screen";
import { WorkspaceUnavailable } from "./workspace-unavailable";

const adapter: NavigationAdapter = {
  push: vi.fn(), replace: vi.fn(), back: vi.fn(), pathname: "/missing/skills",
  searchParams: new URLSearchParams(), hash: "", getShareableUrl: (path) => path,
};
beforeEach(() => { vi.clearAllMocks(); state.workspaces = []; });
afterEach(cleanup);

function renderScreen(unavailable = false) {
  return renderWithI18n(<NavigationProvider value={adapter}>
    {unavailable ? <WorkspaceUnavailable /> : <DeadEndScreen title="Nothing here" description="The link may be wrong." />}
  </NavigationProvider>);
}

describe("DeadEndScreen", () => {
  it("states the dead end in the centered design and offers logout when no workspace exists", async () => {
    const user = userEvent.setup();
    const { container } = renderScreen();
    expect(container.firstElementChild).toHaveClass("aurora-centered");
    expect(screen.getByRole("heading", { level: 1, name: "Nothing here" }).parentElement).toHaveClass("aurora-empty");
    expect(screen.getByText("The link may be wrong.")).toBeVisible();
    expect(screen.queryByRole("button", { name: "Open my workspace" })).toBeNull();
    await user.click(screen.getByRole("button", { name: "Log out" }));
    expect(logout).toHaveBeenCalledTimes(1);
  });

  it("preserves both recovery exits and resolves the first workspace to its skills route", async () => {
    state.workspaces = [{ slug: "acme" }, { slug: "other" }];
    const user = userEvent.setup();
    renderScreen();
    await user.click(screen.getByRole("button", { name: "Open my workspace" }));
    expect(adapter.replace).toHaveBeenCalledWith("/acme/skills");
    await user.click(screen.getByRole("button", { name: "Log out" }));
    expect(logout).toHaveBeenCalledTimes(1);
  });

  it("clears the stale workspace cookie on the unavailable branch without losing recovery", () => {
    document.cookie = "last_workspace_slug=missing; path=/";
    renderScreen(true);
    expect(document.cookie).not.toContain("last_workspace_slug=");
    expect(screen.getByRole("heading", { level: 1 })).toBeVisible();
    expect(screen.getByRole("button", { name: "Log out" })).toBeVisible();
  });
});
