# Aurora Two-Day Payable-Subscription MVP

> **For agentic workers:** this is a prioritised execution plan, not a TDD implementation plan. Each task has a verifiable outcome and an acceptance check. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Let a real user subscribe or buy a top-up, receive credits, and spend them on a text generation — live, within two days. Taking money is the first deliverable; everything else is ranked below it.

**Architecture:** Reuse what is already merged (Plan 5): Stripe Checkout for subscriptions and top-ups, the signed webhook at `POST /api/aurora/billing/stripe/webhook`, monthly credit settlement, and the flat per-skill credit prices the catalog already carries (60–100 credits for text). Do **not** bring in the metered rate card from PR #38 for this launch: it changes settlement, adds six migrations and a quote endpoint, and buys nothing for the first payment. Text runs on the current Claude route; `deepseek-v4.1-flash-1017` is a follow-up change once credentials arrive.

**Tech Stack:** Go 1.26.9 (Chi, sqlc, pgx), PostgreSQL, Stripe (Checkout, webhook, Customer Portal), Next.js `apps/aurora` + `packages/views/aurora`, the existing AWS deployment path.

**Spec:** this plan. Related background: `docs/superpowers/specs/2026-10-25-aurora-credit-billing-redesign-design.md` (phase 2), `docs/superpowers/plans/2026-09-13-aurora-subscriptions-payments.md` (the implementation this plan operates).

## Decisions this plan takes

| Question | Decision for the MVP | Why |
|---|---|---|
| Billing model | Flat catalog prices (60–100 credits per text generation) | Already implemented and tested; metering is a phase-2 upgrade |
| Sellable scope | Text deliverables only | Every catalog expert is `text`-first; media rendering still depends on unconfigured providers |
| Text model | Launch on the current Claude route; switch to `deepseek-v4.1-flash-1017` when the owner supplies credentials | The switch is not on the payment critical path, and the credentials are not available yet |
| Plans | Free 200 credits / 10 runs, then **S$19 / 1,900**, **S$59 / 5,900**, **S$169 / 16,900** | Owner decision 2026-10-27: three paid tiers at face value (100 credits = S$1), so no plan discounts credits. `tiers.go` still holds the old free/creator/pro rows (200 / 3,000 / 12,000) and must change |
| Refunds | **None** — purchases are final; terms say so before checkout is enabled | Owner decision 2026-10-27. Settlement still releases an unused or failed reservation; that is not a money refund |
| Annual billing, signup trial, fourth tier | Not in the MVP | Each is a pricing decision with its own analysis |

**Margin sanity check for flat pricing.** Text experts bill 60–100 credits (S$0.60–1.00; the 120-credit experts are the media ones and are out of MVP scope). A typical text run (≈30K input + 10K output) costs ≈US$0.012 ≈ S$0.016 on DeepSeek at US$0.176/0.706 per M — a 97% margin at the 60-credit floor. Even a run ten times that size (≈300K in + 100K out, ≈S$0.16) still clears ~74%. The MVP therefore does not need metering to be profitable; it needs the payment path to work, and phase 2 exists for the pathological runs.

## Global Constraints

- The tier catalog changes in this launch: three paid tiers at S$19 / S$59 / S$169 granting 1,900 / 5,900 / 16,900 credits. Do not change the catalog's per-skill credit prices (60–100 for text) or the database schema.
- Prices are decided (2026-10-27) and must agree in three places: the Stripe price, `packages/views/locales/*/aurora.json` (`billing.subscription.tiers`, `billing.topup.packs`), and `server/internal/aurora/tiers.go` grants. Top-ups stay at 100 credits per SGD.
- No refunds: the checkout page and the terms state that all purchases are final. Do not build a refund path; the settlement's release of an unused reservation is separate and unchanged.
- Fail closed and loudly: unconfigured payments return 503, insufficient balance returns 402 with the shortfall, a failed webhook is never acked.
- Production deployment uses the existing multica deploy path; no new AWS resource is created for this launch.
- No development on `main`; docs and code changes ship on a branch.

## Review Focus

1. **Paid but no credits.** A successful Checkout whose webhook never arrives leaves the user charged and empty. The launch must have a provider-side replay path and a manual grant runbook. (T3, T10)
2. **Credits granted twice.** Stripe redelivers events; grants key on the Checkout Session id or the subscription month. Replaying the same event must be a no-op. (T4)
3. **UI price ≠ charged price.** The plan copy is hardcoded; the charge comes from the Stripe price id. One mismatch means charging the wrong amount and apologising, since purchases are final. (T5)
4. **Subscription active but the app still shows free.** The webhook writes the subscription row; a stale client read must refresh within one refetch, and the tier gate must read the row, not a cache. (T6)
5. **Generation fails after reserving.** The failure path must release the reservation back to the wallet; a retried terminal callback must not release it twice. (T4)
6. **Free-tier abuse.** Multiple accounts or workspaces each get 200 credits per month. Accepted for launch; record it, do not engineer around it in two days. (Risks)

## Priorities

### P0 — without these there is no payment (Day 1)

| # | Task | Owner | Est. | Depends on | Done when |
|---|---|---|---|---|---|
| T0 | Rewrite the tier catalog: `creator` = S$19/1,900, `pro` = S$59/5,900, new `business` = S$169/16,900 (keep the existing tier ids so no stored row is renamed); add the third monthly price env and its wiring; extend the client tier order and copy | Eng | 3h | price decision (made) | `go test ./internal/aurora` passes; the billing page lists three paid plans with the new prices |
| T1 | Create the Stripe products and prices: creator monthly, pro monthly, business monthly, top-up 5, top-up 20 (skip yearly) | Owner | 1h | T0 amounts | Five price ids exist in the live account and are recorded in the launch note |
| T2 | Inject `STRIPE_SECRET_KEY`, `STRIPE_WEBHOOK_SECRET`, `AURORA_STRIPE_PRICE_CREATOR_MONTHLY`, `AURORA_STRIPE_PRICE_PRO_MONTHLY`, `AURORA_STRIPE_PRICE_BUSINESS_MONTHLY`, `AURORA_STRIPE_PRICE_TOPUP_5`, `AURORA_STRIPE_PRICE_TOPUP_20` into the production environment and redeploy | Ops | 1h | T1 | `POST /api/aurora/billing/checkout` returns a Stripe URL instead of 503 `payments not configured` |
| T3 | Make the webhook reachable and signed: public route, no auth, WAF/ALB allows Stripe; register the endpoint in Stripe | Ops | 1h | T2 | A Stripe dashboard test event returns 200 and no "invalid signature" in logs |
| T4 | Test-mode end-to-end: subscribe → credits granted → generate → charge → release on failure | Eng | 2h | T2, T3 | The acceptance checklist below passes with test keys |
| T5 | Align every locale's price and credit copy with the Stripe prices (19 / 59 / 169, 1,900 / 5,900 / 16,900) and state the no-refund terms beside checkout | Eng | 1h | T0, T1 | A reviewer can read the UI price and the Stripe price and find them identical |
| T6 | Verify entitlement: a new subscription raises the tier and the monthly grant; cancel/past-due falls back to free | Eng | 1h | T4 | Billing page shows the paid tier after purchase; deleting the subscription in Stripe shows free |

**Day-1 gate (18:00):** a test-mode purchase credits the account and a generation debits it. If this is red, Day 2 is spent fixing it, not on DeepSeek.

### P1 — ship-quality, Day 2

| # | Task | Owner | Est. | Depends on | Done when |
|---|---|---|---|---|---|
| T7 | DeepSeek route spike — **blocked until the owner supplies credentials; skip if they do not arrive** | Eng | 2h (hard timebox) | credentials | Either an artifact produced by `deepseek-v4.1-flash-1017`, or a written blocker and the decision to stay on Claude for launch |
| T8 | Cost check: token cost per text run against its credit price, at the frozen FX | Eng | 1h | existing runs | A number in the launch note; margin >75% |
| T9 | Cancellation path: a "manage subscription" entry that opens the Stripe Customer Portal, or a documented ops SOP if the endpoint is not ready | Eng | 2h | T4 | A subscriber can cancel without contacting support |
| T10 | No-refund operations: terms copy, how support answers a refund request, and how a chargeback is disputed and recorded | Ops | 1h | T4 | Runbook checked into the launch note; the terms line is live on the checkout page |
| T11 | Alerts on webhook 5xx, settlement failures and 402 rate | Eng | 1h | T3 | An alert fires when the webhook is forced to fail |
| T12 | Live smoke test on the real account: one small top-up with a real card on an internal account (keep the credits; this is not a customer refund) | Owner + Ops | 1h | T1–T4 | Money moved into the live account; ledger balanced |

### P2 — explicitly out (do not start)

- The metered rate card, quote endpoint, `aurora_generation_charge` and SKU pricing from PR #38.
- Image / video / PDF deliverables and their provider costs.
- Annual billing, signup trial, multi-currency. Plan re-pricing is settled (S$19/59/169, 2026-10-27) and is not reopened here.
- Self-service plan switching, invoices/tax automation, dunning.

## Acceptance checklist

A launch is green only when every line is observed, not inferred:

- [ ] A new user lands on `/{workspaceSlug}/billing` and sees the free tier's 200 credits and 10-run limit.
- [ ] Subscribing with a test card returns to `?checkout=success`, the page shows the paid tier within one refresh, and the wallet gains the tier's monthly credits exactly once.
- [ ] A top-up purchase adds exactly its credits once; replaying the webhook adds nothing.
- [ ] A text generation debits 60–100 credits, settles, and the ledger shows one line with the skill name.
- [ ] A failed generation releases its reservation; a replayed terminal callback releases nothing further.
- [ ] The checkout page states that all purchases are final before the pay button, and support has the matching runbook.
- [ ] With the balance at zero, create returns 402 with `requiredCredits` / `availableCredits` / `shortfallCredits`, and the UI shows the shortfall.
- [ ] Deleting the subscription in Stripe flips the app to the free tier on the next read.
- [ ] Unsetting `STRIPE_SECRET_KEY` makes checkout answer 503, not a broken button.
- [ ] The webhook answers 200 to a Stripe test event from the dashboard and logs no signature error.
- [ ] A text generation on the chosen model produces its artifact end-to-end.

## Two-day schedule

**Day 1**

| Time | Work | Checkpoint |
|---|---|---|
| 09:00 | T0 tier catalog + T1 Stripe products (parallel) | Three plans exist in code and in Stripe |
| 11:00 | T2 env and redeploy; T3 webhook reachability | Checkout returns a URL |
| 13:00 | T5 copy and no-refund terms; T4 test-mode purchase | Test purchase completes |
| 15:00 | T4 continues: grant idempotency, generation charge, failure release | Ledger balanced |
| 16:30 | T6 entitlement read-back | Tier flips both ways |
| 18:00 | **Day-1 gate** | Green or Day 2 goes to fixing it |

**Day 2**

| Time | Work | Checkpoint |
|---|---|---|
| 09:00 | T7 DeepSeek spike **only if credentials arrived** (hard stop at 11:00); otherwise T8 cost check and T9 cancellation | Route verified or launch stays on Claude |
| 11:00 | T10 no-refund runbook; T11 alerts | Failure paths covered |
| 14:00 | T12 live smoke on an internal account | Money round-trips into the live account |
| 16:00 | Launch review against the acceptance checklist; switch Stripe to live mode | Launch or a written go/no-go |

## Risks

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Stripe account review / business verification delays live mode | Medium | Blocks launch | Start verification on Day 1 morning; test mode proves the product meanwhile |
| Webhook blocked by WAF or the wrong host | Medium | Paid users get nothing | T3 is a P0; verify from the Stripe dashboard, not by curl alone |
| DeepSeek provider route unavailable (credentials not supplied yet) | High | None for launch | Launch on Claude; the model switch lands as its own change when credentials arrive |
| Price copy and Stripe price drift | High if unmanaged | Wrong-amount charges | One frozen decision, one reviewer, one checklist line |
| No cancellation UI | High (today) | Support burden, refund requests | T9 portal link; SOP if the link slips |
| Free-tier multi-account abuse | Certain | Small credit leakage | Accepted for launch; measure and revisit |

## Decisions and what remains open

**Resolved (owner, 2026-10-27):**

1. **Prices and credits** — S$19 / S$59 / S$169, granting 1,900 / 5,900 / 16,900 credits at face value; three paid tiers plus the free tier.
2. **Refunds** — none; purchases are final, stated before checkout.
3. **Launch model** — the current Claude route; DeepSeek `deepseek-v4.1-flash-1017` follows when credentials arrive.

**Still to confirm:**

- **Currency** — the figures assume SGD (the docs' `S$`). If the Stripe account bills in USD, use the same numbers as USD and re-read the margin table in the audit, which is FX-sensitive.
- **DeepSeek credentials** — endpoint and key arrive later; nothing in the launch depends on them.
- **Annual 11/12** — not part of this launch.
