/**
 * The part of Aurora's type surface that is not derived from a wire schema.
 *
 * The entity shapes — `AuroraSkill`, `AuroraGeneration`, `AuroraAsset`,
 * `AuroraGenerationDetail`, `AuroraBalance`, `AuroraTransaction` — are inferred
 * from the zod schemas in ./schema, because that is where the wire contract
 * lives. Both are re-exported from ./index, so consumers import from
 * `@multica/core/aurora`. What is left here is the vocabulary the hooks need:
 * request bodies, list params, and the generation status rules the progress
 * poll keys on.
 */

/**
 * One attachment constraint on a skill, as served under `attachment_rules`.
 *
 * The server counts every kind listed in one rule together, so `transcription`
 * is one rule over `["audio", "video"]` with a max of 1.
 */
export interface AuroraSkillAttachmentRule {
  /** Attachment kinds this rule accepts: image | document | audio | video. */
  kinds: string[];
  /** Minimum number of accepted files; 0 means optional. */
  min: number;
  /** Maximum number of accepted files. */
  max: number;
  /** Per-file size cap in bytes. */
  maxBytes: number;
}

/** Body of `POST /api/aurora/generations`. */
export interface CreateAuroraGenerationRequest {
  skillId: string;
  prompt: string;
  /** Ids of already-uploaded workspace attachments, in the skill's kinds. */
  attachmentIds?: string[];
}

/**
 * Body of `POST /api/aurora/tools/{toolId}/runs`.
 *
 * The server owns the executor and the issue text; the client only carries what
 * the person entered. Ids must already be uploaded workspace attachments — the
 * server rejects one that belongs elsewhere.
 */
export interface CreateAuroraToolRunRequest {
  source?: string;
  values?: Record<string, string>;
  attachmentIds?: string[];
}

/**
 * Paging for the generation and asset lists. Both are optional: the server
 * falls back to its own defaults (50 rows from offset 0) for a missing,
 * unparseable or out-of-range value, so a client bug cannot turn a list into a
 * 400.
 *
 * Declared as aliases rather than interfaces so they stay assignable to the
 * `Record<string, ...>` that ./api serialises query params with — an interface
 * has no implicit index signature.
 */
export type AuroraListParams = {
  limit?: number;
  offset?: number;
};

/** Paging for the asset library, plus its one filter. */
export type AuroraAssetsParams = AuroraListParams & {
  /** Narrows the library to one generation. Omitted lists the whole workspace. */
  generationId?: string;
};

/**
 * How often an unfinished generation is re-read, in ms.
 *
 * The MVP polls rather than subscribes: a generation's status is derived
 * server-side from the task it enqueued (`aurora.go`), and the `task:progress`
 * realtime frame is not produced for it yet. Three seconds keeps a
 * composer-to-result flow feeling live; one in-flight generation costs one
 * request per three seconds until it settles.
 */
export const AURORA_GENERATION_POLL_MS = 3_000;

/**
 * Whether the server has stopped working on this generation.
 *
 * Only "completed" and "failed" are final — the server collapses queued,
 * dispatched, running, waiting and deferred into queued/running, and a
 * cancelled task into failed. The check is a positive list rather than a
 * negation of "queued"/"running" on purpose: an unrecognised status from a
 * newer server would otherwise read as final and stop the poll in the window
 * before the generation's assets existed.
 *
 * "needs_input" is not final either: it is the third, declarative ending — the
 * round stopped to ask a question and resumes when the person replies.
 */
export function isAuroraGenerationTerminal(status: string | undefined): boolean {
  return status === "completed" || status === "failed";
}

/**
 * The high-level execution state of a workspace.
 *
 * The server projects the workspace node and Fleet health. No node is
 * `unconfigured`; a starting node is `provisioning`. Unavailable nodes are
 * `offline` or `failed`, and only a ready node is `online`.
 */
export type AuroraRuntimeState =
  | "unconfigured"
  | "provisioning"
  | "online"
  | "offline"
  | "failed";

/**
 * Maps a wire state onto the view's runtime vocabulary.
 *
 * The schema keeps the wire value a plain string so a state from a newer
 * server still parses; an unrecognized one collapses to `unconfigured`, which
 * is the documented safe default (it offers provisioning guidance rather than
 * claiming a node is ready).
 */
export function normalizeAuroraRuntimeState(
  state: string | undefined,
): AuroraRuntimeState {
  switch (state) {
    case "provisioning":
    case "online":
    case "offline":
    case "failed":
    case "unconfigured":
      return state;
    default:
      return "unconfigured";
  }
}

/**
 * A payment channel Aurora can sell a subscription through.
 *
 * The server decides which ones a deployment offers and lists them as
 * `availableProviders` on the plan read, in the order the buttons should
 * appear. "stripe" is card checkout plus the Stripe Customer Portal; "antom" is
 * the monthly-only channel that has no portal, so it is cancelled in the app.
 */
export type AuroraPaymentProvider = "stripe" | "antom";

/**
 * Whether a value from the wire is a channel this build can charge through.
 *
 * The plan read drops anything else (./schema) rather than letting it become a
 * button that submits a provider the checkout endpoint would reject.
 */
export function isAuroraPaymentProvider(
  value: unknown,
): value is AuroraPaymentProvider {
  return value === "stripe" || value === "antom";
}

/**
 * Body of `POST /api/aurora/billing/checkout`.
 *
 * The return URLs are the client's to build: it knows its own origin and the
 * workspace slug route (`/<slug>/billing`), and the API host is not a page the
 * user can be sent back to. The server only checks that they are http(s)
 * absolute URLs.
 *
 * `provider` is always sent, even though the server defaults a missing one to
 * Stripe: the channel is the user's choice, and a request that silently fell
 * back would charge through a different one than the button named.
 */
export interface CreateAuroraCheckoutRequest {
  tier: string;
  billingCycle: "monthly" | "yearly";
  provider: AuroraPaymentProvider;
  successUrl: string;
  cancelUrl: string;
}

/** Body of `POST /api/aurora/billing/topup/checkout`. */
export interface CreateAuroraTopupCheckoutRequest {
  topupId: string;
  successUrl: string;
  cancelUrl: string;
}

/**
 * Body of `POST /api/aurora/billing/portal`.
 *
 * The portal is Stripe's own page, so the client only names where Stripe sends
 * the subscriber back afterwards. The server validates it against the same
 * trusted origins the checkout return URLs use.
 */
export interface CreateAuroraBillingPortalRequest {
  returnUrl: string;
}
