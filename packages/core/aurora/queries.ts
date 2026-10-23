import { queryOptions, useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "../hooks";
import {
  getAuroraBalance,
  getAuroraGeneration,
  getAuroraRuntime,
  getAuroraSubscription,
  listAuroraAssets,
  listAuroraGenerations,
  listAuroraSkills,
  listAuroraTools,
  listAuroraTopups,
  listAuroraTransactions,
} from "./api";
import {
  AURORA_GENERATION_POLL_MS,
  isAuroraGenerationTerminal,
  type AuroraAssetsParams,
  type AuroraListParams,
} from "./types";

/**
 * Workspace-scoped Aurora keys.
 *
 * The catalog, the generations and the library are all per-workspace, so `wsId`
 * is the second segment of every key: switching workspaces must not serve one
 * workspace's generations out of another's cache.
 */
export const auroraKeys = {
  all: (wsId: string) => ["aurora", wsId] as const,
  skills: (wsId: string) => [...auroraKeys.all(wsId), "skills"] as const,
  /** Each tool page's executing expert, as the server maps it. */
  tools: (wsId: string) => [...auroraKeys.all(wsId), "tools"] as const,
  /** The list and every detail: invalidating this refreshes both. */
  generations: (wsId: string) => [...auroraKeys.all(wsId), "generations"] as const,
  // The page params are part of the key, so a paged read cannot be served the
  // default page's rows. The "list" segment keeps a page from ever colliding
  // with a generation id.
  generationList: (wsId: string, params?: AuroraListParams) =>
    [...auroraKeys.generations(wsId), "list", params ?? {}] as const,
  generation: (wsId: string, id: string) =>
    [...auroraKeys.generations(wsId), id] as const,
  assets: (wsId: string) => [...auroraKeys.all(wsId), "assets"] as const,
  /** The workspace's managed execution node and its runtime binding. */
  runtime: (wsId: string) => [...auroraKeys.all(wsId), "runtime"] as const,
  // No "list" segment here: assets has no id-keyed sibling to collide with, so
  // the params object is the only thing that can occupy this position.
  assetList: (wsId: string, params?: AuroraAssetsParams) =>
    [...auroraKeys.assets(wsId), params ?? {}] as const,
};

/**
 * Wallet keys, deliberately *not* scoped to a workspace.
 *
 * The Aurora wallet belongs to the user, not the workspace — the server reads
 * the caller's account and every workspace shows the same balance — so keying
 * these on `wsId` would refetch identical data on every workspace switch. Same
 * reasoning as `packages/core/billing/queries.ts`. The separate `"wallet"`
 * segment keeps them out of the `["aurora", wsId]` subtree, so invalidating a
 * workspace's data cannot touch the account's.
 */
export const auroraWalletKeys = {
  all: () => ["aurora", "wallet"] as const,
  balance: () => [...auroraWalletKeys.all(), "balance"] as const,
  transactions: () => [...auroraWalletKeys.all(), "transactions"] as const,
  /**
   * The plan and the packs on sale, also user-scoped and also exempt from the
   * `wsId` rule: a subscription belongs to the account, not to the workspace
   * the screen is open in, and the server reads the caller rather than
   * X-Workspace-ID. Keying them on `wsId` would refetch identical data on every
   * workspace switch — the same reason the wallet keys above live here.
   */
  subscription: () => [...auroraWalletKeys.all(), "subscription"] as const,
  topups: () => [...auroraWalletKeys.all(), "topups"] as const,
};

// The cache default is `staleTime: Infinity` (packages/core/query-client.ts),
// which suits data only this client moves. Aurora's reads are not all like
// that: a generation's status and its assets change server-side while the user
// is on another screen, and with an infinite stale-time the mount-time and
// reconnect refetches never fire, leaving only the mutation invalidations. The
// queries below that can drift on their own therefore set a stale-time; the
// catalog, which is a server-side constant (`aurora/catalog.go`), keeps the
// default.
//
// Most `data` below is a `ParseResult`: the payload plus whether the body it
// came from was readable. A degraded read is *not* an error state — nothing
// threw, so `isError` stays false — which means a view that only checks
// `isError` renders the fallback as the response. Views combine the two with
// `isAuroraDegraded` (./api). The plan and the pack catalog are the exception:
// their parsers throw on a malformed body, so those queries surface a real
// error state instead.

export function auroraSkillsOptions(wsId: string) {
  return queryOptions({
    queryKey: auroraKeys.skills(wsId),
    queryFn: () => listAuroraSkills(),
    enabled: wsId.length > 0,
  });
}

/**
 * The tool → expert directory.
 *
 * The mapping is a server-side constant, so it keeps the cache's default
 * stale-time; it shares the workspace scope because the submission pre-check
 * pairs it with that workspace's agent list.
 */
export function auroraToolsOptions(wsId: string) {
  return queryOptions({
    queryKey: auroraKeys.tools(wsId),
    queryFn: () => listAuroraTools(),
    enabled: wsId.length > 0,
  });
}

export function auroraGenerationsOptions(
  wsId: string,
  params?: AuroraListParams,
) {
  return queryOptions({
    queryKey: auroraKeys.generationList(wsId, params),
    queryFn: () => listAuroraGenerations(params),
    enabled: wsId.length > 0,
    staleTime: 30 * 1000,
  });
}

export function auroraGenerationDetailOptions(wsId: string, id: string) {
  return queryOptions({
    queryKey: auroraKeys.generation(wsId, id),
    queryFn: () => getAuroraGeneration(id),
    enabled: wsId.length > 0 && id.length > 0,
    // MVP progress: poll while the generation can still change, stop once it
    // cannot. A malformed-but-successful body degrades to a null value, which
    // is not terminal, so it keeps polling rather than parking the screen on an
    // empty result — the next tick is what recovers it.
    //
    // A *failed* read stops instead. `refetchInterval` ignores query status
    // (QueryObserver re-arms the timer unconditionally), so without this an
    // id that 404s — deleted, or belonging to another workspace — would be
    // re-requested every 3s for as long as the screen is open, and with the
    // cache's `retry: 1` each tick costs two round-trips. The query is in a
    // terminal error state by then, so polling it again buys nothing.
    refetchInterval: (query) =>
      query.state.status === "error" ||
      isAuroraGenerationTerminal(query.state.data?.value?.status)
        ? false
        : AURORA_GENERATION_POLL_MS,
  });
}

export function auroraAssetsOptions(wsId: string, params?: AuroraAssetsParams) {
  return queryOptions({
    queryKey: auroraKeys.assetList(wsId, params),
    queryFn: () => listAuroraAssets(params),
    enabled: wsId.length > 0,
    staleTime: 30 * 1000,
  });
}

/**
 * The workspace's execution target.
 *
 * The node moves on its own — provisioning starts on a generation and the
 * reaper stops an idle node later — so a short stale-time lets a return to the
 * screen pick that up without polling for it. A malformed body degrades to
 * `unconfigured` in the parser rather than throwing, so this query has no
 * error path to drive from a drifted response.
 */
export function auroraRuntimeOptions(wsId: string) {
  return queryOptions({
    queryKey: auroraKeys.runtime(wsId),
    queryFn: () => getAuroraRuntime(),
    enabled: wsId.length > 0,
    staleTime: 30 * 1000,
  });
}

export function auroraBalanceOptions() {
  return queryOptions({
    queryKey: auroraWalletKeys.balance(),
    queryFn: () => getAuroraBalance(),
    // The wallet moves only when a generation reserves or settles credits, so
    // it is refreshed by the mutation that causes the move rather than by
    // polling for it.
    staleTime: 30 * 1000,
  });
}

export function auroraTransactionsOptions() {
  return queryOptions({
    queryKey: auroraWalletKeys.transactions(),
    queryFn: () => listAuroraTransactions(),
    staleTime: 30 * 1000,
  });
}

export function auroraSubscriptionOptions() {
  return queryOptions({
    queryKey: auroraWalletKeys.subscription(),
    queryFn: () => getAuroraSubscription(),
    // The plan moves when a checkout completes — which happens on another
    // origin, in the Stripe-hosted page — so the screen cannot rely on a
    // mutation invalidation to pick the change up. A short stale-time is what
    // makes the return trip from checkout show the new plan without a manual
    // reload, without polling for a change that is usually not coming.
    staleTime: 30 * 1000,
  });
}

export function auroraTopupsOptions() {
  return queryOptions({
    queryKey: auroraWalletKeys.topups(),
    queryFn: () => listAuroraTopups(),
    // The catalog is deployment configuration and does not move while the app
    // is open.
    staleTime: Infinity,
  });
}

// The hooks below resolve the workspace themselves instead of taking it: every
// Aurora surface renders inside the `[workspaceSlug]` layout, which is what
// establishes the current workspace, so there is no case in which a caller has
// an id that `useWorkspaceId` cannot. The `*Options` functions above stay
// wsId-first for callers that already hold one (prefetch, tests).

/** The catalog, including the phase-2 skills that cannot be run yet. */
export function useAuroraSkills() {
  const wsId = useWorkspaceId();
  return useQuery(auroraSkillsOptions(wsId));
}

/** The workspace's generations, newest first. */
export function useAuroraGenerations(params?: AuroraListParams) {
  const wsId = useWorkspaceId();
  return useQuery(auroraGenerationsOptions(wsId, params));
}

/** One generation with its assets, polled while it is still in flight. */
export function useAuroraGenerationDetail(id: string) {
  const wsId = useWorkspaceId();
  return useQuery(auroraGenerationDetailOptions(wsId, id));
}

/** The workspace's content library, optionally narrowed to one generation. */
export function useAuroraAssets(params?: AuroraAssetsParams) {
  const wsId = useWorkspaceId();
  return useQuery(auroraAssetsOptions(wsId, params));
}

/** The workspace's managed execution node and its runtime binding. */
export function useAuroraRuntime() {
  const wsId = useWorkspaceId();
  return useQuery(auroraRuntimeOptions(wsId));
}

/** The caller's wallet. */
export function useAuroraBalance() {
  return useQuery(auroraBalanceOptions());
}

/** The caller's ledger, newest first. */
export function useAuroraTransactions() {
  return useQuery(auroraTransactionsOptions());
}

/** The caller's plan, its limits and this month's usage. */
export function useAuroraSubscription() {
  return useQuery(auroraSubscriptionOptions());
}

/** The credit packs this deployment sells. */
export function useAuroraTopups() {
  return useQuery(auroraTopupsOptions());
}
