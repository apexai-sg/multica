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
 * The directory owns which agent's drawer is open. That stays component state
 * rather than a URL so a half-written prompt does not survive a refresh as a
 * linkable address.
 */
export interface SkillDirectoryProps {
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

export function SkillDirectory({
  worksHref,
  topUpHref,
  issueHref,
}: SkillDirectoryProps = {}) {
  const { t } = useT("aurora");
  const { agents, query: agentsQuery } = useAuroraAgentDirectory();
  const skillsQuery = useAuroraSkills();
  const skills = skillsQuery.data?.value ?? EMPTY_SKILLS;
  const [query, setQuery] = useState("");
  const [selected, setSelected] = useState<SelectedAgent | null>(null);

  // One classification pass: generation entries first, then issue entries.
  // System-generated agents with no current catalogue entry are omitted.
  const entries = useMemo(
    () => directoryEntries(agents, skills),
    [agents, skills],
  );
  const visible = useMemo(
    () => entries.filter((entry) => matchesAgentQuery(entry.agent, query)),
    [entries, query],
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
      <DirectoryHero entries={agentsKnown ? entries : undefined} />
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
          <h2>{t(($) => $.directory.title)}</h2>
        </div>
        {agentsKnown && !catalogUnreadable ? (
          <span>
            {t(($) => $.directory.visible_count, { count: visible.length })}
          </span>
        ) : null}
      </div>

      <div className="aurora-toolbar">
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
              query ? (
                <Button
                  type="button"
                  variant="outline"
                  onClick={() => setQuery("")}
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
              <AgentCard entry={entry} onSelect={() => setSelected(entry)} />
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
  return (
    <section className="aurora-hero">
      <div>
        <span className="aurora-hero-label">
          {t(($) => $.directory.hero_tag)}
        </span>
        <h1>{t(($) => $.directory.hero_title)}</h1>
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
          <span className="aurora-card-title">{agent.name}</span>
        </span>
        <span aria-hidden="true" className="aurora-card-arrow">
          <ArrowUpRight className="size-4" />
        </span>
      </span>
      {agent.description ? (
        <span className="aurora-card-description">{agent.description}</span>
      ) : null}
      {skill && skill.output.length === 1 && skill.output[0] === "text" ? (
        <span className="aurora-delivery-note">
          {t(($) => $.directory.text_output)}
        </span>
      ) : null}
      {chips.length > 0 ? (
        <span className="aurora-chips">
          {chips.map((chip) => (
            <span key={chip.id}>{chip.name}</span>
          ))}
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
          <Skeleton className="h-20 w-full rounded-xl" />
        </li>
      ))}
    </ul>
  );
}
