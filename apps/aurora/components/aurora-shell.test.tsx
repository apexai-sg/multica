import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Sheet, SheetContent, SheetTitle, SheetTrigger } from "@multica/ui/components/ui/sheet";
import type { NavigationAdapter } from "@multica/views/navigation";
import { NavigationProvider } from "@multica/views/navigation";
import { renderWithI18n } from "../test/render";

// Logging out tears down the whole client session, which needs the CoreProvider
// the shell is normally mounted under. The shell's job here is to offer the
// action, not to perform it.
const { logout } = vi.hoisted(() => ({ logout: vi.fn() }));
vi.mock("@multica/views/auth", () => ({ useLogout: () => logout }));
// Shared data matrices live in views; the app verifies wiring and responsiveness.
vi.mock("@multica/core/auth", () => {
  const state = { user: { id: "u1", name: "Hannah Han", email: "hannah@apex.ai", avatar_url: null, onboarded_at: null, onboarding_questionnaire: {}, starter_content_state: null, language: null, profile_description: "", timezone: null, created_at: "", updated_at: "" } };
  return { useAuthStore: Object.assign((selector: (value: typeof state) => unknown) => selector(state), { getState: () => state }) };
});
vi.mock("@multica/core/aurora", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/aurora")>()),
  useAuroraBalance: () => ({ data: { value: { availableMicro: 2_480_000_000 }, degraded: false }, isPending: false, isError: false }),
  useAuroraSubscription: () => ({ data: { tier: "creator", status: "active", currentPeriodEnd: null, cancelAtPeriodEnd: false, limits: { generationsPerMonth: 30, concurrency: 1 }, usage: { generationsUsedThisMonth: 12, activeGenerations: 0 } }, isPending: false, isError: false }),
  useAuroraRuntime: () => ({ data: { value: { workspaceId: "ws", node: { id: "node", status: "online", ready: true, provider: "docker", createdAt: "" }, runtimeId: "rt", state: "online" }, degraded: false }, isPending: false, isError: false }),
}));

function stubCompact(initial: boolean) {
  let compact = initial;
  const listeners = new Set<() => void>();
  vi.stubGlobal("matchMedia", vi.fn((query: string) => ({
    get matches() { return query === "(max-width: 900px)" && compact; },
    media: query, onchange: null, addListener: vi.fn(), removeListener: vi.fn(),
    addEventListener: (_event: string, listener: () => void) => { if (query === "(max-width: 900px)") listeners.add(listener); },
    removeEventListener: (_event: string, listener: () => void) => listeners.delete(listener),
    dispatchEvent: () => true,
  })));
  return (next: boolean) => act(() => { compact = next; listeners.forEach((listener) => listener()); });
}
beforeEach(() => { stubCompact(false); logout.mockReset(); });
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

import { AuroraShell } from "./aurora-shell";

function stubAdapter(pathname: string): NavigationAdapter {
  return {
    push: vi.fn(),
    replace: vi.fn(),
    back: vi.fn(),
    pathname,
    searchParams: new URLSearchParams(),
    hash: "",
    getShareableUrl: (path) => path,
  };
}

function shell(pathname: string) {
  return (
    <NavigationProvider value={stubAdapter(pathname)}>
      <AuroraShell slug="acme">
        <p>page</p>
      </AuroraShell>
    </NavigationProvider>
  );
}
function renderShell(pathname: string) { return renderWithI18n(shell(pathname)); }

describe("AuroraShell", () => {
  it.each([false, true])("keeps page Sheets independent of closed navigation (compact=%s)", async (compact) => {
    stubCompact(compact);
    const user = userEvent.setup();
    renderWithI18n(
      <NavigationProvider value={stubAdapter("/acme/skills")}>
        <AuroraShell slug="acme">
          <Sheet>
            <SheetTrigger>Open generation</SheetTrigger>
            <SheetContent><SheetTitle>Generation</SheetTitle></SheetContent>
          </Sheet>
        </AuroraShell>
      </NavigationProvider>,
    );
    const opener = screen.getByRole("button", { name: "Open generation" });
    await user.click(opener);
    const dialog = await screen.findByRole("dialog", { name: "Generation" });
    expect(document.querySelector('[data-slot="sheet-overlay"]')).toBeVisible();
    await waitFor(() => expect(dialog.contains(document.activeElement)).toBe(true));
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(opener).toHaveFocus();
  });

  it("keeps the mobile account genuinely nested and restores focus in two stages", async () => {
    stubCompact(true);
    const user = userEvent.setup();
    renderShell("/acme/skills");
    const menu = screen.getByRole("button", { name: "Open menu" });
    await user.click(menu);
    const account = await screen.findByRole("button", { name: "Account" });
    await user.click(account);
    const dialog = await screen.findByRole("dialog", { name: "Hannah Han" });
    expect(document.querySelector('[data-slot="sheet-overlay"]')).toBeVisible();
    expect(document.querySelector('[data-slot="dialog-overlay"]')).toBeNull();
    for (let index = 0; index < 5; index++) {
      await user.tab();
      await waitFor(() => expect(dialog.contains(document.activeElement)).toBe(true));
    }
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Hannah Han" })).toBeNull());
    expect(account).toHaveFocus();
    expect(screen.getByRole("dialog", { name: "Aurora" })).toBeVisible();
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(menu).toHaveFocus();
  });
  it("wires the account and runtime destinations into the desktop chrome", async () => {
    const user = userEvent.setup();
    renderShell("/acme/skills");
    expect(screen.queryByRole("button", { name: "Open menu" })).toBeNull();
    expect(screen.getAllByRole("navigation")).toHaveLength(1);
    expect(screen.getByRole("link", { name: "Online" })).toHaveAttribute("href", "/acme/runtimes");
    expect(screen.getByRole("link", { name: "Top up" })).toHaveAttribute("href", "/acme/billing");
    await user.click(screen.getByRole("button", { name: "Account" }));
    expect(await screen.findByRole("dialog", { name: "Hannah Han" })).toBeVisible();
    expect(document.querySelector('[data-slot="dialog-overlay"]')).toBeVisible();
  });
  it("opens the mobile navigation by keyboard and returns focus after Escape", async () => {
    stubCompact(true);
    const user = userEvent.setup();
    renderShell("/acme/skills");
    const opener = await screen.findByRole("button", { name: "Open menu" });
    expect(screen.queryByRole("navigation")).toBeNull();
    opener.focus();
    await user.keyboard("{Enter}");
    const dialog = await screen.findByRole("dialog", { name: "Aurora" });
    expect(within(dialog).getByRole("navigation", { name: "Aurora" })).toBeVisible();
    expect(document.querySelectorAll('nav[aria-label="Aurora"]')).toHaveLength(1);
    await waitFor(() => expect(dialog.contains(document.activeElement)).toBe(true));
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(opener).toHaveFocus();
    expect(document.querySelectorAll('nav[aria-label="Aurora"]')).toHaveLength(0);
  });
  it("closes mobile navigation using the named close button", async () => {
    stubCompact(true);
    const user = userEvent.setup();
    renderShell("/acme/skills");
    const opener = await screen.findByRole("button", { name: "Open menu" });
    await user.click(opener);
    await user.click(await screen.findByRole("button", { name: "Close menu" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(opener).toHaveFocus();
  });
  it("closes both mobile overlays after a committed path change", async () => {
    stubCompact(true);
    const user = userEvent.setup();
    const view = renderShell("/acme/skills");
    await user.click(await screen.findByRole("button", { name: "Open menu" }));
    await user.click(await screen.findByRole("button", { name: "Account" }));
    expect(await screen.findByRole("dialog", { name: "Hannah Han" })).toBeVisible();
    view.rerender(shell("/acme/billing"));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(document.querySelector('[data-slot="sheet-overlay"]')).toBeNull();
    expect(document.querySelector('[data-slot="dialog-overlay"]')).toBeNull();
    await user.click(screen.getByRole("button", { name: "Open menu" }));
    expect(await screen.findByRole("link", { name: "Credits" })).toHaveAttribute("aria-current", "page");
  });
  it.each([false, true])("closes an interrupted account on breakpoint change without resurrection (initial compact=%s)", async (initialCompact) => {
    const resize = stubCompact(initialCompact);
    const user = userEvent.setup();
    renderShell("/acme/skills");
    if (initialCompact) await user.click(screen.getByRole("button", { name: "Open menu" }));
    await user.click(screen.getByRole("button", { name: "Account" }));
    const dialog = await screen.findByRole("dialog", { name: "Hannah Han" });
    await waitFor(() => expect(dialog.contains(document.activeElement)).toBe(true));

    resize(!initialCompact);
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(document.querySelector('[data-slot="sheet-overlay"]')).toBeNull();
    expect(document.querySelector('[data-slot="dialog-overlay"]')).toBeNull();
    const entry = screen.getByRole("button", { name: initialCompact ? "Account" : "Open menu" });
    await waitFor(() => expect(entry).toHaveFocus());
    expect(screen.getByRole("main")).not.toHaveAttribute("aria-hidden");
    await user.tab();
    expect(document.activeElement).not.toBe(entry);
    expect(document.documentElement).not.toHaveAttribute("data-base-ui-scroll-locked");

    // Neither returning to the old layout nor opening navigation revives account.
    resize(initialCompact);
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    resize(true);
    await user.click(screen.getByRole("button", { name: "Open menu" }));
    expect(await screen.findByRole("dialog", { name: "Aurora" })).toBeVisible();
    expect(screen.queryByRole("dialog", { name: "Hannah Han" })).toBeNull();
    await user.click(screen.getByRole("button", { name: "Account" }));
    expect(await screen.findByRole("dialog", { name: "Hannah Han" })).toBeVisible();
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.getByRole("button", { name: "Account" })).toHaveFocus());
  });
  it("unmounts the open sheet on desktop resize without a residual modal lock", async () => {
    const resize = stubCompact(true);
    const user = userEvent.setup();
    renderShell("/acme/skills");
    await user.click(await screen.findByRole("button", { name: "Open menu" }));
    await screen.findByRole("dialog", { name: "Aurora" });
    resize(false);
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(document.querySelector('[data-slot="sheet-overlay"]')).toBeNull();
    expect(screen.getAllByRole("navigation")).toHaveLength(1);
    expect(screen.getByRole("main")).not.toHaveAttribute("aria-hidden");
    const create = screen.getByRole("link", { name: "Create" });
    create.focus();
    expect(create).toHaveFocus();
    await user.click(screen.getByRole("button", { name: "Log out" }));
    expect(logout).toHaveBeenCalledTimes(1);
    resize(true);
    expect(await screen.findByRole("button", { name: "Open menu" })).toBeVisible();
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(screen.queryByRole("navigation")).toBeNull();
  });
  it("closes the desktop account dialog after a committed path change", async () => {
    const user = userEvent.setup();
    const view = renderShell("/acme/skills");
    await user.click(screen.getByRole("button", { name: "Account" }));
    await screen.findByRole("dialog");
    view.rerender(shell("/acme/billing"));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(screen.getByRole("link", { name: "Credits" })).toHaveAttribute("aria-current", "page");
  });
  it("links every destination the app serves", () => {
    renderShell("/acme/skills");

    expect(screen.getByRole("navigation", { name: "Aurora" })).toBeVisible();
    expect(screen.getByRole("link", { name: "Create" })).toHaveAttribute(
      "href",
      "/acme/skills",
    );
    expect(screen.getByRole("link", { name: "My works" })).toHaveAttribute(
      "href",
      "/acme/works",
    );
    expect(screen.getByRole("link", { name: "History" })).toHaveAttribute(
      "href",
      "/acme/history",
    );
    expect(screen.getByRole("link", { name: "Execution" })).toHaveAttribute(
      "href",
      "/acme/runtimes",
    );
    expect(screen.getByRole("link", { name: "Credits" })).toHaveAttribute(
      "href",
      "/acme/billing",
    );
  });

  it("marks exactly the destination being viewed", () => {
    renderShell("/acme/works");

    expect(screen.getByRole("link", { name: "My works" })).toHaveAttribute(
      "aria-current",
      "page",
    );
    expect(screen.getByRole("link", { name: "Create" })).not.toHaveAttribute(
      "aria-current",
    );
    expect(screen.getByRole("link", { name: "Credits" })).not.toHaveAttribute(
      "aria-current",
    );
  });

  it("counts a trailing slash as the same destination", () => {
    renderShell("/acme/billing/");

    expect(screen.getByRole("link", { name: "Credits" })).toHaveAttribute(
      "aria-current",
      "page",
    );
  });

  it("keeps the works destination selected on its generation detail route", () => {
    renderShell("/acme/works/abc");

    expect(screen.getByRole("link", { name: "My works" })).toHaveAttribute(
      "aria-current",
      "page",
    );
    for (const name of ["Create", "History", "Execution", "Credits"]) {
      expect(screen.getByRole("link", { name })).not.toHaveAttribute(
        "aria-current",
      );
    }
  });

  it("marks nothing on a route the nav does not own", () => {
    renderShell("/acme/unknown");

    for (const name of ["Create", "My works", "History", "Execution", "Credits"]) {
      expect(screen.getByRole("link", { name })).not.toHaveAttribute(
        "aria-current",
      );
    }
  });

  it("renders the page it wraps", () => {
    renderShell("/acme/skills");
    expect(screen.getByText("page")).toBeVisible();
  });

  it("offers the account action", () => {
    renderShell("/acme/skills");
    expect(screen.getByRole("button", { name: "Log out" })).toBeVisible();
  });
});
