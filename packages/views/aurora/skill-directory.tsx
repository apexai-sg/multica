"use client";

import { useMemo, useState } from "react";
import {
  ArrowUpRight,
  LayoutGrid,
  Search,
  SearchX,
  Sparkles,
} from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import {
  InputGroup,
  InputGroupAddon,
  InputGroupInput,
} from "@multica/ui/components/ui/input-group";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { useAuroraSkills, type AuroraSkill } from "@multica/core/aurora";
import type { Agent } from "@multica/core/types";
import { AuroraAvatar } from "./aurora-avatar";
import { AuroraPageState } from "./aurora-page-header";
import { useLocale, useT } from "../i18n";
import { GenerationComposer } from "./generation-composer";
import { formatCredits } from "./format";
import { AuroraLoadFailed } from "./load-failed";
import {
  agentSkillChips,
  agentSubmissionSkill,
  matchesAgentQuery,
  orderDirectoryAgents,
} from "./aurora-agents";
import { useAuroraAgentDirectory } from "./use-aurora-agent-directory";

/**
 * The consumer directory: every agent the backend shows this member, with the
 * skill catalog consulted only to decide which agents can submit.
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

interface SelectedAgent {
  agent: Agent;
  skill: AuroraSkill;
}

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

  // Submittable agents first; the backend's order holds inside each group.
  const ordered = useMemo(
    () => orderDirectoryAgents(agents, skills),
    [agents, skills],
  );
  const visible = useMemo(
    () => ordered.filter((agent) => matchesAgentQuery(agent, query)),
    [ordered, query],
  );

  // A failed refetch over a list this screen already read is a stale list, not
  // an unreadable one: keep rendering it. Only a failure with no data behind it
  // takes the load-failed state, which is what AuroraLoadFailed documents for
  // every other surface.
  const agentsRead = agentsQuery.data !== undefined;
  const loadFailed = agentsQuery.isError && !agentsRead;
  const agentsKnown = !agentsQuery.isPending && agentsRead;

  return (
    <div className="aurora-page">
      <DirectoryHero
        agents={agentsKnown ? ordered : undefined}
        skills={skills}
      />
      <ol
        className="aurora-quick-steps"
        aria-label={t(($) => $.directory.guide_label)}
      >
        <li>
          <span>1</span>
          {t(($) => $.directory.step_choose)}
        </li>
        <li>
          <span>2</span>
          {t(($) => $.directory.step_describe)}
        </li>
        <li>
          <span>3</span>
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
        {agentsKnown ? (
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

      {agentsQuery.isPending ? (
        <DirectorySkeleton />
      ) : loadFailed ? (
        <AuroraLoadFailed
          title={t(($) => $.directory.load_failed_title)}
          onRetry={() => void agentsQuery.refetch()}
        />
      ) : visible.length === 0 ? (
        <AuroraPageState
          icon={SearchX}
          title={t(($) => $.directory.empty_title)}
          description={t(($) => $.directory.empty_description)}
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
      ) : (
        <ul className={GRID_CLASS}>
          {visible.map((agent) => {
            const skill = agentSubmissionSkill(agent, skills);
            return (
              <li key={agent.id}>
                <AgentCard
                  agent={agent}
                  skill={skill}
                  onSelect={() => {
                    if (skill) setSelected({ agent, skill });
                  }}
                />
              </li>
            );
          })}
        </ul>
      )}

      <GenerationComposer
        agent={selected?.agent ?? null}
        skill={selected?.skill ?? null}
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
  agents,
  skills,
}: {
  agents: Agent[] | undefined;
  skills: AuroraSkill[];
}) {
  const { t } = useT("aurora");
  return (
    <section className="aurora-hero">
      <div>
        <span className="aurora-hero-label">
          {t(($) => $.directory.hero_tag)}
        </span>
        <h1>{t(($) => $.directory.hero_title)}</h1>
        <p>{t(($) => $.directory.hero_subtitle)}</p>
      </div>
      {/* Decorative: the first four agents as tiles. It carries no fact the
          list below does not, so it is hidden from assistive tech. */}
      <div className="aurora-hero-orbit" aria-hidden="true">
        <div className="aurora-hero-center">
          <Sparkles className="size-6" />
        </div>
        {(agents ?? []).slice(0, 4).map((agent, index) => (
          <span
            key={agent.id}
            className={"aurora-orbit-avatar aurora-orbit-avatar--" + (index + 1)}
          >
            <AuroraAvatar
              name={agent.name}
              id={agent.id}
              avatarUrl={agent.avatar_url}
              available={Boolean(agentSubmissionSkill(agent, skills))}
            />
          </span>
        ))}
      </div>
    </section>
  );
}

function AgentCard({
  agent,
  skill,
  onSelect,
}: {
  agent: Agent;
  skill: AuroraSkill | null;
  onSelect: () => void;
}) {
  const { t } = useT("aurora");
  const locale = useLocale();
  const chips = agentSkillChips(agent);

  const content = (
    <>
      <span className="aurora-card-top">
        <AuroraAvatar
          name={agent.name}
          id={agent.id}
          avatarUrl={agent.avatar_url}
          size="lg"
          available={Boolean(skill)}
        />
        <span className="aurora-card-name">
          <span className="aurora-card-title">{agent.name}</span>
        </span>
        {skill ? (
          <span aria-hidden="true" className="aurora-card-arrow">
            <ArrowUpRight className="size-4" />
          </span>
        ) : null}
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
        {skill ? (
          <>
            <span className="aurora-card-state">
              <span aria-hidden="true" className="aurora-dot" />
              {t(($) => $.directory.ready)}
            </span>
            <strong>
              {t(($) => $.credits, {
                credits: formatCredits(skill.credits, locale),
              })}
            </strong>
          </>
        ) : (
          <span className="aurora-status-pill aurora-status-pill--muted">
            {t(($) => $.directory.read_only)}
          </span>
        )}
      </span>
    </>
  );

  if (!skill) {
    return (
      <div className="aurora-card aurora-card--read-only">{content}</div>
    );
  }
  return (
    <button type="button" onClick={onSelect} className="aurora-card">
      {content}
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
