# Aurora directory driven by backend agents

Status: approved design, ready for an implementation plan.
Date: 2026-10-18.
Issue: https://github.com/apexai-sg/multica/issues/1.
Base branch: `origin/codex/apex-consumer-workbench` (the P1 consumer-workbench
frontend work).

## Context

Aurora's consumer directory is currently built from a server-side skill
catalog. The 24 catalog entries are compiled into the Go binary
(`server/internal/aurora/catalog.go`), and the consumer workbench adds a second
layer of hardcoded display data in the frontend
(`packages/views/aurora/tool-guidance.ts`: display names, summaries, and
examples for 24 ids), a hardcoded "coming soon" list, and a hardcoded
recommended-id list.

The workspace's real agents are configured in the Multica web backend at
`https://mca.apexxai.net/apexai/agents`. Those agent rows carry the display
data the consumer should show: name, description, avatar, attached skills, and
conversation starters. The same agents are executed through the ordinary issue
task path: generation creation already creates an issue, assigns the carrier
agent, and enqueues an issue task; the daemon then runs a normal agent process.
Aurora's generation row and artifact collection are a separate layer keyed by
the agent's `aurora:<skillId>` system key.

## Goal

Make the Aurora directory a projection of the workspace's configured agents.
Every visible, non-archived agent appears with its backend-configured name,
description, avatar, skills, and conversation starters. Remove the frontend's
hardcoded agent copy and the hardcoded planned/recommended lists.

## Non-goals

- No publishing flow and no Aurora-specific agent configuration. The agent row
  is the only source of display data.
- No new agent schema, API, migration, daemon, billing, or execution changes.
- No new submission path for ordinary agents. Ordinary agents are read-only in
  this round; submission behavior for them is a later decision.
- No change to generation creation, credits, issue execution, artifact
  collection, or settlement.

## Decisions

### Data source

The directory reads the standard agent list (`GET /api/agents`, through the
shared `agentListOptions` query) instead of the Aurora skill catalog.
`packages/views/aurora` uses the shared `useWorkspaceId()` and query options; the
Aurora workspace layout already establishes the workspace header.

- Filter out agents whose `archived_at` is non-null. (The shared query includes
  archived agents for the management surfaces.)
- The list is permission-filtered by the backend exactly like the agents page:
  a member sees their own private agents and agents shared with them. Agents
  invisible to the viewer do not appear. This is accepted for this round; a
  workspace-wide consumer directory would require a backend visibility change.
- The skill catalog (`GET /api/aurora/skills`) is still fetched, but only as
  execution metadata (credits, attachment rules, availability) for agents that
  already carry an Aurora identity. It is never used to name, describe, or
  illustrate an agent.

### Card fields

Every card renders only backend agent data:

| Card element | Source |
| --- | --- |
| Title | `agent.name` |
| Summary | `agent.description` (omitted when empty) |
| Avatar | `agent.avatar_url` (existing initials/tint fallback when empty) |
| Skill chips | `agent.skills` where `enabled !== false`, showing `name` |
| Example copy | `agent.conversation_starters` (used by the composer) |

No fallback names, summaries, or examples are invented in the client. A
submittable card additionally shows the matched catalog entry's price and
ready state; a read-only card shows neither.

### Submittable agents

An agent is submittable when:

1. `agent.system_key` is `aurora:<skillId>`, and
2. `<skillId>` exists in the Aurora skill catalog returned by the server with
   `available !== false`.

Such an agent keeps the existing generation composer and submission path. The
catalog continues to supply credits and attachment rules; the card and composer
use the agent for every display field.

All other agents are displayed read-only: no price, no submit button, no new
interaction. If the catalog query fails, agents still render, but no agent is
treated as submittable (fail closed).

### Directory UI

- Remove `tool-guidance.ts`, the `PLANNED` "coming soon" list, and
  `RECOMMENDED_TOOL_IDS`.
- Remove category tabs (agents have no category field) and the recommended tab.
  Keep the search box; it matches name, description, enabled skill names and
  descriptions, and conversation-starter labels and prompts.
- `directory.visible_count` and the hero stats count agents, not skills.
  Labels change from skill wording to agent/assistant wording.
- The empty state offers clearing the search.
- Card order is the backend agent-list order (creation time), with submittable
  agents first. The decorative hero orbit uses the first four agents in that
  order.

### Composer

Only opened for submittable agents:

- Title, avatar, name, and deliverable summary come from the agent.
- Example buttons come from `agent.conversation_starters`; when absent, no
  example button and no placeholder example is shown.
- Credits, attachment rules, and availability still come from the matched
  catalog entry.
- Submission behavior, request shape, credits, issue creation, and artifact
  handling are unchanged.

### History and works

Generation rows still carry only `skillId`. The client builds a
`skillId -> agent` map from the agent list (`system_key` suffix) and prefers the
agent's name in history and work views (neither view renders an agent avatar
today). When no visible agent matches, the existing server catalog name remains
the fallback.

### Removed behavior and copy

- Delete the hardcoded display tables and their tests.
- Remove the directory's category, recommended, and "coming soon" i18n keys.
- Update the directory copy in all five locale bundles:
  - "skill" wording becomes agent/assistant wording;
  - the empty-state text no longer mentions categories;
  - no copy promises unavailable features or prices.

## Edge cases

- Agent list load failure: render the existing load-failed state with retry.
- Agent list success but empty: render the empty state without inventing agents.
- Agent has no description, no skills, or no starters: show only the fields
  that exist; do not substitute client copy.
- Agent archived: excluded.
- `system_key` with a retired or unknown skill id: displayed read-only.
- Duplicate skill names across agents: each agent card is independent; the
  client never keys display data by skill id except for the history fallback.
- A submittable agent whose runtime is unavailable: the existing composer error
  handling applies; no new detection is added in this round.

## Testing

- Views tests, beside the implementation:
  - the directory renders names, descriptions, avatars, and skill chips from a
    mocked agent list;
  - archived agents are excluded;
  - a submittable agent opens the composer; an ordinary agent has no submit
    affordance and no price;
  - search matches skill names and conversation starters;
  - the composer renders agent display fields and starters;
  - no test references the deleted hardcoded tables.
- Commands: `pnpm typecheck`, `pnpm lint`, and the scoped Aurora/views test
  suites. Report what ran and what was skipped.
- No Go tests are expected because the backend is unchanged; the implementation
  plan must verify that no backend file was touched.

## Delivery

The work lands on a branch based on `origin/codex/apex-consumer-workbench`. The
current worktree branch points at `main`; it must first include the
consumer-workbench commit (merge or rebase) before implementation so the
hardcoded tables being removed actually exist.

### Deployment prerequisite

The directory lists existing carrier agents; it does not synthesize catalog
cards. Before deploying to a workspace, verify that every catalog id the
workspace should offer has an `aurora:<id>` carrier (`GET /api/agents`). A
missing carrier is invisible or read-only, and the public agent API cannot set
`system_key`; `EnsureSystemAgents` has no production caller. Backfill before
release (or create each generation once through the API, which seeds the
carrier before the Fleet and credit checks).
