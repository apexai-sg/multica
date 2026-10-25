# Aurora credit billing: metered by delivered output

Status: draft design, pending owner approval.
Date: 2026-10-25.
Branch: `feat/credit-refactor`.

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
  declared by the agent. The 2K row stays in the card's shape but has no rate
  until a provider that outputs 2K is wired (Seedance 2.5 tops out at 1080P).

Exact numbers are the owner's call (see Open questions). The card ships with
the structure plus placeholder values and fails closed: a missing rate for a
delivered kind fails the settlement instead of charging zero.

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
- Text is counted from the bytes already streamed; no extra storage read.
- **Text scope**: every `role=primary` and `role=supporting` text artifact is
  billed; `role=transcript` is not. A transcript is a byproduct of another
  skill's route (video-captions publishes the ASR text beside the video), and
  billing it would charge twice for one deliverable. Issue comments are not
  artifacts and are never billed.
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
  (`3 张图 × 2 积分`, `1 段视频 768P × 5 秒 × 4 积分/秒`), a total, and a link to
  the account page. When `over_cap` is false and the refund occurred, show the
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
   three tiers, credit packs, and the plan screen modelled on the reference —
   including the comparison table that renders each tier's credits as seconds
   of video and numbers of images at the card's rates. That table is generated
   from the rate card, never hand-written, so it cannot drift from what the
   user is actually charged.

Each phase ships independently; phase 1 is meaningful without 2 and 3.

## Open questions for the owner

1. **Existing balances and subscriptions.** Re-denominate at a stated ratio,
   grandfather at the old value, or reset and re-grant? No correct answer
   without knowing whether any real money has been taken.
2. **The rate numbers.** The design fixes the shape; the numbers need a margin
   target against the ARK invoice. The reference is a sanity anchor, not a
   source: its published allotments imply about 40 credits per HD image and 70
   per second of 768P / 120 per second of 2K video in *its* credit, which is
   worth roughly ¥0.0065. This design's credit is worth S$0.01, so its rates
   must be set from our own cost, not copied. Text has no reference rate at all
   (the reference does not charge for the agent's text output).

### Cost basis (verified 2026-10-25, list prices)

At 1 CNY ≈ S$0.19 and 100 credits = S$1, one credit is about 0.19 CNY of
provider cost. ARK list prices, converted:

| line | ARK price | cost / credit-value | at 3× margin |
|---|---|---|---|
| image, Seedream 5.0 Pro ≤2.36 MP | ¥0.30/image | 5.7 credits | 17 credits |
| image, Seedream 5.0 Pro >2.36 MP | ¥0.60/image | 11.4 credits | 34 credits |
| video 480P | ¥0.67/s (70/M tok × 9,608 tok/s) | 12.8 credits | 38 credits |
| video 720P | ¥1.51/s (70/M tok × 21,600 tok/s) | 28.7 credits | 86 credits |
| video 1080P | ¥3.74/s (77/M tok × 48,600 tok/s) | 71.1 credits | 213 credits |

Two consequences the owner must price around:

- **Seedance 2.5 tops out at 1080P**, so the 2K tier in section 2 has no
  provider behind it today. Drop it from the card until a 2K model is wired.
- **Our video cost is several times the reference's retail price.** The
  reference sells 768P at roughly ¥0.23–0.29/s (≈S$0.05/s); Seedance 2.5's 720P
  costs S$0.29/s before margin. The reference's plan economics — ¥65 for 143
  seconds of 768P — cannot be reproduced on this provider. Either the video
  price is visibly higher than the reference's, or video leans on the 480P
  draft-then-upscale route, or the margin on video is thin.

Account-specific discounts are unknown: `arkcli auth status` reports an invalid
SSO refresh token, so `Price` versus `OriginalPrice` could not be read.
3. **SGD prices and credit grants.** The names and the shape come from the
   reference; the numbers do not transfer, because the reference's credit is
   worth about ¥0.0065 while this design's is worth S$0.01. A tier's monthly
   credits, its price, and the rate card must satisfy one rule: the credits a
   tier grants must not exceed what the same money buys in packs, or nobody
   buys a pack. A coherent first set: Starter S$12/mo → 1,200 credits, Plus
   S$89/mo → 9,000, Pro S$249/mo → 25,500, with packs at 100 credits per S$1
   plus 0/5/10% volume bonus. The owner confirms or replaces these.
4. **The signup bonus.** The reference gives new users free credits valid
   three days. Keep that shape for `auroraSignupBonusMicro` (a real trial with
   a hard expiry), or drop free credits entirely?

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
