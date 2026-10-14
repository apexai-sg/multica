package aurora

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// SystemAgentDef describes an Aurora expert carrier. Every carrier is
// member-visible and assignable. The 24 definitions correspond one-to-one with
// the 24 catalog entries; SystemKey is the agent's stable identity
// ("aurora:"+skillID), never its display name.
type SystemAgentDef struct {
	SkillID      string
	SystemKey    string
	Name         string
	Instructions string
	// RequiredTools is the provider-operation set copied from the skill's
	// execution policy. It documents what the skill's steps perform; it is not
	// a tool allowlist — Aurora runs on the ordinary agent surface, so nothing
	// narrows the model's tools to this set (see SkillExecutionPolicy.RequiredTools).
	RequiredTools []string
}

// SystemAgents returns the 24 system-agent definitions in catalog order.
func SystemAgents() []SystemAgentDef {
	cat := Catalog()
	defs := make([]SystemAgentDef, len(cat))
	for i, e := range cat {
		defs[i] = systemAgentDef(e)
	}
	return defs
}

func systemAgentDef(e SkillCatalogEntry) SystemAgentDef {
	def := SystemAgentDef{
		SkillID:      e.ID,
		SystemKey:    "aurora:" + e.ID,
		Name:         e.Name,
		Instructions: systemAgentInstructions(e),
	}
	if policy, ok := ExecutionPolicy(e.ID); ok {
		def.RequiredTools = policy.RequiredTools
	}
	return def
}

// The managed runtime is the workspace's server-hosted host for Aurora's
// system agents. provider distinguishes it from user daemon-registered
// runtimes; nothing else keys off the value yet, task routing will read the
// agent's provider at claim time (Plan 3 Task 3).
const (
	managedRuntimeProvider = "aurora_managed"
	managedRuntimeName     = "Aurora Managed Runtime"
)

// ManagedRuntimeID returns the workspace's managed (server-hosted) runtime —
// the execution carrier for its system agents, and the id a sandbox daemon
// adds to its claim set on managed registration (Plan 3 Task 3). It returns
// pgx.ErrNoRows when the workspace has not seeded yet: the seed runs lazily on
// generation creation, so an unseeded workspace has no managed runtime to
// serve.
func ManagedRuntimeID(ctx context.Context, q *db.Queries, workspaceID pgtype.UUID) (pgtype.UUID, error) {
	rt, err := q.GetAuroraManagedRuntime(ctx, db.GetAuroraManagedRuntimeParams{
		WorkspaceID: workspaceID,
		Provider:    managedRuntimeProvider,
	})
	if err != nil {
		return pgtype.UUID{}, err
	}
	return rt.ID, nil
}

// EnsureAuroraExpert lazily materialises one expert in a workspace: the managed
// runtime, the carrier agent for the requested skill, and every skill the
// carrier runs with — the primary brief plus the expert's sub-skills and their
// supporting files. It is idempotent, so generation creation can call it for
// every request without counting rows first.
//
// The generation path seeds only the requested expert on purpose: the vendored
// library is ~7 MiB, and a workspace should pay for the expert it actually
// runs. EnsureSystemAgents remains for tests and backfill.
//
// ownerID must be a real user id (agent.owner_id references "user"): the
// caller passes the workspace owner. No transaction is taken here on purpose:
// each statement is independently idempotent, so a partially-applied seed
// converges on retry. The handler still serialises concurrent first seeds
// behind a per-workspace advisory lock, because ensureManagedRuntime's
// lookup-then-insert has no ON CONFLICT arbiter.
func EnsureAuroraExpert(ctx context.Context, q *db.Queries, workspaceID, ownerID pgtype.UUID, skillID string) error {
	runtimeID, err := ensureManagedRuntime(ctx, q, workspaceID, ownerID)
	if err != nil {
		return err
	}
	return ensureExpert(ctx, q, workspaceID, ownerID, runtimeID, skillID)
}

// EnsureSystemAgents materialises every vendored expert in a workspace. It is
// the backfill/test entry point; generation creation seeds one expert instead
// (EnsureAuroraExpert).
func EnsureSystemAgents(ctx context.Context, q *db.Queries, workspaceID, ownerID pgtype.UUID) error {
	runtimeID, err := ensureManagedRuntime(ctx, q, workspaceID, ownerID)
	if err != nil {
		return err
	}
	for _, expert := range Experts() {
		if err := ensureExpert(ctx, q, workspaceID, ownerID, runtimeID, expert.Slug); err != nil {
			return err
		}
	}
	return nil
}

// ensureExpert writes one expert's primary skill, its sub-skills and their
// files, the carrier, and every junction. Each statement is independently
// idempotent, so a partially applied seed converges on retry.
func ensureExpert(ctx context.Context, q *db.Queries, workspaceID, ownerID, runtimeID pgtype.UUID, skillID string) error {
	entry, ok := Lookup(skillID)
	if !ok {
		return fmt.Errorf("aurora: skill %q is not in the catalog", skillID)
	}
	expert, ok := ExpertBySlug(skillID)
	if !ok {
		return fmt.Errorf("aurora: skill %q has no vendored expert", skillID)
	}

	primary, err := q.UpsertAuroraSkill(ctx, db.UpsertAuroraSkillParams{
		WorkspaceID: workspaceID,
		Name:        entry.Name,
		Content:     systemSkillContent(entry),
		CreatedBy:   ownerID,
	})
	if err != nil {
		return fmt.Errorf("upsert skill %q: %w", skillID, err)
	}
	skillIDs := []pgtype.UUID{primary.ID}

	for _, sub := range expert.Subskills {
		content, ok := LoadExpertSubskill(skillID, sub.Slug)
		if !ok {
			return fmt.Errorf("aurora: expert %q sub-skill %q is missing from the vendored library", skillID, sub.Slug)
		}
		row, err := q.UpsertAuroraSkill(ctx, db.UpsertAuroraSkillParams{
			WorkspaceID: workspaceID,
			Name:        sub.Name,
			Content:     content.Content,
			CreatedBy:   ownerID,
		})
		if err != nil {
			return fmt.Errorf("upsert sub-skill %q: %w", sub.Name, err)
		}
		if err := syncSkillFiles(ctx, q, row.ID, content.Files); err != nil {
			return fmt.Errorf("sync sub-skill %q files: %w", sub.Name, err)
		}
		skillIDs = append(skillIDs, row.ID)
	}

	def := systemAgentDef(entry)
	// Adopt the workspace's existing carrier for this system_key before
	// inserting one. Migration 172's unique index includes runtime_id, so an
	// insert-first seed would mint a second carrier after a runtime teardown
	// unbound the first; agent_workspace_name_unique then fails the seed and
	// generation creation breaks, while GetAgentBySystemKey keeps resolving
	// the older, unbound row.
	starters := expertConversationStarters(expert)
	agent, err := q.AdoptAuroraSystemAgent(ctx, db.AdoptAuroraSystemAgentParams{
		WorkspaceID:          workspaceID,
		OwnerID:              ownerID,
		RuntimeID:            runtimeID,
		SystemKey:            pgtype.Text{String: def.SystemKey, Valid: true},
		Name:                 def.Name,
		Instructions:         def.Instructions,
		ConversationStarters: starters,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		agent, err = q.UpsertAuroraSystemAgent(ctx, db.UpsertAuroraSystemAgentParams{
			WorkspaceID:          workspaceID,
			OwnerID:              ownerID,
			RuntimeID:            runtimeID,
			SystemKey:            pgtype.Text{String: def.SystemKey, Valid: true},
			Name:                 def.Name,
			Instructions:         def.Instructions,
			ConversationStarters: starters,
		})
	}
	if err != nil {
		return fmt.Errorf("upsert agent %q: %w", skillID, err)
	}

	for _, id := range skillIDs {
		if err := q.AddAgentSkill(ctx, db.AddAgentSkillParams{AgentID: agent.ID, SkillID: id}); err != nil {
			return fmt.Errorf("link agent %q: %w", skillID, err)
		}
	}
	return nil
}

// syncSkillFiles makes a sub-skill's stored supporting files exactly the
// vendored set: every vendored file is upserted, and a stored file the library
// no longer carries is deleted, so a re-seed after an upstream change leaves no
// orphan behind.
func syncSkillFiles(ctx context.Context, q *db.Queries, skillID pgtype.UUID, files []ExpertFileContent) error {
	existing, err := q.ListSkillFiles(ctx, skillID)
	if err != nil {
		return err
	}
	wanted := make(map[string]bool, len(files))
	for _, file := range files {
		if _, err := q.UpsertSkillFile(ctx, db.UpsertSkillFileParams{
			SkillID: skillID,
			Path:    file.Path,
			Content: file.Content,
		}); err != nil {
			return err
		}
		wanted[file.Path] = true
	}
	for _, row := range existing {
		if wanted[row.Path] {
			continue
		}
		if err := q.DeleteSkillFile(ctx, row.ID); err != nil {
			return err
		}
	}
	return nil
}

// ensureManagedRuntime returns the workspace's managed runtime, creating it on
// first seed. Lookup-then-insert is the idempotency strategy: the runtime's
// NULL daemon_id sits outside migration 121's partial unique index (NULLs are
// distinct), so there is no ON CONFLICT arbiter to lean on. Concurrent seeds
// could mint a second row; the call site serialises on a per-workspace lock
// (Plan 3 Task 2), which is where that guarantee belongs.
func ensureManagedRuntime(ctx context.Context, q *db.Queries, workspaceID, ownerID pgtype.UUID) (pgtype.UUID, error) {
	runtimeID, err := ManagedRuntimeID(ctx, q, workspaceID)
	if err == nil {
		return runtimeID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return pgtype.UUID{}, err
	}
	created, err := q.CreateAuroraManagedRuntime(ctx, db.CreateAuroraManagedRuntimeParams{
		WorkspaceID: workspaceID,
		Name:        managedRuntimeName,
		Provider:    managedRuntimeProvider,
		OwnerID:     ownerID,
	})
	if err != nil {
		return pgtype.UUID{}, err
	}
	return created.ID, nil
}

// The agent conversation-starter contract is owned by the handler and mirrored
// in packages/core/agents/constants.ts; these are the same limits (see
// normaliseAgentConversationStarters, and migration 404's CHECK for the count).
const (
	expertStarterMax       = 3
	expertStarterLabelMax  = 80
	expertStarterPromptMax = 4000
)

// agentConversationStarter is the JSON shape agent.conversation_starters holds.
type agentConversationStarter struct {
	Label  string `json:"label"`
	Prompt string `json:"prompt"`
}

// expertConversationStarters maps an expert's upstream quick-start cards onto
// the agent's first-turn suggestions: the first three cards with a non-empty
// label and prompt, capped at the handler's limits. It always returns a JSON
// array, never null, so the column's jsonb_typeof CHECK holds.
func expertConversationStarters(expert Expert) []byte {
	starters := make([]agentConversationStarter, 0, expertStarterMax)
	for _, quick := range expert.QuickPrompts {
		if len(starters) == expertStarterMax {
			break
		}
		label := strings.TrimSpace(quick.LabelZh)
		prompt := strings.TrimSpace(quick.PromptZh)
		if label == "" || prompt == "" {
			continue
		}
		starters = append(starters, agentConversationStarter{
			Label:  truncateRunes(label, expertStarterLabelMax),
			Prompt: truncateRunes(prompt, expertStarterPromptMax),
		})
	}
	raw, err := json.Marshal(starters)
	if err != nil {
		// Marshalling a slice of strings cannot fail; keep the value an array.
		return []byte("[]")
	}
	return raw
}

// truncateRunes caps a string at max runes: the starter limits count
// characters, not bytes, and a Chinese label is three bytes per character.
func truncateRunes(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}

// systemAgentInstructions is the minimal system prompt for a skill's system
// agent. Skeletal on purpose: the real prompt is the runtime brief the claim
// path composes.
func systemAgentInstructions(e SkillCatalogEntry) string {
	return fmt.Sprintf(
		"You are the Aurora %s agent (skill %q). Produce %s output from %s input.",
		e.NameEn, e.ID, strings.Join(e.Output, " / "), strings.Join(e.Input, " / "),
	)
}

// systemSkillContent is the prompt content stored on the primary skill row: the
// canonical vendored workflow, refreshed on every seed. It deliberately never
// reads a sub-skill SKILL.md — those are separate skills the carrier also holds.
func systemSkillContent(e SkillCatalogEntry) string {
	brief, _ := Workflow(e.ID)
	return brief
}
