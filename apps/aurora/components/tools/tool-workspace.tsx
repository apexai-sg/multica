"use client";

import type { ToolDefinition } from "./tool-definitions";
import { FaceSwapWorkbench } from "./face-swap";
import { ToolWorkbench } from "./tool-workbench";

export function ToolWorkspace({ tool }: { tool: ToolDefinition }) {
  return tool.id === "face-swap"
    ? <FaceSwapWorkbench />
    : <ToolWorkbench key={tool.id} tool={tool} />;
}
