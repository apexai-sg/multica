import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useWorkspaceId } from "../hooks";
import {
  createAuroraBillingPortal,
  createAuroraCheckout,
  createAuroraGeneration,
  createAuroraTopupCheckout,
  deleteAuroraAsset,
} from "./api";
import { auroraKeys, auroraWalletKeys } from "./queries";
import type {
  CreateAuroraBillingPortalRequest,
  CreateAuroraCheckoutRequest,
  CreateAuroraGenerationRequest,
  CreateAuroraTopupCheckoutRequest,
} from "./types";

/**
 * Enqueues one generation.
 *
 * The create call reserves the skill's credits before it returns, so the
 * balance on screen is stale the moment it settles — and it is just as stale
 * when the call is rejected: a 402 means the wallet was already short when the
 * request left, and the number the user is looking at was read earlier still.
 * That is why the wallet is invalidated on settle rather than on success.
 *
 * The 402 itself is left to the caller: `isAuroraInsufficientCreditsError`
 * (./api) tells the composer to show the top-up prompt. Nothing is rolled back
 * here because nothing was applied optimistically — a generation is created by
 * the server, not by the screen.
 */
export function useCreateAuroraGeneration() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (request: CreateAuroraGenerationRequest) =>
      createAuroraGeneration(request),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: auroraWalletKeys.all() });
      // Covers both the generation list and any cached detail, since a
      // generation's status and assets move together.
      qc.invalidateQueries({ queryKey: auroraKeys.generations(wsId) });
    },
  });
}

/**
 * Removes one asset from the library.
 *
 * Not optimistic, for the reason CLAUDE.md gives for deletes generally and a
 * second one specific to assets: the server also drops the stored object, so a
 * row rolled back into the list after a failed delete would offer the user a
 * file that is already gone. The row disappears when the server confirms it.
 */
export function useDeleteAuroraAsset() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (assetId: string) => deleteAuroraAsset(assetId),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: auroraKeys.assets(wsId) });
      // The same asset is listed inside its generation's detail.
      qc.invalidateQueries({ queryKey: auroraKeys.generations(wsId) });
    },
  });
}

/**
 * Starts a subscription checkout.
 *
 * Core returns the validated checkout URL but deliberately does not navigate:
 * external navigation is platform behavior (web same-tab vs Electron shell),
 * so the view hands this result to its platform adapter.
 *
 * On success the wallet and the plan are invalidated. The purchase completes on
 * Stripe's origin, so this client will not see the webhook that grants the
 * credits — the invalidation is what makes the return trip re-read both.
 *
 * A 409 (a live subscription already exists) and a 503 (no Stripe
 * configuration) are left to the caller: neither is retryable, and the screen
 * has copy for both.
 */
export function useCreateAuroraCheckout() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (request: CreateAuroraCheckoutRequest) =>
      createAuroraCheckout(request),
    onSettled: () => {
      // The plan the card names, and the wallet — the webhook grants the first
      // month's credits, so both move. The pack catalogue does not, so it is
      // left alone.
      qc.invalidateQueries({ queryKey: auroraWalletKeys.subscription() });
      qc.invalidateQueries({ queryKey: auroraWalletKeys.balance() });
    },
  });
}

/** Starts a one-time credit purchase. Same shape as the subscription one. */
export function useCreateAuroraTopupCheckout() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (request: CreateAuroraTopupCheckoutRequest) =>
      createAuroraTopupCheckout(request),
    onSettled: () => {
      // Only the wallet moves: a top-up buys credits, not a plan.
      qc.invalidateQueries({ queryKey: auroraWalletKeys.balance() });
      qc.invalidateQueries({ queryKey: auroraWalletKeys.transactions() });
    },
  });
}

/**
 * Opens the Stripe Customer Portal, where a subscriber cancels or changes their
 * payment method.
 *
 * Core returns the validated portal URL but does not navigate, exactly like the
 * checkout mutations: external navigation is platform behavior, so the view
 * hands it to its platform adapter. Stripe's cancellation webhook can land
 * while the portal is open, so creating the session invalidates the plan and
 * the wallet. That invalidation does not itself refetch anything: the fresh
 * read comes from the full navigation back to the app, which remounts the
 * billing view.
 *
 * A 409 (no subscription to manage) and a 503 (no Stripe configuration) are
 * left to the caller; neither is retryable.
 */
export function useCreateAuroraBillingPortal() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (request: CreateAuroraBillingPortalRequest) =>
      createAuroraBillingPortal(request),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: auroraWalletKeys.subscription() });
      qc.invalidateQueries({ queryKey: auroraWalletKeys.balance() });
    },
  });
}
