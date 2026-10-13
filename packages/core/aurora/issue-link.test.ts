// @vitest-environment node
import { describe, expect, it } from "vitest";

import { auroraIssueHref, auroraIssuePath } from "./issue-link";

describe("auroraIssuePath", () => {
  it("builds the workspace issue path for a PREFIX-N identifier", () => {
    expect(auroraIssuePath("acme", "MUL-42")).toBe("/acme/issues/MUL-42");
  });

  it("percent-encodes the slug so it cannot break out of its own segment", () => {
    expect(auroraIssuePath("a/b", "MUL-42")).toBe("/a%2Fb/issues/MUL-42");
  });

  it("returns null for an identifier that is not PREFIX-N", () => {
    expect(auroraIssuePath("acme", "")).toBeNull();
    expect(auroraIssuePath("acme", "#42")).toBeNull();
    expect(auroraIssuePath("acme", "not an identifier")).toBeNull();
    expect(auroraIssuePath("acme", "-42")).toBeNull();
  });
});

describe("auroraIssueHref", () => {
  it("joins the configured origin to the issue path", () => {
    expect(
      auroraIssueHref("acme", "MUL-42", "https://mca.example/"),
    ).toBe("https://mca.example/acme/issues/MUL-42");
  });

  it("returns the relative path when the origin is empty", () => {
    expect(auroraIssueHref("acme", "MUL-42", "")).toBe(
      "/acme/issues/MUL-42",
    );
    expect(auroraIssueHref("acme", "MUL-42", undefined)).toBe(
      "/acme/issues/MUL-42",
    );
  });

  it("returns null for an identifier that is not PREFIX-N", () => {
    expect(auroraIssueHref("acme", "", "https://mca.example")).toBeNull();
    expect(auroraIssueHref("acme", "#42", "https://mca.example")).toBeNull();
  });
});
