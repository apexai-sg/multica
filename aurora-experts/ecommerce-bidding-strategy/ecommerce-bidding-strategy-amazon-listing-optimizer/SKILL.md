---
name: ecommerce-bidding-strategy-amazon-listing-optimizer
version: 1.0.0
display_name: 亚马逊Listing优化
display_name_en: Amazon Listing Optimizer
description_zh: 优化亚马逊商品 Listing：标题、五点描述、产品描述、后台搜索词，按 A9 算法规则埋词、提转化、提搜索权重。输出可直接粘贴的英文标题/五点/Search Terms。
description_en: Optimize an Amazon product listing: title, five bullet points, product description, and backend search terms, following A9 rules for keyword placement and conversion. Returns ready-to-paste English title, bullets, and search terms.
description: 触发条件：用户要写亚马逊 Listing、优化标题/五点、埋关键词、提升搜索排名、写 Search Terms、做亚马逊Listing文案时使用。反向触发：「亚马逊标题怎么写」「五点描述怎么优化」「怎么埋关键词」「Search Terms 怎么填」。只做 Listing 文案与关键词优化，不做广告投放、不碰账号安全。区别于「淘宝拼多多高转化标题生成」：后者是国内电商短标题、堆热词、凑点击率；本技能是亚马逊 A9 体系下的英文标题+五点长描述+后台 Search Terms，逻辑词序、首字母大写、埋词位置规则完全不同。
---

# 亚马逊Listing优化 amazon-listing-optimizer

把产品卖点翻译成符合 A9 算法的英文 Listing：标题 + 五点 + 描述 + 后台 Search Terms，提升搜索曝光与转化。

## 何时使用

- 新品上架要写标题、五点、描述
- 老品流量低、转化差，要优化 Listing
- 要埋关键词、写后台 Search Terms
- 多站点（美/欧/日）要本地化文案

## 创作流程

### Step 1 收集产品与关键词
| 输入项 | 默认 | 说明 |
|---|---|---|
| 产品名/品类 | 必填 | 如 wireless earbuds |
| 核心卖点 | 3-5 个 | 差异化点 |
| 目标关键词 | 必填 | 已有词表或需扩词 |
| 目标站点 | 美国站 | 美国/欧洲/日本 |
| 竞品链接 | 可选 | 参考其高频词 |

### Step 2 标题优化
结构：品牌 + 核心词 + 关键特性 + 适用场景/型号，首字母大写，前 60-80 字符最关键。核心关键词放最前。

### Step 3 五点描述
每点：全大写开头短语 + 冒号 + 2-3 句说明。结构：痛点→卖点→证据→适用场景。埋次要关键词，不重复堆砌。

### Step 4 Search Terms 与描述
- 后台 Search Terms：全小写、空格分隔、不重复标题词、不含品牌词/竞品名、≤250 bytes
- 产品描述：补充故事感与细节，继续埋长尾词

## 输出格式

```text
【Title】（168字符，前60字符预览区可见）
SoundPeats Wireless Earbuds, Bluetooth 5.3 Earbuds with Noise Cancellation, 30H Playtime Earphones for Sport Running

【Bullet Points】
• NOISE CANCELLATION: Blocks 90% of ambient noise so you can focus on music or calls. Ideal for commutes and open offices.
• 30-HOUR PLAYTIME: 6 hours per charge plus 24 hours from the charging case. Quick charge 10 min = 2 hours use.
• BLUETOOTH 5.3: Stable connection within 33 feet. Auto-pair when you open the case.
• IPX7 WATERPROOF: Sweat and rain resistant, built for running, gym, and workouts.
• ULTRA LIGHT FIT: Each earbud weighs only 4g with 3 sizes of ear tips for a secure fit.

【Product Description】
SoundPeats wireless earbuds are designed for people who want studio-quality sound without the cable. Whether you are training for a marathon or taking calls in a noisy café, the noise-isolating design keeps distractions out.

【Backend Search Terms】
earbuds bluetooth wireless earphones noise cancelling waterproof running sport gym workout exercise

【关键词埋点检查】
  核心词 wireless earbuds → 标题首位 ✓
  长尾 noise cancellation / waterproof running → 五点 ✓
  场景 sport/gym/workout → Search Terms ✓
```

## 边界与说明

- 不使用违禁词（best/#1/cure 等医疗/绝对化用语，详见 references）
- 关键词以真实搜索量为参考，不堆砌同义词；单复数 Amazon 自动处理
- 不承诺排名/转化率提升，A9 算法持续变化
- 不代写竞品品牌词，不侵犯商标；Search Terms 不含竞品名/ASIN
