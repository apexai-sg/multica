"use client";

import type { LucideIcon } from "lucide-react";
import { cn } from "@multica/ui/lib/utils";

/**
 * Aurora's own page chrome.
 *
 * The shared `CollectionPageHeader` is web and desktop's — a 48px toolbar row
 * that belongs to the Multica shell — so Aurora carries the design system's page
 * head instead: an eyebrow, the title, whatever facts the collection has, and
 * the page's actions.
 *
 * There is deliberately no `description` prop. The reference layout has a
 * subtitle slot, but a page states its title and its facts once; a sentence that
 * restates the title is exactly what the copy rules exist to keep out.
 */
export function AuroraPageHeader({
  icon: Icon,
  eyebrow,
  title,
  meta,
  actions,
}: {
  icon: LucideIcon;
  eyebrow: string;
  title: string;
  /** Facts about the collection — a count, a date range. Not a sentence. */
  meta?: React.ReactNode;
  actions?: React.ReactNode;
}) {
  return (
    <header className="aurora-page-head">
      <div className="aurora-page-head-main">
        <p className="aurora-eyebrow">
          <Icon aria-hidden="true" className="size-3.5" />
          {eyebrow}
        </p>
        <h1>{title}</h1>
        {meta ? <p className="aurora-page-meta">{meta}</p> : null}
      </div>
      {actions ? <div className="aurora-page-actions">{actions}</div> : null}
    </header>
  );
}

type AuroraPageStateTone = "muted" | "destructive" | "warning";

const TONE_CLASS: Record<AuroraPageStateTone, string> = {
  muted: "text-muted-foreground",
  destructive: "text-destructive",
  warning: "text-warning",
};

/**
 * The one empty / failed / no-match surface every Aurora page uses.
 *
 * The tone colours the icon only: the title stays legible as a title in all
 * three cases, and a screen that painted the whole block red would shout at a
 * user who is only looking at an empty library.
 */
export function AuroraPageState({
  icon: Icon,
  title,
  description,
  actions,
  tone = "muted",
  role,
}: {
  icon: LucideIcon;
  title: string;
  description?: string;
  actions?: React.ReactNode;
  tone?: AuroraPageStateTone;
  role?: "alert" | "status";
}) {
  return (
    <div role={role} className="aurora-empty">
      <Icon aria-hidden="true" className={cn("size-8", TONE_CLASS[tone])} />
      <h3>{title}</h3>
      {description ? <p>{description}</p> : null}
      {actions ? <div className="aurora-empty-actions">{actions}</div> : null}
    </div>
  );
}
