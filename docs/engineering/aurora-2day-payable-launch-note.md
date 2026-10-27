# Aurora two-day payable-subscription launch note

计划：`docs/superpowers/plans/2026-10-27-aurora-two-day-payable-subscription-mvp.md`，分支 `feat/aurora-2day-mvp`。父问题 #43，票号 #45–#56 对应 T1–T12。

本文件是计划里所说的 "launch note"（ruling：放在 `docs/engineering/`），把无法在本环境执行的 Owner/Ops 任务写成可执行步骤，并记录计划要求的决策与留空待填项。

每个小节首行标注状态：

- `code-complete`：代码已合入，本节只描述契约，不重复实现。
- `owner/ops — 本环境未执行`：需要 Stripe 账号/密钥、生产环境或 DeepSeek 凭证，本次未执行，也没有被验证过。
- `本文计算`：基于冻结价目表的算术，不是实测数据。

行号以写入时的工作树为准。其他任务（如 #53）并行改动同一批文件后行号会偏移，引用以函数名和字符串为准。

## 票号映射

| 票号 | 任务 | 负责 | 状态 |
|---|---|---|---|
| #44 | T0 目录与价格 | Eng | code-complete |
| #45 | T1 Stripe 产品与价格 | Owner | owner/ops — 本环境未执行（见 T1） |
| #46 | T2 生产环境变量 | Ops | owner/ops — 本环境未执行（见 T2） |
| #47 | T3 Webhook 可达性 | Ops | owner/ops — 本环境未执行（见 T3） |
| #48 | T4 测试模式端到端 | Eng | 清单已写好；执行需要 test 密钥，本环境未执行（见 T4） |
| #49 | T5 价格/积分文案与不退款条款 | Eng | code-complete；条款已渲染在两处购买控件旁（见 T10） |
| #50 | T6 权益回读 | Eng | 步骤已写好；执行需要一次测试购买，本环境未执行（见 T6） |
| #51 | T7 DeepSeek 路由 spike | Eng | BLOCKED：凭证未提供，决策已记录（见 T7） |
| #52 | T8 成本核算 | Eng | 本文计算（非实测，见 T8） |
| #53 | T9 取消路径（Customer Portal） | Eng | 另一实现者交付，本说明不验收 |
| #54 | T10 不退款运营 | Ops | runbook 见 T10；线上条款本环境未验证 |
| #55 | T11 告警 | Eng | 另一实现者交付，本说明不验收 |
| #56 | T12 线上冒烟 | Owner + Ops | owner/ops — 本环境未执行（见 T12） |

## T1 — Stripe products and prices (#45)

**状态：owner/ops — 本环境未执行。**

要在 Stripe 里创建五个对象（三个订阅 + 两个一次性积分包）。`server/internal/aurora/tiers.go` 持有月额度授予与 price-id 槽位：`creator` 1,900 / `pro` 5,900 / `business` 16,900；top-up `t5` 500 / `t20` 2,000。金额与币种不在这份 Go 目录里：它们来自计划 §Decisions and what remains open（S$19 / S$59 / S$169，100 credits = S$1）和 `packages/views/locales/*/aurora.json` 的 `billing.subscription.tiers` 与 `billing.topup.packs` 文案。建 Stripe price 时这三处必须一致。

| 对象 | env 变量（价目 id 填入处） | 金额 | 币种 | 类型 | 目录授予 |
|---|---|---|---|---|---|
| Creator 月付 | `AURORA_STRIPE_PRICE_CREATOR_MONTHLY` | S$19.00（minor unit `1900`） | SGD | recurring, `month` | 1,900 credits/月 |
| Pro 月付 | `AURORA_STRIPE_PRICE_PRO_MONTHLY` | S$59.00（`5900`） | SGD | recurring, `month` | 5,900 credits/月 |
| Business 月付 | `AURORA_STRIPE_PRICE_BUSINESS_MONTHLY` | S$169.00（`16900`） | SGD | recurring, `month` | 16,900 credits/月 |
| Top-up 5 | `AURORA_STRIPE_PRICE_TOPUP_5` | S$5.00（`500`） | SGD | one-time | 500 credits |
| Top-up 20 | `AURORA_STRIPE_PRICE_TOPUP_20` | S$20.00（`2000`） | SGD | one-time | 2,000 credits |

年付不在本次售卖范围：`AURORA_STRIPE_PRICE_CREATOR_YEARLY`、`AURORA_STRIPE_PRICE_PRO_YEARLY` 保持为空；`business` 在 `tiers.go` 里根本没有年付槽位，年付请求一律按未配置的失败关闭处理。

**币种未决。** 计划 §Still to confirm 仍写着 SGD 与 USD 二选一。Stripe 的 Price 一旦创建，币种和金额都不可改：如果账号实际以 USD 结算，就按同样数字建 USD price（`1900`/`5900`/`16900`/`500`/`2000` 分），并回来改这张表；UI 文案用的是 `$`（`packages/views/locales/*/aurora.json` 的 `billing.subscription.tiers` 与 `billing.topup.packs`），所以在建 live price 前必须先把币种定下来。

### Dashboard 步骤

1. 先留在 **Test mode**，全部验证通过后再切 Live。
2. Products → Add product：名称（如 `Aurora Creator`）、价格 `19.00`、币种按上面决定、**Recurring / Monthly**。重复三次，分别 19 / 59 / 169。
3. Products → Add product：`Aurora Top-up 5`，价格 `5.00`，**One-off**（不是 recurring）；再来一个 `Aurora Top-up 20`，`20.00`。
4. 打开每个产品的价格行，复制 `price_...` id。
5. 填入下面的 T1 价目 id 表，再注入环境变量（T2）。

### REST / CLI

金额用最小货币单位（分），订阅价格必须带 `recurring[interval]=month`：

```sh
# 建产品（每个对象一次）
curl https://api.stripe.com/v1/products \
  -u "$STRIPE_SECRET_KEY:" \
  -d name="Aurora Creator"

# 建月付价格：19.00 SGD
curl https://api.stripe.com/v1/prices \
  -u "$STRIPE_SECRET_KEY:" \
  -d product=prod_xxx \
  -d unit_amount=1900 \
  -d currency=sgd \
  -d "recurring[interval]=month"

# 建一次性 top-up 价格：5.00 SGD（不带 recurring）
curl https://api.stripe.com/v1/prices \
  -u "$STRIPE_SECRET_KEY:" \
  -d product=prod_topup5 \
  -d unit_amount=500 \
  -d currency=sgd
```

CLI 等价写法（注意给方括号加引号，避免 shell 展开）：

```sh
stripe prices create --product prod_xxx --unit-amount 1900 --currency sgd '--recurring[interval]=month'
```

价格不可改：`unit_amount`、`currency`、`product`、`recurring` 在 Price 上都是不可变的。已有的 $9.90 / $29 之类价格不能编辑成新价，只能新建一个 Price，再归档旧的：

```sh
stripe prices update price_old --active=false
```

同一个产品的旧价格归档后，历史订阅仍按原价续费；这是 Stripe 的语义，不是本项目的行为。

### T1 价目 id 记录表（owner 填，填完才算 T1 完成）

| 对象 | Test mode `price_...` | Live mode `price_...` |
|---|---|---|
| Creator 月付 |  |  |
| Pro 月付 |  |  |
| Business 月付 |  |  |
| Top-up 5 |  |  |
| Top-up 20 |  |  |

要求：这五个 id 必须记录在本文件；test 与 live 是两套独立对象、两套 id，不能混用。把 live 的 `sk_live_...` 和 test 的 `price_...` 配在一起时，Stripe 会拒绝这个 price，checkout 返回 `500 failed to create checkout`（而不是 503）——看到这个 500 先查模式是否配错。

## T2 — Production environment (#46)

**状态：owner/ops — 本环境未执行。**

服务端在启动时读取这些变量（`server/cmd/server/router.go:464-472`），在 `handler.New` 里构造支付 provider（`server/internal/handler/handler.go:577-580`）和价格目录（`handler.go:599-607`）。值只在**启动时读一次**，所以注入后必须重启/重新部署才生效。

| env 变量 | 作用 | 缺失后果 |
|---|---|---|
| `STRIPE_SECRET_KEY` | Stripe API 调用（Checkout、Portal） | 与 webhook secret 任一为空 → provider 为 nil → 所有支付接口 503 |
| `STRIPE_WEBHOOK_SECRET` | webhook 签名校验（`server/internal/aurora/stripe.go:136`） | 同上：`NewStripeProvider` 在两个 key 任一为空时返回 nil（`stripe.go:51-58`），所以只补 secret key 仍然是 503 |
| `AURORA_STRIPE_PRICE_CREATOR_MONTHLY` | Creator 月付 price id | 只有 Creator 月付 checkout 503 |
| `AURORA_STRIPE_PRICE_PRO_MONTHLY` | Pro 月付 price id | 只有 Pro 月付 checkout 503 |
| `AURORA_STRIPE_PRICE_BUSINESS_MONTHLY` | Business 月付 price id | 只有 Business 月付 checkout 503 |
| `AURORA_STRIPE_PRICE_TOPUP_5` | 500 credits 包 price id | 只有 `t5` top-up checkout 503 |
| `AURORA_STRIPE_PRICE_TOPUP_20` | 2,000 credits 包 price id | 只有 `t20` top-up checkout 503 |

这些是**部署环境**的配置，不进仓库：`.env.example` 和 `scripts/` 都没有 Stripe 变量，仓库里也不应有真实值。

### 验证

`POST /api/aurora/billing/checkout` 成功时返回 `200` 与 `{"checkoutUrl":"https://checkout.stripe.com/..."}`（`server/internal/handler/aurora.go` 的 `CreateAuroraCheckout` 末尾）；`POST /api/aurora/billing/topup/checkout` 同形。接口是 human-only（`router.go:2127-2128` 的 `RequireHumanActor`），要用登录会话，不能用 agent task token 调。

```sh
curl -i -X POST 'https://<api-host>/api/aurora/billing/checkout' \
  -H 'Content-Type: application/json' \
  -H 'Cookie: <登录会话 cookie>' \
  -d '{"tier":"creator","billingCycle":"monthly",
       "successUrl":"https://<app-host>/<workspaceSlug>/billing?checkout=success",
       "cancelUrl":"https://<app-host>/<workspaceSlug>/billing"}'
```

503 的三种消息与缺失项一一对应：

| HTTP 状态与消息 | 含义 | 代码位置 |
|---|---|---|
| `503 payments not configured` | `STRIPE_SECRET_KEY` 或 `STRIPE_WEBHOOK_SECRET` 为空，provider 没建起来 | `aurora.go` `CreateAuroraCheckout` 开头的 `h.Payments == nil` 分支（写入时 :1376-1378） |
| `503 tier price not configured` | 该 tier 对应周期的 price id 为空 | 同函数 `priceID == ""` 分支（写入时 :1421-1423） |
| `503 topup price not configured` | 该 top-up 包的 price id 为空 | `CreateAuroraTopupCheckout` 的 `topup.PriceID == ""` 分支（写入时 :1580-1582） |

两个容易误判的非 503 失败：

- `400 successUrl and cancelUrl must use a trusted app origin`：返回地址的 origin 必须是 `MULTICA_APP_URL`（回退 `FRONTEND_ORIGIN`）或 `CORS_ALLOWED_ORIGINS` 里的 origin。判定在 `server/internal/handler/aurora.go` 的 `validCheckoutURL`，两个 env 分别在 `router.go` 的 `appURLFromEnv` 与 `allowedOrigins` 读取。不是 env 缺失。
- `409`：该用户还有待完成或已生效的订阅意图，属于业务状态，不是配置错。

### 失败关闭行为

- 未配置的 price id 只关掉**那一个** checkout，其他计划照常。
- `business` 年付没有 price 槽位，请求年付即 `503 tier price not configured`。
- `free` 不可购买（`400 unknown tier`）。
- 验收清单里“unset `STRIPE_SECRET_KEY` → 503”要在 test/staging 验证，**不要**在生产 unset 支付密钥。

## T3 — Webhook reachability (#47)

**状态：owner/ops — 本环境未执行。**

- 端点：`POST /api/aurora/billing/stripe/webhook`。注册在**公开**路由组（`server/cmd/server/router.go:1550`，位于认证分组之外；注释 :1545-1549 说明原因）。没有 session 认证，`Stripe-Signature` 是唯一认证（`aurora.go` 的 `StripeWebhook`：缺 header 返回 `401 missing Stripe-Signature header`，验签失败返回 `400 invalid signature`）；body 上限 1 MiB。
- 处理的四个事件（`handleStripeEvent`）：
  - `checkout.session.completed`
  - `checkout.session.async_payment_succeeded`
  - `customer.subscription.updated`
  - `customer.subscription.deleted`
- 其他事件被忽略并 ack（`200`），这是故意的：Stripe 发的事件远多于本产品消费的。

### Ops 步骤

1. Stripe Dashboard → Developers → Webhooks → Add endpoint。
2. Endpoint URL：`https://<api-host>/api/aurora/billing/stripe/webhook`（必须直达 API，不经重定向；Stripe 不跟随 POST 重定向）。
3. 勾选上面四个事件（选全部事件也能工作，未处理的事件会被 ack）。
4. 复制该 endpoint 的 Signing secret（`whsec_...`）→ `STRIPE_WEBHOOK_SECRET`，然后重启服务（见 T2 的启动时读取）。
5. 放行入口：WAF / ALB / 安全组必须允许 Stripe 对该路径的 POST。这是现有部署路径上的放行规则，计划明确不为本次新建 AWS 资源。该路径不能挂 SSO、Basic Auth 或要求 cookie；也不要在前面加会改写 body 的代理，验签用的是原始 body。
6. Test mode 与 Live mode 的 endpoint、`whsec_` 都是两套，不可互换。

### 本地开发

```sh
stripe listen --forward-to localhost:8080/api/aurora/billing/stripe/webhook
```

`stripe listen` 会打印一个**不同的** `whsec_...`：把它写进本地环境并重启服务，不要拿 Dashboard endpoint 的 secret 配本地。

### 验证

Dashboard → Webhooks → 选该 endpoint → Send test event（例如 `checkout.session.completed`）：

- 期望 `200`，响应体 `{"received":true}`；
- 服务端日志里没有 `invalid signature`；
- 若 400，先查 secret 与模式是否配错；若 500 且日志有 `stripe webhook handling failed`，是可重试的处理失败（Stripe 会重投，ledger 幂等键保证安全）；若 `200` 但日志有 `stripe event with invalid userId metadata`，说明 Checkout 的 metadata 没带上（配置/代码问题），事件被显式 ack 掉，不会自动修复。

## T4 — Test-mode end-to-end (#48)

**状态：清单已写好；执行需要 Stripe test 密钥与 test price id，本环境未执行。**

前置：T2 注入的是 **test** 值（`sk_test_...`、test `price_...`、test endpoint 的 `whsec_...`）；T3 的端点已在 test mode 注册。测试卡：`4242 4242 4242 4242`，任意未来有效期与 CVC。

按顺序执行；每一步都要看到证据才算过，不做推断。

| # | 操作 | 期望的可观察证据 | 对应验收清单行 |
|---|---|---|---|
| 1 | 新用户进入 `/{workspaceSlug}/billing` | 免费版卡片显示 200 credits 与 10 runs/月 | “A new user lands on `/{workspaceSlug}/billing`…” |
| 2 | 用测试卡订阅 Creator | 回到 `?checkout=success`；页面在约 1 分钟内自动 refetch（`billing.tsx` 的 checkout 返回轮询），套餐显示 `creator`；钱包 +1,900，且**只有一次**；流水为 `kind=adjustment`、`reference=sub:<userID>:<YYYY-MM>`。若本月已先领过免费 200，升级按差额补到 1,900：同一 `reference` 下留有两行 `adjustment`（200 与 1,700），幂等键不同（免费额 `grant:...:allowance:200000000`、付费额 `grant:...:allowance:1900000000`，见 `EnsureMonthlyAllowance`）；本月尚未领过免费额则只有一行 1,900 | “Subscribing with a test card returns to `?checkout=success`…” |
| 3 | 跑一次文本技能（如 60 credits 的 `content-creation-*` 或 `office-*`） | 创建即扣 60：流水一行 `kind=deduction`、`reference=<generationID>`；完成后 generation 为 `completed`；流水里该行能显示技能名（页面用 generation→skill 映射） | “A text generation debits 60–100 credits…” |
| 4 | 重投第 2 步的 `checkout.session.completed`：Dashboard → Events → 该事件 → Resend，或 `stripe events resend evt_...` | `200`；余额不变；不新增流水行（月度授予按 `grant:sub:<userID>:<YYYY-MM>:allowance:<micro>` 幂等，`EnsureMonthlyAllowance` 发现已授予即 no-op） | “the wallet gains the tier's monthly credits exactly once” |
| 5 | 制造一次失败生成（见下方触发方式） | 流水出现一行 `kind=refund`、`reference=<generationID>`；generation 为 `failed` | “A failed generation releases its reservation…” |
| 6 | 重投该任务的终态回调（daemon 的 complete/fail 重放，或让 sweeper 再跑一遍） | `200`；不再出现第二行 refund（settlement 只处理仍为 `queued` 的 generation；refund 以 generation id 为幂等键） | “…a replayed terminal callback releases nothing further” |
| 7 | 买 `t5` top-up | +500 一次；流水一行 `kind=topup`、`reference=cs_...` | “A top-up purchase adds exactly its credits once…” |
| 8 | 重投第 7 步的 Checkout 事件 | `200`；余额不变、无新行（grant 以 Checkout Session id 为键） | “…replaying the webhook adds nothing” |
| 9 | 把余额花到不足一次生成（验收清单写的是 0） | HTTP `402`，body 含 `error: "insufficient credits"` 与 `requiredCredits` / `availableCredits` / `shortfallCredits`（单位是 credits 而非 micro；`shortfall = max(0, required - available)`，见 `aurora.go` 的 `ErrInsufficientCredits` 分支）；composer 显示差多少 credits，没有这三个字段时回落到技能价格/余额文案；该挂载没有 top-up 链接（见下） | “With the balance at zero, create returns 402…” |
| 10 | Dashboard 发一次测试事件 | `200`，日志无 `invalid signature` | “The webhook answers 200 to a Stripe test event…” |
| 11 | 24h 结算循环（`server/cmd/server/aurora_settlement.go`，`auroraSettlementInterval = 24h`） | 当日已授予的月额度不会重复追加；上月未用余额在次月第一次结算时按 `expire` 扣减（如有） | —（补充观察，不在验收清单内） |
| 12 | 在 checkout 卡片上核对条款 | 两个购买控件旁都显示 `billing.checkout.no_refund`（见 T10） | “The checkout page states that all purchases are final…” |
| 13 | 一次完整文本生成到产物 | 生成详情有已提交 artifact；作品库可见 | “A text generation on the chosen model produces its artifact end-to-end” |

验收清单里另有两行不在 T4 的步骤内：一行是 “Deleting the subscription in Stripe flips the app to the free tier on the next read”，由 T6 第 2–3 步覆盖；一行是 “Unsetting `STRIPE_SECRET_KEY` makes checkout answer 503, not a broken button”，由 T2 的验证覆盖（在 test/staging 执行）。

失败生成的触发方式（test 环境）：最可控的是让后台任务失败——停掉/断开该 workspace 的 runtime，让任务被 sweeper 判失败，或让 daemon 上报 fail。失败结算路径是 `server/internal/service/aurora_completion.go` 的 `settleAuroraOnFailed`（任务失败时由 `server/internal/service/task.go` 的 `FailTask` 与 sweeper 汇总点调用）；成功但无产物的回调也会被判失败并退款。

**第 9 步说明（T4a 已落地）**：402 的三字段契约在本说明第一稿之后实现（T4a；`server/internal/handler/aurora.go` 的 `ErrInsufficientCredits` 分支）。`writeJSON` 返回 `error` 与 `requiredCredits` / `availableCredits` / `shortfallCredits`；若拒绝后的余额读取失败，则改为 `500 failed to load balance`。客户端 `getAuroraInsufficientCreditsDetails` 解析这三个字段，composer 用 `shortfallCredits` 显示差多少（`composer.shortfall`，中文文案“还差 N 积分。”），字段缺失（旧服务端/无法解码的 body）时回落到技能价格与钱包余额文案。**top-up 链接未接**：`GenerationComposer` 只在传入 `topUpHref` 时渲染那个 `AlertAction`，而本挂载 `apps/aurora/app/[workspaceSlug]/skills/connected/page.tsx` 没有传；把接上 `topUpHref` 作为后续改动，不要写进本次验收。

**第 11 步说明**：结算循环每天跑，`EnsureMonthlyAllowance` 与 `Expire` 都以月度为键幂等；当月重投 webhook 不会叠加授予。

## T6 — Entitlement read-back (#50)

**状态：步骤已写好；执行需要一次测试购买，本环境未执行。**

服务端读取路径（每次请求都读库，没有权益缓存）：

`GET /api/aurora/billing/subscription`（`router.go:2133`，`RequireHumanActor`）→ `GetAuroraSubscription`（`aurora.go`）→ `aurora.LimitsForUser`（`server/internal/aurora/entitlement.go:29`）→ `GetAuroraSubscriptionByUser`（`server/pkg/db/queries/aurora_subscription.sql:62`）。只有 `status == "active"` 才把生效 tier 提升为订阅 tier；无记录、`canceled`、`past_due` 以及其他任何状态都回落到 `free`；目录里已不存在的 tier 也回落 `free`。

客户端：`useAuroraSubscription`（`packages/core/aurora/queries.ts:269`）→ `GET /api/aurora/billing/subscription`（`packages/core/aurora/api.ts:76`）。billing 页在 `?checkout=success` 后的约一分钟内重复 refetch；一次失败的 refetch 不会把已有数据当成最新（`billing.tsx` 的 stale 处理）。强制刷新 = 重新 refetch / 重新加载页面，不是清缓存。

### 步骤

1. 按 T4 完成一次订阅购买，回到 billing 页。期望：套餐卡显示付费 tier（creator/pro/business）、本期结束时间、当月 generation 用量（如 `/30`、`/100`、`/300`）；钱包里已有该 tier 的月额度（1,900 / 5,900 / 16,900）。
2. 在 Stripe 侧取消订阅，有两条路径、行为不同：
   - Customer Portal（`POST /api/aurora/billing/portal`，若 #53 已上线）默认是**到期取消**：发 `customer.subscription.updated` 且 `cancel_at_period_end=true`；`handleSubscriptionUpsert` 仍把 status 记为 `active`，所以付费权益保留到当期结束（billing 页显示“到期后结束”）。
   - Dashboard → 该订阅 → Cancel immediately（或 `stripe subscriptions cancel sub_...`）是**立即取消**：发 `customer.subscription.deleted`，`handleSubscriptionDeleted` 把 status 记为 `canceled`。
3. 到期取消的订阅要等到期日 Stripe 发出 `customer.subscription.deleted`（立即取消则在 `deleted` 到达后）再读一次（重载页面或 refetch）。期望：生效 tier 回 `free`，上限 200 credits / 10 runs/月；已发的月额度**不回收**，可继续花到该月窗口结束（`handleSubscriptionDeleted` 的注释即此语义）。
4. 到期未付（past_due）：Stripe 把非 `active`/`trialing`/`canceled` 的状态一律映射为 `past_due`（`stripeSubscriptionStatus`），同样回落 free。要在测试里主动造出这个状态，需要 Stripe test clock 或一张续费会失败的测试卡，属可选步骤；本分支没有“把某订阅改成 past_due”的运维开关，若无法造出，就在 launch review 里记“past-due 仅有单元测试覆盖、未现场观察”，不要写成已验证。
5. 负向检查：从未订阅的账号 → free；`aurora_subscription` 行存在但 tier 已从目录移除 → free（`LimitsForUser` 的 fallback）。

“subscription active but the app still shows free” 的处理：先 refetch 一次；服务端不缓存权益，所以第二次读取仍为 free 说明订阅行的 `status` 不是 `active`——去查 webhook 是否到达、`STRIPE_WEBHOOK_SECRET` 是否与当前模式一致，而不是清客户端缓存。

## T7 — DeepSeek route spike (#51) — BLOCKED, decision recorded

**状态：BLOCKED；决策已记录。**

阻塞原因：owner 尚未提供 DeepSeek 的 endpoint 与 key。仓库里也没有 Aurora 文本路由的 DeepSeek 模型固定值：专家路由是一个标签常量 `expertRoute = "claude-text"`（`server/internal/aurora/experts_catalog.go:26`），具体模型来自 daemon/provider 配置而不是本仓库；`server/internal/metrics/pricing.go` 里的 `deepseek:v4-*` 只是计价行，不构成路由。

**决策（来自计划 §Decisions）：** 本次上线继续走当前 Claude 路由；切到 `deepseek-v4.1-flash-1017` 是一个独立改动，等凭证到位再做。它不在支付关键路径上，本说明的其它小节不依赖它。

### 如果凭证到达：2 小时硬超时 spike 程序

1. 按仓库既有方式注入 provider 凭证（节点 provider 环境、0600 私密文件、不进 Git），不把 key 写进仓库或 issue。
2. 把一个测试 workspace 的文本模型指向 `deepseek-v4.1-flash-1017`。
3. 跑一次 60 credits 的文本技能，直到产物落库。
4. 产出**一个**可核对的东西：该次生成的 generation id、artifact、ledger 行，以及实际使用的模型 id；把它记到本节的记录表。
5. 到 2 小时就停，不管进行到哪一步。若没有产物：在本节写下阻塞（凭证不可用/路由不可配/产物失败），保持 Claude 上线。

| spike 日期 | 模型 id | 结果（产物 / 阻塞） | 记录 |
|---|---|---|---|
|  |  |  |  |

## T8 — Cost check (#52)

**状态：本文计算，不是实测。** 没有付费运行数据：没有真实用户跑过量，本表用审计的冻结价目表与目录价计算（进度记录 R6）。

方法：`单次成本 = input_tokens/1e6 × input_rate + output_tokens/1e6 × output_rate`（USD），按审计冻结汇率 **USD 1 = SGD 1.28** 换成 S$；**100 credits = S$1**，所以 S$ 成本 ×100 即等值 credits。目录里文本技能收 60 / 80 / 90 / 100 credits（`server/internal/aurora/experts_catalog.go`；120 credits 的是媒体技能，不在 MVP 范围）。判断用最保守的 **60 credits 下限**，同时给出 100 credits 上限的结果。

费率（来源：`docs/superpowers/specs/2026-10-27-aurora-all-provider-pricing-audit.md` §C，HopBase 2026-10-26 报价，每 1M tokens）：

| 模型 | input US$/M | output US$/M |
|---|---:|---:|
| Claude Haiku 5.5 | 0.0279 | 0.14 |
| Claude Sonnet 5.5 | 0.559 | 2.79 |
| Claude Fable 5.1 | 2.79 | 14 |
| Claude Opus 5.5 | 1.12 | 5.59 |
| DeepSeek V4.1 Flash | 0.176 | 0.706 |

算例（Sonnet，典型量）：`0.03 × 0.559 + 0.01 × 2.79 = US$0.04467`，`× 1.28 = S$0.05718`，`÷ 0.01 = 5.72 credits`；60 credits 时毛利 `1 − 0.05718/0.60 = 90.5%`。

### 典型量：30,000 input + 10,000 output

| 模型 | 成本/次 S$ | 等值 credits | 毛利 @60 credits | 毛利 @100 credits | ≥75% @60 |
|---|---:|---:|---:|---:|---|
| Claude Haiku 5.5 | 0.00286 | 0.29 | 99.5% | 99.7% | 是 |
| Claude Sonnet 5.5 | 0.05718 | 5.72 | 90.5% | 94.3% | 是 |
| Claude Opus 5.5 | 0.11456 | 11.46 | 80.9% | 88.5% | 是 |
| Claude Fable 5.1 | 0.28634 | 28.63 | 52.3% | 71.4% | **否** |
| DeepSeek V4.1 Flash | 0.01580 | 1.58 | 97.4% | 98.4% | 是 |

### 10× 病态量：300,000 input + 100,000 output

| 模型 | 成本/次 S$ | 等值 credits | 毛利 @60 credits | 毛利 @100 credits | ≥75% @60 |
|---|---:|---:|---:|---:|---|
| Claude Haiku 5.5 | 0.02863 | 2.86 | 95.2% | 97.1% | 是 |
| Claude Sonnet 5.5 | 0.57178 | 57.18 | 4.7% | 42.8% | **否** |
| Claude Opus 5.5 | 1.14560 | 114.56 | −90.9% | −14.6% | **否** |
| Claude Fable 5.1 | 2.86336 | 286.34 | −377.2% | −186.3% | **否** |
| DeepSeek V4.1 Flash | 0.15795 | 15.80 | 73.7% | 84.2% | **否**（100 credits 时 84.2%） |

### 结论

- 典型量在 Haiku / Sonnet 上都明显高于 75%（假设上线路由是这两者之一）：Haiku 99.5%、Sonnet 90.5%（60 credits 下限）。DeepSeek 若上线，典型量 97.4%（按审计报价，非实测）。
- Opus 典型量 80.9% 仍过线；Fable 典型量 52.3% **不过线**，不应作为默认文本模型。
- 10× 病态量不过线：Sonnet 在 60 credits 时只有 4.7% 毛利，Opus/Fable 直接为负。这正是计划把 phase-2 计量（按 token 计费）留作后续的原因；本次靠固定 60–100 credits 的价格承担长文本风险。
- **未固定项**：上线路由的具体模型 id 不在仓库里（见 T7）。上面的 “Haiku/Sonnet” 是假设——需要和 daemon/provider 配置核对实际模型，再决定是否把结论写死。
- 本核算**不含支付手续费**（见下），也不含失败重试、缓存读写和长上下文档位；审计 §C 指出 Claude 的 cache 与 >100K prompt 档位尚无供应商报价，因此这里按标准单价估。

### 毛利背景（支付手续费，不计入上表）

按 Stripe 新加坡卡费率 **3.4% + S$0.50**（境内卡），国际卡再加 0.5%。手续费 = 金额 × 费率 + 0.50：

| 收费 | 手续费（境内） | 占比 | 手续费（国际卡 +0.5%） | 占比 |
|---|---:|---:|---:|---:|
| S$19 订阅 | S$1.146 | 6.03% | S$1.241 | 6.53% |
| S$59 订阅 | S$2.506 | 4.25% | S$2.801 | 4.75% |
| S$169 订阅 | S$6.246 | 3.70% | S$7.091 | 4.20% |
| S$5 top-up | S$0.670 | 13.40% | S$0.695 | 13.90% |
| S$20 top-up | S$1.180 | 5.90% | S$1.280 | 6.40% |

**小额 top-up 的费率最重**（S$5 有 13.4% 被手续费吃掉，是 S$169 订阅的 3.6 倍；S$20 按 3.4% + S$0.50 算是 5.9%，见上表）。以 Stripe 账号实际费率页为准；“小额 top-up 费率显著偏高”的结论不因此改变。订阅侧的积分按面值授予（100 credits = S$1），所以手续费直接压在每次收费的毛利上，不与 token 成本混算。

## T10 — No-refund operations (#54)

**状态：runbook 已写好；线上条款 code-complete（T5/#49），本环境未在浏览器里验证。**

### 条款

- 文案键：`billing.checkout.no_refund`，五个 locale 都有（`packages/views/locales/{en,zh-Hans,ja,ko,fr}/aurora.json`）：英文 “All purchases are final and non-refundable.”，中文“所有购买均为最终购买，不可退款。”
- 渲染位置：`packages/views/aurora/billing.tsx` 里两处 `$.billing.checkout.no_refund`——订阅购买卡片与 top-up 购买区，均在购买控件旁，付款前可见。
- 代码里**没有退款路径**：没有退款接口，也没有 Stripe refund 调用。结算里的“释放”是把未使用的预留 credits 退回钱包（`settleAuroraOnFailed`，`server/internal/service/aurora_completion.go`），不是退钱；已发放的月额度取消订阅也不回收。

### 支持 runbook

**1. 顾客要求退款**

按条款拒绝：购买为最终购买、不可退款，付款前已展示。不要承诺“下次抵扣”或任何金额补偿。**本次上线不带积分调整工具**：`CreditService.Grant` 在生产只有两个调用点——Stripe webhook 的 top-up 授予（`aurora.go` 的 `handleTopupCompleted`）与注册赠送（`server/internal/handler/auth.go`），没有运营接口或 CLI。任何积分调整都是工程侧的数据库操作，见第 2 条的规则；客服不能自助执行。

**2. “扣款了但没有积分”**

1. 先在 Stripe 找到对应的 Checkout Session / 事件，从 Dashboard → Events → Resend（或 `stripe events resend evt_...`）重投。
2. 确认响应 `200`、日志无签名错误，再核对该用户的流水。`GET /api/aurora/billing/transactions` 只解析会话里的调用者（`auroraBillingUser`，`aurora.go`），客服不能用它读别人的账本：要么用该用户自己的会话打开账单页，要么按 user id / `credit_ledger.idempotency_key` 直接查 `credit_ledger` 表。
3. 如果已有授予行：什么都不用做——积分已经在账上（前端可能只是没刷新）。
4. 如果确实没有：只有工程能补，且必须使用 webhook 会用的同一个身份，否则之后的正常重投会二次发放：
   - top-up：`kind = 'topup'`，`reference = <Checkout Session id，cs_...>`，`idempotency_key = 'grant:' + <同一个 cs_...>`，金额 = 包 credits × 1e6 micro。这与 `CreditService.Grant` 的键完全一致（`server/internal/aurora/credit.go:76-90`），`InsertCreditLedger` 的 `ON CONFLICT (idempotency_key) DO NOTHING` 会让后续重投变成 no-op。
   - 订阅月额度：`kind = 'adjustment'`，`reference = 'sub:<userID>:<YYYY-MM>'`，`idempotency_key = 'grant:sub:<userID>:<YYYY-MM>:allowance:<targetMicro>'`。月度结算用 `reference` 求和判断“本月已授予”（`SumMonthlyGrantByReference`），所以补写后结算不会重复发放。
   - 写的时候必须和余额更新在**同一个事务**里，形如 `credit.go` 的 `adjust`：`EnsureCreditBalance` → `CreditCreditBalance` → `InsertCreditLedger`（含 `balance_after_micro`）。不要只 `UPDATE credit_balance` 而不写 ledger：账实不符且失去幂等键。
5. 先重投，再考虑补写；能靠重投解决的永远不要手工写账。

**3. 拒付（chargeback）**

1. Stripe Dashboard → Payments → 找到该笔 → 进入 dispute，按 Stripe 给的截止时间提交证据：Checkout Session、付款前展示的不退款条款、已授积分与使用记录。
2. 在截止前提交；逾期视为放弃。
3. 结果出来后在下面登记。注意：拒付失败（钱被划走）时，代码不会回收已发积分（没有 clawback），这笔损失按运营损失记录，不要用“退积分”去对冲。

| 日期 | 收费/金额 | 类型（退款请求 / 拒付） | 处理与结果 | 记录人 |
|---|---|---|---|---|
|  |  |  |  |  |

## T12 — Live smoke test (#56)

**状态：owner + ops — 本环境未执行。**

用小额 top-up（S$5，500 credits）在内部账号上用真实卡跑一次。目的不是收款额，而是证明 live 模式的完整链路；积分保留，**不是**给客户的退款。

前置：

1. T1 的 live price id 已填进本文件的表，T2 的 live 环境变量已注入并重启。
2. T3 的 live endpoint 已注册，live signing secret 已写入 `STRIPE_WEBHOOK_SECRET`。注意：live endpoint 收不到 Dashboard 的 test event，第一笔真实付款就是它的第一次真实投递。
3. 账号是内部账号，卡是真实卡；金额上限 S$5。

### 有序清单

| # | 步骤 | 通过标准 |
|---|---|---|
| 1 | 核对 UI 与 Stripe：top-up 卡片显示 `$5` / 500 credits，live 价格 `unit_amount=500`、`currency` 与 UI 一致 | 两处数字完全相同 |
| 2 | 用内部账号在 live 模式买 `t5` | 跳转到 Stripe Checkout，金额 S$5（或决定的币种） |
| 3 | 完成支付，回到 `?checkout=success` | 页面在约 1 分钟内刷新出 500 credits |
| 4 | 看流水 | 恰一行 `kind=topup`，`reference=cs_...`，金额 +500 credits；无重复行 |
| 5 | 看服务端日志 | webhook `200`，无 `invalid signature`、无 `stripe webhook handling failed` |
| 6 | 积分保留 | 不给该笔做退款；不手工扣回 |
| 7 | 记录 | 填下面的记录表 |

### 记录表（owner 填）

| 日期时间（含时区） | 金额/币种 | Stripe 对象 id（session / charge） | ledger reference（`cs_...`） | 结果 | 记录人 |
|---|---|---|---|---|---|
|  |  |  |  |  |  |

### 回滚 / 中止条件

- **付款前中止**（不产生任何资金动作）：UI 价格 ≠ Stripe 金额或币种；live price id 与 T2 环境变量不一致；live endpoint 未注册或 secret 未注入；Checkout 返回 503/400。
- **付款后不允许“退款回滚”**：本产品不退款。若扣款成功但没有积分，走 T10 第 2 条（重投 → 必要时按同一幂等键补写），不要用退款“撤销”。
- 若出现重复授予：不要删账本行（append-only），记录并升级工程处理；用户的可用余额以账本为准。
- 若 webhook 在 15 分钟内未到达：停止继续下单，先修 T3，再重试一次小额；已付款的那笔按 T10 处理。

## Still open

- **币种**：SGD 还是 USD（计划 §Still to confirm）。Price 的币种不可变，必须在创建 live 价格前决定；UI 目前用 `$`，定价数字不变，仅币种改变。
- **DeepSeek 凭证**：owner 未提供 endpoint/key。T7 阻塞，上线继续用 Claude；切换为独立改动。
- **年付 11/12**：不在本次范围；`AURORA_STRIPE_PRICE_*_YEARLY` 保持空，`business` 无年付槽位。
- **本说明核出的偏差与后续，需在 launch review 处理**：(a) 402 的 `requiredCredits`/`availableCredits`/`shortfallCredits` 已由 T4a 落地（见 T4 第 9 步），composer 在本挂载没有 top-up 链接（`topUpHref` 未传），接上属后续改动；(b) past-due 状态没有可用的现场造法，只有单元测试覆盖；(c) T8 的模型路由是假设，需与 daemon/provider 配置核对。
