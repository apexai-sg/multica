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

- **Commercial rule:** compare the exact same model, resolution, audio mode, token direction, context tier, API region and pay-as-you-go channel. For an eligible SKU, require **supplier actual cost < Apex retail < official standard API list**, in matching units. Where equality, absent quotes, unverified specifications, or other provider charges undermine this rule, **disable the SKU rather than invent a price**.
- **Currencies:** fixed reference FX from PR #38 (USD 1 = SGD 1.28); 100 credits = SGD 1. The 2026-10-26 sheet is all-USD, so `S$ = US$ × 1.28` and `credits = US$ × 128`. Prices are indicative; checkout must use a versioned SGD rate card refreshed against procurement/official data and FX.
- **Scope:** official **standard online pay-as-you-go API list** only; not consumer subscriptions, negotiated contracts, resource packs, promotional coupons, batch discounts, off-peak/peak arbitrage, or other resellers. Such alternatives can be cheaper. Never advertise "cheaper than official" without naming this comparator and qualifying the variant.
- **Profit metric:** gross margin shown is `(Apex output price − HopBase quoted output cost) / Apex output price`. It is **not net profit**: input tokens, reference images/videos, retry charges, payment fees, infrastructure, tax, fraud and FX changes may consume it. A price above the supplier's *single output* cost is not proof that a whole generation makes money. Published retail should be evaluated at the **lowest actual realized revenue per credit**, accounting for subscription grants and pack bonuses (see section E).
- **Supplier basis:** HopBase ToB quote dated **2026-10-26**, valid through
  **2026-11-25**. Video is billed only on delivered output; failed generations
  are not charged.
- **Status:** `CANDIDATE` = arithmetic meets the two bounds for the **specified output-only** SKU; `PROVISIONAL` = official tier data is corroborated but the official developer page could not be text-verified, or the tier is a self-deployed variant without a direct official comparator; `HOLD` = no defensible commercial price under the owner's constraints or missing cost component. Even CANDIDATE requires the common launch gates below.

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
| MiniMax H3 768P | US$0.068/s = **S$0.08704/s** | US$0.08/s = S$0.1024/s | **9 credits/s = S$0.09/s** | 12.1% less | **3.3%** | **HOLD/REVIEW: post-fee unsafe** |
| MiniMax H3 2K | US$0.111/s = **S$0.14208/s** | US$0.13/s = S$0.1664/s | **15 credits/s = S$0.15/s** | 9.9% less | **5.3%** | **HOLD/REVIEW: post-fee unsafe** |
| H3 self-deployed Lite 480P | US$0.015/s = **S$0.0192/s** | US$0.05/s = S$0.064/s | 3 credits/s = S$0.03/s | 53.1% less | **36.0%** | CANDIDATE, pending availability |
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

- **Standard H3 is no longer viable at 9/15 credits/s.** The 2026-10-26 cost
  rose to US$0.068/0.111 per second (S$0.08704/0.14208), leaving only
  3.3%/5.3% output margin before reference inputs, retries, fees and FX. Under
  the audit's own rule (nonpositive or unsafe post-fee margin blocks the SKU),
  these two rows are **HOLD/REVIEW**, not candidates. The cheaper H3
  self-deployed Lite tier (US$0.024/0.039) restores ~66% margin at the same
  9/15 retail and is the preferred default **if the runtime can use that tier**
  and its product limits (text/image-to-video, 16:9/9:16, ≤6 reference images,
  batch output) are acceptable. The self-deployed tier (US$0.0331/0.0551) is a
  middle option at ~53%.
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
other families. That removes one HOLD reason but not all: cache read/write and
long-context tiers are still missing for several models, and the runtime must
report authoritative provider token usage. DeepSeek is still commercially
blocked because its supplier price exceeds the official off-peak list.

| Model / scope | HopBase in/out per M | Official std in/out per M | Apex candidate (in/out, per M) | Of official | Output gross margin | Status |
|---|---:|---:|---:|---:|---:|---|
| `claude-haiku-5-5`, ≤100K prompt | US$0.0279 / 0.14 | US$0.10 / 0.50 | **8 / 40 credits** | 37.5% less both | **55.4% / 55.2%** | **HOLD** until cache + >100K prompt rates and real metering |
| `claude-sonnet-5-5` | US$0.559 / 2.79 | US$2 / 10 | **150 / 750 credits** | 41.4% less both | **52.3% / 52.4%** | **HOLD** until cache + long-context rates |
| `claude-fable-5-1` (optional) | US$2.79 / 14 | US$10 / 50 | 640 / 3,200 credits (50%) | 50% less | 50% | HOLD pending text metering |
| `claude-opus-5-5` (optional) | US$1.12 / 5.59 | US$4 / 20 | 256 / 1,280 credits (50%) | 50% less | 50% | HOLD pending text metering |
| `deepseek-v4.1-flash` / `-1017` | US$0.176 / 0.706 | peak US$0.294/1.18; off-peak US$0.147/0.59 | **NO ALWAYS-CHEAPER RATE** | — | — | **HOLD**: supplier > official off-peak |
| all other text families (GPT, Gemini, GLM, Grok, Kimi, Qwen) | quoted | quoted | not priced here | — | — | HOLD until per-model usage metering |

- Claude official output is **US$2.50/M for prompts >100K**, not the ≤100K
  tier; the supplier long-context cost is missing. Cache-read prices are given
  only for fable/opus; Haiku and Sonnet cache write/read are still unquoted.
  Treat the 8/40 and 150/750 candidates as provisional arithmetic, not a live
  price.
- DeepSeek's supplier output (US$0.706) and input (US$0.176) both exceed the
  official **off-peak** list (US$0.59 output, US$0.147 input). A 20% margin
  would require a supplier price below US$0.472/M output and US$0.118/M input;
  the current sheet does not provide it. Keep HOLD.
- **DO NOT** sell any text output at the old generic `1 credit / 1K` rate: that
  is S$10/M and exceeds the official standard output rate for Claude Haiku and
  DeepSeek; it also ignores billable input. Per-model **input, output, cache
  read/write, long context and batch** tariffs are required.
- For text, **never ceil each charge line to a full credit.** Per-token fees are
  sub-credit. Debit measured provider token usage in integer microcredits;
  track input/output/cache/thinking usage and billable retries. Estimating
  output tokens from delivered Unicode text is not an authoritative meter.
  `CountTextTokens` is a non-billing UI estimate only.
- Text generation must stay on legacy behavior behind a **non-public flag** or
  be disabled for new metered sales until complete provider usage/cost
  provenance is implemented.

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

| Plan / scenario | Effective SGD/credit | H3 768P at 9 cr/s vs **S$0.08704** cost | H3 2K at 15 cr/s vs **S$0.14208** cost | Standard H3 viability |
|---|---:|---:|---:|---|
| Face-value topup, S$1/100 | S$0.010000 | S$0.09000 (**3.3%**) | S$0.15000 (**5.3%**) | unsafe |
| Current S$89/9,000 | S$0.009889 | S$0.08900 (**2.2%**) | S$0.14833 (**4.2%**) | unsafe |
| Current S$249/25,500 | S$0.009765 | S$0.08788 (**1.0%**) | S$0.14647 (**3.0%**) | unsafe |
| Proposed S$49/5,500 | S$0.008909 | **S$0.08018 (loss)** | **S$0.13364 (loss)** | fails |
| Proposed S$149/18,000 | S$0.008278 | **S$0.07450 (loss)** | **S$0.12417 (loss)** | fails |

- The S$49/5,500 and S$149/18,000 rows remain **rejected** under the credit-only
  rule.
- More importantly, **every current plan now fails the safe-margin test for
  standard H3**: even face-value topup leaves 3.3%/5.3% before fees. The fix is
  not another price increase (the 10/16-credit ceiling is already near the
  official list); it is to **use the H3 self-deployed Lite or self-deployed
  tier**, or renegotiate the supplier price.
- With H3 Lite (US$0.024/0.039), the same 9/15 retail earns ~66% output margin
  at every plan row above, so the plan catalogue and the Lite tier can coexist.
  The Business row remains only a larger credit grant.

## F. Engineering acceptance and release gates (PR #38)

1. **Rate book keyed by exact provider model + SKU variant:** image model, size and count; video model, resolution, audio, reference inputs and duration; text model, input/output tokens, context tier and caching. Every rate has official URL, supplier quote revision, FX, `effective_at`, `expires_at` and approval status. Unknown/model-mismatched/unapproved SKUs **fail closed before payment**, not silently at zero or at another model's price.
2. **Cost & comparator validation:** `provider_cost_SGD < realized_public_sale_SGD < matching_official_API_list_SGD` for the chosen mode at effective FX and the most discounted permitted funding source, plus an operator-set minimum **contribution** margin after fees/retries. A nonpositive or unsafe post-fee margin blocks the SKU even when the two simple bounds pass. Suppress discount labels when batch/promotional/off-peak/region tiers cannot support the claim.
3. **Input billing:** charge/reserve all paid input modalities and true provider usage (tokens, image reference count, input video duration, thinking, tools and retries). If provider metering is unavailable, disable affected modes.
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
