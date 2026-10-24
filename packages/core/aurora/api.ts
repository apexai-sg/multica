import { ApiError, api } from "../api";
import {
  parseWithFallback,
  parseWithFallbackResult,
  type ParseResult,
} from "../api/schema";
import {
  auroraAssetsSchema,
  auroraBalanceSchema,
  auroraCheckoutResponseSchema,
  auroraExecutionTargetSchema,
  auroraGenerationDetailSchema,
  auroraGenerationResponseSchema,
  auroraGenerationsSchema,
  auroraSkillsSchema,
  auroraSubscriptionResponseSchema,
  auroraSubscriptionSchema,
  auroraToolRunSchema,
  auroraToolsSchema,
  auroraTopupsSchema,
  auroraTransactionsSchema,
  type AuroraAsset,
  type AuroraBalance,
  type AuroraCheckout,
  type AuroraExecutionTarget,
  type AuroraGeneration,
  type AuroraGenerationDetail,
  type AuroraSkill,
  type AuroraSubscription,
  type AuroraToolExecutor,
  type AuroraToolRun,
  type AuroraTopup,
  type AuroraTransaction,
} from "./schema";
import {
  normalizeAuroraRuntimeState,
  type AuroraAssetsParams,
  type AuroraListParams,
  type CreateAuroraCheckoutRequest,
  type CreateAuroraGenerationRequest,
  type CreateAuroraToolRunRequest,
  type CreateAuroraTopupCheckoutRequest,
} from "./types";

/**
 * Aurora's network surface.
 *
 * Routes and response schemas are the two halves of one contract, so they live
 * together here and in ./schema rather than being split between
 * `api/client.ts` and `api/schemas.ts` the way the older domains are. The
 * shared client still owns the transport: `api.requestJson` carries the auth
 * and CSRF headers, the CSRF retry, the 401 path and the structured `ApiError`.
 *
 * Each read `parseAurora*` below is the schema plus its fallback, so a malformed
 * body degrades to an empty list, a zero balance, or `null` for a single
 * entity — never a thrown parse error and never a blank screen. Each of those
 * returns that fallback *with* `degraded: true` rather than as a bare value: a
 * fallback is a placeholder, but "0 credits" and "no skills" are claims, and a
 * caller cannot tell a corrupted body from a genuinely empty one without the
 * flag. See `isAuroraDegraded`.
 *
 * The purchase-boundary parsers — `parseAuroraSubscription`,
 * `parseAuroraTopups`, `parseAuroraCheckout` and `parseAuroraTopupCheckout` —
 * are deliberately the exception: a corrupted body there throws so it becomes a
 * visible query or mutation error rather than a placeholder a reader could act
 * on.
 */

const AURORA_SKILLS_PATH = "/api/aurora/skills";
const AURORA_TOOLS_PATH = "/api/aurora/tools";
const AURORA_GENERATIONS_PATH = "/api/aurora/generations";
const AURORA_ASSETS_PATH = "/api/aurora/assets";
const AURORA_RUNTIME_PATH = "/api/aurora/runtime";
const AURORA_BALANCE_PATH = "/api/aurora/billing/balance";
const AURORA_TRANSACTIONS_PATH = "/api/aurora/billing/transactions";
const AURORA_SUBSCRIPTION_PATH = "/api/aurora/billing/subscription";
const AURORA_TOPUPS_PATH = "/api/aurora/billing/topups";
const AURORA_CHECKOUT_PATH = "/api/aurora/billing/checkout";
const AURORA_TOPUP_CHECKOUT_PATH = "/api/aurora/billing/topup/checkout";

// The label `parseWithFallback` logs, built from the path it describes so the
// two cannot drift. The detail route carries its `{id}` placeholder rather than
// a value, so a log line groups every generation read instead of one per id.
const SKILLS_ENDPOINT = `GET ${AURORA_SKILLS_PATH}`;
const TOOLS_ENDPOINT = `GET ${AURORA_TOOLS_PATH}`;
const CREATE_TOOL_RUN_ENDPOINT = `POST ${AURORA_TOOLS_PATH}/{toolId}/runs`;
const GENERATIONS_ENDPOINT = `GET ${AURORA_GENERATIONS_PATH}`;
const GENERATION_ENDPOINT = `GET ${AURORA_GENERATIONS_PATH}/{id}`;
const CREATE_GENERATION_ENDPOINT = `POST ${AURORA_GENERATIONS_PATH}`;
const ASSETS_ENDPOINT = `GET ${AURORA_ASSETS_PATH}`;
const RUNTIME_ENDPOINT = `GET ${AURORA_RUNTIME_PATH}`;
const BALANCE_ENDPOINT = `GET ${AURORA_BALANCE_PATH}`;
const TRANSACTIONS_ENDPOINT = `GET ${AURORA_TRANSACTIONS_PATH}`;
const SUBSCRIPTION_ENDPOINT = `GET ${AURORA_SUBSCRIPTION_PATH}`;
const TOPUPS_ENDPOINT = `GET ${AURORA_TOPUPS_PATH}`;
const CHECKOUT_ENDPOINT = `POST ${AURORA_CHECKOUT_PATH}`;
const TOPUP_CHECKOUT_ENDPOINT = `POST ${AURORA_TOPUP_CHECKOUT_PATH}`;

/**
 * Renders the list params the server understands as a URL suffix — `""` when
 * there are none, otherwise a leading `?`. Absent params are skipped, so a
 * request carries only the filters the caller actually set.
 */
function querySuffix(
  params?: Record<string, string | number | undefined>,
): string {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params ?? {})) {
    if (value !== undefined && value !== "") search.set(key, String(value));
  }
  const encoded = search.toString();
  return encoded ? `?${encoded}` : "";
}

// ---------------------------------------------------------------------------
// Parsing
// ---------------------------------------------------------------------------

// Each fallback is built at the call site rather than shared as a module
// constant: the parser returns the fallback by reference, so one shared array
// would be aliased by every degraded cache entry — and a single in-place
// mutation of any of them would corrupt all the others.

/** The skill catalog, or an empty directory when the body is unreadable. */
export function parseAuroraSkills(
  data: unknown,
): ParseResult<AuroraSkill[]> {
  const parsed = parseWithFallbackResult<{ skills: AuroraSkill[] }>(
    data,
    auroraSkillsSchema,
    { skills: [] },
    { endpoint: SKILLS_ENDPOINT },
  );
  return { value: parsed.value.skills, degraded: parsed.degraded };
}

/**
 * The tool → expert directory, or an empty one when the body is unreadable.
 *
 * Empty is the safe fallback: the tool page reads a missing entry as "no
 * executor configured" and disables submission, so a degraded read never lets
 * the client fall back to guessing an agent.
 */
export function parseAuroraTools(
  data: unknown,
): ParseResult<AuroraToolExecutor[]> {
  const parsed = parseWithFallbackResult<{ tools: AuroraToolExecutor[] }>(
    data,
    auroraToolsSchema,
    { tools: [] },
    { endpoint: TOOLS_ENDPOINT },
  );
  return { value: parsed.value.tools, degraded: parsed.degraded };
}

/**
 * The server answered 2xx but the body could not be read, so whether an issue
 * exists is unknown. Callers must not treat this as "nothing was created": a
 * retry can open a second issue for the same submission.
 */
export class AuroraToolRunOutcomeUnknownError extends Error {
  constructor() {
    super("the tool run outcome could not be read");
    this.name = "AuroraToolRunOutcomeUnknownError";
  }
}

/**
 * The issue a tool run created.
 *
 * A malformed body throws rather than degrading: the issue may already exist,
 * and a placeholder would leave the caller with neither a link nor an error.
 * The error is typed so the caller can tell "the run was refused" apart from
 * "the run happened and we cannot read the answer".
 */
export function parseAuroraToolRun(data: unknown): AuroraToolRun {
  const parsed = parseWithFallback<AuroraToolRun | null>(
    data,
    auroraToolRunSchema,
    null,
    { endpoint: CREATE_TOOL_RUN_ENDPOINT },
  );
  if (!parsed) throw new AuroraToolRunOutcomeUnknownError();
  return parsed;
}

/**
 * The generation created by `POST /api/aurora/generations`, or null when the
 * body is unreadable.
 *
 * Null rather than an empty generation: the id is the only handle the caller
 * has on what was just created, and a placeholder id would send the composer
 * to a detail screen for something that does not exist.
 */
export function parseAuroraGeneration(
  data: unknown,
): ParseResult<AuroraGeneration | null> {
  const parsed = parseWithFallbackResult<{
    generation: AuroraGeneration;
  } | null>(data, auroraGenerationResponseSchema, null, {
    endpoint: CREATE_GENERATION_ENDPOINT,
  });
  return { value: parsed.value?.generation ?? null, degraded: parsed.degraded };
}

/** One page of the workspace's generations, or an empty list. */
export function parseAuroraGenerations(
  data: unknown,
): ParseResult<AuroraGeneration[]> {
  const parsed = parseWithFallbackResult<{ generations: AuroraGeneration[] }>(
    data,
    auroraGenerationsSchema,
    { generations: [] },
    { endpoint: GENERATIONS_ENDPOINT },
  );
  return { value: parsed.value.generations, degraded: parsed.degraded };
}

/** One generation with its assets, or null when the body is unreadable. */
export function parseAuroraGenerationDetail(
  data: unknown,
): ParseResult<AuroraGenerationDetail | null> {
  const parsed = parseWithFallbackResult<{
    generation: AuroraGenerationDetail;
  } | null>(data, auroraGenerationDetailSchema, null, {
    endpoint: GENERATION_ENDPOINT,
  });
  return { value: parsed.value?.generation ?? null, degraded: parsed.degraded };
}

/** The workspace's asset library, or an empty library. */
export function parseAuroraAssets(data: unknown): ParseResult<AuroraAsset[]> {
  const parsed = parseWithFallbackResult<{ assets: AuroraAsset[] }>(
    data,
    auroraAssetsSchema,
    { assets: [] },
    { endpoint: ASSETS_ENDPOINT },
  );
  return { value: parsed.value.assets, degraded: parsed.degraded };
}

/**
 * The workspace's execution target, or the unconfigured projection.
 *
 * A malformed body degrades to `unconfigured` with `degraded: true`: the safe
 * default offers provisioning guidance rather than claiming a node is ready.
 * An unrecognized but well-formed `state` is normalized to `unconfigured` too,
 * while the rest of the target (node, runtime id) is kept — the node's own
 * fields are still readable even when this build does not know its state.
 */
export function parseAuroraRuntime(
  data: unknown,
): ParseResult<AuroraExecutionTarget> {
  const parsed = parseWithFallbackResult<AuroraExecutionTarget>(
    data,
    auroraExecutionTargetSchema,
    { workspaceId: "", node: null, runtimeId: null, state: "unconfigured" },
    { endpoint: RUNTIME_ENDPOINT },
  );
  return {
    value: {
      ...parsed.value,
      state:
        parsed.value.state === "online" && parsed.value.node?.ready !== true
          ? "offline"
          : normalizeAuroraRuntimeState(parsed.value.state),
    },
    degraded: parsed.degraded,
  };
}

/** The caller's wallet, or a zero balance the next refetch will correct. */
export function parseAuroraBalance(data: unknown): ParseResult<AuroraBalance> {
  return parseWithFallbackResult<AuroraBalance>(
    data,
    auroraBalanceSchema,
    { availableMicro: 0 },
    { endpoint: BALANCE_ENDPOINT },
  );
}

/** The caller's ledger, newest first, or an empty ledger. */
export function parseAuroraTransactions(
  data: unknown,
): ParseResult<AuroraTransaction[]> {
  const parsed = parseWithFallbackResult<{
    transactions: AuroraTransaction[];
  }>(data, auroraTransactionsSchema, { transactions: [] }, {
    endpoint: TRANSACTIONS_ENDPOINT,
  });
  return { value: parsed.value.transactions, degraded: parsed.degraded };
}

/**
 * The caller's plan.
 *
 * Optional fields still receive schema defaults for compatibility with older
 * servers, but a body that cannot identify a subscription at all is not safe to
 * turn into Free: this screen can start a real recurring purchase. The unique
 * fallback object lets us retain parseWithFallback's boundary diagnostics while
 * promoting degradation to a visible query error.
 */
export function parseAuroraSubscription(data: unknown): AuroraSubscription {
  const fallback = { subscription: auroraSubscriptionSchema.parse({}) };
  const parsed = parseWithFallback<{ subscription: AuroraSubscription }>(
    data,
    auroraSubscriptionResponseSchema,
    fallback,
    { endpoint: SUBSCRIPTION_ENDPOINT },
  );
  if (parsed === fallback) throw new Error("invalid subscription response");
  return parsed.subscription;
}

/** The purchasable credit packs; an unreadable catalog is a visible error. */
export function parseAuroraTopups(data: unknown): AuroraTopup[] {
  const fallback: { topups: AuroraTopup[] } = { topups: [] };
  const parsed = parseWithFallback<{ topups: AuroraTopup[] }>(
    data,
    auroraTopupsSchema,
    fallback,
    { endpoint: TOPUPS_ENDPOINT },
  );
  if (parsed === fallback) throw new Error("invalid topup response");
  return parsed.topups;
}

/**
 * The validated HTTPS checkout URL from a started purchase.
 *
 * A malformed response throws: checkout is a command, and silently returning a
 * placeholder would leave the button with neither navigation nor an error.
 */
export function parseAuroraCheckout(data: unknown): string {
  return parseCheckoutUrl(data, CHECKOUT_ENDPOINT);
}

/** The same body shape, logged under the topup endpoint it came from. */
export function parseAuroraTopupCheckout(data: unknown): string {
  return parseCheckoutUrl(data, TOPUP_CHECKOUT_ENDPOINT);
}

function parseCheckoutUrl(data: unknown, endpoint: string): string {
  const parsed = parseWithFallback<AuroraCheckout | null>(
    data,
    auroraCheckoutResponseSchema,
    null,
    { endpoint },
  );
  if (!parsed) {
    // A checkout is a command, not a readable projection: treating a malformed
    // 200 body as a successful null result leaves the button with no navigation
    // and no error. Throw so the mutation enters its visible failure state.
    throw new Error("invalid checkout response");
  }
  return parsed.checkoutUrl;
}

// ---------------------------------------------------------------------------
// Requests
// ---------------------------------------------------------------------------

/** The catalog, including the phase-2 skills that cannot be run yet. */
export async function listAuroraSkills(): Promise<ParseResult<AuroraSkill[]>> {
  return parseAuroraSkills(await api.requestJson(AURORA_SKILLS_PATH));
}

/** Every tool page and the expert the server resolves it to. */
export async function listAuroraTools(): Promise<
  ParseResult<AuroraToolExecutor[]>
> {
  return parseAuroraTools(await api.requestJson(AURORA_TOOLS_PATH));
}

/**
 * Creates a tool run: the server seeds the mapped expert, checks the runtime,
 * and opens an issue assigned to that expert.
 *
 * Throws `ApiError` 409 `tool_executor_unmapped` for a tool with no mapping and
 * 503 `aurora_runtime_unavailable` when the workspace has no usable sandbox —
 * in both cases the server created no issue. A malformed success body also
 * throws (see parseAuroraToolRun).
 */
export async function createAuroraToolRun(
  toolId: string,
  request: CreateAuroraToolRunRequest,
): Promise<AuroraToolRun> {
  const body: Record<string, unknown> = {};
  if (request.source !== undefined) body.source = request.source;
  if (request.values) body.values = request.values;
  if (request.fileNames?.length) body.file_names = request.fileNames;
  if (request.attachmentIds?.length) body.attachment_ids = request.attachmentIds;

  const raw = await api.requestJson(
    `${AURORA_TOOLS_PATH}/${encodeURIComponent(toolId)}/runs`,
    { method: "POST", body: JSON.stringify(body) },
  );
  return parseAuroraToolRun(raw);
}

/**
 * Enqueues a generation and reserves its credits.
 *
 * Throws `ApiError` with a 402 when the wallet cannot cover the skill — the
 * caller shows the top-up prompt — and 429 for the monthly quota, the
 * concurrency cap or the per-user frequency gate. Both are distinguishable
 * with `isAuroraInsufficientCreditsError` / `isAuroraRateLimitError`.
 */
export async function createAuroraGeneration(
  request: CreateAuroraGenerationRequest,
): Promise<ParseResult<AuroraGeneration | null>> {
  const raw = await api.requestJson(AURORA_GENERATIONS_PATH, {
    method: "POST",
    body: JSON.stringify(request),
  });
  return parseAuroraGeneration(raw);
}

/** The workspace's generations, newest first. */
export async function listAuroraGenerations(
  params?: AuroraListParams,
): Promise<ParseResult<AuroraGeneration[]>> {
  const raw = await api.requestJson(
    `${AURORA_GENERATIONS_PATH}${querySuffix(params)}`,
  );
  return parseAuroraGenerations(raw);
}

/** One generation with its assets — the progress screen's polling source. */
export async function getAuroraGeneration(
  id: string,
): Promise<ParseResult<AuroraGenerationDetail | null>> {
  const raw = await api.requestJson(
    `${AURORA_GENERATIONS_PATH}/${encodeURIComponent(id)}`,
  );
  return parseAuroraGenerationDetail(raw);
}

/** The workspace's managed execution node and its runtime binding. */
export async function getAuroraRuntime(): Promise<
  ParseResult<AuroraExecutionTarget>
> {
  return parseAuroraRuntime(await api.requestJson(AURORA_RUNTIME_PATH));
}

/** The workspace's content library, optionally narrowed to one generation. */
export async function listAuroraAssets(
  params?: AuroraAssetsParams,
): Promise<ParseResult<AuroraAsset[]>> {
  const raw = await api.requestJson(
    `${AURORA_ASSETS_PATH}${querySuffix(params)}`,
  );
  return parseAuroraAssets(raw);
}

/** Removes an asset and the object behind it. The server answers 204. */
export async function deleteAuroraAsset(assetId: string): Promise<void> {
  await api.requestJson(
    `${AURORA_ASSETS_PATH}/${encodeURIComponent(assetId)}`,
    { method: "DELETE" },
  );
}

/**
 * The authenticated download route for one asset.
 *
 * This is a request target, not a link target: the route requires the bearer
 * token, so a bare navigation 401s. `downloadAuroraAsset` requests it through
 * the shared API client and then follows whichever shape the server answers
 * with — a redirect to a short-lived signed URL, or the streamed file.
 */
export function auroraAssetDownloadPath(assetId: string): string {
  return `${AURORA_ASSETS_PATH}/${encodeURIComponent(assetId)}/download`;
}

/** The caller's wallet. Every workspace shows the same balance. */
export async function getAuroraBalance(): Promise<ParseResult<AuroraBalance>> {
  return parseAuroraBalance(await api.requestJson(AURORA_BALANCE_PATH));
}

/** The caller's ledger, newest first. */
export async function listAuroraTransactions(): Promise<
  ParseResult<AuroraTransaction[]>
> {
  return parseAuroraTransactions(
    await api.requestJson(AURORA_TRANSACTIONS_PATH),
  );
}

/** The caller's plan and this month's usage. Every workspace shows the same. */
export async function getAuroraSubscription(): Promise<AuroraSubscription> {
  return parseAuroraSubscription(
    await api.requestJson(AURORA_SUBSCRIPTION_PATH),
  );
}

/** The credit packs this deployment sells. */
export async function listAuroraTopups(): Promise<AuroraTopup[]> {
  return parseAuroraTopups(await api.requestJson(AURORA_TOPUPS_PATH));
}

/**
 * Starts a subscription checkout and returns the URL to send the browser to.
 *
 * Throws `ApiError` with 409 when the caller already has a live subscription,
 * and 503 when the deployment has no Stripe configuration — neither is a state
 * the client can retry its way out of, so the caller reports it rather than
 * retrying.
 */
export async function createAuroraCheckout(
  request: CreateAuroraCheckoutRequest,
): Promise<string> {
  const raw = await api.requestJson(AURORA_CHECKOUT_PATH, {
    method: "POST",
    body: JSON.stringify(request),
  });
  return parseAuroraCheckout(raw);
}

/** Starts a one-time credit purchase and returns the checkout URL. */
export async function createAuroraTopupCheckout(
  request: CreateAuroraTopupCheckoutRequest,
): Promise<string> {
  const raw = await api.requestJson(AURORA_TOPUP_CHECKOUT_PATH, {
    method: "POST",
    body: JSON.stringify(request),
  });
  return parseAuroraTopupCheckout(raw);
}

// ---------------------------------------------------------------------------
// Error classification
// ---------------------------------------------------------------------------

/**
 * 402: the wallet cannot cover the skill's credits, so nothing was reserved and
 * nothing was enqueued. The composer turns this into the top-up prompt instead
 * of a generic failure.
 */
export function isAuroraInsufficientCreditsError(error: unknown): boolean {
  return error instanceof ApiError && error.status === 402;
}

/**
 * 429: one of the three server gates refused the request — the monthly
 * generation quota, the concurrency cap, or the per-user frequency limit. They
 * share a status and differ only in the server's message, which is not a
 * contract, so callers surface one "limit reached, try again later" line
 * rather than guessing which gate fired.
 */
export function isAuroraRateLimitError(error: unknown): boolean {
  return error instanceof ApiError && error.status === 429;
}

/**
 * 409: a checkout was refused because the caller already has an active or
 * past-due subscription. Retrying cannot succeed, and the plan screen says why
 * rather than reporting a generic failure.
 */
export function isAuroraCheckoutConflictError(error: unknown): boolean {
  return error instanceof ApiError && error.status === 409;
}

/**
 * 503: this deployment has no payment provider or no price for the requested
 * plan. Not retryable, and not the user's fault — the screen says payments are
 * unavailable rather than asking them to try again.
 */
export function isAuroraPaymentsUnavailableError(error: unknown): boolean {
  return error instanceof ApiError && error.status === 503;
}

/**
 * 404: no generation with this id exists in the caller's workspace. The detail
 * route renders a not-found state rather than a retry card — the read
 * succeeded, the generation is simply gone. A generation in another workspace
 * answers the same way, because the query is scoped by workspace.
 */
export function isAuroraGenerationNotFoundError(error: unknown): boolean {
  return error instanceof ApiError && error.status === 404;
}

/**
 * A read the server answered 2xx to, with a body the schema rejected.
 *
 * The query is not in an error state — nothing threw — so a view that branches
 * on `isError` alone would render the fallback as if it were the response: an
 * empty directory, an empty library, or a wallet holding 0. Reading this
 * alongside `isError` is what makes a corrupted body a load failure rather
 * than an empty state. Takes `undefined` because a query that has not resolved
 * yet has no payload to judge.
 *
 * The `schemaLogger` warning is still the diagnostic trail for *why* the body
 * failed; this only says that it did.
 */
export function isAuroraDegraded(
  payload: ParseResult<unknown> | undefined,
): boolean {
  return payload?.degraded === true;
}
