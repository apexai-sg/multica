import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { AuroraSkill } from "@multica/core/aurora";
import { renderWithI18n } from "../test/i18n";
import { stubNavigationAdapter } from "../test/navigation";
import { NavigationProvider } from "../navigation";

const mocks = vi.hoisted(() => ({
  skills: vi.fn(),
  runtime: vi.fn(),
  create: vi.fn(),
  detail: vi.fn(),
  balance: vi.fn(),
}));

vi.mock("@multica/core/aurora", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/aurora")>();
  return {
    ...actual,
    useAuroraSkills: () => mocks.skills(),
    useAuroraRuntime: () => mocks.runtime(),
    // The directory owns the drawer, so the composer's hooks resolve here too.
    useCreateAuroraGeneration: () => ({
      mutateAsync: mocks.create,
      isPending: false,
    }),
    useAuroraGenerationDetail: (id: string) => mocks.detail(id),
    useAuroraBalance: () => mocks.balance(),
  };
});

import { SkillDirectory } from "./skill-directory";

/**
 * The shipped catalog (`server/internal/aurora/catalog.go`), id by id: 16
 * entries, of which avatar-video, ppt and excel are listed but not runnable.
 * The counts in these tests are the catalog's own contract, so the fixture
 * mirrors it rather than shrinking to a convenient sample.
 */
const CATALOG: AuroraSkill[] = [
  ["poster", "海报制作", "Poster", "image", 76],
  ["xhs-image", "小红书图片", "Xiaohongshu Image", "image", 62],
  ["product-image", "商品图制作", "Product Image", "image", 86],
  ["text-image", "文字生成图片", "Text to Image", "image", 68],
  ["image-edit", "图片修改", "Image Edit", "image", 52],
  ["id-photo", "证件照制作", "ID Photo", "image", 36],
  ["image-video", "图片生成视频", "Image to Video", "video", 188],
  ["text-video", "文字生成视频", "Text to Video", "video", 168],
  ["video-captions", "视频剪辑与字幕", "Video Captions", "video", 98],
  ["xhs-copy", "小红书文案", "Xiaohongshu Copy", "content", 26],
  ["resume", "简历制作", "Resume", "office", 42],
  ["document-summary", "文件总结", "Document Summary", "office", 38],
  ["transcription", "录音转文字", "Transcription", "office", 30],
].map(([id, name, nameEn, category, credits]) => ({
  id: id as string,
  name: name as string,
  nameEn: nameEn as string,
  category: category as string,
  credits: credits as number,
  input: id === "poster" ? ["text", "image"] : ["text"],
  output: ["image"],
  featured: false,
  available: true,
  attachments: [],
}));

const UNAVAILABLE: AuroraSkill[] = [
  {
    id: "avatar-video",
    name: "数字人口播",
    nameEn: "Avatar Video",
    category: "video",
    credits: 148,
    input: ["text"],
    output: ["video"],
    featured: false,
    available: false,
    attachments: [],
  },
  {
    id: "ppt",
    name: "PPT 制作",
    nameEn: "PPT",
    category: "office",
    credits: 82,
    input: ["text"],
    output: ["pptx"],
    featured: false,
    available: false,
    attachments: [],
  },
  {
    id: "excel",
    name: "Excel 数据分析",
    nameEn: "Excel Analysis",
    category: "office",
    credits: 46,
    input: ["text"],
    output: ["xlsx"],
    featured: false,
    available: false,
    attachments: [],
  },
];

const ALL_SKILLS = [...CATALOG.slice(0, 9), UNAVAILABLE[0], ...CATALOG.slice(9), ...UNAVAILABLE.slice(1)];

function renderDirectory() {
  return renderWithI18n(
    <NavigationProvider value={stubNavigationAdapter()}>
      <SkillDirectory />
    </NavigationProvider>,
  );
}

/** The skill cards, as opposed to the category tabs and the search box. */
function cards() {
  return within(screen.getByRole("list")).getAllByRole("button");
}

describe("SkillDirectory", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.runtime.mockReturnValue({
      data: { value: { state: "online" }, degraded: false },
      isPending: false,
    });
    mocks.skills.mockReturnValue({
      data: { value: ALL_SKILLS, degraded: false },
      isPending: false,
    });
    mocks.detail.mockReturnValue({ data: undefined, isPending: true });
    mocks.balance.mockReturnValue({
      data: { value: { availableMicro: 0 }, degraded: false },
      isPending: false,
    });
  });

  it.each([0, 1, 3])("pluralizes real counts (%i) and shows each hero numeral once", (count) => {
    mocks.skills.mockReturnValue({ data: { value: CATALOG.slice(0, count), degraded: false }, isPending: false });
    renderDirectory();
    const noun = count === 1 ? "skill" : "skills";
    expect(screen.getByText(`${count} ${noun} available`)).toBeVisible();
    const stats = screen.getByRole("heading", { level: 1 }).closest("section")!.querySelectorAll(".aurora-hero-stats > span");
    expect(stats).toHaveLength(2);
    expect(stats[0]!.textContent).toBe(`${count} ${noun} runnable`);
    expect(stats[1]!.textContent).toBe(`${count} ${noun} in the catalog`);
    for (const stat of stats) {
      expect(stat.querySelectorAll("strong")).toHaveLength(1);
      expect(stat.querySelector("strong")).toHaveTextContent(String(count));
    }
  });

  it("uses the skill glossary for new Chinese directory copy without renaming legacy search", async () => {
    const user = userEvent.setup();
    renderWithI18n(<NavigationProvider value={stubNavigationAdapter()}><SkillDirectory /></NavigationProvider>, { locale: "zh-Hans" });
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("挑一个 skill，Aurora 帮你跑完。");
    expect(screen.getByText("16 个 skill 可用")).toBeVisible();
    const hero = screen.getByRole("heading", { level: 1 }).closest("section");
    expect(hero).toHaveTextContent("13 个 skill 可运行");
    expect(hero).toHaveTextContent("16 个 skill");
    const search = screen.getByRole("searchbox", { name: "搜索功能" });
    expect(search).toBeVisible();
    await user.type(search, "no-matching-skill");
    expect(screen.getByRole("button", { name: "查看全部 skill" })).toBeVisible();
  });

  it("summarises real catalog totals and runtime in the hero", () => {
    renderDirectory();
    const hero = screen.getByRole("heading", { level: 1 }).closest("section");
    expect(hero).toHaveTextContent("Online");
    expect(hero).toHaveTextContent("13 skills runnable");
    expect(hero).toHaveTextContent("16 skills in the catalog");
    expect(hero?.querySelector(".aurora-hero-orbit")).toHaveAttribute("aria-hidden", "true");
  });

  it.each([
    { isPending: true },
    { isError: true },
    { data: { value: { state: "online" }, degraded: true } },
  ])("does not claim a runtime state from an unreadable read: %j", (overrides) => {
    mocks.runtime.mockReturnValue({ data: { value: { state: "online" }, degraded: false }, ...overrides });
    renderDirectory();
    expect(screen.queryByText("Online")).not.toBeInTheDocument();
  });

  it("counts visible cards and clears both search and category from the empty state", async () => {
    const user = userEvent.setup();
    renderDirectory();
    expect(screen.getByText("16 skills available")).toBeVisible();
    await user.click(screen.getByRole("tab", { name: "Video" }));
    expect(screen.getByText("4 skills available")).toBeVisible();
    const search = screen.getByRole("searchbox", { name: "Search skills" });
    expect(search).toHaveAttribute("data-slot", "input-group-control");
    await user.type(search, "nothing matches");
    expect(screen.getByText("0 skills available")).toBeVisible();
    await user.click(screen.getByRole("button", { name: "Show all skills" }));
    expect(search).toHaveValue("");
    expect(screen.getByRole("tab", { name: "All" })).toHaveAttribute("aria-selected", "true");
    expect(cards()).toHaveLength(16);
  });

  it("keeps each card one named target with capabilities, state and one price", () => {
    renderDirectory();
    const poster = screen.getByRole("button", { name: /^Image\s*Poster\s*Text\s*Images\s*Ready\s*76 credits$/ });
    expect(within(poster).queryByRole("button")).not.toBeInTheDocument();
    expect(within(poster).getAllByText("76 credits")).toHaveLength(1);
    expect(poster.querySelector(".aurora-avatar")).toHaveAttribute("aria-hidden", "true");
    expect(poster.querySelector(".aurora-card-arrow")).toHaveAttribute("aria-hidden", "true");
    expect(screen.getByRole("button", { name: /PPT/ })).toHaveTextContent("Coming soon");
  });

  it.each([
    { data: undefined, isPending: true },
    { data: { value: [], degraded: true }, isPending: false },
    { data: undefined, isPending: false, isError: true },
  ])("does not present an unread catalog as zero skills: %j", (result) => {
    mocks.skills.mockReturnValue(result);
    renderDirectory();
    expect(screen.queryByText(/0 skills (runnable|in the catalog|available)/)).not.toBeInTheDocument();
  });

  it("reports a successfully read empty catalog honestly", () => {
    mocks.skills.mockReturnValue({ data: { value: [], degraded: false }, isPending: false });
    renderDirectory();
    expect(screen.getByText("0 skills available")).toBeVisible();
    expect(screen.getByText("No matching skills")).toBeVisible();
  });

  it("lists the whole catalog and flags the skills that have not launched", () => {
    renderDirectory();

    expect(cards()).toHaveLength(16);
    expect(screen.getAllByText("Coming soon")).toHaveLength(3);
  });

  it("narrows the grid to one category", async () => {
    const user = userEvent.setup();
    renderDirectory();

    await user.click(screen.getByRole("tab", { name: "Video" }));

    // Four video skills, one of which is the unavailable avatar-video.
    expect(cards()).toHaveLength(4);
    expect(screen.getAllByText("Coming soon")).toHaveLength(1);
  });

  it("narrows the grid to a search across both catalog names", async () => {
    const user = userEvent.setup();
    renderDirectory();

    await user.type(screen.getByPlaceholderText("Search skills"), "海报");

    expect(cards()).toHaveLength(1);
    expect(screen.getByRole("button", { name: /Poster/ })).toBeInTheDocument();
  });

  it("says so when a search matches nothing", async () => {
    const user = userEvent.setup();
    renderDirectory();

    await user.type(screen.getByPlaceholderText("Search skills"), "zzzz");

    expect(screen.getByText("No matching skills")).toBeInTheDocument();
    expect(screen.queryByRole("list")).not.toBeInTheDocument();
  });

  it("offers the composer for the skill that was chosen", async () => {
    const user = userEvent.setup();
    renderDirectory();

    await user.click(screen.getByRole("button", { name: /Poster/ }));

    expect(
      await screen.findByRole("button", { name: "Generate" }),
    ).toBeInTheDocument();
    expect(screen.getByText("76 credits per generation")).toBeInTheDocument();
  });

  it("offers the composer for a skill that has not launched, so the reason is reachable", async () => {
    const user = userEvent.setup();
    renderDirectory();

    await user.click(screen.getByRole("button", { name: /PPT/ }));

    expect(await screen.findByText("Not available yet")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Generate" })).toBeDisabled();
  });

  it("offers a retry instead of an empty grid when the catalog cannot be read", async () => {
    const user = userEvent.setup();
    const refetch = vi.fn();
    mocks.skills.mockReturnValue({
      data: undefined,
      isPending: false,
      isError: true,
      refetch,
    });
    renderDirectory();

    expect(
      screen.getByText("Could not load the skill directory"),
    ).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Try again" }));
    expect(refetch).toHaveBeenCalled();
  });

  it("reports a catalog it could not read as a failed load, not an empty directory", async () => {
    // GH #55: a 200 whose body the schema rejected parses to an empty catalog
    // and resolves the query, so `isError` is false. Without the degraded flag
    // this rendered as "No matching skills" — a claim the client never read.
    const user = userEvent.setup();
    const refetch = vi.fn();
    mocks.skills.mockReturnValue({
      data: { value: [], degraded: true },
      isPending: false,
      refetch,
    });

    renderDirectory();

    expect(
      screen.getByText("Could not load the skill directory"),
    ).toBeInTheDocument();
    expect(screen.queryByText("No matching skills")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Try again" }));
    expect(refetch).toHaveBeenCalled();
  });
});
