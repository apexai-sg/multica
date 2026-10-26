# Aurora credit billing: metered by delivered output

Status: draft design, pending owner approval.
Date: 2026-10-25; all-provider commercial review updated 2026-10-27.
Branch: `feat/credit-refactor`.

**Pricing override and production gate:** The owner-requested complete supplier-vs-official audit is [2026-10-27-aurora-all-provider-pricing-audit.md](./2026-10-27-aurora-all-provider-pricing-audit.md). That SKU-level table supersedes earlier image/text/other-model price assumptions in this file. PR #38 is documentation-only; rates marked HOLD/PROVISIONAL must not be publicly sold. A positive single-output margin is insufficient if subscriptions, input materials, retries or fees make the full job loss-making.

## Context

Aurora charges one fixed price per skill run. `SkillCatalogEntry.Credits` is a
constant per expert (60, 80, 90, 100, or 120 today,
`server/internal/aurora/experts_catalog.go`),
the create handler reserves `credits × 1e6` micro-credits before enqueueing
(`server/internal/handler/aurora.go:285`), and settlement records
`credits_charged = credits_reserved` with no reconciliation
(`server/internal/service/aurora_completion.go:136`). Only a failed or
moderation-blocked generation refunds.

That price is unrelated to what the run actually produces. One account of three
images, one account of one image, and one account of twenty images all cost the
same. Worse, the provider cost of those images and videos is paid on the
platform lane (`ARK_PLATFORM_API_KEY`) and never enters the ledger, so the
charge and the cost have no relationship at all.

The user-visible result is the reported problem: a single balance number, a
transaction list of bare amounts, and no way to answer "how much is left, what
did I spend it on, when does it expire". `packages/views/aurora/billing.tsx`
shows balance plus ledger rows and already maps a generation row to its skill
name; what it cannot show is the itemization — a deduction still carries one
amount, not "3 images + one 5s 768P clip".

The reference is MiniMax's media-plan subscribe page (`design.minimax.cn`):
three tiers — Starter, Plus, Pro — sold monthly or yearly, each granting a
monthly credit allowance that expires 31 days after it arrives; separately sold
credit packs valid one year; one shared credit pool spent earliest-expiry-first;
and, above all, a comparison table that translates a tier's credits into
concrete output ("83 秒 2K 视频", "250 张高清图片"). That last table is the
mechanic that answers "how much is left and what does it buy", and this design
adopts it.

## Goal

Charge for what the run actually delivered, at published rates, in a unit the
user can reason about, and show the arithmetic at the three moments where it
matters:

1. **Before submit** — the most this skill can cost, the current balance, and
   the shortfall when the balance is not enough.
2. **On the result page** — what this run charged, itemized by kind and
   quantity.
3. **On the account page** — balance, this month's spend split by text / image
   / video, and when the remaining credits expire.

## Success criteria

- A run that delivers three images costs three times a run that delivers one,
  and the result page names both the count and the rate.
- A text-only run costs by delivered output length, not a flat fee.
- A video run costs by measured resolution and measured duration; the numbers
  shown are measured by the server, not reported by the agent.
- No user can be charged more than the amount quoted before submit.
- The account page answers "what expires, and when" without reading the ledger.

## Non-goals

- Passing through the provider's fluctuating invoice as an unquoted customer charge. Published customer prices remain the authority, but actual provider request-level usage and costs must be captured internally and used to prevent loss-making sales.
- Enforcing the cap inside the runtime. Aurora runs on ordinary agents with
  shell access and no tool allowlist or turn cap; the server cannot stop a run
  mid-flight.
- Per-seat, per-workspace, or team billing. This stays a personal wallet.
- Refunds for over-cap output. Over-cap output is delivered and absorbed.

## Anti-goals

- A charge the user did not see coming. Every debit must be bounded by a number
  shown before submit.
- A charge computed from model-reported metadata. The manifest writer is the
  agent; its `metadata` is untrusted input for billing.
- A price that changes for an already-settled run when the rate card changes.

## Decisions taken with the owner

- **D1. Meter by delivered output, settle after the run.** The reserved amount
  is a ceiling; the charge is what the artifacts add up to; the difference is
  refunded.
- **D2. Three output categories, SKU-specific meters.** Images per output image; video by the exact provider-supported resolution/audio/input configuration and measured seconds; text by provider-reported input, output, cache and reasoning usage (not approximations of output text).
- **D3. Text is commercially HOLD until its whole-request cost can be quoted.** The old output-only 1-credit/1K-token rate exceeds relevant official API list prices and ignores billable input. Do not silently bill text by a Unicode heuristic; distinguish free displayed transcript artifacts from priced model token usage.
- **D4. Reservation is by a per-skill declared cap.** The catalog declares an
  output cap per skill; submit freezes the worst-case charge; a balance below
  the cap is refused with the shortfall.
- **D5. One credit pool.** No per-kind wallets. Kind differences live in the
  rate card and in the itemized charge.
- **D6. Credits are the unit; currency is SGD.** 100 credits = S$1.00. The
  credit is not pegged to a provider cost.
- **D7. Three paid tiers — Starter, Plus, Pro — plus credit packs**, sold
  monthly or yearly (yearly = ten months' price, "立省2月"). The tier names,
  the pack sizes, and the credits-to-output comparison table follow the
  reference page.
- **D8. No free tier.** New users get no monthly grant. The `free` row in
  `TierCatalog`, the lazy free-monthly grant in `entitlement.go`, and the free
  tier's part of the month-end settlement are removed. Using any metered skill
  requires a subscription or a purchased pack; an account with no plan can sign
  in but cannot generate.

## Design

### 1. Unit and arithmetic

- Internal storage stays micro-credits; `credit_balance.available_micro` and
  `credit_ledger.amount_micro` keep their meaning (`1 credit = 1e6 micro`).
- User-facing display is credits; `100 credits = S$1.00`, so one credit is
  S$0.01 and one micro-credit is S$1e-8.
- Every rate in the card is an integer number of micro-credits per unit, so all
  arithmetic is integer and no float ever touches a balance.
- **No whole-credit rounding for metered text or fractional video seconds.** Use exact fixed-point integer microcredit arithmetic, round only to the minimum internal microcredit precision where necessary, and make the posted price and line-item amount consistent. A whole-credit charge may be used for an expressly per-image SKU, not as a generic minimum.

### 2. The rate card

One server-owned table, versioned by a string (`rate_version`), in
`server/internal/aurora/pricing.go`. Never duplicated in the frontend: the
client renders the rates the API returns.

```go
type RateCard struct {
    Version string
    Models  map[string]ModelRate // keyed by provider model id; each has SKU variants
}

type ModelRate struct {
    Kind  string    // text | image | video
    Text  *TextRate // requires provider input/output/cache usage; HOLD in this phase
    Image *ImageRate
    Video []VideoRate // micro-credits per second, by measured height
}
```

**Rates require an exact model and SKU variant**, not just a kind. Variant includes image resolution, video resolution/audio/reference inputs and text token category/context tier. The earlier one-model-per-kind fleet is useful only as an implementation fixture. Any unsupported model/variant is HOLD and must fail at generation creation; do not make the runtime quietly switch to a pricier provider while charging a cheaper one.

**The model is chosen server-side, at generation creation**, from the skill's
configured model — not inferred from the artifact. The server cannot tell which
model produced a file (it measures count, resolution, duration; the manifest's
model claim is agent-written), so the declared model is the only trustworthy
pricing key. If the run then uses something pricier, the difference is absorbed
like any other over-cap cost and counted.

v1 exposes no user-facing model picker: a skill's configured model is the only
input, so "switch to a better model" is an operator change. A per-request
"standard / high quality" choice is a later phase; it would become an explicit
request field priced from the same card, never a free-form model name from the
client.

- **Text**: bill via the provider's actual input/output/cache/thinking token counts and separately agreed rates. `asciiRunes/4 + nonASCIIRunes*3/4` can be used only for a **non-billing UI estimate**; it is not a provider token meter.
- **Image**: one rate per image per model; an image that carries no tier uses
  the model's single rate.
- **Video**: charge by an explicitly requested and server-authorized provider-specific SKU, then validate the delivered duration/dimensions against that SKU. Do **not** infer 2K solely from output height ≥1080 or charge 1080P at a 2K tier. Unquoted video tiers are HOLD.

**Pricing (owner-requested model-wide recheck, 2026-10-27).** Indicative output-only candidate rate card below. These are SKU-specific and are NOT active production prices. The [all-provider audit](./2026-10-27-aurora-all-provider-pricing-audit.md) contains every HopBase-quoted SKU, official comparisons, discounts, per-output gross margins, and reasons for HOLD.

| line / exact SKU | proposed rate | status |
|---|---|---|
| image `seedream-5-0-lite` | **4 credits/image** | CANDIDATE |
| image `seedream-5-0-pro` ≤2.61 MP | **5 credits/image** | CANDIDATE |
| image `nano-banana-2.1` 1K | **3 credits/image** | CANDIDATE, upstream ID verification |
| image `gemini-3-pro-image` 1K/2K | **15 credits/image** | CANDIDATE, input fees separate |
| MiniMax H3 video 768P | **9 credits/second** | CANDIDATE, thin margin |
| MiniMax H3 video 2K | **15 credits/second** | CANDIDATE, thin margin |
| Kling v3 Omni silent, no reference video, 720P / 1080P | **10 / 14 credits/second** | PROVISIONAL: OEM pricing confirmation needed |
| `claude-haiku-5-5` output ≤100K prompt / `claude-sonnet-5-5` output | **40 / 750 credits per 1M output tokens**, output component only | HOLD until input/caching/total job cost verified |
| `deepseek-v4.1-flash` text, MiniMax H3-Max 480P, Kling 2K | **no qualifying live rate** | HOLD |
| all other models/variants including GPT/Opus/Kimi/Qwen | **unpriced** | HOLD pending procurement quote and OEM match |

**MiniMax H3 video price check (output only; 100 credits = S$1).** Official API reference: https://platform.minimax.io/subscribe/token-plan?tab=api-enterprise ; HopBase ToB quote dated 2026-10-12; reference FX below. The customer rate is cheaper than the comparable **official API list price**, not necessarily every consumer membership or promotional package.

| H3 video output | HopBase cost | Official API list | Apex AI rate | Discount to official API | Output-only gross margin |
|---|---:|---:|---:|---:|---:|
| 768P / s | ¥0.425 ≈ S$0.08075 | US$0.08 ≈ S$0.1024 | 9 credits = S$0.09 | 12.1% below list (87.9% of list) | 10.3% |
| 2K / s | ¥0.68 ≈ S$0.12920 | US$0.13 ≈ S$0.1664 | 15 credits = S$0.15 | 9.9% below list (90.1% of list) | 13.9% |

**Margin warning and subscription-unit economics:** Output-only comparisons are not the same as end-to-end contribution profit. With 100 credits = S$1, MiniMax H3 9/15 credits/s has only ~10.3%/~13.9% output margin before costs. Importantly, a subscriber who pays **S$149 for 18,000 credits** (proposed in an earlier review comment, not approved) supplies only S$0.00828 revenue/credit: both H3 SKUs would lose money at 9/15 credits/s. This plan is NOT approved alongside these rates. The single wallet must check realized revenue per credit across all funded sources including discounted plans and topup bonuses (audit §E) and block loss-making combinations, or renegotiate supplier rates. Revalidate FX and official list prices, and never claim blanket "official 30–70% off".

| tier | monthly | credits/month |
|---|---|---|
| Starter | S$12 | 1,200 |
| Plus | S$89 | 9,000 |
| Pro | S$249 | 25,500 |

Existing **unapproved** proposal: annual pricing equals ten monthly payments, and large packs add bonus credits. **Do not implement or publish these plan/pack terms** until effective realized SGD/credit across every funding source is reconciled with the lowest-margin active SKU. Earlier S$49/5,500 and S$149/18,000 alternatives create negative H3 video output margins; see audit §E.

Fail closed **before reservation** when a model/SKU is absent, unquoted, expired, under margin floor, or HOLD; re-check price-version consistency at settlement rather than guessing a different amount.

### 3. Metering happens at artifact staging

The bytes are streamed in `server/internal/handler/aurora_artifact_upload.go`
(both the upload and the provider-import paths go through
`streamAuroraArtifact`), which computes the SHA-256 as it reads and sniffs only
the first 512 bytes. The commit path (`ReportTaskArtifacts` in
`aurora_artifact.go`) never sees the bytes. Measure in the streaming path,
once, while the bytes are in hand — do not re-read the object at settlement.

Video needs a seekable file: the duration lives in the `moov` atom, which may
sit at the end of the object, and `Storage` exposes only a sequential
`GetReader`. The streaming path therefore spills the artifact to a temporary
file (bounded by the existing per-kind byte cap) and probes that file. Any
other approach requires adding range reads to `Storage`; the temp file is the
cheaper, local change.

Rejection happens at **staging**, where the agent can still convert or re-export
the file. A staging rejection is a 400 the agent sees while it still owns the
run; rejection at commit would only fail the whole generation and refund it.

New server-owned column `measured jsonb NOT NULL DEFAULT '{}'` on
`aurora_artifact_staging`, copied to `aurora_asset.measured` at commit
(separate from the existing model-written `metadata`):

| kind  | measured fields |
|-------|-----------------|
| text  | `tokens` (from the delivered body) |
| image | `width`, `height` |
| video | `duration_ms`, `width`, `height` |
| pdf   | not billed in v1; no fields |

Rules:

- A **video whose duration or dimensions cannot be measured is rejected** with
  400 at staging. Fail-closed at the boundary, while the agent can still correct
  the artifact, instead of guessing a charge later. This requires a probe in the
  API image (`ffprobe`) or a pure-Go MP4/WebM reader; the builder picks the
  cheaper one that covers `mp4`, `mov`, and `webm`.
- An **image whose header cannot be decoded** is rejected the same way.
- Text file bytes may be counted for display/estimates, but are **not authoritative token billing**. The provider call must supply server-trusted actual input/output/caching/reasoning usage before any text SKU becomes a paid metered product.
- **Text scope**: primary/supporting text outputs are delivered artifacts, but **none are charged using their display length**. Once a text model is approved, bill actual provider-request usage including input/cached/reasoning and generated output according to the accepted quote; distinguish incidental transcripts so they cannot be double-billed. Issue comments are not billable artifacts.
- `pdf` artifacts are delivered but cost nothing. Stated in the account page
  copy so it is not a surprise.

### 4. Output caps and reservation

`SkillCatalogEntry.Credits` is replaced by `Caps`:

```go
type OutputCaps struct {
    MaxTextTokens int64
    MaxImages     int
    MaxVideoSecs  float64
    MaxVideoTier  string // 480P | 720P | 1080P | 2K
}
```

Create-time flow (`CreateAuroraGeneration`):

1. Resolve the *approved* model+variant+input mode; if HOLD, unquoted or uneconomic for the user's funding source, reject before reserve. For enabled requests, compute `cap_micro` from user-selected maximum output and all chargeable inputs/attempt limits at a frozen rate version, using the same rate card used for settlement.
2. Balance `< cap_micro` → 402 `insufficient_credits` with `required_credits`,
   `available_credits`, and `shortfall_credits` in the body.
3. Otherwise `Reserve(cap_micro)` exactly as today, with the generation id as
   reference.
4. The cap is written into the task brief so the model knows its budget. The
   brief instruction is advisory; the charge does not depend on it.

A skill with `MaxImages = 0` and `MaxVideoSecs = 0` is a text-only skill and
reserves only its text ceiling.

**The cap is per generation, not per round.** A generation can end a round in
`needs_input`, take a follow-up comment, and re-enter `queued` on the *same*
reservation (`UpdateAuroraGenerationRound`, `server/pkg/db/queries/aurora.sql`).
Every round of that conversation delivers artifacts into the same generation,
so the cap has to cover the whole conversation and settlement sums all rounds.
The consequence is deliberate: a long conversation can exceed its cap, and the
overage is absorbed like any other over-cap output.

**Abandoned holds are released.** A generation parked in `needs_input` with no
reply settles nothing and would hold its reservation forever — harmless when
the reservation equalled the price, a permanently frozen balance once it is a
ceiling. A sweeper fails such a generation after 24 hours without activity,
refunding the full reservation through the normal failure path.

The `rate_version` in force at creation is written onto the generation row, so
both the rounds and the eventual settlement price against the card the user was
quoted. `pricing.go` therefore keeps a versioned map, not a single struct, and
a missing version fails settlement rather than falling back to today's rates.

### 5. Settlement

On success, in `aurora_completion.go`:

1. Load the generation's delivered assets with their `measured` usage.
2. Compute one line per (kind, rate tier): `quantity`, `unit_rate_micro`,
   `amount_micro`.
3. `actual_micro = Σ amount_micro`, capped at `credits_reserved`.
4. Write `aurora_generation_charge` rows.
5. If `actual_micro < reserved`, `Refund(reserved - actual)` with reference
   `settle:<generationID>` — idempotent, so a replayed terminal event is a
   no-op.
6. Record `credits_charged = actual_micro`.

On failure, refund the reservation in full (unchanged behaviour) — and it keeps
its existing reference, the bare generation id. Only the successful-settlement
partial refund uses `settle:<generationID>`, so the two paths cannot collide and
a replayed terminal event is still a no-op.

**Runs in flight across the deploy.** A generation created before this ships
carries `rate_version = 'legacy'`, which prices the whole reservation at the
skill's old flat `Credits` value and charges exactly that, ignoring
measurements. That also covers a run that was mid-flight when the migration
landed and therefore has assets with no `measured` row.

**Settlement failure is not a charge decision.** A read or write failure leaves
the generation non-terminal so the next terminal event or tick retries, exactly
as `settleAuroraOnCompleted` does today. It never falls back to "charge the
reservation" or "refund everything" as a guess.

If `actual_micro > reserved` — the agent ignored its cap, or a multi-round
conversation outran it — charge the reservation and record the overage on the
charge row (`over_cap = true`, the measured quantity preserved). Never debit
below zero. Log and count it as `aurora_charge_over_cap`; a rising counter means
the cap table is wrong or the cap needs to grow with conversation length.

### 6. Wallet: batches, expiry, and spend order

Today `expireUserMonth` computes a month's remainder from
`granted + netSpend` and has a known deviation when a tick is missed. It also
cannot express "this pack expires in a year".

Introduce `credit_batch`:

| column | meaning |
|---|---|
| `id` | batch id |
| `user_id` | owner |
| `source` | `monthly` \| `topup` \| `signup` \| `adjustment` |
| `amount_micro` | face value |
| `remaining_micro` | spendable remainder |
| `expires_at` | null = never expires |
| `reference` | the existing idempotency reference |

`credit_batch_allocation (ledger_id, batch_id, amount_micro)` records which
batches a debit consumed, so the ledger can explain a charge and the account
page can show per-batch expiry.

- Spend order is **soonest expiry first**, then oldest batch; non-expiring
  batches go last. That is what makes "use the grant before the pack" true
  without a special case.
- `credit_balance.available_micro` remains the denormalized sum, so the hot
  path is unchanged.
- **Monthly credits expire 31 days after they arrive**, not at month end (the
  reference's rule, and a better fit for batches anyway): the monthly batch
  carries `expires_at = granted_at + 31 days`. Expiring a batch retires its
  stored `remaining_micro`, so a missed tick self-heals.
- Topup packs get `expires_at = created_at + 1 year`.
- The signup bonus, if kept, gets the reference's 3-day expiry (see Open
  questions).

**Seeding the batches.** Every user with a non-zero balance at migration gets
exactly one non-expiring `adjustment` batch equal to their current
`credit_balance.available_micro`. Historical grants cannot be replayed
faithfully — month-end expiry was computed as `granted + netSpend`, not from
stored remainders — so per-batch allocation history begins at the migration
instant and existing ledger rows stay untouched. The invariant
`balance == Σ remaining` holds on day one by construction.

The local `source` vocabulary (`monthly | topup | signup | adjustment`) is
deliberately not the cloud `BillingBatch` one (`purchase | bonus | adjustment`);
the local table is the authority and the API boundary maps onto the cloud
vocabulary where the two meet.

### 7. Schema changes

Aurora's fork-only migrations take numbers from 5000 upward. Every index is
`CREATE [UNIQUE] INDEX CONCURRENTLY` in its own single-statement file.

`5000` is taken (`5000_aurora_carrier_invocation`), so this series starts at
5001 alongside it.

- `5001_aurora_artifact_measured` — add `measured jsonb` to
  `aurora_artifact_staging` and `aurora_asset`.
- `5002_credit_batch` — table.
- `5003_credit_batch_user_expiry_idx` — concurrent index.
- `5004_credit_batch_allocation` — table.
- `5005_credit_batch_allocation_ledger_idx` — concurrent index.
- `5006_aurora_generation_charge` — table.
- `5007_aurora_generation_charge_generation_idx` — concurrent index.
- `5008_aurora_generation_rate_version` — add `rate_version text` to
  `aurora_generation` so a settled run keeps the card it was priced with.

`aurora_generation_charge`:

| column | meaning |
|---|---|
| `generation_id`, `workspace_id`, `user_id` | attribution |
| `kind` | `text` \| `image` \| `video` |
| `model` | the provider model id the rate came from |
| `quantity` | numeric(12,3): tokens/1000, images, seconds |
| `unit_rate_micro` | the rate applied |
| `amount_micro` | `quantity × rate`, rounded up at line level |
| `measured` | the measurement backing `quantity` (height, duration, tokens) |
| `rate_version` | the card version |
| `over_cap` | boolean |

### 8. API changes

- `GET /api/aurora/skills` — each entry gains `max_credits` (the reservation
  ceiling, in credits) and `caps`; `credits` stays as a deprecated alias of
  `max_credits` so an installed desktop client keeps rendering a number.
- `POST /api/aurora/generations` — 402 body gains
  `required_credits` / `available_credits` / `shortfall_credits`.
- `GET /api/aurora/generations/{id}` — gains `charges[]` (kind, model, quantity,
  unit rate, amount, measured) and `rate_version`.
- `GET /api/aurora/billing/balance` — gains `reserved_micro` (money held by
  in-flight generations), `expires_at` on the per-batch breakdown, and
  `monthly_usage` split by kind for the current month. `available_micro` keeps
  meaning "spendable right now"; the account page shows available and reserved
  separately, because a run in flight must not look like a spend.
- `GET /api/aurora/billing/transactions` — each row gains the charge summary
  the transaction screen renders.
- Zod schemas in `packages/core/api/schema.ts` (or the aurora schema module)
  are updated with defaults and malformed-response tests.

### 9. UI changes

Three surfaces, matching the success criteria:

- **Skill directory and composer** (`skill-directory.tsx`,
  `generation-composer.tsx`): "最多 X 积分" replaces "X 积分". The insufficient
  balance dialog shows the shortfall, not just a failure.
- **Result page** (`generation-detail.tsx`): a charge block — one row per line
  (`3 张图 × 9 积分（seedream-5-0-lite）`, `1 段视频 768P × 5 秒 × 9 积分/秒
  （MiniMax H3）`), a total, and a link to the account page. When `over_cap` is false and the refund occurred, show the
  actual charge only; never show the reservation.
- **Account page** (`billing.tsx`): available and held-in-flight shown
  separately, balance split by expiry batch ("本月赠送 1,800，10-31 作废" /
  "积分包 5,000，2027-10 到期"), this month's spend split by kind, and the
  ledger with human labels carrying the itemization.
- **Subscribe page** (`billing.tsx` or a sibling plan route): the monthly /
  yearly toggle, the three tiers with their monthly credits, the four packs
  with their validity, and the comparison table. Every number in the table is
  computed from the rate card and the tier's credit grant; the page reads
  rates from the API rather than carrying its own copy.

Copy follows the repository UI-copy rules: no restatement, cost and expiry
stated once beside the number.

### 10. Migration of existing money

The unit is being re-denominated: today `1 USD = 1000 credits`; tomorrow
`100 credits = S$1`. Existing balances, subscription prices, and topup packs
were all priced under the old unit. This design does **not** settle how they
convert (see Open questions); the data migration that creates the starting
`adjustment` batches is written once that answer exists.

Until then the entitlement path keeps working on today's numbers.

## Failure modes and invariants

- **No charge without a reservation.** Every debit is preceded by a
  corresponding reserve for the same generation.
- **No charge without a measurement.** A missing video probe, an undecodable
  image, or an unparseable text artifact is a commit-time 400, never a zero
  charge and never a guess.
- **Idempotency.** Reserve keys on the generation id, refund on
  `settle:<generationID>`, expiry on the batch id. Replayed terminal events and
  rerun settlement ticks are no-ops.
- **No negative balance.** Over-cap output is absorbed, not debited.
- **The cap covers the whole conversation.** Every round of a `needs_input`
  generation counts against the one reservation made at creation, and an
  abandoned generation releases that reservation after 24 hours idle.
- **Rates are frozen per run.** `rate_version` on the generation means a card
  change never reprices history.
- **Same rate card on both sides.** Reservation and settlement call one
  function; a second implementation is a bug.

## Testing

- Go tests: exact fixed-point microcredits for small text/video quantities; video requested-tier vs measured-dimension boundaries (719/720/768/1079/1080/1439/1440); missing/HOLD/expired provider SKUs fail before reservation; per-model official-versus-supplier guards; supplier FX movement and discounted wallet credits cannot yield a negative permitted margin.
- Handler tests for the commit-time measurements with real fixture files
  (a tiny PNG, a 1-second MP4) — measured values come from the file, and an
  agent-supplied `metadata.duration` never wins.
- Settlement tests: actual < reserved refunds the difference; actual > reserved
  charges the reservation and flags `over_cap`; a replayed completion does not
  double-charge; a failed run refunds in full.
- Wallet tests: FEFO ordering, expiry of a monthly batch, a topup surviving a
  month boundary, and the sum invariant `balance == Σ remaining`.
- Frontend tests beside the existing ones in `packages/views/aurora/` for the
  three surfaces, asserting the itemization renders from the API shape and that
  the pre-submit number is the ceiling.
- `pnpm typecheck`, `pnpm lint`, `pnpm test`, and `make test` as the final gate.

## Rollout

1. **Metering and charging** (sections 3–5, 7 partially): measured usage,
   rate card, caps, settlement, charge table, result-page itemization. Runs can
   keep today's fixed prices as a transitional rate card with one flat rate per
   kind if needed, but the pipeline is the new one.
2. **Wallet batches** (section 6): expiry truth and the account-page split.
3. **Currency, plans, and the subscribe page** (D6, D7): SGD price IDs, the
   three tiers, credit packs, and the plan screen modelled on the reference —
   including the comparison table that renders each tier's credits as seconds
   of video and numbers of images at the card's rates. That table is generated
   from the rate card, never hand-written, so it cannot drift from what the
   user is actually charged.

Each phase ships independently; phase 1 is meaningful without 2 and 3.

## Cost basis (HopBase ToB quote 2026-10-12, valid to 2026-11-11)

The provider is HopBase (`api.hop-base.com`), not a direct Volcengine
integration. The quote's settlement terms already match this design: video is
billed only for finished output, and a failed generation is not charged.

Previously envisioned fixture fleet was `deepseek-v4.1-flash` for text, `seedream-5-0-lite` for image, and MiniMax H3 for video. **DeepSeek text is now commercial HOLD** at the quoted buy rate vs the OEM off-peak API list; no paid default text SKU may be silently selected. Per-model/variant activation is governed by the new pricing audit.

At 1 USD ≈ S$1.28, 1 CNY ≈ S$0.19, and 100 credits = S$1:

| line | HopBase price | cost / credits |
|---|---|---|
| image, `seedream-5-0-lite` | $0.0238/image | 3.0 credits |
| image, `seedream-5-0-pro` ≤2.61 MP | $0.0306/image | 3.9 credits |
| image, `nano-banana-2.1` 1K | $0.0153/image | 2.0 credits |
| image, `gemini-3-pro-image` | $0.0941/image | 12.0 credits |
| video, MiniMax H3 768P | ¥0.425/s | 8.1 credits/s |
| video, MiniMax H3 2K | ¥0.68/s | 13.0 credits/s |
| video, MiniMax H3-Max fast 480P | ¥0.34/s | 6.5 credits/s |
| video, Kling v3 omni silent 720P / 1080P / 2K | ¥0.45 / ¥0.60 / ¥0.75 per s | 8.6 / 11.5 / 14.4 credits/s |
| text, `deepseek-v4.1-flash` output (default) | ¥5.20/M tokens | 0.099 credits per 1k tokens |
| text, `deepseek-v4.1-flash` input | ¥1.30/M tokens | 0.025 credits per 1k tokens |
| text, `claude-haiku-5-5` output | $0.14/M tokens | 0.018 credits per 1k tokens |
| text, `claude-sonnet-5-5` output | $2.80/M tokens | 0.36 credits per 1k tokens |

Three consequences the owner must price around:

- **The server cannot tell which model produced an artifact.** It measures
  bytes — count, resolution, duration — and the manifest's model claim is
  agent-written. So one rate must cover every model a skill may reach, and each
  skill's brief has to pin the model it runs. A skill that silently picks
  `gemini-3-pro-image` costs 4× the rate set for `seedream-5-0-lite`.
- **Differentiate MiniMax consumer subscriptions from MiniMax's official API list.** The official H3 API lists 768P at US$0.08/s and 2K at US$0.13/s; both are above our quoted HopBase unit cost at the reference FX. The revised 9/15-credit rates are below those API list prices, but output-only gross margins are just 10.3%/13.9%. Do not claim a discount against MiniMax's consumer subscription effective rate or other models without verifying the comparable use case; separately billed inputs or retries may erase margin.
- **The old text rate is neither competitively priced nor safe.** At S$0.01/1K output (S$10/M), generic DeepSeek/Haiku text would be above official API output rates; long inputs could also cost more than short outputs earn. DeepSeek's own off-peak Flash cost can undercut the HopBase quote. Claude output has headroom but supplier input/cache costs are missing; those text SKUs remain HOLD.

The quote expires 2026-11-11; the card versions make a re-quote a config change
rather than a code change.

## Open questions for the owner

1. **Existing balances and subscriptions.** Re-denominate at a stated ratio,
   grandfather at the old value, or reset and re-grant? No correct answer
   without knowing whether any real money has been taken.
2. **The signup bonus.** The reference gives new users free credits valid three days. Keep a small expiry-limited bonus, or drop it? Count its effective revenue as zero when computing allowed SKU contribution margin.
3. **Approve actual supplier quote coverage and active model list.** Confirm HopBase mapping, SKU-specific input costs, official comparator, rate validity, contribution margin floor and treatment of plan/pack bonuses before enabling any of the new output-only candidates.

## What is left to the builder

- The probe implementation (ffprobe versus a pure-Go reader) inside the
  streaming path's temp file, and the exact 400 bodies for unmeasurable
  artifacts.
- The per-skill cap values for all 33 experts.
- The exact JSON shape of the itemization on both API and UI, within the
  constraints above.
- Whether `expire` ledger rows survive as a kind once batches carry expiry, or
  whether batch expiry becomes its own write with its own label.
- The idle timeout that releases an abandoned `needs_input` hold (24 hours is a
  starting value, not a requirement).
