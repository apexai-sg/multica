"use client";

import { useEffect, useMemo } from "react";
import { Wallet } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { Progress } from "@multica/ui/components/ui/progress";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import {
  isAuroraCheckoutConflictError,
  isAuroraDegraded,
  isAuroraPaymentsUnavailableError,
  useAuroraBalance,
  useAuroraGenerations,
  useAuroraSkills,
  useAuroraSubscription,
  useAuroraTopups,
  useAuroraTransactions,
  useCreateAuroraCheckout,
  useCreateAuroraTopupCheckout,
  type AuroraSubscription,
  type AuroraTransaction,
} from "@multica/core/aurora";
import { useWorkspaceSlug } from "@multica/core/paths";
import {
  AuroraPageHeader,
  AuroraPageState,
} from "./aurora-page-header";
import { useAppOrigin, useNavigation } from "../navigation";
import { openExternal } from "../platform";
import { useLocale, useT } from "../i18n";
import { formatCredits, formatMicroCredits } from "./format";
import {
  ledgerKindLabel,
  skillDisplayNamesById,
  subscriptionStatusLabel,
  subscriptionTierLabel,
  topupPackLabel,
} from "./labels";
import { AuroraLoadFailed } from "./load-failed";

const CHECKOUT_RETURN_POLL_MS = 2_000;
const CHECKOUT_RETURN_MAX_POLLS = 30;

/**
 * The wallet and the plan: what is left, how it moved, and the way to buy more.
 *
 * Every number the ledger carries is a micro-credit, so the conversion to
 * credits happens here rather than at each call site — a screen that showed a
 * raw micro-credit would be off by a factor of a million from the catalog it
 * prices against.
 */
export function AuroraBilling() {
  const { t } = useT("aurora");
  const { t: tBilling } = useT("billing");
  const locale = useLocale();
  const navigation = useNavigation();
  const appOrigin = useAppOrigin();
  const slug = useWorkspaceSlug();
  const balanceQuery = useAuroraBalance();
  const transactionsQuery = useAuroraTransactions();
  const generationsQuery = useAuroraGenerations();
  const skillsQuery = useAuroraSkills();
  const subscriptionQuery = useAuroraSubscription();
  const topupsQuery = useAuroraTopups();
  const checkout = useCreateAuroraCheckout();
  const topupCheckout = useCreateAuroraTopupCheckout();
  const checkoutReturned = navigation.searchParams.get("checkout") === "success";
  const { refetch: refetchBalance } = balanceQuery;
  const { refetch: refetchTransactions } = transactionsQuery;
  const { refetch: refetchSubscription } = subscriptionQuery;

  // Stripe may redirect the browser before its webhook commits. A one-off read
  // can therefore return the pre-payment plan and wallet; staleTime only marks
  // that data stale later and does not schedule another read. Poll for one
  // minute after an explicit success return so the webhook race self-heals,
  // then stop to avoid turning an abandoned or delayed payment into permanent
  // background traffic.
  useEffect(() => {
    if (!checkoutReturned) return;

    let polls = 0;
    const refresh = () => {
      polls += 1;
      void refetchBalance();
      void refetchTransactions();
      void refetchSubscription();
    };
    refresh();
    const timer = setInterval(() => {
      refresh();
      if (polls >= CHECKOUT_RETURN_MAX_POLLS) clearInterval(timer);
    }, CHECKOUT_RETURN_POLL_MS);
    return () => clearInterval(timer);
  }, [
    checkoutReturned,
    refetchBalance,
    refetchTransactions,
    refetchSubscription,
  ]);

  const transactions = transactionsQuery.data?.value ?? [];

  // A ledger row's `reference` is a generation id for a charge or its refund,
  // and a grant key (`sub:`/`signup:`) for everything else. Only the first kind
  // can be resolved back to something a reader recognises, so the two maps
  // below are built once and both misses fall through to the kind.
  const { skillIdByGeneration, skillNameById } = useMemo(() => {
    const skillIdByGeneration = new Map<string, string>();
    for (const generation of generationsQuery.data?.value ?? []) {
      skillIdByGeneration.set(generation.id, generation.skillId);
    }
    return {
      skillIdByGeneration,
      skillNameById: skillDisplayNamesById(
        skillsQuery.data?.value ?? [],
        locale,
      ),
    };
  }, [generationsQuery.data, skillsQuery.data, locale]);

  // Where Stripe sends the browser back to. Built from the adapter's app origin
  // and the route's workspace slug, so a desktop client returns to the
  // environment it is connected to rather than to whatever origin the renderer
  // happens to be on. Both can be missing (server render, a component mounted
  // outside a workspace route), and the checkout endpoints take these URLs from
  // the client — so a missing one disables the buttons rather than sending
  // Stripe a URL the user cannot come back from.
  const returnURLs = useMemo(() => {
    if (!appOrigin || !slug) return null;
    const base = `${appOrigin}/${slug}/billing`;
    return {
      successUrl: `${base}?checkout=success`,
      cancelUrl: `${base}?checkout=cancel`,
    };
  }, [appOrigin, slug]);

  function referenceSkillName(transaction: AuroraTransaction): string | null {
    const skillId = skillIdByGeneration.get(transaction.reference);
    if (!skillId) return null;
    return skillNameById.get(skillId) ?? null;
  }

  const isLoading =
    balanceQuery.isPending ||
    transactionsQuery.isPending ||
    subscriptionQuery.isPending ||
    topupsQuery.isPending;
  // A read the schema rejected is fatal on its own, because its fallback is a
  // number rather than a blank: a corrupted balance parses to 0, and "you have
  // 0 credits" is not a degraded rendering of the truth — it is a different
  // statement, and one that sends the user to top up against it. The ledger
  // degrades to an empty list, which reads as "you have never spent anything".
  //
  // A *failed* read is only fatal when it left nothing behind: a background
  // refetch that failed over rows already in cache is a stale ledger, and
  // replacing a readable balance and ledger with an error card is the worse
  // trade. Same rule as the library. Billing state is included here because
  // hiding a failed plan read or rendering a failed catalog read as "no packs"
  // would misrepresent a purchase boundary.
  const loadFailed =
    (transactionsQuery.isError && transactions.length === 0) ||
    isAuroraDegraded(transactionsQuery.data) ||
    (balanceQuery.isError && balanceQuery.data === undefined) ||
    isAuroraDegraded(balanceQuery.data) ||
    (subscriptionQuery.isError && subscriptionQuery.data === undefined) ||
    (topupsQuery.isError && topupsQuery.data === undefined);

  const balance = formatMicroCredits(
    balanceQuery.data?.value?.availableMicro ?? 0,
    locale,
  );
  const topups = topupsQuery.data ?? [];
  const subscription = subscriptionQuery.data;
  const checkoutError = checkout.error ?? topupCheckout.error;

  return (
    <div className="aurora-page">
      <AuroraPageHeader
        icon={Wallet}
        title={t(($) => $.billing.title)}
      />
      {isLoading ? (
        <BillingSkeleton />
      ) : loadFailed ? (
        <AuroraLoadFailed
          title={t(($) => $.billing.load_failed_title)}
          onRetry={() => {
            void balanceQuery.refetch();
            void transactionsQuery.refetch();
            void subscriptionQuery.refetch();
            void topupsQuery.refetch();
          }}
        />
      ) : (
        <div className="flex flex-col gap-6">
          <div className="aurora-stat-cards">
            <article className="aurora-panel aurora-stat">
              <span>{t(($) => $.billing.balance_title)}</span>
              <strong>{balance}</strong>
            </article>
            {subscription ? (
              <article className="aurora-panel aurora-stat">
                <span>{t(($) => $.billing.subscription.usage_label)}</span>
                <strong>
                  {t(($) => $.billing.subscription.usage_value, {
                    used: subscription.usage.generationsUsedThisMonth,
                    limit: subscription.limits.generationsPerMonth,
                  })}
                </strong>
              </article>
            ) : null}
            <article className="aurora-panel aurora-stat">
              <span>{t(($) => $.billing.transactions_title)}</span>
              <strong>{transactions.length}</strong>
            </article>
          </div>
          {subscription ? (
            <PlanSection
              subscription={subscription}
              pending={checkout.isPending}
              canCheckout={returnURLs !== null}
              onSubscribe={(tier) => {
                if (!returnURLs) return;
                checkout.mutate(
                  { tier, billingCycle: "monthly", ...returnURLs },
                  {
                    onSuccess: (checkoutUrl) =>
                      openExternal(checkoutUrl, { webTarget: "same-tab" }),
                  },
                );
              }}
            />
          ) : null}

          {checkoutError ? (
            <p role="alert" className="text-caption text-destructive">
              {isAuroraCheckoutConflictError(checkoutError)
                ? t(($) => $.billing.checkout.already_subscribed)
                : isAuroraPaymentsUnavailableError(checkoutError)
                  ? t(($) => $.billing.checkout.unavailable)
                  : t(($) => $.billing.checkout.failed)}
            </p>
          ) : null}

          <section className="flex flex-col gap-2">
            <div className="aurora-section-head">
              <h2>{t(($) => $.billing.topup.title)}</h2>
            </div>
            {topups.length === 0 ? (
              <p className="text-caption text-muted-foreground">
                {t(($) => $.billing.topup.empty)}
              </p>
            ) : (
              <div className="aurora-plans">
                {topups.map((topup) => (
                  <button
                    key={topup.id}
                    type="button"
                    className="aurora-plan disabled:pointer-events-none disabled:opacity-50"
                    aria-label={t(($) => $.billing.topup.pack, {
                      credits: formatCredits(topup.credits, locale),
                      price: t(
                        ($) => $.billing.topup.packs[topupPackLabel(topup.id)].price,
                      ),
                    })}
                    aria-busy={topupCheckout.isPending}
                    disabled={topupCheckout.isPending || !returnURLs}
                    onClick={() => {
                      if (!returnURLs) return;
                      topupCheckout.mutate(
                        {
                          topupId: topup.id,
                          ...returnURLs,
                        },
                        {
                          onSuccess: (checkoutUrl) =>
                            openExternal(checkoutUrl, {
                              webTarget: "same-tab",
                            }),
                        },
                      );
                    }}
                  >
                    <strong>
                      {t(($) => $.credits, {
                        credits: formatCredits(topup.credits, locale),
                      })}
                    </strong>
                    <small>
                      {t(($) => $.billing.topup.packs[topupPackLabel(topup.id)].price)}
                    </small>
                  </button>
                ))}
              </div>
            )}
          </section>

          <section className="flex flex-col gap-2">
            <div className="aurora-section-head">
              <h2>{t(($) => $.billing.transactions_title)}</h2>
            </div>
            {transactions.length === 0 ? (
              <AuroraPageState
                icon={Wallet}
                title={t(($) => $.billing.transactions_empty_title)}
                description={t(
                  ($) => $.billing.transactions_empty_description,
                )}
              />
            ) : (
              <ul className="aurora-list">
                {transactions.map((transaction) => (
                  <TransactionRow
                    key={transaction.id}
                    transaction={transaction}
                    // A row the catalog can attribute names the skill; one it
                    // cannot shows only the kind, which is the same label the
                    // reader would have fallen back to anyway.
                    skillName={referenceSkillName(transaction)}
                    kindLabel={tBilling(
                      ($) =>
                        $.transaction.kind[
                          ledgerKindLabel(transaction.kind)
                        ],
                    )}
                  />
                ))}
              </ul>
            )}
          </section>
        </div>
      )}
    </div>
  );
}

/**
 * The plans the price table lists, in display order. The free tier is included
 * so the table reads as the whole menu — and it is the row a free user is
 * already on, which is why it carries no button.
 *
 * The ids are the API contract (`POST /api/aurora/billing/checkout` takes
 * `tier`); the prices and credit allowances beside them are copy, because the
 * server returns neither for a plan it has not sold.
 */
const TIER_ORDER = ["free", "creator", "pro"] as const;

/**
 * The plan card: which plan the caller is on, what it allows, how much of that
 * they have used, and — when they are not already subscribed — what the paid
 * plans cost.
 *
 * The tier named here is the one the server's gates apply, so the usage bar
 * under it is always measured against a limit that is really enforced. A
 * canceled plan therefore reads as the free tier with a canceled status rather
 * than as a plan the user no longer has.
 */
function PlanSection({
  subscription,
  pending,
  canCheckout,
  onSubscribe,
}: {
  subscription: AuroraSubscription;
  pending: boolean;
  canCheckout: boolean;
  onSubscribe: (tier: string) => void;
}) {
  const { t } = useT("aurora");
  const locale = useLocale();

  const used = subscription.usage.generationsUsedThisMonth;
  const limit = subscription.limits.generationsPerMonth;
  // A limit of zero would divide by zero. It is not a plan the catalog sells,
  // but a degraded read must not put NaN on screen.
  const percent = limit > 0 ? Math.min(100, (used / limit) * 100) : 0;
  const renewsOn = subscription.currentPeriodEnd
    ? formatDate(subscription.currentPeriodEnd, locale)
    : null;

  // A user without a subscription (empty status), or one whose prior plan is
  // canceled, may buy a plan. Unknown future states fail closed: an older
  // client must not offer a second recurring subscription it cannot interpret.
  const canSubscribe =
    subscription.status === "" || subscription.status === "canceled";

  return (
    <section className="aurora-panel flex flex-col gap-3">
      <div className="flex items-center justify-between gap-3">
        <div className="flex min-w-0 flex-col">
          <span className="aurora-eyebrow">
            {t(($) => $.billing.subscription.title)}
          </span>
          <span className="aurora-card-title">
            {t(
              ($) =>
                $.billing.subscription.tiers[subscriptionTierLabel(subscription.tier)]
                  .name,
            )}
          </span>
        </div>
        {subscription.status ? (
          <span className="shrink-0 text-caption text-muted-foreground">
            {t(
              ($) =>
                $.billing.subscription.status[
                  subscriptionStatusLabel(subscription.status)
                ],
            )}
          </span>
        ) : null}
      </div>

      {renewsOn &&
      subscription.status === "active" &&
      !subscription.cancelAtPeriodEnd ? (
        <span className="text-caption text-muted-foreground">
          {t(($) => $.billing.subscription.renews_on, { date: renewsOn })}
        </span>
      ) : null}
      {subscription.cancelAtPeriodEnd ? (
        <span className="text-caption text-muted-foreground">
          {t(($) => $.billing.subscription.cancel_scheduled)}
        </span>
      ) : null}

      <div className="flex flex-col gap-1">
        <Progress
          value={percent}
          aria-label={t(($) => $.billing.subscription.usage_label)}
        />
      </div>

      {canSubscribe ? (
        <ul className="flex flex-col">
          {TIER_ORDER.map((tier) => (
            <li
              key={tier}
              className="flex items-center justify-between gap-3 border-t border-surface-border py-2 first:border-t-0"
            >
              <div className="flex min-w-0 flex-col">
                <span className="truncate text-body">
                  {
                    t(
                      ($) =>
                        $.billing.subscription.tiers[subscriptionTierLabel(tier)].name,
                    )
                  }
                </span>
                <span className="truncate text-caption text-muted-foreground">
                  {
                    t(
                      ($) =>
                        $.billing.subscription.tiers[subscriptionTierLabel(tier)]
                          .summary,
                    )
                  }
                </span>
              </div>
              <div className="flex shrink-0 items-center gap-2">
                <span className="font-mono text-caption tabular-nums">
                  {
                    t(
                      ($) =>
                        $.billing.subscription.tiers[subscriptionTierLabel(tier)].price,
                    )
                  }
                </span>
                {tier === "free" ? null : (
                  <Button
                    type="button"
                    size="sm"
                    disabled={pending || !canCheckout}
                    onClick={() => onSubscribe(tier)}
                  >
                    {t(($) => $.billing.subscription.subscribe)}
                  </Button>
                )}
              </div>
            </li>
          ))}
        </ul>
      ) : null}
    </section>
  );
}

/**
 * A period end as a readable date.
 *
 * A value the browser cannot parse is shown as it arrived: the server sends
 * RFC3339, and replacing an unparseable one with today's date would tell the
 * user their plan renews on a date that is simply wrong.
 */
function formatDate(value: string, locale: string): string {
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) return value;
  return new Intl.DateTimeFormat(locale, { dateStyle: "medium" }).format(parsed);
}

function TransactionRow({
  transaction,
  skillName,
  kindLabel,
}: {
  transaction: AuroraTransaction;
  skillName: string | null;
  kindLabel: string;
}) {
  const { t } = useT("aurora");
  const locale = useLocale();
  const amount =
    transaction.amountMicro < 0
      ? t(($) => $.billing.amount_negative, {
          amount: formatMicroCredits(-transaction.amountMicro, locale),
        })
      : t(($) => $.billing.amount_positive, {
          amount: formatMicroCredits(transaction.amountMicro, locale),
        });

  return (
    <li className="aurora-row">
      <div className="aurora-row-main flex flex-col">
        <span className="truncate text-body">{kindLabel}</span>
        {skillName ? (
          <span className="truncate text-caption text-muted-foreground">
            {skillName}
          </span>
        ) : null}
      </div>
      <div className="flex shrink-0 flex-col items-end">
        <span className="aurora-row-amount">{amount}</span>
        <span className="font-mono text-caption tabular-nums text-muted-foreground">
          {t(($) => $.billing.balance_after, {
            amount: formatMicroCredits(transaction.balanceAfterMicro, locale),
          })}
        </span>
      </div>
    </li>
  );
}

function BillingSkeleton() {
  return (
    <div className="flex flex-col gap-3">
      <Skeleton className="h-24 w-full rounded-xl" />
      <Skeleton className="h-5 w-20" />
      <Skeleton className="h-24 w-full rounded-xl" />
    </div>
  );
}
