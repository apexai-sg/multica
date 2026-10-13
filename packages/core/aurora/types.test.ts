// @vitest-environment node
import { describe, expect, it } from "vitest";
import { auroraGenerationSchema, type AuroraGeneration } from "./schema";
import { isAuroraGenerationTerminal } from "./types";

describe("isAuroraGenerationTerminal", () => {
  it("treats completed and failed as final", () => {
    expect(isAuroraGenerationTerminal("completed")).toBe(true);
    expect(isAuroraGenerationTerminal("failed")).toBe(true);
  });

  it("keeps polling the two in-flight statuses", () => {
    expect(isAuroraGenerationTerminal("queued")).toBe(false);
    expect(isAuroraGenerationTerminal("running")).toBe(false);
  });

  it("keeps polling an unrecognised status rather than assuming it is final", () => {
    // A newer server could add a status. Reading it as final would stop the
    // poll before the generation's assets existed; reading it as in-flight
    // costs one request every three seconds until the server settles it.
    expect(isAuroraGenerationTerminal("paused-for-review")).toBe(false);
  });

  it("keeps polling a round that stopped to ask a question", () => {
    // needs_input is a third, declarative ending: the round ended without a
    // failure and resumes when the person replies, so it is not final.
    expect(isAuroraGenerationTerminal("needs_input")).toBe(false);
  });

  it("keeps polling when the detail body could not be read", () => {
    expect(isAuroraGenerationTerminal(undefined)).toBe(false);
    expect(isAuroraGenerationTerminal("")).toBe(false);
  });
});

describe("AuroraGeneration", () => {
  it("types the exposed task id alongside the settled fields", () => {
    // The entity type is inferred from the schema; this pins that the task id
    // the list and detail endpoints expose survives into the typed value.
    const generation: AuroraGeneration = auroraGenerationSchema.parse({
      id: "gen-1",
      skillId: "poster",
      prompt: "a cat",
      status: "queued",
      taskId: "task-1",
    });

    expect(generation.taskId).toBe("task-1");
  });
});
