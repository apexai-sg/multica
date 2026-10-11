"use client";

import { ChevronRight, LogOut } from "lucide-react";
import {
  isAuroraDegraded,
  useAuroraBalance,
  useAuroraSubscription,
} from "@multica/core/aurora";
import { useAuthStore } from "@multica/core/auth";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { AppLink } from "../navigation";
import { useLocale, useT } from "../i18n";
import { useLogout } from "../auth";
import { avatarInitial } from "./aurora-avatar";
import { formatMicroCredits } from "./format";
import { subscriptionTierLabel } from "./labels";

/**
 * The account, on the two surfaces that show it: the sidebar panel and the
 * dialog behind its ⋯ button.
 *
 * Both share the same identity. The panel's unread wallet prints an unknown
 * marker, never a zero that could send the reader to checkout unnecessarily.
 */

/** A glyph, not copy: it stands in for a number the client has not read. */
const UNKNOWN = "—";

function useAccountIdentity() {
  const user = useAuthStore((state) => state.user);
  const name = user?.name?.trim() || user?.email || "";
  return { name, email: user?.email ?? "" };
}

export function AuroraAccountPanel({
  creditsHref,
  onOpenAccount,
}: {
  creditsHref: string;
  onOpenAccount: () => void;
}) {
  const { t } = useT("aurora");
  const locale = useLocale();
  const { name } = useAccountIdentity();
  const balanceQuery = useAuroraBalance();
  const subscriptionQuery = useAuroraSubscription();

  const balance = balanceQuery.data?.value;
  const balanceReadable =
    balance !== undefined &&
    !balanceQuery.isPending &&
    !balanceQuery.isError &&
    !isAuroraDegraded(balanceQuery.data);
  const subscription =
    subscriptionQuery.isPending || subscriptionQuery.isError
      ? undefined
      : subscriptionQuery.data;
  const used = subscription?.usage.generationsUsedThisMonth;
  const limit = subscription?.limits.generationsPerMonth;
  const quotaReadable =
    limit !== undefined &&
    used !== undefined &&
    Number.isFinite(limit) &&
    Number.isFinite(used) &&
    limit >= 0 && used >= 0;
  const percent =
    quotaReadable && limit > 0 ? Math.min(100, (used / limit) * 100) : 0;

  return (
    <section className="aurora-user-panel">
      <div className="aurora-user-row">
        <span className="aurora-user-avatar" aria-hidden="true">
          {avatarInitial(name)}
        </span>
        <div className="aurora-user-copy">
          <strong>{name}</strong>
          {subscription ? (
            <span>
              {t(
                ($) =>
                  $.billing.subscription.tiers[
                    subscriptionTierLabel(subscription.tier)
                  ].name,
              )}
            </span>
          ) : null}
        </div>
        <button
          type="button"
          className="aurora-more-button"
          aria-label={t(($) => $.shell.account_menu)}
          onClick={onOpenAccount}
        >
          <span aria-hidden="true">•••</span>
        </button>
      </div>

      <div className="aurora-token-row">
        <span>{t(($) => $.billing.balance_title)}</span>
        <strong>
          {balanceReadable
            ? formatMicroCredits(balance.availableMicro, locale)
            : UNKNOWN}
        </strong>
      </div>

      {/* The meter measures the monthly generation allowance, not the wallet:
          credits are the reference's decorative bar there, while the allowance
          is the number that has a real denominator on this API. */}
      {quotaReadable ? (
        <>
          <div className="aurora-token-row">
            <span>{t(($) => $.billing.subscription.usage_label)}</span>
            <strong>
              {t(($) => $.billing.subscription.usage_value, {
                used,
                limit,
              })}
            </strong>
          </div>
          {limit > 0 ? (
            <div
              className="aurora-meter"
              role="progressbar"
              aria-valuemin={0}
              aria-valuemax={limit}
              aria-valuenow={Math.min(used, limit)}
              aria-valuetext={t(($) => $.billing.subscription.usage_value, {
                used,
                limit,
              })}
              aria-label={t(($) => $.billing.subscription.usage_label)}
            >
              <span
                className="aurora-meter-fill"
                style={{ width: percent + "%" }}
              />
            </div>
          ) : null}
        </>
      ) : null}

      <AppLink href={creditsHref} className="aurora-recharge-button">
        {t(($) => $.billing.top_up)}
      </AppLink>
    </section>
  );
}

export function AuroraAccountDialog({
  open,
  onOpenChange,
  creditsHref,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  creditsHref: string;
}) {
  const { t } = useT("aurora");
  const { t: tLayout } = useT("layout");
  const { name, email } = useAccountIdentity();
  const logout = useLogout();

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="aurora-dialog aurora-dialog--account">
        <div className="aurora-modal-hero">
          <span className="aurora-user-avatar" aria-hidden="true">
            {avatarInitial(name)}
          </span>
          {/* The identity is the heading: a title that said "Account" would
              name the surface, not the person on it. */}
          <DialogTitle>{name || t(($) => $.shell.account_menu)}</DialogTitle>
          {email ? <DialogDescription>{email}</DialogDescription> : null}
        </div>
        <div className="aurora-modal-actions">
          <AppLink href={creditsHref}>
            {t(($) => $.billing.title)}
            <ChevronRight aria-hidden="true" className="size-4" />
          </AppLink>
          <button
            type="button"
            onClick={() => {
              onOpenChange(false);
              logout();
            }}
          >
            {tLayout(($) => $.sidebar.log_out)}
            <LogOut aria-hidden="true" className="size-4" />
          </button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
