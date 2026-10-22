# Apex AI 页面接入与部署交接

本次把已确认的 18 个工具首页与全屏工作台接入正式 Next.js 页面。
预览与正式网站共用 `components/tools/`，头像位于 `public/avatars/`，
不再需要从预览目录复制页面。此交付完成前端接入；生成、上传存储和模型接口仍待技术连接。

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
工具卡片与后台专家目录尚未映射，不能根据名称猜测 provider 或调用接口。
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

## 下一阶段接口工作

18 个页面的输入、设置和结果布局见 [工具交接表](preview/README.md#design-handoff)。
当前文件仅在本机预览，没有上传、推理、扣费或真实结果下载。
服务接入后需逐项完成文件存储、格式/时长限制、任务进度、取消、失败重试、实际结果、下载与计费。
不要仅删除“尚未接入”提示就视为功能完成。保留换脸双素材布局；视频提取分别返回带时间范围的口播与画面文字。

## 本次验证记录

- Aurora 全部 124 项测试、类型检查及本次修改的 ESLint 检查通过。
- 正式 Next.js 构建包含 `/[workspaceSlug]/skills/connected` 与 `/[workspaceSlug]/tools/[toolId]`。
- 在本机运行正式构建，连接临时虚构 API，逐个检查 18 个工具在 1440 像素与 320 像素视口的路由、全屏显示和横向溢出；另检查刷新、筛选和返回。
- 以下截图来自该正式构建，账号与余额是本机验收数据，不是线上账户。没有运行真实模型、线上登录联调、Docker 构建或 AWS 部署。

![正式首页](docs/approved-ui/apex-production-home.png)

![电脑换脸页面](docs/approved-ui/apex-production-face-desktop.png)

![手机换脸页面](docs/approved-ui/apex-production-face-mobile.png)
