# Aurora UI 设计系统重做（apps/aurora + packages/views/aurora）— 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 `apps/aurora` 的界面换成 DESIGN.md 定义的设计系统——暖纸色画布、深海军蓝侧栏与 Hero、紫色主操作、珊瑚橙点缀、粉彩标签、8px 按钮 / 12px 卡片——结构与交互照搬参考实现 `aurora-ai-agents`。

**Architecture:** 三层，样式作用域限于 Aurora。`packages/views/aurora/aurora.css` 是设计层：`--aurora-*` 变量 + `aurora-*` 前缀类，不读任何 Multica token；`apps/aurora/app/globals.css` 把 Multica 语义 token 桥接到这套调色板（该文件只被 Aurora 一个 app 引入，所以桥接只影响 Aurora 的 CSS bundle），Aurora 里的 shadcn 原语因此自动换色；`packages/views/aurora/*.tsx` 与 `apps/aurora/components/*.tsx` 把原来的 Tailwind 工具类簇换成语义 `aurora-*` 类。**生成、结算、查询逻辑与既有可访问名保持不变**；新增的移动导航使用共享 Sheet 管理焦点与关闭，新增展示文案按计划补齐 i18n。

**Tech Stack:** Next.js 16 App Router、React 19、Tailwind v4（`@import` / `@theme`）、shadcn + Base UI 原语、TanStack Query、vitest + Testing Library。

**Spec:** `/Users/lirichen/Work/apexai/aurora-ai-agents/DESIGN.md`（`Apex-AI-Tools-design`）+ 参考样式表 `/Users/lirichen/Work/apexai/aurora-ai-agents/app/globals.css`（413 行）。Task 1 是它的前缀化移植；移动导航的遮罩与关闭使用共享 Sheet，不照搬手写 off-canvas。

## Global Constraints

以下约束对**每个** Task 都成立，不再重复。

- **不碰行为。** 每个 Task 只改 `className`、新增 `className`、CSS、以及新增展示型子组件。不改 hook 调用、不改 state、不改 `onClick`/`onChange`、不改查询键、不改 props 签名（Task 2 新增组件除外）。Task 3 的移动导航是明确例外：新增响应式状态与共享 Sheet 的打开/关闭控制，不改变页面业务状态。
- **不碰可访问契约。** 保留所有 `role`、`aria-*`、`htmlFor`/`id` 配对、`disabled`、`aria-busy`、`aria-label`、`type="button"`、装饰图标的 `aria-hidden="true"`。现存测试大量用 `getByRole`/`getByLabelText`/文案查询，改完必须全绿。
- **不新增文案，除非本计划明确列出。** 文案规则见 `apps/docs/content/docs/developers/conventions.mdx` §3：标题、标签、数值、按钮动作不重复描述；描述默认省略。计划里新加的 i18n 键要一次补齐 5 个 locale（`en`/`zh-Hans`/`ja`/`ko`/`fr`），`packages/views/locales/parity.test.ts` 会强制键平价。
- **CSS 类必须 `aurora-` 前缀。** `aurora.css` 里的规则是**无层级（unlayered）纯 CSS**，在层叠里胜过 Tailwind 的 `@layer utilities`；不带前缀的通用名（`.card`、`.sidebar`）会静默压过 `packages/ui` 原语。同理：给一个元素加了 `aurora-*` 类之后，不要指望再用工具类覆盖它的同一属性。
- **颜色与圆角只能来自 `--aurora-*`。** CSS 里不写裸色值（`rgba()` 遮罩/半透明白除外，那是 DESIGN.md 自己的写法）；TSX 里半径只用 Tailwind 阶梯：`rounded-sm`=4px、`rounded-md`=6px、`rounded-lg`=8px、`rounded-xl`=12px、`rounded-2xl`=16px、`rounded-full`=胶囊/圆点。**禁止** `rounded` 裸类和 `rounded-[6px]` 这类定值（`scripts/check-ui-radius-tokens.mjs` 会让 CI 红）。
- **Aurora 锁定亮色。** DESIGN.md 只有一套画布，参考实现没有暗色主题。Task 1 用 `forcedTheme="light"` 钉住，不发明暗色盘。
- **不动共享组件。** `packages/views/layout/collection-page.tsx`、`packages/views/auth/login-page.tsx`、`packages/ui/**` 一律不改：Aurora 换成自己的 page header/state，登录页靠 token 桥换色。**Aurora 专属的 chrome 只放 `packages/views/aurora/`；app 的导航与断点 wiring 留在 `apps/aurora/`。**
- **改测试前先读测试文件。** 本计划给的新断言里的渲染辅助（`renderDirectory`、`renderComposer` 之类）以被测文件里实际存在的那个为准：先跑一次现有测试确认基线是绿的，再复用它的 harness 与 fixture 名称写新断言。不要新造一套 provider 包装。
- **每个 Task 的验收命令：**
  ```bash
  pnpm --filter @multica/views test -- aurora          # 组件测试
  pnpm --filter @multica/aurora test                    # app 测试
  pnpm --filter @multica/views typecheck && pnpm --filter @multica/aurora typecheck
  node scripts/check-ui-radius-tokens.mjs
  ```
  最后一个 Task 追加全量：`pnpm typecheck && pnpm lint && pnpm test && pnpm check:ui-radii`。

## 设计基线

### 参考实现 → Aurora 映射

| 参考（`aurora-ai-agents`） | Aurora 落点 | 说明 |
|---|---|---|
| `.sidebar` 深蓝侧栏 + `.user-panel`（积分 + 进度条 + 充值）+ `.nav-groups` | `AuroraShell` 侧栏 | 用户信息 / 可用积分 / 本月生成进度 / 充值入口；导航 5 项 + 退出 |
| `.hero-panel`（navy + 径向光 + 轨道头像） | 技能目录页顶部 | 内容改为真实状态：运行节点状态 + 可用积分 + 本月生成 + 技能数 |
| `.directory-head` + `.category-tabs` + `.agent-grid` + `.agent-card` | `SkillDirectory` | 分类 pill + 技能卡网格 + 卡片内 `.aurora-avatar`（首字母 + 粉彩底，颜色由 id 派生） |
| `.drawer-layer` / `.agent-drawer` / `.task-composer` / `.run-button` | `GenerationComposer`（已是 `Sheet side="right"`） | 抽屉宽度、头部、`.aurora-composer` 输入块、结果区 |
| `.task-list` + `.status-pill` | `HistoryList` / 运行中的生成 / 账单流水 | 统一 `.aurora-list` + `.aurora-row` + `.aurora-status-pill` |
| `.works-grid` + `.work-card` | `WorksList` 的作品区 | 缩略图网格；图片资产渲染真图，其它资产渲染首字母 |
| `.usage-cards` + `.usage-panel` | `AuroraBilling` | 三张真实数字卡（余额 / 本月已用 / 本月生成）+ 套餐面板 |
| `.plans`（充值弹窗里的三档） | 账单页充值档位 + 套餐档位 | 紧凑卡片按钮，推荐档紫色描边 |
| `.subpage-head`（eyebrow + h1 + 副标题 + 动作） | `AuroraPageHeader` | eyebrow 翻译成 13px（不是 11px）：DESIGN.md 禁止 11px 中文 |
| `.empty-state` | `AuroraPageState` | 空态/错误态统一 |
| `.modal-card` / `.account-modal` | 账户弹窗（`Dialog`） | 身份 + 积分入口 + 退出 |
| `.login-screen` 分屏 | 不移植 | `LoginPage` 是 web/desktop 共享组件；靠 token 桥拿到纸色画布 + 紫色按钮 + 12px 卡片，另外传入 Aurora 品牌 logo |

### 参考实现首页：布局与交互对照

先把参考的首页拆成"区域 → 交互 → Aurora 落点"，再据此写 Task。这张表是本计划的验收基线：**每个区域要么落在某个 Task 上，要么在"刻意不移植"里给出数据缺口或状态归属的理由。**

**布局区域（参考 → Aurora）**

| 参考区域 | 内容 | Aurora 落点 |
|---|---|---|
| `.sidebar` 272px | 品牌块（渐变方块 + 两行字） | Task 3 `.aurora-sidebar-top` + `.aurora-brand*` |
| | `.user-panel`：头像 + 姓名 + 套餐 + ⋯ 按钮 | Task 3 `AuroraAccountPanel` |
| | 积分行 + 进度条 + 充值按钮 | 同：额度行 + `role="progressbar"` 的条 + 充值链接（量的是本月额度，接口里只有它有分母） |
| | `.nav-groups` 两组：组标题 + 条目 + 计数徽章 + 选中左紫条 | Task 3 两组导航 + `[aria-current="page"]::before` 紫条（**计数徽章不做**：没有一处能在不新增查询的前提下拿到真实条数） |
| | `.sidebar-footer`：设置 + 退出登录 | Task 3 退出登录（设置项没有落地页，不做） |
| `.topbar` 64px | ☰ + 移动端品牌 + 右侧搜索框 + 帮助 + 头像 | Task 3：☰ + 品牌 + 右侧**运行状态 chip**（参考放的是搜索与头像；头像入口已在侧栏，重复放一个是同一个动作两个按钮） |
| `.hero-panel` | mini-label（绿点）+ h1（珊瑚橙强调词）+ 段落 + 3 个数字 + 右侧轨道 | Task 4：mini-label 换成**真实运行状态**、h1 一句话、数字换成**真实技能数**、轨道用可运行技能的头像；**段落不做**（文案规则：不写泛化引导） |
| `.directory-head` | eyebrow + h2 + 右侧"{{visible}} 个功能可用" | Task 4：`.aurora-section-head` + 同一个"按当前筛选可见的条数" |
| `.category-tabs` | 5 个胶囊 tab | Task 4：`Tabs` + `.aurora-pill-tabs`/`.aurora-pill-tab`（分类取目录实际用到的，不写死） |
| 搜索框 | 在顶栏，即时过滤 | Task 4：**放在工具条**，样式照 `.search-box`（`InputGroup` + ⌕ 图标 + 280px + 聚焦紫边）；理由见"刻意不移植" |
| `.agent-grid` | 3 列卡片，1120→2 列，690→1 列 | Task 4 `.aurora-card-grid` |
| `.empty-state` | 图标 + 标题 + 说明 + 「查看全部功能」按钮 | Task 4 `AuroraPageState` + `actions`（清空搜索与分类） |

**卡片结构**

| 参考 | Aurora |
|---|---|
| 推荐徽章（右上绝对定位） | `.aurora-card--featured` 珊瑚描边（`skill.featured` 存在；徽章文案没有对应字段，用描边表达同一个事实） |
| `.card-top`：头像 52px + 小标签/标题/「类别 · N 个 Skills」+ 箭头 | `AuroraAvatar size="lg"` + 分类小字 + 标题 + 消耗 + `.aurora-card-arrow` |
| 描述 | **不做**（目录没有描述字段） |
| `.skill-chips`：3 个能力标签 | `.aurora-chips`：来自 `skill.input` 的模态标签（文本/图片/文档/音频/视频/表格） |
| `.model-line`：`Provider · Model` | **不做**（目录没有 provider/model 字段） |
| `footer`：左"Skill 已配置"绿点，右"约 N 积分/次" | `.aurora-card-footer`：左 `.aurora-card-state`（可运行，绿点）或"即将上线"胶囊，右 `credits` |

**交互**

| 交互 | 参考行为 | Aurora 落点 |
|---|---|---|
| 点分类 tab | `setCategory`，网格即时过滤 | 同（`Tabs onValueChange`） |
| 输入搜索 | `setSearch`，即时过滤标题/类别/描述/技能名 | 同（过滤显示名 + 分类 + id） |
| 点卡片 | 打开抽屉、清空草稿、复位结果 | 同（`setSelected` + `GenerationComposer key={skill.id}` 重挂载） |
| 关抽屉 | 遮罩 / × / Esc | 遮罩 / Esc / `Sheet` 原语的关闭按钮 |
| 点快捷提示 | 填进输入框、复位结果态 | **不做**（见下） |
| 空输入提交 | toast「先输入你想完成的内容」 | 提交按钮 `disabled`（`canSubmit`）；Aurora 的输入框与按钮在同一屏，禁用比事后提示更早 |
| 余额 < 消耗 | 直接弹充值弹窗 | 402 → 抽屉内 `Alert` + 充值链接（不抢焦点弹窗） |
| 提交成功 | `.task-success` + 「查看任务」 | 状态胶囊 + 进度说明 + 结果产物 + 「打开作品库」（Aurora 会真的轮询到结算，信息比参考多） |
| 充值 | 三档 `.plans` + `popular` 描边 + toast | 账单页 `.aurora-plans` 档位卡（真跳 Stripe；没有 `popular` 字段，不编推荐档） |
| 账户菜单 | 个人信息 / 积分用量 / 退出登录 | 身份 + 积分入口 + 退出登录 |
| 移动端 | ☰ → 抽屉 + 遮罩 + 点链接自动收 | 同（Task 3） |
| 空态恢复 | 「查看全部功能」清空搜索与分类 | 同（Task 4） |
| toast | 右下角 2.6s | **不做**（Aurora 现有交互都能就地反馈） |

### 刻意不移植（每条都给出理由）

- **暗色主题**：DESIGN.md 只有一套画布。
- **账单柱状图**（`.bar-chart`）：接口只给首页 50 条流水，按天聚合会画出"看起来完整、其实截断"的图；不发明没法诚实呈现的图表。
- **Hero 的段落**（"想做什么，直接点什么"式引导）：文案规则明确要求不重复标题、不写泛化引导；Hero 保留结构与配色，内容换成真实状态与数字。
- **卡片的描述与模型行**：`SkillCatalogEntry` 里没有 `description`、也没有 `provider`/`model`，不编。要补就得先动 `server/internal/aurora/catalog.go` 与 16 个技能 × 5 locale 的产品文案，那是另一个计划。
- **抽屉里的快捷提示**（`.quick-prompts`，点击填入输入框）：需要每个技能的示例提示词字段，同样缺失；`composer.prompt_placeholder` 已经是"给一个例子"的现有做法，先留着。
- **顶栏那块搜索**：参考把它放在顶栏，但它只过滤首页网格。搬上去需要把过滤条件提升成跨组件状态（URL `?q=` 或一个新 store），而 Aurora 的搜索与它驱动的分类 tab 在同一个工具条里，本就是同一屏的过滤区。**样式照搬**（`.search-box` 的图标 + 白底 + 发丝边 + 聚焦紫边），位置留在页面内。
- **帮助按钮**（`.help-button`）：Aurora 没有帮助中心，放一个点不动的 `?` 是假控件。
- **导航计数徽章**（`.nav-group button i`）：没有一处能在不新增查询的前提下拿到真实条数。
- **toast**：参考用它做校验与演示购买反馈；Aurora 的校验是禁用按钮、购买跳转 Stripe、复制有就地状态，没有需要瞬时提示的交互。
- **登录分屏视觉**（`.login-visual`/轨道）：要给共享 `LoginPage` 开分叉，收益不值这个代价。
- **`.profile-*` 一套**：Aurora 没有个人信息页。

### 前缀化命名对照

参考的 `.agent-card`/`.sidebar`/`.hero-panel` 等一律改为 `aurora-` 前缀，语义更贴 Aurora 的：`.aurora-card`、`.aurora-card-grid`、`.aurora-sidebar`、`.aurora-hero`、`.aurora-list`/`.aurora-row`、`.aurora-panel`、`.aurora-stat`、`.aurora-empty`、`.aurora-dialog`、`.aurora-drawer`、`.aurora-tint-1..6`。完整清单是 Task 1 的交付物，也是唯一权威。

---

### Task 1: Aurora 设计层（`aurora.css` + token 桥 + 亮色锁定 + 字体）

**Files:**
- Create: `packages/views/aurora/aurora.css`
- Modify: `apps/aurora/app/globals.css`
- Modify: `apps/aurora/app/layout.tsx`
- Test: `apps/aurora/app/aurora-theme.test.ts`

**Interfaces:**
- Consumes: 无（本 Task 是其余所有 Task 的基础）。
- Produces: `--aurora-*` 变量与 `aurora-*` 类（下面 CSS 是唯一权威清单）；以及被桥接的 Multica 语义 token。**后续所有 Task 只能用这份清单里的类名，不得新增裸 CSS。**
- 关键约束：`aurora.css` 的规则是 unlayered 纯 CSS，**层叠优先级高于 Tailwind 工具类**。给元素加 `aurora-*` 类之后，同一属性上的工具类不再生效。

- [ ] **Step 1: 建 `packages/views/aurora/aurora.css`**

```css
/* =============================================================================
 * Aurora design layer
 *
 * The visual system apps/aurora renders: warm paper canvas, one navy chrome
 * column, violet primary, sparse coral accent, pastel tags. The rules and the
 * values come from DESIGN.md; the structure is a port of the Aurora AI Tools
 * reference stylesheet so the two products read as one design.
 *
 * Three properties keep this file contained:
 *
 * 1. Every class is prefixed `aurora-`. These rules ship unlayered, and an
 *    unlayered rule outranks every Tailwind utility - an unprefixed `.card` or
 *    `.sidebar` would silently out-style a packages/ui primitive.
 * 2. Every color and radius is an `--aurora-*` variable declared here. Nothing
 *    reads a Multica token, so this layer renders the same wherever it lands.
 * 3. The palette is light-only, matching DESIGN.md, which defines one canvas.
 *    apps/aurora pins the theme instead of this file inventing a dark palette.
 *
 * apps/aurora/app/globals.css imports this file and then maps the Multica
 * semantic tokens onto this palette, which is what re-themes the shadcn
 * primitives used inside Aurora (Button, Input, Sheet, Dialog, Tabs, Progress).
 * ========================================================================== */

:root {
  color-scheme: light;

  --aurora-ink: #172034;
  --aurora-charcoal: #37352f;
  --aurora-slate: #5d606b;
  --aurora-muted: #6f7583;
  --aurora-soft-muted: #9298a4;
  --aurora-paper: #f6f4ef;
  --aurora-surface: #f1efe9;
  --aurora-card: #ffffff;
  --aurora-line: #e8e5de;
  --aurora-line-soft: #efede8;
  --aurora-line-strong: #cfcbc2;
  --aurora-navy: #13223a;
  --aurora-navy-deep: #0e1a2d;
  --aurora-navy-mid: #1f3354;
  --aurora-violet: #7166f3;
  --aurora-violet-pressed: #5d52dc;
  --aurora-violet-deep: #4a40b8;
  --aurora-coral: #ff6c50;
  --aurora-coral-deep: #c9452c;
  --aurora-green: #2fac7b;
  --aurora-warning: #d97a1a;
  --aurora-error: #d65d4c;
  --aurora-tint-peach: #ffe8d4;
  --aurora-tint-rose: #fde0ec;
  --aurora-tint-mint: #d9f3e1;
  --aurora-tint-lavender: #e6e0f5;
  --aurora-tint-sky: #dcecfa;
  --aurora-tint-yellow: #fef7d6;
  --aurora-on-dark-muted: rgba(255, 255, 255, 0.62);
  --aurora-shadow-subtle: 0 1px 2px rgba(15, 15, 15, 0.04);
  --aurora-shadow-card: 0 4px 12px rgba(15, 15, 15, 0.08);
  --aurora-shadow-modal: 0 16px 48px -8px rgba(15, 15, 15, 0.16);
  --aurora-r-xs: 4px;
  --aurora-r-sm: 6px;
  --aurora-r-md: 8px;
  --aurora-r-lg: 12px;
  --aurora-r-xl: 16px;
  --aurora-sidebar-width: 272px;
}

/* The Aurora body scope includes the portalled navigation and generation UI. */
.aurora-theme :focus-visible,
.aurora-drawer :focus-visible,
.aurora-dialog :focus-visible {
  outline: 2px solid var(--aurora-violet);
  outline-offset: 2px;
}

/* --- Shell: sidebar -------------------------------------------------------- */

.aurora-app {
  display: flex;
  height: 100svh;
  min-height: 0;
  color: var(--aurora-ink);
  background: var(--aurora-paper);
  font-size: 14px;
  line-height: 1.6;
}

.aurora-nav-drawer {
  width: min(var(--aurora-sidebar-width), 94vw);
  max-width: none;
  gap: 0;
  padding: 0;
  border: 0;
  background: var(--aurora-navy);
}

.aurora-nav-drawer .aurora-sidebar {
  position: static;
  display: flex;
  width: 100%;
  height: 100%;
  visibility: visible;
  transform: none;
}

.aurora-sidebar {
  z-index: 40;
  display: flex;
  width: var(--aurora-sidebar-width);
  flex: 0 0 auto;
  flex-direction: column;
  overflow-y: auto;
  color: #fff;
  background: var(--aurora-navy);
}

.aurora-sidebar-top {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 24px 20px 20px;
}

.aurora-brand {
  display: flex;
  align-items: center;
  gap: 10px;
}

.aurora-brand-mark {
  display: grid;
  width: 34px;
  height: 34px;
  place-items: center;
  border-radius: var(--aurora-r-md);
  color: #fff;
  background: linear-gradient(135deg, var(--aurora-violet), var(--aurora-coral));
  font-size: 18px;
  font-weight: 700;
}

.aurora-brand-copy {
  display: flex;
  flex-direction: column;
}

.aurora-brand-copy strong {
  font-size: 16px;
  letter-spacing: 0.12em;
  line-height: 1.1;
}

.aurora-brand-copy small {
  margin-top: 4px;
  color: rgba(255, 255, 255, 0.55);
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.12em;
}

/* The same brand on paper rather than on the navy column: the sign-in card. */
.aurora-brand--on-light .aurora-brand-copy strong {
  color: var(--aurora-navy);
}

.aurora-brand--on-light .aurora-brand-copy small {
  color: var(--aurora-muted);
}

.aurora-sidebar-close,
.aurora-menu-button {
  display: none;
  width: 40px;
  height: 40px;
  place-items: center;
  border: 1px solid var(--aurora-line);
  border-radius: var(--aurora-r-md);
  color: var(--aurora-ink);
  background: var(--aurora-card);
  font-size: 16px;
  line-height: 1;
  cursor: pointer;
}

.aurora-sidebar-close {
  border-color: rgba(255, 255, 255, 0.18);
  color: #fff;
  background: transparent;
}

.aurora-user-panel {
  margin: 0 14px 20px;
  padding: 16px;
  border: 1px solid rgba(255, 255, 255, 0.1);
  border-radius: var(--aurora-r-lg);
  background: rgba(255, 255, 255, 0.05);
}

.aurora-user-row {
  display: flex;
  align-items: center;
  gap: 10px;
}

.aurora-user-avatar {
  display: grid;
  width: 36px;
  height: 36px;
  flex: 0 0 auto;
  place-items: center;
  border-radius: var(--aurora-r-md);
  color: var(--aurora-violet-deep);
  background: var(--aurora-tint-lavender);
  font-size: 13px;
  font-weight: 600;
}

.aurora-user-copy {
  display: flex;
  min-width: 0;
  flex: 1;
  flex-direction: column;
}

.aurora-user-copy strong {
  overflow: hidden;
  font-size: 14px;
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.aurora-user-copy span {
  margin-top: 2px;
  overflow: hidden;
  color: rgba(255, 255, 255, 0.6);
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.aurora-more-button {
  padding: 6px;
  border: 0;
  border-radius: var(--aurora-r-sm);
  color: rgba(255, 255, 255, 0.6);
  background: none;
  letter-spacing: 1px;
  cursor: pointer;
}

.aurora-more-button:hover {
  background: rgba(255, 255, 255, 0.08);
}

.aurora-token-row {
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  margin-top: 18px;
}

.aurora-token-row span {
  color: var(--aurora-on-dark-muted);
  font-size: 12px;
}

.aurora-token-row strong {
  font-size: 18px;
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}

.aurora-meter {
  height: 4px;
  margin: 10px 0 14px;
  overflow: hidden;
  border-radius: 4px;
  background: rgba(255, 255, 255, 0.12);
}

.aurora-meter-fill {
  display: block;
  height: 100%;
  border-radius: inherit;
  background: var(--aurora-violet);
}

.aurora-recharge-button {
  display: block;
  width: 100%;
  padding: 9px 12px;
  border: 1px solid rgba(255, 255, 255, 0.18);
  border-radius: var(--aurora-r-md);
  color: #fff;
  background: transparent;
  font-size: 13px;
  font-weight: 500;
  text-align: center;
  cursor: pointer;
  transition: background 0.15s ease;
}

.aurora-recharge-button:hover {
  background: rgba(255, 255, 255, 0.08);
}

.aurora-nav-groups {
  padding: 0 12px;
}

.aurora-nav-group {
  margin-bottom: 20px;
}

.aurora-nav-group > p {
  margin: 0 10px 6px;
  color: rgba(255, 255, 255, 0.45);
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.1em;
  text-transform: uppercase;
}

.aurora-nav-link {
  position: relative;
  display: flex;
  width: 100%;
  align-items: center;
  gap: 10px;
  padding: 9px 10px;
  border: 0;
  border-radius: var(--aurora-r-md);
  color: rgba(255, 255, 255, 0.72);
  background: transparent;
  font-size: 14px;
  font-weight: 500;
  text-align: left;
  cursor: pointer;
  transition: background 0.15s ease, color 0.15s ease;
}

.aurora-nav-link:hover {
  color: #fff;
  background: rgba(255, 255, 255, 0.06);
}

.aurora-nav-link[aria-current="page"] {
  color: #fff;
  background: rgba(255, 255, 255, 0.1);
}

.aurora-nav-link[aria-current="page"]::before {
  position: absolute;
  left: -12px;
  width: 3px;
  height: 20px;
  border-radius: 0 3px 3px 0;
  background: var(--aurora-violet);
  content: "";
}

.aurora-nav-link svg {
  width: 18px;
  height: 18px;
  flex: 0 0 auto;
}

.aurora-sidebar-footer {
  margin-top: auto;
  padding: 0 12px 16px;
}

/* --- Shell: workspace column ---------------------------------------------- */

.aurora-main {
  display: flex;
  min-width: 0;
  flex: 1;
  flex-direction: column;
}

.aurora-topbar {
  z-index: 30;
  display: flex;
  height: 64px;
  flex: 0 0 auto;
  align-items: center;
  gap: 12px;
  padding: 0 24px;
  border-bottom: 1px solid var(--aurora-line);
  background: rgba(246, 244, 239, 0.92);
  backdrop-filter: blur(12px);
}

.aurora-topbar-brand {
  display: none;
  margin-left: 4px;
  font-size: 15px;
  font-weight: 700;
  letter-spacing: 0.12em;
}

.aurora-topbar-brand span {
  color: var(--aurora-violet);
}

.aurora-topbar-actions {
  display: flex;
  margin-left: auto;
  align-items: center;
  gap: 8px;
}

.aurora-status-chip {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  padding: 7px 12px;
  border: 1px solid var(--aurora-line);
  border-radius: 9999px;
  color: var(--aurora-slate);
  background: var(--aurora-card);
  font-size: 12px;
  font-weight: 500;
}

.aurora-scroll {
  min-height: 0;
  flex: 1 1 auto;
  overflow-y: auto;
  padding: 24px 20px 64px;
}

/* A full-screen centred message outside the shell: the dead ends, the
   no-workspace notice and the sign-in fallback. */
.aurora-centered {
  display: flex;
  min-height: 100svh;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 24px;
  padding: 48px 24px;
  text-align: center;
  background: var(--aurora-paper);
}

.aurora-page {
  width: min(1200px, 100%);
  margin-inline: auto;
}

/* --- Page head, section head, empty state --------------------------------- */

.aurora-page-head {
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  gap: 16px;
  padding: 8px 0 24px;
}

.aurora-page-head-main {
  min-width: 0;
}

.aurora-page-head h1 {
  margin: 0;
  font-size: 32px;
  font-weight: 600;
  letter-spacing: -0.5px;
  line-height: 1.2;
}

.aurora-page-meta {
  display: flex;
  align-items: center;
  gap: 12px;
  margin: 6px 0 0;
  color: var(--aurora-muted);
  font-size: 13px;
}

.aurora-page-actions {
  display: flex;
  flex: 0 0 auto;
  align-items: center;
  gap: 8px;
}

.aurora-eyebrow {
  display: flex;
  align-items: center;
  gap: 6px;
  margin: 0 0 6px;
  color: var(--aurora-violet);
  font-size: 13px;
  font-weight: 600;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.aurora-section-head {
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  gap: 16px;
  margin: 40px 0 16px;
}

.aurora-section-head h2 {
  margin: 0;
  font-size: 24px;
  font-weight: 600;
  letter-spacing: -0.3px;
}

.aurora-section-head > span {
  color: var(--aurora-muted);
  font-size: 13px;
}

.aurora-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 16px;
}

.aurora-empty {
  padding: 56px 20px;
  border: 1px dashed var(--aurora-line-strong);
  border-radius: var(--aurora-r-lg);
  text-align: center;
}

.aurora-empty h3 {
  margin: 12px 0 6px;
  color: var(--aurora-ink);
  font-size: 18px;
  font-weight: 600;
}

.aurora-empty p {
  max-width: 30rem;
  margin: 0 auto 16px;
  color: var(--aurora-muted);
  font-size: 14px;
}

.aurora-empty-actions {
  display: flex;
  justify-content: center;
  gap: 8px;
}

/* --- Hero ------------------------------------------------------------------ */

.aurora-hero {
  position: relative;
  display: grid;
  grid-template-columns: 1.35fr 0.65fr;
  align-items: center;
  overflow: hidden;
  padding: 40px 48px;
  border-radius: var(--aurora-r-xl);
  color: #fff;
  background: radial-gradient(
      circle at 80% 20%,
      rgba(113, 102, 243, 0.32),
      transparent 40%
    ),
    var(--aurora-navy);
}

.aurora-hero::before {
  position: absolute;
  inset: 0;
  opacity: 0.08;
  background-image: radial-gradient(circle, #fff 1px, transparent 1px);
  background-size: 24px 24px;
  content: "";
  mask-image: linear-gradient(90deg, transparent 30%, #000);
}

.aurora-hero > * {
  position: relative;
  z-index: 2;
}

.aurora-hero h1 {
  margin: 16px 0 12px;
  font-size: clamp(28px, 3.2vw, 40px);
  font-weight: 600;
  letter-spacing: -1px;
  line-height: 1.12;
}

.aurora-hero p {
  max-width: 560px;
  margin: 0;
  color: rgba(255, 255, 255, 0.72);
  font-size: 16px;
  line-height: 1.6;
}

.aurora-mini-label {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  padding: 4px 10px;
  border: 1px solid rgba(255, 255, 255, 0.18);
  border-radius: 9999px;
  color: #fff;
  font-size: 12px;
  font-weight: 500;
}

.aurora-mini-label::before {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--aurora-green);
  content: "";
}

.aurora-mini-label--warning::before {
  background: var(--aurora-warning);
}

.aurora-mini-label--error::before {
  background: var(--aurora-error);
}

.aurora-mini-label--muted::before {
  background: var(--aurora-soft-muted);
}

.aurora-mini-label--running::before {
  background: var(--aurora-violet);
}

.aurora-hero-stats {
  display: flex;
  flex-wrap: wrap;
  gap: 24px;
  margin-top: 24px;
}

.aurora-hero-stats span {
  color: var(--aurora-on-dark-muted);
  font-size: 13px;
}

.aurora-hero-stats strong {
  margin-right: 4px;
  color: #fff;
  font-size: 18px;
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}

.aurora-hero-orbit {
  position: relative;
  width: 310px;
  height: 218px;
  justify-self: end;
}

.aurora-hero-orbit::before,
.aurora-hero-orbit::after {
  position: absolute;
  top: 50%;
  left: 50%;
  border: 1px solid rgba(255, 255, 255, 0.12);
  border-radius: 50%;
  content: "";
  transform: translate(-50%, -50%);
}

.aurora-hero-orbit::before {
  width: 208px;
  height: 208px;
}

.aurora-hero-orbit::after {
  width: 290px;
  height: 150px;
  transform: translate(-50%, -50%) rotate(-18deg);
}

.aurora-hero-center {
  position: absolute;
  top: 50%;
  left: 50%;
  display: grid;
  width: 76px;
  height: 76px;
  place-items: center;
  border-radius: var(--aurora-r-xl);
  background: var(--aurora-violet);
  box-shadow: var(--aurora-shadow-modal);
  font-size: 20px;
  font-weight: 700;
  letter-spacing: 0.06em;
  line-height: 0.9;
  text-align: center;
  transform: translate(-50%, -50%);
}

.aurora-hero-center small {
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.16em;
}

.aurora-orbit-avatar {
  position: absolute;
  padding: 3px;
  border-radius: var(--aurora-r-lg);
  background: rgba(255, 255, 255, 0.1);
}

.aurora-orbit-avatar .aurora-avatar {
  width: 48px;
  height: 48px;
  border-radius: 10px;
}

.aurora-orbit-avatar--1 {
  top: 2px;
  left: 50px;
}

.aurora-orbit-avatar--2 {
  top: 27px;
  right: 15px;
}

.aurora-orbit-avatar--3 {
  bottom: 2px;
  left: 31px;
}

.aurora-orbit-avatar--4 {
  right: 49px;
  bottom: 7px;
}

/* --- Avatars, dots, tags -------------------------------------------------- */

.aurora-avatar {
  position: relative;
  display: grid;
  width: 44px;
  height: 44px;
  flex: 0 0 auto;
  place-items: center;
  border-radius: var(--aurora-r-md);
  color: var(--aurora-charcoal);
  background: var(--aurora-surface);
  font-size: 13px;
  font-weight: 600;
}

.aurora-avatar--lg {
  width: 52px;
  height: 52px;
  border-radius: 10px;
  font-size: 14px;
}

.aurora-avatar--coral,
.aurora-avatar--rust {
  background: var(--aurora-tint-peach);
}

.aurora-avatar--violet {
  background: var(--aurora-tint-lavender);
}

.aurora-avatar--rose,
.aurora-avatar--strawberry {
  background: var(--aurora-tint-rose);
}

.aurora-avatar--mint {
  background: var(--aurora-tint-mint);
}

.aurora-avatar--amber {
  background: var(--aurora-tint-yellow);
}

.aurora-avatar--blue {
  background: var(--aurora-tint-sky);
}

.aurora-avatar-dot {
  position: absolute;
  right: -2px;
  bottom: -2px;
  width: 11px;
  height: 11px;
  border: 2px solid var(--aurora-card);
  border-radius: 50%;
  background: var(--aurora-green);
}

.aurora-dot {
  width: 8px;
  height: 8px;
  flex: 0 0 auto;
  border-radius: 50%;
  background: var(--aurora-green);
}

.aurora-dot--muted {
  background: var(--aurora-soft-muted);
}

.aurora-dot--warning {
  background: var(--aurora-warning);
}

.aurora-dot--error {
  background: var(--aurora-error);
}

.aurora-dot--running {
  background: var(--aurora-violet);
}

/* --- Skill cards ---------------------------------------------------------- */

.aurora-card-grid {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 16px;
}

.aurora-card {
  position: relative;
  display: flex;
  width: 100%;
  min-width: 0;
  flex-direction: column;
  overflow: hidden;
  padding: 20px;
  border: 1px solid var(--aurora-line);
  border-radius: var(--aurora-r-lg);
  color: var(--aurora-ink);
  background: var(--aurora-card);
  font: inherit;
  text-align: left;
  cursor: pointer;
  transition: box-shadow 0.18s ease, border-color 0.18s ease,
    transform 0.18s ease;
}

.aurora-card:hover {
  border-color: var(--aurora-line-strong);
  box-shadow: var(--aurora-shadow-card);
  transform: translateY(-2px);
}

.aurora-card--featured {
  border-color: #ffc9bc;
}

.aurora-card--unavailable {
  color: var(--aurora-soft-muted);
}

.aurora-card-top {
  display: flex;
  align-items: center;
  gap: 12px;
}

.aurora-card-name {
  display: flex;
  min-width: 0;
  flex: 1;
  flex-direction: column;
}

.aurora-card-category {
  color: var(--aurora-soft-muted);
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.aurora-card-title {
  margin-top: 2px;
  font-size: 17px;
  font-weight: 600;
  line-height: 1.35;
}

.aurora-card-meta {
  margin-top: 2px;
  color: var(--aurora-muted);
  font-size: 13px;
}

.aurora-card-arrow {
  display: grid;
  width: 32px;
  height: 32px;
  flex: 0 0 auto;
  place-items: center;
  border: 1px solid var(--aurora-line);
  border-radius: var(--aurora-r-md);
  color: var(--aurora-muted);
  background: var(--aurora-card);
}

.aurora-card:hover .aurora-card-arrow {
  border-color: var(--aurora-violet);
  color: #fff;
  background: var(--aurora-violet);
}

.aurora-chips {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-top: 14px;
}

.aurora-chips span {
  padding: 2px 8px;
  border-radius: var(--aurora-r-sm);
  color: var(--aurora-charcoal);
  background: var(--aurora-surface);
  font-size: 12px;
  font-weight: 500;
}

.aurora-card-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin: auto -20px -20px;
  margin-top: 16px;
  padding: 12px 20px;
  border-top: 1px solid var(--aurora-line-soft);
}

/* The state cell, not "any direct span": the unavailable case is a pill in the
   same slot, and a descendant selector here would repaint that pill's text. */
.aurora-card-state {
  display: flex;
  align-items: center;
  gap: 6px;
  color: var(--aurora-green);
  font-size: 13px;
  font-weight: 500;
}

.aurora-card-footer strong {
  color: var(--aurora-muted);
  font-size: 13px;
  font-weight: 500;
  font-variant-numeric: tabular-nums;
}

/* --- Pill tabs ------------------------------------------------------------ */

.aurora-pill-tabs {
  display: flex;
  height: auto;
  gap: 8px;
  padding: 2px 0 4px;
  overflow-x: auto;
  background: transparent;
  scrollbar-width: none;
}

.aurora-pill-tabs::-webkit-scrollbar {
  display: none;
}

.aurora-pill-tab {
  height: auto;
  flex: 0 0 auto;
  padding: 7px 16px;
  border: 1px solid var(--aurora-line);
  border-radius: 9999px;
  color: var(--aurora-slate);
  background: var(--aurora-card);
  font-size: 14px;
  font-weight: 500;
  cursor: pointer;
  transition: border-color 0.15s ease, color 0.15s ease, background 0.15s ease;
}

.aurora-pill-tab:hover {
  border-color: var(--aurora-line-strong);
  color: var(--aurora-ink);
}

.aurora-pill-tab[data-active],
.aurora-pill-tab[aria-selected="true"] {
  border-color: var(--aurora-navy);
  color: #fff;
  background: var(--aurora-navy);
}

/* --- Lists and rows ------------------------------------------------------- */

.aurora-list {
  overflow: hidden;
  border: 1px solid var(--aurora-line);
  border-radius: var(--aurora-r-lg);
  background: var(--aurora-card);
}

.aurora-row {
  display: flex;
  align-items: center;
  gap: 16px;
  padding: 16px 20px;
  border-bottom: 1px solid var(--aurora-line-soft);
}

.aurora-row:last-child {
  border-bottom: 0;
}

.aurora-row-stack {
  display: flex;
  flex-direction: column;
  gap: 10px;
  padding: 16px 20px;
  border-bottom: 1px solid var(--aurora-line-soft);
}

.aurora-row-stack:last-child {
  border-bottom: 0;
}

.aurora-row-main {
  min-width: 0;
  flex: 1;
}

.aurora-row-main h3 {
  margin: 0 0 2px;
  font-size: 15px;
  font-weight: 600;
}

.aurora-row-main p {
  margin: 0;
  color: var(--aurora-muted);
  font-size: 13px;
}

.aurora-row-amount {
  color: var(--aurora-slate);
  font-size: 14px;
  font-weight: 500;
  font-variant-numeric: tabular-nums;
  text-align: right;
}

.aurora-icon-tile {
  display: grid;
  width: 40px;
  height: 40px;
  flex: 0 0 auto;
  place-items: center;
  border-radius: var(--aurora-r-md);
  color: var(--aurora-violet-deep);
  background: var(--aurora-tint-lavender);
  font-size: 14px;
}

.aurora-status-pill {
  min-width: 64px;
  padding: 2px 10px;
  border-radius: 9999px;
  color: #1f7a54;
  background: var(--aurora-tint-mint);
  font-size: 12px;
  font-weight: 600;
  text-align: center;
}

.aurora-status-pill--working {
  color: var(--aurora-violet-deep);
  background: var(--aurora-tint-lavender);
}

.aurora-status-pill--failed {
  color: var(--aurora-coral-deep);
  background: var(--aurora-tint-rose);
}

.aurora-status-pill--muted {
  color: var(--aurora-slate);
  background: var(--aurora-surface);
}

/* --- Works grid ----------------------------------------------------------- */

.aurora-works-grid {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 16px;
}

.aurora-work-card {
  padding: 12px;
  border: 1px solid var(--aurora-line);
  border-radius: var(--aurora-r-lg);
  background: var(--aurora-card);
  transition: box-shadow 0.18s ease;
}

.aurora-work-card:hover {
  box-shadow: var(--aurora-shadow-card);
}

.aurora-work-thumb {
  position: relative;
  display: grid;
  height: 170px;
  place-items: center;
  overflow: hidden;
  border-radius: var(--aurora-r-md);
  background: var(--aurora-surface);
}

.aurora-work-thumb img {
  width: 100%;
  height: 100%;
  object-fit: cover;
}

.aurora-work-thumb > strong {
  color: rgba(23, 32, 52, 0.28);
  font-size: 56px;
  font-weight: 700;
}

.aurora-work-tag {
  position: absolute;
  top: 10px;
  left: 10px;
  z-index: 1;
  padding: 2px 8px;
  border-radius: var(--aurora-r-sm);
  color: var(--aurora-charcoal);
  background: rgba(255, 255, 255, 0.75);
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.aurora-work-card h3 {
  margin: 12px 4px 2px;
  font-size: 15px;
  font-weight: 600;
}

.aurora-work-card p {
  margin: 0 4px 4px;
  color: var(--aurora-muted);
  font-size: 13px;
}

.aurora-work-actions {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin: 4px 4px 0;
}

/* --- Panels and stats ----------------------------------------------------- */

.aurora-panel {
  padding: 24px;
  border: 1px solid var(--aurora-line);
  border-radius: var(--aurora-r-lg);
  background: var(--aurora-card);
}

.aurora-panel-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 16px;
}

.aurora-panel-head h2 {
  margin: 0 0 2px;
  font-size: 18px;
  font-weight: 600;
}

.aurora-panel-head p {
  margin: 0;
  color: var(--aurora-muted);
  font-size: 13px;
}

.aurora-stat-cards {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 16px;
}

.aurora-stat {
  padding: 20px 24px;
}

.aurora-stat > span {
  display: block;
  margin-bottom: 8px;
  color: var(--aurora-muted);
  font-size: 13px;
}

.aurora-stat > strong {
  font-size: 28px;
  font-weight: 600;
  letter-spacing: -0.5px;
  font-variant-numeric: tabular-nums;
}

.aurora-stat small {
  margin-left: 6px;
  color: var(--aurora-muted);
  font-size: 13px;
}

.aurora-field-grid {
  display: grid;
  gap: 16px;
}

.aurora-field dt {
  color: var(--aurora-muted);
  font-size: 13px;
}

.aurora-field dd {
  margin: 2px 0 0;
  font-size: 14px;
}

/* --- Drawer (generation composer) ----------------------------------------- */

.aurora-drawer {
  display: flex;
  width: min(540px, 94vw);
  max-width: none;
  flex-direction: column;
  gap: 0;
  overflow-y: auto;
  padding: 28px 28px 20px;
  border-left: 1px solid var(--aurora-line);
  background: var(--aurora-card);
  box-shadow: var(--aurora-shadow-modal);
}

.aurora-drawer-head {
  display: flex;
  align-items: center;
  gap: 14px;
  padding: 4px 40px 20px 0;
  border-bottom: 1px solid var(--aurora-line);
}

.aurora-drawer-head > div {
  min-width: 0;
}

.aurora-drawer-head-category {
  color: var(--aurora-violet);
  font-size: 12px;
  font-weight: 600;
  letter-spacing: 0.06em;
}

.aurora-drawer-head h2 {
  margin: 2px 0 4px;
  font-size: 24px;
  font-weight: 600;
  letter-spacing: -0.3px;
}

.aurora-drawer-head p {
  display: flex;
  align-items: center;
  gap: 6px;
  margin: 0;
  color: var(--aurora-green);
  font-size: 13px;
}

.aurora-drawer-section {
  padding: 20px 0 0;
}

.aurora-drawer-section h3 {
  margin: 0 0 10px;
  font-size: 15px;
  font-weight: 600;
}

.aurora-composer {
  margin-top: 20px;
  padding: 16px;
  border: 1px solid var(--aurora-line-strong);
  border-radius: var(--aurora-r-lg);
  background: var(--aurora-card);
}

.aurora-composer:focus-within {
  border-color: var(--aurora-violet);
  box-shadow: 0 0 0 1px var(--aurora-violet);
}

.aurora-composer label {
  display: block;
  margin-bottom: 8px;
  font-size: 14px;
  font-weight: 600;
}

.aurora-composer-tools {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 12px;
  padding-top: 12px;
  border-top: 1px solid var(--aurora-line-soft);
}

.aurora-file-row {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  color: var(--aurora-muted);
  font-size: 14px;
  cursor: pointer;
}

.aurora-file-row:hover {
  color: var(--aurora-ink);
  text-decoration: underline;
  text-underline-offset: 4px;
}

.aurora-thumbs {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.aurora-thumb {
  display: grid;
  width: 80px;
  height: 80px;
  place-items: center;
  overflow: hidden;
  border: 1px solid var(--aurora-line);
  border-radius: var(--aurora-r-md);
  background: var(--aurora-surface);
  cursor: pointer;
}

.aurora-thumb:hover {
  border-color: var(--aurora-line-strong);
}

.aurora-thumb img {
  width: 100%;
  height: 100%;
  object-fit: cover;
  transition: transform 0.18s ease;
}

.aurora-thumb:hover img {
  transform: scale(1.05);
}

/* --- Dialog (preview, account) -------------------------------------------- */

.aurora-dialog {
  width: min(560px, 94vw);
  max-width: none;
  max-height: 90vh;
  overflow-y: auto;
  padding: 32px;
  border-radius: var(--aurora-r-xl);
  background: var(--aurora-card);
  box-shadow: var(--aurora-shadow-modal);
}

.aurora-dialog--wide {
  width: min(720px, 94vw);
}

.aurora-dialog--account {
  width: min(380px, 92vw);
  padding: 24px;
}

.aurora-modal-hero {
  padding: 8px 0 20px;
  border-bottom: 1px solid var(--aurora-line);
  text-align: center;
}

.aurora-modal-hero .aurora-user-avatar {
  width: 56px;
  height: 56px;
  margin: 0 auto 12px;
  border-radius: var(--aurora-r-lg);
  font-size: 16px;
}

.aurora-modal-hero h2 {
  margin: 0 0 2px;
  font-size: 18px;
  font-weight: 600;
}

.aurora-modal-hero p {
  margin: 0;
  color: var(--aurora-muted);
  font-size: 13px;
}

.aurora-modal-actions {
  display: flex;
  flex-direction: column;
}

.aurora-modal-actions a,
.aurora-modal-actions button {
  display: flex;
  width: 100%;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 12px 8px;
  border: 0;
  border-bottom: 1px solid var(--aurora-line-soft);
  color: var(--aurora-charcoal);
  background: transparent;
  font-size: 14px;
  text-align: left;
  cursor: pointer;
}

.aurora-modal-actions a:hover,
.aurora-modal-actions button:hover {
  background: var(--aurora-paper);
}

.aurora-plans {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 12px;
}

.aurora-plan {
  position: relative;
  display: flex;
  min-height: 136px;
  flex-direction: column;
  align-items: flex-start;
  justify-content: center;
  padding: 20px;
  border: 1px solid var(--aurora-line);
  border-radius: var(--aurora-r-lg);
  color: var(--aurora-ink);
  background: var(--aurora-card);
  font: inherit;
  text-align: left;
  cursor: pointer;
  transition: border-color 0.15s ease, box-shadow 0.15s ease;
}

.aurora-plan:hover {
  border-color: var(--aurora-line-strong);
  box-shadow: var(--aurora-shadow-card);
}

.aurora-plan em {
  position: absolute;
  top: 12px;
  right: 12px;
  padding: 2px 8px;
  border-radius: 9999px;
  color: #fff;
  background: var(--aurora-violet);
  font-size: 12px;
  font-style: normal;
  font-weight: 600;
}

.aurora-plan span {
  margin-bottom: 6px;
  color: var(--aurora-muted);
  font-size: 13px;
}

.aurora-plan strong {
  font-size: 24px;
  font-weight: 600;
  letter-spacing: -0.5px;
  font-variant-numeric: tabular-nums;
}

.aurora-plan small {
  color: var(--aurora-muted);
  font-size: 13px;
}

/* --- Tints ---------------------------------------------------------------- */
/* Declared last on purpose: these share specificity with the surfaces that use
   them (.aurora-work-thumb, .aurora-icon-tile), so source order decides. */

.aurora-tint-1 {
  background: var(--aurora-tint-peach);
}

.aurora-tint-2 {
  background: var(--aurora-tint-lavender);
}

.aurora-tint-3 {
  background: var(--aurora-tint-sky);
}

.aurora-tint-4 {
  background: var(--aurora-tint-mint);
}

.aurora-tint-5 {
  background: var(--aurora-tint-yellow);
}

.aurora-tint-6 {
  background: var(--aurora-tint-rose);
}

/* --- Responsive ----------------------------------------------------------- */

@media (max-width: 1120px) {
  .aurora-card-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .aurora-hero {
    grid-template-columns: 1.15fr 0.85fr;
    padding: 36px;
  }

  .aurora-hero-orbit {
    width: 260px;
    transform: scale(0.86);
    transform-origin: right center;
  }
}

@media (max-width: 900px) {
  .aurora-sidebar {
    display: none;
  }

  .aurora-sidebar-close,
  .aurora-menu-button {
    display: grid;
  }

  .aurora-topbar {
    padding: 0 16px;
  }

  .aurora-topbar-brand {
    display: block;
  }

  .aurora-hero {
    grid-template-columns: 1fr 0.62fr;
  }

  .aurora-hero-orbit {
    width: 210px;
    transform: scale(0.72);
  }
}

@media (max-width: 690px) {
  .aurora-scroll {
    padding: 16px 14px 48px;
  }

  .aurora-card-grid {
    grid-template-columns: 1fr;
  }

  .aurora-card {
    padding: 18px;
  }

  .aurora-card-footer {
    margin-right: -18px;
    margin-bottom: -18px;
    margin-left: -18px;
    padding-right: 18px;
    padding-left: 18px;
  }

  .aurora-hero {
    grid-template-columns: 1fr;
    padding: 28px 24px;
  }

  .aurora-hero h1 {
    font-size: 28px;
  }

  .aurora-hero p {
    font-size: 15px;
  }

  .aurora-hero-orbit {
    display: none;
  }

  .aurora-hero-stats {
    gap: 16px;
  }

  .aurora-page-head {
    align-items: flex-start;
  }

  .aurora-page-head h1 {
    font-size: 26px;
  }

  .aurora-section-head {
    margin-top: 32px;
  }

  .aurora-section-head h2 {
    font-size: 22px;
  }

  .aurora-stat-cards {
    grid-template-columns: 1fr;
  }

  .aurora-works-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .aurora-work-thumb {
    height: 130px;
  }

  .aurora-plans {
    grid-template-columns: 1fr;
  }

  .aurora-plan {
    min-height: 96px;
  }

  .aurora-dialog {
    padding: 24px 20px;
  }

  .aurora-drawer {
    padding: 22px 18px 16px;
  }

  .aurora-composer-tools {
    flex-wrap: wrap;
  }
}

@media (prefers-reduced-motion: reduce) {
  .aurora-theme [data-slot="sheet-content"],
  .aurora-theme [data-slot="sheet-overlay"],
  .aurora-theme *,
  .aurora-theme *::before,
  .aurora-theme *::after {
    animation-duration: 0.01ms !important;
    transition-duration: 0.01ms !important;
  }
}```

- [ ] **Step 2: 写守卫测试（先红）**

`apps/aurora/app/aurora-theme.test.ts`：守住"引用即声明"、"用到即定义"、桥接完整性、以及 DESIGN.md 的字号下限。拼错一个类名不会报错、只会变成没样式。用 TypeScript AST 只扫描 `className` 与明确列出的类名常量，避免把 import 路径或 DOM ID 当成类。动态拼接只展开下表的有限集合，不允许任意前缀匹配；新增类名常量或动态表达式时同步维护扫描清单。未使用类检查在 Task 11 完成迁移后启用。

```ts
// @vitest-environment node
import { readdirSync, readFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { describe, expect, it } from "vitest";
import ts from "typescript";

/**
 * The Aurora design layer is two files that must agree: the stylesheet that
 * declares every `aurora-*` class, and the app's globals.css that imports it and
 * bridges the Multica tokens onto its palette. Neither can fail loudly on its
 * own — a mistyped class name or an undeclared variable renders nothing at
 * all — so this test is the only thing between a typo and a silently unstyled
 * control.
 */

const repoRoot = resolve(import.meta.dirname, "../../..");
const css = readFileSync(
  resolve(repoRoot, "packages/views/aurora/aurora.css"),
  "utf8",
);
const globals = readFileSync(
  resolve(repoRoot, "apps/aurora/app/globals.css"),
  "utf8",
);

/** Every class selector the stylesheet defines, without the leading dot. */
function declaredClasses(source: string): Set<string> {
  const names = new Set<string>();
  for (const match of source.matchAll(/\.(aurora-[a-z0-9-]+)/g)) {
    names.add(match[1]);
  }
  return names;
}

/** Every `--aurora-*` variable the stylesheet reads. */
function referencedVariables(source: string): Set<string> {
  const names = new Set<string>();
  for (const match of source.matchAll(/var\((--aurora-[a-z0-9-]+)/g)) {
    names.add(match[1]);
  }
  return names;
}

/** Every `--aurora-*` variable the stylesheet declares. */
function declaredVariables(source: string): Set<string> {
  const names = new Set<string>();
  for (const match of source.matchAll(/^\s*(--aurora-[a-z0-9-]+)\s*:/gm)) {
    names.add(match[1]);
  }
  return names;
}

/** Explicit finite families used by the three concatenated class expressions. */
const DYNAMIC_CLASSES: Record<string, string[]> = {
  "aurora-avatar--": ["coral", "violet", "rose", "mint", "amber", "blue", "rust", "strawberry"],
  "aurora-orbit-avatar--": ["1", "2", "3", "4"],
  "aurora-tint-": ["1", "2", "3", "4", "5", "6"],
};
const CLASS_CONSTANTS = new Set([
  "GRID_CLASS", "TONE_CLASS", "AURORA_RUNTIME_VISUAL", "GENERATION_STATUS_PILL",
]);

/** Inspect JSX className expressions and the named class maps, never IDs/imports. */
function classesInSource(source: string): Set<string> {
  const file = ts.createSourceFile("source.tsx", source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  const used = new Set<string>();
  const collect = (node: ts.Node) => {
    if (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) {
      for (const token of node.text.split(/\s+/)) {
        if (!token.startsWith("aurora-")) continue;
        const suffixes = DYNAMIC_CLASSES[token];
        if (suffixes) suffixes.forEach((suffix) => used.add(token + suffix));
        else used.add(token);
      }
    }
    ts.forEachChild(node, collect);
  };
  const visit = (node: ts.Node) => {
    if (ts.isJsxAttribute(node) && node.name.getText(file) === "className" && node.initializer) {
      collect(node.initializer);
    } else if (ts.isVariableDeclaration(node) && ts.isIdentifier(node.name) &&
      CLASS_CONSTANTS.has(node.name.text) && node.initializer) {
      collect(node.initializer);
    }
    ts.forEachChild(node, visit);
  };
  visit(file);
  return used;
}

/** All non-test TS/TSX sources under the app and its shared Aurora views. */
function classesUsedIn(root: string): Set<string> {
  const used = new Set<string>();
  const walk = (current: string) => {
    for (const entry of readdirSync(current, { withFileTypes: true })) {
      if (entry.name === "node_modules" || entry.name === ".next") continue;
      const next = join(current, entry.name);
      if (entry.isDirectory()) walk(next);
      else if (/\.tsx?$/.test(entry.name) && !/\.(test|spec)\.tsx?$/.test(entry.name)) {
        classesInSource(readFileSync(next, "utf8")).forEach((name) => used.add(name));
      }
    }
  };
  walk(root);
  return used;
}

const declared = declaredClasses(css);

describe("aurora design layer", () => {
  it("imports the stylesheet from the app's globals", () => {
    expect(globals).toContain("packages/views/aurora/aurora.css");
  });

  it("declares every --aurora variable it reads", () => {
    const variables = declaredVariables(css);
    const missing = [...referencedVariables(css)].filter(
      (name) => !variables.has(name),
    );
    expect(missing).toEqual([]);
  });

  it("defines every aurora class the components use", () => {
    const used = new Set([
      ...classesUsedIn(resolve(repoRoot, "packages/views/aurora")),
      ...classesUsedIn(resolve(repoRoot, "apps/aurora")),
    ]);
    const unresolved = [...used].filter((token) => !declared.has(token));
    expect(unresolved).toEqual([]);
  });

  it("ignores module paths and DOM IDs while checking actual class names", () => {
    const source = `
      import { Providers } from "./aurora-providers";
      const GRID_CLASS = "aurora-card-grid";
      const AURORA_RUNTIME_VISUAL = { online: { dot: "aurora-dot" } };
      const view = <div id="aurora-prompt" className={cn("aurora-page", GRID_CLASS)} />;
      const typo = <span className="aurora-page-typo" />;
      const dynamic = <span className={"aurora-tint-" + index} />;
    `;
    const used = classesInSource(source);
    expect(used.has("aurora-providers")).toBe(false);
    expect(used.has("aurora-prompt")).toBe(false);
    expect(used.has("aurora-card-grid")).toBe(true);
    expect(used.has("aurora-dot")).toBe(true);
    expect(used.has("aurora-tint-6")).toBe(true);
    expect([...used].filter((name) => !declared.has(name))).toEqual(["aurora-page-typo"]);
  });

  it("bridges the palette onto the semantic tokens the primitives read", () => {
    for (const token of [
      "--background",
      "--foreground",
      "--card",
      "--popover",
      "--primary",
      "--primary-foreground",
      "--secondary",
      "--muted",
      "--muted-foreground",
      "--accent",
      "--destructive",
      "--border",
      "--input",
      "--ring",
      "--sidebar",
      "--sidebar-accent",
      "--sidebar-accent-foreground",
      "--surface-border",
    ]) {
      expect(globals).toContain(token + ":");
    }
  });

  it("keeps body copy at or above DESIGN.md's floor", () => {
    expect(globals).toContain("--text-caption: 13px");
    expect(globals).toContain("--text-micro: 12px");
    expect(globals).toContain("--text-body-lg: 16px");
  });
});
```

- [ ] **Step 3: 跑测试确认红**

Run: `pnpm --filter @multica/aurora test -- aurora-theme`
Expected: FAIL — 尚未追加 globals.css 的 import 与 token 桥；Step 1 已创建 `aurora.css`，不应预期 ENOENT。

- [ ] **Step 4: 桥接 token（`apps/aurora/app/globals.css`）**

在已有的 5 条 `@import` 之后追加第 6 条：

```css
@import "../../../packages/views/aurora/aurora.css";
```

然后在文件已有的 `html[lang|="ja"]` 块**之后**追加桥接块：

```css
/* Aurora theme bridge — the DESIGN.md palette mapped onto Multica's semantic
   tokens. This file belongs to one app, so the bridge ships in Aurora's CSS
   bundle only: web and desktop keep the Multica palette, and every packages/ui
   primitive rendered inside Aurora (Button, Input, Sheet, Dialog, Tabs,
   Progress, Skeleton) picks the new palette up without a second theme.

   The three --text-* overrides are DESIGN.md's floor, not a new scale: helper
   text 13px, micro 12px, roomy body 16px. Every other step already matches. */
:root {
  --background: var(--aurora-paper);
  --foreground: var(--aurora-ink);
  --card: var(--aurora-card);
  --card-foreground: var(--aurora-ink);
  --popover: var(--aurora-card);
  --popover-foreground: var(--aurora-ink);
  --primary: var(--aurora-violet);
  --primary-foreground: #ffffff;
  --secondary: var(--aurora-surface);
  --secondary-foreground: var(--aurora-charcoal);
  --muted: var(--aurora-surface);
  --muted-foreground: var(--aurora-muted);
  --accent: var(--aurora-surface);
  --accent-foreground: var(--aurora-ink);
  --destructive: var(--aurora-error);
  --border: var(--aurora-line);
  --input: var(--aurora-line-strong);
  --ring: var(--aurora-violet);
  --success: var(--aurora-green);
  --warning: var(--aurora-warning);
  --info: var(--aurora-violet);
  --brand: var(--aurora-violet);
  --brand-foreground: #ffffff;
  --faint-foreground: var(--aurora-soft-muted);
  --app-shell: var(--aurora-paper);
  --page-canvas: var(--aurora-paper);
  --surface: var(--aurora-card);
  --surface-foreground: var(--aurora-ink);
  --surface-raised: var(--aurora-card);
  --surface-hover: var(--aurora-surface);
  --surface-selected: var(--aurora-surface);
  --surface-selected-foreground: var(--aurora-ink);
  --surface-border: var(--aurora-line);
  --sidebar: var(--aurora-navy);
  --sidebar-foreground: #ffffff;
  --sidebar-primary: var(--aurora-violet);
  --sidebar-primary-foreground: #ffffff;
  --sidebar-accent: rgba(255, 255, 255, 0.1);
  --sidebar-accent-foreground: #ffffff;
  --sidebar-border: rgba(255, 255, 255, 0.1);
  --sidebar-ring: var(--aurora-violet);
  --text-caption: 13px;
  --text-caption--line-height: 19px;
  --text-micro: 12px;
  --text-micro--line-height: 16px;
  --text-body-lg: 16px;
  --text-body-lg--line-height: 26px;
}
```

- [ ] **Step 5: 换字体为 Geist 并钉住亮色（`apps/aurora/app/layout.tsx`）**

DESIGN.md 的 `fontFamily` 是 `Geist, "PingFang SC", ...`；本 app 现在用 Inter。改用 `Geist`（同包的 `Geist_Mono` 已在用，取字风险同类），变量名同步换成 `--font-geist-sans`：

```tsx
import { Geist, Geist_Mono } from "next/font/google";

// Geist is the design system's Latin UI face (DESIGN.md typography.fontFamily).
// globals.css composes the full --font-sans stack around this variable.
const geistSans = Geist({
  subsets: ["latin"],
  style: ["normal", "italic"],
  variable: "--font-geist-sans",
});
```

```tsx
<html
  lang={HTML_LANG[locale]}
  suppressHydrationWarning
  className={cn(
    "antialiased font-sans h-full",
    geistSans.variable,
    geistMono.variable,
  )}
>
  <body className="aurora-theme h-full overflow-hidden">
    {/* DESIGN.md defines one canvas and no dark palette. Pinning the theme is
        what keeps the paper surfaces from rendering under a .dark class whose
        tokens this app never overrides. */}
    <ThemeProvider forcedTheme="light">
```

`globals.css` 里两处 `var(--font-inter)` 改成 `var(--font-geist-sans)`（连带把解释 `inter.variable` 的注释改成 `geistSans.variable`）；`viewport.themeColor` 的 light 条目改成 `#f6f4ef`。

- [ ] **Step 6: 跑测试确认绿**

Run: `pnpm --filter @multica/aurora test -- aurora-theme && pnpm --filter @multica/aurora typecheck`
Expected: PASS，现阶段的 6 条断言全绿。此时允许基础 CSS 尚无消费者；零未使用类断言到 Task 11 才启用。

- [ ] **Step 7: 看真实渲染**

Run: `pnpm dev:aurora`，打开 `/login`。
Expected: 画布 `#f6f4ef`；卡片纯白 + 1px 发丝边 + 12px 圆角；主按钮紫色 `#7166f3` 白字；输入框聚焦是 2px 紫环；标题 600 字重带负字距。**五个页面此时仍是 Multica 旧皮肤（工具类还在），这是预期**——Task 2 起逐页替换。

- [ ] **Step 8: Commit**

```bash
git add packages/views/aurora/aurora.css apps/aurora/app/globals.css apps/aurora/app/layout.tsx apps/aurora/app/aurora-theme.test.ts
git commit -m "feat(aurora): port the design layer and bridge the theme"
```

---

### Task 2: Aurora 展示原语（`AuroraAvatar` / `AuroraPageHeader` / `AuroraPageState`）

**Files:**
- Create: `packages/views/aurora/aurora-avatar.tsx`
- Create: `packages/views/aurora/aurora-page-header.tsx`
- Modify: `packages/views/aurora/index.ts`（导出 `AuroraAvatar`、`avatarInitial`、`avatarTint`、`AuroraPageHeader`、`AuroraPageState`）
- Modify: `packages/views/aurora/load-failed.tsx`（`CollectionPageState` → `AuroraPageState`）
- Test: `packages/views/aurora/aurora-avatar.test.tsx`
- Test: `packages/views/aurora/aurora-page-header.test.tsx`

**Interfaces:**
- Consumes: Task 1 的 `.aurora-avatar`/`.aurora-avatar--{tint}`/`.aurora-avatar--lg`/`.aurora-avatar-dot`、`.aurora-page-head`/`.aurora-eyebrow`/`.aurora-page-meta`/`.aurora-page-actions`、`.aurora-empty`/`.aurora-empty-actions`。
- Produces:
  - `type AuroraAvatarTint = "coral" | "violet" | "rose" | "mint" | "amber" | "blue" | "rust" | "strawberry"`
  - `avatarTint(id: string): AuroraAvatarTint` — 稳定（同一个 id 永远同一个色）
  - `avatarInitial(name: string): string` — 最多两个字符
  - `AuroraAvatar({ name, id, size?: "md" | "lg", available?: boolean, className?: string })`
  - `AuroraPageHeader({ icon, eyebrow, title, meta?, actions? })` —— 刻意没有 `description`（文案规则）
  - `AuroraPageState({ icon, title, description?, actions?, tone?, role? })`

- [ ] **Step 1: 写失败测试**

`packages/views/aurora/aurora-avatar.test.tsx`：

```tsx
import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import {
  AuroraAvatar,
  avatarInitial,
  avatarTint,
  tintIndex,
} from "./aurora-avatar";

describe("avatarTint", () => {
  it("is stable for the same id", () => {
    expect(avatarTint("xhs-image")).toBe(avatarTint("xhs-image"));
  });

  it("keeps the pastel index inside the six declared surfaces", () => {
    const ids = ["a", "poster", "xhs-image", "product-image", "text-image"];
    for (const id of ids) {
      expect(tintIndex(id)).toBeGreaterThanOrEqual(1);
      expect(tintIndex(id)).toBeLessThanOrEqual(6);
    }
    expect(tintIndex("poster")).toBe(tintIndex("poster"));
  });

  it("does not paint the whole catalog one colour", () => {
    const ids = [
      "poster",
      "xhs-image",
      "product-image",
      "text-image",
      "image-edit",
      "id-photo",
      "xhs-copy",
      "resume",
    ];
    expect(new Set(ids.map(avatarTint)).size).toBeGreaterThan(1);
  });
});

describe("avatarInitial", () => {
  it("takes the first letter of a one-word name", () => {
    expect(avatarInitial("Poster")).toBe("P");
  });

  it("takes two initials from a two-word name", () => {
    expect(avatarInitial("Product Image")).toBe("PI");
  });

  it("takes one character of a Chinese name", () => {
    expect(avatarInitial("海报制作")).toBe("海");
  });

  it("falls back when there is no name", () => {
    expect(avatarInitial("   ")).toBe("?");
  });
});

describe("AuroraAvatar", () => {
  it("is decorative and carries the id's tint", () => {
    const { container } = render(<AuroraAvatar name="Poster" id="poster" />);
    const tile = container.querySelector(".aurora-avatar");
    expect(tile).not.toBeNull();
    expect(tile).toHaveAttribute("aria-hidden", "true");
    expect(tile?.className).toContain(`aurora-avatar--${avatarTint("poster")}`);
  });

  it("draws the state dot only when the skill can run", () => {
    const { container: runnable } = render(
      <AuroraAvatar name="Poster" id="poster" available />,
    );
    expect(runnable.querySelector(".aurora-avatar-dot")).not.toBeNull();

    const { container: blocked } = render(<AuroraAvatar name="Poster" id="poster" />);
    expect(blocked.querySelector(".aurora-avatar-dot")).toBeNull();
  });

  it("renders beside a name without joining the accessible name", () => {
    render(
      <button type="button">
        <AuroraAvatar name="Poster" id="poster" />
        Poster
      </button>,
    );
    expect(screen.getByRole("button", { name: "Poster" })).toBeInTheDocument();
  });
});
```

`packages/views/aurora/aurora-page-header.test.tsx`：

```tsx
import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { Sparkles } from "lucide-react";
import { AuroraPageHeader, AuroraPageState } from "./aurora-page-header";

describe("AuroraPageHeader", () => {
  it("renders the eyebrow, the title, the facts and the actions", () => {
    render(
      <AuroraPageHeader
        icon={Sparkles}
        eyebrow="AI tools"
        title="Create"
        meta={<span>16 skills</span>}
        actions={<button type="button">Top up</button>}
      />,
    );

    expect(
      screen.getByRole("heading", { level: 1, name: "Create" }),
    ).toBeVisible();
    expect(screen.getByText("AI tools")).toBeVisible();
    expect(screen.getByText("16 skills")).toBeVisible();
    expect(screen.getByRole("button", { name: "Top up" })).toBeVisible();
  });

  it("omits the meta row and the action slot when the page has neither", () => {
    render(<AuroraPageHeader icon={Sparkles} eyebrow="AI tools" title="Create" />);

    expect(screen.queryByRole("button")).toBeNull();
    expect(screen.getByRole("heading", { level: 1 })).toBeVisible();
  });
});

describe("AuroraPageState", () => {
  it("announces a failure as an alert", () => {
    render(
      <AuroraPageState
        role="alert"
        tone="destructive"
        icon={Sparkles}
        title="Could not load the skill directory"
      />,
    );

    expect(screen.getByRole("alert")).toHaveTextContent(
      "Could not load the skill directory",
    );
  });

  it("renders the description and the recovery action when it has them", () => {
    render(
      <AuroraPageState
        icon={Sparkles}
        title="No generations yet"
        description="Finished generations and their results appear here."
        actions={<button type="button">Try again</button>}
      />,
    );

    expect(
      screen.getByText("Finished generations and their results appear here."),
    ).toBeVisible();
    expect(screen.getByRole("button", { name: "Try again" })).toBeVisible();
  });
});
```

- [ ] **Step 2: 跑测试确认红**

Run: `pnpm --filter @multica/views test -- aurora-avatar aurora-page-header`
Expected: FAIL — `Failed to resolve import "./aurora-avatar"`。

- [ ] **Step 3: 写 `packages/views/aurora/aurora-avatar.tsx`**

```tsx
"use client";

import { cn } from "@multica/ui/lib/utils";

/**
 * The design's avatar: initials on a pastel tile, with an optional state dot.
 *
 * Skills ship no portrait art, so the tint is derived from the id. The same
 * skill therefore keeps the same colour on every surface that names it — the
 * card, the drawer header, the hero orbit — which is what makes the tile read
 * as an identity rather than as decoration.
 */
export type AuroraAvatarTint =
  | "coral"
  | "violet"
  | "rose"
  | "mint"
  | "amber"
  | "blue"
  | "rust"
  | "strawberry";

const TINTS: AuroraAvatarTint[] = [
  "coral",
  "violet",
  "rose",
  "mint",
  "amber",
  "blue",
  "rust",
  "strawberry",
];

/**
 * A stable tint for an id.
 *
 * A hash rather than the catalog index: an index would repaint every tile the
 * moment the server reorders the catalog, and the colour has to survive a
 * re-render even if the list it came from does not.
 */
export function avatarTint(id: string): AuroraAvatarTint {
  return TINTS[hashId(id) % TINTS.length];
}

/**
 * A stable index in 1..6 for the six pastel surfaces (`aurora-tint-1` … `-6`),
 * so a work card and a skill tile derived from the same id land on the same
 * family of colours without sharing a table.
 */
export function tintIndex(id: string): number {
  return (hashId(id) % 6) + 1;
}

function hashId(id: string): number {
  let hash = 0;
  for (let position = 0; position < id.length; position += 1) {
    hash = (hash * 31 + id.charCodeAt(position)) >>> 0;
  }
  return hash;
}

/** Up to two characters for the tile. */
export function avatarInitial(name: string): string {
  const trimmed = name.trim();
  if (!trimmed) return "?";
  // Latin names read better as two initials ("Product Image" -> "PI"); a CJK
  // name is one word and one meaningful character.
  const words = trimmed.split(/\s+/).filter((word) => /^[A-Za-z0-9]/.test(word));
  const first = words[0] ?? "";
  const second = words[1] ?? "";
  if (first && second) return (first[0] + second[0]).toUpperCase();
  // Array.from splits by code point, so a surrogate pair is one character.
  return Array.from(trimmed)[0]?.toUpperCase() ?? "?";
}

export function AuroraAvatar({
  name,
  id,
  size = "md",
  available = false,
  className,
}: {
  name: string;
  id: string;
  size?: "md" | "lg";
  /** Draws the state dot. Only a runnable skill gets one. */
  available?: boolean;
  className?: string;
}) {
  // Decorative in every call site: the tile always sits beside the name it
  // stands for, so announcing it would read the identity twice.
  return (
    <span
      aria-hidden="true"
      className={cn(
        "aurora-avatar",
        size === "lg" && "aurora-avatar--lg",
        "aurora-avatar--" + avatarTint(id),
        className,
      )}
    >
      {avatarInitial(name)}
      {available ? <span className="aurora-avatar-dot" /> : null}
    </span>
  );
}
```

- [ ] **Step 4: 写 `packages/views/aurora/aurora-page-header.tsx`**

```tsx
"use client";

import type { LucideIcon } from "lucide-react";
import { cn } from "@multica/ui/lib/utils";

/**
 * Aurora's own page chrome.
 *
 * The shared `CollectionPageHeader` is web and desktop's — a 48px toolbar row
 * that belongs to the Multica shell — so Aurora carries the design system's page
 * head instead: an eyebrow, the title, whatever facts the collection has, and
 * the page's actions.
 *
 * There is deliberately no `description` prop. The reference layout has a
 * subtitle slot, but a page states its title and its facts once; a sentence that
 * restates the title is exactly what the copy rules exist to keep out.
 */
export function AuroraPageHeader({
  icon: Icon,
  eyebrow,
  title,
  meta,
  actions,
}: {
  icon: LucideIcon;
  eyebrow: string;
  title: string;
  /** Facts about the collection — a count, a date range. Not a sentence. */
  meta?: React.ReactNode;
  actions?: React.ReactNode;
}) {
  return (
    <header className="aurora-page-head">
      <div className="aurora-page-head-main">
        <p className="aurora-eyebrow">
          <Icon aria-hidden="true" className="size-3.5" />
          {eyebrow}
        </p>
        <h1>{title}</h1>
        {meta ? <p className="aurora-page-meta">{meta}</p> : null}
      </div>
      {actions ? <div className="aurora-page-actions">{actions}</div> : null}
    </header>
  );
}

type AuroraPageStateTone = "muted" | "destructive" | "warning";

const TONE_CLASS: Record<AuroraPageStateTone, string> = {
  muted: "text-muted-foreground",
  destructive: "text-destructive",
  warning: "text-warning",
};

/**
 * The one empty / failed / no-match surface every Aurora page uses.
 *
 * The tone colours the icon only: the title stays legible as a title in all
 * three cases, and a screen that painted the whole block red would shout at a
 * user who is only looking at an empty library.
 */
export function AuroraPageState({
  icon: Icon,
  title,
  description,
  actions,
  tone = "muted",
  role,
}: {
  icon: LucideIcon;
  title: string;
  description?: string;
  actions?: React.ReactNode;
  tone?: AuroraPageStateTone;
  role?: "alert" | "status";
}) {
  return (
    <div role={role} className="aurora-empty">
      <Icon aria-hidden="true" className={cn("size-8", TONE_CLASS[tone])} />
      <h3>{title}</h3>
      {description ? <p>{description}</p> : null}
      {actions ? <div className="aurora-empty-actions">{actions}</div> : null}
    </div>
  );
}
```

- [ ] **Step 5: 导出并改 `load-failed.tsx`**

`packages/views/aurora/index.ts` 追加：

```ts
export {
  AuroraAvatar,
  avatarInitial,
  avatarTint,
  tintIndex,
} from "./aurora-avatar";
export { AuroraPageHeader, AuroraPageState } from "./aurora-page-header";
```

`packages/views/aurora/load-failed.tsx`：把 `CollectionPageState` 换成 `AuroraPageState`，`icon={Frown}` / `tone="destructive"` / `role="alert"` / `title` / `actions` 五个 prop 原样保留，`../layout/collection-page` 的 import 删掉。

- [ ] **Step 6: 跑测试确认绿**

Run: `pnpm --filter @multica/views test -- aurora-avatar aurora-page-header && pnpm --filter @multica/views typecheck`
Expected: PASS。`load-failed` 的改变会被 `works-list`/`history-list`/`billing`/`generation-detail` 的测试覆盖，同样要全绿。

- [ ] **Step 7: Commit**

```bash
git add packages/views/aurora/aurora-avatar.tsx packages/views/aurora/aurora-page-header.tsx packages/views/aurora/index.ts packages/views/aurora/load-failed.tsx packages/views/aurora/aurora-avatar.test.tsx packages/views/aurora/aurora-page-header.test.tsx
git commit -m "feat(aurora): add the avatar, page header and page state primitives"
```

---

### Task 3: 应用外壳（侧栏 + 顶栏 + 账户 + 移动抽屉）与全部新文案

**Files:**
- Create: `packages/views/aurora/aurora-account-panel.tsx`
- Modify: `packages/views/aurora/runtime-status.tsx`（新增导出的 `AuroraRuntimeChip`，`STATE_VISUAL` 换成共享词表 `AURORA_RUNTIME_VISUAL`）
- Modify: `packages/views/aurora/index.ts`
- Modify: `apps/aurora/components/aurora-shell.tsx`（整文件重写）
- Modify: `apps/aurora/components/aurora-shell.test.tsx`
- Modify: `packages/views/locales/{en,zh-Hans,ja,ko,fr}/aurora.json`

**Interfaces:**
- Consumes: Task 1 的 `aurora-*` 类；Task 2 的 `avatarInitial`；`useAuroraBalance`/`useAuroraSubscription`/`useAuroraRuntime`/`isAuroraDegraded`；`useLogout`。
- Produces:
  - `AuroraAccountPanel({ creditsHref, onOpenAccount })`
  - `AuroraAccountDialog({ open, onOpenChange, creditsHref })`
  - `AuroraRuntimeChip({ href })`
  - `AURORA_RUNTIME_VISUAL: Record<AuroraRuntimeState, { dot: string; pill: string; text: string }>` —— `dot`/`pill` 是 `aurora-*` 类，`text` 是 Multica 语义色工具类（由 token 桥落到同一调色板）。三处（运行页、目录 hero、顶栏 chip）共用这一份。
- 实测数据形状：`useAuroraBalance().data?.value?.availableMicro`；`useAuroraSubscription().data` **直接就是** `AuroraSubscription`（不套 `.value`）；`useAuroraRuntime().data?.value` 是 `AuroraExecutionTarget`。

- [ ] **Step 1: 补 25 个 i18n 键（5 个 locale 各一次）**

插入位置：`"shell"` 块放在 `"runtime"` 与 `"workspace"` 之间；其余键加进各自已有的块。**只加键，不动已有键的值。**

`en/aurora.json`：

```json
  "shell": {
    "brand_tagline": "AI STUDIO",
    "menu_open": "Open menu",
    "menu_close": "Close menu",
    "account_menu": "Account",
    "nav_group_work": "Workspace",
    "nav_group_account": "Account"
  },
```

```json
"directory": {
  "eyebrow": "AI TOOLS",
  "hero_title": "Pick a skill. Aurora runs it.",
  "stats_runnable": "skills runnable",
  "stats_total": "skills in the catalog",
  "visible_count": "{{visible}} skills available",
  "ready": "Ready",
  "clear_filters": "Show all skills",
  "inputs": {
    "text": "Text",
    "image": "Images",
    "document": "Documents",
    "audio": "Audio",
    "video": "Video",
    "spreadsheet": "Spreadsheets",
    "other": "Other"
  }
}
```

```text
works     → "eyebrow": "CREATIONS"
history   → "eyebrow": "HISTORY"
detail    → "eyebrow": "GENERATION"
runtime   → "eyebrow": "EXECUTION"
billing   → "eyebrow": "CREDITS"
```

`zh-Hans/aurora.json`：

```json
  "shell": {
    "brand_tagline": "AI STUDIO",
    "menu_open": "打开菜单",
    "menu_close": "关闭菜单",
    "account_menu": "账户",
    "nav_group_work": "工作区",
    "nav_group_account": "账户"
  },
```

```json
"directory": {
  "eyebrow": "创作工具",
  "hero_title": "挑一个功能，Aurora 帮你跑完。",
  "stats_runnable": "个功能可运行",
  "stats_total": "个功能",
  "visible_count": "{{visible}} 个功能可用",
  "ready": "可运行",
  "clear_filters": "查看全部功能",
  "inputs": {
    "text": "文本",
    "image": "图片",
    "document": "文档",
    "audio": "音频",
    "video": "视频",
    "spreadsheet": "表格",
    "other": "其他"
  }
}
```

```text
works     → "eyebrow": "作品"
history   → "eyebrow": "历史"
detail    → "eyebrow": "生成"
runtime   → "eyebrow": "运行时"
billing   → "eyebrow": "计费"
```

`ja/aurora.json`：

```json
  "shell": {
    "brand_tagline": "AI STUDIO",
    "menu_open": "メニューを開く",
    "menu_close": "メニューを閉じる",
    "account_menu": "アカウント",
    "nav_group_work": "ワークスペース",
    "nav_group_account": "アカウント"
  },
```

```json
"directory": {
  "eyebrow": "AI ツール",
  "hero_title": "スキルを選ぶだけ。実行は Aurora が行います。",
  "stats_runnable": "件が実行可能",
  "stats_total": "件のスキル",
  "visible_count": "{{visible}} 件が利用可能",
  "ready": "実行可能",
  "clear_filters": "すべてのスキルを表示",
  "inputs": {
    "text": "テキスト",
    "image": "画像",
    "document": "ドキュメント",
    "audio": "音声",
    "video": "動画",
    "spreadsheet": "表計算",
    "other": "その他"
  }
}
```

```text
works     → "eyebrow": "作品"
history   → "eyebrow": "履歴"
detail    → "eyebrow": "生成"
runtime   → "eyebrow": "実行環境"
billing   → "eyebrow": "クレジット"
```

`ko/aurora.json`：

```json
  "shell": {
    "brand_tagline": "AI STUDIO",
    "menu_open": "메뉴 열기",
    "menu_close": "메뉴 닫기",
    "account_menu": "계정",
    "nav_group_work": "워크스페이스",
    "nav_group_account": "계정"
  },
```

```json
"directory": {
  "eyebrow": "AI 도구",
  "hero_title": "스킬을 고르면 Aurora가 실행합니다.",
  "stats_runnable": "개 실행 가능",
  "stats_total": "개 스킬",
  "visible_count": "{{visible}}개 사용 가능",
  "ready": "실행 가능",
  "clear_filters": "전체 스킬 보기",
  "inputs": {
    "text": "텍스트",
    "image": "이미지",
    "document": "문서",
    "audio": "오디오",
    "video": "동영상",
    "spreadsheet": "스프레드시트",
    "other": "기타"
  }
}
```

```text
works     → "eyebrow": "작품"
history   → "eyebrow": "기록"
detail    → "eyebrow": "생성"
runtime   → "eyebrow": "실행 환경"
billing   → "eyebrow": "크레딧"
```

`fr/aurora.json`：

```json
  "shell": {
    "brand_tagline": "AI STUDIO",
    "menu_open": "Ouvrir le menu",
    "menu_close": "Fermer le menu",
    "account_menu": "Compte",
    "nav_group_work": "Espace de travail",
    "nav_group_account": "Compte"
  },
```

```json
"directory": {
  "eyebrow": "OUTILS IA",
  "hero_title": "Choisissez une compétence, Aurora s'en charge.",
  "stats_runnable": "compétences exécutables",
  "stats_total": "compétences au catalogue",
  "visible_count": "{{visible}} compétences disponibles",
  "ready": "Prêt",
  "clear_filters": "Afficher toutes les compétences",
  "inputs": {
    "text": "Texte",
    "image": "Images",
    "document": "Documents",
    "audio": "Audio",
    "video": "Vidéo",
    "spreadsheet": "Tableurs",
    "other": "Autre"
  }
}
```

```text
works     → "eyebrow": "CRÉATIONS"
history   → "eyebrow": "HISTORIQUE"
detail    → "eyebrow": "GÉNÉRATION"
runtime   → "eyebrow": "EXÉCUTION"
billing   → "eyebrow": "CRÉDITS"
```

- [ ] **Step 2: 跑平价测试**

Run: `pnpm --filter @multica/views test -- parity`
Expected: PASS（5 个 locale 的键集完全一致）。

- [ ] **Step 3: 建 `packages/views/aurora/aurora-account-panel.tsx`**

```tsx
"use client";

import { ChevronRight, LogOut } from "lucide-react";
import {
  isAuroraDegraded,
  useAuroraBalance,
  useAuroraSubscription,
} from "@multica/core/aurora";
import { useAuthStore } from "@multica/core/auth";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { AppLink } from "../navigation";
import { useLocale, useT } from "../i18n";
import { useLogout } from "../auth";
import { avatarInitial } from "./aurora-avatar";
import { formatMicroCredits } from "./format";
import { subscriptionTierLabel } from "./labels";

/**
 * The account, on the two surfaces that show it: the sidebar panel and the
 * dialog behind its ⋯ button.
 *
 * Both live in one file because they read the same identity and the same
 * wallet, and both obey one rule about unread data: a read the client has not
 * made prints the unknown marker, never a zero. "You have 0 credits" is not a
 * degraded rendering of an unread balance — it is a different statement, and
 * one that sends the reader to a checkout page over it.
 */

/** A glyph, not copy: it stands in for a number the client has not read. */
const UNKNOWN = "—";

function useAccountIdentity() {
  const user = useAuthStore((state) => state.user);
  const name = user?.name?.trim() || user?.email || "";
  return { name, email: user?.email ?? "" };
}

export function AuroraAccountPanel({
  creditsHref,
  onOpenAccount,
}: {
  creditsHref: string;
  onOpenAccount: () => void;
}) {
  const { t } = useT("aurora");
  const locale = useLocale();
  const { name } = useAccountIdentity();
  const balanceQuery = useAuroraBalance();
  const subscriptionQuery = useAuroraSubscription();

  const balance = balanceQuery.data?.value;
  const balanceReadable =
    balance !== undefined && !isAuroraDegraded(balanceQuery.data);
  const subscription = subscriptionQuery.data;
  const used = subscription?.usage.generationsUsedThisMonth;
  const limit = subscription?.limits.generationsPerMonth;
  const percent =
    limit !== undefined && used !== undefined && limit > 0
      ? Math.min(100, (used / limit) * 100)
      : 0;

  return (
    <section className="aurora-user-panel">
      <div className="aurora-user-row">
        <span className="aurora-user-avatar" aria-hidden="true">
          {avatarInitial(name)}
        </span>
        <div className="aurora-user-copy">
          <strong>{name}</strong>
          {subscription ? (
            <span>
              {t(
                ($) =>
                  $.billing.subscription.tiers[
                    subscriptionTierLabel(subscription.tier)
                  ].name,
              )}
            </span>
          ) : null}
        </div>
        <button
          type="button"
          className="aurora-more-button"
          aria-label={t(($) => $.shell.account_menu)}
          onClick={onOpenAccount}
        >
          <span aria-hidden="true">•••</span>
        </button>
      </div>

      <div className="aurora-token-row">
        <span>{t(($) => $.billing.balance_title)}</span>
        <strong>
          {balanceReadable
            ? formatMicroCredits(balance.availableMicro, locale)
            : UNKNOWN}
        </strong>
      </div>

      {/* The meter measures the monthly generation allowance, not the wallet:
          credits are the reference's decorative bar there, while the allowance
          is the number that has a real denominator on this API. */}
      {subscription && limit !== undefined ? (
        <>
          <div className="aurora-token-row">
            <span>{t(($) => $.billing.subscription.usage_label)}</span>
            <strong>
              {t(($) => $.billing.subscription.usage_value, {
                used: used ?? 0,
                limit,
              })}
            </strong>
          </div>
          <span
            className="aurora-meter"
            role="progressbar"
            aria-valuemin={0}
            aria-valuemax={limit}
            aria-valuenow={used ?? 0}
            aria-label={t(($) => $.billing.subscription.usage_label)}
          >
            <span
              className="aurora-meter-fill"
              style={{ width: percent + "%" }}
            />
          </span>
        </>
      ) : null}

      <AppLink href={creditsHref} className="aurora-recharge-button">
        {t(($) => $.billing.top_up)}
      </AppLink>
    </section>
  );
}

export function AuroraAccountDialog({
  open,
  onOpenChange,
  creditsHref,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  creditsHref: string;
}) {
  const { t } = useT("aurora");
  const { t: tLayout } = useT("layout");
  const { name, email } = useAccountIdentity();
  const logout = useLogout();

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="aurora-dialog aurora-dialog--account">
        <div className="aurora-modal-hero">
          <span className="aurora-user-avatar" aria-hidden="true">
            {avatarInitial(name)}
          </span>
          {/* The identity is the heading: a title that said "Account" would
              name the surface, not the person on it. */}
          <DialogTitle>{name}</DialogTitle>
          {email ? <DialogDescription>{email}</DialogDescription> : null}
        </div>
        <div className="aurora-modal-actions">
          <AppLink href={creditsHref}>
            {t(($) => $.billing.title)}
            <ChevronRight aria-hidden="true" className="size-4" />
          </AppLink>
          <button
            type="button"
            onClick={() => {
              onOpenChange(false);
              logout();
            }}
          >
            {tLayout(($) => $.sidebar.log_out)}
            <LogOut aria-hidden="true" className="size-4" />
          </button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
```

- [ ] **Step 4: `runtime-status.tsx` 换共享词表并加 chip**

把文件顶部原来的 `STATE_VISUAL` 换成下面这份**导出**的（三处要共用），页面里的用法从 `visual.dot`/`visual.tone` 改成 `visual.dot`/`visual.text`：

```tsx
/**
 * The runtime state vocabulary, in one place: the dot class, the pill modifier
 * and the tonal text class. Exported because three surfaces name the same five
 * states — this page, the directory hero and the topbar chip — and a second
 * mapping would be a second thing to keep in step.
 */
export const AURORA_RUNTIME_VISUAL: Record<
  AuroraRuntimeState,
  { dot: string; pill: string; text: string }
> = {
  online: {
    dot: "aurora-dot",
    pill: "aurora-mini-label",
    text: "text-success",
  },
  offline: {
    dot: "aurora-dot aurora-dot--warning",
    pill: "aurora-mini-label aurora-mini-label--warning",
    text: "text-warning",
  },
  provisioning: {
    dot: "aurora-dot aurora-dot--running",
    pill: "aurora-mini-label aurora-mini-label--running",
    text: "text-info",
  },
  failed: {
    dot: "aurora-dot aurora-dot--error",
    pill: "aurora-mini-label aurora-mini-label--error",
    text: "text-destructive",
  },
  unconfigured: {
    dot: "aurora-dot aurora-dot--muted",
    pill: "aurora-mini-label aurora-mini-label--muted",
    text: "text-muted-foreground",
  },
};
```

同文件末尾追加（补 `useT`、`AppLink` 的 import）：

```tsx
/** The runtime state, as a link to the page that explains it. Renders nothing
 *  until the read resolves: no chip beats a wrong one. */
export function AuroraRuntimeChip({ href }: { href: string }) {
  const { t } = useT("aurora");
  const runtime = useAuroraRuntime();
  const target = runtime.data?.value;
  if (!target) return null;
  return (
    <AppLink href={href} className="aurora-status-chip">
      <span
        aria-hidden="true"
        className={AURORA_RUNTIME_VISUAL[target.state].dot}
      />
      {t(($) => $.runtime.status[target.state])}
    </AppLink>
  );
}
```

`packages/views/aurora/index.ts` 追加：

```ts
export {
  AuroraAccountDialog,
  AuroraAccountPanel,
} from "./aurora-account-panel";
export { AuroraRuntimeChip } from "./runtime-status";
```

- [ ] **Step 5: 重写 `apps/aurora/components/aurora-shell.tsx`**

```tsx
"use client";

import { useEffect, useState } from "react";
import type { LucideIcon } from "lucide-react";
import {
  Coins,
  History,
  Images,
  LogOut,
  Menu,
  Server,
  Sparkles,
  X,
} from "lucide-react";
import {
  AuroraAccountDialog,
  AuroraAccountPanel,
  AuroraRuntimeChip,
} from "@multica/views/aurora";
import { AppLink, useNavigation } from "@multica/views/navigation";
import { useT } from "@multica/views/i18n";
import { useLogout } from "@multica/views/auth";
import {
  Sheet,
  SheetClose,
  SheetContent,
  SheetTitle,
  SheetTrigger,
} from "@multica/ui/components/ui/sheet";
import { auroraRoutes } from "@/lib/routes";

/**
 * The app's chrome: one navy column, five destinations, one account action.
 *
 * A destination is a link, never a button — the shell is the only navigator in
 * Aurora, and a plain anchor keeps middle-click, "copy link" and the browser's
 * own history semantics working. Labels come from the `aurora` namespace so the
 * nav and the page it opens always name the same thing.
 *
 * At 900px and below, the column lives in a modal Sheet. The primitive owns
 * initial focus, Tab containment, Escape, outside dismissal and focus return
 * to the SheetTrigger. Only one copy of the navigation is mounted.
 */
export function AuroraShell({
  slug,
  children,
}: {
  slug: string;
  children: React.ReactNode;
}) {
  const { t } = useT("aurora");
  const { t: tLayout } = useT("layout");
  const { pathname } = useNavigation();
  const logout = useLogout();
  const routes = auroraRoutes(slug);
  const [navOpen, setNavOpen] = useState(false);
  const [compact, setCompact] = useState(false);
  const [accountOpen, setAccountOpen] = useState(false);

  useEffect(() => {
    // Match the stylesheet exactly; existing useIsMobile uses a different cutoff.
    const media = window.matchMedia("(max-width: 900px)");
    const sync = () => {
      setCompact(media.matches);
      if (!media.matches) setNavOpen(false);
    };
    sync();
    media.addEventListener("change", sync);
    return () => media.removeEventListener("change", sync);
  }, []);

  const workItems = [
    {
      href: routes.skills(),
      icon: Sparkles,
      label: t(($) => $.directory.title),
    },
    { href: routes.works(), icon: Images, label: t(($) => $.works.title) },
    { href: routes.history(), icon: History, label: t(($) => $.history.title) },
  ];
  const accountItems = [
    { href: routes.runtime(), icon: Server, label: t(($) => $.runtime.title) },
    { href: routes.billing(), icon: Coins, label: t(($) => $.billing.title) },
  ];
  const worksHref = routes.works();
  // Trailing slashes are the same destination, and Next keeps the URL the user
  // typed. Comparing raw strings would drop the selected state for `…/works/`.
  const current = pathname.replace(/\/+$/, "");
  const selected = (href: string) =>
    current === href ||
    // The works destination owns the per-generation detail route below it
    // (…/works/{generationId}), so it stays selected while one is open; every
    // other destination matches its own path exactly.
    (href === worksHref && current.startsWith(worksHref + "/"));

  // A committed route closes both overlays: each one covers the page it just
  // opened. `pathname` is the dependency because the adapter reports the route
  // the app landed on, not the click that started it.
  useEffect(() => {
    setNavOpen(false);
    setAccountOpen(false);
  }, [pathname]);

  const sidebar = (
    <nav
      id="aurora-nav"
      aria-label="Aurora"
      className="aurora-sidebar"
    >
      <div className="aurora-sidebar-top">
        <span className="aurora-brand">
          <span aria-hidden="true" className="aurora-brand-mark">
            A
          </span>
          <span className="aurora-brand-copy">
            <strong>Aurora</strong>
            <small>{t(($) => $.shell.brand_tagline)}</small>
          </span>
        </span>
        {compact ? (
          <SheetClose
            className="aurora-sidebar-close"
            aria-label={t(($) => $.shell.menu_close)}
          >
            <X aria-hidden="true" className="size-4" />
          </SheetClose>
        ) : null}
      </div>

      <AuroraAccountPanel
        creditsHref={routes.billing()}
        onOpenAccount={() => setAccountOpen(true)}
      />

      <div className="aurora-nav-groups">
        <div className="aurora-nav-group">
          <p>{t(($) => $.shell.nav_group_work)}</p>
          <ul>
            {workItems.map((item) => (
              <NavLink
                key={item.href}
                {...item}
                selected={selected(item.href)}
              />
            ))}
          </ul>
        </div>
        <div className="aurora-nav-group">
          <p>{t(($) => $.shell.nav_group_account)}</p>
          <ul>
            {accountItems.map((item) => (
              <NavLink
                key={item.href}
                {...item}
                selected={selected(item.href)}
              />
            ))}
          </ul>
        </div>
      </div>

      <div className="aurora-sidebar-footer">
        <button type="button" className="aurora-nav-link" onClick={logout}>
          <LogOut aria-hidden="true" />
          {tLayout(($) => $.sidebar.log_out)}
        </button>
      </div>
    </nav>
  );

  return (
    <Sheet open={compact && navOpen} onOpenChange={setNavOpen}>
      <div className="aurora-app">
        {compact ? (
          <SheetContent side="left" showCloseButton={false} className="aurora-nav-drawer">
            <SheetTitle className="sr-only">Aurora</SheetTitle>
            {sidebar}
          </SheetContent>
        ) : sidebar}

        <div className="aurora-main">
          <header className="aurora-topbar">
            {compact ? (
              <SheetTrigger
                className="aurora-menu-button"
                aria-label={t(($) => $.shell.menu_open)}
              >
                <Menu aria-hidden="true" className="size-4" />
              </SheetTrigger>
            ) : null}
            <span className="aurora-topbar-brand">
              Aurora <span>AI</span>
            </span>
            <div className="aurora-topbar-actions">
              {/* The runtime state belongs in the chrome: every page here starts a
                  generation, and a node that is offline is the reason the next
                  one will not. */}
              <AuroraRuntimeChip href={routes.runtime()} />
            </div>
          </header>
          <main className="aurora-scroll">{children}</main>
        </div>

        <AuroraAccountDialog
          open={accountOpen}
          onOpenChange={setAccountOpen}
          creditsHref={routes.billing()}
        />
      </div>
    </Sheet>
  );
}

function NavLink({
  href,
  icon: Icon,
  label,
  selected,
}: {
  href: string;
  icon: LucideIcon;
  label: string;
  selected: boolean;
}) {
  return (
    <li>
      <AppLink
        href={href}
        aria-current={selected ? "page" : undefined}
        className="aurora-nav-link"
      >
        <Icon aria-hidden="true" />
        <span className="truncate">{label}</span>
      </AppLink>
    </li>
  );
}
```

- [ ] **Step 6: 扩充 `apps/aurora/components/aurora-shell.test.tsx`**

顶部追加两个 mock（`vi.mock` 会被提升，摆放顺序无所谓）：

```tsx
import { useAuthStore } from "@multica/core/auth";

// The account panel and the runtime chip read the wallet, the plan and the
// node. Mocked at the hook boundary so the shell's own job — chrome, links,
// selected state — is what this suite measures.
vi.mock("@multica/core/aurora", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/aurora")>();
  return {
    ...actual,
    useAuroraBalance: () => ({
      data: { value: { availableMicro: 2_480_000_000 }, degraded: false },
      isPending: false,
    }),
    useAuroraSubscription: () => ({
      data: {
        tier: "creator",
        status: "active",
        currentPeriodEnd: null,
        cancelAtPeriodEnd: false,
        limits: { generationsPerMonth: 30, concurrency: 1 },
        usage: { generationsUsedThisMonth: 12, activeGenerations: 0 },
      },
      isPending: false,
    }),
    useAuroraRuntime: () => ({
      data: {
        value: {
          workspaceId: "ws",
          node: null,
          runtimeId: null,
          state: "online",
        },
        degraded: false,
      },
      isPending: false,
    }),
  };
});
```

测试顶部从 vitest 补 `beforeEach`/`afterEach`，从 Testing Library 补 `cleanup`/`waitFor`，再加以下媒体查询桩。每条现有测试默认桌面；移动测试显式切换到 900px 分支。Sheet 在 jsdom 中还需要 `window.matchMedia`，不能只把 `innerWidth` 改小。

```tsx
function stubCompact(compact: boolean) {
  vi.stubGlobal("matchMedia", vi.fn((query: string) => ({
    matches: query === "(max-width: 900px)" && compact,
    media: query,
    onchange: null,
    addListener: vi.fn(),
    removeListener: vi.fn(),
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    dispatchEvent: vi.fn(() => true),
  })));
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
```

`describe` 之前加 `beforeEach`：

```tsx
beforeEach(() => {
  stubCompact(false);
  // The sidebar panel names the reader, so the shell needs an identity to
  // render under. The store is real here; only the server reads are mocked.
  useAuthStore.setState({
    user: {
      id: "u1",
      name: "Hannah Han",
      email: "hannah@apex.ai",
      avatar_url: null,
      onboarded_at: null,
      onboarding_questionnaire: {},
    },
    isLoading: false,
  });
});
```

`describe` 末尾追加四条（新引入 `userEvent` 与 `within`）：

```tsx
  it("opens the mobile navigation and returns focus after Escape", async () => {
    stubCompact(true);
    const user = userEvent.setup();
    renderShell("/acme/skills");
    const opener = await screen.findByRole("button", { name: "Open menu" });
    expect(screen.queryByRole("navigation", { name: "Aurora" })).toBeNull();
    opener.focus();
    await user.keyboard("{Enter}");
    const dialog = await screen.findByRole("dialog", { name: "Aurora" });
    expect(within(dialog).getByRole("navigation", { name: "Aurora" })).toBeVisible();
    await waitFor(() => expect(dialog.contains(document.activeElement)).toBe(true));
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Aurora" })).toBeNull());
    expect(opener).toHaveFocus();
    expect(screen.queryByRole("navigation", { name: "Aurora" })).toBeNull();
  });

  it("shows the plan, the balance and this month's usage", () => {
    renderShell("/acme/skills");

    expect(screen.getByText("Hannah Han")).toBeVisible();
    expect(screen.getByText("Creator")).toBeVisible();
    expect(screen.getByText("2,480")).toBeVisible();
    expect(screen.getByText("12 of 30")).toBeVisible();
    expect(screen.getByRole("link", { name: "Top up" })).toHaveAttribute(
      "href",
      "/acme/billing",
    );
  });

  it("links the runtime state to the page that explains it", () => {
    renderShell("/acme/skills");

    expect(screen.getByRole("link", { name: "Online" })).toHaveAttribute(
      "href",
      "/acme/runtimes",
    );
  });

  it("opens the account dialog from the sidebar's more button", async () => {
    const user = userEvent.setup();
    renderShell("/acme/skills");

    await user.click(screen.getByRole("button", { name: "Account" }));

    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText("hannah@apex.ai")).toBeVisible();
    expect(
      within(dialog).getByRole("link", { name: "Credits" }),
    ).toHaveAttribute("href", "/acme/billing");
    expect(
      within(dialog).getByRole("button", { name: "Log out" }),
    ).toBeVisible();
  });
```

- [ ] **Step 7: 跑测试确认绿**

Run: `pnpm --filter @multica/aurora test -- aurora-shell && pnpm --filter @multica/views test -- runtime-status`
Expected: PASS。原有 7 条断言（5 个链接、选中态、尾斜杠、详情路由、未拥有路由、children、Log out）全部保持通过。

- [ ] **Step 8: 手动看一遍响应式与键盘**

Run: `pnpm dev:aurora`。
Expected: 侧栏深蓝 `#13223a`，选中项左侧有紫色指示条；窗口缩到 900px 及以下侧栏收起，顶栏出现 ☰ 与品牌名；点 ☰ 打开共享 Sheet，焦点进入抽屉；Tab/Shift+Tab 不进入背景，Esc/遮罩/关闭按钮可关且焦点返回 ☰；关闭时侧栏不挂载；账户弹窗 380px、16px 圆角、modal 阴影；顶栏右侧是状态 chip + 账户按钮。

- [ ] **Step 9: Commit**

```bash
git add packages/views/aurora/aurora-account-panel.tsx packages/views/aurora/runtime-status.tsx packages/views/aurora/index.ts apps/aurora/components/aurora-shell.tsx apps/aurora/components/aurora-shell.test.tsx packages/views/locales
git commit -m "feat(aurora): rebuild the app shell on the design system"
```

---

### Task 4: 技能目录（Hero + 分类 pill + 卡片网格）

**Files:**
- Modify: `packages/views/aurora/labels.ts`（新增 `auroraInputLabel`）
- Modify: `packages/views/aurora/skill-directory.tsx`
- Test: `packages/views/aurora/labels.test.ts`
- Test: `packages/views/aurora/skill-directory.test.tsx`

**Interfaces:**
- Consumes: Task 2 的 `AuroraAvatar`、`AuroraPageState`；Task 3 的 `AURORA_RUNTIME_VISUAL`；`directory.{eyebrow,hero_title,stats_runnable,stats_total,visible_count,ready,clear_filters,inputs.*}`（Task 3 Step 1 已写入 5 个 locale）。
- Produces: `auroraInputLabel(modality: string): AuroraInputLabel`（`"text" | "image" | "document" | "audio" | "video" | "spreadsheet" | "other"`），导出给目录卡片的 chips 用；`SkillDirectory` 签名不变。

**这一页刻意没有 `AuroraPageHeader`。** Hero 就是它的页头：一个 h1 + 真实状态数字。再叠一个 h1 会变成两层标题，而参考实现在这一页也只有 Hero；但 **`.directory-head` 那一段要有**——eyebrow + h2 + 右侧"按当前筛选可见的条数"，参考的 `{visibleAgents.length} 个功能可用` 正是这个。

- [ ] **Step 1: 先写测试**

在 `skill-directory.test.tsx` 的 `mocks` 里加 `runtime: vi.fn()`，`vi.mock` 工厂里加 `useAuroraRuntime: () => mocks.runtime()`，`beforeEach` 里加：

```tsx
    mocks.runtime.mockReturnValue({
      data: {
        value: { workspaceId: "ws", node: null, runtimeId: null, state: "online" },
        degraded: false,
      },
      isPending: false,
    });
```

`describe` 末尾追加：

```tsx
  it("summarises the catalog and the node in the hero", () => {
    renderDirectory();

    expect(screen.getByText("Online")).toBeVisible();

    const hero = screen.getByRole("heading", { level: 1 }).closest("section");
    // 16 skills in the catalog, 3 of which cannot run yet.
    expect(hero).toHaveTextContent("13 skills runnable");
    expect(hero).toHaveTextContent("16 skills in the catalog");
  });

  it("counts what the filter left, and reopens the grid from the empty state", async () => {
    const user = userEvent.setup();
    renderDirectory();

    // The line counts the visible rows, not the catalog — that is what makes it
    // worth a line of its own above the grid.
    expect(screen.getByText("16 skills available")).toBeVisible();

    await user.type(
      screen.getByPlaceholderText("Search skills"),
      "nothing matches this",
    );
    expect(screen.getByText("0 skills available")).toBeVisible();

    await user.click(screen.getByRole("button", { name: "Show all skills" }));
    expect(screen.getByText("16 skills available")).toBeVisible();
    expect(cards()).toHaveLength(16);
  });

  it("shows each skill's input modalities, readiness and cost", () => {
    renderDirectory();

    const poster = screen.getByRole("button", { name: /Poster/ });
    // poster accepts text + image (server/internal/aurora/catalog.go).
    expect(within(poster).getByText("Text")).toBeVisible();
    expect(within(poster).getByText("Images")).toBeVisible();
    expect(within(poster).getByText("Ready")).toBeVisible();
    expect(within(poster).getByText("76 credits")).toBeVisible();

    const ppt = screen.getByRole("button", { name: /PPT/ });
    expect(within(ppt).getByText("Coming soon")).toBeVisible();
  });
```

（`cards()`/`userEvent`/`within` 都用该文件已有的那套。）

- [ ] **Step 2: 跑测试确认红**

Run: `pnpm --filter @multica/views test -- skill-directory`
Expected: FAIL — 页面上没有 h1（`getByRole("heading", { level: 1 })` 找不到）。

- [ ] **Step 3: 先加模态标签助手，再加 `DirectoryHero`**

`labels.ts` 里按现有 `auroraCategoryLabel` 的同一形状加一份（目录卡片的能力 chips 需要它：`skill.input` 是 `text`/`image`/`document`/`audio`/`video`/`spreadsheet` 这些裸值）：

```ts
/** What a skill accepts, as `directory.inputs.*`. */
export type AuroraInputLabel =
  | "text"
  | "image"
  | "document"
  | "audio"
  | "video"
  | "spreadsheet"
  | "other";

const INPUT_LABELS: Record<string, AuroraInputLabel> = {
  text: "text",
  image: "image",
  document: "document",
  audio: "audio",
  video: "video",
  spreadsheet: "spreadsheet",
};

/**
 * The label key for an input modality. A value from a newer server falls to
 * "other" — the catalog type is open on the wire, and an unlabelled chip would
 * render as an empty pill.
 */
export function auroraInputLabel(modality: string): AuroraInputLabel {
  return INPUT_LABELS[modality] ?? "other";
}
```

`labels.test.ts` 追加：

```ts
describe("auroraInputLabel", () => {
  it("maps the catalog's modalities", () => {
    expect(auroraInputLabel("image")).toBe("image");
    expect(auroraInputLabel("spreadsheet")).toBe("spreadsheet");
  });

  it("falls back for a modality this build has not seen", () => {
    expect(auroraInputLabel("hologram")).toBe("other");
  });
});
```

然后 `skill-directory.tsx` 里新增 `DirectoryHero`（import 需要补 `AuroraAvatar`、`AURORA_RUNTIME_VISUAL`、`useAuroraRuntime`）：

```tsx
function DirectoryHero({ skills }: { skills: AuroraSkill[] }) {
  const { t } = useT("aurora");
  const locale = useLocale();
  const runtime = useAuroraRuntime();
  const state = runtime.data?.value?.state;
  const runnable = skills.filter((skill) => skill.available);
  return (
    <section className="aurora-hero">
      <div>
        {state ? (
          <span className={AURORA_RUNTIME_VISUAL[state].pill}>
            {t(($) => $.runtime.status[state])}
          </span>
        ) : null}
        <h1>{t(($) => $.directory.hero_title)}</h1>
        <div className="aurora-hero-stats">
          <span>
            <strong>{runnable.length}</strong>{" "}
            {t(($) => $.directory.stats_runnable)}
          </span>
          <span>
            <strong>{skills.length}</strong>{" "}
            {t(($) => $.directory.stats_total)}
          </span>
        </div>
      </div>
      {/* Decorative: the four runnable skills as tiles. It carries no fact the
          stats above do not, which is why it is hidden from assistive tech. */}
      <div className="aurora-hero-orbit" aria-hidden="true">
        <div className="aurora-hero-center">
          <Sparkles className="size-6" />
        </div>
        {runnable.slice(0, 4).map((skill, index) => (
          <span
            key={skill.id}
            className={
              "aurora-orbit-avatar aurora-orbit-avatar--" + (index + 1)
            }
          >
            <AuroraAvatar
              name={skillDisplayName(skill, locale)}
              id={skill.id}
              available
            />
          </span>
        ))}
      </div>
    </section>
  );
}```

- [ ] **Step 4: 重排页面骨架（section head + 搜索框 + 空态恢复）**

把 `SkillDirectory` 的返回改成 `.aurora-page` 列，并把参考首页的四个区域依次落位（Hero → directory-head → 工具条 → 网格）：

```tsx
  return (
    <div className="aurora-page">
      <DirectoryHero skills={skills} />

      <div className="aurora-section-head">
        <div>
          <p className="aurora-eyebrow">
            <LayoutGrid aria-hidden="true" className="size-3.5" />
            {t(($) => $.directory.eyebrow)}
          </p>
          <h2>{t(($) => $.directory.title)}</h2>
        </div>
        {/* The reference counts what the filter left, not the catalog: the line
            has to change when the reader narrows the grid, or it is noise. */}
        <span>
          {t(($) => $.directory.visible_count, { visible: visible.length })}
        </span>
      </div>

      <Tabs value={category} onValueChange={setCategory} className="gap-0">
        <div className="aurora-toolbar">
          <TabsList className="aurora-pill-tabs">
            <TabsTrigger value={AURORA_CATEGORY_ALL} className="aurora-pill-tab">
              {t(($) => $.directory.categories.all)}
            </TabsTrigger>
            {categories.map((value) => (
              <TabsTrigger key={value} value={value} className="aurora-pill-tab">
                {t(($) => $.directory.categories[auroraCategoryLabel(value)])}
              </TabsTrigger>
            ))}
          </TabsList>
          {/* The reference puts this box in the top bar, where it looks global
              but only ever filtered this grid. It stays beside the tabs it
              belongs to; the box itself is the same one — icon, white field,
              hairline border, violet border on focus. */}
          <InputGroup className="h-10 w-full max-w-[280px]">
            <InputGroupAddon>
              <Search aria-hidden="true" />
            </InputGroupAddon>
            <InputGroupInput
              type="search"
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder={t(($) => $.directory.search_placeholder)}
              aria-label={t(($) => $.directory.search_placeholder)}
            />
          </InputGroup>
        </div>
        <TabsContent value={category}>
          {skillsQuery.isPending ? (
            <DirectorySkeleton />
          ) : catalogUnreadable && !hasCatalog ? (
            <AuroraLoadFailed
              title={t(($) => $.directory.load_failed_title)}
              onRetry={() => void skillsQuery.refetch()}
            />
          ) : visible.length === 0 ? (
            // The reference gives the empty grid a way out instead of only an
            // apology, and it is the same recovery: clear what was typed and
            // what was selected.
            <AuroraPageState
              icon={SearchX}
              title={t(($) => $.directory.empty_title)}
              description={t(($) => $.directory.empty_description)}
              actions={
                <Button
                  type="button"
                  variant="outline"
                  onClick={() => {
                    setQuery("");
                    setCategory(AURORA_CATEGORY_ALL);
                  }}
                >
                  {t(($) => $.directory.clear_filters)}
                </Button>
              }
            />
          ) : (
            <ul className={GRID_CLASS}>
              {visible.map((skill) => (
                <li key={skill.id}>
                  <SkillCard
                    skill={skill}
                    displayName={skillDisplayName(skill, locale)}
                    credits={formatCredits(skill.credits, locale)}
                    onSelect={() => setSelected(skill)}
                  />
                </li>
              ))}
            </ul>
          )}
        </TabsContent>
      </Tabs>
      <GenerationComposer
        skill={selected}
        open={selected !== null}
        onOpenChange={(open) => {
          if (!open) setSelected(null);
        }}
        worksHref={worksHref}
        topUpHref={topUpHref}
      />
    </div>
  );
```

逐项对照（这是本 Task 的完整改动清单）：

| 位置 | 现在 | 改成 |
|---|---|---|
| 根 div | `flex h-full min-h-0 flex-col` | `aurora-page` |
| `CollectionPageHeader` 整块 | 图标 + 标题 + count + Input | **删掉**，换成 `<DirectoryHero skills={skills} />` + 上面的 `.aurora-section-head` |
| `GRID_CLASS` | `grid grid-cols-[repeat(auto-fill,minmax(11rem,1fr))] gap-3` | `aurora-card-grid` |
| `Tabs` | `flex min-h-0 flex-1 flex-col gap-0` | `gap-0` |
| 工具条 div | `{PAGE_TOOLBAR}` | `aurora-toolbar` |
| `TabsList` / `TabsTrigger` | 无 | `aurora-pill-tabs` / `aurora-pill-tab` |
| Search `Input` | 在 header 的 actions 里，`w-48 sm:w-56` | `InputGroup` + `InputGroupAddon`（`Search` 图标）+ `InputGroupInput` |
| `TabsContent` | `min-h-0 flex-1 overflow-y-auto px-4 py-3` | 不传 className |
| 空态 | `CollectionPageState`（无动作） | `AuroraPageState` + 清空搜索与分类的动作 |
| 骨架卡片 | `h-20 w-full rounded-lg` | `h-20 w-full rounded-xl` |
| import | `CollectionPageHeader`、`PAGE_TOOLBAR` | 删；换成 `AuroraPageState`、`AuroraAvatar`、`AURORA_RUNTIME_VISUAL`、`auroraInputLabel`、`InputGroup`/`InputGroupAddon`/`InputGroupInput`、`Button`、`LayoutGrid`/`Search`/`SearchX` |

滚动条归外壳所有（`<main className="aurora-scroll">`），所以视图内部不再有自己的 `overflow-y-auto`。

- [ ] **Step 5: 换掉 `SkillCard` 的皮肤（含能力 chips 与 footer）**

```tsx
function SkillCard({
  skill,
  displayName,
  credits,
  onSelect,
}: {
  skill: AuroraSkill;
  displayName: string;
  credits: string;
  onSelect: () => void;
}) {
  const { t } = useT("aurora");
  return (
    // One button, not a card with a button in it: the whole tile is the target,
    // and the arrow is decoration inside it rather than a second control. The
    // grid's own test helper counts the buttons in this list.
    <button
      type="button"
      onClick={onSelect}
      className={cn(
        "aurora-card",
        skill.featured && "aurora-card--featured",
        !skill.available && "aurora-card--unavailable",
      )}
    >
      <span className="aurora-card-top">
        <AuroraAvatar
          name={displayName}
          id={skill.id}
          size="lg"
          available={skill.available}
        />
        <span className="aurora-card-name">
          <span className="aurora-card-category">
            {t(($) => $.directory.categories[auroraCategoryLabel(skill.category)])}
          </span>
          <span className="aurora-card-title">{displayName}</span>
        </span>
        <span aria-hidden="true" className="aurora-card-arrow">
          <ArrowUpRight className="size-4" />
        </span>
      </span>
      {/* What the skill accepts, from the catalog's own modalities. The
          reference shows three capability tags here. */}
      <span className="aurora-chips">
        {skill.input.map((modality) => (
          <span key={modality}>
            {t(($) => $.directory.inputs[auroraInputLabel(modality)])}
          </span>
        ))}
      </span>
      <span className="aurora-card-footer">
        {skill.available ? (
          <span className="aurora-card-state">
            <span aria-hidden="true" className="aurora-dot" />
            {t(($) => $.directory.ready)}
          </span>
        ) : (
          <span className="aurora-status-pill aurora-status-pill--muted">
            {t(($) => $.directory.unavailable)}
          </span>
        )}
        <strong>{t(($) => $.credits, { credits })}</strong>
      </span>
    </button>
  );
}
```

要点：

- 卡片**仍然只有一个 button**——网格的 `cards()` 辅助函数数的是 `getByRole("list")` 里的 button，卡内再放一个按钮就会让它数错。
- **消耗从卡片主体移到 footer**（参考的 footer 右侧就是"约 N 积分/次"）。同一个事实只说一次，所以 `.aurora-card-meta` 那一行不要再写一遍 credits。
- chips 的 key 用模态值本身（`text`/`image`/…），它是同一张卡里唯一的集合，且顺序来自目录。
- `available` 的两态：可运行时是绿点 + 文字（`.aurora-card-state`），不可运行时是灰胶囊（`.aurora-status-pill--muted`）。**不要**把胶囊套进 `.aurora-card-state`：那条规则的优先级更高，会把胶囊的文字重新染绿。

- [ ] **Step 6: 跑测试确认绿**

Run: `pnpm --filter @multica/views test -- skill-directory && pnpm --filter @multica/views typecheck`
Expected: PASS。重点复查：`cards()` 仍是 16/4；`"Coming soon"` 仍是 3；分类 tab 的 `getByRole("tab", { name: "Video" })` 仍能找到（`Tabs`/`TabsTrigger` 原语没换）。

- [ ] **Step 7: 看渲染**

Run: `pnpm dev:aurora` → `/skills`。
Expected: 深蓝 Hero 带径向紫光与点阵；左上角是状态 pill；标题 40px/-1px 字距；两个真实数字；右侧轨道上四个技能头像；下面是分类 pill（选中深蓝底白字）与右侧搜索框；卡片三列、白底 12px 圆角、悬停上浮 2px 且箭头变紫。

- [ ] **Step 8: Commit**

```bash
git add packages/views/aurora/skill-directory.tsx packages/views/aurora/skill-directory.test.tsx
git commit -m "feat(aurora): rebuild the skill directory on the design system"
```

---

### Task 5: 生成抽屉（Composer）与产出（Artifacts）

**Files:**
- Modify: `packages/views/aurora/generation-composer.tsx`
- Modify: `packages/views/aurora/generation-artifacts.tsx`
- Test: `packages/views/aurora/generation-composer.test.tsx`（只加断言，不改既有）
- Test: `packages/views/aurora/generation-artifacts.test.tsx`（可选：缩略图/下载行）

**Interfaces:**
- Consumes: Task 2 的 `AuroraAvatar`；Task 1 的 `.aurora-drawer*`、`.aurora-composer*`、`.aurora-thumbs`/`.aurora-thumb`、`.aurora-file-row`、`.aurora-dialog`/`--wide`。
- Produces: 无新导出。`GenerationComposer`/`GenerationArtifacts` 的 props 与行为完全不变（Sheet 仍负责焦点陷阱、Esc、遮罩关闭）。

**不要**给 `SheetContent` 换掉原语：抽屉的关闭按钮、焦点回填、`aria-modal` 全由它提供。参考实现里那个手写的 `×` 按钮不移植 —— 原语的关闭按钮已经带可访问名与正确位置，重写只会丢掉一层保障。

- [ ] **Step 1: 抽屉外壳（`generation-composer.tsx`）**

| 位置 | 现在 | 改成 |
|---|---|---|
| `SheetContent`（135） | `w-full gap-0 sm:max-w-md` | `aurora-drawer` |
| 抽屉主体根 div（363） | `flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto p-4` | `flex flex-col gap-5` |
| `SheetHeader`（364） | `p-0` | `aurora-drawer-head`，内部结构换成下面的三行式 |
| `form`（391） | `flex flex-col gap-2` | `aurora-composer` |
| 附件组 div（406） | `flex flex-col gap-2` | 不变 |
| 提交按钮那一行（新增） | 直接挂在 form 末尾 | 包一层 `<div className="aurora-composer-tools">` |
| 结果区 div（540） | `flex flex-col gap-2` | `aurora-drawer-section` |
| 结果下载按钮（548-553） | `inline-flex w-fit items-center gap-1.5 text-body underline …` | `aurora-file-row` |
| "打开作品库" `AppLink`（566-570） | `inline-flex w-fit items-center gap-1 text-body underline …` | `aurora-file-row` |

抽屉头部替换成（`categoryLabel` 用现成的 `auroraCategoryLabel` + `directory.categories.*`，显示名继续用现有 `skillDisplayName(skill, locale)`，消耗用 `formatCredits(skill.credits, locale)`）：

```tsx
<SheetHeader className="aurora-drawer-head">
  <AuroraAvatar
    name={skillDisplayName(skill, locale)}
    id={skill.id}
    size="lg"
    available={skill.available}
  />
  <div>
    <span className="aurora-drawer-head-category">{categoryLabel}</span>
    <SheetTitle>{skillDisplayName(skill, locale)}</SheetTitle>
    <SheetDescription>
      {t(($) => $.composer.cost, { credits: formatCredits(skill.credits, locale) })}
    </SheetDescription>
  </div>
</SheetHeader>```

表单结构（`.aurora-composer` 本身就是那个 44px 高的描边输入块，里面 textarea 去边框，所以 form 与输入块合成了一个元素）：

```tsx
<form className="aurora-composer" onSubmit={handleSubmit}>
  <Label htmlFor="aurora-prompt">{t(($) => $.composer.prompt_label)}</Label>
  <Textarea id="aurora-prompt" … />
  {/* 附件组（原样） */}
  <div className="aurora-composer-tools">
    <Button type="submit" …>…</Button>
  </div>
</form>
```

- [ ] **Step 2: 产出区（`generation-artifacts.tsx`）**

| 位置 | 现在 | 改成 |
|---|---|---|
| 骨架行 div（151） | `flex flex-wrap gap-2` | `aurora-thumbs` |
| 骨架 `Skeleton`（153） | `size-20 rounded-md` | `size-20 rounded-lg` |
| 缩略图 `ul`（180） | `flex flex-wrap gap-2` | `aurora-thumbs` |
| 缩略图按钮（183-190） | `group size-20 overflow-hidden rounded-md border border-surface-border bg-muted transition-colors hover:border-foreground/20 focus-visible:…` | `aurora-thumb` |
| 缩略图 `img`（193-200） | `size-full object-cover transition-transform group-hover:scale-105` | `size-full object-cover`（缩放交给 CSS） |
| 下载行按钮（212-217） | `inline-flex items-center gap-1.5 text-body text-muted-foreground underline …` | `aurora-file-row` |
| 预览 `DialogContent`（254） | `sm:max-w-3xl` | `aurora-dialog aurora-dialog--wide` |
| 预览 `img`（264-268） | `max-h-[70vh] w-full rounded-md object-contain` | `max-h-[70vh] w-full rounded-lg object-contain` |

**同一文件里把 `GenerationStatusBadge` 换成设计系统的状态胶囊**（works / history / 详情 / 抽屉四处共用它，改这一处四个页面一起换）：

```tsx
/**
 * The state pill, keyed by the same status label its text comes from: a run
 * still in flight is violet, a finished one mint, a failed one coral, and a
 * status this build has never seen stays neutral rather than claiming a state.
 */
export const GENERATION_STATUS_PILL: Record<AuroraGenerationStatusLabel, string> = {
  queued: "aurora-status-pill aurora-status-pill--working",
  running: "aurora-status-pill aurora-status-pill--working",
  completed: "aurora-status-pill",
  failed: "aurora-status-pill aurora-status-pill--failed",
  unknown: "aurora-status-pill aurora-status-pill--muted",
};

export function GenerationStatusBadge({
  status,
  className,
}: {
  status: string;
  className?: string;
}) {
  const { t } = useT("aurora");
  const label = generationStatusLabel(status);
  return (
    <span className={cn(GENERATION_STATUS_PILL[label], className)}>
      {t(($) => $.composer.status[label])}
    </span>
  );
}
```

（`Badge` 在该文件的 import 里随之删掉；`isAuroraGenerationTerminal` 仍被产物区使用，保留。）

抽屉里那个带 `role="status"` 的运行中状态（`generation-composer.tsx` 490 行）也换成同一颗胶囊，但**保留 live region**：`<span role="status" className={GENERATION_STATUS_PILL[generationStatusLabel(generation.status)]}>`。

其余一律不动：缩略图按钮的 `aria-label`（`history.preview_label`）、下载行的 `aria-label`（`works.download` + format）、`type="button"`、`loading="lazy"`、宽高 80 的防抖位、`aria-hidden` 的骨架容器、预览弹窗的 `DialogTitle`/`DialogDescription` 全部保留。

- [ ] **Step 3: 加断言（不改既有断言）**

`generation-composer.test.tsx` 末尾追加：

```tsx
  it("renders the prompt block as one bordered composer", async () => {
    renderComposer();
    await openComposer();

    // The textarea lives inside the bordered block, so the two are one control
    // visually and the label still points at the field.
    const field = screen.getByLabelText("What should it make?");
    expect(field.closest(".aurora-composer")).not.toBeNull();
    expect(
      within(field.closest(".aurora-composer") as HTMLElement).getByRole(
        "button",
        { name: "Generate" },
      ),
    ).toBeVisible();
  });
```

（`renderComposer()`/`openComposer()` 用该文件里已有的辅助函数；若名字不同，用它的等价物。）

- [ ] **Step 4: 跑测试**

Run: `pnpm --filter @multica/views test -- generation-composer generation-artifacts`
Expected: PASS。既有断言覆盖：不可用技能提示、附件上限与上传状态、402/429 分支、结果下载行、预览弹窗开关——都必须仍然通过。

- [ ] **Step 5: 看渲染**

Run: `pnpm dev:aurora` → `/skills` → 点任意技能。
Expected: 右侧滑入 540px 抽屉、28px 内边距、modal 阴影；头部是 52px 头像 + 分类小字 + 24px 技能名 + 消耗说明；输入块是描边 12px 圆角，聚焦时紫色 1px 光晕 + 边框；右下角紫色提交按钮；有产出时下方是 80px 圆角缩略图网格与下划线式下载行。

- [ ] **Step 6: Commit**

```bash
git add packages/views/aurora/generation-composer.tsx packages/views/aurora/generation-artifacts.tsx packages/views/aurora/generation-composer.test.tsx
git commit -m "feat(aurora): restyle the generation drawer and its artifacts"
```

---

### Task 6: 作品库（生成记录 + 作品网格）

**Files:**
- Modify: `packages/views/aurora/works-list.tsx`
- Test: `packages/views/aurora/works-list.test.tsx`

**Interfaces:**
- Consumes: Task 2 的 `AuroraPageHeader`/`AuroraPageState`/`avatarInitial`/`tintIndex`；Task 5 的 `GenerationStatusBadge`、`isPreviewableImage`；`works.eyebrow`。
- Produces: 无新导出。

- [ ] **Step 1: 骨架与页头**

| 位置 | 现在 | 改成 |
|---|---|---|
| 根 div | `flex h-full min-h-0 flex-col` | `aurora-page` |
| `CollectionPageHeader` | icon=`Library` + title | `AuroraPageHeader`：同 icon + `eyebrow={t(($) => $.works.eyebrow)}` + title |
| 滚动 div | `min-h-0 flex-1 overflow-y-auto px-4 py-3` | 删掉这层（滚动归 `<main className="aurora-scroll">`） |
| 内容 div | `flex flex-col gap-6` | `flex flex-col` |
| 两个 `section` | `flex flex-col gap-2` | `flex flex-col gap-3` |
| 两个 `h2` | `text-label font-medium` | 各自包一层 `<div className="aurora-section-head"><h2>{…}</h2></div>` |
| 生成记录 ul | `rounded-lg border border-surface-border` | `aurora-list` |
| 作品 ul | `rounded-lg border border-surface-border` | `aurora-works-grid` |
| 空态 | `CollectionPageState` | `AuroraPageState` |
| 骨架 | `WorksSkeleton` 里的 `rounded-lg` | `rounded-xl` |

删除失败时的 `Alert` 保留原样（`--destructive` 已被 token 桥接到珊瑚红）。

- [ ] **Step 2: `GenerationRow` 换成设计系统的行**

| 位置 | 现在 | 改成 |
|---|---|---|
| `li` | `flex items-center gap-3 border-b border-surface-border px-3 py-2 last:border-b-0` | `aurora-row` |
| 首列（新增） | 无 | `<span aria-hidden="true" className="aurora-icon-tile"><Sparkles className="size-4" /></span>` |
| 左列 div | `flex min-w-0 flex-1 flex-col` | `aurora-row-main` |
| 标题 `span` | `truncate text-body` | 不变 |
| 副标题 `span` | `truncate text-caption text-muted-foreground` | 不变 |
| 积分 `span` | `shrink-0 font-mono text-caption tabular-nums text-muted-foreground` | `aurora-row-amount` |
| 状态 | `GenerationStatusBadge` | 不变（Task 5 已换成胶囊，`className="shrink-0"` 保留） |

链接仍然包在标题里（`AppLink` + hover 下划线），保留 `generationHref` 缺省时渲染纯文本的分支。

- [ ] **Step 3: `AssetRow` 换成作品卡**

```tsx
function AssetRow({
  asset,
  onRequestDelete,
}: {
  asset: AuroraAsset;
  onRequestDelete: () => void;
}) {
  const { t } = useT("aurora");
  const download = useAuroraAssetDownload();
  // A row that carries no format falls back to its kind, and then has nothing
  // else to say — the second line is only there to add something.
  const detail = asset.format ? asset.kind : null;
  const title = asset.format ?? asset.kind;
  return (
    <li className="aurora-work-card">
      {/* The design's work tile: the file itself when it is an image, and the
          first character of its label when it is not. */}
      <div className={"aurora-work-thumb aurora-tint-" + tintIndex(asset.id)}>
        {isPreviewableImage(asset) ? (
          // eslint-disable-next-line @next/next/no-img-element
          <img src={asset.mediaUrl ?? ""} alt="" loading="lazy" />
        ) : (
          <strong aria-hidden="true">{avatarInitial(title)}</strong>
        )}
        <span className="aurora-work-tag">{asset.kind}</span>
      </div>
      <h3>{title}</h3>
      {detail ? <p>{detail}</p> : null}
      <div className="aurora-work-actions">
        <button
          type="button"
          className="aurora-file-row"
          onClick={() => void download(asset)}
        >
          <Download aria-hidden="true" className="size-3.5" />
          {t(($) => $.works.download)}
        </button>
        <Button type="button" variant="ghost" size="sm" onClick={onRequestDelete}>
          {t(($) => $.works.delete)}
        </Button>
      </div>
    </li>
  );
}```

要点：缩略图**只有** `isPreviewableImage` 为真时才渲染 `img`（它已经拒绝了相对 URL、`data:`、`javascript:`），其余资产渲染首字母 —— 这与详情页的策略一致，不新增一套判断。`work-tag` 是装饰性的 kind 标签，和下面的 `h3` 会读同一个词两次，但它是视觉分类标记、`h3` 是可访问名；这是参考实现的结构，保留。

- [ ] **Step 4: 加断言**

`works-list.test.tsx` 末尾追加：

```tsx
  it("renders the library as work cards with a tinted tile each", () => {
    renderWorks();

    const grid = screen.getByRole("heading", { name: "Library" })
      .closest("section") as HTMLElement;
    const cards = within(grid).getAllByRole("listitem");
    expect(cards.length).toBeGreaterThan(0);
    for (const card of cards) {
      // Every tile carries one of the six declared pastels, never a bare thumb.
      expect(card.querySelector('[class*="aurora-tint-"]')).not.toBeNull();
    }
  });
```

（`renderWorks()` 用该文件已有的渲染辅助；`heading` 名按现有断言里的英文标题填。）

- [ ] **Step 5: 跑测试**

Run: `pnpm --filter @multica/views test -- works-list`
Expected: PASS。既有断言覆盖：两条 section 标题、删除确认弹窗、删除失败提示、加载失败重试、空态、无技能名时的处理 —— 全部保持。

- [ ] **Step 6: Commit**

```bash
git add packages/views/aurora/works-list.tsx packages/views/aurora/works-list.test.tsx
git commit -m "feat(aurora): restyle the library as rows and work cards"
```

---

### Task 7: 历史（筛选 pill + 任务行 + 产出）

**Files:**
- Modify: `packages/views/aurora/history-list.tsx`
- Test: `packages/views/aurora/history-list.test.tsx`

**Interfaces:**
- Consumes: Task 2 的 `AuroraPageHeader`/`AuroraPageState`；Task 5 的 `GenerationArtifacts`；`history.eyebrow`。
- Produces: 无新导出。

- [ ] **Step 1: 骨架与工具条**

| 位置 | 现在 | 改成 |
|---|---|---|
| 根 div | `flex h-full min-h-0 flex-col` | `aurora-page` |
| `CollectionPageHeader` | icon=`History` + title + count | `AuroraPageHeader`：icon + `eyebrow` + title，`meta={<span>{t(($) => $.credits, { credits: formatCredits(generations.length, locale) })}</span>}` **不要**——见下 |
| `Tabs` | `flex min-h-0 flex-1 flex-col gap-0` | `gap-0` |
| 工具条 div | `{PAGE_TOOLBAR}` | `aurora-toolbar` |
| `TabsList` / `TabsTrigger` | 无 | `aurora-pill-tabs` / `aurora-pill-tab` |
| `TabsContent` | `min-h-0 flex-1 overflow-y-auto px-4 py-3` | 不传 className |
| 行 ul | `rounded-lg border border-surface-border` | `aurora-list` |
| 空态 ×2 / 加载失败 | `CollectionPageState` | `AuroraPageState` |
| 骨架 | `h-24 w-full rounded-lg` | `h-24 w-full rounded-xl` |

**页头的 meta 用条数，不要用积分数**：`generations.length` 直接就是"这个列表有多少条"，写成积分是在给一个数字套上它没有的单位。用：

```tsx
<AuroraPageHeader
  icon={History}
  eyebrow={t(($) => $.history.eyebrow)}
  title={t(($) => $.history.title)}
  meta={<span>{generations.length}</span>}
/>
```

- [ ] **Step 2: `HistoryRow` 换成堆叠行**

| 位置 | 现在 | 改成 |
|---|---|---|
| `li` | `flex flex-col gap-2 border-b border-surface-border px-3 py-3 last:border-b-0` | `aurora-row-stack` |
| 首行 div | `flex items-start gap-3` | 不变 |
| 左列 div | `flex min-w-0 flex-1 flex-col` | `aurora-row-main` |
| 标题 `span` | `truncate text-body` | 不变 |
| 元信息 `span` | `flex min-w-0 items-center gap-2 text-caption text-muted-foreground` | 不变 |
| 积分 `span` | `shrink-0 font-mono text-caption tabular-nums text-muted-foreground` | `aurora-row-amount` |
| 状态 | `GenerationStatusBadge` | 不变 |
| 失败原因 `p` | `flex items-start gap-1.5 text-caption text-destructive` | 不变 |
| 产出 | `GenerationArtifacts` | 不变 |

行里没有图标格：历史行的左端是标题，插一个装饰图块只会把标题推右。`.aurora-icon-tile` 由作品库的生成行使用。

- [ ] **Step 3: 加断言**

`history-list.test.tsx` 末尾追加：

```tsx
  it("counts the listed generations in the page header", () => {
    renderHistory();

    expect(
      screen.getByRole("heading", { name: "History" }).closest("header"),
    ).toHaveTextContent("3");
  });
```

（条数按该文件 fixture 的实际长度填；`closest("header")` 命中的是 `AuroraPageHeader` 的 `<header>`。）

- [ ] **Step 4: 跑测试**

Run: `pnpm --filter @multica/views test -- history-list`
Expected: PASS。重点：状态 tab 的 `getByRole("tab")`、技能筛选 `Select`、失败原因、产出缩略图、空态与"无匹配"两条分支全部保持。

- [ ] **Step 5: Commit**

```bash
git add packages/views/aurora/history-list.tsx packages/views/aurora/history-list.test.tsx
git commit -m "feat(aurora): restyle the history list"
```

---

### Task 8: 生成详情（页头 + 字段面板 + 任务段）

**Files:**
- Modify: `packages/views/aurora/generation-detail.tsx`
- Test: `packages/views/aurora/generation-detail.test.tsx`

**Interfaces:**
- Consumes: Task 2 的 `AuroraPageHeader`/`AuroraPageState`；Task 5 的 `GenerationArtifacts`/`GenerationStatusBadge`；`detail.eyebrow`。
- Produces: 无新导出。

- [ ] **Step 1: 骨架**

| 位置 | 现在 | 改成 |
|---|---|---|
| 根 div | `flex h-full min-h-0 flex-col` | `aurora-page` |
| `CollectionPageHeader` | icon=`Sparkles` + title + 返回 `AppLink`（`buttonVariants({ variant: "outline", size: "sm" })`） | `AuroraPageHeader`：icon + `eyebrow={t(($) => $.detail.eyebrow)}` + title，`actions` 保留那段 `AppLink`（`buttonVariants` 的 outline/sm 不变，`ArrowLeft` 与文案不变） |
| 滚动 div | `min-h-0 flex-1 overflow-y-auto px-4 py-3` | 删掉这层 |
| `GenerationFields` 根 | `flex flex-col gap-6` | 不变 |
| 两个 section | `flex flex-col gap-3` / `gap-2` | `flex flex-col gap-3` |
| 两个 `h2` | `text-label font-medium` | 各包一层 `<div className="aurora-section-head"><h2>…</h2></div>` |
| 字段 `dl` | `grid gap-3 sm:grid-cols-2` | `aurora-field-grid aurora-panel sm:grid-cols-2` |
| `Field` 的 div | 无 className | `aurora-field` |
| `Field` 的 `dt`/`dd` | `text-caption text-muted-foreground` / `text-body` | 不变（`.aurora-field dt/dd` 已给出同样的排版，工具类留着无害） |
| `DetailSkeleton` | `rounded-lg` | `rounded-xl` |
| `notFound` 空态 | `CollectionPageState` | `AuroraPageState` |

失败原因块、`elapsed` 计算、`TaskSection` 的复制逻辑与 `taskHref` 分支**一行不动**。

- [ ] **Step 2: `TaskSection` 的皮肤**

```tsx
    <section className="flex flex-col gap-3">
      <div className="aurora-section-head">
        <h2>{t(($) => $.detail.task_title)}</h2>
      </div>
      {taskHref ? (
        …原样的 AppLink…
      ) : (
        <div className="aurora-panel flex flex-col gap-2">
          …原样的 code + 复制按钮 + 说明 + 失败提示…
        </div>
      )}
    </section>
```

`code` 元素保持 `rounded-xs bg-muted px-2 py-1 font-mono text-caption`（`--muted` 已被桥接到 `#f1efe9`）与 `truncate`；复制按钮保持 `variant="ghost" size="icon-xs"` + `aria-label`（复制/已复制两态）。

- [ ] **Step 3: 加断言**

`generation-detail.test.tsx` 末尾追加：

```tsx
  it("groups the process facts into one panel", () => {
    renderDetail();

    const status = screen.getByText("Status");
    const panel = status.closest(".aurora-panel");
    expect(panel).not.toBeNull();
    // Status, created, elapsed, skill, reserved and charged all live in the
    // same panel; the prompt spans both columns inside it.
    expect(within(panel as HTMLElement).getByText("Prompt")).toBeVisible();
  });
```

（`renderDetail()` 用该文件已有的渲染辅助。）

- [ ] **Step 4: 跑测试**

Run: `pnpm --filter @multica/views test -- generation-detail`
Expected: PASS。既有断言覆盖：加载失败与 404 两种状态、字段值、失败原因、结算文案、任务 ID 复制与"无任务页"说明 —— 全部保持。

- [ ] **Step 5: Commit**

```bash
git add packages/views/aurora/generation-detail.tsx packages/views/aurora/generation-detail.test.tsx
git commit -m "feat(aurora): restyle the generation detail page"
```

---

### Task 9: 运行时（节点面板 + 进行中）

**Files:**
- Modify: `packages/views/aurora/runtime-status.tsx`
- Test: `packages/views/aurora/runtime-status.test.tsx`

**Interfaces:**
- Consumes: Task 2 的 `AuroraPageHeader`；Task 3 的 `AURORA_RUNTIME_VISUAL`；Task 5 的 `GenerationStatusBadge`；`runtime.eyebrow`。
- Produces: 无新导出（`AuroraRuntimeChip` 在 Task 3 已导出）。

- [ ] **Step 1: 骨架**

| 位置 | 现在 | 改成 |
|---|---|---|
| 根 div | `flex h-full min-h-0 flex-col` | `aurora-page` |
| `CollectionPageHeader` | icon=`Gauge` + title | `AuroraPageHeader`：icon + `eyebrow={t(($) => $.runtime.eyebrow)}` + title |
| 滚动 div | `min-h-0 flex-1 overflow-y-auto px-4 py-3` | 删掉这层 |
| 外层 div | `flex flex-col gap-6` | 不变 |
| `Skeleton` | `h-16 w-full rounded-lg` | `h-16 w-full rounded-xl` |
| `RuntimeNodeCard` section | `flex flex-col gap-2 rounded-lg border border-surface-border p-3` | `aurora-panel flex flex-col gap-2` |
| 状态圆点 | `visual.dot`（Task 3 后已是 `aurora-dot…`） | 不变 |
| 状态文字 | `text-body font-medium` + `visual.tone` | `text-body font-medium` + `visual.text` |
| 生成记录 section | `flex flex-col gap-2` | `flex flex-col gap-3` + `<div className="aurora-section-head"><h2>…</h2></div>` |
| 进行中 ul | `rounded-lg border border-surface-border` | `aurora-list` |
| 空态 p | `text-body text-muted-foreground` | 不变 |
| `ActiveGenerationRow` li | `flex items-center gap-3 border-b border-surface-border px-3 py-2 last:border-b-0` | `aurora-row` |
| 提示词 span | `min-w-0 flex-1 truncate text-body` | `aurora-row-main truncate` |
| 状态 | `<Badge variant="secondary">` | `<GenerationStatusBadge status={current.status} className="shrink-0" />` |

`settled` 集合、`markSettled`、`ActiveGenerationRow` 的 `useEffect` 轮询逻辑一行不动。

- [ ] **Step 2: 加断言**

`runtime-status.test.tsx` 末尾追加：

```tsx
  it("renders the node state as a dot plus a word, not colour alone", () => {
    renderRuntime();

    const node = screen.getByRole("heading", { name: "Execution" })
      .closest(".aurora-page") as HTMLElement;
    expect(within(node).getByText("Online")).toBeVisible();
    expect(node.querySelector(".aurora-dot")).not.toBeNull();
  });
```

- [ ] **Step 3: 跑测试**

Run: `pnpm --filter @multica/views test -- runtime-status`
Expected: PASS。既有断言覆盖五种状态、`runtime_not_provisioned` 无 Retry、离线/failed 有 Retry、以及"详情结算后行被移除" —— 全部保持。

- [ ] **Step 4: 看渲染**

Run: `pnpm dev:aurora` → `/runtimes`。Expected：节点卡片是白色 12px 圆角面板，左上角圆点 + 状态词（颜色来自桥接后的语义 token）；下面"进行中的生成"是同样的行式列表。

- [ ] **Step 5: Commit**

```bash
git add packages/views/aurora/runtime-status.tsx packages/views/aurora/runtime-status.test.tsx
git commit -m "feat(aurora): restyle the runtime page"
```

---

### Task 10: 积分与订阅（数字卡 + 套餐面板 + 充值档位 + 流水）

**Files:**
- Modify: `packages/views/aurora/billing.tsx`
- Test: `packages/views/aurora/billing.test.tsx`

**Interfaces:**
- Consumes: Task 2 的 `AuroraPageHeader`/`AuroraPageState`；`billing.eyebrow`。
- Produces: 无新导出。

**柱状图不移植。** 接口给的是首页 50 条流水，按天聚合会画出一张看起来完整、其实被截断的图。三张数字卡说的都是客户端真读到的事实。

- [ ] **Step 1: 骨架与三张数字卡**

| 位置 | 现在 | 改成 |
|---|---|---|
| 根 div | `flex h-full min-h-0 flex-col` | `aurora-page` |
| `CollectionPageHeader` | icon=`Wallet` + title | `AuroraPageHeader`：icon + `eyebrow={t(($) => $.billing.eyebrow)}` + title，**不传 actions**（充值入口就在页面里，页头再放一个是同一件事说两遍） |
| 滚动 div | `min-h-0 flex-1 overflow-y-auto px-4 py-3` | 删掉这层 |
| 内容 div | `flex flex-col gap-6` | `flex flex-col gap-6` |
| 余额 section | `flex flex-col gap-1 rounded-lg border border-surface-border p-4` + label + `text-display-sm` | **删掉**，并入下面的数字卡 |
| 充值 section | `flex flex-col gap-2` + `text-label` h2 + `flex flex-wrap gap-2` | `aurora-section-head` + `aurora-plans` |
| 流水 section | `flex flex-col gap-2` + `text-label` h2 | `aurora-section-head` |
| 流水 ul | `rounded-lg border border-surface-border` | `aurora-list` |
| 空态 | `CollectionPageState` | `AuroraPageState` |
| 骨架 | `BillingSkeleton` 的 `rounded-lg` | `rounded-xl` |

在内容 div 最前面插入数字卡（`subscription` 不存在时第二张卡不渲染，格子里留空比编一个数好）：

```tsx
        <div className="aurora-stat-cards">
          <article className="aurora-panel aurora-stat">
            <span>{t(($) => $.billing.balance_title)}</span>
            <strong>{balance}</strong>
          </article>
          {subscription ? (
            <article className="aurora-panel aurora-stat">
              <span>{t(($) => $.billing.subscription.usage_label)}</span>
              <strong>
                {t(($) => $.billing.subscription.usage_value, {
                  used: subscription.usage.generationsUsedThisMonth,
                  limit: subscription.limits.generationsPerMonth,
                })}
              </strong>
            </article>
          ) : null}
          <article className="aurora-panel aurora-stat">
            <span>{t(($) => $.billing.transactions_title)}</span>
            <strong>{transactions.length}</strong>
          </article>
        </div>
```

- [ ] **Step 2: 充值档位换成设计系统的档位卡**

现在的 `Button variant="outline" size="sm"` 一排换成 `.aurora-plan`（`<button>`，因为它是动作不是链接）：

```tsx
            <div className="aurora-plans">
              {topups.map((topup) => {
                const credits = formatCredits(topup.credits, locale);
                const price = t(
                  ($) => $.billing.topup.packs[topupPackLabel(topup.id)].price,
                );
                return (
                  <button
                    key={topup.id}
                    type="button"
                    className="aurora-plan"
                    // The card splits the fact across two lines; the accessible
                    // name keeps the one sentence the template already ships.
                    aria-label={t(($) => $.billing.topup.pack, { credits, price })}
                    disabled={topupCheckout.isPending || !returnURLs}
                    aria-busy={topupCheckout.isPending}
                    onClick={() => {
                      if (!returnURLs) return;
                      topupCheckout.mutate(
                        { topupId: topup.id, ...returnURLs },
                        {
                          onSuccess: (checkoutUrl) =>
                            openExternal(checkoutUrl, { webTarget: "same-tab" }),
                        },
                      );
                    }}
                  >
                    <strong>{t(($) => $.credits, { credits })}</strong>
                    <small>{price}</small>
                  </button>
                );
              })}
            </div>
```

要点：`aria-busy`、`disabled`、以及 `topupCheckout.isPending || !returnURLs` 的判断**原样保留**；`onClick` 调的还是原来那个 mutation；`CreditCard` 图标删掉（卡片自己就是按钮，不再需要引导图标）。可见文案从一句话拆成两行（大数字是积分数、下面小字是价格），`billing.topup.pack` 那句模板转成 `aria-label`，所以**这条事实仍然只声明一次**，且可访问名与改造前逐字相同。如果 `billing.test.tsx` 里那条 `5,000 credits · $5` 用的是文本查询，把它换成 `getByRole("button", { name: "5,000 credits · $5" })`。

- [ ] **Step 3: 套餐面板与流水行**

| 位置 | 现在 | 改成 |
|---|---|---|
| `PlanSection` section | `flex flex-col gap-3 rounded-lg border border-surface-border p-4` | `aurora-panel flex flex-col gap-3` |
| "套餐"小字 span | `text-caption text-muted-foreground` | `aurora-eyebrow` |
| 当前套餐名 span | `truncate text-label font-medium` | `aurora-card-title` |
| 状态 span / 续费 span / 取消 span | `text-caption text-muted-foreground` | 不变 |
| 用量行 | `flex items-center justify-between gap-3 text-caption text-muted-foreground` | 不变 |
| `Progress` | 原样 | 原样（`bg-primary` 已被桥接成紫色） |
| 档位 `ul`/`li` | 带 `border-t border-surface-border` 的行 | 不变；`Subscribe` 按钮保持 `size="sm"` |
| `TransactionRow` li | `flex items-center gap-3 border-b border-surface-border px-3 py-2 last:border-b-0` | `aurora-row` |
| 左列 div | `flex min-w-0 flex-1 flex-col` | `aurora-row-main` |
| 右列 div | `flex shrink-0 flex-col items-end` | 不变 |
| 金额 span | `font-mono text-body tabular-nums` | `aurora-row-amount` |
| 变动后余额 span | `font-mono text-caption tabular-nums text-muted-foreground` | 不变 |

结算错误提示 `p[role="alert"]`、`canSubscribe` 判断、`TIER_ORDER`、`returnURLs` 的计算一行不动。

- [ ] **Step 4: 跑测试**

Run: `pnpm --filter @multica/views test -- billing`
Expected: PASS（把 `5,000 credits · $5` 那条断言按 Step 2 改写后）。既有断言覆盖：余额、套餐名与状态、续费日、用量 `12 of 30`、三档价格与订阅按钮、免费档无按钮、流水正负号与 `Balance after`、五种结账错误文案、加载失败重试、空流水 —— 全部保持。

- [ ] **Step 5: 看渲染**

Run: `pnpm dev:aurora` → `/billing`。Expected：三张白卡数字（28px/600/负字距）；套餐面板里有紫色进度条；充值档位是三张 136px 高的卡片按钮（悬停升起）；流水是行式列表，负数是珊瑚红方向、正数是绿色方向（沿用现有 `amount_positive`/`amount_negative` 文案，不加新色）。

- [ ] **Step 6: Commit**

```bash
git add packages/views/aurora/billing.tsx packages/views/aurora/billing.test.tsx
git commit -m "feat(aurora): restyle credits and billing"
```

---

### Task 11: 死胡同 / 无工作区 / 登录品牌 + 全量验证

**Files:**
- Modify: `apps/aurora/components/dead-end-screen.tsx`
- Modify: `apps/aurora/components/no-workspace-notice.tsx`
- Modify: `apps/aurora/app/login/page.tsx`
- Test: `apps/aurora/components/dead-end-screen.test.tsx`
- Verify: 全仓

**Interfaces:**
- Consumes: Task 1 的 `.aurora-centered`、`.aurora-empty`、`.aurora-brand`/`.aurora-brand--on-light`；`shell.brand_tagline`。
- Produces: 无新导出。

- [ ] **Step 1: `DeadEndScreen` 用设计系统的居中版式**

```tsx
export function DeadEndScreen({
  title,
  description,
}: {
  title: string;
  description: string;
}) {
  return (
    <div className="aurora-centered">
      <div className="aurora-empty w-full max-w-lg">
        <h1 className="text-display-sm font-semibold tracking-tight">
          {title}
        </h1>
        <p>{description}</p>
      </div>
      <WorkspaceRecovery />
    </div>
  );
}
```

`WorkspaceRecovery` 的两个出口按钮、"没有工作区就不渲染第一个按钮"的分支、`WorkspaceUnavailable` 清 cookie 的 effect，一行不动。

- [ ] **Step 2: `NoWorkspaceNotice` 换外层**

外层 `flex min-h-svh items-center justify-center px-6` → `aurora-centered`；`Card` 那一套（`CardHeader`/`CardTitle`/`CardDescription`/`CardContent` 与 `Button variant="outline"`）保留——`--card`/`--border`/`--radius` 已被桥接到设计系统，Card 自己就是对的。`min-h-svh` 由 `.aurora-centered` 提供。

- [ ] **Step 3: 登录页的品牌**

`apps/aurora/app/login/page.tsx` 的 `LoginPageContent` 里取一次 `const { t } = useT("aurora");`，然后给 `LoginPage` 传 `logo`：

```tsx
    <LoginPage
      logo={
        <span className="aurora-brand aurora-brand--on-light justify-center">
          <span aria-hidden="true" className="aurora-brand-mark">
            A
          </span>
          <span className="aurora-brand-copy">
            <strong>Aurora</strong>
            <small>{t(($) => $.shell.brand_tagline)}</small>
          </span>
        </span>
      }
      onSuccess={handleSuccess}
      google={…}
      onTokenObtained={setLoggedInCookie}
    />
```

`LoginPage` 本身是 web/desktop 共享组件，**不要改它**：它靠 token 桥拿到纸色画布、白卡 12px 圆角、紫色主按钮与紫色焦点环，这已经是设计系统的卡片。分屏视觉（`.login-visual` + 轨道）不移植，理由见"刻意不移植"。

- [ ] **Step 4: 给死胡同加一个测试（它现在没有）**

`apps/aurora/components/dead-end-screen.test.tsx`：

```tsx
import { describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import { NavigationProvider } from "@multica/views/navigation";
import type { NavigationAdapter } from "@multica/views/navigation";
import { renderWithI18n } from "../test/render";

// The recovery offers two ways out and reads the workspace list to decide
// whether the first one exists. Mocked at the boundary: this suite is about the
// screen a stranded reader lands on, not about the list.
vi.mock("@multica/views/auth", () => ({ useLogout: () => vi.fn() }));
vi.mock("@multica/core/workspace", () => ({
  useWorkspaceList: () => ({ workspaces: [] }),
}));

import { DeadEndScreen } from "./dead-end-screen";

const adapter: NavigationAdapter = {
  push: vi.fn(),
  replace: vi.fn(),
  back: vi.fn(),
  pathname: "/acme/nope",
  searchParams: new URLSearchParams(),
  hash: "",
  getShareableUrl: (path) => path,
};

describe("DeadEndScreen", () => {
  it("states the dead end and offers a way out", () => {
    renderWithI18n(
      <NavigationProvider value={adapter}>
        <DeadEndScreen title="Nothing here" description="The link may be wrong." />
      </NavigationProvider>,
    );

    expect(
      screen.getByRole("heading", { level: 1, name: "Nothing here" }),
    ).toBeVisible();
    expect(screen.getByText("The link may be wrong.")).toBeVisible();
    expect(screen.getByRole("button", { name: "Log out" })).toBeVisible();
  });
});
```

- [ ] **Step 5: 完成迁移后启用未使用类检查，再做全量验证**

现在才在 `apps/aurora/app/aurora-theme.test.ts` 的 `describe` 末尾加入以下断言。Task 1 不加：基础 CSS 的消费者是 Tasks 2–11 的交付物。若发现未使用规则，删除对应 CSS；不要用注释、import 路径或 DOM ID 冒充消费者。

```ts
  it("declares no class left unused after the full migration", () => {
    const used = new Set([
      ...classesUsedIn(resolve(repoRoot, "packages/views/aurora")),
      ...classesUsedIn(resolve(repoRoot, "apps/aurora")),
    ]);
    expect([...declared].filter((name) => !used.has(name))).toEqual([]);
  });
```

同时增加 Portal 减少动态效果守卫，先核对 CSS 选择器，并在下一步通过浏览器验证实际计算样式：

```ts
  it("covers portalled Sheets and backdrops when motion is reduced", () => {
    expect(globals).toContain("aurora.css");
    const layout = readFileSync(resolve(repoRoot, "apps/aurora/app/layout.tsx"), "utf8");
    expect(classesInSource(layout).has("aurora-theme")).toBe(true);
    const reduced = css.slice(css.indexOf("@media (prefers-reduced-motion: reduce)"));
    expect(reduced).toContain('.aurora-theme [data-slot="sheet-content"]');
    expect(reduced).toContain('.aurora-theme [data-slot="sheet-overlay"]');
    expect(reduced).toContain("transition-duration: 0.01ms !important");
  });
```

```bash
pnpm typecheck
pnpm lint
pnpm test
pnpm check:ui-radii
pnpm check:ui-exports
```

Expected: 全绿。若 `check:ui-radii` 报错，只可能是新写的 `rounded` 裸类或 `rounded-[Npx]`；按 Global Constraints 的阶梯换成 `rounded-sm|md|lg|xl|2xl|full`。

- [ ] **Step 6: 手工验收（逐条打勾）**

Run: `pnpm dev:aurora`

1. `/login`：纸色画布、白卡 12px、紫色按钮、卡片顶部有品牌标识与 `AI STUDIO` 小字；Tab 走一遍有紫色 2px 焦点环。
2. 登录后 `/skills`（参考实现的首页，逐项对照）：
   - Hero：深蓝 + 径向紫光 + 点阵；状态 pill、40px 标题、两个真实数字、右侧轨道四个头像。
   - Section head：紫色 eyebrow + h2 + 右侧"按当前筛选可见的条数"。
   - 工具条：分类 pill（选中深蓝底白字）+ 右侧搜索框（⌕ 图标、白底、发丝边、聚焦紫边）。
   - 点分类 pill：网格即时过滤，右侧计数同步变。
   - 在搜索框输入：网格即时过滤；输入无匹配时出现空态与「查看全部功能」，点它回到全量 16 张。
   - 卡片：三列、白底 12px；每张有头像、分类小字、标题、能力 chips、footer（绿点 + 可运行 / 灰胶囊"即将上线" + 消耗）；悬停上浮 2px 且箭头变紫。
   - 点卡片：右侧滑入抽屉；Esc 与遮罩可关；关掉后焦点回到那张卡片。
3. `/works`：生成记录是行式列表（图标格 + 标题 + 积分 + 状态胶囊），作品是网格卡（缩略图或首字母 + kind 标签 + 下载/删除）。
4. `/history`：状态 pill tab + 技能下拉；行内是提示词、功能、时间、积分、状态胶囊、失败原因、产出缩略图。
5. `/works/{id}`：字段在一个白色面板里两列排布，提示词跨两列；返回按钮在页头右侧。
6. `/runtimes`：节点面板 + 进行中列表。
7. `/billing`：三张数字卡、套餐面板（紫色进度条）、三张充值档位卡、流水行。
8. 任意页面把窗口缩到 900px 及以下：侧栏变共享 Sheet、顶栏出现 ☰ 与品牌名；键盘打开后焦点进入抽屉，Tab/Shift+Tab 留在其中，Esc/遮罩/关闭按钮关闭后焦点回到 ☰；**收起时侧栏不挂载**。随后在打开状态放大到 901px，确认 Sheet 关闭、背景恢复可交互且只剩一个桌面导航。
9. 缩到 690px 以下：卡片单列、Hero 单列且轨道隐藏、数字卡单列。
10. 系统开启"减少动态效果"后：分别打开移动导航 Sheet 和生成 Sheet，检查 Portal 内 `[data-slot="sheet-content"]` 与 `[data-slot="sheet-overlay"]` 的 computed `transition-duration` 都为 `0.00001s`；卡片过渡也接近 0。再关闭该系统选项，确认原语原有过渡恢复。
11. 键盘走一遍抽屉与两个弹窗：Esc 能关、焦点回到触发元素、金色描边不存在（焦点环是紫色）。

- [ ] **Step 7: Commit**

```bash
git add apps/aurora/components/dead-end-screen.tsx apps/aurora/components/no-workspace-notice.tsx apps/aurora/app/login/page.tsx apps/aurora/components/dead-end-screen.test.tsx apps/aurora/app/aurora-theme.test.ts
git commit -m "feat(aurora): finish the design pass on the standalone screens"
```

---

## Self-Review

**1. DESIGN.md 逐条覆盖**

| DESIGN.md 规则 | 落在哪 |
|---|---|
| 暖纸色画布 + 纯白卡片 | Task 1（`--aurora-paper`/`--aurora-card` + token 桥 `--background`/`--card`） |
| 深海军蓝是唯一大面积深色（侧栏 + Hero） | Task 1 `.aurora-sidebar`/`.aurora-hero`；Task 3 侧栏；Task 4 Hero |
| 紫色只用于主操作/选中/焦点环/推荐描边 | Task 1 `--aurora-violet` + 焦点环；token 桥 `--primary` |
| 珊瑚橙保持稀少 | 只用两处：品牌渐变里的 `--aurora-coral`、Hero 标题的强调词槽位（本计划未用强调词，所以实际只剩品牌渐变） |
| 淡色标签（tint-*） | Task 1 `.aurora-avatar--*`、`.aurora-tint-1..6`；Task 6 作品缩略图 |
| 字体：正文 ≥14px、说明 13px、最小 11px 且只给大写英文 | Task 1 的 `--text-caption: 13px`/`--text-micro: 12px`/`--text-body-lg: 16px`；eyebrow 用 13px 而非参考的 11px（中文不允许 11px） |
| 形状：按钮/输入 8px、卡片 12px、Hero/弹窗 16px、胶囊只给 pill/徽章/圆点 | Global Constraints 的半径阶梯 + Task 1 的 `--aurora-r-*` |
| 层次：默认平卡（白底 + 1px 发丝边），悬停最多 card 阴影，弹窗用 modal 阴影，不用毛玻璃/多层光晕 | Task 1 `.aurora-card`/`.aurora-panel`/`.aurora-dialog`；唯一的 `backdrop-filter` 留在顶栏（DESIGN.md 明确允许） |
| 渐变只允许品牌标志与 Hero 弱径向光 | Task 1 `.aurora-brand-mark` + `.aurora-hero`，其余一律纯色 |
| 响应式断点 1120 / 900 / 690 | Task 1 三个 `@media`；Task 3 的媒体查询与 CSS 同为 `(max-width: 900px)`；Task 11 Step 6 逐条手验 |
| 触控目标 ≥40px、输入 44px | 顶栏按钮 40px、`.aurora-plan` ≥96px；输入沿用 `Input` 的 36px——**偏差**，见下 |

**2. 与参考实现的偏差（都是有意的）**

1. **eyebrow 13px 而不是 11px**：DESIGN.md 的 11px 只允许大写英文，中文必须 ≥13px；eyebrow 要翻译，所以按 13px 落地。
2. **Hero 不写口号**：标题一句话（`directory.hero_title`），其余是两个真实数字与状态 pill，不重复标题、不写泛化引导。
3. **侧栏进度条量的是本月生成额度，不是余额**：接口里只有额度有分母。
4. **账单不画柱状图**：首页 50 条流水按天聚合会画出被截断的图。
5. **抽屉不手写关闭按钮**：用 `Sheet` 原语那颗，保住焦点回填与 Esc。
6. **登录不做分屏视觉**：`LoginPage` 是 web/desktop 共享的，靠 token 桥换色；分屏要给共享组件开分叉。
7. **卡片没有描述、chips、模型行**：`AuroraSkill` 没有这些字段，不编。
8. **搜索留在页面工具条**，没有搬到顶栏：Aurora 的搜索是按页过滤，顶栏放一个全局搜索会声称一个不存在的跨页能力。

**3. 占位符扫描**

计划里的每个代码块都是可直接粘贴的内容；需要执行者打开被测文件确认的是"该文件已有的渲染辅助叫什么"，已在 Global Constraints 里点明，并且新断言的编写方式不依赖具体名字（自带 wrapper 的 Task 11 测试给了完整代码）。

**4. 类型/命名一致性**

- `AuroraRuntimeVisual` 的词表字段在 Task 3（定义）、Task 4（`pill`）、Task 9（`dot`/`text`）三处一致：`{ dot, pill, text }`。
- `AuroraPageHeader` 的 props（`icon/eyebrow/title/meta/actions`）在 Task 2 定义，Task 6/7/8/9/10 使用处逐一对齐。
- `GenerationStatusBadge` 在 Task 5 换成胶囊但仍只收 `{ status, className }`，Task 6/7/9 的调用处不需要改签名。
- `tintIndex` / `avatarInitial` / `avatarTint` 在 Task 2 定义并导出，Task 3（`avatarInitial`）、Task 6（`tintIndex`/`avatarInitial`）使用。
- CSS 类清单以 Task 1 样式表为准；Task 1 检查实际使用类已定义，Task 11 完成迁移后检查未使用类并删除多余规则。Task 4 回归的 `.aurora-chips`/`.aurora-card-footer`/`.aurora-card-state` 三个类正是这次首页复核的结果——第一版把卡片的 chips 与 footer 剪掉了，因为当时以为没有数据填它们；复核后确认 `skill.input` 与 `skill.available`+`skill.credits` 就是这两个位置的现成数据。

**5. 风险与回滚**

- **最大风险是 token 桥的波及面**：它改变 `apps/aurora` 里所有 shadcn 原语的颜色与三处字号。回滚单位是 Task 1 —— 只删 `globals.css` 里那段桥与那行 `@import`，其它 Task 的 `aurora-*` 类会失去变量来源而退化成无样式，所以**要么整体上，要么整体下**，不要在中间态交付。
- **字体从 Inter 换 Geist** 会改变全站行宽断行；Task 1 Step 7 与 Task 11 Step 6 都要求目视确认没有截断。
- **`forcedTheme="light"`** 会把 Aurora 的暗色偏好用户留在亮色；这是 DESIGN.md 只有一套画布的直接结果，需要暗色时应当先补一份暗色 token 再放开。

## 执行交接

计划完成，保存在 `docs/superpowers/plans/2026-10-10-aurora-ui-design-system.md`。两种执行方式：

**1. Subagent-Driven（推荐）** —— 每个 Task 派一个全新的 subagent，Task 之间我来评审；Task 1 必须先单独验收通过再往下，因为它定义其余所有 Task 的类名与变量；此时只验证声明与桥接，不要求后续尚未创建的消费者存在，未使用类检查到 Task 11 才加入。

**2. Inline Execution** —— 在当前会话里按 executing-plans 批量执行，带检查点。

选哪种？

## 修订记录

- 2026-10-10 初稿：Task 1–11 覆盖设计层、外壳、五个页面、独立屏与全量验证。
- 2026-10-10 复核回写（首页）：把参考实现首页拆成"区域 → 交互 → Aurora 落点"的对照表（见"设计基线"），据此补回三处被初稿剪掉的东西——`.directory-head` 那一段（eyebrow + h2 + 按筛选可见的条数）、卡片的能力 chips（来自 `skill.input`）与 footer（可运行/即将上线 + 消耗）、空态里的"查看全部功能"恢复动作；搜索框按参考的 `.search-box` 造型改用 `InputGroup`。i18n 增量从 14 键涨到 25 键，CSS 类从 125 涨到 130。

- 2026-10-10 PR #228 复审修订：样式守卫改用 TypeScript AST，只扫描实际 `className` 与显式类名映射，有限动态类名做完整展开；未使用类检查延后到 Task 11。移动导航改为共享 Sheet + SheetTrigger，并补断点桩、键盘打开/Esc/焦点恢复回归及浏览器 Tab/遮罩/断点切换验收。Aurora body 加 `aurora-theme`，减少动态效果规则覆盖 Portal 中的 Sheet 根元素和遮罩。另将抽屉示例的不存在字段和充值示例的未定义函数改回现有显示名/积分格式化与结账回调，避免执行时丢失 wiring。

## 待确认

复核只回写了"能落地的部分"。剩下三处是**数据缺口**而不是设计取舍，需要你决定是否单开一个计划补服务端字段：

| 缺口 | 影响 | 补它要动什么 |
|---|---|---|
| `SkillCatalogEntry` 没有 `description` | 卡片少了参考实现里那行描述 | `server/internal/aurora/catalog.go` + 客户端 schema + 16 个技能 × 5 locale 的产品文案 |
| 没有 `provider`/`model` | 卡片少了 `.model-line` | 目录字段 + 每个技能的推荐/备用模型事实（这些事实在 Fleet 侧，不一定该由目录表拥有） |
| 没有示例提示词 | 抽屉少了 `.quick-prompts` | 目录字段 + 16×3 条示例 × 5 locale |
