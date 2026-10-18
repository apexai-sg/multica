import type { ComponentProps } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { cleanup, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWithI18n } from "../../test/render";

const { router, queryClient, searchParams, authState, configState } = vi.hoisted(() => ({
  router: { push: vi.fn(), replace: vi.fn() },
  queryClient: { getQueryData: vi.fn(), ensureQueryData: vi.fn() },
  searchParams: new URLSearchParams(),
  authState: { user: null, isLoading: false },
  configState: { googleClientId: "" },
}));
vi.mock("next/navigation", () => ({ useRouter: () => router, useSearchParams: () => searchParams }));
vi.mock("@tanstack/react-query", () => ({ useQueryClient: () => queryClient }));
vi.mock("@multica/core/auth", async (original) => ({
  ...(await original<typeof import("@multica/core/auth")>()),
  useAuthStore: Object.assign((selector: (s: typeof authState) => unknown) => selector(authState), { getState: () => authState }),
}));
vi.mock("@multica/core/config", () => ({
  useConfigStore: Object.assign((selector: (s: typeof configState) => unknown) => selector(configState), { getState: () => configState }),
}));
// Shared auth behavior has its own suite; this boundary exposes app-owned slots and callbacks.
vi.mock("@multica/views/auth", async (original) => ({
  ...(await original<typeof import("@multica/views/auth")>()),
  useLogout: () => vi.fn(),
  LoginPage: ({ logo, onSuccess }: ComponentProps<typeof import("@multica/views/auth").LoginPage>) => <div>{logo}<button onClick={onSuccess}>Complete login</button></div>,
}));
import Page from "./page";
afterEach(cleanup);
beforeEach(() => { vi.clearAllMocks(); searchParams.delete("next"); queryClient.getQueryData.mockReturnValue([{ slug: "acme" }]); });

it("provides Aurora's light-surface brand and existing tagline to the shared login", () => {
  renderWithI18n(<Page />);
  const name = screen.getByText("Apex AI");
  expect(name.closest(".aurora-brand")).toHaveClass("aurora-brand--on-light");
  expect(screen.getByText("CREATE SOMETHING USEFUL")).toBeVisible();
  expect(screen.getByText("A")).toHaveAttribute("aria-hidden", "true");
});
it("preserves the login success destination", async () => {
  const user = userEvent.setup();
  renderWithI18n(<Page />);
  await user.click(screen.getByRole("button", { name: "Complete login" }));
  expect(router.push).toHaveBeenCalledWith("/acme/skills");
});
it("preserves a sanitized deep link on login success", async () => {
  searchParams.set("next", "/acme/works");
  const user = userEvent.setup();
  renderWithI18n(<Page />);
  await user.click(screen.getByRole("button", { name: "Complete login" }));
  expect(router.push).toHaveBeenCalledWith("/acme/works");
});
