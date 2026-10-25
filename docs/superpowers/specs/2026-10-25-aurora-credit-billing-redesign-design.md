# Aurora credit billing: metered by delivered output

Status: draft design, pending owner approval.
Date: 2026-10-25.
Branch: `feat/credit-refactor`.

## Context

Aurora charges one fixed price per skill run. `SkillCatalogEntry.Credits` is a
constant per expert (60–100 today, `server/internal/aurora/experts_catalog.go`),
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
shows balance plus ledger rows; a deduction row carries an amount and a
reference id, not "3 images + one 5s 768P clip".

The reference is MiniMax's media-plan subscribe page (`design.minimax.cn`,
server-rendered data from its public combo-products API): three subscription
tiers priced monthly, monthly credit grants that expire at month end, separately
sold credit packs, and one shared credit pool with published per-unit rates for
text, image, and video.

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

- Charging by provider spend. The platform's ARK bill is not the user's bill,
  and provider calls are invisible to the server.
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
- **D2. Three kinds, three meters.** Text by delivered tokens; image per image;
  video by resolution × seconds.
- **D3. Text is metered, not free.** Output only, converted from the delivered
  text artifact. Internal reasoning and tool-loop tokens are not billed.
- **D4. Reservation is by a per-skill declared cap.** The catalog declares an
  output cap per skill; submit freezes the worst-case charge; a balance below
  the cap is refused with the shortfall.
- **D5. One credit pool.** No per-kind wallets. Kind differences live in the
  rate card and in the itemized charge.
- **D6. Credits are the unit; currency is SGD.** 100 credits = S$1.00. The
  credit is not pegged to a provider cost.
- **D7. Three paid tiers plus credit packs**, plus the existing free tier
  outside that count.
- **D8. The free tier survives** with its lazy monthly grant and month-end
  expiry unchanged in shape.

## Design

### 1. Unit and arithmetic

- Internal storage stays micro-credits; `credit_balance.available_micro` and
  `credit_ledger.amount_micro` keep their meaning (`1 credit = 1e6 micro`).
- User-facing display is credits; `100 credits = S$1.00`, so one credit is
  S$0.01 and one micro-credit is S$1e-8.
- Every rate in the card is an integer number of micro-credits per unit, so all
  arithmetic is integer and no float ever touches a balance.
- Charges round **up** to the next whole credit at the line level, so a partial
  unit never rounds to free.

### 2. The rate card

One server-owned table, versioned by a string (`rate_version`), in
`server/internal/aurora/pricing.go`. Never duplicated in the frontend: the
client renders the rates the API returns.

```go
type RateCard struct {
    Version string
    Text    TextRate    // micro-credits per 1,000 delivered tokens
    Image   []ImageRate // per delivered image, matched by longest declared tier
    Video   []VideoRate // micro-credits per second, matched by measured height
}
```

- **Text**: `tokens = asciiRunes/4 + nonASCIIRunes*3/4`, rounded up. The
  coefficients are constants on the card, not scattered call sites.
- **Image**: one rate per image in v1. The type is a slice so a per-model tier
  can be added without a schema change; an image that matches no tier uses the
  first entry.
- **Video**: tiers by **measured height**: `<720` → 480P, `720–1079` → 720P,
  `1080–1439` → 1080P, `≥1440` → 2K. Height is measured from the file, not
  declared by the agent.

Exact numbers are the owner's call (see Open questions). The card ships with
the structure plus placeholder values and fails closed: a missing rate for a
delivered kind fails the settlement instead of charging zero.

### 3. Metering happens at artifact commit

`server/internal/handler/aurora_artifact.go` already streams every artifact to
compute its digest and sniff its MIME. Measure there, once, while the bytes are
in hand — do not re-read the file at settlement.

New server-owned column `aurora_asset.measured jsonb NOT NULL DEFAULT '{}'`
(separate from the existing model-written `metadata`):

| kind  | measured fields |
|-------|-----------------|
| text  | `tokens` (from the delivered body) |
| image | `width`, `height` |
| video | `duration_ms`, `width`, `height` |
| pdf   | not billed in v1; no fields |

Rules:

- A **video whose duration or dimensions cannot be measured is rejected** with
  400 at commit. Fail-closed at the boundary, while the agent can still correct
  the artifact, instead of guessing a charge later. This requires a probe in the
  API image (`ffprobe`) or a pure-Go MP4/WebM reader; the builder picks the
  cheaper one that covers `mp4`, `mov`, and `webm`.
- An **image whose header cannot be decoded** is rejected the same way.
- Text is counted from the bytes already streamed; no extra storage read.
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

1. `cap_micro = text(max_text_tokens) + images(max_images) + video(max_video_secs, max_video_tier)`,
   using the same rate card the settlement will use.
2. Balance `< cap_micro` → 402 `insufficient_credits` with `required_credits`,
   `available_credits`, and `shortfall_credits` in the body.
3. Otherwise `Reserve(cap_micro)` exactly as today, with the generation id as
   reference.
4. The cap is written into the task brief so the model knows its budget. The
   brief instruction is advisory; the charge does not depend on it.

A skill with `MaxImages = 0` and `MaxVideoSecs = 0` is a text-only skill and
reserves only its text ceiling.

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

On failure, refund the reservation in full (unchanged behaviour).

If `actual_micro > reserved` — the agent ignored its cap — charge the
reservation and record the overage on the charge row (`over_cap = true`, the
measured quantity preserved). Never debit below zero. Log and count it as
`aurora_charge_over_cap`; a rising counter means the cap table is wrong.

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
- Month-end expiry becomes: expire the `monthly` batches whose `expires_at`
  passed, for their `remaining_micro`. Missing a tick self-heals because the
  amount is stored, not derived.
- Topup packs get `expires_at = created_at + 1 year`.
- Signup bonus keeps its existing rule (already `expires_at`-less today; owner
  to confirm — see Open questions).

Runs and subscriptions predating the batch table get one `adjustment` batch
each, created by the data migration.

### 7. Schema changes

Aurora's fork-only migrations take numbers from 5000 upward. Every index is
`CREATE [UNIQUE] INDEX CONCURRENTLY` in its own single-statement file.

- `5000_aurora_asset_measured` — add `measured jsonb`.
- `5001_credit_batch` — table.
- `5002_credit_batch_user_expiry_idx` — concurrent index.
- `5003_credit_batch_allocation` — table.
- `5004_credit_batch_allocation_ledger_idx` — concurrent index.
- `5005_aurora_generation_charge` — table.
- `5006_aurora_generation_charge_generation_idx` — concurrent index.
- `5007_aurora_generation_rate_version` — add `rate_version text` to
  `aurora_generation` so a settled run keeps the card it was priced with.

`aurora_generation_charge`:

| column | meaning |
|---|---|
| `generation_id`, `workspace_id`, `user_id` | attribution |
| `kind` | `text` \| `image` \| `video` |
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
- `GET /api/aurora/generations/{id}` — gains `charges[]` (kind, quantity, unit
  rate, amount, measured) and `rate_version`.
- `GET /api/aurora/billing/balance` — gains `expires_at` on the per-batch
  breakdown and `monthly_usage` split by kind for the current month.
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
  (`3 张图 × 2 积分`, `1 段视频 768P × 5 秒 × 4 积分/秒`), a total, and a link to
  the account page. When `over_cap` is false and the refund occurred, show the
  actual charge only; never show the reservation.
- **Account page** (`billing.tsx`): balance split by expiry batch ("本月赠送
  1,800，10-31 作废" / "积分包 5,000，2027-10 到期"), this month's spend split
  by kind, and the ledger with human labels carrying the itemization.

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
- **Rates are frozen per run.** `rate_version` on the generation means a card
  change never reprices history.
- **Same rate card on both sides.** Reservation and settlement call one
  function; a second implementation is a bug.

## Testing

- Go table tests for the rate card: token conversion boundaries, video tier
  edges (719/720/1079/1080/1439/1440), rounding up, missing rates fail closed.
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
   three tiers, credit packs, and the plan screen modelled on the reference.

Each phase ships independently; phase 1 is meaningful without 2 and 3.

## Open questions for the owner

1. **Existing balances and subscriptions.** Re-denominate at a stated ratio,
   grandfather at the old value, or reset and re-grant? No correct answer
   without knowing whether any real money has been taken.
2. **The rate numbers.** The design fixes the shape; the actual credits per
   1,000 tokens, per image, and per second at each tier need a margin target
   against the ARK invoice.
3. **Tier names and prices.** Three tiers plus packs, in SGD, with the yearly
   discount (if any).

## What is left to the builder

- The probe implementation (ffprobe versus a pure-Go reader) and the exact
  400 responses for unmeasurable artifacts.
- The per-skill cap values for all 33 experts.
- The exact JSON shape of the itemization on both API and UI, within the
  constraints above.
- Whether `expire` ledger rows survive as a kind once batches carry expiry, or
  whether batch expiry becomes its own write with its own label.
