import { describe, expect, it } from "vitest";
import type { Agent } from "../types";
import { filterAssignableAgents } from "./assignable";

const agent = (overrides: Partial<Agent> = {}) =>
  ({
    id: "agent-1",
    owner_id: "user-1",
    visibility: "workspace",
    permission_mode: "public_to",
    archived_at: null,
    ...overrides,
  }) as Agent;

describe("filterAssignableAgents", () => {
  it("drops archived agents", () => {
    const kept = filterAssignableAgents(
      [agent({ id: "live" }), agent({ id: "archived", archived_at: "2026-01-01T00:00:00Z" })],
      { userId: "user-1", role: "owner" },
    );
    expect(kept.map((entry) => entry.id)).toEqual(["live"]);
  });

  it("keeps the caller's own agent regardless of visibility mode", () => {
    const kept = filterAssignableAgents(
      [agent({ id: "own", owner_id: "user-1", permission_mode: "private" })],
      { userId: "user-1", role: "member" },
    );
    expect(kept.map((entry) => entry.id)).toEqual(["own"]);
  });

  it("drops an agent the caller may not invoke", () => {
    const kept = filterAssignableAgents(
      [agent({ id: "someone-elses", owner_id: "user-2", permission_mode: "private" })],
      { userId: "user-1", role: "member" },
    );
    expect(kept).toEqual([]);
  });

  it("treats an unresolved member list as no assignable agents", () => {
    expect(filterAssignableAgents(undefined, { userId: "user-1", role: null })).toEqual([]);
  });
});
