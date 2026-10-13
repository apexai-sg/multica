// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { AuroraGeneration } from "@multica/core/aurora";
import {
  AURORA_HISTORY_SKILL_ALL,
  AURORA_HISTORY_STATUS_ALL,
  filterAuroraHistory,
} from "./history-filter";

function generation(
  overrides: Partial<AuroraGeneration> = {},
): AuroraGeneration {
  return {
    id: "gen-1",
    skillId: "poster",
    prompt: "a poster",
    status: "completed",
    creditsReserved: 0,
    creditsCharged: 0,
    error: null,
    createdAt: "",
    taskId: "",
    issueIdentifier: "",
    ...overrides,
  };
}

describe("filterAuroraHistory", () => {
  it("keeps every generation under the All tab and the All skills option", () => {
    const generations = [
      generation({ id: "a", status: "completed" }),
      generation({ id: "b", status: "failed" }),
      generation({ id: "c", status: "running" }),
    ];

    expect(
      filterAuroraHistory(generations, AURORA_HISTORY_STATUS_ALL, AURORA_HISTORY_SKILL_ALL),
    ).toHaveLength(3);
  });

  it("keeps only the tab's terminal status", () => {
    const generations = [
      generation({ id: "done", status: "completed" }),
      generation({ id: "bad", status: "failed" }),
      generation({ id: "busy", status: "running" }),
    ];

    expect(
      filterAuroraHistory(generations, "completed", AURORA_HISTORY_SKILL_ALL).map(
        (item) => item.id,
      ),
    ).toEqual(["done"]);
    expect(
      filterAuroraHistory(generations, "failed", AURORA_HISTORY_SKILL_ALL).map(
        (item) => item.id,
      ),
    ).toEqual(["bad"]);
  });

  it("narrows to one skill", () => {
    const generations = [
      generation({ id: "poster", skillId: "poster" }),
      generation({ id: "video", skillId: "video" }),
    ];

    expect(
      filterAuroraHistory(generations, AURORA_HISTORY_STATUS_ALL, "video").map(
        (item) => item.id,
      ),
    ).toEqual(["video"]);
  });

  it("combines the status tab and the skill", () => {
    const generations = [
      generation({ id: "done-poster", status: "completed", skillId: "poster" }),
      generation({ id: "bad-poster", status: "failed", skillId: "poster" }),
      generation({ id: "done-video", status: "completed", skillId: "video" }),
    ];

    expect(
      filterAuroraHistory(generations, "completed", "poster").map(
        (item) => item.id,
      ),
    ).toEqual(["done-poster"]);
  });
});
