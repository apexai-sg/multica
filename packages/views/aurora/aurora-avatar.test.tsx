import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { AuroraAvatar } from "./aurora-avatar";

describe("AuroraAvatar", () => {
  it("wires initials and tint without changing the adjacent accessible name", () => {
    const { container } = render(
      <button type="button">
        <AuroraAvatar name="Poster" id="poster" />
        Poster
      </button>,
    );
    const tile = container.querySelector(".aurora-avatar");
    expect(tile).toHaveAttribute("aria-hidden", "true");
    expect(tile).toHaveClass("aurora-avatar--blue");
    expect(tile).toHaveTextContent("P");
    expect(tile).not.toHaveClass("aurora-avatar--lg");
    expect(screen.getByRole("button", { name: "Poster" })).toBeVisible();
  });

  it("supports the large tile and caller class", () => {
    const { container } = render(
      <AuroraAvatar
        name="Product Image"
        id="a"
        size="lg"
        className="aurora-orbit-avatar"
      />,
    );
    expect(container.firstChild).toHaveClass(
      "aurora-avatar",
      "aurora-avatar--lg",
      "aurora-orbit-avatar",
    );
    expect(container.firstChild).toHaveTextContent("PI");
  });

  it("draws the state dot only for a runnable skill", () => {
    const { container, rerender } = render(
      <AuroraAvatar name="Poster" id="poster" />,
    );
    expect(container.querySelector(".aurora-avatar-dot")).toBeNull();
    rerender(<AuroraAvatar name="Poster" id="poster" available />);
    expect(container.querySelector(".aurora-avatar-dot")).toBeInTheDocument();
    rerender(<AuroraAvatar name="Poster" id="poster" available={false} />);
    expect(container.querySelector(".aurora-avatar-dot")).toBeNull();
  });
});
