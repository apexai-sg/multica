import { WorkspaceToolDirectory } from "@/components/tools/workspace-tool-directory";

export default async function SkillsPage({ params }: {
  params: Promise<{ workspaceSlug: string }>;
}) {
  const { workspaceSlug } = await params;
  return <WorkspaceToolDirectory slug={workspaceSlug} />;
}
