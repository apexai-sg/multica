import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ApiError } from "@multica/core/api";
import type { AuroraGenerationDetail, AuroraSkill } from "@multica/core/aurora";
import type { Agent } from "@multica/core/types";
import { renderWithI18n } from "../test/i18n";
import { stubNavigationAdapter } from "../test/navigation";
import { NavigationProvider } from "../navigation";

// The composer's own job is the submit lifecycle: what it sends, what it shows
// while the server works, and which server refusal maps to which prompt. The
// transport is Task 1's module and is already covered there, so only its two
// hooks are replaced — `isAuroraInsufficientCreditsError` and its 429 sibling
// stay real, because classifying the error *is* the behaviour under test here.

const mocks = vi.hoisted(() => ({
  create: vi.fn(),
  detail: vi.fn(),
  detailRefetch: vi.fn(),
  balance: vi.fn(),
  upload: vi.fn(),
}));

// The upload hook is core's and is covered by its own suite; the composer's job
// is to turn a picked file into an id and to gate submit on its state. `api` is
// passed straight through to the mocked hook, so the real one never runs.
vi.mock("@multica/core/hooks/use-file-upload", () => ({
  useFileUpload: () => ({
    upload: mocks.upload,
    uploadWithToast: mocks.upload,
    uploading: false,
  }),
}));

vi.mock("@multica/core/aurora", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/aurora")>();
  return {
    ...actual,
    useCreateAuroraGeneration: () => ({
      mutateAsync: mocks.create,
      isPending: false,
    }),
    useAuroraGenerationDetail: (id: string) => mocks.detail(id),
    useAuroraBalance: () => mocks.balance(),
  };
});

import { GenerationComposer } from "./generation-composer";

const MICRO = 1_000_000;

function skill(overrides: Partial<AuroraSkill> = {}): AuroraSkill {
  return {
    id: "content-creation-lyrics-songwriting",
    name: "歌词创作专家",
    nameEn: "Content Creation Lyrics Songwriting Expert",
    category: "content-creation",
    credits: 60,
    input: ["text", "document"],
    output: ["text"],
    featured: false,
    available: true,
    attachments: [],
    avatarUrl: "",
    ...overrides,
  };
}

function agent(overrides: Partial<Agent> = {}): Agent {
  return {
    id: "agent-lyrics",
    workspace_id: "ws-1",
    runtime_id: "rt-1",
    name: "Write lyrics",
    description: "Short lyrics for songs.",
    instructions: "",
    avatar_url: null,
    runtime_mode: "local",
    runtime_config: {},
    max_concurrent_tasks: 1,
    owner_id: "user-1",
    archived_at: null,
    custom_args: [],
    visibility: "private",
    permission_mode: "private",
    invocation_targets: [],
    model: "claude",
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    status: "idle",
    skills: [{ id: "s-lyrics", name: "歌词创作", description: "", enabled: true }],
    archived_by: null,
    conversation_starters: [
      {
        label: "Try an example",
        prompt: "A warm song about a family trip to Singapore",
      },
    ],
    ...overrides,
  };
}

const DEFAULT_AGENT = agent();

const IMAGE_RULE = { kinds: ["image"], min: 0, max: 4, maxBytes: 25 << 20 };
const REQUIRED_IMAGE_RULE = {
  kinds: ["image"],
  min: 1,
  max: 1,
  maxBytes: 25 << 20,
};

function fileInput(): HTMLInputElement {
  const input = document.querySelector<HTMLInputElement>('input[type="file"]');
  if (!input) throw new Error("file input not rendered");
  return input;
}

function pickedFile(name = "reference.png", type = "image/png") {
  return new File(["x"], name, { type });
}

function detail(
  overrides: Partial<AuroraGenerationDetail> = {},
): AuroraGenerationDetail {
  return {
    id: "gen-1",
    skillId: "content-creation-lyrics-songwriting",
    prompt: "a launch poster",
    status: "queued",
    creditsReserved: 60 * MICRO,
    creditsCharged: 0,
    error: null,
    createdAt: "2026-09-23T00:00:00Z",
    taskId: "",
    issueIdentifier: "",
    assets: [],
    ...overrides,
  };
}

function renderComposer(
  props: Partial<Parameters<typeof GenerationComposer>[0]>,
) {
  return renderWithI18n(
    <NavigationProvider value={stubNavigationAdapter()}>
      <GenerationComposer
        agent={DEFAULT_AGENT}
        skill={skill()}
        open
        onOpenChange={vi.fn()}
        {...props}
      />
    </NavigationProvider>,
  );
}

async function submitPrompt(prompt = "a launch poster") {
  const user = userEvent.setup();
  await user.type(screen.getByLabelText("What should it make?"), prompt);
  await user.click(screen.getByRole("button", { name: "Generate" }));
  return user;
}

/**
 * A read, shaped the way `parseAurora*` hands it over: the payload plus whether
 * the body behind it was readable. Used for both the create mutation's result
 * and the query's `data`.
 */
function payload<T>(value: T, degraded = false) {
  return { value, degraded };
}

/**
 * Swap window.location for one whose assign can be asserted. jsdom's own
 * Location is read-only and real navigation is unimplemented, so the test uses
 * a stand-in and restores the original afterwards.
 */
function stubLocationAssign(assign: ReturnType<typeof vi.fn>): () => void {
  const original = window.location;
  Object.defineProperty(window, "location", {
    writable: true,
    configurable: true,
    value: { assign, href: original.href, origin: original.origin },
  });
  return () =>
    Object.defineProperty(window, "location", {
      writable: true,
      configurable: true,
      value: original,
    });
}

describe("GenerationComposer", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.detail.mockReturnValue({
      data: undefined,
      isPending: true,
      isError: false,
      refetch: mocks.detailRefetch,
    });
    mocks.balance.mockReturnValue({
      data: payload({ availableMicro: 50 * MICRO }),
      isPending: false,
    });
  });

  it("adds optional guidance to the original prompt and keeps the expert ID", async () => {
    const user = userEvent.setup();
    mocks.create.mockResolvedValue(payload(detail()));
    renderComposer({});
    await user.type(screen.getByLabelText("What should it make?"), "A song about courage");
    expect(screen.getByRole("button", { name: "Try an example" })).toBeDisabled();
    await user.type(screen.getByLabelText("Who is it for? (optional)"), "Young children");
    await user.type(screen.getByLabelText("Style & requirements (optional)"), "Simple and warm");
    await user.click(screen.getByRole("button", { name: "Generate" }));
    expect(mocks.create).toHaveBeenCalledWith(expect.objectContaining({
      skillId: "content-creation-lyrics-songwriting",
      prompt: expect.stringMatching(/A song about courage[\s\S]*Young children[\s\S]*Simple and warm/),
    }));
  });

  it("groups the labelled prompt and submit action in the Aurora drawer", () => {
    renderComposer({});
    const drawer = screen.getByRole("dialog", { name: "Write lyrics" });
    expect(drawer).toHaveClass("aurora-drawer");
    const heading = screen.getByRole("heading", { name: "Write lyrics" });
    const header = heading.closest(".aurora-drawer-head");
    expect(header).not.toBeNull();
    expect(header).toHaveClass("flex-row");
    expect(header?.querySelector(".aurora-avatar--lg")).toHaveAttribute(
      "aria-hidden",
      "true",
    );
    const field = screen.getByLabelText("What should it make?");
    // The field carries its own surface and focus ring now; the panel is a flat
    // card rather than one large focusable box.
    expect(field).toHaveClass("aurora-textarea");
    expect(field.parentElement).toHaveClass("aurora-field");
    const form = field.closest("form");
    expect(form).toHaveClass("aurora-composer");
    expect(
      within(form!).getByRole("button", { name: "Generate" }).parentElement,
    ).toHaveClass("aurora-composer-tools");
    expect(drawer).toHaveAccessibleDescription("60 credits per generation");
  });

  it("keeps the running status live while applying the shared working pill", async () => {
    mocks.create.mockResolvedValue(payload(detail()));
    mocks.detail.mockReturnValue({
      data: payload(detail({ status: "running" })),
      isPending: false,
    });
    renderComposer({});
    await submitPrompt();
    expect(screen.getByRole("status")).toHaveTextContent("Generating");
    expect(screen.getByRole("status")).toHaveClass(
      "aurora-status-pill",
      "aurora-status-pill--working",
    );
  });

  it("sends the prompt and follows the generation it started", async () => {
    mocks.create.mockResolvedValue(payload(detail({ status: "queued" })));
    mocks.detail.mockReturnValue({
      data: payload(detail({ status: "running" })),
      isPending: false,
    });

    renderComposer({});
    await submitPrompt();

    expect(mocks.create).toHaveBeenCalledWith({
      skillId: "content-creation-lyrics-songwriting",
      prompt: "a launch poster",
      attachmentIds: [],
    });
    // The detail read is what the poll is attached to, so it must be asking
    // about the id the server just returned.
    expect(mocks.detail).toHaveBeenCalledWith("gen-1");
    expect(await screen.findByText("Generating")).toBeInTheDocument();
  });

  it("refuses to submit a skill that has not launched", () => {
    renderComposer({ skill: skill({ available: false }) });

    expect(screen.getByRole("button", { name: "Generate" })).toBeDisabled();
    expect(screen.getByText("Not available yet")).toBeInTheDocument();
  });

  it("refuses to submit an empty prompt", async () => {
    const user = userEvent.setup();
    renderComposer({});

    const submit = screen.getByRole("button", { name: "Generate" });
    expect(submit).toBeDisabled();

    await user.type(screen.getByLabelText("What should it make?"), "a poster");
    expect(submit).toBeEnabled();
  });

  it("turns a 402 into the top-up prompt, with the shortfall", async () => {
    mocks.create.mockRejectedValue(
      new ApiError("insufficient credits", 402, "Payment Required"),
    );

    renderComposer({});
    await submitPrompt();

    expect(await screen.findByText("Not enough credits")).toBeInTheDocument();
    expect(
      screen.getByText("This skill costs 60 credits and you have 50."),
    ).toBeInTheDocument();
  });

  it("turns a 429 into the limits prompt", async () => {
    mocks.create.mockRejectedValue(
      new ApiError("too many requests", 429, "Too Many Requests"),
    );

    renderComposer({});
    await submitPrompt();

    expect(await screen.findByText("Limit reached")).toBeInTheDocument();
    expect(screen.queryByText("Not enough credits")).not.toBeInTheDocument();
  });

  it("keeps the prompt after a failed submit so it can be retried", async () => {
    mocks.create.mockRejectedValue(new Error("network down"));

    renderComposer({});
    await submitPrompt();

    expect(
      await screen.findByText("Could not start the generation. Try again."),
    ).toBeInTheDocument();
    expect(screen.getByLabelText("What should it make?")).toHaveValue(
      "a launch poster",
    );
  });

  it("shows the finished assets and the app's library link", async () => {
    mocks.create.mockResolvedValue(payload(detail()));
    mocks.detail.mockReturnValue({
      data: payload(
        detail({
          status: "completed",
          assets: [
            {
              id: "asset-1",
              generationId: "gen-1",
              kind: "image",
              mediaUrl: "https://cdn.test/poster.png",
              format: "png",
              createdAt: "2026-09-23T00:00:00Z",
            },
          ],
        }),
      ),
      isPending: false,
    });

    renderComposer({ worksHref: "/acme/works" });
    await submitPrompt();

    expect(await screen.findByText("Result")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Open library" })).toHaveAttribute(
      "href",
      "/acme/works",
    );
  });

  it("omits the library link when the host app supplies no route", async () => {
    mocks.create.mockResolvedValue(payload(detail()));
    mocks.detail.mockReturnValue({
      data: payload(detail({ status: "completed" })),
      isPending: false,
    });

    renderComposer({});
    await submitPrompt();

    expect(await screen.findByText("Result")).toBeInTheDocument();
    expect(
      screen.queryByRole("link", { name: "Open library" }),
    ).not.toBeInTheDocument();
  });

  it("spends the submit button once a generation is running", async () => {
    // A second POST reserves the skill's credits again, and the drawer can only
    // follow one generation — so the button is not a retry once the server has
    // accepted the first call.
    mocks.create.mockResolvedValue(payload(detail()));
    mocks.detail.mockReturnValue({
      data: payload(detail({ status: "running" })),
      isPending: false,
    });

    renderComposer({});
    await submitPrompt();

    const submit = await screen.findByRole("button", { name: "Generate" });
    expect(submit).toBeDisabled();
    expect(mocks.create).toHaveBeenCalledTimes(1);
  });

  it("tells the user not to retry when the create response could not be read", async () => {
    // A 2xx whose body will not parse means the generation may well exist and
    // its credits are already reserved, so the generic "try again" copy would
    // walk the user into a second charge.
    mocks.create.mockResolvedValue(payload(null, true));

    renderComposer({});
    await submitPrompt();

    expect(
      await screen.findByText(
        "The generation may already have started, but its response could not be read. Check My works before starting another.",
      ),
    ).toBeInTheDocument();
    expect(
      screen.queryByText("Could not start the generation. Try again."),
    ).not.toBeInTheDocument();
  });

  it("spends the submit button when the create response could not be read", async () => {
    // The copy sends the user to the library before starting another, so the
    // button has to agree with it: a second POST here reserves the skill's
    // credits again for a generation the first one may well have started.
    mocks.create.mockResolvedValue(payload(null, true));

    const user = userEvent.setup();
    renderComposer({});
    await submitPrompt();

    const submit = await screen.findByRole("button", { name: "Generate" });
    await user.type(screen.getByLabelText("What should it make?"), "again");

    expect(submit).toBeDisabled();
    expect(mocks.create).toHaveBeenCalledTimes(1);
  });

  it("keeps reporting the run when the progress read fails, and can retry it", async () => {
    // The poll gives up on a failed read, so this screen is the only way back —
    // and rendering nothing here is what let the drawer look like a submit that
    // never happened.
    const user = userEvent.setup();
    mocks.create.mockResolvedValue(payload(detail()));
    mocks.detail.mockReturnValue({
      data: undefined,
      isPending: false,
      isError: true,
      refetch: mocks.detailRefetch,
    });

    renderComposer({});
    await submitPrompt();

    expect(
      await screen.findByText("Could not read this generation's progress."),
    ).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Try again" }));
    expect(mocks.detailRefetch).toHaveBeenCalled();
  });

  it("keeps the last status it read when a progress read fails", async () => {
    // React Query keeps the data a failed refetch could not replace, so one
    // dropped poll tick reports the error *beside* the last known status
    // instead of replacing a status that was read successfully a moment ago.
    mocks.create.mockResolvedValue(payload(detail()));
    mocks.detail.mockReturnValue({
      data: payload(detail({ status: "running" })),
      isPending: false,
      isError: true,
      refetch: mocks.detailRefetch,
    });

    renderComposer({});
    await submitPrompt();

    expect(await screen.findByText("Generating")).toBeInTheDocument();
    expect(
      screen.getByText("Could not read this generation's progress."),
    ).toBeInTheDocument();
  });

  it("reports a progress read it could not parse instead of waiting forever", async () => {
    // GH #55: a 2xx body the schema rejected leaves a null generation and
    // resolves the query, so `isError` is false. The drawer used to sit on the
    // spinner for a generation it had no status for.
    mocks.create.mockResolvedValue(payload(detail()));
    mocks.detail.mockReturnValue({
      data: payload(null, true),
      isPending: false,
      refetch: mocks.detailRefetch,
    });

    renderComposer({});
    await submitPrompt();

    expect(
      await screen.findByText("Could not read this generation's progress."),
    ).toBeInTheDocument();
    expect(screen.queryByText("Generating")).not.toBeInTheDocument();
  });

  it("does not state a balance it could not read", async () => {
    // Quoting "you have 0" off an unloaded wallet sends the user to top up
    // against a number the client invented.
    mocks.create.mockRejectedValue(
      new ApiError("insufficient credits", 402, "Payment Required"),
    );
    mocks.balance.mockReturnValue({ data: undefined, isPending: true });

    renderComposer({});
    await submitPrompt();

    expect(await screen.findByText("Not enough credits")).toBeInTheDocument();
    expect(
      screen.getByText("This skill costs 60 credits."),
    ).toBeInTheDocument();
  });

  it("does not state a balance it could not parse", async () => {
    // A corrupted wallet body parses to 0 — a number, not a blank — so this is
    // the one case where the fallback would be quoted as a fact (GH #55).
    mocks.create.mockRejectedValue(
      new ApiError("insufficient credits", 402, "Payment Required"),
    );
    mocks.balance.mockReturnValue({
      data: payload({ availableMicro: 0 }, true),
      isPending: false,
    });

    renderComposer({});
    await submitPrompt();

    expect(await screen.findByText("Not enough credits")).toBeInTheDocument();
    expect(
      screen.getByText("This skill costs 60 credits."),
    ).toBeInTheDocument();
  });

  it("offers the app's checkout route when a skill costs more than the wallet holds", async () => {
    // The shortfall is the one refusal the user can act on, and the drawer is
    // where they hit it — so the way to fix it belongs here too, when the host
    // app has a route to send them to.
    mocks.create.mockRejectedValue(
      new ApiError("insufficient credits", 402, "Payment Required"),
    );

    renderComposer({ topUpHref: "/checkout" });
    await submitPrompt();

    expect(await screen.findByRole("link", { name: "Top up" })).toHaveAttribute(
      "href",
      "/checkout",
    );
  });

  it("offers no checkout link when the host app supplies no route", async () => {
    mocks.create.mockRejectedValue(
      new ApiError("insufficient credits", 402, "Payment Required"),
    );

    renderComposer({});
    await submitPrompt();

    expect(await screen.findByText("Not enough credits")).toBeInTheDocument();
    expect(
      screen.queryByRole("link", { name: "Top up" }),
    ).not.toBeInTheDocument();
  });

  it("derives the image accept filter, label, and limit from the parsed policy", () => {
    renderComposer({ skill: skill({ attachments: [IMAGE_RULE] }) });

    expect(screen.getByLabelText("Reference images")).toBeInTheDocument();
    expect(fileInput()).toHaveAttribute("accept", "image/png,image/jpeg");
    expect(fileInput()).toHaveAttribute("multiple");
  });

  it("derives the document accept filter from the parsed policy", () => {
    renderComposer({
      skill: skill({
        id: "legal-legal-research",
        attachments: [
          { kinds: ["document"], min: 1, max: 1, maxBytes: 25 << 20 },
        ],
      }),
    });

    expect(screen.getByLabelText("Document")).toBeInTheDocument();
    expect(fileInput()).toHaveAttribute("accept", ".txt,.md,.pdf,.docx");
    expect(fileInput()).not.toHaveAttribute("multiple");
  });

  it("derives an audio-or-video accept filter from the parsed policy", () => {
    renderComposer({
      skill: skill({
        id: "media-script-breakdown",
        attachments: [
          { kinds: ["audio", "video"], min: 1, max: 1, maxBytes: 100 << 20 },
        ],
      }),
    });

    expect(screen.getByLabelText("Audio or video")).toBeInTheDocument();
    expect(fileInput()).toHaveAttribute(
      "accept",
      ".wav,.mp3,.ogg,.opus,.mp4,.mov,.webm",
    );
  });

  it("renders no attachment input for a skill the server declares takes none", () => {
    renderComposer({});

    expect(document.querySelector('input[type="file"]')).toBeNull();
  });

  it("presents the file policy as a labelled dropzone over the native control", async () => {
    const user = userEvent.setup();
    mocks.upload.mockResolvedValue({ id: "att-1" });
    renderComposer({ skill: skill({ attachments: [IMAGE_RULE] }) });

    const input = fileInput();
    expect(input).toHaveClass("aurora-file-input");
    expect(input).toHaveAttribute("accept", "image/png,image/jpeg");

    const zone = screen.getByRole("button", { name: "Choose files" });
    expect(zone).toHaveClass("aurora-dropzone");
    expect(zone).toHaveAttribute("aria-describedby", "aurora-attachments-hint");
    // The hint is derived from the same rules the server validates against.
    expect(screen.getByText("PNG, JPEG · up to 4")).toBeInTheDocument();

    await user.upload(input, pickedFile());
    expect(await screen.findByText("reference.png")).toBeInTheDocument();
  });

  it("uploads a file dropped on the dropzone", async () => {
    mocks.upload.mockResolvedValue({ id: "att-drop" });
    renderComposer({ skill: skill({ attachments: [IMAGE_RULE] }) });

    fireEvent.drop(screen.getByRole("button", { name: "Choose files" }), {
      dataTransfer: { files: [pickedFile("dropped.png")] },
    });

    expect(await screen.findByText("dropped.png")).toBeInTheDocument();
    expect(mocks.upload).toHaveBeenCalledTimes(1);
  });

  it("keeps submit disabled until a required attachment is uploaded", async () => {
    const user = userEvent.setup();
    mocks.upload.mockResolvedValue({ id: "att-1" });
    renderComposer({
      skill: skill({ id: "design-ui-prototype", attachments: [REQUIRED_IMAGE_RULE] }),
    });

    const submit = screen.getByRole("button", { name: "Generate" });
    await user.type(screen.getByLabelText("What should it make?"), "a poster");
    expect(submit).toBeDisabled();

    await user.upload(fileInput(), pickedFile());
    expect(await screen.findByText("reference.png")).toBeInTheDocument();
    expect(submit).toBeEnabled();
  });

  it("removes a selected file and re-disables a required submit", async () => {
    const user = userEvent.setup();
    mocks.upload.mockResolvedValue({ id: "att-1" });
    renderComposer({
      skill: skill({ id: "design-ui-prototype", attachments: [REQUIRED_IMAGE_RULE] }),
    });

    await user.type(screen.getByLabelText("What should it make?"), "a poster");
    await user.upload(fileInput(), pickedFile());

    const submit = screen.getByRole("button", { name: "Generate" });
    expect(submit).toBeEnabled();

    await user.click(screen.getByRole("button", { name: "Remove reference.png" }));

    expect(screen.queryByText("reference.png")).not.toBeInTheDocument();
    expect(submit).toBeDisabled();
  });

  it("keeps the prompt when an upload fails and leaves submit blocked", async () => {
    const user = userEvent.setup();
    mocks.upload.mockRejectedValue(new Error("storage down"));
    renderComposer({
      skill: skill({ id: "design-ui-prototype", attachments: [REQUIRED_IMAGE_RULE] }),
    });

    await user.type(screen.getByLabelText("What should it make?"), "a poster");
    await user.upload(fileInput(), pickedFile());

    expect(await screen.findByText("Upload failed.")).toBeInTheDocument();
    expect(screen.getByLabelText("What should it make?")).toHaveValue("a poster");
    expect(screen.getByRole("button", { name: "Generate" })).toBeDisabled();
    expect(mocks.create).not.toHaveBeenCalled();
  });

  it("submits only the attachment ids that finished uploading", async () => {
    const user = userEvent.setup();
    mocks.create.mockResolvedValue(payload(detail()));
    mocks.upload
      .mockResolvedValueOnce({ id: "att-1" })
      .mockRejectedValueOnce(new Error("storage down"));
    renderComposer({ skill: skill({ attachments: [IMAGE_RULE] }) });

    await user.type(screen.getByLabelText("What should it make?"), "a poster");
    await user.upload(fileInput(), [
      pickedFile("first.png"),
      pickedFile("second.png"),
    ]);

    // The failed upload blocks submit, so it is removed before the retry.
    await user.click(
      await screen.findByRole("button", { name: "Remove second.png" }),
    );
    await user.click(screen.getByRole("button", { name: "Generate" }));

    expect(mocks.create).toHaveBeenCalledWith({
      skillId: "content-creation-lyrics-songwriting",
      prompt: "a poster",
      attachmentIds: ["att-1"],
    });
  });

  it("keeps successful selections when the submission fails", async () => {
    const user = userEvent.setup();
    mocks.create.mockRejectedValue(
      new ApiError("server error", 500, "Server Error"),
    );
    mocks.upload.mockResolvedValue({ id: "att-1" });
    renderComposer({ skill: skill({ attachments: [IMAGE_RULE] }) });

    await user.type(screen.getByLabelText("What should it make?"), "a poster");
    await user.upload(fileInput(), pickedFile());
    await user.click(screen.getByRole("button", { name: "Generate" }));

    expect(
      await screen.findByText("Could not start the generation. Try again."),
    ).toBeInTheDocument();
    expect(screen.getByText("reference.png")).toBeInTheDocument();
    expect(screen.getByLabelText("What should it make?")).toHaveValue("a poster");
  });

  it("opens the issue after submit instead of polling the drawer", async () => {
    const assign = vi.fn();
    const restore = stubLocationAssign(assign);
    try {
      mocks.create.mockResolvedValue(
        payload(detail({ issueIdentifier: "MUL-42" })),
      );

      renderComposer({
        issueHref: (identifier) => "/acme/issues/" + identifier,
      });
      await submitPrompt();

      expect(assign).toHaveBeenCalledWith("/acme/issues/MUL-42");
      // The drawer never starts following the generation, so the issue page —
      // not a drawer poll — is where the run is watched.
      expect(mocks.detail).not.toHaveBeenCalledWith("gen-1");
      expect(screen.queryByText("Generating")).not.toBeInTheDocument();
    } finally {
      restore();
    }
  });

  it("keeps the drawer polling when the response has no usable issue identifier", async () => {
    const assign = vi.fn();
    const restore = stubLocationAssign(assign);
    try {
      mocks.create.mockResolvedValue(
        payload(detail({ issueIdentifier: "#42" })),
      );
      mocks.detail.mockReturnValue({
        data: payload(detail({ status: "running" })),
        isPending: false,
      });

      renderComposer({ issueHref: () => null });
      await submitPrompt();

      expect(assign).not.toHaveBeenCalled();
      expect(mocks.detail).toHaveBeenCalledWith("gen-1");
      expect(await screen.findByText("Generating")).toBeInTheDocument();
    } finally {
      restore();
    }
  });

  it("renders the agent's own description and conversation starters", () => {
    renderComposer({});
    expect(screen.getByText("Short lyrics for songs.")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Try an example" }),
    ).toBeInTheDocument();
  });

  it("omits the deliverable and examples when the agent has neither", () => {
    renderComposer({
      agent: agent({ description: "", conversation_starters: [] }),
    });
    expect(
      screen.queryByText("What you will receive"),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Try an example" }),
    ).not.toBeInTheDocument();
  });
});
