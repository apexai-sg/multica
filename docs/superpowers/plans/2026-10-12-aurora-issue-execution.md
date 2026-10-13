# Aurora 生成走问题详情实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

上面这一行留给执行器，保持英文。说明用中文。代码、命令、提交信息用英文。

**目标：** 提交一次生成之后，Agent 在一条 issue 上跑；缺必填信息时它在那条 issue 上提问并停住，人回复后接着跑；人打开的是现成的问题详情。

**做法：** 建 issue（指派该技能的系统 agent）→ 预扣一份目录标价 → 带 `aurora_issue` 上下文入队。`isAuroraTask` 保留，Aurora 轮次继续走技能文档 + manifest + 产物收集。新增一条「提问轮」出口：manifest 声明 `outcome: needs_input`，生成停在 `needs_input`、预留不扣不退。后续轮次由评论触发，沿用同一份 claim 上下文，并把 `task_id` 指到新一轮。

**技术栈：** Go（Chi、sqlc、pgx）、现有 Aurora 预扣与产物暂存链路、现有 `IssueDetail`。

**规格：** `docs/superpowers/specs/2026-10-12-aurora-issue-execution-design.md`

**修订：** 2026-10-13（第二版）。相对上一版的四处改动：①「Agent 在 issue 上提问」从计划外改为**必须完整实现**；②保留 `isAuroraTask`，Aurora 轮次的提示词分派、产物收集、每轮挂载清理、技能文档强制仍由它判定；③计费按现状保留但**不改实现**，`file_count`、按文件扣款、透支、7 天超时推迟；④新增评论门闩与续跑轮（上一版没有）。

## 全局约束

- **五步按依赖排，同一个 PR 一起上。** 任务 1 落地时还没有任何带 issue 的轮次，所以提示词必须保留「没有 issue」的那一支，否则中间态里 Aurora 轮会被告知去评论一条不存在的 issue。
- **`isAuroraTask` 保留。** 它继续决定：提示词走 `buildAuroraPrompt`、结束收 manifest、每轮清理 `/workspace/{input,output}`、技能文档必须存在。不要为了「和普通 issue 一样」把它删掉或旁路。
- **计费不改实现。** 不改 `server/internal/aurora/credit.go`；不加 `file_count`；不按 token；不做透支。一次生成预扣一份目录 `Credits`，键仍是 `reserve:` + 生成 id。成功轮沿用现有结算（已扣 = 这份预扣），入队失败沿用现有退款。
- **提问轮不结算。** `needs_input` 是这一版的核心出口：不扣、不退、预留继续挂着。退款只发生在入队失败。
- 不建外键。`issue_id` 索引用单独迁移，`CREATE INDEX CONCURRENTLY`。
- 标题是目录 `Name` + 全角 `：` + 去掉首尾空白后的提示词前 40 个 rune。
- 打开路径是 `/{workspaceSlug}/issues/{issueIdentifier}`。`issueIdentifier` 必须是 `service.IssueIdentifier` 的 `PREFIX-N`；`getIssuePrefix` 失败时会渲染成 `#N`，客户端把它当缺失处理，不要跳。不写死主机。
- 新 JSON 字段用 Zod 默认值，不用 `as T`。
- 不在 `main` 上开发。PR 开到 `eanfs/multica`。

## 不做（本版）

- `file_count`、按文件扣款、透支、暂存行 `billed_at` —— 推迟。
- 7 天超时退款 —— 推迟。**已知代价**：一条停在 `needs_input` 的生成，那份预扣会一直挂着。并发闸门按 **task** 状态计数（`aurora.sql` 的 `CountActiveGenerations`），不受影响；月度额度在创建时已计。
- `aurora-issue` 皮肤、`IssueDetail` 的结构与类名改动。
- 独立执行页。
- 「再来一版」入口 —— 本版由人回 Aurora 抽屉重新提交（新生成、新 issue、新的一份预扣）。issue 是普通 Multica issue，不往里加 Aurora 专属入口。
- 13 份技能文档除提问段以外的任何改动。

## 文件

| 文件 | 改什么 |
| --- | --- |
| `server/internal/service/task.go` | `AuroraIssueContextType`；`EnqueueAuroraIssueTask`；`EnqueueAuroraIssueRound` |
| `server/internal/service/issue.go` | `IssueCreateOpts.SkipAssignedAgentEnqueue` |
| `server/internal/service/aurora_completion.go` | 只结算仍在进行中的轮次 |
| `server/internal/handler/daemon.go` | claim 抄出提示词、附件、issue id、本轮评论 |
| `server/internal/handler/aurora.go` | 创建顺序、响应里的 issue 编号、`abandonAuroraIssue` |
| `server/internal/handler/aurora_artifact.go` | 接受「提问轮」上报，写 `needs_input` |
| `server/internal/handler/aurora_issue_comment.go` | 新增：评论门闩与续跑轮的路由 |
| `server/internal/handler/comment.go` | 在合并/义务登记之前接入门闩 |
| `server/internal/daemon/prompt.go` | 提示词带 issue、本轮评论、提问指引 |
| `server/internal/daemon/aurora_manifest.go` | manifest 加 `outcome`，收集器认它 |
| `server/internal/daemon/daemon.go` | `attachAuroraArtifacts` 不再把提问轮判死 |
| `server/internal/daemon/client.go` | `ReportTaskArtifacts` 请求体加 `outcome` |
| `server/internal/aurora/issue_title.go` | 新增：`IssueTitle` |
| `server/internal/aurora/workflows/*.md` | 13 份各加同一段 Failure behavior |
| `server/migrations/586_aurora_generation_issue.up.sql` | 加 `issue_id` |
| `server/migrations/587_aurora_generation_issue_idx.up.sql` | `issue_id` 并发索引 |
| `server/pkg/db/queries/aurora.sql` | 生成查询带 `issue_id`；`GetAuroraGenerationByIssueID`；`UpdateAuroraGenerationBound`；`UpdateAuroraGenerationRound`；`MarkAuroraGenerationNeedsInput` |
| `server/pkg/db/queries/agent.sql` | issue task 写入 Aurora 上下文 |
| `packages/core/aurora/schema.ts` | `issueIdentifier` 默认 `""` |
| `packages/core/aurora/types.ts` | `needs_input` 不是终态 |
| `packages/core/aurora/issue-link.ts` | 新增：`auroraIssuePath` / `auroraIssueHref` |
| `packages/views/aurora/labels.ts` | `needs_input` 的状态标签 |
| `packages/views/locales/{en,zh-Hans,fr,ja,ko}/aurora.json` | `composer.status.needs_input`（五份都要，`parity.test.ts` 会校验） |
| `packages/views/aurora/{works-list,history-list,generation-detail}.tsx` | 链接到 issue |
| `packages/views/aurora/generation-composer.tsx` | 成功后打开问题详情 |
| `apps/aurora/lib/issue-link.ts` | 新增：读 `NEXT_PUBLIC_WEB_ORIGIN` |
| `apps/aurora/config/runtime-urls.ts` | 解析 `NEXT_PUBLIC_WEB_ORIGIN` |
| `apps/aurora/app/[workspaceSlug]/issues/[id]/page.tsx` | 新增：未配置 origin 时的渲染路径 |
| `.env.example` | 声明 `NEXT_PUBLIC_WEB_ORIGIN` |

2026-10-13 最高迁移前缀是 `585`。动手前再看一眼；如果更大，用 `max+1` 和 `max+2`，不要复用已有前缀。

---

### 任务 1：claim 认 issue 轮，提示词带 issue 与本轮评论

**文件：**
- 修改：`server/internal/service/task.go`（只加上下文类型常量）
- 修改：`server/internal/handler/daemon.go`
- 修改：`server/internal/daemon/prompt.go`
- 修改：`server/internal/daemon/prompt_test.go`
- 测试：`server/internal/handler/daemon_aurora_claim_test.go`

**接口：**
- 产出：`const AuroraIssueContextType = "aurora_issue"`，定义在 `server/internal/service/task.go`。
- 产出：`func applyAuroraIssueClaim(resp *AgentTaskResponse, task db.AgentTaskQueue) bool`

技能文档仍由现有的 `ensureTaskSkillBundles` 按 `aurora:` 系统键放入工作目录，产物仍由 `isAuroraTask` 判定收集。不要把 markdown 嵌进 claim。

本任务落地时还没有带 issue 的轮次，所以提示词必须同时保留两支。这是中间态的硬要求，不是可选项。

- [ ] **步骤 1：写会失败的测试**

改 `TestBuildAuroraPromptPointsAtTheSkillDocument`：设上 `IssueID`。删掉期望 `"do not create, update, or comment on issues"`。改为期望 issue id、`Do not create another issue.`、`.claude/skills/`、以及 `multica issue comment add`。

新增两个测试：

- `TestBuildAuroraPromptWithoutIssueKeepsTheOldWording`：`IssueID` 为空时提示词仍写「不是 issue 轮」，且不出现 `Do not create another issue.`。
- `TestBuildAuroraPromptCarriesTheTriggerComment`：`TriggerCommentContent` 非空时，提示词里出现该正文。

`TestApplyAuroraIssueClaimCopiesPromptAndIssue`：issue 已绑定、context 类型是 `aurora_issue` 时，claim 得到 `generation_id`、提示词和附件 id，且不清除 `IssueID`。类型是 `quick_create` 或字段缺失时返回 false，行为与现在相同。

- [ ] **步骤 2：运行，确认失败**

运行：`(cd server && go test ./internal/daemon/ -run 'TestBuildAuroraPrompt' -count=1)`

预期：失败。

- [ ] **步骤 3：改提示词和 claim**

`buildAuroraPrompt`：

- `task.IssueID != ""` 时，开头改成「这一轮就跑在下面这条 issue 上，不要再建一条 issue」，并打印 `Issue id`。为空时保留现在的 `It is NOT an issue run` 措辞。
- 保留技能文档指引、`Task id`、`Generation id`、输出根与 manifest 路径、用户提示词、附件。
- 硬规则里删掉「不要评论 issue」，换成：缺必填信息时在这条 issue 上提问（用 `multica issue comment add <issue id> --content-file`），写完就停。只在有 issue 时给这一条。
- `task.TriggerCommentContent != ""` 时打印「The person's latest reply on this issue:」加上正文 —— 这是后续轮次的真实输入。
- 让模型自己去读这条 issue 的评论（`multica issue comment list <issue id> --roots-only --summary --compact --output json`）。被门闩拦下的评论（进行中发的、上一轮之后发的）不会进入任何提示词，只有它能读到。
- 顺手改掉两处会变成假话的注释：`buildPromptBody` 里的「A generation is not an issue」，以及 `buildAuroraPrompt` 自己的 doc comment。

在 `server/internal/service/task.go` 增加：

```go
const AuroraIssueContextType = "aurora_issue"
```

任务 2 的入队使用这个常量，不要再定义一次。

```go
func applyAuroraIssueClaim(resp *AgentTaskResponse, task db.AgentTaskQueue) bool {
    var ctx struct {
        Type          string   `json:"type"`
        GenerationID  string   `json:"generation_id"`
        Prompt        string   `json:"prompt"`
        AttachmentIDs []string `json:"attachment_ids"`
    }
    if json.Unmarshal(task.Context, &ctx) != nil || ctx.Type != service.AuroraIssueContextType {
        return false
    }
    resp.QuickCreatePrompt = ctx.Prompt
    resp.QuickCreateAttachmentIDs = append([]string(nil), ctx.AttachmentIDs...)
    resp.GenerationID = ctx.GenerationID
    return true
}
```

在 `taskToResponse` 之后、对每条 claim 调用。不要放进 `!task.IssueID.Valid` 的分支 —— 带 issue 的 Aurora 轮次正是要走这里。旧的 quick-create 分支留着，`GenerationID` 仍为空时它照样生效。

- [ ] **步骤 4：运行测试**

运行：`(cd server && go test ./internal/daemon/ -run 'TestBuildAuroraPrompt' -count=1 && go test ./internal/handler/ -run TestApplyAuroraIssueClaimCopiesPromptAndIssue -count=1)`

预期：通过。

- [ ] **步骤 5：提交**

```bash
git commit -m "feat(aurora): keep the skill brief on an issue claim"
```

---

### 任务 2：建 issue、按现在的方式预扣、再入队

**文件：**
- 新建：`server/migrations/586_aurora_generation_issue.up.sql` / `.down.sql`
- 新建：`server/migrations/587_aurora_generation_issue_idx.up.sql` / `.down.sql`
- 新建：`server/internal/aurora/issue_title.go` / `issue_title_test.go`
- 新建：`server/internal/migrations/aurora_generation_issue_migration_test.go`
- 修改：`server/pkg/db/queries/aurora.sql`、`server/pkg/db/queries/agent.sql`
- 修改：`server/internal/service/task.go`、`server/internal/service/issue.go`
- 修改：`server/internal/handler/aurora.go`、`server/internal/handler/aurora_test.go`
- 修改：`packages/core/aurora/schema.ts`

**接口：**
- 依赖：任务 1 的 `AuroraIssueContextType`，现有 `Credit.Reserve`、`failGenerationAndRefund`、`IssueService.Create`。
- 产出：`aurora_generation.issue_id UUID`
- 产出：`func IssueTitle(displayName, prompt string) string`
- 产出：`func (s *TaskService) EnqueueAuroraIssueTask(ctx context.Context, issue db.Issue, generationID, prompt string, attachmentIDs []string) (db.AgentTaskQueue, error)`
- 产出：`IssueCreateOpts.SkipAssignedAgentEnqueue bool`
- 产出：响应字段 `issueIdentifier`。客户端默认 `""`。

- [ ] **步骤 1：写会失败的测试**

`TestIssueTitleUsesDisplayNameAndFortyRunes`：`IssueTitle("海报制作", "  "+strings.Repeat("中", 41))` 等于 `"海报制作：" + strings.Repeat("中", 40)`。

`TestAuroraGenerationIssueMigrationUpDown`：照 `channel_chat_context_generation_migration_test.go` 的用法调 `applyMigrationFile`（helper 定义在 `attribution_constraint_migration_test.go`）。断言 `issue_id` 是 `uuid`，索引名是 `aurora_generation_issue_idx`；down 之后两者都消失。不要另写迁移运行器。

`TestCreateAuroraGenerationBindsIssueBeforeEnqueue` 照 `TestCreateAuroraGenerationReservesAndEnqueues` 的准备。断言：

- 响应有 `issueIdentifier`，形如 `PREFIX-N`（断言前缀非空，避免 `#N` 退化值）
- 生成行 `issue_id` 非空
- 该 task 的 `issue_id` 就是这一条
- task context 的 `type` 是 `aurora_issue`，`generation_id` 是这次生成
- 这条 issue 只有 1 条 task（防止 `maybeEnqueueOnAssign` 与 Aurora 各入队一次）
- 标题是 `小红书图片：` 加提示词
- 这条 issue 的 `creator_id` 是发起人
- 预扣金额仍是 `62_000_000`，和现在的 `xhs-image` 测试一样

清理时删掉关联 issue。不要改预扣金额的断言。

`TestIssueTitleUsesDisplayNameAndFortyRunes` 在 `./internal/aurora/`，别忘了它也要跑（见步骤 5）。

- [ ] **步骤 2：运行，确认失败**

运行：`(cd server && go test ./internal/handler/ -run TestCreateAuroraGenerationBindsIssueBeforeEnqueue -count=1)`

预期：失败，没有 issue。

- [ ] **步骤 3：写迁移**

`586_aurora_generation_issue.up.sql`：

```sql
ALTER TABLE aurora_generation
    ADD COLUMN IF NOT EXISTS issue_id UUID;
```

`586` 的 down 是 `DROP COLUMN IF EXISTS issue_id`。

`587` 的 up：

```sql
-- GetAuroraGenerationByIssueID. No foreign key. Own file because
-- CREATE INDEX CONCURRENTLY cannot run inside a transaction.
CREATE INDEX CONCURRENTLY IF NOT EXISTS aurora_generation_issue_idx
    ON aurora_generation (issue_id)
    WHERE issue_id IS NOT NULL;
```

`587` 的 down 是 `DROP INDEX CONCURRENTLY IF EXISTS aurora_generation_issue_idx`。

- [ ] **步骤 4：查询带上这一列**

六条已有生成查询的 SELECT / RETURNING 都在 `error` 后加 `issue_id`。INSERT 不写它。再加：

```sql
-- name: GetAuroraGenerationByIssueID :one
-- 任务 4 的评论门闩用它判断这条 issue 属于哪次生成、是否终态。
SELECT id, workspace_id, user_id, skill_id, prompt, status, task_id,
       credits_reserved, credits_charged, error, issue_id, created_at, updated_at
FROM aurora_generation
WHERE issue_id = $1 AND workspace_id = $2;

-- name: UpdateAuroraGenerationBound :one
UPDATE aurora_generation
SET task_id = $2, credits_reserved = $3, issue_id = $4, updated_at = now()
WHERE id = $1 AND workspace_id = $5
RETURNING id, workspace_id, user_id, skill_id, prompt, status, task_id,
          credits_reserved, credits_charged, error, issue_id, created_at, updated_at;
```

列清单以 `aurora.sql` 里现有查询为准，只插入 `issue_id`，不要顺手加别的列。

`CreateAgentTask` 的 context `CASE` 在现有 `head_sha` 之外，允许写入 `type`、`generation_id`、`prompt`、`attachment_ids`。没有这些参数时，行为与现在相同（`NULL`）。不要改 `CreateDeferredChannelIssueTask`。

然后 `make sqlc`。

- [ ] **步骤 5：创建顺序与入队**

`IssueCreateOpts` 加 `SkipAssignedAgentEnqueue bool`。`maybeEnqueueOnAssign` 增加最后一个参数 `skipEnqueue bool`，为真时立刻返回空 UUID（也跳过 squad 与 runtime 提示那两条支路）。`Create` 传入 `opts.SkipAssignedAgentEnqueue`。直接调用 `maybeEnqueueOnAssign` 的测试传 `false`。

```go
func IssueTitle(displayName, prompt string) string {
    runes := []rune(strings.TrimSpace(prompt))
    if len(runes) > 40 {
        runes = runes[:40]
    }
    return strings.TrimSpace(displayName) + "：" + string(runes)
}

func (s *TaskService) EnqueueAuroraIssueTask(ctx context.Context, issue db.Issue, generationID, prompt string, attachmentIDs []string) (db.AgentTaskQueue, error) {
    return s.enqueueIssueTaskWithAurora(ctx, issue, pgtype.UUID{}, generationID, prompt, attachmentIDs)
}
```

`enqueueIssueTask` 与 `enqueueIssueTaskWithCommentPlan` 的另一处调用方（rerun 路径）把这三项传空。空附件列表写成 `[]`，不要写成 null。

校验、审核、系统 agent 播种、sandbox、权益检查保持不动。`createAuroraGenerationWithEntitlement` 之后：

1. `GetAgentBySystemKey`，键是 `"aurora:" + entry.ID`。失败则 `markGenerationFailed`，返回 500。此时还没预扣，不退款。
2. `IssueService.Create`：`Status: "todo"`，`Priority: "none"`，`Title: aurora.IssueTitle(entry.Name, req.Prompt)`，描述是提示词，`CreatorType: "member"`，**`CreatorID: userUUID`**，指派这个系统 agent，带上附件，`SkipAssignedAgentEnqueue: true`。失败则 `markGenerationFailed`，返回 500。
   `CreatorID` 不是可选项：入队归属只从 issue 的 `CreatorType/CreatorID` 解析（`attributionForIssueTask`）。缺了它会退到 owner_fallback，在 attribution fail-closed 的工作区会**直接拒绝入队**。
3. `Credit.Reserve` 保持现在的调用：`amountMicro := int64(entry.Credits) * microCreditsPerCredit`，reference 是生成 id。余额不足时 `abandonAuroraIssue` 再 `markGenerationFailed(..., "insufficient credits")`，返回 402。其它错误保持现状。
4. `EnqueueAuroraIssueTask`。失败则 `abandonAuroraIssue`，再 `failGenerationAndRefund`，返回 500。
5. `UpdateAuroraGenerationBound`。失败则先 `CancelTaskWithReason`，再放弃 issue 并退款。生成行这时还没有 `task_id`，结算不会再退一次。
6. 201 响应加上 `service.IssueIdentifier(prefix, issue.Number)`；`prefix` 为空（`#N` 退化值）时不加这个字段。

```go
func (h *Handler) abandonAuroraIssue(ctx context.Context, issue db.Issue) {
    if !issue.ID.Valid {
        return
    }
    _, err := h.Queries.UpdateIssue(ctx, db.UpdateIssueParams{
        ID:     issue.ID,
        Status: pgtype.Text{String: "cancelled", Valid: true},
    })
    if err != nil {
        slog.Warn("aurora issue abandon failed", "issue_id", uuidToString(issue.ID), "error", err)
    }
}
```

`UpdateIssue` 里省略的 assignee narg 会写成 NULL，从而解除指派（`next_assignee_type` / `next_assignee_id` 没有 COALESCE）。不要传入旧的 assignee。

`packages/core/aurora/schema.ts` 的生成 schema 加 `issueIdentifier: z.string().default("")`。旧响应仍能解析。不要改 `e2e/fixtures.ts` 的返回类型注解。

- [ ] **步骤 6：运行测试**

运行：`(cd server && go test ./internal/aurora/ -run TestIssueTitle -count=1 && go test ./internal/handler/ -run 'TestCreateAuroraGeneration' -count=1 && go test ./internal/migrations/ -run TestAuroraGenerationIssueMigrationUpDown -count=1)`

运行：`pnpm --filter @multica/core test -- schema.test.ts`

预期：通过。预扣金额与改动前相同。

- [ ] **步骤 7：提交**

```bash
git commit -m "feat(aurora): run a generation on an issue"
```

---

### 任务 3：提问轮 —— 不写产物也能体面结束

这一版的核心。今天一个 Aurora 轮次只有两种结局：有产物 → 结算成功；没有产物或失败 → 判死并退款。提问轮要成为第三种：**声明式地结束，不扣不退**。

**文件：**
- 修改：`server/internal/daemon/aurora_manifest.go`、`server/internal/daemon/aurora_manifest_test.go`、`server/internal/daemon/daemon.go`、`server/internal/daemon/client.go`
- 修改：`server/internal/handler/aurora_artifact.go`、`aurora_artifact_test.go`
- 修改：`server/internal/service/aurora_completion.go`、`aurora_completion_test.go`
- 修改：`server/pkg/db/queries/aurora.sql`
- 修改：`packages/core/aurora/types.ts`、`packages/views/aurora/labels.ts`
- 修改：`packages/views/locales/{en,zh-Hans,fr,ja,ko}/aurora.json`
- 修改：`server/internal/aurora/workflows/*.md`（13 份）

**接口：**
- 产出：manifest 顶层可选 `"outcome": "needs_input"`（缺省视为已交付），此时 `"artifacts": []` 是合法的。
- 产出：`ReportTaskArtifacts` 请求体可选 `"outcome"`；`outcome` 为 `needs_input` 时允许空 `artifacts`。
- 产出：生成状态 `needs_input`（非终态）。

- [ ] **步骤 1：写会失败的测试**

- `TestCollectAuroraArtifactsNeedsInputOutcome`：`outcome` 为 `needs_input`、`artifacts` 为空数组时，收集器返回零产物且**无错误**；`outcome` 为空且 `artifacts` 为空时，仍报 `manifest declares no artifacts`（这是现有守卫，不能放宽）。
- `TestAttachAuroraArtifactsNeedsInputDoesNotBlock`：`attachAuroraArtifacts` 之后 task 不是 `blocked`，且结果带上了这个 outcome。
- `TestReportTaskArtifactsNeedsInputMarksGeneration`：空 `artifacts` + `needs_input` → 200，生成变 `needs_input`；生成的 `credits_charged` 与 `credits_reserved` 都不变。
- `TestSettleSkipsNeedsInputGeneration`：生成已是 `needs_input` 时，`settleAuroraOnCompleted` 与 `settleAuroraOnFailed` 都不动账本、不改状态。
- `needs_input` 在 `isAuroraGenerationTerminal` 里是 false。

- [ ] **步骤 2：运行，确认失败**

运行：`(cd server && go test ./internal/daemon/ -run 'TestCollectAuroraArtifactsNeedsInput|TestAttachAuroraArtifactsNeedsInput' -count=1 && go test ./internal/service/ -run TestSettleSkipsNeedsInputGeneration -count=1)`

预期：失败。

- [ ] **步骤 3：daemon 端认这个 outcome**

`auroraManifest` 加一个可选字段 `Outcome`，JSON 键是 `outcome`。`CollectAuroraArtifacts`：`outcome == "needs_input"` 时跳过 `validateAuroraManifestArtifacts`（就是它现在拒绝空产物列表），头校验照旧（schema/version/task_id/skill_id/producer 仍必须对上），返回空产物与 nil。`provider_run` 可以是 `null`。返回签名多带一个 outcome。

`attachAuroraArtifacts`：拿到 outcome 后不再置 `blocked`，把 outcome 交给结果对象。`ReportTaskArtifacts` 的请求体加可选 `outcome`，daemon 在**结束回调之前**上报（现有顺序本来就是先报产物再报终态，别打乱）。

- [ ] **步骤 4：服务端收下它**

`ReportTaskArtifacts`：`outcome == "needs_input"` 时允许空 `artifacts`，用一个 CAS 把它写成 `needs_input`：

```sql
-- name: MarkAuroraGenerationNeedsInput :one
UPDATE aurora_generation
SET status = 'needs_input', updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND status = 'queued'
RETURNING id, workspace_id, user_id, skill_id, prompt, status, task_id,
          credits_reserved, credits_charged, error, issue_id, created_at, updated_at;
```

`AND status = 'queued'` 让它天然幂等，也不会覆盖已结算的行。`artifacts` 非空时仍走现有校验与提交路径，一个字不改。

`settleableAuroraGeneration` 只结算仍在进行中的轮次：`gen.Status != "queued"` 时返回 false。这样：

- 提问轮（已写 `needs_input`）结束 → 结算空转，预留原地不动。
- 正常轮 → 状态仍是 `queued` → 照现有逻辑结算。
- 真失败（没有 outcome、也没产物）→ 状态仍是 `queued` → 照现有逻辑判死并退款。**这条守卫必须留着。**

`deriveGenerationStatus` 不用改：非 `queued` 的存储状态原样返回。但上报必须发生在终态回调**之前**，否则行还是 `queued`、task 已完成时，详情页会先按 task 派生出「完成」。

- [ ] **步骤 5：前端认这个状态**

`packages/core/aurora/types.ts`：`needs_input` 不是终态（`isAuroraGenerationTerminal`）。
`packages/views/aurora/labels.ts`：`GENERATION_STATUS_LABELS` 加 `needs_input: "needs_input"`，`AuroraGenerationStatusLabel` 联合类型加它。
五个 `aurora.json` 的 `composer.status` 加 `needs_input`（en：`Waiting for your reply`；zh-Hans：`等你回复`；fr/ja/ko 各一句）。少任何一个，`packages/views/locales/parity.test.ts` 会红。

- [ ] **步骤 6：13 份技能文档加同一段**

写在现有 `## Failure behavior` 里，**不新增标题**，不新增任何 `aurora.<verb>`（`TestRewrittenWorkflowsDescribeRealSteps` 会拒绝）。供应商调用步骤一个字不改。同一段：

```
Missing a required input: ask on the issue this run was claimed from —
`multica issue comment add <issue id> --content-file ./question.md` — then end
this round. Declare it in the manifest: `"outcome": "needs_input"` with
`"artifacts": []`. Write no files and call no provider: anything you store is
counted as an artifact and billed.
```

- [ ] **步骤 7：运行测试**

运行：`(cd server && go test ./internal/daemon/ -count=1 && go test ./internal/service/ -run 'Aurora|Settle' -count=1 && go test ./internal/handler/ -run 'TestReportTaskArtifacts' -count=1 && go test ./internal/aurora/ -count=1)`

运行：`pnpm --filter @multica/core test -- types.test.ts && pnpm --filter @multica/views test -- labels.test.ts`

运行：`pnpm --filter @multica/views test -- parity`

预期：通过。

- [ ] **步骤 8：提交**

```bash
git commit -m "feat(aurora): let a round end by asking a question"
```

---

### 任务 4：评论门闩与续跑轮

**文件：**
- 新建：`server/internal/handler/aurora_issue_comment.go` / `aurora_issue_comment_test.go`
- 修改：`server/internal/handler/comment.go`
- 修改：`server/internal/service/task.go`
- 修改：`server/pkg/db/queries/aurora.sql`

**接口：**
- 产出：`func (h *Handler) auroraIssueRoundDecision(ctx context.Context, issue db.Issue, commentBody string) auroraRoundDecision`
- 产出：`func (s *TaskService) EnqueueAuroraIssueRound(ctx context.Context, issue db.Issue, generation db.AuroraGeneration, triggerCommentID pgtype.UUID) (db.AgentTaskQueue, error)`

三种结果，一个都不能少：

| 生成状态 | 人会看到 | 这一轮 |
| --- | --- | --- |
| 进行中（`queued`/`running`） | 评论留下 | **不入队、不并入当前轮** |
| `needs_input` | 评论留下 | 过审核后开下一轮 |
| `completed` / 已判死 | 评论留下 | 不入队 |

- [ ] **步骤 1：写会失败的测试**

`server/internal/handler/aurora_issue_comment_test.go`：

- `TestAuroraIssueCommentDuringRoundIsNotMerged`：当前轮 task 仍在 `queued`/`running` 时，评论不进 `coalesced_comment_ids`、不进 trigger summary，也不产生新 task。
- `TestAuroraIssueCommentOnNeedsInputOpensRound`：评论过审后产生新 task，`task_id` 指到新 task，生成回到 `queued`，`credits_reserved` 不变。
- `TestAuroraIssueCommentOnCompletedDoesNotEnqueue`：`completed` 上评论不入队。
- `TestAuroraIssueCommentBlockedByModeration`：审核拒绝时评论留下、不入队、生成状态与预扣都不变。
- `TestNonAuroraIssueCommentUnaffected`：普通 issue 的评论路径逐字不变（旧响应/旧行为回归）。

- [ ] **步骤 2：运行，确认失败**

运行：`(cd server && go test ./internal/handler/ -run TestAuroraIssueComment -count=1)`

预期：失败。

- [ ] **步骤 3：门闩放在合并之前**

这一步的位置是本任务的关键。`comment.go` 现在的顺序是：先 `registerPlannedCommentForActiveTask` 把评论登记到进行中 run 的义务上，再 `mergeCommentIntoPendingTask` 把它折进排队中的 task，最后才 `enqueueSingleCommentTrigger`。**只在入队前拦，评论仍会被折进当前轮**，那就不是规格要的「进行中不能被塞进当前轮」。

所以在 `enqueueCommentAgentTriggers` 之前（`computeCommentAgentTriggers` 之后）插入一次判定：issue 有对应的 Aurora 生成时，走 `auroraIssueRoundDecision`。它只有两种处置 —— **拦下**（评论留在库里，不登记义务、不合并、不入队）或 **开新一轮**。普通 issue 一个字节都不变。

判定：

1. `GetAuroraGenerationByIssueID(issue.ID, issue.WorkspaceID)`。`ErrNoRows` → 不是 Aurora issue，原路返回。
2. 当前轮进行中 → 拦下。
3. 生成是 `needs_input` → 审核评论正文（`h.Moderation.ScreenPrompt`，用法见 `CreateAuroraGeneration`）：通过则开新一轮；不通过则拦下。
4. 终态 → 拦下。

- [ ] **步骤 4：续跑轮**

```sql
-- name: UpdateAuroraGenerationRound :one
UPDATE aurora_generation
SET task_id = $2, status = 'queued', error = NULL, updated_at = now()
WHERE id = $1 AND workspace_id = $3
RETURNING id, workspace_id, user_id, skill_id, prompt, status, task_id,
          credits_reserved, credits_charged, error, issue_id, created_at, updated_at;
```

`EnqueueAuroraIssueRound` 用与第一轮完全相同的 `aurora_issue` 上下文入队（技能文档、提示词、附件、生成 id 都照旧），然后 `UpdateAuroraGenerationRound` 把 `task_id` 指过去、状态回到 `queued`、清掉上一轮的 `error`。**不第二次预扣。**

顺序仍是「先入队、后写回」。claim 不查库（上下文里什么都有），所以这个窗口无害；等旧 task 的结算迟到时会发现生成已经指向新 task（`GetAuroraGenerationByTaskID` 查不到），直接跳过 —— 这正是规格 §1.5 要的。

- [ ] **步骤 5：运行测试**

运行：`(cd server && go test ./internal/handler/ -run 'TestAuroraIssueComment|TestNonAuroraIssueComment' -count=1 && go test ./internal/handler/ -run 'TestCreateAuroraGeneration|TestEnqueueAuroraIssue' -count=1 && go test ./internal/service/ -count=1)`

预期：通过。

- [ ] **步骤 6：提交**

```bash
git commit -m "feat(aurora): gate comments and open the next round"
```

---

### 任务 5：打开现成的问题详情

**文件：**
- 新建：`packages/core/aurora/issue-link.ts` / `issue-link.test.ts`
- 新建：`apps/aurora/lib/issue-link.ts`
- 新建：`apps/aurora/app/[workspaceSlug]/issues/[id]/page.tsx`
- 修改：`apps/aurora/config/runtime-urls.ts`、`.env.example`
- 修改：`packages/views/aurora/generation-composer.tsx`
- 修改：`packages/views/aurora/{works-list,history-list,generation-detail}.tsx`
- 修改：`apps/aurora/app/[workspaceSlug]/{works/page.tsx,works/[generationId]/page.tsx,history/page.tsx}`

**接口：**
- 产出：`auroraIssuePath(slug, identifier)` 返回 `/${slug}/issues/${identifier}`，两段都 `encodeURIComponent`。`auroraIssueHref(slug, identifier, origin)` 在 origin 为空时返回这条相对路径；否则去掉 origin 末尾斜杠再拼上。origin 由应用层从 `NEXT_PUBLIC_WEB_ORIGIN` 读，不进 `packages/core`。
- `identifier` 不是 `PREFIX-N`（例如 `getIssuePrefix` 失败时的 `#N`）时，两个函数都返回 `null`，调用方保持现有行为。

- [ ] **步骤 1：写会失败的路径测试**

`auroraIssuePath("acme", "MUL-42")` 等于 `/acme/issues/MUL-42`。origin 为 `https://mca.example/` 时 href 等于 `https://mca.example/acme/issues/MUL-42`。origin 为空时 href 是相对路径。`identifier` 为 `""` 或 `"#42"` 时返回 `null`。

- [ ] **步骤 2：运行，确认失败**

运行：`pnpm --filter @multica/core test -- issue-link.test.ts`

预期：失败。

- [ ] **步骤 3：配置与路由**

`NEXT_PUBLIC_WEB_ORIGIN` 现在全仓库只有文档里提到，必须先接线：

- `.env.example` 声明它（注释写清：Aurora 提交后跳过去的那套 Multica Web 的 origin；不设则在 Aurora 内渲染）。
- `apps/aurora/config/runtime-urls.ts` 加一个解析函数（该文件已经是「env 进、URL 出」的形状，照 `cleanHttpUrl` 的写法）。

`apps/aurora/app/[workspaceSlug]/issues/[id]/page.tsx` 只渲染现成的 `IssueDetailRoute`，不包 `DashboardLayout`（`[workspaceSlug]/layout.tsx` 已经负责登录态与工作区解析）。

**已知代价（本版接受）**：Aurora 没有 project/agent 路由，未配置 origin 时在 Aurora 内渲染的 issue 详情里，指向那些页面的链接会 404。自托管/本地开发会落在这条路径上。

- [ ] **步骤 4：成功后打开它**

创建成功且 `issueIdentifier` 能拼出 URL 时，`window.location.assign(href)`，不要再在抽屉里轮询。标识为空或退化的旧响应仍用现在的抽屉轮询。

作品库、历史、旧的生成详情：有 `issueIdentifier` 就链到这条 issue，没有就留在现在的作品页（URL 由 app 层注入，照 `works/page.tsx` 的 `generationHref` 写法）。不要删旧页面。

- [ ] **步骤 5：运行测试**

运行：`pnpm --filter @multica/core test -- issue-link.test.ts && pnpm --filter @multica/aurora test && pnpm --filter @multica/views test -- 'generation-composer|works-list|history-list|generation-detail'`

预期：通过。

- [ ] **步骤 6：提交**

```bash
git commit -m "feat(aurora): open the existing issue detail after submit"
```

---

## 验收

- 创建响应带 `issueIdentifier`（`PREFIX-N`，非 `#N`）。生成行有 `issue_id`，第一轮 task 的 issue 就是这一条，且这条 issue 只有 1 条 task。
- 这条 issue 的 `creator_id` 是发起人；预扣金额、预扣键与改动前逐字相同。
- claim 指向该技能文档，并带上 issue id、用户提示词、附件 id；`IssueID` 没有被清掉。
- 没有 issue 的旧 quick-create 轮次仍能正常 claim 与构造提示词。
- **缺必填信息时**：Agent 在这条 issue 上留下提问评论，manifest 声明 `outcome: needs_input`、零产物；生成进入 `needs_input`；`credits_charged` 与 `credits_reserved` 都不变；task 不是 `blocked`。
- 真失败（无 outcome、无产物）仍判死并退款 —— 与改动前一致。
- `needs_input` 上的人回复：评论过审后开新一轮，`task_id` 指向新 task，生成回到进行中，**不第二次预扣**。
- 进行中的人回复：评论留在 issue 上，既不进 `coalesced_comment_ids`、不进 trigger summary，也不产生新 task；它不会被丢，下一轮的提示词让模型自己读 issue 评论。
- 终态生成上的人回复：不入队。审核不通过的回复：评论留下、不入队、预扣不变。
- 普通 issue 的评论触发行为逐字不变。
- 提交后打开的是现成问题详情，不是新页面；`issueIdentifier` 缺失或退化时退回抽屉轮询。
- 13 份技能文档都有提问段，没有新标题，没有新的 `aurora.<verb>`。
- 没有 `file_count`、没有按文件扣款、没有 7 天超时、没有 `aurora-issue` 皮肤。
