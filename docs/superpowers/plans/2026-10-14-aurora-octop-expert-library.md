# Aurora 导入 octop 专家库 —— 实施计划

> **For agentic workers:** 按 Task 顺序执行；Task 1 完成后是检查点 A（停一下，让 Lead 审 vendored 资产与生成器），Task 2–4 可并行但共享同一工作树时注意写域。Steps 用 - [ ] 语法跟踪。

**Goal:** 用 172.16.12.89 上 octop 的 24 个专家（技能集工作流 + 6 个子技能、含 scripts/references）替换 Aurora 现在硬编码的 16 个 skill；每个专家成为一个 Aurora system agent，子技能作为该 agent 的附属 workspace skill 随 claim 进入沙箱 `.claude/skills/`。

**Architecture:** octop 的三层（模板缓存 / agent 工作区 / 内置技能）在 Aurora 压成两层：catalog 条目 + system agent（专家），以及附属 skill 行（主 brief 一份、子技能各一份，子技能带 `skill_file`）。主 brief 由生成器按 Aurora 既有五段契约重写；执行路由复用 `claude-text`；产物仍走 `com.multica.aurora.artifacts` manifest，因此结算、审核、作品库链路零改动。种子按「用户实际请求的那个专家」懒加载，避免每 workspace 落 7 MiB。

**Tech Stack:** Go（embed、Chi、pgx、sqlc）、Node ESM 生成器（仓库 `scripts/*.mjs` 风格）、PostgreSQL 既有 `skill`/`skill_file` 表、前端 Next.js + 5 语言包。

**Spec:** [`docs/superpowers/specs/2026-10-14-aurora-octop-expert-library-design.md`](../specs/2026-10-14-aurora-octop-expert-library-design.md)

**来源快照:** `172.16.12.89:/home/project/octop/data/expert_market`（905 文件 / 25 模板），本地副本 `/tmp/octop-expert-market`（不入库）。

## Global Constraints

- 不新增外键、不做级联删除；不新增数据库迁移（`skill` 与 `skill_file` 已足够）。
- SQL 若改动必须 `make sqlc`，生成代码与查询一起提交。
- 代码注释英文；提交为 atomic conventional commits；PR 开在 `eanfs/multica`。
- 五段 brief 契约（`## Inputs` / `## Steps` / `## Required outputs` / `## Artifact manifest` / `## Failure behavior`）与 manifest schema `com.multica.aurora.artifacts` 保持不变；brief 中不得出现 `aurora.<verb>`（manifest schema 除外）。
- 子技能名必须是 ASCII 且 workspace 内唯一：`<expert-slug>--<sub-slug>`。
- 定向验证优先：`cd server && go test ./internal/aurora/... ./internal/daemon/... -count=1`，再 `go test ./internal/handler/... -run Aurora`；前端 `pnpm typecheck && pnpm lint && pnpm test`。
- 已知基线：AGENTS.md leftover(9) 记录本机 handler/service 有既存红项；同一组包在基线 commit 上先跑一遍对比，只报差异。
- 不做：部署到 89 / AWS；真实 Fleet 端到端（需单独授权）。

## File Structure

| 文件 | 责任 | Task |
| --- | --- | --- |
| `scripts/aurora-import-experts.mjs`（新） | 从快照生成 vendored 资产 + index.json；`--check` 防漂移 | 1 |
| `scripts/aurora-import-experts.test.mjs`（新） | 生成器单测（包装、稳定性、漂移） | 1 |
| `server/internal/aurora/experts/<slug>/workflow.md`（新，24） | 主 brief（生成产物，commit） | 1 |
| `server/internal/aurora/experts/<slug>/subskills/**`（新，161 目录） | 子技能原文（生成产物，commit） | 1 |
| `server/internal/aurora/experts/index.json`（新） | 专家/子技能/文件清单 + sha256（生成产物） | 1 |
| `server/internal/aurora/experts/PROVENANCE.md`（新） | 来源、抓取日期、版本、缺失项、许可证声明 | 1 |
| `server/internal/aurora/experts_assets.go`（新） | `//go:embed` + 解析 index，导出 `ExpertIndex/ExpertWorkflow/ExpertSubskills` | 1 |
| `server/internal/aurora/experts_catalog.go`（新） | 24 行人工表（category/credits/input/output/featured/attachments） | 2 |
| `server/internal/aurora/catalog.go` | `catalog` 改为由人工表 + index 构造 | 2 |
| `server/internal/aurora/execution_policy.go` | 24 条 `claude-text` 策略 | 2 |
| `server/internal/aurora/workflows.go`、`workflows/`（删） | brief 改从 experts 树读取 | 2 |
| `server/internal/aurora/agents.go` | 拆 `EnsureAuroraRuntime` / `EnsureAuroraExpert` | 3 |
| `server/internal/handler/aurora.go` | 生成路径只种子请求的专家 | 3 |
| `packages/views/aurora/labels.ts` + 5 个 `locales/*/aurora.json` | 六个场景分类标签 | 4 |
| `AGENTS.md` + `apps/docs` 相关页 | 更新 13/16 → 24 专家的表述 | 4 |
| 测试夹具（handler / daemon / fleet / agent / core / views） | 旧 skill id → 新专家 slug | 5 |

---

### Task 0: 工作树与源快照 ✅

- [x] `git worktree` 从 `06850495c` 切出 `feat/aurora-octop-expert-library`（`.agents/worktrees/aurora-octop-expert-library`）。
- [x] `rsync -a 172.16.12.89:/home/project/octop/data/expert_market/ /tmp/octop-expert-market/`（905 文件 / 25 模板）。

---

### Task 1: 导入生成器 + vendored 资产（**检查点 A**）

**Files:** `scripts/aurora-import-experts.mjs`（新）、`scripts/aurora-import-experts.test.mjs`（新）、`server/internal/aurora/experts/**`（生成）、`server/internal/aurora/experts_assets.go`（新）

**Interfaces:**
- Consumes: `--source <dir>`（octop `expert_market` 快照目录）、`--check`。
- Produces: `experts/<slug>/workflow.md`、`experts/<slug>/subskills/<sub>/**`、`experts/index.json`；Go 侧 `aurora.ExpertIndex() []Expert`、`aurora.ExpertWorkflow(slug) (string, bool)`、`aurora.ExpertSubskills(slug) []ExpertSubskill`。

**Steps:**
- [ ] 定义 24 个目标 slug（六场景，按 `manifest.scene` 过滤；排除 `skillhub-skillset-tech-test-automation`），并断言每个 slug 的 zh label 非空且唯一。
- [ ] 主 brief 包装器：五段固定文案 + `## Persona`（SOUL.md）+ `## Steps`（技能集 SKILL.md 正文，把 `**Name**` 形式的子技能引用改写成 `.claude/skills/<slug>--<sub>/SKILL.md`）。
- [ ] 子技能落盘：原文写入 `subskills/<sub>/**`，只把 SKILL.md 的 `name:` 改写为 `<slug>--<sub>`。
- [ ] index.json：稳定排序，记录 label/description/scene/子技能 slug、每个文件的 path/size/sha256 与整包 digest。
- [ ] `--check`：重算并与已提交树逐字节比对（含 index.json 规范化 JSON），任何差异退出非零。
- [ ] `experts_assets.go`：`//go:embed experts`，`init()` 解析 index，返回拷贝；解析失败 `panic`（编译期资产，同 `workflows.go` 的失败方式）。
- [ ] 单测：24 个专家、每专家 6 个（`marketing-social-media-operation` 为 5 个）子技能、brief 五段齐全且含 `com.multica.aurora.artifacts`、`--check` 能捕获篡改。

**验证:** `node --test scripts/aurora-import-experts.test.mjs`；`cd server && go build ./...`。

---

### Task 2: catalog / execution policy / brief 读取替换

**Files:** `server/internal/aurora/experts_catalog.go`（新）、`catalog.go`、`execution_policy.go`、`workflows.go`、`workflows/*.md`（删 13 个）、`catalog_test.go`、`execution_policy_test.go`、`workflows_test.go`

**Interfaces:**
- Consumes: `ExpertIndex()`。
- Produces: 24 条 catalog（`Catalog()/Lookup()/Exists()/AssetKind()` 签名不变）；24 条 `ExecutionPolicy`（Route 全 `claude-text`，`Attachments = documentConstraint(0,1)`，`RequiredTools = ["aurora.write_text_artifact"]`）；`Workflow(id)` 从 experts 树读主 brief。

**默认积分（可调）:** 电商 80 / 内容创作 60 / 营销 80 / 法律 100 / 影视媒体 120 / 设计 90；每个场景首个专家 `featured=true`；`Input = ["text","document"]`、`Output = ["text"]`。

**Steps:**
- [ ] `experts_catalog.go` 写 24 行（slug、category、credits、input、output、featured），名称/描述取自 index。
- [ ] `catalog` 在 `init()` 由人工表 + index 构造；`lookupCatalogEntry` 顺序 = 人工表顺序。
- [ ] `executionPolicies` 换成 24 条；删旧 13 条。
- [ ] 删 `workflows/*.md`；`Workflow()` 改走 `ExpertWorkflow()`（保留 `strings.TrimSpace` 与空判定语义）。
- [ ] 测试：id 列表 24 条且两两唯一、双语名非空、credit>0、catalog slug 集合 == index slug 集合、available == 24、unavailable 期望为空、五段契约对 24 份 brief 全量成立。

**验证:** `cd server && go test ./internal/aurora/... -count=1`。

---

### Task 3: 种子（Agent + 子技能 + skill_file）

**Files:** `server/internal/aurora/agents.go`、`server/internal/handler/aurora.go`（准备路径）、`agents_test.go`

**Interfaces:**
- `func EnsureAuroraRuntime(ctx, q, workspaceID, ownerID) (pgtype.UUID, error)`
- `func EnsureAuroraExpert(ctx, q, workspaceID, ownerID, skillID string) error` —— 主 skill + 子技能行 + `skill_file` + agent adopt/upsert + 7 条 junction
- `func EnsureSystemAgents(ctx, q, workspaceID, ownerID) error` 保留为 runtime + 遍历 catalog（测试与回填）

**Steps:**
- [ ] 复用 `UpsertAuroraSkill`（改名 `UpsertAuroraSystemSkill` 如需要）写入主 skill 的 content；为子技能新增 sqlc 查询（`UpsertAuroraSubskill`：按 `(workspace_id, name)` upsert，返回 id）。
- [ ] 用既有 `UpsertSkillFile` 写子技能文件（path 相对该子技能目录：`references/x.md`、`scripts/y.sh`）；`AddAgentSkill` 对 7 个 skill 各一次（已 ON CONFLICT DO NOTHING）。
- [ ] 自愈：不再存在的子技能行 → 删文件行/解除 junction（仅在 `EnsureAuroraRuntime` 里做一次旧 carrier 归档，不动历史行）。
- [ ] handler 的 workspace 准备改为「runtime + 请求的那个 expert」，不再每次生成都遍历 24 个。
- [ ] 测试：单专家种子 → 1 agent + 7 skill + files；重复调用幂等；旧 carrier 归档；`ensureAuroraSubskillName` 唯一性。

**验证:** `cd server && go test ./internal/aurora/... ./internal/handler/... -run 'Aurora|Agent' -count=1`。

---

### Task 4: 前端分类与 i18n + 文档

**Files:** `packages/views/aurora/labels.ts`、`packages/views/locales/{en,zh-Hans,fr,ja,ko}/aurora.json`、`AGENTS.md`、`apps/docs/content/docs/developers/local-docker-fleet*.mdx`（若提及 13 个技能）

**Steps:**
- [ ] `CATEGORY_LABELS` 增六个场景；`AuroraCategoryLabel` 联合类型同步。
- [ ] 5 个语言包补 `aurora.directory.categories.{ecommerce,content-creation,marketing,legal,media,design}`（zh: 电商/内容创作/营销/法律/影视媒体/设计）；跑 parity 测试。
- [ ] 更新 AGENTS.md Aurora 段（13 个 available / 16 catalog → 24 专家；技能文档位置与生成器说明）。
- [ ] 前端测试：`labels.test.ts` 增例；`skill-filter.test.ts` / `skill-directory.test.tsx` 夹具换新 slug。

**验证:** `pnpm typecheck && pnpm lint && pnpm test`。

---

### Task 5: 测试夹具迁移 + 全量验证 + PR

**Files（机械替换）:** `server/internal/handler/aurora_*_test.go`、`server/internal/daemon/aurora_manifest_test.go`、`aurora_task_test.go`、`server/internal/fleet/integration/aurora_*_test.go`、`server/pkg/agent/aurora_sandbox_smoke_test.go`、`server/internal/aurora/agents_test.go`、`packages/core/aurora/*.test.ts(x)`、`packages/views/aurora/*.test.ts(x)`

**Steps:**
- [ ] `xhs-image`/`product-image`/`text-video` 等旧 id → 新专家 slug（`xhs-image` → `content-creation-lyrics-songwriting` 之类的文本型专家，保证 attachment 期望一致）。
- [ ] `grep` 确认 shippable 代码对旧 16 id 零引用（历史 plan 文档除外）。
- [ ] 定向 + 全量：`go build ./...`、`go test ./internal/... -count=1`（与基线对比）、`pnpm typecheck && pnpm lint && pnpm test`、`node scripts/aurora-import-experts.mjs --check`、`git diff --check`。
- [ ] PR 到 `eanfs/multica`（`gh pr create --repo eanfs/multica`），按仓库 PR 模板写；body 附来源快照与验收证据。

## 验收标准

- [ ] `GET /api/aurora/skills` 返回 24 条，旧 16 id 不出现。
- [ ] 任一专家：`/api/agents/{id}/skills` 7 条全 `enabled=true`；`skill_file` 含子技能 `references/`、`scripts/`。
- [ ] 沙箱 `.claude/skills/` 下主技能 + 6 子技能目录齐备。
- [ ] `node scripts/aurora-import-experts.mjs --check` 干净；`go test ./internal/aurora/... ./internal/daemon/...` 通过；前端三件套通过（或只剩基线红项）。
- [ ] 旧 16 id 在 shippable 代码中零引用。
- [ ] 不做：部署、真实 Fleet 端到端。
