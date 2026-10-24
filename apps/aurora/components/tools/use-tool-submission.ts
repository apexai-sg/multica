"use client";

import { useCallback, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { isAgentRuntimeBound } from "@multica/core/agents";
import {
  AuroraToolRunOutcomeUnknownError,
  auroraToolsOptions,
  createAuroraToolRun,
  isAuroraDegraded,
} from "@multica/core/aurora";
import { useAuthStore } from "@multica/core/auth";
import { useWorkspaceId } from "@multica/core/hooks";
import { useFileUpload } from "@multica/core/hooks/use-file-upload";
import { canAssignAgentToIssue } from "@multica/core/permissions";
import type { Agent, MemberRole } from "@multica/core/types";
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
  /** Local files selected in the tool; uploaded before the run is created. */
  files?: File[];
};

/**
 * Why the submit button is unavailable, in the order the reasons are checked.
 * `ready` is the only state that permits a submission.
 *
 * `no-expert` is the server saying it has no mapped executor for this tool;
 * `no-agent` is the mapped executor missing from the agents this viewer may
 * assign; `no-runtime` is that agent having lost its runtime binding.
 */
export type ToolSubmissionStatus =
  | "loading"
  | "error"
  | "no-expert"
  | "no-agent"
  | "no-runtime"
  | "ready";

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
    case "no-expert":
      return "该工具尚未配置执行专家";
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
 * The server decides which expert runs a tool: it resolves the tool id through
 * its mapping, seeds that expert's carrier, checks the workspace runtime and
 * opens the issue assigned to the carrier. This hook only pre-checks that the
 * run can start — so a workspace without a runtime fails before the user
 * uploads files — and then hands the upload ids to the server. It never picks
 * an agent.
 *
 * The pre-check reads the mapping and the agent list. The agent list shows
 * admins every agent in the workspace, including other members' private ones,
 * while the backend's invocation gate refuses to assign work to those — so the
 * candidates are narrowed with `canAssignAgentToIssue`, the same rule the
 * AssigneePicker uses.
 *
 * PR #17's semantics are preserved: the button stays locked after a run exists
 * because the route replaces this page. Files are uploaded before the run is
 * created, so a refusal after the upload leaves those rows unattached; the
 * pre-check above is what keeps the common refusals from reaching that point.
 */
export function useToolSubmission(tool: ToolDefinition, slug: string) {
  const wsId = useWorkspaceId();
  const userId = useAuthStore((s) => s.user?.id ?? null);
  const {
    data: tools,
    isLoading: toolsLoading,
    isError: toolsError,
    refetch: refetchTools,
  } = useQuery(auroraToolsOptions(wsId));
  // The tools read seeds the workspace's carriers server-side, so the agent list
  // must start only after it settles. Firing both together would let a fresh
  // workspace resolve "no-agent" from a list read before the seed committed, and
  // "重新加载" would never converge.
  const {
    data: agents,
    isLoading: agentsLoading,
    isError: agentsError,
    refetch: refetchAgents,
  } = useQuery({ ...agentListOptions(wsId), enabled: !toolsLoading });
  const {
    data: members,
    isLoading: membersLoading,
    isError: membersError,
    refetch: refetchMembers,
  } = useQuery(memberListOptions(wsId));
  const { upload } = useFileUpload(api);
  const navigation = useNavigation();
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [createdIdentifier, setCreatedIdentifier] = useState<string | null>(null);
  // The server answered 2xx but the body could not be read. The issue may
  // exist, so a retry could create a second one; the button stays locked and
  // the page tells the user to check the issue list.
  const [outcomeUnknown, setOutcomeUnknown] = useState(false);
  // The button is disabled while pending, but a click that lands between the
  // render and the state flush would still reach this callback. The ref is the
  // lock; `pending` is only what the UI renders.
  const inFlight = useRef(false);

  const rawRole = members?.find((m) => m.user_id === userId)?.role;
  const role: MemberRole | null =
    rawRole === "owner" || rawRole === "admin" || rawRole === "member"
      ? rawRole
      : null;

  const executor = tools?.value.find((entry) => entry.id === tool.id) ?? null;
  const assignable = (agents ?? []).filter(
    (candidate) =>
      !candidate.archived_at &&
      canAssignAgentToIssue(candidate, { userId, role }).allowed,
  );
  const carrier: Agent | null = executor
    ? (assignable.find(
        (candidate) =>
          candidate.system_key === `aurora:${executor.expertSlug}`,
      ) ?? null)
    : null;
  const runnable = carrier !== null && isAgentRuntimeBound(carrier);

  // A degraded tools body parses to an empty directory. Reading that as
  // "no-expert" would tell the user the server has no executor — a claim about
  // configuration that is false, with no reload offered. It is a load failure.
  const status: ToolSubmissionStatus =
    toolsLoading || agentsLoading || membersLoading
      ? "loading"
      : toolsError || isAuroraDegraded(tools) || agentsError || membersError
        ? "error"
        : !executor
          ? "no-expert"
          : !carrier
            ? "no-agent"
            : !runnable
              ? "no-runtime"
              : "ready";

  const canSubmit = status === "ready" && !pending && !outcomeUnknown;

  const submit = useCallback(
    async ({ source, values, files = [] }: ToolSubmissionInput) => {
      if (inFlight.current) return;
      if (status !== "ready") {
        setError(toolSubmissionNotice(status));
        return;
      }
      inFlight.current = true;
      setPending(true);
      setError(null);
      setCreatedIdentifier(null);

      let identifier: string;
      try {
        const uploaded: string[] = [];
        for (const file of files) {
          const result = await upload(file);
          if (!result?.id) {
            throw new Error(`无法上传 ${file.name}，请重新选择文件。`);
          }
          uploaded.push(result.id);
        }

        const run = await createAuroraToolRun(tool.id, {
          source,
          values,
          fileNames: files.map((file) => file.name),
          attachmentIds: uploaded,
        });
        identifier = run.identifier;
      } catch (err) {
        if (err instanceof AuroraToolRunOutcomeUnknownError) {
          // The request reached the server and an issue may exist. Keep the
          // lock: retrying could create a second one, and there is no
          // identifier to show.
          setOutcomeUnknown(true);
          setPending(false);
          setError("提交结果未能确认，任务可能已创建。请先到任务列表查看，不要重复提交。");
          return;
        }
        setError(
          err instanceof Error && err.message
            ? err.message
            : "任务提交未完成，请稍后重试。",
        );
        setPending(false);
        inFlight.current = false;
        return;
      }

      // The issue exists from here on. Record its identifier before trying to
      // navigate: `push` returns void and swallows a failed route, so the
      // number is the only durable proof the user can act on.
      setCreatedIdentifier(identifier);

      // Navigation is deliberately outside the try above: telling the user a
      // created task failed would invite a retry that creates a second one.
      const href = auroraIssueHrefFor(slug)(identifier);
      if (!href) {
        setPending(false);
        inFlight.current = false;
        return;
      }

      try {
        navigation.push(href);
      } catch {
        setError(`任务已创建（${identifier}），但打开详情页失败，请到任务列表查看。`);
        setPending(false);
        inFlight.current = false;
        return;
      }

      // Success: stay locked. The route replaces this page, and re-enabling the
      // button before that commit would let the same click create a second
      // issue and run the expert twice.
    },
    [executor, runnable, navigation, slug, status, tool.id, upload],
  );

  const reloadAgents = useCallback(() => {
    void refetchTools();
    void refetchAgents();
    void refetchMembers();
  }, [refetchAgents, refetchMembers, refetchTools]);

  return {
    submit,
    pending,
    error,
    createdIdentifier,
    outcomeUnknown,
    canSubmit,
    status,
    agent: carrier,
    reloadAgents,
  };
}
