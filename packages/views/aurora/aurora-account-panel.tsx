"use client";

import { ChevronRight, LogOut } from "lucide-react";
import type { ReactNode } from "react";
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
 * The account, on the surfaces that show it: the compact sidebar panel and the
 * dialog behind that panel's ⋯ button.
 *
 * They share one identity and one wallet read. An unread wallet prints an
 * unknown marker, never a zero that could send the reader to checkout
 * unnecessarily.
 */

/** A glyph, not copy: it stands in for a number the client has not read. */
const UNKNOWN = "—";

type AccountQuota = {
  used: number;
  limit: number;
  percent: number;
};

function useAccountIdentity() {
  const user = useAuthStore((state) => state.user);
  const name = user?.name?.trim() || user?.email || "";
  return { name, email: user?.email ?? "" };
}

function useAccountSnapshot() {
  const { t } = useT("aurora");
  const locale = useLocale();
  const identity = useAccountIdentity();
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
    limit >= 0 &&
    used >= 0;

  return {
    ...identity,
    tierName: subscription
      ? t(
          ($) =>
            $.billing.subscription.tiers[
              subscriptionTierLabel(subscription.tier)
            ].name,
        )
      : null,
    balanceText: balanceReadable
      ? formatMicroCredits(balance.availableMicro, locale)
      : UNKNOWN,
    quota: quotaReadable
      ? {
          used,
          limit,
          percent: limit > 0 ? Math.min(100, (used / limit) * 100) : 0,
        }
      : null,
  };
}

function AccountIdentity({
  name,
  tierName,
  title,
}: {
  name: string;
  tierName: string | null;
  /** Replaces the plain name when the row has to name a dialog or popover. */
  title?: ReactNode;
}) {
  return (
    <>
      <span className="aurora-user-avatar" aria-hidden="true">
        {avatarInitial(name)}
      </span>
      <div className="aurora-user-copy">
        {title ?? <strong>{name}</strong>}
        {tierName ? <span>{tierName}</span> : null}
      </div>
    </>
  );
}

/**
 * Wallet, allowance and the way to buy more. The meter measures the monthly
 * generation allowance, not the wallet: credits are the reference's decorative
 * bar there, while the allowance is the number that has a real denominator.
 */
function AccountWallet({
  creditsHref,
  balanceText,
  quota,
}: {
  creditsHref: string;
  balanceText: string;
  quota: AccountQuota | null;
}) {
  const { t } = useT("aurora");
  return (
    <>
      <div className="aurora-token-row">
        <span>{t(($) => $.billing.balance_title)}</span>
        <strong>{balanceText}</strong>
      </div>
      {quota ? (
        <>
          <div className="aurora-token-row">
            <span>{t(($) => $.billing.subscription.usage_label)}</span>
            <strong>
              {t(($) => $.billing.subscription.usage_value, {
                used: quota.used,
                limit: quota.limit,
              })}
            </strong>
          </div>
          {quota.limit > 0 ? (
            <div
              className="aurora-meter"
              role="progressbar"
              aria-valuemin={0}
              aria-valuemax={quota.limit}
              aria-valuenow={Math.min(quota.used, quota.limit)}
              aria-valuetext={t(($) => $.billing.subscription.usage_value, {
                used: quota.used,
                limit: quota.limit,
              })}
              aria-label={t(($) => $.billing.subscription.usage_label)}
            >
              <span
                className="aurora-meter-fill"
                style={{ width: quota.percent + "%" }}
              />
            </div>
          ) : null}
        </>
      ) : null}
      <AppLink href={creditsHref} className="aurora-recharge-button">
        {t(($) => $.billing.top_up)}
      </AppLink>
    </>
  );
}

export function AuroraAccountPanel({
  creditsHref,
  onOpenAccount,
}: {
  creditsHref: string;
  onOpenAccount: () => void;
}) {
  const { t } = useT("aurora");
  const { name, tierName, balanceText, quota } = useAccountSnapshot();

  return (
    <section className="aurora-user-panel">
      <div className="aurora-user-row">
        <AccountIdentity name={name} tierName={tierName} />
        <button
          type="button"
          className="aurora-more-button"
          aria-label={t(($) => $.shell.account_menu)}
          onClick={onOpenAccount}
        >
          <span aria-hidden="true">•••</span>
        </button>
      </div>
      <AccountWallet
        creditsHref={creditsHref}
        balanceText={balanceText}
        quota={quota}
      />
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
