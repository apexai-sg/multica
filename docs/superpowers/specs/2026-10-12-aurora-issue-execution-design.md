# Aurora 生成走问题详情

日期：2026-10-12

状态：书面规格。实施计划见 [`docs/superpowers/plans/2026-10-12-aurora-issue-execution.md`](../plans/2026-10-12-aurora-issue-execution.md)。尚未实施。

## 1. 目标与已确认边界

Aurora 提交一次生成之后，人打开的是现成的问题详情，不是一张新的执行页。在那张页上能看 Agent 已经写下的话和已经存上的文件，能在 Agent 停住之后回复，失败也能续。13 个可用技能用同一种说话方式。

已确认：

1. 每次生成在 Agent 开口之前建一条 issue，指派该技能的系统 agent。第一轮用 `EnqueueTaskForIssue`，不用没有 issue 的 quick-create。claim 仍带 Aurora 技能文档和附件，不能改成普通 issue 简报。
2. 打开的路径是 `/{工作区}/issues/{编号}`。`WEB_ORIGIN` 来自配置，不写死主机。登录态过不了跨应用跳转时，Aurora 在自己的同一路径上挂现成的 `IssueDetail`。两种开法都不是新页面。
3. 主区域是评论和时间线里已经写下的话，加上已经存上的文件。没有的步骤不编。
4. 说话是双向的，下一轮才生效。进行中不能把一句话塞进当前轮。
5. 一条生成对应一条 issue。`task_id` 指向当前轮。下一轮把 `task_id` 改到新 task。旧 task 晚到的结算对不上这张单，直接跳过。
6. 账单先有文件数。`file_count` 是唯一账。已扣次数等于文件数，金额是文件数乘该技能标价。接不接受、审不审核通过，都不改这个数。
7. 没存上文件的轮次不增加文件数，也不新增扣费。开工时预扣 1 份标价，只是下一个文件的余额占用，不进文件数，也不进已扣次数。
8. 做完之后，这条 issue 上的评论不再启动 Agent。再来一版是新生成、新 issue，人在 Aurora 抽屉里再提交，不自动提交。
9. Aurora 生成的 issue 用 Aurora 调色。其它 issue 的 DOM 和样式不改。不改 Multica issue 的样式文件，也不把 Aurora 调色写进 web 的 `:root`。
10. 13 个技能不新加章节。在现有 Failure behavior 里加同一段话：缺必填信息就在这条 issue 上评论提问，然后停，不要写 manifest，不要存文件。

## 2. 现状

这些事实来自当前代码，实施时以代码为准。

- 提交在 `packages/views/aurora/generation-composer.tsx` 的右侧抽屉。成功后抽屉里轮询生成状态，人只能等或关掉。
- `CreateAuroraGeneration` 调用 `EnqueueQuickCreateTask`，issue 为空。评论触发和会话续跑都要求 task 有 issue。
- 结算按 task 结束时有没有已提交的作品：有就扣掉整份预留，没有或失败就退款。没有「等你回复」这种非终态。
- `Reserve` 已经从余额扣款，键是 `reserve:` 加生成 id。它拒绝非正数，余额不够就返回 `ErrInsufficientCredits`，不会把余额扣成负数。
- 文件存进 `aurora_artifact_staging` 之后才有暂存行。审核和提交成作品发生在这之后。
- 问题详情是 `packages/views/issues/components/issue-detail.tsx`，web 路由是 `apps/web/app/[workspaceSlug]/(dashboard)/issues/[id]/page.tsx`。
- Aurora 调色在 `packages/views/aurora/aurora.css`，类名都带 `aurora-` 前缀。`apps/aurora/app/globals.css` 把语义 token 映射到这套调色，而且只在 Aurora 应用的 `:root` 上。web 不加载这份映射。
- 13 份技能文档在 `server/internal/aurora/workflows/`，固定五节：Inputs、Steps、Required outputs、Artifact manifest、Failure behavior。测试拒绝 manifest 约定以外的 `aurora.<verb>`。

## 3. 不做什么

- 不做新的 Aurora 执行页，不嵌一套仿问题详情的新布局。
- 不把普通 issue 改成 Aurora 样式，不改 Multica issue 的样式文件。
- 不编造阶段时间线。
- 不改 13 个技能的供应商调用步骤。
- 不按轮次扣一整份，也不按接不接受来加减文件数。
- 不把用户上传的附件算进文件数。
- 不在做完的那条 issue 上免费再跑一轮。

## 4. 对象

| 对象 | 责任 | 不承担 |
| --- | --- | --- |
| `aurora_generation` | 这一次生成的账单、当前轮、对应的 issue | 评论正文、会话续跑 |
| issue | 人看到的页面、评论、时间线、文件附件 | 积分账 |
| task | 一轮 Agent 执行 | 跨轮账单 |
| `aurora_artifact_staging` | 一个存储成功的产出文件 | 决定这轮算不算做完 |

`aurora_generation` 增加 `issue_id` 和 `file_count`。不建外键。`issue_id` 的索引单独一条迁移，用 `CREATE INDEX CONCURRENTLY`。关系在应用代码里校验。

`file_count` 从 0 开始。已扣金额等于 `file_count` 乘该技能在目录里的标价。标价仍是目录里的 `Credits`，不另定单价。

标题是 `技能显示名：提示词前 40 个字`。工作区没有 `aurora` 标签就建一个，并打在这条 issue 上。标签用来过滤。人认标题。

## 5. 创建与打开

顺序固定：

1. 沿用现在的校验、提示词审核、权益检查。
2. 插入生成行，`file_count` 为 0。
3. 建 issue，指派该技能的系统 agent，写上标题、提示词和附件。
4. 预扣 1 份标价。键仍是 `reserve:` 加生成 id。这不是已扣次数。
5. `EnqueueTaskForIssue`。失败则解除指派，把 issue 标为不会再入队的终态，退还这份预扣，生成标为 `failed`。
6. 把 `issue_id` 和 `task_id` 写回生成。
7. 响应带上 issue 的编号。Aurora 抽屉关掉，打开问题详情。

打开地址是 `NEXT_PUBLIC_WEB_ORIGIN` 加上 `/{工作区}/issues/{编号}`。该配置缺失，或跳转后没有登录态，就在 Aurora 应用的同一路径渲染 `IssueDetail`。作品库、历史、旧的生成详情链接都指到这条 issue。

claim 必须把技能文档、附件和这条 issue 的 id 交给 Agent。没有 issue id，Agent 无法把提问写进评论。

## 6. 一轮怎样结束

不解析评论正文。现有的 `settleAuroraOnCompleted` 和 `settleAuroraOnFailed` 不再用「有作品就扣整份预留、没有或失败就退款」处理这种生成。任务结束只改状态。退款只发生在入队失败和 7 天超时，且只退尚未对上文件的预扣。

- Agent 写了 manifest：生成变为 `completed`。已存上的文件保持已计入的文件数。这条 issue 再收到评论，评论留下，但不入队。
- Agent 没写 manifest：生成变为 `needs_input`。文件数保持这一轮实际存上的个数。人可以评论续跑。
- 入队之前就失败：生成 `failed`，退还尚未对上文件的预扣。没有这种预扣就不退。

`needs_input` 上的评论才是下一轮。服务端入队，把 `task_id` 改到新 task，不第二次预扣。当前轮还是 `queued` 或 `running` 时，评论可以留下，但不入队，也不并行开一轮。问题详情在这两种状态下不把评论框当成可以发送的下一轮。

`completed` 或已超时的 `failed` 上，评论触发直接跳过，不入队。

再来一版是问题详情上的一个链接，只在 `completed` 时出现。它打开 Aurora 抽屉，用生成 id 取回技能和提示词，不把提示词放进 URL。人再点提交。那一次走第 5 节的全部顺序，是新生成、新 issue、新的 1 份预扣。

跟进评论在入队之前走和原始提示词一样的审核。审核不通过：评论留下，Agent 不跑，文件数和预扣都不变。

## 7. 文件数

一个文件计入 `file_count`，当且仅当它是这条生成某一轮产出的、存储已经成功的暂存行。每个暂存 id 只加 1。用户上传的附件不加。存储失败不加。只提问不加。之后的审核拒绝、不展示、不提交成作品，都不减。

计费和加数在同一个事务里：

- 还有未对上文件的预扣时，第一个文件用掉它。不再扣第二次。`file_count` 加 1，已扣次数加 1。
- 预扣已经用完时，每个新文件按暂存 id 做一次扣款。余额不够也要记下这笔扣款，余额可以因此为负。不能调用会拒绝透支的 `Reserve`，否则这个文件会变成免费的。
- 同一暂存 id 的重放不得再次加数或扣款。

问题详情可以不展示被拒绝的文件。不展示不改变文件数。

7 天没有新的一轮：只退还尚未对上文件的那份预扣，已按文件扣掉的不退。生成变为 `failed`，issue 上留一句说明。之后的评论不再入队。时钟从进入 `needs_input` 起算，新的一轮把它清掉。

## 8. 样式

`IssueDetail` 的结构和类名保持不变。

这条 issue 有对应的 Aurora 生成时，页面根节点加上 `aurora-issue`。这个作用域加载与 `apps/aurora/app/globals.css` 相同的语义 token 映射，只作用于该节点及其弹出层。弹出的菜单和对话框必须落在同一作用域里，不能还是 Multica 的颜色。

没有 Aurora 生成的 issue 不加 `aurora-issue`。web 的 `:root` 不写入 `--background: var(--aurora-paper)` 或任何 Aurora token。不修改 Multica issue 的样式文件。Aurora 应用自己的 `:root` 已经有这套映射，在那里打开同一组件不再做第二套皮肤。

## 9. 技能文档

13 份文档的 Failure behavior 各加同一段，不新增标题：

缺必填信息时，在 claim 给出的 issue 上发表评论，写明要问的问题，然后结束这一轮。不要写 artifact manifest。不要存文件。存文件会使文件数增加并扣费。

Steps 开头加一句，指向这段，不复制第二份规则。不新增 `aurora.<verb>`。供应商调用步骤不改。

## 10. 验收

- 创建响应带 issue 编号。生成行有 `issue_id`。第一轮 task 的 issue 就是这一条。
- claim 指向该技能文档，并带上 issue id。不得只用普通 issue 简报代替技能文档。
- `needs_input` 的评论入队，不第二次预扣，`task_id` 改为新 task。
- `queued` 或 `running` 时的评论不入队。
- `completed` 和超时 `failed` 的评论不入队。
- 再来一版不自动创建生成。提交之后是新的生成和新的 issue。
- 同一暂存 id 只让 `file_count` 加 1，只扣一次。
- 审核拒绝不减少 `file_count`。
- 没存上文件的结束进入 `needs_input`，`file_count` 不变。
- 余额不够时，第二个文件仍然留下扣款记录，且不重复扣第一个已用预扣抵掉的文件。
- 7 天超时只退尚未对上文件的预扣。
- 普通 issue 详情的根节点没有 `aurora-issue`。web 的 `:root` 没有 Aurora 的 `--background` 映射。
- Aurora 生成的 issue 详情根节点有 `aurora-issue`，弹出层使用同一映射。
- 13 份文档都有第 9 节那段话，且没有新的 `aurora.<verb>`。

## 11. 实施顺序

实施计划另写。顺序是：生成与 issue 的绑定和入队，文件数与扣款，评论续跑和完成锁，问题详情的打开与皮肤，技能文档。前一步的验收不过，不开始下一步。
