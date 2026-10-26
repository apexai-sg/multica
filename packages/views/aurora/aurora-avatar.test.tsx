import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";

// The real resolver prefixes the API base. That base belongs to the app boot
// (`setApiInstance`), which a component test does not run, and the mapping
// itself is owned by packages/core/workspace/avatar-url.test.ts — here the
// question is only whether the tile uses the URL it was handed.
vi.mock("@multica/core/workspace/avatar-url", () => ({
  resolvePublicFileUrl: (url: string | null | undefined) => url ?? null,
}));

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

  it("draws the carrier agent's portrait instead of the initials", () => {
    const { container } = render(
      <AuroraAvatar
        name="Poster"
        id="poster"
        avatarUrl="/uploads/agents/lobster.png"
        available
      />,
    );
    const tile = container.querySelector(".aurora-avatar")!;
    // A relative path is one of the backend's objects, so it resolves against
    // the API base before it reaches the <img>.
    expect(tile.querySelector("img")!.getAttribute("src")).toContain(
      "/uploads/agents/lobster.png",
    );
    // The tint paints the fallback, so a portrait does not get one.
    expect(tile).not.toHaveClass("aurora-avatar--blue");
    expect(tile).not.toHaveTextContent("P");
    // The state dot is an overlay, not part of the fallback.
    expect(tile.querySelector(".aurora-avatar-dot")).toBeInTheDocument();
  });

  it("prefers a bundled expert portrait over the carrier avatar", () => {
    const { container } = render(
      <AuroraAvatar
        name="Lyrics helper"
        id="a-lyrics"
        avatarUrl="/uploads/lyrics.png"
        bundledAvatarUrl="/avatars/experts/content-creation-lyrics-songwriting.png"
      />,
    );
    // The bundled portrait ships with the app, so it is used verbatim rather
    // than resolved against the API base like the carrier's upload would be.
    expect(container.querySelector("img")!.getAttribute("src")).toBe(
      "/avatars/experts/content-creation-lyrics-songwriting.png",
    );
  });

  it("falls back to the initials when the bundled portrait cannot load", () => {
    const { container } = render(
      <AuroraAvatar
        name="Lyrics helper"
        id="a-lyrics"
        bundledAvatarUrl="/avatars/experts/gone.png"
      />,
    );
    fireEvent.error(container.querySelector("img")!);
    const tile = container.querySelector(".aurora-avatar")!;
    expect(tile.querySelector("img")).toBeNull();
    expect(tile).toHaveTextContent("L");
  });

  it("falls back to the initials when the portrait cannot load", () => {
    const { container } = render(
      <AuroraAvatar name="Poster" id="poster" avatarUrl="/uploads/gone.png" />,
    );
    fireEvent.error(container.querySelector("img")!);
    const tile = container.querySelector(".aurora-avatar")!;
    expect(tile.querySelector("img")).toBeNull();
    expect(tile).toHaveTextContent("P");
    // The tint belongs to the fallback, so a broken portrait restores it.
    expect(tile).toHaveClass("aurora-avatar--blue");
  });

  it("retries the next portrait after the previous one failed", () => {
    const { container, rerender } = render(
      <AuroraAvatar name="Poster" id="poster" avatarUrl="/uploads/gone.png" />,
    );
    fireEvent.error(container.querySelector("img")!);
    rerender(<AuroraAvatar name="Poster" id="poster" avatarUrl="/uploads/new.png" />);
    expect(container.querySelector("img")!.getAttribute("src")).toContain(
      "/uploads/new.png",
    );
    expect(container.querySelector(".aurora-avatar")).not.toHaveTextContent("P");
  });

  it("renders an emoji avatar marker without treating it as an image", () => {
    const { container } = render(
      <AuroraAvatar name="Poster" id="poster" avatarUrl="emoji:🦞" />,
    );
    const tile = container.querySelector(".aurora-avatar")!;
    expect(tile).toHaveTextContent("🦞");
    expect(tile.querySelector("img")).toBeNull();
  });
});
