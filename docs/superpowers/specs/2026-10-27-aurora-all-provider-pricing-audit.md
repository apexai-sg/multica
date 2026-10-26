# Aurora / Apex AI cross-provider retail pricing audit

**Date:** 2026-10-27. **Status:** owner-requested pricing proposal on PR #38. Prices are NOT deployed. This audit supersedes the old assumption "3× HopBase cost" and any proposed blanket "official 30–70% discount". The subject is the supplier-quoted SKUs, **not every model advertised in HopBase's much larger public catalogue**.

## Policy and comparison basis

- **Commercial rule:** compare the exact same model, resolution, audio mode, token direction, context tier, API region and pay-as-you-go channel. For an eligible SKU, require **supplier actual cost < Apex retail < official standard API list**, in matching units. Where equality, absent quotes, unverified specifications, or other provider charges undermine this rule, **disable the SKU rather than invent a price**.
- **Currencies:** fixed reference FX from PR #38 (USD 1 = SGD 1.28; CNY 1 = SGD 0.19); 100 credits = SGD 1. Prices are indicative; checkout must use a versioned SGD rate card refreshed against procurement/official data and FX. Round displayed SGD to a sensible precision, **not the actual microcredit settlement**.
- **Scope:** official **standard online pay-as-you-go API list** only; not consumer subscriptions, negotiated contracts, resource packs, promotional coupons, batch discounts, off-peak/peak arbitrage, or other resellers. Such alternatives can be cheaper. Never advertise "cheaper than official" without naming this comparator and qualifying the variant.
- **Profit metric:** gross margin shown is `(Apex output price − HopBase quoted output cost) / Apex output price`. It is **not net profit**: input tokens, reference images/videos, retry charges, payment fees, infrastructure, tax, fraud and FX changes may consume it. A price above the supplier's *single output* cost is not proof that a whole generation makes money.
- **Supplier basis:** HopBase ToB quote dated 2026-10-12, valid through 2026-11-11, as copied in PR #38 spec. USD amounts marked `$` in the quote are USD; `¥` means CNY.
- **Status:** `CANDIDATE` = arithmetic meets the two bounds for the **specified output-only** SKU; `PROVISIONAL` = official tier data is corroborated but the official developer page could not be text-verified; `HOLD` = no defensible commercial price under the owner's constraints or missing cost component. Even CANDIDATE requires the common launch gates below.

## A. Image generation — proposed output unit rates

| Model / scope | HopBase quote | Official standard API | Apex candidate | Of official | Output gross margin | Status |
|---|---:|---:|---:|---:|---:|---|
| `seedream-5-0-lite`, per image (Volcengine Ark) | US$0.0238 = **S$0.030464** | ¥0.22 = **S$0.0418** | **4 credits/image = S$0.04** | **9.57折** (4.3% less) | **23.8%** | CANDIDATE; no meaningful FX cushion |
| `seedream-5-0-pro`, ≤2.61 MP single image | US$0.0306 = **S$0.039168** | ¥0.30 = **S$0.057** | **5 credits/image = S$0.05** | **8.77折** (12.3% less) | **21.7%** | CANDIDATE; higher resolution and input images separately priced |
| `nano-banana-2.1`, Google 1K output image | US$0.0153 = **S$0.019584** | US$0.0336 = **S$0.043008** | **3 credits/image = S$0.03** | **6.98折** (30.2% less) | **34.7%** | CANDIDATE; 1K standard only |
| `gemini-3-pro-image`, Google 1K/2K output image | US$0.0941 = **S$0.120448** | US$0.134 = **S$0.17152** | **15 credits/image = S$0.15** | **8.75折** (12.5% less) | **19.7%** | CANDIDATE; NOT 4K; input tokens extra |

**Important:** Google has cheaper **Batch** prices (Nano Banana 2.1 1K US$0.0168, Gemini 3 Pro Image 1K/2K US$0.067). Our candidates do **not** beat every official channel. Volcengine's other model-access channel lists different Seedream tariffs: ensure the actual SKU and channel are Ark and not a different reseller/endpoint. Gemini image generation may produce billable text/reasoning/input; not included in the output-only margins.

## B. Video generation — output seconds only

| Model / scope | HopBase quote | Official reference | Apex candidate | Of official | Output gross margin | Status |
|---|---:|---:|---:|---:|---:|---|
| MiniMax H3 768P | ¥0.425/s = **S$0.08075/s** | US$0.08/s = **S$0.1024/s** | **9 credits/s = S$0.09/s** | **8.79折** (12.1% less) | **10.3%** | CANDIDATE; **thin margin** |
| MiniMax H3 2K | ¥0.68/s = **S$0.12920/s** | US$0.13/s = **S$0.1664/s** | **15 credits/s = S$0.15/s** | **9.01折** (9.9% less) | **13.9%** | CANDIDATE; **thin margin** |
| MiniMax H3-Max fast 480P | ¥0.34/s = **S$0.06460/s** | US$0.05/s = **S$0.064/s**, or CN ¥0.33/s | **NO VALID RATE** | — | — | **HOLD: cost exceeds official price** |
| Kling v3 Omni, **silent, no reference video**, 720P | ¥0.45/s = **S$0.08550/s** | reported CN ¥0.60/s = **S$0.114/s** | **10 credits/s = S$0.10/s** | **8.77折** (12.3% less) | **14.5%** | **PROVISIONAL** official-page verification needed |
| Kling v3 Omni, **silent, no reference video**, 1080P | ¥0.60/s = **S$0.114/s** | reported CN ¥0.80/s = **S$0.152/s** | **14 credits/s = S$0.14/s** | **9.21折** (7.9% less) | **18.6%** | **PROVISIONAL** official-page verification needed |
| Kling v3 Omni, silent 2K | ¥0.75/s = **S$0.14250/s** | comparable **2K official rate NOT VERIFIED** | **NO VALIDATED RATE** | — | — | **HOLD** until official 2K price/scope verified |

- MiniMax H3: standard H3 API public rate is confirmed. Additional reference images and input video may be billed separately; exclude these modes from the output-only price unless also metered.
- MiniMax H3-Max: supplier 480P ¥0.34/s is already above provider's ¥0.33/s list and US$0.05/s list; renegotiate (for ~20% output margin under ¥0.33/s, target buying cost ≤ ¥0.26/s).
- Kling: direct official pricing is hosted on a JavaScript developer site; corroborating October 2026 API rate trackers show ¥0.60 and ¥0.80 for **silent/no reference video**. These values are **not sufficiently primary-sourced to enable a public discount claim**, so the 10/14-credit rows are PROVISIONAL. There is no verified official 2K comparator, and input-video/native-audio SKUs can differ.
- A 1080P artifact must **not** be mapped to the H3 "2K" price solely because its height exceeds 1079. Pricing must use the exact provider/requested resolution tier, with measured dimensions validating it.

## C. Text generation — amounts per 1,000,000 **output** tokens

| Model / scope | HopBase output quote | Official standard output | Apex output-only candidate | Of official | Output gross margin | Status |
|---|---:|---:|---:|---:|---:|---|
| `claude-haiku-5-5`, prompts ≤100K tokens | US$0.14 = **S$0.1792** | US$0.50 = **S$0.64** | **40 credits/M output = S$0.40** | **6.25折** (37.5% less) | **55.2%** | **HOLD** until input/cache costs quoted + metered |
| `claude-sonnet-5-5` | US$2.80 = **S$3.584** | US$10 = **S$12.80** | **750 credits/M output = S$7.50** | **5.86折** (41.4% less) | **52.2%** | **HOLD** until input/cache costs quoted + metered |
| `deepseek-v4.1-flash` output | ¥5.20 = **S$0.988** | off-peak US$0.60 = **S$0.768**; peak US$1.20 = S$1.536 | **NO ALWAYS-CHEAPER RATE** | — | — | **HOLD**: supplier > official off-peak |
| `deepseek-v4.1-flash` cache-miss input | ¥1.30 = **S$0.247** | off-peak US$0.15 = **S$0.192**; peak US$0.30 = S$0.384 | **NO ALWAYS-CHEAPER RATE** | — | — | **HOLD**: supplier > official off-peak |

- Claude Haiku 5.5 official output is **US$2.50/M for prompts >100K**, not the ≤100K tier above; the long-context **supplier** cost is missing. Claude batch API can be cheaper than regular API. These figures concern *output only*, not price for a full Claude Code session.
- DeepSeek's current canonical public model ID is `deepseek-flash`; the quote uses a provider alias. Confirm routing and server-side usage. For a 20% output cost margin against DeepSeek's off-peak output list, buying price must fall **below ¥3.23/M output** (currently ¥5.20). Cache-miss input must fall below **¥0.81/M** (currently ¥1.30).
- **DO NOT** sell any text output at the old generic `1 credit / 1K` rate: that is S$10/M and exceeds the official standard output rate for Claude Haiku and DeepSeek; it also ignores billable input. Per-model **input, output, cache read/write, long context and batch** tariffs are required.
- For text, **never ceil each charge line to a full credit.** Per-token fees are sub-credit. Debit measured provider token usage in integer microcredits (with appropriate fixed-point scaling); track all input/output/internal thinking usage and billable retries. Estimating output tokens from delivered Unicode text is not an authoritative provider token meter. `CountTextTokens` may be retained for a *non-billing UI estimate* only.
- Text generation must stay on legacy behavior behind a **non-public migration flag** or be disabled for new metered sales until complete provider usage/cost provenance is implemented; do not advertise its price as cheaper than direct API meanwhile.

## D. Not covered by the contract quote — no invented margin

HopBase's public catalog includes **OpenAI GPT / GPT Image, other Claude (Opus/Fable), other Gemini, MiniMax text/audio, Qwen, GLM, Grok, Kimi, Midjourney, Seedance, other Kling, other Seedream** and others. Public *official* rates are not a supplier ToB contract. PR #38 only quotes the exact model/variant rows above. Treat every unquoted model/variant (including GPT and Claude Opus) as **UNPRICED/HOLD** until the supplier provides a dated **input/output/cache/media-mode/region** cost and the comparable official API list is verified. Do not implicitly price all models at the default Seedream or H3 rate. The live supplier account's actual active pricing / discounts should be checked against HopBase's dashboard before approval.

## E. Engineering acceptance and release gates (PR #38)

1. **Rate book keyed by exact provider model + SKU variant:** image model, size and count; video model, resolution, audio, reference inputs and duration; text model, input/output tokens, context tier and caching. Every rate has official URL, supplier quote revision, FX, `effective_at`, `expires_at` and approval status. Unknown/model-mismatched/unapproved SKUs **fail closed before payment**, not silently at zero or at another model's price.
2. **Cost & comparator validation:** `provider_cost_SGD < public_sale_SGD < matching_official_API_list_SGD` for the chosen mode at effective FX, plus an operator-set minimum **contribution** margin after fees/retries. A nonpositive or unsafe post-fee margin blocks the SKU even when the two simple bounds pass. Suppress price-discount labels when batch/promotional/off-peak/region tiers cannot support the claim.
3. **Input billing:** charge/reserve all paid input modalities and true provider usage (tokens, image reference count, input video duration, thinking, tools and retries). Fix the output-only text settlement proposal: otherwise a long input + tiny output could be loss-making. If provider metering is unavailable, disable affected modes.
4. **Rounding:** ledger uses int64 microcredits and precise fixed-point rate arithmetic, **no full-credit per-charge-line ceiling** on text or millisecond video. Display may round while persisted debits must not. Check charges over both tiny and large quantities against the published per-unit comparator.
5. **Quality and billing boundaries:** keep one consistent map from requested/resolved SKU to delivered measured artifact; never charge 1080P as "2K"; preserve per-run max/hold/refund and legacy balance migration protections. Require per-SKU before/after tests, output quantity examples, and price guardrail tests for supplier changes, official cuts and FX movements.
6. **Staged rollout:** pricing memo first, then rate card and measurements in an **unreleased feature flag**; migrate wallet carefully; reverify HopBase's quote before its 2026-11-11 expiry; enable candidate SKUs only after verified real request cost/usage tests and user-visible quotations. **No code change is contained in this document-only PR.**

## Source URLs and verification limits (as of 2026-10-27)

- [HopBase ToB supplier quote as recorded in PR #38](https://github.com/apexai-sg/multica/pull/38), dated 2026-10-12; it is **not** an independent OEM price.
- [Volcengine Ark official model pricing](https://docs.volcengine.com/docs/ark/model-pricing?lang=zh) — Seedream 5 Lite ¥0.22, Pro ≤2.61 MP ¥0.30 single-image mode. Other Volcengine products/regions can have different tariffs.
- [Google Gemini API official pricing](https://ai.google.dev/gemini-api/docs/pricing) — Nano Banana 2.1 US$0.0336/1K standard; Gemini 3 Pro Image US$0.134/1K–2K standard; lower Batch SKUs.
- [MiniMax official API pricing](https://platform.minimax.io/docs/pricing/overview) and [API token plan](https://platform.minimax.io/subscribe/token-plan?tab=api-enterprise) — H3 US$0.08/768P and US$0.13/2K; H3-Max US$0.05/480P.
- [Claude Sonnet 5.5 official](https://platform.claude.com/docs/en/models/sonnet-5-5/overview), [Claude Haiku 5.5 official](https://platform.claude.com/docs/en/models/haiku-5-5/overview) — separate input/output pricing and long-context tiers.
- [DeepSeek official API pricing](https://api-docs.deepseek.com/quick_start/pricing/) — 2026 current `deepseek-flash` peak/off-peak, cache hit/miss, output.
- [Kling official developer pricing (JS rendered)](https://kling.ai/document-api/pricing/base/video), **not directly text-verifiable**; secondary cross-check [ClipCost 2026-10-25 rate capture](https://www.costperclip.com/tools/klingapi) and [Kling API CN tracking](https://vidcost.com/api/kling-api-cn/). Verify directly in the official developer portal before enabling or claiming discount.
