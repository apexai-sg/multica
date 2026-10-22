import { notFound } from "next/navigation";
import type { Metadata } from "next";
import { findTool } from "@/components/tools/tool-definitions";
import { ToolWorkspace } from "@/components/tools/tool-workspace";

type Props = { params: Promise<{ workspaceSlug: string; toolId: string }> };

export async function generateMetadata({ params }: Props): Promise<Metadata> {
  const { toolId } = await params;
  const tool = findTool(toolId);
  return { title: `${tool?.name ?? "未找到工具"} · Apex AI` };
}

export default async function ToolPage({ params }: Props) {
  const { workspaceSlug, toolId } = await params;
  const tool = findTool(toolId);
  if (!tool) notFound();
  return <ToolWorkspace key={tool.id} tool={tool} slug={workspaceSlug} />;
}
