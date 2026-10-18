import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type {
  AuroraAsset,
  AuroraGeneration,
  AuroraSkill,
} from "@multica/core/aurora";
import { NavigationProvider } from "../navigation";
import { renderWithI18n } from "../test/i18n";
import { stubNavigationAdapter } from "../test/navigation";

// The download helper is deliberately left real: a row must fetch the asset
// through the api client (which carries the bearer token), not point a bare
// navigation at the route. Only the transport is mocked, at the documented
// `@multica/core/api` layer.

const SIGNED = "https://cdn.example.test/asset.png?Signature=s";

const mocks = vi.hoisted(() => ({
  generations: vi.fn(),
  assets: vi.fn(),
  skills: vi.fn(),
  remove: vi.fn(),
  requestResponse: vi.fn(),
}));

vi.mock("@multica/core/aurora", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/aurora")>();
  return {
    ...actual,
    useAuroraGenerations: () => mocks.generations(),
    useAuroraAssets: () => mocks.assets(),
    useAuroraSkills: () => mocks.skills(),
    useDeleteAuroraAsset: () => ({
      mutateAsync: mocks.remove,
      isPending: false,
    }),
  };
});

const agentNames = vi.hoisted(() => ({ current: new Map<string, string>() }));

vi.mock("./use-aurora-agent-names", () => ({
  useAuroraAgentNames: () => agentNames.current,
}));

vi.mock("@multica/core/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/api")>();
  return {
    ...actual,
    api: { requestResponse: mocks.requestResponse },
  } as unknown as typeof actual;
});

vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn() } }));

/** A 302 to a signed URL, the shape the endpoint answers for CloudFront/presign. */
function redirectResponse(): Response {
  return {
    status: 302,
    ok: false,
    redirected: false,
    url: "",
    headers: new Headers({ Location: SIGNED }),
    body: null,
    blob: async () => new Blob(),
  } as unknown as Response;
}

import { WorksList } from "./works-list";
import { avatarInitial, tintIndex } from "./aurora-avatar-helpers";
import { toast } from "sonner";

const MICRO = 1_000_000;

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
    issueIdentifier: "",
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

function renderWorks() {
  return renderWithI18n(<WorksList />);
}

/**
 * A resolved read, shaped the way `parseAurora*` hands it to the query: the
 * payload plus whether the body behind it was readable.
 */
function read<T>(value: T, degraded = false) {
  return { data: { value, degraded }, isPending: false };
}

describe("WorksList", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    agentNames.current = new Map();
    mocks.generations.mockReturnValue(read([generation()]));
    mocks.assets.mockReturnValue(read([asset()]));
    mocks.skills.mockReturnValue(read([POSTER]));
  });

  it("renders work cards with canonical tints and decorative image metadata", () => {
    const { container } = renderWorks();
    const heading = screen.getByRole("heading", { name: "png", level: 3 });
    const card = heading.closest("li")!;
    expect(card).toHaveClass("aurora-work-card");
    expect(card.parentElement).toHaveClass("aurora-works-grid");
    const thumb = card.querySelector(".aurora-work-thumb")!;
    expect(thumb).toHaveClass("aurora-tint-" + tintIndex("asset-1"));
    expect(thumb).toHaveAttribute("aria-hidden", "true");
    expect(thumb.querySelector("img")).toHaveAttribute("src", asset().mediaUrl);
    expect(thumb.querySelector("img")).toHaveAttribute("alt", "");
    expect(thumb.querySelector("img")).toHaveAttribute("loading", "lazy");
    expect(within(card).queryByRole("img")).not.toBeInTheDocument();
    expect(card.querySelector("p")).toHaveTextContent("image");
    expect(container.querySelector(".aurora-page-head")).toBeInTheDocument();
    expect(screen.getByText("a launch poster").closest("li")).toHaveClass("aurora-row");
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });

  it("uses decorative initials for files the shared predicate cannot preview", () => {
    const files = [
      asset({ id: "relative", mediaUrl: "/poster.png", format: "Relative image" }),
      asset({ id: "document", kind: "future-kind", format: "Future format" }),
    ];
    mocks.assets.mockReturnValue(read(files));
    renderWorks();
    for (const file of files) {
      const card = screen.getByRole("heading", { name: file.format! }).closest("li")!;
      const thumb = card.querySelector(".aurora-work-thumb")!;
      expect(thumb).toHaveClass("aurora-tint-" + tintIndex(file.id));
      expect(thumb).toHaveAttribute("aria-hidden", "true");
      expect(thumb.querySelector("img")).toBeNull();
      expect(thumb.querySelector("strong")).toHaveTextContent(avatarInitial(file.format!));
      expect(card.querySelector("p")).toHaveTextContent(file.kind);
      expect(within(card).getByRole("button", { name: "Download" })).toBeEnabled();
    }
  });

  it("does not add a detail line when the format already names the kind", () => {
    mocks.assets.mockReturnValue(read([asset({ kind: "future-kind", format: "future-kind" })]));
    renderWorks();
    const card = screen.getByRole("heading", { name: "future-kind" }).closest("li")!;
    expect(card.querySelector("p")).toBeNull();
    expect(card.querySelector(".aurora-work-thumb")).toHaveAttribute("aria-hidden", "true");
  });

  it("stacks generation metadata and lets intact file actions wrap on narrow cards", () => {
    renderWorks();
    expect(screen.getByText("a launch poster").parentElement).toHaveClass("flex", "flex-col");
    const download = screen.getByRole("button", { name: "Download" });
    expect(download.parentElement).toHaveClass("flex-wrap");
    expect(download).toHaveClass("shrink-0");
    expect(download.querySelector("svg")).toHaveClass("shrink-0");
  });

  it("lists a generation with the skill that produced it", () => {
    renderWorks();

    expect(screen.getByText("a launch poster")).toBeInTheDocument();
    expect(screen.getByText("Poster")).toBeInTheDocument();
    expect(screen.getByText("Done")).toBeInTheDocument();
    expect(screen.getByText("76 credits")).toBeInTheDocument();
  });

  it("prefers the backend agent's name over the catalog name", () => {
    agentNames.current = new Map([["poster", "Poster helper"]]);
    renderWorks();
    expect(screen.getByText("Poster helper")).toBeInTheDocument();
    expect(screen.queryByText("Poster")).not.toBeInTheDocument();
  });

  it("does not price a generation that was refunded", () => {
    // A failed generation is refunded in full (aurora.go's completion path
    // writes creditsCharged 0), so the amount it reserved is not a cost —
    // printing it under "credits" claims a spend that never happened.
    mocks.generations.mockReturnValue(read([generation({ status: "failed" })]));

    renderWorks();

    expect(screen.getByText("Failed")).toBeInTheDocument();
    expect(screen.queryByText("76 credits")).not.toBeInTheDocument();
  });

  it("names the skill as unknown rather than blank when the catalog has dropped it", () => {
    // A generation outlives the catalog entry that made it, so the row has to
    // say something the reader can act on instead of rendering nothing.
    mocks.generations.mockReturnValue(
      read([generation({ skillId: "retired-skill" })]),
    );

    renderWorks();

    expect(screen.getByText("Unknown skill")).toBeInTheDocument();
  });

  it("downloads every asset through the authenticated endpoint, not a bare link", async () => {
    const user = userEvent.setup();
    mocks.assets.mockReturnValue(
      read([asset(), asset({ id: "asset-2", kind: "video", format: "mp4" })]),
    );
    mocks.requestResponse.mockResolvedValue(redirectResponse());
    const open = vi.spyOn(window, "open").mockReturnValue(null);

    const { container } = renderWorks();

    // A bare anchor to the route 401s in the browser: navigation cannot carry
    // the bearer token. The control must not exist.
    expect(
      container.querySelector('a[href="/api/aurora/assets/asset-1/download"]'),
    ).toBeNull();

    const buttons = screen.getAllByRole("button", { name: "Download" });
    expect(buttons).toHaveLength(2);

    await user.click(buttons[0]!);
    expect(mocks.requestResponse).toHaveBeenCalledWith(
      "/api/aurora/assets/asset-1/download",
    );
    expect(open).toHaveBeenCalledWith(SIGNED, "_blank", "noopener,noreferrer");

    await user.click(buttons[1]!);
    expect(mocks.requestResponse).toHaveBeenLastCalledWith(
      "/api/aurora/assets/asset-2/download",
    );
  });

  it("reports a refused download instead of leaving the row silent", async () => {
    const user = userEvent.setup();
    mocks.requestResponse.mockRejectedValue(new Error("missing authorization"));

    renderWorks();
    await user.click(screen.getByRole("button", { name: "Download" }));

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith(
        "Could not download the file. Try again.",
      ),
    );
  });

  it("does not repeat an asset's kind when it has no format to show above it", () => {
    mocks.assets.mockReturnValue(read([asset({ format: null })]));

    renderWorks();

    expect(screen.getAllByText("image")).toHaveLength(1);
  });

  it("deletes an asset only once the confirmation is accepted", async () => {
    const user = userEvent.setup();
    mocks.remove.mockResolvedValue(undefined);

    renderWorks();
    await user.click(screen.getByRole("button", { name: "Delete" }));

    expect(await screen.findByText("Delete this file?")).toBeInTheDocument();
    expect(mocks.remove).not.toHaveBeenCalled();

    await user.click(screen.getByRole("button", { name: "Delete file" }));

    expect(mocks.remove).toHaveBeenCalledWith("asset-1");
  });

  it("keeps the row and reports the failure when a delete fails", async () => {
    const user = userEvent.setup();
    mocks.remove.mockRejectedValue(new Error("nope"));

    renderWorks();
    await user.click(screen.getByRole("button", { name: "Delete" }));
    await user.click(
      await screen.findByRole("button", { name: "Delete file" }),
    );

    // The server drops the stored object as well as the row, so a failed delete
    // must not leave the list claiming a file the user can no longer fetch is
    // gone — and must not imply it is still there either.
    expect(
      await screen.findByText("Could not delete the file. Try again."),
    ).toBeInTheDocument();
    expect(screen.getByText("png")).toBeInTheDocument();
  });

  it("closes the confirmation when a delete fails, so the report is not stuck behind it", async () => {
    // `AlertDialogAction` is a plain Button, not a close primitive: leaving the
    // dialog open on failure covered the report with a modal backdrop that also
    // marks everything outside it `aria-hidden`. Sighted users saw a dialog
    // that did nothing; screen-reader users heard nothing at all.
    const user = userEvent.setup();
    mocks.remove.mockRejectedValue(new Error("nope"));

    renderWorks();
    await user.click(screen.getByRole("button", { name: "Delete" }));
    await user.click(
      await screen.findByRole("button", { name: "Delete file" }),
    );
    await screen.findByText("Could not delete the file. Try again.");

    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
  });

  it("stays silent about unknown skills when the catalog itself could not be read", async () => {
    // A failed catalog request used to stamp "Unknown skill" on every row — a
    // screenful of claims the client had no basis for. It is only unknown when
    // the catalog answered and did not list it.
    mocks.skills.mockReturnValue({
      data: undefined,
      isPending: false,
      isError: true,
    });

    renderWorks();

    expect(screen.queryByText("Unknown skill")).not.toBeInTheDocument();
    expect(screen.getByText("a launch poster")).toBeInTheDocument();
  });

  it("stays silent about unknown skills when the catalog body could not be read", () => {
    // A degraded catalog parses to an empty list, so it is not "the catalog
    // dropped this entry" either — same silence as a failed read.
    mocks.skills.mockReturnValue(read([], true));

    renderWorks();

    expect(screen.queryByText("Unknown skill")).not.toBeInTheDocument();
    expect(screen.getByText("a launch poster")).toBeInTheDocument();
  });

  it("offers the empty copy before anything has been generated", () => {
    mocks.generations.mockReturnValue(read([]));
    mocks.assets.mockReturnValue(read([]));

    renderWorks();

    expect(screen.getByText("No generations yet")).toBeInTheDocument();
    expect(screen.getByText("No content yet")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Delete" })).not.toBeInTheDocument();
  });

  it("reports a list it could not read as a failed load, not as an empty library", () => {
    // GH #55: a malformed 200 parses to [] and resolves the query, so the
    // screen used to claim the workspace had generated nothing.
    mocks.generations.mockReturnValue(read([], true));
    mocks.assets.mockReturnValue(read([], true));

    renderWorks();

    expect(screen.getByText("Could not load your works")).toBeInTheDocument();
    expect(screen.queryByText("No generations yet")).not.toBeInTheDocument();
    expect(screen.queryByText("No content yet")).not.toBeInTheDocument();
  });

  it("reports the unreadable half even when the other list resolved", () => {
    // One read is enough: the library is one screen, and a screen that cannot
    // be trusted about its assets is not a screen that can be trusted about
    // anything on it. Same rule a failed read already followed.
    mocks.assets.mockReturnValue(read([], true));

    renderWorks();

    expect(screen.getByText("Could not load your works")).toBeInTheDocument();
    expect(screen.queryByText("a launch poster")).not.toBeInTheDocument();
  });

  it("links each generation row to the app's detail route and navigates there", async () => {
    const user = userEvent.setup();
    const push = vi.fn();

    renderWithI18n(
      <NavigationProvider value={stubNavigationAdapter({ push })}>
        <WorksList generationHref={(id) => "/acme/works/" + id} />
      </NavigationProvider>,
    );

    const row = screen.getByRole("link", { name: "a launch poster" });
    expect(row).toHaveAttribute("href", "/acme/works/gen-1");

    await user.click(row);
    expect(push).toHaveBeenCalledWith("/acme/works/gen-1");
  });

  it("links a generation that ran on an issue to that issue when the app can", async () => {
    const user = userEvent.setup();
    const push = vi.fn();
    mocks.generations.mockReturnValue(
      read([generation({ issueIdentifier: "MUL-42" })]),
    );

    renderWithI18n(
      <NavigationProvider value={stubNavigationAdapter({ push })}>
        <WorksList
          generationHref={(id) => "/acme/works/" + id}
          issueHref={(identifier) => "/acme/issues/" + identifier}
        />
      </NavigationProvider>,
    );

    const row = screen.getByRole("link", { name: "a launch poster" });
    expect(row).toHaveAttribute("href", "/acme/issues/MUL-42");

    await user.click(row);
    expect(push).toHaveBeenCalledWith("/acme/issues/MUL-42");
  });

  it("keeps the generation route when the app cannot build an issue URL", () => {
    mocks.generations.mockReturnValue(
      read([generation({ issueIdentifier: "#42" })]),
    );

    renderWithI18n(
      <NavigationProvider value={stubNavigationAdapter()}>
        <WorksList
          generationHref={(id) => "/acme/works/" + id}
          issueHref={() => null}
        />
      </NavigationProvider>,
    );

    expect(screen.getByRole("link", { name: "a launch poster" })).toHaveAttribute(
      "href",
      "/acme/works/gen-1",
    );
  });
});
