# Aurora / Apex AI cross-provider retail pricing audit

**Date:** 2026-10-27. **Status:** owner-requested pricing proposal on PR #38.
Prices are NOT deployed. This audit supersedes the old assumption "3× HopBase
cost" and any proposed blanket "official 30–70% discount". The subject is the
supplier-quoted SKUs, **not every model advertised in HopBase's much larger
public catalogue**.

**Cost basis updated 2026-10-27:** the owner supplied the HopBase ToB quote
dated **2026-10-26** (valid 30 days, to **2026-11-25**), which supersedes the
2026-10-12 quote used by the earlier revision. The new sheet prices everything
in **USD** and adds new low-cost video tiers (H3 self-deployed / self-deployed
Lite), lower Gemini image costs, and input+output Claude costs. All numbers
below are recomputed from that sheet.

## Policy and comparison basis

- **Commercial rule:** compare the exact same model, resolution, audio mode, token direction, context tier, API region and pay-as-you-go channel. For an eligible SKU, require **supplier actual cost < Apex retail < official standard API list**, in matching units. Where equality, absent quotes, unverified specifications, or other provider charges undermine this rule, **disable the SKU rather than invent a price**. **Owner override (2026-10-27):** standard MiniMax H3 is priced above the official list to reach a 20% output margin (section B). It is a deliberate exception, not a pattern: it must carry no discount claim and must not be cited as evidence that other SKUs may break the bound.
- **Currencies:** fixed reference FX from PR #38 (USD 1 = SGD 1.28); 100 credits = SGD 1. The 2026-10-26 sheet is all-USD, so `S$ = US$ × 1.28` and `credits = US$ × 128`. Prices are indicative; checkout must use a versioned SGD rate card refreshed against procurement/official data and FX.
- **Scope:** official **standard online pay-as-you-go API list** only; not consumer subscriptions, negotiated contracts, resource packs, promotional coupons, batch discounts, off-peak/peak arbitrage, or other resellers. Such alternatives can be cheaper. Never advertise "cheaper than official" without naming this comparator and qualifying the variant.
- **Profit metric:** gross margin shown is `(Apex output price − HopBase quoted output cost) / Apex output price`. It is **not net profit**: input tokens, reference images/videos, retry charges, payment fees, infrastructure, tax, fraud and FX changes may consume it. A price above the supplier's *single output* cost is not proof that a whole generation makes money. Published retail should be evaluated at the **lowest actual realized revenue per credit**, accounting for subscription grants and pack bonuses (see section E).
- **Supplier basis:** HopBase ToB quote dated **2026-10-26**, valid through
  **2026-11-25**. Video is billed only on delivered output; failed generations
  are not charged.
- **Status:** `CANDIDATE` = arithmetic meets the two bounds for the **specified output-only** SKU (except the owner-overridden standard H3 rows, which meet the margin floor but sit above the official list); `PROVISIONAL` = official tier data is corroborated but the official developer page could not be text-verified, or the tier is a self-deployed variant without a direct official comparator; `HOLD` = no defensible commercial price under the owner's constraints or missing cost component. Even CANDIDATE requires the common launch gates below.

## A. Image generation — proposed output unit rates

| Model / scope | HopBase 2026-10-26 | Official standard API | Apex candidate | Of official | Output gross margin | Status |
|---|---:|---:|---:|---:|---:|---|
| `seedream-5-0-lite`, per image | US$0.0238 = **S$0.030464** | US$0.035 = S$0.0448 (sheet); Ark list ¥0.22 ≈ S$0.0418 | **4 credits/image = S$0.04** | 4.3–10.7% less | **23.8%** | CANDIDATE; no FX cushion |
| `seedream-5-0-pro`, ≤2.61 MP | US$0.0306 = **S$0.039168** | US$0.045 = S$0.0576 (sheet); Ark list ¥0.30 ≈ S$0.057 | **5 credits/image = S$0.05** | 12.3–13.2% less | **21.7%** | CANDIDATE |
| `nano-banana-2.1` 1K | US$0.0153 = **S$0.019584** | US$0.0336 = S$0.043008 | **3 credits/image = S$0.03** | 30.2% less | **34.7%** | CANDIDATE |
| `nano-banana-2.1` 2K | US$0.023 = **S$0.02944** | US$0.0504 = S$0.064512 | **4 credits/image = S$0.04** (3 credits = 1.9% margin) | 38.0% less | **26.4%** | CANDIDATE at 4 credits |
| `nano-banana-2.1` 4K | US$0.0517 = **S$0.066176** | US$0.113 = S$0.14464 | **8 credits/image = S$0.08** | 44.7% less | **17.3%** | CANDIDATE |
| `gemini-3-pro-image` 1K/2K (one-price, includes input tokens) | US$0.0588 = **S$0.075264** | US$0.134 = S$0.17152 | **15 credits/image = S$0.15** | 12.5% less | **49.8%** | CANDIDATE; cost dropped from US$0.0941 |
| `gemini-3-pro-image` 4K (same one-price) | US$0.0588 = **S$0.075264** | US$0.24 = S$0.3072 | **15 credits/image = S$0.15** | 51.2% less | **49.8%** | CANDIDATE |
| `gemini-3.1-flash-image` | US$0.0588 = **S$0.075264** | US$0.0672 = S$0.086016 | **8 credits/image = S$0.08** (max under official) | 7.0% less | **5.9%** | **HOLD**: post-fee unsafe |

**Important:** Google has cheaper **Batch** prices. Volcengine's other
model-access channel lists different Seedream tariffs: ensure the actual SKU
and channel are Ark. The Gemini 3 Pro Image one-price includes input tokens;
Nano Banana may bill input separately, so its output-only margin is optimistic.
The 2026-10-26 Gemini 3 Pro Image cost (US$0.0588) is far below the previous
2026-10-12 figure (US$0.0941); the 15-credit price now carries ~50% output
margin, so a lower retail price is possible but not approved.

## B. Video generation — output seconds only

| Model / scope | HopBase 2026-10-26 | Official reference | Apex candidate | Of official | Output gross margin | Status |
|---|---:|---:|---:|---:|---:|---|
| MiniMax H3 768P | US$0.068/s = **S$0.08704/s** | US$0.08/s = S$0.1024/s | **11 credits/s = S$0.11/s** | **7.4% ABOVE** | **20.9%** | CANDIDATE by owner override (2026-10-27); no discount claim |
| MiniMax H3 2K | US$0.111/s = **S$0.14208/s** | US$0.13/s = S$0.1664/s | **18 credits/s = S$0.18/s** | **8.2% ABOVE** | **21.1%** | CANDIDATE by owner override (2026-10-27); no discount claim |
| H3 self-deployed Lite 480P | US$0.015/s = **S$0.0192/s** | US$0.05/s = S$0.064/s (**H3-Max 480P**, nearest comparator; no official Lite 480P rate) | 3 credits/s = S$0.03/s | 53.1% less | **36.0%** | CANDIDATE, pending availability |
| H3 self-deployed Lite 768P | US$0.024/s = **S$0.03072/s** | US$0.08/s = S$0.1024/s | **9 credits/s = S$0.09/s** | 12.1% less | **65.9%** | CANDIDATE, pending availability |
| H3 self-deployed Lite 2K | US$0.039/s = **S$0.04992/s** | US$0.13/s = S$0.1664/s | **15 credits/s = S$0.15/s** | 9.9% less | **66.7%** | CANDIDATE, pending availability |
| H3 self-deployed 768P | US$0.0331/s = **S$0.042368/s** | US$0.08/s (H3 standard) | **9 credits/s = S$0.09/s** | 12.1% less | **52.9%** | PROVISIONAL: self-deployed tier, no direct official comparator |
| H3 self-deployed 1080P | US$0.0441/s = **S$0.056448/s** | **no official H3 1080P rate** | 14 credits/s = S$0.14/s | — | **59.7%** | **HOLD**: no official comparator |
| H3 self-deployed 2K | US$0.0551/s = **S$0.070528/s** | US$0.13/s (H3 standard) | **15 credits/s = S$0.15/s** | 9.9% less | **53.0%** | PROVISIONAL |
| H3 self-deployed 4K | US$0.0662/s = **S$0.084736/s** | **no official H3 4K rate** | 16 credits/s = S$0.16/s | — | **47.0%** | **HOLD**: no official comparator |
| H3-Max 480P | US$0.0425/s = S$0.0544/s | US$0.05/s = S$0.064/s | 6 credits/s = S$0.06/s | 6.3% less | 9.3% | **HOLD: thin** |
| H3-Max 768P | US$0.068/s = S$0.08704/s | US$0.08/s = S$0.1024/s | 9 credits/s = S$0.09/s | 12.1% less | 3.3% | **HOLD** |
| Kling v3 Omni, silent/no reference video, 720P | US$0.0662/s = **S$0.084736/s** | US$0.0882/s = S$0.112896/s | **10 credits/s = S$0.10/s** | 11.4% less | **15.3%** | **PROVISIONAL** official page unverified |
| Kling v3 Omni, silent/no reference video, 1080P | US$0.0882/s = **S$0.112896/s** | US$0.118/s = S$0.15104/s | **14 credits/s = S$0.14/s** | 7.3% less | **19.4%** | **PROVISIONAL** |
| Kling v3 Omni, **with audio**, 720P/1080P | US$0.0882 / 0.11 per s | US$0.118 / 0.147 | 12 / 15 credits/s (thin) | ~20% less | 5.9% / 6.1% | **HOLD: thin** |
| Grok Imagine video 480P | US$0.048/s = **S$0.06144/s** | US$0.08/s = S$0.1024/s | **8 credits/s = S$0.08/s** | 21.9% less | **23.2%** | CANDIDATE |
| Grok Imagine video 720P | US$0.084/s = **S$0.10752/s** | US$0.14/s = S$0.1792/s | **12 credits/s = S$0.12/s** (11 = 2.3%) | 33.0% less | **10.4%** | CANDIDATE |
| Grok Imagine video 1080P | US$0.15/s = **S$0.192/s** | US$0.25/s = S$0.32/s | **22 credits/s = S$0.22/s** | 31.3% less | **12.7%** | CANDIDATE |

- **Standard H3 cannot reach the owner's 20% margin at 9/15 credits/s, and the
  repriced 11/18 rows break the two-bound rule.** The 2026-10-26 cost is
  US$0.068/0.111 per second (S$0.08704/0.14208): at 9/15 the output margin is
  only 3.3%/5.3%, while a 20% margin needs S$0.1088/0.1776 per second —
  **above** the official list of S$0.1024/0.1664. In other words no price can
  be both ≥20% gross and cheaper than the official API; the maximum margin
  below the official list is 15.0%/14.6%. At the owner's direction the rows are
  priced at **11/18 credits/s (20.9%/21.1%)**, which is 7.4%/8.2% **above** the
  official list, so **no "cheaper than official" claim may be made for standard
  H3** and its discount label must be suppressed. The H3 self-deployed Lite
  tier (US$0.024/0.039) still dominates commercially: the same 9/15 retail
  yields ~66% and stays below the official list, so Lite remains the preferred
  default **if the runtime can use that tier** and its product limits
  (text/image-to-video, 16:9/9:16, ≤6 reference images, batch output) are
  acceptable. The self-deployed tier (US$0.0331/0.0551) is a middle option at
  ~53%.
- MiniMax H3: additional reference images and input video may be billed
  separately; the sheet says the first 5 input images are free. Exclude these
  modes from the output-only price unless also metered.
- Kling: the 2026-10-26 sheet lists both supplier and official per-second
  tariffs, which corroborates the 10/14-credit rows, but the official developer
  page is still not text-verifiable. Keep PROVISIONAL and do not advertise a
  Kling discount until the official page is checked.
- A 1080P artifact must **not** be mapped to the H3 "2K" price solely because
  its height exceeds 1079. Pricing must use the exact provider/requested
  resolution tier, with measured dimensions validating it.
- Seedance is billed per million video tokens (with/without video input), so it
  is not directly comparable to the per-second rows; keep HOLD until the token
  formula and official comparator are verified.

## C. Text generation — amounts per 1,000,000 tokens

The 2026-10-26 sheet now gives **input and output** costs for Claude and most
other families. **The metering question is settled** (owner decision
2026-10-27): text bills from the `task_usage` rows the daemon already reports
for each task, so no new usage protocol is needed. **Default text model
(owner decision 2026-10-27): `deepseek-v4.1-flash`.** Its supplier cost
(US$0.176/0.706 per M) sits above DeepSeek's **off-peak promotional** price but
well below the official **standard (peak) list** (US$0.294/1.18), which is the
comparator this audit's scope uses — off-peak and batch discounts are excluded
from the bound. Earlier revisions marked DeepSeek HOLD by comparing against the
off-peak discount; that contradicted the stated scope and is corrected here.
Claude still blocks on pricing: cache read/write and >100K prompt tiers have no
supplier quote.

| Model / scope | HopBase in/out per M | Official std in/out per M | Apex candidate (in/out, per M) | Of official | Gross margin (in / out) | Status |
|---|---:|---:|---:|---:|---:|---|
| `deepseek-v4.1-flash` / `-1017` (**default**) | US$0.176 / 0.706 | US$0.294 / 1.18 (standard) | **29 / 113 credits** | 22.9% / 25.2% less | **22.3% / 20.0%** | **CANDIDATE**; cache-read billed at the input rate until a cache-hit quote exists |
| `claude-haiku-5-5`, ≤100K prompt | US$0.0279 / 0.14 | US$0.10 / 0.50 | **8 / 40 credits** | 37.5% less both | **55.4% / 55.2%** | **HOLD** until cache + >100K prompt rates are quoted |
| `claude-sonnet-5-5` | US$0.559 / 2.79 | US$2 / 10 | **150 / 750 credits** | 41.4% less both | **52.3% / 52.4%** | **HOLD** until cache + long-context rates |
| `claude-fable-5-1` (optional) | US$2.79 / 14 | US$10 / 50 | 640 / 3,200 credits (50% below list) | 50% less | **44.2% / 44.0%** | HOLD until cache/long-context rates are quoted |
| `claude-opus-5-5` (optional) | US$1.12 / 5.59 | US$4 / 20 | 256 / 1,280 credits (50% below list) | 50% less | **44.0% / 44.1%** | HOLD until cache/long-context rates are quoted |
| all other text families (GPT, Gemini, GLM, Grok, Kimi, Qwen) | quoted | quoted | not priced here | — | — | HOLD: no approved per-model rate |

- Claude official output is **US$2.50/M for prompts >100K**, not the ≤100K
  tier; the supplier long-context cost is missing. Cache-read prices are given
  only for fable/opus; Haiku and Sonnet cache write/read are still unquoted.
  Treat the 8/40 and 150/750 candidates as provisional arithmetic, not a live
  price.
- DeepSeek is the default text model at **29 / 113 credits per M tokens**.
  Both rows are above the supplier cost (22.53 / 90.37 credits per M) and below
  the official standard list (37.63 / 151.04), so they satisfy both bounds with
  22.3% / 20.0% gross margin. They are **above the off-peak promotional price**
  (18.82 / 75.52), so no "always cheaper than DeepSeek" claim is allowed;
  compare only against the standard list and say so. Cache reads have no
  supplier quote, so they bill at the input rate — conservative for us, never
  a loss, and revisited when a cache-hit price arrives. There is no separate
  long-context tier in this quote.
- **DO NOT** sell any text output at the old generic `1 credit / 1K` rate: that
  is S$10/M and exceeds the official standard output rate for Claude Haiku and
  DeepSeek; it also ignores billable input. Per-model **input, output, cache
  read/write, long context and batch** tariffs are required.
- For text, **never ceil each charge line to a full credit.** Per-token fees are
  sub-credit. Debit the `task_usage` token rows in integer microcredits;
  `input_tokens` already excludes cached tokens, and cache read/write bill
  separately at their own rates. Estimating output tokens from delivered
  Unicode text is not an authoritative meter.
  `CountTextTokens` is a non-billing UI estimate only.
- Text generation for models with **no quoted cache or long-context rate** (today:
  every Claude row) must stay on legacy behavior behind a **non-public flag** or
  be disabled for new metered sales until those rates are quoted; the token
  source is no longer the blocker. `deepseek-v4.1-flash` has neither a cache-hit
  nor a long-context tier in this quote, so it bills cache reads at the input
  rate and is **not** blocked by this rule.

## D. Quoted but not yet eligible — no invented margin

The 2026-10-26 sheet prices most families, including GPT-6/5.x, Gemini 4,
GLM 5.3, Grok 4.7, Kimi K3, Qwen 3.8, Seedance, Midjourney, Kling, Qwen image
and audio. Those rows are **not** automatically eligible: each still needs the
exact upstream model mapping, the matching official comparator for the chosen
variant, and real provider usage metering. Do not implicitly price a model at
the default Seedream or H3 rate, and do not publish a discount claim for a row
whose official page is unverified. The live supplier account's active pricing
must be checked against the HopBase console before approval. `nano-banana-2.1`
is a HopBase alias; verify its upstream mapping to
`gemini-nano-banana-2.1` before using Google's list as the comparator.

## E. Subscription-credit economics — CRITICAL (updated for 2026-10-26 cost)

The two-bound tests assume a paid credit is worth **S$0.01**. That is false
whenever a plan or pack grants more than 100 credits per SGD, and Aurora has
one fungible credit pool. **Owner decision 2026-10-27: billing is credit-only**
— every plan is just a credit grant, with no per-plan SKU gating — so a plan
that cannot clear the most expensive active SKU is simply not offered.

| Plan / scenario | Effective SGD/credit | H3 768P at 11 cr/s vs **S$0.08704** cost | H3 2K at 18 cr/s vs **S$0.14208** cost | Standard H3 viability |
|---|---:|---:|---:|---|
| Face-value topup, S$1/100 | S$0.010000 | S$0.11000 (**20.9%**) | S$0.18000 (**21.1%**) | meets 20% |
| Review-proposed S$89/9,000 | S$0.009889 | S$0.10878 (**20.0%**; 19.98% before rounding) | S$0.17800 (**20.2%**) | marginally meets 20% |
| Review-proposed S$249/25,500 | S$0.009765 | S$0.10741 (**19.0%**) | S$0.17577 (**19.2%**) | ~19%, just below |
| Proposed S$49/5,500 | S$0.008909 | S$0.09800 (**11.2%**) | S$0.16036 (**11.4%**) | fails |
| Proposed S$149/18,000 | S$0.008278 | S$0.09106 (**4.4%**) | S$0.14900 (**4.6%**) | fails |

- The S$49/5,500 and S$149/18,000 rows remain **rejected as a policy choice**.
- The 11/18 repricing clears the 20% output target at face value and on the
  S$89 plan, but the S$249 row lands at ~19% and the two rejected rows are far
  below it. At the lowest permitted funding source (the S$249 row), 20% needs
  **12/19** credits/s — S$0.11718/S$0.18554, or 14.4%/11.5% above the official
  list. Reaching it on the two rejected rows is also arithmetically possible:
  **13/20** clears the S$49 row (24.9%/20.3%) and **14/22** clears the S$149
  row (24.9%/22.0%), and a single global 14/22 clears all five rows
  (21.98%–37.83%) — but those prices sit 32–37% above the official list, and
  the owner declined to raise standard H3 further. The rejection is therefore a
  pricing decision, not an arithmetic limit.
- This is why the two H3 tiers should not be the default video path: the Lite
  tier (US$0.024/0.039) earns 65.9%/66.7% at the *old* 9/15 retail at face
  value — ≈59–67% across the plan rows above — stays below the official list, and is the intended default once its
  availability is confirmed.

## F. Engineering acceptance and release gates (PR #38)

1. **Rate book keyed by exact provider model + SKU variant:** image model, size and count; video model, resolution, audio, reference inputs and duration; text model, input/output tokens, context tier and caching. Every rate has official URL, supplier quote revision, FX, `effective_at`, `expires_at` and approval status. Unknown/model-mismatched/unapproved SKUs **fail closed before payment**, not silently at zero or at another model's price.
2. **Cost & comparator validation:** `provider_cost_SGD < realized_public_sale_SGD < matching_official_API_list_SGD` for the chosen mode at effective FX and the most discounted permitted funding source, plus an operator-set minimum **contribution** margin after fees/retries. The only sanctioned exception is standard MiniMax H3, priced above the official list by owner override and therefore barred from any discount claim. A nonpositive or unsafe post-fee margin blocks the SKU even when the two simple bounds pass. Suppress discount labels when batch/promotional/off-peak/region tiers cannot support the claim.
3. **Input billing:** charge/reserve all paid input modalities and the reported `task_usage` rows (input, output, cache read, cache write) for text; image references and input video duration have no row and must be either measured or excluded from the sold SKU. If a generation's usage row is missing at settlement, charge the reserved ceiling and flag it rather than settling free.
4. **Rounding:** ledger uses int64 microcredits and precise fixed-point rate arithmetic, **no full-credit per-charge-line ceiling** on text or millisecond video. Display may round while persisted debits must not.
5. **Quality and billing boundaries:** keep one consistent map from requested/resolved SKU to delivered measured artifact; never charge 1080P as "2K"; preserve per-run max/hold/refund and the pre-launch test-data cleanup. Require per-SKU before/after tests, output quantity examples, and price guardrail tests for supplier changes, official cuts and FX movements.
6. **Staged rollout:** pricing memo first, then rate card and measurements in an **unreleased feature flag**; **reverify the HopBase quote before its 2026-11-25 expiry**; enable candidate SKUs only after verified real request cost/usage tests and user-visible quotations. **No code change is contained in this document-only PR.**

## Source URLs and verification limits (as of 2026-10-27)

- HopBase ToB quote dated **2026-10-26**, valid to **2026-11-25**, supplied by
  the owner (PDF, 11 pages, all-USD, video billed on delivered output only).
  It is a supplier quote, **not** an independent OEM price.
- [Volcengine Ark official model pricing](https://docs.volcengine.com/docs/ark/model-pricing?lang=zh) — Seedream 5 Lite ¥0.22, Pro ≤2.61 MP ¥0.30 single-image mode. The 2026-10-26 sheet lists different USD official-pay prices (US$0.035 / 0.045); use the lower comparator for the discount claim and re-verify the channel.
- [Google Gemini API official pricing](https://ai.google.dev/gemini-api/docs/pricing) — Nano Banana 2.1 US$0.0336/1K standard; Gemini 3 Pro Image US$0.134/1K–2K, US$0.24/4K; lower Batch SKUs.
- [MiniMax official API pricing](https://platform.minimax.io/docs/pricing/overview) and [API token plan](https://platform.minimax.io/subscribe/token-plan?tab=api-enterprise) — H3 US$0.08/768P and US$0.13/2K; H3-Max US$0.05/480P. Self-deployed Lite/self-deployed tiers have no public official comparator on that page.
- [Claude Sonnet 5.5 official](https://platform.claude.com/docs/en/models/sonnet-5-5/overview), [Claude Haiku 5.5 official](https://platform.claude.com/docs/en/models/haiku-5-5/overview) — separate input/output pricing and long-context tiers.
- [DeepSeek official API pricing](https://api-docs.deepseek.com/quick_start/pricing/) — 2026 current `deepseek-flash` peak/off-peak, cache hit/miss, output.
- [Kling official developer pricing (JS rendered)](https://kling.ai/document-api/pricing/base/video), **not directly text-verifiable**; secondary cross-check [ClipCost 2026-10-25 rate capture](https://www.costperclip.com/tools/klingapi) and [Kling API CN tracking](https://vidcost.com/api/kling-api-cn/). Verify directly in the official developer portal before enabling or claiming discount.
