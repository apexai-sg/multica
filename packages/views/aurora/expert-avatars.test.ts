// @vitest-environment node
import { describe, expect, it } from "vitest";
import { expertAvatarUrl } from "./expert-avatars";

describe("expertAvatarUrl", () => {
  it("points a catalog expert at its own package portrait", () => {
    expect(expertAvatarUrl("content-creation-lyrics-songwriting")).toBe(
      "/avatars/experts/content-creation-lyrics-songwriting.png",
    );
    expect(expertAvatarUrl("media-video-editing")).toBe(
      "/avatars/experts/media-video-editing.png",
    );
  });

  it("borrows the sibling portrait for an expert without its own package", () => {
    // The nine local-only experts have no SkillHub package of their own.
    expect(expertAvatarUrl("media-face-swap")).toBe(
      "/avatars/experts/media-video-editing.png",
    );
    expect(expertAvatarUrl("office-document-reading")).toBe(
      "/avatars/experts/academic-paper-search.png",
    );
    // `ecommerce-bidding-strategy` reuses a byte-identical duplicate.
    expect(expertAvatarUrl("ecommerce-bidding-strategy")).toBe(
      "/avatars/experts/ecommerce-pricing-analysis.png",
    );
  });

  it("returns null for an id outside the catalog", () => {
    expect(expertAvatarUrl("preview-tool")).toBeNull();
    expect(expertAvatarUrl("a-user-created-agent")).toBeNull();
  });
});
