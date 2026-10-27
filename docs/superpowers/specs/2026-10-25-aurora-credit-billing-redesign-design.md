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
(`server/internal/handler/aurora.go:281`), and settlement records
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
- Once a text SKU is approved, a text run costs by provider-reported
  input/output/cache usage, not by delivered Unicode length and not a flat fee.
  Until then every text-only skill is commercially HOLD and cannot be sold
  metered.
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
- **D4. Reservation is by the per-request requested output, bounded by the
  skill's declared maximum.** Where the skill exposes image count / video
  duration / video mode, submit quotes and reserves that selection, not the
  generic worst case. A skill with no selectable shape declares a visible fixed
  budget. A balance below the quote is refused with the shortfall.
- **D5. One credit pool, credit-only billing.** No per-kind wallets. Kind
  differences live in the rate card and in the itemized charge. Every plan,
  pack and entitlement is expressed only as a credit deduction from this pool:
  no per-plan SKU gating, no second wallet and no separate entitlement object
  (owner decision, 2026-10-27).
- **D6. Credits are the unit; currency is SGD.** 100 credits = S$1.00. The
  credit is not pegged to a provider cost.
- **D7. Three paid tiers — Starter, Plus, Pro — plus credit packs**, sold
  monthly or yearly. The tier names, pack sizes and credits-to-output
  comparison table follow the reference page. The per-credit catalogue remains
  **owner-approval gated** (see §2): the monthly plan discount cannot be
  adopted while it makes an active SKU loss-making at its realized SGD/credit.
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

**Every rate record carries its own provenance and validity window**, not
just a number: `approval` (`approved` \| `candidate` \| `provisional` \|
`hold`), `official_url`, `supplier_quote_revision`, `fx`, `effective_at` and
`expires_at`. Reservation and settlement fail closed when a record is
`provisional`/`hold`, unquoted, not yet effective, or expired; a `candidate`
record may be priced only by the disabled metered pipeline and is not sellable
until the owner approves it and the catalog sets `metered_enabled`. The current
HopBase quote (dated 2026-10-26) expires 2026-11-25 and must be re-verified
before any candidate is enabled. A rate's version string (`rate_version`) is frozen onto each
generation at creation; changing a rate is a new card version, never an edit
that reprices settled history.

**The model is chosen server-side, at generation creation**, from the skill's
configured model — not inferred from the artifact. The server cannot tell which
model produced a file (it measures count, resolution, duration; the manifest's
model claim is agent-written), so the declared model is the only trustworthy
pricing key. If the run then uses something pricier, the difference is absorbed
like any other over-cap cost and counted.

v1 exposes no user-facing **model** picker: a skill's configured model is the
only input, so "switch to a better model" is an operator change. A per-request
"standard / high quality" choice is a later phase; it would become an explicit
request field priced from the same card, never a free-form model name from the
client.

v1 **does** expose the billable output shape where the skill supports it:
image count, video duration and the provider-supported video mode/resolution.
The client sends those values as enumerated request fields (never a price and
never a model name), the server validates them against the skill's `Caps` and
the approved SKU list, and the same values produce the pre-submit quote and the
reservation. A skill that cannot express its output shape — including an
open-ended conversation — declares a visible fixed budget instead (see §4).

- **Text**: bill via the provider's actual input/output/cache/thinking token counts and separately agreed rates. `asciiRunes/4 + nonASCIIRunes*3/4` can be used only for a **non-billing UI estimate**; it is not a provider token meter.
- **Image**: one rate per image per model; an image that carries no tier uses
  the model's single rate.
- **Video**: charge by an explicitly requested and server-authorized provider-specific SKU, then validate the delivered duration/dimensions against that SKU. Do **not** infer 2K solely from output height ≥1080 or charge 1080P at a 2K tier. Unquoted video tiers are HOLD.

**Video SKU boundaries.** The rate is keyed by the requested/authorized SKU,
not derived from the delivered file. The measured height only validates that
the delivered artifact matches the SKU that was priced; a mismatch fails the
artifact at staging (400) rather than selecting another tier.

| model | requested SKU | accepted measured height | rate |
|---|---|---:|---|
| MiniMax H3 **Lite** (self-deployed) | 480P | 480 | 3 credits/s (CANDIDATE, availability unconfirmed) |
| MiniMax H3 **Lite** (self-deployed) | 768P | 720–1079 | 9 credits/s (CANDIDATE, availability unconfirmed) |
| MiniMax H3 **Lite** (self-deployed) | 2K | 1440 | 15 credits/s (CANDIDATE, availability unconfirmed) |
| MiniMax H3 (standard) | 768P | 720–1079 | 11 credits/s (CANDIDATE by owner override; 20.9% margin, above official list) |
| MiniMax H3 (standard) | 2K | 1440 | 18 credits/s (CANDIDATE by owner override; 21.1% margin, above official list) |
| Kling v3 Omni (silent, no reference video) | 720P | 720 | 10 credits/s (PROVISIONAL) |
| Kling v3 Omni (silent, no reference video) | 1080P | 1080 | 14 credits/s (PROVISIONAL) |
| any model | 480P without an approved row, 1440-not-2K shapes, 2160/4K, or no configured SKU | — | HOLD / reject before reserve |

Boundary values (`719`, `720`, `768`, `1079`, `1080`, `1439`, `1440`) are
tested against the requested SKU: `1080`–`1439` is **not** silently priced as
2K, and `2160` is not silently priced as anything. Kling's provisional rows
stay disabled until its OEM tier table is verified. The 2026-10-26 quote
raised standard H3 to US$0.068/0.111 per second, which would leave only
3.3%/5.3% at the old 9/15 retail; the owner raised those two tiers to 11/18
credits/s to reach ~20% output margin, which puts them **above** the official
list and bars any discount claim for standard H3. The Lite tier (US$0.024/0.039)
keeps the 9/15 retail at ~66% and stays below the official list, so it is the
intended default **once its availability and product limits are confirmed**. Do
not silently map a Lite request to the standard tier or vice versa: the model id
is part of the rate key.

**Pricing (recomputed from the HopBase quote dated 2026-10-26, valid to 2026-11-25).** Indicative output-only candidate rate card below. These are SKU-specific and are NOT active production prices. The [all-provider audit](./2026-10-27-aurora-all-provider-pricing-audit.md) contains every HopBase-quoted SKU, the official comparator, the discount, per-output gross margin and the reason for each HOLD.

| line / exact SKU | proposed rate | status |
|---|---|---|
| image `seedream-5-0-lite` | **4 credits/image** | CANDIDATE |
| image `seedream-5-0-pro` ≤2.61 MP | **5 credits/image** | CANDIDATE |
| image `nano-banana-2.1` 1K / 2K / 4K | **3 / 4 / 8 credits/image** | CANDIDATE, upstream ID verification |
| image `gemini-3-pro-image` 1K/2K/4K | **15 credits/image** | CANDIDATE, one-price including input tokens |
| image `gemini-3.1-flash-image` | 8 credits/image (max) | HOLD: 5.9% margin, post-fee unsafe |
| MiniMax H3 **Lite** video 480P / 768P / 2K | **3 / 9 / 15 credits/second** | CANDIDATE, pending availability and product limits |
| MiniMax H3 **standard** video 768P / 2K | **11 / 18 credits/second** | CANDIDATE by owner override: 20.9% / 21.1% margin, 7.4% / 8.2% **above** official list |
| H3 self-deployed 768P / 2K | 9 / 15 credits/second | PROVISIONAL: self-deployed tier, approximate official comparator |
| H3 self-deployed 1080P / 4K | 14 / 16 credits/second | HOLD: no official comparator |
| Kling v3 Omni silent, no reference video, 720P / 1080P | **10 / 14 credits/second** | PROVISIONAL: OEM pricing confirmation needed |
| Kling v3 Omni **with audio**, 720P / 1080P | 12 / 15 credits/second | HOLD: 5.9% / 6.1% margin |
| Grok Imagine video 480P / 720P / 1080P (if that provider is wired) | **8 / 12 / 22 credits/second** | CANDIDATE, provider not currently integrated |
| `claude-haiku-5-5` ≤100K prompt in/out / `claude-sonnet-5-5` in/out | **8 / 40 and 150 / 750 credits per 1M tokens**, output and input components only | HOLD until cache and >100K prompt tiers are quoted |
| `deepseek-v4.1-flash` text, H3-Max 480P/768P, Kling 2K, Seedance | **no qualifying live rate** | HOLD |
| all other models/variants including GPT/Gemini text/Opus/Kimi/Qwen | **unpriced** | HOLD pending usage metering and OEM match |

**MiniMax H3 video price check (output only; 100 credits = S$1).** Official API reference: https://platform.minimax.io/subscribe/token-plan?tab=api-enterprise ; HopBase ToB quote dated 2026-10-26; reference FX below (USD 1 = SGD 1.28, so `credits/s = US$/s × 128`). The **Lite** rate is cheaper than the comparable **official API list price** (not necessarily every consumer membership or promotional package). The **standard** tier is deliberately priced **above** the official list to reach the owner's 20% margin target.

| H3 video output | HopBase cost | Official API list | Apex AI rate | vs official API | Output-only gross margin |
|---|---:|---:|---:|---:|---:|
| **standard** 768P / s | US$0.068 ≈ S$0.08704 | US$0.08 ≈ S$0.1024 | 11 credits = S$0.11 | **7.4% above list** | **20.9%** |
| **standard** 2K / s | US$0.111 ≈ S$0.14208 | US$0.13 ≈ S$0.1664 | 18 credits = S$0.18 | **8.2% above list** | **21.1%** |
| **Lite** 768P / s | US$0.024 ≈ S$0.03072 | US$0.08 ≈ S$0.1024 | 9 credits = S$0.09 | 12.1% below list | **65.9%** |
| **Lite** 2K / s | US$0.039 ≈ S$0.04992 | US$0.13 ≈ S$0.1664 | 15 credits = S$0.15 | 9.9% below list | **66.7%** |

The 20% target and the "below official" bound are mutually exclusive for
standard H3: at this cost the maximum margin below the official list is
15.0% (768P) / 14.6% (2K). The owner chose margin, so standard H3 must carry no
discount label and must not be marketed as cheaper than the official API.

**Margin warning and subscription-unit economics:** Output-only comparisons are not end-to-end contribution profit. The 11/18 rates clear 20% only at face value and on the S$89 plan; the S$249 grant realizes ~19%, and reference images, retries, payment fees and FX can erode that further. Separately, a subscriber who pays **S$149 for 18,000 credits** (proposed in an earlier review comment, not approved) supplies only S$0.00828 revenue/credit: standard H3 would earn ~4.4% and even the Lite tier's S$0.09 per 768P second would net ~8% there. The single wallet must check realized revenue per credit across all funded sources including discounted plans and topup bonuses (audit §E); under credit-only billing the fix is to refuse that plan or renegotiate supplier rates, **not** to gate SKUs per plan. Revalidate FX and official list prices, and never claim blanket "official 30–70% off".

**Credit-only billing rule (owner decision, 2026-10-27).** Every entitlement,
plan and charge is expressed only as a credit deduction from the single pool
(D5). There is no per-plan SKU gating, no second wallet and no separate
entitlement object. Consequently the **minimum effective SGD/credit across all
offered plans and packs must clear the most expensive active SKU's provider
cost**; a plan that cannot is not offered. If a Business plan exists, its
benefit is a larger credit grant (and its price), not a separate feature
entitlement.

**Plan catalogue — proposals for owner sign-off, none activated.** The
currently implemented rows are shown for contrast; the review-proposed rows are
not approved and must not ship until the effective-SGD-per-credit check passes
for every active SKU.

| plan | current | review-proposed (NOT approved) | credits/month | effective S$/credit |
|---|---:|---:|---:|---:|
| Starter | S$12 | S$12 | 1,200 | 0.010000 |
| Plus | S$89 | **S$49** | 5,500 | **0.008909** |
| Pro | S$249 | **S$149** | 18,000 | **0.008278** |
| Business | — | S$249 (larger credit grant only; no separate entitlement) | 25,500 | 0.009765 |

Under the credit-only rule the S$49/5,500 and S$149/18,000 rows are
**rejected**: at the standard H3 11/18 rates they realize S$0.0980 / 0.1604 and
S$0.0911 / 0.1490 per second, i.e. 11.2%/11.4% and 4.4%/4.6% output margin, and
no integer rate reaches 20% on them. With standard H3 repriced to 11/18,
face-value topup earns 20.9%/21.1%, S$89/9,000 earns 20.0%/20.2%, and
S$249/25,500 earns 19.0%/19.2%; the catalogue therefore clears the 20% target
on the first two rows and lands just below it on Business. The Lite tier at the
original 9/15 still earns ~66% on every plan row and remains the better
default. Choosing standard H3 instead means accepting that it is no longer
cheaper than the official list; that trade-off is approved but must not be
advertised as a discount. The S$249 row, if kept as "Business", is only a larger
credit grant — do not invent team/priority entitlements that the credit-only
rule cannot express.

**Annual billing:** 11 months paid for 12 served (one month free), replacing
the earlier ten-month proposal, pending owner approval and contribution-margin
validation. **Signup trial:** a single small credit grant, 3-day expiry, exact
amount set by the owner, granted once per verified identity; repeated signups,
device/account reuse and expired-grant re-requests must be blocked, and the
grant counts as zero realized revenue in every margin check.

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

`SkillCatalogEntry.Credits` is replaced by `Caps`, expressed in integer units
so no float reaches a balance:

```go
type OutputCaps struct {
    MaxTextTokens  int64
    MaxImages      int
    MaxVideoMilli  int64 // 10_000 = 10 s
    MaxVideoHeight int   // the highest configured SKU height; 0 = no video
}
```

A per-request selection (`imageCount`, `videoSeconds`, `videoMode`) is clamped
to these caps and priced by the SKU table in §2. The catalog cap is a bound,
not the reservation: a request for 5 s of 768P reserves 5 s of 768P, not 10 s
of 2K.

For an **open-ended/conversational skill** that cannot express its output shape
server-side, the visible budget is the full cap; the submit surface states that
the cap covers the whole conversation, and the drain/over-cap behaviour is
documented where the user chooses to continue.

Create-time flow (`CreateAuroraGeneration`):

1. Resolve the *approved* model+variant+input mode; if HOLD, unquoted, expired
   or uneconomic for the user's funding source, reject before reserve. For
   enabled requests, compute `cap_micro` from the **selected** output and all
   chargeable inputs/attempt limits at a frozen rate version, using the same
   rate card the pre-submit quote returned and the same one settlement uses.
2. Balance `< cap_micro` → 402 `insufficient_credits` with `requiredCredits`,
   `availableCredits`, and `shortfallCredits` in the body (the wire uses the
   handler's existing camelCase convention).
3. Otherwise `Reserve(cap_micro)` exactly as today, with the generation id as
   reference.
4. The cap is written into the task brief so the model knows its budget. The
   brief instruction is advisory; the charge does not depend on it.

A separate read-only `POST /api/aurora/generations/quote` (same body as create,
no side effects) returns the same `requiredCredits` / `availableCredits` /
`shortfallCredits`, so the composer can show the number before submit; create
recomputes it transactionally rather than trusting the client.

A skill with `MaxImages = 0` and `MaxVideoMilli = 0` is text-only and remains
**commercially HOLD** until actual provider input/output/cache usage, supplier
costs and a defensible under-official retail price can be quoted; no live
ceiling should be reserved for such a skill prematurely.

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
2. Compute one line per (kind, rate tier): `quantity_milli`, `unit_rate_micro`,
   `amount_micro`, using integer microcredit arithmetic (§1).
3. `actual_micro = Σ amount_micro`, capped at `credits_reserved`.
4. Write `aurora_generation_charge` rows.
5. Write the internal cost record for the run (actual provider/model, token and
   media usage, attempts and cumulative provider cost) — see §5.1.
6. If `actual_micro < reserved`, `Refund(reserved - actual)` with reference
   `settle:<generationID>` — idempotent, so a replayed terminal event is a
   no-op.
7. Record `credits_charged = actual_micro`.

### 5.1 Internal provider cost accounting

Customer billing stays output-based, but the platform must know what each run
cost, or the pricing rules cannot be enforced. On every terminal event
(success **and** failure, including chargeable failed attempts), record one
internal row: resolved provider and exact model id, input/output/cache/thinking
tokens, video seconds and resolution, reference-image/video counts, retry
attempts, cumulative provider cost in micro-SGD, the FX and quote revision used,
and the generation id. This record is never exposed as a customer charge.

Settlement compares the run's realized revenue (the actual charge, in the
currency the credits were funded at) with its recorded provider cost and blocks
or flags any run whose contribution margin is nonpositive. Model identity is
pinned at dispatch: the model the server chose is written into the task payload
and the runtime's provider-run record must match it. A mismatch is recorded,
settled at the reserved ceiling (never refunded as if a cheaper model ran), and
counted as `aurora_model_mismatch`; it is a release blocker, not a pricing
choice made by the agent.

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

**No legacy seeding.** Aurora has no paying customers yet; every existing
balance, subscription and pack is test data. The wallet phase starts from zero:
the destructive cleanup in §10 removes those rows before launch instead of
seeding `adjustment` batches from them. The invariant `balance == Σ remaining`
therefore holds from an empty ledger, not from a converted history.

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
- `5008_aurora_generation_rate_version` — add `rate_version text` and
  `output_selection jsonb` to `aurora_generation`, so a settled run keeps the
  card and the selected SKU it was priced with.
- `5009_aurora_generation_cost` — internal provider-cost record per run
  (never a customer charge).
- `5010_aurora_generation_cost_generation_idx` — concurrent index.

`aurora_generation_charge`:

| column | meaning |
|---|---|
| `generation_id`, `workspace_id`, `user_id` | attribution |
| `kind` | `text` \| `image` \| `video` |
| `model` | the provider model id the rate came from |
| `quantity_milli` | integer: images × 1000, video milliseconds, text tokens × 1000 |
| `unit_rate_micro` | the rate applied, micro-credits per 1000 units |
| `amount_micro` | `ceil(quantity_milli × unit_rate_micro / 1000)`, exact microcredits, never rounded up to a whole credit |
| `measured` | the measurement backing `quantity_milli` (height, duration, tokens) |
| `rate_version` | the card version |
| `over_cap` | boolean |

`aurora_generation_cost` (internal, not billable):

| column | meaning |
|---|---|
| `generation_id`, `workspace_id`, `user_id` | attribution |
| `provider`, `model` | exact dispatched provider and model id |
| `input_tokens`, `output_tokens`, `cache_read_tokens`, `cache_write_tokens`, `thinking_tokens` | provider-reported usage |
| `video_milli`, `video_height`, `image_count`, `reference_count` | media usage |
| `attempts`, `chargeable_attempts` | retry accounting |
| `provider_cost_micro_sgd`, `fx`, `supplier_quote_revision` | cost basis |
| `model_mismatch` | the runtime ran a different model than the one priced |

### 8. API changes

- `GET /api/aurora/skills` — each entry gains `maxCredits` (the maximum
  reservation, in credits), `caps`, `pricingStatus` and `meteredEnabled`;
  `credits` stays as a deprecated alias of `maxCredits` so an installed desktop
  client keeps rendering a number. HOLD/PROVISIONAL entries report
  `meteredEnabled: false` and are not selectable for a paid run.
- `POST /api/aurora/generations/quote` — read-only; same body as create
  (including the selected output), returns `requiredCredits`,
  `availableCredits`, `shortfallCredits` and the resolved `rateVersion`.
- `POST /api/aurora/generations` — accepts the selected output fields; 402 body
  carries `requiredCredits` / `availableCredits` / `shortfallCredits`.
- `GET /api/aurora/generations/{id}` — gains `charges[]` (`kind`, `model`,
  `sku`, `quantityMilli`, `unitRateMicro`, `amountMicro`, `measured`,
  `overCap`) and `rateVersion`.
- `GET /api/aurora/billing/balance` — gains `reservedMicro` (money held by
  in-flight generations), `expiresAt` on the per-batch breakdown, and
  `monthlyUsage` split by kind for the current month. `availableMicro` keeps
  meaning "spendable right now"; the account page shows available and reserved
  separately, because a run in flight must not look like a spend.
- `GET /api/aurora/billing/transactions` — each row gains the charge summary
  the transaction screen renders.
- The wire follows the handler's existing camelCase convention; the client
  schemas mirror it. Zod schemas in `packages/core/api/schema.ts` (or the
  aurora schema module) are updated with defaults and malformed-response tests.

### 9. UI changes

Three surfaces, matching the success criteria:

- **Skill directory and composer** (`skill-directory.tsx`,
  `generation-composer.tsx`): "最多 X 积分" replaces "X 积分". The insufficient
  balance dialog shows the shortfall, not just a failure.
- **Result page** (`generation-detail.tsx`): a charge block — one row per line
  (`3 张图 × 4 积分（seedream-5-0-lite）`, `1 段视频 768P × 5 秒 × 9 积分/秒
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

### 10. Existing balances: cleanup, not conversion

**Decision (2026-10-27): there are no legacy users.** Aurora has not taken real
money; every balance, subscription, pack and ledger row in the current
environment is test data. The old `1 USD = 1000 credits` unit therefore has no
customer value to preserve, and no conversion ratio is needed.

- **No `adjustment` batches are seeded** from existing balances (see §6).
- **Runtime data is cleared before launch**, not migrated: test balances, test
  subscriptions, packs and ledger rows are removed, and any in-flight
  `legacy` generation is failed/refunded to the test balance first. The wallet
  and rate card start empty.
- **This is a destructive operation and needs explicit authorization** when it
  is actually run in an environment. It is not part of phase 1 and not part of
  this documentation PR.
- **No migration fixtures or rollback tests are required** for the old unit.
  The only precondition is a pre-launch check that the environment contains no
  real paid balance; if that check ever fails, stop and re-open this decision
  instead of inventing a ratio.

The current entitlement path keeps working on today's numbers until that
cleanup runs.

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
- **Same approved SKU rate card on both sides.** Reservation and settlement call one frozen version of the model/variant rate function; a second implementation, unapproved fallback or model-switch during execution is a bug.
- **Effective-credit profitability.** Check the lowest real SGD received per credit among all allowed membership and pack funding sources; reject any SKU/plan combination with nonpositive contribution margin, even if its nominal S$0.01-credit price looks viable.
- **No sale without a cost record.** A run that cannot record its provider/model and usage cost is not metered-sold; the SKU is disabled instead. A model mismatch settles at the reserved ceiling and raises an alarm.
- **Quote equals settlement.** The pre-submit quote, the reservation and the settlement price line use one frozen rate version and one arithmetic; a test asserts they agree for the same selection.

## Testing

- Go tests: exact fixed-point microcredits for small text/video quantities; video requested-tier vs measured-dimension boundaries (719/720/768/1079/1080/1439/1440); missing/HOLD/expired provider SKUs fail before reservation; per-model official-versus-supplier guards; supplier FX movement and discounted wallet credits cannot yield a negative permitted margin.
- Quote/reservation/settlement agreement for the same selected output; an expired rate record fails closed; a 1080P artifact is never settled at the 2K rate.
- Provider cost tests: a multi-attempt job records every attempt and its cumulative cost; a model mismatch settles at the ceiling and flags; a run whose cost exceeds its realized revenue is flagged and cannot be silently sold again.
- Handler tests for the commit-time measurements with real fixture files
  (a tiny PNG, a 1-second MP4) — measured values come from the file, and an
  agent-supplied `metadata.duration` never wins.
- Settlement tests: actual < reserved refunds the difference; actual > reserved
  charges the reservation and flags `over_cap`; a replayed completion does not
  double-charge; a failed run refunds in full; a charge line is idempotent on
  replay.
- No legacy-balance conversion tests: §10 is a pre-launch cleanup of test data,
  not a migration. The cleanup itself verifies an empty wallet before the
  first metered sale; that check is not part of phase 1.
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

## Cost basis (HopBase ToB quote 2026-10-26, valid to 2026-11-25)

The provider is HopBase (`api.hop-base.com`), not a direct Volcengine
integration. The 2026-10-26 sheet is denominated in **USD** (it supersedes the
2026-10-12 CNY-denominated quote) and keeps the settlement terms this design
assumes: video is billed only for finished output, and a failed generation is
not charged.

Previously envisioned fixture fleet was `deepseek-v4.1-flash` for text, `seedream-5-0-lite` for image, and MiniMax H3 for video. **DeepSeek text is still commercial HOLD** at the quoted buy rate vs the OEM off-peak API list; no paid default text SKU may be silently selected. Per-model/variant activation is governed by the new pricing audit.

At 1 USD ≈ S$1.28 and 100 credits = S$1 (`credits = US$ × 128`):

| line | HopBase price | cost / credits |
|---|---|---|
| image, `seedream-5-0-lite` | $0.0238/image | 3.05 credits |
| image, `seedream-5-0-pro` ≤2.61 MP | $0.0306/image | 3.92 credits |
| image, `nano-banana-2.1` 1K / 2K / 4K | $0.0153 / 0.023 / 0.0517 per image | 1.96 / 2.94 / 6.62 credits |
| image, `gemini-3-pro-image` 1K–4K (one-price, includes input) | $0.0588/image | 7.53 credits |
| image, `gemini-3.1-flash-image` | $0.0588/image | 7.53 credits |
| video, MiniMax H3 **standard** 768P / 2K | $0.068 / 0.111 per s | 8.70 / 14.21 credits/s |
| video, MiniMax H3 **self-deployed Lite** 480P / 768P / 2K | $0.015 / 0.024 / 0.039 per s | 1.92 / 3.07 / 4.99 credits/s |
| video, MiniMax H3 **self-deployed** 768P / 1080P / 2K / 4K | $0.0331 / 0.0441 / 0.0551 / 0.0662 per s | 4.24 / 5.64 / 7.05 / 8.47 credits/s |
| video, MiniMax H3-Max 480P / 768P | $0.0425 / 0.068 per s | 5.44 / 8.70 credits/s |
| video, Kling v3 omni silent 720P / 1080P | $0.0662 / 0.0882 per s | 8.47 / 11.29 credits/s |
| video, Kling v3 omni with audio 720P / 1080P | $0.0882 / 0.11 per s | 11.29 / 14.08 credits/s |
| video, Grok Imagine 480P / 720P / 1080P | $0.048 / 0.084 / 0.15 per s | 6.14 / 10.75 / 19.20 credits/s |
| text, `claude-haiku-5-5` input / output (≤100K prompt) | $0.0279 / 0.14 per M tokens | 3.57 / 17.92 credits per M |
| text, `claude-sonnet-5-5` input / output | $0.559 / 2.79 per M tokens | 71.55 / 357.12 credits per M |
| text, `deepseek-v4.1-flash` input / output | $0.176 / 0.706 per M tokens | 22.53 / 90.37 credits per M |

Three consequences the owner must price around:

- **The server cannot tell which model produced an artifact.** It measures
  bytes — count, resolution, duration — and the manifest's model claim is
  agent-written. So one rate must cover every model a skill may reach, and each
  skill's brief has to pin the model it runs. A skill that silently picks
  `gemini-3-pro-image` costs 2.5× the rate set for `seedream-5-0-lite`.
- **MiniMax changed its quoted cost on 2026-10-26, forcing standard H3 above the official list.** The official H3 API lists 768P at US$0.08/s and 2K at US$0.13/s; the supplier now charges US$0.068/US$0.111, leaving only 3.3%/5.3% at 9/15 credits. Reaching the owner's 20% target needs 11/18 credits/s, which is 7.4%/8.2% **above** the official list — so standard H3 can no longer be described as cheaper than the official API. The self-deployed Lite tier (US$0.024/US$0.039) earns ~66% at 9/15 and stays below the official list, making it the intended default once its availability and product limits are confirmed. Do not claim a discount against MiniMax's consumer subscription effective rate or other models without verifying the comparable use case; separately billed inputs or retries may erase margin.
- **The old text rate is neither competitively priced nor safe.** At S$0.01/1K output (S$10/M), generic DeepSeek/Haiku text would be above official API output rates; long inputs could also cost more than short outputs earn. DeepSeek's own off-peak Flash list ($0.147/$0.59 per M) still undercuts its HopBase quote ($0.176/$0.706). Claude input and output costs are now quoted, but cache read/write and >100K prompt tiers are not, so those text SKUs remain HOLD.

The quote expires 2026-11-25; the card versions make a re-quote a config change
rather than a code change.

## Open questions for the owner

**Solved (2026-10-27):** existing balances need no conversion — there are no
paying users and the data is test-only; §10 now defines a pre-launch cleanup
instead.

1. **The signup bonus.** The reference gives new users free credits valid three days. Keep a small expiry-limited bonus, or drop it? If kept: exact credit amount, the abuse controls, and the rule that it counts as zero realized revenue in every margin check.
2. **Approve actual supplier quote coverage and active model list.** Confirm HopBase mapping, SKU-specific input costs, official comparator, rate validity, contribution margin floor and treatment of plan/pack bonuses before enabling any of the new output-only candidates.
3. **Final plan price/credit numbers.** The credit-only rule is fixed (D5,
   §2): no per-plan SKU gating. What remains is the final price and credit
   amount per plan and whether a S$249 "Business" row (larger grant only)
   exists. S$49/5,500 and S$149/18,000 are rejected under the current supplier
   cost; revisit only after a renegotiated quote. Annual 11/12 is part of the
   same commercial sign-off.
4. **Cost-accounting fields.** Confirm the exact provider usage signals the
   runtime can report per attempt (tokens, cache, media counts, model id), since
   an unreported field disables the affected SKU rather than falling back to an
   estimate.

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
