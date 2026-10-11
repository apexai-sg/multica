"use client";

import { WorkspaceRecovery } from "@/components/workspace-recovery";

/**
 * The app's two dead ends — a slug that names no workspace, and a URL that
 * names no route — are one screen with two sets of words: a heading, the
 * reason, and the ways out.
 *
 * They share the component rather than the copy because the recovery below is
 * the part that must not drift: both branches leave the user in exactly the
 * same position, so a screen that offered a different way out — or none —
 * would strand half the people who reach it.
 */
export function DeadEndScreen({
  title,
  description,
}: {
  title: string;
  description: string;
}) {
  return (
    <div className="aurora-centered">
      <div className="aurora-empty w-full max-w-lg">
        <h1 className="text-display-sm font-semibold tracking-tight">
          {title}
        </h1>
        <p>{description}</p>
      </div>
      <WorkspaceRecovery />
    </div>
  );
}
