# Aurora 订阅接入 Antom 支付渠道

日期：2026-10-28
状态：书面规格，待确认
关联：apexai-sg/multica#57（两天可付费订阅 MVP）、#58（该 MVP 的阻塞票记录）

## 1. 意图

Aurora 的可付费订阅（PR #57）代码已经完成，但 6 张票卡在 Stripe 上：环境里没有
`STRIPE_*` 凭证，而新开 Stripe 账号要走 KYB。公司手上已经有 Antom（蚂蚁国际 Ams）
商户号，因此把 Antom 作为**第二个订阅支付渠道**接进来：Stripe 代码原样保留，计费页
让用户自己选渠道，哪条渠道先通就先卖哪条。

Antom 的订阅模型和 Stripe 不同，这是这份设计存在的原因：Antom 由商家调接口创建
订阅 → 用户跳到 Antom 授权页授权 → **Antom 每个周期自动扣款**，并通过
`notifyPayment（订阅）` 把每一期的扣款结果推给商家；订阅本身的状态变更走另一条
`notifySubscription`。也就是说，Stripe 的一条 webhook 在这里变成两条，且请求/通知
都要 RSA 签名与验签。

## 2. 目标

- 计费页的订阅卡同时给出 Stripe / Antom 两个入口，用户二选一；只有一个渠道可用时
  只显示可用的那个。
- Antom 订阅 creator / pro / business 三档月付，金额沿用现有 S$19 / S$59 / S$169。
- 用户在 Antom 授权成功后，订阅行落到与 Stripe **同一张** `aurora_subscription`，
  档位、状态、当期结束时间、月度授信全部与 Stripe 语义一致。
- 每期扣款成功（`notifyPayment（订阅）`）后发放当自然月积分；重复通知不会重复发放。
- 订阅终止（`notifySubscription` 的 `TERMINATE` / `CANCEL`）能让权益按现有规则回落。
- Antom 订阅的取消在应用内完成（Antom 没有 Stripe Customer Portal 这种托管页面）。
- Stripe 渠道的行为逐字节不变：接口、事件、表字段、失败语义都不动。

## 3. 非目标

- 充值包（t5 / t20）不走 Antom。一次性支付是另一条接口线，等订阅跑通再单独做。
- 不做年付。Antom 侧这一期只卖月付。
- 不迁移已有 Stripe 订阅到 Antom，也不做渠道互转。
- 不做 Antom 退款 / 争议处理（#54 仍按 Stripe 写，Antom 侧补运维 SOP 即可）。
- 不改 Stripe Customer Portal（#53 的 Stripe 部分保持原方案）。
- 不做计费页的渠道记忆或默认渠道偏好。
- 不改积分定价、预留、结算与退款逻辑。
- 不接 Antom 的 `notifyRefund` / `notifyDispute`，收到也按未知通知确认掉。

## 4. 反目标

出现下面任何一条，即使验收项全打勾也是失败：

- 用户选 Antom 却拿到 Stripe 的结账页（或反之）。
- 重复通知、乱序通知导致一个月发放两次积分，或终止后又被「复活」。
- 通知验签失败仍然写入积分或订阅状态。
- Antom 未配置时 Stripe 渠道被连带关闭，或计费页显示了一个点了 503 的按钮。
- 金额取自请求体：客户端改一下数字就能用 S$1 买 business。

## 5. 现状（代码事实）

- `server/internal/aurora/stripe.go`：`PaymentProvider` 接口 4 个方法
  （`CreateSubscriptionCheckout` / `CreateTopupCheckout` / `GetSubscriptionState` /
  `ConstructEvent`），`NewStripeProvider` 双密钥缺一即返回 nil。
- `server/internal/handler/aurora.go`：
  - `CreateAuroraCheckout` 校验 `successUrl`/`cancelUrl` 只落可信 origin，按
    `billingCycle` 取 price id，空 id 即 503。
  - `beginAuroraSubscriptionCheckout` 先落一条 `pending` 意图，`user_id` 唯一，
    以 `checkout_idempotency_key` 作幂等键（25 小时 TTL）。
  - `StripeWebhook` 挂在公开路由 `/api/aurora/billing/stripe/webhook`，先验签再读体。
  - 授信走 `grantSubscriptionAllowance` → `Credit.EnsureMonthlyAllowance`，按自然月
    幂等；订阅状态机只有 `active` / `past_due` / `canceled`。
- `server/migrations/512`、`515`：`aurora_subscription` 一用户一行，
  `stripe_customer_id` / `stripe_subscription_id` / `stripe_event_created_at` 等列。
- 前端：`packages/core/aurora` 的 `createAuroraCheckout` 把后端返回的
  `checkoutUrl` 交给 `openExternal`，返回页轮询订阅行（2s × 30 次）。
- 已有可借鉴实现：`eanfs/sub2api` 分支 `feat/antom-hosted-checkout` 的
  `backend/internal/payment/provider/antom.go`（自实现 RSA 签名 / 验签，未引 SDK）。

## 6. 设计

### 6.1 渠道抽象与选择

Stripe 的 `PaymentProvider` 保持原样，不动签名。新增一个平行的
`server/internal/aurora/antom.go`：

```go
type AntomSubscriptions interface {
    // CreateSubscription 返回 Antom 授权页 URL（normalUrl）。
    CreateSubscription(ctx context.Context, req AntomSubscriptionRequest) (url string, err error)
    // CancelSubscription 调 /subscriptions/cancel，type=CANCEL 表示期未停止。
    CancelSubscription(ctx context.Context, subscriptionID string) error
    // InquirySubscription 用于对账，返回订阅状态与当期起止。
    InquirySubscription(ctx context.Context, subscriptionID string) (AntomSubscriptionState, error)
    // VerifyNotification 验签并解析两类通知。
    VerifyNotification(method, path string, headers http.Header, body []byte) (AntomNotification, error)
}
```

Handler 持仓从「一个 `h.Payments`」变成「`h.Payments`（Stripe）+ `h.Antom`」，
任一个为 nil 就表示该渠道未配置。`NewAntomProvider` 与 `NewStripeProvider` 同语义：
配置不全返回 nil，不 panic。

渠道在请求里显式指定：`POST /api/aurora/billing/checkout` 的请求体新增
`provider` 字段（`stripe` | `antom`），**缺省 `stripe`**，未知值 400。缺省值是给
已发布客户端的兼容层，不是给前端偷懒用的。

「哪些渠道可用」由服务端说，不由前端猜：`GET /api/aurora/billing/subscription`
的响应加 `availableProviders: string[]`（`parseWithFallback` + 默认 `[]`）。
渠道可用 = provider 已配置 **且** 该档位在这个渠道有可用金额；Antom 渠道这一期
只支持 `monthly`。

### 6.2 订阅创建

Antom 用 `POST /ams/api/v1/subscriptions/create`：

| 字段 | 取值 |
| --- | --- |
| `subscriptionRequestId` | 复用现有 `checkout_idempotency_key`（`aurora-sub-<uuid>`，< 64 字节） |
| `subscriptionDescription` | `<tier> monthly` 一类稳定文案 |
| `subscriptionRedirectUrl` | 前端传入的 `successUrl`（Antom 只有授权后回跳，没有 cancelUrl 概念） |
| `subscriptionStartTime` | 请求时刻 |
| `periodRule` | `{ periodType: "MONTH", periodCount: "1" }` |
| `paymentMethod` | `{"paymentMethodType": "CARD"}`（自动扣款需要可留存的卡；商户号需开通卡收单） |
| `env` | `{"terminalType": "WEB"}`（Antom 必填） |
| `settlementStrategy` | `{}`，或在配置了结算币种时 `{"settlementCurrency": "<币种>"}`（Antom 必填） |
| `subscriptionNotificationUrl` | `{PublicURL}/api/aurora/billing/antom/notify/subscription` |
| `paymentNotificationUrl` | `{PublicURL}/api/aurora/billing/antom/notify/payment` |
| `orderInfo` / `paymentAmount` | 服务端从档位表取，**绝不读请求体** |

回调 host 用 `cfg.PublicURL`（API 从公网可达的绝对地址），不是 `cfg.AppURL`：本仓库的
Aurora 前端（`aod`）与后端（`mcapi`）是两个 host，`PublicURL` 与 `AppURL` 的分离就是
为这种部署留的（现有回调 URL 也都用 `PublicURL`）。`PublicURL` 为空时 Antom 渠道不启用
——通知发不到，渠道卖出去也是废的。

`/subscriptions/create` 的响应只有授权链接（`normalUrl` / `applinkUrl` / `schemeUrl`），
**不返回 `subscriptionId`**。所以 `antom_subscription_id` 只能在第一条通知到达时落库，
本地行的匹配主键是两条通知都携带的 `subscriptionRequestId`（即我们自己的
`checkout_idempotency_key`）。

金额表长在 `tiers.go` 的档位结构上（`AntomAmountMinorMonthly` + 币种常量 `SGD`），
与 Stripe price id 并列，同一个文件是改价的唯一位置。**Antom 的 `Amount.value` 是币种
最小单位的整数字符串**（`"1900"` = S$19.00，查询接口的 `"10000"` = USD 100.00 也是同一
口径），不是 `"19.00"`。

`cancelUrl` 在 Antom 渠道不使用：用户中途关页没有回调，前端沿用现有的返回页轮询，
订阅行仍是 `pending` 时就显示「未完成」。这一点写进接口注释，免得后来的人以为漏了。

### 6.3 通知与验签

两条公开路由，都不挂认证，靠签名认人：

- `POST /api/aurora/billing/antom/notify/payment` —— `notifyPayment（订阅）`
- `POST /api/aurora/billing/antom/notify/subscription` —— `notifySubscription`

验签规则（Antom「签名和验签」）：待签名内容是
`<method> <path>\n<client-id>.<request-time>.<body>`，`signature` 头是
`algorithm=RSA256,keyVersion=1,signature=<urlencoded base64>`，用 Antom 公钥
`sha256withRSA` 验。`client-id` 必须等于配置值，`keyVersion` 必须等于配置值。

处理顺序：验签 → 解析 → 业务写入 → 返回固定确认报文

```json
{"result":{"resultCode":"SUCCESS","resultStatus":"S","resultMessage":"success"}}
```

- 验签失败：401，不确认（Antom 会重试；重试也验不过，日志里必须显眼）。
- 业务失败（数据库抖动等）：500，不确认，让 Antom 重试。
- 未知通知类型或未知 tier：**确认掉**（200），只记日志 —— 重试修不好一条永久畸形
  的通知，这与现有 Stripe webhook 的取舍一致。

Antom 的重试策略是 24 小时内最多 8 次（0s / 2m / 10m / 10m / 1h / 2h / 6h / 15h）。
**沙箱不重试**：格式不对就直接丢，所以沙箱联调必须看日志确认每一次都回了确认报文。

告警：`multica_aurora_billing_webhook_events_total` 的 `event` label 是固定枚举
（`knownAuroraWebhookEvents`），Antom 的通知会被归一成 `other`，按事件类型的告警规则
出不来线。新增两个 label —— `antom_payment_notify` 与 `antom_subscription_notify` ——
并加进 `AllAuroraWebhookEvents()` 的预热列表，让 5xx / 验签失败能按渠道分开看。

### 6.4 订阅状态与授信

本地行按这个顺序匹配，命中即停：

1. `subscriptionRequestId` → `checkout_idempotency_key`：pending 行也认，所以首期扣款
   一定落得下来。
2. `subscriptionId` → `antom_subscription_id`：请求 ID 那条路断了（例如本地行被人工
   改过）时的兜底。
3. 都没命中 → **确认掉 + 告警**，不重试。重试造不出一行本地记录，只会让 Antom 重投
   八次。

Antom 的通知**没有可靠的统一时间戳**（`notifySubscription` 报文里根本没有时间字段），
所以这里不用「事件时间排序」那套，改用**显式状态转移**，让任何到达顺序都收敛到同一个
终态：

`notifyPayment（订阅）`：

| 通知 | 本地写入 |
| --- | --- |
| `resultStatus = "S"` | `current_period_end = periodEndTime`；`antom_subscription_id = subscriptionId`；`pending`/`past_due`/`active` → `active`；随后发放当月积分 |
| `resultStatus != "S"` | 状态置 `past_due`（停发、权益回落 free 档）；不动 `current_period_end` |
| 行已 `canceled` 且 `subscriptionId` 与行上的一致 | **忽略**（终止是终态） |

`notifySubscription`：

| `subscriptionNotificationType` | 本地状态 |
| --- | --- |
| `CREATE` / `UPDATE` / `CHANGE`（`subscriptionStatus=ACTIVE`） | `active` |
| `CANCEL` | 保持 `active`，`cancel_at_period_end = true` |
| `TERMINATE`（`subscriptionStatus=TERMINATED`） | `canceled`，`cancel_at_period_end = false` |
| 行已 `canceled` 且 `subscriptionId` 与行上的一致 | 除 `TERMINATE` 外一律忽略 |

「新订阅」的判定是 `subscriptionId` 与行上不同：那是一次重新订阅，允许把 `canceled`
改回 `active` 并换掉 ID。这样迟到的 `CREATE` 复活不了已终止的订阅，而真正的重新订阅
也不会被终态挡住。

积分发放走 `EnsureMonthlyAllowance`，按自然月幂等：同一期重复通知、跨期乱序通知都
不会多发。失败通知不发积分，且状态已降到 `past_due`，结算扫描（只认
`status='active' AND current_period_end > now()`）自然也不会补发。

### 6.5 取消

新增 `POST /api/aurora/billing/subscription/cancel`（需登录，账户级）：

- `payment_provider = antom` → 调 `/subscriptions/cancel`（`cancellationType=CANCEL`），
  成功后本地置 `cancel_at_period_end = true`；Antom 侧失败原样透出 502，不改本地状态。
- `payment_provider = stripe` → 返回 409 并给出 Portal 提示，Stripe 侧仍走 Portal。

计费页在「Antom 渠道 + active + 未设置期未取消」时显示取消按钮。这是 #53 在 Antom
侧的等价实现。

### 6.6 数据模型

一个迁移（编号取 5001，`5000` 已被 `aurora_carrier_invocation` 占用）：

- `payment_provider TEXT NOT NULL DEFAULT 'stripe'` —— 订阅属于哪个渠道。
- `antom_subscription_id TEXT` —— Antom 的订阅 ID；Stripe 订阅继续用
  `stripe_subscription_id`。

`stripe_event_created_at` **不改名、不参与 Antom 写入**：Antom 的状态收敛靠 6.4 的显式
转移，不靠时间排序，所以那条 Stripe 专属的排序规则原样留在 Stripe 路径上。Antom 另有
一条专用 upsert（`UpsertAuroraAntomSubscription`），它的 `ON CONFLICT` 分支按
「已 `canceled` 且 subscriptionId 相同 → 不改」判重，而不是按时间戳。

索引按仓库规矩另起一个迁移文件、单语句 `CREATE UNIQUE INDEX CONCURRENTLY`：
`antom_subscription_id` 唯一（`WHERE antom_subscription_id IS NOT NULL`）。

`pending` 意图复用现有列：`checkout_idempotency_key` 同时充当 Antom 的
`subscriptionRequestId`（`aurora-sub-<uuid>`，47 字符，在 Antom 的 64 字符上限内），
`checkout_billing_cycle` 记录 `monthly`。

冲突判定：只要存在非 `canceled` 的行就 409，不区分渠道——同一用户不能同时持有一份
Stripe 订阅和一份 Antom 订阅。已经有一个**未完成的、属于另一渠道的** pending 意图时
也 409，提示用户先完成或等它过期（25 小时）。选 409 而不是替换，是因为被替换掉的
Stripe Checkout Session 用户仍可能完成，随后 webhook 匹配不到 pending 意图会持续 500、
Stripe 重试三天；拒绝一次只是让用户等，不会制造一个永远失败的 webhook 回路。

### 6.7 API 契约变化

| 变更 | 兼容策略 |
| --- | --- |
| `POST /billing/checkout` 新增 `provider` | 缺省 `stripe`；未知值 400 |
| `GET /billing/subscription` 新增 `availableProviders` | 客户端 schema 默认 `[]`，未知值丢弃 |
| 新增 `POST /billing/subscription/cancel` | 新路由，无兼容问题 |

### 6.8 前端

`packages/views/aurora/billing.tsx` 的订阅档位列表：档位行右下的按钮从「一个升级
按钮」变为按 `availableProviders` 渲染的渠道按钮组（`Stripe` / `Antom`，文案走
i18n，五个 locale 都要补）。只有一个渠道时不渲染选择，保持现在的单按钮观感。

`CreateAuroraCheckoutRequest` 增加 `provider`，`packages/core/aurora/api.ts` 原样
调用；返回仍是 `checkoutUrl`，`openExternal` 逻辑不变（Antom 的 `normalUrl` 就是
一条 https 链接）。

取消按钮的 pending 态与失败提示复用现有 mutation 的写法；成功后
`invalidate` 订阅查询。

### 6.9 配置与 fail-closed

| 环境变量 | 说明 |
| --- | --- |
| `ANTOM_CLIENT_ID` | 商户 clientId（沙箱与生产不同） |
| `ANTOM_MERCHANT_PRIVATE_KEY` | 商户 RSA 私钥（PEM 或 base64 DER，≥2048 位） |
| `ANTOM_PUBLIC_KEY` | Antom 公钥，用于验签 |
| `ANTOM_KEY_VERSION` | 缺省 `1` |
| `ANTOM_API_BASE` | 区域网关，缺省 `https://open-sea-global.alipay.com`，只接受官方域名 |
| `ANTOM_CURRENCY` | 缺省 `SGD` |

缺任意关键项 → `h.Antom == nil` → Antom 渠道不出现在 `availableProviders`，
Stripe 不受影响；反向也成立。金额不进环境变量（不是 Stripe 那种双模式 id）。

另外两条依赖现有配置：

- `MULTICA_PUBLIC_URL`（`cfg.PublicURL`）必须能拼出回调地址；为空时 Antom 渠道不
  启用——通知地址都拼不出来的渠道，卖出去就是一笔收不到账的订阅。
- 回调路径本身不进 WAF 豁免：它们是普通 JSON POST，靠签名认人。

### 6.10 运维前置（不在代码里，但会挡住上线）

1. Antom 商户号需要**开通订阅（Subscription）产品**与 **SGD 收款**。
2. `/api/aurora/billing/antom/notify/*` 必须能从公网到达，并且**不属于**需要
   WAF 豁免的路径（有签名，正常请求就是普通 JSON POST）。按现有结构性债（WAF 按
   host 逐条列支）确认这两条路径不被规则拦。
3. 沙箱与生产的 clientId/密钥分开保存，走部署侧的密钥注入流程。

## 7. 关键取舍

- **平行 provider，不做统一抽象。** Stripe 的事件模型（Checkout Session +
  Subscription 生命周期）和 Antom（订阅创建 + 每期扣款通知）压不进一个接口，硬抽
  会把两边都拧成对方的样子。两个接口、两个 handler、一张表，比一个四处分支的
  `PaymentProvider` 更可读。
- **金额由服务端档位表给出。** Antom 允许请求里带金额，这正是被篡改的入口；金额不进
  请求体，也不进环境变量。
- **不改 `stripe_event_created_at`，也不让 Antom 用它。** 改名的收益只是好读，代价是
  动 Stripe 路径的全部查询；而 Antom 的状态收敛用显式转移本来就比时间排序稳。
- **换渠道的 pending 意图用 409 拒绝，不替换。** 替换会给「用户随后完成旧 Stripe
  会话」留下一个永远 500 的 webhook 回路；拒绝一次只是等 25 小时。
- **取消用 CANCEL 而不是 TERMINATE。** 用户取消不该立刻断服务，与现有
  `cancelAtPeriodEnd` 语义一致；TERMINATE 只在 Antom 侧真的终止时反映。

## 8. 风险

- **订阅产品/币种未开通**：`/subscriptions/create` 会返回业务失败码，用户看到的是
  「暂时无法结账」。上线前必须先跑通沙箱 create。
- **沙箱不重试**：确认报文格式错一次就等于丢一期扣款通知。所有确认分支共用同一个
  序列化函数，并用测试锁住字段与大小写。
- **通知乱序 / 重复**：靠自然月幂等 + 事件时间排序 + `canceled` 优先兜底；缺任何
  一条都会变成多发积分或复活订阅，验收要显式覆盖。
- **弃单**：Antom 没有 cancelUrl，中途关页的意图会留到 TTL 过期；`pending` 行不
  影响权益，但要在返回页文案上讲清楚「未完成」。
- **对账缺口**：只靠通知，Antom 漏推或我方长时间 5xx 会丢期。这一期不做定时 inquiry
  对账（非目标），但 `InquirySubscription` 要能在运维手册里手动跑（launch note 记录
  命令与判读方式），不新开管理路由——多一个带鉴权的对外面，比少一次手动对账更贵。

## 9. 验收

- 沙箱：创建订阅 → 授权 → 收到 `notifyPayment（订阅）` → 当月积分到账；重复投同一条
  通知不重复发。
- 沙箱：`notifySubscription` 的 CREATE / CANCEL / TERMINATE 分别落到 `active` /
  `active + cancelAtPeriodEnd` / `canceled`；乱序投递不改变最终态。
- 伪造签名、错 clientId、错 keyVersion 一律 401 且无写入。
- Stripe 渠道：现有测试全绿，端到端行为不变。
- 前端：只有 Antom 配置时只显示 Antom；两个都配置时显示两个按钮；点击各自拿到对应
  渠道的结账链接。
- 金额：把请求体塞进金额/币种字段不改变实际扣款金额（结构上不存在该字段）。

## 10. 未决问题（实现前由 owner 确认）

1. 沙箱联调用的商户号是否已开通订阅产品与 SGD？未开通则 #48/#50 的验收只能停在
   「代码完成」。
2. `paymentMethod.paymentMethodType` 具体用哪个（卡 / 钱包 / 全部由 Antom 收银台决定）？
3. 计费页两个渠道按钮的文案与顺序（推荐：Stripe 在前，Antom 在后）。
