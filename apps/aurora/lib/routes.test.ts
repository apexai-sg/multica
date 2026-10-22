// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { Workspace } from "@multica/core/types";
import {
  auroraRoutes,
  isWorkspaceSlug,
  resolveAuroraDestination,
} from "./routes";

// Only `slug` is read here — the destination resolver never touches the rest of
// the workspace record, so the fixture says exactly that instead of restating
// twenty unrelated fields that no assertion depends on.
const workspace = (slug: string) => ({ slug }) as Workspace;

describe("auroraRoutes", () => {
  it("builds this app's five destinations under the workspace slug", () => {
    const routes = auroraRoutes("acme");
    expect(routes.skills()).toBe("/acme/skills");
    expect(routes.catalog()).toBe("/acme/skills/connected");
    expect(routes.tools()).toBe("/acme/tools");
    expect(routes.tool("face-swap")).toBe("/acme/tools/face-swap");
    expect(routes.tool("a/b")).toBe("/acme/tools/a%2Fb");
    expect(routes.works()).toBe("/acme/works");
    expect(routes.work("gen-1")).toBe("/acme/works/gen-1");
    expect(routes.history()).toBe("/acme/history");
    expect(routes.runtime()).toBe("/acme/runtimes");
    expect(routes.billing()).toBe("/acme/billing");
  });

  it("encodes the slug so a path segment cannot break out of its own", () => {
    expect(auroraRoutes("a/b").works()).toBe("/a%2Fb/works");
  });
});

describe("resolveAuroraDestination", () => {
  it("opens the first workspace's skill directory", () => {
    expect(resolveAuroraDestination([workspace("acme")])).toBe("/acme/skills");
    expect(
      resolveAuroraDestination([workspace("acme"), workspace("other")]),
    ).toBe("/acme/skills");
  });

  it("reports no destination instead of inventing a route", () => {
    // The app serves no workspace-creation route; a caller that gets null has
    // to say so where it is rather than navigate.
    expect(resolveAuroraDestination([])).toBeNull();
  });
});

describe("isWorkspaceSlug", () => {
  it("accepts the server's slug shape", () => {
    expect(isWorkspaceSlug("acme")).toBe(true);
    expect(isWorkspaceSlug("acme-2")).toBe(true);
    expect(isWorkspaceSlug("a")).toBe(true);
  });

  it("rejects anything that could not name a workspace", () => {
    expect(isWorkspaceSlug(undefined)).toBe(false);
    expect(isWorkspaceSlug(null)).toBe(false);
    expect(isWorkspaceSlug("")).toBe(false);
    expect(isWorkspaceSlug("Acme")).toBe(false);
    expect(isWorkspaceSlug("acme_2")).toBe(false);
    expect(isWorkspaceSlug("-acme")).toBe(false);
    expect(isWorkspaceSlug("acme-")).toBe(false);
    expect(isWorkspaceSlug("acme/../evil")).toBe(false);
    expect(isWorkspaceSlug("//evil.test")).toBe(false);
  });
});
