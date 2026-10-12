# Aurora 生成走问题详情实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

上面这一行留给执行器，保持英文。说明用中文。代码、命令、提交信息用英文。

**目标：** 提交一次生成之后，Agent 在一条 issue 上跑，人打开的是现成的问题详情。

**做法：** 先插入生成，再建 issue，预扣仍用现在的一份目录标价，然后 `EnqueueTaskForIssue`。claim 仍带技能文档、提示词、附件和 issue id。结算金额不改。

**技术栈：** Go（Chi、sqlc、pgx）、现有 Aurora 预扣、现有 `IssueDetail`。

**规格：** `docs/superpowers/specs/2026-10-12-aurora-issue-execution-design.md`

修订：2026-10-13。计费按本计划，不按规格第 6、7、9 节。

## 全局约束

- 不拆计划。四步：列、创建并入队、claim 提示词、打开现成问题详情。
- 不建外键。`issue_id` 索引用单独迁移，`CREATE INDEX CONCURRENTLY`。
- **计费不改实现。** 不改 `server/internal/aurora/credit.go`。不改 `settleAuroraOnCompleted` / `settleAuroraOnFailed` 的金额。不加 `file_count`。不按 token。不做透支。不做 7 天超时。一次生成仍预扣目录 `Credits` 一份，键仍是 `reserve:` 加生成 id。成功时现有结算把已扣记成这份预扣；失败时现有退款退回这份预扣。一条生成一条 issue，所以这就是按 issue 计一份。
- 不新增 `needs_input`。不给评论加 Aurora 门闩。人在这条 issue 上回复，走现有 assignee 入队，不再预扣。这是有意的简单做法，不在本计划里堵。
- 不做皮肤。不改 `IssueDetail` 的结构和类名。不改 13 份技能文档。不做「再来一版」。再提交就是现有抽屉里的新生成、新 issue、新的一份预扣。
- 标题是目录 `Name` 加全角 `：`，再加去掉首尾空白后的提示词前 40 个 rune。
- 打开路径是 `/{workspaceSlug}/issues/{issueIdentifier}`。`issueIdentifier` 是 `service.IssueIdentifier` 的 `PREFIX-N`。不写死主机。`NEXT_PUBLIC_WEB_ORIGIN` 未设置时，Aurora 在同一路径渲染现成的 `IssueDetailRoute`。
- 新 JSON 字段用 Zod 默认值，不用 `as T`。
- 不在 `main` 上开发。PR 开到 `eanfs/multica`。

## 不做

- 文件数、按文件扣款、透支、暂存行 `billed_at`
- `needs_input`、评论续跑门闩、完成锁、7 天超时
- `aurora-issue` 皮肤、弹出层换色
- 技能文档改写
- 按 token 计费

## 文件

| 文件 | 改什么 |
| --- | --- |
| `server/migrations/586_aurora_generation_issue.up.sql` | 只加 `issue_id` |
| `server/migrations/587_aurora_generation_issue_idx.up.sql` | `issue_id` 并发索引 |
| `server/pkg/db/queries/aurora.sql` | 查询带上 `issue_id` |
| `server/pkg/db/queries/agent.sql` | issue task 写入 Aurora 上下文 |
| `server/internal/handler/aurora.go` | 创建顺序和响应里的 issue 编号 |
| `server/internal/service/task.go` | `EnqueueAuroraIssueTask` |
| `server/internal/service/issue.go` | 创建时跳过自动入队 |
| `server/internal/handler/daemon.go` | claim 抄出提示词和附件 |
| `server/internal/daemon/prompt.go` | 提示词带上 issue id，禁止再建一条 issue |
| `packages/core/aurora/schema.ts` | `issueIdentifier` 默认 `""` |
| `apps/aurora` | 成功后打开现成问题详情 |

2026-10-13 最高迁移前缀是 `585`。动手前再看一眼；如果更大，用 `max+1` 和 `max+2`，不要复用已有前缀。

---

### 任务 1：只加 `issue_id`

**文件：**
- 新建：`server/migrations/586_aurora_generation_issue.up.sql`
- 新建：`server/migrations/586_aurora_generation_issue.down.sql`
- 新建：`server/migrations/587_aurora_generation_issue_idx.up.sql`
- 新建：`server/migrations/587_aurora_generation_issue_idx.down.sql`
- 修改：`server/pkg/db/queries/aurora.sql`
- 测试：`server/internal/migrations/aurora_generation_issue_migration_test.go`

**接口：**
- 产出：`aurora_generation.issue_id UUID`。每条生成查询的列清单在 `error` 后加 `issue_id`，其余列不动。

- [ ] **步骤 1：写会失败的迁移测试**

照 `channel_chat_context_generation_migration_test.go` 的 `applyMigrationFile`。断言 `issue_id` 是 `uuid`，索引名是 `aurora_generation_issue_idx`。down 之后两者都消失。不要另写迁移运行器。

- [ ] **步骤 2：运行，确认失败**

运行：`(cd server && go test ./internal/migrations/ -run TestAuroraGenerationIssueMigrationUpDown -count=1)`

预期：失败，迁移文件不存在。

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

- [ ] **步骤 5：生成 sqlc 并跑测试**

运行：`make sqlc`

然后 `(cd server && go test ./internal/handler/ ./internal/service/ ./internal/aurora/ -count=0)`。编不过的 `db.AuroraGeneration` 字面量把 `IssueID` 设为零值，不改行为。

运行：`(cd server && go test ./internal/migrations/ -run TestAuroraGenerationIssueMigrationUpDown -count=1)`

预期：通过。

- [ ] **步骤 6：提交**

```bash
git commit -m "feat(aurora): store the issue id on a generation"
```

---

### 任务 2：先建 issue，再按现在的方式预扣，再入队

**文件：**
- 新建：`server/internal/aurora/issue_title.go`
- 新建：`server/internal/aurora/issue_title_test.go`
- 修改：`server/pkg/db/queries/agent.sql`
- 修改：`server/internal/service/task.go`
- 修改：`server/internal/service/issue.go`
- 修改：`server/internal/handler/aurora.go`
- 修改：`server/internal/handler/aurora_test.go`
- 修改：`packages/core/aurora/schema.ts`

**接口：**
- 依赖：现有 `Credit.Reserve`、`failGenerationAndRefund`、`IssueService.Create`。
- 产出：
  - `func IssueTitle(displayName, prompt string) string`
  - `const AuroraIssueContextType = "aurora_issue"`
  - `func (s *TaskService) EnqueueAuroraIssueTask(ctx context.Context, issue db.Issue, generationID, prompt string, attachmentIDs []string) (db.AgentTaskQueue, error)`
  - `IssueCreateOpts.SkipAssignedAgentEnqueue bool`
  - 响应字段 `issueNumber`、`issueIdentifier`。客户端默认 `0` 和 `""`。

- [ ] **步骤 1：写会失败的测试**

`TestIssueTitleUsesDisplayNameAndFortyRunes`：`IssueTitle("海报制作", "  "+strings.Repeat("中", 41))` 等于 `"海报制作：" + strings.Repeat("中", 40)`。

`TestCreateAuroraGenerationBindsIssueBeforeEnqueue` 照 `TestCreateAuroraGenerationReservesAndEnqueues` 的准备。断言：

- 响应有 `issueIdentifier`，形如 `PREFIX-N`
- 生成行 `issue_id` 非空
- 该 task 的 `issue_id` 就是这一条
- task context 的 `type` 是 `aurora_issue`，`generation_id` 是这次生成
- 这条 issue 只有 1 条 task
- 标题是 `小红书图片：` 加提示词
- 预扣金额仍是 `62_000_000`，和现在的 `xhs-image` 测试一样

清理时删掉关联 issue。不要改预扣金额的断言。

- [ ] **步骤 2：运行，确认失败**

运行：`(cd server && go test ./internal/handler/ -run TestCreateAuroraGenerationBindsIssueBeforeEnqueue -count=1)`

预期：失败，没有 issue。

- [ ] **步骤 3：跳过自动入队，写入 Aurora 上下文**

`IssueCreateOpts` 加 `SkipAssignedAgentEnqueue bool`。`maybeEnqueueOnAssign` 增加最后一个参数 `skipEnqueue bool`，为真时立刻返回空 UUID。`Create` 传入 `opts.SkipAssignedAgentEnqueue`。直接调用 `maybeEnqueueOnAssign` 的测试传 `false`。

`CreateAgentTask` 的 context `CASE` 在现有 `head_sha` 之外，允许写入 `type`、`generation_id`、`prompt`、`attachment_ids`。没有这些参数时，行为与现在相同。不要改 `CreateDeferredChannelIssueTask`。然后 `make sqlc`。

```go
const AuroraIssueContextType = "aurora_issue"

func (s *TaskService) EnqueueAuroraIssueTask(ctx context.Context, issue db.Issue, generationID, prompt string, attachmentIDs []string) (db.AgentTaskQueue, error) {
    return s.enqueueIssueTaskWithAurora(ctx, issue, pgtype.UUID{}, generationID, prompt, attachmentIDs)
}
```

`enqueueIssueTask` 把这三项传空。`task.go` 里另一处调用 `enqueueIssueTaskWithCommentPlan` 的地方也传空。空附件列表写成 `[]`，不要写成 null。

- [ ] **步骤 4：改创建顺序**

校验、审核、系统 agent、sandbox、权益检查保持不动。`createAuroraGenerationWithEntitlement` 之后：

1. `GetAgentBySystemKey`，键是 `"aurora:" + entry.ID`。失败则 `markGenerationFailed`，返回 500。此时还没预扣，不退款。
2. `IssueService.Create`：`Status: "todo"`，`Priority: "none"`，`Title: aurora.IssueTitle(entry.Name, req.Prompt)`，描述是提示词，指派这个系统 agent，`CreatorType: "member"`，带上附件，`SkipAssignedAgentEnqueue: true`。失败则 `markGenerationFailed`，返回 500。
3. `Credit.Reserve` 保持现在的调用：`amountMicro := int64(entry.Credits) * microCreditsPerCredit`，reference 是生成 id。余额不足时调用下面的 `abandonAuroraIssue`，`markGenerationFailed(..., "insufficient credits")`，返回 402。
4. `EnqueueAuroraIssueTask`。失败则 `abandonAuroraIssue`，再 `failGenerationAndRefund`，返回 500。
5. `UpdateAuroraGenerationBound`。失败则先 `CancelTaskWithReason`，再放弃 issue 并退款。生成行这时还没有 `task_id`，结算不会再退一次。
6. 201 响应加上 `issue.Number` 和 `service.IssueIdentifier(h.getIssuePrefix(...), issue.Number)`。

```go
func IssueTitle(displayName, prompt string) string {
    runes := []rune(strings.TrimSpace(prompt))
    if len(runes) > 40 {
        runes = runes[:40]
    }
    return strings.TrimSpace(displayName) + "：" + string(runes)
}

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

`UpdateIssue` 里省略的 assignee narg 会写成 NULL，从而解除指派。不要传入旧的 assignee。对照生成的 `UpdateIssueParams` 确认这一点。

`packages/core/aurora/schema.ts` 加 `issueNumber: z.number().int().default(0)` 和 `issueIdentifier: z.string().default("")`。旧响应仍能解析。不要改 `e2e/fixtures.ts` 的返回类型注解。

- [ ] **步骤 5：运行测试**

运行：`(cd server && go test ./internal/handler/ -run 'TestCreateAuroraGeneration' -count=1)`

运行：`pnpm --filter @multica/core test -- types.test.ts`

预期：通过。预扣金额与改动前相同。

- [ ] **步骤 6：提交**

```bash
git commit -m "feat(aurora): run a generation on an issue"
```

---

### 任务 3：claim 仍带技能文档和 issue id

**文件：**
- 修改：`server/internal/handler/daemon.go`
- 修改：`server/internal/daemon/prompt.go`
- 修改：`server/internal/daemon/prompt_test.go`
- 测试：`server/internal/handler/daemon_aurora_claim_test.go`

**接口：**
- 产出：`func applyAuroraIssueClaim(resp *AgentTaskResponse, task db.AgentTaskQueue) bool`

技能文档仍由现有的 `ensureTaskSkillBundles` 按 `aurora:` 系统键放入工作目录。不要把 markdown 嵌进 claim。不要改 13 份技能文档。

- [ ] **步骤 1：写会失败的测试**

改 `TestBuildAuroraPromptPointsAtTheSkillDocument`：设上 `IssueID`。删掉期望 `"do not create, update, or comment on issues"`。改为期望 issue id、`Do not create another issue.`，以及 `.claude/skills/`。

`TestApplyAuroraIssueClaimCopiesPromptAndIssue`：issue 已绑定、context 类型是 `aurora_issue` 时，claim 得到 `generation_id`、提示词和附件 id，且不清除 `IssueID`。类型是 `quick_create` 时返回 false。没有 issue 的旧 quick-create 分支留着。

- [ ] **步骤 2：运行，确认失败**

运行：`(cd server && go test ./internal/daemon/ -run 'TestBuildAuroraPrompt' -count=1)`

预期：失败。

- [ ] **步骤 3：改提示词和 claim**

`buildAuroraPrompt` 开头改成：这是一次生成的一轮，就在下面这条 issue 上，不要再建一条 issue。打印 `Issue id`。保留技能文档、提示词和附件。硬规则里删掉「不要评论 issue」。提问时用 `multica issue comment add <issue id>` 写在这条 issue 上。不要把 Aurora task 送进 `buildCommentPrompt` 或普通 issue 简报。`isAuroraTask` 仍在 quick-create 分支前面。

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

在 `taskToResponse` 之后、对每条 claim 调用。不要放进 `!task.IssueID.Valid` 的分支。`GenerationID` 仍为空时，保留现有的 `GetAuroraGenerationByTaskID` 宽限路径。

- [ ] **步骤 4：运行测试**

运行：`(cd server && go test ./internal/daemon/ -run 'TestBuildAuroraPrompt' -count=1 && go test ./internal/handler/ -run TestApplyAuroraIssueClaimCopiesPromptAndIssue -count=1)`

预期：通过。

- [ ] **步骤 5：提交**

```bash
git commit -m "feat(aurora): keep the skill brief on an issue claim"
```

---

### 任务 4：打开现成的问题详情

**文件：**
- 新建：`packages/core/aurora/issue-link.ts`
- 新建：`packages/core/aurora/issue-link.test.ts`
- 新建：`apps/aurora/lib/issue-link.ts`
- 新建：`apps/aurora/app/[workspaceSlug]/issues/[id]/page.tsx`
- 修改：`packages/views/aurora/generation-composer.tsx`
- 修改：`packages/views/aurora/works-list.tsx`
- 修改：`apps/aurora/app/[workspaceSlug]/works/page.tsx`
- 修改：历史页和 `apps/aurora/app/[workspaceSlug]/works/[generationId]/page.tsx`

**接口：**
- 产出：`auroraIssuePath(slug, identifier)` 返回 `/${slug}/issues/${identifier}`，两段都 `encodeURIComponent`。`auroraIssueHref(slug, identifier, origin)` 在 origin 为空时返回这条相对路径；否则去掉 origin 末尾斜杠再拼上。origin 由应用层从 `NEXT_PUBLIC_WEB_ORIGIN` 读，不进 `packages/core`。

- [ ] **步骤 1：写会失败的路径测试**

`auroraIssuePath("acme", "MUL-42")` 等于 `/acme/issues/MUL-42`。origin 为 `https://mca.example/` 时，href 等于 `https://mca.example/acme/issues/MUL-42`。origin 为空时 href 是相对路径。

- [ ] **步骤 2：运行，确认失败**

运行：`pnpm --filter @multica/core test -- issue-link.test.ts && pnpm --filter @multica/aurora test -- issue-link.test.ts`

预期：失败。

- [ ] **步骤 3：创建成功后打开 issue**

`apps/aurora/app/[workspaceSlug]/issues/[id]/page.tsx` 只渲染现成的 `IssueDetailRoute`，不包 `DashboardLayout`。Aurora 工作区布局已经会把未登录的人转到登录页。

创建成功且 `issueIdentifier` 非空时，`window.location.assign(href)`，不要再在抽屉里轮询。标识为空的旧响应仍用现在的抽屉轮询。

作品库、历史、旧的生成详情：有 `issueIdentifier` 就链到这条 issue，没有就留在现在的作品页。不要删旧页面。

- [ ] **步骤 4：运行测试**

运行：`pnpm --filter @multica/core test -- issue-link.test.ts && pnpm --filter @multica/aurora test -- issue-link.test.ts && pnpm --filter @multica/views test -- generation-composer.test.tsx`

预期：通过。

- [ ] **步骤 5：提交**

```bash
git commit -m "feat(aurora): open the existing issue detail after submit"
```

---

## 验收

- 创建响应带 `issueIdentifier`。生成行有 `issue_id`。第一轮 task 的 issue 就是这一条。
- claim 指向该技能文档，并带上 issue id。不得只用普通 issue 简报。
- 预扣金额、预扣键、成功记账、失败退款与改动前相同。没有新的扣款函数。
- 提交后打开的是现成问题详情，不是新页面。
- 没有 `file_count`、`needs_input`、皮肤改动、技能文档改动。
