# Aurora 工具 → 专家映射 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 18 个工具页提交的任务落到该工具指定的专家 agent，并补齐 9 个缺失专家、修掉三项遗留缺陷。

**Architecture:** 映射表归服务端（`server/internal/aurora/tool_experts.go`），前端不再猜执行者；新增 `POST /api/aurora/tools/{toolId}/runs` 由服务端 seed 专家、解析 carrier、检查运行时后建 issue。33 个专家在首次进入 Aurora 时一次性 seed；carrier 为 `public_to` + workspace 调用目标，owner 恒为工作区 owner。

**Tech Stack:** Go（Chi、sqlc、pgx）、Next.js 16 App Router、React Query、Zod、Vitest、node:test、PostgreSQL 迁移。

**Spec:** [docs/superpowers/specs/2026-10-23-aurora-tool-expert-mapping-design.md](../specs/2026-10-23-aurora-tool-expert-mapping-design.md)

**Status (2026-10-23):** Tasks 1–9 complete, each gated by an independent task review.
Task 1 is on PR #17 (`47ee4c79f`); Tasks 2 and 3 are PR #21 and PR #22; Tasks 4–9 are
on the draft PR #23. Tasks 10–13 (run endpoint, core client, frontend switch, docs and
full verification) remain.

## Global Constraints

- 18 个工具 id 必须全部有映射；未映射返回 409 `tool_executor_unmapped`，且**不创建 issue**。
- 运行时不可达返回 503，且**不创建 issue**。
- 工具任务**不计 Aurora 积分**。
- 新迁移号段 **≥ 5000**；已应用的迁移**不重命名**。
- 不复用 `no-agent` 表示「未映射」；新状态名为 `no-expert`。
- 不伪造媒体产出：brief 必须写明真实模型 id，或写明「当前环境没有可用的媒体生成 provider」。
- 代码注释用英文；UI 文案用简体中文。
- PR 目标仓库 `apexai-sg/multica`，基线分支 `apexai/main`；conventional commits。

## Review Focus

- 附件 id 属于别的工作区时，必须 400 而不是静默丢附件 —— Task 10 的测试覆盖。
- 成员 B 在成员 A 先 seed 之后提交，必须 200 —— Task 7 的 owner-drift 回归测试覆盖。
- 运行时不可达时不得留下「永不执行」的 issue —— Task 10 覆盖。
- 未映射的工具 id 必须 409 且零 issue —— Task 10 覆盖。
- admin 能在列表里看到他人 private agent，但不得被前端选中 —— Task 1 与 Task 12 覆盖。
- 跨时钟域偏差不得拒绝合法观测 —— Task 3 覆盖。
- 本地专家缺 brief 或五段不全 —— Task 5 的测试覆盖。

---

### Task 1: PR #17 — 只提供可派活的 agent

**Files:**
- Modify: `apps/aurora/components/tools/use-tool-submission.ts`
- Test: `apps/aurora/components/tools/use-tool-submission.test.tsx`

**Interfaces:**
- Consumes: `canAssignAgentToIssue(agent, { userId, role })` from `@multica/core/permissions` (returns `{ allowed: boolean }`); `useAuthStore` from `@multica/core/auth`; `memberListOptions(wsId)` from `@multica/core/workspace/queries`.
- Produces: unchanged `useToolSubmission` return shape.

- [ ] **Step 1: Write the failing test**

在 `use-tool-submission.test.tsx` 增加两个用例。mock 需要 `api.listMembers` 与 auth store：

```tsx
vi.mock("@multica/core/auth", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/auth")>()),
  useAuthStore: Object.assign(
    (selector: (state: { user: { id: string } }) => unknown) => selector({ user: { id: "u-admin" } }),
    { getState: () => ({ user: { id: "u-admin" } }) },
  ),
}));
```

```tsx
it("skips another member's private agent, which the list shows to admins but the assign gate refuses", async () => {
  mocks.listMembers.mockResolvedValue([{ user_id: "u-admin", role: "admin" }]);
  mocks.listAgents.mockResolvedValue([
    agent({ id: "private", owner_id: "u-other", permission_mode: "private" }),
    agent({ id: "shared", system_key: "mika", permission_mode: "public_to",
            invocation_targets: [{ target_type: "workspace", target_id: "ws-1" }] }),
  ]);
  const { result } = await render();
  expect(result.current.agent?.id).toBe("shared");
});

it("reports no-agent when the only visible agent is not assignable", async () => {
  mocks.listMembers.mockResolvedValue([{ user_id: "u-admin", role: "admin" }]);
  mocks.listAgents.mockResolvedValue([
    agent({ id: "private", owner_id: "u-other", permission_mode: "private" }),
  ]);
  const { result } = await render();
  expect(result.current.status).toBe("no-agent");
  expect(result.current.canSubmit).toBe(false);
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `pnpm --filter @multica/aurora exec vitest run components/tools/use-tool-submission.test.tsx`
Expected: FAIL — the first assertion receives `"private"`.

- [ ] **Step 3: Implement the invocation filter**

在 hook 内取 `const userId = useAuthStore((s) => s.user?.id ?? null)` 与 `const { data: members, isLoading: membersLoading } = useQuery(memberListOptions(wsId))`；把 `members.find((m) => m.user_id === userId)?.role` 收敛为 `owner | admin | member | null`。`activeAgents` 在原有「未归档 + `isAgentRuntimeBound`」之上再过滤 `canAssignAgentToIssue(candidate, { userId, role }).allowed`。`status` 的 `loading` 分支同时包含 `membersLoading`。

- [ ] **Step 4: Run tests to verify they pass**

Run: `pnpm --filter @multica/aurora exec vitest run components/tools/use-tool-submission.test.tsx`
Expected: PASS（全部用例）。

- [ ] **Step 5: Commit and push to the PR branch**

```bash
git add apps/aurora/components/tools/use-tool-submission.ts apps/aurora/components/tools/use-tool-submission.test.tsx
git commit -m "fix(aurora): only offer agents the viewer may assign"
git push apexai fix/aurora-tool-submission-agent
```

---

### Task 2: #18 — 迁移号段 grandfather

**Files:**
- Modify: `server/internal/migrations/migrations_lint_test.go`

**Interfaces:**
- Consumes: existing `grandfatheredSharedPrefixes map[int]string`, `auroraLegacyPrefixes map[int]bool`.
- Produces: no exported symbols.

- [ ] **Step 1: Run the failing test**

Run: `cd server && go test ./internal/migrations/ -run TestMigrationNumericPrefixesAreUnique -count=1`
Expected: FAIL，15 行 `share numeric prefix`（573–579、581–588）。

- [ ] **Step 2: Add the 15 entries**

按现有格式逐条加入，值写两个文件名，例如：

```go
573: "573_fleet_nodes_owner_index / 573_issue_workflow",
588: "588_aurora_asset_media_url_idx / 588_daemon_token_owner",
```

- [ ] **Step 3: Run the test to verify it passes**

Run: `cd server && go test ./internal/migrations/ -run TestMigrationNumericPrefixesAreUnique -count=1`
Expected: PASS。

- [ ] **Step 4: Add the anti-rot test**

```go
func TestGrandfatheredPrefixesStillCollide(t *testing.T) {
	files := migrationFilesForLint(t, "*.up.sql")
	count := map[int]int{}
	for _, file := range files {
		stem, _, ok := splitMigrationFilename(filepath.Base(file))
		if !ok { continue }
		prefix, _, ok := strings.Cut(stem, "_")
		if !ok { continue }
		if n, err := strconv.Atoi(prefix); err == nil { count[n]++ }
	}
	for number := range grandfatheredSharedPrefixes {
		if count[number] < 2 {
			t.Errorf("grandfathered prefix %d no longer collides; remove it", number)
		}
	}
}
```

Run: `cd server && go test ./internal/migrations/ -run TestGrandfatheredPrefixesStillCollide -count=1` → PASS。

- [ ] **Step 5: Cover `fleet_*` in the reserved-range test**

`TestAuroraMigrationsUseTheReservedRange` 的过滤条件改为 `strings.Contains(stem, "aurora") || strings.Contains(stem, "fleet")`；把 `auroraLegacyPrefixes` 改名 `forkLegacyPrefixes` 并补进 564–584 中尚未列出的号：564–572、573–579、580、581、582、584（583 已存在）。

Run: `cd server && go test ./internal/migrations/ -count=1` → PASS。

- [ ] **Step 6: Commit**

```bash
git add server/internal/migrations/migrations_lint_test.go
git commit -m "fix(migrations): grandfather the 573-588 fork/upstream collisions"
```

---

### Task 3: #19 — fleet/store 的 DATABASE_URL 契约与跨时钟容差

**Files:**
- Modify: `server/internal/testutil/fleet.go`
- Modify: `server/internal/fleet/store/recovery.go`
- Modify: `server/internal/fleet/store/recovery_test.go`
- Modify: `server/internal/fleet/store/aurora_bootstrap_test.go`（注释）
- Modify: `AGENTS.md`（Aurora 遗留项第 10 条）

**Interfaces:**
- Produces: `const observationClockSkew = 2 * time.Second`（store 包内私有）。

- [ ] **Step 1: Write the failing unit test**

在 `TestRecoveryObservationValidation`（`recovery_test.go`）的 `cases` 表里，把 `future` 改为 `o.ObservedAt = now.Add(3 * time.Second)`，并新增：

```go
{"future-within-clock-skew", func(o *model.Observation) { o.ObservedAt = now.Add(1500 * time.Millisecond) }, true},
```

Run: `cd server && go test ./internal/fleet/store/ -run TestRecoveryObservationValidation -count=1`
Expected: FAIL — `future-within-clock-skew` 被判为无效。

- [ ] **Step 2: Implement the skew bound**

在 `recovery.go` 增加：

```go
// observationClockSkew bounds the comparison between an observation stamped by
// the caller's clock and a timestamp read from PostgreSQL. The two are separate
// clock domains; sub-second NTP skew is not evidence of a forged observation.
const observationClockSkew = 2 * time.Second
```

`validateObservation` 的未来判据改为 `o.ObservedAt.After(now.Add(observationClockSkew))`；`bootstrapObservation` 的判据改为 `o.ObservedAt.Add(observationClockSkew).Before(snap.Operation.BootstrapClaimedAt)`。30 秒陈旧上限不变。

- [ ] **Step 3: Run the test to verify it passes**

Run: `cd server && go test ./internal/fleet/store/ -run TestRecoveryObservationValidation -count=1`
Expected: PASS。

- [ ] **Step 4: Skip without DATABASE_URL**

`NewFleetFixture` 的 `t.Fatal("Fleet tests require explicit DATABASE_URL")` 改为 `t.Skip("Fleet tests require explicit DATABASE_URL")`。

Run: `cd server && DATABASE_URL= go test ./internal/fleet/store/ -count=1`
Expected: `ok`，退出码 0（全部 SKIP）。

- [ ] **Step 5: Verify against a throwaway database**

```bash
createdb -h 127.0.0.1 -p 5432 -U multica multica_fleet_fix_check
cd server && DATABASE_URL='postgres://multica:multica@localhost:5432/multica_fleet_fix_check?sslmode=disable' go run ./cmd/migrate up
for i in 1 2 3 4 5; do DATABASE_URL='postgres://multica:multica@localhost:5432/multica_fleet_fix_check?sslmode=disable' go test ./internal/fleet/store/ -count=1 || break; done
dropdb -h 127.0.0.1 -p 5432 -U multica multica_fleet_fix_check
```

Expected: 5 次全 PASS。

- [ ] **Step 6: Update the comments and AGENTS.md**

删除 `claimAuroraRebootstrap` 上「ConfirmBootstrap 比较本地时钟因此跳过」的过时理由；把 `AGENTS.md` Aurora 遗留项第 10 条改成「已用 2 秒跨时钟容差修复；无 `DATABASE_URL` 时整套 skip」。

- [ ] **Step 7: Commit**

```bash
git add server/internal/testutil/fleet.go server/internal/fleet/store/recovery.go server/internal/fleet/store/recovery_test.go server/internal/fleet/store/aurora_bootstrap_test.go AGENTS.md
git commit -m "fix(fleet): tolerate cross-clock observation skew and skip without DATABASE_URL"
```

---

### Task 4: 导入脚本接受本地专家

**Files:**
- Modify: `scripts/aurora-import-experts.mjs`
- Create: `server/internal/aurora/experts-local/index.json`
- Create: `server/internal/aurora/experts/media-face-swap/workflow.md`
- Test: `scripts/aurora-import-experts.test.mjs`

**Interfaces:**
- Produces: `buildLibrary(sourceDir, localIndexPath = LOCAL_INDEX_ABS)`；`export function isManagedBrief(relPath: string, localSlugs: Set<string>): boolean`。

- [ ] **Step 1: Write the failing test**

在 `aurora-import-experts.test.mjs` 加：

```js
test("merges locally authored experts into the generated index", async () => {
  const { dir } = scaffold();
  const local = join(dir, "local.json");
  writeFileSync(local, JSON.stringify({ experts: [{
    slug: "media-demo-local", name_zh: "本地演示", name_en: "Local Demo",
    scene: "media", description_zh: "本地", description_en: "Local",
    quick_prompts: [{ label_zh: "开始", label_en: "Start", prompt_zh: "开始", prompt_en: "Start" }],
  }] }));
  const { files } = await buildLibrary(dir, local);
  const index = JSON.parse(files.get("server/internal/aurora/experts/index.json"));
  assert.ok(index.experts.some((e) => e.slug === "media-demo-local"));
  assert.ok(index.experts.some((e) => e.slug === "ecommerce-demo-a"));
});

test("a locally authored brief is not compared by --check", () => {
  assert.equal(isManagedBrief("media-demo-local/workflow.md", new Set(["media-demo-local"])), false);
  assert.equal(isManagedBrief("ecommerce-demo-a/workflow.md", new Set(["media-demo-local"])), true);
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `node --test scripts/aurora-import-experts.test.mjs`
Expected: FAIL — `buildLibrary` 忽略第二个参数 / `isManagedBrief` 未导出。

- [ ] **Step 3: Implement the merge**

`buildLibrary` 读取本地 index，把条目转成与上游相同的 expert 形状（`missing_subskills: []`）后与快照专家合并，按 slug 排序再 `renderIndex`。`currentFiles()` 的受管判定改为 `file.path === "index.json" || (file.path.endsWith("/workflow.md") && isManagedBrief(file.path, localSlugs))`，`localSlugs` 来自本地 index。

- [ ] **Step 4: Run test to verify it passes**

Run: `node --test scripts/aurora-import-experts.test.mjs`
Expected: PASS。

- [ ] **Step 5: Add the first real local expert**

`experts-local/index.json` 写入 `media-face-swap` 一条；字段与 Go 的 `aurora.Expert` 对齐（`slug`、`name_zh`、`name_en`、`scene`、`description_zh`、`description_en`、`missing_subskills: []`、`quick_prompts`（1–3 条，四条双语字段齐全）、`subskills: []`）。`experts/media-face-swap/workflow.md` 写五段（`## Inputs`、`## Persona`、`## Steps`、`## Required outputs`、`## Artifact manifest`、`## Failure behavior`），Failure behavior 写明：当前环境没有可用的换脸模型，不得伪造视频，改为给出可执行的替代方案。

- [ ] **Step 6: Commit**

```bash
git add scripts/aurora-import-experts.mjs scripts/aurora-import-experts.test.mjs server/internal/aurora/experts-local/index.json server/internal/aurora/experts/media-face-swap/workflow.md
git commit -m "feat(aurora): accept locally authored experts in the expert importer"
```

---

### Task 5: 其余 8 个专家与目录定义

**Files:**
- Create: 8 × `server/internal/aurora/experts/<slug>/workflow.md`
- Modify: `server/internal/aurora/experts-local/index.json`
- Modify: `server/internal/aurora/experts_catalog.go`
- Modify: `server/internal/aurora/catalog.go`
- Test: `server/internal/aurora/workflows_test.go`

**Interfaces:**
- `expertDef` 增加 `Inputs []string`、`Outputs []string`（为空时沿用包级默认 `expertInputs`/`expertOutputs`）。
- 新增 `func mediaConstraint(min, max int, kinds ...AttachmentKind) []AttachmentConstraint`。

- [ ] **Step 1: Write the failing honesty test**

```go
func TestMediaExpertsNameTheirProviderPath(t *testing.T) {
	pinned := []string{"doubao-seedream-5-0-pro-260628", "doubao-seedance-2-5-260628"}
	const noProvider = "当前环境没有可用的媒体生成 provider"
	for _, slug := range []string{
		"media-face-swap", "media-image-retouch", "media-portrait-studio",
		"media-video-translation", "media-voiceover", "office-spreadsheet-analysis",
		"office-document-reading", "office-resume-optimization", "design-website-build",
	} {
		brief, ok := aurora.Workflow(slug)
		if !ok { t.Fatalf("%s has no workflow", slug) }
		if !strings.Contains(brief, noProvider) && !slices.ContainsFunc(pinned, func(id string) bool { return strings.Contains(brief, id) }) {
			t.Errorf("%s names neither a pinned model id nor the no-provider clause", slug)
		}
	}
}
```

Run: `cd server && go test ./internal/aurora/ -run TestMediaExpertsNameTheirProviderPath -count=1`
Expected: FAIL — brief 不存在。

- [ ] **Step 2: Add the 8 briefs and index entries**

slug 与用途：`media-image-retouch`（Seedream 编辑）、`media-portrait-studio`（Seedream 生成）、`media-video-translation`（ASR+翻译+字幕）、`media-voiceover`（TTS，写明核实路径）、`office-spreadsheet-analysis`（openpyxl/csv）、`office-document-reading`（文本+页码）、`office-resume-optimization`（文本+PDF）、`design-website-build`（HTML/Tailwind）。每份五段齐全，媒体类写明模型 id 或 no-provider 子句。`experts-local/index.json` 的每条与 Task 4 的字段一致（含 `subskills: []`）。

- [ ] **Step 3: Add the 9 catalog definitions**

`expertDefs` 追加 9 行；`expertDef` 增加 `Inputs`/`Outputs` 并按专家填：图片类 `Inputs: {"text","image"}`、`Outputs: {"image"}`；视频类 `Outputs: {"video"}`；音频 `Outputs: {"audio"}`；办公类保持默认。`buildCatalog` 改为 `Input: slices.Clone(def.Inputs or expertInputs)`、`Output: ...`。附件约束用 `mediaConstraint(0, 1, AttachmentImage)` 等。

- [ ] **Step 4: Regenerate the embedded index**

The Go catalog reads `server/internal/aurora/experts/index.json`, not `experts-local/index.json`. Regenerate it from the snapshot plus the local index:

```bash
node scripts/aurora-import-experts.mjs --source /tmp/octop-expert-market
node scripts/aurora-import-experts.mjs --check --source /tmp/octop-expert-market
```

Expected: the first run reports only `server/internal/aurora/experts/index.json` changed; the second prints `aurora expert library is up to date (33 experts)` and exits 0. If any other file changes, stop and report — the snapshot no longer matches the committed tree.

Do not hand-edit `experts/index.json`; it is generated. `experts-local/index.json` remains the authoring source for the nine.

- [ ] **Step 5: Update the count assertions**

`server/internal/aurora/experts_assets_test.go:18`、`workflows_test.go:40` 的 24 改为 33；`server/internal/handler/aurora_test.go` 的 `len(out.Skills) != 24` 同样改为 33。

Run: `cd server && go test ./internal/aurora/ -count=1`
Expected: PASS（含 `TestRewrittenWorkflowsDescribeRealSteps`）。

- [ ] **Step 6: Commit**

```bash
git add server/internal/aurora/experts server/internal/aurora/experts-local server/internal/aurora/experts_catalog.go server/internal/aurora/catalog.go server/internal/aurora/*_test.go server/internal/handler/aurora_test.go
git commit -m "feat(aurora): add the nine experts the tool pages need"
```

---

### Task 6: 首次进入 Aurora 时全量 seed

**Files:**
- Modify: `server/pkg/db/queries/member.sql`（新增 `GetWorkspaceOwnerUserID`），运行 `make sqlc`
- Modify: `server/internal/handler/aurora.go`
- Test: `server/internal/handler/aurora_test.go`

**Interfaces:**
- Produces: `func (h *Handler) ensureAuroraCatalogSeed(ctx context.Context, workspaceID pgtype.UUID) error`
- Consumes: `aurora.EnsureSystemAgents(ctx, q, workspaceID, ownerID)`；`h.Queries.GetWorkspaceOwnerUserID`。

- [ ] **Step 1: Add the query**

```sql
-- name: GetWorkspaceOwnerUserID :one
-- The owner a product-defined carrier belongs to. Expert carriers and the
-- managed runtime must share one owner, or runtimeVerdict blocks the workspace.
SELECT user_id FROM member WHERE workspace_id = $1 AND role = 'owner' LIMIT 1;
```

Run: `make sqlc`。

- [ ] **Step 2: Write the failing test**

在 `aurora_test.go` 建一个一次性工作区并直接调用 helper：

```go
func TestEnsureAuroraCatalogSeedSeedsEveryExpertOnce(t *testing.T) {
	ws := dbfx.Workspace(t, "aurora seed", "aurora-seed-"+uuid.NewString())
	dbfx.Member(t, ws, testUserID, "owner")
	ctx := context.Background()
	if err := testHandler.ensureAuroraCatalogSeed(ctx, parseUUID(ws)); err != nil { t.Fatal(err) }
	if n := dbfx.Count(t, `SELECT count(*) FROM agent WHERE workspace_id=$1 AND system_key LIKE 'aurora:%'`, ws); n != 33 {
		t.Fatalf("carriers = %d, want 33", n)
	}
	if err := testHandler.ensureAuroraCatalogSeed(ctx, parseUUID(ws)); err != nil { t.Fatal(err) }
	if n := dbfx.Count(t, `SELECT count(*) FROM agent WHERE workspace_id=$1 AND system_key LIKE 'aurora:%'`, ws); n != 33 {
		t.Fatalf("second seed duplicated carriers: %d", n)
	}
}
```

Run: `cd server && go test ./internal/handler/ -run TestEnsureAuroraCatalogSeedSeedsEveryExpertOnce -count=1`
Expected: FAIL — `ensureAuroraCatalogSeed` 未定义。

- [ ] **Step 3: Implement the guarded seed**

```go
func (h *Handler) ensureAuroraCatalogSeed(ctx context.Context, workspaceID pgtype.UUID) error
```

实现：取 `aurora:<workspaceID>` advisory xact lock；`Count` 存活 `aurora:%` carrier；小于 `len(aurora.Catalog())` 时按 owner 调 `aurora.EnsureSystemAgents` 并提交。`ListAuroraSkills` 在 `aurora.Catalog()` 之前调用它（失败只记日志，不阻断目录）。

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd server && go test ./internal/handler/ -run 'TestEnsureAuroraCatalogSeedSeedsEveryExpertOnce|TestListAuroraSkills' -count=1`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add server/pkg/db/queries/member.sql server/pkg/db/generated server/internal/handler/aurora.go server/internal/handler/aurora_test.go
git commit -m "feat(aurora): seed every expert the first time a workspace opens Aurora"
```

---

### Task 7: carrier 权限、归属与回填迁移

**Files:**
- Modify: `server/pkg/db/queries/aurora_agents.sql`
- Modify: `server/internal/aurora/agents.go`
- Modify: `server/internal/handler/aurora.go`
- Create: `server/migrations/5000_aurora_carrier_invocation.up.sql` / `.down.sql`
- Test: `server/internal/handler/aurora_test.go`

**Interfaces:**
- `ensureAuroraExpertSeed(ctx, workspaceID, skillID)` 改为内部解析工作区 owner（签名去掉 `ownerID`）。
- `ensureExpert` 在拿到 carrier 后调用 `q.CreateAgentInvocationTarget(ctx, db.CreateAgentInvocationTargetParams{AgentID: agent.ID, TargetType: "workspace", TargetID: workspaceID})`。

- [ ] **Step 1: Write the failing test**

```go
func TestAuroraCarrierIsAssignableByANonOwnerMember(t *testing.T) {
	ws := dbfx.Workspace(t, "aurora perms", "aurora-perms-"+uuid.NewString())
	dbfx.Member(t, ws, testUserID, "owner")
	member := dbfx.User(t, "aurora member", "aurora-"+uuid.NewString()+"@test.invalid")
	dbfx.Member(t, ws, member, "member")
	// owner seeds first, then the member seeds again: the carrier must stay the owner's.
	_ = testHandler.ensureAuroraCatalogSeed(context.Background(), parseUUID(ws))
	req := newRequestAs(member, http.MethodGet, "/api/aurora/skills", nil)
	req.Header.Set("X-Workspace-ID", ws)
	testutil.Call(t, testHandler.ListAuroraSkills, req).Want(http.StatusOK)
	var carrierOwner, runtimeOwner string
	dbfx.QueryRow(t, `SELECT owner_id::text FROM agent WHERE workspace_id=$1 AND system_key='aurora:media-face-swap'`, ws).Scan(&carrierOwner)
	dbfx.QueryRow(t, `SELECT owner_id::text FROM agent_runtime WHERE workspace_id=$1 AND provider='aurora_managed'`, ws).Scan(&runtimeOwner)
	if carrierOwner != testUserID || runtimeOwner != testUserID { t.Fatalf("owners = %s / %s", carrierOwner, runtimeOwner) }
}
```

Run: `cd server && go test ./internal/handler/ -run TestAuroraCarrierIsAssignableByANonOwnerMember -count=1`
Expected: FAIL — 第二次 seed 会把 owner 改成 `member`。

- [ ] **Step 2: Make the carrier public and owner-stable**

`UpsertAuroraSystemAgent` 与 `AdoptAuroraSystemAgent` 的 `permission_mode` 改 `'public_to'`；`ensureExpert` 补调用目标（`target_id` 存 workspace_id，迁移 130 为 NOT NULL）。`ensureAuroraCatalogSeed`、`ensureAuroraExpertSeed`、`CreateAuroraManagedRuntime` 一律用 `GetWorkspaceOwnerUserID` 的结果作为 owner。

Run: `make sqlc && cd server && go test ./internal/handler/ -run TestAuroraCarrierIsAssignableByANonOwnerMember -count=1`
Expected: PASS。

- [ ] **Step 3: Write the backfill migration**

`5000_aurora_carrier_invocation.up.sql`：

```sql
UPDATE agent SET permission_mode = 'public_to', updated_at = now()
WHERE system_key LIKE 'aurora:%' AND permission_mode <> 'public_to';

INSERT INTO agent_invocation_target (agent_id, target_type, target_id)
SELECT a.id, 'workspace', a.workspace_id FROM agent a
WHERE a.system_key LIKE 'aurora:%'
ON CONFLICT (agent_id, target_type, target_id) DO NOTHING;

UPDATE agent_runtime rt SET owner_id = m.user_id, updated_at = now()
FROM member m
WHERE rt.provider = 'aurora_managed' AND m.workspace_id = rt.workspace_id AND m.role = 'owner';
```

`.down.sql` 反向改回 `private` 并删除这些 carrier 的 workspace 目标。

- [ ] **Step 4: Run the migration and the package tests**

```bash
createdb -h 127.0.0.1 -p 5432 -U multica multica_aurora_perm_check
cd server && DATABASE_URL='postgres://multica:multica@localhost:5432/multica_aurora_perm_check?sslmode=disable' go run ./cmd/migrate up
DATABASE_URL='postgres://multica:multica@localhost:5432/multica_aurora_perm_check?sslmode=disable' go test ./internal/handler/ -run 'Aurora' -count=1
dropdb -h 127.0.0.1 -p 5432 -U multica multica_aurora_perm_check
```

Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add server/pkg/db server/internal/aurora/agents.go server/internal/handler/aurora.go server/internal/handler/aurora_test.go server/migrations/5000_aurora_carrier_invocation.*.sql
git commit -m "fix(aurora): make expert carriers invocable and owned by the workspace owner"
```

---

### Task 8: 工具 → 专家映射表

**Files:**
- Create: `server/internal/aurora/tool_experts.go`
- Test: `server/internal/aurora/tool_experts_test.go`

**Interfaces:**
- Produces: `func ToolExpert(toolID string) (string, bool)`；`func ToolIDs() []string`（固定顺序的 18 个 id）。

- [ ] **Step 1: Write the failing test**

```go
func TestEveryShippedToolHasACatalogExpert(t *testing.T) {
	if got := len(ToolIDs()); got != 18 { t.Fatalf("tools = %d, want 18", got) }
	for _, id := range ToolIDs() {
		slug, ok := ToolExpert(id)
		if !ok { t.Errorf("%s has no expert mapping", id); continue }
		if _, ok := Lookup(slug); !ok { t.Errorf("%s maps to unknown expert %q", id, slug) }
		if strings.HasPrefix(slug, "aurora:") { t.Errorf("%s keeps a retired skill id", id) }
	}
}

func TestUnknownToolHasNoExpert(t *testing.T) {
	if _, ok := ToolExpert("no-such-tool"); ok { t.Fatal("unknown tool resolved") }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd server && go test ./internal/aurora/ -run 'TestEveryShippedToolHasACatalogExpert|TestUnknownToolHasNoExpert' -count=1`
Expected: FAIL — `ToolIDs` 未定义。

- [ ] **Step 3: Implement the table**

按 spec 第 4 节写入 18 行 `var toolExperts = map[string]string{...}` 与 `toolIDs` 切片（顺序与 `tool-definitions.ts` 一致）。

- [ ] **Step 4: Run test to verify it passes**

Run: `cd server && go test ./internal/aurora/ -count=1`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add server/internal/aurora/tool_experts.go server/internal/aurora/tool_experts_test.go
git commit -m "feat(aurora): map every tool page to its expert"
```

---

### Task 9: `GET /api/aurora/tools`

**Files:**
- Create: `server/internal/handler/aurora_tools.go`
- Modify: `server/cmd/server/router.go`
- Test: `server/internal/handler/aurora_tools_test.go`

**Interfaces:**
- Produces: `func (h *Handler) ListAuroraTools(w http.ResponseWriter, r *http.Request)`；响应 `{"tools":[{"id":"poster","expert_slug":"marketing-ad-copywriting","expert_name":"广告文案专家"}]}`。

- [ ] **Step 1: Write the failing test**

```go
func TestListAuroraToolsReturnsEveryMapping(t *testing.T) {
	var out struct {
		Tools []struct {
			ID         string `json:"id"`
			ExpertSlug string `json:"expert_slug"`
			ExpertName string `json:"expert_name"`
		} `json:"tools"`
	}
	testutil.Call(t, testHandler.ListAuroraTools, newRequest(http.MethodGet, "/api/aurora/tools", nil)).Want(http.StatusOK).JSON(&out)
	if len(out.Tools) != 18 { t.Fatalf("tools = %d, want 18", len(out.Tools)) }
	for _, tool := range out.Tools {
		if tool.ID == "" || tool.ExpertSlug == "" || tool.ExpertName == "" {
			t.Errorf("incomplete mapping %#v", tool)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd server && go test ./internal/handler/ -run TestListAuroraToolsReturnsEveryMapping -count=1`
Expected: FAIL — 方法未定义。

- [ ] **Step 3: Implement handler and route**

`aurora_tools.go` 里遍历 `aurora.ToolIDs()`，用 `aurora.ToolExpert` + `aurora.Lookup` 填 name；`router.go` 的 workspace 分组内加 `r.Get("/api/aurora/tools", h.ListAuroraTools)`（仅成员校验，不需要 `RequireHumanActor`）。

- [ ] **Step 4: Run test to verify it passes**

Run: `cd server && go test ./internal/handler/ -run TestListAuroraToolsReturnsEveryMapping -count=1`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add server/internal/handler/aurora_tools.go server/internal/handler/aurora_tools_test.go server/cmd/server/router.go
git commit -m "feat(aurora): expose the tool to expert mapping"
```

---

### Task 10: `POST /api/aurora/tools/{toolId}/runs`

**Files:**
- Modify: `server/internal/handler/aurora_tools.go`
- Modify: `server/cmd/server/router.go`
- Test: `server/internal/handler/aurora_tools_test.go`

**Interfaces:**
- Consumes: `aurora.ToolExpert`；`h.ensureAuroraCatalogSeed`；`h.ensureAuroraExpertSeed`；`h.Queries.GetAgentBySystemKey`；`aurora.ManagedRuntimeID`；`h.ensureWorkspaceSandbox`；`h.IssueService.Create`；`h.Queries.ListAttachmentsByIDs`。
- Produces: `func (h *Handler) CreateAuroraToolRun(w http.ResponseWriter, r *http.Request)`；响应 `{"identifier":"MUL-42","issue_id":"<uuid>","expert":{"slug":"...","name":"..."}}`。

- [ ] **Step 1: Write the failing tests**

```go
// auroraToolWorkspace creates a workspace owned by testUserID plus an ordinary
// member, and seeds every carrier. Returns (workspaceID, memberUserID).
func auroraToolWorkspace(t *testing.T) (string, string) {
	t.Helper()
	ws := dbfx.Workspace(t, "aurora tools", "aurora-tools-"+uuid.NewString())
	dbfx.Member(t, ws, testUserID, "owner")
	member := dbfx.User(t, "aurora tool member", "aurora-tool-"+uuid.NewString()+"@test.invalid")
	dbfx.Member(t, ws, member, "member")
	if err := testHandler.ensureAuroraCatalogSeed(context.Background(), parseUUID(ws)); err != nil {
		t.Fatal(err)
	}
	return ws, member
}

func postToolRun(t *testing.T, ws, user, toolID string, body map[string]any) *testutil.Response {
	t.Helper()
	req := withURLParam(newRequestAs(user, http.MethodPost, "/api/aurora/tools/"+toolID+"/runs", body), "toolId", toolID)
	req.Header.Set("X-Workspace-ID", ws)
	return testutil.Call(t, testHandler.CreateAuroraToolRun, req)
}

func TestCreateAuroraToolRunAssignsTheMappedExpert(t *testing.T) {
	ws, member := auroraToolWorkspace(t)
	var out struct {
		Identifier string `json:"identifier"`
		IssueID    string `json:"issue_id"`
		Expert     struct{ Slug, Name string }
	}
	postToolRun(t, ws, member, "poster", map[string]any{"source": "秋季新品海报"}).Want(http.StatusCreated).JSON(&out)
	if out.Identifier == "" || out.Expert.Slug != "marketing-ad-copywriting" {
		t.Fatalf("run = %#v", out)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM issue WHERE workspace_id=$1 AND assignee_id=(SELECT id FROM agent WHERE workspace_id=$1 AND system_key='aurora:marketing-ad-copywriting')`, ws); n != 1 {
		t.Fatalf("issues assigned to the carrier = %d, want 1", n)
	}
	postToolRun(t, ws, member, "poster", map[string]any{"source": "再来一版"}).Want(http.StatusCreated)
	if n := dbfx.Count(t, `SELECT count(*) FROM agent WHERE workspace_id=$1 AND system_key='aurora:marketing-ad-copywriting'`, ws); n != 1 {
		t.Fatalf("carriers = %d, want 1", n)
	}
}

func TestCreateAuroraToolRunRejectsAnUnknownTool(t *testing.T) {
	ws, member := auroraToolWorkspace(t)
	before := dbfx.Count(t, `SELECT count(*) FROM issue WHERE workspace_id=$1`, ws)
	postToolRun(t, ws, member, "no-such-tool", map[string]any{}).Want(http.StatusConflict)
	if after := dbfx.Count(t, `SELECT count(*) FROM issue WHERE workspace_id=$1`, ws); after != before {
		t.Fatalf("issues changed: %d -> %d", before, after)
	}
}

func TestCreateAuroraToolRunRejectsForeignAttachments(t *testing.T) {
	ws, member := auroraToolWorkspace(t)
	postToolRun(t, ws, member, "poster", map[string]any{"attachment_ids": []string{uuid.NewString()}}).Want(http.StatusBadRequest)
}

func TestCreateAuroraToolRunRefusesWhenTheRuntimeIsUnavailable(t *testing.T) {
	ws, member := auroraToolWorkspace(t)
	previous := testHandler.SandboxManager
	testHandler.SandboxManager = nil
	t.Cleanup(func() { testHandler.SandboxManager = previous })
	before := dbfx.Count(t, `SELECT count(*) FROM issue WHERE workspace_id=$1`, ws)
	postToolRun(t, ws, member, "poster", map[string]any{"source": "海报"}).Want(http.StatusServiceUnavailable)
	if after := dbfx.Count(t, `SELECT count(*) FROM issue WHERE workspace_id=$1`, ws); after != before {
		t.Fatalf("issues changed: %d -> %d", before, after)
	}
}

func TestCreateAuroraToolRunAllowsANonOwnerMember(t *testing.T) {
	ws, member := auroraToolWorkspace(t)
	postToolRun(t, ws, member, "copywriting", map[string]any{"source": "写一段文案"}).Want(http.StatusCreated)
}
```

`testutil.Response` 是 `testutil.Call` 的返回类型；若类型名不同，用现有测试里同样的链式写法即可。

Run: `cd server && go test ./internal/handler/ -run TestCreateAuroraToolRun -count=1`
Expected: FAIL — 方法未定义。

- [ ] **Step 2: Implement the handler**

顺序固定：成员校验 → `chi.URLParam(r, "toolId")` → 映射（未映射 409）→ `ensureAuroraCatalogSeed` + `ensureAuroraExpertSeed` → `GetAgentBySystemKey` → `ListAttachmentsByIDs` 校验数量（不一致 400）→ `ManagedRuntimeID` + `ensureWorkspaceSandbox`（失败 503）→ 服务端拼标题与描述 → `IssueService.Create`（`AssigneeType: "agent"`、`AssigneeID: carrier.ID`、`CreatorType: "member"`、`CreatorID: userUUID`、`AllowDuplicate: true`、`IssueCreateOpts{}` 不跳过入队）→ 201。

标题与描述使用与现有工具页相同的结构，只是移到服务端：

```
title = <工具名> · <source 前 80 字或「任务」>   （总长 ≤ 120）
description =
  ## 工具\n<工具名>
  ## 需求\n<source.trim()>                       （无 source 时省略）
  ## 设置\n- <field id>: <value> …               （无设置时省略）
  ## 素材\n<file_names 用、连接>（见附件）        （无文件时省略）
  ## 执行专家\n<专家中文名>（<slug>）
  <媒体限制说明：若当前环境无法生成真实媒体文件（图片/视频/音频），请明确说明限制，并给出可执行的方案、脚本或文案作为替代。>
```

- [ ] **Step 3: Wire the route**

```go
r.With(
	handler.RequireHumanActor,
	middleware.RateLimitByUser(rdb, envPositiveInt("RATE_LIMIT_AURORA_TOOL_RUNS", 30), time.Minute),
).Post("/api/aurora/tools/{toolId}/runs", h.CreateAuroraToolRun)
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd server && go test ./internal/handler/ -run TestCreateAuroraToolRun -count=1`
Expected: PASS（5 个用例）。

- [ ] **Step 5: Commit**

```bash
git add server/internal/handler/aurora_tools.go server/internal/handler/aurora_tools_test.go server/cmd/server/router.go
git commit -m "feat(aurora): create tool runs server-side on the mapped expert"
```

---

### Task 11: 前端 API 客户端与 schema

**Files:**
- Modify: `packages/core/aurora/types.ts`
- Modify: `packages/core/aurora/schema.ts`
- Modify: `packages/core/aurora/api.ts`
- Modify: `packages/core/aurora/queries.ts`
- Test: `packages/core/aurora/api.test.ts`、`schema.test.ts`

**Interfaces:**
- `export type AuroraToolExecutor = { id: string; expertSlug: string; expertName: string }`
- `export type AuroraToolRun = { identifier: string; issueId: string; expert: { slug: string; name: string } }`
- `listAuroraTools(): Promise<ParseResult<AuroraToolExecutor[]>>`
- `createAuroraToolRun(toolId: string, request: { source?: string; values?: Record<string,string>; fileNames?: string[]; attachmentIds?: string[] }): Promise<AuroraToolRun>`
- `auroraToolsOptions(wsId: string)`，key `auroraKeys.tools(wsId)`。

- [ ] **Step 1: Write the failing schema/API tests**

```ts
it("maps the tools response onto camelCase executors", () => {
  expect(parseAuroraTools({ tools: [{ id: "poster", expert_slug: "marketing-ad-copywriting", expert_name: "广告文案专家" }] }))
    .toEqual({ data: [{ id: "poster", expertSlug: "marketing-ad-copywriting", expertName: "广告文案专家" }], degraded: false });
});

it("posts the run body and returns the created issue", async () => {
  // mock requestJson → { identifier: "MUL-42", issue_id: "i-1", expert: { slug: "s", name: "n" } }
  await expect(createAuroraToolRun("poster", { source: "x", attachmentIds: ["a-1"] }))
    .resolves.toEqual({ identifier: "MUL-42", issueId: "i-1", expert: { slug: "s", name: "n" } });
});

it("throws on a malformed run response instead of degrading", () => {
  // requestJson → { identifier: 42 } → expect rejects
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `pnpm --filter @multica/core exec vitest run aurora/schema.test.ts aurora/api.test.ts`
Expected: FAIL — 导出不存在。

- [ ] **Step 3: Implement parsers and client**

`parseAuroraTools` 走 `parseWithFallbackResult`（读接口失败时降级为空列表并标 `degraded`）。`createAuroraToolRun` 用严格 zod 校验后返回，畸形响应**抛出**（issue 可能已创建，不能静默当成空）。

- [ ] **Step 4: Run tests to verify they pass**

Run: `pnpm --filter @multica/core exec vitest run aurora/schema.test.ts aurora/api.test.ts`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add packages/core/aurora/types.ts packages/core/aurora/schema.ts packages/core/aurora/api.ts packages/core/aurora/queries.ts packages/core/aurora/*.test.ts
git commit -m "feat(core): add the aurora tool run client"
```

---

### Task 12: 工具页改用服务端执行者

**Files:**
- Modify: `apps/aurora/components/tools/use-tool-submission.ts`
- Modify: `apps/aurora/components/tools/tool-workbench.tsx`
- Modify: `apps/aurora/components/tools/face-swap.tsx`
- Test: `apps/aurora/components/tools/use-tool-submission.test.tsx`、`tool-workbench.test.tsx`、`face-swap.test.tsx`

**Interfaces:**
- Consumes: `auroraToolsOptions(wsId)`、`createAuroraToolRun`、`useFileUpload`、`auroraIssueHrefFor`。
- Produces: `ToolSubmissionStatus` 增加 `"no-expert"`；`toolSubmissionNotice` 增加对应文案「该工具尚未配置执行专家」。

- [ ] **Step 1: Write the failing tests**

测试文件已经 mock 了 `@multica/core/api`；本步骤再加一个 `@multica/core/aurora` 的 mock，并把 `auroraToolsOptions` 一起替换（否则真实 options 会闭包住未被替换的 `listAuroraTools`）：

```tsx
const mocks = vi.hoisted(() => ({
  uploadFile: vi.fn(), listAgents: vi.fn(), listMembers: vi.fn(),
  listTools: vi.fn(), createToolRun: vi.fn(),
}));

vi.mock("@multica/core/aurora", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@multica/core/aurora")>()),
  auroraToolsOptions: (wsId: string) => ({
    queryKey: ["aurora", wsId, "tools"],
    queryFn: () => mocks.listTools(wsId),
  }),
  createAuroraToolRun: mocks.createToolRun,
}));

const carrier = (systemKey: string, overrides: Partial<Agent> = {}) =>
  agent({ system_key: systemKey, ...overrides });
```

`beforeEach` 里补 `mocks.listMembers.mockResolvedValue([{ user_id: "u-1", role: "owner" }])` 与 `mocks.listTools.mockResolvedValue([{ id: "poster", expertSlug: "marketing-ad-copywriting", expertName: "广告文案专家" }])`。

```tsx
it("reports no-expert for a tool the server does not map", async () => {
  mocks.listTools.mockResolvedValue([{ id: "other", expertSlug: "x", expertName: "X" }]);
  const { result } = await render();
  expect(result.current.status).toBe("no-expert");
});

it("submits through the tool-run endpoint with the uploaded attachment ids", async () => {
  mocks.listAgents.mockResolvedValue([carrier("aurora:marketing-ad-copywriting")]);
  mocks.createToolRun.mockResolvedValue({ identifier: "MUL-42", issueId: "i-1", expert: { slug: "marketing-ad-copywriting", name: "广告文案专家" } });
  const { result } = await render();
  await act(async () => { await result.current.submit({ source: "海报", files: [file("p.png")] }); });
  expect(mocks.createToolRun).toHaveBeenCalledWith("poster", expect.objectContaining({ attachmentIds: ["att-1"] }));
  expect(mocks.createIssue).not.toHaveBeenCalled();
  expect(push).toHaveBeenCalledWith("/acme/issues/MUL-42");
});

it("keeps the button disabled when the mapped carrier has no runtime", async () => {
  mocks.listAgents.mockResolvedValue([carrier("aurora:poster", { runtime_bound: false, runtime_id: "" })]);
  const { result } = await render();
  expect(result.current.status).toBe("no-runtime");
});

it("keeps the shipped tool ids in step with the server mapping", () => {
  // The same 18-id list is asserted by TestEveryShippedToolHasACatalogExpert.
  expect(apexTools.map((t) => t.id).sort()).toEqual([
    "avatar-video", "copywriting", "face-swap", "image-retouch", "pdf-reader",
    "portraits", "presentation", "poster", "product-images", "rednote-images",
    "resume", "short-video", "spreadsheet", "video-edit", "video-transcript",
    "video-translate", "voiceover", "website",
  ]);
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `pnpm --filter @multica/aurora exec vitest run components/tools/use-tool-submission.test.tsx`
Expected: FAIL。

- [ ] **Step 3: Rewrite the hook**

删除 mika/最旧 agent 选择；**保留 Task 1 的 `canAssignAgentToIssue` 过滤**（carrier 在迁移后是 `public_to`，但旧数据或异常配置下仍要挡住他人 private agent）。状态机：`loading`（tools、agents、members 任一在载）→ `error`（任一失败）→ `no-expert`（无映射）→ `no-agent`（映射的 carrier 不在可派活列表）→ `no-runtime`（carrier 未绑运行时）→ `ready`。`submit` 只做：上传 → `createAuroraToolRun` → 用返回的 `identifier` 走 `auroraIssueHrefFor`；保留 `inFlight` ref 与「成功后不释放」、导航在 try 之外的语义。删除测试里所有 `mocks.createIssue` 断言，改断言 `mocks.createToolRun`。

- [ ] **Step 4: Update the notice copy and both workbenches**

`toolSubmissionNotice` 增加 `no-expert`；`tool-workbench.tsx` 与 `face-swap.tsx` 无需改结构（已渲染 notice + 重载按钮），只补 `no-expert` 的文案断言。

- [ ] **Step 5: Run the aurora suite**

Run: `pnpm --filter @multica/aurora typecheck && pnpm --filter @multica/aurora exec vitest run`
Expected: PASS。

- [ ] **Step 6: Commit**

```bash
git add apps/aurora/components/tools
git commit -m "feat(aurora): submit tool runs through the server-side executor"
```

---

### Task 13: 文档与整树验证

**Files:**
- Modify: `apps/aurora/DEPLOYMENT_HANDOFF.md`
- Modify: `AGENTS.md`（Aurora Roadmap）

- [ ] **Step 1: Update the handoff and roadmap**

`DEPLOYMENT_HANDOFF.md`：工具提交改为「由该工具指定的专家执行」；写明首次进入 Aurora 会创建 33 个专家；写明 provider 凭据在工作区共享。`AGENTS.md` 的 Aurora Roadmap：24 → 33、映射表位置、首次进入 seed、`public_to` 权限与 owner 归属各一条。

- [ ] **Step 2: Run the full verification**

```bash
node --test scripts/aurora-import-experts.test.mjs
cd server && go test ./internal/aurora/ ./internal/migrations/ -count=1
cd server && DATABASE_URL= go test ./internal/fleet/store/ -count=1
cd .. && pnpm typecheck && pnpm lint && pnpm test
```

Expected: 全绿；`DATABASE_URL=` 下 fleet 包退出码 0。

- [ ] **Step 3: Commit**

```bash
git add apps/aurora/DEPLOYMENT_HANDOFF.md AGENTS.md
git commit -m "docs(aurora): record the tool expert mapping and expert seeding"
```
