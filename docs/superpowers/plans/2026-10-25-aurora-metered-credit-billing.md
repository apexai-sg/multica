# Aurora Metered Credit Billing (Phase 1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Introduce server-measured and itemized charging by explicitly approved provider/model/SKU, not a generic 3× markup. Image count and resolved video resolution × measured seconds are the media meters; text bills from the daemon's `task_usage` token rows, with `deepseek-v4.1-flash` as the default text model. Every SKU is gated on approved whole-request economics.

**Architecture:** A versioned, server-owned rate card prices measured usage. Media usage is measured once, at artifact staging, where the bytes already stream; text usage is read from the `task_usage` rows of every task the generation accumulated (`aurora_generation.task_ids`, one per round). The create request selects the output shape (within the skill's caps); one quote/reserve path prices that selection; settlement sums the generation's assets and token rows at the same frozen SKU, writes itemized `aurora_generation_charge` rows, and refunds the difference between the reserved maximum and the actual charge. Internal `aurora_generation_cost` rows record what the provider actually cost. Runs created before this change carry `rate_version = 'legacy'` and settle at the old flat skill price.

**Tech Stack:** Go 1.26.9 (the `server/go.mod` version; Chi, pgx, sqlc), PostgreSQL migrations, Next.js + React + TanStack Query, vitest.

**Spec:** `docs/superpowers/specs/2026-10-25-aurora-credit-billing-redesign-design.md`. **Mandatory owner pricing override:** `docs/superpowers/specs/2026-10-27-aurora-all-provider-pricing-audit.md`. The audit covers every model with a quoted HopBase cost, marks unquoted models HOLD, and includes effective subscriber-credit revenue checks. **Do not deploy any retail rates in this documentation-only PR.**

## PR #38 review closure

The 2026-10-26 review and the 2026-10-27 owner follow-up are addressed as
follows; every row names the place a reviewer can check.

| Review item | Where |
|---|---|
| P0-1 reserve the user's selected output, not a worst-case skill cap | spec D4/§4; Task 2 `Quote`, Task 7 quote+reserve, Task 9 composer |
| P0-2 resolve the video-tier contradiction; no 1080P-as-2K | spec §2 SKU boundary table; Task 1 tests; Task 2 cap 1440 |
| P0-3 capture provider cost, pin the model, count retries, alert | spec §5.1; Task 11 |
| P1-4 keep the old 9/24/39 and 1-credit/1K launch rates | **Superseded** by the owner's 2026-10-27 audit (4/5/3/15 image; H3 Lite 9/15, standard 11/18); old blanket rates are documented as replaced in the spec and audit |
| P1-5 membership tiers, Business plan, margin scenarios | spec §2 credit-only rule (D5: no per-plan SKU gating); S$49/5,500 and S$149/18,000 **rejected** under current cost; Open question 2 |
| P1-6 annual 11/12 and abuse-controlled signup trial | **Resolved 2026-10-27:** no signup trial at launch; the existing 500-credit grant is removed in the pre-launch cleanup. spec §2 |
| P0-7 auditable old-balance conversion, preserve prepaid value | **Resolved 2026-10-27:** there are no paying users; existing rows are test data. spec §10 is a pre-launch cleanup, not a conversion |
| P0-8 re-verify HopBase before 2026-11-25; fail closed | spec §2 rate metadata; plan Global Constraints; Tasks 1, 12 |
| Text metering source (was open question 4) | **Resolved 2026-10-27:** read `task_usage` for the generation's task. spec D2/§3; Task 6 tests; Task 11 |

**Scope:** Phase 1 only (spec §Rollout item 1). The batch wallet (spec §6), the
pre-launch test-data cleanup (spec §10), and the SGD plan catalogue (spec
§Rollout item 3) get their own plans; this one ships and is meaningful without
them. The wallet phase takes migration numbers 5002–5005; this plan uses 5001,
5006–5012.

## Global Constraints

- `1 credit = 1_000_000 micro-credits`; `100 credits = S$1.00`.
- All money is `int64` micro-credits. No float ever touches a balance or a rate.
- Charge exact **integer microcredits**, not up to the next whole credit per line. Text/fractional video rounding to whole credits could price short requests above official; use integer fixed-point arithmetic and explicitly prevent overflow.
- Rates are integers in `micro-credits per unit`: per 1,000 text tokens, per image, per video second.
- Rates are keyed by **model plus SKU variant** (image pixels; video tier, audio, reference inputs; text input/output/cache/context). An unconfigured/HOLD/expired model fails **before reservation**, never settles at zero or inherits another model's price.
- Text tokens come from `task_usage` for the generation's own task (`input_tokens` excludes cached tokens; input side is `input + cache_read + cache_write`). No row at settlement charges the reserved ceiling and flags `aurora_usage_missing`; a heuristic token count never becomes a charge.
- No signup credit grant ships with this plan (owner decision 2026-10-27); the existing 500-credit grant is removed in the pre-launch cleanup.
- Every rate record carries `Approval`, `OfficialURL`, `SupplierQuoteRevision`,
  `FX`, `EffectiveAt` and `ExpiresAt`. `provisional`, `hold`, uneffective or
  expired records are unsellable; the 2026-10-26 HopBase quote expires
  2026-11-25 and must be re-verified before any candidate is enabled.
- Model selection is server-side at generation creation. A request never supplies a model name; it may select only the enumerated output shape (image count, video seconds, provider-supported video mode).
- The reservation is the **selected** output's maximum, bounded by the skill's caps, not the generic worst case. Actual > reserved is absorbed and flagged, never debited.
- The wire is the handler's existing casing per endpoint: the generation endpoints
  (`/api/aurora/generations…`) are camelCase, and every new field on them is
  camelCase; `/api/aurora/skills` serializes `aurora.SkillCatalogEntry` directly,
  whose tags are snake_case (`name_en`, `attachment_rules`), so the fields added
  there are snake_case (`max_credits`, `pricing_status`, `metered_enabled`) and
  `packages/core/aurora/schema.ts` keeps parsing that endpoint as snake_case.
- Migrations for this fork take numbers from 5000; `5000` is taken, so this plan
  uses 5001, 5006–5012. Every index is `CREATE [UNIQUE] INDEX CONCURRENTLY` in
  its own single-statement file, including a new table's primary key: declare no
  inline `PRIMARY KEY`, build the unique index concurrently, then attach it with
  `ADD CONSTRAINT … PRIMARY KEY USING INDEX` (the 532–534 pattern).
- Every concurrent up build is registered in `server/cmd/migrate/main.go`'s
  `concurrentIndexCleanups`; `TestEveryConcurrentUpBuildHasCleanup` fails
  otherwise.
- New Aurora server test files use `package aurora` when they exercise unexported
  helpers; tests appended to the existing `package aurora_test` files
  (`catalog_test.go`, `settlement_test.go`) qualify every symbol with `aurora.`.
- No foreign keys, cascading deletes, or cascading updates.
- Code, comments, and commits in English. The Aurora UI copy stays Chinese.

## Review Focus

1. **A video whose probe fails at staging.** A file that cannot be probed must be rejected with 400 before it is stored — never stored unmeasured and settled at zero. (Task 4)
2. **A text generation with no `task_usage` row.** Settlement must charge the reserved ceiling and flag `aurora_usage_missing`; it must never fall back to zero, to the delivered text length, or to another model's rate. (Task 6)
3. **Cached tokens counted twice.** `input_tokens` excludes cached tokens; `cache_read` must be billed as its own line at the cache rate or, when unquoted (DeepSeek today), at the input rate — never folded back into input and never free. (Tasks 1, 6)
4. **A resumed round reporting the same usage again.** `task_usage` is one cumulative row per `(task, provider, model)`; settlement must emit one line per `(model, category)` and a replayed terminal callback must not add a second charge. A daemon that reported per-round deltas instead of cumulative totals would undercount, which the settlement cannot detect — hold the report contract in review. (Task 6)
5. **A skill pinned to a HOLD model.** A Claude-pinned text skill must render unavailable; the default `deepseek-v4.1-flash` must not be silently substituted for it. (Task 2)
6. **A generation created before the deploy.** It carries `rate_version = 'legacy'` and must charge its old flat `credits_reserved`, never look up a model, and never re-price. (Task 6)
7. **A multi-round `needs_input` conversation.** Every round's assets and token rows belong to the same generation; `aurora_generation.task_ids` accumulates one id per round, and settlement sums all of them, not the last round's. (Tasks 3, 6)
8. **An agent that ignores its ceiling.** The charge is capped at the reservation, the line is marked `over_cap`, the balance never goes negative, and the event is logged. (Task 6)
9. **A replayed terminal callback.** Completion and failure are each idempotent: no second refund, no second charge row. (Tasks 6, 8)
10. **Commercial guardrails.** Before any paid generation, verify supplier output/input cost < realized sale proceeds < comparable OEM API list at the current FX **across all allowed funding sources**, including discounted subscription credits and topups. Billing is credit-only (D5): every plan is just a credit grant, and the minimum effective SGD/credit across all offered plans must clear the most expensive active SKU. The S$49/5,500 and S$149/18,000 review-proposed rows are **rejected as a policy choice**, not because no price exists: at 11/18 they earn 11.2%/11.4% and 4.4%/4.6%, and clearing 20% on them would need 13/20 or 14/22 credits/s — prices further above the official list that the owner declined to set. Standard
MiniMax H3 is priced above the OEM list by explicit owner override to reach
20% output margin; that exception is limited to those two rows, must suppress
any discount label, and does not relax the bound for any other SKU (spec §2,
audit §E).
11. **A request that selects 5 s of 768P.** It must quote and reserve 5 s of 768P, not 10 s of 2K. The same selected output drives the pre-submit quote, the reservation and settlement; a test asserts all three agree. (Tasks 7, 9)
12. **A 1080P artifact against a H3 2K SKU.** It must be rejected as a mismatch, never settled at the 2K rate. Boundary heights 719/720/768/1079/1080/1439/1440 are covered by the requested-SKU tests. (Task 1)
13. **An expensive model reached by an agent.** The dispatched model is pinned in the task payload, the runtime's provider-run record must match, and the run's actual provider cost is recorded per attempt. A mismatch settles at the reserved ceiling and raises an alarm. (Task 11)
14. **A rate record past its quote window.** It fails closed before reservation; a HOLD or PROVISIONAL SKU is not selectable in any client. (Tasks 1, 2, 7)

---

### Task 1: Rate card

**Files:**
- Create: `server/internal/aurora/pricing.go`
- Test: `server/internal/aurora/pricing_test.go`

**Interfaces:**
- Produces:
  - `type OutputCaps struct { MaxTextTokens int64; MaxImages int; MaxVideoMilli int64; MaxVideoHeight int }` (JSON `maxTextTokens`, `maxImages`, `maxVideoMilli`, `maxVideoHeight`). Task 2 attaches this type to the catalog entry; it is declared here because the ceiling is priced from it.
  - `type Measured struct { Tokens int64; Width int; Height int; DurationMS int64 }` (JSON tags `tokens`, `width`, `height`, `duration_ms`, all `omitempty`, matching the spec §7 column table).
  - `type Usage struct { Kind, Model, SKU string; InputTokens, OutputTokens, CacheReadTokens, CacheWriteTokens int64; Images int; SecondsMilli int64; Height int }` — `SKU` is the explicitly requested/authorized provider variant, not an agent-written label and not inferred from the file. For text it is the token category (`input`, `output`, `cache_read`, `cache_write`), so each category becomes its own charge line.
  - `type ChargeLine struct { Kind, Model, SKU string; QuantityMilli int64; UnitRateMicro int64; AmountMicro int64 }`.
  - `type RateMeta struct { Approval string; OfficialURL string; SupplierQuoteRevision string; FX string; EffectiveAt, ExpiresAt time.Time }` — `Approval` is `approved` \| `candidate` \| `provisional` \| `hold`; only `approved` and `candidate` are priceable, and `candidate` stays unsellable in production until the owner approves it and the catalog marks the skill `metered_enabled`.
  - `type VideoRate struct { SKU string; Height int; MicroPerSecond int64; Meta RateMeta }` — `Height` is the exact accepted measured height (720, 768-range, 1080 or 1440), not an open-ended threshold.
  - `type ModelRate struct { Kind string; Meta RateMeta; Text *TextRate; Image *ImageRate; Video []VideoRate }`.
  - `type TextRate struct { MicroPerKTokenInput, MicroPerKTokenOutput, MicroPerKTokenCacheRead, MicroPerKTokenCacheWrite int64 }` — a zero cache rate means the category is not separately priced and is billed at the input rate (DeepSeek today).
  - `type ImageRate struct { MicroPerImage int64 }`.
  - `type RateCard struct { Version string; Models map[string]ModelRate }`.
  - `func DefaultRateCard() *RateCard`
  - `func (c *RateCard) Charge(u Usage, now time.Time) (ChargeLine, error)`
  - `func (c *RateCard) Ceiling(textModel, imageModel, videoModel, videoSKU string, selection OutputCaps, now time.Time) (int64, error)`
  - `func (c *RateCard) VideoRateForSKU(model, sku string, measuredHeight int) (VideoRate, bool)`
  - `func CountTextTokens(body []byte) int64`
  - `var ErrRateMissing = errors.New("aurora: rate missing for model")`
  - `var ErrRateExpired = errors.New("aurora: rate record is not sellable")`
  - `var ErrSKUMismatch = errors.New("aurora: measured output does not match the priced SKU")`
- Consumes: nothing.

Quantity convention (one rule, used everywhere):
- text: `QuantityMilli` = token count (one milli-unit of the 1,000-token rate unit is one token), so `amount = tokens × micro_per_1K / 1000`. `SKU` is the token category: `input`, `output`, `cache_read`, `cache_write`.
- image: `QuantityMilli` = image count × 1000.
- video: `QuantityMilli` = measured milliseconds, validated against the **requested** SKU. The SKU's accepted measured heights are: H3 Lite 768P = 720–1079, H3 Lite 2K = 1440, Kling 720P = 720, Kling 1080P = 1080. A measured height outside its SKU is `ErrSKUMismatch` (rejected at staging), never repriced at a neighbouring tier; 2160/4K and 1080–1439 H3 output are HOLD.

`raw_micro = ceil(QuantityMilli * UnitRateMicro / 1000)` using overflow-safe integer math; `AmountMicro = raw_micro`. Do **not** round to full credits. Refunds and displays must reconcile to the exact microcredit ledger.

A rate record is priceable only when `Approval` is `approved` or `candidate`,
`EffectiveAt <= now < ExpiresAt`, and all provenance fields are non-empty;
otherwise `Charge` and `Ceiling` return `ErrRateExpired`. `provisional` and
`hold` rows are never priceable. A `candidate` row is priceable for tests and
for the disabled metered pipeline, but the skill it belongs to reports
`pricing_status = candidate` and `metered_enabled = false` until the owner
approves it (Task 2), so no client can sell it.

- [ ] **Step 1: Write the failing tests**

```go
var now = time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)

func TestUnpricedTextFailsClosed(t *testing.T) {
    _, err := DefaultRateCard().Charge(Usage{Kind: "text", Model: "claude-haiku-5-5", SKU: "input", InputTokens: 3500}, now)
    if !errors.Is(err, ErrRateMissing) { t.Fatalf("unapproved text must be HOLD, got %v", err) }
}

func TestChargeVideoFollowsRequestedSKU(t *testing.T) {
    // 768P accepts 720–1079; 2K accepts exactly 1440.
    for _, h := range []int{720, 768, 1079} {
        line, err := DefaultRateCard().Charge(Usage{Kind: "video", Model: "MiniMax-H3-Lite", SKU: "768P", SecondsMilli: 5000, Height: h}, now)
        if err != nil || line.UnitRateMicro != 9_000_000 || line.AmountMicro != 45_000_000 {
            t.Fatalf("768P height %d: got %+v, err %v", h, line, err)
        }
    }
    line, err := DefaultRateCard().Charge(Usage{Kind: "video", Model: "MiniMax-H3-Lite", SKU: "2K", SecondsMilli: 5000, Height: 1440}, now)
    if err != nil || line.UnitRateMicro != 15_000_000 || line.AmountMicro != 75_000_000 {
        t.Fatalf("2K: got %+v, err %v", line, err)
    }
    // 1080–1439 is not a H3 SKU; 2160 (4K) is unpriced. Neither may borrow 2K.
    for _, h := range []int{1080, 1439, 2160} {
        if _, err := DefaultRateCard().Charge(Usage{Kind: "video", Model: "MiniMax-H3-Lite", SKU: "2K", SecondsMilli: 5000, Height: h}, now); !errors.Is(err, ErrSKUMismatch) {
            t.Fatalf("height %d must not settle at 2K, got %v", h, err)
        }
    }
    // A 1080P artifact requested as 768P is also a mismatch.
    if _, err := DefaultRateCard().Charge(Usage{Kind: "video", Model: "MiniMax-H3-Lite", SKU: "768P", SecondsMilli: 5000, Height: 1080}, now); !errors.Is(err, ErrSKUMismatch) {
        t.Fatalf("1080P against 768P must fail, got %v", err)
    }
}

func TestChargeImages(t *testing.T) {
	line, err := DefaultRateCard().Charge(Usage{Kind: "image", Model: "seedream-5-0-lite", Images: 3}, now)
	if err != nil || line.AmountMicro != 12_000_000 || line.QuantityMilli != 3000 {
		t.Fatalf("got %+v, err %v", line, err)
	}
}

func TestChargeTextUsesInputAndOutputRates(t *testing.T) {
	// 29 credits/M input, 113 credits/M output (DeepSeek default).
	in, err := DefaultRateCard().Charge(Usage{Kind: "text", Model: "deepseek-v4.1-flash", SKU: "input", InputTokens: 1_000_000}, now)
	if err != nil || in.AmountMicro != 29_000_000 {
		t.Fatalf("input: got %+v, err %v", in, err)
	}
	out, err := DefaultRateCard().Charge(Usage{Kind: "text", Model: "deepseek-v4.1-flash", SKU: "output", OutputTokens: 100_000}, now)
	if err != nil || out.AmountMicro != 11_300_000 {
		t.Fatalf("output: want 11.3 credits, got %+v, err %v", out, err)
	}
}

func TestChargeTextBillsCacheReadAtInputRate(t *testing.T) {
	// DeepSeek has no cache-hit quote: cache_read falls back to the input rate
	// and is never free and never folded into input.
	line, err := DefaultRateCard().Charge(Usage{Kind: "text", Model: "deepseek-v4.1-flash", SKU: "cache_read", CacheReadTokens: 1_000_000}, now)
	if err != nil || line.AmountMicro != 29_000_000 || line.SKU != "cache_read" {
		t.Fatalf("cache_read: got %+v, err %v", line, err)
	}
}

func TestChargeDoesNotRoundUpToWholeCredits(t *testing.T) {
	// 1 second of a 9-credit/s SKU is 9 credits; 333 ms must be exact microcredits.
	line, err := DefaultRateCard().Charge(Usage{Kind: "video", Model: "MiniMax-H3-Lite", SKU: "768P", SecondsMilli: 333, Height: 768}, now)
	if err != nil || line.AmountMicro != 2_997_000 || line.AmountMicro%(1_000_000) == 0 {
		t.Fatalf("want 0.002997 credits, got %+v, err %v", line, err)
	}
}

func TestProvisionalAndExpiredRatesFailClosed(t *testing.T) {
	later := now.AddDate(0, 0, 30) // after the HopBase 2026-11-25 quote window
	if _, err := DefaultRateCard().Charge(Usage{Kind: "image", Model: "seedream-5-0-lite", Images: 1}, later); !errors.Is(err, ErrRateExpired) {
		t.Fatalf("expired quote must fail closed, got %v", err)
	}
	if _, err := DefaultRateCard().Charge(Usage{Kind: "video", Model: "kling-v3-omni", SKU: "720P", SecondsMilli: 1000, Height: 720}, now); !errors.Is(err, ErrRateExpired) {
		t.Fatalf("provisional Kling must fail closed, got %v", err)
	}
}

func TestChargeUnknownModelFailsClosed(t *testing.T) {
	if _, err := DefaultRateCard().Charge(Usage{Kind: "image", Model: "nope", Images: 1}, now); !errors.Is(err, ErrRateMissing) {
		t.Fatalf("want ErrRateMissing, got %v", err)
	}
}

func TestCeilingPricesTheSelectedOutput(t *testing.T) {
	// 2 images + 5 s of 768P = 8 + 45 = 53 credits, not the 10 s worst case.
	selection := OutputCaps{MaxImages: 2, MaxVideoMilli: 5_000, MaxVideoHeight: 768}
	got, err := DefaultRateCard().Ceiling("", "seedream-5-0-lite", "MiniMax-H3-Lite", "768P", selection, now)
	if err != nil || got != 53_000_000 {
		t.Fatalf("got %d, err %v", got, err)
	}
}

func TestCountTextTokens(t *testing.T) {
	// 4 ASCII runes ≈ 1 token; 3 CJK runes ≈ 2 tokens.
	if got := CountTextTokens([]byte("abcd")); got != 1 {
		t.Fatalf("ascii: got %d", got)
	}
	if got := CountTextTokens([]byte("一二三")); got != 2 {
		t.Fatalf("cjk: got %d", got)
	}
}
```

- [ ] **Step 2: Run the tests and confirm they fail**

Run: `cd server && go test ./internal/aurora -run 'TestCharge|TestCeiling|TestCountText|TestProvisional|TestUnpriced' -count=1`
Expected: FAIL — `undefined: DefaultRateCard`.

- [ ] **Step 3: Implement `pricing.go`**

`DefaultRateCard()` returns `Version: "2026-10-27-candidate"` containing only audited non-HOLD output SKU candidates for development (see audit for margins, input-cost restrictions and rollout gates). Each entry carries `RateMeta{Approval: "candidate", OfficialURL, SupplierQuoteRevision, FX, EffectiveAt: 2026-10-26, ExpiresAt: 2026-11-25}`; Kling rows carry `Approval: "provisional"` and are not priceable:

| model / SKU | kind | proposed rate |
|---|---|---|
| `seedream-5-0-lite` / standard output image | image | `MicroPerImage: 4_000_000` (4 credits) |
| `seedream-5-0-pro` / ≤2.61 MP single-image mode | image | `MicroPerImage: 5_000_000` (5 credits) |
| `nano-banana-2.1` / 1K, 2K, 4K | image | `MicroPerImage: 3_000_000 / 4_000_000 / 8_000_000`, verify upstream mapping |
| `gemini-3-pro-image` / 1K, 2K, 4K | image | `MicroPerImage: 15_000_000` (15 credits; one-price incl. input) |
| `MiniMax-H3-Lite` / `480P` | video | `MicroPerSecond: 3_000_000`, accepted height 480; availability unconfirmed |
| `MiniMax-H3-Lite` / `768P` | video | `MicroPerSecond: 9_000_000`, accepted heights 720–1079 |
| `MiniMax-H3-Lite` / `2K` | video | `MicroPerSecond: 15_000_000`, accepted height 1440 |
| `kling-v3-omni` / `720P`, `1080P` silent, no reference video | video | **PROVISIONAL, 10/14 credits/s, not priceable until OEM validated** |
| `MiniMax-H3` / `768P`, `2K` | video | `MicroPerSecond: 11_000_000 / 18_000_000` (20.9%/21.1%), **CANDIDATE by owner override: above the official list, no discount claim** |
| `MiniMax-H3` self-deployed / `768P`, `2K` | video | **PROVISIONAL, 9/15 credits/s, not priceable until availability is confirmed** (52.9%/53.0% against the self-deployed cost) |
| `grok-imagine` / `480P`, `720P`, `1080P` | video | `MicroPerSecond: 8_000_000 / 12_000_000 / 22_000_000`, **CANDIDATE (provider not integrated; 23.2%/10.4%/12.7%)** |
| `deepseek-v4.1-flash` / input, output (**default text model**) | text | `MicroPerKTokenInput: 29_000`, `MicroPerKTokenOutput: 113_000`; cache read bills at the input rate until a cache-hit quote exists |
| `MiniMax-H3` self-deployed 1080P/4K, H3-Max, Kling with audio, Claude text, all unquoted models | any | **HOLD / not in live rate book** |

The tests above use `MiniMax-H3-Lite` because the Lite tier is the intended
default: at 9/15 credits/s it earns ~66% and stays below MiniMax's official
list. The **standard** tier was repriced to 11/18 credits/s to reach ~20% after
the 2026-10-26 cost rise, which puts it **above** the official list; it remains
sellable only without any discount claim. The model id stays part of the rate
key so a request can never borrow one tier's price for the other.

**Video lookup uses the requested/authorized provider SKU** (e.g. H3 768P or H3 2K), with measured height/duration validating the delivered artifact. Do not choose a 2K rate merely because `height >= 1080`; an unconfigured 1080P variant must be held or rejected.

**Owner-requested MiniMax H3 pricing (2026-10-27, recosted against the 2026-10-26 quote):** the Lite tier is 9 credits/s (768P) and 15 credits/s (2K), replacing 24/39; at the spec's reference FX these are 12.1% and 9.9% below MiniMax's official API list (US$0.08/s and US$0.13/s) and earn **65.9% / 66.7%** against the Lite supplier cost (US$0.024 / 0.039 per s). The **standard** tier costs US$0.068 / 0.111 per s, so 9/15 would leave only 3.3% / 5.3%; at the owner's direction it prices at **11 / 18 credits/s**, earning **20.9% / 21.1%** — but that is 7.4% / 8.2% **above** the official list, so standard H3 must carry no discount claim and must not be described as cheaper than the official API. The 20% target and the "below official" bound are mutually exclusive at this cost (the maximum below-list margin is 15.0% / 14.6%). Additional reference/input costs, retries, fees, and infrastructure still apply. Pricing for input-video/reference-heavy workflows needs an explicit surcharge or restriction before production. Verify both source prices before any external price advertising; this is not a blanket model discount.

The old height-based 1080P→2K lookup is removed as a pricing shortcut. Define exact provider SKU boundaries and test mismatched/unsupported output dimensions before enabling each mode.

`CountTextTokens` is a **non-billing preview heuristic only**: per class, integer division `ascii/4 + nonASCII×3/4` (truncating, not rounding up — `"一二三"` is 2 and `"abcd一二三"` is 3), with a minimum of 1 for a non-empty body. It never feeds a charge. Text billing reads the `task_usage` rows described in spec §3 (owner decision 2026-10-27), so no new usage-reporting protocol is built here. **`deepseek-v4.1-flash` is the default text model and is priceable**; only text models with no quoted rate (today: Claude, whose cache and >100K-prompt tiers are unquoted) stay out of the live card.

`Charge` returns `ErrRateMissing` when the model is absent or when the kind has no rate on that model. For text, `Ceiling` prices `MaxTextTokens` at the **output** rate, the most expensive category, so the reserved ceiling can never undershoot a real run.

- [ ] **Step 4: Run the tests and confirm they pass**

Run: `cd server && go test ./internal/aurora -run 'TestCharge|TestCeiling|TestCountText|TestProvisional|TestUnpriced' -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add server/internal/aurora/pricing.go server/internal/aurora/pricing_test.go
git commit -m "feat(aurora): add the model-keyed rate card"
```

---

### Task 2: Output caps and models on the catalog

**Files:**
- Modify: `server/internal/aurora/catalog.go`
- Modify: `server/internal/aurora/experts_catalog.go`
- Test: `server/internal/aurora/catalog_test.go`
- Modify: `packages/core/aurora/schema.ts`, `packages/core/aurora/api.test.ts`

**Interfaces:**
- Consumes: `RateCard.Ceiling` from Task 1.
- Produces:
  - `type OutputModels struct { Text, Image, Video string }` (JSON tags `text`, `image`, `video`).
  - `SkillCatalogEntry` gains `Caps OutputCaps` (`json:"caps"`), `Models OutputModels` (`json:"models"`), `MaxCredits int64` (`json:"max_credits"`, **whole credits**, the display ceiling rounded up from the micro-credit quote), `PricingStatus string` (`json:"pricing_status"`, approved/candidate/provisional/hold), and `MeteredEnabled bool` (`json:"metered_enabled"`) — snake_case because this endpoint serializes the struct directly. `Credits` stays as a deprecated alias of `MaxCredits`; HOLD entries must not masquerade as paid-available skills. `MaxCredits` is display only: every reservation uses `Quote(selection)`, never `MaxCredits`.
  - `func DefaultCaps(outputs []string) OutputCaps`
  - `func (e SkillCatalogEntry) ModelsOrDefault() OutputModels`
  - `func (e SkillCatalogEntry) Quote(selection OutputCaps, card *RateCard, now time.Time) (int64, error)` — clamps the selection to `Caps`, prices it with `Ceiling`, and is the one function `POST /api/aurora/generations/quote` and create both call. It lives in `package aurora`, so the card type is unqualified.

Caps come from the skill's declared `Output` kinds, not 33 hand-edited rows:
`video` present → `{MaxTextTokens: 0, MaxVideoMilli: 10000, MaxVideoHeight: 1440}` **only if any additional text/tool costs are accounted for**;
else `image` present → `{MaxTextTokens: 0, MaxImages: 4}` under the same restriction;
else text-only → `{MaxTextTokens: 8000}` and it is sellable when pinned to
`deepseek-v4.1-flash` (the default text model); a skill pinned to a Claude
model stays HOLD until that model's cache and long-context rates are quoted.

`MaxVideoHeight` is the highest **configured** SKU height (1440 for H3 2K). A
2160/4K request is not covered by a cap or a rate, so it is rejected rather
than silently priced as 2K. `MaxCredits` is `Quote(Caps)` for a sellable entry
(the visible upper bound); it is `0` for HOLD entries.

`ModelsOrDefault()` selects `deepseek-v4.1-flash` for text skills — it is the default text model at 29 / 113 credits per M tokens (audit §C), priced as a **candidate** rate: the field is set, the skill stays `metered_enabled = false` until the owner approves the card. A skill pinned to a Claude model stays unavailable while that model is HOLD. For paid generations, model+SKU must still resolve to an explicit priceable rate, or the skill is unavailable (do not silently fall back to another model).

- [ ] **Step 1: Write the failing test**

Append to `catalog_test.go`, which is `package aurora_test`, so every symbol carries the `aurora.` prefix:

```go
func TestCatalogCapsFollowDeclaredOutputs(t *testing.T) {
	for _, entry := range aurora.Catalog() {
		hasVideo, hasImage := slices.Contains(entry.Output, "video"), slices.Contains(entry.Output, "image")
		switch {
		case hasVideo && (entry.Caps.MaxVideoMilli != 10000 || entry.Caps.MaxVideoHeight != 1440):
			t.Fatalf("%s: video cap %d/%d", entry.ID, entry.Caps.MaxVideoMilli, entry.Caps.MaxVideoHeight)
		case !hasVideo && hasImage && entry.Caps.MaxImages != 4:
			t.Fatalf("%s: image cap %d", entry.ID, entry.Caps.MaxImages)
		case !hasVideo && !hasImage && entry.Caps.MaxTextTokens != 8000:
			t.Fatalf("%s: text cap %d", entry.ID, entry.Caps.MaxTextTokens)
		}
		if entry.PricingStatus == "approved" && (entry.MaxCredits <= 0 || int64(entry.Credits) != entry.MaxCredits) {
			t.Fatalf("%s: approved but no safe cap: max=%d credits=%d", entry.ID, entry.MaxCredits, entry.Credits)
		}
		if entry.PricingStatus != "approved" && entry.MeteredEnabled {
			t.Fatalf("%s: HOLD, provisional or unapproved candidate cannot be sold", entry.ID)
		}
	}
}

func TestHoldModelSkillIsUnavailable(t *testing.T) {
	// Pinned to a HOLD model: no sale and no silent fallback to the default.
	pinned := aurora.SkillCatalogEntry{ID: "hold-text", Output: []string{"text"}, Models: aurora.OutputModels{Text: "claude-haiku-5-5"}}
	if _, err := pinned.Quote(aurora.OutputCaps{MaxTextTokens: 8000}, aurora.DefaultRateCard(), now); !errors.Is(err, aurora.ErrRateMissing) {
		t.Fatalf("HOLD text model must be unavailable, got %v", err)
	}
	// Unpinned text skills default to the priced DeepSeek model.
	if got := (aurora.SkillCatalogEntry{ID: "default-text", Output: []string{"text"}}).ModelsOrDefault().Text; got != "deepseek-v4.1-flash" {
		t.Fatalf("default text model = %q, want deepseek-v4.1-flash", got)
	}
}
```

- [ ] **Step 2: Run it and confirm it fails**

Run: `cd server && go test ./internal/aurora -run 'TestCatalogCaps|TestHoldModel' -count=1`
Expected: FAIL — `entry.Caps undefined`.

- [ ] **Step 3: Implement the caps and projection**

Add the types to `catalog.go`, fill `Caps`/`Models`/`MaxCredits` only for actually eligible price-book configurations; set `PricingStatus` and `MeteredEnabled=false` for HOLD/provisional/unpriced skills. Never crash the catalog because a HOLD text model has no rate: represent it as unavailable. `attachAttachmentRules` and `cloneEntry` must copy pricing fields correctly.

Then extend the client schema so the new fields survive parsing: add `max_credits: z.number().default(0)`, `pricing_status: z.string().default("hold")`, `metered_enabled: z.boolean().default(false)`, `caps` (all fields defaulted to 0) and `models` (all fields defaulted to `""`) to the skill schema in `packages/core/aurora/schema.ts`, and a malformed-response case to `packages/core/aurora/api.test.ts` asserting a payload without the new fields still parses to a non-sellable entry.

- [ ] **Step 4: Run the tests**

Run: `cd server && go test ./internal/aurora -run 'TestCatalogCaps|TestHoldModel' -count=1` then, from the repo root, `pnpm -C packages/core exec vitest run aurora/api.test.ts`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add server/internal/aurora/catalog.go server/internal/aurora/experts_catalog.go server/internal/aurora/catalog_test.go packages/core/aurora/schema.ts packages/core/aurora/api.test.ts
git commit -m "feat(aurora): declare per-skill output caps and models"
```

---

### Task 3: Schema and queries

**Files:**
- Create: `server/migrations/5001_aurora_artifact_measured.{up,down}.sql`
- Create: `server/migrations/5006_aurora_generation_charge.{up,down}.sql`
- Create: `server/migrations/5007_aurora_generation_charge_pkey.{up,down}.sql`
- Create: `server/migrations/5008_aurora_generation_rate_version.{up,down}.sql`
- Create: `server/migrations/5009_aurora_generation_cost.{up,down}.sql`
- Create: `server/migrations/5010_aurora_generation_cost_pkey.{up,down}.sql`
- Create: `server/migrations/5011_aurora_generation_charge_primary_key.{up,down}.sql`
- Create: `server/migrations/5012_aurora_generation_cost_primary_key.{up,down}.sql`
- Modify: `server/pkg/db/queries/aurora.sql`
- Modify: `server/cmd/migrate/main.go` (register both concurrent index builds)
- Regenerate: `server/pkg/db/generated/*` (via `make sqlc`)

**Interfaces:**
- Produces (sqlc):
  - `db.AuroraArtifactStaging.Measured []byte`, `db.AuroraAsset.Measured []byte`, `db.AuroraGeneration.RateVersion string`, `db.AuroraGeneration.OutputSelection []byte`, `db.AuroraGeneration.TaskIds []pgtype.UUID`.
  - `CreateAuroraArtifactStagingParams.Measured []byte`, `CreateAuroraAssetParams.Measured []byte` (sqlc maps `jsonb` to `[]byte`, not `json.RawMessage`).
  - `CreateAuroraGenerationParams.RateVersion string`, `CreateAuroraGenerationParams.OutputSelection []byte`.
  - `CreateAuroraGenerationChargeParams{GenerationID, WorkspaceID, UserID pgtype.UUID; Kind, Model, SKU string; QuantityMilli, UnitRateMicro, AmountMicro int64; Measured []byte; RateVersion string; OverCap bool}` and `CreateAuroraGenerationCharge`.
  - `ListAuroraGenerationCharges(ctx, generationID, workspaceID) ([]db.AuroraGenerationCharge, error)`.
  - `ListAuroraAssetsForCharge(ctx, generationID, workspaceID) ([]db.AuroraAsset, error)` — every asset of the generation, no limit.
  - `GetTaskUsageForTasks(ctx, taskIDs []pgtype.UUID) ([]db.TaskUsage, error)` — the token rows for every round of one generation.
  - `CreateAuroraGenerationCostParams` and `ListAuroraGenerationCosts` for the internal cost rows (Task 11).

- [ ] **Step 1: Write the migrations**

`5001_aurora_artifact_measured.up.sql`

```sql
-- Server-measured usage for billing: tokens, width/height, duration. Kept
-- apart from `metadata`, which the agent writes and which billing never trusts.
ALTER TABLE aurora_artifact_staging ADD COLUMN IF NOT EXISTS measured jsonb NOT NULL DEFAULT '{}';
ALTER TABLE aurora_asset ADD COLUMN IF NOT EXISTS measured jsonb NOT NULL DEFAULT '{}';
```

`5006_aurora_generation_charge.up.sql`

```sql
-- One row per (kind, model, sku) charge line of a settled generation. For text,
-- sku is the token category (input/output/cache_read/cache_write) and
-- `quantity_milli` is the token count; images use count × 1000, video uses
-- milliseconds. No inline primary key or index: the repo convention builds the
-- key index CONCURRENTLY (5007) and attaches it (5011), because the migration
-- runner must not create indexes inside the implicit transaction.
CREATE TABLE IF NOT EXISTS aurora_generation_charge (
    generation_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    user_id uuid NOT NULL,
    kind text NOT NULL,
    model text NOT NULL,
    sku text NOT NULL DEFAULT '',
    quantity_milli bigint NOT NULL,
    unit_rate_micro bigint NOT NULL,
    amount_micro bigint NOT NULL,
    measured jsonb NOT NULL DEFAULT '{}',
    rate_version text NOT NULL,
    over_cap boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now()
);
```

`5007` (single statement):

```sql
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS aurora_generation_charge_pkey
    ON aurora_generation_charge (generation_id, kind, model, sku);
```

`5011_aurora_generation_charge_primary_key.up.sql` (single statement):

```sql
ALTER TABLE aurora_generation_charge
    ADD CONSTRAINT aurora_generation_charge_pkey PRIMARY KEY USING INDEX aurora_generation_charge_pkey;
```

`5008_aurora_generation_rate_version.up.sql`

```sql
ALTER TABLE aurora_generation ADD COLUMN IF NOT EXISTS rate_version text NOT NULL DEFAULT 'legacy';
ALTER TABLE aurora_generation ADD COLUMN IF NOT EXISTS output_selection jsonb NOT NULL DEFAULT '{}';
-- Every round of a needs_input conversation dispatches its own task, and
-- UpdateAuroraGenerationRound overwrites `task_id` on each round. Settlement
-- prices text from task_usage, so the ids of all rounds are kept here; the
-- backfill gives an existing generation its current (last) task.
ALTER TABLE aurora_generation ADD COLUMN IF NOT EXISTS task_ids uuid[] NOT NULL DEFAULT '{}';
UPDATE aurora_generation SET task_ids = ARRAY[task_id] WHERE task_id IS NOT NULL AND task_ids = '{}';
```

`output_selection` freezes the selected output (image count, video seconds,
video mode/SKU) the user was quoted, so settlement prices the same SKU the
reservation did.

`5009_aurora_generation_cost.up.sql`

```sql
-- Internal provider cost per run. Never a customer charge; used to enforce
-- supplier cost < realized revenue < official list and to detect model drift.
-- One row per generation, so generation_id is the natural primary key (5010
-- builds it, 5012 attaches it); that also makes a replayed terminal event an
-- insert no-op.
CREATE TABLE IF NOT EXISTS aurora_generation_cost (
    generation_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    user_id uuid NOT NULL,
    provider text NOT NULL,
    model text NOT NULL,
    input_tokens bigint NOT NULL DEFAULT 0,
    output_tokens bigint NOT NULL DEFAULT 0,
    cache_read_tokens bigint NOT NULL DEFAULT 0,
    cache_write_tokens bigint NOT NULL DEFAULT 0,
    cost_usd_ticks bigint,
    video_milli bigint NOT NULL DEFAULT 0,
    video_height integer NOT NULL DEFAULT 0,
    image_count integer NOT NULL DEFAULT 0,
    reference_count integer NOT NULL DEFAULT 0,
    attempts integer NOT NULL DEFAULT 0,
    chargeable_attempts integer NOT NULL DEFAULT 0,
    provider_cost_micro_sgd bigint NOT NULL DEFAULT 0,
    fx text NOT NULL DEFAULT '',
    supplier_quote_revision text NOT NULL DEFAULT '',
    model_mismatch boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now()
);
```

`cost_usd_ticks` is copied from the matching `task_usage` row and stays NULL
when the provider reported no price; NULL marks `provider_cost_micro_sgd` as
rate-table estimated. There is no `thinking_tokens` column: `task_usage` has no
thinking counter today (spec §5.1).

`5010` (single statement):

```sql
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS aurora_generation_cost_pkey
    ON aurora_generation_cost (generation_id);
```

`5012_aurora_generation_cost_primary_key.up.sql` (single statement):

```sql
ALTER TABLE aurora_generation_cost
    ADD CONSTRAINT aurora_generation_cost_pkey PRIMARY KEY USING INDEX aurora_generation_cost_pkey;
```

Each `.down.sql` reverses its own statement (`DROP INDEX CONCURRENTLY IF EXISTS`, `DROP TABLE IF EXISTS`, `DROP COLUMN IF EXISTS`).

- [ ] **Step 2: Register both concurrent index builds**

Add to `server/cmd/migrate/main.go`'s `concurrentIndexCleanups`:

```go
"5007_aurora_generation_charge_pkey": "aurora_generation_charge_pkey",
"5010_aurora_generation_cost_pkey":   "aurora_generation_cost_pkey",
```

`TestEveryConcurrentUpBuildHasCleanup` (in `cmd/migrate`) fails when a
concurrent build has no entry, so this step is required for the build to pass.

- [ ] **Step 3: Add the queries to `aurora.sql` and regenerate**

Add `CreateAuroraGenerationCharge` (`INSERT … ON CONFLICT (generation_id, kind, model, sku) DO NOTHING`), `ListAuroraGenerationCharges`, `ListAuroraAssetsForCharge`, `CreateAuroraGenerationCost` (`ON CONFLICT (generation_id) DO NOTHING`), `ListAuroraGenerationCosts`, and `GetTaskUsageForTasks` (`SELECT * FROM task_usage WHERE task_id = ANY($1::uuid[]) ORDER BY model`). Add `measured` to `CreateAuroraArtifactStaging`, `rate_version`/`output_selection` to `CreateAuroraGeneration`, `measured` to the asset insert, and `task_ids` to `UpdateAuroraGenerationTask` and `UpdateAuroraGenerationRound`:

```sql
task_ids = CASE WHEN $2::uuid = ANY(task_ids) THEN task_ids ELSE array_append(task_ids, $2::uuid) END
```

`aurora.sql` spells out every column in each `aurora_generation` and
`aurora_asset` query, so adding a column changes each query's generated row
struct. Update every one of them in the same commit or `go build` fails:
`CreateAuroraGeneration`, `GetAuroraGeneration`,
`GetAuroraGenerationByTaskID`, `GetAuroraGenerationByIssueID`,
`ListAuroraGenerations`, `UpdateAuroraGenerationTask`,
`UpdateAuroraGenerationBound`, `UpdateAuroraGenerationTerminal`,
`UpdateAuroraGenerationRound`, `CreateAuroraAsset`, `ListAuroraAssets`,
`GetAuroraAsset`.

- [ ] **Step 4: Regenerate and build**

Run: `make sqlc && cd server && go build ./...`
Expected: builds; the migration lint test passes.

Run: `cd server && go test ./internal/migrations ./cmd/migrate -run 'TestMigration|TestEveryConcurrent' -count=1`
Expected: PASS (no duplicate numeric prefixes, every concurrent build registered).

- [ ] **Step 5: Commit**

```bash
git add server/migrations server/cmd/migrate/main.go server/pkg/db/queries/aurora.sql server/pkg/db/generated
git commit -m "feat(aurora): add measured usage and generation charge tables"
```

---

### Task 4: Measure at staging

**Files:**
- Modify: `server/internal/handler/aurora_artifact_upload.go`
- Modify: `Dockerfile` (runtime stage)
- Modify: `server/go.mod` (+ `golang.org/x/image`)
- Test: `server/internal/handler/aurora_artifact_upload_test.go`

**Interfaces:**
- Produces:
  - `func measureAuroraArtifact(kind, tempPath string) (aurora.Measured, error)`
  - `streamAuroraArtifact` returns `(key string, size int64, digest string, measured aurora.Measured, err error)`.
  - `insertAuroraArtifactStaging` takes the measured value and stores it as JSON.

Rules:
- text → record `aurora.CountTextTokens(body)` for display only. It is never a billing quantity: settlement prices text from `task_usage` (Task 6).
- image → decode the config header (`image.DecodeConfig` with `_ "image/png"`, `_ "image/jpeg"`, `_ "image/gif"`, `_ "golang.org/x/image/webp"`); record width/height.
- video → run `ffprobe -v error -show_entries stream=width,height:format=duration -of json <path>`; record width, height, and `DurationMS = round(duration × 1000)`.
- Any failure returns an error; the caller responds 400 `"artifact could not be measured"` and deletes the staged object.

- [ ] **Step 1: Write the failing tests**

```go
func TestMeasureAuroraArtifactText(t *testing.T) {
	path := writeTemp(t, []byte("abcd一二三"))
	got, err := measureAuroraArtifact("text", path)
	if err != nil || got.Tokens != 3 { // 1 ascii token + 2 cjk tokens
		t.Fatalf("got %+v, err %v", got, err)
	}
}

func TestMeasureAuroraArtifactRejectsUnmeasurableVideo(t *testing.T) {
	path := writeTemp(t, []byte("not a video"))
	if _, err := measureAuroraArtifact("video", path); err == nil {
		t.Fatal("want an error for an unprobeable video")
	}
}

func TestStageRejectsUnmeasurableVideo(t *testing.T) {
	// POST /api/agent/tasks/{id}/aurora-artifacts with Kind "video" and MIME
	// video/mp4 but non-video bytes → 400, and no staging row is written.
}
```

Use an existing tiny PNG fixture from `server/internal/aurora/testdata` for the image case.

- [ ] **Step 2: Run and confirm they fail**

Run: `cd server && go test ./internal/handler -run 'TestMeasure|TestStageRejects' -count=1`
Expected: FAIL — `undefined: measureAuroraArtifact`.

- [ ] **Step 3: Implement**

Spill the stream to `os.CreateTemp` while uploading (an `io.TeeReader` into the temp file), so the probe and the digest share one pass. Measure after the upload succeeds and before the staging insert. On measurement failure delete the object with `deleteAuroraArtifactObject`. Add `ffmpeg` to the runtime `apk add` line in `Dockerfile` and `golang.org/x/image` to `server/go.mod`.

- [ ] **Step 4: Run the tests**

Run: `cd server && go test ./internal/handler -run 'TestMeasure|TestStageRejects' -count=1`
Expected: PASS. (`ffprobe` must be on the test machine's PATH; if it is not, the video case skips with `t.Skip` and the commit message says so.)

- [ ] **Step 5: Commit**

```bash
git add server/internal/handler/aurora_artifact_upload.go server/internal/handler/aurora_artifact_upload_test.go Dockerfile server/go.mod server/go.sum
git commit -m "feat(aurora): measure artifacts at staging"
```

---

### Task 5: Carry measured usage onto the asset

**Files:**
- Modify: `server/internal/handler/aurora_artifact.go`
- Test: `server/internal/handler/aurora_artifact_test.go`

**Interfaces:**
- Consumes: `db.AuroraArtifactStaging.Measured` from Task 3.
- Produces: every asset created by `ReportTaskArtifacts` carries the same `measured` JSON as its staging row.

- [ ] **Step 1: Write the failing test**

```go
func TestReportCopiesMeasuredToAsset(t *testing.T) {
	// Stage an image with measured {"width":1024,"height":1024}, report it,
	// then load the asset and assert measured.width == 1024.
}
```

- [ ] **Step 2: Run and confirm it fails**

Run: `cd server && go test ./internal/handler -run TestReportCopiesMeasuredToAsset -count=1`
Expected: FAIL — measured is `{}`.

- [ ] **Step 3: Implement**

Pass `staging.Measured` into `CreateAuroraAssetParams`; make `ListAuroraAssets` return it (the generated struct picks the column up from the migration).

- [ ] **Step 4: Run the tests**

Run: `cd server && go test ./internal/handler -run 'TestReport' -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add server/internal/handler/aurora_artifact.go server/internal/handler/aurora_artifact_test.go
git commit -m "feat(aurora): carry measured usage onto the asset"
```

---

### Task 6: Settle by measured usage

**Files:**
- Modify: `server/internal/service/aurora_completion.go`
- Modify: `server/internal/handler/handler.go` (wire the card)
- Test: `server/internal/service/aurora_completion_test.go`

**Interfaces:**
- Consumes: `aurora.RateCard`, `aurora.Charge`, `aurora.Lookup(gen.SkillID)`, `gen.OutputSelection`, `gen.TaskIds`, `db.ListAuroraAssetsForCharge`, `db.GetTaskUsageForTasks`, `db.CreateAuroraGenerationCharge`, `Credit.Refund`.
- Produces:
  - `TaskService.RateCard *aurora.RateCard` (set beside `taskSvc.Credit` in `handler.go`).
  - `func (s *TaskService) auroraChargeLines(gen db.AuroraGeneration, assets []db.AuroraAsset, now time.Time) ([]aurora.ChargeLine, error)`

Behaviour:
- `gen.RateVersion == "legacy"` → one line at the old flat price: `AmountMicro = gen.CreditsReserved`, `Kind = "legacy"`, `Model = "legacy"`, `QuantityMilli = 1000`. No model lookup.
- Otherwise decode `gen.OutputSelection` (the frozen selected output/SKU) and price that exact SKU, never a tier inferred from the delivered height alone. Images are priced by count, video by measured milliseconds at the selected SKU's rate. A measured height outside the selected SKU is a settlement error (non-terminal, logged, `aurora_sku_mismatch`), never a repricing. **Text is priced from `task_usage`**, not from delivered Unicode length: read the rows for every round of the generation (`db.GetTaskUsageForTasks(ctx, gen.TaskIds)` — `task_id` alone only names the last round), and emit one charge line per `(model, category)` — `input`, `output`, `cache_read` and `cache_write` when the row reports them. When two rounds ran the same model, their rows are summed into one line per category, so the unique `(generation_id, kind, model, sku)` index still holds. Input and output use their own rates; `cache_read` uses the card's cache rate when set, otherwise the input rate (never zero, never free). No row → charge `gen.CreditsReserved`, flag `aurora_usage_missing`, and alert; never settle a text generation at zero because the report is absent. A paid job that used an unpriced model/variant should have been rejected at creation.
- `actual = min(sum, gen.CreditsReserved)`. `over_cap = sum > gen.CreditsReserved`.
- Write the charge rows (idempotent via the unique index), then `Refund(gen.CreditsReserved - actual)` when positive, with reference `settle:<generationID>`.
- Then `UpdateAuroraGenerationTerminal` with `CreditsCharged: actual`.

- [ ] **Step 1: Write the failing tests**

```go
func TestSettlementRefundsTheDifference(t *testing.T) {
	// reserved 180 credits (the 10 s standard-2K cap); assets: one 5 s 2K
	// standard-H3 video + one Seedream Lite image.
	// → actual = 90 + 4 = 94 credits, refund 86 credits, one refund row.
}

func TestSettlementChargesReservationWhenOverCap(t *testing.T) {
	// reserved 100 credits; assets worth 300.
	// → charged 100, charge row carries over_cap = true, balance never negative.
}

func TestSettlementLegacyGenerationKeepsItsFlatPrice(t *testing.T) {
	// rate_version "legacy", reserved 90 credits, assets worth 9.
	// → charged 90, no model lookup, no refund.
}

func TestSettlementSumsAllRounds(t *testing.T) {
	// assets and token rows reported across two rounds of one generation all
	// count; task_ids names both tasks.
}

func TestSettlementPricesTextFromTaskUsage(t *testing.T) {
	// task_usage: (deepseek, deepseek-v4.1-flash, in 1_000_000, out 1_000_000)
	// → 29 input + 113 output credits = 142 credits, one line per category.
}

func TestSettlementChargesCeilingWhenUsageMissing(t *testing.T) {
	// a text generation whose task has no task_usage row charges the reserved
	// ceiling and flags aurora_usage_missing; it never settles at zero.
}

func TestSettlementIsIdempotent(t *testing.T) {
	// settle twice → one refund row, one set of charge rows.
}
```

- [ ] **Step 2: Run and confirm they fail**

Run: `cd server && go test ./internal/service -run TestSettlement -count=1`
Expected: FAIL.

- [ ] **Step 3: Implement**

Replace the `Limit: 1` asset read in `settleAuroraOnCompleted` with `ListAuroraAssetsForCharge`. Decode `gen.OutputSelection` and pass its SKU to `aurora.Charge`. A read failure still returns without settling (existing rule). An unknown/expired model or a measured-height/SKU mismatch leaves the generation non-terminal and logs, so a later replay or correction can settle it; it never falls back to the reservation or to another tier.

- [ ] **Step 4: Run the tests**

Run: `cd server && go test ./internal/service -run TestSettlement -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add server/internal/service/aurora_completion.go server/internal/service/aurora_completion_test.go server/internal/handler/handler.go
git commit -m "feat(aurora): settle generations by measured usage"
```

---

### Task 7: Quote and reserve the selected output

**Files:**
- Modify: `server/internal/handler/aurora.go`
- Modify: `server/cmd/server/router.go` (register `POST /api/aurora/generations/quote` next to the create/list/get routes)
- Modify: `server/internal/service/task.go` (carry the selected caps into the Aurora task context)
- Modify: `server/internal/daemon/execenv/` (render the ceiling line into the brief from that context)
- Test: `server/internal/handler/aurora_test.go`

**Interfaces:**
- Consumes: `entry.Caps`, `entry.MaxCredits`, `entry.Quote`, `RateCard`.
- Produces:
  - The create request gains `output: {imageCount, videoSeconds, videoMode}` (all optional, zero = skill default), validated against `entry.Caps` and the enumerated SKU list; no model name and no price is accepted from the client.
  - `func writeAuroraInsufficientCredits(w http.ResponseWriter, required, available int64)` writing
    `{"error":"insufficient credits","requiredCredits":N,"availableCredits":N,"shortfallCredits":N}` with 402.
  - `POST /api/aurora/generations/quote` — same body, no side effects, returns
    `{requiredCredits, availableCredits, shortfallCredits, rateVersion}`.
  - `CreateAuroraGeneration` writes `rate_version` and `output_selection` when inserting the generation row.

Behaviour:
1. Resolve an approved/candidate model+variant with a sellable rate record and the funding-source contribution-margin guard. If HOLD/provisional/unpriced/expired, or the selected shape has no configured SKU (for example 2160/4K or a 1080P H3 request), reject **before** reserving.
2. Clamp the selection to `entry.Caps`, then `amountMicro := entry.Quote(selection)` — the **selected** output's maximum, not `entry.MaxCredits`. A 5 s 768P request reserves 5 s of 768P; an open-ended skill uses the cap and says so.
3. If `balance < amountMicro` → 402 with the three figures; nothing is created. The quote endpoint returns the same numbers without creating anything.
4. Otherwise reserve `amountMicro` as today and freeze `output_selection`.
5. The task context gains the selected caps (`imageCount`, `videoSeconds`, `videoMode`) beside `generationId`; the daemon's brief renders them as one line: `本次最多可交付：图片 N 张 / 视频 N 秒（上限，超出不额外计费）。` — the selected caps, not the price. The brief text lives in `server/internal/daemon/execenv/`, not in the handler.

- [ ] **Step 1: Write the failing tests**

```go
func TestCreateRefusesWhenBalanceBelowQuote(t *testing.T) {
	// balance 50 credits, selected 2 images + 5 s 768P = 53 → 402 and body carries
	// requiredCredits 53, availableCredits 50, shortfallCredits 3;
	// no generation row, no issue, no reservation.
}

func TestCreateReservesTheSelectedOutput(t *testing.T) {
	// balance 1000, selected 5 s 768P → reserved == 45 credits (not the 10 s/2K cap),
	// and the row's rate_version/output_selection match the quote.
}

func TestQuoteMatchesCreateReservation(t *testing.T) {
	// the quote endpoint and create return the same requiredCredits for the same body.
}

func TestCreateRejectsUnsupportedSKUAndHoldSkill(t *testing.T) {
	// output 2160/4K → 400 invalid output shape; a HOLD/provisional skill →
	// 409 sku_unavailable; neither creates a generation, issue or reservation.
}
```

- [ ] **Step 2: Run and confirm they fail**

Run: `cd server && go test ./internal/handler -run 'TestCreate|TestQuote' -count=1`
Expected: FAIL.

- [ ] **Step 3: Implement**

Replace the `entry.Credits` arithmetic with the selection quote, add the shortfall response and the read-only quote handler, validate the selection before any write, and pass `RateVersion` plus the serialized `OutputSelection` into `CreateAuroraGeneration`.

- [ ] **Step 4: Run the tests**

Run: `cd server && go test ./internal/handler -run 'TestCreate|TestQuote' -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add server/internal/handler/aurora.go server/internal/handler/aurora_test.go
git commit -m "feat(aurora): quote and reserve the selected output"
```

---

### Task 8: Expose the charge lines

**Files:**
- Modify: `server/internal/handler/aurora.go`
- Test: `server/internal/handler/aurora_test.go`

**Interfaces:**
- Produces: `GET /api/aurora/generations/{id}` gains
  `creditsCharged`, `rateVersion`, and
  `charges: [{kind, model, sku, quantityMilli, unitRateMicro, amountMicro, measured, overCap}]`,
  omitted as `[]` when the generation has not settled.

- [ ] **Step 1: Write the failing test**

```go
func TestGenerationDetailListsCharges(t *testing.T) {
	// settled generation with two charge rows → response carries both, in
	// amount-descending order, each with model, sku and amountMicro.
}
```

- [ ] **Step 2: Run and confirm it fails**

Run: `cd server && go test ./internal/handler -run TestGenerationDetailListsCharges -count=1`
Expected: FAIL.

- [ ] **Step 3: Implement**

Load with `ListAuroraGenerationCharges` and marshal into the response struct; keep every new field additive.

- [ ] **Step 4: Run the tests**

Run: `cd server && go test ./internal/handler -run 'TestGeneration' -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add server/internal/handler/aurora.go server/internal/handler/aurora_test.go
git commit -m "feat(aurora): return the charge breakdown with a generation"
```

---

### Task 9: Show the arithmetic

**Files:**
- Modify: `packages/core/aurora/schema.ts`, `packages/core/aurora/types.ts`
- Modify: `packages/core/aurora/api.ts` (the quote request), `packages/core/aurora/queries.ts` (the quote hook)
- Modify: `packages/core/aurora/api.test.ts`, `packages/core/aurora/queries.test.ts`
- Modify: `packages/views/aurora/generation-detail.tsx` (+ `.test.tsx`)
- Modify: `packages/views/aurora/skill-directory.tsx`, `packages/views/aurora/generation-composer.tsx` (+ tests)

**Interfaces:**
- Consumes: the detail response from Task 8, `entry.max_credits` from Task 2, and
  `POST /api/aurora/generations/quote` from Task 7.
- Produces: an output selection control in the composer and a pre-submit quote
  that refreshes as the selection changes.

Behaviour:
- Skill card and composer show `最多 {max_credits} 积分` instead of `{credits} 积分`.
- The composer exposes image count / video seconds / video mode only where the
  skill's caps allow them, and shows the quote from the server before submit
  (`本次预计最多 X 积分`); it never computes a price client-side. HOLD and
  unapproved-candidate skills render as unavailable, not as purchasable.
- The insufficient-credits dialog shows the shortfall from the 402 body.
- The result page renders one row per charge: `3 张图 × 4 积分（seedream-5-0-lite）`,
  `视频 768P × 5.0 秒 × 9 积分/秒（MiniMax-H3-Lite）`,
  `文本 输入 1.0M tokens × 29 积分/M（deepseek-v4.1-flash）`; HOLD text SKUs are
  not shown as purchasable, then the total. A `legacy` line renders as
  `本次 90 积分`. Token rows show `quantityMilli` as tokens, not credits.
- Never show the reserved ceiling on the result page.

- [ ] **Step 1: Write the failing component test**

```tsx
it("itemizes a settled generation", () => {
  renderDetail({ creditsCharged: 49_000_000, charges: [
    { kind: "video", model: "MiniMax-H3-Lite", sku: "768P", quantityMilli: 5000, unitRateMicro: 9_000_000, amountMicro: 45_000_000, overCap: false },
    { kind: "image", model: "seedream-5-0-lite", sku: "", quantityMilli: 1000, unitRateMicro: 4_000_000, amountMicro: 4_000_000, overCap: false },
  ]});
  expect(screen.getByText(/5\.0 秒/)).toBeTruthy();
  expect(screen.getByText(/49/)).toBeTruthy();
});

it("renders a text token line", () => {
  renderDetail({ creditsCharged: 29_000_000, charges: [
    { kind: "text", model: "deepseek-v4.1-flash", sku: "input", quantityMilli: 1_000_000, unitRateMicro: 29_000, amountMicro: 29_000_000, overCap: false },
  ]});
  expect(screen.getByText(/输入 1\.0M/)).toBeTruthy();
  expect(screen.getByText(/29/)).toBeTruthy();
});

it("shows the server quote before submit", async () => {
  renderComposer({ maxCredits: 180, quote: 53_000_000 });
  expect(await screen.findByText(/53/)).toBeTruthy();
});
```

- [ ] **Step 2: Run and confirm it fails**

Run: `pnpm -C packages/views exec vitest run aurora/generation-detail.test.tsx aurora/generation-composer.test.tsx`
Expected: FAIL.

- [ ] **Step 3: Implement**

Add optional, defaulted `charges` (including `sku`)/`creditsCharged`/`rateVersion` to the generation schema; render the block only when `charges.length > 0`. Update the two skill display sites to `最多 N 积分`, gate selection/purchase on `metered_enabled`, and load the quote through a TanStack Query keyed by the selected output.

- [ ] **Step 4: Run the tests**

Run: `pnpm -C packages/views exec vitest run aurora` then `pnpm -C packages/core exec vitest run aurora`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/core/aurora packages/views/aurora
git commit -m "feat(aurora): itemize the charge on the result page"
```

---

### Task 10: Release abandoned holds

**Files:**
- Modify: `server/internal/aurora/settlement.go` (or a new `server/internal/aurora/holds.go`)
- Modify: `server/internal/service/aurora_completion.go` (a failure path that takes a generation id)
- Modify: `server/cmd/server/aurora_settlement.go` (call the sweep from the existing settlement tick)
- Modify: `server/pkg/db/queries/aurora.sql`
- Test: `server/internal/aurora/settlement_test.go`

**Interfaces:**
- Produces:
  - `db.ListStaleAuroraNeedsInput(ctx, olderThan pgtype.Timestamptz) ([]db.AuroraGeneration, error)`
  - `func SweepAbandonedAuroraHolds(ctx context.Context, q *db.Queries, fail func(ctx context.Context, gen db.AuroraGeneration) error, now time.Time) error`, called from the same tick that runs `RunMonthlySettlement`.
  - `const auroraHoldIdleTimeout = 24 * time.Hour`

Behaviour: a generation in `needs_input` whose `updated_at` is older than the timeout is failed with reason `"no reply"` and its reservation refunded through the existing failure path (reference = generation id, so a replay is a no-op).

- [ ] **Step 1: Write the failing test**

```go
func TestSweepReleasesOnlyStaleHolds(t *testing.T) {
	// two needs_input generations, one 25h old and one 1h old →
	// only the old one fails and is refunded, once.
}
```

- [ ] **Step 2: Run and confirm it fails**

Run: `cd server && go test ./internal/aurora -run TestSweep -count=1`
Expected: FAIL.

- [ ] **Step 3: Implement**

Add the query, the sweep, and the scheduler call next to `RunMonthlySettlement`.

- [ ] **Step 4: Run the tests**

Run: `cd server && go test ./internal/aurora -run TestSweep -count=1 && go test ./internal/service -run TestSettlement -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add server/internal/aurora server/internal/service/aurora_completion.go server/pkg/db/queries/aurora.sql server/pkg/db/generated
git commit -m "feat(aurora): release abandoned conversation holds"
```

---

### Task 11: Provider cost, model pin, and margin observability

**Files:**
- Create: `server/internal/aurora/cost.go` (types and pure cost/margin functions)
- Create: `server/internal/service/aurora_cost.go` (TaskService wiring)
- Test: `server/internal/aurora/cost_test.go`
- Modify: `server/internal/service/aurora_completion.go`
- Modify: `server/internal/service/task.go` (pin the dispatched model/SKU into the Aurora task context, where the payload is assembled)
- Test: `server/internal/service/aurora_completion_test.go`

**Interfaces:**
- Consumes: `aurora_generation_cost` (Task 3), the runtime's provider-run record, `RateCard` cost entries.
- Produces:
  - `type ModelSKU struct { Model, SKU string }` (the dispatched pair frozen at creation).
  - `type ProviderUsage struct { Provider, Model string; InputTokens, OutputTokens, CacheReadTokens, CacheWriteTokens int64; CostUSDticks int64; VideoMilli int64; VideoHeight int; ImageCount, ReferenceCount int; Attempts, ChargeableAttempts int }` — `CostUSDticks` is the `task_usage.cost_usd_ticks` value, 0 when the provider reported none; there is no thinking-token counter to carry.
  - `func ProviderCostMicroSGD(usage ProviderUsage, costRates CostRates, fx string) (int64, error)` — pure, in `aurora`.
  - `func MarginVerdict(costMicro, revenueMicro, floorPercent int64) error` — pure, in `aurora`.
  - `func (s *TaskService) recordAuroraCost(ctx context.Context, gen db.AuroraGeneration, usage ProviderUsage, dispatched ModelSKU) error` — writes one idempotent `aurora_generation_cost` row and applies the cost table at the frozen `fx` and `supplier_quote_revision`.
  - `func (s *TaskService) auroraMarginVerdict(cost, revenueMicro int64) error` — returns an error when contribution margin is below the owner floor.
  - Counters/log keys: `aurora_model_mismatch`, `aurora_cost_exceeds_revenue`, `aurora_charge_over_cap`, `aurora_rate_expired_blocked`.

Behaviour:
- **Reuse `task_usage` for token accounting.** The `(provider, model)` rows and their `cost_usd_ticks` (provider-reported price, when the daemon supplies one) are the token/cost source; do not add a parallel token reporter. `cost_usd_ticks` is authoritative only when positive (0 means "not reported"), so absent values fall back to the rate table and are marked estimated.
- **Pin at dispatch.** The resolved model and SKU are written into `output_selection` and the task payload. On completion, compare the runtime's provider-run record with the pinned model; a mismatch is recorded in `aurora_generation_cost.model_mismatch`, settles at the reserved ceiling (never refunds as if the cheaper model ran), and logs/counts `aurora_model_mismatch`. A missing provider-run record is recorded as unknown; the SKU's sales flag is reviewed, never guessed.
- **Count attempts.** Every attempt, including chargeable failed attempts and retries, is accumulated into the cost row; the customer charge still counts delivered artifacts only.
- **Margin check.** After settlement, compare `provider_cost_micro_sgd` with the realized revenue of the charge at the funding source's effective SGD/credit. A nonpositive/low contribution margin logs `aurora_cost_exceeds_revenue` and blocks that SKU/funding-source pair from further sales until an operator clears it; it never reprices the settled run.
- **Never customer-visible.** The cost rows and margin verdicts are internal; the API exposes only the charge lines.

- [ ] **Step 1: Write the failing tests**

```go
func TestCostRowAccumulatesRetries(t *testing.T) {
	// two attempts, one failed and one delivered → attempts 2, chargeable 2,
	// provider cost is the sum, charge counts only the delivered artifact.
}

func TestModelMismatchSettlesAtCeiling(t *testing.T) {
	// dispatched seedream-5-0-lite, runtime reports gemini-3-pro-image →
	// model_mismatch true, charged == reserved, no refund, counter incremented.
}

func TestCostAboveRevenueIsFlaggedAndBlocksSales(t *testing.T) {
	// provider cost > realized revenue → aurora_cost_exceeds_revenue logged and
	// the SKU/funding pair rejected on the next create; the settled charge is unchanged.
}

func TestCostRowIsIdempotent(t *testing.T) {
	// replay completion → one cost row.
}
```

- [ ] **Step 2: Run and confirm they fail**

Run: `cd server && go test ./internal/aurora ./internal/service -run 'TestCost|TestModelMismatch' -count=1`
Expected: FAIL.

- [ ] **Step 3: Implement**

Add the cost table query usage, compute cost from the frozen quote's cost rates rather than any delivered-text estimate, wire the pinned model/SKU into the task payload, and add the counters. Treat the runtime's usage record as bookkeeping input for cost only — never as a billing quantity. When the record is missing or unparseable, record `unknown` and refuse new metered sales for that SKU until the signal is restored.

- [ ] **Step 4: Run the tests**

Run: `cd server && go test ./internal/aurora ./internal/service -run 'TestCost|TestModelMismatch|TestSettlement' -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add server/internal/aurora/cost.go server/internal/aurora/cost_test.go server/internal/service/aurora_cost.go server/internal/service/aurora_completion.go server/internal/service/aurora_completion_test.go server/internal/service/task.go
git commit -m "feat(aurora): record provider cost and pin the dispatched model"
```

---

### Task 12: Whole-branch verification

- [ ] **Step 1: Run the Go suite**

Run: `make test`
Expected: PASS. The workstation's `internal/handler` and parts of `internal/service` carry a known pre-existing red set; record exactly which tests fail and confirm they also fail on the base commit before attributing any of them to this branch.

- [ ] **Step 2: Run the frontend suite**

Run: `pnpm typecheck && pnpm lint && pnpm test`
Expected: PASS.

- [ ] **Step 3: Confirm the money invariant**

Run: `cd server && go test ./internal/aurora ./internal/service ./internal/handler -run 'Aurora|Settlement|Charge|RateCard' -count=1`

**Additional release blocker tests**: no live rate for HOLD/unquoted/expired/
provisional/unsafe SKUs; quote == reservation == settlement for the same
selected output; validated upstream aliases; no 1080P-as-2K and no 2160/4K
borrowing; no 1-credit minimum on subcredit text; refund/idempotency; fail
before reserving for unavailable models; provider cost recorded per attempt
with cost < realized revenue < official list at the lowest permitted
SGD/credit (the two standard-H3 rows are the one sanctioned exception, priced
above the official list by owner override); model-mismatch settles at the
ceiling; missing usage disables the SKU. Text settlement is covered by its own
blockers: `deepseek-v4.1-flash` is the default text model and its charge lines
equal the `task_usage` rows at 29 / 113 credits per M tokens, a missing row
charges the ceiling with `aurora_usage_missing`, `cache_read` bills at the
card's cache rate when set and the input rate otherwise (never zero). **Signup
grant precondition (not a task in this plan):** no workspace may be switched to
metered while the 500-credit signup grant still issues. Gating it off is the
spec §10 pre-launch cleanup, whose plan ships before any metered workspace is
enabled; this plan only records the precondition, it does not remove the grant.
The S$49/5,500 and S$149/18,000 plan rows are
rejected as a policy choice — clearing 20% on them would need 13/20 or 14/22
credits/s, further above the official list; the margin gate must reject standard
MiniMax H3 at 9/15 credits/s (3.3%/5.3%), accept it at 11/18 (20.9%/21.1%) only
with the official-list-bound override recorded and the discount label
suppressed, and accept the Lite tier at 9/15 (65.9%/66.7%) under the normal
bound. Neither plan row is activated by this plan.
Expected: PASS.

- [ ] **Step 4: Commit any fixes as their own commits**
