"use client";

import { useCallback, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { isAgentRuntimeBound } from "@multica/core/agents";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import { useFileUpload } from "@multica/core/hooks/use-file-upload";
import { useCreateIssue } from "@multica/core/issues/mutations";
import { canAssignAgentToIssue } from "@multica/core/permissions";
import type { Agent, Issue, MemberRole } from "@multica/core/types";
import {
  agentListOptions,
  memberListOptions,
} from "@multica/core/workspace/queries";
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
 * Why the submit button is unavailable, in the order the reasons are checked.
 * `ready` is the only state that permits a submission.
 */
export type ToolSubmissionStatus =
  | "loading"
  | "error"
  | "no-agent"
  | "no-runtime"
  | "ready";

const NO_AGENT_ERROR =
  "当前工作区没有可执行的助手，请先在工作区设置里创建或连接一个助手。";

/**
 * Localized explanation for a non-ready submission state. Components render
 * this next to the submit button so a disabled button always says why.
 */
export function toolSubmissionNotice(status: ToolSubmissionStatus): string {
  switch (status) {
    case "loading":
      return "正在加载工作区助手…";
    case "error":
      return "无法加载工作区助手";
    case "no-agent":
      return "工作区还没有可用的助手";
    case "no-runtime":
      return "工作区的助手尚未连接运行环境";
    case "ready":
      return "提交后由 AI 在任务详情页执行并返回结果";
  }
}

/**
 * Submit one tool run as an ordinary issue task.
 *
 * Aurora's generation pipeline only exists for catalog carriers. The 18 tools
 * are consumer entry points, so the run is handed to a workspace agent as an
 * issue: the agent executes on the issue and writes its result there as a
 * comment and/or attachments, which the issue detail page renders. The user
 * lands on that detail page, so the run and its output share one address.
 *
 * Which agent runs it is decided from the agents the viewer may assign in this
 * workspace — the built-in assistant when present, otherwise the oldest agent
 * that still has a runtime. Tools deliberately carry no `agentKey`: the
 * retired skill ids do not map onto the current expert catalog, so a per-tool
 * key could only match by accident. Any agent the viewer can assign is
 * expected to handle the text part of a tool task; media generation still
 * depends on its providers.
 */
export function useToolSubmission(tool: ToolDefinition, slug: string) {
  const wsId = useWorkspaceId();
  const userId = useAuthStore((s) => s.user?.id ?? null);
  const {
    data: agents,
    isLoading,
    isError,
    refetch,
  } = useQuery(agentListOptions(wsId));
  // The agent list shows admins every agent in the workspace, including other
  // members' private ones, while the backend's invocation gate refuses to
  // assign work to those — the submission would upload its attachments first
  // and then fail with a 403. The viewer's role is what decides that gate, so
  // it has to resolve before the list can be narrowed.
  const { data: members, isLoading: membersLoading } = useQuery(
    memberListOptions(wsId),
  );
  const createIssue = useCreateIssue();
  const { upload } = useFileUpload(api);
  const navigation = useNavigation();
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [createdIdentifier, setCreatedIdentifier] = useState<string | null>(null);
  // The button is disabled while pending, but a click that lands between the
  // render and the state flush would still reach this callback. The ref is the
  // lock; `pending` is only what the UI renders.
  const inFlight = useRef(false);

  const rawRole = members?.find((m) => m.user_id === userId)?.role;
  const role: MemberRole | null =
    rawRole === "owner" || rawRole === "admin" || rawRole === "member"
      ? rawRole
      : null;

  const activeAgents = (agents ?? []).filter(
    (candidate) =>
      !candidate.archived_at &&
      canAssignAgentToIssue(candidate, { userId, role }).allowed,
  );
  const runnableAgents = activeAgents.filter(isAgentRuntimeBound);
  const agent: Agent | null =
    runnableAgents.find((candidate) => candidate.system_key === "mika") ??
    runnableAgents[0] ??
    null;

  const status: ToolSubmissionStatus =
    isLoading || membersLoading
      ? "loading"
      : isError
        ? "error"
        : runnableAgents.length > 0
          ? "ready"
          : activeAgents.length > 0
            ? "no-runtime"
            : "no-agent";

  const canSubmit = status === "ready" && !pending;

  const submit = useCallback(
    async ({ source, values, files = [] }: ToolSubmissionInput) => {
      if (inFlight.current) return;
      if (!agent) {
        setError(NO_AGENT_ERROR);
        return;
      }
      inFlight.current = true;
      setPending(true);
      setError(null);
      setCreatedIdentifier(null);

      const uploaded: string[] = [];
      let created: Issue;
      try {
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
        created = await createIssue.mutateAsync({
          title,
          description: sections.join("\n\n"),
          assignee_type: "agent",
          assignee_id: agent.id,
          attachment_ids: uploaded,
          // Consumer tasks are deliberately repeatable; the duplicate-title
          // guard would otherwise reject the same prompt a second time.
          allow_duplicate: true,
        });
      } catch (err) {
        setError(
          err instanceof Error && err.message
            ? err.message
            : "任务提交未完成，请稍后重试。",
        );
        setPending(false);
        inFlight.current = false;
        return;
      }

      // The issue exists from here on. Navigation is deliberately outside the
      // try above: telling the user a created task failed would invite a retry
      // that creates a second one.
      const href = auroraIssueHrefFor(slug)(created.identifier);
      if (!href) {
        setCreatedIdentifier(created.identifier);
        setPending(false);
        inFlight.current = false;
        return;
      }

      try {
        navigation.push(href);
      } catch {
        setCreatedIdentifier(created.identifier);
        setError(`任务已创建（${created.identifier}），但打开详情页失败，请到任务列表查看。`);
        setPending(false);
        inFlight.current = false;
        return;
      }

      // Success: stay locked. The route replaces this page, and re-enabling the
      // button before that commit would let the same click create a second
      // issue and run the agent twice.
    },
    [agent, createIssue, navigation, slug, tool.name, upload],
  );

  const reloadAgents = useCallback(() => {
    void refetch();
  }, [refetch]);

  return {
    submit,
    pending,
    error,
    createdIdentifier,
    canSubmit,
    status,
    agent,
    reloadAgents,
  };
}
