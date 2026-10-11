"use client";

import { useMemo, useState } from "react";
import { CircleAlert, History } from "lucide-react";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import {
  Tabs,
  TabsContent,
  TabsList,
  TabsTrigger,
} from "@multica/ui/components/ui/tabs";
import {
  isAuroraDegraded,
  useAuroraGenerationDetail,
  useAuroraGenerations,
  useAuroraSkills,
  type AuroraAsset,
  type AuroraGeneration,
  type AuroraSkill,
} from "@multica/core/aurora";
import { AuroraPageHeader, AuroraPageState } from "./aurora-page-header";
import { AppLink } from "../navigation";
import { useLocale, useT, useTimeAgo } from "../i18n";
import { formatMicroCredits } from "./format";
import {
  GenerationArtifacts,
  GenerationStatusBadge,
  generationCredits,
} from "./generation-artifacts";
import {
  generationStatusLabel,
  skillDisplayName,
  skillDisplayNamesById,
} from "./labels";
import { AuroraLoadFailed } from "./load-failed";
import {
  AURORA_HISTORY_SKILL_ALL,
  AURORA_HISTORY_STATUS_ALL,
  filterAuroraHistory,
  type AuroraHistoryStatusFilter,
} from "./history-filter";

// Stable references for "not loaded yet", so the derived values below do not
// see a fresh array on every render. Same reasoning as SkillDirectory.
const EMPTY_GENERATIONS: AuroraGeneration[] = [];
const EMPTY_SKILLS: AuroraSkill[] = [];
const EMPTY_ASSETS: AuroraAsset[] = [];

/**
 * Aurora's generation history: past generations and the results they produced.
 *
 * The library served the files but not the request that made them, and the
 * composer's Result area only ever showed the one generation it started. This
 * screen pairs each generation summary with its own assets, so a finished
 * result is reachable long after the drawer that started it closed.
 *
 * The list and each row's detail are two reads because that is what the API
 * offers: the list is a page of summaries, and only the detail endpoint returns
 * a generation together with its assets. The detail is preferred for the status
 * too — the list's stored value lags the task-derived one — so a generation
 * that has just settled renders as finished here.
 */
export interface HistoryListProps {
  /**
   * The app's route for one generation. Omitted, a row's prompt is plain text
   * rather than a link, which is what the tests and any app without the route
   * need.
   */
  generationHref?: (generationId: string) => string;
}

export function HistoryList({ generationHref }: HistoryListProps = {}) {
  const { t } = useT("aurora");
  const locale = useLocale();
  const generationsQuery = useAuroraGenerations();
  const skillsQuery = useAuroraSkills();
  const [status, setStatus] = useState<AuroraHistoryStatusFilter>(
    AURORA_HISTORY_STATUS_ALL,
  );
  const [skillId, setSkillId] = useState<string>(AURORA_HISTORY_SKILL_ALL);

  const generations = generationsQuery.data?.value ?? EMPTY_GENERATIONS;
  const skills = skillsQuery.data?.value ?? EMPTY_SKILLS;

  const skillNames = useMemo(
    () => skillDisplayNamesById(skills, locale),
    [skills, locale],
  );

  // A failed or degraded catalog is not "the catalog dropped this entry":
  // without the distinction a failed request stamps "Unknown skill" on every
  // row, which is a claim the client never read. WorksList resolves names the
  // same way.
  const catalogLoaded =
    skillsQuery.data !== undefined && !isAuroraDegraded(skillsQuery.data);

  const visible = useMemo(
    () => filterAuroraHistory(generations, status, skillId),
    [generations, status, skillId],
  );

  const hasGenerations = generations.length > 0;
  // A read the schema rejected is a load failure even though it resolved: it
  // left an empty list behind, and "nothing here" is a claim the client never
  // read. A failed refetch that kept rows already in cache is a stale list,
  // not an error screen.
  const loadFailed =
    generationsQuery.isError || isAuroraDegraded(generationsQuery.data);

  return (
    <div className="aurora-page">
      <AuroraPageHeader
        icon={History}
        title={t(($) => $.history.title)}
        meta={<span>{generations.length}</span>}
      />

      <Tabs
        value={status}
        onValueChange={(next) => setStatus(next as AuroraHistoryStatusFilter)}
        className="gap-0"
      >
        <div className="aurora-toolbar">
          <TabsList className="aurora-pill-tabs">
            <TabsTrigger
              className="aurora-pill-tab"
              value={AURORA_HISTORY_STATUS_ALL}
            >
              {t(($) => $.history.status_all)}
            </TabsTrigger>
            <TabsTrigger className="aurora-pill-tab" value="completed">
              {t(($) => $.composer.status.completed)}
            </TabsTrigger>
            <TabsTrigger className="aurora-pill-tab" value="failed">
              {t(($) => $.composer.status.failed)}
            </TabsTrigger>
          </TabsList>
          {skills.length > 0 ? (
            <Select
              items={[
                {
                  value: AURORA_HISTORY_SKILL_ALL,
                  label: t(($) => $.history.skill_all),
                },
                ...skills.map((skill) => ({
                  value: skill.id,
                  label: skillDisplayName(skill, locale),
                })),
              ]}
              value={skillId}
              onValueChange={(next) =>
                setSkillId(next ?? AURORA_HISTORY_SKILL_ALL)
              }
            >
              <SelectTrigger
                className="min-h-10 max-w-full"
                aria-label={t(($) => $.history.skill_label)}
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent align="end">
                <SelectItem value={AURORA_HISTORY_SKILL_ALL}>
                  {t(($) => $.history.skill_all)}
                </SelectItem>
                {skills.map((skill) => (
                  <SelectItem key={skill.id} value={skill.id}>
                    {skillDisplayName(skill, locale)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          ) : null}
        </div>

        <TabsContent value={status}>
          {generationsQuery.isPending ? (
            <HistorySkeleton />
          ) : loadFailed && !hasGenerations ? (
            <AuroraLoadFailed
              title={t(($) => $.history.load_failed_title)}
              onRetry={() => void generationsQuery.refetch()}
            />
          ) : !hasGenerations ? (
            <AuroraPageState
              icon={History}
              title={t(($) => $.history.empty_title)}
              description={t(($) => $.history.empty_description)}
            />
          ) : visible.length === 0 ? (
            <AuroraPageState
              icon={History}
              title={t(($) => $.history.no_match_title)}
              description={t(($) => $.history.no_match_description)}
            />
          ) : (
            <ul className="aurora-list">
              {visible.map((generation) => (
                <HistoryRow
                  key={generation.id}
                  generation={generation}
                  skillName={
                    skillNames.get(generation.skillId) ??
                    (catalogLoaded ? t(($) => $.works.skill_unknown) : null)
                  }
                  generationHref={generationHref}
                />
              ))}
            </ul>
          )}
        </TabsContent>
      </Tabs>
    </div>
  );
}

/**
 * One generation: what was asked, how it ended, and the files it produced.
 *
 * The row owns its own detail read. A generation's assets only exist on the
 * detail endpoint, and fetching them from the workspace-wide asset list would
 * hit that list's page limit — silently hiding the results of older rows,
 * which is the exact thing this screen exists to fix.
 */
function HistoryRow({
  generation,
  skillName,
  generationHref,
}: {
  generation: AuroraGeneration;
  /** Null when the catalog could not name the skill — nothing to say, not a fact. */
  skillName: string | null;
  generationHref?: (generationId: string) => string;
}) {
  const { t } = useT("aurora");
  const locale = useLocale();
  const timeAgo = useTimeAgo();
  const detailQuery = useAuroraGenerationDetail(generation.id);

  // Prefer the detail's live status and settlement fields; keep the list's
  // summary until it arrives so the row never renders blank. A detail body the
  // schema rejected resolves to a null value, which falls back the same way.
  const current = detailQuery.data?.value ?? generation;
  const status = generationStatusLabel(current.status);
  const credits = generationCredits(current);
  const assets = detailQuery.data?.value?.assets ?? EMPTY_ASSETS;
  const prompt = current.prompt || t(($) => $.works.no_prompt);

  return (
    <li className="aurora-row-stack">
      <div className="flex items-start gap-3">
        <div className="aurora-row-main flex flex-col">
          <span className="truncate text-body">
            {generationHref ? (
              <AppLink
                href={generationHref(current.id)}
                className="transition-colors hover:text-foreground hover:underline"
              >
                {prompt}
              </AppLink>
            ) : (
              prompt
            )}
          </span>
          <span className="flex min-w-0 items-center gap-2 text-caption text-muted-foreground">
            {skillName ? <span className="truncate">{skillName}</span> : null}
            {current.createdAt ? (
              <time dateTime={current.createdAt} className="shrink-0">
                {timeAgo(current.createdAt)}
              </time>
            ) : null}
          </span>
        </div>
        {credits !== null ? (
          <span className="aurora-row-amount">
            {t(($) => $.credits, {
              credits: formatMicroCredits(credits, locale),
            })}
          </span>
        ) : null}
        <GenerationStatusBadge status={current.status} className="shrink-0" />
      </div>

      {status === "failed" && current.error ? (
        <p className="flex items-start gap-1.5 text-caption text-destructive">
          <CircleAlert aria-hidden="true" className="mt-0.5 size-3.5 shrink-0" />
          <span className="min-w-0 break-words">{current.error}</span>
        </p>
      ) : null}

      <GenerationArtifacts
        generation={current}
        assets={assets}
        loading={detailQuery.isPending}
      />
    </li>
  );
}

/** Placeholder rows at the list's own geometry, so the page does not jump. */
function HistorySkeleton() {
  return (
    <div className="flex flex-col gap-2">
      {Array.from({ length: 3 }, (_, index) => (
        <Skeleton key={index} className="h-24 w-full rounded-xl" />
      ))}
    </div>
  );
}
