import { afterEach, describe, expect, it, vi } from "vitest";
import { setApiInstance } from "@multica/core/api";
import { listAuroraSkills, getAuroraBalance, getAuroraRuntime, getAuroraSubscription } from "@multica/core/aurora";
import { createPreviewApi } from "./fixtures";

afterEach(() => vi.unstubAllGlobals());

describe("local design preview isolation", () => {
  it("serves readable fixture data through the production parsers", async () => {
    const fetch = vi.fn();
    vi.stubGlobal("fetch", fetch);
    setApiInstance(createPreviewApi());
    expect((await listAuroraSkills()).degraded).toBe(false);
    expect((await getAuroraBalance()).degraded).toBe(false);
    expect((await getAuroraRuntime()).degraded).toBe(false);
    expect((await getAuroraSubscription()).tier).toBe("free");
    expect(fetch).not.toHaveBeenCalled();
  });

  it("rejects mutations and unknown reads without a network fallback", async () => {
    const fetch = vi.fn();
    vi.stubGlobal("fetch", fetch);
    const api = createPreviewApi();
    await expect(api.requestJson("/api/aurora/skills", { method: "POST" })).rejects.toThrow();
    await expect(api.requestJson("https://example.test/api/me")).rejects.toThrow();
    await expect(api.logout()).rejects.toThrow();
    expect(fetch).not.toHaveBeenCalled();
  });
});
