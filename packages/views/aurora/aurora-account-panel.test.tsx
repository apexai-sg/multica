import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { setApiInstance } from "@multica/core/api";
import type { ApiClient } from "@multica/core/api/client";
import type { AuthState } from "@multica/core/auth";
import { renderWithI18n } from "../test/i18n";
import { stubNavigationAdapter } from "../test/navigation";
import { NavigationProvider } from "../navigation";
import { AuroraAccountDialog, AuroraAccountMenu, AuroraAccountPanel } from "./aurora-account-panel";

const { logout, auth } = vi.hoisted(() => ({ logout: vi.fn(), auth: { user: null as AuthState["user"] } }));
vi.mock("@multica/core/auth", () => ({
  useAuthStore: Object.assign((selector: (state: typeof auth) => unknown) => selector(auth), { getState: () => auth }),
}));
vi.mock("../auth", () => ({ useLogout: () => logout }));

const SUBSCRIPTION = {
  tier: "creator", status: "active", currentPeriodEnd: null, cancelAtPeriodEnd: false,
  limits: { generationsPerMonth: 30, concurrency: 1 },
  usage: { generationsUsedThisMonth: 12, activeGenerations: 0 },
};
const clients: QueryClient[] = [];
function installReads(balance: unknown = { availableMicro: 2_480_000_000 }, subscription: unknown = { subscription: SUBSCRIPTION }) {
  setApiInstance({ requestJson: async (path: string) => {
    const body = path === "/api/aurora/billing/balance" ? balance
      : path === "/api/aurora/billing/subscription" ? subscription : new Error(path);
    if (body instanceof Error) throw body;
    return body;
  } } as unknown as ApiClient);
}
function Account() {
  const [open, setOpen] = useState(false);
  return <><AuroraAccountPanel creditsHref="/acme/billing" onOpenAccount={() => setOpen(true)} />
    <AuroraAccountDialog open={open} onOpenChange={setOpen} creditsHref="/acme/billing" /></>;
}
function Menu() {
  const [open, setOpen] = useState(false);
  return (
    <AuroraAccountMenu
      open={open}
      onOpenChange={setOpen}
      creditsHref="/acme/billing"
      runtimeHref="/acme/runtimes"
    />
  );
}
function renderMenu() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  clients.push(client);
  return renderWithI18n(
    <QueryClientProvider client={client}>
      <NavigationProvider value={stubNavigationAdapter()}>
        <Menu />
      </NavigationProvider>
    </QueryClientProvider>,
  );
}
function renderAccount() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  clients.push(client);
  const result = renderWithI18n(<QueryClientProvider client={client}>
    <NavigationProvider value={stubNavigationAdapter()}><Account /></NavigationProvider>
  </QueryClientProvider>);
  return { ...result, client };
}
beforeEach(() => {
  logout.mockReset();
  installReads();
  auth.user = { id: "u1", name: " Hannah Han ", email: "hannah@apex.ai", avatar_url: null, onboarded_at: null, onboarding_questionnaire: {}, starter_content_state: null, language: null, profile_description: "", timezone: null, created_at: "", updated_at: "" };
});
afterEach(() => {
  cleanup();
  clients.splice(0).forEach((client) => client.clear());
  setApiInstance(null as unknown as ApiClient);
});

describe("AuroraAccountPanel", () => {
  it("shows identity, real credits, plan and the monthly allowance", async () => {
    renderAccount();
    expect(screen.getByText("Hannah Han")).toBeVisible();
    expect(await screen.findByText("2,480")).toBeVisible();
    expect(await screen.findByText("Creator")).toBeVisible();
    expect(screen.getByText("12 of 30")).toBeVisible();
    const meter = screen.getByRole("progressbar", { name: "Generations this month" });
    expect(meter).toHaveAttribute("aria-valuemin", "0");
    expect(meter).toHaveAttribute("aria-valuemax", "30");
    expect(meter).toHaveAttribute("aria-valuenow", "12");
    expect(meter.firstElementChild).toHaveStyle({ width: "40%" });
    expect(screen.getByRole("link", { name: "Top up" })).toHaveAttribute("href", "/acme/billing");
  });
  it("gives the meter a block box so its fixed height contains the fill", async () => {
    renderAccount();
    const meter = await screen.findByRole("progressbar");
    // An inline span ignores the meter height; its 100%-high block child
    // then resolves against the whole panel and overlaps navigation.
    expect(getComputedStyle(meter).display).toBe("block");
  });
  it("falls back to email when the name is blank", () => {
    auth.user = { ...auth.user!, name: "  " };
    renderAccount();
    expect(screen.getByText("hannah@apex.ai")).toBeVisible();
  });
  it("does not invent a wallet or free plan while loading", () => {
    installReads(new Promise(() => {}), new Promise(() => {}));
    renderAccount();
    expect(screen.getByText("—")).toBeVisible();
    expect(screen.queryByText("Free")).toBeNull();
    expect(screen.queryByRole("progressbar")).toBeNull();
  });
  it.each(["malformed", "failed"])("does not present %s reads as zero credits or a free plan", async (kind) => {
    installReads(kind === "failed" ? new Error("offline") : { availableMicro: "broken" }, kind === "failed" ? new Error("offline") : { subscription: null });
    const { client } = renderAccount();
    await waitFor(() => expect(client.isFetching()).toBe(0));
    expect(screen.getByText("—")).toBeVisible();
    expect(screen.queryByText("0")).toBeNull();
    expect(screen.queryByText("Free")).toBeNull();
    expect(screen.queryByRole("progressbar")).toBeNull();
  });
  it("renders a genuine zero balance", async () => {
    installReads({ availableMicro: 0 });
    renderAccount();
    expect(await screen.findByText("0")).toBeVisible();
  });
  it("clamps the meter but keeps the actual over-limit usage", async () => {
    installReads(undefined, { subscription: { ...SUBSCRIPTION, usage: { generationsUsedThisMonth: 45, activeGenerations: 0 } } });
    renderAccount();
    expect(await screen.findByText("45 of 30")).toBeVisible();
    const meter = screen.getByRole("progressbar");
    expect(meter).toHaveAttribute("aria-valuenow", "30");
    expect(meter).toHaveAttribute("aria-valuetext", "45 of 30");
    expect(meter.firstElementChild).toHaveStyle({ width: "100%" });
  });
  it("does not draw a meter with a zero denominator", async () => {
    installReads(undefined, { subscription: { ...SUBSCRIPTION, limits: { generationsPerMonth: 0, concurrency: 1 } } });
    renderAccount();
    expect(await screen.findByText("12 of 0")).toBeVisible();
    expect(screen.queryByRole("progressbar")).toBeNull();
  });
  it.each([-1, NaN, Infinity])("does not display invalid quota %s", async (limit) => {
    installReads(undefined, { subscription: { ...SUBSCRIPTION, limits: { generationsPerMonth: limit, concurrency: 1 } } });
    const { client } = renderAccount();
    await waitFor(() => expect(client.isFetching()).toBe(0));
    expect(screen.queryByRole("progressbar")).toBeNull();
    expect(screen.queryByText(/12 of/)).toBeNull();
  });
  it("names an unknown tier honestly", async () => {
    installReads(undefined, { subscription: { ...SUBSCRIPTION, tier: "future" } });
    renderAccount();
    expect(await screen.findByText("Unknown plan")).toBeVisible();
    expect(screen.queryByText("Free")).toBeNull();
  });
});
describe("AuroraAccountDialog", () => {
  it("keeps the dialog named when identity is temporarily unavailable", async () => {
    auth.user = null;
    const user = userEvent.setup();
    renderAccount();
    await user.click(screen.getByRole("button", { name: "Account" }));
    expect(await screen.findByRole("dialog", { name: "Account" })).toBeVisible();
  });
  it("opens with keyboard, names the identity and restores focus on Escape", async () => {
    const user = userEvent.setup();
    renderAccount();
    const opener = screen.getByRole("button", { name: "Account" });
    opener.focus();
    await user.keyboard("{Enter}");
    const dialog = await screen.findByRole("dialog", { name: "Hannah Han" });
    expect(within(dialog).getByText("hannah@apex.ai")).toBeVisible();
    expect(within(dialog).getByRole("link", { name: "Credits & top-up" })).toHaveAttribute("href", "/acme/billing");
    await waitFor(() => expect(dialog.contains(document.activeElement)).toBe(true));
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(opener).toHaveFocus();
  });
  it("closes before invoking the existing logout flow", async () => {
    const user = userEvent.setup();
    renderAccount();
    await user.click(screen.getByRole("button", { name: "Account" }));
    await user.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "Log out" }));
    expect(logout).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });
});

describe("AuroraAccountMenu", () => {
  it("prints an unknown marker on the trigger until the wallet is read", () => {
    installReads(new Promise(() => {}), new Promise(() => {}));
    renderMenu();
    const trigger = screen.getByRole("button", { name: "Account" });
    expect(trigger).toHaveTextContent("—");
    expect(trigger).not.toHaveTextContent("0");
    expect(screen.queryByText("Free")).toBeNull();
  });

  it("opens the card without a modal overlay and returns focus on Escape", async () => {
    const user = userEvent.setup();
    renderMenu();
    const trigger = screen.getByRole("button", { name: "Account" });
    expect(trigger).toHaveTextContent("Hannah Han");
    expect(await within(trigger).findByText("2,480")).toBeVisible();

    await user.click(trigger);
    const menu = await screen.findByRole("dialog", { name: "Hannah Han" });
    expect(document.querySelector('[data-slot="dialog-overlay"]')).toBeNull();
    expect(within(menu).getByRole("link", { name: "Top up" })).toHaveAttribute("href", "/acme/billing");
    expect(within(menu).getByRole("link", { name: "Service status" })).toHaveAttribute("href", "/acme/runtimes");
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(trigger).toHaveFocus();
  });

  it("closes before invoking the existing logout flow", async () => {
    const user = userEvent.setup();
    renderMenu();
    await user.click(screen.getByRole("button", { name: "Account" }));
    await user.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "Log out" }));
    expect(logout).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });
});
