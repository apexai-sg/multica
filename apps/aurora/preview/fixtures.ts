import { ApiClient } from "@multica/core/api";
import type { Agent, StorageAdapter, User, Workspace } from "@multica/core/types";

const time = "2026-10-19T00:00:00Z";
export const workspace: Workspace = {
  id: "apex-preview", slug: "preview", name: "Apex AI",
  description: "Local design fixtures", context: null, settings: {}, repos: [],
  issue_prefix: "DEMO", avatar_url: null, created_at: time, updated_at: time,
};
export const user: User = {
  id: "preview-user", name: "Hannah", email: "preview@example.test",
  avatar_url: null, onboarded_at: time, onboarding_questionnaire: {},
  starter_content_state: "imported", language: "zh-Hans", profile_description: "",
  timezone: "Asia/Singapore", created_at: time, updated_at: time,
};

// These are visual fixtures, not a list of deployed capabilities or prices.
const examples = [
  { name: "海报制作", description: "输入主题、文案和尺寸，生成活动、促销、招聘和朋友圈海报。", output: "image", credits: 76, skills: ["中文排版", "品牌海报", "多尺寸导出"], starter: "做一张秋季新品上市海报", prompt: "为咖啡店制作一张秋季新品上市海报。主标题：把秋天装进杯子里。暖色调，适合朋友圈。" },
  { name: "小红书图片", description: "把你的主题和资料，变成吸引眼球的封面、攻略长图和整套配图。", output: "image", credits: 62, skills: ["小红书封面", "攻略长图", "九宫格配图"], starter: "制作新加坡亲子游攻略配图", prompt: "制作新加坡三天两夜亲子游攻略配图，面向有 3—8 岁孩子的家庭，风格清新自然。" },
  { name: "短视频制作", description: "描述你想讲的故事，制作产品介绍、品牌展示和社交平台短视频。", output: "video", credits: 120, skills: ["视频脚本", "产品展示", "社交短片"], starter: "把新品亮点变成一段短视频", prompt: "制作一段 15 秒的咖啡新品介绍短片，展示手冲过程和产品特色。" },
  { name: "文案创作", description: "写产品介绍、活动文案和社交内容，让每一个想法都有合适的表达。", output: "text", credits: 30, skills: ["品牌文案", "社交内容", "中英双语"], starter: "写一篇自然分享的小红书文案", prompt: "写一篇分享新加坡周末散步路线的小红书文案，口吻自然，约 300 字。" },
  { name: "音频配音", description: "把文字变成清晰自然的语音，为视频、课程和产品介绍添加配音。", output: "audio", credits: 40, skills: ["中文配音", "英文配音", "语气调整"], starter: "为一段产品介绍生成配音", prompt: "用自然亲切的语气为这段产品介绍配音：一杯好咖啡，开启美好的一天。" },
  { name: "办公助手", description: "整理资料、梳理思路、起草邮件，把重复的小事交给你的 AI 助手。", output: null, credits: 0, skills: ["资料整理", "邮件起草", "工作计划"], starter: "帮我安排本周的工作重点", prompt: "帮我梳理本周的工作计划，按优先级列出事项、时间安排和预期成果。" },
];

export const agents: Agent[] = examples.map((example, index) => ({
  id: `preview-agent-${index}`, workspace_id: workspace.id, runtime_id: "preview-runtime",
  name: example.name, description: example.description, instructions: "",
  avatar_url: `/avatars/person-${index + 1}.svg`, runtime_mode: "local", runtime_config: {},
  max_concurrent_tasks: 1, owner_id: user.id, archived_at: null, custom_args: [],
  visibility: "private", permission_mode: "private", invocation_targets: [], model: "",
  created_at: time, updated_at: time, status: "idle", archived_by: null,
  system_key: example.output ? `aurora:preview-tool-${index}` : undefined,
  skills: example.skills.map((name, skill) => ({ id: `preview-skill-${index}-${skill}`, name, description: "", enabled: true })),
  conversation_starters: [{ label: example.starter, prompt: example.prompt }],
}));

const responses: Record<string, unknown> = {
  "/api/aurora/skills": { skills: examples.flatMap((example, index) => example.output ? [{
    id: `preview-tool-${index}`, name: example.name, name_en: example.name,
    category: example.output, credits: example.credits, input: ["text"],
    output: [example.output], available: true, featured: false,
  }] : []) },
  "/api/aurora/runtime": { workspaceId: workspace.id, node: null, runtimeId: null, state: "unconfigured" },
  "/api/aurora/billing/balance": { availableMicro: 1200000000 },
  "/api/aurora/billing/subscription": { subscription: { tier: "free", limits: { generationsPerMonth: 10, concurrency: 1 }, usage: { generationsUsedThisMonth: 0, activeGenerations: 0 } } },
};

export const storage: StorageAdapter = {
  getItem: () => null, setItem: () => {}, removeItem: () => {}, keys: () => [],
};

// Fail closed: the preview never falls back to the real network transport.
export function createPreviewApi() {
  const handlers: Partial<ApiClient> = {
    getBaseUrl: () => "",
    listWorkspaces: async () => [workspace],
    listAgents: async () => agents,
    requestJson: async <T>(path: string, options?: RequestInit): Promise<T> => {
      if ((!options?.method || options.method === "GET") && path in responses) {
        return structuredClone(responses[path]) as T;
      }
      throw new Error("This operation is unavailable in the local design preview.");
    },
  };
  return new Proxy(new ApiClient("/preview-disabled"), {
    get(_target, property: string) {
      if (property === "then") return undefined;
      const handler = handlers[property as keyof ApiClient];
      if (handler !== undefined) return handler;
      return () => Promise.reject(new Error("Preview operation unavailable."));
    },
  });
}
