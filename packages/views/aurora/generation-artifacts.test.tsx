import { describe, expect, it } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWithI18n } from "../test/i18n";
import type { AuroraAsset, AuroraGeneration } from "@multica/core/aurora";
import {
  GenerationArtifacts,
  GenerationStatusBadge,
  isPreviewableImage,
} from "./generation-artifacts";

function image(mediaUrl: AuroraAsset["mediaUrl"]): AuroraAsset {
  return {
    id: "asset-1",
    generationId: "gen-1",
    kind: "image",
    mediaUrl,
    format: "png",
    createdAt: "2026-09-23T00:00:00Z",
  };
}

describe("isPreviewableImage", () => {
  it("accepts an image with an absolute http(s) URL", () => {
    expect(isPreviewableImage(image("https://cdn.test/poster.png"))).toBe(true);
    expect(isPreviewableImage(image("http://cdn.test/poster.png"))).toBe(true);
  });

  const unusable: [string, AuroraAsset["mediaUrl"]][] = [
    // The server can emit an empty pointer (valid-empty becomes &"") as well as
    // no pointer at all; both mean "no file to show".
    ["missing", null],
    ["empty", ""],
    ["relative", "/files/poster.png"],
    ["scheme-relative", "//cdn.test/poster.png"],
    ["non-http", "data:image/png;base64,AAAA"],
    ["javascript", "javascript:alert(1)"],
  ];

  it.each(unusable)("rejects an image whose URL is %s", (_label, mediaUrl) => {
    expect(isPreviewableImage(image(mediaUrl))).toBe(false);
  });

  it("rejects a non-image asset even when it carries a URL", () => {
    const video: AuroraAsset = {
      ...image("https://cdn.test/clip.mp4"),
      kind: "video",
    };
    expect(isPreviewableImage(video)).toBe(false);
  });
});

const generation: AuroraGeneration = {
  id: "gen-1",
  skillId: "poster",
  prompt: "a launch poster",
  status: "completed",
  creditsReserved: 76_000_000,
  creditsCharged: 76_000_000,
  error: null,
  createdAt: "2026-09-23T00:00:00Z",
  taskId: "",
};

describe("GenerationStatusBadge presentation", () => {
  it.each([
    ["queued", "Queued", "aurora-status-pill--working"],
    ["running", "Generating", "aurora-status-pill--working"],
    ["completed", "Done", "aurora-status-pill"],
    ["failed", "Failed", "aurora-status-pill--failed"],
    ["future-status", "Unknown", "aurora-status-pill--muted"],
  ])("renders %s with its labelled status treatment", (status, label, treatment) => {
    renderWithI18n(
      <GenerationStatusBadge status={status} className="host-status" />,
    );
    const badge = screen.getByText(label);
    expect(badge).toHaveClass("aurora-status-pill", treatment, "host-status");
    expect(badge).toHaveAttribute("data-slot", "badge");
    if (status === "completed") {
      expect(badge.className).not.toMatch(/--working|--failed|--muted/);
    }
  });
});

describe("GenerationArtifacts presentation", () => {
  it("uses Aurora thumbnails and file rows without losing preview semantics", async () => {
    const user = userEvent.setup();
    renderWithI18n(
      <GenerationArtifacts
        generation={generation}
        assets={[
          image("https://cdn.test/poster.png"),
          { ...image(null), id: "file-1", kind: "document", format: "pdf" },
        ]}
      />,
    );
    const preview = screen.getByRole("button", { name: "Preview png" });
    expect(preview).toHaveClass("aurora-thumb");
    expect(preview.closest("ul")).toHaveClass("aurora-thumbs");
    expect(preview).toHaveAttribute("type", "button");
    expect(preview.querySelector("img")).toHaveAttribute("loading", "lazy");
    expect(preview.querySelector("img")).toHaveAttribute("width", "80");
    expect(preview.querySelector("img")).toHaveAttribute("height", "80");
    expect(screen.getByRole("button", { name: "pdf Download" })).toHaveClass(
      "aurora-file-row",
    );
    await user.click(preview);
    const dialog = screen.getByRole("dialog", { name: "a launch poster" });
    expect(dialog).toHaveClass("aurora-dialog", "aurora-dialog--wide");
    expect(dialog).toHaveAccessibleDescription();
    expect(dialog.querySelector("img")).toHaveClass("rounded-lg");
    // Native variant-styled actions do not receive the shared Button slot
    // sizing rule. Keep their touch targets at the Aurora forty-pixel floor.
    for (const action of [
      screen.getByRole("link", { name: "Open in new tab" }),
      screen.getByRole("button", { name: "Download" }),
    ]) {
      expect(action).toHaveClass("min-h-10", "min-w-10");
    }
    expect(screen.getByRole("link", { name: "Open in new tab" })).toHaveAttribute(
      "href",
      "https://cdn.test/poster.png",
    );
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("keeps loading thumbnail geometry decorative", () => {
    const { container } = renderWithI18n(
      <GenerationArtifacts generation={generation} assets={[]} loading />,
    );
    const thumbs = container.querySelector(".aurora-thumbs");
    expect(thumbs).not.toBeNull();
    expect(thumbs?.parentElement).toHaveAttribute("aria-hidden", "true");
    expect(thumbs?.children).toHaveLength(4);
    for (const skeleton of thumbs!.children) {
      expect(skeleton).toHaveClass("size-20", "rounded-lg");
    }
  });
});
