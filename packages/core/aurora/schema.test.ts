// @vitest-environment node
import { describe, expect, it } from "vitest";
import {
  auroraAssetSchema,
  auroraBalanceSchema,
  auroraCheckoutResponseSchema,
  auroraExecutionTargetSchema,
  auroraGenerationDetailSchema,
  auroraGenerationSchema,
  auroraRuntimeNodeSchema,
  auroraSkillsSchema,
  auroraSubscriptionSchema,
  auroraToolRunSchema,
  auroraToolsSchema,
  auroraTopupsSchema,
  auroraTransactionsSchema,
} from "./schema";

describe("auroraSkillsSchema", () => {
  it("fills in the optional fields a minimal entry omits", () => {
    const res = auroraSkillsSchema.parse({
      skills: [
        { id: "poster", name: "海报制作", credits: 76, category: "image" },
      ],
    });

    expect(res.skills[0]?.nameEn).toBe("");
    expect(res.skills[0]?.input).toEqual([]);
    expect(res.skills[0]?.output).toEqual([]);
    expect(res.skills[0]?.featured).toBe(false);
    expect(res.skills[0]?.available).toBe(true);
  });

  it("maps the wire's name_en onto nameEn", () => {
    const res = auroraSkillsSchema.parse({
      skills: [
        {
          id: "poster",
          name: "海报制作",
          name_en: "Poster",
          credits: 76,
          category: "image",
        },
      ],
    });

    expect(res.skills[0]?.nameEn).toBe("Poster");
  });

  it("rejects an entry that is missing a required field", () => {
    // `credits` is required — the composer prices a run from it, so an entry
    // without one is not usable. zod rejects the whole array rather than the
    // single entry, which is the documented trade-off: one drifted row degrades
    // the directory to empty rather than rendering a skill that cannot be
    // priced. api.test.ts owns the other half — that the caller sees [].
    const res = auroraSkillsSchema.safeParse({
      skills: [{ id: "poster", name: "海报制作", category: "image" }],
    });

    expect(res.success).toBe(false);
  });
});

describe("auroraGenerationSchema", () => {
  it("defaults creditsReserved and keeps an unknown status parseable", () => {
    const res = auroraGenerationSchema.parse({
      id: "gen-1",
      skillId: "poster",
      prompt: "a cat",
      status: "some-future-status",
    });

    expect(res.creditsReserved).toBe(0);
    expect(res.status).toBe("some-future-status");
    // An older server that predates taskId still parses; "" is "no task link".
    expect(res.taskId).toBe("");
  });

  it("keeps the task id the list and detail endpoints expose", () => {
    const res = auroraGenerationSchema.parse({
      id: "gen-1",
      skillId: "poster",
      prompt: "a cat",
      status: "running",
      taskId: "task-1",
    });

    expect(res.taskId).toBe("task-1");
  });

  it("rejects an entry missing its id", () => {
    const res = auroraGenerationSchema.safeParse({
      skillId: "poster",
      prompt: "a cat",
      status: "queued",
    });

    expect(res.success).toBe(false);
  });
});

describe("auroraGenerationDetailSchema", () => {
  it("defaults a missing assets list to empty", () => {
    const res = auroraGenerationDetailSchema.parse({
      generation: {
        id: "gen-1",
        skillId: "poster",
        prompt: "a cat",
        status: "running",
      },
    });

    expect(res.generation.assets).toEqual([]);
  });
});

describe("auroraAssetSchema", () => {
  it("defaults the nullable and timestamp columns", () => {
    const res = auroraAssetSchema.parse({
      id: "asset-1",
      generationId: "gen-1",
      kind: "image",
    });

    expect(res.mediaUrl).toBeNull();
    expect(res.format).toBeNull();
    expect(res.createdAt).toBe("");
  });

  it("keeps an explicitly null mediaUrl usable", () => {
    const res = auroraAssetSchema.parse({
      id: "asset-1",
      generationId: "gen-1",
      kind: "image",
      mediaUrl: null,
      format: "png",
      createdAt: "2026-09-22T00:00:00Z",
    });

    expect(res.mediaUrl).toBeNull();
    expect(res.format).toBe("png");
  });
});

describe("auroraBalanceSchema", () => {
  it("defaults a missing balance to zero", () => {
    expect(auroraBalanceSchema.parse({}).availableMicro).toBe(0);
  });
});

describe("auroraTransactionsSchema", () => {
  it("defaults the optional ledger columns", () => {
    const res = auroraTransactionsSchema.parse({
      transactions: [{ id: "tx-1", kind: "deduction", amountMicro: -76 }],
    });

    expect(res.transactions[0]?.balanceAfterMicro).toBe(0);
    expect(res.transactions[0]?.reference).toBe("");
    expect(res.transactions[0]?.createdAt).toBe("");
  });
});

describe("auroraSubscriptionSchema", () => {
  it("defaults every field so a degraded read still renders a plan", () => {
    // The plan card is the first thing the billing screen draws; a body that
    // predates the endpoint must produce a free plan, not a blank card.
    const res = auroraSubscriptionSchema.parse({});

    expect(res.tier).toBe("free");
    expect(res.status).toBe("");
    expect(res.currentPeriodEnd).toBeNull();
    expect(res.cancelAtPeriodEnd).toBe(false);
    expect(res.limits).toEqual({ generationsPerMonth: 10, concurrency: 1 });
    expect(res.usage).toEqual({
      generationsUsedThisMonth: 0,
      activeGenerations: 0,
    });
  });

  it("keeps a plan and its usage as they arrive", () => {
    const res = auroraSubscriptionSchema.parse({
      tier: "creator",
      status: "active",
      currentPeriodEnd: "2026-10-24T00:00:00Z",
      cancelAtPeriodEnd: true,
      limits: { generationsPerMonth: 30, concurrency: 2 },
      usage: { generationsUsedThisMonth: 12, activeGenerations: 1 },
    });

    expect(res.tier).toBe("creator");
    expect(res.currentPeriodEnd).toBe("2026-10-24T00:00:00Z");
    expect(res.limits.generationsPerMonth).toBe(30);
    expect(res.usage.generationsUsedThisMonth).toBe(12);
  });

  it("keeps a status this build has never seen", () => {
    // The server sends the status as a plain string; a new one must reach the
    // screen so its label mapping can fall back, not fail the parse.
    const res = auroraSubscriptionSchema.parse({ status: "paused" });

    expect(res.status).toBe("paused");
  });

  it("defaults the payment provider to Stripe", () => {
    // The channel arrived with the Antom launch. A server that predates it (or
    // a body that omits the field) could only have been selling through
    // Stripe, so that is the reading that keeps its plan card truthful.
    expect(auroraSubscriptionSchema.parse({}).provider).toBe("stripe");
    expect(auroraSubscriptionSchema.parse({ provider: "" }).provider).toBe("");
    expect(auroraSubscriptionSchema.parse({ provider: "antom" }).provider).toBe(
      "antom",
    );
  });

  it("drops a channel this build cannot sell from availableProviders", () => {
    // The list decides which buttons the plan card offers. A channel from a
    // newer server must not remove the ones this build can sell, and must not
    // become a button that would submit an unknown provider.
    const res = auroraSubscriptionSchema.parse({
      availableProviders: ["stripe", "venmo"],
    });

    expect(res.availableProviders).toEqual(["stripe"]);
  });

  it("collapses a repeated channel", () => {
    // The list is rendered as buttons keyed by channel: a duplicate would be a
    // duplicate React key and a second button that does the same thing.
    const res = auroraSubscriptionSchema.parse({
      availableProviders: ["stripe", "stripe", "antom"],
    });

    expect(res.availableProviders).toEqual(["stripe", "antom"]);
  });

  it("defaults a missing availableProviders to no channels", () => {
    expect(auroraSubscriptionSchema.parse({}).availableProviders).toEqual([]);
  });

  it("collapses a malformed availableProviders instead of failing the plan read", () => {
    // The plan card is the purchase boundary: a bad channel list must cost the
    // buttons, not the whole subscription read.
    expect(
      auroraSubscriptionSchema.parse({ availableProviders: null })
        .availableProviders,
    ).toEqual([]);
    expect(
      auroraSubscriptionSchema.parse({ availableProviders: "stripe" })
        .availableProviders,
    ).toEqual([]);
  });
});

describe("auroraTopupsSchema", () => {
  it("defaults a missing list to empty", () => {
    expect(auroraTopupsSchema.parse({}).topups).toEqual([]);
  });

  it("reads the packs", () => {
    const res = auroraTopupsSchema.parse({
      topups: [{ id: "t5", credits: 5000 }],
    });

    expect(res.topups[0]?.id).toBe("t5");
    expect(res.topups[0]?.credits).toBe(5000);
  });
});

describe("auroraExecutionTargetSchema", () => {
  it("defaults a missing node, runtime and state to the unconfigured projection", () => {
    // The runtime screen is reachable before any node exists, so an empty body
    // must still describe a target rather than fail the parse.
    const res = auroraExecutionTargetSchema.parse({});

    expect(res.workspaceId).toBe("");
    expect(res.node).toBeNull();
    expect(res.runtimeId).toBeNull();
    expect(res.state).toBe("unconfigured");
  });

  it("keeps an unknown state parseable for the parser to normalize", () => {
    // The schema stays lenient (z.string), like every other server enum here, so
    // a state from a newer server still parses; parseAuroraRuntime in ./api maps
    // it to unconfigured. Rejecting it here would degrade the whole target.
    expect(
      auroraExecutionTargetSchema.parse({ state: "future-state" }).state,
    ).toBe("future-state");
  });

  it("reads a node that omits its optional provider fields", () => {
    const res = auroraRuntimeNodeSchema.parse({
      id: "node-1",
      status: "online",
      ready: true,
      createdAt: "2026-10-06T00:00:00Z",
    });

    expect(res.provider).toBe("");
    expect(res.errorCode).toBeUndefined();
    expect(res.operationId).toBeUndefined();
    expect(res.createdAt).toBe("2026-10-06T00:00:00Z");
  });
});

describe("auroraCheckoutResponseSchema", () => {
  it("rejects a body with no checkout URL", () => {
    // No default here on purpose: an empty URL would navigate the user back to
    // the page they are already on, as if the purchase had completed.
    expect(auroraCheckoutResponseSchema.safeParse({}).success).toBe(false);
  });

  it("reads the URL the browser is sent to", () => {
    expect(
      auroraCheckoutResponseSchema.parse({
        checkoutUrl: "https://checkout.stripe.com/c/test",
      }).checkoutUrl,
    ).toBe("https://checkout.stripe.com/c/test");
  });
});

describe("auroraToolsSchema", () => {
  it("keeps the expert's display name alongside its slug", () => {
    const res = auroraToolsSchema.parse({
      tools: [
        {
          id: "poster",
          expert_slug: "marketing-ad-copywriting",
          expert_name: "广告文案专家",
        },
      ],
    });

    expect(res.tools[0]).toEqual({
      id: "poster",
      expertSlug: "marketing-ad-copywriting",
      expertName: "广告文案专家",
    });
  });

  it("rejects an entry with no executor rather than inventing one", () => {
    expect(
      auroraToolsSchema.safeParse({ tools: [{ id: "poster" }] }).success,
    ).toBe(false);
  });

  it("rejects a body with no tool list, so the caller sees a load failure", () => {
    // A default of [] here would parse as "this workspace has no mapped tools"
    // and surface as no-expert: a false claim about configuration.
    expect(auroraToolsSchema.safeParse({}).success).toBe(false);
  });
});

describe("auroraToolRunSchema", () => {
  it("maps the created run onto camelCase", () => {
    expect(
      auroraToolRunSchema.parse({
        identifier: "MUL-42",
        issue_id: "i-1",
        expert: { slug: "marketing-ad-copywriting", name: "广告文案专家" },
      }),
    ).toEqual({
      identifier: "MUL-42",
      issueId: "i-1",
      expert: { slug: "marketing-ad-copywriting", name: "广告文案专家" },
    });
  });

  it("rejects a run with no identifier, which the client cannot link to", () => {
    expect(
      auroraToolRunSchema.safeParse({ issue_id: "i-1", expert: {} }).success,
    ).toBe(false);
  });

  it("accepts a run whose optional fields the server omits", () => {
    // The client navigates by identifier only; requiring issue_id/expert would
    // turn a created issue into a thrown error after the fact.
    expect(auroraToolRunSchema.parse({ identifier: "MUL-42" })).toEqual({
      identifier: "MUL-42",
      issueId: null,
    });
  });
});
