"use client";

import type { ReactNode } from "react";
import { Button } from "@multica/ui/components/ui/button";

import { toolSubmissionNotice, type ToolSubmissionStatus } from "./use-tool-submission";

/**
 * Everything both tool workbenches say about a submission: the reason the
 * button is unavailable, a reload action when the workspace failed to load, the
 * error, and the created issue when the detail page could not open.
 *
 * `children` is the workbench-specific readiness line, rendered before the
 * shared notice so each page keeps its own wording.
 */
export function ToolSubmissionNote({
  id,
  children,
  submission,
}: {
  id: string;
  children: ReactNode;
  submission: {
    status: ToolSubmissionStatus;
    error: string | null;
    createdIdentifier: string | null;
    reloadAgents: () => void;
  };
}) {
  return (
    <>
      <p id={id}>
        {children}
        <span>·</span>
        {toolSubmissionNotice(submission.status)}
        {submission.status === "error" ||
        submission.status === "no-agent" ||
        submission.status === "no-runtime" ? (
          <>
            <span>·</span>
            <Button type="button" variant="outline" size="sm" onClick={submission.reloadAgents}>
              重新加载
            </Button>
          </>
        ) : null}
      </p>
      {submission.error ? (
        <p className="apex-upload-error" role="alert">
          {submission.error}
        </p>
      ) : null}
      {submission.createdIdentifier ? (
        <p className="apex-sample-note" role="status">
          任务已创建：{submission.createdIdentifier}，请到任务列表查看。
        </p>
      ) : null}
    </>
  );
}
