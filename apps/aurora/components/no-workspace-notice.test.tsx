import { afterEach, expect, it, vi } from "vitest";
import { cleanup, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWithI18n } from "../test/render";
const { logout } = vi.hoisted(() => ({ logout: vi.fn() }));
vi.mock("@multica/views/auth", () => ({ useLogout: () => logout }));
import { NoWorkspaceNotice } from "./no-workspace-notice";
afterEach(cleanup);
it("centers the existing card and keeps logout as the recovery action", async () => {
  const user = userEvent.setup();
  const { container } = renderWithI18n(<NoWorkspaceNotice />);
  expect(container.firstElementChild).toHaveClass("aurora-centered");
  expect(container.querySelector('[data-slot="card"]')).toBeVisible();
  expect(container.querySelector('[data-slot="card-description"]')).toBeVisible();
  await user.click(screen.getByRole("button", { name: "Log out" }));
  expect(logout).toHaveBeenCalledTimes(1);
});
