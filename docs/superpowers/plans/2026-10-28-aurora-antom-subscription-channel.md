# Aurora Antom 订阅渠道 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在保留现有 Stripe 订阅通道的前提下，新增 Antom（蚂蚁国际 Ams）订阅渠道：用户可在计费页二选一，Antom 每期自动扣款后经 `notifyPayment（订阅）` 发放当月积分。

**Architecture:** 两个平行的 provider 实现，不做统一抽象。Stripe 的 `aurora.PaymentProvider` 原样保留；新增 `aurora.AntomProvider` 与自实现的 RSA2 签名/验签（不引 SDK）。两个渠道共用 `aurora_subscription` 表，用 `payment_provider` 区分，通知写入全部幂等。

**Tech Stack:** Go（Chi、sqlc、pgx）、Next.js/React（TanStack Query + zod）、PostgreSQL。

**Spec:** `docs/superpowers/specs/2026-10-28-aurora-antom-subscription-channel-design.md`

## Global Constraints

- 价格：creator S$19 / pro S$59 / business S$169，均为**月付**；金额只从服务端档位表取，请求体不接受金额或币种。
- 币种：`SGD`，Antom `Amount.value` 为两位小数字符串（`"19.00"`）。
- Antom 签名待签名内容逐字节为 `<method> <path>\n<client-id>.<request-time>.<body>`，算法 `sha256withRSA`，`signature` 头格式 `algorithm=RSA256,keyVersion=<v>,signature=<urlencoded base64>`。
- 通知确认报文逐字节为 `{"result":{"resultCode":"SUCCESS","resultStatus":"S","resultMessage":"success"}}`。
- 验签失败返回 401 且不写入；业务失败返回 500 让 Antom 重试；未知通知类型/未知档位返回确认报文（200）。
- 新增迁移编号从 **5001** 起（5000 已占用）；并发索引各占一个单语句迁移文件。
- Stripe 路径行为不变：接口、事件、失败语义、既有测试全部保持通过。
- 代码注释用英文；UI 文案走 i18n，五个 locale（en / fr / ja / ko / zh-Hans）必须同时补齐。

## Review Focus

- **重复/乱序通知**：同一期扣款通知投两次、`TERMINATE` 之后迟到的 `CREATE`，都不能多发积分或复活订阅。
- **伪造通知**：缺签名头、错 `client-id`、错 `keyVersion`、改过 body 的请求，一律 401 且数据库无写入。
- **金额篡改**：请求体里塞 `amount` / `currency` / `price` 字段不改变实际扣款金额。
- **渠道可用性**：只配置一个渠道时，前端不显示另一个渠道的按钮；未配置的渠道不能连带关闭 Stripe。
- **取消语义**：Antom 的 `CANCEL`（期未停止）与 `TERMINATE`（立即终止）在本地必须分别落到 `cancel_at_period_end=true` 与 `status=canceled`。

---

### Task 1: Antom 配置与请求签名

**Files:**
- Create: `server/internal/aurora/antom.go`
- Test: `server/internal/aurora/antom_test.go`

**Interfaces:**
- Consumes: 无
- Produces:
  - `type AntomConfig struct { ClientID, MerchantPrivateKey, AntomPublicKey, KeyVersion, APIBase, Currency string }`
  - `func NewAntomClient(cfg AntomConfig) (*AntomClient, error)`
  - `func antomSignContent(method, path, clientID, requestTime string, body []byte) []byte`
  - `func (c *AntomClient) signatureHeader(requestTime string, body []byte) (string, error)`

- [ ] **Step 1: Write the failing tests**

`server/internal/aurora/antom_test.go`（纯单测，不需要数据库）：

```go
func TestNewAntomClientRequiresCredentials(t *testing.T) // 缺 ClientID / 私钥 / 公钥，各自返回 error，且错误文本包含字段名
func TestNewAntomClientRejectsNonOfficialBaseURL(t *testing.T) // APIBase = "https://evil.example.com" → error
func TestNewAntomClientDefaults(t *testing.T) // APIBase 为空 → "https://open-sea-global.alipay.com"；Currency 为空 → "SGD"；KeyVersion 为空 → "1"
func TestAntomSignContentIsExact(t *testing.T) // method=POST, path=/ams/api/v1/subscriptions/create, clientID=SANDBOX_1, requestTime=1685599933871, body={"a":1}
// 期望字节：`POST /ams/api/v1/subscriptions/create\nSANDBOX_1.1685599933871.{"a":1}`
func TestAntomSignatureHeaderRoundTrips(t *testing.T) // 用生成的私钥签名，再用同文件解析出的公钥验签通过；篡改 body 后验签失败
func TestNewAntomClientRejectsWeakKey(t *testing.T) // 1024 位私钥 → error
```

生成测试密钥用 `crypto/rsa` + `crypto/x509` 在 helper `newTestAntomKeys(t) (privPEM, pubPEM string)` 里完成，不要提交固定私钥。

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd server && go test ./internal/aurora/ -run 'TestAntom|TestNewAntom' -v`
Expected: FAIL，`undefined: NewAntomClient` / `undefined: antomSignContent`

- [ ] **Step 3: Implement the client core**

在 `server/internal/aurora/antom.go` 实现：

- `AntomClient` 字段：`cfg AntomConfig`、`privateKey *rsa.PrivateKey`、`publicKey *rsa.PublicKey`、`http *http.Client`（`Timeout: 30s`，`CheckRedirect` 返回 `http.ErrUseLastResponse`）。
- 密钥解析：接受 PEM 或 base64 DER；私钥先 `ParsePKCS1PrivateKey`，失败再 `ParsePKCS8PrivateKey`；拒绝 `< 2048` 位。
- `APIBase` 白名单：`https://open-sea-global.alipay.com`、`https://open-na-global.alipay.com`、`https://open-de-global.alipay.com`、`https://open.antglobal-us.com`、`https://open-sea.alipay.com`、`https://open-na.alipay.com`；其余返回 error。
- `antomSignContent` 按 Global Constraints 的字节格式拼接；`signatureHeader` 用 `rsa.SignPKCS1v15` + `crypto.SHA256`，base64 后 `url.QueryEscape`，拼成 `algorithm=RSA256,keyVersion=<v>,signature=<escaped>`。

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd server && go test ./internal/aurora/ -run 'TestAntom|TestNewAntom' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add server/internal/aurora/antom.go server/internal/aurora/antom_test.go
git commit -m "feat(aurora): add Antom client config and RSA2 request signing"
```

---

### Task 2: Antom 订阅接口调用（create / cancel / inquire）

**Files:**
- Modify: `server/internal/aurora/antom.go`
- Test: `server/internal/aurora/antom_test.go`

**Interfaces:**
- Consumes: Task 1 的 `AntomClient`、`signatureHeader`、`antomSignContent`
- Produces:
  - `type AntomProvider interface { CreateSubscription(ctx context.Context, req AntomSubscriptionRequest) (string, error); CancelSubscription(ctx context.Context, subscriptionID string) error; InquirySubscription(ctx context.Context, subscriptionID string) (AntomSubscriptionState, error); VerifyNotification(method, path string, headers http.Header, body []byte) (AntomNotification, error) }`
  - `type AntomSubscriptionRequest struct { RequestID, Tier, Amount, Currency, RedirectURL, PaymentNotifyURL, SubscriptionNotifyURL string; StartTime time.Time }`
  - `type AntomSubscriptionState struct { SubscriptionID, Status string; CurrentPeriodEnd time.Time }`
  - 路径常量：`/ams/api/v1/subscriptions/create`、`/ams/api/v1/subscriptions/cancel`、`/ams/api/v1/subscriptions/inquireSubscription`

- [ ] **Step 1: Write the failing tests**

用 `httptest.NewServer` 作网关，`AntomClient.cfg.APIBase` 指向它（测试内允许非官方 origin：构造后直接改 `client.cfg.APIBase`，并让测试文件在同一包内）：

```go
func TestCreateSubscriptionPostsSignedRequest(t *testing.T)
// 断言：方法 POST、路径 /ams/api/v1/subscriptions/create、头 client-id / request-time / signature 齐全，
// 且 signature 能用测试公钥验签；body 里 subscriptionRequestId == "aurora-sub-test"，
// paymentAmount.value == "19.00"、currency == "SGD"；
// 返回 {"result":{"resultStatus":"S","resultCode":"SUCCESS"},"normalUrl":"https://checkout.example/x"} → 返回该 URL
func TestCreateSubscriptionRejectsFailedResult(t *testing.T) // resultStatus=F → 返回 error，错误文本包含 resultCode
func TestCancelSubscriptionSendsCANCEL(t *testing.T) // body cancellationType == "CANCEL"，subscriptionId 透传
func TestCancelSubscriptionRejectsFailedResult(t *testing.T)
func TestInquirySubscriptionReadsState(t *testing.T)
// 返回 {"result":{"resultStatus":"S"},"subscriptionId":"S1","subscriptionStatus":"ACTIVE",
//      "periodRule":{...},"currentPeriodEndTime":"2026-11-28T12:00:00+08:00"}
//   → AntomSubscriptionState{SubscriptionID:"S1", Status:"ACTIVE", CurrentPeriodEnd:解析值}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd server && go test ./internal/aurora/ -run 'TestCreateSubscription|TestCancelSubscription|TestInquirySubscription' -v`
Expected: FAIL，`undefined: AntomProvider` / 方法未实现

- [ ] **Step 3: Implement the three calls**

共用一个 `func (c *AntomClient) post(ctx context.Context, path string, body any) ([]byte, error)`：
序列化 body → 取 `request-time`（毫秒）→ 加 `client-id` / `signature` / `Content-Type: application/json` → POST → 读响应 → 解析 `result.resultStatus`，非 `"S"` 返回 `fmt.Errorf("antom %s failed: %s %s", path, resultCode, resultMessage)`。

`CreateSubscription` 的请求体字段严格按 Spec 6.2 的表格；`normalUrl` 为空时返回 error（fail closed）。

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd server && go test ./internal/aurora/ -run 'TestCreateSubscription|TestCancelSubscription|TestInquirySubscription' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add server/internal/aurora/antom.go server/internal/aurora/antom_test.go
git commit -m "feat(aurora): call Antom subscription create, cancel and inquiry"
```

---

### Task 3: 通知验签与解析

**Files:**
- Modify: `server/internal/aurora/antom.go`
- Test: `server/internal/aurora/antom_test.go`

**Interfaces:**
- Consumes: Task 1 的 `antomSignContent`、公钥
- Produces:
  - `type AntomNotification struct { Type, RequestID, SubscriptionID, Status, NotificationType, PaymentID, PhaseNo string; PeriodStart, PeriodEnd, OccurredAt time.Time }`
  - `func (c *AntomClient) VerifyNotification(method, path string, headers http.Header, body []byte) (AntomNotification, error)`
  - `Type` 取值：`"payment"`（含 `paymentId`+`periodStartTime`）或 `"subscription"`（含 `subscriptionStatus`）

- [ ] **Step 1: Write the failing tests**

```go
func TestVerifyNotificationAcceptsSignedPayment(t *testing.T)
// body 含 result.resultStatus=S、paymentId、subscriptionId、subscriptionRequestId、
// periodStartTime、periodEndTime、paymentTime、phaseNo → Type=="payment"，OccurredAt==paymentTime
func TestVerifyNotificationAcceptsSignedSubscription(t *testing.T)
// body 含 subscriptionStatus=TERMINATED、subscriptionNotificationType=TERMINATE → Type=="subscription"
func TestVerifyNotificationRejectsBadSignature(t *testing.T) // 改 body 一个字节 → error
func TestVerifyNotificationRejectsWrongClientID(t *testing.T) // header client-id 不等于配置 → error
func TestVerifyNotificationRejectsWrongKeyVersion(t *testing.T) // keyVersion=9 → error
func TestVerifyNotificationRejectsUnknownAlgorithm(t *testing.T) // algorithm=RSA1024 → error
func TestVerifyNotificationParsesRenewalPayment(t *testing.T) // 第二期 payment（phaseNo="2"）→ PeriodEnd 解析正确
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd server && go test ./internal/aurora/ -run 'TestVerifyNotification' -v`
Expected: FAIL，`undefined: AntomNotification`

- [ ] **Step 3: Implement verification and parsing**

`VerifyNotification`：按 Task 1 的格式重建待签名内容（`method` 用 `r.Method`、`path` 用 `r.URL.Path`）→ 解析 `signature` 头三个字段（缺一或多出未知字段即报错）→ `url.QueryUnescape` → base64 → `rsa.VerifyPKCS1v15`。验签通过后才解析 body：先看 `paymentId` 非空 → `Type="payment"`（`OccurredAt = paymentTime`，缺失时退回 `time.Now()`）；否则看 `subscriptionStatus` 非空 → `Type="subscription"`（`OccurredAt` 取 `subscriptionStartTime`，缺失退回 `time.Now()`）；两者都没有 → 返回 error。

时间用 `time.Parse(time.RFC3339, v)`，同时接受带小数秒的写法。

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd server && go test ./internal/aurora/ -run 'TestVerifyNotification' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add server/internal/aurora/antom.go server/internal/aurora/antom_test.go
git commit -m "feat(aurora): verify and parse Antom subscription notifications"
```

---

### Task 4: 数据库迁移与查询

**Files:**
- Create: `server/migrations/5001_aurora_subscription_payment_provider.up.sql`
- Create: `server/migrations/5001_aurora_subscription_payment_provider.down.sql`
- Create: `server/migrations/5002_aurora_subscription_antom_id_index.up.sql`
- Create: `server/migrations/5002_aurora_subscription_antom_id_index.down.sql`
- Modify: `server/pkg/db/queries/aurora_subscription.sql`
- Modify: `server/pkg/db/generated/*`（`make sqlc` 生成，不手改）
- Test: `server/internal/handler/aurora_test.go`（既有订阅测试必须继续通过）

**Interfaces:**
- Consumes: 无
- Produces（sqlc 生成的方法与参数，后续任务直接使用）：
  - `StartAuroraSubscriptionCheckoutParams{UserID, Tier, PaymentProvider, CheckoutIdempotencyKey, CheckoutBillingCycle}`
  - `UpsertAuroraAntomSubscriptionParams{UserID, Tier, Status, AntomSubscriptionID, CurrentPeriodEnd, CancelAtPeriodEnd}`
  - `GetAuroraSubscriptionByCheckoutKey(ctx, checkoutID) (AuroraSubscription, error)`
  - `GetAuroraSubscriptionByAntomID(ctx, antomID) (AuroraSubscription, error)`
  - `MarkAuroraSubscriptionCancelAtPeriodEnd(ctx, userID) (AuroraSubscription, error)`

- [ ] **Step 1: Write the migrations**

`5001_aurora_subscription_payment_provider.up.sql`：

```sql
ALTER TABLE IF EXISTS aurora_subscription
    ADD COLUMN IF NOT EXISTS payment_provider TEXT NOT NULL DEFAULT 'stripe',
    ADD COLUMN IF NOT EXISTS antom_subscription_id TEXT;
```

`5001_...down.sql` 反向删这两列。`stripe_event_created_at` **保持原名不动**：Antom 不
使用它，Stripe 的排序语义原样保留。

`5002_...index.up.sql` 只有一条语句：

```sql
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS aurora_subscription_antom_id_key
    ON aurora_subscription (antom_subscription_id)
    WHERE antom_subscription_id IS NOT NULL;
```

`5002_...down.sql`：`DROP INDEX CONCURRENTLY IF EXISTS aurora_subscription_antom_id_key;`

- [ ] **Step 2: Update the queries and regenerate sqlc**

- `StartAuroraSubscriptionCheckout` 增加 `payment_provider` 列与参数（`pending` 写入时
  带上渠道）。
- 新增 `UpsertAuroraAntomSubscription`：`INSERT ... ON CONFLICT (user_id) DO UPDATE`
  **不带时间戳条件**，改用终态守卫——

  ```sql
  WHERE NOT (
      aurora_subscription.status = 'canceled'
      AND aurora_subscription.antom_subscription_id IS NOT DISTINCT FROM EXCLUDED.antom_subscription_id
  )
  ```

  即「已终止且是同一个 Antom 订阅」的行不再被改写；`canceled` 行若换了一个新的
  `antom_subscription_id`（重新订阅）则允许覆盖。
- 新增三个查询：`GetAuroraSubscriptionByCheckoutKey`（`WHERE checkout_idempotency_key = $1`）、
  `GetAuroraSubscriptionByAntomID`（`WHERE antom_subscription_id = $1`）、
  `MarkAuroraSubscriptionCancelAtPeriodEnd`（`UPDATE ... SET cancel_at_period_end = true,
  updated_at = now() WHERE user_id = $1 RETURNING ...`）。

Run: `make sqlc`
Expected: 生成成功，无 diff 报错

- [ ] **Step 3: Run the migration and the existing subscription tests**

Run: `make test`（Makefile 会先 `ensure-postgres` 再 `migrate up`）
Expected: 迁移应用成功；既有的 aurora 订阅测试全绿（Stripe 路径未受影响）

- [ ] **Step 4: Commit**

```bash
git add server/migrations server/pkg/db/queries/aurora_subscription.sql server/pkg/db/generated
git commit -m "feat(aurora): store the subscription payment provider and Antom subscription id"
```

---

### Task 5: 结账接口按渠道分发

**Files:**
- Modify: `server/internal/handler/handler.go`（`Antom aurora.AntomProvider` 字段）
- Modify: `server/internal/handler/aurora.go:1391-1530`（`CreateAuroraCheckout`、`beginAuroraSubscriptionCheckout`）
- Test: `server/internal/handler/aurora_test.go`

**Interfaces:**
- Consumes: Task 2 的 `AntomProvider.CreateSubscription`、Task 4 的 `StartAuroraSubscriptionCheckoutParams.PaymentProvider`
- Produces:
  - `Handler.Antom`（可为 nil，表示渠道未配置）
  - `beginAuroraSubscriptionCheckout(ctx, userID pgtype.UUID, tier, billingCycle, provider string) (string, error)` —— 新增 `provider` 参数
  - 请求体字段 `provider`（缺省 `"stripe"`）

- [ ] **Step 1: Write the failing tests**

在 `aurora_test.go` 新增 `fakeAntom`（实现 `aurora.AntomProvider`，记录 `requests []aurora.AntomSubscriptionRequest`、`cancelIDs []string`，可注入 `createErr`）与 `installAuroraAntom(t, fake)` helper（设置 `testHandler.Antom`、`testHandler.Tiers`、`testHandler.cfg.CheckoutReturnOrigins`）。

```go
func TestCreateSubscriptionCheckoutRejectsUnknownProvider(t *testing.T)  // provider="alipay" → 400
func TestCreateSubscriptionCheckoutDefaultsToStripe(t *testing.T)        // 无 provider 字段 → 走 Stripe（fakePayments 记录到 checkout key）
func TestCreateSubscriptionCheckoutRunsOnAntom(t *testing.T)
// provider="antom", tier="creator", monthly → 200 {"checkoutUrl":"https://antom.example/auth"}；
// fakeAntom 收到的 Amount=="19.00"、Currency=="SGD"、RequestID 前缀 "aurora-sub-"、Tier=="creator"
func TestCreateSubscriptionCheckoutAntomRejectsYearly(t *testing.T)      // billingCycle="yearly" + provider="antom" → 400
func TestCreateSubscriptionCheckoutAntomFailsClosedWithoutClient(t *testing.T) // h.Antom == nil → 503
func TestCreateSubscriptionCheckoutAntomConflictWhenSubscribed(t *testing.T)  // 已存在 active 行 → 409
func TestCreateSubscriptionCheckoutRejectsPendingIntentOnAnotherProvider(t *testing.T)
// 预置 payment_provider="stripe"、status="pending"、未过期的行，请求 provider="antom" → 409（不替换意图）
func TestCreateSubscriptionCheckoutReusesPendingIntentOnSameProvider(t *testing.T)
// 预置 provider="antom" 的 pending 意图 → 复用同一个 checkout key
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `make test` 后用 `cd server && go test ./internal/handler/ -run 'TestCreateSubscriptionCheckout' -v`
Expected: FAIL，`unknown field provider` / `Handler.Antom` 未定义

- [ ] **Step 3: Implement provider dispatch**

`CreateAuroraCheckout`：解析请求体后先把 `provider` 归一化（空 → `"stripe"`；不在 `{"stripe","antom"}` → 400 `"unknown provider"`）。Stripe 分支保持现有代码不变；Antom 分支要求 `h.Antom != nil`（否则 503）且 `billingCycle == "monthly"`（否则 400 `"billingCycle must be monthly for this provider"`），金额从 `h.Tiers.Lookup(tier)` 的 Antom 金额字段取，两条通知 URL 由 `h.cfg.PublicURL` 拼出（`PublicURL` 为空时渠道在构造阶段就不可用，见 Task 8），调 `CreateSubscription`，返回 `{"checkoutUrl": url}`。

`beginAuroraSubscriptionCheckout` 把 `provider` 写进 `StartAuroraSubscriptionCheckoutParams.PaymentProvider`。冲突判定在现有规则上加两条：

- 已有非 `canceled` 的行（任何渠道）→ 409。
- 已有未过期的 `pending` 行且 `payment_provider` 与本次请求不同 → 409（不替换：被替换掉的
  Stripe 会话用户仍可能完成，那会让 webhook 永远匹配不上、Stripe 重试三天）。
- 未过期的 `pending` 行且渠道相同、档位与周期相同 → 复用原 `checkout_idempotency_key`。

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd server && go test ./internal/handler/ -run 'TestCreateSubscriptionCheckout' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add server/internal/handler/handler.go server/internal/handler/aurora.go server/internal/handler/aurora_test.go
git commit -m "feat(aurora): start subscription checkouts on the selected provider"
```

---

### Task 6: 通知路由、确认报文与授信

**Files:**
- Modify: `server/internal/handler/aurora.go`（新增两个 handler 与 `writeAntomAck`）
- Modify: `server/cmd/server/router.go:1549` 附近（公开路由）
- Test: `server/internal/handler/aurora_test.go`

**Interfaces:**
- Consumes: Task 3 的 `VerifyNotification`/`AntomNotification`、Task 4 的 `UpsertAuroraAntomSubscription`/`GetAuroraSubscriptionByCheckoutKey`/`GetAuroraSubscriptionByAntomID`、既有 `grantSubscriptionAllowance`
- Produces:
  - `func (h *Handler) AntomPaymentNotification(w http.ResponseWriter, r *http.Request)`
  - `func (h *Handler) AntomSubscriptionNotification(w http.ResponseWriter, r *http.Request)`
  - `const maxAntomNotifyBodySize = 64 << 10`
  - `metrics.AuroraWebhookEventAntomPayment = "antom_payment_notify"`、`metrics.AuroraWebhookEventAntomSubscription = "antom_subscription_notify"`

- [ ] **Step 1: Write the failing tests**

```go
func TestAntomPaymentNotificationGrantsMonthlyAllowance(t *testing.T)
// 预置 pending 行（tier=creator, checkout_idempotency_key="aurora-sub-x"）；
// 投通知 payment S、subscriptionRequestId="aurora-sub-x"、subscriptionId="S1"、periodEnd=now+30d →
// 200 + 确认报文；行变 active，antom_subscription_id="S1"，current_period_end 更新；当月 allowance 到账
func TestAntomPaymentNotificationMatchesByAntomIDWhenRequestKeyIsGone(t *testing.T)
// 行上 checkout_idempotency_key 为 NULL、antom_subscription_id="S1"；通知只带 S1 → 仍命中
func TestAntomPaymentNotificationIsIdempotent(t *testing.T)
// 同一条通知投两次 → 200 两次，积分只发一次
func TestAntomPaymentNotificationMarksPastDueOnFailure(t *testing.T)
// resultStatus=F → 行 status="past_due"，不发积分，current_period_end 不变
func TestAntomSubscriptionNotificationTerminates(t *testing.T)
// TERMINATE → status="canceled"，cancel_at_period_end=false
func TestAntomSubscriptionNotificationCancelWaitsForPeriodEnd(t *testing.T)
// CANCEL → status="active"，cancel_at_period_end=true
func TestAntomSubscriptionNotificationIgnoresLateCreate(t *testing.T)
// 先 TERMINATE（subscriptionId="S1"）再投 CREATE（同为 "S1"）→ 仍为 canceled
func TestAntomSubscriptionNotificationAcceptsResubscribe(t *testing.T)
// 行已 canceled 且 subscriptionId="S1"，通知 CREATE 带 "S2" → status="active"，ID 换为 "S2"
func TestAntomNotificationRejectsBadSignature(t *testing.T)       // 401，无写入
func TestAntomNotificationAcksUnknownSubscription(t *testing.T)   // 找不到本地行 → 200 确认，只记日志
func TestAntomNotificationFailsClosedWithoutProvider(t *testing.T) // h.Antom == nil → 503
// metrics：两个新 event label 出现在预热序列里
func TestAuroraWebhookEventsIncludeAntom(t *testing.T) // 在 server/internal/metrics/aurora_billing_test.go
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd server && go test ./internal/handler/ -run 'TestAntom' -v && go test ./internal/metrics/ -run 'TestAuroraWebhookEventsIncludeAntom' -v`
Expected: FAIL，handler 与 label 未定义

- [ ] **Step 3: Implement the two handlers, routes and metric labels**

公共部分（`serveAntomNotification(w, r) (aurora.AntomNotification, bool)`）：`h.Antom == nil` → 503
并记 `AuroraWebhookResultNotConfigured`；限流复用 `h.WebhookIPRateLimiter`（超限记
`RateLimited`）；`http.MaxBytesReader` 限 `maxAntomNotifyBodySize`（超限记 `BadRequest`）；
`VerifyNotification` 失败 → 401 并记 `InvalidSignature`。

匹配顺序（命中即停）：`GetAuroraSubscriptionByCheckoutKey(RequestID)` →
`GetAuroraSubscriptionByAntomID(SubscriptionID)` → 都没有则记日志、确认、返回
`HandlerError` 之外的结果标签（用 `Unknown`），**不重试**。

`AntomPaymentNotification`：`Status=="S"` 时用 `UpsertAuroraAntomSubscription`
（`PaymentProvider="antom"`、`Status="active"`、`CurrentPeriodEnd=PeriodEnd`、
`AntomSubscriptionID=SubscriptionID`）；返回 `pgx.ErrNoRows` 说明命中终态守卫（已终止的
同一订阅），确认且不发积分；写入成功才 `grantSubscriptionAllowance(ctx, row.UserID, tier)`。
`Status!="S"` 时以 `Status="past_due"` 写入且不发积分。

`AntomSubscriptionNotification`：`CREATE`/`UPDATE`/`CHANGE` → `active`；`CANCEL` →
`active` + `cancel_at_period_end=true`；`TERMINATE` → `canceled` + `cancel_at_period_end=false`；
其余类型记日志后确认。

两条 handler 各以 `RecordAuroraWebhookEvent(metrics.AuroraWebhookEventAntomPayment|....AntomSubscription, result)` 计数；在 `knownAuroraWebhookEvents` 加两条映射，并把两个 label 加进 `AllAuroraWebhookEvents()`。

确认统一走 `writeAntomAck(w)`，payload 逐字节等于 Global Constraints 里的字符串。

`router.go` 公开路由组新增：

```go
r.Post("/api/aurora/billing/antom/notify/payment", h.AntomPaymentNotification)
r.Post("/api/aurora/billing/antom/notify/subscription", h.AntomSubscriptionNotification)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd server && go test ./internal/handler/ -run 'TestAntom' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add server/internal/handler/aurora.go server/internal/handler/aurora_test.go server/cmd/server/router.go
git commit -m "feat(aurora): handle Antom subscription notifications"
```

---

### Task 7: 取消订阅与渠道可用列表

**Files:**
- Modify: `server/internal/handler/aurora.go`（`AuroraSubscriptionResponse`、`GetAuroraSubscription`、新增 `CancelAuroraSubscription`）
- Modify: `server/cmd/server/router.go`（登录后路由）
- Test: `server/internal/handler/aurora_test.go`

**Interfaces:**
- Consumes: Task 2 的 `CancelSubscription`、Task 4 的 `MarkAuroraSubscriptionCancelAtPeriodEnd`
- Produces:
  - `AuroraSubscriptionResponse.Provider string` 与 `AuroraSubscriptionResponse.AvailableProviders []string`
  - `func (h *Handler) CancelAuroraSubscription(w http.ResponseWriter, r *http.Request)`

- [ ] **Step 1: Write the failing tests**

```go
func TestSubscriptionResponseListsAvailableProviders(t *testing.T)
// 只配置 Stripe → ["stripe"]；Stripe+Antom → ["stripe","antom"]；都没配 → []
func TestSubscriptionResponseReportsProvider(t *testing.T) // 行 payment_provider="antom" → 响应 provider=="antom"
func TestCancelAuroraSubscriptionCallsAntom(t *testing.T)
// antom 行 + h.Antom 可用 → 200；fakeAntom.cancelIDs == [antom_subscription_id]；行 cancel_at_period_end=true
func TestCancelAuroraSubscriptionRejectsStripeChannel(t *testing.T) // 409，提示用 Portal
func TestCancelAuroraSubscriptionFailsClosedWhenAntomDown(t *testing.T) // CancelSubscription 报错 → 502，行不变
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd server && go test ./internal/handler/ -run 'TestSubscriptionResponse|TestCancelAuroraSubscription' -v`
Expected: FAIL

- [ ] **Step 3: Implement the response fields and the cancel endpoint**

`GetAuroraSubscription` 填 `Provider`（行存在时）与 `AvailableProviders`（`h.Payments != nil` 追加 `stripe`，`h.Antom != nil` 追加 `antom`，顺序固定）。

`CancelAuroraSubscription`：需登录；无行 → 404；`payment_provider != "antom"` → 409 `"use the billing portal"`；`antom_subscription_id` 为空 → 409；调 `h.Antom.CancelSubscription`，失败 → 502 且本地不变；成功 → `MarkAuroraSubscriptionCancelAtPeriodEnd` → 200 返回更新后的订阅响应体（与 GET 同构，复用同一构造函数）。

路由（登录组内）：`r.Post("/api/aurora/billing/subscription/cancel", h.CancelAuroraSubscription)`。

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd server && go test ./internal/handler/ -run 'TestSubscriptionResponse|TestCancelAuroraSubscription' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add server/internal/handler/aurora.go server/internal/handler/aurora_test.go server/cmd/server/router.go
git commit -m "feat(aurora): cancel Antom subscriptions in-app and list available providers"
```

---

### Task 8: 配置接线（env → provider）

**Files:**
- Modify: `server/internal/handler/handler.go`（`Config` 增加六个 Antom 字段并在 `New` 中构造）
- Modify: `server/cmd/server/router.go:459-472`（读 env）
- Test: `server/internal/handler/handler_test.go`

**Interfaces:**
- Consumes: Task 1 的 `NewAntomClient`
- Produces: `Config.AntomClientID/AntomMerchantPrivateKey/AntomPublicKey/AntomKeyVersion/AntomAPIBase/AntomCurrency`

- [ ] **Step 1: Write the failing test**

```go
func TestNewHandlerBuildsAntomProviderFromConfig(t *testing.T)
// 六项齐全 + PublicURL 非空 → h.Antom != nil；缺 ClientID → nil；私钥非法 → nil；
// PublicURL 为空 → nil（且 Stripe 不受影响）
```

若 `handler_test.go` 不存在则新建（包内测试，直接调 `New` 需要数据库连接，参照同目录既有测试的 `testHandler` 装配方式；必要时改为对未导出构造函数 `newAntomFromConfig(cfg)` 的单元测试，保证不需要数据库）。

- [ ] **Step 2: Run test to verify it fails**

Run: `cd server && go test ./internal/handler/ -run 'TestNewHandlerBuildsAntom' -v`
Expected: FAIL

- [ ] **Step 3: Implement the wiring**

`handler.New` 里照 Stripe 的写法构造（不要给接口字段赋 nil 具体指针）：

```go
var antom aurora.AntomProvider
if cfg.PublicURL == "" {
    slog.Warn("antom payments disabled: MULTICA_PUBLIC_URL is unset")
} else if antomClient, err := aurora.NewAntomClient(aurora.AntomConfig{...}); err == nil && antomClient != nil {
    antom = antomClient
} else if err != nil {
    slog.Warn("antom payments disabled", "error", err)
}
```

`PublicURL` 为空时渠道不可用，而不是在结账时才发现通知地址拼不出来——前端也就不会
渲染一个必然失败的按钮。

`router.go` 读 `ANTOM_CLIENT_ID`、`ANTOM_MERCHANT_PRIVATE_KEY`、`ANTOM_PUBLIC_KEY`、`ANTOM_KEY_VERSION`、`ANTOM_API_BASE`、`ANTOM_CURRENCY`。

- [ ] **Step 4: Run test to verify it passes**

Run: `cd server && go test ./internal/handler/ -run 'TestNewHandlerBuildsAntom' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add server/internal/handler/handler.go server/internal/handler/handler_test.go server/cmd/server/router.go
git commit -m "feat(aurora): wire Antom credentials from the environment"
```

---

### Task 9: 前端渠道选择、取消入口与文案

**Files:**
- Modify: `packages/core/aurora/types.ts`、`packages/core/aurora/schema.ts`、`packages/core/aurora/api.ts`、`packages/core/aurora/mutations.ts`
- Modify: `packages/views/aurora/billing.tsx`
- Modify: `packages/views/locales/{en,fr,ja,ko,zh-Hans}/aurora.json`
- Test: `packages/core/aurora/schema.test.ts`、`packages/core/aurora/mutations.test.tsx`、`packages/views/aurora/billing.test.tsx`

**Interfaces:**
- Consumes: Task 7 的 `provider` / `availableProviders` / 取消接口
- Produces: `AuroraPaymentProvider = "stripe" | "antom"`；`CreateAuroraCheckoutRequest.provider`；`cancelAuroraSubscription()`；`useCancelAuroraSubscription()`

- [ ] **Step 1: Write the failing tests**

```ts
// schema.test.ts
it("defaults availableProviders to [] and drops unknown values") // {subscription:{availableProviders:["stripe","venmo"]}} → ["stripe"]；缺字段 → []
it("defaults provider to stripe")                                 // 缺 provider → "stripe"

// mutations.test.tsx
it("passes provider through to the checkout endpoint")            // POST body 含 provider:"antom"
it("calls the cancel endpoint")                                   // POST /api/aurora/billing/subscription/cancel

// billing.test.tsx
it("renders one button per available provider")                   // availableProviders ["stripe","antom"] → 两个按钮，文案 "Stripe"/"Antom"
it("hides the Antom button when Antom is unavailable")            // ["stripe"] → 只有一个按钮
it("shows cancel only for an active Antom subscription")          // provider:"antom", status:"active", cancelAtPeriodEnd:false → 可见；canceled → 不显示
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `pnpm exec vitest run packages/core/aurora/schema.test.ts packages/core/aurora/mutations.test.tsx packages/views/aurora/billing.test.tsx`
Expected: FAIL

- [ ] **Step 3: Implement the client surface and the UI**

- `schema.ts`：`auroraSubscriptionSchema` 增加 `provider: z.string().default("stripe")`、`availableProviders: z.array(z.enum(["stripe","antom"])).catch([]).default([])`（未知值被丢弃）。
- `types.ts`：`CreateAuroraCheckoutRequest` 增加 `provider: AuroraPaymentProvider`。
- `api.ts`：`cancelAuroraSubscription()` POST 取消路径并解析成订阅对象；新增路径常量。
- `mutations.ts`：`useCancelAuroraSubscription`，成功后 invalidate 订阅查询。
- `billing.tsx`：`subscription.availableProviders` 为每个渠道渲染一个升级按钮（`t(($) => $.billing.subscription.providers[provider])`），`onSubscribe(tier, provider)`；Antom 且 `status==="active" && !cancelAtPeriodEnd` 时渲染取消按钮，pending 态复用 mutation。
- 五个 locale 补齐 `billing.subscription.providers.stripe/antom`、`billing.subscription.cancel`、`billing.subscription.cancel_failed`。

- [ ] **Step 4: Run tests to verify they pass**

Run: `pnpm exec vitest run packages/core/aurora packages/views/aurora/billing.test.tsx packages/views/locales/parity.test.ts`
Expected: PASS（parity 测试保证五个 locale 键一致）

- [ ] **Step 5: Commit**

```bash
git add packages/core/aurora packages/views/aurora/billing.tsx packages/views/aurora/billing.test.tsx packages/views/locales
git commit -m "feat(aurora): let users pick the payment provider and cancel Antom plans"
```

---

### Task 10: 文档与阻塞票同步

**Files:**
- Modify: `AGENTS.md`（Aurora Roadmap 的阻塞条目）
- Modify: `docs/engineering/aurora-2day-payable-launch-note.md`
- Modify: `docs/engineering/aurora-billing-alerts.md`（加两个 Antom event label 的 PromQL）
- Create: `server/cmd/antom-inquiry/main.go`（手动对账命令，`InquirySubscription` 的唯一调用方）

**Interfaces:**
- Consumes: Task 2 的 `InquirySubscription`、Task 6 的两个 metric label
- Produces: 运维可执行的 Antom 上线清单与对账命令

- [ ] **Step 1: 写对账命令**

`server/cmd/antom-inquiry/main.go`：从同一组 `ANTOM_*` 环境变量构造 client，参数 `-subscription-id`，
打印订阅状态与当期起止时间，非 `S` 结果退出码非零。薄封装，不另写测试（`InquirySubscription`
本身在 Task 2 已覆盖）。

- [ ] **Step 2: 更新 AGENTS.md 的阻塞条目**

把「六张票全部等 Stripe」改写为两段：Stripe 侧仍缺凭证；Antom 侧已通代码，等待①商户号
开通订阅产品与 SGD ②六个 `ANTOM_*` 变量与 `MULTICA_PUBLIC_URL` ③两条通知路径公网可达。
明确写出「#48/#50 现在可以走 Antom 沙箱验收」。

- [ ] **Step 3: launch note 增加 Antom 运行手册**

内容：六个环境变量与 `MULTICA_PUBLIC_URL`、两条通知 URL、沙箱「不重试」这一差异、
取消语义（`CANCEL` vs `TERMINATE`）、失败通知落 `past_due` 的含义、
`go run ./cmd/antom-inquiry -subscription-id <id>` 的对账用法与判读。

- [ ] **Step 4: 校验**

Run: `git diff --check && grep -n "STRIPE_" AGENTS.md | head`
Expected: 无空白错误；AGENTS.md 中不再有「所有阻塞都因缺少 Stripe 凭证」的表述

- [ ] **Step 5: Commit**

```bash
git add AGENTS.md docs/engineering server/cmd/antom-inquiry
git commit -m "docs(aurora): record the Antom channel launch prerequisites and reconciliation"
```

- [ ] **Step 6: 在 GitHub 上同步阻塞票（人工步骤，需 owner 授权执行）**

对 #45 / #46 / #47 / #48 / #50 / #56 各留一条评论，说明该票在 Antom 渠道下的新含义与
验收方式；标签保持 `type::task` + `P0-Urgent` + `S1-Todo` 不变。

- [ ] **Step 5: 在 GitHub 上同步阻塞票（人工步骤，需 owner 授权执行）**

对 #45 / #46 / #47 / #48 / #50 / #56 各留一条评论，说明该票在 Antom 渠道下的新含义与验收方式，并按需要调整标签（保持 `type::task` + `P0-Urgent`，`S1-Todo` 不动）。

---

## Self-Review

- **Spec coverage**：6.1→Task 5/8；6.2→Task 2/5；6.3→Task 3/6（含两个 metric label）；
  6.4→Task 6；6.5→Task 7；6.6→Task 4/5（冲突判定）；6.7→Task 5/7；6.8→Task 9；
  6.9→Task 8（含 `PublicURL` 门槛）；6.10→Task 10。
- **类型一致性**：`AntomProvider` 四个方法在 Task 2 定义，Task 5/6/7/10 只调用；
  `UpsertAuroraAntomSubscription` 在 Task 4 定义、Task 6 使用，且不再出现
  `ProviderEventCreatedAt`（该列只属于 Stripe 写入）。
- **Review Focus 落点**：重复/乱序→Task 6 的 `TestAntomPaymentNotificationIsIdempotent`、
  `TestAntomSubscriptionNotificationIgnoresLateCreate`、`...AcceptsResubscribe`；
  伪造→Task 3 与 Task 6 的 401 测试；金额篡改→Task 5 的 `RunsOnAntom`（金额来自档位表）；
  渠道可用性→Task 7 与 Task 9；取消语义→Task 6 的两条取消测试与 Task 7 的 409。
