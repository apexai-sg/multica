/**
 * How the catalog's and the server's vocabulary reaches the screen.
 *
 * Two things live here, both of them about picking a label rather than
 * rendering one:
 *
 *  - The server enums, mapped onto the locale keys that name them. Both are
 *    deliberately open on the wire — the schemas keep them `z.string()` so a
 *    value from a newer server still parses — so each mapping ends in a
 *    fallback instead of assuming this build has seen every value. Without it a
 *    new status would render as an undefined label rather than as "Unknown".
 *  - The catalog's bilingual skill names, picked for the reader's language.
 *
 * Each mapping returns a *key segment*, not copy. Callers index the bundle with
 * it under the namespace that owns the label: `aurora.composer.status.*` for a
 * generation status, `billing.transaction.kind.*` for a ledger kind.
 */

import type { AuroraSkill } from "@multica/core/aurora";

/** Where a generation is, as `aurora.composer.status.*`. */
export type AuroraGenerationStatusLabel =
  | "queued"
  | "running"
  | "completed"
  | "failed"
  | "needs_input"
  | "unknown";

const GENERATION_STATUS_LABELS: Record<string, AuroraGenerationStatusLabel> = {
  queued: "queued",
  running: "running",
  completed: "completed",
  failed: "failed",
  needs_input: "needs_input",
};

/**
 * The status key for a generation.
 *
 * The server collapses the lifecycle it reads from the enqueued task into these
 * four (`aurora.go`), and a cancelled task reports as `failed`.
 */
export function generationStatusLabel(
  status: string | undefined,
): AuroraGenerationStatusLabel {
  return GENERATION_STATUS_LABELS[status ?? ""] ?? "unknown";
}

/** How a wallet moved, as `billing.transaction.kind.*`. */
export type AuroraLedgerKindLabel =
  | "topup"
  | "deduction"
  | "refund"
  | "adjustment"
  | "expire"
  | "unknown";

const LEDGER_KIND_LABELS: Record<string, AuroraLedgerKindLabel> = {
  topup: "topup",
  deduction: "deduction",
  refund: "refund",
  adjustment: "adjustment",
  // Plan 5's monthly settlement writes this; the server's kind list
  // (`aurora/credit.go`) documents it as unreleased, so it is only ever seen
  // once that lands.
  expire: "expire",
};

/**
 * The label key for a ledger row's kind.
 *
 * This is the fallback path for a transaction the screen cannot attribute to a
 * generation — a grant, a plan key, or anything a later release writes.
 */
export function ledgerKindLabel(
  kind: string | undefined,
): AuroraLedgerKindLabel {
  return LEDGER_KIND_LABELS[kind ?? ""] ?? "unknown";
}

/**
 * The name to put on a generation that only carries a skill id.
 *
 * The consumer surfaces show agent names; this is the fallback for an id no
 * visible agent owns. The catalog carries a Chinese `name` and English
 * `nameEn`, so a Chinese reader gets the Chinese name and every other reader
 * gets the English one. `nameEn` defaults to `""` in the schema, so a Chinese
 * name is still the better answer than an empty label.
 */
export function skillDisplayName(skill: AuroraSkill, locale: string): string {
  if (locale.startsWith("zh")) return skill.name;
  return skill.nameEn || skill.name;
}

/**
 * The catalog as a `skillId -> display name` lookup, for the screens that hold
 * an id rather than the skill it names.
 *
 * Both the library and the ledger resolve a skill this way, so building the map
 * lives here rather than being spelled out twice with the two copies free to
 * disagree about the language.
 */
export function skillDisplayNamesById(
  skills: AuroraSkill[],
  locale: string,
): Map<string, string> {
  return new Map(
    skills.map((skill) => [skill.id, skillDisplayName(skill, locale)]),
  );
}

/** A plan, as `aurora.billing.subscription.tiers.*`. */
export type AuroraTierLabel = "free" | "creator" | "pro" | "unknown";

const TIER_LABELS: Record<string, AuroraTierLabel> = {
  free: "free",
  creator: "creator",
  pro: "pro",
};

/**
 * The label key for a plan.
 *
 * The server sends the tier as a plain string (a new one must still parse), so
 * this ends in a fallback: a plan this build has never heard of reads as an
 * unknown plan rather than as an empty label.
 */
export function subscriptionTierLabel(
  tier: string | undefined,
): AuroraTierLabel {
  return TIER_LABELS[tier ?? ""] ?? "unknown";
}

/** A subscription state, as `aurora.billing.subscription.status.*`. */
export type AuroraSubscriptionStatusLabel =
  | "active"
  | "past_due"
  | "canceled"
  | "unknown";

const SUBSCRIPTION_STATUS_LABELS: Record<
  string,
  AuroraSubscriptionStatusLabel
> = {
  active: "active",
  past_due: "past_due",
  canceled: "canceled",
};

/** The label key for a subscription's stored status. */
export function subscriptionStatusLabel(
  status: string | undefined,
): AuroraSubscriptionStatusLabel {
  return SUBSCRIPTION_STATUS_LABELS[status ?? ""] ?? "unknown";
}

/** A credit pack, as `aurora.billing.topup.packs.*`. */
export type AuroraTopupPackLabel = "t5" | "t20" | "other";

const TOPUP_PACK_LABELS: Record<string, AuroraTopupPackLabel> = {
  t5: "t5",
  t20: "t20",
};

/**
 * The label key for a credit pack's price.
 *
 * Packs are priced in copy rather than on the wire: the server sends the pack's
 * credits, not what it costs, so the price lives with the rest of the
 * translated text. A pack this build does not know gets no price rather than a
 * wrong one.
 */
export function topupPackLabel(id: string): AuroraTopupPackLabel {
  return TOPUP_PACK_LABELS[id] ?? "other";
}
