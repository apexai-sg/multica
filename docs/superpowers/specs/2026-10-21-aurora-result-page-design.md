# Aurora 创作结果页与透明计费

日期：2026-10-21
状态：书面规格，待确认
关联：apexai-sg/multica#3（#1 的后续）

## 1. 意图

人提交一次海报需求，落到的是一张工作区管理页：负责人、优先级、项目、Agent 运行日志，成品埋在评论里。套餐页上的价格是翻译文件里手写的 `"$9.90/mo"`，而 Stripe 真正收的钱来自一串客户端从来看不到的 price ID。积分、每月次数、并发是三个不同的门槛，界面把它们压成了一句「已达上限」。

这份设计要做的是：让落点页回答人的四个问题——我要了什么、现在到哪了、我欠多少、成品在哪——并把工作区那套东西收到一个默认折叠、但随时能打开的区块里。价格和币种来自真实支付商品；三个门槛分别讲清楚，包括「充值不会抬高另外两个」这件事。

## 2. 目标

- 提交成功后落到这次生成上，不再落到 issue 上。
- 页面优先给出：需求（提示词与上传文件）、真实状态、助手在等你回什么、产出的文件。
- 文件能预览、下载、复制链接，并能作为下一次生成的起点。
- 需要补充信息的一轮保留回复闭环；失败的一轮可以重试；两者都在提交前给出价格。
- 页面上的每个积分数字都是服务端真实记下的（预留 / 实扣 / 退回），没有估算。
- 商品价格与 ISO 币种来自 price ID 背后的 Stripe Price 对象；读不到就不显示价格。
- 套餐页说清积分、每月次数、并发，以及充值改变什么、不改变什么。

## 3. 非目标

- 不做页内回复框。这一轮 issue 页仍是回复路径（#3 结尾那条约束），结果页给它一个链接。
- 不新建 Aurora 执行页，也不复制 issue 页的控件。结果页就是现有生成详情的重构。
- 不编造进度百分比或阶段时间线。API 不持久化阶段。
- 不改普通（非 Aurora）issue 的渲染，不改 Multica 共享的 issue 详情。
- 不加年付切换，不加新的结账流程。
- 不加数据库迁移。
- 不改生成的执行、积分定价、预留、结算与退款逻辑。

## 4. 反目标

出现下面任何一条，即使验收项全打勾也是失败：

- 支付商品读不到时仍然显示价格。
- 客户端从未读到的余额、用量、价格被显示成 `0`。
- 用百分比或自造阶段描述进度。
- 只移除跳转，却没留下能用的回复入口。
- 重试或修改前没有先给出价格。
- 结算之前把预留额说成实扣，或把实扣说成 0。

## 5. 现状（已在代码中核对）

- 提交后跳 issue：`packages/views/aurora/generation-composer.tsx` 用 `issueHref(created.issueIdentifier)` 调 `window.location.assign`；`apps/aurora/app/[workspaceSlug]/issues/[id]/page.tsx` 渲染共享的 `IssueDetailRoute`（`packages/views/issues/components/`），负责人、优先级、项目、时间线、运行日志都在那里。
- 生成本来就有自己的页面：`apps/aurora/app/[workspaceSlug]/works/[generationId]/page.tsx` 渲染 `GenerationDetail`（`packages/views/aurora/generation-detail.tsx`），显示状态、时间、提示词、预留/实扣积分、成品和一个可复制的 task id。它不读附件、不读评论，也没有任何动作入口。
- `GET /api/aurora/generations/{id}` 返回 `AuroraGenerationResponse`（`server/internal/handler/aurora.go:123`）：id、skillId、prompt、status、creditsReserved、creditsCharged、error、createdAt、taskId、issueIdentifier。**不返回** issue id、附件、评论。
- 成品由 `GenerationArtifacts`（`packages/views/aurora/generation-artifacts.tsx`）渲染：图片缩略图、预览弹窗、下载行。没有复制链接。
- needs_input 的提问是模型自己写在 issue 上的一条普通 Agent 评论；`server/internal/handler/aurora_issue_comment.go` 只是评论的准入闸门（审核通过才开下一轮），并不产生提问。当本轮 Agent 一条评论都没发时，任务结束时服务端会合成一条 `author_type=agent` 的回退评论。客户端在生成页读不到这些。
- `needs_input` 不结算：`settleAuroraGeneration` 只处理 `status == queued` 的行（`server/internal/service/aurora_completion.go:73`），所以提问轮的预留会一直挂着，早先规格推迟的 7 天超时退款并未实现。
- `aurora_generation.issue_id` 在行里存在，且被用来解析编号（`aurora.go:365`），但没有序列化出去。
- 价格是翻译字面值：`packages/views/locales/*/aurora.json` 的 `billing.subscription.tiers.*.price` 与 `billing.topup.packs.*.price`（`"$0"`、`"$9.90/mo"`、`"$29/mo"`、`"$5"`、`"$20"`）。
- `PaymentProvider`（`server/internal/aurora/stripe.go:30`）只创建结账、读订阅、验 webhook，从不读价格。
- `GET /api/aurora/billing/topups` 只返回 `{id, credits}`（`aurora.go:1901`）。`GET /api/aurora/billing/subscription` 返回 tier、status、周期、`limits{generationsPerMonth, concurrency}` 与 `usage{generationsUsedThisMonth, activeGenerations}`——没有每月积分额度，也没有价格。
- 套餐数值只有一处，`server/internal/aurora/tiers.go`：free 200 积分 / 10 次 / 并发 1，creator 3000 / 30 / 2，pro 12000 / 100 / 5；充值包 t5 = 5000、t20 = 20000 积分。
- 充值走 `LedgerKindTopup` 给钱包加积分；每月次数与并发来自 `LimitsForUser`，充值不影响。
- 提交处把 402 渲染成「积分不足」并带价格和（已知时的）余额（`SubmitFailure`），429 一律一行通用文案。技能页目前**没有**传 `topUpHref`，尽管 `/{slug}/billing` 已经存在。
- 早先的 issue 执行规格（`docs/superpowers/specs/2026-10-12-aurora-issue-execution-design.md`）把 issue 页定为结果面，并推迟了「再来一版」入口——它用生成 id 打开抽屉取回技能和提示词，从不把提示词放进 URL。本规格沿用那个机制，只重新讨论「人落在哪一页」。

## 6. 决定

下面每一条都是本规格替人做的判断；人可以推翻其中任一条，不影响其余部分。

**D1 —— 结果页是 `/{工作区}/works/{generationId}`。** 生成页已经存在、以生成为键，作品库和历史也早已链到它。被否掉的备选是「Aurora 自己的 issue 页」：它要把 issue 屏的时间线、成员校验、运行日志整套搬过来，只为复刻一个消费级页面，而它存在的理由——回复路径——本来就是那张真正的 issue 页。

**D2 —— issue 详情继续是回复路径。** 结果页用编号链过去。新生成的提交不再把人送到那里。这正是 #3 结尾的要求：保留原任务回复路径，别让「只是移除跳转」变成「无法补充资料」。

**D3 —— 结果页读 issue，补齐生成载荷里没有的东西。** 详情响应新增 `issueId`；页面用它取附件（`issueAttachmentsOptions`）和时间线（`issueTimelineOptions`）。不做新的聚合接口：issue 接口已经存在、按工作区限权、且由 TanStack Query 缓存。

**D4 —— needs_input 展示助手的最后一条消息，并给出跳转。** 取时间线上最后一条 `actor_type` 为 `agent`、`actor_id` 等于该 issue 指派对象的 `comment`，作为待回复内容渲染，回复动作链到 issue 页。标题用「助手说」而不是「提问」：这一条可能是模型写的问题，也可能是本轮没写评论时服务端合成的回退消息，两者都该原样呈现，不该替它宣称是什么。一条 agent 评论都没有时，给一句「助手在等你补充信息」并只留回复链接。这一轮不做页内输入框（见非目标）。

**D5 —— 「继续修改」用生成预填抽屉。** 技能 id 与提示词直接来自当前页面上的生成；`GenerationComposer` 新增 `initialPrompt` 属性。提示词不进 URL。结果是一次新生成、新预留，抽屉在提交前显示价格。失败的生成用同一个动作，文案是「重试」。

**D6 —— 「复制」指复制文件的直链。** 预览弹窗和每个文件行都提供。页面另外提供「复制需求」，方便把提示词拿到别处。不做成品正文的内联复制：asset 模型没有文本正文，对 `mediaUrl` 的跨域抓取也没有保证。只复制当前页面能解析成绝对 http(s) 的地址：`mediaUrl` 为空、或按页面 origin 拼接后不是 http(s) 时，不显示复制动作——让用户拿到一个粘出去打不开的链接，比不给按钮更糟。

**D7 —— 管理属性与运行信息收进一个默认折叠区块。** 里面是 task id（沿用现在的可复制形式）、issue 编号链接，以及一句指向 issue 页的说明（负责人、优先级、项目、运行日志在那边）。这个页面不放负责人/优先级/项目控件——把工程控制项搬到消费页，本身就是问题而不是解法。

**D8 —— 价格来自 Stripe Price 对象，读不到就显示「没有」。** 服务端为每个已配置的 price ID 读 `unit_amount`、`currency`、`recurring.interval` 并返回。支付未配置、price ID 为空、或 Stripe 报错时，价格是 `null`，界面完全不显示金额。翻译里的价格字面值删除，而不是留作兜底。

**D9 —— 一个 catalog 接口取代充值包列表。** `GET /api/aurora/billing/catalog` 返回每个可购买的套餐与充值包，连同各自的门槛与价格。计费页的套餐限制从它渲染，不再写死在文案里。同一次改动里删除 `GET /api/aurora/billing/topups`；`apps/aurora` 之外没有客户端调用 Aurora 计费，且它与服务端同批发布。

**D10 —— 三个门槛分三条讲。** 积分（一次生成多少钱，开始时预留、失败退回，月度额度月底过期，充值不过期）、每月次数（档次门槛，充值不会抬高）、并发（同时能跑几个）。提交被限流时的提示：可读的两项里只要有一项已经触顶，就并列给出两项当前数值；两项都还有余量，说明触发的是频率限制（`RateLimitByUser`），此时只给一句「请求过于频繁，请稍后再试」，因为并列两个还富余的数字只会让人更困惑。计费页是这三条的解释处。

## 7. 要建的东西

### 7.1 服务端（`server/`）

1. **生成载荷加 `issueId`。** 在 `AuroraGenerationResponse` 加 `IssueID string json:"issueId,omitempty"`，创建与详情两个响应都填（行里本来就有）。列表响应不需要，形状不动。
2. **支付边界加 `GetPrice`。** `internal/aurora/stripe.go` 新增 `type Price struct { AmountMinor int64; Currency string; Interval string }` 与 `PaymentProvider.GetPrice(ctx, priceID) (Price, error)`，用 `client.V1Prices.Retrieve` 实现。`Interval` 取 Stripe 的 `recurring.interval`（`month`/`year`），一次性商品为空。`unit_amount` 为 nil 视为「不可用」，不是 0。
3. **价格缓存。** `internal/aurora/pricing.go`：在 `PaymentProvider` 之上包一层 `PriceCache`，带互斥锁、TTL（5 分钟）和可注入的时钟。缓存成功结果，不缓存错误。冷缓存只读**本次展示要用到**的 price ID：creator 月付、pro 月付、t5、t20，共四次。年付的两个 ID 仍留在配置里供结账使用，但不进 catalog——界面这一版不提供年付切换，读它只会多一次 Stripe 调用和一份没人渲染的数据。
4. **catalog 处理函数。** `GET /api/aurora/billing/catalog`，走 `RequireHumanActor`，返回：

   ```json
   {
     "paymentsConfigured": true,
     "tiers": [
       {"tier": "free", "monthlyCredits": 200, "generationsPerMonth": 10,
        "concurrency": 1, "price": null},
       {"tier": "creator", "monthlyCredits": 3000, "generationsPerMonth": 30,
        "concurrency": 2, "price": {"amountMinor": 990, "currency": "usd", "interval": "month"}}
     ],
     "topups": [
       {"id": "t5", "credits": 5000,
        "price": {"amountMinor": 500, "currency": "usd", "interval": ""}}
     ]
   }
   ```

   `paymentsConfigured` 在处理函数没有支付实现时为 false。金额一律是 Stripe 报的该币种最小单位；小数位数由客户端按币种决定。每个 tier 只有一个 `price`，即月付；年付价格不在这个载荷里。删除 `/billing/topups` 路由。

### 7.2 核心客户端（`packages/core/aurora/`）

- `schema.ts`：`auroraGenerationSchema.issueId`（string，默认 `""`）；新增 `auroraPriceSchema`（`amountMinor: number`、`currency: string`、`interval: string`）与 `auroraCatalogSchema`（`paymentsConfigured`、`tiers[]`、`topups[]`，全部带默认值，降级响应仍渲染成空目录）。删除 `auroraTopupSchema` / `auroraTopupsSchema`。
- `api.ts`：`getAuroraCatalog()` 走 `parseWithFallback`；删除 `listAuroraTopups`。
- `queries.ts`：`auroraCatalogOptions`（staleTime 5 分钟）与 `useAuroraCatalog`；删除充值包 hook。

### 7.3 视图（`packages/views/aurora/`）

**`generation-detail.tsx` —— 重构成结果页。**

自上而下：

1. 页头：工具名为标题、真实状态徽章、时间；返回作品库的面包屑保留。
2. **计费行。** 进行中：「已预留 N 积分——结束后按实际结算，失败全额退回。」`needs_input`：「已预留 N 积分，本轮尚未结算——回复后继续跑，失败才退回。」已完成：「实扣 N 积分。」失败：「未扣费，已退回 N 积分。」数值只来自 `creditsReserved` / `creditsCharged` 与状态。`needs_input` 单独一条文案是必须的：这一轮不结算（见 §5），照搬「结束后结算」会在一个可能永远不结束的页面上给出一句不成立的承诺。
3. **需求。** 提示词完整换行显示。issue 解析成功时，列出上传的附件：图片缩略图与可下载的文件行。只取 `comment_id` 为空的附件，即 issue 自己的附件；回复评论里补传的文件属于对话，不属于「我提了什么」。
4. **助手说**（仅 `needs_input`）：取该 issue 指派 Agent 的最后一条评论原样呈现，下面接链到 issue 页的回复动作；一条都没有时给固定文案。见 D4。
5. **成品。** `GenerationArtifacts`，加上复制链接动作；终态且无文件时如实说明。下面放动作：「继续修改」（失败时为「重试」），按钮上带该工具的积分价格。工具已不在目录里、或目录里没有可见的 carrier Agent 时禁用并各给一行说明——`GenerationComposer` 在 `agent` 为空时返回 null，不拦就会点开一个空抽屉。
6. **高级信息（默认折叠）。** 可复制的 task id、issue 编号链接，以及指向 issue 页的说明（负责人、优先级、项目、运行日志）。

失败原因在失败时直接放在状态下方。

新属性：`topUpHref`；页面自己持有「继续修改」用的抽屉，通过 `useAuroraAgentDirectory()` 按 `skillId` 解析出 Agent。

**`generation-composer.tsx`。**

- 新属性 `generationHref?: (generationId: string) => string`。生成创建成功后跳它，不再跳 issue。未提供时保持今天的行为（跳 issue，否则在抽屉里轮询）。
- 新属性 `initialPrompt?: string`，只在挂载时写入一次输入框。
- 429 的文案按 D10：读 `useAuroraSubscription`，可读的两项里有一项触顶就并列当前数值，都富余则回到通用的一句频率提示。
- 普通 Agent 任务流（`skill === null`）继续跳 issue，不变。

**`generation-artifacts.tsx`。** 预览弹窗与每个文件行加复制链接动作，复用现有剪贴板工具与它的失败态。

**`billing.tsx`。**

- 改读 catalog，不再读充值包列表。
- 套餐卡片显示服务端给的门槛（每月积分、每月次数、并发）与真实价格；无价格时显示「本实例未开售」并保持安静。
- 一小段门槛说明：一分积分买到什么、月度额度会过期而充值积分不会、每月次数是档次上限、充值不抬高它。
- catalog 里该商品没有价格时，购买按钮禁用。

**`format.ts`。** `formatMoney(amountMinor, currency, locale)`，用 `Intl.NumberFormat` 的 `style: "currency"`，小数位从该 locale 自身的货币格式推导，避免把零小数币种（日元）除以 100。未知币种退回 `"CODE 12.34"` 而不是抛错。

### 7.4 应用（`apps/aurora/`）

- `app/[workspaceSlug]/skills/page.tsx`：传 `generationHref={(id) => routes.work(id)}`；传 `topUpHref={routes.billing()}`，让积分不足的提示终于指向已经存在的路由。
- `app/[workspaceSlug]/works/[generationId]/page.tsx`：传 `topUpHref`。
- `app/[workspaceSlug]/issues/[id]/page.tsx` 不动——它是回复路径。

### 7.5 多语言（`packages/views/locales/{en,zh-Hans,fr,ja,ko}/aurora.json`）

在 `detail` 下新增上述各段的键，新增 `composer.continue` / `composer.retry_quote`、复制动作、计费页的 catalog 与门槛文案。五个文件里删除 `billing.subscription.tiers.*.price` 与 `billing.topup.packs.*.price`。parity 测试会强制五语对齐。

## 8. 行为细节

**提交后的跳转。**

| 情形 | 结果 |
| --- | --- |
| 生成已创建、有 `generationHref`、响应可读 | 跳 `works/{id}` |
| 生成已创建、响应不可读（没有 id） | 沿用现有 `unreadable` 结局，不跳转 |
| 生成已创建、没有 `generationHref`（其它宿主） | 沿用现有跳 issue / 抽屉内轮询 |
| 普通 Agent 任务（`skill === null`） | 沿用现有跳 issue，不变 |

**状态。** `queued` / `running` 显示为进行中，带转圈与「创建于 X 前」（现有的相对时间）；`needs_input` 渲染「助手说」区；`completed` 与 `failed` 是终态并停止轮询。没有百分比，没有阶段名。不做走秒计时器，也不在无产物时给出「已用时 N」：载荷没有 last-updated 字段，现有 `elapsedMs` 的语义是「创建 → 最新产物」，没有产物就没有时长，这个语义保持不动。想表达「跑了多久」只能用创建时间的相对值。

**数据缺失。**

- 没有 `issueIdentifier`/`issueId`（旧服务端）：不显示附件、回复链接、高级信息里的 issue 行；提示词、状态、积分、成品照常。
- 目录读不到：工具名退回 id，「继续修改」禁用，积分仍按已记录的数字显示，不显示单次价格。
- 生成响应降级：沿用现有「加载失败」界面与重试。
- issue 读失败：需求区显示提示词并说明文件列不出来；成品区不受影响。

**金额语义。** 预留不是实扣。失败的生成报实扣 0 与退回额，页面说「已退回」，绝不说「实扣 0」。完成的生成报 `creditsCharged`。`needs_input` 的预留在整条生成结束前一直挂着，页面如实说「已预留、尚未结算」，不承诺何时结算——服务端这一版没有 7 天超时退款。

**轮询。** 详情查询在非终态保持 3 秒轮询。时间线查询在详情首次报出 `needs_input` 时刷新一次，让提问一出现就能看到，又不常驻第二个轮询。

## 9. 约束

- `packages/views` 不得引入 `next/*`；抽屉与详情继续以属性接收路由。
- `packages/views` 里不定义 store；页面用 Query 加抽屉开关的本地状态。
- API 兼容：新增字段都有默认值；旧服务端仍能渲染页面，只是少附件和回复链接。
- 无迁移。不改预留、结算、退款逻辑。
- UI 文案遵循仓库规则：描述可选、不重复标签、权限/费用/后果保持可见。

## 10. 验证

服务端（Go，`make test` / 定向包）：

- `GetPrice` 正确映射 `unit_amount`/`currency`/`interval`；nil 金额视为不可用。
- 价格缓存：TTL 内命中，过期重取，错误不缓存。
- catalog 处理函数：支付未配置 → `paymentsConfigured: false` 且价格全为 null；假价格 → 精确最小单位与 ISO 币种；缺某个 price id → 只有该项为 null；Stripe 报错 → 该项 null 且请求整体不失败。
- 生成创建/详情带 `issueId`；旧 fixture 不带也能解析。
- `aurora_test.go` 的 `fakePayments` 补 `GetPrice`。

前端（`pnpm typecheck`、`pnpm lint`、`pnpm test`）：

- schema：降级 body 上 catalog 取默认值；`issueId` 默认；价格 schema。
- `formatMoney`：美元、零小数币种、未知币种。
- 结果页：需求与附件（评论附件被排除）；needs_input 的「助手说」与回复链接、无 agent 评论时的固定文案；预留/实扣/退回/未结算四种措辞；复制链接在绝对 http(s) 上出现、在空值或不可解析时不出现；目录无该项、或没有可见 carrier Agent 时继续禁用；高级信息默认折叠。
- 抽屉：给了 `generationHref` 时跳生成；没给时回退到 issue 路径；`initialPrompt` 正确预填；429 在「有门槛触顶」与「都富余」两种情形下的文案。
- 计费页：价格来自 catalog，null 时不显示，支付关闭时购买禁用；三个门槛分条陈述。
- 五语 parity。

手工验收（#3 点名的四种情形）：

1. 余额不足 → 提交处拒绝，给出价格与余额，充值链接可用。
2. 每月次数耗尽 → 拒绝说明次数，并说明充值不会重置。
3. 失败退款 → 结果页给出失败原因与「已退回」，绝不显示扣费。
4. 支付未配置 → 计费页没有价格并说明原因；结果页不受影响。

## 11. 实施顺序

1. 服务端：`issueId`、`GetPrice`、缓存、catalog、测试。
2. 核心：schema、api、queries、测试。
3. 视图：结果页、成品复制、抽屉新属性、计费页、多语言。
4. 应用：路由接线、`topUpHref`。
5. 验证扫一遍，再用本地环境手工走完四种情形。

## 12. 留给构建者

- 高级信息区块与门槛说明的具体措辞和排版。
- issue 链接叫「回复」还是「打开任务」——一个动作、一个标签，评审时定。
- `formatMoney` 先放 `views/aurora/format.ts`，等第二个界面用到再考虑上移 core。
- 测试文件位置沿用现有的「紧贴实现」约定。
