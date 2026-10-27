// @vitest-environment node
import { beforeEach, describe, expect, it } from "vitest";
import { ApiError, setApiInstance } from "../api";
import type { ApiClient } from "../api/client";
import {
  auroraAssetDownloadPath,
  createAuroraBillingPortal,
  createAuroraCheckout,
  createAuroraGeneration,
  createAuroraToolRun,
  createAuroraTopupCheckout,
  deleteAuroraAsset,
  getAuroraRuntime,
  getAuroraSubscription,
  isAuroraCheckoutConflictError,
  isAuroraDegraded,
  isAuroraGenerationNotFoundError,
  isAuroraInsufficientCreditsError,
  isAuroraPaymentsUnavailableError,
  isAuroraRateLimitError,
  listAuroraAssets,
  listAuroraGenerations,
  listAuroraSkills,
  listAuroraTools,
  listAuroraTopups,
  parseAuroraAssets,
  parseAuroraBalance,
  parseAuroraBillingPortal,
  parseAuroraCheckout,
  parseAuroraGeneration,
  parseAuroraGenerationDetail,
  parseAuroraGenerations,
  parseAuroraRuntime,
  parseAuroraSkills,
  parseAuroraSubscription,
  parseAuroraTopupCheckout,
  parseAuroraTopups,
  parseAuroraTools,
  parseAuroraTransactions,
} from "./api";

/** Records the request each endpoint builds, and answers with a fixed body. */
function installFakeClient(body: unknown) {
  const requests: Array<{ path: string; init?: RequestInit }> = [];
  setApiInstance({
    requestJson: async (path: string, init?: RequestInit) => {
      requests.push({ path, init });
      return body;
    },
  } as unknown as ApiClient);
  return requests;
}

describe("parseAuroraSkills", () => {
  it("returns the catalog when the body matches", () => {
    const skills = parseAuroraSkills({
      skills: [
        {
          id: "poster",
          name: "海报制作",
          name_en: "Poster",
          category: "image",
          credits: 76,
          input: ["text", "image"],
          output: ["image"],
          featured: true,
          available: true,
        },
      ],
    });

    expect(skills.degraded).toBe(false);
    expect(skills.value).toHaveLength(1);
    expect(skills.value[0]?.nameEn).toBe("Poster");
    expect(skills.value[0]?.credits).toBe(76);
  });

  it("degrades a non-array body to an empty directory, flagged as degraded", () => {
    expect(parseAuroraSkills({ skills: "not-an-array" })).toEqual({
      value: [],
      degraded: true,
    });
    expect(parseAuroraSkills(null)).toEqual({ value: [], degraded: true });
  });

  it("degrades an entry missing a required field to an empty directory", () => {
    // schema.test.ts owns why zod rejects the whole entry; this is the half the
    // directory screen depends on.
    expect(
      parseAuroraSkills({
        skills: [{ id: "poster", name: "海报制作", category: "image" }],
      }),
    ).toEqual({ value: [], degraded: true });
  });

  it("parses the policy's attachment rules and defaults them for an older server", () => {
    const withRules = parseAuroraSkills({
      skills: [
        {
          id: "poster",
          name: "海报制作",
          category: "image",
          credits: 76,
          attachment_rules: [
            { kinds: ["image"], min: 0, max: 4, max_bytes: 26_214_400 },
          ],
        },
      ],
    });

    expect(withRules.value[0]?.attachments).toEqual([
      { kinds: ["image"], min: 0, max: 4, maxBytes: 26_214_400 },
    ]);

    // A server that predates the field must still parse: the composer treats an
    // absent rule set as "this skill takes no attachments".
    const withoutRules = parseAuroraSkills({
      skills: [
        { id: "content-creation-lyrics-songwriting", name: "歌词创作专家", category: "content-creation", credits: 60 },
      ],
    });

    expect(withoutRules.value[0]?.attachments).toEqual([]);
  });

  it("carries the carrier agent's avatar and defaults it for an older server", () => {
    const withAvatar = parseAuroraSkills({
      skills: [
        {
          id: "media-short-video-copy",
          name: "短视频文案专家",
          category: "media",
          credits: 120,
          avatar_url: "/api/avatars/signature/workspaces/ws/lobster.png",
        },
      ],
    });

    expect(withAvatar.value[0]?.avatarUrl).toBe(
      "/api/avatars/signature/workspaces/ws/lobster.png",
    );

    // A server that predates the field must still parse: the directory keeps
    // drawing the skill's initials, which is the same rendering as "no avatar".
    const withoutAvatar = parseAuroraSkills({
      skills: [
        { id: "poster", name: "海报制作", category: "image", credits: 76 },
      ],
    });

    expect(withoutAvatar.value[0]?.avatarUrl).toBe("");
  });

  it("tells a degraded catalog apart from an empty one (GH #55)", () => {
    // The directory renders "no skills" for both without the flag, and only one
    // of them is a fact.
    const empty = parseAuroraSkills({ skills: [] });
    const degraded = parseAuroraSkills({ skills: "not-an-array" });

    expect(empty.value).toEqual(degraded.value);
    expect(isAuroraDegraded(empty)).toBe(false);
    expect(isAuroraDegraded(degraded)).toBe(true);
  });
});

describe("parseAuroraGenerations", () => {
  it("degrades a non-array body to an empty list, flagged as degraded", () => {
    expect(parseAuroraGenerations({ generations: 42 })).toEqual({
      value: [],
      degraded: true,
    });
  });

  it("passes the exposed task id through, defaulting an older server to empty", () => {
    const parsed = parseAuroraGenerations({
      generations: [
        { id: "gen-1", skillId: "poster", prompt: "a cat", status: "running", taskId: "task-1" },
        { id: "gen-2", skillId: "poster", prompt: "a dog", status: "queued" },
      ],
    });

    expect(parsed.value[0]?.taskId).toBe("task-1");
    expect(parsed.value[1]?.taskId).toBe("");
  });
});

describe("parseAuroraGeneration", () => {
  it("unwraps the create response", () => {
    const parsed = parseAuroraGeneration({
      generation: {
        id: "gen-1",
        skillId: "poster",
        prompt: "a cat",
        status: "queued",
        creditsReserved: 76,
      },
    });

    expect(parsed.degraded).toBe(false);
    expect(parsed.value?.id).toBe("gen-1");
    expect(parsed.value?.creditsReserved).toBe(76);
  });

  it("reads an unreadable body as null rather than an empty generation", () => {
    // A placeholder id here would send the composer to a detail screen for a
    // generation that does not exist. `degraded` is what the composer turns into
    // "check My works before starting another" instead of a retry.
    expect(parseAuroraGeneration({ generation: { id: "gen-1" } })).toEqual({
      value: null,
      degraded: true,
    });
    expect(parseAuroraGeneration("not-an-object")).toEqual({
      value: null,
      degraded: true,
    });
  });
});

describe("parseAuroraGenerationDetail", () => {
  it("returns the generation with its assets", () => {
    const detail = parseAuroraGenerationDetail({
      generation: {
        id: "gen-1",
        skillId: "poster",
        prompt: "a cat",
        status: "completed",
        creditsReserved: 76,
        taskId: "task-1",
        assets: [
          {
            id: "asset-1",
            generationId: "gen-1",
            kind: "image",
            mediaUrl: "https://cdn.example/a.png",
            format: "png",
            createdAt: "2026-09-22T00:00:00Z",
          },
        ],
      },
    });

    expect(detail.degraded).toBe(false);
    expect(detail.value?.status).toBe("completed");
    expect(detail.value?.taskId).toBe("task-1");
    expect(detail.value?.assets).toHaveLength(1);
    expect(detail.value?.assets[0]?.mediaUrl).toBe("https://cdn.example/a.png");
  });

  it("reads an unreadable body as a degraded null", () => {
    expect(
      parseAuroraGenerationDetail({ generation: "not-an-object" }),
    ).toEqual({ value: null, degraded: true });
  });
});

describe("parseAuroraAssets", () => {
  it("degrades a non-array body to an empty library, flagged as degraded", () => {
    expect(parseAuroraAssets({ assets: "not-an-array" })).toEqual({
      value: [],
      degraded: true,
    });
  });
});

describe("parseAuroraRuntime", () => {
  it("discards unknown and malformed runtime reason codes without losing the projection", () => {
    for (const errorCode of ["private credential", null, 42]) {
      const parsed = parseAuroraRuntime({
        state: "offline",
        node: { ready: false, errorCode },
      });
      expect(parsed.degraded).toBe(false);
      expect(parsed.value.state).toBe("offline");
      expect(parsed.value.node?.errorCode).toBeUndefined();
    }
  });

  it("does not report online when the node explicitly is not ready", () => {
    const parsed = parseAuroraRuntime({
      state: "online",
      node: { status: "future", ready: false },
    });
    expect(parsed.degraded).toBe(false);
    expect(parsed.value.state).toBe("offline");
  });

  it("reads the workspace's execution target", () => {
    const parsed = parseAuroraRuntime({
      workspaceId: "ws-1",
      node: {
        id: "node-1",
        status: "online",
        ready: true,
        provider: "docker",
        createdAt: "2026-10-06T00:00:00Z",
      },
      runtimeId: "rt-1",
      state: "online",
    });

    expect(parsed.degraded).toBe(false);
    expect(parsed.value.state).toBe("online");
    expect(parsed.value.node?.id).toBe("node-1");
    expect(parsed.value.node?.ready).toBe(true);
    expect(parsed.value.runtimeId).toBe("rt-1");
  });

  it("degrades a malformed body to the unconfigured projection", () => {
    // The endpoint is a console view; a drifted body must render "not set up"
    // rather than crash the screen. schema.test.ts owns the defaults.
    expect(parseAuroraRuntime("not-an-object")).toEqual({
      value: {
        workspaceId: "",
        node: null,
        runtimeId: null,
        state: "unconfigured",
      },
      degraded: true,
    });
    expect(parseAuroraRuntime({ node: "not-an-object" }).degraded).toBe(true);
  });

  it("maps an unknown state to unconfigured without discarding the node", () => {
    // The server's state is a plain string, so a value from a newer server must
    // still parse; the view's vocabulary is the four documented states.
    const parsed = parseAuroraRuntime({
      workspaceId: "ws-1",
      node: {
        id: "node-1",
        status: "future",
        ready: false,
        provider: "docker",
        createdAt: "2026-10-06T00:00:00Z",
      },
      runtimeId: "rt-1",
      state: "paused",
    });

    expect(parsed.value.state).toBe("unconfigured");
    expect(parsed.value.node?.status).toBe("future");
    expect(parsed.value.runtimeId).toBe("rt-1");
  });

  it("keeps a node whose provider field is missing", () => {
    const parsed = parseAuroraRuntime({
      workspaceId: "ws-1",
      node: {
        id: "node-1",
        status: "starting",
        ready: false,
        createdAt: "2026-10-06T00:00:00Z",
      },
      state: "provisioning",
    });

    expect(parsed.value.state).toBe("provisioning");
    expect(parsed.value.node?.provider).toBe("");
  });
});

describe("parseAuroraBalance and parseAuroraTransactions", () => {
  it("reads the wallet and the ledger", () => {
    expect(parseAuroraBalance({ availableMicro: 12_000_000 })).toEqual({
      value: { availableMicro: 12_000_000 },
      degraded: false,
    });
    expect(
      parseAuroraTransactions({
        transactions: [
          {
            id: "tx-1",
            kind: "deduction",
            amountMicro: -76_000_000,
            balanceAfterMicro: 4_000_000,
            reference: "gen-1",
            createdAt: "2026-09-22T00:00:00Z",
          },
        ],
      }).value,
    ).toHaveLength(1);
  });

  it("degrades unreadable bodies to zero and to an empty ledger", () => {
    expect(parseAuroraBalance("not-an-object").value.availableMicro).toBe(0);
    expect(parseAuroraTransactions({ transactions: 7 }).value).toEqual([]);
  });

  it("flags a degraded wallet so its 0 cannot be read as a real balance", () => {
    // The sharpest case in GH #55: a corrupted body parses to a *number*, and
    // the billing screen printed it as "0 credits" — a claim that sends the user
    // to top up against a figure the client never read.
    const broke = parseAuroraBalance({ availableMicro: 0 });
    const degraded = parseAuroraBalance("not-an-object");

    expect(degraded.value).toEqual(broke.value);
    expect(isAuroraDegraded(broke)).toBe(false);
    expect(isAuroraDegraded(degraded)).toBe(true);
  });

  it("treats a missing payload as not degraded", () => {
    // A query that has not resolved has no payload to judge; the caller reads
    // that as pending, not as a corrupted body.
    expect(isAuroraDegraded(undefined)).toBe(false);
  });
});

describe("parseAuroraSubscription", () => {
  it("reads the plan the server reports", () => {
    const res = parseAuroraSubscription({
      subscription: { tier: "pro", status: "active" },
    });

    expect(res.tier).toBe("pro");
    expect(res.status).toBe("active");
  });

  it("rejects an unreadable body instead of inventing a free plan", () => {
    // Billing state controls a real purchase. A paid user must not see a
    // purchase-capable Free card because the response contract degraded.
    expect(() =>
      parseAuroraSubscription({ subscription: "nope" }),
    ).toThrow("invalid subscription response");
  });
});

describe("parseAuroraTopups", () => {
  it("rejects an unreadable body instead of claiming no packs are sold", () => {
    expect(() => parseAuroraTopups({ topups: 7 })).toThrow(
      "invalid topup response",
    );
  });

  it("reads the packs", () => {
    expect(
      parseAuroraTopups({ topups: [{ id: "t20", credits: 20000 }] }),
    ).toEqual([{ id: "t20", credits: 20000 }]);
  });
});

describe("parseAuroraCheckout", () => {
  it("reads the checkout URL", () => {
    expect(
      parseAuroraCheckout({ checkoutUrl: "https://checkout.stripe.com/c/1" }),
    ).toBe("https://checkout.stripe.com/c/1");
  });

  it("rejects an unreadable body instead of reporting a successful checkout", () => {
    expect(() => parseAuroraCheckout({})).toThrow("invalid checkout response");
    expect(() => parseAuroraTopupCheckout({ checkoutUrl: 7 })).toThrow(
      "invalid checkout response",
    );
  });

  it("rejects checkout destinations that are not absolute HTTPS URLs", () => {
    expect(() =>
      parseAuroraCheckout({ checkoutUrl: "http://checkout.stripe.test/c/1" }),
    ).toThrow("invalid checkout response");
    expect(() =>
      parseAuroraCheckout({ checkoutUrl: "javascript:alert(1)" }),
    ).toThrow("invalid checkout response");
    expect(() => parseAuroraCheckout({ checkoutUrl: "/checkout/1" })).toThrow(
      "invalid checkout response",
    );
  });
});

describe("parseAuroraBillingPortal", () => {
  it("reads the portal URL", () => {
    expect(
      parseAuroraBillingPortal({
        portalUrl: "https://billing.stripe.com/p/session/1",
      }),
    ).toBe("https://billing.stripe.com/p/session/1");
  });

  it("rejects an unreadable body instead of reporting a successful portal session", () => {
    expect(() => parseAuroraBillingPortal({})).toThrow(
      "invalid portal response",
    );
    expect(() => parseAuroraBillingPortal({ portalUrl: 7 })).toThrow(
      "invalid portal response",
    );
  });

  it("rejects portal destinations that are not absolute HTTPS URLs", () => {
    for (const portalUrl of [
      "http://billing.stripe.test/p/session/1",
      "javascript:alert(1)",
      "/billing/portal/1",
    ]) {
      expect(() => parseAuroraBillingPortal({ portalUrl })).toThrow(
        "invalid portal response",
      );
    }
  });
});

describe("request paths", () => {
  beforeEach(() => {
    setApiInstance(null as unknown as ApiClient);
  });

  it("reads the catalog from the skills endpoint", async () => {
    const requests = installFakeClient({ skills: [] });

    await listAuroraSkills();

    expect(requests).toHaveLength(1);
    expect(requests[0]?.path).toBe("/api/aurora/skills");
    expect(requests[0]?.init).toBeUndefined();
  });

  it("reads the execution target from the runtime endpoint", async () => {
    const requests = installFakeClient({
      workspaceId: "ws-1",
      node: null,
      runtimeId: null,
      state: "unconfigured",
    });

    const parsed = await getAuroraRuntime();

    expect(requests).toHaveLength(1);
    expect(requests[0]?.path).toBe("/api/aurora/runtime");
    expect(requests[0]?.init).toBeUndefined();
    expect(parsed.value.state).toBe("unconfigured");
  });

  it("passes the generation list's paging through as query params", async () => {
    const requests = installFakeClient({ generations: [] });

    await listAuroraGenerations({ limit: 5, offset: 10 });

    expect(requests[0]?.path).toBe("/api/aurora/generations?limit=5&offset=10");
  });

  it("omits absent query params instead of sending empty ones", async () => {
    const requests = installFakeClient({ assets: [] });

    await listAuroraAssets({ generationId: "gen-1" });

    expect(requests[0]?.path).toBe("/api/aurora/assets?generationId=gen-1");
  });

  it("posts the composer's request body, including attachment ids, as JSON", async () => {
    const requests = installFakeClient({ generation: null });

    await createAuroraGeneration({
      skillId: "poster",
      prompt: "a cat",
      attachmentIds: ["att-1", "att-2"],
    });

    expect(requests[0]?.path).toBe("/api/aurora/generations");
    expect(requests[0]?.init?.method).toBe("POST");
    expect(requests[0]?.init?.body).toBe(
      JSON.stringify({
        skillId: "poster",
        prompt: "a cat",
        attachmentIds: ["att-1", "att-2"],
      }),
    );
  });

  it("reads the plan and the topup list from the billing endpoints", async () => {
    let requests = installFakeClient({ subscription: { tier: "free" } });

    await getAuroraSubscription();

    expect(requests[0]?.path).toBe("/api/aurora/billing/subscription");

    requests = installFakeClient({ topups: [] });
    await listAuroraTopups();
    expect(requests[0]?.path).toBe("/api/aurora/billing/topups");
  });

  it("posts the checkout bodies as JSON", async () => {
    const returnURLs = {
      successUrl: "https://app.example.com/acme/billing?checkout=success",
      cancelUrl: "https://app.example.com/acme/billing?checkout=cancel",
    };
    let requests = installFakeClient({ checkoutUrl: "https://stripe.test/1" });

    await createAuroraCheckout({
      tier: "creator",
      billingCycle: "monthly",
      ...returnURLs,
    });

    expect(requests[0]?.path).toBe("/api/aurora/billing/checkout");
    expect(requests[0]?.init?.method).toBe("POST");
    expect(requests[0]?.init?.body).toBe(
      JSON.stringify({
        tier: "creator",
        billingCycle: "monthly",
        ...returnURLs,
      }),
    );

    requests = installFakeClient({ checkoutUrl: "https://stripe.test/2" });
    await createAuroraTopupCheckout({ topupId: "t5", ...returnURLs });
    expect(requests[0]?.path).toBe("/api/aurora/billing/topup/checkout");
  });

  it("posts the portal return URL and reads the portal URL", async () => {
    const requests = installFakeClient({
      portalUrl: "https://billing.stripe.com/p/session/1",
    });

    const portalUrl = await createAuroraBillingPortal({
      returnUrl: "https://app.example.com/acme/billing",
    });

    expect(portalUrl).toBe("https://billing.stripe.com/p/session/1");
    expect(requests[0]?.path).toBe("/api/aurora/billing/portal");
    expect(requests[0]?.init?.method).toBe("POST");
    expect(requests[0]?.init?.body).toBe(
      JSON.stringify({ returnUrl: "https://app.example.com/acme/billing" }),
    );
  });

  it("deletes an asset with no body to read", async () => {
    const requests = installFakeClient(undefined);

    await expect(deleteAuroraAsset("asset-1")).resolves.toBeUndefined();

    expect(requests[0]?.path).toBe("/api/aurora/assets/asset-1");
    expect(requests[0]?.init?.method).toBe("DELETE");
  });
});

describe("auroraAssetDownloadPath", () => {
  it("builds the download path the browser follows", () => {
    expect(auroraAssetDownloadPath("asset-1")).toBe(
      "/api/aurora/assets/asset-1/download",
    );
  });
});

describe("error classification", () => {
  it("reads 402 as an insufficient wallet", () => {
    expect(
      isAuroraInsufficientCreditsError(
        new ApiError("insufficient credits", 402, "Payment Required"),
      ),
    ).toBe(true);
  });

  it("reads 429 as one of the server's limits", () => {
    expect(
      isAuroraRateLimitError(
        new ApiError("too many requests", 429, "Too Many Requests"),
      ),
    ).toBe(true);
  });

  it("reads 409 as an existing subscription", () => {
    expect(
      isAuroraCheckoutConflictError(
        new ApiError("subscription already exists", 409, "Conflict"),
      ),
    ).toBe(true);
  });

  it("reads 503 as a deployment without payments", () => {
    expect(
      isAuroraPaymentsUnavailableError(
        new ApiError("payments not configured", 503, "Unavailable"),
      ),
    ).toBe(true);
  });

  it("reads 404 as a generation that is gone", () => {
    expect(
      isAuroraGenerationNotFoundError(
        new ApiError("generation not found", 404, "Not Found"),
      ),
    ).toBe(true);
    expect(isAuroraGenerationNotFoundError(new ApiError("boom", 500, "Server"))).toBe(
      false,
    );
    expect(isAuroraGenerationNotFoundError(new Error("network"))).toBe(false);
  });

  it("does not claim unrelated failures", () => {
    expect(isAuroraInsufficientCreditsError(new Error("network"))).toBe(false);
    expect(isAuroraRateLimitError(new ApiError("boom", 500, "Server Error"))).toBe(
      false,
    );
    expect(isAuroraInsufficientCreditsError(new ApiError("boom", 429, "Rate"))).toBe(
      false,
    );
    expect(isAuroraCheckoutConflictError(new Error("network"))).toBe(false);
    expect(
      isAuroraCheckoutConflictError(new ApiError("boom", 503, "Unavailable")),
    ).toBe(false);
    expect(
      isAuroraPaymentsUnavailableError(new ApiError("boom", 409, "Conflict")),
    ).toBe(false);
  });
});

describe("parseAuroraTools", () => {
  it("maps the tools response onto camelCase executors", () => {
    expect(
      parseAuroraTools({
        tools: [
          {
            id: "poster",
            expert_slug: "marketing-ad-copywriting",
            expert_name: "广告文案专家",
          },
        ],
      }),
    ).toEqual({
      value: [
        {
          id: "poster",
          expertSlug: "marketing-ad-copywriting",
          expertName: "广告文案专家",
        },
      ],
      degraded: false,
    });
  });

  it("degrades an unreadable directory to an empty list", () => {
    // The tool page renders "no executor" from the absence of a mapping, so an
    // empty directory is the safe fallback — it never guesses an agent.
    expect(parseAuroraTools({ tools: "not-an-array" })).toEqual({
      value: [],
      degraded: true,
    });
  });
});

describe("createAuroraToolRun", () => {
  it("posts the run body and returns the created issue", async () => {
    const requests = installFakeClient({
      identifier: "MUL-42",
      issue_id: "i-1",
      expert: { slug: "s", name: "n" },
    });

    await expect(
      createAuroraToolRun("poster", { source: "x", attachmentIds: ["a-1"] }),
    ).resolves.toEqual({
      identifier: "MUL-42",
      issueId: "i-1",
      expert: { slug: "s", name: "n" },
    });

    expect(requests[0]?.path).toBe("/api/aurora/tools/poster/runs");
    expect(requests[0]?.init?.method).toBe("POST");
    // The server's wire shape is snake_case (spec §8.1); the client's request
    // object is camelCase and maps here.
    expect(JSON.parse(String(requests[0]?.init?.body))).toEqual({
      source: "x",
      attachment_ids: ["a-1"],
    });
  });

  it("throws on a malformed run response instead of degrading", async () => {
    // The issue may already exist, so a malformed body must surface as an
    // error — silently returning a placeholder would hide a created run.
    installFakeClient({ identifier: 42 });

    await expect(
      createAuroraToolRun("poster", { source: "x" }),
    ).rejects.toThrow();
  });
});

describe("listAuroraTools", () => {
  it("reads the directory from the tools endpoint", async () => {
    const requests = installFakeClient({ tools: [] });

    await expect(listAuroraTools()).resolves.toEqual({
      value: [],
      degraded: false,
    });
    expect(requests[0]?.path).toBe("/api/aurora/tools");
  });
});
