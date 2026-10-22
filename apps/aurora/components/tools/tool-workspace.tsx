"use client";

import type { ToolDefinition } from "./tool-definitions";
import { FaceSwapWorkbench } from "./face-swap";
import { ToolWorkbench } from "./tool-workbench";

export function ToolWorkspace({ tool, slug }: { tool: ToolDefinition; slug: string }) {
  return tool.id === "face-swap"
    ? <FaceSwapWorkbench slug={slug} />
    : <ToolWorkbench key={tool.id} tool={tool} slug={slug} />;
}
