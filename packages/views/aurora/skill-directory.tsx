"use client";

import { useMemo, useState } from "react";
import { ArrowUpRight, LayoutGrid, Search, SearchX, Sparkles, Image, Film, FileSpreadsheet, Presentation, Clock3 } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import {
  InputGroup,
  InputGroupAddon,
  InputGroupInput,
} from "@multica/ui/components/ui/input-group";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import {
  Tabs,
  TabsContent,
  TabsList,
  TabsTrigger,
} from "@multica/ui/components/ui/tabs";
import { cn } from "@multica/ui/lib/utils";
import {
  isAuroraDegraded,
  useAuroraSkills,
  type AuroraSkill,
} from "@multica/core/aurora";
import { AuroraAvatar } from "./aurora-avatar";
import { AuroraPageState } from "./aurora-page-header";
import { useLocale, useT } from "../i18n";
import { GenerationComposer } from "./generation-composer";
import { formatCredits } from "./format";
import { auroraCategoryLabel, auroraInputLabel, skillDisplayName } from "./labels";
import { AuroraLoadFailed } from "./load-failed";
import { AURORA_CATEGORY_ALL, filterAuroraSkills } from "./skill-filter";
import { orderTools, RECOMMENDED_TOOL_IDS, toolGuidance } from "./tool-guidance";

const RECOMMENDED = "recommended";
const PLANNED = [
  { key: "poster", icon: Image, ids: ["poster"] },
  { key: "text_image", icon: Image, ids: ["text-image", "text-to-image"] },
  { key: "image_video", icon: Film, ids: ["image-video", "image-to-video"] },
  { key: "text_video", icon: Film, ids: ["text-video", "text-to-video"] },
  { key: "product_image", icon: Image, ids: ["product-image"] },
  { key: "image_edit", icon: Image, ids: ["image-edit"] },
  { key: "ppt", icon: Presentation, ids: ["ppt"] },
  { key: "excel", icon: FileSpreadsheet, ids: ["excel"] },
] as const;

/**
 * The skill directory: the whole catalog, filtered, and the drawer that runs
 * one entry.
 *
 * The directory owns which skill the drawer is showing. Putting that in the URL
 * would make a half-written prompt survive a refresh as a linkable address,
 * which is not what a consumer picking a skill out of a grid expects — so it
 * stays component state, and picking a skill is a click rather than a
 * navigation. `worksHref`, `topUpHref` and `issueHref` are forwarded to the
 * drawer so the app keeps owning the library, checkout and issue routes.
 */
export interface SkillDirectoryProps {
  /** The app's library route, offered on a finished generation. */
  worksHref?: string;
  /** The app's checkout route, offered when a skill costs more than the wallet holds. */
  topUpHref?: string;
  /** The app's route for the issue a generation runs on, forwarded to the drawer. */
  issueHref?: (identifier: string) => string | null;
}

// The grid's own geometry, shared by the cards and the skeleton so the page
// does not jump when the catalog resolves.
const GRID_CLASS = "aurora-card-grid";

// A stable reference for "the catalog has not loaded yet". `data ?? []` would
// hand the memos below a fresh array on every render while the query is
// pending, which defeats their dependency check and rebuilds the category tabs
// each time. Same reasoning as the EMPTY_* constants in
// `dashboard/components/dashboard-page.tsx`.
const EMPTY_SKILLS: AuroraSkill[] = [];

export function SkillDirectory({
  worksHref,
  topUpHref,
  issueHref,
}: SkillDirectoryProps = {}) {
  const { t } = useT("aurora");
  const locale = useLocale();
  const skillsQuery = useAuroraSkills();
  const [query, setQuery] = useState("");
  const [category, setCategory] = useState<string>(RECOMMENDED);
  const [selected, setSelected] = useState<AuroraSkill | null>(null);

  const skills = skillsQuery.data?.value ?? EMPTY_SKILLS;

  // One tab per category the catalog actually uses, in catalog order, rather
  // than a fixed list: a category a newer server adds gets a tab without a
  // client change, and one it drops stops being offered.
  const categories = useMemo(
    () => Array.from(new Set(skills.map((skill) => skill.category))),
    [skills],
  );

  const visible = useMemo(
    () => {
      const matching = filterAuroraSkills(skills, query, category === RECOMMENDED ? AURORA_CATEGORY_ALL : category);
      // Search always covers the catalog when the popular tab is selected.
      const recommended = matching.filter((skill) => RECOMMENDED_TOOL_IDS.includes(skill.id));
      return orderTools(category === RECOMMENDED && !query.trim() && recommended.length > 0 ? recommended : matching);
    },
    [skills, query, category],
  );

  const hasCatalog = skills.length > 0;
  // A body the schema rejected is not an empty catalog. It parses to `[]` and
  // resolves the query, so `isError` alone would render the empty state — "the
  // directory is empty" is a claim the client never read. Both outcomes take
  // the failure branch; only the one that left rows behind keeps them.
  const catalogUnreadable =
    skillsQuery.isError || isAuroraDegraded(skillsQuery.data);
  // Do not turn a pending or rejected catalog into a claim of zero skills.
  const catalogKnown =
    !skillsQuery.isPending &&
    (hasCatalog || (!catalogUnreadable && !!skillsQuery.data));

  return (
    <div className="aurora-page">
      <DirectoryHero skills={catalogKnown ? skills : undefined} />
      <ol className="aurora-quick-steps" aria-label={t(($) => $.directory.guide_label)}>
        <li><span>1</span>{t(($) => $.directory.step_choose)}</li>
        <li><span>2</span>{t(($) => $.directory.step_describe)}</li>
        <li><span>3</span>{t(($) => $.directory.step_download)}</li>
      </ol>

      <div className="aurora-section-head">
        <div>
          <p className="aurora-eyebrow">
            <LayoutGrid aria-hidden="true" className="size-3.5" />
            {t(($) => $.directory.eyebrow)}
          </p>
          <h2>{t(($) => $.directory.title)}</h2>
        </div>
        {/* The reference counts what the filter left, not the catalog: the line
            has to change when the reader narrows the grid, or it is noise. */}
        {catalogKnown ? (
          <span>
            {t(($) => $.directory.visible_count, { count: visible.length })}
          </span>
        ) : null}
      </div>

      <Tabs value={category} onValueChange={setCategory} className="gap-0">
        <div className="aurora-toolbar">
          <TabsList className="aurora-pill-tabs">
            <TabsTrigger value={RECOMMENDED} className="aurora-pill-tab">{t(($) => $.directory.recommended)}</TabsTrigger>
            <TabsTrigger value={AURORA_CATEGORY_ALL} className="aurora-pill-tab">
              {t(($) => $.directory.categories.all)}
            </TabsTrigger>
            {categories.map((value) => (
              <TabsTrigger key={value} value={value} className="aurora-pill-tab">
                {t(($) => $.directory.categories[auroraCategoryLabel(value)])}
              </TabsTrigger>
            ))}
          </TabsList>
          {/* The reference puts this box in the top bar, where it looks global
              but only ever filtered this grid. It stays beside the tabs it
              belongs to; the box itself is the same one — icon, white field,
              hairline border, violet border on focus. */}
          <InputGroup className="w-full max-w-[280px] bg-card">
            <InputGroupAddon>
              <Search aria-hidden="true" />
            </InputGroupAddon>
            <InputGroupInput
              type="search"
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder={t(($) => $.directory.search_placeholder)}
              aria-label={t(($) => $.directory.search_placeholder)}
            />
          </InputGroup>
        </div>
        <TabsContent value={category}>
          {skillsQuery.isPending ? (
            <DirectorySkeleton />
          ) : catalogUnreadable && !hasCatalog ? (
            <AuroraLoadFailed
              title={t(($) => $.directory.load_failed_title)}
              onRetry={() => void skillsQuery.refetch()}
            />
          ) : visible.length === 0 ? (
            // The reference gives the empty grid a way out instead of only an
            // apology, and it is the same recovery: clear what was typed and
            // what was selected.
            <AuroraPageState
              icon={SearchX}
              title={t(($) => $.directory.empty_title)}
              description={t(($) => $.directory.empty_description)}
              actions={
                <Button
                  type="button"
                  variant="outline"
                  onClick={() => {
                    setQuery("");
                    setCategory(AURORA_CATEGORY_ALL);
                  }}
                >
                  {t(($) => $.directory.clear_filters)}
                </Button>
              }
            />
          ) : (
            <ul className={GRID_CLASS}>
              {visible.map((skill) => (
                <li key={skill.id}>
                  <SkillCard
                    skill={skill}
                    displayName={skillDisplayName(skill, locale)}
                    summary={toolGuidance(skill.id, locale)?.summary}
                    credits={formatCredits(skill.credits, locale)}
                    onSelect={() => setSelected(skill)}
                  />
                </li>
              ))}
            </ul>
          )}
        </TabsContent>
      </Tabs>
      <section className="aurora-upcoming" aria-labelledby="aurora-upcoming-title">
        <div className="aurora-section-head">
          <div><p className="aurora-eyebrow"><Clock3 aria-hidden="true" className="size-3.5" />{t(($) => $.directory.unavailable)}</p>
          <h2 id="aurora-upcoming-title">{t(($) => $.directory.upcoming_title)}</h2></div>
        </div>
        <p className="aurora-section-note">{t(($) => $.directory.upcoming_description)}</p>
        <div className="aurora-planned-grid">
          {PLANNED.filter((item) => !skills.some((skill) => item.ids.some((id) => id === skill.id))).map(({ key, icon: Icon }) => (
            <div key={key} className="aurora-planned-card">
              <Icon aria-hidden="true" className="size-5" />
              <div><strong>{t(($) => $.directory.planned[key].name)}</strong><span>{t(($) => $.directory.planned[key].output)}</span></div>
              <span className="aurora-status-pill aurora-status-pill--muted">{t(($) => $.directory.unavailable)}</span>
            </div>
          ))}
        </div>
      </section>
      <GenerationComposer
        skill={selected}
        open={selected !== null}
        onOpenChange={(open) => {
          if (!open) setSelected(null);
        }}
        worksHref={worksHref}
        topUpHref={topUpHref}
        issueHref={issueHref}
      />
    </div>
  );
}

function DirectoryHero({ skills }: { skills: AuroraSkill[] | undefined }) {
  const { t } = useT("aurora");
  const locale = useLocale();
  const runnable = (skills ?? EMPTY_SKILLS).filter((skill) => skill.available);
  return (
    <section className="aurora-hero">
      <div>
        <span className="aurora-hero-label">{t(($) => $.directory.hero_tag)}</span>
        <h1>{t(($) => $.directory.hero_title)}</h1>
        <p>{t(($) => $.directory.hero_subtitle)}</p>
      </div>
      {/* Decorative: the four runnable skills as tiles. It carries no fact the
          stats above do not, which is why it is hidden from assistive tech. */}
      <div className="aurora-hero-orbit" aria-hidden="true">
        <div className="aurora-hero-center">
          <Sparkles className="size-6" />
        </div>
        {runnable.slice(0, 4).map((skill, index) => (
          <span
            key={skill.id}
            className={
              "aurora-orbit-avatar aurora-orbit-avatar--" + (index + 1)
            }
          >
            <AuroraAvatar
              name={skillDisplayName(skill, locale)}
              id={skill.id}
              avatarUrl={skill.avatarUrl}
              available
            />
          </span>
        ))}
      </div>
    </section>
  );
}

/**
 * One catalog entry.
 *
 * An unavailable skill is still a live button. It is not a disabled action —
 * the action is "open this skill", and opening it is the only way to read why
 * it cannot run yet; the badge and the muted fill carry the state instead. The
 * price is shown next to it either way, because a roadmap entry is worth
 * pricing.
 */
function SkillCard({
  skill,
  displayName,
  summary,
  credits,
  onSelect,
}: {
  skill: AuroraSkill;
  displayName: string;
  summary?: string;
  credits: string;
  onSelect: () => void;
}) {
  const { t } = useT("aurora");
  return (
    // One button, not a card with a button in it: the whole tile is the target,
    // and the arrow is decoration inside it rather than a second control. The
    // grid's own test helper counts the buttons in this list.
    <button
      type="button"
      onClick={onSelect}
      className={cn(
        "aurora-card",
        skill.featured && "aurora-card--featured",
        !skill.available && "aurora-card--unavailable",
      )}
    >
      <span className="aurora-card-top">
        <AuroraAvatar
          name={displayName}
          id={skill.id}
          avatarUrl={skill.avatarUrl}
          size="lg"
          available={skill.available}
        />
        <span className="aurora-card-name">
          <span className="aurora-card-category">
            {t(($) => $.directory.categories[auroraCategoryLabel(skill.category)])}
          </span>
          <span className="aurora-card-title">{displayName}</span>
        </span>
        <span aria-hidden="true" className="aurora-card-arrow">
          <ArrowUpRight className="size-4" />
        </span>
      </span>
      {summary ? <span className="aurora-card-description">{summary}</span> : null}
      {skill.output.length === 1 && skill.output[0] === "text" ? (
        <span className="aurora-delivery-note">{t(($) => $.directory.text_output)}</span>
      ) : null}
      {/* What the skill accepts, from the catalog's own modalities. The
          reference shows three capability tags here. */}
      <span className="aurora-chips">
        {skill.input.map((modality) => (
          <span key={modality}>
            {t(($) => $.directory.inputs[auroraInputLabel(modality)])}
          </span>
        ))}
      </span>
      <span className="aurora-card-footer">
        {skill.available ? (
          <span className="aurora-card-state">
            <span aria-hidden="true" className="aurora-dot" />
            {t(($) => $.directory.ready)}
          </span>
        ) : (
          <span className="aurora-status-pill aurora-status-pill--muted">
            {t(($) => $.directory.unavailable)}
          </span>
        )}
        <strong>{t(($) => $.credits, { credits })}</strong>
      </span>
    </button>
  );
}

/** Placeholder cards at the grid's own geometry, so the page does not jump. */
function DirectorySkeleton() {
  return (
    <ul className={GRID_CLASS}>
      {Array.from({ length: 8 }, (_, index) => (
        <li key={index}>
          <Skeleton className="h-20 w-full rounded-xl" />
        </li>
      ))}
    </ul>
  );
}
