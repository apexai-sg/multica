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

// See aurora-avatar.test.tsx: the real resolver needs the app's API singleton,
// and the directory only has to hand the URL through to the tile.
vi.mock("@multica/core/workspace/avatar-url", () => ({
  resolvePublicFileUrl: (url: string | null | undefined) => url ?? null,
}));

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
  avatarUrl: "",
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
  return within(document.querySelector<HTMLElement>(".aurora-card-grid")!).getAllByRole("button");
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

  it("prioritizes eight useful tasks without changing backend IDs or prices", async () => {
    const user = userEvent.setup();
    renderDirectory();
    expect(cards()).toHaveLength(8);
    expect(cards()[0]).toHaveTextContent("Social posts & content plans");
    expect(cards()[0]).toHaveTextContent("80 credits");
    await user.click(screen.getByRole("tab", { name: "All tools" }));
    expect(cards()).toHaveLength(24);
  });

  it("uses consumer language and explains the text deliverable in Chinese", async () => {
    const user = userEvent.setup();
    renderWithI18n(<NavigationProvider value={stubNavigationAdapter()}><SkillDirectory /></NavigationProvider>, { locale: "zh-Hans" });
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("你今天想做什么？");
    expect(screen.getByText("8 个功能")).toBeVisible();
    await user.type(screen.getByRole("searchbox"), "视频剪辑");
    const card = cards()[0]!;
    expect(card).toHaveTextContent("视频剪辑方案");
    expect(card).toHaveTextContent("当前不交付成片");
    expect(card).toHaveTextContent("Markdown");
  });

  it("searches new task names and original expert names across the full catalog", async () => {
    const user = userEvent.setup();
    renderDirectory();
    const search = screen.getByRole("searchbox");
    await user.type(search, "小红书");
    expect(cards()).toHaveLength(1);
    expect(cards()[0]).toHaveTextContent("Social posts & content plans");
    await user.clear(search);
    await user.type(search, "歌词创作专家");
    expect(cards()).toHaveLength(1);
    expect(cards()[0]).toHaveTextContent("Write lyrics");
  });

  it("keeps planned media and office tools non-interactive with no made-up prices", () => {
    renderDirectory();
    const section = screen.getByRole("heading", { name: "Images, video & office tools" }).closest("section")!;
    expect(within(section).getByText("Image to video")).toBeVisible();
    expect(within(section).getByText("PowerPoint creation")).toBeVisible();
    expect(within(section).queryByRole("button")).not.toBeInTheDocument();
    expect(section).not.toHaveTextContent(/\d+ credits/);
    expect(mocks.create).not.toHaveBeenCalled();
  });

  it("does not duplicate a tool that a later server adds to the catalog", () => {
    mocks.skills.mockReturnValue({ data: { value: [...CATALOG, { ...CATALOG[0], id: "poster", name: "海报制作", nameEn: "Poster design", output: ["image"] }], degraded: false }, isPending: false });
    renderDirectory();
    const section = screen.getByRole("heading", { name: "Images, video & office tools" }).closest("section")!;
    expect(within(section).queryByText("Poster design")).not.toBeInTheDocument();
  });

  it("keeps a single named target and preserves the carrier avatar", () => {
    mocks.skills.mockReturnValue({ data: { value: CATALOG.map((skill) => ({ ...skill, avatarUrl: "/uploads/portrait.png" })), degraded: false }, isPending: false });
    renderDirectory();
    expect(cards()[0]!.querySelector(".aurora-avatar-img")).toHaveAttribute("src", "/uploads/portrait.png");
    expect(within(cards()[0]!).queryByRole("button")).not.toBeInTheDocument();
  });

  it("filters categories and clears both filters when nothing matches", async () => {
    const user = userEvent.setup();
    renderDirectory();
    await user.click(screen.getByRole("tab", { name: "Video scripts" }));
    expect(cards()).toHaveLength(4);
    await user.type(screen.getByRole("searchbox"), "nothing matches");
    expect(screen.getByText("0 tools shown")).toBeVisible();
    await user.click(screen.getByRole("button", { name: "Show all tools" }));
    expect(screen.getByRole("searchbox")).toHaveValue("");
    expect(cards()).toHaveLength(24);
  });

  it.each([
    { data: undefined, isPending: true },
    { data: { value: [], degraded: true }, isPending: false },
    { data: undefined, isPending: false, isError: true },
  ])("does not present an unread catalog as zero tools: %j", (result) => {
    mocks.skills.mockReturnValue(result);
    renderDirectory();
    expect(screen.queryByText("0 tools shown")).not.toBeInTheDocument();
  });

  it("reports a successfully read empty catalog honestly", () => {
    mocks.skills.mockReturnValue({ data: { value: [], degraded: false }, isPending: false });
    renderDirectory();
    expect(screen.getByText("0 tools shown")).toBeVisible();
  });

  it.each([true, false])("offers retry for a failed or degraded catalog (degraded=%s)", async (degraded) => {
    const user = userEvent.setup();
    const refetch = vi.fn();
    mocks.skills.mockReturnValue({ data: { value: [], degraded }, isPending: false, isError: !degraded, refetch });
    renderDirectory();
    await user.click(screen.getByRole("button", { name: "Try again" }));
    expect(refetch).toHaveBeenCalledOnce();
  });

  it("opens a guided composer with the server price", async () => {
    const user = userEvent.setup();
    renderDirectory();
    await user.click(cards()[0]!);
    expect(await screen.findByRole("button", { name: "Generate" })).toBeDisabled();
    expect(screen.getByText("80 credits per generation")).toBeVisible();
    await user.click(screen.getByRole("button", { name: "Try an example" }));
    expect((screen.getByLabelText("What should it make?") as HTMLTextAreaElement).value).toContain("Singapore");
    expect(screen.getByRole("button", { name: "Generate" })).toBeEnabled();
  });
});
