# Aurora 生成走问题详情实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

上面这一行留给执行器，保持英文。下面的说明用中文，与规格一致。代码、命令、提交信息仍用英文。

**目标：** 人提交一次 Aurora 生成之后，打开的是现成的问题详情。Agent 在这条 issue 上跑。只有存成功的产出文件才扣费。后续评论只在生成等输入时才续跑。

**做法：** 一行生成管账单和当前轮。一条 issue 管页面。创建时先插入生成，再建 issue，且不自动入队；预扣 1 份标价之后，再入队一条仍带着技能提示词和附件的 issue task。暂存一个文件时，在同一个事务里增加 `file_count`：有未对上的预扣就用掉它，否则允许透支扣款。任务结束只改状态。评论只有在 `needs_input` 时才开下一轮。

**技术栈：** Go（Chi、sqlc、pgx）、PostgreSQL 迁移、Next.js App Router、Zod、Vitest、现有 Aurora 积分账。

**规格：** `docs/superpowers/specs/2026-10-12-aurora-issue-execution-design.md`

## 全局约束

- 不要拆这份计划。规格第 11 节是一条流水线：绑定并入队，然后文件数与扣款，然后评论续跑和完成锁，然后打开与皮肤，然后技能文档。前一阶段的验收不过，不开始下一阶段。
- 不建外键，不做级联删除或级联更新。issue 和生成的关系在应用代码里校验。
- `issue_id` 的索引单独一条迁移，用 `CREATE INDEX CONCURRENTLY`。运行器在事务外执行这个文件。
- `file_count` 从 0 开始。已扣金额是 `file_count` 乘目录里的 `Credits`。微积分是 `Credits * 1_000_000`（`server/internal/handler/aurora.go` 里的 `microCreditsPerCredit`）。
- 开工预扣的键仍是 `reserve:` 加生成 id。这份占用不进 `file_count`，也不进已扣次数。
- 预扣用完之后的文件，不要调用 `CreditService.Reserve` 或 `DeductCreditBalance`。两者会拒绝非正数或透支。
- 同一个暂存 id 不得再次加数或扣款。加数和扣款在同一个事务里提交。
- 用户上传的附件不加 `file_count`。审核拒绝、不展示、不提交成作品，都不减。
- 退款只发生在入队失败和 7 天超时，且只退尚未对上文件的预扣（`file_count` 仍为 0 时那份预扣）。没有这种预扣就不退。
- 一条生成对应一条 issue。`task_id` 只指向当前轮。晚到的结算如果 task id 不是生成当前的 `task_id`，直接跳过。
- 不解析评论正文来判断做完没有。不再用「有已提交作品就扣整份预留、没有就退款」。
- `needs_input` 不是账单终态。不要把它加进 `auroraTerminal` 或 `isAuroraGenerationTerminal`。也不要加进 `CountActiveGenerations`。
- 标题是目录 `Name`（不是 `NameEn`）加全角 `：`，再加去掉首尾空白后的提示词前 40 个 rune。
- 工作区标签名是 `aurora`，资源类型 `issue`，颜色 `#7166f3`。没有就建。名称冲突就再读一次。
- 打开路径是 `/{workspaceSlug}/issues/{issueIdentifier}`。`issueIdentifier` 是 `service.IssueIdentifier` 产生的 `PREFIX-N`。源码里不写主机。浏览器来源是去掉末尾斜杠的 `NEXT_PUBLIC_WEB_ORIGIN`。该环境变量未设置时，留在 Aurora 的同一路径。不探测 cookie。
- 不改 `IssueDetail` 的元素树和已有类名。唯一新增的类是现有根节点上的 `aurora-issue`。不改 `packages/ui/styles/` 下的 Multica issue 样式。不把 Aurora token 写进 web 的 `:root`。不把 issue 皮肤引进 `apps/aurora/app/globals.css`。
- 技能文档：不加标题，不加 `aurora.<verb>`，不改供应商步骤。Failure behavior 里的那段话是规格原句，只出现一次。
- 新的 JSON 字段用 Zod 加默认值解析，不用 `as T`。
- 代码、注释、提交信息保持英文。本计划的说明用中文。界面文案进 `en`、`zh-Hans`、`ja`、`ko`、`fr`。
- 系统评论保持英文，与 `RuntimeUnusableNotice` 一致。
- 移动端不在范围内。桌面端跟着共享的 `IssueDetail` 改，不新开路由。
- 不在 `main` 上开发。每次提交只包含该任务点名的文件。之后的 PR 开到 `eanfs/multica`。

---

## 文件职责

| 文件 | 职责 |
| --- | --- |
| `server/migrations/586_aurora_generation_issue.up.sql` / `.down.sql` | `issue_id`、`file_count`、`needs_input_at`，暂存行的 `billed_at` |
| `server/migrations/587_aurora_generation_issue_idx.up.sql` / `.down.sql` | `issue_id` 的并发索引 |
| `server/pkg/db/queries/aurora.sql` | 生成的读写，含新列 |
| `server/pkg/db/queries/credit.sql` | 允许透支的扣款 |
| `server/pkg/db/queries/issue_label.sql` | 按名称查标签 |
| `server/pkg/db/queries/agent.sql` | issue task 插入时写入 Aurora 上下文 |
| `server/internal/aurora/issue_title.go` | 标题函数 |
| `server/internal/aurora/credit.go` | `ChargeStagedFile` |
| `server/internal/service/task.go` | `EnqueueAuroraIssueTask`、结算分支、超时清扫 |
| `server/internal/handler/aurora.go` | 创建顺序、响应字段、issue 回滚 |
| `server/internal/handler/aurora_artifact_upload.go` | 暂存和扣款放在同一个事务 |
| `server/internal/handler/daemon.go` | 把 Aurora 上下文抄进已绑定 issue 的 claim |
| `server/internal/daemon/prompt.go` | issue id，以及允许提问的评论 |
| `server/internal/handler/comment.go` | 续跑门闩 |
| `server/cmd/server/runtime_sweeper.go` | 调用 7 天清扫 |
| `server/internal/aurora/workflows/*.md` | 同一段 Failure behavior |
| `packages/core/aurora/schema.ts` | 新响应字段，带默认值 |
| `packages/core/aurora/issue-link.ts` | 路径函数，不读 `process.env` |
| `apps/aurora/lib/issue-link.ts` | 来源取自环境变量 |
| `apps/aurora/app/[workspaceSlug]/issues/[id]/page.tsx` | 现成的 `IssueDetailRoute` |
| `packages/ui/components/ui/portal-container.tsx` | 弹出层容器上下文 |
| `packages/views/aurora/aurora-issue-skin.css` | 只作用于 `.aurora-issue` 的 token 映射 |
| `packages/views/issues/components/issue-detail.tsx` | 类名、锁住的评论框、再来一版链接 |
| `packages/views/aurora/generation-composer.tsx` | 关掉抽屉并打开 issue；预填提示词 |

`needs_input_at` 是规格第 7 节的时钟。`updated_at` 在写 `file_count` 和 `task_id` 时也会动，不能当时钟。`billed_at` 是暂存 id 的重放标记。这两列都不是新的产品对象。

## 阶段门

- 任务 4 之后：创建响应带 `issueIdentifier`；生成行有 `issue_id`；第一轮 task 的 `issue_id` 就是这条 issue；claim 提示词点名技能文档和 issue id。
- 任务 6 之后：同一个暂存 id 只让 `file_count` 加 1；第二个文件可以透支；被拒绝的作品不减少文件数。
- 任务 9 之后：`needs_input` 入队且不第二次预扣；`queued`、`running`、`completed`、已超时的 `failed` 不入队；超时只退尚未对上文件的预扣。
- 任务 12 之后：普通 issue 的根节点没有 `aurora-issue`；web 的 `:root` 没有 Aurora 的 `--background`；Aurora 生成的 issue 根节点有这个类，弹出的菜单继承同一套映射。
- 任务 13 之后：13 份文档都有规格那段话，且只出现一次，没有新的 `aurora.<verb>`。

---

### 任务 1：生成列和 sqlc

**文件：**
- 新建：`server/migrations/586_aurora_generation_issue.up.sql`
- 新建：`server/migrations/586_aurora_generation_issue.down.sql`
- 新建：`server/migrations/587_aurora_generation_issue_idx.up.sql`
- 新建：`server/migrations/587_aurora_generation_issue_idx.down.sql`
- 修改：`server/pkg/db/queries/aurora.sql`
- 测试：`server/internal/migrations/aurora_generation_issue_migration_test.go`

**接口：**
- 依赖：`server/migrations/500_aurora_generation.up.sql` 里的 `aurora_generation` 表；`aurora_artifact_staging` 表。
- 产出：列 `issue_id UUID`、`file_count BIGINT NOT NULL DEFAULT 0`、`needs_input_at timestamptz`、`aurora_artifact_staging.billed_at timestamptz`。`aurora.sql` 里每条生成查询都按这个顺序返回下列：

```sql
id, workspace_id, user_id, skill_id, prompt, status, task_id,
credits_reserved, credits_charged, error, issue_id, file_count, needs_input_at,
created_at, updated_at
```

- [ ] **步骤 1: 确认迁移编号**

运行： `ls server/migrations | awk -F_ '/^[0-9]+_/ {print $1}' | sort -n | tail -1`

这条分支上预期是 `585`。如果更大，列迁移用 `max+1`，索引迁移用 `max+2`，写文件前把下面所有 `586` 和 `587` 换成这两个数。不要复用已有前缀。

- [ ] **步骤 2: 写会失败的迁移测试**

新建 `server/internal/migrations/aurora_generation_issue_migration_test.go`。照 `channel_chat_context_generation_migration_test.go`：在临时 schema 上应用 up 文件，断言列和部分索引，再应用 down 文件，断言它们消失。断言如下：

```go
func TestAuroraGenerationIssueMigrationUpDown(t *testing.T) {
    // apply 586 then 587 using the same helper the channel-context migration test uses
    assertColumn(t, "aurora_generation", "issue_id", "uuid")
    assertColumn(t, "aurora_generation", "file_count", "bigint")
    assertColumn(t, "aurora_generation", "needs_input_at", "timestamp with time zone")
    assertColumn(t, "aurora_artifact_staging", "billed_at", "timestamp with time zone")
    assertIndex(t, "aurora_generation_issue_idx")
    // apply both down files, 587 first
    assertNoColumn(t, "aurora_generation", "issue_id")
    assertNoIndex(t, "aurora_generation_issue_idx")
}
```

用该包里已有的 `applyMigrationFile`。不要另写一套迁移运行器。如果这个函数在 channel 测试文件里未导出，就把本测试放进同一个 `migrations` 包再调用它。

- [ ] **步骤 3: 运行测试，确认它失败**

运行： `(cd server && go test ./internal/migrations/ -run TestAuroraGenerationIssueMigrationUpDown -count=1)`

预期：失败，因为迁移文件还不存在。

- [ ] **步骤 4: 写迁移**

`server/migrations/586_aurora_generation_issue.up.sql`:

```sql
ALTER TABLE aurora_generation
    ADD COLUMN IF NOT EXISTS issue_id UUID,
    ADD COLUMN IF NOT EXISTS file_count BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS needs_input_at timestamptz;

ALTER TABLE aurora_artifact_staging
    ADD COLUMN IF NOT EXISTS billed_at timestamptz;
```

`server/migrations/586_aurora_generation_issue.down.sql`:

```sql
ALTER TABLE aurora_artifact_staging DROP COLUMN IF EXISTS billed_at;
ALTER TABLE aurora_generation DROP COLUMN IF EXISTS needs_input_at;
ALTER TABLE aurora_generation DROP COLUMN IF EXISTS file_count;
ALTER TABLE aurora_generation DROP COLUMN IF EXISTS issue_id;
```

`server/migrations/587_aurora_generation_issue_idx.up.sql`:

```sql
-- GetAuroraGenerationByIssueID looks up the generation for one issue. No foreign
-- key by house rule. Partial so generations that predate this column stay out
-- of the index.
--
-- In its own file because CREATE INDEX CONCURRENTLY cannot share a statement
-- or run inside a transaction.
CREATE INDEX CONCURRENTLY IF NOT EXISTS aurora_generation_issue_idx
    ON aurora_generation (issue_id)
    WHERE issue_id IS NOT NULL;
```

`server/migrations/587_aurora_generation_issue_idx.down.sql`:

```sql
DROP INDEX CONCURRENTLY IF EXISTS aurora_generation_issue_idx;
```

- [ ] **步骤 5: 扩展每条生成查询**

在 `server/pkg/db/queries/aurora.sql` 里，把已有六条生成查询（`CreateAuroraGeneration`、`GetAuroraGeneration`、`UpdateAuroraGenerationTask`、`UpdateAuroraGenerationTerminal`、`GetAuroraGenerationByTaskID`、`ListAuroraGenerations`）的列清单换成接口节里的那份。`CreateAuroraGeneration` 的 INSERT 仍只写 `(workspace_id, user_id, skill_id, prompt, status)`，`file_count` 用默认值。

追加：

```sql
-- name: GetAuroraGenerationByIssueID :one
SELECT id, workspace_id, user_id, skill_id, prompt, status, task_id,
       credits_reserved, credits_charged, error, issue_id, file_count, needs_input_at,
       created_at, updated_at
FROM aurora_generation
WHERE issue_id = $1 AND workspace_id = $2;

-- name: UpdateAuroraGenerationBound :one
UPDATE aurora_generation
SET task_id = $2, credits_reserved = $3, issue_id = $4, updated_at = now()
WHERE id = $1 AND workspace_id = $5
RETURNING id, workspace_id, user_id, skill_id, prompt, status, task_id,
          credits_reserved, credits_charged, error, issue_id, file_count, needs_input_at,
          created_at, updated_at;

-- name: LockAuroraGeneration :one
SELECT id, workspace_id, user_id, skill_id, prompt, status, task_id,
       credits_reserved, credits_charged, error, issue_id, file_count, needs_input_at,
       created_at, updated_at
FROM aurora_generation
WHERE id = $1 AND workspace_id = $2
FOR UPDATE;

-- name: IncrementAuroraGenerationFileCount :one
UPDATE aurora_generation
SET file_count = file_count + 1,
    credits_charged = credits_charged + sqlc.arg('unit_micro'),
    updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING file_count, credits_charged;

-- name: UpdateAuroraGenerationRoundStatus :execrows
UPDATE aurora_generation
SET status = $2,
    error = $3,
    needs_input_at = sqlc.narg('needs_input_at'),
    updated_at = now()
WHERE id = $1 AND workspace_id = $4 AND task_id = $5 AND status IN ('queued', 'running');

-- name: AdvanceAuroraGenerationRound :execrows
UPDATE aurora_generation
SET task_id = $2, status = 'queued', needs_input_at = NULL, updated_at = now()
WHERE id = $1 AND workspace_id = $3 AND status = 'needs_input';

-- name: ListAuroraGenerationsAwaitingInput :many
SELECT id, workspace_id, user_id, skill_id, prompt, status, task_id,
       credits_reserved, credits_charged, error, issue_id, file_count, needs_input_at,
       created_at, updated_at
FROM aurora_generation
WHERE status = 'needs_input'
  AND needs_input_at IS NOT NULL
  AND needs_input_at < sqlc.arg('before')
ORDER BY needs_input_at
LIMIT sqlc.arg('row_limit');

-- name: LockAuroraArtifactStaging :one
SELECT id, billed_at
FROM aurora_artifact_staging
WHERE id = $1
FOR UPDATE;

-- name: MarkAuroraArtifactStagingBilled :execrows
UPDATE aurora_artifact_staging
SET billed_at = now(), updated_at = now()
WHERE id = $1 AND billed_at IS NULL;
```

如果 `aurora_artifact_staging` 没有 `updated_at`，就不要写那一列。写 mark 查询前先看 `server/pkg/db/queries/aurora_artifact_staging.sql`。`LockAuroraArtifactStaging` 的 SELECT 跟真实列对齐（优先只取 `id, billed_at` 的窄 `:one`；只有必须扫进 `db.AuroraArtifactStaging` 时才带上 `CreateAuroraArtifactStaging` 返回的列）。

追加到 `server/pkg/db/queries/credit.sql`：

```sql
-- name: DeductCreditBalanceAllowOverdraft :one
UPDATE credit_balance
SET available_micro = available_micro - sqlc.arg('amount_micro'), updated_at = now()
WHERE user_id = $1
RETURNING available_micro;
```

追加到 `server/pkg/db/queries/issue_label.sql`：

```sql
-- name: GetIssueLabelByName :one
SELECT * FROM issue_label
WHERE workspace_id = $1
  AND resource_type = 'issue'
  AND LOWER(name) = LOWER($2);
```

- [ ] **步骤 6: 重新生成 sqlc，并修好字面量**

运行： `make sqlc`

然后运行 `(cd server && go test ./internal/handler/ ./internal/service/ ./internal/aurora/ -count=0)`。凡是不再能编译的 `db.AuroraGeneration` 复合字面量，把 `IssueID`、`FileCount`、`NeedsInputAt` 设为零值。这些修补不改变行为。

- [ ] **步骤 7: 运行迁移测试**

运行： `(cd server && go test ./internal/migrations/ -run TestAuroraGenerationIssueMigrationUpDown -count=1)`

预期： PASS.

- [ ] **步骤 8: 提交**

```bash
git add server/migrations/586_aurora_generation_issue.up.sql \
  server/migrations/586_aurora_generation_issue.down.sql \
  server/migrations/587_aurora_generation_issue_idx.up.sql \
  server/migrations/587_aurora_generation_issue_idx.down.sql \
  server/pkg/db/queries/aurora.sql server/pkg/db/queries/credit.sql \
  server/pkg/db/queries/issue_label.sql server/pkg/db/generated \
  server/internal/migrations/aurora_generation_issue_migration_test.go
git commit -m "feat(aurora): add generation issue and file-count columns"
```

如果步骤 1 改了编号，`git add` 就用改后的文件名。

---

### 任务 2: 先建 issue，再预扣，再入队

**文件：**
- 新建： `server/internal/aurora/issue_title.go`
- 新建： `server/internal/aurora/issue_title_test.go`
- 修改： `server/pkg/db/queries/agent.sql` (`CreateAgentTask` context expression)
- 修改： `server/internal/service/task.go` (`EnqueueAuroraIssueTask`)
- 修改： `server/internal/service/issue.go` (`IssueCreateOpts`, `maybeEnqueueOnAssign`)
- 修改： `server/internal/handler/aurora.go` (`CreateAuroraGeneration`, response structs, `generationSummary`)
- 测试： `server/internal/handler/aurora_test.go`

**Interfaces:**
- 依赖： `IssueService.Create`, `GetAgentBySystemKey`, `Credit.Reserve`, catalog `entry.Name` and `entry.Credits`.
- 产出：
  - `func IssueTitle(displayName, prompt string) string`
  - `const AuroraIssueContextType = "aurora_issue"`
  - `type AuroraIssueContext struct { Type, GenerationID, Prompt string; AttachmentIDs []string }`
  - `func (s *TaskService) EnqueueAuroraIssueTask(ctx context.Context, issue db.Issue, generationID, prompt string, attachmentIDs []string) (db.AgentTaskQueue, error)`
  - `IssueCreateOpts.SkipAssignedAgentEnqueue bool`
  - JSON fields `issueNumber` (int), `issueIdentifier` (string), `fileCount` (int). Zero defaults on the client.

- [ ] **步骤 1: 写会失败的标题测试**

`server/internal/aurora/issue_title_test.go`:

```go
func TestIssueTitleUsesDisplayNameAndFortyRunes(t *testing.T) {
    got := IssueTitle("海报制作", "  "+strings.Repeat("中", 41))
    want := "海报制作：" + strings.Repeat("中", 40)
    if got != want {
        t.Fatalf("IssueTitle = %q, want %q", got, want)
    }
    if IssueTitle("海报制作", "短") != "海报制作：短" {
        t.Fatal("short prompt was truncated or lost the fullwidth colon")
    }
}
```

- [ ] **步骤 2: 运行**

运行： `(cd server && go test ./internal/aurora/ -run TestIssueTitleUsesDisplayNameAndFortyRunes -count=1)`

预期：失败，`IssueTitle` 未定义。

- [ ] **步骤 3: 实现标题函数**

`server/internal/aurora/issue_title.go`:

```go
package aurora

import "strings"

// IssueTitle is the issue title for one generation. The cut is 40 runes of the
// trimmed prompt, not 40 bytes. The colon is the fullwidth colon from the spec.
func IssueTitle(displayName, prompt string) string {
    runes := []rune(strings.TrimSpace(prompt))
    if len(runes) > 40 {
        runes = runes[:40]
    }
    return strings.TrimSpace(displayName) + "：" + string(runes)
}
```

- [ ] **步骤 4: 写会失败的创建测试**

在 `server/internal/handler/aurora_test.go` 增加 `TestCreateAuroraGenerationBindsIssueBeforeEnqueue`。准备步骤照 `TestCreateAuroraGenerationReservesAndEnqueues`（发积分、`xhs-image`、同一条提示词）。同时解码 `issueNumber`、`issueIdentifier`、`fileCount`。断言：

```go
if out.Generation.IssueNumber <= 0 {
    t.Fatal("expected an issue number")
}
if out.Generation.IssueIdentifier == "" || !strings.Contains(out.Generation.IssueIdentifier, "-") {
    t.Fatalf("issueIdentifier = %q", out.Generation.IssueIdentifier)
}
if out.Generation.FileCount != 0 {
    t.Fatalf("fileCount = %d, want 0", out.Generation.FileCount)
}
var issueID pgtype.UUID
var fileCount int64
dbfx.QueryRow(t, `SELECT issue_id, file_count FROM aurora_generation WHERE id = $1`, gen.ID).Scan(&issueID, &fileCount)
if !issueID.Valid || fileCount != 0 {
    t.Fatalf("issue_id valid=%v file_count=%d", issueID.Valid, fileCount)
}
var taskIssue pgtype.UUID
var rawContext []byte
dbfx.QueryRow(t, `SELECT issue_id, context FROM agent_task_queue WHERE id = $1`, taskID).Scan(&taskIssue, &rawContext)
if taskIssue != issueID {
    t.Fatal("task issue_id is not the generation issue")
}
var ctx struct {
    Type         string `json:"type"`
    GenerationID string `json:"generation_id"`
}
if err := json.Unmarshal(rawContext, &ctx); err != nil || ctx.Type != "aurora_issue" || ctx.GenerationID != gen.ID {
    t.Fatalf("task context = %s", rawContext)
}
var tasks int
dbfx.QueryRow(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1`, issueID).Scan(&tasks)
if tasks != 1 {
    t.Fatalf("issue has %d tasks, want 1", tasks)
}
var title, status, assigneeType string
dbfx.QueryRow(t, `SELECT title, status, assignee_type FROM issue WHERE id = $1`, issueID).Scan(&title, &status, &assigneeType)
if title != "小红书图片："+prompt { // prompt is under 40 runes
    t.Fatalf("title = %q", title)
}
if status != "todo" || assigneeType != "agent" {
    t.Fatalf("status=%s assignee_type=%s", status, assigneeType)
}
var labelName string
dbfx.QueryRow(t, `
    SELECT l.name FROM issue_label l
    JOIN issue_to_label x ON x.label_id = l.id
    WHERE x.issue_id = $1 AND l.resource_type = 'issue'`, issueID).Scan(&labelName)
if labelName != "aurora" {
    t.Fatalf("label = %q", labelName)
}
```

清理时删掉这条 issue 的 `issue_to_label`、`agent_task_queue` 行、`issue` 行和 `aurora_generation` 行。不要删共享的 `aurora` 标签，别的测试还会用。

同时扩展 `TestCreateAuroraGenerationReservesAndEnqueues`，以及每一个通过 `CreateAuroraGeneration` 创建后又删除 `aurora_generation` 行的 handler 测试：把关联 issue 和它的 `issue_to_label` 也删掉。漏掉的 issue 会消耗工作区计数，后面再跑可能撞号。

- [ ] **步骤 5: 运行创建测试**

运行： `(cd server && go test ./internal/handler/ -run TestCreateAuroraGenerationBindsIssueBeforeEnqueue -count=1)`

预期：失败，因为还没有 `issueNumber`，也没有 issue 行。

- [ ] **步骤 6: 关掉自动入队**

In `IssueCreateOpts` add:

```go
// SkipAssignedAgentEnqueue creates an assigned issue without the automatic
// task. Aurora reserves credits before the only enqueue, so the create path
// must not start a second task.
SkipAssignedAgentEnqueue bool
```

在 `maybeEnqueueOnAssign` 里，调用方传入这个开关时立刻返回 `pgtype.UUID{}`。把它从 `Create` 传下去：给 `maybeEnqueueOnAssign` 加这个布尔参数。该函数目前没有 opts，所以在最后加 `skipEnqueue bool`，`Create` 传入 `opts.SkipAssignedAgentEnqueue`。改那一处生产调用。如果有测试直接调用 `maybeEnqueueOnAssign`，传 `false`。

- [ ] **步骤 7: 把 Aurora 上下文写进 issue task**

在 `server/pkg/db/queries/agent.sql` 里，把 `CreateAgentTask` 的 context `CASE` 换成：

```sql
CASE
    WHEN COALESCE(sqlc.narg('head_sha')::text, '') <> ''
      OR COALESCE(sqlc.narg('aurora_context_type')::text, '') <> ''
    THEN jsonb_strip_nulls(jsonb_build_object(
        'head_sha', NULLIF(sqlc.narg('head_sha')::text, ''),
        'type', NULLIF(sqlc.narg('aurora_context_type')::text, ''),
        'generation_id', NULLIF(sqlc.narg('aurora_generation_id')::text, ''),
        'prompt', NULLIF(sqlc.narg('aurora_prompt')::text, ''),
        'attachment_ids', sqlc.narg('aurora_attachment_ids')::jsonb
    ))
    ELSE NULL
END
```

不要改 `CreateDeferredChannelIssueTask`。运行 `make sqlc`。

In `server/internal/service/task.go`:

```go
const AuroraIssueContextType = "aurora_issue"

type AuroraIssueContext struct {
    Type          string
    GenerationID  string
    Prompt        string
    AttachmentIDs []string
}

func (s *TaskService) EnqueueAuroraIssueTask(ctx context.Context, issue db.Issue, generationID, prompt string, attachmentIDs []string) (db.AgentTaskQueue, error) {
    return s.enqueueIssueTaskWithAurora(ctx, issue, pgtype.UUID{}, AuroraIssueContext{
        Type:          AuroraIssueContextType,
        GenerationID:  generationID,
        Prompt:        prompt,
        AttachmentIDs: attachmentIDs,
    })
}
```

给 `enqueueIssueTaskWithCommentPlan` 的最后一个参数加上 `aurora *AuroraIssueContext`。`enqueueIssueTask` 传 `nil`。`task.go:6119` 那处调用也传 `nil`。`aurora != nil` 时，在 `runFleetTx` 之前填上新的 `CreateAgentTaskParams` 字段。附件 id 用 `json.Marshal`；空切片必须是 `[]`，不能是 null，这样 claim 才能区分「没有附件」和「上下文缺失」。把这段 JSON 传给 sqlc 生成的 `AuroraAttachmentIds` 字段。

- [ ] **步骤 8: 改创建处理的顺序**

保留现有的校验、`ScreenPrompt`、`ensureAuroraSystemAgents`、runtime 和 sandbox 调用。`createAuroraGenerationWithEntitlement` 之后：

1. 用 `GetAgentBySystemKey`（`"aurora:" + entry.ID`）加载系统 agent。出错则 `markGenerationFailed` 并返回 500。不退款：还没预扣。
2. `labelID, err := h.ensureAuroraLabel(ctx, workspaceID)`。出错则 `markGenerationFailed` 并返回 500。
3. `IssueService.Create`，字段为 `Status: "todo"`、`Priority: "none"`、`Title: aurora.IssueTitle(entry.Name, req.Prompt)`、`Description: pgtype.Text{String: req.Prompt, Valid: true}`、`AssigneeType: "agent"`、`AssigneeID: agent.ID`、`CreatorType: "member"`、`CreatorID: userUUID`、`AttachmentIDs: attachmentIDs`、`LabelIDs: []pgtype.UUID{labelID}`，以及 `IssueCreateOpts{SkipAssignedAgentEnqueue: true}`。出错则 `markGenerationFailed` 并返回 500。
4. `Credit.Reserve` 仍按现在的做法，`amountMicro := int64(entry.Credits) * microCreditsPerCredit`，reference 是 `uuidToString(row.ID)`。`ErrInsufficientCredits` 时调用 `abandonAuroraIssue`，`markGenerationFailed(..., "insufficient credits")`，返回 402。其他预扣错误：放弃 issue，标失败，返回 500。
5. `EnqueueAuroraIssueTask(ctx, issue, uuidToString(row.ID), req.Prompt, uuidStrings(attachmentIDs))`。出错则放弃 issue，`failGenerationAndRefund`，返回 500。
6. `UpdateAuroraGenerationBound` 写入 task id、`amountMicro`、issue id、workspace id。出错则先 `CancelTaskWithReason(ctx, task.ID, "failed to record task", "agent_error")`，再放弃 issue，`failGenerationAndRefund`，返回 500。先取消再退款是因为生成行还没有 `task_id`，结算不能再退一次。
7. 用 201 返回原有字段，再加上：

```go
IssueNumber:     issue.Number,
IssueIdentifier: service.IssueIdentifier(h.getIssuePrefix(r.Context(), workspaceID), issue.Number),
FileCount:       updated.FileCount,
```

这三个字段加到 `AuroraGenerationResponse`，也加到 `AuroraGenerationSummaryResponse` / `generationSummary`。线上不需要 `IssueID`，需要的是 `issueIdentifier`。`Handler` 上已有 `getIssuePrefix`。

`abandonAuroraIssue` 在本任务被调用，所以定义在这里。任务 3 只补锁住它的测试。

```go
func (h *Handler) abandonAuroraIssue(ctx context.Context, issue db.Issue) {
    if !issue.ID.Valid {
        return
    }
    if _, err := h.Queries.UpdateIssue(ctx, db.UpdateIssueParams{
        ID:     issue.ID,
        Status: pgtype.Text{String: "cancelled", Valid: true},
        // AssigneeType and AssigneeID stay unset. UpdateIssue treats a null
        // narg as the new value, which clears the assignee. A cancelled issue
        // with no assignee does not take the comment assignee fallback.
    }); err != nil {
        slog.Warn("aurora issue abandon failed", "issue_id", uuidToString(issue.ID), "error", err)
    }
}
```

对照生成的 `UpdateIssueParams`，确认省略的 assignee narg 编码为 NULL，并且 SQL 把这个 NULL 写成新值。`issue.sql` 里 `sqlc.narg('assignee_type')` 就是下一个值，不是 `COALESCE`。如果 sqlc 把这些字段做成指针，就留 nil。不要传入旧的 assignee。

`ensureAuroraLabel`:

```go
func (h *Handler) ensureAuroraLabel(ctx context.Context, workspaceID pgtype.UUID) (pgtype.UUID, error) {
    row, err := h.Queries.GetIssueLabelByName(ctx, db.GetIssueLabelByNameParams{
        WorkspaceID: workspaceID,
        Name:        "aurora",
    })
    if err == nil {
        return row.ID, nil
    }
    if !errors.Is(err, pgx.ErrNoRows) {
        return pgtype.UUID{}, err
    }
    created, err := h.Queries.CreateLabel(ctx, db.CreateLabelParams{
        WorkspaceID:  workspaceID,
        ResourceType: "issue",
        Name:         "aurora",
        Color:        "#7166f3",
    })
    if err == nil {
        return created.ID, nil
    }
    if !isUniqueViolation(err) {
        return pgtype.UUID{}, err
    }
    row, err = h.Queries.GetIssueLabelByName(ctx, db.GetIssueLabelByNameParams{
        WorkspaceID: workspaceID,
        Name:        "aurora",
    })
    if err != nil {
        return pgtype.UUID{}, err
    }
    return row.ID, nil
}
```

`CreateLabelParams` 跟 sqlc 结构对齐（`Description` 可能是 `pgtype.Text`，留成无效）。

- [ ] **步骤 9: 客户端默认值**

在 `packages/core/aurora/schema.ts` 的 `auroraGenerationSchema` 里加上：

```ts
issueNumber: z.number().int().default(0),
issueIdentifier: z.string().default(""),
fileCount: z.number().int().default(0),
```

在 `packages/core/aurora/types.test.ts` 加一条：没有这些键的旧响应解析成 `0`、`""`、`0`。`e2e/fixtures.ts` 的 `createAuroraGeneration` 保持现有返回类型注解；schema 默认值让 `safeParse` 仍然成功。不要改这个 fixture。

- [ ] **步骤 10: 运行测试**

运行： `(cd server && go test ./internal/aurora/ -run TestIssueTitleUsesDisplayNameAndFortyRunes -count=1 && go test ./internal/handler/ -run 'TestCreateAuroraGeneration' -count=1)`

运行： `pnpm --filter @multica/core test -- types.test.ts`

预期：通过。原有的预扣测试仍然通过，清理会删掉 issue。

- [ ] **步骤 11: 提交**

```bash
git add server/internal/aurora/issue_title.go server/internal/aurora/issue_title_test.go \
  server/pkg/db/queries/agent.sql server/pkg/db/generated server/internal/service/task.go \
  server/internal/service/issue.go server/internal/handler/aurora.go \
  server/internal/handler/aurora_test.go packages/core/aurora/schema.ts \
  packages/core/aurora/types.test.ts
git commit -m "feat(aurora): bind each generation to an issue before enqueue"
```

---

### 任务 3: 入队失败就回滚

**文件：**
- 修改： `server/internal/handler/aurora.go`
- 测试： `server/internal/handler/aurora_test.go`

**Interfaces:**
- 依赖： `UpdateIssue`, `failGenerationAndRefund`, `CancelTaskWithReason`.
- 依赖： `abandonAuroraIssue` from Task 2. This task does not add a second function.
- 产出：入队失败的测试。没有新函数。

- [ ] **步骤 1: 写会失败的测试**

`TestCreateAuroraGenerationEnqueueFailureAbandonsIssue`. Force `EnqueueAuroraIssueTask` to fail by archiving the system agent after `ensureAuroraSystemAgents` and before the handler's enqueue. The clean seam is a handler-level test that uses a workspace whose Aurora agent has no runtime: call `ensureAuroraSystemAgents`, then `UPDATE agent SET runtime_id = NULL WHERE system_key = 'aurora:xhs-image' AND workspace_id = $1`, then POST. Reserve still succeeds because it happens before enqueue. Assert:

- HTTP 500
- generation `status = 'failed'`
- ledger has a `refund:` row for the generation id and no leftover deduction beyond the refunded reserve (balance equals the pre-create balance)
- issue `status = 'cancelled'` and `assignee_id IS NULL`
- `agent_task_queue` has no row for that issue
- generation `issue_id` is null (the bind write never happened)

If nulling `runtime_id` fails the agent load earlier than enqueue, instead replace `TaskService` on the test handler with a stub whose `EnqueueAuroraIssueTask` returns an error. Do not stub `Reserve`. The stub type must embed the real `*service.TaskService` or the handler field type must already be an interface. If it is a concrete `*service.TaskService`, do not introduce an interface. Use the `runtime_id` setup, and if that fails inside `IssueService.Create` rather than enqueue, set `runtime_id` back for create and install a failing enqueue by closing the agent's runtime after create is not possible from outside. In that case add an exported test hook only if one already exists. Preferred path: the `runtime_id = NULL` update is done after create would have loaded the agent, so do it by wrapping is not available. Inspect `EnqueueTaskForIssue`: it loads the agent again and returns `"agent has no runtime"`. Create's `shouldEnqueue` is skipped. So null the runtime after `ensureAuroraSystemAgents` would also fail Create's assignee readiness only if Create checks readiness. `maybeEnqueueOnAssign` is skipped, and `Create` itself does not require a runtime. Null the runtime before the POST. Create still inserts the assigned issue. Enqueue then fails. That is the test setup.

- [ ] **步骤 2: 运行**

运行： `(cd server && go test ./internal/handler/ -run TestCreateAuroraGenerationEnqueueFailureAbandonsIssue -count=1)`

预期：失败。issue 仍是 `todo` 且还指派着 agent，或者预扣没有退回。

- [ ] **步骤 3: 确认任务 2 的回滚接在入队失败这条路上**

`CreateAuroraGeneration` 在 `EnqueueAuroraIssueTask` 返回错误时已经调用 `abandonAuroraIssue` 和 `failGenerationAndRefund`。不要再写第二个放弃函数。如果测试失败是因为入队失败后 assignee 还在，那就是这条错误分支没调用 `abandonAuroraIssue`。接上这个调用。不要改 `UpdateIssue` 的语义。

- [ ] **步骤 4: 运行测试**

运行： `(cd server && go test ./internal/handler/ -run 'TestCreateAuroraGeneration(EnqueueFailure|BindsIssue|Reserves)' -count=1)`

预期： PASS.

- [ ] **步骤 5: 提交**

```bash
git add server/internal/handler/aurora.go server/internal/handler/aurora_test.go
git commit -m "fix(aurora): abandon the issue and refund when enqueue fails"
```

---

### 任务 4: claim 仍带技能文档、issue id 和附件

**文件：**
- 修改： `server/internal/handler/daemon.go`
- 修改： `server/internal/daemon/prompt.go`
- 修改： `server/internal/daemon/prompt_test.go`
- 测试： `server/internal/handler/daemon_aurora_claim_test.go`

**Interfaces:**
- 依赖： `AuroraIssueContextType`, `task.IssueID`, `Agent.SystemKey` prefix `aurora:`, `ensureTaskSkillBundles` (unchanged).
- 产出： `func applyAuroraIssueClaim(resp *AgentTaskResponse, task db.AgentTaskQueue) bool`.

- [ ] **步骤 1: 写会失败的提示词测试**

改 `server/internal/daemon/prompt_test.go` 里的 `TestBuildAuroraPromptPointsAtTheSkillDocument`。把 `IssueID` 设为 `cccccccc-cccc-4ccc-8ccc-cccccccccccc`。删掉期望字符串 `"do not create, update, or comment on issues"`。改为期望这些字符串：

```go
"Issue id: `cccccccc-cccc-4ccc-8ccc-cccccccccccc`"
"multica issue comment add cccccccc-cccc-4ccc-8ccc-cccccccccccc"
"Do not write an artifact manifest."
"Do not store a file."
"Do not create another issue."
```

加上 `TestBuildAuroraPromptIncludesFollowUpComment`：

```go
func TestBuildAuroraPromptIncludesFollowUpComment(t *testing.T) {
    out := BuildPrompt(Task{
        ID:                    "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
        IssueID:               "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
        GenerationID:          "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
        Agent:                 &AgentData{SystemKey: "aurora:poster"},
        QuickCreatePrompt:     "original prompt",
        TriggerCommentID:      "dddddddd-dddd-4ddd-8ddd-dddddddddddd",
        TriggerCommentContent: "use a blue background",
    }, "claude")
    for _, want := range []string{"use a blue background", "Issue id: `cccccccc-cccc-4ccc-8ccc-cccccccccccc`"} {
        if !strings.Contains(out, want) {
            t.Errorf("prompt missing %q\n%s", want, out)
        }
    }
    if strings.Contains(out, "do not create, update, or comment") {
        t.Fatal("prompt still forbids the issue comment")
    }
}
```

- [ ] **步骤 2: 运行提示词测试**

运行： `(cd server && go test ./internal/daemon/ -run 'TestBuildAuroraPrompt' -count=1)`

预期：失败，缺了删掉的那句，也缺 issue id。

- [ ] **步骤 3: Rewrite `buildAuroraPrompt`**

换掉开头和硬规则。保留技能文档那句、输出目录那句、提示词引用和附件行。函数体变成：

```go
func buildAuroraPrompt(task Task) string {
    skillID, _ := auroraSkillID(task)
    var b strings.Builder
    b.WriteString("You are running as an Aurora creation agent for a Multica workspace. This run is one round of one generation, on the issue named below. Do not create another issue.\n\n")
    fmt.Fprintf(&b, "Skill: `%s`\n\n", skillID)
    b.WriteString("Your working directory carries this skill's document in the project skills directory (for Claude: `.claude/skills/`). Read it and follow its steps: it names the inputs, the commands or provider calls to use, the artifact manifest to write, and the failure rules.\n\n")
    if task.IssueID != "" {
        fmt.Fprintf(&b, "Issue id: `%s`\n", task.IssueID)
    }
    fmt.Fprintf(&b, "Task id: `%s`\n", task.ID)
    if task.GenerationID != "" {
        fmt.Fprintf(&b, "Generation id: `%s`\n", task.GenerationID)
    }
    fmt.Fprintf(&b, "Write this run's artifacts under `%s`, and its manifest at `%s`, only when the skill document says to store a file. The manifest must name this task id and skill id.\n\n",
        auroraSandboxOutputRoot, auroraSandboxOutputRoot+"/"+auroraManifestRelativePath)
    if prompt := strings.TrimSpace(task.QuickCreatePrompt); prompt != "" {
        b.WriteString("The user's generation prompt:\n\n")
        fmt.Fprintf(&b, "> %s\n\n", strings.ReplaceAll(prompt, "\n", "\n> "))
    }
    if task.TriggerCommentContent != "" {
        b.WriteString("The user's latest comment is the instruction for this round:\n\n")
        fmt.Fprintf(&b, "> %s\n\n", strings.ReplaceAll(task.TriggerCommentContent, "\n", "\n> "))
    }
    if len(task.QuickCreateAttachmentIDs) > 0 {
        fmt.Fprintf(&b, "Attachments for this run: %s\n", strings.Join(task.QuickCreateAttachmentIDs, ", "))
        b.WriteString("Fetch each one with `multica attachment download <id>`, which writes it locally and prints the path.\n\n")
    } else {
        b.WriteString("This run has no attachments.\n\n")
    }
    b.WriteString("If a required input is missing: post one comment on this issue with `multica issue comment add ")
    b.WriteString(task.IssueID)
    b.WriteString(" --content-file ./question.md`, naming the question, then stop. Do not write an artifact manifest. Do not store a file. Storing a file increases the file count and charges the user.\n\n")
    b.WriteString("Hard rules:\n")
    b.WriteString("- Do the work yourself with the tools you have; you are the only execution path.\n")
    b.WriteString("- Do not create another issue.\n")
    b.WriteString("- When a required input is missing, comment the question and stop. Do not write an artifact manifest. Do not store a file.\n")
    b.WriteString("- Otherwise write the artifacts and their manifest exactly as the skill document specifies.\n")
    b.WriteString("- Never submit a second billable provider request for this run. If a provider call fails, stop and report its message.\n")
    b.WriteString("- When you are done, print one short line naming the produced artifact and stop.\n")
    return b.String()
}
```

`isAuroraTask` 仍放在 `buildPromptBody` 里 quick-create 分支前面。不要把 Aurora task 送进 `buildCommentPrompt` 或普通 issue 简报。

- [ ] **步骤 4: 写会失败的 claim 测试**

`server/internal/handler/daemon_aurora_claim_test.go`, in package `handler_test` if that is where `daemon_test.go` lives; otherwise the handler test package that can construct `db.AgentTaskQueue`. Test the extracted function, not the full HTTP claim:

```go
func TestApplyAuroraIssueClaimCopiesPromptAndIssue(t *testing.T) {
    task := db.AgentTaskQueue{
        IssueID: pgtype.UUID{Valid: true},
        Context: []byte(`{"type":"aurora_issue","generation_id":"gen-1","prompt":"a poster","attachment_ids":["att-1"]}`),
    }
    resp := AgentTaskResponse{IssueID: "issue-1"}
    if !applyAuroraIssueClaim(&resp, task) {
        t.Fatal("expected aurora issue context")
    }
    if resp.GenerationID != "gen-1" || resp.QuickCreatePrompt != "a poster" || len(resp.QuickCreateAttachmentIDs) != 1 {
        t.Fatalf("claim = %+v", resp)
    }
    if resp.IssueID != "issue-1" {
        t.Fatal("issue id was cleared")
    }
}
```

第二个用例：已绑定 issue 的 task 如果上下文类型是 `quick_create`，返回 false，且不拷贝。没有 issue 的旧 quick-create 分支必须留着。

- [ ] **步骤 5: 运行 claim 测试**

运行： `(cd server && go test ./internal/handler/ -run TestApplyAuroraIssueClaimCopiesPromptAndIssue -count=1)`

预期：失败，函数未定义。

- [ ] **步骤 6: 实现 claim 拷贝**

In `server/internal/handler/daemon.go`:

```go
func applyAuroraIssueClaim(resp *AgentTaskResponse, task db.AgentTaskQueue) bool {
    if len(task.Context) == 0 {
        return false
    }
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

在 claim 组装器里、`taskToResponse` 之后调用它，包括已经有 `IssueID` 的 task。不要放进 `!task.IssueID.Valid` 的 quick-create 分支。如果它返回 true 且 `resp.GenerationID` 为空，保留现有的 `GetAuroraGenerationByTaskID` 宽限路径，让刚入队、行还没写上的情况仍能补上 id。技能包仍由 `ensureTaskSkillBundles` 负责，不要把 workflow markdown 嵌进 claim。

- [ ] **步骤 7: 运行测试**

运行： `(cd server && go test ./internal/daemon/ -run 'TestBuildAuroraPrompt' -count=1 && go test ./internal/handler/ -run TestApplyAuroraIssueClaimCopiesPromptAndIssue -count=1)`

预期： PASS.

第 1 阶段门：任务 2 的创建测试和这个 claim 测试合在一起，就是规格的第一道验收。两者都通过之前，不开始任务 5。

- [ ] **步骤 8: 提交**

```bash
git add server/internal/handler/daemon.go server/internal/handler/daemon_aurora_claim_test.go \
  server/internal/daemon/prompt.go server/internal/daemon/prompt_test.go
git commit -m "feat(aurora): carry the skill brief and issue id on claim"
```

---

### 任务 5: 每个暂存文件只计一次、只扣一次

**文件：**
- 修改： `server/internal/aurora/credit.go`
- 修改： `server/internal/handler/aurora_artifact_upload.go`
- 测试： `server/internal/aurora/credit_file_test.go`
- 测试： `server/internal/handler/aurora_artifact_upload_test.go`

**Interfaces:**
- 依赖： `LockAuroraArtifactStaging`, `LockAuroraGeneration`, `IncrementAuroraGenerationFileCount`, `MarkAuroraArtifactStagingBilled`, `DeductCreditBalanceAllowOverdraft`, `InsertCreditLedger`, `EnsureCreditBalance`.
- 产出： `func (s *CreditService) ChargeStagedFile(ctx context.Context, qtx *db.Queries, userID, workspaceID, generationID, stagingID pgtype.UUID, unitMicro int64) error`.

- [ ] **步骤 1: 写会失败的扣款测试**

`server/internal/aurora/credit_file_test.go` uses the handler test DB helpers if they are in package `handler_test`. If `CreditService` tests already live in `server/internal/aurora`, put the test next to them and use that package's DB fixture. The behavior:

1. Generation with `credits_reserved = 76_000_000`, `file_count = 0`, `credits_charged = 0`, balance already reduced by the reserve.
2. `ChargeStagedFile` for staging A, unit `76_000_000`. Balance unchanged. `file_count = 1`, `credits_charged = 76_000_000`. No ledger row `file:`+A.
3. Call it again for staging A. `file_count` stays 1. Balance unchanged.
4. `ChargeStagedFile` for staging B with balance `0`. `file_count = 2`, `credits_charged = 152_000_000`, balance negative by `76_000_000`, one ledger row `file:`+B, kind `deduction`, amount `-76000000`.
5. Replay staging B. No second ledger row, `file_count` stays 2.

扣款路径不要调用 `Reserve`。测的不是函数源码，而是余额可以变成负数。`Reserve` 做不到这一点。

- [ ] **步骤 2: 运行**

运行： `(cd server && go test ./internal/aurora/ -run TestChargeStagedFileConsumesReserveThenOverdrafts -count=1)`

预期：失败，方法未定义。

- [ ] **步骤 3: 实现 `ChargeStagedFile`**

```go
func (s *CreditService) ChargeStagedFile(ctx context.Context, qtx *db.Queries, userID, workspaceID, generationID, stagingID pgtype.UUID, unitMicro int64) error {
    if unitMicro <= 0 {
        return fmt.Errorf("file charge unit must be positive, got %d", unitMicro)
    }
    staging, err := qtx.LockAuroraArtifactStaging(ctx, stagingID)
    if err != nil {
        return err
    }
    if staging.BilledAt.Valid {
        return nil
    }
    gen, err := qtx.LockAuroraGeneration(ctx, db.LockAuroraGenerationParams{
        ID: generationID, WorkspaceID: workspaceID,
    })
    if err != nil {
        return err
    }
    unmatched := gen.FileCount == 0 && gen.CreditsReserved > 0
    if !unmatched {
        if err := qtx.EnsureCreditBalance(ctx, userID); err != nil {
            return err
        }
        balanceAfter, err := qtx.DeductCreditBalanceAllowOverdraft(ctx, db.DeductCreditBalanceAllowOverdraftParams{
            UserID: userID, AmountMicro: unitMicro,
        })
        if err != nil {
            return err
        }
        key := "file:" + util.UUIDToString(stagingID)
        if _, err := qtx.InsertCreditLedger(ctx, db.InsertCreditLedgerParams{
            UserID: userID, WorkspaceID: workspaceID,
            Kind: LedgerKindDeduction, AmountMicro: -unitMicro,
            BalanceAfterMicro: balanceAfter,
            Reference: util.UUIDToString(generationID),
            IdempotencyKey: key,
        }); err != nil {
            return err
        }
    }
    if _, err := qtx.IncrementAuroraGenerationFileCount(ctx, db.IncrementAuroraGenerationFileCountParams{
        ID: generationID, WorkspaceID: workspaceID, UnitMicro: unitMicro,
    }); err != nil {
        return err
    }
    marked, err := qtx.MarkAuroraArtifactStagingBilled(ctx, stagingID)
    if err != nil {
        return err
    }
    if marked == 0 {
        return fmt.Errorf("staging %s was billed concurrently", util.UUIDToString(stagingID))
    }
    return nil
}
```

加锁顺序是暂存行，然后生成，然后余额。不要先锁生成再锁暂存行。

- [ ] **步骤 4: 从暂存插入处调用它**

把 `insertAuroraArtifactStaging` 改成先 `h.TxStarter.Begin`，再用 `h.Queries.WithTx(tx)` 插入，然后：

```go
unit := int64(entry.Credits) * microCreditsPerCredit
entry, ok := aurora.Lookup(scope.generation.SkillID)
if !ok {
    return db.AuroraArtifactStaging{}, fmt.Errorf("unknown skill %q", scope.generation.SkillID)
}
if err := h.Credit.ChargeStagedFile(ctx, qtx, scope.generation.UserID, scope.workspaceID, scope.generation.ID, stagingID, unit); err != nil {
    return db.AuroraArtifactStaging{}, err
}
if err := tx.Commit(ctx); err != nil {
    return db.AuroraArtifactStaging{}, err
}
```

上传和供应商导入都走这个函数，所以都会扣款。唯一约束冲突在提交前返回，所以同一个 manifest artifact id 的重放不会再扣。调用方出错时仍会 `deleteAuroraArtifactObject`，因为事务已经把行回滚了。

- [ ] **步骤 5: 运行测试**

运行： `(cd server && go test ./internal/aurora/ -run TestChargeStagedFileConsumesReserveThenOverdrafts -count=1 && go test ./internal/handler/ -run 'TestAuroraArtifact' -count=1)`

预期：通过。已有的上传测试仍然通过。如果它们在没有积分服务时断言暂存行，就给测试 handler 接上积分服务。不要在生产路径上加 `h.Credit == nil` 就跳过扣款。

- [ ] **步骤 6: 提交**

```bash
git add server/internal/aurora/credit.go server/internal/aurora/credit_file_test.go \
  server/internal/handler/aurora_artifact_upload.go server/internal/handler/aurora_artifact_upload_test.go
git commit -m "feat(aurora): charge each staged file once, overdraft after the reserve"
```

---

### 任务 6: 拒绝不改变文件数

**文件：**
- 测试： `server/internal/handler/aurora_artifact_test.go`

**Interfaces:**
- 依赖： `file_count` from Task 5, `DeleteAuroraAsset` / the existing hide-or-reject path in `aurora_artifact.go`.
- 产出：没有新函数。这是回归锁。不要加减少文件数的代码。

- [ ] **步骤 1：如果已有减少文件数的代码，先写会失败的测试；否则写锁住现状的测试**

`TestRejectingAuroraAssetDoesNotChangeFileCount`. Stage one file through the upload helper the existing artifact tests use, so `file_count` is 1. Then call the existing asset reject or `DeleteAuroraAsset` path that removes it from works. Read `file_count` again. Want `1`. Also assert a staging row whose status is not committed still leaves `file_count` at 1. If the current reject path already leaves the count alone, the test passes on first run; keep it. It is the spec lock, not a placeholder.

- [ ] **步骤 2: 运行**

运行： `(cd server && go test ./internal/handler/ -run TestRejectingAuroraAssetDoesNotChangeFileCount -count=1)`

预期：如果现在没有减少文件数的代码，就通过。如果失败，删掉减少文件数的代码。不要把测试改成期望更小的数。

- [ ] **步骤 3: 提交**

```bash
git add server/internal/handler/aurora_artifact_test.go
git commit -m "test(aurora): rejecting an asset does not change file count"
```

第 2 阶段门：任务 5 和任务 6 的测试通过。通过之前不开始任务 7。

---

### 任务 7: 任务结束只改状态

**文件：**
- 修改： `server/internal/service/aurora_completion.go`
- 测试： `server/internal/service/aurora_completion_test.go` (create it if the package has no settlement test; otherwise extend the existing file)

**Interfaces:**
- 依赖：`UpdateAuroraGenerationRoundStatus`。manifest 是否写过，用 `ListAuroraAssets` 判断。只有匹配的 manifest 提交成作品之后才会有资产行，所以资产列表为空就是没写 manifest。不要解析评论。
- 产出：除了字符串 `needs_input`，不加新的状态常量。`auroraTerminal` 仍只有 `completed` 和 `failed`。

- [ ] **步骤 1: 写会失败的测试s**

三个用例，起点都是：生成的 `task_id` 就是正在结束的 task，状态是 `queued`：

1. No `aurora_asset` row. After `settleAuroraOnCompleted`, status is `needs_input`, `needs_input_at` is set, `file_count` is unchanged, balance is unchanged, no `refund:` ledger row.
2. One `aurora_asset` row. Status is `completed`, `needs_input_at` is null, `credits_charged` is unchanged (not overwritten with `credits_reserved`).
3. Generation `task_id` points at a different task. `settleAuroraOnCompleted` and `settleAuroraOnFailed` leave status and balance untouched.

第四个用例：状态已经是 `needs_input`，同一个 task 再结算一次。状态保持 `needs_input`。`WHERE status IN ('queued', 'running')` 使这次更新变成空操作。

- [ ] **步骤 2: 运行这些测试**

运行： `(cd server && go test ./internal/service/ -run 'TestSettleAuroraIssueRound' -count=1)`

预期：失败。 Today no assets refunds, and assets set `credits_charged = credits_reserved`.

- [ ] **步骤 3: 给结算加分支**

在 `settleAuroraOnCompleted` 和 `settleAuroraOnFailed` 里，`settleableAuroraGeneration` 之后：

```go
if gen.IssueID.Valid {
    s.settleAuroraIssueRound(ctx, gen, task, manifestReported)
    return
}
```

完成路径上，`manifestReported` 就是 `len(assets) > 0`。失败路径也要加载作品；有作品就当成写了 manifest（`completed`），否则是 `needs_input`。任务失败的字符串不是退款理由。两条已绑定 issue 的路径都不要调用 `Credit.Refund`。

```go
func (s *TaskService) settleAuroraIssueRound(ctx context.Context, gen db.AuroraGeneration, task db.AgentTaskQueue, manifestReported bool) {
    if gen.TaskID != task.ID {
        return
    }
    status := "needs_input"
    var needsInputAt pgtype.Timestamptz
    if manifestReported {
        status = auroraStatusCompleted
    } else {
        needsInputAt = pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
    }
    rows, err := s.Queries.UpdateAuroraGenerationRoundStatus(ctx, db.UpdateAuroraGenerationRoundStatusParams{
        ID: gen.ID, Status: status, Error: pgtype.Text{},
        NeedsInputAt: needsInputAt, WorkspaceID: gen.WorkspaceID, TaskID: task.ID,
    })
    if err != nil || rows == 0 {
        slog.Warn("aurora issue round settle skipped", "generation_id", util.UUIDToString(gen.ID), "error", err)
    }
}
```

没有 issue 的分支（`!gen.IssueID.Valid`）仍走旧的扣整份或退款路径，这样这次改动之前的行还能结算。新创建的生成一旦入队成功就一定有 issue。

`settleableAuroraGeneration` 必须把 `needs_input` 当成不可再结算。改成要求 `gen.Status == "queued" || gen.Status == "running"`，不要再用 `!auroraTerminal`。不要把 `needs_input` 加进 `auroraTerminal`。

- [ ] **步骤 4: 运行测试**

运行： `(cd server && go test ./internal/service/ -run 'TestSettleAurora' -count=1)`

预期： PASS.

- [ ] **步骤 5: 提交**

```bash
git add server/internal/service/aurora_completion.go server/internal/service/aurora_completion_test.go
git commit -m "feat(aurora): end an issue round as completed or needs-input"
```

---

### 任务 8: 只从 needs-input 续跑

**文件：**
- 修改： `server/internal/handler/comment.go`
- 修改： `packages/views/issues/components/comment-input.tsx`
- 修改： `packages/views/issues/components/issue-detail.tsx`
- 修改： `packages/views/locales/{en,zh-Hans,ja,ko,fr}/issues.json`
- 测试： `server/internal/handler/aurora_comment_round_test.go`
- 测试： `packages/views/issues/components/comment-composers.test.tsx`

**Interfaces:**
- 依赖： `GetAuroraGenerationByIssueID`, `AdvanceAuroraGenerationRound`, `EnqueueAuroraIssueTask`, `ListAttachmentsByComment`, `Moderation.ScreenPrompt`, `CancelTaskWithReason`.
- 产出： `func (h *Handler) auroraCommentGate(ctx context.Context, issue db.Issue) (string, db.AuroraGeneration, error)` returning `""` (not an Aurora issue), `"enqueue"`, or `"save"`. `CommentInput` gains `submitDisabled: boolean` and `disabledReason: string`.

- [ ] **步骤 1: 写会失败的服务端测试**

`TestAuroraNeedsInputCommentEnqueuesWithoutReserve`: generation status `needs_input`, `credits_reserved` already set, one existing reserve ledger row. Post a member comment with no mention. Assert one new task, `task_id` updated, status `queued`, `needs_input_at` null, no second `reserve:` row, balance unchanged.

`TestAuroraRunningCommentDoesNotEnqueue`: status `queued`. Comment row exists. No new task.

`TestAuroraCompletedCommentDoesNotEnqueue`: status `completed`. No new task.

`TestAuroraFollowUpModerationSkipsEnqueue`: `ScreenPrompt` returns not allowed. Comment row exists. Status stays `needs_input`. `file_count` and reserve unchanged.

用 `testutil.Call` 打现有的创建评论 handler。门闩必须从 `triggerTasksForComment` 走，那个 handler 已经会调用它。

- [ ] **步骤 2: 运行这些测试**

运行： `(cd server && go test ./internal/handler/ -run 'TestAurora(NeedsInput|Running|Completed|FollowUp)' -count=1)`

预期：失败。 A `needs_input` comment either does not enqueue or enqueues a plain issue task with no Aurora context.

- [ ] **步骤 3: 实现门闩**

```go
func (h *Handler) auroraCommentGate(ctx context.Context, issue db.Issue) (string, db.AuroraGeneration, error) {
    gen, err := h.Queries.GetAuroraGenerationByIssueID(ctx, db.GetAuroraGenerationByIssueIDParams{
        IssueID: issue.ID, WorkspaceID: issue.WorkspaceID,
    })
    if errors.Is(err, pgx.ErrNoRows) {
        return "", db.AuroraGeneration{}, nil
    }
    if err != nil {
        return "", db.AuroraGeneration{}, err
    }
    switch gen.Status {
    case "needs_input":
        return "enqueue", gen, nil
    default:
        return "save", gen, nil
    }
}
```

在 `triggerTasksForComment` 开头、`computeCommentAgentTriggers` 之前：

```go
gate, gen, err := h.auroraCommentGate(ctx, issue)
if err != nil {
    slog.Warn("aurora comment gate failed", "issue_id", uuidToString(issue.ID), "error", err)
    return nil
}
if gate == "save" {
    return nil
}
if gate == "enqueue" {
    decision, err := h.Moderation.ScreenPrompt(ctx, comment.Content)
    if err != nil || !decision.Allowed {
        reason := "moderation rejected"
        if err != nil {
            reason = err.Error()
        } else {
            reason = decision.Reason
        }
        h.recordModerationBlock(ctx, gen.ID, issue.WorkspaceID, aurora.ModerationScopePrompt, reason)
        return nil
    }
    attachments, err := h.Queries.ListAttachmentsByComment(ctx, comment.ID)
    if err != nil {
        slog.Warn("aurora follow-up attachments failed", "error", err)
        return nil
    }
    ids := make([]string, 0, len(attachments))
    for _, row := range attachments {
        ids = append(ids, uuidToString(row.ID))
    }
    task, err := h.TaskService.EnqueueAuroraIssueTask(ctx, issue, uuidToString(gen.ID), comment.Content, ids)
    if err != nil {
        slog.Warn("aurora follow-up enqueue failed", "error", err)
        return nil
    }
    advanced, err := h.Queries.AdvanceAuroraGenerationRound(ctx, db.AdvanceAuroraGenerationRoundParams{
        ID: gen.ID, TaskID: task.ID, WorkspaceID: issue.WorkspaceID,
    })
    if err != nil || advanced == 0 {
        _, _ = h.TaskService.CancelTaskWithReason(ctx, task.ID, "generation round was no longer waiting", "agent_error")
        return nil
    }
    return []CommentTriggerOutcome{{ /* the existing outcome shape for one enqueued assignee task; copy the fields enqueueSingleCommentTrigger already fills */ }}
}
```

读 `CommentTriggerOutcome`，填上 `enqueueSingleCommentTrigger` 给 `issue_assignee` 填的那些字段。这条路径不要再调用 `computeCommentAgentTriggers`。提及别的 agent 不能并行开一轮。预览必须一致：`PreviewCommentTriggers` 开头，门闩是 `"save"` 或查询出错时返回空触发列表；门闩是 `"enqueue"` 时返回现有 assignee 回退会给出的预览，但不要入队。

- [ ] **步骤 4: 写会失败的评论框测试**

In `comment-composers.test.tsx`, render `CommentInput` with `submitDisabled` and `disabledReason="This round is still running. The next round can start after it stops."`. The submit button is disabled. The reason text is visible. A test without the prop still submits.

- [ ] **步骤 5: 运行评论框测试**

运行： `pnpm --filter @multica/views test -- comment-composers.test.tsx`

预期：失败，属性不存在。

- [ ] **步骤 6: 只在 queued 或 running 时禁止发送**

`CommentInput` 把 `disabled={submitDisabled}` 传给 `SubmitButton`。`submitDisabled` 为真时用 `<p>` 显示 `disabledReason`。不要清掉草稿。

`IssueDetail` 只在有 issue id 时用工作区范围的查询加载生成。加 `GET` handler `GetAuroraGenerationByIssue`，路径 `/api/aurora/issues/{issueId}/generation`，返回 `generationSummary` 或 404。注册在其他 Aurora 路由旁边。客户端是 `getAuroraGenerationByIssue` 加 `auroraGenerationByIssueOptions`，用现有 schema 做 Zod 解析。`IssueDetail` 用 `enabled: Boolean(issue?.id)` 调用它。

`generation.status` 是 `queued` 或 `running` 时，传入 `submitDisabled` 和对应语言的句子。其他状态评论框仍可发送。服务端对 `completed` 和 `failed` 仍然不入队。

在每个 `packages/views/locales/*/issues.json` 的 `detail` 下加上：

| Locale | `aurora_round_locked` |
| --- | --- |
| en | This round is still running. The next round can start after it stops. |
| zh-Hans | 这一轮还在进行。停住之后才能开始下一轮。 |
| ja | このラウンドはまだ実行中です。止まってから次のラウンドを始められます。 |
| ko | 이 라운드가 아직 실행 중입니다. 멈춘 뒤에 다음 라운드를 시작할 수 있습니다. |
| fr | Ce tour est encore en cours. Le tour suivant peut commencer une fois qu'il s'arrête. |

- [ ] **步骤 7: 运行测试**

运行： `(cd server && go test ./internal/handler/ -run 'TestAurora(NeedsInput|Running|Completed|FollowUp)' -count=1)`

运行： `pnpm --filter @multica/views test -- comment-composers.test.tsx`

预期： PASS.

- [ ] **步骤 8: 提交**

```bash
git add server/internal/handler/comment.go server/internal/handler/aurora_comment_round_test.go \
  server/internal/handler/aurora.go packages/core/aurora \
  packages/views/issues/components/comment-input.tsx \
  packages/views/issues/components/issue-detail.tsx \
  packages/views/issues/components/comment-composers.test.tsx \
  packages/views/locales/en/issues.json packages/views/locales/zh-Hans/issues.json \
  packages/views/locales/ja/issues.json packages/views/locales/ko/issues.json \
  packages/views/locales/fr/issues.json
git commit -m "feat(aurora): continue a generation only from needs-input"
```

`git add` 的路径改成实际改过的文件。不要加无关的语言文件。

---

### 任务 9: 7 天后只退尚未对上文件的预扣

**文件：**
- 新建： `server/internal/service/aurora_timeout.go`
- 新建： `server/internal/service/aurora_timeout_test.go`
- 修改： `server/cmd/server/runtime_sweeper.go`

**Interfaces:**
- 依赖： `ListAuroraGenerationsAwaitingInput`, `Credit.Refund`, `CreateComment`, `UpdateAuroraGenerationTerminal` only if a new query is unnecessary. Do not call `markGenerationFailed`: it writes `credits_charged = 0`.
- 产出： `func (s *TaskService) ExpireAuroraNeedsInput(ctx context.Context, before time.Time, limit int32) (int, error)`.

- [ ] **步骤 1: 写会失败的测试s**

1. `needs_input_at` nine days ago, `file_count = 0`, `credits_reserved > 0`. After `ExpireAuroraNeedsInput(ctx, time.Now(), 50)`, status is `failed`, balance includes the refund, one system comment on the issue contains `waited 7 days`, and a later comment does not enqueue (call the Task 8 gate; status `failed` returns `"save"`).
2. Same age, `file_count = 1`. Status `failed`, balance unchanged, comment does not say the reservation was returned, `credits_charged` unchanged.
3. `needs_input_at` one day ago. No change.
4. Calling expire twice does not insert a second refund. `Credit.Refund` is already idempotent on `refund:`+generation id; assert one ledger row.

如果 `UpdateAuroraGenerationTerminal` 不能保持 `credits_charged` 不动，就加一条状态写入查询。优先用新查询：

```sql
-- name: FailAuroraGenerationKeepCharge :execrows
UPDATE aurora_generation
SET status = 'failed',
    error = $2,
    needs_input_at = NULL,
    updated_at = now()
WHERE id = $1 AND workspace_id = $3 AND status = 'needs_input';
```

`credits_charged` 不在 SET 列表里。

- [ ] **步骤 2: 运行测试**

运行： `(cd server && go test ./internal/service/ -run TestExpireAuroraNeedsInput -count=1)`

预期：失败，函数未定义。

- [ ] **步骤 3: 实现清扫**

```go
const auroraNeedsInputTimeout = 7 * 24 * time.Hour

func (s *TaskService) ExpireAuroraNeedsInput(ctx context.Context, now time.Time, limit int32) (int, error) {
    rows, err := s.Queries.ListAuroraGenerationsAwaitingInput(ctx, db.ListAuroraGenerationsAwaitingInputParams{
        Before: now.Add(-auroraNeedsInputTimeout), RowLimit: limit,
    })
    if err != nil {
        return 0, err
    }
    expired := 0
    for _, gen := range rows {
        if gen.FileCount == 0 && gen.CreditsReserved > 0 && s.Credit != nil {
            if err := s.Credit.Refund(ctx, gen.UserID, gen.WorkspaceID, gen.CreditsReserved, util.UUIDToString(gen.ID)); err != nil {
                slog.Warn("aurora timeout refund failed", "generation_id", util.UUIDToString(gen.ID), "error", err)
                continue
            }
        }
        note := "This generation waited 7 days for a reply and has stopped. Stored files stay charged."
        if gen.FileCount == 0 {
            note = "This generation waited 7 days for a reply and has stopped. The unused reservation was returned."
        }
        updated, err := s.Queries.FailAuroraGenerationKeepCharge(ctx, db.FailAuroraGenerationKeepChargeParams{
            ID: gen.ID, Error: pgtype.Text{String: "needs input timed out", Valid: true}, WorkspaceID: gen.WorkspaceID,
        })
        if err != nil || updated == 0 {
            continue
        }
        if gen.IssueID.Valid {
            _, _ = s.Queries.CreateComment(ctx, db.CreateCommentParams{
                ID: dbid.NewV7(), IssueID: gen.IssueID, WorkspaceID: gen.WorkspaceID,
                AuthorType: "system", AuthorID: pgtype.UUID{Valid: true},
                Content: note, Type: "system",
            })
        }
        expired++
    }
    return expired, nil
}
```

在 `runRuntimeSweeper` 里、`sweepAuroraSandboxNodes` 之后，`taskSvc != nil` 时调用 `taskSvc.ExpireAuroraNeedsInput(ctx, time.Now().UTC(), 50)`。计数非零才打日志。30 秒的清扫间隔不变；判断条件是 7 天。

- [ ] **步骤 4: 运行测试**

运行： `(cd server && go test ./internal/service/ -run TestExpireAuroraNeedsInput -count=1)`

预期： PASS.

第 3 阶段门：任务 7、8、9 的测试通过。

- [ ] **步骤 5: 提交**

```bash
git add server/internal/service/aurora_timeout.go server/internal/service/aurora_timeout_test.go \
  server/pkg/db/queries/aurora.sql server/pkg/db/generated server/cmd/server/runtime_sweeper.go
git commit -m "feat(aurora): stop a generation that waits seven days"
```

---

### 任务 10: 再来一版只打开抽屉，不自动提交

**文件：**
- 修改： `packages/views/aurora/generation-composer.tsx`
- 修改： `packages/views/issues/components/issue-detail.tsx`
- 修改： `packages/views/locales/{en,zh-Hans,ja,ko,fr}/issues.json`
- 测试： `packages/views/aurora/generation-composer.test.tsx`
- 测试： `packages/views/issues/components/issue-detail.aurora.test.tsx`

**Interfaces:**
- 依赖： generation `skillId`, `prompt`, `status` from the issue query in Task 8. `GenerationComposer` props.
- 产出：`GenerationComposer` 的 `initialPrompt?: string`。提交仍调用 `useCreateAuroraGeneration`。URL 查询参数里不放提示词。

- [ ] **步骤 1: 写会失败的测试s**

Composer test: render with `initialPrompt="blue poster"` and a mocked create that fails the test if called during mount. The textarea shows `blue poster`. Clicking submit calls create once. No call before the click.

Issue-detail test: generation status `completed` renders a link named `Another version`. Status `needs_input` does not. Clicking the link shows the composer with the generation prompt and does not call create.

- [ ] **步骤 2: 运行这些测试**

运行： `pnpm --filter @multica/views test -- generation-composer.test.tsx issue-detail.aurora.test.tsx`

预期：失败。

- [ ] **步骤 3：实现链接和预填抽屉**

给 `ComposerBody` 加 `initialPrompt?: string`。用 `useState(initialPrompt ?? "")` 初始化。不要把提示词放进 `open`、路由或查询串。

In `IssueDetail`, when `generation?.status === "completed"`, render a button in `trailingActions` (the existing slot) whose accessible name is `t(($) => $.detail.aurora_another_version)`. On click, set local state `{ skillId, prompt }` and render `GenerationComposer` with the catalog skill from `useAuroraSkills()` (`enabled` only when that state is set) and `initialPrompt={prompt}`. `onOpenChange(false)` clears the state. Do not call create in the click handler.

Locale `detail.aurora_another_version`:

| Locale | String |
| --- | --- |
| en | Another version |
| zh-Hans | 再来一版 |
| ja | 別のバージョン |
| ko | 다른 버전 |
| fr | Autre version |

- [ ] **步骤 4: 运行测试**

运行： `pnpm --filter @multica/views test -- generation-composer.test.tsx issue-detail.aurora.test.tsx`

预期： PASS.

- [ ] **步骤 5: 提交**

```bash
git add packages/views/aurora/generation-composer.tsx \
  packages/views/aurora/generation-composer.test.tsx \
  packages/views/issues/components/issue-detail.tsx \
  packages/views/issues/components/issue-detail.aurora.test.tsx \
  packages/views/locales/en/issues.json packages/views/locales/zh-Hans/issues.json \
  packages/views/locales/ja/issues.json packages/views/locales/ko/issues.json \
  packages/views/locales/fr/issues.json
git commit -m "feat(aurora): open another version in the drawer without submitting"
```

---

### 任务 11: 打开 issue：有 web 来源就跳过去，没有就留在 Aurora

**文件：**
- 新建： `packages/core/aurora/issue-link.ts`
- 新建： `packages/core/aurora/issue-link.test.ts`
- 新建： `apps/aurora/lib/issue-link.ts`
- 新建： `apps/aurora/lib/issue-link.test.ts`
- 新建： `apps/aurora/app/[workspaceSlug]/issues/[id]/page.tsx`
- 修改： `packages/views/aurora/generation-composer.tsx`
- 修改： `packages/views/aurora/works-list.tsx`
- 修改： `apps/aurora/app/[workspaceSlug]/works/page.tsx`
- 修改：历史页，以及 `apps/aurora/app/[workspaceSlug]/works/[generationId]/page.tsx`

**Interfaces:**
- 依赖：创建响应里的 `issueIdentifier`。`useCanonicalIssue` 接受 `PREFIX-N`。
- 产出：
  - `export function auroraIssuePath(slug: string, identifier: string): string`
  - `export function auroraIssueHref(slug: string, identifier: string, origin: string | undefined): string`

- [ ] **步骤 1: 写会失败的路径测试**

`packages/core/aurora/issue-link.test.ts` (`// @vitest-environment node`):

```ts
expect(auroraIssuePath("acme", "MUL-42")).toBe("/acme/issues/MUL-42");
expect(auroraIssuePath("a b", "MUL-42")).toBe("/a%20b/issues/MUL-42");
```

`apps/aurora/lib/issue-link.test.ts`:

```ts
expect(auroraIssueHref("acme", "MUL-42", undefined)).toBe("/acme/issues/MUL-42");
expect(auroraIssueHref("acme", "MUL-42", "https://mca.example")).toBe("https://mca.example/acme/issues/MUL-42");
expect(auroraIssueHref("acme", "MUL-42", "https://mca.example/")).toBe("https://mca.example/acme/issues/MUL-42");
```

- [ ] **步骤 2: 运行这些测试**

运行： `pnpm --filter @multica/core test -- issue-link.test.ts && pnpm --filter @multica/aurora test -- issue-link.test.ts`

预期：失败。

- [ ] **步骤 3: 实现路径函数和路由**

```ts
export function auroraIssuePath(slug: string, identifier: string): string {
  return `/${encodeURIComponent(slug)}/issues/${encodeURIComponent(identifier)}`;
}
```

```ts
import { auroraIssuePath } from "@multica/core/aurora/issue-link";

export function auroraIssueHref(slug: string, identifier: string, origin: string | undefined): string {
  const path = auroraIssuePath(slug, identifier);
  const trimmed = origin?.replace(/\/$/, "") ?? "";
  if (!trimmed) return path;
  return `${trimmed}${path}`;
}
```

抽屉在应用层读 `process.env.NEXT_PUBLIC_WEB_ORIGIN`，不在 `packages/core` 里读。把解析好的 href 传进视图，或在 `apps/aurora` 读环境变量再把 `openIssueHref` 传给 `GenerationComposer`。创建成功时 `window.location.assign(href)`。相对地址留在 Aurora。绝对地址去 web 应用。不要探测 cookie。

`apps/aurora/app/[workspaceSlug]/issues/[id]/page.tsx`:

```tsx
"use client";
import { use } from "react";
import { IssueDetailRoute } from "@multica/views/issues/components";

export default function AuroraIssuePage({ params }: { params: Promise<{ id: string }> }) {
  const { id } = use(params);
  return <IssueDetailRoute routeId={id} />;
}
```

不要包 `DashboardLayout`。Aurora 的工作区布局已经设置工作区，并把未登录的人转到登录页。web 来源未设置时，这就是退路。

Works list: add optional `issueHref?: (identifier: string) => string`. When `generation.issueIdentifier` is non-empty and `issueHref` is set, the row links there. Otherwise keep `generationHref(generation.id)` for rows that predate an issue. History and the old `[generationId]` page use the same rule: if the loaded generation has an identifier, `redirect` or an `<a>` to `issueHref`; if not, render the existing detail. Do not drop the old page.

- [ ] **步骤 4: 成功后关掉抽屉**

在 `ComposerBody` 里，创建结果的 `issueIdentifier` 非空时，跳到那个 href，不要开始 `useAuroraGenerationDetail` 轮询。标识为空（更旧的服务端）仍用现在抽屉里的轮询。`onCreated` 由持有工作区 slug 的应用页传入。

- [ ] **步骤 5: 运行测试**

运行： `pnpm --filter @multica/core test -- issue-link.test.ts && pnpm --filter @multica/aurora test -- issue-link.test.ts && pnpm --filter @multica/views test -- generation-composer.test.tsx`

预期： PASS.

- [ ] **步骤 6: 提交**

```bash
git add packages/core/aurora/issue-link.ts packages/core/aurora/issue-link.test.ts \
  apps/aurora/lib/issue-link.ts apps/aurora/lib/issue-link.test.ts \
  apps/aurora/app/\[workspaceSlug\]/issues \
  packages/views/aurora/generation-composer.tsx packages/views/aurora/works-list.tsx \
  apps/aurora/app/\[workspaceSlug\]/works apps/aurora/app/\[workspaceSlug\]/history
git commit -m "feat(aurora): open the generation on its issue"
```

---

### 任务 12: 只给 Aurora 生成的 issue 上皮肤

**文件：**
- 新建： `packages/ui/components/ui/portal-container.tsx`
- 新建： `packages/views/aurora/aurora-issue-skin.css`
- 修改： `packages/ui/components/ui/{dialog,dropdown-menu,sheet,popover,select,tooltip,alert-dialog,context-menu,hover-card}.tsx`
- 修改： `packages/views/issues/components/issue-detail.tsx`
- 修改： `apps/web/app/globals.css`
- 测试： `apps/aurora/app/aurora-theme.test.ts`
- 测试： `packages/views/issues/components/issue-detail.aurora.test.tsx`

**Interfaces:**
- 依赖：`apps/aurora/app/globals.css` 第 59–80 行的 token 赋值（`--background` 到 `--info`）。读那段并抄这些自定义属性。不要另造一套色板。
- 产出： `PortalContainerProvider` and `usePortalContainer()`. Context value is `React.RefObject<HTMLElement | null> | null`.

- [ ] **步骤 1: 写会失败的皮肤测试**

扩展 `apps/aurora/app/aurora-theme.test.ts`（它已经从磁盘读 CSS）：

```ts
const skin = readFileSync(resolve(repoRoot, "packages/views/aurora/aurora-issue-skin.css"), "utf8");
const webGlobals = readFileSync(resolve(repoRoot, "apps/web/app/globals.css"), "utf8");
expect(skin).toContain(".aurora-issue");
expect(skin).toContain("--background: var(--aurora-paper)");
expect(webGlobals).toContain("aurora-issue-skin.css");
expect(webGlobals).not.toMatch(/:root\s*\{[^}]*--background:\s*var\(--aurora-paper\)/);
expect(globals).not.toContain("aurora-issue-skin.css"); // Aurora app globals
```

问题详情测试：有生成的 issue，根节点类名含 `aurora-issue`。生成查询 404 的 issue 没有这个类。

- [ ] **步骤 2: 运行这些测试**

运行： `pnpm --filter @multica/aurora test -- aurora-theme.test.ts && pnpm --filter @multica/views test -- issue-detail.aurora.test.tsx`

预期：失败。

- [ ] **步骤 3: 加上弹出层容器**

`packages/ui/components/ui/portal-container.tsx`:

```tsx
"use client";
import { createContext, useContext, type RefObject } from "react";

const PortalContainerContext = createContext<RefObject<HTMLElement | null> | null>(null);

export function PortalContainerProvider({
  container,
  children,
}: {
  container: RefObject<HTMLElement | null> | null;
  children: React.ReactNode;
}) {
  return <PortalContainerContext.Provider value={container}>{children}</PortalContainerContext.Provider>;
}

export function usePortalContainer(): HTMLElement | undefined {
  return useContext(PortalContainerContext)?.current ?? undefined;
}
```

文件列表里的每个弹出层，把 `container={usePortalContainer()}` 传给原始 `Portal`。hook 返回 undefined 时省略这个属性（`container={usePortalContainer() || undefined}`），这样普通 issue 仍弹到 `document.body`。`ui` 不能引用 `views`。

- [ ] **步骤 4: 把类限定在作用域里**

在 `detailContent` 里现有的根 `<div className="relative flex h-full min-w-0 flex-1 flex-col">` 上加 ref，并在有生成时加上 `aurora-issue`。用 `PortalContainerProvider` 包住这个 div 的子节点：有生成时容器是这个 ref，否则是 `null`。不要多加 DOM 节点。不要改已有类名。

`packages/views/aurora/aurora-issue-skin.css` 只把 Aurora `:root` 的 token 赋值抄到 `.aurora-issue` 下。从 `apps/web/app/globals.css` 引入。不要从 `apps/aurora/app/globals.css` 引入。不要改 `packages/ui/styles/`。

- [ ] **步骤 5: 运行测试**

运行： `pnpm --filter @multica/aurora test -- aurora-theme.test.ts && pnpm --filter @multica/views test -- issue-detail.aurora.test.tsx && pnpm --filter @multica/ui test`

预期：通过。如果 `ui` 没有弹出层测试，views 测试加 CSS 测试就是这道门。

第 4 阶段门：任务 10、11、12 的测试通过。

- [ ] **步骤 6: 提交**

```bash
git add packages/ui/components/ui/portal-container.tsx packages/ui/components/ui \
  packages/views/aurora/aurora-issue-skin.css packages/views/issues/components/issue-detail.tsx \
  packages/views/issues/components/issue-detail.aurora.test.tsx \
  apps/web/app/globals.css apps/aurora/app/aurora-theme.test.ts
git commit -m "feat(aurora): skin only the issue that has a generation"
```

---

### 任务 13: 每份技能文档加同一段缺信息说明

**文件：**
- 修改： all 13 files in `server/internal/aurora/workflows/`
- 修改： `server/internal/aurora/workflows_test.go`

**Interfaces:**
- 依赖：现有五个标题。`TestRewrittenWorkflowsDescribeRealSteps`。
- 产出：没有新的 Go 函数。那段话和指向它的句子就是契约。

这 13 个文件是 `document-summary.md`、`id-photo.md`、`image-edit.md`、`image-video.md`、`poster.md`、`product-image.md`、`resume.md`、`text-image.md`、`text-video.md`、`transcription.md`、`video-captions.md`、`xhs-copy.md`、`xhs-image.md`。

- [ ] **步骤 1: 扩展会失败的测试**

在 `TestRewrittenWorkflowsDescribeRealSteps` 的标题循环之后：

```go
const missingInput = "缺必填信息时，在 claim 给出的 issue 上发表评论，写明要问的问题，然后结束这一轮。不要写 artifact manifest。不要存文件。存文件会使文件数增加并扣费。"
const missingInputPointer = "If a required input is missing, follow Failure behavior: comment the question on the claim's issue and stop without storing a file."
if strings.Count(brief, missingInput) != 1 {
    t.Errorf("skill %q has the missing-input paragraph %d times", id, strings.Count(brief, missingInput))
}
if !strings.Contains(brief, missingInputPointer) {
    t.Errorf("skill %q does not point Steps at Failure behavior", id)
}
```

- [ ] **步骤 2: 运行**

运行： `(cd server && go test ./internal/aurora/ -run TestRewrittenWorkflowsDescribeRealSteps -count=1)`

预期：失败，出现次数是 0。

- [ ] **步骤 3: 改每一份文档**

在 13 个文件里，紧接 `## Failure behavior` 标题、第一条列表之前，插入下面这段，上下各空一行：

```markdown
缺必填信息时，在 claim 给出的 issue 上发表评论，写明要问的问题，然后结束这一轮。不要写 artifact manifest。不要存文件。存文件会使文件数增加并扣费。
```

紧接 `## Steps` 标题、第 1 步之前，插入这句：

```markdown
If a required input is missing, follow Failure behavior: comment the question on the claim's issue and stop without storing a file.
```

不要加标题。不要把那段中文再抄进 Steps。不要改 `## Inputs`、`## Required outputs`、`## Artifact manifest`，也不要改任何供应商命令。

- [ ] **步骤 4: 运行测试**

运行： `(cd server && go test ./internal/aurora/ -run 'TestRewrittenWorkflows|TestEveryAvailableSkill' -count=1)`

预期： PASS. 第 5 阶段门。

- [ ] **步骤 5: 提交**

```bash
git add server/internal/aurora/workflows server/internal/aurora/workflows_test.go
git commit -m "docs(aurora): tell every skill to ask on the issue instead of storing a file"
```

---

## 自检

规格覆盖：

- §5 create order is Tasks 2–3. Claim skill document, attachments, and issue id are Task 4. Open URL and fallback are Task 11.
- 第 6 节一轮怎样结束是任务 7。评论续跑、审核和完成锁是任务 8。再来一版是任务 10。
- §7 file count, reserve consumption, overdraft, and replay are Task 5. Reject does not decrement is Task 6. Seven-day refund is Task 9.
- §8 skin and portals are Task 12. Aurora app does not load a second skin.
- §9 skill paragraph is Task 13.
- 第 4 节的 `needs_input_at` 和暂存行的 `billed_at` 是时钟和重放规则需要的列，写在任务 1。

占位检查：没有任务写 TBD、TODO、「以后再实现」或「同任务 N」。13 份技能文档用同一段原文，因为规格要求同一段话，测试会数每个文件里的出现次数。

类型一致性：`EnqueueAuroraIssueTask`、`ChargeStagedFile`、`auroraCommentGate`（`""` / `"enqueue"` / `"save"`）、`ExpireAuroraNeedsInput`、`auroraIssuePath`、`auroraIssueHref` 在定义它们的任务和调用它们的任务里用同一个名字。线上字段是 `issueIdentifier`。`needs_input` 不加进 `auroraTerminal`。
