"use client";

import { useCallback, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { useFileUpload } from "@multica/core/hooks/use-file-upload";
import { useCreateIssue } from "@multica/core/issues/mutations";
import { agentListOptions } from "@multica/core/workspace/queries";
import { useNavigation } from "@multica/views/navigation";
import { auroraIssueHrefFor } from "@/lib/issue-link";
import type { ToolDefinition } from "./tool-definitions";

export type ToolSubmissionInput = {
  /** Free text the user typed into the tool's source field. */
  source?: string;
  /** Chosen values for the tool's settings fields, keyed by field id. */
  values?: Record<string, string>;
  /** Local files selected in the tool; uploaded before the issue is created. */
  files?: File[];
};

/**
 * Submit one tool run as an ordinary issue task.
 *
 * Aurora's generation pipeline only exists for catalog carriers. The 18 tools
 * are consumer entry points, so the run is handed to a real workspace agent as
 * an issue: the agent executes on the issue and writes its result there as a
 * comment and/or attachments, which the issue detail page renders. The user
 * lands on that detail page, so the run and its output share one address.
 *
 * The agent comes from the backend: the tool's `agentKey` selects the matching
 * workspace agent by `system_key`, with the workspace's general assistant as a
 * fallback. No agent data is fabricated in the tool definitions.
 */
export function useToolSubmission(tool: ToolDefinition, slug: string) {
  const wsId = useWorkspaceId();
  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const createIssue = useCreateIssue();
  const { upload } = useFileUpload(api);
  const navigation = useNavigation();
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const activeAgents = agents.filter((agent) => !agent.archived_at);
  const agent =
    (tool.agentKey
      ? activeAgents.find((candidate) => candidate.system_key === tool.agentKey)
      : undefined) ??
    activeAgents.find((candidate) => candidate.system_key === "mika") ??
    activeAgents[0] ??
    null;

  const canSubmit = agent !== null && !pending && !createIssue.isPending;

  const submit = useCallback(
    async ({ source, values, files = [] }: ToolSubmissionInput) => {
      if (!agent || pending) return;
      setPending(true);
      setError(null);
      try {
        const uploaded: string[] = [];
        for (const file of files) {
          const result = await upload(file);
          if (!result?.id) {
            throw new Error(`无法上传 ${file.name}，请重新选择文件。`);
          }
          uploaded.push(result.id);
        }

        const sections = [
          `## 工具\n${tool.name}`,
          source?.trim() ? `## 需求\n${source.trim()}` : "",
          values && Object.keys(values).length > 0
            ? `## 设置\n${Object.entries(values)
                .map(([key, value]) => `- ${key}: ${value}`)
                .join("\n")}`
            : "",
          files.length > 0
            ? `## 素材\n${files.map((file) => file.name).join("、")}（见附件）`
            : "",
          "请完成这个任务，把生成内容作为评论或附件返回。若当前环境无法生成真实媒体文件（图片/视频/音频），请明确说明限制，并给出可执行的方案、脚本或文案作为替代。",
        ].filter(Boolean);

        const title = `${tool.name} · ${source?.trim().slice(0, 80) || "任务"}`.slice(
          0,
          120,
        );
        const created = await createIssue.mutateAsync({
          title,
          description: sections.join("\n\n"),
          assignee_type: "agent",
          assignee_id: agent.id,
          attachment_ids: uploaded,
          // Consumer tasks are deliberately repeatable; the duplicate-title
          // guard would otherwise reject the same prompt a second time.
          allow_duplicate: true,
        });

        const href =
          auroraIssueHrefFor(slug)(created.identifier) ??
          `/${encodeURIComponent(slug)}/issues/${encodeURIComponent(created.identifier)}`;
        navigation.push(href);
      } catch (err) {
        setError(
          err instanceof Error && err.message
            ? err.message
            : "任务提交未完成，请稍后重试。",
        );
      } finally {
        setPending(false);
      }
    },
    [agent, createIssue, navigation, pending, slug, tool.name, upload],
  );

  return { submit, pending, error, canSubmit, agent };
}
