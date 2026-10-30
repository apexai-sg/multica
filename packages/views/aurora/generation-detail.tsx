"use client";

import { useState } from "react";
import {
  ArrowLeft, Check, CircleAlert, Copy, FileQuestion, Sparkles } from "@multica/ui/icons";
import { Button, buttonVariants } from "@multica/ui/components/ui/button";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { Spinner } from "@multica/ui/components/ui/spinner";
import { copyText } from "@multica/ui/lib/clipboard";
import { cn } from "@multica/ui/lib/utils";
import {
  isAuroraDegraded,
  isAuroraGenerationNotFoundError,
  isAuroraGenerationTerminal,
  useAuroraGenerationDetail,
  useAuroraSkills,
  type AuroraAsset,
  type AuroraGenerationDetail,
  type AuroraSkill,
} from "@multica/core/aurora";
import { AppLink } from "../navigation";
import { useLocale, useT, useTimeAgo } from "../i18n";
import { AuroraPageHeader, AuroraPageState } from "./aurora-page-header";
import { formatElapsed, formatMicroCredits } from "./format";
import {
  GenerationArtifacts,
  GenerationStatusBadge,
} from "./generation-artifacts";
import { generationStatusLabel, skillDisplayName } from "./labels";
import { AuroraLoadFailed } from "./load-failed";
import { useAuroraAgentNames } from "./use-aurora-agent-names";

// Stable reference for "the catalog has not loaded yet", so the skill lookup
// below does not rebuild its array on every render.
const EMPTY_SKILLS: AuroraSkill[] = [];

/**
 * One generation: the process that produced it and the results it produced.
 *
 * Deep-linkable, because a generation is a thing a user comes back to — from
 * the works list, from the history list, or from a link someone else pasted.
 * The process section can only show what the payload carries: status, times,
 * skill, prompt, credits and the failure reason. The API persists no
 * step-by-step phase timeline (the runtime logs phases but never returns them),
 * so nothing here pretends to be one.
 *
 * The payload has no "last updated" field. The elapsed time is therefore
 * measured from the generation's creation to the newest artifact write, which
 * is the only end timestamp the detail read exposes; with no artifacts there is
 * no elapsed figure at all.
 */
export interface GenerationDetailProps {
  generationId: string;
  /** The app's library route, offered as the way back. */
  backHref?: string;
  /**
   * The app's route for one task. When omitted, the task id is shown in a
   * copyable form instead — an app without a task page must not invent one.
   */
  taskHref?: (taskId: string) => string;
  /**
   * The app's route for the issue the generation ran on. Takes the
   * `issueIdentifier` and returns null when it cannot build a URL — what core's
   * `auroraIssueHref` does for a missing or degraded identifier — in which case
   * no issue link is offered.
   */
  issueHref?: (identifier: string) => string | null;
}

export function GenerationDetail({
  generationId,
  backHref,
  taskHref,
  issueHref,
}: GenerationDetailProps) {
  const { t } = useT("aurora");
  const detailQuery = useAuroraGenerationDetail(generationId);

  const generation = detailQuery.data?.value;
  // A read the schema rejected resolves without a generation, which is a load
  // failure rather than "not found": the client never read an answer at all.
  const loadFailed =
    detailQuery.isError || isAuroraDegraded(detailQuery.data);
  // A 404 is an answer — the generation is gone — and deserves its own state
  // rather than a retry card that cannot help.
  const notFound =
    detailQuery.isError &&
    isAuroraGenerationNotFoundError(detailQuery.error);
  const issueUrl =
    generation?.issueIdentifier && issueHref
      ? issueHref(generation.issueIdentifier)
      : null;

  return (
    <div className="aurora-page">
      <AuroraPageHeader
        icon={Sparkles}
        eyebrow={t(($) => $.detail.eyebrow)}
        title={t(($) => $.detail.title)}
        actions={
          issueUrl || backHref ? (
            <>
              {issueUrl ? (
                <AppLink
                  href={issueUrl}
                  className={cn(
                    buttonVariants({ variant: "outline", size: "sm" }),
                    "min-h-10 min-w-10",
                  )}
                >
                  {generation?.issueIdentifier}
                </AppLink>
              ) : null}
              {backHref ? (
                <AppLink
                  href={backHref}
                  className={cn(
                    buttonVariants({ variant: "outline", size: "sm" }),
                    "min-h-10 min-w-10",
                  )}
                >
                  <ArrowLeft aria-hidden="true" className="size-3.5" />
                  {t(($) => $.detail.back)}
                </AppLink>
              ) : null}
            </>
          ) : undefined
        }
      />
      {detailQuery.isPending ? (
        <DetailSkeleton />
      ) : notFound ? (
        <AuroraPageState
          icon={FileQuestion}
          title={t(($) => $.detail.not_found_title)}
          description={t(($) => $.detail.not_found_description)}
        />
      ) : loadFailed || !generation ? (
        <AuroraLoadFailed
          title={t(($) => $.detail.load_failed_title)}
          onRetry={() => void detailQuery.refetch()}
        />
      ) : (
        <GenerationFields generation={generation} taskHref={taskHref} />
      )}
    </div>
  );
}

/** The readable generation: process, result, and the task it ran as. */
function GenerationFields({
  generation,
  taskHref,
}: {
  generation: AuroraGenerationDetail;
  taskHref?: (taskId: string) => string;
}) {
  const { t } = useT("aurora");
  const locale = useLocale();
  const timeAgo = useTimeAgo();
  const skillsQuery = useAuroraSkills();

  const skills = skillsQuery.data?.value ?? EMPTY_SKILLS;
  const agentNames = useAuroraAgentNames();
  const catalogLoaded =
    skillsQuery.data !== undefined && !isAuroraDegraded(skillsQuery.data);
  const skill = skills.find((entry) => entry.id === generation.skillId);
  // The detail read carries only the skill id. Prefer the presenting agent's
  // backend name, then the catalog's display name; fall back to the id itself
  // when the catalog has not loaded, because a raw id is still a fact, and to
  // the shared unknown label only when the catalog answered and did not list
  // it.
  const skillName =
    agentNames.get(generation.skillId) ??
    (skill
      ? skillDisplayName(skill, locale)
      : catalogLoaded
        ? t(($) => $.works.skill_unknown)
        : generation.skillId);

  const assets = generation.assets ?? [];
  const elapsed = elapsedMs(generation.createdAt, assets);
  const failed = generationStatusLabel(generation.status) === "failed";
  const terminal = isAuroraGenerationTerminal(generation.status);
  const prompt = generation.prompt || t(($) => $.works.no_prompt);

  return (
    <div className="flex flex-col gap-6">
      <section className="flex flex-col gap-3">
        <div className="aurora-section-head">
          <h2>{t(($) => $.detail.process_title)}</h2>
        </div>
        <dl className="aurora-field-grid aurora-panel sm:grid-cols-2">
          <Field label={t(($) => $.detail.status_label)}>
            <span className="flex items-center gap-2">
              <GenerationStatusBadge status={generation.status} />
              {!terminal ? <Spinner aria-hidden="true" /> : null}
            </span>
          </Field>
          {generation.createdAt ? (
            <Field label={t(($) => $.detail.created_label)}>
              <time dateTime={generation.createdAt}>
                {timeAgo(generation.createdAt)}
              </time>
            </Field>
          ) : null}
          {elapsed !== null ? (
            <Field label={t(($) => $.detail.elapsed_label)}>
              {formatElapsed(elapsed, locale)}
            </Field>
          ) : null}
          <Field label={t(($) => $.detail.skill_label)}>{skillName}</Field>
          <Field label={t(($) => $.detail.credits_reserved_label)}>
            {t(($) => $.credits, {
              credits: formatMicroCredits(generation.creditsReserved, locale),
            })}
          </Field>
          <Field label={t(($) => $.detail.credits_charged_label)}>
            {failed
              ? t(($) => $.detail.refunded)
              : t(($) => $.credits, {
                  credits: formatMicroCredits(generation.creditsCharged, locale),
                })}
          </Field>
          <Field
            label={t(($) => $.detail.prompt_label)}
            className="sm:col-span-2"
          >
            <p className="whitespace-pre-wrap break-words">{prompt}</p>
          </Field>
        </dl>
        {failed && generation.error ? (
          <div className="flex flex-col gap-1">
            <h3 className="text-caption font-medium text-destructive">
              {t(($) => $.detail.failure_label)}
            </h3>
            <p className="flex items-start gap-1.5 text-body text-destructive">
              <CircleAlert
                aria-hidden="true"
                className="mt-0.5 size-3.5 shrink-0"
              />
              <span className="min-w-0 break-words">{generation.error}</span>
            </p>
          </div>
        ) : null}
      </section>

      <section className="flex flex-col gap-3">
        <div className="aurora-section-head">
          <h2>{t(($) => $.detail.result_title)}</h2>
        </div>
        <GenerationArtifacts generation={generation} assets={assets} />
        {terminal ? (
          <p className="text-caption text-muted-foreground">
            {failed
              ? t(($) => $.detail.settled_refunded)
              : t(($) => $.detail.settled_charged, {
                  credits: formatMicroCredits(generation.creditsCharged, locale),
                })}
          </p>
        ) : null}
      </section>

      {generation.taskId ? (
        <TaskSection taskId={generation.taskId} taskHref={taskHref} />
      ) : null}
    </div>
  );
}

/**
 * The task the generation ran as.
 *
 * The app has no task route, so the id is offered in a copyable form with an
 * explicit note rather than a link that would 404. The taskHref prop is the
 * seam for an app that grows one: the same section then renders a real link.
 */
function TaskSection({
  taskId,
  taskHref,
}: {
  taskId: string;
  taskHref?: (taskId: string) => string;
}) {
  const { t } = useT("aurora");
  const [copied, setCopied] = useState(false);
  const [copyFailed, setCopyFailed] = useState(false);

  async function handleCopy() {
    if (await copyText(taskId)) {
      setCopied(true);
      setCopyFailed(false);
      setTimeout(() => setCopied(false), 1500);
    } else {
      setCopyFailed(true);
    }
  }

  return (
    <section className="flex flex-col gap-3">
      <div className="aurora-section-head">
        <h2>{t(($) => $.detail.task_title)}</h2>
      </div>
      {taskHref ? (
        <AppLink
          href={taskHref(taskId)}
          className="inline-flex w-fit items-center gap-1 text-body underline decoration-muted-foreground/30 underline-offset-4 transition-colors hover:text-foreground"
        >
          {t(($) => $.detail.task_id_label)}: {taskId}
        </AppLink>
      ) : (
        <div className="aurora-panel flex flex-col gap-2">
          <span className="text-caption text-muted-foreground">
            {t(($) => $.detail.task_id_label)}
          </span>
          <div className="flex items-center gap-1.5">
            <code className="min-w-0 truncate rounded-xs bg-muted px-2 py-1 font-mono text-caption">
              {taskId}
            </code>
            <Button
              type="button"
              variant="ghost"
              size="icon-xs"
              className="shrink-0"
              aria-label={
                copied
                  ? t(($) => $.detail.task_copied)
                  : t(($) => $.detail.task_copy)
              }
              onClick={() => void handleCopy()}
            >
              {copied ? (
                <Check aria-hidden="true" />
              ) : (
                <Copy aria-hidden="true" />
              )}
            </Button>
          </div>
          <p className="text-caption text-muted-foreground">
            {t(($) => $.detail.task_unavailable)}
          </p>
          {copyFailed ? (
            <p role="alert" className="text-caption text-destructive">
              {t(($) => $.detail.task_copy_failed)}
            </p>
          ) : null}
        </div>
      )}
    </section>
  );
}

/** One labelled process value. */
function Field({
  label,
  children,
  className,
}: {
  label: string;
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <div className={cn("aurora-field", className)}>
      <dt className="text-caption text-muted-foreground">{label}</dt>
      <dd className="text-body">{children}</dd>
    </div>
  );
}

/**
 * Creation-to-latest-artifact span in ms, or null when there is nothing to
 * measure. See the component comment: the payload carries no update timestamp,
 * so the newest artifact write is the end of the span.
 */
function elapsedMs(createdAt: string, assets: AuroraAsset[]): number | null {
  const start = Date.parse(createdAt);
  if (!Number.isFinite(start)) return null;
  let latest = start;
  for (const asset of assets) {
    const written = Date.parse(asset.createdAt);
    if (Number.isFinite(written) && written > latest) latest = written;
  }
  return latest > start ? latest - start : null;
}

/** Placeholder sections at the page's own geometry, so it does not jump. */
function DetailSkeleton() {
  return (
    <div className="flex flex-col gap-4">
      <Skeleton className="h-5 w-24" />
      <Skeleton className="h-28 w-full rounded-xl" />
      <Skeleton className="h-5 w-20" />
      <Skeleton className="h-28 w-full rounded-xl" />
    </div>
  );
}
