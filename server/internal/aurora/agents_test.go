package aurora_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/aurora"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var (
	agentsTestPool        *pgxpool.Pool
	agentsTestWorkspaceID string
	agentsTestUserID      string
)

const (
	agentsFixtureEmail = "aurora-agents-fixture@multica.ai"
	agentsFixtureSlug  = "aurora-agents-fixture"
)

// TestMain builds the fixture workspace the DB test hangs off. Without a
// database the suite exits green rather than red, the same contract every
// other DB-backed package here follows: the pure unit test above still runs on
// a laptop with no Postgres.
func TestMain(m *testing.M) {
	ctx := context.Background()
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		fmt.Printf("Skipping aurora DB tests: could not connect: %v\n", err)
		os.Exit(m.Run())
	}
	if err := pool.Ping(ctx); err != nil {
		fmt.Printf("Skipping aurora DB tests: database not reachable: %v\n", err)
		pool.Close()
		os.Exit(m.Run())
	}
	if err := seedAgentsFixture(ctx, pool); err != nil {
		fmt.Printf("Skipping aurora DB tests: seed failed: %v\n", err)
		pool.Close()
		os.Exit(m.Run())
	}
	agentsTestPool = pool

	code := m.Run()
	_, _ = pool.Exec(ctx, `DELETE FROM workspace WHERE slug = $1`, agentsFixtureSlug)
	_, _ = pool.Exec(ctx, `DELETE FROM "user" WHERE email = $1`, agentsFixtureEmail)
	pool.Close()
	os.Exit(code)
}

func seedAgentsFixture(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, `DELETE FROM workspace WHERE slug = $1`, agentsFixtureSlug); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `DELETE FROM "user" WHERE email = $1`, agentsFixtureEmail); err != nil {
		return err
	}
	if err := pool.QueryRow(ctx, `INSERT INTO "user" (name, email) VALUES ($1, $2) RETURNING id`,
		"Aurora Agents Fixture User", agentsFixtureEmail).Scan(&agentsTestUserID); err != nil {
		return err
	}
	if err := pool.QueryRow(ctx, `INSERT INTO workspace (name, slug, issue_prefix) VALUES ($1, $2, $3) RETURNING id`,
		"Aurora Agents Fixture", agentsFixtureSlug, "AUR").Scan(&agentsTestWorkspaceID); err != nil {
		return err
	}
	_, err := pool.Exec(ctx, `INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'owner')`,
		agentsTestWorkspaceID, agentsTestUserID)
	return err
}

// agentsFixtureSkill is the one expert the focused seed tests drive.
const agentsFixtureSkill = "ecommerce-bidding-strategy"

func TestSystemAgentsMatchCatalog(t *testing.T) {
	defs := aurora.SystemAgents()
	cat := aurora.Catalog()
	if len(defs) != len(cat) {
		t.Fatalf("system agents %d != catalog %d", len(defs), len(cat))
	}
	keys := map[string]bool{}
	for _, d := range defs {
		if d.SystemKey != "aurora:"+d.SkillID {
			t.Fatalf("system key %q != aurora:%s", d.SystemKey, d.SkillID)
		}
		if d.Name == "" || d.Instructions == "" {
			t.Errorf("system agent %q missing name or instructions", d.SkillID)
		}
		policy, ok := aurora.ExecutionPolicy(d.SkillID)
		if !ok {
			if len(d.RequiredTools) != 0 {
				t.Errorf("unavailable system agent %q has required tools %v", d.SkillID, d.RequiredTools)
			}
		} else if !slices.Equal(d.RequiredTools, policy.RequiredTools) {
			t.Errorf("system agent %q required tools = %v, want %v", d.SkillID, d.RequiredTools, policy.RequiredTools)
		}
		keys[d.SkillID] = true
	}
	for _, c := range cat {
		if !keys[c.ID] {
			t.Fatalf("catalog skill %q has no system agent", c.ID)
		}
	}
}

// Uses the same fixture availability gate as the existing DB tests.
func TestAuroraAgentsAreVisibleToMembers(t *testing.T) {
	if agentsTestPool == nil {
		t.Skip("database not available")
	}
	placeholders := seedUnavailableAgents(t)
	ctx := context.Background()
	tx, err := agentsTestPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)
	ws, owner := util.MustParseUUID(agentsTestWorkspaceID), util.MustParseUUID(agentsTestUserID)
	if err := aurora.EnsureSystemAgents(ctx, q, ws, owner); err != nil {
		t.Fatal(err)
	}
	agents, err := q.ListAgents(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]db.Agent{}
	for _, agent := range agents {
		if strings.HasPrefix(agent.SystemKey.String, "aurora:") {
			byKey[agent.SystemKey.String] = agent
		}
	}
	if len(byKey) != len(expertIDs) {
		t.Fatalf("visible Aurora agents = %d, want %d", len(byKey), len(expertIDs))
	}
	for _, entry := range aurora.Catalog() {
		agent, ok := byKey["aurora:"+entry.ID]
		if !entry.Available {
			if ok {
				t.Fatalf("unavailable agent %s is visible", entry.ID)
			}
			continue
		}
		if !ok || agent.Kind != "user" || agent.Name != entry.Name {
			t.Fatalf("catalog agent %s not visible: %+v", entry.ID, agent)
		}
		if _, err := q.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: agent.ID, WorkspaceID: ws}); err != nil {
			t.Fatal(err)
		}
		if _, err := q.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: agent.ID, WorkspaceID: owner}); err == nil {
			t.Fatal("cross-workspace agent leaked")
		}
		skills, err := q.ListAgentSkills(ctx, agent.ID)
		if err != nil {
			t.Fatalf("list skills for %s: %v", entry.ID, err)
		}
		if len(skills) != 1 {
			t.Fatalf("catalog skill links for %s = %d, want 1", entry.ID, len(skills))
		}
		found := false
		for _, skill := range skills {
			if skill.Name == entry.Name {
				found = true
			}
		}
		if !found {
			t.Fatalf("primary skill %q not linked: %v", entry.Name, skills)
		}
	}
	// Simulate a previously seeded hidden, archived agent. Reseeding must repair
	// it in place, preserving the identity used by generation lookup.
	original := byKey["aurora:"+agentsFixtureSkill]
	if _, err := tx.Exec(ctx, "UPDATE agent SET kind = 'system', archived_at = now(), archived_by = $2 WHERE id = $1", original.ID, owner); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := aurora.EnsureSystemAgents(ctx, q, ws, owner); err != nil {
			t.Fatal(err)
		}
		got, err := q.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: original.ID, WorkspaceID: ws})
		if err != nil || got.ArchivedAt.Valid || got.ArchivedBy.Valid || got.RuntimeID != original.RuntimeID || got.SystemKey != original.SystemKey {
			t.Fatalf("seed did not restore identity: %+v %v", got, err)
		}
		for id, before := range placeholders {
			var after string
			if err := tx.QueryRow(ctx, "SELECT to_jsonb(agent)::text FROM agent WHERE id = $1", id).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if after != before {
				t.Fatalf("unavailable agent %s changed: before %s after %s", id, before, after)
			}
		}
	}
}

// TestAuroraAgentVisibilityMigrationIsIdempotent pins migration 585, which
// froze the 13 catalog keys that existed when it was applied. The catalog has
// since moved to the vendored experts, so the test seeds those historical rows
// itself and proves two things: the migration is idempotent in both
// directions, and a carrier whose system key the migration does not name is
// untouched either way.
func TestAuroraAgentVisibilityMigrationIsIdempotent(t *testing.T) {
	if agentsTestPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	tx, err := agentsTestPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	ws, owner := util.MustParseUUID(agentsTestWorkspaceID), util.MustParseUUID(agentsTestUserID)

	insertCarrier := func(key, kind string) pgtype.UUID {
		t.Helper()
		var id pgtype.UUID
		if err := tx.QueryRow(ctx, `
			INSERT INTO agent (workspace_id, owner_id, name, kind, runtime_mode, system_key, runtime_config, visibility)
			VALUES ($1, $2, $3, $4, 'cloud', $5, '{}'::jsonb, 'workspace')
			RETURNING id`, ws, owner, key, kind, key).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	// Read the kind directly: GetAgentInWorkspace only resolves member-visible
	// carriers, and the migration's whole subject is what is visible.
	kindOf := func(id pgtype.UUID) string {
		t.Helper()
		var kind string
		if err := tx.QueryRow(ctx, "SELECT kind FROM agent WHERE id = $1", id).Scan(&kind); err != nil {
			t.Fatalf("load carrier: %v", err)
		}
		return kind
	}

	// The keys migration 585 names, and one it must leave alone.
	legacyKeys := []string{
		"aurora:poster", "aurora:xhs-image", "aurora:product-image", "aurora:text-image",
		"aurora:image-edit", "aurora:id-photo", "aurora:image-video", "aurora:text-video",
		"aurora:video-captions", "aurora:xhs-copy", "aurora:resume", "aurora:document-summary",
		"aurora:transcription",
	}
	legacy := make([]pgtype.UUID, 0, len(legacyKeys))
	for _, key := range legacyKeys {
		legacy = append(legacy, insertCarrier(key, "system"))
	}
	current := insertCarrier("aurora:"+agentsFixtureSkill, "system")

	readMigration := func(name string) string {
		t.Helper()
		raw, err := os.ReadFile("../../migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}

	up := readMigration("585_aurora_agents_visible.up.sql")
	for i := 0; i < 2; i++ {
		tag, err := tx.Exec(ctx, up)
		if err != nil {
			t.Fatal(err)
		}
		if i == 1 && tag.RowsAffected() != 0 {
			t.Fatalf("second up migration changed %d rows", tag.RowsAffected())
		}
	}
	for i, id := range legacy {
		if got := kindOf(id); got != "user" {
			t.Fatalf("legacy carrier %s kind = %q, want user", legacyKeys[i], got)
		}
	}
	if got := kindOf(current); got != "system" {
		t.Fatalf("up migration touched an unnamed key: kind = %q, want system", got)
	}

	down := readMigration("585_aurora_agents_visible.down.sql")
	for i := 0; i < 2; i++ {
		tag, err := tx.Exec(ctx, down)
		if err != nil {
			t.Fatal(err)
		}
		if i == 1 && tag.RowsAffected() != 0 {
			t.Fatal("down migration is not idempotent")
		}
	}
	for i, id := range legacy {
		if got := kindOf(id); got != "system" {
			t.Fatalf("legacy carrier %s kind = %q after down, want system", legacyKeys[i], got)
		}
	}
	if got := kindOf(current); got != "system" {
		t.Fatalf("down migration touched an unnamed key: kind = %q, want system", got)
	}
}

func TestEnsureSystemAgents(t *testing.T) {
	if agentsTestPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	q := db.New(agentsTestPool)
	ws := util.MustParseUUID(agentsTestWorkspaceID)
	owner := util.MustParseUUID(agentsTestUserID)

	if err := aurora.EnsureSystemAgents(ctx, q, ws, owner); err != nil {
		t.Fatalf("EnsureSystemAgents: %v", err)
	}
	assertSeeded(t, ws)

	// Idempotency: a second call must not add rows.
	if err := aurora.EnsureSystemAgents(ctx, q, ws, owner); err != nil {
		t.Fatalf("EnsureSystemAgents (retry): %v", err)
	}
	assertSeeded(t, ws)
	assertSkillContentMatchesWorkflow(t, ws)
}

// assertSkillContentMatchesWorkflow proves the seed writes each available
// skill's embedded brief to the skill row without reading a vendor SKILL.md.
func assertSkillContentMatchesWorkflow(t *testing.T, ws pgtype.UUID) {
	t.Helper()
	ctx := context.Background()
	for _, entry := range aurora.Catalog() {
		if !entry.Available {
			continue
		}
		var content string
		if err := agentsTestPool.QueryRow(ctx,
			`SELECT content FROM skill WHERE workspace_id = $1 AND name = $2`, ws, entry.Name).Scan(&content); err != nil {
			t.Fatalf("load skill %q content: %v", entry.ID, err)
		}
		brief, ok := aurora.Workflow(entry.ID)
		if !ok {
			if brief != "" {
				t.Errorf("Workflow(%q) = %q, want no brief", entry.ID, brief)
			}
			continue
		}
		if content != brief {
			t.Errorf("skill %q content does not match the embedded workflow", entry.ID)
		}
	}
}

func assertSeeded(t *testing.T, ws pgtype.UUID) {
	t.Helper()
	ctx := context.Background()

	var runtimeCount int
	if err := agentsTestPool.QueryRow(ctx, `
		SELECT count(*) FROM agent_runtime
		WHERE workspace_id = $1 AND daemon_id IS NULL AND runtime_mode = 'cloud' AND provider = 'aurora_managed'`,
		ws).Scan(&runtimeCount); err != nil {
		t.Fatalf("count managed runtime: %v", err)
	}
	if runtimeCount != 1 {
		t.Fatalf("managed runtime rows = %d, want 1", runtimeCount)
	}

	var runtimeMode string
	var daemonID pgtype.Text
	if err := agentsTestPool.QueryRow(ctx, `
		SELECT runtime_mode, daemon_id FROM agent_runtime
		WHERE workspace_id = $1 AND daemon_id IS NULL AND runtime_mode = 'cloud' AND provider = 'aurora_managed'
		LIMIT 1`, ws).Scan(&runtimeMode, &daemonID); err != nil {
		t.Fatalf("load managed runtime: %v", err)
	}
	if runtimeMode != "cloud" {
		t.Errorf("managed runtime runtime_mode = %q, want cloud", runtimeMode)
	}
	if daemonID.Valid {
		t.Errorf("managed runtime daemon_id = %q, want NULL", daemonID.String)
	}

	var agentCount int
	if err := agentsTestPool.QueryRow(ctx,
		`SELECT count(*) FROM agent WHERE workspace_id = $1 AND kind = 'user' AND system_key LIKE 'aurora:%'`, ws).Scan(&agentCount); err != nil {
		t.Fatalf("count system agents: %v", err)
	}
	if agentCount != len(aurora.Catalog()) {
		t.Errorf("system agent rows = %d, want %d", agentCount, len(aurora.Catalog()))
	}

	// Every carrier offers suggestions: the migration's CHECK allows at most
	// three, and the seed maps the expert's quick-start cards.
	var withoutStarters int
	if err := agentsTestPool.QueryRow(ctx, `
		SELECT count(*) FROM agent
		WHERE workspace_id = $1 AND system_key LIKE 'aurora:%'
		  AND jsonb_array_length(conversation_starters) = 0`, ws).Scan(&withoutStarters); err != nil {
		t.Fatalf("count carriers without starters: %v", err)
	}
	if withoutStarters != 0 {
		t.Errorf("carriers without conversation starters = %d, want 0", withoutStarters)
	}

	var skillCount int
	if err := agentsTestPool.QueryRow(ctx,
		`SELECT count(*) FROM skill WHERE workspace_id = $1`, ws).Scan(&skillCount); err != nil {
		t.Fatalf("count skills: %v", err)
	}
	if skillCount != len(aurora.Catalog()) {
		t.Errorf("skill rows = %d, want %d", skillCount, len(aurora.Catalog()))
	}

	var junctionCount int
	if err := agentsTestPool.QueryRow(ctx, `
		SELECT count(*) FROM agent_skill
		WHERE agent_id IN (SELECT id FROM agent WHERE workspace_id = $1 AND kind = 'user' AND system_key LIKE 'aurora:%')`,
		ws).Scan(&junctionCount); err != nil {
		t.Fatalf("count agent_skill: %v", err)
	}
	if junctionCount != len(aurora.Catalog()) {
		t.Errorf("agent_skill rows = %d, want %d", junctionCount, len(aurora.Catalog()))
	}
}

// Seed legacy hidden placeholders and capture their complete row state.
func seedUnavailableAgents(t *testing.T) map[string]string {
	t.Helper()
	dbfx := testutil.New(agentsTestPool, agentsTestWorkspaceID, agentsTestUserID)
	snapshots := map[string]string{}
	for _, key := range []string{"aurora:avatar-video", "aurora:ppt", "aurora:excel"} {
		id := dbfx.Insert(t, "agent", testutil.Cols{
			"workspace_id": agentsTestWorkspaceID, "owner_id": agentsTestUserID,
			"name": "Preserved " + key, "kind": "system", "system_key": key,
			"runtime_mode": "cloud", "runtime_config": testutil.Raw("'{}'::jsonb"), "visibility": "workspace",
		})
		var snapshot string
		if err := agentsTestPool.QueryRow(context.Background(), "SELECT to_jsonb(agent)::text FROM agent WHERE id = $1", id).Scan(&snapshot); err != nil {
			t.Fatal(err)
		}
		snapshots[id] = snapshot
	}
	return snapshots
}

// TestAuroraCarriersSurviveRuntimeTeardown pins the reseed contract after a
// managed runtime is deleted. Migration 172's unique index includes runtime_id,
// so an insert-first seed would mint a second carrier per skill while
// GetAgentBySystemKey kept resolving the older, unbound row: the workspace would
// show duplicate Aurora agents and a generation would bind to a carrier with no
// runtime.
func TestAuroraCarriersSurviveRuntimeTeardown(t *testing.T) {
	if agentsTestPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	ws, owner := util.MustParseUUID(agentsTestWorkspaceID), util.MustParseUUID(agentsTestUserID)
	tx, err := agentsTestPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)

	if err := aurora.EnsureSystemAgents(ctx, q, ws, owner); err != nil {
		t.Fatal(err)
	}
	first, err := aurora.ManagedRuntimeID(ctx, q, ws)
	if err != nil {
		t.Fatal(err)
	}
	key := pgtype.Text{String: "aurora:" + agentsFixtureSkill, Valid: true}
	before, err := q.GetAgentBySystemKey(ctx, db.GetAgentBySystemKeyParams{WorkspaceID: ws, SystemKey: key})
	if err != nil {
		t.Fatal(err)
	}
	carriers := countAvailableAuroraCarriers(t, ctx, tx, ws)

	// Simulate the runtime-delete path: it unbinds every kind='user' agent and
	// then removes the runtime row.
	if _, err := q.UnbindUserAgentsFromRuntime(ctx, first); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "DELETE FROM agent_runtime WHERE id = $1", first); err != nil {
		t.Fatal(err)
	}

	if err := aurora.EnsureSystemAgents(ctx, q, ws, owner); err != nil {
		t.Fatal(err)
	}
	second, err := aurora.ManagedRuntimeID(ctx, q, ws)
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatal("teardown did not mint a new managed runtime")
	}
	after, err := q.GetAgentBySystemKey(ctx, db.GetAgentBySystemKeyParams{WorkspaceID: ws, SystemKey: key})
	if err != nil {
		t.Fatal(err)
	}
	if after.ID != before.ID {
		t.Fatalf("reseed changed carrier identity: %x -> %x", before.ID.Bytes, after.ID.Bytes)
	}
	if after.RuntimeID != second {
		t.Fatalf("carrier runtime = %v, want the new managed runtime %v", after.RuntimeID, second)
	}
	if got := countAvailableAuroraCarriers(t, ctx, tx, ws); got != carriers {
		t.Fatalf("available carriers = %d, want %d after reseed", got, carriers)
	}
}

// countAvailableAuroraCarriers counts only the 13 available carriers, so the
// hidden placeholders another test seeds cannot change the result.
func countAvailableAuroraCarriers(t *testing.T, ctx context.Context, tx pgx.Tx, ws pgtype.UUID) int {
	t.Helper()
	keys := make([]string, 0, len(aurora.Catalog()))
	for _, e := range aurora.Catalog() {
		if e.Available {
			keys = append(keys, "aurora:"+e.ID)
		}
	}
	var n int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM agent WHERE workspace_id = $1 AND system_key = ANY($2)", ws, keys).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestEnsureAuroraExpertSeedsOneCarrier pins the per-request seed: one call
// materialises the requested expert alone, with its primary brief and no
// sub-skill rows, and a second call adds nothing.
func TestEnsureAuroraExpertSeedsOneCarrier(t *testing.T) {
	if agentsTestPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	tx, err := agentsTestPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)
	ws, owner := util.MustParseUUID(agentsTestWorkspaceID), util.MustParseUUID(agentsTestUserID)

	expert, ok := aurora.ExpertBySlug(agentsFixtureSkill)
	if !ok {
		t.Fatalf("no vendored expert %q", agentsFixtureSkill)
	}
	entry, ok := aurora.Lookup(agentsFixtureSkill)
	if !ok {
		t.Fatalf("no catalog entry %q", agentsFixtureSkill)
	}

	if err := aurora.EnsureAuroraExpert(ctx, q, ws, owner, agentsFixtureSkill); err != nil {
		t.Fatalf("EnsureAuroraExpert: %v", err)
	}
	agent, err := q.GetAgentBySystemKey(ctx, db.GetAgentBySystemKeyParams{
		WorkspaceID: ws,
		SystemKey:   pgtype.Text{String: "aurora:" + agentsFixtureSkill, Valid: true},
	})
	if err != nil {
		t.Fatalf("carrier not seeded: %v", err)
	}
	if agent.Name != entry.Name {
		t.Errorf("carrier name = %q, want %q", agent.Name, entry.Name)
	}
	if !agent.RuntimeID.Valid {
		t.Error("carrier is not bound to the managed runtime")
	}

	// The carrier's first-turn suggestions are the expert's upstream quick-start
	// cards: the first three, label and prompt.
	var starters []struct {
		Label  string `json:"label"`
		Prompt string `json:"prompt"`
	}
	if err := json.Unmarshal(agent.ConversationStarters, &starters); err != nil {
		t.Fatalf("unmarshal conversation_starters: %v", err)
	}
	if len(starters) != 3 {
		t.Fatalf("conversation starters = %d, want 3", len(starters))
	}
	for i, want := range expert.QuickPrompts[:3] {
		if starters[i].Label != want.LabelZh || starters[i].Prompt != strings.TrimSpace(want.PromptZh) {
			t.Errorf("starter %d = %+v, want label %q prompt %q", i, starters[i], want.LabelZh, want.PromptZh)
		}
	}

	skills, err := q.ListAgentSkills(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 1 {
		t.Fatalf("linked skills = %d, want 1", len(skills))
	}
	if skills[0].Name != entry.Name {
		t.Errorf("linked skill = %q, want %q", skills[0].Name, entry.Name)
	}
	if skills[0].Content == "" || !strings.Contains(skills[0].Content, ".claude/skills/"+expert.Subskills[0].Name+"/SKILL.md") {
		t.Fatal("primary skill content does not name the runtime sub-skill directory")
	}
	rows, err := q.ListSkillFiles(ctx, skills[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("primary skill files = %d, want 0", len(rows))
	}

	// Idempotency: the second call must leave the same row counts.
	if err := aurora.EnsureAuroraExpert(ctx, q, ws, owner, agentsFixtureSkill); err != nil {
		t.Fatalf("EnsureAuroraExpert (retry): %v", err)
	}
	again, err := q.ListAgentSkills(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != len(skills) {
		t.Fatalf("linked skills after retry = %d, want %d", len(again), len(skills))
	}
	rowsAfter, err := q.ListSkillFiles(ctx, again[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rowsAfter) != 0 {
		t.Fatalf("primary skill files after retry = %d, want 0", len(rowsAfter))
	}
}
