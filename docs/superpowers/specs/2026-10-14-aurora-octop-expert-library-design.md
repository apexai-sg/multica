# Aurora 专家库导入（octop SkillHub → Aurora）

日期：2026-10-14

状态：书面规格。实施以 [`docs/superpowers/plans/2026-10-14-aurora-octop-expert-library.md`](../plans/2026-10-14-aurora-octop-expert-library.md) 为准。

来源记录：[`89-expert-sync.md`](/Users/lirichen/Work/GithubRepo/octop-ops/89-expert-sync.md)（2026-10-14 在 172.16.12.89 上把 SkillHub 六个场景的专家装成「模板 + Agent + 技能」的实操手册与脚本）。

## 1. 目标

Aurora 现在的 16 个 skill 是手写的：每个 skill 一份单文件 brief，跑一条固定供应商路由。本次把 172.16.12.89 上 octop 的专家库整车导入，**24 个专家 1:1 替换这 16 个 skill**：

- 一个专家 = 一个 Aurora skill / system agent（Agents 层）；
- 一个专家 = 一套技能集：1 份技能集工作流（主 brief）+ 6 个子技能，子技能带自己的 SKILL.md、`references/`、`scripts/`（Skills 层）；
- 子技能作为该 agent 的附属 workspace skill 落库，随 claim 一起写进沙箱 `.claude/skills/`，模型可以逐个读取。

## 2. 已确认边界

1. 范围是文档「六个场景」的 24 个专家；`tech-test-automation`（89 上既有的第 25 个模板）不在本次范围。
2. 源是 89 卷内的快照（`/home/project/octop/data/expert_market`，905 文件 / 约 7 MiB），不是重新从 `api.skillhub.cn` 下载：上游对 `content-writer` 已 404，卷内快照才是本次事实来源。
3. 三个 phase-2 skill（`avatar-video`、`ppt`、`excel`）随旧 16 个 skill 一起退役；Aurora 不再有「生成类」目录条目（Seedream / Seedance / ASR 路由代码保留，但没有 catalog 条目指向它们）。
4. 旧 16 个 skill 的目录条目、brief、执行策略全部删除；旧 workspace 遗留的 carrier 归档、旧 skill 行保留（不删行，历史 generation / asset 仍引用它们）。
5. 24 个专家全部 `available = true`。
6. 执行路由全部复用已有的 `claude-text`（producer `multica-aurora-runtime`），产物默认是一份 Markdown。
7. 不改 `apps/mobile/`；不改 Multica issue / 作品库 / 生成详情的行为契约；不新增 API 字段。

## 3. 对象与数据流

三层，来源是 octop 的三个落盘层（模板缓存 / agent 工作区 / 内置技能），在 Aurora 里被压成两层。

| Aurora 对象 | 来源 | 落盘 |
| --- | --- | --- |
| catalog 条目（24） | `manifest.json` 的 label/description + `experts_catalog.go` 的人工表 | `server/internal/aurora/catalog.go` 构造 |
| 主 brief（24） | 技能集 `skills/<slug>/SKILL.md` 正文 + `SOUL.md`，套 Aurora 五段契约 | `server/internal/aurora/experts/<slug>/workflow.md`（commit，`//go:embed`） |
| 子技能（143） | `skills/<sub>/` 原文，含 `references/`、`scripts/`、`_` / `.` 文件 | `aurora-experts/<slug>/<dir>/`（commit，不 embed）。`<dir>` 是 `sanitizeSkillName` 的不动点 |
| 快速开始预设 | `manifest.json` 的 `quick_prompts` | `experts/index.json` 的 `quick_prompts`；种子时取前 3 条写入 `agent.conversation_starters` |

数据流：

```
API GET /api/aurora/skills                 → catalog（24 条，从 experts/index.json + 人工表构造）
POST /api/aurora/generations {skillId}     → 懒种子该专家：1 agent + 1 主 skill 行（brief）
node scripts/aurora-import-experts.mjs --import <dest>
                                           → 把 aurora-experts/ 拷进 cloud runtime 的 AURORA_EXPERTS_DIR
                                             （镜像默认 /opt/aurora/experts，不打进二进制）
claim（daemon）                            → 主 brief 写入任务 .claude/skills/；
                                             再把 AURORA_EXPERTS_DIR/<slug>/ 拷进同一目录
任务结束                                   → manifest（kind=text, producer=multica-aurora-runtime）照旧结算
```

子技能正文不进 API、`multica` 或 `fleet-node` 二进制。更新技能只重跑导入脚本，不重建 API。

## 4. 命名与标识

- catalog id = 专家 slug（如 `ecommerce-bidding-strategy`）；system key = `aurora:<slug>`（机制不变）。
- 主 skill 行名 = catalog `Name`（上游中文标签，如「竞价策略专家」）。daemon 的 `requireAuroraSkillDocument` 按这个名字校验，不能改。
- 子技能目录名 = `sanitizeSkillName(<专家 slug>-<子技能 slug>)`（单个 `-`，小写 `[a-z0-9]`）。`execenv.sanitizeSkillName` 把 `[^a-z0-9]+` 收成一个 `-`，所以 `--` 不是不动点，brief 路径必须用这个目录名。`_meta.json` 只留在仓库作来源记录，导入时排除；其余文件，包括 `.learnings/` 和 `__init__.py`，都进 runtime。
- 子技能 SKILL.md 的 `name:` frontmatter 在导入时改写成同一个 namespaced 值，保证目录名与 frontmatter 一致。

## 5. brief 契约

每份主 brief 五段齐全（`workflows_test.go` 的既有契约不变）：

- `## Inputs`：prompt + 可选 1 个文档（复用 `xhs-copy.md` 的下载/解析 shell 片段）；
- `## Persona`（额外段）：`SOUL.md` 原文；
- `## Steps`：技能集工作流正文原文，把「使用 X 技能」改写成「读取 `.claude/skills/<namespaced>/SKILL.md` 并按其执行」；
- `## Required outputs`：`<outputRoot>/artifacts/<slug>.md`（UTF-8 Markdown ≤ 2 MiB）；
- `## Artifact manifest`：与 `xhs-copy` 同款的 JSON 模板（`skill_id` = 专家 slug，kind `text`，format `md`）；
- `## Failure behavior`：通用条目 + 外部工具/凭据缺失时的降级条款。

## 6. 已知偏差（接受）

1. **外部凭据缺失**：多数专家的原始步骤要 Google Ads / Amazon / 浏览器等凭据，托管沙箱里没有。brief 的 Failure behavior 固定要求：不得编造，改为基于用户输入产出可交付文本，并说明哪些步骤未执行。这一条是本计划接受的语义降级；逐条剥离外部调用另开一轮。
2. **上游缺包**：`marketing-social-media-operation` 只有 5 个子技能（`content-writer` 上游 404）。种子以 index 为准，不按技能集 frontmatter 的声明数校验。
3. **第三方内容与许可证**：SkillHub 技能包的 `_meta.json` 只有 ownerId/slug/version，没有许可证字段。vendored 的原文与来源在 `experts/PROVENANCE.md` 里记录。
4. **主技能目录名**：中文 display name 经 `sanitizeSkillName` 后固定为 `skill`（现状即如此，本次不改）。
5. **旧 workspace 残留**：旧 system agent 归档、旧 skill 行保留。

## 7. 验收

以实施计划的「验收标准」一节为准，并加这一条：API 返回 24 条；单专家种子出 1 agent + 1 主 skill 行，不写子技能 `skill_file`；`aurora-import-experts.mjs --import <dest>` 把除 `_meta.json` 以外的子技能文件拷进 runtime；daemon 再把 `AURORA_EXPERTS_DIR/<slug>/` 拷进任务 `.claude/skills/`，目录名与 brief 一致。部署与真实 Fleet 端到端跑不在本次范围。
