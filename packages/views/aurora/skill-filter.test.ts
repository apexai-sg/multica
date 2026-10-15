// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { AuroraSkill } from "@multica/core/aurora";
import { AURORA_CATEGORY_ALL, filterAuroraSkills } from "./skill-filter";

function skill(overrides: Partial<AuroraSkill> = {}): AuroraSkill {
  return {
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
    ...overrides,
  };
}

// A synthetic three-entry catalog: the filter is pure, so these ids and
// categories exist to exercise it rather than to mirror the shipped experts.
// The unavailable row stays synthetic too — the schema still admits one, and
// the filter must keep listing it in its category.
const CATALOG: AuroraSkill[] = [
  skill(),
  skill({ id: "media-short-video-copy", name: "短视频文案专家", nameEn: "Media Short Video Copy Expert", category: "media" }),
  skill({ id: "excel", name: "Excel 数据分析", nameEn: "Excel Analysis", category: "office", available: false }),
];

describe("filterAuroraSkills", () => {
  it("returns the whole catalog with no query and the all category", () => {
    expect(filterAuroraSkills(CATALOG, "", AURORA_CATEGORY_ALL)).toEqual(
      CATALOG,
    );
  });

  it("narrows to one category and keeps the unavailable ones listed", () => {
    // An unavailable skill still belongs in its category tab — it is the
    // "coming soon" half of the directory, not a hidden row.
    const office = filterAuroraSkills(CATALOG, "", "office");
    expect(office.map((s) => s.id)).toEqual(["excel"]);
  });

  it("matches the English name for a reader who is looking at the English one", () => {
    expect(
      filterAuroraSkills(CATALOG, "poster", AURORA_CATEGORY_ALL).map(
        (s) => s.id,
      ),
    ).toEqual(["poster"]);
  });

  it("matches the Chinese name too, so a search is not language-bound", () => {
    expect(
      filterAuroraSkills(CATALOG, "海报", AURORA_CATEGORY_ALL).map(
        (s) => s.id,
      ),
    ).toEqual(["poster"]);
  });

  it("ignores case and surrounding space", () => {
    expect(
      filterAuroraSkills(CATALOG, "  pOsTeR ", AURORA_CATEGORY_ALL).map(
        (s) => s.id,
      ),
    ).toEqual(["poster"]);
  });

  it("applies the category and the query together", () => {
    expect(filterAuroraSkills(CATALOG, "文案", "office")).toEqual([]);
    expect(
      filterAuroraSkills(CATALOG, "文案", "media").map((s) => s.id),
    ).toEqual(["media-short-video-copy"]);
  });

  it("returns nothing rather than everything when nothing matches", () => {
    expect(filterAuroraSkills(CATALOG, "nothing like this", AURORA_CATEGORY_ALL)).toEqual([]);
  });
});
