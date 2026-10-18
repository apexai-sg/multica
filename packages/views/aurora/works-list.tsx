"use client";

import { useMemo, useState } from "react";
import { CircleAlert, Download, Library, Sparkles } from "lucide-react";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@multica/ui/components/ui/alert-dialog";
import { Alert, AlertDescription } from "@multica/ui/components/ui/alert";
import { Button } from "@multica/ui/components/ui/button";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { Spinner } from "@multica/ui/components/ui/spinner";
import {
  isAuroraDegraded,
  useAuroraAssets,
  useAuroraGenerations,
  useAuroraSkills,
  useDeleteAuroraAsset,
  type AuroraAsset,
  type AuroraGeneration,
} from "@multica/core/aurora";
import { AuroraPageHeader, AuroraPageState } from "./aurora-page-header";
import { avatarInitial, tintIndex } from "./aurora-avatar-helpers";
import { AppLink } from "../navigation";
import { useLocale, useT } from "../i18n";
import { formatMicroCredits } from "./format";
import {
  GenerationStatusBadge,
  isPreviewableImage,
} from "./generation-artifacts";
import { generationStatusLabel, skillDisplayNamesById } from "./labels";
import { AuroraLoadFailed } from "./load-failed";
import { useAuroraAgentNames } from "./use-aurora-agent-names";
import { useAuroraAssetDownload } from "./use-aurora-asset-download";

/**
 * The library: what has been generated, and the files those generations
 * produced.
 *
 * The two lists come from their own endpoints rather than one nesting the
 * other — a generation is worth watching while it runs, and its assets only
 * exist once it finishes — so they are rendered side by side instead of being
 * reconciled into a single tree the server does not offer.
 */
export interface WorksListProps {
  /**
   * The app's route for one generation. Omitted, a row's prompt is plain text
   * rather than a link.
   */
  generationHref?: (generationId: string) => string;
  /**
   * The app's route for the issue a generation runs on. Takes the
   * `issueIdentifier` and returns null when it cannot build a URL — what core's
   * `auroraIssueHref` does for a missing or degraded identifier — in which case
   * the row keeps `generationHref`.
   */
  issueHref?: (identifier: string) => string | null;
}

export function WorksList({ generationHref, issueHref }: WorksListProps = {}) {
  const { t } = useT("aurora");
  const locale = useLocale();
  const generationsQuery = useAuroraGenerations();
  const assetsQuery = useAuroraAssets();
  const skillsQuery = useAuroraSkills();
  const deleteAsset = useDeleteAuroraAsset();
  const [pendingDelete, setPendingDelete] = useState<AuroraAsset | null>(null);
  const [deleteFailed, setDeleteFailed] = useState(false);

  const generations = generationsQuery.data?.value ?? [];
  const assets = assetsQuery.data?.value ?? [];
  const skills = skillsQuery.data?.value;
  const agentNames = useAuroraAgentNames();

  // The agent that presented the skill names the row when it exists, because
  // that is the card the user picked. The catalog's display name is the
  // fallback, and it is the same query the directory reads, so it is already
  // in the cache when the user arrives here.
  const skillNames = useMemo(() => {
    const names = skillDisplayNamesById(skills ?? [], locale);
    for (const [skillId, agentName] of agentNames) names.set(skillId, agentName);
    return names;
  }, [skills, locale, agentNames]);

  // Whether an unresolved `skillId` means "the catalog dropped this entry" or
  // "the catalog never loaded". Without the distinction a failed catalog request
  // stamped "Unknown skill" on every row — a screenful of assertions the client
  // had no basis for. A degraded catalog is the same case: it parses to an empty
  // list, which resolves the query without ever having read the catalog. Billing
  // resolves names the same way and stays silent instead, which is what a row
  // with nothing to say should do.
  const catalogLoaded =
    skills !== undefined && !isAuroraDegraded(skillsQuery.data);

  async function confirmDelete() {
    const asset = pendingDelete;
    if (!asset) return;
    setDeleteFailed(false);
    try {
      await deleteAsset.mutateAsync(asset.id);
    } catch {
      // The row stays: the server also drops the stored object, so an
      // optimistic removal would leave the list claiming a file is gone when
      // the delete never happened.
      setDeleteFailed(true);
    }
    // Closed on both paths. `AlertDialogAction` is a plain Button here, not a
    // close primitive, so leaving it open on failure parked the user in front
    // of a modal — which is also what hid the failure: the report renders
    // behind the overlay, where it is both covered and `aria-hidden`, so a
    // sighted user saw a dialog that did nothing and a screen-reader user
    // heard nothing at all.
    setPendingDelete(null);
  }

  const isLoading = generationsQuery.isPending || assetsQuery.isPending;
  // A read the schema rejected is a load failure even though the query
  // resolved: it left `[]` behind, and "nothing here" is a claim the client
  // never read. A *failed* read is only fatal when it left nothing behind — a
  // background refetch that dropped over rows already in cache is a stale list,
  // and replacing a readable one with an error card is the worse trade.
  const loadFailed =
    ((generationsQuery.isError || isAuroraDegraded(generationsQuery.data)) &&
      generations.length === 0) ||
    ((assetsQuery.isError || isAuroraDegraded(assetsQuery.data)) &&
      assets.length === 0);

  return (
    <div className="aurora-page">
      <AuroraPageHeader
        icon={Library}
        eyebrow={t(($) => $.works.eyebrow)}
        title={t(($) => $.works.title)}
      />

      {isLoading ? (
        <WorksSkeleton />
      ) : loadFailed ? (
        <AuroraLoadFailed
          title={t(($) => $.works.load_failed_title)}
          onRetry={() => {
            void generationsQuery.refetch();
            void assetsQuery.refetch();
          }}
        />
      ) : (
        <div className="flex flex-col">
          {deleteFailed ? (
            <Alert variant="destructive">
              <CircleAlert aria-hidden="true" />
              <AlertDescription>
                {t(($) => $.works.delete_failed)}
              </AlertDescription>
            </Alert>
          ) : null}

          <section className="flex flex-col gap-3">
            <div className="aurora-section-head">
              <h2>{t(($) => $.works.generations_title)}</h2>
            </div>
            {generations.length === 0 ? (
              <AuroraPageState
                icon={Library}
                title={t(($) => $.works.generations_empty_title)}
                description={t(($) => $.works.generations_empty_description)}
              />
            ) : (
              <ul className="aurora-list">
                {generations.map((generation) => (
                  <GenerationRow
                    key={generation.id}
                    generation={generation}
                    skillName={
                      skillNames.get(generation.skillId) ??
                      (catalogLoaded
                        ? t(($) => $.works.skill_unknown)
                        : null)
                    }
                    credits={
                      // A failed generation is refunded in full, so the
                      // amount it reserved is not a cost and printing it as
                      // one claims a spend the client never read. Null is
                      // "nothing was charged", not "we do not know".
                      generationStatusLabel(generation.status) === "failed"
                        ? null
                        : formatMicroCredits(
                            generation.creditsReserved,
                            locale,
                          )
                    }
                    noPrompt={t(($) => $.works.no_prompt)}
                    generationHref={generationHref}
                    issueHref={issueHref}
                  />
                ))}
              </ul>
            )}
          </section>

          <section className="flex flex-col gap-3">
            <div className="aurora-section-head">
              <h2>{t(($) => $.works.assets_title)}</h2>
            </div>
            {assets.length === 0 ? (
              <AuroraPageState
                icon={Library}
                title={t(($) => $.works.assets_empty_title)}
                description={t(($) => $.works.assets_empty_description)}
              />
            ) : (
              <ul className="aurora-works-grid">
                {assets.map((asset) => (
                  <AssetRow
                    key={asset.id}
                    asset={asset}
                    onRequestDelete={() => setPendingDelete(asset)}
                  />
                ))}
              </ul>
            )}
          </section>
        </div>
      )}

      <AlertDialog
        open={pendingDelete !== null}
        onOpenChange={(open) => {
          if (!open) setPendingDelete(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t(($) => $.works.delete_title)}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t(($) => $.works.delete_description)}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t(($) => $.cancel)}</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={deleteAsset.isPending}
              aria-busy={deleteAsset.isPending}
              onClick={() => void confirmDelete()}
            >
              {deleteAsset.isPending ? <Spinner aria-hidden="true" /> : null}
              {t(($) => $.works.delete_confirm)}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

/** One generation: what was asked for, by which skill, and where it got to. */
function GenerationRow({
  generation,
  skillName,
  credits,
  noPrompt,
  generationHref,
  issueHref,
}: {
  generation: AuroraGeneration;
  /** Null when the catalog could not name the skill — nothing to say, not a fact. */
  skillName: string | null;
  /** Null when nothing was charged for it — a failed generation is refunded. */
  credits: string | null;
  noPrompt: string;
  generationHref?: (generationId: string) => string;
  issueHref?: (identifier: string) => string | null;
}) {
  const { t } = useT("aurora");
  const prompt = generation.prompt || noPrompt;
  // The issue is where the run's conversation lives, so it wins over the
  // generation page when the app can build its URL; an empty or degraded
  // identifier leaves the row on the generation route.
  const issueUrl =
    generation.issueIdentifier && issueHref
      ? issueHref(generation.issueIdentifier)
      : null;
  const href =
    issueUrl ?? (generationHref ? generationHref(generation.id) : null);
  return (
    <li className="aurora-row">
      <span aria-hidden="true" className="aurora-icon-tile">
        <Sparkles className="size-4" />
      </span>
      <div className="aurora-row-main flex flex-col">
        <span className="truncate text-body">
          {href ? (
            <AppLink
              href={href}
              className="transition-colors hover:text-foreground hover:underline"
            >
              {prompt}
            </AppLink>
          ) : (
            prompt
          )}
        </span>
        {skillName ? (
          <span className="truncate text-caption text-muted-foreground">
            {skillName}
          </span>
        ) : null}
      </div>
      {credits ? (
        <span className="aurora-row-amount">
          {t(($) => $.credits, { credits })}
        </span>
      ) : null}
      <GenerationStatusBadge status={generation.status} className="shrink-0" />
    </li>
  );
}

/** One stored file, downloaded through the authenticated asset helper. */
function AssetRow({
  asset,
  onRequestDelete,
}: {
  asset: AuroraAsset;
  onRequestDelete: () => void;
}) {
  const { t } = useT("aurora");
  const download = useAuroraAssetDownload();
  const title = asset.format ?? asset.kind;
  const detail = title !== asset.kind ? asset.kind : null;
  return (
    <li className="aurora-work-card min-w-0">
      <div
        aria-hidden="true"
        className={"aurora-work-thumb aurora-tint-" + tintIndex(asset.id)}
      >
        {isPreviewableImage(asset) ? (
          <img src={asset.mediaUrl} alt="" loading="lazy" />
        ) : (
          <strong>{avatarInitial(title)}</strong>
        )}
        {/* Classification is decorative; the title/detail below owns its text. */}
        <span
          className="aurora-work-tag before:content-[attr(data-kind)]"
          data-kind={asset.kind}
        />
      </div>
      <h3 className="break-words">{title}</h3>
      {detail ? <p className="break-words">{detail}</p> : null}
      <div className="aurora-work-actions flex-wrap">
        <button
          type="button"
          onClick={() => void download(asset)}
          className="aurora-file-row shrink-0"
        >
          <Download aria-hidden="true" className="size-3.5 shrink-0" />
          {t(($) => $.works.download)}
        </button>
        <Button
          type="button"
          variant="ghost"
          size="sm"
          className="shrink-0"
          onClick={onRequestDelete}
        >
          {t(($) => $.works.delete)}
        </Button>
      </div>
    </li>
  );
}

function WorksSkeleton() {
  return (
    <div className="flex flex-col gap-2">
      <Skeleton className="h-5 w-28" />
      <Skeleton className="h-24 w-full rounded-xl" />
      <Skeleton className="h-5 w-24" />
      <Skeleton className="h-24 w-full rounded-xl" />
    </div>
  );
}
