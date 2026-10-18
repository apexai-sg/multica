"use client";

import { useCallback, useEffect, useState } from "react";
import { CircleAlert, Gauge } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import {
  isAuroraDegraded,
  isAuroraGenerationTerminal,
  useAuroraGenerationDetail,
  useAuroraGenerations,
  useAuroraRuntime,
  type AuroraExecutionTarget,
  type AuroraGeneration,
  type AuroraRuntimeState,
} from "@multica/core/aurora";
import { AuroraPageHeader } from "./aurora-page-header";
import { useT } from "../i18n";
import { AppLink } from "../navigation";
import { GenerationStatusBadge } from "./generation-artifacts";

/**
 * The workspace's execution view.
 *
 * It surfaces the managed sandbox node the Fleet runs this workspace's
 * generations on, plus the generations currently in flight. The node is read
 * from `GET /api/aurora/runtime`; the in-flight rows reuse the existing 3s
 * detail poll rather than a second status protocol, so nothing here opens a
 * realtime channel.
 *
 * Dangerous lifecycle actions stay in the runtime management surface; this
 * screen only reports and guides.
 */

/**
 * The runtime state vocabulary, in one place: the dot class, the pill modifier
 * and the tonal text class. Exported because two surfaces name the same five
 * states — this page and the topbar chip — and a second mapping would be a
 * second thing to keep in step.
 */
export const AURORA_RUNTIME_VISUAL: Record<
  AuroraRuntimeState,
  { dot: string; pill: string; text: string }
> = {
  online: {
    dot: "aurora-dot",
    pill: "aurora-mini-label",
    text: "text-success",
  },
  offline: {
    dot: "aurora-dot aurora-dot--warning",
    pill: "aurora-mini-label aurora-mini-label--warning",
    text: "text-warning",
  },
  provisioning: {
    dot: "aurora-dot aurora-dot--running",
    pill: "aurora-mini-label aurora-mini-label--running",
    text: "text-info",
  },
  failed: {
    dot: "aurora-dot aurora-dot--error",
    pill: "aurora-mini-label aurora-mini-label--error",
    text: "text-destructive",
  },
  unconfigured: {
    dot: "aurora-dot aurora-dot--muted",
    pill: "aurora-mini-label aurora-mini-label--muted",
    text: "text-muted-foreground",
  },
};

export function RuntimeStatus() {
  const { t } = useT("aurora");
  const runtime = useAuroraRuntime();
  const generations = useAuroraGenerations();
  // Ids whose own detail read has reported a terminal status. The list read is
  // a one-shot snapshot — it has no interval and the client disables
  // refetch-on-focus — so its stored status can stay "queued" long after the
  // generation settles. The row's polled detail is the live source, and it is
  // what removes the row rather than only flipping its badge.
  const [settled, setSettled] = useState<ReadonlySet<string>>(() => new Set());

  const target = runtime.data?.value;
  // A row leaves the running list when either read says it is terminal: the
  // list already filters the statuses it knows, and a row whose detail settles
  // is dropped even before the list catches up.
  const active = (generations.data?.value ?? []).filter(
    (generation) =>
      !isAuroraGenerationTerminal(generation.status) &&
      !settled.has(generation.id),
  );

  const markSettled = useCallback((id: string) => {
    setSettled((previous) => {
      if (previous.has(id)) return previous;
      const next = new Set(previous);
      next.add(id);
      return next;
    });
  }, []);

  return (
    <div className="aurora-page">
      <AuroraPageHeader
        icon={Gauge}
        title={t(($) => $.runtime.title)}
      />

      <div className="flex flex-col gap-6">
        {runtime.isPending ? (
          <Skeleton className="h-16 w-full rounded-xl" />
        ) : (
          <RuntimeNodeCard
            target={target}
            onRetry={() => void runtime.refetch()}
            retrying={runtime.isFetching}
          />
        )}

        <section className="flex flex-col gap-3">
          <div className="aurora-section-head">
            <h2>{t(($) => $.runtime.generations_title)}</h2>
          </div>
          {active.length === 0 ? (
            <p className="text-body text-muted-foreground">
              {t(($) => $.runtime.generations_empty)}
            </p>
          ) : (
            <ul className="aurora-list">
              {active.map((generation) => (
                <ActiveGenerationRow
                  key={generation.id}
                  generation={generation}
                  onSettled={markSettled}
                />
              ))}
            </ul>
          )}
        </section>
      </div>
    </div>
  );
}

/**
 * The node's state, with recovery guidance where the state is actionable.
 *
 * `target` is undefined only in the moment between the query erroring and the
 * screen re-rendering; the parser already turns a malformed body into the
 * `unconfigured` projection, so the fallback here says the same thing.
 */
function RuntimeNodeCard({
  target,
  onRetry,
  retrying,
}: {
  target: AuroraExecutionTarget | undefined;
  onRetry: () => void;
  retrying: boolean;
}) {
  const { t } = useT("aurora");
  const state = target?.state ?? "unconfigured";
  const visual = AURORA_RUNTIME_VISUAL[state];
  // Recovery only re-reads the projection; it never provisions or restarts a node.
  const recovery =
    state === "failed" || state === "unconfigured" || state === "offline";
  const code = target?.node?.errorCode;
  // A workspace with no node yet is provisioned by starting a generation, so the
  // node-less unconfigured state keeps provisioning guidance and offers no Retry:
  // a refetch cannot create a node. A server-reported reason always wins.
  const awaitingFirstGeneration = state === "unconfigured" && target?.node == null;
  const reason =
    code ??
    (awaitingFirstGeneration ? "runtime_not_provisioned" : "runtime_offline");

  return (
    <section className="aurora-panel flex flex-col gap-2">
      <div className="flex items-center gap-2">
        {state === "failed" ? (
          <CircleAlert
            aria-hidden="true"
            className={`size-3.5 shrink-0 ${visual.text}`}
          />
        ) : (
          <span
            aria-hidden="true"
            className={`size-2 shrink-0 rounded-full ${visual.dot}`}
          />
        )}
        <span className={`text-body font-medium ${visual.text}`}>
          {t(($) => $.runtime.status[state])}
        </span>
      </div>
      {recovery ? (
        <p role="alert" className="text-caption text-warning">
          {t(($) => $.runtime.reasons[reason])}
        </p>
      ) : null}
      {recovery && !awaitingFirstGeneration ? (
        <Button
          variant="outline"
          size="sm"
          className="self-start"
          onClick={onRetry}
          disabled={retrying}
          aria-busy={retrying}
        >
          {t(($) => $.runtime.retry)}
        </Button>
      ) : null}
    </section>
  );
}

/**
 * One generation that has not settled yet.
 *
 * Its own detail query is the polling source: it reuses the existing 3s
 * cadence and stops itself once the server reports a terminal status, so the
 * row is live without a second status channel. Reporting that terminal status
 * up is also what removes the row — the list snapshot cannot be trusted to
 * catch up on its own.
 */
function ActiveGenerationRow({
  generation,
  onSettled,
}: {
  generation: AuroraGeneration;
  onSettled: (id: string) => void;
}) {
  const { t } = useT("aurora");
  const detail = useAuroraGenerationDetail(generation.id);
  const current = detail.data?.value ?? generation;

  useEffect(() => {
    if (isAuroraGenerationTerminal(current.status)) {
      onSettled(generation.id);
    }
  }, [current.status, generation.id, onSettled]);

  return (
    <li className="aurora-row">
      <span className="aurora-row-main truncate">
        {current.prompt || t(($) => $.works.no_prompt)}
      </span>
      <GenerationStatusBadge status={current.status} className="shrink-0" />
    </li>
  );
}

/** The runtime state, as a link to the page that explains it. Renders nothing
 *  until the read resolves: no chip beats a wrong one. */
export function AuroraRuntimeChip({ href }: { href: string }) {
  const { t } = useT("aurora");
  const runtime = useAuroraRuntime();
  const target = runtime.data?.value;
  if (
    !target || runtime.isPending || runtime.isError ||
    isAuroraDegraded(runtime.data)
  ) return null;
  return (
    <AppLink href={href} className="aurora-status-chip">
      <span
        aria-hidden="true"
        className={AURORA_RUNTIME_VISUAL[target.state].dot}
      />
      {t(($) => $.runtime.status[target.state])}
    </AppLink>
  );
}
