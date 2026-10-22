# Apex AI 页面接入与部署交接

本次把已确认的 18 个工具首页与全屏工作台接入正式 Next.js 页面。
预览与正式网站共用 `components/tools/`，头像位于 `public/avatars/`，
不再需要从预览目录复制页面。工具提交会创建一条真实 issue 并分配给后端
agent，由 agent 执行后在 issue detail 中返回评论和附件。

## 部署对象

- 仓库：`apexai-sg/multica`，应用：`apps/aurora`。
- 构建命令：在仓库根目录执行 `corepack pnpm install --frozen-lockfile`，再执行 `corepack pnpm --filter @multica/aurora build`。
- 容器沿用 `Dockerfile.aurora`，其中已复制 Next.js 静态文件与 `apps/aurora/public`。请重新构建前端镜像，记录新的提交 SHA、镜像标签和 digest，并沿用部署环境现有配置。只更新后端或重启旧镜像不会更新页面。
- `pnpm --filter @multica/aurora preview` 仅用于本地设计验收，不能代替正式构建；不要把其虚构账号、积分或模拟 API 部署到生产。
- 不需要新增数据库迁移、模型密钥或新的环境变量。沿用现有登录、工作区和 API 配置。

## 正式访问入口

以下 `{workspaceSlug}` 为当前用户实际工作区标识，不能填写本地预览的 `preview`。

| 页面 | 路径 |
| --- | --- |
| 登录后首页 / 18 个工具 | `/{workspaceSlug}/skills` |
| 图片／视频换脸 | `/{workspaceSlug}/tools/face-swap` |
| 视频文案提取 | `/{workspaceSlug}/tools/video-transcript` |
| 商品图制作 | `/{workspaceSlug}/tools/product-images` |
| 其他工具 | 首页对应卡片链接，统一为 `/{workspaceSlug}/tools/{toolId}` |
| 原有后端工具库 | `/{workspaceSlug}/skills/connected`，首页“现有工具库”入口 |

工具详情页使用完整窗口，不包在侧栏或抽屉内；原有登录和工作区访问校验继续生效。
账号、积分、作品、历史记录、服务状态继续使用现有真实系统。
工具通过 `agentKey`（后端 agent 的 `system_key`）选择执行该任务的后端 agent；
没有对应 agent 时回退到工作区的通用助手，agent 数据全部来自后端。
原有后端工具库保留其现有提交路径，避免前端改版中断已有能力。

## 部署测试版后的验收

1. 使用真实账号登录，进入自己的 `/skills`：共 18 个工具，前三个依次为换脸、视频文案提取、商品图制作。
2. 逐个打开工具，应跳转独立 `/tools/{toolId}`，没有右侧抽屉或常驻侧栏；直接刷新详情页也能打开。
3. 分类、搜索后进入工具，点击“返回工具列表”，分类和搜索词应保留；浏览器后退、前进应正常。
4. 电脑与手机检查所有头像、页面宽度和滚动，底部结果区应可以到达。头像应从前端域名 `/avatars/person-*.svg` 加载。
5. 换脸支持切换图片／视频，两份素材独立选择、替换与移除；未选择必填素材时主按钮不可点击。
6. 视频文案提取的人物说话与画面文字分开显示。示例结果必须保留“与上传的视频无关”的标注，不能当成提取成功。
7. 用退出登录的浏览器打开工具链接，仍应进入登录流程；错误的工具标识应显示未找到页面。
8. 检查“现有工具库”、作品、历史记录和账号入口。先由 Hannah 确认外观，再发布到正式域名。
9. 提交一个文本类工具任务（如“文案创作”）：应自动打开 issue detail，agent 执行后在评论/附件中返回结果。
10. 提交一个媒体类工具任务：agent 应明确说明当前无法生成真实媒体，并给出方案；不能出现伪造图片/视频。

## 任务执行与结果

每个工具页面提交时：

1. 把已选择的本地文件上传为 issue 附件（沿用现有附件上传限制）。
2. 创建一条 issue，标题取工具名与需求摘要，描述包含工具、需求、设置和素材说明，并分配给该工具对应的后端 agent（`agentKey`；没有对应 agent 时回退到工作区通用助手）。
3. 创建成功后自动打开 `/{workspaceSlug}/issues/{identifier}`（issue detail）。

agent 在 issue 上执行任务，并把结果以评论和附件返回，结果直接显示在 issue detail。
上传存储、任务进度、取消、失败重试与计费沿用 Multica 现有的 issue/agent 任务能力，
前端不再展示“尚未接入”的模拟结果。

限制：文本/方案类任务可由普通 agent 完成；图片、视频、音频的**真实媒体生成**仍取决于
后端是否配置了对应 provider。未配置时 agent 会在结果中说明限制并给出方案，不伪造媒体产物。
未配置运行时的 agent 无法执行任务，提交会失败并在页面上报错。

## 本次验证记录

- `pnpm --filter @multica/aurora typecheck` 通过。
- `pnpm --filter @multica/aurora exec vitest run`：124 项通过（含工具提交 hook 的单元测试与路由测试）。
- `pnpm --filter @multica/aurora build` 通过，路由包含 `/[workspaceSlug]/tools/[toolId]`、`/[workspaceSlug]/skills/connected` 与 `/[workspaceSlug]/issues/[id]`。
- 未在开发机执行真实模型调用或线上登录提交；部署后由真实账号验收任务提交与 issue detail 结果。

![正式首页](docs/approved-ui/apex-production-home.png)

![电脑换脸页面](docs/approved-ui/apex-production-face-desktop.png)

![手机换脸页面](docs/approved-ui/apex-production-face-mobile.png)
