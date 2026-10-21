export type ToolCategory = "image" | "video" | "office" | "writing" | "audio" | "website";
export type FileKind = "image" | "video" | "pdf" | "sheet" | "resume";
export type ResultKind = "image" | "video" | "slides" | "table" | "document" | "audio" | "website" | "transcript";

export const toolCategories: { id: ToolCategory; name: string }[] = [
  { id: "image", name: "图片工具" }, { id: "video", name: "视频工具" },
  { id: "office", name: "办公助手" }, { id: "writing", name: "写作助手" },
  { id: "audio", name: "音频工具" }, { id: "website", name: "网站设计" },
];

export type ToolField = {
  id: string;
  label: string;
  options: string[];
};

export type PreviewTool = {
  id: string;
  legacyAgentId?: string;
  name: string;
  category: ToolCategory;
  added?: boolean;
  avatar: number;
  description: string;
  tagline: string;
  chips: string[];
  upload?: { kind: FileKind; title: string; hint: string; optional?: boolean };
  source?: { label: string; placeholder: string; example: string; optional?: boolean };
  fields: ToolField[];
  action: string;
  result: { kind: ResultKind; title: string; description: string; items: string[] };
};

// This registry is a visual proposal only. It is not a provider capability catalog.
export const previewTools: PreviewTool[] = [
  {
    id: "video-transcript", name: "视频文案提取", category: "video", added: true, avatar: 3,
    description: "上传视频，把人物说的话和画面里的文字分别提取出来，方便复制和整理。",
    tagline: "把视频里的声音和文字，变成可编辑的文案。",
    chips: ["口播转文字", "画面文字", "时间点定位"],
    upload: { kind: "video", title: "上传视频", hint: "访谈、课程、口播或带字幕的视频" },
    fields: [
      { id: "scope", label: "提取内容", options: ["全部提取", "只提取人物说话", "只提取画面文字"] },
      { id: "language", label: "视频语言", options: ["自动识别", "中文", "英文", "中英混合"] },
      { id: "format", label: "口播整理方式", options: ["保留原话", "去掉语气词"] },
    ],
    action: "提取文案",
    result: { kind: "transcript", title: "提取结果", description: "口播和画面文字分开查看，可编辑、复制或导出。", items: ["人物说话", "画面文字"] },
  },
  {
    id: "face-swap", name: "图片／视频换脸", category: "video", avatar: 2,
    description: "放入原视频或图片，再放入一张人脸照片，制作新的图片或视频。",
    tagline: "两份素材，一个新作品。", chips: ["视频换脸", "图片换脸", "双框上传"],
    fields: [], action: "生成换脸视频",
    result: { kind: "video", title: "生成结果", description: "预览并下载新作品。", items: ["视频", "图片"] },
  },
  {
    id: "poster", legacyAgentId: "preview-agent-0", name: "海报制作", category: "image", avatar: 1,
    description: "输入主题、文案和尺寸，制作活动、促销、招聘和朋友圈海报。",
    tagline: "让一张海报，把你的重点讲清楚。", chips: ["中文排版", "品牌海报", "多尺寸"],
    source: { label: "海报内容", placeholder: "写下主题、标题、活动时间和想要的风格…", example: "咖啡店秋季新品海报。标题：把秋天装进杯子里。副标题：桂花拿铁，温暖上市。整体暖色调，适合朋友圈。" },
    upload: { kind: "image", title: "品牌或产品素材", hint: "可放产品照片或品牌标志", optional: true },
    fields: [
      { id: "style", label: "海报风格", options: ["简约高级", "温暖生活", "活力促销", "科技感"] },
      { id: "ratio", label: "画面比例", options: ["3:4 竖版", "1:1 方形", "16:9 横版"] },
      { id: "count", label: "方案数量", options: ["1 张", "4 张"] },
    ],
    action: "制作海报", result: { kind: "image", title: "海报预览", description: "在这里挑选方案，查看大图并下载。", items: ["海报方案", "大图预览", "图片下载"] },
  },
  {
    id: "rednote-images", legacyAgentId: "preview-agent-1", name: "小红书图片", category: "image", avatar: 2,
    description: "把主题和资料排成封面、攻略长图和整套笔记配图。",
    tagline: "从吸引眼球的封面，到一整套内容配图。", chips: ["笔记封面", "攻略长图", "整套配图"],
    source: { label: "笔记主题和内容", placeholder: "例如旅行攻略、美食推荐、产品介绍…", example: "新加坡三天两夜亲子游：动物园、滨海湾花园、圣淘沙。适合带 3—8 岁孩子的家庭。做一张封面和五张攻略配图。" },
    upload: { kind: "image", title: "参考图片", hint: "放入想用在笔记里的照片", optional: true },
    fields: [
      { id: "type", label: "配图类型", options: ["封面＋内页", "只做封面", "攻略长图"] },
      { id: "style", label: "视觉风格", options: ["清新自然", "杂志风", "手账风"] },
      { id: "count", label: "图片数量", options: ["3 张", "6 张", "9 张"] },
    ],
    action: "制作笔记配图", result: { kind: "image", title: "笔记配图", description: "按封面、内页顺序查看整组图片。", items: ["封面", "内容页", "整组下载"] },
  },
  {
    id: "image-retouch", name: "图片精修", category: "image", added: true, avatar: 4,
    description: "给图片换背景、去杂物、调光线，让普通照片更干净好看。",
    tagline: "保留你喜欢的，修好不满意的。", chips: ["抠图换背景", "去除杂物", "清晰修复"],
    upload: { kind: "image", title: "上传要修改的图片", hint: "产品、人像或生活照片都可以" },
    source: { label: "具体修改要求", placeholder: "例如去掉路人，保留人物和建筑…", example: "保留照片主体，去掉背景里的杂物，让光线更自然，整体稍微明亮一些。", optional: true },
    fields: [
      { id: "task", label: "修图目标", options: ["智能精修", "抠图换背景", "去除杂物", "提升清晰度"] },
      { id: "background", label: "背景处理", options: ["保留原背景", "纯白背景", "透明背景"] },
      { id: "strength", label: "调整程度", options: ["自然", "明显"] },
    ],
    action: "开始精修", result: { kind: "image", title: "修图前后", description: "对照原图与修图效果，再选择下载。", items: ["原图", "精修结果", "对比查看"] },
  },
  {
    id: "product-images", name: "商品图制作", category: "image", added: true, avatar: 1,
    description: "上传商品照片，制作白底主图、生活场景图和促销展示图。",
    tagline: "一张产品照片，变成一组好看的商品图。", chips: ["电商主图", "场景搭建", "产品展示"],
    upload: { kind: "image", title: "上传产品照片", hint: "优先选择主体完整、清晰的产品图" },
    source: { label: "产品卖点或画面要求", placeholder: "例如护肤精华，放在有晨光的梳妆台上…", example: "一瓶植物精华，保留原有包装与文字，放在自然晨光下的浅色台面上，周围点缀绿叶。", optional: true },
    fields: [
      { id: "scene", label: "展示场景", options: ["纯色背景", "家居生活", "自然户外", "摄影棚"] },
      { id: "ratio", label: "图片比例", options: ["1:1 方形", "3:4 竖版", "16:9 横版"] },
      { id: "count", label: "方案数量", options: ["4 张", "1 张"] },
    ],
    action: "制作商品图", result: { kind: "image", title: "商品图方案", description: "查看不同场景，选择适合店铺和社交平台的图片。", items: ["白底主图", "场景方案", "图片下载"] },
  },
  {
    id: "portraits", name: "人像写真", category: "image", added: true, avatar: 5,
    description: "用一张清晰的人像照片，制作职业照、个人形象照和风格写真。",
    tagline: "换一种风格，记录更好的自己。", chips: ["职业形象", "社交头像", "风格写真"],
    upload: { kind: "image", title: "上传人像照片", hint: "清晰、无遮挡的正面照片更合适" },
    fields: [
      { id: "style", label: "写真风格", options: ["职业形象", "自然生活", "时尚杂志", "毕业纪念"] },
      { id: "background", label: "拍摄背景", options: ["简洁纯色", "摄影棚", "自然户外"] },
      { id: "ratio", label: "照片比例", options: ["3:4 竖版", "1:1 头像"] },
    ],
    action: "制作人像写真", result: { kind: "image", title: "写真相册", description: "选出喜欢的照片，查看大图并下载。", items: ["写真方案", "头像裁切", "照片下载"] },
  },
  {
    id: "short-video", legacyAgentId: "preview-agent-2", name: "短视频制作", category: "video", avatar: 3,
    description: "描述一个想法，制作产品介绍、品牌展示和社交平台短片。",
    tagline: "把脑海里的故事，变成一段短片。", chips: ["视频脚本", "产品短片", "社交视频"],
    source: { label: "你想拍什么", placeholder: "描述主题、场景、人物和想表达的内容…", example: "制作一段咖啡店新品短片：晨光落在木桌上，特写手冲咖啡，最后出现标题「今天，从一杯好咖啡开始」。" },
    upload: { kind: "image", title: "参考画面", hint: "上传产品或场景图片作为创作参考", optional: true },
    fields: [
      { id: "duration", label: "视频时长", options: ["15 秒", "30 秒", "60 秒"] },
      { id: "ratio", label: "视频比例", options: ["9:16 竖屏", "16:9 横屏", "1:1 方形"] },
      { id: "style", label: "画面风格", options: ["真实自然", "电影感", "动画风"] },
    ],
    action: "制作短视频", result: { kind: "video", title: "短视频预览", description: "播放成片，查看脚本并下载视频。", items: ["成片", "视频脚本", "视频下载"] },
  },
  {
    id: "video-edit", name: "视频剪辑", category: "video", added: true, avatar: 6,
    description: "上传原视频，提炼精彩片段、调整节奏，并加上字幕。",
    tagline: "把长素材，剪成想看的精彩片段。", chips: ["精彩片段", "自动字幕", "节奏调整"],
    upload: { kind: "video", title: "上传原视频", hint: "课程、活动记录、口播或产品视频" },
    source: { label: "剪辑要求", placeholder: "例如保留产品介绍，去掉开头等待和重复段落…", example: "保留重点讲解，去掉停顿和重复内容，开头放最精彩的一句话，结尾保留行动建议。", optional: true },
    fields: [
      { id: "duration", label: "成片时长", options: ["30 秒", "60 秒", "3 分钟", "自动安排"] },
      { id: "ratio", label: "画面比例", options: ["保持原比例", "9:16 竖屏", "16:9 横屏"] },
      { id: "captions", label: "字幕", options: ["添加字幕", "不加字幕"] },
    ],
    action: "开始剪辑", result: { kind: "video", title: "剪辑结果", description: "按片段预览剪辑效果，并下载成片。", items: ["成片预览", "片段时间线", "字幕文件"] },
  },
  {
    id: "avatar-video", name: "数字人口播", category: "video", added: true, avatar: 2,
    description: "输入一段文案，让数字人替你讲产品、做介绍或录课程。",
    tagline: "写好文案，让你的数字人开口。", chips: ["产品讲解", "课程口播", "人物形象"],
    source: { label: "口播文案", placeholder: "输入希望数字人说的话…", example: "大家好，欢迎来到 Apex AI。选一个你需要的工具，放入素材，就可以开始创作。今天我们一起试试商品图制作。" },
    upload: { kind: "image", title: "自定义人物照片", hint: "不上传时使用右侧选择的示例形象", optional: true },
    fields: [
      { id: "avatar", label: "示例人物形象", options: ["亲切女讲师", "商务男讲师", "活力主持人"] },
      { id: "language", label: "口播语言", options: ["中文", "英文"] },
      { id: "pace", label: "语速", options: ["自然", "稍慢", "稍快"] },
    ],
    action: "制作口播视频", result: { kind: "video", title: "数字人口播预览", description: "预览人物、声音和字幕组合后的效果。", items: ["口播视频", "字幕", "视频下载"] },
  },
  {
    id: "video-translate", name: "视频翻译", category: "video", added: true, avatar: 4,
    description: "把视频翻译成另一种语言，搭配新配音和双语字幕。",
    tagline: "让你的内容，被更多人听懂。", chips: ["多语翻译", "配音替换", "双语字幕"],
    upload: { kind: "video", title: "上传要翻译的视频", hint: "选择人声清晰的视频" },
    fields: [
      { id: "sourceLanguage", label: "原视频语言", options: ["自动识别", "中文", "英文"] },
      { id: "targetLanguage", label: "翻译成", options: ["英文", "中文", "日文", "韩文"] },
      { id: "captions", label: "字幕形式", options: ["双语字幕", "只显示译文"] },
      { id: "voice", label: "声音风格", options: ["自然讲述", "商务讲解"] },
    ],
    action: "翻译视频", result: { kind: "video", title: "翻译视频预览", description: "对照原文、译文和视频，确认后导出。", items: ["翻译视频", "对照文案", "字幕文件"] },
  },
  {
    id: "presentation", name: "PPT 制作", category: "office", added: true, avatar: 1,
    description: "输入主题和资料，整理演示大纲，排成汇报或介绍用的 PPT。",
    tagline: "从一个主题，到一份讲得清楚的演示。", chips: ["内容大纲", "自动排版", "逐页预览"],
    source: { label: "演示主题和资料", placeholder: "要讲什么、给谁看、希望听众记住什么…", example: "给团队介绍 Apex AI 工具平台。包含用户痛点、核心工具、使用流程、产品优势和下一步计划，适合 10 分钟分享。" },
    fields: [
      { id: "purpose", label: "使用场景", options: ["工作汇报", "产品介绍", "教学培训", "商业提案"] },
      { id: "pages", label: "页数", options: ["6 页", "10 页", "15 页"] },
      { id: "style", label: "设计风格", options: ["商务简洁", "品牌展示", "清新活力"] },
    ],
    action: "制作 PPT", result: { kind: "slides", title: "演示文稿", description: "左侧查看目录，右侧预览每一页，完成后导出 PPT。", items: ["演示大纲", "逐页预览", "PPT 导出"] },
  },
  {
    id: "spreadsheet", name: "Excel 助手", category: "office", added: true, avatar: 6,
    description: "上传表格，整理数据、解释公式、汇总重点或制作图表。",
    tagline: "把复杂表格，变成看得懂的结果。", chips: ["数据整理", "公式帮助", "图表汇总"],
    upload: { kind: "sheet", title: "上传 Excel 或 CSV", hint: "选择要整理、分析的表格" },
    source: { label: "你想怎么处理这份表格", placeholder: "例如按月份汇总收入，并找出增长最快的产品…", example: "按月份汇总销售额，列出销售额最高的五个产品，再给出一张趋势图。" },
    fields: [
      { id: "task", label: "主要任务", options: ["数据汇总", "制作图表", "清理数据", "解释公式"] },
      { id: "language", label: "结果语言", options: ["中文", "英文"] },
    ],
    action: "处理表格", result: { kind: "table", title: "表格处理结果", description: "查看整理后的表格、分析说明和图表。", items: ["结果表格", "图表", "处理说明"] },
  },
  {
    id: "pdf-reader", name: "PDF 阅读助手", category: "office", added: true, avatar: 3,
    description: "上传 PDF，快速读懂重点，针对内容提问并定位引用页码。",
    tagline: "长文档的重点，一眼就能找到。", chips: ["重点摘要", "文档问答", "页码引用"],
    upload: { kind: "pdf", title: "上传 PDF", hint: "报告、说明书、课程资料或论文" },
    source: { label: "想了解的问题", placeholder: "例如这份报告最重要的三个结论是什么？", example: "总结这份资料的核心观点、关键数据和建议，并标出对应页码。", optional: true },
    fields: [
      { id: "mode", label: "阅读方式", options: ["重点摘要", "回答问题", "提取行动项"] },
      { id: "length", label: "内容长度", options: ["简明", "详细"] },
    ],
    action: "开始阅读", result: { kind: "document", title: "阅读结果", description: "回答旁保留引用页码，方便回到原文核对。", items: ["内容摘要", "问答", "引用页码"] },
  },
  {
    id: "copywriting", legacyAgentId: "preview-agent-3", name: "文案创作", category: "writing", avatar: 4,
    description: "写产品介绍、活动文案和社交内容，让想法有合适的表达。",
    tagline: "把想说的话，写得更动人。", chips: ["社交内容", "品牌文案", "中英双语"],
    source: { label: "文案主题和要求", placeholder: "写什么、发给谁、有哪些必须提到的内容…", example: "写一篇新加坡周末散步路线的小红书文案，推荐植物园和周边咖啡店，口吻像朋友分享，约 300 字。" },
    fields: [
      { id: "platform", label: "发布场景", options: ["小红书", "朋友圈", "产品介绍", "活动推广"] },
      { id: "tone", label: "表达语气", options: ["自然分享", "专业清晰", "活泼有趣"] },
      { id: "length", label: "文案长度", options: ["简短", "适中", "详细"] },
    ],
    action: "创作文案", result: { kind: "document", title: "文案草稿", description: "选择喜欢的版本，继续修改或复制使用。", items: ["标题", "正文", "话题标签"] },
  },
  {
    id: "voiceover", legacyAgentId: "preview-agent-4", name: "音频配音", category: "audio", avatar: 5,
    description: "把文字变成自然的语音，为视频、课程和产品介绍配音。",
    tagline: "让每一句文案，都有合适的声音。", chips: ["中文配音", "英文配音", "语速调整"],
    source: { label: "配音文案", placeholder: "输入希望朗读的文字…", example: "一杯好咖啡，开启美好的一天。欢迎来到我们的咖啡小店，让今天的忙碌慢下来。" },
    fields: [
      { id: "voice", label: "声音风格", options: ["温柔女声", "自然男声", "活力女声"] },
      { id: "language", label: "配音语言", options: ["中文", "英文"] },
      { id: "pace", label: "语速", options: ["正常", "稍慢", "稍快"] },
    ],
    action: "生成配音", result: { kind: "audio", title: "配音试听", description: "试听声音和节奏，再下载音频。", items: ["音频试听", "配音文案", "音频下载"] },
  },
  {
    id: "website", name: "网站设计助手", category: "website", added: true, avatar: 6,
    description: "描述业务和网站目的，安排页面结构、文案和视觉样式。",
    tagline: "把你的业务，放进一个好看又清楚的网站。", chips: ["页面结构", "网站文案", "视觉设计"],
    source: { label: "网站需求", placeholder: "你的业务是什么？希望访客看完后做什么？", example: "做一个新加坡亲子旅行服务网站。展示定制行程、服务优势和客户故事，让家长能留下出行日期和联系方式。风格温暖清晰。" },
    fields: [
      { id: "type", label: "网站类型", options: ["企业官网", "产品介绍", "个人主页", "活动页面"] },
      { id: "style", label: "整体风格", options: ["简洁现代", "温暖亲切", "科技感"] },
      { id: "color", label: "主色调", options: ["深蓝紫", "自然绿", "暖橙色"] },
    ],
    action: "设计网站", result: { kind: "website", title: "网站方案", description: "切换电脑和手机视图，查看页面布局。", items: ["电脑端", "手机端", "页面说明"] },
  },
  {
    id: "resume", name: "简历优化", category: "office", added: true, avatar: 5,
    description: "结合目标岗位，整理简历结构、突出经历亮点并优化表达。",
    tagline: "让你的经历，更准确地被看见。", chips: ["岗位匹配", "经历润色", "简历排版"],
    upload: { kind: "resume", title: "上传现有简历", hint: "支持 PDF 或 Word 简历" },
    source: { label: "目标岗位和招聘要求", placeholder: "粘贴职位介绍，或写下你想申请的岗位…", example: "申请市场运营岗位，工作包括内容策划、社交媒体运营、活动执行和数据复盘。", optional: true },
    fields: [
      { id: "focus", label: "优化重点", options: ["整体优化", "突出项目经历", "精简表达"] },
      { id: "language", label: "简历语言", options: ["中文", "英文"] },
      { id: "pages", label: "篇幅", options: ["1 页", "2 页"] },
    ],
    action: "优化简历", result: { kind: "document", title: "简历优化结果", description: "对照修改建议和优化后的简历，确认经历描述准确。", items: ["修改建议", "优化简历", "文档导出"] },
  },
];

export const toolPath = (tool: PreviewTool) => `/preview/tools/${tool.id}`;
export const toolAgentId = (tool: PreviewTool) => tool.legacyAgentId ?? `preview-${tool.id}`;
export const findPreviewTool = (path: string) => previewTools.find((tool) =>
  path === toolPath(tool) || path === `/preview/tools/${toolAgentId(tool)}`,
);
