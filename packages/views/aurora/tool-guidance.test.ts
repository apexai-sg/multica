// @vitest-environment node
import { describe, expect, it } from "vitest";
import { toolGuidance, orderTools } from "./tool-guidance";
import { filterAuroraSkills } from "./skill-filter";
import type { AuroraSkill } from "@multica/core/aurora";

describe("consumer tool contracts", () => {
  it("does not claim rendered media for the text-only editing route", () => {
    expect(toolGuidance("media-video-editing", "zh-Hans")?.summary).toContain("不交付成片");
    expect(toolGuidance("media-video-editing", "en")?.summary).toContain("no rendered video");
  });
  it("leaves future backend tools intact and rejects prototype keys", () => {
    expect(toolGuidance("future-tool", "en")).toBeUndefined();
    expect(toolGuidance("constructor", "en")).toBeUndefined();
    const items = [{ id: "future-tool" }, { id: "marketing-social-media-operation" }];
    expect(orderTools(items)[0]!.id).toBe("marketing-social-media-operation");
    expect(items[0]!.id).toBe("future-tool");
  });
  it("searches the new task name without modifying the execution identity", () => {
    const skill = { id: "marketing-social-media-operation", name: "社媒运营专家", nameEn: "Social Media Expert", category: "marketing" } as AuroraSkill;
    expect(filterAuroraSkills([skill], "小红书", "all")).toEqual([skill]);
    expect(filterAuroraSkills([skill], "小红书", "media")).toEqual([]);
  });
});
