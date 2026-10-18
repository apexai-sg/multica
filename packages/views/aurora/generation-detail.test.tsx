import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ApiError } from "@multica/core/api";
import type {
  AuroraAsset,
  AuroraGenerationDetail,
  AuroraSkill,
} from "@multica/core/aurora";
import { NavigationProvider } from "../navigation";
import { renderWithI18n } from "../test/i18n";
import { stubNavigationAdapter } from "../test/navigation";

const mocks = vi.hoisted(() => ({ detail: vi.fn(), skills: vi.fn() }));

const agentNames = vi.hoisted(() => ({ current: new Map<string, string>() }));

// The copy path is a UI concern here; jsdom has no clipboard and no
// execCommand, so the real util would report failure on every run.
vi.mock("@multica/ui/lib/clipboard", () => ({
  copyText: vi.fn().mockResolvedValue(true),
}));

vi.mock("@multica/core/aurora", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/aurora")>();
  return {
    ...actual,
    useAuroraGenerationDetail: (id: string) => mocks.detail(id),
    useAuroraSkills: () => mocks.skills(),
  };
});

vi.mock("./use-aurora-agent-names", () => ({
  useAuroraAgentNames: () => agentNames.current,
}));

import { copyText } from "@multica/ui/lib/clipboard";
import { GenerationDetail } from "./generation-detail";

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
  avatarUrl: "",
};

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

function detail(
  overrides: Partial<AuroraGenerationDetail> = {},
): AuroraGenerationDetail {
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
    issueIdentifier: "",
    assets: [],
    ...overrides,
  };
}

/** A resolved read, shaped the way the parse helpers hand it to the query. */
function read<T>(value: T, degraded = false) {
  return {
    data: { value, degraded },
    isPending: false,
    isError: false,
    refetch: vi.fn(),
  };
}

function pending() {
  return {
    data: undefined,
    isPending: true,
    isError: false,
    refetch: vi.fn(),
  };
}

function failed(error: unknown) {
  return {
    data: undefined,
    isPending: false,
    isError: true,
    error,
    refetch: vi.fn(),
  };
}

function renderDetail(
  props: Partial<Parameters<typeof GenerationDetail>[0]> = {},
) {
  return renderWithI18n(
    <NavigationProvider value={stubNavigationAdapter()}>
      <GenerationDetail generationId="gen-1" {...props} />
    </NavigationProvider>,
  );
}

describe("GenerationDetail", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(copyText).mockReset().mockResolvedValue(true);
    agentNames.current = new Map();
    mocks.detail.mockReturnValue(read(detail()));
    mocks.skills.mockReturnValue(read([POSTER]));
  });

  it("places the existing back destination in the Aurora page header", () => {
    const { container } = renderDetail({ backHref: "/acme/works" });
    const heading = screen.getByRole("heading", {
      name: "Generation detail",
      level: 1,
    });
    const header = heading.closest(".aurora-page-head");
    expect(header).not.toBeNull();
    expect(container.firstChild).toHaveClass("aurora-page");
    expect(within(header as HTMLElement).getByText("GENERATION")).toBeVisible();
    const back = within(header as HTMLElement).getByRole("link", {
      name: "Back to My works",
    });
    expect(back).toHaveAttribute("href", "/acme/works");
    expect(back).toHaveClass("min-h-10", "min-w-10");
  });

  it("prefers the backend agent's name over the catalog name", () => {
    agentNames.current = new Map([["poster", "Poster helper"]]);
    renderDetail();
    expect(screen.getByText("Poster helper")).toBeInTheDocument();
    expect(screen.queryByText("Poster")).not.toBeInTheDocument();
  });

  it("groups labelled process facts in a responsive panel with a full-width prompt", () => {
    mocks.detail.mockReturnValue(
      read(detail({ assets: [asset({ createdAt: "2026-09-23T00:02:00Z" })] })),
    );
    renderDetail();
    const panel = screen.getByText("Status", { selector: "dt" }).closest("dl");
    expect(panel).toHaveClass(
      "aurora-panel",
      "aurora-field-grid",
      "sm:grid-cols-2",
    );
    for (const label of [
      "Status",
      "Created",
      "Elapsed",
      "Tool",
      "Credits reserved",
      "Credits charged",
      "Prompt",
    ]) {
      const term = within(panel!).getByText(label, { selector: "dt" });
      expect(term.parentElement).toHaveClass("aurora-field");
      expect(term.nextElementSibling?.tagName).toBe("DD");
      expect(term.nextElementSibling).not.toBeEmptyDOMElement();
    }
    expect(within(panel!).getByText("Prompt").parentElement).toHaveClass(
      "sm:col-span-2",
    );
    expect(within(panel!).getByText("a launch poster")).toBeVisible();
    for (const name of ["Process", "Result"]) {
      expect(
        screen.getByRole("heading", { name, level: 2 }).parentElement,
      ).toHaveClass("aurora-section-head");
    }
  });

  it("keeps copy feedback and recovery inside the task panel without inventing a route", async () => {
    const user = userEvent.setup();
    vi.mocked(copyText).mockResolvedValueOnce(false).mockResolvedValueOnce(true);
    mocks.detail.mockReturnValue(read(detail({ taskId: "task-1" })));
    renderDetail();
    expect(
      screen.getByRole("heading", { name: "Task", level: 2 }).parentElement,
    ).toHaveClass("aurora-section-head");
    const panel = screen.getByText("task-1").closest(".aurora-panel");
    expect(panel).not.toBeNull();
    const task = within(panel as HTMLElement);
    expect(
      task.getByText(
        "This app has no task page yet. Copy the ID to inspect the run elsewhere.",
      ),
    ).toBeVisible();
    expect(screen.queryByRole("link", { name: /task-1/ })).not.toBeInTheDocument();
    await user.click(task.getByRole("button", { name: "Copy task ID" }));
    expect(task.getByRole("alert")).toHaveTextContent("Could not copy");
    await user.click(task.getByRole("button", { name: "Copy task ID" }));
    expect(task.getByRole("button", { name: "Copied" })).toBeVisible();
    expect(task.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("restores the copy label after the existing feedback timeout", async () => {
    vi.useFakeTimers();
    try {
      mocks.detail.mockReturnValue(read(detail({ taskId: "task-1" })));
      renderDetail();
      await act(async () => {
        fireEvent.click(screen.getByRole("button", { name: "Copy task ID" }));
      });
      expect(screen.getByRole("button", { name: "Copied" })).toBeVisible();
      await act(async () => {
        vi.advanceTimersByTime(1500);
      });
      expect(screen.getByRole("button", { name: "Copy task ID" })).toBeVisible();
    } finally {
      vi.useRealTimers();
    }
  });

  it("uses the Aurora not-found surface without a retry action", () => {
    mocks.detail.mockReturnValue(
      failed(new ApiError("generation not found", 404, "Not Found")),
    );
    renderDetail();
    expect(
      screen.getByText("Generation not found").closest(".aurora-empty"),
    ).not.toBeNull();
    expect(
      screen.queryByRole("button", { name: "Try again" }),
    ).not.toBeInTheDocument();
  });

  it("keeps a degraded answer retryable rather than presenting it as missing", async () => {
    const user = userEvent.setup();
    const query = read(null, true);
    mocks.detail.mockReturnValue(query);
    renderDetail();
    expect(
      screen.getByText("Could not load this generation").closest(".aurora-empty"),
    ).not.toBeNull();
    expect(screen.queryByText("Generation not found")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Try again" }));
    expect(query.refetch).toHaveBeenCalledOnce();
  });

  it("shows the process fields for a completed generation", () => {
    mocks.detail.mockReturnValue(
      read(
        detail({
          assets: [
            asset({ createdAt: "2026-09-23T00:02:00Z" }),
            asset({
              id: "asset-2",
              kind: "video",
              format: "mp4",
              mediaUrl: null,
              createdAt: "2026-09-23T00:01:30Z",
            }),
          ],
        }),
      ),
    );

    const { container } = renderDetail();

    expect(screen.getByText("Poster")).toBeInTheDocument();
    expect(screen.getByText("a launch poster")).toBeInTheDocument();
    expect(
      screen.getByText("Done", { selector: "[data-slot='badge']" }),
    ).toBeInTheDocument();
    expect(container.querySelector("time")).toHaveAttribute(
      "dateTime",
      "2026-09-23T00:00:00Z",
    );
    // The only end timestamp the payload carries is the newest artifact write.
    expect(screen.getByText("2m")).toBeInTheDocument();
    // Reserved and charged are both shown for a settled generation.
    expect(screen.getAllByText("76 credits")).toHaveLength(2);
    expect(screen.getByText("Charged 76 credits")).toBeInTheDocument();
  });

  it("renders an image artifact as a thumbnail and a non-image as a download row", async () => {
    const user = userEvent.setup();
    mocks.detail.mockReturnValue(
      read(
        detail({
          assets: [
            asset(),
            asset({ id: "asset-2", kind: "video", format: "mp4", mediaUrl: null }),
          ],
        }),
      ),
    );

    renderDetail();

    const preview = screen.getByRole("button", { name: "Preview png" });
    const image = preview.querySelector("img");
    expect(image).toHaveAttribute("src", "https://cdn.test/poster.png");
    expect(image).toHaveAttribute("loading", "lazy");

    // A bare anchor to the download route would 401, so the row is a button
    // that goes through the authenticated client.
    const download = screen.getByRole("button", { name: "mp4 Download" });
    expect(within(download).getByText("mp4")).toBeInTheDocument();
    expect(within(download).getByText("Download")).toBeInTheDocument();

    await user.click(preview);
    const dialog = await screen.findByRole("dialog");
    expect(
      within(dialog).getByRole("link", { name: "Open in new tab" }),
    ).toHaveAttribute("href", "https://cdn.test/poster.png");
    expect(
      within(dialog).getByRole("button", { name: "Download" }),
    ).toBeInTheDocument();
  });

  it("shows the failure reason and the refunded state for a failed generation", () => {
    mocks.detail.mockReturnValue(
      read(
        detail({
          status: "failed",
          creditsCharged: 0,
          error: "provider rejected the prompt",
        }),
      ),
    );

    renderDetail();

    expect(
      screen.getByText("Failed", { selector: "[data-slot='badge']" }),
    ).toBeInTheDocument();
    expect(
      screen.getByText("provider rejected the prompt"),
    ).toBeInTheDocument();
    // The charged field reads as refunded, and only the reservation is priced.
    expect(screen.getByText("Refunded")).toBeInTheDocument();
    expect(screen.getAllByText("76 credits")).toHaveLength(1);
    expect(
      screen.getByText("No credits were charged; the reservation was refunded."),
    ).toBeInTheDocument();
  });

  it("shows a loading skeleton before the generation resolves", () => {
    mocks.detail.mockReturnValue(pending());

    const { container } = renderDetail();

    expect(
      container.querySelectorAll("[data-slot='skeleton']").length,
    ).toBeGreaterThan(0);
    expect(screen.queryByText("Process")).not.toBeInTheDocument();
  });

  it("reports a generation that does not exist as not found", () => {
    mocks.detail.mockReturnValue(
      failed(new ApiError("generation not found", 404, "Not Found")),
    );

    renderDetail();

    expect(screen.getByText("Generation not found")).toBeInTheDocument();
    expect(
      screen.queryByText("Could not load this generation"),
    ).not.toBeInTheDocument();
  });

  it("reports an unreadable generation as a load failure and retries it", async () => {
    const user = userEvent.setup();
    const refetch = vi.fn();
    mocks.detail.mockReturnValue({
      data: undefined,
      isPending: false,
      isError: true,
      error: new ApiError("boom", 500, "Server Error"),
      refetch,
    });

    renderDetail();

    expect(
      screen.getByText("Could not load this generation"),
    ).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Try again" }));
    expect(refetch).toHaveBeenCalled();
  });

  it("offers the way back to the works list", () => {
    renderDetail({ backHref: "/acme/works" });

    expect(
      screen.getByRole("link", { name: "Back to My works" }),
    ).toHaveAttribute("href", "/acme/works");
  });

  it("shows a task id in a copyable form when the app has no task route", async () => {
    const user = userEvent.setup();
    mocks.detail.mockReturnValue(read(detail({ taskId: "task-1" })));

    renderDetail();

    expect(screen.getByText("task-1")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Copy task ID" }));
    expect(copyText).toHaveBeenCalledWith("task-1");
    expect(
      await screen.findByRole("button", { name: "Copied" }),
    ).toBeInTheDocument();
  });

  it("links the task when the app provides a task route", () => {
    mocks.detail.mockReturnValue(read(detail({ taskId: "task-1" })));

    renderDetail({ taskHref: (id) => "/acme/tasks/" + id });

    expect(screen.getByRole("link", { name: /task-1/ })).toHaveAttribute(
      "href",
      "/acme/tasks/task-1",
    );
  });

  it("links to the issue the generation ran on when the app provides one", () => {
    mocks.detail.mockReturnValue(read(detail({ issueIdentifier: "MUL-42" })));

    renderDetail({ issueHref: (identifier) => "/acme/issues/" + identifier });

    expect(screen.getByRole("link", { name: "MUL-42" })).toHaveAttribute(
      "href",
      "/acme/issues/MUL-42",
    );
  });

  it("offers no issue link when the app cannot build one", () => {
    mocks.detail.mockReturnValue(read(detail({ issueIdentifier: "#42" })));

    renderDetail({ issueHref: () => null });

    expect(screen.queryByRole("link", { name: "#42" })).not.toBeInTheDocument();
  });
});
