import { z } from "zod";
import type {
  AuroraRuntimeState,
  AuroraSkillAttachmentRule,
} from "./types";

/**
 * Wire schemas for the Aurora API (`server/internal/handler/aurora.go`,
 * `aurora_artifact.go`).
 *
 * They are deliberately lenient, in the shape CLAUDE.md's API-compatibility
 * rules ask for: fields an older server may omit carry defaults, nullable
 * columns are nullable rather than optional, and enum-ish columns stay
 * `z.string()` so a value from a newer server still parses. A body that is not
 * a shape at all (a list where an object belongs, a string where a list
 * belongs) fails the parse and is handled by the `parseAurora*` functions in
 * ./api, which route every response through `parseWithFallback`.
 */

/**
 * One catalog entry. The catalog is the single source of truth for the
 * directory, the composer's credit price, and whether a skill can run.
 *
 * The wire names the English label `name_en` (`aurora/catalog.go`) while the
 * rest of the API is camelCase; the transform maps it to `nameEn` so consumers
 * see one naming convention.
 */
const auroraSkillAttachmentRuleSchema = z.object({
  kinds: z.array(z.string()).default([]),
  min: z.number().default(0),
  max: z.number().default(0),
  max_bytes: z.number().default(0),
});

export const auroraSkillSchema = z
  .object({
    id: z.string(),
    name: z.string(),
    name_en: z.string().default(""),
    category: z.string(),
    credits: z.number(),
    input: z.array(z.string()).default([]),
    output: z.array(z.string()).default([]),
    featured: z.boolean().default(false),
    /** false = a phase-2 skill: listed, but not yet runnable. */
    available: z.boolean().default(true),
    /**
     * The avatar the workspace set on this skill's carrier agent, already
     * resolved by the server for a browser to load. Empty when the workspace
     * never seeded that carrier, left it without a portrait, or runs a server
     * that predates the field — the tile then falls back to the skill's
     * initials, so an older server changes nothing.
     */
    avatar_url: z.string().default(""),
    /**
     * The skill's parsed input rules. Defaulted to an empty array so a server
     * that predates the field still parses: the composer then renders no file
     * input rather than guessing a rule set.
     */
    attachment_rules: z.array(auroraSkillAttachmentRuleSchema).default([]),
  })
  .transform(({ name_en, avatar_url, attachment_rules, ...skill }) => ({
    ...skill,
    nameEn: name_en,
    avatarUrl: avatar_url,
    attachments: attachment_rules.map(
      (rule): AuroraSkillAttachmentRule => ({
        kinds: rule.kinds,
        min: rule.min,
        max: rule.max,
        maxBytes: rule.max_bytes,
      }),
    ),
  }));
export type AuroraSkill = z.infer<typeof auroraSkillSchema>;

export const auroraSkillsSchema = z.object({
  skills: z.array(auroraSkillSchema),
});

export const auroraGenerationSchema = z.object({
  id: z.string(),
  skillId: z.string(),
  prompt: z.string(),
  /**
   * "queued" | "running" | "completed" | "failed", derived server-side from
   * the enqueued task. Kept as a plain string so an unrecognised value from a
   * newer server still parses — consumers switch on it with a `default` branch
   * and decide terminality with `isAuroraGenerationTerminal` (./types).
   */
  status: z.string(),
  creditsReserved: z.number().default(0),
  /**
   * The amount settlement actually charged. Zero until the generation settles,
   * and zero again once a failed generation is refunded — which is why the
   * history view falls back to `creditsReserved` only while one is still in
   * flight, and shows nothing at all once it has failed.
   */
  creditsCharged: z.number().default(0),
  /**
   * The failure reason the execution layer wrote back, or null. A plain
   * nullable string on purpose: the server owns its wording, and a newer server
   * may send a reason this build has never seen.
   */
  error: z.string().nullable().default(null),
  /** RFC 3339 creation timestamp; empty when an older server omits it. */
  createdAt: z.string().default(""),
  /**
   * The enqueued agent task this generation runs as, or "" when the server does
   * not expose one — an older server that predates the field, or a row that
   * never enqueued. The empty-string default is deliberate: the detail view
   * treats "" as "no task link to offer", which is the same state as absent.
   */
  taskId: z.string().default(""),
  /**
   * The "PREFIX-N" key of the issue this generation runs on, or "" when the
   * server could not resolve a workspace prefix (the degraded "#N" form, which
   * the client reads as "no issue link"). Defaulted so an older server's
   * response still parses.
   */
  issueIdentifier: z.string().default(""),
});
export type AuroraGeneration = z.infer<typeof auroraGenerationSchema>;

/** POST /api/aurora/generations answers with the generation under a wrapper. */
export const auroraGenerationResponseSchema = z.object({
  generation: auroraGenerationSchema,
});

export const auroraGenerationsSchema = z.object({
  generations: z.array(auroraGenerationSchema),
});

export const auroraAssetSchema = z.object({
  id: z.string(),
  generationId: z.string(),
  kind: z.string(),
  /** A row can exist before its object does; both are usable as "no file". */
  mediaUrl: z.string().nullable().default(null),
  format: z.string().nullable().default(null),
  createdAt: z.string().default(""),
});
export type AuroraAsset = z.infer<typeof auroraAssetSchema>;

/**
 * GET /api/aurora/generations/{id}. The detail response is the summary plus
 * the generation's own assets, which is why the library list has no separate
 * per-generation query.
 */
export const auroraGenerationDetailSchema = z.object({
  generation: auroraGenerationSchema.extend({
    assets: z.array(auroraAssetSchema).default([]),
  }),
});
export type AuroraGenerationDetail = z.infer<
  typeof auroraGenerationDetailSchema
>["generation"];

export const auroraAssetsSchema = z.object({
  assets: z.array(auroraAssetSchema),
});

/**
 * The workspace's managed execution node as `GET /api/aurora/runtime` reports
 * it. `id` is the fleet node identity, `status` the node lifecycle state, and
 * `ready` its readiness. `provider` and the optional `errorCode` /
 * `operationId` are deliberately lenient: an older server may omit them, and a
 * public error code is the only failure detail the endpoint ever returns — the
 * raw failure reason stays server-side so no internal path or credential reaches
 * the client.
 */
export const auroraRuntimeNodeSchema = z.object({
  id: z.string().default(""),
  status: z.string().default(""),
  ready: z.boolean().default(false),
  provider: z.string().default(""),
  errorCode: z
    .enum(["runtime_unconfigured", "runtime_offline", "runtime_policy_unavailable"])
    .optional()
    .catch(undefined),
  operationId: z.string().optional(),
  createdAt: z.string().default(""),
});
export type AuroraRuntimeNode = z.infer<typeof auroraRuntimeNodeSchema>;

/**
 * The workspace's execution target.
 *
 * `state` stays a plain string so a newer server's state still parses;
 * `parseAuroraRuntime` (./api) normalizes it into `AuroraRuntimeState`, which
 * is why the exported `AuroraExecutionTarget` narrows the field after the fact.
 */
export const auroraExecutionTargetSchema = z.object({
  workspaceId: z.string().default(""),
  node: auroraRuntimeNodeSchema.nullable().default(null),
  runtimeId: z.string().nullable().default(null),
  state: z.string().default("unconfigured"),
});

type AuroraExecutionTargetWire = z.infer<typeof auroraExecutionTargetSchema>;

/** The parsed target the runtime view renders. */
export interface AuroraExecutionTarget
  extends Omit<AuroraExecutionTargetWire, "state"> {
  state: AuroraRuntimeState;
}

/** GET /api/aurora/billing/balance — the caller's wallet, in micro-credits. */
export const auroraBalanceSchema = z.object({
  availableMicro: z.number().default(0),
});
export type AuroraBalance = z.infer<typeof auroraBalanceSchema>;

export const auroraTransactionSchema = z.object({
  id: z.string(),
  kind: z.string(),
  /** Signed: a reservation is negative, a refund or grant positive. */
  amountMicro: z.number(),
  balanceAfterMicro: z.number().default(0),
  /** The row's subject — a generation id, or a grant/plan key. */
  reference: z.string().default(""),
  createdAt: z.string().default(""),
});
export type AuroraTransaction = z.infer<typeof auroraTransactionSchema>;

export const auroraTransactionsSchema = z.object({
  transactions: z.array(auroraTransactionSchema).default([]),
});

/**
 * GET /api/aurora/billing/subscription.
 *
 * `tier` is the tier the server's gates actually apply — a canceled plan
 * reports the free tier here — while `status` is the stored subscription
 * status and is empty for a user with no plan row at all. The two together are
 * what let the screen say "Free" without claiming a canceled plan is still
 * running.
 *
 * Every field carries a default because the endpoint is the one the plan screen
 * renders first: a server that predates it (or a degraded body) must still
 * produce a free-plan card rather than a blank screen.
 */
export const auroraSubscriptionSchema = z.object({
  tier: z.string().default("free"),
  status: z.string().default(""),
  currentPeriodEnd: z.string().nullable().default(null),
  cancelAtPeriodEnd: z.boolean().default(false),
  limits: z
    .object({
      generationsPerMonth: z.number().default(10),
      concurrency: z.number().default(1),
    })
    .default({ generationsPerMonth: 10, concurrency: 1 }),
  usage: z
    .object({
      generationsUsedThisMonth: z.number().default(0),
      activeGenerations: z.number().default(0),
    })
    .default({ generationsUsedThisMonth: 0, activeGenerations: 0 }),
});
export type AuroraSubscription = z.infer<typeof auroraSubscriptionSchema>;

export const auroraSubscriptionResponseSchema = z.object({
  subscription: auroraSubscriptionSchema,
});

/** GET /api/aurora/billing/topups — the purchasable credit packs. */
export const auroraTopupSchema = z.object({
  id: z.string(),
  credits: z.number(),
});
export type AuroraTopup = z.infer<typeof auroraTopupSchema>;

export const auroraTopupsSchema = z.object({
  topups: z.array(auroraTopupSchema).default([]),
});

/**
 * The checkout endpoints' shared answer: a URL to send the browser to. No
 * default — a body without a URL means the checkout did not start, and a
 * placeholder URL would navigate the user somewhere meaningless.
 */
export const auroraCheckoutResponseSchema = z.object({
  // A checkout URL crosses a trust boundary and is handed to an external
  // navigator. Absolute HTTPS is the minimum safe contract: zod's URL check
  // alone also accepts schemes such as javascript:.
  checkoutUrl: z.url({ protocol: /^https$/ }),
});
export type AuroraCheckout = z.infer<typeof auroraCheckoutResponseSchema>;
