"use client";

import { useMemo, useState } from "react";
import {
  ArrowUpRight,
  CircleAlert,
  LayoutGrid,
  Search,
  SearchX,
  Sparkles,
} from "lucide-react";
import {
  Alert,
  AlertAction,
  AlertDescription,
} from "@multica/ui/components/ui/alert";
import { Button } from "@multica/ui/components/ui/button";
import {
  InputGroup,
  InputGroupAddon,
  InputGroupInput,
} from "@multica/ui/components/ui/input-group";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
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
import { AuroraLoadFailed } from "./load-failed";
import {
  agentSkillChips,
  directoryEntries,
  matchesAgentQuery,
  type DirectoryEntry,
} from "./aurora-agents";
import { useAuroraAgentDirectory } from "./use-aurora-agent-directory";

/**
 * The consumer directory: the backend agents this member can run, each routed
 * either to an Aurora generation (a current carrier) or to an ordinary issue
 * task (a user-created agent). The catalog decides both listability and which
 * mode applies, so a catalog that could not be read is reported rather than
 * mistaken for "no assistants".
 *
 * By default the directory owns the selected drawer. A host can supply
 * onSelect to open its own tool page while retaining this classification.
 */
export interface SkillDirectoryProps {
  /** Let the host open a tool page instead of the built-in drawer. */
  onSelect?: (entry: DirectoryEntry) => void;
  /** The app's library route, offered on a finished generation. */
  worksHref?: string;
  /** The app's checkout route, offered when a generation costs more than the wallet holds. */
  topUpHref?: string;
  /** The app's route for the issue a generation runs on, forwarded to the drawer. */
  issueHref?: (identifier: string) => string | null;
}

// The grid's own geometry, shared by the cards and the skeleton so the page
// does not jump while the agent list resolves.
const GRID_CLASS = "aurora-card-grid";

// A stable reference for "the catalog has not loaded yet", so the memos below
// are not handed a fresh array on every render.
const EMPTY_SKILLS: AuroraSkill[] = [];

type SelectedAgent = DirectoryEntry;
type OutputFilter =
  | "all" | "image" | "video" | "text" | "audio" | "assistant" | "other";

function matchesOutput(entry: DirectoryEntry, filter: OutputFilter) {
  if (filter === "all") return true;
  if (entry.mode === "issue") return filter === "assistant";
  if (filter === "other") {
    return !entry.skill.output.some((kind) =>
      ["image", "video", "text", "audio"].includes(kind),
    );
  }
  return entry.skill.output.includes(filter);
}

export function SkillDirectory({
  onSelect,
  worksHref,
  topUpHref,
  issueHref,
}: SkillDirectoryProps = {}) {
  const { t } = useT("aurora");
  const { agents, query: agentsQuery } = useAuroraAgentDirectory();
  const skillsQuery = useAuroraSkills();
  const skills = skillsQuery.data?.value ?? EMPTY_SKILLS;
  const [query, setQuery] = useState("");
  const [output, setOutput] = useState<OutputFilter>("all");
  const [selected, setSelected] = useState<SelectedAgent | null>(null);

  // One classification pass: generation entries first, then issue entries.
  // System-generated agents with no current catalogue entry are omitted.
  const entries = useMemo(
    () => directoryEntries(agents, skills),
    [agents, skills],
  );
  const filterOptions: { value: OutputFilter; label: string }[] = [
    { value: "all", label: t(($) => $.directory.filter_all) },
    { value: "image", label: t(($) => $.directory.filter_image) },
    { value: "video", label: t(($) => $.directory.filter_video) },
    { value: "text", label: t(($) => $.directory.filter_text) },
    { value: "audio", label: t(($) => $.directory.filter_audio) },
    { value: "assistant", label: t(($) => $.directory.filter_assistant) },
    { value: "other", label: t(($) => $.directory.filter_misc) },
  ];
  const filters = filterOptions.filter(
    (filter) => filter.value === "all" ||
      entries.some((entry) => matchesOutput(entry, filter.value)),
  );
  // A refreshed catalog can remove a category while it is selected.
  const activeOutput = filters.some((filter) => filter.value === output)
    ? output : "all";
  const visible = useMemo(
    () => entries.filter((entry) =>
      matchesAgentQuery(entry.agent, query) && matchesOutput(entry, activeOutput),
    ),
    [entries, query, activeOutput],
  );

  // A failed refetch over a list this screen already read is a stale list, not
  // an unreadable one: keep rendering it. Only a failure with no data behind it
  // takes the load-failed state, which is what AuroraLoadFailed documents for
  // every other surface.
  const agentsRead = agentsQuery.data !== undefined;
  const loadFailed = agentsQuery.isError && !agentsRead;
  const agentsKnown = !agentsQuery.isPending && agentsRead;
  // The catalog is what maps a carrier to its generation contract. Until it has
  // been read, or when the read failed, carriers cannot be classified, so the
  // directory must say so instead of claiming the workspace has no assistants.
  const catalogDegraded = isAuroraDegraded(skillsQuery.data);
  const catalogUnreadable =
    catalogDegraded || (skillsQuery.isError && skillsQuery.data === undefined);
  // A successfully read empty list means no assistant is configured, which is
  // a different fact from "this keyword matched none"; the two get distinct
  // copy rather than telling the user to try another keyword that cannot help.
  const noAgents = query.trim() === "" && entries.length === 0;

  return (
    <div className="aurora-page">
      <DirectoryHero
        entries={agentsKnown && !skillsQuery.isPending && !catalogUnreadable
          ? entries : undefined}
      />
      <ol
        className="aurora-quick-steps"
        aria-label={t(($) => $.directory.guide_label)}
      >
        <li>
          <span aria-hidden="true">1</span>
          {t(($) => $.directory.step_choose)}
        </li>
        <li>
          <span aria-hidden="true">2</span>
          {t(($) => $.directory.step_describe)}
        </li>
        <li>
          <span aria-hidden="true">3</span>
          {t(($) => $.directory.step_download)}
        </li>
      </ol>

      <div className="aurora-section-head">
        <div>
          <p className="aurora-eyebrow">
            <LayoutGrid aria-hidden="true" className="size-3.5" />
            {t(($) => $.directory.eyebrow)}
          </p>
          <h2>{t(($) => $.directory.section_title)}</h2>
        </div>
        {agentsKnown && !catalogUnreadable ? (
          <span>
            {t(($) => $.directory.visible_count, { count: visible.length })}
          </span>
        ) : null}
      </div>

      <div className="aurora-toolbar">
        <div
          className="aurora-directory-filters"
          role="group"
          aria-label={t(($) => $.directory.filter_label)}
        >
          {filters.map((filter) => (
            <Button
              key={filter.value}
              type="button"
              variant="outline"
              className="aurora-directory-filter"
              aria-pressed={activeOutput === filter.value}
              onClick={() => setOutput(filter.value)}
            >
              {filter.label}
            </Button>
          ))}
        </div>
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

      {catalogUnreadable ? (
        <Alert variant="destructive">
          <CircleAlert aria-hidden="true" />
          <AlertDescription>
            {t(($) => $.directory.catalog_unreadable)}
          </AlertDescription>
          <AlertAction>
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => void skillsQuery.refetch()}
            >
              {t(($) => $.retry)}
            </Button>
          </AlertAction>
        </Alert>
      ) : null}

      {agentsQuery.isPending || skillsQuery.isPending ? (
        <DirectorySkeleton />
      ) : loadFailed ? (
        <AuroraLoadFailed
          title={t(($) => $.directory.load_failed_title)}
          onRetry={() => void agentsQuery.refetch()}
        />
      ) : visible.length === 0 ? (
        catalogUnreadable ? null : (
          <AuroraPageState
            icon={SearchX}
            title={
              noAgents
                ? t(($) => $.directory.empty_agents_title)
                : t(($) => $.directory.empty_title)
            }
            description={
              noAgents
                ? t(($) => $.directory.empty_agents_description)
                : t(($) => $.directory.empty_description)
            }
            actions={
              query || activeOutput !== "all" ? (
                <Button
                  type="button"
                  variant="outline"
                  onClick={() => {
                    setQuery("");
                    setOutput("all");
                  }}
                >
                  {t(($) => $.directory.clear_search)}
                </Button>
              ) : undefined
            }
          />
        )
      ) : (
        <ul className={GRID_CLASS}>
          {visible.map((entry) => (
            <li key={entry.agent.id}>
              <AgentCard entry={entry} onSelect={() => {
                if (onSelect) onSelect(entry);
                else setSelected(entry);
              }} />
            </li>
          ))}
        </ul>
      )}

      <GenerationComposer
        agent={selected?.agent ?? null}
        skill={
          selected?.mode === "generation" ? selected.skill : null
        }
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

function DirectoryHero({
  entries,
}: {
  entries: DirectoryEntry[] | undefined;
}) {
  const { t } = useT("aurora");
  const skillCount = new Set(entries?.flatMap(({ agent }) =>
    agentSkillChips(agent).map((skill) => skill.id),
  )).size;
  return (
    <section className="aurora-hero">
      <div>
        <span className="aurora-hero-label">
          {t(($) => $.directory.hero_tag)}
        </span>
        <h1>
          {t(($) => $.directory.hero_title)}
          <span>{t(($) => $.directory.hero_highlight)}</span>
        </h1>
        <p className="aurora-hero-description">
          {t(($) => $.directory.hero_description)}
        </p>
        {entries ? (
          <div className="aurora-hero-stats">
            <span>
              <strong>{entries.length}</strong>{t(($) => $.directory.tools_label)}
            </span>
            <span>
              <strong>{skillCount}</strong>{t(($) => $.directory.skills_label)}
            </span>
          </div>
        ) : null}
      </div>
      {/* Decorative: the first four agents as tiles. It carries no fact the
          list below does not, so it is hidden from assistive tech. */}
      <div className="aurora-hero-orbit" aria-hidden="true">
        <div className="aurora-hero-center">
          <Sparkles className="size-6" />
        </div>
        {(entries ?? []).slice(0, 4).map((entry, index) => (
          <span
            key={entry.agent.id}
            className={"aurora-orbit-avatar aurora-orbit-avatar--" + (index + 1)}
          >
            <AuroraAvatar
              name={entry.agent.name}
              id={entry.agent.id}
              avatarUrl={entry.agent.avatar_url}
              available
            />
          </span>
        ))}
      </div>
    </section>
  );
}

function AgentCard({
  entry,
  onSelect,
}: {
  entry: DirectoryEntry;
  onSelect: () => void;
}) {
  const { t } = useT("aurora");
  const locale = useLocale();
  const { agent } = entry;
  const skill = entry.mode === "generation" ? entry.skill : null;
  const chips = agentSkillChips(agent);
  const starter = agent.conversation_starters?.find(
    (item) => item.label.trim() && item.prompt.trim(),
  );

  return (
    <button type="button" onClick={onSelect} className="aurora-card">
      <span className="aurora-card-top">
        <AuroraAvatar
          name={agent.name}
          id={agent.id}
          avatarUrl={agent.avatar_url}
          size="lg"
          available
        />
        <span className="aurora-card-name">
          <span className="aurora-card-kind">
            {skill ? t(($) => $.directory.tool_label) : t(($) => $.directory.filter_assistant)}
          </span>
          <span className="aurora-card-title">{agent.name}</span>
          {chips.length > 0 ? (
            <span className="aurora-card-meta">
              {t(($) => $.directory.skill_count, { count: chips.length })}
            </span>
          ) : null}
        </span>
        <span aria-hidden="true" className="aurora-card-arrow">
          <ArrowUpRight className="size-4" />
        </span>
      </span>
      {agent.description ? (
        <span className="aurora-card-description">{agent.description}</span>
      ) : null}
      {chips.length > 0 ? (
        <span className="aurora-chips">
          {chips.map((chip) => (
            <span key={chip.id}>{chip.name}</span>
          ))}
        </span>
      ) : null}
      {starter ? (
        <span className="aurora-card-example">
          <Sparkles aria-hidden="true" className="size-3.5" />
          <span>{t(($) => $.directory.example_label)} <strong>{starter.label}</strong></span>
        </span>
      ) : null}
      {skill && skill.output.length === 1 && skill.output[0] === "text" ? (
        <span className="aurora-delivery-note">
          {t(($) => $.directory.text_output)}
        </span>
      ) : null}
      <span className="aurora-card-footer">
        <span className="aurora-card-state">
          <span aria-hidden="true" className="aurora-dot" />
          {t(($) => $.directory.ready)}
        </span>
        {skill ? (
          <strong>
            {t(($) => $.credits, {
              credits: formatCredits(skill.credits, locale),
            })}
          </strong>
        ) : null}
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
          <Skeleton className="h-80 w-full rounded-3xl" />
        </li>
      ))}
    </ul>
  );
}
