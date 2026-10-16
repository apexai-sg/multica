/** Consumer names describe the deliverable, while IDs keep the execution contract.
 * Like the server catalog, editorial content is bilingual; other locales use English.
 * These entries describe the current text route, never an unconnected media model.
 */
type Copy = readonly [name: string, summary: string, example: string];
type ToolCopy = { zh: Copy; en: Copy };
const TOOLS: Record<string, ToolCopy> = {
  "marketing-social-media-operation": {
    zh: ["小红书文案与发帖计划", "标题、正文、话题标签，也能规划一周内容。", "帮我写一篇新加坡亲子游的小红书文案，面向带3—8岁孩子的家长，语气像真实分享。给我3个标题、正文和话题标签，不编造价格。"],
    en: ["Social posts & content plans", "Captions, headlines, hashtags and a weekly posting plan.", "Write an Instagram post about a family trip to Singapore, for parents with young children. Give me three headlines, a warm caption and hashtags. Do not invent prices."],
  },
  "ecommerce-product-copywriting": {
    zh: ["商品标题与卖点", "整理商品优势，生成标题、卖点和详情文案。", "为一款500ml不锈钢保温杯写商品标题、5条卖点和详情介绍，面向上班族。没有提供的保温时长不要编造。"],
    en: ["Product titles & descriptions", "Turn product details into titles, benefits and listing copy.", "Write a title, five benefits and a product description for a 500ml stainless steel travel mug for commuters. Do not invent insulation specifications."],
  },
  "media-short-video-copy": {
    zh: ["短视频脚本", "开场、口播和镜头建议，交付文字脚本。", "写一个30秒的咖啡店介绍视频脚本，包含开场、口播和镜头建议，竖屏，语气轻松。交付脚本文字。"],
    en: ["Short video scripts", "Hooks, voiceover and shot ideas as a written script.", "Write a 30-second vertical video script introducing a coffee shop. Include a hook, friendly voiceover and shot suggestions. Deliver a written script."],
  },
  "marketing-ad-copywriting": {
    zh: ["广告与促销文案", "按产品和客群生成多组广告标题与正文。", "为一家瑜伽馆写3组新手体验课广告，面向久坐上班族，强调循序渐进，不编造优惠价格或健康效果。"],
    en: ["Ads & promotional copy", "Several headline and copy options for your audience.", "Write three ads for a beginner yoga class aimed at office workers. Emphasize a gradual start without inventing discounts or health claims."],
  },
  "marketing-event-planning": {
    zh: ["活动方案", "活动流程、物料清单和执行分工。", "为一个30人社区亲子活动制定半天方案，包含时间表、物料和人员分工。预算暂未确定，请列出需要确认的费用。"],
    en: ["Event plans", "An agenda, materials checklist and responsibilities.", "Plan a half-day family event for 30 people, with an agenda, materials and team responsibilities. List costs that need confirmation rather than inventing a budget."],
  },
  "media-storyboard-design": {
    zh: ["故事分镜脚本", "把故事拆成镜头、对白与时长，不生成分镜图片。", "把姐弟争抢推车座位的故事写成45秒动画分镜脚本，列出每个镜头的动作、中英文对白和时长。输出文字分镜表。"],
    en: ["Storyboard scripts", "Shot descriptions, dialogue and timings; no generated images.", "Create a written 45-second animation storyboard about siblings competing for a stroller seat. Include actions, dialogue and shot timings."],
  },
  "marketing-competitor-analysis": {
    zh: ["竞品对比分析", "依据提供的资料比较产品、价格和定位。", "帮我对比两家旅行社的亲子游产品。我会上传产品资料，请比较行程、已知价格和服务差异，缺少信息的地方明确标注。"],
    en: ["Competitor comparisons", "Compare products, pricing and positioning from your sources.", "Compare two family travel packages using the documents I provide. Compare itineraries, known prices and services, and flag missing evidence."],
  },
  "ecommerce-promotion-planning": {
    zh: ["店铺促销方案", "活动机制、宣传节奏和预算待办。", "为一家社区烘焙店策划周末促销，目标是吸引新客，给出活动机制、宣传文案和需要核算的成本。"],
    en: ["Store promotion plans", "Promotion mechanics, campaign timing and budget checks.", "Plan a weekend promotion for a neighborhood bakery to attract new customers, including mechanics, promotional copy and costs to verify."],
  },
  "content-creation-lyrics-songwriting": {
    zh: ["歌词创作", "按主题写歌词，交付文本，不生成歌曲音频。", "为孩子写一首关于勇敢尝试的中文儿歌歌词，包含两段主歌和一段重复副歌，句子简短。"],
    en: ["Write lyrics", "Lyrics for your theme; this tool does not deliver song audio.", "Write children's song lyrics about trying something new. Include two short verses and a repeated chorus. Deliver lyrics only."],
  },
  "content-creation-novel-writing": {
    zh: ["故事与小说写作", "故事大纲、人物设定和章节内容。", "写一个适合4岁孩子的睡前故事，主角是一只怕黑的小兔子，约600字，结尾温暖。"],
    en: ["Stories & fiction", "Story outlines, characters and chapters.", "Write a warm bedtime story for a four-year-old about a rabbit afraid of the dark, about 500 words."],
  },
  "content-creation-screenwriting": {
    zh: ["短剧与剧本写作", "人物、场景和对白，交付剧本文字。", "写一个3分钟亲子短剧，讲孩子第一次自己收拾玩具，包含场景和对白，语气幽默。"],
    en: ["Screenplays & short dramas", "Characters, scenes and dialogue in a written screenplay.", "Write a three-minute family comedy about a child tidying their toys for the first time, with scenes and dialogue."],
  },
  "design-prd-writing": {
    zh: ["产品需求文档", "把产品想法整理为功能、流程和验收要求。", "把餐厅库存管理的想法整理成产品需求文档，包含员工记录进出货、老板查看库存和低库存提醒，先做最小可用版本。"],
    en: ["Product requirements", "Turn an idea into features, flows and acceptance criteria.", "Draft requirements for a restaurant inventory MVP with stock entries, a manager overview and low-stock alerts."],
  },
  "design-ui-prototype": {
    zh: ["网页布局与交互方案", "页面结构与交互说明；当前交付文字方案。", "为旅行社官网设计首页布局方案，包含私人定制、亲子游和咨询入口，说明手机端如何排列。输出文字设计说明。"],
    en: ["Website layout plans", "Page structure and interaction notes, delivered as text.", "Plan a travel agency homepage with custom trips, family tours and an enquiry form. Explain mobile layout in a written design brief."],
  },
  "ecommerce-bidding-strategy": {
    zh: ["广告投放计划", "预算分配和投放建议，不自动操作广告账户。", "根据我提供的产品毛利和历史数据制定广告预算分配方案，标明假设和需要补充的数据，不自动投放。"],
    en: ["Advertising plans", "Budget and campaign recommendations; no automatic ad buying.", "Use my product margins and campaign data to plan an advertising budget. Label assumptions and missing data. Do not place ads."],
  },
  "ecommerce-pricing-analysis": {
    zh: ["商品定价分析", "依据成本和目标毛利比较定价方案。", "产品成本20新币，包装3新币，平台费为售价的10%。请比较售价35、40和45新币的毛利，并说明未计入的费用。"],
    en: ["Product pricing analysis", "Compare prices using your costs and margin goals.", "A product costs SGD20 plus SGD3 packaging, with a 10% platform fee. Compare margins at SGD35, SGD40 and SGD45 and list excluded costs."],
  },
  "ecommerce-product-selection": {
    zh: ["选品对比", "比较候选商品的需求、成本和风险。", "根据我上传的3款商品资料做选品对比，比较成本、客群和售后风险，不编造销量。"],
    en: ["Compare product ideas", "Compare demand, costs and risks using your product information.", "Compare three products from the documents I upload by costs, customers and support risks. Do not invent sales figures."],
  },
  "ecommerce-user-profiling": {
    zh: ["客户群体分析", "从客户资料提炼需求、痛点与购买场景。", "根据我提供的匿名客户反馈，整理客户群体、购买原因和常见顾虑，把推测与事实分开。"],
    en: ["Customer audience analysis", "Identify needs, concerns and buying situations from your data.", "Use my anonymized customer feedback to identify audience groups, reasons to buy and concerns. Separate evidence from assumptions."],
  },
  "legal-contract-drafting": {
    zh: ["合同条款草拟", "整理条款草稿和待确认事项，需专业复核。", "为设计服务草拟交付范围、修改次数和付款条款，具体金额和适用地区尚未确定，请保留待填写项并列出复核问题。"],
    en: ["Draft contract clauses", "Draft clauses and open questions for professional review.", "Draft scope, revision and payment clauses for a design service. Leave amounts and jurisdiction as placeholders for professional review."],
  },
  "legal-contract-generation": {
    zh: ["合同草稿整理", "按已有合作信息整理合同草稿，需专业复核。", "根据我提供的合作要点整理服务合同草稿，不补造双方资料、金额或法律依据，并列出需要专业复核的内容。"],
    en: ["Prepare a contract draft", "Organize supplied terms into a draft for professional review.", "Organize my supplied terms into a service contract draft without inventing parties, amounts or legal authorities. Flag items for professional review."],
  },
  "legal-legal-research": {
    zh: ["法规资料整理", "整理资料、引用和待核实问题，供专业复核。", "根据我上传的法规资料整理摘要和引用位置，列出适用地区、资料日期及需要进一步核实的问题。"],
    en: ["Organize legal sources", "Source summaries, references and questions for professional review.", "Summarize my uploaded legal materials with citations, jurisdiction, source dates and questions requiring verification."],
  },
  "legal-litigation-strategy": {
    zh: ["纠纷材料整理", "梳理时间线、证据和咨询律师的问题。", "根据我提供的纠纷记录整理事件时间线、证据清单和咨询律师的问题，区分已证实事实和个人陈述。"],
    en: ["Organize dispute records", "A timeline, evidence checklist and questions for a lawyer.", "Organize my dispute records into a timeline, evidence checklist and questions for a lawyer. Separate verified facts from allegations."],
  },
  "marketing-user-growth": {
    zh: ["获客与增长方案", "获客渠道、试验计划和衡量指标。", "为一家新开的本地瑜伽馆制定4周获客方案，预算待定，列出低成本试验和衡量指标，不保证结果。"],
    en: ["Customer growth plans", "Acquisition channels, experiments and success measures.", "Plan four weeks of low-cost acquisition experiments for a new local yoga studio. Budget is not set. Include measures without guaranteeing results."],
  },
  "media-script-breakdown": {
    zh: ["剧本拍摄清单", "从剧本整理场景、角色、道具和拍摄事项。", "根据我上传的短片剧本，整理场景、角色、道具和拍摄准备清单，标记尚未明确的信息。"],
    en: ["Script production checklists", "Scenes, cast, props and preparation from your script.", "Break down my uploaded short-film script into scenes, cast, props and preparation, flagging missing information."],
  },
  "media-video-editing": {
    zh: ["视频剪辑方案", "剪辑步骤、字幕建议与节奏规划；当前不交付成片。", "我有3段亲子旅行素材，分别是出发、景点和返程。请规划一条30秒短视频的剪辑顺序、字幕和节奏，输出文字方案。"],
    en: ["Video editing plans", "Editing steps, caption ideas and pacing; no rendered video yet.", "I have three family travel clips: departure, sightseeing and return. Plan the order, captions and pacing for a 30-second video. Deliver a written editing plan."],
  },
};

export const RECOMMENDED_TOOL_IDS = [
  "marketing-social-media-operation", "ecommerce-product-copywriting",
  "media-short-video-copy", "marketing-ad-copywriting",
  "marketing-event-planning", "media-storyboard-design",
  "marketing-competitor-analysis", "ecommerce-promotion-planning",
];

export function toolGuidance(id: string, locale: string) {
  if (!Object.hasOwn(TOOLS, id)) return undefined;
  const [name, summary, example] = TOOLS[id]![locale.startsWith("zh") ? "zh" : "en"];
  return { name, summary, example };
}

export function toolSearchText(id: string): string {
  if (!Object.hasOwn(TOOLS, id)) return "";
  const copy = TOOLS[id]!;
  return [...copy.zh.slice(0, 2), ...copy.en.slice(0, 2)].join(" ");
}

export function orderTools<T extends { id: string }>(tools: T[]): T[] {
  const rank = (id: string) => {
    const index = RECOMMENDED_TOOL_IDS.indexOf(id);
    return index < 0 ? RECOMMENDED_TOOL_IDS.length : index;
  };
  return [...tools].sort((a, b) => rank(a.id) - rank(b.id));
}
