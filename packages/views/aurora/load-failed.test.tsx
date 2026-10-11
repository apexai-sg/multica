import { describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWithI18n } from "../test/i18n";
import { AuroraLoadFailed } from "./load-failed";

describe("AuroraLoadFailed", () => {
  it("uses Aurora's failure surface and preserves the retry contract", async () => {
    const onRetry = vi.fn();
    renderWithI18n(
      <AuroraLoadFailed title="Could not load works" onRetry={onRetry} />,
    );
    const alert = screen.getByRole("alert");
    expect(alert).toHaveClass("aurora-empty");
    expect(
      within(alert).getByRole("heading", { name: "Could not load works" }),
    ).toBeVisible();
    const retry = within(alert).getByRole("button", { name: "Try again" });
    expect(retry).toHaveAttribute("type", "button");
    await userEvent.setup().click(retry);
    expect(onRetry).toHaveBeenCalledTimes(1);
  });
});
