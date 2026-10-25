# Aurora Metered Credit Billing (Phase 1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Charge every Aurora run for what it actually delivered — text by delivered tokens, image per image, video by measured resolution × measured seconds — against a per-model rate card, with a per-skill ceiling reserved up front.

**Architecture:** A versioned, server-owned rate card prices measured usage. Usage is measured once, at artifact staging, where the bytes already stream. Settlement sums the generation's assets, writes itemized `aurora_generation_charge` rows, and refunds the difference between the reserved ceiling and the actual charge. Runs created before this change carry `rate_version = 'legacy'` and settle at the old flat skill price.

**Tech Stack:** Go 1.27 (Chi, pgx, sqlc), PostgreSQL migrations, Next.js + React + TanStack Query, vitest.

**Spec:** `docs/superpowers/specs/2026-10-25-aurora-credit-billing-redesign-design.md`

**Scope:** Phase 1 only (spec §Rollout item 1). The batch wallet (spec §6) and the SGD plan catalogue (spec §Rollout item 3) get their own plans; this one ships and is meaningful without them. Phase 2 takes migration numbers 5002–5005.

## Global Constraints

- `1 credit = 1_000_000 micro-credits`; `100 credits = S$1.00`.
- All money is `int64` micro-credits. No float ever touches a balance or a rate.
- Each charge line rounds **up** to a whole credit: `amount_micro = ceilUpToCredit(raw_micro)`.
- Rates are integers in `micro-credits per unit`: per 1,000 text tokens, per image, per video second.
- Rates are keyed by **model id**. An unconfigured model fails settlement; it never charges zero.
- Model selection is server-side at generation creation. A request never supplies a model name.
- The reserved ceiling is the maximum a user can be charged for that generation; actual > reserved is absorbed and flagged, never debited.
- Migrations for this fork take numbers from 5000; `5000` is taken, so this plan uses 5001 and 5006–5008. Every index is `CREATE [UNIQUE] INDEX CONCURRENTLY` in its own single-statement file.
- No foreign keys, cascading deletes, or cascading updates.
- Code, comments, and commits in English. The Aurora UI copy stays Chinese.

## Review Focus

1. **A video whose probe fails at staging.** A file that cannot be probed must be rejected with 400 before it is stored — never stored unmeasured and settled at zero. (Task 4)
2. **A generation created before the deploy.** It carries `rate_version = 'legacy'` and must charge its old flat `credits_reserved`, never look up a model, and never re-price. (Task 6)
3. **A multi-round `needs_input` conversation.** Every round's assets belong to the same generation; settlement must sum all of them, not the last round's. (Task 6)
4. **An agent that ignores its ceiling.** The charge is capped at the reservation, the line is marked `over_cap`, the balance never goes negative, and the event is logged. (Task 6)
5. **A replayed terminal callback.** Completion and failure are each idempotent: no second refund, no second charge row. (Tasks 6, 8)

---

### Task 1: Rate card

**Files:**
- Create: `server/internal/aurora/pricing.go`
- Test: `server/internal/aurora/pricing_test.go`

**Interfaces:**
- Produces:
  - `type OutputCaps struct { MaxTextTokens int64; MaxImages int; MaxVideoMilli int64; MaxVideoHeight int }` (JSON `max_text_tokens`, `max_images`, `max_video_milli`, `max_video_height`). Task 2 attaches this type to the catalog entry; it is declared here because the ceiling is priced from it.
  - `type Measured struct { Tokens int64; Width int; Height int; DurationMS int64 }` (JSON tags `tokens`, `width`, `height`, `duration_ms`, all `omitempty`).
  - `type Usage struct { Kind, Model string; Tokens int64; Images int; SecondsMilli int64; Height int }`.
  - `type ChargeLine struct { Kind, Model string; QuantityMilli int64; UnitRateMicro int64; AmountMicro int64 }`.
  - `type VideoRate struct { Tier string; MaxHeight int; MicroPerSecond int64 }`.
  - `type ModelRate struct { Kind string; Text *TextRate; Image *ImageRate; Video []VideoRate }`.
  - `type TextRate struct { MicroPerKToken int64 }`.
  - `type ImageRate struct { MicroPerImage int64 }`.
  - `type RateCard struct { Version string; Models map[string]ModelRate }`.
  - `func DefaultRateCard() *RateCard`
  - `func (c *RateCard) Charge(u Usage) (ChargeLine, error)`
  - `func (c *RateCard) Ceiling(textModel, imageModel, videoModel string, caps OutputCaps) (int64, error)`
  - `func (c *RateCard) VideoRateForHeight(model string, height int) (VideoRate, bool)`
  - `func CountTextTokens(body []byte) int64`
  - `var ErrRateMissing = errors.New("aurora: rate missing for model")`
- Consumes: nothing.

Quantity convention (one rule, used everywhere):
- text: `QuantityMilli` = delivered tokens.
- image: `QuantityMilli` = image count × 1000.
- video: `QuantityMilli` = milliseconds.

`raw = QuantityMilli * UnitRateMicro / 1000`; `AmountMicro = ceil(raw / 1e6) * 1e6`.

- [ ] **Step 1: Write the failing tests**

```go
func TestChargeTextRoundsUpToWholeCredits(t *testing.T) {
	line, err := DefaultRateCard().Charge(Usage{Kind: "text", Model: "deepseek-v4.1-flash", Tokens: 3500})
	// 3.5k tokens × 1 credit/k = 3.5 credits → 4
	if err != nil || line.AmountMicro != 4_000_000 || line.QuantityMilli != 3500 {
		t.Fatalf("got %+v, err %v", line, err)
	}
}

func TestChargeVideoMatchesMeasuredHeight(t *testing.T) {
	// 768P output measures 720 or 768; both must land on the 768P rate (24/s).
	for _, h := range []int{720, 768, 1079} {
		line, err := DefaultRateCard().Charge(Usage{Kind: "video", Model: "MiniMax-H3", SecondsMilli: 5000, Height: h})
		if err != nil || line.UnitRateMicro != 24_000_000 || line.AmountMicro != 120_000_000 {
			t.Fatalf("height %d: got %+v, err %v", h, line, err)
		}
	}
	line, _ := DefaultRateCard().Charge(Usage{Kind: "video", Model: "MiniMax-H3", SecondsMilli: 5000, Height: 1440})
	if line.UnitRateMicro != 39_000_000 || line.AmountMicro != 195_000_000 {
		t.Fatalf("2K: got %+v", line)
	}
}

func TestChargeImages(t *testing.T) {
	line, err := DefaultRateCard().Charge(Usage{Kind: "image", Model: "seedream-5-0-lite", Images: 3})
	if err != nil || line.AmountMicro != 27_000_000 || line.QuantityMilli != 3000 {
		t.Fatalf("got %+v, err %v", line, err)
	}
}

func TestChargeUnknownModelFailsClosed(t *testing.T) {
	if _, err := DefaultRateCard().Charge(Usage{Kind: "image", Model: "nope", Images: 1}); !errors.Is(err, ErrRateMissing) {
		t.Fatalf("want ErrRateMissing, got %v", err)
	}
}

func TestCeilingSumsPerKindMaxima(t *testing.T) {
	caps := OutputCaps{MaxTextTokens: 2000, MaxImages: 4, MaxVideoMilli: 10_000, MaxVideoHeight: 1440}
	got, err := DefaultRateCard().Ceiling("deepseek-v4.1-flash", "seedream-5-0-lite", "MiniMax-H3", caps)
	// 2 + 36 + 390 credits
	if err != nil || got != 428_000_000 {
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

Run: `cd server && go test ./internal/aurora -run 'TestCharge|TestCeiling|TestCountText' -count=1`
Expected: FAIL — `undefined: DefaultRateCard`.

- [ ] **Step 3: Implement `pricing.go`**

`DefaultRateCard()` returns `Version: "2026-10-25"` with exactly:

| model | kind | rate |
|---|---|---|
| `deepseek-v4.1-flash` | text | `MicroPerKToken: 1_000_000` (1 credit / 1k tokens) |
| `seedream-5-0-lite` | image | `MicroPerImage: 9_000_000` (9 credits) |
| `MiniMax-H3` | video | `{Tier: "768P", MaxHeight: 1079, MicroPerSecond: 24_000_000}`, `{Tier: "2K", MaxHeight: 2160, MicroPerSecond: 39_000_000}` |

`VideoRateForHeight` picks the first `VideoRate` whose `MaxHeight >= height`, ordered ascending; a height above every tier is `false`.

`CountTextTokens` counts runes: `ascii*1/4 + nonASCII*3/4`, rounded up, with a minimum of 1 for a non-empty body and 0 for an empty one.

`Charge` returns `ErrRateMissing` when the model is absent or when the kind has no rate on that model.

- [ ] **Step 4: Run the tests and confirm they pass**

Run: `cd server && go test ./internal/aurora -run 'TestCharge|TestCeiling|TestCountText' -count=1`
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
  - `SkillCatalogEntry` gains `Caps OutputCaps` (`json:"caps"`), `Models OutputModels` (`json:"models"`), `MaxCredits int64` (`json:"max_credits"`). `Credits` stays, now equal to `MaxCredits/1e6` for older clients.
  - `func DefaultCaps(outputs []string) OutputCaps`
  - `func (e SkillCatalogEntry) ModelsOrDefault() OutputModels`

Caps come from the skill's declared `Output` kinds, not 33 hand-edited rows:
`video` present → `{MaxTextTokens: 2000, MaxVideoMilli: 10000, MaxVideoHeight: 2160}`;
else `image` present → `{MaxTextTokens: 2000, MaxImages: 4}`;
else → `{MaxTextTokens: 8000}`.

`ModelsOrDefault()` returns `{Text: "deepseek-v4.1-flash", Image: "seedream-5-0-lite", Video: "MiniMax-H3"}` for any empty field.

- [ ] **Step 1: Write the failing test**

```go
func TestCatalogCapsFollowDeclaredOutputs(t *testing.T) {
	for _, entry := range Catalog() {
		hasVideo, hasImage := slices.Contains(entry.Output, "video"), slices.Contains(entry.Output, "image")
		switch {
		case hasVideo && entry.Caps.MaxVideoMilli != 10000:
			t.Fatalf("%s: video cap %d", entry.ID, entry.Caps.MaxVideoMilli)
		case !hasVideo && hasImage && entry.Caps.MaxImages != 4:
			t.Fatalf("%s: image cap %d", entry.ID, entry.Caps.MaxImages)
		case !hasVideo && !hasImage && entry.Caps.MaxTextTokens != 8000:
			t.Fatalf("%s: text cap %d", entry.ID, entry.Caps.MaxTextTokens)
		}
		if entry.MaxCredits <= 0 || entry.Credits != entry.MaxCredits/1_000_000 {
			t.Fatalf("%s: max=%d credits=%d", entry.ID, entry.MaxCredits, entry.Credits)
		}
	}
}
```

- [ ] **Step 2: Run it and confirm it fails**

Run: `cd server && go test ./internal/aurora -run TestCatalogCaps -count=1`
Expected: FAIL — `entry.Caps undefined`.

- [ ] **Step 3: Implement the caps and projection**

Add the types to `catalog.go`, fill `Caps`/`Models`/`MaxCredits` in `buildCatalog()` using `DefaultCaps(expert.Output)` and `DefaultRateCard().Ceiling(...)`. `attachAttachmentRules` must copy the new fields (it builds a copy). Update `cloneEntry` so the copy keeps caps and models.

Then extend the client schema so the new fields survive parsing: add `max_credits: z.number().default(0)`, `caps` (all fields defaulted to 0) and `models` (all fields defaulted to `""`) to the skill schema in `packages/core/aurora/schema.ts`, and a malformed-response case to `packages/core/aurora/api.test.ts` asserting a payload without the new fields still parses.

- [ ] **Step 4: Run the tests**

Run: `cd server && go test ./internal/aurora -run TestCatalogCaps -count=1 && cd .. && pnpm exec vitest run packages/core/aurora/api.test.ts`
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
- Create: `server/migrations/5007_aurora_generation_charge_generation_idx.{up,down}.sql`
- Create: `server/migrations/5008_aurora_generation_rate_version.{up,down}.sql`
- Modify: `server/pkg/db/queries/aurora.sql`
- Regenerate: `server/pkg/db/generated/*` (via `make sqlc`)

**Interfaces:**
- Produces (sqlc):
  - `db.AuroraArtifactStaging.Measured []byte`, `db.AuroraAsset.Measured []byte`, `db.AuroraGeneration.RateVersion string`.
  - `CreateAuroraArtifactStagingParams.Measured json.RawMessage`, `CreateAuroraAssetParams.Measured json.RawMessage`.
  - `CreateAuroraGenerationParams.RateVersion string`.
  - `CreateAuroraGenerationChargeParams{GenerationID, WorkspaceID, UserID pgtype.UUID; Kind, Model string; QuantityMilli, UnitRateMicro, AmountMicro int64; Measured []byte; RateVersion string; OverCap bool}` and `CreateAuroraGenerationCharge`.
  - `ListAuroraGenerationCharges(ctx, generationID, workspaceID) ([]db.AuroraGenerationCharge, error)`.
  - `ListAuroraAssetsForCharge(ctx, generationID, workspaceID) ([]db.AuroraAsset, error)` — every asset of the generation, no limit.

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
-- One row per (kind, model) charge line of a settled generation. `quantity_milli`
-- counts tokens (text), images × 1000, or milliseconds (video).
CREATE TABLE IF NOT EXISTS aurora_generation_charge (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    generation_id uuid NOT NULL,
    workspace_id uuid NOT NULL,
    user_id uuid NOT NULL,
    kind text NOT NULL,
    model text NOT NULL,
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
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS aurora_generation_charge_generation_idx
    ON aurora_generation_charge (generation_id, kind, model);
```

`5008_aurora_generation_rate_version.up.sql`

```sql
ALTER TABLE aurora_generation ADD COLUMN IF NOT EXISTS rate_version text NOT NULL DEFAULT 'legacy';
```

Each `.down.sql` reverses its own statement (`DROP INDEX CONCURRENTLY IF EXISTS`, `DROP TABLE IF EXISTS`, `DROP COLUMN IF EXISTS`).

- [ ] **Step 2: Add the queries to `aurora.sql` and regenerate**

Add `CreateAuroraGenerationCharge` (`INSERT … ON CONFLICT (generation_id, kind, model) DO NOTHING`), `ListAuroraGenerationCharges`, `ListAuroraAssetsForCharge`. Add `measured` to `CreateAuroraArtifactStaging`, `rate_version` to `CreateAuroraGeneration`, `measured` to the asset insert.

- [ ] **Step 3: Regenerate and build**

Run: `make sqlc && cd server && go build ./...`
Expected: builds; the migration lint test passes.

Run: `cd server && go test ./internal/migrations -run TestMigration -count=1`
Expected: PASS (no duplicate numeric prefixes).

- [ ] **Step 4: Commit**

```bash
git add server/migrations server/pkg/db/queries/aurora.sql server/pkg/db/generated
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
- text → `aurora.CountTextTokens(body)`.
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
- Consumes: `aurora.RateCard`, `aurora.Charge`, `aurora.Lookup(gen.SkillID)`, `db.ListAuroraAssetsForCharge`, `db.CreateAuroraGenerationCharge`, `Credit.Refund`.
- Produces:
  - `TaskService.RateCard *aurora.RateCard` (set beside `taskSvc.Credit` in `handler.go`).
  - `func (s *TaskService) auroraChargeLines(gen db.AuroraGeneration, assets []db.AuroraAsset) ([]aurora.ChargeLine, error)`

Behaviour:
- `gen.RateVersion == "legacy"` → one line at the old flat price: `AmountMicro = gen.CreditsReserved`, `Kind = "legacy"`, `Model = "legacy"`, `QuantityMilli = 1000`. No model lookup.
- Otherwise: group assets by kind; text sums `measured.tokens` over `role != "transcript"`; images count; video sums `measured.duration_ms` and prices each asset by its own `measured.height`.
- `actual = min(sum, gen.CreditsReserved)`. `over_cap = sum > gen.CreditsReserved`.
- Write the charge rows (idempotent via the unique index), then `Refund(gen.CreditsReserved - actual)` when positive, with reference `settle:<generationID>`.
- Then `UpdateAuroraGenerationTerminal` with `CreditsCharged: actual`.

- [ ] **Step 1: Write the failing tests**

```go
func TestSettlementRefundsTheDifference(t *testing.T) {
	// reserved 428 credits; assets: one 5s 2K video + 1,200 text tokens.
	// → actual = 195 + 2 = 197 credits, refund 231 credits, one refund row.
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
	// assets reported across two rounds of one generation both count.
}

func TestSettlementIsIdempotent(t *testing.T) {
	// settle twice → one refund row, one set of charge rows.
}
```

- [ ] **Step 2: Run and confirm they fail**

Run: `cd server && go test ./internal/service -run TestSettlement -count=1`
Expected: FAIL.

- [ ] **Step 3: Implement**

Replace the `Limit: 1` asset read in `settleAuroraOnCompleted` with `ListAuroraAssetsForCharge`. A read failure still returns without settling (existing rule). An unknown model leaves the generation non-terminal and logs, so a later replay can settle it.

- [ ] **Step 4: Run the tests**

Run: `cd server && go test ./internal/service -run TestSettlement -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add server/internal/service/aurora_completion.go server/internal/service/aurora_completion_test.go server/internal/handler/handler.go
git commit -m "feat(aurora): settle generations by measured usage"
```

---

### Task 7: Reserve the ceiling at creation

**Files:**
- Modify: `server/internal/handler/aurora.go`
- Test: `server/internal/handler/aurora_test.go`

**Interfaces:**
- Consumes: `entry.Caps`, `entry.MaxCredits`, `RateCard.Ceiling`.
- Produces:
  - `func writeAuroraInsufficientCredits(w http.ResponseWriter, required, available int64)` writing
    `{"error":"insufficient credits","requiredCredits":N,"availableCredits":N,"shortfallCredits":N}` with 402.
  - `CreateAuroraGeneration` writes `rate_version` when inserting the generation row.

Behaviour:
1. `amountMicro := entry.MaxCredits` (already the ceiling).
2. If `balance < amountMicro` → 402 with the three figures; nothing is created.
3. Otherwise reserve `amountMicro` as today.
4. The task brief gains one line: `本次最多可交付：图片 N 张 / 视频 N 秒（上限，超出不额外计费）。` — the caps, not the price.

- [ ] **Step 1: Write the failing tests**

```go
func TestCreateRefusesWhenBalanceBelowCeiling(t *testing.T) {
	// balance 100 credits, video skill ceiling 428 → 402 and body carries
	// requiredCredits 428, availableCredits 100, shortfallCredits 328;
	// no generation row, no issue, no reservation.
}

func TestCreateReservesTheCeiling(t *testing.T) {
	// balance 1000 → reserved == entry.MaxCredits, and the row's rate_version
	// is the card's version.
}
```

- [ ] **Step 2: Run and confirm they fail**

Run: `cd server && go test ./internal/handler -run 'TestCreateRefuses|TestCreateReserves' -count=1`
Expected: FAIL.

- [ ] **Step 3: Implement**

Replace the `entry.Credits` arithmetic with `entry.MaxCredits`, add the shortfall response, and pass `RateVersion: aurora.DefaultRateCard().Version` into `CreateAuroraGeneration`.

- [ ] **Step 4: Run the tests**

Run: `cd server && go test ./internal/handler -run 'TestCreate' -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add server/internal/handler/aurora.go server/internal/handler/aurora_test.go
git commit -m "feat(aurora): reserve the declared output ceiling"
```

---

### Task 8: Expose the charge lines

**Files:**
- Modify: `server/internal/handler/aurora.go`
- Test: `server/internal/handler/aurora_test.go`

**Interfaces:**
- Produces: `GET /api/aurora/generations/{id}` gains
  `creditsCharged`, `rateVersion`, and
  `charges: [{kind, model, quantityMilli, unitRateMicro, amountMicro, measured, overCap}]`,
  omitted as `[]` when the generation has not settled.

- [ ] **Step 1: Write the failing test**

```go
func TestGenerationDetailListsCharges(t *testing.T) {
	// settled generation with two charge rows → response carries both, in
	// amount-descending order, each with model and amountMicro.
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
- Modify: `packages/core/aurora/api.test.ts`
- Modify: `packages/views/aurora/generation-detail.tsx` (+ `.test.tsx`)
- Modify: `packages/views/aurora/skill-directory.tsx`, `packages/views/aurora/generation-composer.tsx` (+ tests)

**Interfaces:**
- Consumes: the detail response from Task 8, `entry.maxCredits` from Task 2.

Behaviour:
- Skill card and composer show `最多 {maxCredits} 积分` instead of `{credits} 积分`.
- The insufficient-credits dialog shows the shortfall from the 402 body.
- The result page renders one row per charge: `3 张图 × 9 积分（seedream-5-0-lite）`,
  `视频 2K × 5.0 秒 × 39 积分/秒（MiniMax-H3）`, `文本 3,500 tokens × 1 积分/千 token（deepseek-v4.1-flash）`,
  then the total. A `legacy` line renders as `本次 90 积分`.
- Never show the reserved ceiling on the result page.

- [ ] **Step 1: Write the failing component test**

```tsx
it("itemizes a settled generation", () => {
  renderDetail({ creditsCharged: 197_000_000, charges: [
    { kind: "video", model: "MiniMax-H3", quantityMilli: 5000, unitRateMicro: 39_000_000, amountMicro: 195_000_000, overCap: false },
    { kind: "text",  model: "deepseek-v4.1-flash", quantityMilli: 3500, unitRateMicro: 1_000_000, amountMicro: 2_000_000, overCap: false },
  ]});
  expect(screen.getByText(/5\.0 秒/)).toBeTruthy();
  expect(screen.getByText(/197/)).toBeTruthy();
});
```

- [ ] **Step 2: Run and confirm it fails**

Run: `pnpm exec vitest run packages/views/aurora/generation-detail.test.tsx`
Expected: FAIL.

- [ ] **Step 3: Implement**

Add optional, defaulted `charges`/`creditsCharged`/`rateVersion` to the generation schema; render the block only when `charges.length > 0`. Update the two skill display sites to `最多 N 积分`.

- [ ] **Step 4: Run the tests**

Run: `pnpm exec vitest run packages/views/aurora packages/core/aurora`
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

### Task 11: Whole-branch verification

- [ ] **Step 1: Run the Go suite**

Run: `make test`
Expected: PASS. The workstation's `internal/handler` and parts of `internal/service` carry a known pre-existing red set; record exactly which tests fail and confirm they also fail on the base commit before attributing any of them to this branch.

- [ ] **Step 2: Run the frontend suite**

Run: `pnpm typecheck && pnpm lint && pnpm test`
Expected: PASS.

- [ ] **Step 3: Confirm the money invariant**

Run: `cd server && go test ./internal/aurora ./internal/service ./internal/handler -run 'Aurora|Settlement|Charge|RateCard' -count=1`
Expected: PASS.

- [ ] **Step 4: Commit any fixes as their own commits**
