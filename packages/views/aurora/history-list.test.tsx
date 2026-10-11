import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type {
  AuroraAsset,
  AuroraGeneration,
  AuroraGenerationDetail,
  AuroraSkill,
} from "@multica/core/aurora";
import { NavigationProvider } from "../navigation";
import { renderWithI18n } from "../test/i18n";
import { stubNavigationAdapter } from "../test/navigation";

// The download control is a real button from the shared helper; these tests
// cover rendering and accessibility, not the request itself (the helper and
// its transport have their own suites).

const mocks = vi.hoisted(() => ({
  generations: vi.fn(),
  skills: vi.fn(),
  detail: vi.fn(),
}));

vi.mock("@multica/core/aurora", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/aurora")>();
  return {
    ...actual,
    useAuroraGenerations: () => mocks.generations(),
    useAuroraSkills: () => mocks.skills(),
    useAuroraGenerationDetail: (id: string) => mocks.detail(id),
  };
});

import { MAX_GENERATION_THUMBNAILS } from "./generation-artifacts";
import { HistoryList } from "./history-list";

const MICRO = 1_000_000;

const POSTER: AuroraSkill = {
  id: "poster",
  name: "海报制作",
  nameEn: "Poster",
  category: "image",
  credits: 76,
  input: ["text"],
  output: ["image"],
  featured: false,
  available: true,
  attachments: [],
};

function generation(
  overrides: Partial<AuroraGeneration> = {},
): AuroraGeneration {
  return {
    id: "gen-1",
    skillId: "poster",
    prompt: "a launch poster",
    status: "completed",
    creditsReserved: 76 * MICRO,
    creditsCharged: 76 * MICRO,
    error: null,
    createdAt: "2026-09-23T00:00:00Z",
    taskId: "",
    ...overrides,
  };
}

function asset(overrides: Partial<AuroraAsset> = {}): AuroraAsset {
  return {
    id: "asset-1",
    generationId: "gen-1",
    kind: "image",
    mediaUrl: "https://cdn.test/poster.png",
    format: "png",
    createdAt: "2026-09-23T00:00:00Z",
    ...overrides,
  };
}

/**
 * A resolved read, shaped the way the `parseAurora*` helpers hand it to the
 * query: the payload plus whether the body behind it was readable.
 */
function read<T>(value: T, degraded = false) {
  return { data: { value, degraded }, isPending: false };
}

/**
 * Route the list and each row's detail read. Every generation's detail is its
 * summary plus the assets configured for it, which is what the real endpoint
 * returns.
 */
function install(
  generations: AuroraGeneration[],
  assetsByGeneration: Record<string, AuroraAsset[]> = {},
) {
  mocks.generations.mockReturnValue(read(generations));
  mocks.skills.mockReturnValue(read([POSTER]));
  mocks.detail.mockImplementation((id: string) => {
    const summary =
      generations.find((item) => item.id === id) ?? generation({ id });
    const value: AuroraGenerationDetail = {
      ...summary,
      assets: assetsByGeneration[id] ?? [],
    };
    return read(value);
  });
}

function renderHistory() {
  return renderWithI18n(<HistoryList />);
}

describe("HistoryList", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    install([generation()], { "gen-1": [asset()] });
  });

  it("lists a generation with its skill, status, creation time and charged credits", () => {
    const { container } = renderHistory();

    expect(screen.getByText("a launch poster")).toBeInTheDocument();
    expect(screen.getByText("Poster")).toBeInTheDocument();
    // Scoped to the badge: the status filter tab reads "Done" too.
    expect(
      screen.getByText("Done", { selector: "[data-slot='badge']" }),
    ).toBeInTheDocument();
    expect(screen.getByText("76 credits")).toBeInTheDocument();
    expect(container.querySelector("time")).toHaveAttribute(
      "dateTime",
      "2026-09-23T00:00:00Z",
    );
  });

  it("keeps the order it was given, which is newest first", () => {
    install([
      generation({ id: "gen-2", prompt: "newest" }),
      generation({ id: "gen-1", prompt: "older" }),
    ]);

    renderHistory();

    const newest = screen.getByText("newest");
    const older = screen.getByText("older");
    expect(
      newest.compareDocumentPosition(older) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
  });

  it("renders an image artifact as a lazy thumbnail and a non-image as a download row", () => {
    install([generation()], {
      "gen-1": [
        asset(),
        asset({ id: "asset-2", kind: "video", format: "mp4", mediaUrl: null }),
      ],
    });

    renderHistory();

    const preview = screen.getByRole("button", { name: "Preview png" });
    const image = preview.querySelector("img");
    expect(image).toHaveAttribute("src", "https://cdn.test/poster.png");
    expect(image).toHaveAttribute("loading", "lazy");
    // The fixed width/height keep a slow image from moving the rows below it.
    expect(image).toHaveAttribute("width", "80");
    expect(image).toHaveAttribute("height", "80");

    // The download row is a button: a bare anchor to the route 401s.
    const download = screen.getByRole("button", { name: "mp4 Download" });
    expect(within(download).getByText("mp4")).toBeInTheDocument();
    expect(within(download).getByText("Download")).toBeInTheDocument();
  });

  it("renders an image with an unusable URL as a download row, like a missing one", () => {
    install([generation()], {
      "gen-1": [
        asset({ id: "asset-null", mediaUrl: null }),
        asset({ id: "asset-empty", mediaUrl: "" }),
      ],
    });

    renderHistory();

    // Neither a null nor an empty pointer is an image the browser can load, so
    // both fall back to the download row rather than a broken thumbnail.
    expect(
      screen.queryByRole("button", { name: /Preview/ }),
    ).not.toBeInTheDocument();
    expect(
      screen.getAllByRole("button", { name: "png Download" }),
    ).toHaveLength(2);
  });

  it("reserves one artifact slot per thumbnail while the detail read is pending", () => {
    mocks.detail.mockReturnValue({
      data: undefined,
      isPending: true,
      isError: false,
      refetch: vi.fn(),
    });

    const { container } = renderHistory();

    expect(
      container.querySelectorAll("[data-slot='skeleton']").length,
    ).toBe(MAX_GENERATION_THUMBNAILS);
  });

  it("opens a larger view from a thumbnail, with open and download actions", async () => {
    const user = userEvent.setup();
    renderHistory();

    await user.click(screen.getByRole("button", { name: "Preview png" }));

    const dialog = await screen.findByRole("dialog");
    expect(
      within(dialog).getByRole("link", { name: "Open in new tab" }),
    ).toHaveAttribute("href", "https://cdn.test/poster.png");
    expect(
      within(dialog).getByRole("button", { name: "Download" }),
    ).toBeInTheDocument();
  });

  it("shows the failure reason when a failed generation's payload carries one", () => {
    install([
      generation({
        id: "gen-1",
        status: "failed",
        creditsCharged: 0,
        error: "provider rejected the prompt",
      }),
    ]);

    renderHistory();

    // Scoped to the badge: the status filter tab reads "Failed" too.
    expect(
      screen.getByText("Failed", { selector: "[data-slot='badge']" }),
    ).toBeInTheDocument();
    expect(
      screen.getByText("provider rejected the prompt"),
    ).toBeInTheDocument();
    // A refunded generation is not priced.
    expect(screen.queryByText("76 credits")).not.toBeInTheDocument();
  });

  it("filters by the status tabs", async () => {
    const user = userEvent.setup();
    install([
      generation({ id: "done", prompt: "finished poster" }),
      generation({
        id: "bad",
        prompt: "failed poster",
        status: "failed",
        creditsCharged: 0,
      }),
    ]);

    renderHistory();
    expect(screen.getByText("finished poster")).toBeInTheDocument();
    expect(screen.getByText("failed poster")).toBeInTheDocument();

    await user.click(screen.getByRole("tab", { name: "Failed" }));
    expect(screen.getByText("failed poster")).toBeInTheDocument();
    expect(screen.queryByText("finished poster")).not.toBeInTheDocument();

    await user.click(screen.getByRole("tab", { name: "Done" }));
    expect(screen.getByText("finished poster")).toBeInTheDocument();
    expect(screen.queryByText("failed poster")).not.toBeInTheDocument();
  });

  it("says nothing matches when a filter excludes every row", async () => {
    const user = userEvent.setup();
    install([generation()]);

    renderHistory();
    await user.click(screen.getByRole("tab", { name: "Failed" }));

    expect(
      screen.getByText("Nothing matches these filters"),
    ).toBeInTheDocument();
  });

  it("offers the empty copy before anything has been generated", () => {
    install([]);

    renderHistory();

    expect(screen.getByText("No generations yet")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Preview/ })).not.toBeInTheDocument();
  });

  it("reports a history it could not read as a failed load, not an empty one", () => {
    mocks.generations.mockReturnValue(read([], true));
    mocks.skills.mockReturnValue(read([POSTER]));

    renderHistory();

    expect(screen.getByText("Could not load your history")).toBeInTheDocument();
    expect(screen.queryByText("No generations yet")).not.toBeInTheDocument();
  });

  it("counts the loaded list in the Aurora header, not credits or filtered rows", async () => {
    const user = userEvent.setup();
    renderHistory();

    const header = screen
      .getByRole("heading", { name: "History", level: 1 })
      .closest("header");
    expect(header).toHaveClass("aurora-page-head");
    expect(header?.querySelector(".aurora-eyebrow")).toBeNull();
    expect(header?.querySelector(".aurora-page-meta")).toHaveTextContent(/^1$/);
    expect(header).not.toHaveTextContent("credits");

    await user.click(screen.getByRole("tab", { name: "Failed" }));
    expect(screen.queryByRole("list")).not.toBeInTheDocument();
    expect(header?.querySelector(".aurora-page-meta")).toHaveTextContent(/^1$/);
  });

  it("wires pill filters and stacked rows without a decorative icon tile", () => {
    const { container } = renderHistory();
    expect(container.firstChild).toHaveClass("aurora-page");
    expect(screen.getByRole("tablist")).toHaveClass("aurora-pill-tabs");
    for (const tab of screen.getAllByRole("tab")) {
      expect(tab).toHaveClass("aurora-pill-tab");
    }
    expect(
      screen.getByRole("combobox", { name: "Skill" }).parentElement,
    ).toHaveClass("aurora-toolbar");
    const row = screen.getByText("a launch poster").closest("li");
    expect(row?.parentElement).toHaveClass("aurora-list");
    expect(row).toHaveClass("aurora-row-stack");
    expect(screen.getByText("a launch poster").parentElement).toHaveClass(
      "aurora-row-main", "flex", "flex-col",
    );
    expect(screen.getByText("76 credits")).toHaveClass("aurora-row-amount");
    expect(row?.querySelector(".aurora-icon-tile")).toBeNull();
    expect(
      screen.queryByRole("link", { name: "a launch poster" }),
    ).not.toBeInTheDocument();
  });

  it("keeps the skill trigger at the touch target floor within the toolbar", () => {
    renderHistory();
    const trigger = screen.getByRole("combobox", { name: "Skill" });
    expect(trigger).toHaveClass("min-h-10", "max-w-full");
    expect(trigger).toHaveAttribute("data-size", "default");
  });

  it("uses Aurora empty and no-match states without changing filter recovery", async () => {
    install([]);
    const empty = renderHistory();
    expect(
      screen.getByText("No generations yet").closest(".aurora-empty"),
    ).not.toBeNull();
    empty.unmount();
    install([generation()]);
    renderHistory();
    const user = userEvent.setup();
    await user.click(screen.getByRole("tab", { name: "Failed" }));
    expect(
      screen.getByText("Nothing matches these filters").closest(".aurora-empty"),
    ).not.toBeNull();
    await user.click(screen.getByRole("tab", { name: "All" }));
    expect(screen.getByText("a launch poster")).toBeInTheDocument();
  });

  it("keeps pending history as card-radius placeholders rather than an empty claim", () => {
    mocks.generations.mockReturnValue({ data: undefined, isPending: true });
    const { container } = renderHistory();
    const placeholders = container.querySelectorAll("[data-slot='skeleton']");
    expect(placeholders).toHaveLength(3);
    for (const placeholder of placeholders) {
      expect(placeholder).toHaveClass("rounded-xl");
    }
    expect(screen.queryByText("No generations yet")).not.toBeInTheDocument();
  });

  it("links each row to the app's generation detail route when it provides one", () => {
    renderWithI18n(
      <NavigationProvider value={stubNavigationAdapter()}>
        <HistoryList generationHref={(id) => "/acme/works/" + id} />
      </NavigationProvider>,
    );

    expect(
      screen.getByRole("link", { name: "a launch poster" }),
    ).toHaveAttribute("href", "/acme/works/gen-1");
  });
});
