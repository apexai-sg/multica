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
 * The shipped catalog (`server/internal/aurora/experts_catalog.go`), id by id:
 * the 24 vendored SkillHub experts, six scenes, every one runnable. The counts
 * in these tests are the catalog's own contract, so the fixture mirrors it
 * rather than shrinking to a convenient sample.
 */
const CATALOG: AuroraSkill[] = [
  ["content-creation-lyrics-songwriting", "歌词创作专家", "Content Creation Lyrics Songwriting Expert", "content-creation", 60],
  ["content-creation-novel-writing", "网文/小说创作专家", "Content Creation Novel Writing Expert", "content-creation", 60],
  ["content-creation-screenwriting", "剧本创作专家", "Content Creation Screenwriting Expert", "content-creation", 60],
  ["design-prd-writing", "PRD 撰写专家", "Design Prd Writing Expert", "design", 90],
  ["design-ui-prototype", "UI 原型设计专家", "Design Ui Prototype Expert", "design", 90],
  ["ecommerce-bidding-strategy", "竞价策略专家", "Ecommerce Bidding Strategy Expert", "ecommerce", 80],
  ["ecommerce-pricing-analysis", "定价分析专家", "Ecommerce Pricing Analysis Expert", "ecommerce", 80],
  ["ecommerce-product-copywriting", "商品文案专家", "Ecommerce Product Copywriting Expert", "ecommerce", 80],
  ["ecommerce-product-selection", "选品分析专家", "Ecommerce Product Selection Expert", "ecommerce", 80],
  ["ecommerce-promotion-planning", "促销策划专家", "Ecommerce Promotion Planning Expert", "ecommerce", 80],
  ["ecommerce-user-profiling", "用户画像专家", "Ecommerce User Profiling Expert", "ecommerce", 80],
  ["legal-contract-drafting", "合同起草专家", "Legal Contract Drafting Expert", "legal", 100],
  ["legal-contract-generation", "合同生成专家", "Legal Contract Generation Expert", "legal", 100],
  ["legal-legal-research", "法规检索专家", "Legal Legal Research Expert", "legal", 100],
  ["legal-litigation-strategy", "诉讼策略专家", "Legal Litigation Strategy Expert", "legal", 100],
  ["marketing-ad-copywriting", "广告文案专家", "Marketing Ad Copywriting Expert", "marketing", 80],
  ["marketing-competitor-analysis", "竞品分析专家", "Marketing Competitor Analysis Expert", "marketing", 80],
  ["marketing-event-planning", "活动策划专家", "Marketing Event Planning Expert", "marketing", 80],
  ["marketing-social-media-operation", "社媒运营专家", "Marketing Social Media Operation Expert", "marketing", 80],
  ["marketing-user-growth", "用户增长专家", "Marketing User Growth Expert", "marketing", 80],
  ["media-script-breakdown", "脚本拆解专家", "Media Script Breakdown Expert", "media", 120],
  ["media-short-video-copy", "短视频文案专家", "Media Short Video Copy Expert", "media", 120],
  ["media-storyboard-design", "分镜设计专家", "Media Storyboard Design Expert", "media", 120],
  ["media-video-editing", "视频剪辑专家", "Media Video Editing Expert", "media", 120],
].map(([id, name, nameEn, category, credits]) => ({
  id: id as string,
  name: name as string,
  nameEn: nameEn as string,
  category: category as string,
  credits: credits as number,
  input: ["text", "document"],
  output: ["text"],
  featured: false,
  available: true,
  attachments: [],
}));

const ALL_SKILLS = CATALOG;

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
    expect(screen.getByText("24 个 skill 可用")).toBeVisible();
    const hero = screen.getByRole("heading", { level: 1 }).closest("section");
    expect(hero).toHaveTextContent("24 个 skill 可运行");
    expect(hero).toHaveTextContent("24 个 skill");
    const search = screen.getByRole("searchbox", { name: "搜索功能" });
    expect(search).toBeVisible();
    await user.type(search, "no-matching-skill");
    expect(screen.getByRole("button", { name: "查看全部 skill" })).toBeVisible();
  });

  it("summarises real catalog totals and runtime in the hero", () => {
    renderDirectory();
    const hero = screen.getByRole("heading", { level: 1 }).closest("section");
    expect(hero).toHaveTextContent("Online");
    expect(hero).toHaveTextContent("24 skills runnable");
    expect(hero).toHaveTextContent("24 skills in the catalog");
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
    expect(screen.getByText("24 skills available")).toBeVisible();
    await user.click(screen.getByRole("tab", { name: "Media" }));
    expect(screen.getByText("4 skills available")).toBeVisible();
    const search = screen.getByRole("searchbox", { name: "Search skills" });
    expect(search).toHaveAttribute("data-slot", "input-group-control");
    await user.type(search, "nothing matches");
    expect(screen.getByText("0 skills available")).toBeVisible();
    await user.click(screen.getByRole("button", { name: "Show all skills" }));
    expect(search).toHaveValue("");
    expect(screen.getByRole("tab", { name: "All" })).toHaveAttribute("aria-selected", "true");
    expect(cards()).toHaveLength(24);
  });

  it("keeps each card one named target with capabilities, state and one price", () => {
    renderDirectory();
    const first = screen.getByRole("button", { name: /^Content creation\s*Content Creation Lyrics Songwriting Expert\s*Text\s*Documents\s*Ready\s*60 credits$/ });
    expect(within(first).queryByRole("button")).not.toBeInTheDocument();
    expect(within(first).getAllByText("60 credits")).toHaveLength(1);
    expect(first.querySelector(".aurora-avatar")).toHaveAttribute("aria-hidden", "true");
    expect(first.querySelector(".aurora-card-arrow")).toHaveAttribute("aria-hidden", "true");
    expect(screen.queryByText("Coming soon")).not.toBeInTheDocument();
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

  it("lists the whole catalog, every expert runnable", () => {
    renderDirectory();

    expect(cards()).toHaveLength(24);
    expect(screen.queryByText("Coming soon")).not.toBeInTheDocument();
  });

  it("narrows the grid to one category", async () => {
    const user = userEvent.setup();
    renderDirectory();

    await user.click(screen.getByRole("tab", { name: "Media" }));

    // Four experts share the media scene.
    expect(cards()).toHaveLength(4);
  });

  it("narrows the grid to a search across both catalog names", async () => {
    const user = userEvent.setup();
    renderDirectory();

    await user.type(screen.getByPlaceholderText("Search skills"), "歌词");

    expect(cards()).toHaveLength(1);
    expect(screen.getByRole("button", { name: /Lyrics/ })).toBeInTheDocument();
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

    await user.click(screen.getByRole("button", { name: /Lyrics/ }));

    expect(
      await screen.findByRole("button", { name: "Generate" }),
    ).toBeInTheDocument();
    expect(screen.getByText("60 credits per generation")).toBeInTheDocument();
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
