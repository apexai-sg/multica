import { apexTools, type ToolDefinition } from "../components/tools/tool-definitions";

const legacyAgentIds: Record<string, string> = {
  poster: "preview-agent-0", "rednote-images": "preview-agent-1",
  "short-video": "preview-agent-2", copywriting: "preview-agent-3", voiceover: "preview-agent-4",
};
export const toolPath = (tool: ToolDefinition) => `/preview/tools/${tool.id}`;
export const toolAgentId = (tool: ToolDefinition) => legacyAgentIds[tool.id] ?? `preview-${tool.id}`;
export const findPreviewTool = (path: string) => apexTools.find((tool) =>
  path === toolPath(tool) || path === `/preview/tools/${toolAgentId(tool)}`,
);
