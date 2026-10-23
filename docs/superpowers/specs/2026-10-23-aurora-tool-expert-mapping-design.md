# Aurora 工具 → 专家映射与遗留缺陷收尾

日期：2026-10-23

状态：书面规格。实施计划（`docs/superpowers/plans/2026-10-23-aurora-tool-expert-mapping.md`）另行创建；本文件是其中设计判断与边界的唯一依据。

关联：PR #17（`fix/aurora-tool-submission-agent`）、issue #16（映射）、#18（迁移号段）、#19（fleet 测试）、#20（产品决策记录）。

## 1. 目标

18 个工具页提交的任务必须落到该工具**指定的**专家 agent，而不是任意一个工作区 agent。后端是映射的唯一权威，前端不再猜测执行者。

同时补齐「9 个工具没有对应专家」的缺口：新增 9 个本地专家进 catalog，做到 18/18 有明确执行者，并让这些专家在真实 provider 可用时产出真媒体。

本文件同时固定三项遗留缺陷的处理方式（#18、#19、PR #17 的 admin 403），因为它们各有需要记录的设计判断。

## 2. 现状

这些事实来自当前代码，实施时以代码为准。

- `apps/aurora/components/tools/tool-definitions.ts` 的 18 个工具只描述 UI；PR #17 删除了误导的 `agentKey` 字段。
- `apps/aurora/components/tools/use-tool-submission.ts` 从工作区 agent 列表里挑：Mika 优先，否则最早创建且仍绑运行时的 agent。**这是客户端的猜测**。
- `server/internal/aurora/catalog.go` 的目录来自 24 个专家；`ensureExpert` 拒绝任何不在目录里的 id，所以退役技能 id 永远无法再被创建。
- 专家 carrier 是 `agent.system_key = 'aurora:<slug>'` 的 `kind='user'` 行，`visibility='workspace'`，`permission_mode='private'`，owner 是**触发 seed 的那个用户**。
- 托管运行时行 `agent_runtime.provider='aurora_managed'`，`visibility='private'`，owner 是**第一个 seed 的用户**。
- `service/agent_ready.go runtimeVerdict` 在 private 运行时下要求 `agent.owner_id == runtime.owner_id`，不等就阻塞该 agent 及全工作区。
- `handler/agent_access.go`：`ListAgents` 让 owner/admin 看到所有人的 agent（含他人 private）；`canInvokeAgent` 对 private 只放行 owner 本人 —— admin 不豁免。二者叠加就是 PR #17 的 admin 403 缺陷。
- `cmd/server/runtime_sweeper.go sweepAuroraSandboxEnsure` 为**每个有托管运行时的工作区**常驻保活一个 sandbox 节点；当前正是靠「懒 seed」把节点数限制在真正用过 Aurora 的工作区。
- `scripts/aurora-import-experts.mjs --check` 从 octop 快照重算完整受管文件集（`experts/index.json`、每份 `workflow.md`、全部子技能文件）并逐字节比对；本地新增的专家会被判为漂移。

## 3. 已确认边界

1. **18/18 有执行者**：9 个无对应专家的工具通过新增专家补齐，不采用「禁用」也不采用「静默回退到通用助手」。
2. #16 设计评论里的 9 条既有映射**全部确认**。
3. 33 个专家 carrier **在首次进入 Aurora 时一次性 seed**；**不在创建工作区时 seed**（见第 6 节的理由）。
4. carrier 权限为 `public_to` + workspace 调用目标（E1）；carrier 与托管运行时的 owner **恒为工作区 owner**（F1）。
5. 9 份新 brief 写真实 provider 调用步骤；确无可用 provider 的，写明限制并给出可执行替代，**不得伪造产出**。
6. 运行时不可达时返回 503 且**不创建 issue**。
7. 工具任务**不计 Aurora 积分**，走普通 issue/agent 通道。
8. 33 个 carrier **保留**在 agent 列表与选择器里，不做响应层隐藏。
9. 退役 carrier（`aurora:poster` 等）**保留不删**。
10. 客户端不再决定执行者；它只做「能否提交」的预检，执行者由服务端按映射解析。
11. #18 采用 grandfather 清单，**不重命名已应用的迁移**（`schema_migrations` 以完整文件名为键，重命名会重放）。
12. #19 无 `DATABASE_URL` 时 skip；跨时钟域比较引入 2 秒容差。

## 4. 工具 → 专家映射

映射表落在 `server/internal/aurora/tool_experts.go`，是唯一权威。工具 id 仍是前端注册表的事，映射不由前端推断。

| 工具 id | 专家 slug | 说明 |
| --- | --- | --- |
| `face-swap` | `media-face-swap`（新） | 无已验证换脸 provider |
| `video-transcript` | `media-short-video-copy` | 文案提取 |
| `product-images` | `ecommerce-product-copywriting` | 文案半程，媒体走 Seedream |
| `poster` | `marketing-ad-copywriting` | 文案半程，媒体走 Seedream |
| `rednote-images` | `marketing-social-media-operation` | 文案半程，媒体走 Seedream |
| `image-retouch` | `media-image-retouch`（新） | Seedream 编辑 |
| `portraits` | `media-portrait-studio`（新） | Seedream 生成 |
| `short-video` | `media-storyboard-design` | 分镜/脚本，媒体走 Seedance |
| `video-edit` | `media-video-editing` | FFmpeg 本地剪辑 |
| `avatar-video` | `media-short-video-copy` | 口播脚本半程 |
| `video-translate` | `media-video-translation`（新） | ASR+翻译+字幕，配音待核实 |
| `presentation` | `design-ui-prototype` | HTML 原型；`.pptx` 由本地脚本产出 |
| `spreadsheet` | `office-spreadsheet-analysis`（新） | 本地表格处理 |
| `pdf-reader` | `office-document-reading`（新） | 文本/文档问答 |
| `copywriting` | `marketing-ad-copywriting` | 完整匹配 |
| `voiceover` | `media-voiceover`（新） | PR #17 实测产出过 MP3，provider 待核实 |
| `website` | `design-website-build`（新） | HTML/Tailwind 产出 |
| `resume` | `office-resume-optimization`（新） | 文本 + PDF 产出 |

同两个工具共用一个专家是允许的（`poster`/`copywriting` → 广告文案；`video-transcript`/`avatar-video` → 短视频文案），但不能引入未登记的 slug。

契约由两侧测试夹住：Go 侧枚举 18 个工具 id 并断言每条都有映射、每个 slug 都在 catalog 里；前端侧断言 `apexTools` 的 18 个 id 与 Go 侧清单一致。新增工具未登记即失败 —— 这是 `agentKey` 事故的防复发点。

## 5. 新增 9 个专家

### 5.1 文件布局

上游 24 个专家由 octop 快照生成；本地的 9 个**不能混进快照生成流程**，否则 `--check` 每次都判漂移，且重跑生成会覆盖。

- 元数据：`server/internal/aurora/experts-local/index.json`（slug、中英文名、scene、简介、quick_prompts）。
- 主 brief：`server/internal/aurora/experts/<slug>/workflow.md`（手工维护，五段齐全 + `## Persona`）。这是 `//go:embed experts` 实际读到的那一份，不另存副本。
- 导入脚本：`buildLibrary` 读本地元数据并把条目合并进 `experts/index.json`；本地 brief **不进受管集合** —— `currentFiles()` 跳过它，`diff()` 因此不会把它判成 orphan。`--check` 仍会比对合并后的 `index.json`。`--import` 只为上游专家拷贝子技能（本地专家暂不带子技能）。
- `experts/PROVENANCE.md` 记录这 9 个为本地撰写（日期、作者、与上游的关系：无）。

### 5.2 目录与种子

catalog 由 24 变 33。`experts_catalog.go` 为每个新 slug 补分类、积分、是否精选；`catalog.go` 的构造断言（专家与目录定义一一对应）保持不变。新专家默认 `available = true`，因此也会出现在 `/skills` 生成入口 —— 这是接受的副作用。

### 5.3 brief 内容

逐专家核对真实可用的 provider，而不是套用同一个模板：

| 专家 | 可用的真实路径 | 状态 |
| --- | --- | --- |
| `media-image-retouch` / `media-portrait-studio` | ARK 平台 lane `doubao-seedream-5-0-pro-260628`（`/api/v3`，需 `ARK_PLATFORM_API_KEY`） | 已核实 |
| `media-face-swap` | 无已验证模型；brief 要求说明限制并给出可执行替代（如开源工具脚本） | 无 provider |
| `media-video-translation` | ASR + 翻译 + 字幕可做；配音需核实 | 部分待核实 |
| `media-voiceover` | PR #17 端到端实测产出过 MP3，但未记录 provider/模型 | 待核实 |
| `office-spreadsheet-analysis` | 本地 Python（openpyxl/csv） | 已核实 |
| `office-document-reading` | 文本解析 + 页码引用 | 已核实 |
| `office-resume-optimization` | 文本 + PDF 渲染 | 已核实 |
| `design-website-build` | HTML/Tailwind 产出 | 已核实 |

`TestRewrittenWorkflowsDescribeRealSteps` 对每份 brief 的五段式约束不变；新增一条检查：涉及媒体的专家必须在 brief 中写明具体模型 id 或明确写出「当前环境无可用 provider」。

## 6. 首次进入 Aurora 的种子

- 触发点：`GET /api/aurora/skills`（进入 Aurora 的第一个请求）与 `POST /api/aurora/tools/{toolId}/runs`（防御性）。
- 守卫：取 `aurora:<workspace>` advisory lock，统计该工作区存活的 `aurora:%` carrier 数；小于 `len(Catalog())` 才调用 `EnsureSystemAgents`，避免每个 GET 重复做近百次 upsert。
- 一次性成本：1 条托管运行时 + 33 条 agent + 33 条主 skill 行。
- **不在创建工作区时 seed**：carrier 必须绑定托管运行时，而 `sweepAuroraSandboxEnsure` 会为每个有托管运行时的工作区常驻保活一个 sandbox 容器。建工作区即 seed 会让从不使用 Aurora 的工作区也常驻容器。首次进入 Aurora 意味着这个工作区确实在用，成本才归位。
- 既有的按需 `EnsureAuroraExpert` 保留：生成创建仍只补它需要的那一个专家。

## 7. 权限与归属

### 7.1 调用权限

`UpsertAuroraSystemAgent` 与 `AdoptAuroraSystemAgent` 把 `permission_mode` 从 `private` 改为 `public_to`，并在 seed 后写入 workspace 调用目标。`agent_invocation_target.target_id` 是 `NOT NULL`，workspace 行按迁移 130 的约定存 **workspace_id**。

回填迁移（号段 ≥ 5000）做三件事：把存量 `aurora:%` carrier 改为 `public_to`；为它们补 workspace 调用目标（幂等）；把 `agent_runtime.provider='aurora_managed'` 的 `owner_id` 对齐到工作区 owner。

安全后果直说：工作区成员由此获得「指挥一个携带 provider 凭据的 shell agent」的能力。这与现有 `visibility='workspace'` agent 给出的授权同级，但 carrier 是脚本与凭据的组合。缓解：只 seed 被请求的专家（现在改为首次进入时一次性 seed 33 个，代价见第 6 节），并在部署文档中写明凭据是工作区共享的。

### 7.2 归属

新增 `GetWorkspaceOwnerUserID` 查询（当前没有 workspace → owner 的查询）。`ensureAuroraExpertSeed`、首次进入的全量 seed、`CreateAuroraManagedRuntime` 一律传工作区 owner，而不是当前操作人。这同时修掉既有的 owner-drift：`AdoptAuroraSystemAgent` 每次 seed 都改写 owner，而运行时 owner 不变，一旦第二个成员触发 seed，`runtimeVerdict` 会阻塞整个工作区。

回归测试必须覆盖：成员 B 触发 seed 后，carrier.owner、runtime.owner 均为工作区 owner，且 B 能提交工具任务。

## 8. 工具执行端点与前端

### 8.1 接口

- `GET /api/aurora/tools` → `[{id, expert_slug|null}]`，静态表，来自 `tool_experts.go`。
- `POST /api/aurora/tools/{toolId}/runs`，请求体 `{source?, values?, file_names?, attachment_ids?}`。

顺序固定：

1. 成员校验；工具 id 查映射。
2. 未映射 → 409 `tool_executor_unmapped`，**不建 issue**。
3. 确保全量 seed 与目标专家已 seed。
4. `GetAgentBySystemKey('aurora:<slug>')` 解析 carrier。
5. 运行时不可达（`ensureWorkspaceSandbox` 失败）→ 503，**不建 issue**。
6. 服务端构造标题与描述（工具名、需求、设置、素材、媒体限制说明），`IssueService.Create` 指派 carrier。
7. 返回 `{identifier, issue_id, expert: {slug, name}}`。

描述模板移到服务端，单一所有者；客户端不再拼 prompt。附件 id 必须先校验属于本工作区。

### 8.2 前端

- `use-tool-submission.ts` 改为调用新端点；删除 Mika/最旧 agent 的选择逻辑。
- 状态：`loading` / `error` / `no-expert`（未映射，沿用 #16 设计评论的命名，不复用 `no-agent`）/ `no-runtime` / `ready`。
- 预检只读：映射存在，且对应 carrier 在 agent 列表里、仍绑运行时。保留预检是为了在**上传之前**阻止提交，避免运行时不可达时留下孤儿附件。
- PR #17 已有的提交锁（成功后不释放）与导航语义保持不变。

## 9. 遗留缺陷

### 9.1 PR #17 的 admin 403

`use-tool-submission.ts` 只按「未归档 + 仍绑运行时」挑 agent。admin 能在列表里看到他人的 private agent，但 `canInvokeAgent` 对 private 只放行 owner。修法：用 `packages/core/permissions` 已有的 `canAssignAgentToIssue(agent, {userId, role})` 过滤候选（AssigneePicker 与 `useWorkspaceAgentAvailability` 已用同一规则）。补一条 admin + 他人 private agent 的回归测试。

### 9.2 #18 迁移号段

`migrations_lint_test.go` 的 `grandfatheredSharedPrefixes` 补齐实际冲突（573–579、581–588；issue 记录时为 573–587，main 前进后新增 588）。另加两条：表里每条必须仍真实碰撞（防腐化）；`TestAuroraMigrationsUseTheReservedRange` 纳入 `fleet_*`（同为 fork 专有，正是本次碰撞的根因）。

### 9.3 #19 fleet/store

- 无 `DATABASE_URL`：`NewFleetFixture` 改为 `t.Skip`，与 `testutil/db_test.go` 记录的仓库约定一致；仍要求显式 URL，不回落到共享开发库。
- 时钟竞争：`validateObservation` 与 `bootstrapObservation` 的「未来」比较引入 `observationClockSkew = 2s`，只放宽未来方向，30 秒陈旧上限不动。这不是只为测试：Fleet 主机与 PostgreSQL 之间的 NTP 偏差在生产上同样会拒绝全部观测、卡死 bootstrap。单测同步调整，并在提交信息里说明放宽的边界与理由。

## 10. 不做什么

- 不改 Mika 的权限与绑定方式。
- 不在客户端猜映射，也不把映射写进前端注册表。
- 不在创建工作区时 seed 专家。
- 不删退役 carrier，不重命名已应用的迁移。
- 不把工具任务接入积分。
- 不改 `ListAgents` 的可见性语义（admin 继续能看到）。
- 不为 9 个新专家编造 provider 能力。

## 11. 风险与已知偏差

1. **9 份 brief 是内容工作**，其中 `face-swap` 无 provider、`voiceover`/`video-translation` 部分待核实；验收只认「brief 写明的路径可执行」，不认「工具页名实相符」。
2. **catalog 24 → 33** 会改变 `/skills` 目录与所有 agent 选择器的内容，属产品可见变化。
3. **权限放开（E1）** 是安全相关变更，已明确接受；部署文档需写明凭据为工作区共享。
4. **2 秒时钟容差**放宽了一项安全校验；限幅并补单测，超差仍拒绝。
5. **契约两侧清单**（Go 18 个 id / TS 18 个 id）需要人工同步，任一侧新增未登记工具都会被测试拦下。

## 12. 验收

- 18 个工具 id 全部有映射；未映射工具 → 409 且零 issue。
- 非 owner 成员提交工具任务 → 200，issue 指派到 `aurora:<slug>`。
- 同一工具重复提交复用同一 carrier，不产生第二个 agent 行。
- Fleet 缺失 → 503 且零 issue。
- 成员 B 触发 seed 后 carrier 与 runtime 的 owner 均为工作区 owner。
- 首次进入 Aurora 后该工作区有 33 个 carrier 与 1 条托管运行时；二次进入不重复 seed。
- `node scripts/aurora-import-experts.mjs --check` 在合并本地专家后通过。
- `go test ./internal/migrations/` 无号段失败；`DATABASE_URL=` 下 `internal/fleet/store` 全 skip 且退出码 0；带库时 5 次连续全绿。
- 前端：`pnpm --filter @multica/aurora typecheck|lint|vitest|build` 通过；无可用执行器时按钮禁用并说明原因。

## 13. 实施顺序

1. PR #17 修复（admin 403）→ 合并。
2. #18、#19 各自独立 PR。
3. 9 个专家 + 全量 seed + 权限/归属修复（后端，可独立评审）。
4. 映射 + 执行端点 + 前端重写（依赖上一步与已合并的 #17）。
5. #20 以本文件的决策回填并关闭；#16 由第 4 步关闭。
