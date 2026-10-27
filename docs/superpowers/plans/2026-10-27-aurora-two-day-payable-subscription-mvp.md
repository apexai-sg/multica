# Aurora Two-Day Payable-Subscription MVP

> **For agentic workers:** this is a prioritised execution plan, not a TDD implementation plan. Each task has a verifiable outcome and an acceptance check. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Let a real user subscribe or buy a top-up, receive credits, and spend them on a text generation — live, within two days. Taking money is the first deliverable; everything else is ranked below it.

**Architecture:** Reuse what is already merged (Plan 5): Stripe Checkout for subscriptions and top-ups, the signed webhook at `POST /api/aurora/billing/stripe/webhook`, monthly credit settlement, and the flat per-skill credit prices the catalog already carries (60–120 credits). Do **not** bring in the metered rate card from PR #38 for this launch: it changes settlement, adds six migrations and a quote endpoint, and buys nothing for the first payment. Text runs on `deepseek-v4.1-flash-1017` if the provider route clears a half-day spike; otherwise the launch ships on the current Claude route and the model switch follows as its own change.

**Tech Stack:** Go 1.26.9 (Chi, sqlc, pgx), PostgreSQL, Stripe (Checkout, webhook, Customer Portal), Next.js `apps/aurora` + `packages/views/aurora`, the existing AWS deployment path.

**Spec:** this plan. Related background: `docs/superpowers/specs/2026-10-25-aurora-credit-billing-redesign-design.md` (phase 2), `docs/superpowers/plans/2026-09-13-aurora-subscriptions-payments.md` (the implementation this plan operates).

## Decisions this plan takes

| Question | Decision for the MVP | Why |
|---|---|---|
| Billing model | Flat catalog prices (60–100 credits per text generation) | Already implemented and tested; metering is a phase-2 upgrade |
| Sellable scope | Text deliverables only | Every catalog expert is `text`-first; media rendering still depends on unconfigured providers |
| Text model | `deepseek-v4.1-flash-1017`, behind a timeboxed spike | Owner direction; at 60+ credits per run the cheapest model is pure margin |
| Plans | The three implemented tiers: free 200 credits / 10 runs, creator 3,000, pro 12,000; top-ups 5,000 / 20,000 | `server/internal/aurora/tiers.go` is the single edit point; changing grants is not a 2-day item |
| Currency and price points | **Owner decision, frozen at Day-1 09:00** — the UI copy currently advertises $9.90 and $29 while the docs propose S$12/S$89/S$249 | The Stripe price, the UI copy and the granted credits must agree before checkout is switched on |
| Annual billing, signup trial, plan restructuring | Not in the MVP | Each is a pricing decision with its own analysis |

**Margin sanity check for flat pricing.** Text experts bill 60–100 credits (S$0.60–1.00; the 120-credit experts are the media ones and are out of MVP scope). A typical text run (≈30K input + 10K output) costs ≈US$0.012 ≈ S$0.016 on DeepSeek at US$0.176/0.706 per M — a 97% margin at the 60-credit floor. Even a run ten times that size (≈300K in + 100K out, ≈S$0.16) still clears ~74%. The MVP therefore does not need metering to be profitable; it needs the payment path to work, and phase 2 exists for the pathological runs.

## Global Constraints

- Do not change the tier catalog's credit grants, the catalog's per-skill credit prices, or the database schema for this launch.
- Prices are decided once (Day 1, 09:00) and then aligned in three places: the Stripe price, `packages/views/locales/*/aurora.json` (`billing.subscription.tiers`, `billing.topup.packs`), and any page that quotes a price.
- Fail closed and loudly: unconfigured payments return 503, insufficient balance returns 402 with the shortfall, a failed webhook is never acked.
- Production deployment uses the existing multica deploy path; no new AWS resource is created for this launch.
- No development on `main`; docs and code changes ship on a branch.

## Review Focus

1. **Paid but no credits.** A successful Checkout whose webhook never arrives leaves the user charged and empty. The launch must have a provider-side replay path and a manual grant runbook. (T3, T10)
2. **Credits granted twice.** Stripe redelivers events; grants key on the Checkout Session id or the subscription month. Replaying the same event must be a no-op. (T4)
3. **UI price ≠ charged price.** The plan copy is hardcoded; the charge comes from the Stripe price id. One mismatch is a refund-and-apology incident. (T5)
4. **Subscription active but the app still shows free.** The webhook writes the subscription row; a stale client read must refresh within one refetch, and the tier gate must read the row, not a cache. (T6)
5. **Generation fails after reserving.** The failure path must refund the reservation; a retried terminal callback must not refund twice. (T4)
6. **Free-tier abuse.** Multiple accounts or workspaces each get 200 credits per month. Accepted for launch; record it, do not engineer around it in two days. (T9)

## Priorities

### P0 — without these there is no payment (Day 1)

| # | Task | Owner | Est. | Depends on | Done when |
|---|---|---|---|---|---|
| T1 | Create the Stripe products and prices: creator monthly, pro monthly, top-up 5, top-up 20 (skip yearly for now) | Owner | 1h | price/currency decision | Four price ids exist in the live account and each is recorded in the launch note |
| T2 | Inject `STRIPE_SECRET_KEY`, `STRIPE_WEBHOOK_SECRET`, `AURORA_STRIPE_PRICE_CREATOR_MONTHLY`, `AURORA_STRIPE_PRICE_PRO_MONTHLY`, `AURORA_STRIPE_PRICE_TOPUP_5`, `AURORA_STRIPE_PRICE_TOPUP_20` into the production environment and redeploy | Ops | 1h | T1 | `POST /api/aurora/billing/checkout` returns a Stripe URL instead of 503 `payments not configured` |
| T3 | Make the webhook reachable and signed: public route, no auth, WAF/ALB allows Stripe; register the endpoint in Stripe | Ops | 1h | T2 | A Stripe dashboard test event returns 200 and no "invalid signature" in logs |
| T4 | Test-mode end-to-end: subscribe → credits granted → generate → charge → refund on failure | Eng | 2h | T2, T3 | The four-step acceptance below passes with test keys |
| T5 | Align the price and credit copy with the Stripe prices in every locale | Eng | 1h | T1 | A reviewer can read the UI price and the Stripe price and find them identical |
| T6 | Verify entitlement: a new subscription raises the tier and the monthly grant; cancel/past-due falls back to free | Eng | 1h | T4 | Billing page shows the paid tier after purchase; deleting the subscription in Stripe shows free |

**Day-1 gate (18:00):** a test-mode purchase credits the account and a generation debits it. If this is red, Day 2 is spent fixing it, not on DeepSeek.

### P1 — ship-quality, Day 2

| # | Task | Owner | Est. | Depends on | Done when |
|---|---|---|---|---|---|
| T7 | DeepSeek route spike: run one text expert end-to-end on `deepseek-v4.1-flash-1017` | Eng | 2h (hard timebox) | — | Either an artifact produced by that model, or a written blocker and a decision to launch on Claude |
| T8 | Cost check: token cost per text run against its credit price, at the frozen FX | Eng | 1h | T7 (or existing runs) | A number in the launch note; margin >75% |
| T9 | Cancellation path: a "manage subscription" entry that opens the Stripe Customer Portal, or a documented ops SOP if the endpoint is not ready | Eng | 2h | T4 | A subscriber can cancel without contacting support |
| T10 | Refund / chargeback runbook: how to refund in Stripe, how to adjust the ledger, what the user sees | Ops | 1h | T4 | Runbook checked into the launch note |
| T11 | Alerts on webhook 5xx, settlement failures and 402 rate | Eng | 1h | T3 | An alert fires when the webhook is forced to fail |
| T12 | Live smoke test on the real account: one small top-up with a real card, then refund | Owner + Ops | 1h | T1–T4 | Money moved and was returned; ledger balanced |

### P2 — explicitly out (do not start)

- The metered rate card, quote endpoint, `aurora_generation_charge` and SKU pricing from PR #38.
- Image / video / PDF deliverables and their provider costs.
- Annual billing, signup trial, plan re-pricing (S$49/S$149 discussion), multi-currency.
- Self-service plan switching, invoices/tax automation, dunning.

## Acceptance checklist

A launch is green only when every line is observed, not inferred:

- [ ] A new user lands on `/{workspaceSlug}/billing` and sees the free tier's 200 credits and 10-run limit.
- [ ] Subscribing with a test card returns to `?checkout=success`, the page shows the paid tier within one refresh, and the wallet gains the tier's monthly credits exactly once.
- [ ] A top-up purchase adds exactly its credits once; replaying the webhook adds nothing.
- [ ] A text generation debits 60–100 credits, settles, and the ledger shows one line with the skill name.
- [ ] A failed generation refunds the reservation; a replayed terminal callback refunds nothing further.
- [ ] With the balance at zero, create returns 402 with `requiredCredits` / `availableCredits` / `shortfallCredits`, and the UI shows the shortfall.
- [ ] Deleting the subscription in Stripe flips the app to the free tier on the next read.
- [ ] Unsetting `STRIPE_SECRET_KEY` makes checkout answer 503, not a broken button.
- [ ] The webhook answers 200 to a Stripe test event from the dashboard and logs no signature error.
- [ ] A text generation on the chosen model produces its artifact end-to-end.

## Two-day schedule

**Day 1**

| Time | Work | Checkpoint |
|---|---|---|
| 09:00 | Freeze price, currency and plan grants; write them into the launch note | Decision recorded |
| 09:30 | T1 (Stripe products/prices), T5 (copy alignment) in parallel with T2 (env) | Price ids exist |
| 11:00 | T3 webhook reachability; T4 test-mode purchase | Test purchase completes |
| 14:00 | T4 continues: grant idempotency, generation charge, failure refund | Ledger balanced |
| 16:00 | T6 entitlement read-back | Tier flips both ways |
| 18:00 | **Day-1 gate** | Green or Day 2 is refunded to fixing it |

**Day 2**

| Time | Work | Checkpoint |
|---|---|---|
| 09:00 | T7 DeepSeek spike (hard stop at 11:00) | Route works, or launch on Claude |
| 11:00 | T8 cost check; T9 cancellation path | Numbers + cancel path |
| 14:00 | T10 runbook, T11 alerts | Failure paths covered |
| 16:00 | T12 live smoke with a real card and immediate refund | Money round-trips |
| 18:00 | Launch review against the acceptance checklist; switch Stripe to live mode | Launch or a written go/no-go |

## Risks

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Stripe account review / business verification delays live mode | Medium | Blocks launch | Start verification on Day 1 morning; test mode proves the product meanwhile |
| Webhook blocked by WAF or the wrong host | Medium | Paid users get nothing | T3 is a P0; verify from the Stripe dashboard, not by curl alone |
| DeepSeek provider route unavailable | Medium | Text runs on Claude | Hard timebox; launch does not depend on it |
| Price copy and Stripe price drift | High if unmanaged | Wrong-amount charges | One frozen decision, one reviewer, one checklist line |
| No cancellation UI | High (today) | Support burden, refund requests | T9 portal link; SOP if the link slips |
| Free-tier multi-account abuse | Certain | Small credit leakage | Accepted for launch; measure and revisit |

## Open owner decisions (needed by Day-1 09:00)

1. **Price and currency** — keep the UI's $9.90 / $29, adopt S$12 / S$89 / S$249, or something else. One answer; the plan copy and the Stripe prices follow it.
2. **Refund policy** — full refund within N days, or case-by-case.
3. **DeepSeek credentials** — which endpoint and key serve `deepseek-v4.1-flash-1017` (the HopBase quote is a cost basis, not necessarily a runtime route).
