import { ApiClient } from "@multica/core/api";
import { previewTools, toolAgentId } from "./tool-definitions";
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

export const FACE_SWAP_AGENT_ID = "preview-face-swap";
export const FACE_SWAP_PATH = "/preview/tools/face-swap";

// These are visual fixtures, not a list of deployed capabilities or prices.
export const agents: Agent[] = previewTools.map((tool) => ({
  id: toolAgentId(tool), workspace_id: workspace.id, runtime_id: "preview-runtime",
  name: tool.name, description: tool.description, instructions: "",
  avatar_url: `/avatars/person-${tool.avatar}.svg`, runtime_mode: "local", runtime_config: {},
  max_concurrent_tasks: 1, owner_id: user.id, archived_at: null, custom_args: [],
  visibility: "private", permission_mode: "private", invocation_targets: [], model: "",
  created_at: time, updated_at: time, status: "idle", archived_by: null,
  system_key: `aurora:preview-${tool.id}`,
  skills: tool.chips.map((name, index) => ({ id: `${tool.id}-${index}`, name, description: "", enabled: true })),
  conversation_starters: tool.source ? [{ label: `试试${tool.name}`, prompt: tool.source.example }] : [],
}));

const responses: Record<string, unknown> = {
  "/api/aurora/skills": { skills: previewTools.map((tool) => ({
    id: `preview-${tool.id}`, name: tool.name, name_en: tool.name,
    category: tool.category, credits: 0,
    input: tool.upload ? [tool.upload.kind === "video" ? "video" : tool.upload.kind === "image" ? "image" : "file"] : ["text"],
    output: [tool.result.kind === "transcript" || tool.result.kind === "document" ? "text" : tool.result.kind],
    available: true, featured: false,
  })) },
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
