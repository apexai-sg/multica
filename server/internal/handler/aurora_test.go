package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/aurora"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/storage"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

func TestListAuroraSkills(t *testing.T) {
	// Loading the directory now seeds the workspace's carriers, so the payload
	// covers the same first-entry path CreateAuroraGeneration already takes.
	cleanupAuroraSystemAgents(t)

	req := newRequest(http.MethodGet, "/api/aurora/skills", nil)
	var out struct {
		Skills []struct {
			ID        string   `json:"id"`
			Name      string   `json:"name"`
			NameEn    string   `json:"name_en"`
			Category  string   `json:"category"`
			Credits   int      `json:"credits"`
			Input     []string `json:"input"`
			Output    []string `json:"output"`
			Featured  *bool    `json:"featured"`
			Available *bool    `json:"available"`
		} `json:"skills"`
	}
	response := testutil.Call(t, testHandler.ListAuroraSkills, req).Want(http.StatusOK)
	response.JSON(&out)
	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	if len(out.Skills) != 33 {
		t.Fatalf("expected 33 experts, got %d", len(out.Skills))
	}
	available := 0
	for _, s := range out.Skills {
		if s.ID == "" || s.Name == "" || s.NameEn == "" || s.Category == "" || s.Credits <= 0 || len(s.Input) == 0 || len(s.Output) == 0 {
			t.Errorf("skill response missing required metadata: %#v", s)
		}
		// False values must be present, not omitted or null, for API consumers.
		if s.Featured == nil || s.Available == nil {
			t.Errorf("skill %q missing featured or available boolean", s.ID)
		}
		if s.Available != nil && *s.Available {
			available++
		}
	}
	if available != len(out.Skills) {
		t.Errorf("available skills = %d of %d, want every listed expert", available, len(out.Skills))
	}
	first := out.Skills[0]
	if first.ID != "content-creation-lyrics-songwriting" || first.Name != "歌词创作专家" ||
		first.NameEn != "Content Creation Lyrics Songwriting Expert" || first.Category != "content-creation" ||
		first.Credits != 60 ||
		!reflect.DeepEqual(first.Input, []string{"text", "document"}) || !reflect.DeepEqual(first.Output, []string{"text"}) ||
		first.Featured == nil || !*first.Featured || first.Available == nil || !*first.Available {
		t.Errorf("unexpected first catalog entry: %#v", first)
	}
}

// TestListAuroraSkillsIncludesCarrierAvatars covers the directory's portrait
// join: the avatar a member set on an expert's carrier agent rides along with
// that expert's catalog entry, and only with that one. Everything else keeps
// the initials tile the client falls back to.
func TestListAuroraSkillsIncludesCarrierAvatars(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	// A throwaway workspace: seeding carriers with system keys into the shared
	// fixture workspace would leak into every later test's "agent ... LIMIT 1"
	// heuristics (see cleanupAuroraSystemAgents).
	ownerID := dbfx.User(t, "Aurora carrier avatar owner", "aurora-carrier-avatar-"+uuid.NewString()+"@example.com")
	workspaceID := dbfx.Workspace(t, "Aurora carrier avatars", "aurora-carrier-avatars-"+uuid.NewString())
	t.Cleanup(func() {
		// dbfx only cleans up its own fixed-slug fixture rows, so this test removes
		// the throwaway user/workspace pair it inserted rather than leaking one per
		// run. Agents go first: they reference both.
		ctx := context.Background()
		testPool.Exec(ctx, `DELETE FROM agent WHERE workspace_id = $1`, workspaceID)
		testPool.Exec(ctx, `DELETE FROM workspace WHERE id = $1`, workspaceID)
		testPool.Exec(ctx, `DELETE FROM "user" WHERE id = $1`, ownerID)
	})

	const (
		withAvatar    = "media-short-video-copy"
		withoutAvatar = "legal-contract-drafting"
		archivedOne   = "design-prd-writing"
		// A third-party URL: nothing this deployment's storage minted, so the
		// response is a passthrough the test can assert exactly. Resolution of a
		// storage object is covered by avatar_test.go.
		avatarURL = "https://cdn.example.com/agents/lobster.png"
		goneURL   = "https://cdn.example.com/agents/archived.png"
	)
	dbfx.Agent(t, "Aurora carrier with avatar", "", testutil.Cols{
		"workspace_id": workspaceID,
		"owner_id":     ownerID,
		"system_key":   aurora.SystemKey(withAvatar),
		"avatar_url":   avatarURL,
	})
	dbfx.Agent(t, "Aurora carrier without avatar", "", testutil.Cols{
		"workspace_id": workspaceID,
		"owner_id":     ownerID,
		"system_key":   aurora.SystemKey(withoutAvatar),
	})
	dbfx.Agent(t, "Archived Aurora carrier", "", testutil.Cols{
		"workspace_id": workspaceID,
		"owner_id":     ownerID,
		"system_key":   aurora.SystemKey(archivedOne),
		"avatar_url":   goneURL,
		"archived_at":  testutil.Raw("now()"),
	})
	// A carrier whose skill the catalog does not list must not invent an entry.
	dbfx.Agent(t, "Retired Aurora carrier", "", testutil.Cols{
		"workspace_id": workspaceID,
		"owner_id":     ownerID,
		"system_key":   "aurora:retired-poster",
		"avatar_url":   goneURL,
	})
	// A runtime teardown can leave two live carriers for one system_key (migration
	// 172's partial unique index includes runtime_id). The directory has to
	// resolve the same one GetAgentBySystemKey does — the earliest — rather than
	// whichever row the planner happened to return last.
	dbfx.Agent(t, "Aurora carrier with avatar rebound", "", testutil.Cols{
		"workspace_id": workspaceID,
		"owner_id":     ownerID,
		"system_key":   aurora.SystemKey(withAvatar),
		"avatar_url":   goneURL,
		"created_at":   testutil.Raw("now() + interval '1 hour'"),
	})

	req := newRequest(http.MethodGet, "/api/aurora/skills", nil)
	req.Header.Set("X-Workspace-ID", workspaceID)
	var out struct {
		Skills []struct {
			ID        string `json:"id"`
			AvatarURL string `json:"avatar_url"`
		} `json:"skills"`
	}
	testutil.Call(t, testHandler.ListAuroraSkills, req).Want(http.StatusOK).JSON(&out)
	if len(out.Skills) != 33 {
		t.Fatalf("expected 33 experts, got %d", len(out.Skills))
	}
	avatars := make(map[string]string, len(out.Skills))
	for _, s := range out.Skills {
		avatars[s.ID] = s.AvatarURL
	}
	if got := avatars[withAvatar]; got != avatarURL {
		t.Errorf("avatar for %q = %q, want %q", withAvatar, got, avatarURL)
	}
	for _, id := range []string{withoutAvatar, archivedOne} {
		if got := avatars[id]; got != "" {
			t.Errorf("avatar for %q = %q, want none", id, got)
		}
	}
}

// cleanupAuroraSystemAgents removes the workspace's lazily seeded Aurora system
// agents, skills and managed runtime at teardown. Generation creation and the
// directory load seed these on first use and they would otherwise persist into
// every later handler test, whose `agent ... LIMIT 1` agent-runtime heuristics
// assume the workspace holds only user-visible agents (kind='system' is excluded
// by GetAgentInWorkspace but not by an unfiltered LIMIT 1). Agents go first so
// their agent_skill rows cascade; skills and the runtime follow once the agents
// no longer reference them.
func cleanupAuroraSystemAgents(t *testing.T) {
	t.Helper()
	names := make([]string, 0, len(aurora.Catalog()))
	titlePrefixes := make([]string, 0, len(aurora.Catalog()))
	for _, e := range aurora.Catalog() {
		names = append(names, e.Name)
		titlePrefixes = append(titlePrefixes, e.Name+"：")
	}
	t.Cleanup(func() {
		ctx := context.Background()
		// A generation now owns an issue titled "<skill name>：<prompt>". Remove
		// them so this shared workspace does not leak Aurora issues into later
		// tests that page or filter the whole issue list. Deleting an issue
		// cascades to the task rows that reference it.
		for _, prefix := range titlePrefixes {
			if _, err := testPool.Exec(ctx,
				`DELETE FROM issue WHERE workspace_id = $1 AND title LIKE $2`,
				testWorkspaceID, prefix+"%"); err != nil {
				t.Errorf("cleanup aurora issues: %v", err)
			}
		}
		if _, err := testPool.Exec(ctx,
			`DELETE FROM agent WHERE workspace_id = $1 AND system_key LIKE 'aurora:%'`, testWorkspaceID); err != nil {
			t.Errorf("cleanup aurora agents: %v", err)
		}
		if _, err := testPool.Exec(ctx,
			`DELETE FROM skill WHERE workspace_id = $1 AND name = ANY($2::text[])`, testWorkspaceID, names); err != nil {
			t.Errorf("cleanup aurora skills: %v", err)
		}
		if _, err := testPool.Exec(ctx,
			`DELETE FROM agent_runtime WHERE workspace_id = $1 AND daemon_id IS NULL AND provider = 'aurora_managed'`, testWorkspaceID); err != nil {
			t.Errorf("cleanup aurora runtime: %v", err)
		}
	})
}

func TestCreateAuroraGenerationReservesAndEnqueues(t *testing.T) {
	creditTestReset(t)
	cleanupAuroraSystemAgents(t)
	ctx := context.Background()
	user := parseUUID(testUserID)
	ws := parseUUID(testWorkspaceID)

	// Fund the caller so the reservation can succeed. The grant needs no
	// particular size — 1000 credits covers the fixture expert's price with room
	// and leaves the post-reservation balance easy to assert.
	if err := testHandler.Credit.Grant(ctx, user, ws, 1_000_000_000, aurora.LedgerKindAdjustment, creditRef("seed")); err != nil {
		t.Fatalf("Grant: %v", err)
	}

	prompt := "生成一张新加坡亲子游封面"
	req := newRequest(http.MethodPost, "/api/aurora/generations", map[string]string{
		"skillId": "ecommerce-bidding-strategy",
		"prompt":  prompt,
	})
	out := testutil.Decode[struct {
		Generation struct {
			ID              string `json:"id"`
			SkillID         string `json:"skillId"`
			Prompt          string `json:"prompt"`
			Status          string `json:"status"`
			CreditsReserved int64  `json:"creditsReserved"`
		} `json:"generation"`
	}](t, testHandler.CreateAuroraGeneration, req, http.StatusCreated)

	gen := out.Generation
	entry, ok := aurora.Lookup("ecommerce-bidding-strategy")
	if !ok {
		t.Fatal("fixture skill is not in the catalog")
	}
	wantReserved := int64(entry.Credits) * 1_000_000
	if gen.ID == "" {
		t.Fatalf("expected non-empty id, got %q", gen.ID)
	}
	if gen.Status != "queued" {
		t.Fatalf("expected queued, got %q", gen.Status)
	}
	if gen.SkillID != "ecommerce-bidding-strategy" {
		t.Fatalf("expected skillId ecommerce-bidding-strategy, got %q", gen.SkillID)
	}
	if gen.Prompt != prompt {
		t.Fatalf("expected prompt %q, got %q", prompt, gen.Prompt)
	}
	if gen.CreditsReserved != wantReserved {
		t.Fatalf("expected creditsReserved %d, got %d", wantReserved, gen.CreditsReserved)
	}

	// The handler must have written the task's id and the reserved micro-credits
	// back onto the row, and the wallet must reflect the reservation.
	var taskID pgtype.UUID
	var reserved int64
	dbfx.QueryRow(t, `SELECT task_id, credits_reserved FROM aurora_generation WHERE id = $1`, gen.ID).Scan(&taskID, &reserved)
	if !taskID.Valid {
		t.Fatal("expected a non-null task_id on the generation row")
	}
	if reserved != wantReserved {
		t.Fatalf("row credits_reserved = %d, want %d", reserved, wantReserved)
	}
	t.Cleanup(func() {
		if taskID.Valid {
			testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
		}
		testPool.Exec(context.Background(), `DELETE FROM aurora_generation WHERE id = $1`, gen.ID)
	})

	// The caller has no subscription, so this first generation of the month
	// also granted the free tier's monthly credits before reserving.
	bal, err := testHandler.Credit.Balance(ctx, user)
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if want := int64(1_000_000_000) + testHandler.Tiers.FreeMonthlyMicro() - wantReserved; bal != want {
		t.Fatalf("balance after reserve = %d, want %d (seed + free monthly grant - reserved)", bal, want)
	}

	// The row must still be scoped to the workspace and user resolved from the
	// request headers, not zero UUIDs (the historical #1661 bug class).
	var wsID, userID pgtype.UUID
	if err := testPool.QueryRow(context.Background(),
		`SELECT workspace_id, user_id FROM aurora_generation WHERE id = $1`, gen.ID,
	).Scan(&wsID, &userID); err != nil {
		t.Fatalf("load generation row: %v", err)
	}
	if wsID.String() != testWorkspaceID || userID.String() != testUserID {
		t.Fatalf("row scoped to workspace/user %s/%s, want %s/%s", wsID.String(), userID.String(), testWorkspaceID, testUserID)
	}
}

func TestCreateAuroraGenerationRejectsInsufficientCredits(t *testing.T) {
	creditTestReset(t)
	auroraSubscriptionTestReset(t)
	cleanupAuroraSystemAgents(t)
	ctx := context.Background()

	// Every skill now costs less than the free monthly grant, so an empty wallet
	// would afford the fixture expert once the lazy grant tops it up to 200.
	// Open the wallet with a debt instead: 200 - 150 = 50 credits is still short
	// of the expert's price, so this request remains the insufficient-credit
	// case it pins. The debt is fixture-only; the service API can never drive a
	// balance negative.
	const openingDebtMicro = -150_000_000
	if _, err := testPool.Exec(ctx,
		`INSERT INTO credit_balance (user_id, available_micro) VALUES ($1, $2)`,
		parseUUID(testUserID), openingDebtMicro); err != nil {
		t.Fatalf("seed opening debt: %v", err)
	}

	req := newRequest(http.MethodPost, "/api/aurora/generations", map[string]string{
		"skillId": "ecommerce-bidding-strategy",
		"prompt":  "余额不足的生成请求",
	})
	response := testutil.Call(t, testHandler.CreateAuroraGeneration, req).Want(http.StatusPaymentRequired)

	// The 402 carries the shortfall in whole credits: what the skill costs, what
	// the wallet held, and the difference a top-up has to cover. The message
	// stays for clients that only read `error`.
	var rejection struct {
		Error            string  `json:"error"`
		RequiredCredits  float64 `json:"requiredCredits"`
		AvailableCredits float64 `json:"availableCredits"`
		ShortfallCredits float64 `json:"shortfallCredits"`
	}
	response.JSON(&rejection)
	if rejection.Error != "insufficient credits" {
		t.Fatalf("error = %q, want 'insufficient credits'", rejection.Error)
	}
	entry, ok := aurora.Lookup("ecommerce-bidding-strategy")
	if !ok {
		t.Fatal("catalog lost ecommerce-bidding-strategy")
	}
	wantRequired := float64(entry.Credits)
	wantAvailable := float64(testHandler.Tiers.FreeMonthlyMicro()+openingDebtMicro) / microCreditsPerCredit
	if rejection.RequiredCredits != wantRequired {
		t.Fatalf("requiredCredits = %v, want %v", rejection.RequiredCredits, wantRequired)
	}
	if rejection.AvailableCredits != wantAvailable {
		t.Fatalf("availableCredits = %v, want %v", rejection.AvailableCredits, wantAvailable)
	}
	if want := wantRequired - wantAvailable; rejection.ShortfallCredits != want {
		t.Fatalf("shortfallCredits = %v, want %v", rejection.ShortfallCredits, want)
	}

	// The aborted generation must be terminal with the documented reason...
	var status string
	var genErr pgtype.Text
	dbfx.QueryRow(t, `
		SELECT status, error FROM aurora_generation
		WHERE workspace_id = $1 AND skill_id = 'ecommerce-bidding-strategy'
		ORDER BY created_at DESC LIMIT 1`,
		testWorkspaceID).Scan(&status, &genErr)
	if status != "failed" {
		t.Fatalf("generation status = %q, want failed", status)
	}
	if !genErr.Valid || genErr.String != "insufficient credits" {
		t.Fatalf("generation error = %q, want 'insufficient credits'", genErr.String)
	}

	// ...and nothing may have been enqueued onto any Aurora system agent.
	taskCount := dbfx.Count(t, `
		SELECT count(*) FROM agent_task_queue atq
		JOIN agent a ON a.id = atq.agent_id
		WHERE a.workspace_id = $1 AND a.system_key LIKE 'aurora:%'`,
		testWorkspaceID)
	if taskCount != 0 {
		t.Fatalf("enqueued %d tasks onto Aurora system agents, want 0", taskCount)
	}
}

func TestCreateAuroraGenerationRejectsUnknownSkill(t *testing.T) {
	req := newRequest(http.MethodPost, "/api/aurora/generations", map[string]string{
		"skillId": "nope",
		"prompt":  "x",
	})
	testutil.Call(t, testHandler.CreateAuroraGeneration, req).Want(http.StatusBadRequest)
}

// TestCreateAuroraGenerationRejectsRetiredSkill keeps the sixteen hand-written
// skills out of the API surface: the catalog now carries the vendored experts
// only, so a retired id and a phase-2 placeholder are both unknown.
func TestCreateAuroraGenerationRejectsRetiredSkill(t *testing.T) {
	for _, skillID := range []string{"poster", "xhs-image", "text-video", "avatar-video", "ppt", "excel"} {
		req := newRequest(http.MethodPost, "/api/aurora/generations", map[string]string{
			"skillId": skillID,
			"prompt":  "x",
		})
		testutil.Call(t, testHandler.CreateAuroraGeneration, req).Want(http.StatusBadRequest)
	}
}

func TestCreateAuroraGenerationRejectsMissingPrompt(t *testing.T) {
	req := newRequest(http.MethodPost, "/api/aurora/generations", map[string]string{
		"skillId": "ecommerce-bidding-strategy",
	})
	testutil.Call(t, testHandler.CreateAuroraGeneration, req).Want(http.StatusBadRequest)
}

func TestCreateAuroraGenerationRejectsMissingSkillID(t *testing.T) {
	req := newRequest(http.MethodPost, "/api/aurora/generations", map[string]string{
		"prompt": "x",
	})
	testutil.Call(t, testHandler.CreateAuroraGeneration, req).Want(http.StatusBadRequest)
}

func TestCreateAuroraGenerationRejectsMalformedBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/aurora/generations", strings.NewReader(`{"skillId":`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", testUserID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	testutil.Call(t, testHandler.CreateAuroraGeneration, req).Want(http.StatusBadRequest)
}

// insertAuroraAttachment writes one uploaded attachment row owned by owner in
// the fixture workspace and returns its id. It stands in for the authenticated
// upload path, which has its own tests.
func insertAuroraAttachment(t *testing.T, filename, contentType string, size int64, owner string) string {
	t.Helper()
	return dbfx.Insert(t, "attachment", testutil.Cols{
		"workspace_id":  testWorkspaceID,
		"uploader_type": "member",
		"uploader_id":   owner,
		"filename":      filename,
		"url":           "https://storage.test/" + filename,
		"content_type":  contentType,
		"size_bytes":    size,
	})
}

// auroraTaskAttachmentIDs loads the attachment ids the handler stamped into the
// enqueued quick-create task's context, and registers the task/row cleanup.
func auroraTaskAttachmentIDs(t *testing.T, generationID string) []string {
	t.Helper()
	ctx := context.Background()
	var taskID pgtype.UUID
	if err := testPool.QueryRow(ctx, `SELECT task_id FROM aurora_generation WHERE id = $1`, generationID).Scan(&taskID); err != nil {
		t.Fatalf("load generation task: %v", err)
	}
	if !taskID.Valid {
		t.Fatalf("generation %s has no task id", generationID)
	}
	var raw []byte
	if err := testPool.QueryRow(ctx, `SELECT context FROM agent_task_queue WHERE id = $1`, taskID).Scan(&raw); err != nil {
		t.Fatalf("load task context: %v", err)
	}
	var payload struct {
		AttachmentIDs []string `json:"attachment_ids"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal task context: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
		testPool.Exec(context.Background(), `DELETE FROM aurora_generation WHERE id = $1`, generationID)
	})
	return payload.AttachmentIDs
}

// TestCreateAuroraGenerationAttachmentIDsReachEnqueue proves the validated ids
// are handed to the enqueued task instead of the nil the handler passed before
// attachment support existed.
func TestCreateAuroraGenerationAttachmentIDsReachEnqueue(t *testing.T) {
	creditTestReset(t)
	cleanupAuroraSystemAgents(t)
	ctx := context.Background()
	user := parseUUID(testUserID)
	ws := parseUUID(testWorkspaceID)

	if err := testHandler.Credit.Grant(ctx, user, ws, 1_000_000_000, aurora.LedgerKindAdjustment, creditRef("seed")); err != nil {
		t.Fatalf("Grant: %v", err)
	}

	// One document is the expert input contract: a written request plus at most
	// one source document.
	document := insertAuroraAttachment(t, "notes.md", "text/markdown", 2048, testUserID)

	req := newRequest(http.MethodPost, "/api/aurora/generations", map[string]any{
		"skillId":       "ecommerce-bidding-strategy",
		"prompt":        "one source document",
		"attachmentIds": []string{document},
	})
	out := testutil.Decode[struct {
		Generation struct {
			ID string `json:"id"`
		} `json:"generation"`
	}](t, testHandler.CreateAuroraGeneration, req, http.StatusCreated)

	ids := auroraTaskAttachmentIDs(t, out.Generation.ID)
	if !reflect.DeepEqual(ids, []string{document}) {
		t.Fatalf("task attachment_ids = %v, want %v", ids, []string{document})
	}
}

// TestAuroraGenerationIDReachesClaimPayload proves the claim response carries
// the real generation id: the daemon writes it into the broker context and
// refuses to substitute the task id, so the protocol must supply it.
func TestAuroraGenerationIDReachesClaimPayload(t *testing.T) {
	creditTestReset(t)
	cleanupAuroraSystemAgents(t)
	ctx := context.Background()
	user := parseUUID(testUserID)
	ws := parseUUID(testWorkspaceID)

	if err := testHandler.Credit.Grant(ctx, user, ws, 1_000_000_000, aurora.LedgerKindAdjustment, creditRef("seed")); err != nil {
		t.Fatalf("Grant: %v", err)
	}

	req := newRequest(http.MethodPost, "/api/aurora/generations", map[string]any{
		"skillId": "ecommerce-bidding-strategy",
		"prompt":  "a claim regression",
	})
	out := testutil.Decode[struct {
		Generation struct {
			ID string `json:"id"`
		} `json:"generation"`
	}](t, testHandler.CreateAuroraGeneration, req, http.StatusCreated)
	generationID := out.Generation.ID
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = (SELECT task_id FROM aurora_generation WHERE id = $1)`, generationID)
		testPool.Exec(context.Background(), `DELETE FROM aurora_generation WHERE id = $1`, generationID)
	})

	var taskID pgtype.UUID
	if err := testPool.QueryRow(ctx, `SELECT task_id FROM aurora_generation WHERE id = $1`, generationID).Scan(&taskID); err != nil {
		t.Fatalf("load generation task id: %v", err)
	}
	if !taskID.Valid {
		t.Fatal("generation has no task id")
	}
	task, err := testHandler.Queries.GetAgentTask(ctx, taskID)
	if err != nil {
		t.Fatalf("load task: %v", err)
	}
	runtime, err := testHandler.Queries.GetAgentRuntime(ctx, task.RuntimeID)
	if err != nil {
		t.Fatalf("load runtime: %v", err)
	}

	claimReq := newRequest(http.MethodPost, "/api/claim", nil)
	resp, _, _, _, _, failure := testHandler.buildClaimedTaskResponse(claimReq, &task, runtime, uuidToString(task.RuntimeID), testWorkspaceID)
	if failure != nil {
		t.Fatalf("buildClaimedTaskResponse: %+v", failure)
	}
	if resp.GenerationID != generationID {
		t.Fatalf("claim generation_id = %q, want %q", resp.GenerationID, generationID)
	}
}

// TestCreateAuroraGenerationAttachmentRejectedBeforeReserve proves a bad
// attachment fails the request before the sandbox is ensured, a generation row
// is written, or any credit is reserved.
func TestCreateAuroraGenerationAttachmentRejectedBeforeReserve(t *testing.T) {
	creditTestReset(t)
	cleanupAuroraSystemAgents(t)

	mgr := &fakeSandboxManager{}
	withSandboxManager(t, mgr)

	document := insertAuroraAttachment(t, "notes.md", "text/markdown", 2048, testUserID)
	foreignOwner := insertAuroraAttachment(t, "not-mine.md", "text/markdown", 2048, "66666666-6666-6666-6666-666666666666")

	otherWorkspace := dbfx.Insert(t, "workspace", testutil.Cols{
		"name":         "Aurora attachment foreign workspace",
		"slug":         "aurora-104-foreign-" + uuid.NewString(),
		"description":  "Foreign workspace",
		"issue_prefix": "FGN",
	})
	foreignWorkspace := dbfx.Insert(t, "attachment", testutil.Cols{
		"workspace_id":  otherWorkspace,
		"uploader_type": "member",
		"uploader_id":   testUserID,
		"filename":      "foreign.md",
		"url":           "https://storage.test/foreign.md",
		"content_type":  "text/markdown",
		"size_bytes":    int64(2048),
	})

	tooMany := []string{document, uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()}

	cases := []struct {
		name string
		body map[string]any
	}{
		{"foreign workspace file", map[string]any{"skillId": "ecommerce-bidding-strategy", "prompt": "x", "attachmentIds": []string{foreignWorkspace}}},
		{"foreign owner file", map[string]any{"skillId": "ecommerce-bidding-strategy", "prompt": "x", "attachmentIds": []string{foreignOwner}}},
		{"more than one document", map[string]any{"skillId": "ecommerce-bidding-strategy", "prompt": "x", "attachmentIds": tooMany}},
		{"duplicate ids", map[string]any{"skillId": "ecommerce-bidding-strategy", "prompt": "x", "attachmentIds": []string{document, document}}},
		{"malformed id", map[string]any{"skillId": "ecommerce-bidding-strategy", "prompt": "x", "attachmentIds": []string{"not-a-uuid"}}},
		{"unknown id", map[string]any{"skillId": "ecommerce-bidding-strategy", "prompt": "x", "attachmentIds": []string{uuid.NewString()}}},
	}

	beforeGenerations := dbfx.Count(t, "SELECT count(*) FROM aurora_generation WHERE workspace_id = $1", testWorkspaceID)
	beforeDeductions := dbfx.Count(t, "SELECT count(*) FROM credit_ledger WHERE user_id = $1 AND kind = $2", testUserID, aurora.LedgerKindDeduction)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			testutil.Call(t, testHandler.CreateAuroraGeneration, newRequest(http.MethodPost, "/api/aurora/generations", tc.body)).Want(http.StatusBadRequest)
		})
	}

	if mgr.callCount() != 0 {
		t.Fatalf("sandbox ensure calls = %d, want 0: rejected input reached provisioning", mgr.callCount())
	}
	if got := dbfx.Count(t, "SELECT count(*) FROM aurora_generation WHERE workspace_id = $1", testWorkspaceID); got != beforeGenerations {
		t.Fatalf("generation rows = %d, want %d unchanged", got, beforeGenerations)
	}
	if got := dbfx.Count(t, "SELECT count(*) FROM credit_ledger WHERE user_id = $1 AND kind = $2", testUserID, aurora.LedgerKindDeduction); got != beforeDeductions {
		t.Fatalf("reservation ledger rows = %d, want %d unchanged", got, beforeDeductions)
	}
}

// creditTestReset empties the fixture user's credit wallet so each test starts
// from a known-empty state, and empties it again on cleanup. credit_balance and
// credit_ledger carry no foreign key to user, so the row fixture's own cleanup
// would otherwise leave these rows behind.
//
// It clears by user_id, which is why every test's references carry testUserID:
// the idempotency key is globally unique and the fast path looks it up by key
// alone, so a row orphaned by an abnormally killed run (whose fixture user no
// longer exists, and whose id no later run reuses) is invisible to this reset
// and would silently satisfy a later run's fast path. Run-unique references
// make these tests independent of cleanup having happened at all.
func creditTestReset(t *testing.T) {
	t.Helper()
	creditTestResetUser(t, testUserID)
}

// creditTestResetUser is creditTestReset for a wallet other than the suite
// fixture's — the billing endpoints are account-scoped, so a test that proves
// they read the caller's wallet needs a second one.
func creditTestResetUser(t *testing.T, userID string) {
	t.Helper()
	reset := func() {
		ctx := context.Background()
		if _, err := testPool.Exec(ctx, `DELETE FROM credit_ledger WHERE user_id = $1`, userID); err != nil {
			t.Fatalf("reset credit_ledger: %v", err)
		}
		if _, err := testPool.Exec(ctx, `DELETE FROM credit_balance WHERE user_id = $1`, userID); err != nil {
			t.Fatalf("reset credit_balance: %v", err)
		}
	}
	reset()
	t.Cleanup(reset)
}

// creditRef namespaces a test reference with the fixture user's id, so the
// idempotency key derived from it is unique to this run. The key is globally
// unique and adjust's fast path looks it up by key alone, without a user
// filter, so a row left by an abnormally killed run — whose fixture user no
// longer exists and whose id no later run reuses — would otherwise satisfy a
// later run's fast path and turn an operation into a silent no-op. Tying
// references to testUserID makes the tests correct regardless of what a
// previous run left behind.
func creditRef(base string) string {
	return base + "-" + testUserID
}

// creditRefFor is creditRef for a wallet other than the fixture user's. The
// namespace has to be the *owning* user's id, not the caller's: a decoy wallet
// funded with creditRef(base) would carry the same idempotency key as the
// caller's own row with that base, so whichever test ran first would satisfy
// the second one's fast path and the second wallet would silently never be
// funded — the exact staleness creditRef exists to prevent.
func creditRefFor(userID, base string) string {
	return base + "-" + userID
}

func TestAuroraCreditReserveAndRefundAreIdempotent(t *testing.T) {
	creditTestReset(t)
	ctx := context.Background()
	user := parseUUID(testUserID)
	ws := parseUUID(testWorkspaceID)
	gen := creditRef("gen-1")

	if err := testHandler.Credit.Grant(ctx, user, ws, 1000, aurora.LedgerKindAdjustment, creditRef("seed")); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	if err := testHandler.Credit.Reserve(ctx, user, ws, 300, gen); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	// Idempotency: re-reserving the same reference must not deduct again.
	if err := testHandler.Credit.Reserve(ctx, user, ws, 300, gen); err != nil {
		t.Fatalf("Reserve retry: %v", err)
	}
	bal, err := testHandler.Credit.Balance(ctx, user)
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if bal != 700 {
		t.Fatalf("balance after idempotent reserve = %d, want 700", bal)
	}

	if err := testHandler.Credit.Refund(ctx, user, ws, 300, gen); err != nil {
		t.Fatalf("Refund: %v", err)
	}
	// Idempotency: re-refunding the same reference must not credit again.
	if err := testHandler.Credit.Refund(ctx, user, ws, 300, gen); err != nil {
		t.Fatalf("Refund retry: %v", err)
	}
	bal, err = testHandler.Credit.Balance(ctx, user)
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if bal != 1000 {
		t.Fatalf("balance after refund = %d, want 1000", bal)
	}
}

func TestAuroraCreditReserveFailsWhenInsufficient(t *testing.T) {
	creditTestReset(t)
	ctx := context.Background()
	user := parseUUID(testUserID)
	ws := parseUUID(testWorkspaceID)

	if err := testHandler.Credit.Reserve(ctx, user, ws, 100, creditRef("gen-x")); !errors.Is(err, aurora.ErrInsufficientCredits) {
		t.Fatalf("Reserve with empty balance: err = %v, want ErrInsufficientCredits", err)
	}
}

func TestAuroraCreditGrantRejectsInvalidKind(t *testing.T) {
	creditTestReset(t)
	ctx := context.Background()
	user := parseUUID(testUserID)
	ws := parseUUID(testWorkspaceID)

	if err := testHandler.Credit.Grant(ctx, user, ws, 100, "bogus", creditRef("seed")); err == nil {
		t.Fatal("Grant with invalid kind should fail")
	}
}

func TestAuroraCreditBalanceIsZeroWithoutARow(t *testing.T) {
	creditTestReset(t)
	bal, err := testHandler.Credit.Balance(context.Background(), parseUUID(testUserID))
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if bal != 0 {
		t.Fatalf("balance for a user with no wallet row = %d, want 0", bal)
	}
}

// TestAuroraCreditRejectsNonPositiveAmounts guards the direction of every
// wallet movement. Reserve negates its argument, so a negative amount would
// credit the wallet while recording a "deduction" row; Grant passes its
// argument straight through, so a negative amount would debit while recording
// a "topup"/"adjustment" row. Both are silent and direction-inverting, and
// neither would be caught by the balance assertions above.
func TestAuroraCreditRejectsNonPositiveAmounts(t *testing.T) {
	creditTestReset(t)
	ctx := context.Background()
	user := parseUUID(testUserID)
	ws := parseUUID(testWorkspaceID)

	calls := map[string]func() error{
		"Reserve": func() error { return testHandler.Credit.Reserve(ctx, user, ws, -100, creditRef("gen-neg")) },
		"Refund":  func() error { return testHandler.Credit.Refund(ctx, user, ws, -100, creditRef("gen-neg")) },
		"Grant": func() error {
			return testHandler.Credit.Grant(ctx, user, ws, -100, aurora.LedgerKindTopup, creditRef("gen-neg"))
		},
		"Reserve/zero": func() error { return testHandler.Credit.Reserve(ctx, user, ws, 0, creditRef("gen-zero")) },
	}
	for name, call := range calls {
		if err := call(); err == nil {
			t.Errorf("%s with a non-positive amount should fail", name)
		}
	}

	// Nothing may have been written: no wallet row, no ledger row.
	bal, err := testHandler.Credit.Balance(ctx, user)
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if bal != 0 {
		t.Fatalf("balance after rejected calls = %d, want 0", bal)
	}
	var ledgerRows int
	if err := testPool.QueryRow(ctx,
		`SELECT count(*) FROM credit_ledger WHERE user_id = $1`, testUserID).Scan(&ledgerRows); err != nil {
		t.Fatalf("count ledger rows: %v", err)
	}
	if ledgerRows != 0 {
		t.Fatalf("ledger rows after rejected calls = %d, want 0", ledgerRows)
	}
}

// TestAuroraCreditConcurrentDuplicateReserveIsIdempotent pins the two branches
// of adjust that only a real race reaches. Both are the difference between
// charging once and charging twice, and neither is reachable from the
// single-goroutine tests above:
//
//   - enough balance for both: the duplicate's deduct succeeds, so it reaches
//     the ledger insert and loses on the unique index — it must roll back the
//     redundant balance change and report success.
//   - enough balance for one only: the winner's deduction is what leaves too
//     little for the duplicate, so the duplicate's conditional deduct matches
//     no row — it must not report that as insufficient credits.
//
// In both cases the duplicate carries the same reference, hence the same
// idempotency key. A distinct key would prove nothing here.
//
// The interleaving is sequenced, not timing-dependent: a transaction holds the
// credit_balance row lock and the duplicate is only released once Postgres
// reports it waiting, so the race is guaranteed rather than hoped for. The
// winner is driven with raw SQL because its transaction has to stay open across
// the duplicate's arrival, which the service API cannot express; those
// statements mirror what adjust's winning transaction does. The behaviour under
// test is the duplicate's.
func TestAuroraCreditConcurrentDuplicateReserveIsIdempotent(t *testing.T) {
	cases := []struct {
		name      string
		seedMicro int64
	}{
		{"duplicate reaches the ledger insert", 1000},
		{"duplicate finds the balance already spent", 300},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			creditTestReset(t)
			ctx := context.Background()
			user := parseUUID(testUserID)
			ws := parseUUID(testWorkspaceID)
			// The winner's raw insert and the loser's Reserve must agree on the
			// idempotency key, so both derive it from this one reference. The
			// service computes "reserve:"+reference; the raw statement below
			// spells out the same key.
			raceRef := creditRef("gen-race")
			raceKey := "reserve:" + raceRef

			if err := testHandler.Credit.Grant(ctx, user, ws, tc.seedMicro, aurora.LedgerKindAdjustment, creditRef("seed")); err != nil {
				t.Fatalf("Grant: %v", err)
			}

			winner, err := testPool.Begin(ctx)
			if err != nil {
				t.Fatalf("begin winner tx: %v", err)
			}
			defer winner.Rollback(ctx)
			var winnerBalance int64
			if err := winner.QueryRow(ctx, `
				UPDATE credit_balance SET available_micro = available_micro - 300, updated_at = now()
				WHERE user_id = $1 AND available_micro >= 300
				RETURNING available_micro`, user).Scan(&winnerBalance); err != nil {
				t.Fatalf("winner deduct: %v", err)
			}
			if _, err := winner.Exec(ctx, `
				INSERT INTO credit_ledger
					(user_id, workspace_id, kind, amount_micro, balance_after_micro, reference, idempotency_key)
				VALUES ($1, $2, 'deduction', -300, $3, $4, $5)`,
				user, ws, winnerBalance, raceRef, raceKey); err != nil {
				t.Fatalf("winner ledger insert: %v", err)
			}

			dupErr := make(chan error, 1)
			go func() {
				dupErr <- testHandler.Credit.Reserve(ctx, user, ws, 300, raceRef)
			}()

			// The winner is still uncommitted, so the duplicate's pre-check
			// missed and it is parked inside its own transaction. Releasing the
			// winner only after observing that wait is what makes the ordering
			// deterministic.
			waitForCreditBalanceLock(t, ctx)

			if err := winner.Commit(ctx); err != nil {
				t.Fatalf("commit winner: %v", err)
			}

			if err := <-dupErr; err != nil {
				t.Fatalf("duplicate Reserve = %v, want nil (the operation already succeeded)", err)
			}

			bal, err := testHandler.Credit.Balance(ctx, user)
			if err != nil {
				t.Fatalf("Balance: %v", err)
			}
			wantBal := tc.seedMicro - 300
			if bal != wantBal {
				t.Fatalf("balance after concurrent duplicate = %d, want %d (deducted exactly once)", bal, wantBal)
			}
			// Exactly one ledger row for the contested key: the winner's. The
			// seed grant has its own key and is not part of this claim.
			var ledgerRows int
			if err := testPool.QueryRow(ctx,
				`SELECT count(*) FROM credit_ledger WHERE idempotency_key = $1`, raceKey).Scan(&ledgerRows); err != nil {
				t.Fatalf("count ledger rows: %v", err)
			}
			if ledgerRows != 1 {
				t.Fatalf("ledger rows for %s = %d, want 1", raceKey, ledgerRows)
			}
		})
	}
}

// waitForCreditBalanceLock blocks until a backend other than this one is
// waiting on a lock while touching credit_balance, or fails the test after a
// bounded wait so a regression surfaces as a failure rather than a hang.
func waitForCreditBalanceLock(t *testing.T, ctx context.Context) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var blocked int
		if err := testPool.QueryRow(ctx, `
			SELECT count(*) FROM pg_stat_activity
			WHERE pid <> pg_backend_pid()
			  AND wait_event_type = 'Lock'
			  AND query LIKE '%credit_balance%'`).Scan(&blocked); err != nil {
			t.Fatalf("poll pg_stat_activity: %v", err)
		}
		if blocked > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("duplicate Reserve never blocked on the credit_balance row lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// auroraBillingResponse is the wire shape the two read-only billing endpoints
// share. Both fields are pointers because the whole point of these tests is to
// tell "the key is absent" from "the key is present and zero" — a value field
// cannot.
type auroraBillingResponse struct {
	AvailableMicro *int64 `json:"availableMicro"`
	Transactions   []struct {
		ID                string `json:"id"`
		Kind              string `json:"kind"`
		AmountMicro       int64  `json:"amountMicro"`
		BalanceAfterMicro int64  `json:"balanceAfterMicro"`
		Reference         string `json:"reference"`
		CreatedAt         string `json:"createdAt"`
	} `json:"transactions"`
}

// TestAuroraBillingBalanceReportsTheCallersWallet pins three things a naive
// implementation gets wrong: the field is present (a user with no wallet row
// still gets a number, not a missing key), it is the *caller's* wallet (the
// endpoints are account-scoped, so a shared or hardcoded balance would pass a
// single-user test), and it follows the ledger rather than drifting from it.
func TestAuroraBillingBalanceReportsTheCallersWallet(t *testing.T) {
	creditTestReset(t)
	ctx := context.Background()
	ws := parseUUID(testWorkspaceID)

	// A second wallet, deliberately funded differently, so a handler that read
	// the wrong account — or any account but the caller's — is visible.
	otherID := dbfx.User(t, "Aurora Billing Other", "aurora-billing-other@multica.test")
	creditTestResetUser(t, otherID)
	if err := testHandler.Credit.Grant(ctx, parseUUID(otherID), ws, 999, aurora.LedgerKindAdjustment, creditRefFor(otherID, "other-seed")); err != nil {
		t.Fatalf("Grant for other user: %v", err)
	}

	// No wallet row at all: 0, present.
	req := newRequest(http.MethodGet, "/api/aurora/billing/balance", nil)
	out := testutil.Decode[auroraBillingResponse](t, testHandler.GetAuroraBillingBalance, req, http.StatusOK)
	if out.AvailableMicro == nil {
		t.Fatal("availableMicro is absent, want 0 for a user with no wallet row")
	}
	if *out.AvailableMicro != 0 {
		t.Fatalf("availableMicro = %d for a fresh wallet, want 0", *out.AvailableMicro)
	}

	// Funded: the number tracks the ledger, and the other user's balance is
	// not what comes back.
	if err := testHandler.Credit.Grant(ctx, parseUUID(testUserID), ws, 2500, aurora.LedgerKindAdjustment, creditRef("seed")); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	out = testutil.Decode[auroraBillingResponse](t, testHandler.GetAuroraBillingBalance, req, http.StatusOK)
	if out.AvailableMicro == nil || *out.AvailableMicro != 2500 {
		t.Fatalf("availableMicro = %v after a 2500 grant, want 2500 (the caller's wallet, not another user's)", out.AvailableMicro)
	}
}

// TestAuroraBillingBalanceRequiresAuthentication covers the failure side of
// requireUserID: without an authenticated user there is no wallet to read, and
// defaulting to some other account's balance would leak it.
func TestAuroraBillingBalanceRequiresAuthentication(t *testing.T) {
	req := newRequest(http.MethodGet, "/api/aurora/billing/balance", nil)
	req.Header.Del("X-User-ID")
	testutil.Call(t, testHandler.GetAuroraBillingBalance, req).Want(http.StatusUnauthorized)
}

// TestAuroraBillingTransactionsListsTheCallersLedger checks the row shape the
// frontend schema consumes (packages/core/aurora/schema.ts), that the newest
// row comes first, and that the list is the caller's ledger only. The empty
// case is asserted too: `transactions` must be `[]`, not `null`, or the zod
// `.default([])` never fires and the UI renders against a null array.
func TestAuroraBillingTransactionsListsTheCallersLedger(t *testing.T) {
	creditTestReset(t)
	ctx := context.Background()
	user := parseUUID(testUserID)
	ws := parseUUID(testWorkspaceID)

	empty := testutil.Decode[auroraBillingResponse](t,
		testHandler.ListAuroraBillingTransactions,
		newRequest(http.MethodGet, "/api/aurora/billing/transactions?limit=50", nil),
		http.StatusOK)
	if empty.Transactions == nil {
		t.Fatal("transactions is null for an empty ledger, want an empty array")
	}
	if len(empty.Transactions) != 0 {
		t.Fatalf("transactions for an empty ledger = %d rows, want 0", len(empty.Transactions))
	}

	// Another user's row, which must not appear in the caller's list.
	otherID := dbfx.User(t, "Aurora Ledger Other", "aurora-ledger-other@multica.test")
	creditTestResetUser(t, otherID)
	if err := testHandler.Credit.Grant(ctx, parseUUID(otherID), ws, 999, aurora.LedgerKindAdjustment, creditRefFor(otherID, "other-seed")); err != nil {
		t.Fatalf("Grant for other user: %v", err)
	}

	gen := creditRef("gen-ledger")
	if err := testHandler.Credit.Grant(ctx, user, ws, 1000, aurora.LedgerKindAdjustment, creditRef("seed")); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	if err := testHandler.Credit.Reserve(ctx, user, ws, 300, gen); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if err := testHandler.Credit.Refund(ctx, user, ws, 300, gen); err != nil {
		t.Fatalf("Refund: %v", err)
	}

	out := testutil.Decode[auroraBillingResponse](t,
		testHandler.ListAuroraBillingTransactions,
		newRequest(http.MethodGet, "/api/aurora/billing/transactions?limit=50", nil),
		http.StatusOK)
	if len(out.Transactions) != 3 {
		t.Fatalf("transactions = %d rows, want 3 (grant, deduction, refund; not the other user's)", len(out.Transactions))
	}
	// Newest first: the refund was written last.
	wantOrder := []struct {
		kind      string
		amount    int64
		balance   int64
		reference string
	}{
		{aurora.LedgerKindRefund, 300, 1000, gen},
		{aurora.LedgerKindDeduction, -300, 700, gen},
		{aurora.LedgerKindAdjustment, 1000, 1000, creditRef("seed")},
	}
	for i, want := range wantOrder {
		got := out.Transactions[i]
		if got.ID == "" {
			t.Errorf("row %d: id is empty", i)
		}
		if got.Kind != want.kind || got.AmountMicro != want.amount || got.BalanceAfterMicro != want.balance || got.Reference != want.reference {
			t.Errorf("row %d = %+v, want kind=%s amount=%d balanceAfter=%d reference=%s",
				i, got, want.kind, want.amount, want.balance, want.reference)
		}
		if _, err := time.Parse(time.RFC3339Nano, got.CreatedAt); err != nil {
			t.Errorf("row %d: createdAt = %q, want RFC3339: %v", i, got.CreatedAt, err)
		}
	}
}

// TestAuroraBillingTransactionsLimit covers the ?limit contract. The ledger is
// seeded past the endpoint's default and maximum so each case is told apart by
// its row count — a smaller ledger would make "honoured", "defaulted" and
// "rejected" all return the same number and prove nothing.
func TestAuroraBillingTransactionsLimit(t *testing.T) {
	creditTestReset(t)

	// Seeded with raw SQL rather than the service: 60 wallet operations would
	// each open a transaction to prove something only the read path decides.
	dbfx.Exec(t, `
		INSERT INTO credit_ledger (user_id, workspace_id, kind, amount_micro, balance_after_micro, reference, idempotency_key)
		SELECT $1, $2, 'adjustment', 10, 10, 'seed-' || i, $3 || '-bulk-' || i
		FROM generate_series(1, 60) AS i`, testUserID, testWorkspaceID, testUserID)

	cases := []struct {
		name  string
		query string
		want  int
	}{
		{"honours a limit inside the range", "?limit=2", 2},
		{"honours the maximum", "?limit=200", 60},
		{"defaults when absent", "", 50},
		{"defaults on junk", "?limit=abc", 50},
		{"defaults on zero", "?limit=0", 50},
		{"defaults on a negative limit", "?limit=-5", 50},
		{"defaults above the maximum", "?limit=201", 50},
		{"defaults on an absurd limit", "?limit=100000", 50},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := testutil.Decode[auroraBillingResponse](t,
				testHandler.ListAuroraBillingTransactions,
				newRequest(http.MethodGet, "/api/aurora/billing/transactions"+tc.query, nil),
				http.StatusOK)
			if len(out.Transactions) != tc.want {
				t.Fatalf("transactions with %q = %d rows, want %d", tc.query, len(out.Transactions), tc.want)
			}
		})
	}
}

// TestAuroraBillingTransactionsRequiresAuthentication mirrors the balance
// endpoint's failure side.
func TestAuroraBillingTransactionsRequiresAuthentication(t *testing.T) {
	req := newRequest(http.MethodGet, "/api/aurora/billing/transactions", nil)
	req.Header.Del("X-User-ID")
	testutil.Call(t, testHandler.ListAuroraBillingTransactions, req).Want(http.StatusUnauthorized)
}

// TestAuroraBillingRejectsMalformedUserID covers the second half of resolving
// the caller. The auth middleware stamps X-User-ID from a validated token in
// production, but the handlers are also reachable from tests and from any
// future caller that sets the header itself, and the UUID rules in AGENTS.md
// require a request-boundary string to go through parseUUIDOrBadRequest. The
// trusted-input variant, parseUUID, panics on a malformed value — a 400 here
// is the difference between a rejected request and a crashed process.
func TestAuroraBillingRejectsMalformedUserID(t *testing.T) {
	handlers := map[string]http.HandlerFunc{
		"balance":      testHandler.GetAuroraBillingBalance,
		"transactions": testHandler.ListAuroraBillingTransactions,
	}
	for name, handler := range handlers {
		t.Run(name, func(t *testing.T) {
			req := newRequest(http.MethodGet, "/api/aurora/billing/"+name, nil)
			req.Header.Set("X-User-ID", "not-a-uuid")
			testutil.Call(t, handler, req).Want(http.StatusBadRequest)
		})
	}
}

// resetAuroraGenerations empties the workspace's generation and asset rows so a
// test starts from a known-empty state. Rows are test data only: the fixture
// deletes what it inserts, but a previous test may have aborted mid-run and
// left rows behind.
func resetAuroraGenerations(t *testing.T) {
	t.Helper()
	dbfx.Exec(t, `DELETE FROM aurora_generation WHERE workspace_id = $1`, testWorkspaceID)
	dbfx.Exec(t, `DELETE FROM aurora_asset WHERE workspace_id = $1`, testWorkspaceID)
}

// insertGeneration writes a queued generation row and returns its id.
func insertGeneration(t *testing.T, prompt string, over ...testutil.Cols) string {
	t.Helper()
	cols := testutil.Cols{
		"workspace_id": testWorkspaceID,
		"user_id":      testUserID,
		"skill_id":     "ecommerce-bidding-strategy",
		"prompt":       prompt,
		"status":       "queued",
	}
	for _, o := range over {
		maps.Copy(cols, o)
	}
	return dbfx.Insert(t, "aurora_generation", cols)
}

func TestListAuroraGenerationsOrdersAndPages(t *testing.T) {
	resetAuroraGenerations(t)

	oldest := insertGeneration(t, "oldest", testutil.Cols{"created_at": testutil.Raw("now() - interval '3 hours'")})
	middle := insertGeneration(t, "middle", testutil.Cols{"created_at": testutil.Raw("now() - interval '2 hours'")})
	newest := insertGeneration(t, "newest", testutil.Cols{"created_at": testutil.Raw("now() - interval '1 hour'")})

	list := func(path string) []string {
		out := testutil.Decode[struct {
			Generations []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"generations"`
		}](t, testHandler.ListAuroraGenerations, newRequest(http.MethodGet, path, nil), http.StatusOK)
		ids := make([]string, 0, len(out.Generations))
		for _, g := range out.Generations {
			if g.Status != "queued" {
				t.Fatalf("list status = %q, want queued", g.Status)
			}
			ids = append(ids, g.ID)
		}
		return ids
	}

	// Newest first.
	all := list("/api/aurora/generations?limit=50&offset=0")
	if len(all) != 3 || all[0] != newest || all[1] != middle || all[2] != oldest {
		t.Fatalf("order = %v, want [newest middle oldest]", all)
	}

	// Pagination boundary: limit caps the page, offset skips into it.
	page1 := list("/api/aurora/generations?limit=2&offset=0")
	if len(page1) != 2 || page1[0] != newest || page1[1] != middle {
		t.Fatalf("first page = %v, want [newest middle]", page1)
	}
	page2 := list("/api/aurora/generations?limit=2&offset=2")
	if len(page2) != 1 || page2[0] != oldest {
		t.Fatalf("second page = %v, want [oldest]", page2)
	}

	// Junk and out-of-range limits fall back to the default instead of erroring.
	list("/api/aurora/generations?limit=not-a-number&offset=0")
	list("/api/aurora/generations?limit=0&offset=0")
	list("/api/aurora/generations?limit=9999&offset=-5")
}

func TestListAuroraGenerationsExposesTaskID(t *testing.T) {
	resetAuroraGenerations(t)

	agentID := dbfx.Agent(t, "Aurora list task id agent", testRuntimeID)
	taskID := dbfx.Task(t, agentID, testutil.Cols{"status": "running", "runtime_id": testRuntimeID})
	withTask := insertGeneration(t, "has task", testutil.Cols{"task_id": taskID})
	withoutTask := insertGeneration(t, "no task")

	out := testutil.Decode[struct {
		Generations []struct {
			ID     string `json:"id"`
			TaskID string `json:"taskId"`
		} `json:"generations"`
	}](t, testHandler.ListAuroraGenerations, newRequest(http.MethodGet, "/api/aurora/generations", nil), http.StatusOK)

	byID := make(map[string]string, len(out.Generations))
	for _, g := range out.Generations {
		byID[g.ID] = g.TaskID
	}
	if byID[withTask] != taskID {
		t.Fatalf("taskId for generation with task = %q, want %q", byID[withTask], taskID)
	}
	if byID[withoutTask] != "" {
		t.Fatalf("taskId for generation without task = %q, want empty", byID[withoutTask])
	}
}

func TestGetAuroraGenerationIncludesAssets(t *testing.T) {
	resetAuroraGenerations(t)

	genID := insertGeneration(t, "detail prompt", testutil.Cols{
		"status":           "completed",
		"credits_reserved": 62_000_000,
		"credits_charged":  62_000_000,
	})
	dbfx.Insert(t, "aurora_asset", testutil.Cols{
		"generation_id": genID,
		"workspace_id":  testWorkspaceID,
		"kind":          "image",
		"media_url":     "https://cdn.example/out.png",
		"format":        "png",
	})

	req := withURLParam(newRequest(http.MethodGet, "/api/aurora/generations/{id}", nil), "id", genID)
	out := testutil.Decode[struct {
		Generation struct {
			ID              string  `json:"id"`
			SkillID         string  `json:"skillId"`
			Prompt          string  `json:"prompt"`
			Status          string  `json:"status"`
			CreditsReserved int64   `json:"creditsReserved"`
			CreditsCharged  int64   `json:"creditsCharged"`
			Error           *string `json:"error"`
			CreatedAt       string  `json:"createdAt"`
			Assets          []struct {
				ID           string  `json:"id"`
				GenerationID string  `json:"generationId"`
				Kind         string  `json:"kind"`
				MediaURL     *string `json:"mediaUrl"`
				Format       *string `json:"format"`
				CreatedAt    string  `json:"createdAt"`
			} `json:"assets"`
		} `json:"generation"`
	}](t, testHandler.GetAuroraGeneration, req, http.StatusOK)

	g := out.Generation
	if g.ID != genID || g.SkillID != "ecommerce-bidding-strategy" || g.Prompt != "detail prompt" {
		t.Fatalf("unexpected generation: %#v", g)
	}
	if g.Status != "completed" {
		t.Fatalf("status = %q, want completed", g.Status)
	}
	if g.CreditsReserved != 62_000_000 || g.CreditsCharged != 62_000_000 {
		t.Fatalf("credits = %d reserved / %d charged, want 62000000", g.CreditsReserved, g.CreditsCharged)
	}
	if g.Error != nil {
		t.Fatalf("error = %q, want null", *g.Error)
	}
	if g.CreatedAt == "" {
		t.Fatal("createdAt empty")
	}
	if len(g.Assets) != 1 {
		t.Fatalf("assets = %d, want 1", len(g.Assets))
	}
	a := g.Assets[0]
	if a.GenerationID != genID || a.Kind != "image" ||
		a.MediaURL == nil || *a.MediaURL != "https://cdn.example/out.png" ||
		a.Format == nil || *a.Format != "png" || a.CreatedAt == "" {
		t.Fatalf("unexpected asset: %#v", a)
	}
}

func TestGetAuroraGenerationDerivesStatusFromTask(t *testing.T) {
	cases := []struct {
		taskStatus string
		want       string
	}{
		{"queued", "queued"},
		{"dispatched", "running"},
		{"running", "running"},
		{"waiting_local_directory", "running"},
		{"deferred", "running"},
		{"completed", "completed"},
		{"failed", "failed"},
		{"cancelled", "failed"},
	}
	for _, tc := range cases {
		t.Run(tc.taskStatus, func(t *testing.T) {
			agentID := dbfx.Agent(t, "Aurora derive "+tc.taskStatus, testRuntimeID)
			taskID := dbfx.Task(t, agentID, testutil.Cols{"status": tc.taskStatus, "runtime_id": testRuntimeID})
			genID := insertGeneration(t, "derive "+tc.taskStatus, testutil.Cols{"task_id": taskID})

			req := withURLParam(newRequest(http.MethodGet, "/api/aurora/generations/{id}", nil), "id", genID)
			out := testutil.Decode[struct {
				Generation struct {
					Status string `json:"status"`
					TaskID string `json:"taskId"`
				} `json:"generation"`
			}](t, testHandler.GetAuroraGeneration, req, http.StatusOK)

			if out.Generation.Status != tc.want {
				t.Fatalf("status for task %q = %q, want %q", tc.taskStatus, out.Generation.Status, tc.want)
			}
			// The detail read exposes the enqueued task so the client can link to it.
			if out.Generation.TaskID != taskID {
				t.Fatalf("taskId = %q, want %q", out.Generation.TaskID, taskID)
			}
		})
	}
}

func TestGetAuroraGenerationNotFound(t *testing.T) {
	// Malformed id → 400, not a crash.
	req := withURLParam(newRequest(http.MethodGet, "/api/aurora/generations/{id}", nil), "id", "not-a-uuid")
	testutil.Call(t, testHandler.GetAuroraGeneration, req).Want(http.StatusBadRequest)

	// Well-formed but absent → 404.
	req = withURLParam(newRequest(http.MethodGet, "/api/aurora/generations/{id}", nil), "id", parseUUID(testUserID).String())
	testutil.Call(t, testHandler.GetAuroraGeneration, req).Want(http.StatusNotFound)

	// A generation in another workspace is invisible: the query is scoped by
	// workspace_id, not just id.
	otherWS := dbfx.Workspace(t, "Aurora other workspace", "aurora-gen-other-ws")
	genID := dbfx.Insert(t, "aurora_generation", testutil.Cols{
		"workspace_id": otherWS,
		"user_id":      testUserID,
		"skill_id":     "ecommerce-bidding-strategy",
		"prompt":       "other workspace",
		"status":       "queued",
	})
	req = withURLParam(newRequest(http.MethodGet, "/api/aurora/generations/{id}", nil), "id", genID)
	testutil.Call(t, testHandler.GetAuroraGeneration, req).Want(http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// Assets — GET /api/aurora/assets, its download, and DELETE
// ---------------------------------------------------------------------------

// auroraAssetBody is the wire shape of one asset, shared by the library list
// and the generation detail endpoint.
type auroraAssetBody struct {
	ID           string  `json:"id"`
	GenerationID string  `json:"generationId"`
	Kind         string  `json:"kind"`
	MediaURL     *string `json:"mediaUrl"`
	Format       *string `json:"format"`
	CreatedAt    string  `json:"createdAt"`
}

type auroraAssetListBody struct {
	Assets []auroraAssetBody `json:"assets"`
}

// insertAsset writes an aurora_asset row and returns its id.
func insertAsset(t *testing.T, generationID string, over ...testutil.Cols) string {
	t.Helper()
	cols := testutil.Cols{
		"generation_id": generationID,
		"workspace_id":  testWorkspaceID,
		"kind":          "image",
		"media_url":     "https://cdn.example.com/aurora/out.png",
		"format":        "png",
	}
	for _, o := range over {
		maps.Copy(cols, o)
	}
	return dbfx.Insert(t, "aurora_asset", cols)
}

// insertAssetInOtherWorkspace writes an asset belonging to a workspace the
// caller is not a member of, with its own generation row. Both are invisible
// to the asset endpoints, which is what the cross-workspace cases assert. The
// slug carries a random suffix because workspace slugs are unique: a run that
// aborts before teardown leaves its workspace behind, and a fixed slug would
// make every later run fail on the insert rather than on its own assertion.
func insertAssetInOtherWorkspace(t *testing.T, slug string) string {
	t.Helper()
	otherWS := dbfx.Workspace(t, "Aurora asset workspace "+slug, slug+"-"+uuid.NewString())
	genID := dbfx.Insert(t, "aurora_generation", testutil.Cols{
		"workspace_id": otherWS,
		"user_id":      testUserID,
		"skill_id":     "ecommerce-bidding-strategy",
		"prompt":       "other workspace asset",
		"status":       "completed",
	})
	return dbfx.Insert(t, "aurora_asset", testutil.Cols{
		"generation_id": genID,
		"workspace_id":  otherWS,
		"kind":          "image",
		"media_url":     "https://cdn.example.com/aurora/secret.png",
		"format":        "png",
	})
}

// withAuroraAssetStorage points the shared handler at an in-memory storage
// backend and restores the previous wiring at cleanup. The shared test handler
// is built without storage, and only the asset download and delete paths reach
// for h.Storage.
func withAuroraAssetStorage(t *testing.T, store storage.Storage) {
	t.Helper()
	origStorage := testHandler.Storage
	origCfg := testHandler.cfg
	origSigner := testHandler.CFSigner
	testHandler.Storage = store
	testHandler.cfg.AttachmentDownloadMode = ""
	testHandler.CFSigner = nil
	t.Cleanup(func() {
		testHandler.Storage = origStorage
		testHandler.cfg = origCfg
		testHandler.CFSigner = origSigner
	})
}

// deleteFailingStorage refuses every object delete, standing in for a storage
// backend that is unreachable at the moment an asset row would be removed.
type deleteFailingStorage struct{ mockStorage }

func (s *deleteFailingStorage) DeleteObject(context.Context, string) error {
	return errors.New("storage unavailable")
}

func listAuroraAssets(t *testing.T, path string, want int) []auroraAssetBody {
	t.Helper()
	return testutil.Decode[auroraAssetListBody](t, testHandler.ListAuroraAssets, newRequest(http.MethodGet, path, nil), want).Assets
}

func TestAuroraAssetListFiltersByGeneration(t *testing.T) {
	resetAuroraGenerations(t)

	genA := insertGeneration(t, "assets A")
	genB := insertGeneration(t, "assets B")
	older := insertAsset(t, genA, testutil.Cols{
		"created_at": testutil.Raw("now() - interval '2 hours'"),
		"kind":       "image",
		"media_url":  "https://cdn.example.com/aurora/older.png",
		"format":     "png",
	})
	newer := insertAsset(t, genA, testutil.Cols{
		"created_at": testutil.Raw("now() - interval '1 hour'"),
		"kind":       "video",
		"media_url":  "https://cdn.example.com/aurora/newer.mp4",
		"format":     "mp4",
	})
	other := insertAsset(t, genB, testutil.Cols{
		"kind":      "document",
		"media_url": "https://cdn.example.com/aurora/other.pdf",
		"format":    "pdf",
	})

	// The unfiltered list is the whole workspace's library, newest first.
	all := listAuroraAssets(t, "/api/aurora/assets?limit=50&offset=0", http.StatusOK)
	if len(all) != 3 || all[0].ID != other || all[1].ID != newer || all[2].ID != older {
		t.Fatalf("workspace order = %v, want [%s %s %s]", assetIDs(all), other, newer, older)
	}

	// generationId narrows the list to one generation's output.
	genAPage := listAuroraAssets(t, "/api/aurora/assets?generationId="+genA+"&limit=50", http.StatusOK)
	if len(genAPage) != 2 || genAPage[0].ID != newer || genAPage[1].ID != older {
		t.Fatalf("generation %s page = %v, want [%s %s]", genA, assetIDs(genAPage), newer, older)
	}
	a := genAPage[0]
	if a.GenerationID != genA || a.Kind != "video" ||
		a.MediaURL == nil || *a.MediaURL != "https://cdn.example.com/aurora/newer.mp4" ||
		a.Format == nil || *a.Format != "mp4" || a.CreatedAt == "" {
		t.Fatalf("unexpected asset: %#v", a)
	}

	genBPage := listAuroraAssets(t, "/api/aurora/assets?generationId="+genB, http.StatusOK)
	if len(genBPage) != 1 || genBPage[0].ID != other {
		t.Fatalf("generation %s page = %v, want [%s]", genB, assetIDs(genBPage), other)
	}

	// Junk and out-of-range paging falls back to the default page rather than
	// erroring, the same contract the generation list holds.
	if len(listAuroraAssets(t, "/api/aurora/assets?limit=not-a-number&offset=-1", http.StatusOK)) != 3 {
		t.Fatal("junk limit did not fall back to the default page")
	}

	// A malformed generation id is a client bug, not an empty library.
	listAuroraAssets(t, "/api/aurora/assets?generationId=not-a-uuid", http.StatusBadRequest)

	// Another workspace's assets stay invisible both with and without a
	// generation filter: the workspace predicate is bound on every path, so
	// naming a foreign generation narrows the caller's list to nothing rather
	// than widening it to that generation's output.
	foreignAsset := insertAssetInOtherWorkspace(t, "aurora-asset-list-other-ws")
	if got := listAuroraAssets(t, "/api/aurora/assets?generationId="+foreignAsset, http.StatusOK); len(got) != 0 {
		t.Fatalf("cross-workspace generation filter returned %v, want none", assetIDs(got))
	}
	if got := listAuroraAssets(t, "/api/aurora/assets?limit=50", http.StatusOK); len(got) != 3 {
		t.Fatalf("unfiltered list = %v, want only this workspace's three assets", assetIDs(got))
	}
}

func assetIDs(assets []auroraAssetBody) []string {
	ids := make([]string, 0, len(assets))
	for _, a := range assets {
		ids = append(ids, a.ID)
	}
	return ids
}

func TestAuroraAssetDownloadRedirectsToSignedURL(t *testing.T) {
	resetAuroraGenerations(t)
	withAuroraAssetStorage(t, &mockStorage{})

	genID := insertGeneration(t, "download")
	assetID := insertAsset(t, genID, testutil.Cols{"media_url": "https://cdn.example.com/aurora/out.png"})

	req := withURLParam(newRequest(http.MethodGet, "/api/aurora/assets/{id}/download", nil), "id", assetID)
	w := testutil.Call(t, testHandler.DownloadAuroraAsset, req).Want(http.StatusFound)

	loc, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	if loc.Query().Get("X-Amz-Signature") == "" {
		t.Fatalf("Location = %q, want a signed storage URL", loc.String())
	}
	// Only the object's key is signed — signing the stored URL would hand a
	// hosted-object URL back to the client verbatim.
	if loc.Path != "/aurora/out.png" {
		t.Fatalf("signed path = %q, want /aurora/out.png", loc.Path)
	}
	// A download saves the file rather than previewing it.
	if disposition := loc.Query().Get("response-content-disposition"); !strings.HasPrefix(disposition, "attachment") || !strings.Contains(disposition, "out.png") {
		t.Fatalf("response-content-disposition = %q, want a forced attachment for out.png", disposition)
	}
}

func TestAuroraAssetDownloadNotFound(t *testing.T) {
	resetAuroraGenerations(t)
	withAuroraAssetStorage(t, &mockStorage{})

	// Malformed id → 400, not a crash.
	req := withURLParam(newRequest(http.MethodGet, "/api/aurora/assets/{id}/download", nil), "id", "not-a-uuid")
	testutil.Call(t, testHandler.DownloadAuroraAsset, req).Want(http.StatusBadRequest)

	// Well-formed but absent → 404.
	req = withURLParam(newRequest(http.MethodGet, "/api/aurora/assets/{id}/download", nil), "id", parseUUID(testUserID).String())
	testutil.Call(t, testHandler.DownloadAuroraAsset, req).Want(http.StatusNotFound)

	// An asset whose row never got a media_url has no object to serve.
	genID := insertGeneration(t, "download without media")
	emptyURL := insertAsset(t, genID, testutil.Cols{"media_url": ""})
	req = withURLParam(newRequest(http.MethodGet, "/api/aurora/assets/{id}/download", nil), "id", emptyURL)
	testutil.Call(t, testHandler.DownloadAuroraAsset, req).Want(http.StatusNotFound)

	// Another workspace's asset is invisible: the lookup is scoped by
	// workspace_id, so its id is not an existence oracle.
	foreignAsset := insertAssetInOtherWorkspace(t, "aurora-asset-download-other-ws")
	req = withURLParam(newRequest(http.MethodGet, "/api/aurora/assets/{id}/download", nil), "id", foreignAsset)
	testutil.Call(t, testHandler.DownloadAuroraAsset, req).Want(http.StatusNotFound)
}

// TestAuroraAssetDownloadStreamsWhenThereIsNoSignedURL covers the deployment
// shape with no signable URL to redirect to (local disk, private object host):
// the object is streamed through the API instead, with the same forced-
// attachment disposition the redirect path ends up with.
func TestAuroraAssetDownloadStreamsWhenThereIsNoSignedURL(t *testing.T) {
	resetAuroraGenerations(t)
	body := []byte("id,total\n1,42\n")
	store := &mockStorage{files: map[string][]byte{"aurora/report.csv": body}}
	withAuroraAssetStorage(t, store)
	testHandler.cfg.AttachmentDownloadMode = "proxy"

	genID := insertGeneration(t, "stream download")
	assetID := insertAsset(t, genID, testutil.Cols{
		"kind":      "document",
		"media_url": "https://cdn.example.com/aurora/report.csv",
		"format":    "csv",
	})

	req := withURLParam(newRequest(http.MethodGet, "/api/aurora/assets/{id}/download", nil), "id", assetID)
	w := testutil.Call(t, testHandler.DownloadAuroraAsset, req).Want(http.StatusOK)
	if !bytes.Equal(w.Body.Bytes(), body) {
		t.Fatalf("body = %q, want the stored object", w.Body.String())
	}
	if disposition := w.Header().Get("Content-Disposition"); !strings.HasPrefix(disposition, "attachment") {
		t.Fatalf("Content-Disposition = %q, want a forced attachment", disposition)
	}

	// The row points at an object the backend no longer has: 404, not an empty
	// 200 file the user would save as garbage.
	missing := insertAsset(t, genID, testutil.Cols{"media_url": "https://cdn.example.com/aurora/gone.png"})
	req = withURLParam(newRequest(http.MethodGet, "/api/aurora/assets/{id}/download", nil), "id", missing)
	testutil.Call(t, testHandler.DownloadAuroraAsset, req).Want(http.StatusNotFound)
}

func TestAuroraAssetDeleteRemovesRowAndObject(t *testing.T) {
	resetAuroraGenerations(t)
	store := &mockStorage{files: map[string][]byte{"aurora/doomed.png": []byte("bytes")}}
	withAuroraAssetStorage(t, store)

	genID := insertGeneration(t, "delete me")
	assetID := insertAsset(t, genID, testutil.Cols{"media_url": "https://cdn.example.com/aurora/doomed.png"})

	req := withURLParam(newRequest(http.MethodDelete, "/api/aurora/assets/{id}", nil), "id", assetID)
	testutil.Call(t, testHandler.DeleteAuroraAsset, req).Want(http.StatusNoContent)

	// The row is gone from the generation's asset list...
	if got := listAuroraAssets(t, "/api/aurora/assets?generationId="+genID, http.StatusOK); len(got) != 0 {
		t.Fatalf("assets after delete = %v, want none", assetIDs(got))
	}
	// ...and so is the stored object: no cascade exists between them, so
	// leaving the file behind would strand it with nothing pointing at it.
	if _, ok := store.files["aurora/doomed.png"]; ok {
		t.Fatal("stored object survived the asset delete")
	}

	// Deleting again is a 404: the asset is already gone.
	testutil.Call(t, testHandler.DeleteAuroraAsset, req).Want(http.StatusNotFound)

	// Malformed id → 400.
	req = withURLParam(newRequest(http.MethodDelete, "/api/aurora/assets/{id}", nil), "id", "not-a-uuid")
	testutil.Call(t, testHandler.DeleteAuroraAsset, req).Want(http.StatusBadRequest)
}

func TestAuroraAssetDeleteLeavesOtherWorkspacesAlone(t *testing.T) {
	resetAuroraGenerations(t)
	withAuroraAssetStorage(t, &mockStorage{})

	foreignAsset := insertAssetInOtherWorkspace(t, "aurora-asset-delete-other-ws")
	req := withURLParam(newRequest(http.MethodDelete, "/api/aurora/assets/{id}", nil), "id", foreignAsset)
	testutil.Call(t, testHandler.DeleteAuroraAsset, req).Want(http.StatusNotFound)

	if n := dbfx.Count(t, `SELECT count(*) FROM aurora_asset WHERE id = $1`, foreignAsset); n != 1 {
		t.Fatalf("cross-workspace delete removed the row (count = %d, want 1)", n)
	}
}

// TestAuroraAssetDeleteWithoutStorage covers a deployment whose storage is not
// wired up: there is no object to reclaim, but the row still goes, so the
// library is not permanently stuck with an asset nobody can remove.
func TestAuroraAssetDeleteWithoutStorage(t *testing.T) {
	resetAuroraGenerations(t)

	genID := insertGeneration(t, "delete without storage")
	assetID := insertAsset(t, genID)

	req := withURLParam(newRequest(http.MethodDelete, "/api/aurora/assets/{id}", nil), "id", assetID)
	testutil.Call(t, testHandler.DeleteAuroraAsset, req).Want(http.StatusNoContent)

	if n := dbfx.Count(t, `SELECT count(*) FROM aurora_asset WHERE id = $1`, assetID); n != 0 {
		t.Fatalf("row survived a delete with no storage configured (count = %d, want 0)", n)
	}
}

// TestAuroraAssetDeleteKeepsRowWhenObjectDeleteFails pins the failure order:
// the row is the only record that the object exists, so an object delete that
// cannot be completed must fail the request and leave the row for a retry
// rather than deleting the row and stranding the file.
func TestAuroraAssetDeleteKeepsRowWhenObjectDeleteFails(t *testing.T) {
	resetAuroraGenerations(t)
	withAuroraAssetStorage(t, &deleteFailingStorage{})

	genID := insertGeneration(t, "delete failure")
	assetID := insertAsset(t, genID)

	req := withURLParam(newRequest(http.MethodDelete, "/api/aurora/assets/{id}", nil), "id", assetID)
	testutil.Call(t, testHandler.DeleteAuroraAsset, req).Want(http.StatusInternalServerError)

	if n := dbfx.Count(t, `SELECT count(*) FROM aurora_asset WHERE id = $1`, assetID); n != 1 {
		t.Fatalf("row removed despite the storage failure (count = %d, want 1)", n)
	}
}

// ---------------------------------------------------------------------------
// Billing — checkout and the Stripe webhook (Plan 5 Task 3)
// ---------------------------------------------------------------------------

// fakePayments stands in for Stripe. The handler tests never reach the network:
// checkout URLs and period ends are whatever the test set, and webhook events
// come from a queue the test fills, so a test can drive an event sequence
// without a signing secret or a real payload.
type fakePayments struct {
	checkoutURL        string
	portalURL          string
	periodEnd          time.Time
	subscriptionStatus string
	cancelAtPeriodEnd  bool
	events             []aurora.Event
	// subscriptionCheckoutKeys records the stable key the handler asks Stripe to
	// use, so tests can prove retries cannot create a second subscription.
	subscriptionCheckoutKeys []string
	// portalCustomerIDs records which Stripe customer the handler asked the
	// portal to manage, so a test can prove the row's id was passed through.
	portalCustomerIDs []string
	// checkoutErr, when set, is what both checkout methods return instead of a
	// URL — the shape of a Stripe outage.
	checkoutErr error
	// portalErr, when set, is what CreateBillingPortalSession returns instead
	// of a URL.
	portalErr error
	// eventErr, when set, is what ConstructEvent returns — an invalid
	// signature, without needing to forge one.
	eventErr error
}

func (f *fakePayments) CreateSubscriptionCheckout(ctx context.Context, priceID, successURL, cancelURL, userID, tier, checkoutID string) (string, error) {
	f.subscriptionCheckoutKeys = append(f.subscriptionCheckoutKeys, checkoutID)
	if f.checkoutErr != nil {
		return "", f.checkoutErr
	}
	return f.checkoutURL, nil
}

func (f *fakePayments) CreateTopupCheckout(ctx context.Context, priceID, successURL, cancelURL, userID, topupID string) (string, error) {
	if f.checkoutErr != nil {
		return "", f.checkoutErr
	}
	return f.checkoutURL, nil
}

func (f *fakePayments) CreateBillingPortalSession(ctx context.Context, customerID, returnURL string) (string, error) {
	f.portalCustomerIDs = append(f.portalCustomerIDs, customerID)
	if f.portalErr != nil {
		return "", f.portalErr
	}
	return f.portalURL, nil
}

func (f *fakePayments) GetSubscriptionState(ctx context.Context, subscriptionID string) (aurora.SubscriptionState, error) {
	status := f.subscriptionStatus
	if status == "" {
		status = "active"
	}
	return aurora.SubscriptionState{
		Status:            status,
		CurrentPeriodEnd:  f.periodEnd,
		CancelAtPeriodEnd: f.cancelAtPeriodEnd,
	}, nil
}

func (f *fakePayments) ConstructEvent(payload []byte, sigHeader string) (aurora.Event, error) {
	if f.eventErr != nil {
		return aurora.Event{}, f.eventErr
	}
	if len(f.events) == 0 {
		return aurora.Event{}, errors.New("fakePayments: no queued event")
	}
	event := f.events[0]
	f.events = f.events[1:]
	return event, nil
}

// installAuroraPayments swaps in a fake provider and a catalog that actually
// has price ids. The suite's shared handler is built without Stripe config, so
// its catalog's price ids are empty and every checkout would fail closed — the
// tests that assert a checkout succeeds must supply priced tiers, and the tests
// that assert fail-closed behavior keep the shared handler as it is.
func installAuroraPayments(t *testing.T, fp aurora.PaymentProvider) {
	t.Helper()
	oldPayments, oldTiers := testHandler.Payments, testHandler.Tiers
	oldCheckoutReturnOrigins := testHandler.cfg.CheckoutReturnOrigins
	testHandler.Payments = fp
	testHandler.Tiers = aurora.NewTierCatalog(
		"price_creator_monthly", "price_creator_yearly",
		"price_pro_monthly", "price_business_monthly", "price_pro_yearly",
		"price_topup_5", "price_topup_20",
	)
	testHandler.cfg.CheckoutReturnOrigins = []string{"https://aurora.example.com"}
	t.Cleanup(func() {
		testHandler.Payments = oldPayments
		testHandler.Tiers = oldTiers
		testHandler.cfg.CheckoutReturnOrigins = oldCheckoutReturnOrigins
	})
}

// auroraSubscriptionTestReset empties the fixture user's subscription row so a
// test starts from "never subscribed" and leaves nothing behind for the next
// one. aurora_subscription carries no foreign key to user, so the fixture's own
// cleanup would not remove it.
func auroraSubscriptionTestReset(t *testing.T) {
	t.Helper()
	reset := func() {
		if _, err := testPool.Exec(context.Background(),
			`DELETE FROM aurora_subscription WHERE user_id = $1`, testUserID); err != nil {
			t.Fatalf("reset aurora_subscription: %v", err)
		}
	}
	reset()
	t.Cleanup(reset)
}

// auroraSubscriptionRow reads the fixture user's subscription row, failing the
// test when there is none.
func auroraSubscriptionRow(t *testing.T) (tier, status string, periodEnd *time.Time) {
	t.Helper()
	var end *time.Time
	if err := testPool.QueryRow(context.Background(),
		`SELECT tier, status, current_period_end FROM aurora_subscription WHERE user_id = $1`,
		testUserID).Scan(&tier, &status, &end); err != nil {
		t.Fatalf("load aurora_subscription row: %v", err)
	}
	return tier, status, end
}

// postStripeWebhook drives the public webhook endpoint. The signature header is
// present but meaningless: the fake provider is what decides whether the event
// verifies, so these tests exercise routing and accounting, not crypto.
func postStripeWebhook(t *testing.T, raw string, want int) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/aurora/billing/stripe/webhook", strings.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Stripe-Signature", "t=1,v1=fake")
	testutil.Call(t, testHandler.StripeWebhook, req).Want(want)
}

// checkoutBody is the request body the billing checkout endpoints accept.
func checkoutBody(extra map[string]string) map[string]string {
	body := map[string]string{
		"successUrl": "https://aurora.example.com/ws-1/billing?checkout=success",
		"cancelUrl":  "https://aurora.example.com/ws-1/billing?checkout=cancel",
	}
	maps.Copy(body, extra)
	return body
}

func TestCreateSubscriptionCheckout(t *testing.T) {
	auroraSubscriptionTestReset(t)
	fp := &fakePayments{checkoutURL: "https://checkout.stripe.com/c/test"}
	installAuroraPayments(t, fp)

	req := newRequest(http.MethodPost, "/api/aurora/billing/checkout", checkoutBody(map[string]string{
		"tier": "creator", "billingCycle": "monthly",
	}))
	out := testutil.Decode[struct {
		CheckoutURL string `json:"checkoutUrl"`
	}](t, testHandler.CreateAuroraCheckout, req, http.StatusOK)
	if out.CheckoutURL != fp.checkoutURL {
		t.Fatalf("checkoutUrl = %q, want %q", out.CheckoutURL, fp.checkoutURL)
	}
}

func TestCreateSubscriptionCheckoutReusesPendingIntent(t *testing.T) {
	auroraSubscriptionTestReset(t)
	fp := &fakePayments{checkoutURL: "https://checkout.stripe.com/c/test"}
	installAuroraPayments(t, fp)

	for range 2 {
		req := newRequest(http.MethodPost, "/api/aurora/billing/checkout", checkoutBody(map[string]string{
			"tier": "creator", "billingCycle": "monthly",
		}))
		testutil.Call(t, testHandler.CreateAuroraCheckout, req).Want(http.StatusOK)
	}
	if len(fp.subscriptionCheckoutKeys) != 2 {
		t.Fatalf("Stripe checkout calls = %d, want 2 retries", len(fp.subscriptionCheckoutKeys))
	}
	if fp.subscriptionCheckoutKeys[0] == "" || fp.subscriptionCheckoutKeys[0] != fp.subscriptionCheckoutKeys[1] {
		t.Fatalf("checkout idempotency keys = %q, want one stable non-empty key", fp.subscriptionCheckoutKeys)
	}
}

func TestCreateSubscriptionCheckoutDoesNotReplacePotentiallyLiveStripeSession(t *testing.T) {
	auroraSubscriptionTestReset(t)
	fp := &fakePayments{checkoutURL: "https://checkout.stripe.com/c/test"}
	installAuroraPayments(t, fp)
	dbfx.Insert(t, "aurora_subscription", testutil.Cols{
		"user_id": testUserID, "tier": "creator", "status": "pending",
		"checkout_idempotency_key": "aurora-sub-existing",
		"checkout_billing_cycle":   "monthly",
		"updated_at":               time.Now().Add(-24*time.Hour - 30*time.Minute),
	})

	req := newRequest(http.MethodPost, "/api/aurora/billing/checkout", checkoutBody(map[string]string{
		"tier": "pro", "billingCycle": "monthly",
	}))
	testutil.Call(t, testHandler.CreateAuroraCheckout, req).Want(http.StatusConflict)
	if len(fp.subscriptionCheckoutKeys) != 0 {
		t.Fatalf("Stripe checkout calls = %d, want none while the previous session may still complete", len(fp.subscriptionCheckoutKeys))
	}
}

func TestCreateSubscriptionCheckoutRequiresReturnURLs(t *testing.T) {
	installAuroraPayments(t, &fakePayments{checkoutURL: "https://checkout.stripe.com/c/test"})
	for name, body := range map[string]map[string]string{
		"missing successUrl": {"tier": "creator", "cancelUrl": "https://aurora.example.com/cancel"},
		"missing cancelUrl":  {"tier": "creator", "successUrl": "https://aurora.example.com/success"},
		"relative url":       {"tier": "creator", "successUrl": "/success", "cancelUrl": "/cancel"},
		"javascript url":     {"tier": "creator", "successUrl": "javascript:alert(1)", "cancelUrl": "https://aurora.example.com/cancel"},
		"userinfo":           {"tier": "creator", "successUrl": "https://user@aurora.example.com/success", "cancelUrl": "https://aurora.example.com/cancel"},
		"wrong port":         {"tier": "creator", "successUrl": "https://aurora.example.com:444/success", "cancelUrl": "https://aurora.example.com/cancel"},
		"wrong scheme":       {"tier": "creator", "successUrl": "http://aurora.example.com/success", "cancelUrl": "https://aurora.example.com/cancel"},
	} {
		t.Run(name, func(t *testing.T) {
			req := newRequest(http.MethodPost, "/api/aurora/billing/checkout", body)
			testutil.Call(t, testHandler.CreateAuroraCheckout, req).Want(http.StatusBadRequest)
		})
	}
}

func TestCreateSubscriptionCheckoutRejectsUntrustedReturnOrigin(t *testing.T) {
	auroraSubscriptionTestReset(t)
	installAuroraPayments(t, &fakePayments{checkoutURL: "https://checkout.stripe.com/c/test"})
	req := newRequest(http.MethodPost, "/api/aurora/billing/checkout", checkoutBody(map[string]string{
		"tier": "creator", "billingCycle": "monthly",
		"successUrl": "https://evil.example.com/steal-checkout",
	}))
	testutil.Call(t, testHandler.CreateAuroraCheckout, req).Want(http.StatusBadRequest)
}

// A deployment with no Stripe keys must not pretend it can take money: the
// endpoint answers 503 rather than a checkout URL nothing will honour.
func TestCreateSubscriptionCheckoutFailsClosedWithoutPayments(t *testing.T) {
	old := testHandler.Payments
	testHandler.Payments = nil
	t.Cleanup(func() { testHandler.Payments = old })

	req := newRequest(http.MethodPost, "/api/aurora/billing/checkout", checkoutBody(map[string]string{
		"tier": "creator", "billingCycle": "monthly",
	}))
	testutil.Call(t, testHandler.CreateAuroraCheckout, req).Want(http.StatusServiceUnavailable)
}

func TestCreateSubscriptionCheckoutRejectsUnknownTier(t *testing.T) {
	installAuroraPayments(t, &fakePayments{checkoutURL: "https://checkout.stripe.com/c/test"})
	for _, tier := range []string{"enterprise", "free", ""} {
		req := newRequest(http.MethodPost, "/api/aurora/billing/checkout", checkoutBody(map[string]string{
			"tier": tier, "billingCycle": "monthly",
		}))
		testutil.Call(t, testHandler.CreateAuroraCheckout, req).Want(http.StatusBadRequest)
	}
}

func TestCreateSubscriptionCheckoutRejectsUnknownBillingCycle(t *testing.T) {
	auroraSubscriptionTestReset(t)
	installAuroraPayments(t, &fakePayments{checkoutURL: "https://checkout.stripe.com/c/test"})
	for _, cycle := range []string{"", "month", "weekly"} {
		req := newRequest(http.MethodPost, "/api/aurora/billing/checkout", checkoutBody(map[string]string{
			"tier": "creator", "billingCycle": cycle,
		}))
		testutil.Call(t, testHandler.CreateAuroraCheckout, req).Want(http.StatusBadRequest)
	}
}

// A tier whose price id is not configured fails closed rather than charging
// whatever price Stripe's default happens to be.
func TestCreateSubscriptionCheckoutRejectsUnconfiguredPrice(t *testing.T) {
	installAuroraPayments(t, &fakePayments{checkoutURL: "https://checkout.stripe.com/c/test"})
	testHandler.Tiers = aurora.NewTierCatalog("", "", "", "", "", "", "")

	req := newRequest(http.MethodPost, "/api/aurora/billing/checkout", checkoutBody(map[string]string{
		"tier": "creator", "billingCycle": "monthly",
	}))
	testutil.Call(t, testHandler.CreateAuroraCheckout, req).Want(http.StatusServiceUnavailable)
}

// MVP semantics: no plan switching while a live subscription exists. The user
// is told they already have one rather than being charged twice.
func TestCreateSubscriptionCheckoutRejectsActiveSubscription(t *testing.T) {
	auroraSubscriptionTestReset(t)
	installAuroraPayments(t, &fakePayments{checkoutURL: "https://checkout.stripe.com/c/test"})
	dbfx.Insert(t, "aurora_subscription", testutil.Cols{
		"user_id": testUserID, "tier": "creator", "status": "active",
	})

	req := newRequest(http.MethodPost, "/api/aurora/billing/checkout", checkoutBody(map[string]string{
		"tier": "pro", "billingCycle": "monthly",
	}))
	testutil.Call(t, testHandler.CreateAuroraCheckout, req).Want(http.StatusConflict)
}

func TestCreateSubscriptionCheckoutRejectsUnknownSubscriptionState(t *testing.T) {
	auroraSubscriptionTestReset(t)
	installAuroraPayments(t, &fakePayments{checkoutURL: "https://checkout.stripe.com/c/test"})
	dbfx.Insert(t, "aurora_subscription", testutil.Cols{
		"user_id": testUserID, "tier": "creator", "status": "paused",
	})

	req := newRequest(http.MethodPost, "/api/aurora/billing/checkout", checkoutBody(map[string]string{
		"tier": "pro", "billingCycle": "monthly",
	}))
	testutil.Call(t, testHandler.CreateAuroraCheckout, req).Want(http.StatusConflict)
}

// A canceled subscription is not a live one, so the user may subscribe again —
// the webhook's upsert overwrites the canceled row.
func TestCreateSubscriptionCheckoutAllowsResubscribeAfterCancel(t *testing.T) {
	auroraSubscriptionTestReset(t)
	fp := &fakePayments{checkoutURL: "https://checkout.stripe.com/c/test"}
	installAuroraPayments(t, fp)
	dbfx.Insert(t, "aurora_subscription", testutil.Cols{
		"user_id": testUserID, "tier": "creator", "status": "canceled",
	})

	req := newRequest(http.MethodPost, "/api/aurora/billing/checkout", checkoutBody(map[string]string{
		"tier": "creator", "billingCycle": "monthly",
	}))
	out := testutil.Decode[struct {
		CheckoutURL string `json:"checkoutUrl"`
	}](t, testHandler.CreateAuroraCheckout, req, http.StatusOK)
	if out.CheckoutURL != fp.checkoutURL {
		t.Fatalf("checkoutUrl = %q, want %q", out.CheckoutURL, fp.checkoutURL)
	}
}

func TestCreateTopupCheckout(t *testing.T) {
	fp := &fakePayments{checkoutURL: "https://checkout.stripe.com/c/topup"}
	installAuroraPayments(t, fp)

	req := newRequest(http.MethodPost, "/api/aurora/billing/topup/checkout", checkoutBody(map[string]string{
		"topupId": "t5",
	}))
	out := testutil.Decode[struct {
		CheckoutURL string `json:"checkoutUrl"`
	}](t, testHandler.CreateAuroraTopupCheckout, req, http.StatusOK)
	if out.CheckoutURL != fp.checkoutURL {
		t.Fatalf("checkoutUrl = %q, want %q", out.CheckoutURL, fp.checkoutURL)
	}
}

func TestCreateTopupCheckoutRejectsUntrustedReturnOrigin(t *testing.T) {
	installAuroraPayments(t, &fakePayments{checkoutURL: "https://checkout.stripe.com/c/topup"})
	req := newRequest(http.MethodPost, "/api/aurora/billing/topup/checkout", checkoutBody(map[string]string{
		"topupId":   "t5",
		"cancelUrl": "https://evil.example.com/steal-checkout",
	}))
	testutil.Call(t, testHandler.CreateAuroraTopupCheckout, req).Want(http.StatusBadRequest)
}

func TestCreateTopupCheckoutRejectsUnknownTopup(t *testing.T) {
	installAuroraPayments(t, &fakePayments{checkoutURL: "https://checkout.stripe.com/c/topup"})
	req := newRequest(http.MethodPost, "/api/aurora/billing/topup/checkout", checkoutBody(map[string]string{
		"topupId": "t50",
	}))
	testutil.Call(t, testHandler.CreateAuroraTopupCheckout, req).Want(http.StatusBadRequest)
}

func TestCreateTopupCheckoutFailsClosedWithoutPayments(t *testing.T) {
	old := testHandler.Payments
	testHandler.Payments = nil
	t.Cleanup(func() { testHandler.Payments = old })

	req := newRequest(http.MethodPost, "/api/aurora/billing/topup/checkout", checkoutBody(map[string]string{
		"topupId": "t5",
	}))
	testutil.Call(t, testHandler.CreateAuroraTopupCheckout, req).Want(http.StatusServiceUnavailable)
}

// The portal is the cancellation path: a subscriber must be able to reach it
// without contacting support, and the handler must hand Stripe the customer id
// stored on their row rather than one supplied by the client.
func TestCreateAuroraBillingPortal(t *testing.T) {
	auroraSubscriptionTestReset(t)
	fp := &fakePayments{portalURL: "https://billing.stripe.com/p/session/test"}
	installAuroraPayments(t, fp)
	dbfx.Insert(t, "aurora_subscription", testutil.Cols{
		"user_id": testUserID, "tier": "creator", "status": "active",
		"stripe_customer_id": "cus_portal_1",
	})

	req := newRequest(http.MethodPost, "/api/aurora/billing/portal", map[string]string{
		"returnUrl": "https://aurora.example.com/ws-1/billing",
	})
	out := testutil.Decode[struct {
		PortalURL string `json:"portalUrl"`
	}](t, testHandler.CreateAuroraBillingPortal, req, http.StatusOK)
	if out.PortalURL != fp.portalURL {
		t.Fatalf("portalUrl = %q, want %q", out.PortalURL, fp.portalURL)
	}
	if len(fp.portalCustomerIDs) != 1 || fp.portalCustomerIDs[0] != "cus_portal_1" {
		t.Fatalf("portal customer ids = %q, want [cus_portal_1]", fp.portalCustomerIDs)
	}
}

// A caller with no plan has nothing to manage, so the portal is refused with
// the same 409 a checkout conflict uses rather than a Stripe error page.
func TestCreateAuroraBillingPortalRequiresSubscription(t *testing.T) {
	auroraSubscriptionTestReset(t)
	fp := &fakePayments{portalURL: "https://billing.stripe.com/p/session/test"}
	installAuroraPayments(t, fp)

	req := newRequest(http.MethodPost, "/api/aurora/billing/portal", map[string]string{
		"returnUrl": "https://aurora.example.com/ws-1/billing",
	})
	testutil.Call(t, testHandler.CreateAuroraBillingPortal, req).Want(http.StatusConflict)
	if len(fp.portalCustomerIDs) != 0 {
		t.Fatalf("portal calls = %d, want none without a subscription row", len(fp.portalCustomerIDs))
	}
}

// A pending row has no Stripe customer yet: there is nothing for the portal to
// show, so it is the same conflict as no row rather than a provider call.
func TestCreateAuroraBillingPortalRequiresStripeCustomer(t *testing.T) {
	auroraSubscriptionTestReset(t)
	fp := &fakePayments{portalURL: "https://billing.stripe.com/p/session/test"}
	installAuroraPayments(t, fp)
	dbfx.Insert(t, "aurora_subscription", testutil.Cols{
		"user_id": testUserID, "tier": "creator", "status": "pending",
	})

	req := newRequest(http.MethodPost, "/api/aurora/billing/portal", map[string]string{
		"returnUrl": "https://aurora.example.com/ws-1/billing",
	})
	testutil.Call(t, testHandler.CreateAuroraBillingPortal, req).Want(http.StatusConflict)
	if len(fp.portalCustomerIDs) != 0 {
		t.Fatalf("portal calls = %d, want none without a Stripe customer", len(fp.portalCustomerIDs))
	}
}

func TestCreateAuroraBillingPortalFailsClosedWithoutPayments(t *testing.T) {
	old := testHandler.Payments
	testHandler.Payments = nil
	t.Cleanup(func() { testHandler.Payments = old })

	req := newRequest(http.MethodPost, "/api/aurora/billing/portal", map[string]string{
		"returnUrl": "https://aurora.example.com/ws-1/billing",
	})
	testutil.Call(t, testHandler.CreateAuroraBillingPortal, req).Want(http.StatusServiceUnavailable)
}

func TestCreateAuroraBillingPortalRejectsUntrustedReturnOrigin(t *testing.T) {
	installAuroraPayments(t, &fakePayments{portalURL: "https://billing.stripe.com/p/session/test"})
	for name, body := range map[string]map[string]string{
		"missing returnUrl": {},
		"untrusted origin":  {"returnUrl": "https://evil.example.com/steal-portal"},
		"relative url":      {"returnUrl": "/ws-1/billing"},
		"javascript url":    {"returnUrl": "javascript:alert(1)"},
	} {
		t.Run(name, func(t *testing.T) {
			req := newRequest(http.MethodPost, "/api/aurora/billing/portal", body)
			testutil.Call(t, testHandler.CreateAuroraBillingPortal, req).Want(http.StatusBadRequest)
		})
	}
}

func TestCreateAuroraBillingPortalReportsProviderFailure(t *testing.T) {
	auroraSubscriptionTestReset(t)
	fp := &fakePayments{portalErr: errors.New("stripe is down")}
	installAuroraPayments(t, fp)
	dbfx.Insert(t, "aurora_subscription", testutil.Cols{
		"user_id": testUserID, "tier": "creator", "status": "active",
		"stripe_customer_id": "cus_portal_1",
	})

	req := newRequest(http.MethodPost, "/api/aurora/billing/portal", map[string]string{
		"returnUrl": "https://aurora.example.com/ws-1/billing",
	})
	testutil.Call(t, testHandler.CreateAuroraBillingPortal, req).Want(http.StatusInternalServerError)
}

// The webhook is public, so an unverified signature must be refused before any
// accounting happens.
func TestStripeWebhookRejectsBadSignature(t *testing.T) {
	creditTestReset(t)
	auroraSubscriptionTestReset(t)
	installAuroraPayments(t, &fakePayments{eventErr: errors.New("invalid signature")})

	postStripeWebhook(t, `{"type":"checkout.session.completed"}`, http.StatusBadRequest)

	bal, err := testHandler.Credit.Balance(context.Background(), parseUUID(testUserID))
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if bal != 0 {
		t.Fatalf("balance = %d after a rejected webhook, want 0", bal)
	}
}

func TestStripeWebhookFailsClosedWithoutPayments(t *testing.T) {
	old := testHandler.Payments
	testHandler.Payments = nil
	t.Cleanup(func() { testHandler.Payments = old })

	postStripeWebhook(t, `{"type":"checkout.session.completed"}`, http.StatusServiceUnavailable)
}

func TestStripeWebhookRejectsMissingSignatureBeforeVerification(t *testing.T) {
	installAuroraPayments(t, &fakePayments{
		events: []aurora.Event{{ID: "evt_unreachable", Type: "invoice.paid"}},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/aurora/billing/stripe/webhook", strings.NewReader(`{}`))
	testutil.Call(t, testHandler.StripeWebhook, req).Want(http.StatusUnauthorized)
}

func TestStripeWebhookRejectsOversizedBody(t *testing.T) {
	installAuroraPayments(t, &fakePayments{
		events: []aurora.Event{{ID: "evt_unreachable", Type: "invoice.paid"}},
	})
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/aurora/billing/stripe/webhook",
		strings.NewReader(strings.Repeat("x", maxStripeWebhookBodySize+1)),
	)
	req.Header.Set(stripeSignatureHeader, "t=1,v1=fake")
	testutil.Call(t, testHandler.StripeWebhook, req).Want(http.StatusRequestEntityTooLarge)
}

// A subscription checkout persists the plan and grants the first month
// immediately, so the buyer can generate without waiting for the settlement
// loop.
func TestStripeWebhookSubscriptionCompleted(t *testing.T) {
	creditTestReset(t)
	auroraSubscriptionTestReset(t)
	periodEnd := time.Now().Add(30 * 24 * time.Hour).UTC().Truncate(time.Second)
	installAuroraPayments(t, &fakePayments{
		periodEnd: periodEnd,
		events: []aurora.Event{{
			ID:   "evt_sub_1",
			Type: "checkout.session.completed",
			Raw: []byte(`{"id":"cs_1","mode":"subscription","payment_status":"paid","customer":{"id":"cus_1"},` +
				`"subscription":{"id":"sub_1"},"metadata":{"userId":"` + testUserID + `","tier":"creator"}}`),
		}},
	})

	postStripeWebhook(t, `{"type":"checkout.session.completed"}`, http.StatusOK)

	tier, status, end := auroraSubscriptionRow(t)
	if tier != "creator" || status != "active" {
		t.Fatalf("subscription row = %s/%s, want creator/active", tier, status)
	}
	if end == nil || !end.UTC().Equal(periodEnd) {
		t.Fatalf("current_period_end = %v, want %v", end, periodEnd)
	}
	bal, err := testHandler.Credit.Balance(context.Background(), parseUUID(testUserID))
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if want := int64(1900) * microCreditsPerCredit; bal != want {
		t.Fatalf("balance = %d, want %d (one month of creator credits)", bal, want)
	}
}

func TestStripeWebhookSubscriptionCompletedUsesCanonicalStatus(t *testing.T) {
	creditTestReset(t)
	auroraSubscriptionTestReset(t)
	fp := &fakePayments{
		checkoutURL:        "https://checkout.stripe.com/c/state",
		periodEnd:          time.Now().Add(30 * 24 * time.Hour).UTC(),
		subscriptionStatus: "past_due",
	}
	installAuroraPayments(t, fp)
	req := newRequest(http.MethodPost, "/api/aurora/billing/checkout", checkoutBody(map[string]string{
		"tier": "creator", "billingCycle": "monthly",
	}))
	testutil.Call(t, testHandler.CreateAuroraCheckout, req).Want(http.StatusOK)
	checkoutID := fp.subscriptionCheckoutKeys[0]
	fp.events = []aurora.Event{{
		ID:      "evt_sub_past_due",
		Type:    "checkout.session.completed",
		Created: 10,
		Raw: []byte(`{"id":"cs_state","mode":"subscription","payment_status":"paid",` +
			`"customer":{"id":"cus_1"},"subscription":{"id":"sub_state"},` +
			`"metadata":{"userId":"` + testUserID + `","tier":"creator","checkoutId":"` + checkoutID + `"}}`),
	}}

	postStripeWebhook(t, `{"type":"checkout.session.completed"}`, http.StatusOK)

	_, status, _ := auroraSubscriptionRow(t)
	if status != "past_due" {
		t.Fatalf("subscription status = %q, want provider's canonical past_due", status)
	}
	if bal, err := testHandler.Credit.Balance(context.Background(), parseUUID(testUserID)); err != nil || bal != 0 {
		t.Fatalf("past-due checkout balance = %d, err=%v, want no allowance", bal, err)
	}
}

func TestStripeWebhookSubscriptionCompletedTopsUpFreeAllowance(t *testing.T) {
	creditTestReset(t)
	auroraSubscriptionTestReset(t)
	userID := parseUUID(testUserID)
	if err := testHandler.ensureFreeMonthlyGrant(context.Background(), userID); err != nil {
		t.Fatalf("ensureFreeMonthlyGrant: %v", err)
	}
	installAuroraPayments(t, &fakePayments{
		periodEnd: time.Now().Add(30 * 24 * time.Hour).UTC(),
		events: []aurora.Event{{
			ID:   "evt_sub_after_free",
			Type: "checkout.session.completed",
			Raw: []byte(`{"id":"cs_after_free","mode":"subscription","payment_status":"paid","customer":{"id":"cus_1"},` +
				`"subscription":{"id":"sub_after_free"},"metadata":{"userId":"` + testUserID + `","tier":"creator"}}`),
		}},
	})

	postStripeWebhook(t, `{"type":"checkout.session.completed"}`, http.StatusOK)

	bal, err := testHandler.Credit.Balance(context.Background(), userID)
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if want := int64(1900) * microCreditsPerCredit; bal != want {
		t.Fatalf("balance after Free-to-Creator upgrade = %d, want %d", bal, want)
	}
}

// A topup credits the wallet keyed by the Stripe Checkout Session id, so a
// redelivery of the same purchase is a no-op rather than a second credit.
func TestStripeWebhookTopupCompleted(t *testing.T) {
	creditTestReset(t)
	event := aurora.Event{
		ID:   "evt_topup_1",
		Type: "checkout.session.completed",
		Raw:  []byte(`{"id":"cs_2","mode":"payment","payment_status":"paid","metadata":{"userId":"` + testUserID + `","topupId":"t5"}}`),
	}
	installAuroraPayments(t, &fakePayments{events: []aurora.Event{event, event}})

	postStripeWebhook(t, `{"type":"checkout.session.completed"}`, http.StatusOK)
	bal, err := testHandler.Credit.Balance(context.Background(), parseUUID(testUserID))
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if want := int64(500) * microCreditsPerCredit; bal != want {
		t.Fatalf("balance = %d, want %d", bal, want)
	}

	// Replay: Stripe redelivers on any non-2xx, so the same event must not
	// credit twice.
	postStripeWebhook(t, `{"type":"checkout.session.completed"}`, http.StatusOK)
	replayed, err := testHandler.Credit.Balance(context.Background(), parseUUID(testUserID))
	if err != nil {
		t.Fatalf("Balance after replay: %v", err)
	}
	if replayed != bal {
		t.Fatalf("replayed topup changed the balance: before=%d after=%d", bal, replayed)
	}
}

func TestStripeWebhookTopupWaitsForDelayedPayment(t *testing.T) {
	creditTestReset(t)
	metadata := `"metadata":{"userId":"` + testUserID + `","topupId":"t5"}`
	installAuroraPayments(t, &fakePayments{events: []aurora.Event{
		{
			ID:   "evt_topup_unpaid",
			Type: "checkout.session.completed",
			Raw:  []byte(`{"id":"cs_delayed","mode":"payment","payment_status":"unpaid",` + metadata + `}`),
		},
		{
			ID:   "evt_topup_paid",
			Type: "checkout.session.async_payment_succeeded",
			Raw:  []byte(`{"id":"cs_delayed","mode":"payment","payment_status":"paid",` + metadata + `}`),
		},
		{
			ID:   "evt_topup_paid_late",
			Type: "checkout.session.completed",
			Raw:  []byte(`{"id":"cs_delayed","mode":"payment","payment_status":"paid",` + metadata + `}`),
		},
	}})

	postStripeWebhook(t, `{"type":"checkout.session.completed"}`, http.StatusOK)
	if bal, err := testHandler.Credit.Balance(context.Background(), parseUUID(testUserID)); err != nil || bal != 0 {
		t.Fatalf("balance before delayed payment = %d, err=%v, want 0", bal, err)
	}

	postStripeWebhook(t, `{"type":"checkout.session.async_payment_succeeded"}`, http.StatusOK)
	want := int64(500) * microCreditsPerCredit
	if bal, err := testHandler.Credit.Balance(context.Background(), parseUUID(testUserID)); err != nil || bal != want {
		t.Fatalf("balance after delayed payment = %d, err=%v, want %d", bal, err, want)
	}

	// A second paid event for the same Checkout Session is the same purchase,
	// even though Stripe assigned it a different event id.
	postStripeWebhook(t, `{"type":"checkout.session.completed"}`, http.StatusOK)
	if bal, err := testHandler.Credit.Balance(context.Background(), parseUUID(testUserID)); err != nil || bal != want {
		t.Fatalf("balance after duplicate paid event = %d, err=%v, want %d", bal, err, want)
	}
}

func TestStripeWebhookRetriesPaidUnknownCatalogItem(t *testing.T) {
	installAuroraPayments(t, &fakePayments{events: []aurora.Event{{
		ID:   "evt_unknown_topup",
		Type: "checkout.session.completed",
		Raw: []byte(`{"id":"cs_unknown","mode":"payment","payment_status":"paid",` +
			`"metadata":{"userId":"` + testUserID + `","topupId":"retired-pack"}}`),
	}}})

	postStripeWebhook(t, `{"type":"checkout.session.completed"}`, http.StatusInternalServerError)
}

// The userId in an event's metadata arrives from a third party. A malformed one
// is acked (so Stripe stops redelivering a permanently-bad event) and changes
// nothing.
func TestStripeWebhookIgnoresMalformedUserMetadata(t *testing.T) {
	creditTestReset(t)
	auroraSubscriptionTestReset(t)
	installAuroraPayments(t, &fakePayments{
		periodEnd: time.Now().Add(30 * 24 * time.Hour),
		events: []aurora.Event{{
			ID:   "evt_bad_user",
			Type: "checkout.session.completed",
			Raw:  []byte(`{"id":"cs_3","mode":"payment","metadata":{"userId":"not-a-uuid","topupId":"t5"}}`),
		}},
	})

	postStripeWebhook(t, `{"type":"checkout.session.completed"}`, http.StatusOK)

	var rows int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM aurora_subscription WHERE user_id = $1`, testUserID).Scan(&rows); err != nil {
		t.Fatalf("count subscriptions: %v", err)
	}
	if rows != 0 {
		t.Fatalf("subscription rows = %d after a malformed-metadata event, want 0", rows)
	}
}

// A subscription update moves the stored row in step with Stripe without
// granting anything: grants are the settlement loop's and the checkout
// event's job.
func TestStripeWebhookSubscriptionUpdated(t *testing.T) {
	creditTestReset(t)
	auroraSubscriptionTestReset(t)
	installAuroraPayments(t, &fakePayments{
		subscriptionStatus: "past_due",
		cancelAtPeriodEnd:  true,
		periodEnd:          time.Unix(4102444800, 0).UTC(),
		events: []aurora.Event{{
			ID:   "evt_sub_updated",
			Type: "customer.subscription.updated",
			Raw: []byte(`{"id":"sub_1","customer":{"id":"cus_stale"},"status":"active",` +
				`"cancel_at_period_end":false,"items":{"data":[{"current_period_end":1}]}}`),
		}},
	})
	dbfx.Insert(t, "aurora_subscription", testutil.Cols{
		"user_id": testUserID, "tier": "creator", "status": "active",
		"stripe_subscription_id": "sub_1",
	})

	postStripeWebhook(t, `{"type":"customer.subscription.updated"}`, http.StatusOK)

	tier, status, end := auroraSubscriptionRow(t)
	if tier != "creator" || status != "past_due" {
		t.Fatalf("subscription row = %s/%s, want creator/past_due", tier, status)
	}
	if end == nil || !end.UTC().Equal(time.Unix(4102444800, 0).UTC()) {
		t.Fatalf("current_period_end = %v, want the item's period end", end)
	}
	bal, err := testHandler.Credit.Balance(context.Background(), parseUUID(testUserID))
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if bal != 0 {
		t.Fatalf("balance = %d after a status update, want 0 (no grant)", bal)
	}
}

// A cancellation stops future grants but does not claw back what was already
// granted — the documented MVP semantic.
func TestStripeWebhookSubscriptionDeleted(t *testing.T) {
	creditTestReset(t)
	auroraSubscriptionTestReset(t)
	installAuroraPayments(t, &fakePayments{
		events: []aurora.Event{{
			ID:   "evt_sub_deleted",
			Type: "customer.subscription.deleted",
			Raw:  []byte(`{"id":"sub_1","customer":{"id":"cus_1"},"status":"canceled"}`),
		}},
	})
	dbfx.Insert(t, "aurora_subscription", testutil.Cols{
		"user_id": testUserID, "tier": "creator", "status": "active",
		"stripe_subscription_id": "sub_1",
	})

	postStripeWebhook(t, `{"type":"customer.subscription.deleted"}`, http.StatusOK)

	tier, status, _ := auroraSubscriptionRow(t)
	if tier != "creator" || status != "canceled" {
		t.Fatalf("subscription row = %s/%s, want creator/canceled", tier, status)
	}
}

func TestStripeWebhookDoesNotReactivateAfterOutOfOrderDeletion(t *testing.T) {
	creditTestReset(t)
	auroraSubscriptionTestReset(t)
	fp := &fakePayments{
		checkoutURL: "https://checkout.stripe.com/c/order",
		periodEnd:   time.Now().Add(30 * 24 * time.Hour).UTC(),
	}
	installAuroraPayments(t, fp)
	req := newRequest(http.MethodPost, "/api/aurora/billing/checkout", checkoutBody(map[string]string{
		"tier": "creator", "billingCycle": "monthly",
	}))
	testutil.Call(t, testHandler.CreateAuroraCheckout, req).Want(http.StatusOK)
	checkoutID := fp.subscriptionCheckoutKeys[0]

	fp.events = []aurora.Event{
		{
			ID:      "evt_delete_newer",
			Type:    "customer.subscription.deleted",
			Created: 20,
			Raw: []byte(`{"id":"sub_ordered","customer":{"id":"cus_1"},"status":"canceled",` +
				`"metadata":{"userId":"` + testUserID + `","tier":"creator","checkoutId":"` + checkoutID + `"}}`),
		},
		{
			ID:      "evt_checkout_older",
			Type:    "checkout.session.completed",
			Created: 10,
			Raw: []byte(`{"id":"cs_ordered","mode":"subscription","payment_status":"paid",` +
				`"customer":{"id":"cus_1"},"subscription":{"id":"sub_ordered"},` +
				`"metadata":{"userId":"` + testUserID + `","tier":"creator","checkoutId":"` + checkoutID + `"}}`),
		},
	}

	postStripeWebhook(t, `{"type":"customer.subscription.deleted"}`, http.StatusOK)
	postStripeWebhook(t, `{"type":"checkout.session.completed"}`, http.StatusOK)

	_, status, _ := auroraSubscriptionRow(t)
	if status != "canceled" {
		t.Fatalf("status after newer deletion then older completion = %q, want canceled", status)
	}
	if bal, err := testHandler.Credit.Balance(context.Background(), parseUUID(testUserID)); err != nil || bal != 0 {
		t.Fatalf("balance after stale completion = %d, err=%v, want 0", bal, err)
	}
}

func TestStripeWebhookDoesNotReactivateFromSameSecondUpdateAfterDeletion(t *testing.T) {
	creditTestReset(t)
	auroraSubscriptionTestReset(t)
	installAuroraPayments(t, &fakePayments{
		subscriptionStatus: "active",
		periodEnd:          time.Now().Add(30 * 24 * time.Hour).UTC(),
		events: []aurora.Event{
			{
				ID:      "evt_delete_same_second",
				Type:    "customer.subscription.deleted",
				Created: 20,
				Raw:     []byte(`{"id":"sub_same_second","customer":{"id":"cus_1"},"status":"canceled"}`),
			},
			{
				ID:      "evt_update_same_second",
				Type:    "customer.subscription.updated",
				Created: 20,
				Raw: []byte(`{"id":"sub_same_second","customer":{"id":"cus_1"},"status":"active",` +
					`"items":{"data":[{"current_period_end":4102444800}]}}`),
			},
		},
	})
	dbfx.Insert(t, "aurora_subscription", testutil.Cols{
		"user_id": testUserID, "tier": "creator", "status": "active",
		"stripe_subscription_id": "sub_same_second",
	})

	postStripeWebhook(t, `{"type":"customer.subscription.deleted"}`, http.StatusOK)
	postStripeWebhook(t, `{"type":"customer.subscription.updated"}`, http.StatusOK)

	_, status, _ := auroraSubscriptionRow(t)
	if status != "canceled" {
		t.Fatalf("status after same-second deletion and stale update = %q, want canceled", status)
	}
	if bal, err := testHandler.Credit.Balance(context.Background(), parseUUID(testUserID)); err != nil || bal != 0 {
		t.Fatalf("balance after same-second stale update = %d, err=%v, want 0", bal, err)
	}
}

// Stripe sends far more event types than this product consumes. An unhandled
// one must be acked, not a 4xx that makes Stripe redeliver it forever.
func TestStripeWebhookIgnoresUnknownEventType(t *testing.T) {
	installAuroraPayments(t, &fakePayments{
		events: []aurora.Event{{ID: "evt_invoice", Type: "invoice.paid", Raw: []byte(`{}`)}},
	})
	postStripeWebhook(t, `{"type":"invoice.paid"}`, http.StatusOK)
}

// ---------------------------------------------------------------------------
// Local entitlement gates (Plan 5 Task 6)
// ---------------------------------------------------------------------------

// seedAuroraGenerationForUser inserts one generation row for a user, dated now,
// and removes it when the test ends. Counting rows is how the monthly gate
// measures usage, so a test that needs usage seeds rows rather than running the
// whole create flow.
func seedAuroraGenerationForUser(t *testing.T, userID string) string {
	t.Helper()
	return dbfx.Insert(t, "aurora_generation", testutil.Cols{
		"workspace_id": testWorkspaceID,
		"user_id":      userID,
		"skill_id":     "ecommerce-bidding-strategy",
		"prompt":       "seeded for an entitlement test",
		"status":       "completed",
	})
}

func TestCreateAuroraGenerationWithEntitlementSerializesConcurrentRequests(t *testing.T) {
	creditTestReset(t)
	auroraSubscriptionTestReset(t)
	auroraGenerationsTestReset(t)

	start := make(chan struct{})
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = testHandler.createAuroraGenerationWithEntitlement(
				context.Background(),
				parseUUID(testUserID),
				parseUUID(testWorkspaceID),
				"ecommerce-bidding-strategy",
				fmt.Sprintf("concurrent entitlement request %d", i),
			)
		}(i)
	}
	close(start)
	wg.Wait()

	var succeeded, limited int
	for _, err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, errAuroraConcurrencyLimit):
			limited++
		default:
			t.Fatalf("concurrent create returned unexpected error: %v", err)
		}
	}
	if succeeded != 1 || limited != 1 {
		t.Fatalf("concurrent creates: succeeded=%d limited=%d, want 1/1", succeeded, limited)
	}
}

// The monthly cap is a hard stop: the eleventh generation in a month is refused
// for a free user, before anything is written.
func TestCreateAuroraGenerationRejectsOverMonthlyLimit(t *testing.T) {
	creditTestReset(t)
	auroraSubscriptionTestReset(t)
	cleanupAuroraSystemAgents(t)
	free, _ := testHandler.Tiers.Lookup("free")
	for i := 0; i < free.GenerationsPerMonth; i++ {
		seedAuroraGenerationForUser(t, testUserID)
	}

	req := newRequest(http.MethodPost, "/api/aurora/generations", map[string]string{
		"skillId": "ecommerce-bidding-strategy",
		"prompt":  "超出月度上限的生成请求",
	})
	testutil.Call(t, testHandler.CreateAuroraGeneration, req).Want(http.StatusTooManyRequests)

	// A refused request must not have created a generation row of its own.
	if n := dbfx.Count(t,
		`SELECT count(*) FROM aurora_generation WHERE user_id = $1 AND status = 'queued'`,
		testUserID,
	); n != 0 {
		t.Fatalf("queued generations = %d after a refused request, want 0", n)
	}
}

// Concurrency is the second gate, and it counts in-flight work rather than the
// month's total: a free user may have one generation running at a time.
func TestCreateAuroraGenerationRejectsOverConcurrency(t *testing.T) {
	creditTestReset(t)
	auroraSubscriptionTestReset(t)
	cleanupAuroraSystemAgents(t)

	agentID := dbfx.Agent(t, "Aurora concurrency gate agent", testRuntimeID)
	taskID := dbfx.Task(t, agentID, testutil.Cols{"status": "queued", "runtime_id": testRuntimeID})
	seedAuroraGenerationForUser(t, testUserID)
	dbfx.Exec(t, `UPDATE aurora_generation SET task_id = $1 WHERE user_id = $2 AND task_id IS NULL`, taskID, testUserID)

	req := newRequest(http.MethodPost, "/api/aurora/generations", map[string]string{
		"skillId": "ecommerce-bidding-strategy",
		"prompt":  "超出并发上限的生成请求",
	})
	testutil.Call(t, testHandler.CreateAuroraGeneration, req).Want(http.StatusTooManyRequests)
}

// A free user's first generation of the month gets the month's free credits —
// granted before the reservation, or the request would be rejected for
// insufficient credits. A second generation in the same month must not grant
// them again.
func TestCreateAuroraGenerationGrantsFreeMonthlyCreditsOnce(t *testing.T) {
	creditTestReset(t)
	auroraSubscriptionTestReset(t)
	cleanupAuroraSystemAgents(t)
	ctx := context.Background()

	// The free grant is 200 credits, and after this pricing change every skill
	// costs less than that, so an empty wallet can afford any single
	// generation. The fixture opens the wallet with a debt instead: read after
	// the grant, the balance is 50 credits, still short of the expert's price, so
	// the first request remains the insufficient-credit call this test has
	// always inspected. The debt is fixture-only — the service API can never
	// drive a balance below zero — and what the assertions pin is the grant: it
	// lands once and is not repeated.
	const openingDebtMicro = -150_000_000
	if _, err := testPool.Exec(ctx,
		`INSERT INTO credit_balance (user_id, available_micro) VALUES ($1, $2)`,
		parseUUID(testUserID), openingDebtMicro); err != nil {
		t.Fatalf("seed opening debt: %v", err)
	}

	req := func() *http.Request {
		return newRequest(http.MethodPost, "/api/aurora/generations", map[string]string{
			"skillId": "ecommerce-bidding-strategy",
			"prompt":  "免费额度发放测试",
		})
	}
	testutil.Call(t, testHandler.CreateAuroraGeneration, req()).Want(http.StatusPaymentRequired)

	bal, err := testHandler.Credit.Balance(ctx, parseUUID(testUserID))
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if want := testHandler.Tiers.FreeMonthlyMicro() + openingDebtMicro; bal != want {
		t.Fatalf("balance = %d, want the free monthly grant less the opening debt %d", bal, want)
	}

	testutil.Call(t, testHandler.CreateAuroraGeneration, req()).Want(http.StatusPaymentRequired)
	bal2, err := testHandler.Credit.Balance(ctx, parseUUID(testUserID))
	if err != nil {
		t.Fatalf("Balance after the second request: %v", err)
	}
	if bal2 != bal {
		t.Fatalf("the free monthly grant was duplicated: before=%d after=%d", bal, bal2)
	}
}

// A free grant is a monthly grant like any other: the settlement expires its
// unused remainder, which is why the lazy grant uses the same "sub:" reference
// shape as a paid plan.
func TestEnsureFreeMonthlyGrantUsesTheMonthlyReference(t *testing.T) {
	creditTestReset(t)
	auroraSubscriptionTestReset(t)
	ctx := context.Background()

	if err := testHandler.ensureFreeMonthlyGrant(ctx, parseUUID(testUserID)); err != nil {
		t.Fatalf("ensureFreeMonthlyGrant: %v", err)
	}
	want := "sub:" + testUserID + ":" + time.Now().UTC().Format("2006-01")
	if n := dbfx.Count(t,
		`SELECT count(*) FROM credit_ledger WHERE user_id = $1 AND kind = 'adjustment' AND reference = $2`,
		testUserID, want,
	); n != 1 {
		t.Fatalf("ledger rows with reference %q = %d, want 1", want, n)
	}
}

// ---------------------------------------------------------------------------
// Subscription status and topup list (Plan 5 Task 7)
// ---------------------------------------------------------------------------

type auroraSubscriptionPayload struct {
	Subscription struct {
		Tier              string  `json:"tier"`
		Status            string  `json:"status"`
		CurrentPeriodEnd  *string `json:"currentPeriodEnd"`
		CancelAtPeriodEnd bool    `json:"cancelAtPeriodEnd"`
		Limits            struct {
			GenerationsPerMonth int `json:"generationsPerMonth"`
			Concurrency         int `json:"concurrency"`
		} `json:"limits"`
		Usage struct {
			GenerationsUsedThisMonth int64 `json:"generationsUsedThisMonth"`
			ActiveGenerations        int64 `json:"activeGenerations"`
		} `json:"usage"`
	} `json:"subscription"`
}

func getAuroraSubscription(t *testing.T) auroraSubscriptionPayload {
	t.Helper()
	req := newRequest(http.MethodGet, "/api/aurora/billing/subscription", nil)
	return testutil.Decode[auroraSubscriptionPayload](t, testHandler.GetAuroraSubscription, req, http.StatusOK)
}

// A user who never subscribed is not an error: the endpoint answers with the
// free tier and no period, so the plan screen renders one shape either way.
func TestAuroraSubscriptionDefaultsToFree(t *testing.T) {
	creditTestReset(t)
	auroraSubscriptionTestReset(t)
	free, ok := testHandler.Tiers.Lookup("free")
	if !ok {
		t.Fatal("free tier missing from the catalog")
	}

	out := getAuroraSubscription(t).Subscription
	if out.Tier != "free" {
		t.Fatalf("tier = %q, want free", out.Tier)
	}
	if out.Status != "" {
		t.Fatalf("status = %q, want empty for a user with no subscription row", out.Status)
	}
	if out.CurrentPeriodEnd != nil {
		t.Fatalf("currentPeriodEnd = %v, want null", *out.CurrentPeriodEnd)
	}
	if out.Limits.GenerationsPerMonth != free.GenerationsPerMonth || out.Limits.Concurrency != free.Concurrency {
		t.Fatalf("limits = %+v, want the free tier %+v", out.Limits, free)
	}
}

// The response's tier is the effective one — the tier its limits belong to. A
// canceled creator is on free limits, and reporting "creator" beside them would
// describe a plan the user does not have.
func TestAuroraSubscriptionReportsTheEffectiveTier(t *testing.T) {
	creditTestReset(t)
	auroraSubscriptionTestReset(t)
	dbfx.Insert(t, "aurora_subscription", testutil.Cols{
		"user_id": testUserID, "tier": "creator", "status": "canceled",
	})

	out := getAuroraSubscription(t).Subscription
	if out.Tier != "free" {
		t.Fatalf("tier = %q, want free for a canceled plan", out.Tier)
	}
	if out.Status != "canceled" {
		t.Fatalf("status = %q, want canceled", out.Status)
	}
}

func TestAuroraSubscriptionReportsAnActivePlan(t *testing.T) {
	creditTestReset(t)
	auroraSubscriptionTestReset(t)
	pro, ok := testHandler.Tiers.Lookup("pro")
	if !ok {
		t.Fatal("pro tier missing from the catalog")
	}
	periodEnd := time.Now().Add(30 * 24 * time.Hour).UTC().Truncate(time.Second)
	dbfx.Insert(t, "aurora_subscription", testutil.Cols{
		"user_id": testUserID, "tier": "pro", "status": "active",
		"current_period_end":     periodEnd,
		"cancel_at_period_end":   true,
		"stripe_subscription_id": "sub_status_" + uuid.NewString(),
	})

	out := getAuroraSubscription(t).Subscription
	if out.Tier != "pro" || out.Status != "active" {
		t.Fatalf("subscription = %s/%s, want pro/active", out.Tier, out.Status)
	}
	if !out.CancelAtPeriodEnd {
		t.Fatal("cancelAtPeriodEnd = false, want true")
	}
	if out.Limits.GenerationsPerMonth != pro.GenerationsPerMonth || out.Limits.Concurrency != pro.Concurrency {
		t.Fatalf("limits = %+v, want the pro tier %+v", out.Limits, pro)
	}
	if out.CurrentPeriodEnd == nil {
		t.Fatal("currentPeriodEnd = null, want the stored period end")
	}
	parsed, err := time.Parse(time.RFC3339Nano, *out.CurrentPeriodEnd)
	if err != nil {
		t.Fatalf("currentPeriodEnd %q is not RFC3339: %v", *out.CurrentPeriodEnd, err)
	}
	if !parsed.Equal(periodEnd) {
		t.Fatalf("currentPeriodEnd = %v, want %v", parsed, periodEnd)
	}
}

// auroraGenerationsTestReset removes the fixture user's generation rows so a
// usage assertion counts what its own test seeded. Several earlier tests leave
// a generation row behind (the ones that assert a refused or failed create),
// and the month's count is a property of the user, not of the test.
func auroraGenerationsTestReset(t *testing.T) {
	t.Helper()
	reset := func() {
		if _, err := testPool.Exec(context.Background(),
			`DELETE FROM aurora_generation WHERE user_id = $1`, testUserID); err != nil {
			t.Fatalf("reset aurora_generation: %v", err)
		}
	}
	reset()
	t.Cleanup(reset)
}

// The plan screen shows this month's usage against the plan's cap, so the
// endpoint reports the same counts the entitlement gate enforces.
func TestAuroraSubscriptionReportsUsage(t *testing.T) {
	creditTestReset(t)
	auroraSubscriptionTestReset(t)
	auroraGenerationsTestReset(t)
	seedAuroraGenerationForUser(t, testUserID)

	out := getAuroraSubscription(t).Subscription
	if out.Usage.GenerationsUsedThisMonth != 1 {
		t.Fatalf("generationsUsedThisMonth = %d, want 1", out.Usage.GenerationsUsedThisMonth)
	}
	if out.Usage.ActiveGenerations != 0 {
		t.Fatalf("activeGenerations = %d, want 0 (the seeded row has no task)", out.Usage.ActiveGenerations)
	}
}

func TestAuroraSubscriptionRequiresAuthentication(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/aurora/billing/subscription", nil)
	testutil.Call(t, testHandler.GetAuroraSubscription, req).Want(http.StatusUnauthorized)
}

// The topup list is what the plan screen's purchase buttons are built from. The
// Stripe price id stays server-side: it is deployment configuration and the
// client only ever passes the id back.
func TestListAuroraTopups(t *testing.T) {
	req := newRequest(http.MethodGet, "/api/aurora/billing/topups", nil)
	out := testutil.Decode[struct {
		Topups []struct {
			ID      string `json:"id"`
			Credits int64  `json:"credits"`
		} `json:"topups"`
	}](t, testHandler.ListAuroraTopups, req, http.StatusOK)

	if len(out.Topups) != 2 {
		t.Fatalf("topups = %d, want 2", len(out.Topups))
	}
	for _, topup := range out.Topups {
		want, ok := testHandler.Tiers.LookupTopup(topup.ID)
		if !ok {
			t.Fatalf("topup %q is not in the catalog", topup.ID)
		}
		if topup.Credits != want.Credits {
			t.Fatalf("topup %q credits = %d, want %d", topup.ID, topup.Credits, want.Credits)
		}
	}
}

// The response must not carry the price id, whatever a future field is called.
func TestListAuroraTopupsHidesPriceIDs(t *testing.T) {
	oldTiers := testHandler.Tiers
	testHandler.Tiers = aurora.NewTierCatalog("", "", "", "", "", "price_secret_5", "price_secret_20")
	t.Cleanup(func() { testHandler.Tiers = oldTiers })

	req := newRequest(http.MethodGet, "/api/aurora/billing/topups", nil)
	response := testutil.Call(t, testHandler.ListAuroraTopups, req).Want(http.StatusOK)
	if body := response.Text(); strings.Contains(body, "price_secret") {
		t.Fatalf("topup response leaks a price id: %s", body)
	}
}

// NewStripeProvider returns a concrete *StripeProvider, so assigning its nil
// result straight to the PaymentProvider interface would create a non-nil
// interface carrying a nil pointer. The handler's 503 guards test the interface
// itself; this pins the wiring to a genuine nil when Stripe is disabled.
func TestNewHandlerLeavesPaymentsNilWithoutStripeSecrets(t *testing.T) {
	if testHandler.Payments != nil {
		t.Fatalf("Payments = %#v without Stripe secrets, want a nil interface", testHandler.Payments)
	}
}

// fakeSandboxManager is an in-process WorkspaceSandboxManager for handler
// tests. It records how often the generation path ensured a sandbox, can fail
// the ensure, and offers an onEnsure hook so an ordering assertion runs at the
// exact moment the ensure happens.
type fakeSandboxManager struct {
	mu         sync.Mutex
	calls      int
	err        error
	onEnsure   func()
	handoffs   []string
	handoffErr error
}

func (f *fakeSandboxManager) Ensure(_ context.Context, workspaceID, runtimeID pgtype.UUID) (db.AuroraSandboxNode, error) {
	f.mu.Lock()
	f.calls++
	onEnsure := f.onEnsure
	err := f.err
	f.mu.Unlock()
	if onEnsure != nil {
		onEnsure()
	}
	if err != nil {
		return db.AuroraSandboxNode{}, err
	}
	return db.AuroraSandboxNode{WorkspaceID: workspaceID, RuntimeID: runtimeID, State: "starting"}, nil
}

// HandoffWorkspaceToFleet records the workspace teardown handoff so a test can
// assert the teardown reached the Fleet seam before deleting the node row.
func (f *fakeSandboxManager) HandoffWorkspaceToFleet(_ context.Context, workspaceID pgtype.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handoffs = append(f.handoffs, uuidToString(workspaceID))
	return f.handoffErr
}

func (f *fakeSandboxManager) handoffCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.handoffs)
}

func (f *fakeSandboxManager) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// withSandboxManager installs mgr on the shared handler and restores the
// previous wiring at cleanup, so a test that disables the fleet cannot leak a
// nil manager into a sibling.
func withSandboxManager(t *testing.T, mgr aurora.WorkspaceSandboxManager) {
	t.Helper()
	prev := testHandler.SandboxManager
	testHandler.SandboxManager = mgr
	t.Cleanup(func() { testHandler.SandboxManager = prev })
}

// auroraSystemTaskCount counts queued work on the workspace's Aurora system
// agents. The fleet gate must leave it unchanged.
func auroraSystemTaskCount(t *testing.T) int {
	t.Helper()
	return dbfx.Count(t, "SELECT count(*) FROM agent_task_queue atq JOIN agent a ON a.id = atq.agent_id WHERE a.workspace_id = $1 AND a.system_key LIKE 'aurora:%'", testWorkspaceID)
}

// TestCreateGenerationEnsuresSandboxBeforeReserve proves the ordering the
// handler now promises: the sandbox is accepted by the fleet before any credit
// is reserved. The manager hook samples the deduction ledger while the ensure
// is running, and the same request must still reach reserve and enqueue.
func TestCreateGenerationEnsuresSandboxBeforeReserve(t *testing.T) {
	creditTestReset(t)
	cleanupAuroraSystemAgents(t)
	ctx := context.Background()
	user := parseUUID(testUserID)
	ws := parseUUID(testWorkspaceID)

	if err := testHandler.Credit.Grant(ctx, user, ws, 1_000_000_000, aurora.LedgerKindAdjustment, creditRef("seed")); err != nil {
		t.Fatalf("Grant: %v", err)
	}

	var deductionsAtEnsure int
	mgr := &fakeSandboxManager{onEnsure: func() {
		deductionsAtEnsure = dbfx.Count(t, "SELECT count(*) FROM credit_ledger WHERE user_id = $1 AND kind = $2", testUserID, aurora.LedgerKindDeduction)
	}}
	withSandboxManager(t, mgr)

	req := newRequest(http.MethodPost, "/api/aurora/generations", map[string]string{
		"skillId": "ecommerce-bidding-strategy",
		"prompt":  "ensure before reserve",
	})
	out := testutil.Decode[struct {
		Generation struct {
			ID string `json:"id"`
		} `json:"generation"`
	}](t, testHandler.CreateAuroraGeneration, req, http.StatusCreated)

	if mgr.callCount() != 1 {
		t.Fatalf("sandbox ensure calls = %d, want exactly 1", mgr.callCount())
	}
	if deductionsAtEnsure != 0 {
		t.Fatalf("ensure observed %d reservation ledger rows, want 0: reserve ran before ensure", deductionsAtEnsure)
	}
	if got := dbfx.Count(t, "SELECT count(*) FROM credit_ledger WHERE user_id = $1 AND kind = $2", testUserID, aurora.LedgerKindDeduction); got != 1 {
		t.Fatalf("reservation ledger rows after create = %d, want 1", got)
	}

	var taskID pgtype.UUID
	if err := testPool.QueryRow(ctx, "SELECT task_id FROM aurora_generation WHERE id = $1", out.Generation.ID).Scan(&taskID); err != nil {
		t.Fatalf("load generation task: %v", err)
	}
	if !taskID.Valid {
		t.Fatal("generation has no task id after create")
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), "DELETE FROM agent_task_queue WHERE id = $1", taskID)
		testPool.Exec(context.Background(), "DELETE FROM aurora_generation WHERE id = $1", out.Generation.ID)
	})
}

// TestCreateGenerationFleetDisabledReturns503WithoutReserve pins fail-closed
// configuration: with no fleet client the catalog still works, but a new
// generation is rejected with the stable code before any row, reserve, or
// enqueue exists.
func TestCreateGenerationFleetDisabledReturns503WithoutReserve(t *testing.T) {
	creditTestReset(t)
	cleanupAuroraSystemAgents(t)
	ctx := context.Background()
	user := parseUUID(testUserID)
	ws := parseUUID(testWorkspaceID)

	if err := testHandler.Credit.Grant(ctx, user, ws, 1_000_000_000, aurora.LedgerKindAdjustment, creditRef("seed")); err != nil {
		t.Fatalf("Grant: %v", err)
	}

	beforeGenerations := dbfx.Count(t, "SELECT count(*) FROM aurora_generation WHERE workspace_id = $1", testWorkspaceID)
	beforeTasks := auroraSystemTaskCount(t)
	beforeDeductions := dbfx.Count(t, "SELECT count(*) FROM credit_ledger WHERE user_id = $1 AND kind = $2", testUserID, aurora.LedgerKindDeduction)
	beforeBalance, err := testHandler.Credit.Balance(ctx, user)
	if err != nil {
		t.Fatalf("Balance before: %v", err)
	}

	withSandboxManager(t, nil)

	req := newRequest(http.MethodPost, "/api/aurora/generations", map[string]string{
		"skillId": "ecommerce-bidding-strategy",
		"prompt":  "fleet disabled",
	})
	body := testutil.Decode[struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}](t, testHandler.CreateAuroraGeneration, req, http.StatusServiceUnavailable)
	if body.Code != "aurora_runtime_unavailable" {
		t.Fatalf("error code = %q, want aurora_runtime_unavailable", body.Code)
	}

	if got := dbfx.Count(t, "SELECT count(*) FROM aurora_generation WHERE workspace_id = $1", testWorkspaceID); got != beforeGenerations {
		t.Fatalf("generation rows = %d, want %d unchanged", got, beforeGenerations)
	}
	if got := auroraSystemTaskCount(t); got != beforeTasks {
		t.Fatalf("aurora task rows = %d, want %d unchanged", got, beforeTasks)
	}
	if got := dbfx.Count(t, "SELECT count(*) FROM credit_ledger WHERE user_id = $1 AND kind = $2", testUserID, aurora.LedgerKindDeduction); got != beforeDeductions {
		t.Fatalf("reservation ledger rows = %d, want %d unchanged", got, beforeDeductions)
	}
	afterBalance, err := testHandler.Credit.Balance(ctx, user)
	if err != nil {
		t.Fatalf("Balance after: %v", err)
	}
	if afterBalance != beforeBalance {
		t.Fatalf("balance = %d, want %d unchanged", afterBalance, beforeBalance)
	}
}

// TestCreateGenerationFleetFailureReturns503WithoutReserveOrEnqueue pins the
// fleet failure path: the ensure is attempted exactly once, and a rejection
// returns the stable 503 with no generation row, no reservation, and no
// enqueued task.
func TestCreateGenerationFleetFailureReturns503WithoutReserveOrEnqueue(t *testing.T) {
	creditTestReset(t)
	cleanupAuroraSystemAgents(t)
	ctx := context.Background()
	user := parseUUID(testUserID)
	ws := parseUUID(testWorkspaceID)

	if err := testHandler.Credit.Grant(ctx, user, ws, 1_000_000_000, aurora.LedgerKindAdjustment, creditRef("seed")); err != nil {
		t.Fatalf("Grant: %v", err)
	}

	beforeGenerations := dbfx.Count(t, "SELECT count(*) FROM aurora_generation WHERE workspace_id = $1", testWorkspaceID)
	beforeTasks := auroraSystemTaskCount(t)
	beforeDeductions := dbfx.Count(t, "SELECT count(*) FROM credit_ledger WHERE user_id = $1 AND kind = $2", testUserID, aurora.LedgerKindDeduction)

	mgr := &fakeSandboxManager{err: errors.New("fleet unavailable")}
	withSandboxManager(t, mgr)

	req := newRequest(http.MethodPost, "/api/aurora/generations", map[string]string{
		"skillId": "ecommerce-bidding-strategy",
		"prompt":  "fleet failure",
	})
	body := testutil.Decode[struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}](t, testHandler.CreateAuroraGeneration, req, http.StatusServiceUnavailable)
	if body.Code != "aurora_runtime_unavailable" {
		t.Fatalf("error code = %q, want aurora_runtime_unavailable", body.Code)
	}
	if mgr.callCount() != 1 {
		t.Fatalf("sandbox ensure calls = %d, want exactly 1", mgr.callCount())
	}

	if got := dbfx.Count(t, "SELECT count(*) FROM aurora_generation WHERE workspace_id = $1", testWorkspaceID); got != beforeGenerations {
		t.Fatalf("generation rows = %d, want %d unchanged", got, beforeGenerations)
	}
	if got := auroraSystemTaskCount(t); got != beforeTasks {
		t.Fatalf("aurora task rows = %d, want %d unchanged", got, beforeTasks)
	}
	if got := dbfx.Count(t, "SELECT count(*) FROM credit_ledger WHERE user_id = $1 AND kind = $2", testUserID, aurora.LedgerKindDeduction); got != beforeDeductions {
		t.Fatalf("reservation ledger rows = %d, want %d unchanged", got, beforeDeductions)
	}
}

// recordingFleetProvisioner is a provider-neutral aurora.FleetProvisioner for
// handler tests. It records the ensure request and answers with a configured
// node or error, so a test can drive the real SandboxManager end to end without
// an HTTP Fleet.
type recordingFleetProvisioner struct {
	mu    sync.Mutex
	calls int
	node  aurora.FleetNode
	err   error
	last  aurora.FleetEnsureRequest
}

func (p *recordingFleetProvisioner) EnsureWorkspaceNode(_ context.Context, _ string, req aurora.FleetEnsureRequest) (aurora.FleetNode, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.last = req
	if p.err != nil {
		return aurora.FleetNode{}, p.err
	}
	node := p.node
	if node.ID == "" {
		node = aurora.FleetNode{ID: "fleet-" + req.NodeID, State: "launching", BackendID: "container-" + req.NodeID}
	}
	return node, nil
}

func (p *recordingFleetProvisioner) DeleteWorkspaceNode(context.Context, string, string) error {
	return nil
}

func (p *recordingFleetProvisioner) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// blockingFailProvisioner is the failing FleetProvisioner the concurrent
// admission regression drives. It holds its first EnsureWorkspaceNode open
// until the test releases it, so the winner's node stays starting without a
// Fleet backend id while the other callers race the same row, then it fails.
type blockingFailProvisioner struct {
	once    sync.Once
	started chan struct{}
	release chan struct{}
}

func (p *blockingFailProvisioner) EnsureWorkspaceNode(context.Context, string, aurora.FleetEnsureRequest) (aurora.FleetNode, error) {
	p.once.Do(func() { close(p.started) })
	<-p.release
	return aurora.FleetNode{}, errors.New("fleet unavailable")
}

func (p *blockingFailProvisioner) DeleteWorkspaceNode(context.Context, string, string) error {
	return nil
}

// resetAuroraSandboxState clears the shared workspace's managed runtime and
// sandbox node before a test that drives the real SandboxManager, so its
// provisioning starts from the same empty state a first request sees.
func resetAuroraSandboxState(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	dbfx.Exec(t, "DELETE FROM aurora_sandbox_node WHERE workspace_id = $1", testWorkspaceID)
	dbfx.Exec(t, "DELETE FROM agent_runtime WHERE workspace_id = $1 AND provider = 'aurora_managed'", testWorkspaceID)
	t.Cleanup(func() {
		testPool.Exec(ctx, "DELETE FROM aurora_sandbox_node WHERE workspace_id = $1", testWorkspaceID)
	})
}

// TestCreateAuroraGenerationOnLocalFleet is the P0 acceptance path: with the
// sandbox manager wired to a working Fleet, POST /api/aurora/generations seeds
// the managed runtime, provisions the workspace node, and persists the Fleet
// backend id the node must later enroll against.
func TestCreateAuroraGenerationOnLocalFleet(t *testing.T) {
	creditTestReset(t)
	cleanupAuroraSystemAgents(t)
	resetAuroraSandboxState(t)
	ctx := context.Background()
	user := parseUUID(testUserID)
	ws := parseUUID(testWorkspaceID)

	if err := testHandler.Credit.Grant(ctx, user, ws, 1_000_000_000, aurora.LedgerKindAdjustment, creditRef("seed")); err != nil {
		t.Fatalf("Grant: %v", err)
	}

	prov := &recordingFleetProvisioner{node: aurora.FleetNode{ID: "backend-node-local", State: "launching", BackendID: "container-local"}}
	withSandboxManager(t, aurora.NewSandboxManager(testHandler.Queries, testPool, prov, auroraEnrollmentImageDigest, nil))

	req := newRequest(http.MethodPost, "/api/aurora/generations", map[string]string{
		"skillId": "ecommerce-bidding-strategy",
		"prompt":  "local fleet acceptance",
	})
	out := testutil.Decode[struct {
		Generation struct {
			ID string `json:"id"`
		} `json:"generation"`
	}](t, testHandler.CreateAuroraGeneration, req, http.StatusCreated)
	if prov.callCount() != 1 {
		t.Fatalf("provisioner ensure calls = %d, want 1", prov.callCount())
	}

	var backend pgtype.Text
	if err := testPool.QueryRow(ctx,
		"SELECT backend_node_id FROM aurora_sandbox_node WHERE workspace_id = $1", ws,
	).Scan(&backend); err != nil {
		t.Fatalf("load sandbox node backend id: %v", err)
	}
	if !backend.Valid || backend.String != "backend-node-local" {
		t.Fatalf("persisted backend_node_id = %+v, want backend-node-local", backend)
	}

	var taskID pgtype.UUID
	if err := testPool.QueryRow(ctx, "SELECT task_id FROM aurora_generation WHERE id = $1", out.Generation.ID).Scan(&taskID); err != nil {
		t.Fatalf("load generation task: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), "DELETE FROM agent_task_queue WHERE id = $1", taskID)
		testPool.Exec(context.Background(), "DELETE FROM aurora_generation WHERE id = $1", out.Generation.ID)
	})
}

// TestCreateAuroraGenerationFleetFailureIs503 is the P0 failure half: a Fleet
// that rejects the ensure answers the stable runtime-unavailable 503, leaves no
// generation row or credit reservation, and records the node failed with no
// backend id so the next request re-provisions instead of adopting it.
func TestCreateAuroraGenerationFleetFailureIs503(t *testing.T) {
	creditTestReset(t)
	cleanupAuroraSystemAgents(t)
	resetAuroraSandboxState(t)
	ctx := context.Background()
	user := parseUUID(testUserID)
	ws := parseUUID(testWorkspaceID)

	if err := testHandler.Credit.Grant(ctx, user, ws, 1_000_000_000, aurora.LedgerKindAdjustment, creditRef("seed")); err != nil {
		t.Fatalf("Grant: %v", err)
	}

	beforeGenerations := dbfx.Count(t, "SELECT count(*) FROM aurora_generation WHERE workspace_id = $1", testWorkspaceID)
	beforeDeductions := dbfx.Count(t, "SELECT count(*) FROM credit_ledger WHERE user_id = $1 AND kind = $2", testUserID, aurora.LedgerKindDeduction)

	prov := &recordingFleetProvisioner{err: errors.New("fleet unavailable")}
	withSandboxManager(t, aurora.NewSandboxManager(testHandler.Queries, testPool, prov, auroraEnrollmentImageDigest, nil))

	req := newRequest(http.MethodPost, "/api/aurora/generations", map[string]string{
		"skillId": "ecommerce-bidding-strategy",
		"prompt":  "fleet failure acceptance",
	})
	body := testutil.Decode[struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}](t, testHandler.CreateAuroraGeneration, req, http.StatusServiceUnavailable)
	if body.Code != "aurora_runtime_unavailable" {
		t.Fatalf("error code = %q, want aurora_runtime_unavailable", body.Code)
	}

	var state string
	var backend pgtype.Text
	if err := testPool.QueryRow(ctx,
		"SELECT state, backend_node_id FROM aurora_sandbox_node WHERE workspace_id = $1", ws,
	).Scan(&state, &backend); err != nil {
		t.Fatalf("load sandbox node: %v", err)
	}
	if state != "failed" {
		t.Fatalf("sandbox node state = %q, want failed", state)
	}
	if backend.Valid {
		t.Fatalf("failed sandbox node kept backend_node_id %q", backend.String)
	}

	if got := dbfx.Count(t, "SELECT count(*) FROM aurora_generation WHERE workspace_id = $1", testWorkspaceID); got != beforeGenerations {
		t.Fatalf("generation rows = %d, want %d unchanged", got, beforeGenerations)
	}
	if got := dbfx.Count(t, "SELECT count(*) FROM credit_ledger WHERE user_id = $1 AND kind = $2", testUserID, aurora.LedgerKindDeduction); got != beforeDeductions {
		t.Fatalf("reservation ledger rows = %d, want %d unchanged", got, beforeDeductions)
	}
}

// TestCreateAuroraGenerationConcurrentFleetFailureIs503 is the concurrent
// regression for the false-201 bug: while one caller's Fleet ensure is held
// open (its node starting with a live enrollment but no backend id), every other
// concurrent caller must fail closed with the runtime-unavailable 503 rather
// than adopt the unconfirmed row. No caller may create a generation or reserve
// credits, and the winner's later failure must not surface as success either.
func TestCreateAuroraGenerationConcurrentFleetFailureIs503(t *testing.T) {
	creditTestReset(t)
	cleanupAuroraSystemAgents(t)
	resetAuroraSandboxState(t)
	ctx := context.Background()
	user := parseUUID(testUserID)
	ws := parseUUID(testWorkspaceID)

	if err := testHandler.Credit.Grant(ctx, user, ws, 1_000_000_000, aurora.LedgerKindAdjustment, creditRef("seed")); err != nil {
		t.Fatalf("Grant: %v", err)
	}

	const prompt = "concurrent fleet failure"
	t.Cleanup(func() {
		testPool.Exec(context.Background(), "DELETE FROM agent_task_queue WHERE id IN (SELECT task_id FROM aurora_generation WHERE workspace_id = $1 AND prompt = $2)", testWorkspaceID, prompt)
		testPool.Exec(context.Background(), "DELETE FROM aurora_generation WHERE workspace_id = $1 AND prompt = $2", testWorkspaceID, prompt)
	})

	beforeGenerations := dbfx.Count(t, "SELECT count(*) FROM aurora_generation WHERE workspace_id = $1", testWorkspaceID)
	beforeDeductions := dbfx.Count(t, "SELECT count(*) FROM credit_ledger WHERE user_id = $1 AND kind = $2", testUserID, aurora.LedgerKindDeduction)

	prov := &blockingFailProvisioner{started: make(chan struct{}), release: make(chan struct{})}
	withSandboxManager(t, aurora.NewSandboxManager(testHandler.Queries, testPool, prov, auroraEnrollmentImageDigest, nil))

	const workers = 6
	type result struct {
		status int
		code   string
	}
	done := make(chan result, workers)
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		go func() {
			<-start
			req := newRequest(http.MethodPost, "/api/aurora/generations", map[string]string{
				"skillId": "ecommerce-bidding-strategy",
				"prompt":  prompt,
			})
			rec := httptest.NewRecorder()
			testHandler.CreateAuroraGeneration(rec, req)
			var body struct {
				Code string `json:"code"`
			}
			_ = json.Unmarshal(rec.Body.Bytes(), &body)
			done <- result{status: rec.Code, code: body.Code}
		}()
	}
	close(start)

	// Hold the winner's Fleet call open so its node stays starting with no
	// backend id. Wait for every other caller to decide against that exact row
	// before releasing the winner, so the assertion covers the real race and not
	// a scheduling accident.
	select {
	case <-prov.started:
	case <-time.After(15 * time.Second):
		t.Fatal("no Fleet provision call started")
	}
	results := make([]result, 0, workers)
	for i := 0; i < workers-1; i++ {
		select {
		case r := <-done:
			results = append(results, r)
		case <-time.After(20 * time.Second):
			t.Fatalf("only %d/%d callers finished while the provision was held open", i, workers-1)
		}
	}
	close(prov.release)
	select {
	case r := <-done:
		results = append(results, r)
	case <-time.After(15 * time.Second):
		t.Fatal("winner did not finish after the Fleet call was released")
	}

	for i, r := range results {
		if r.status != http.StatusServiceUnavailable || r.code != "aurora_runtime_unavailable" {
			t.Fatalf("caller %d = %d %q, want 503 aurora_runtime_unavailable", i, r.status, r.code)
		}
	}
	if got := dbfx.Count(t, "SELECT count(*) FROM aurora_generation WHERE workspace_id = $1", testWorkspaceID); got != beforeGenerations {
		t.Fatalf("generation rows = %d, want %d unchanged", got, beforeGenerations)
	}
	if got := dbfx.Count(t, "SELECT count(*) FROM credit_ledger WHERE user_id = $1 AND kind = $2", testUserID, aurora.LedgerKindDeduction); got != beforeDeductions {
		t.Fatalf("reservation ledger rows = %d, want %d unchanged", got, beforeDeductions)
	}
}

// auroraClaimFixture builds a legacy quick-create Aurora round: a task with no
// issue, an aurora system agent, and a generation row keyed to the task. New
// rounds are issue-bound and carry their generation id in the task context, so
// this is the shape that still exercises the DB-link grace on the quick-create
// claim path.
func auroraClaimFixture(t *testing.T) (db.AgentTaskQueue, db.AgentRuntime, string) {
	t.Helper()
	creditTestReset(t)
	cleanupAuroraSystemAgents(t)
	ctx := context.Background()
	user := parseUUID(testUserID)
	ws := parseUUID(testWorkspaceID)
	if err := testHandler.Credit.Grant(ctx, user, ws, 1_000_000_000, aurora.LedgerKindAdjustment, creditRef("seed")); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	if err := testHandler.ensureAuroraExpertSeed(ctx, ws, "ecommerce-bidding-strategy"); err != nil {
		t.Fatalf("ensure aurora system agents: %v", err)
	}
	agent, err := testHandler.Queries.GetAgentBySystemKey(ctx, db.GetAgentBySystemKeyParams{
		WorkspaceID: ws,
		SystemKey:   pgtype.Text{String: "aurora:ecommerce-bidding-strategy", Valid: true},
	})
	if err != nil {
		t.Fatalf("load aurora system agent: %v", err)
	}
	task, err := testHandler.TaskService.EnqueueQuickCreateTask(ctx, ws, user, agent.ID, pgtype.UUID{},
		"claim generation lookup regression", "high", "", pgtype.UUID{}, pgtype.UUID{}, nil)
	if err != nil {
		t.Fatalf("enqueue quick-create task: %v", err)
	}
	generation, err := testHandler.Queries.CreateAuroraGeneration(ctx, db.CreateAuroraGenerationParams{
		WorkspaceID: ws,
		UserID:      user,
		SkillID:     "ecommerce-bidding-strategy",
		Prompt:      "claim generation lookup regression",
	})
	if err != nil {
		t.Fatalf("create generation row: %v", err)
	}
	if _, err := testHandler.Queries.UpdateAuroraGenerationTask(ctx, db.UpdateAuroraGenerationTaskParams{
		ID:              generation.ID,
		TaskID:          task.ID,
		CreditsReserved: 0,
		WorkspaceID:     ws,
	}); err != nil {
		t.Fatalf("link generation to task: %v", err)
	}
	generationID := uuidToString(generation.ID)
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, task.ID)
		testPool.Exec(context.Background(), `DELETE FROM aurora_generation WHERE id = $1`, generationID)
	})
	// The real claim flow marks the task dispatched before assembling the
	// response; mirror that so the failure path can settle it.
	dbfx.Exec(t, `UPDATE agent_task_queue SET status = 'dispatched', dispatched_at = now() WHERE id = $1`, task.ID)
	task, err = testHandler.Queries.GetAgentTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("load task: %v", err)
	}
	runtime, err := testHandler.Queries.GetAgentRuntime(ctx, task.RuntimeID)
	if err != nil {
		t.Fatalf("load runtime: %v", err)
	}
	return task, runtime, generationID
}

// TestClaimAuroraTaskGenerationLookupSettlesOrRetries is the Important-4
// regression. The enqueue path inserts the quick-create task and kicks the
// daemon BEFORE it writes aurora_generation.task_id, so a daemon woken inside
// that window legitimately reads no row and must be redelivered. A miss that
// outlives the link grace is permanent and settles through the normal failure
// path, while a real database error on the same lookup stays retryable.
func TestClaimAuroraTaskGenerationLookupSettlesOrRetries(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	t.Run("unlinked generation requeues then succeeds", func(t *testing.T) {
		task, runtime, generationID := auroraClaimFixture(t)
		// The task is claimable before the handler has written task_id onto the
		// generation row: mirror that window by clearing the back-link.
		dbfx.Exec(t, `UPDATE aurora_generation SET task_id = NULL WHERE id = $1`, generationID)

		req := newRequest(http.MethodPost, "/api/claim", nil)
		_, _, _, _, _, failure := testHandler.buildClaimedTaskResponse(req, &task, runtime, uuidToString(task.RuntimeID), testWorkspaceID)
		if failure == nil || failure.settled {
			t.Fatalf("failure = %+v, want an unsettled retryable failure inside the link window", failure)
		}
		if failure.outcome != "error_aurora_generation_unlinked" || failure.status != http.StatusInternalServerError {
			t.Fatalf("failure = %+v, want error_aurora_generation_unlinked/500", failure)
		}
		var status string
		dbfx.QueryRow(t, `SELECT status FROM agent_task_queue WHERE id = $1`, task.ID).Scan(&status)
		if status != "queued" {
			t.Fatalf("task status = %q, want queued after the retryable requeue", status)
		}

		// The handler finishes the back-link; the redelivered claim now runs.
		dbfx.Exec(t, `UPDATE aurora_generation SET task_id = $1 WHERE id = $2`, task.ID, generationID)
		requeued, err := testHandler.Queries.GetAgentTask(context.Background(), task.ID)
		if err != nil {
			t.Fatalf("reload requeued task: %v", err)
		}
		resp, _, _, _, _, failure := testHandler.buildClaimedTaskResponse(req, &requeued, runtime, uuidToString(requeued.RuntimeID), testWorkspaceID)
		if failure != nil {
			t.Fatalf("second claim failure = %+v, want success once the generation is linked", failure)
		}
		if resp.GenerationID != generationID {
			t.Fatalf("GenerationID = %q, want %q", resp.GenerationID, generationID)
		}
	})

	t.Run("missing row settles terminal after the link grace", func(t *testing.T) {
		task, runtime, generationID := auroraClaimFixture(t)
		dbfx.Exec(t, `DELETE FROM aurora_generation WHERE id = $1`, generationID)
		// Age the task past the grace: the enqueue writer has either committed
		// the back-link or failed, so no-rows can never resolve.
		dbfx.Exec(t, `UPDATE agent_task_queue SET created_at = now() - make_interval(secs => $2) WHERE id = $1`,
			task.ID, (auroraGenerationLinkGrace + time.Minute).Seconds())
		aged, err := testHandler.Queries.GetAgentTask(context.Background(), task.ID)
		if err != nil {
			t.Fatalf("reload aged task: %v", err)
		}

		req := newRequest(http.MethodPost, "/api/claim", nil)
		_, _, _, _, _, failure := testHandler.buildClaimedTaskResponse(req, &aged, runtime, uuidToString(aged.RuntimeID), testWorkspaceID)
		if failure == nil || !failure.settled {
			t.Fatalf("failure = %+v, want a settled terminal failure", failure)
		}
		if failure.outcome != "error_aurora_generation_missing" || failure.status != http.StatusConflict {
			t.Fatalf("failure = %+v, want error_aurora_generation_missing/409", failure)
		}
		var status, reason string
		dbfx.QueryRow(t, `SELECT status, failure_reason FROM agent_task_queue WHERE id = $1`, task.ID).Scan(&status, &reason)
		if status != "failed" {
			t.Fatalf("task status = %q, want failed", status)
		}
		if reason != taskfailure.ReasonInvalidTaskIdentity.String() {
			t.Fatalf("failure_reason = %q, want %q", reason, taskfailure.ReasonInvalidTaskIdentity)
		}
	})

	t.Run("database error stays retryable", func(t *testing.T) {
		task, runtime, _ := auroraClaimFixture(t)
		broken := *testHandler
		broken.Queries = db.New(failQueryDBTX{
			DBTX:   testPool,
			failOn: "FROM aurora_generation",
			err:    errors.New("simulated transient read failure"),
		})
		req := newRequest(http.MethodPost, "/api/claim", nil)
		_, _, _, _, _, failure := broken.buildClaimedTaskResponse(req, &task, runtime, uuidToString(task.RuntimeID), testWorkspaceID)
		if failure == nil || failure.settled {
			t.Fatalf("failure = %+v, want an unsettled retryable failure", failure)
		}
		if failure.outcome != "error_aurora_generation" || failure.status != http.StatusInternalServerError {
			t.Fatalf("failure = %+v, want error_aurora_generation/500", failure)
		}
		var status string
		dbfx.QueryRow(t, `SELECT status FROM agent_task_queue WHERE id = $1`, task.ID).Scan(&status)
		if status == "failed" {
			t.Fatalf("task status = %q, want it preserved for redelivery", status)
		}
	})
}

// TestCreateAuroraGenerationBindsIssueBeforeEnqueue proves create binds a real
// issue before enqueue: the generation row carries its id, exactly one task is
// enqueued on the issue (so the assignee-driven auto-enqueue is suppressed),
// that task carries the aurora_issue context for this generation, and the
// issue is titled and attributed from the catalog and requester.
func TestCreateAuroraGenerationBindsIssueBeforeEnqueue(t *testing.T) {
	creditTestReset(t)
	cleanupAuroraSystemAgents(t)
	ctx := context.Background()
	user := parseUUID(testUserID)
	ws := parseUUID(testWorkspaceID)

	if err := testHandler.Credit.Grant(ctx, user, ws, 1_000_000_000, aurora.LedgerKindAdjustment, creditRef("seed")); err != nil {
		t.Fatalf("Grant: %v", err)
	}

	prompt := "绑定 issue 的亲子游封面"
	req := newRequest(http.MethodPost, "/api/aurora/generations", map[string]string{
		"skillId": "ecommerce-bidding-strategy",
		"prompt":  prompt,
	})
	out := testutil.Decode[struct {
		Generation struct {
			ID              string `json:"id"`
			IssueIdentifier string `json:"issueIdentifier"`
			CreditsReserved int64  `json:"creditsReserved"`
		} `json:"generation"`
	}](t, testHandler.CreateAuroraGeneration, req, http.StatusCreated)

	gen := out.Generation
	if gen.ID == "" {
		t.Fatal("expected non-empty generation id")
	}
	entry, ok := aurora.Lookup("ecommerce-bidding-strategy")
	if !ok {
		t.Fatal("fixture skill is not in the catalog")
	}
	wantReserved := int64(entry.Credits) * 1_000_000
	if gen.CreditsReserved != wantReserved {
		t.Fatalf("creditsReserved = %d, want %d", gen.CreditsReserved, wantReserved)
	}
	// The identifier is the workspace prefix plus the issue number. The "#N"
	// degraded form has no prefix and the client must read it as absent, so the
	// assertion rejects it explicitly.
	if gen.IssueIdentifier == "" || strings.HasPrefix(gen.IssueIdentifier, "#") || !strings.Contains(gen.IssueIdentifier, "-") {
		t.Fatalf("issueIdentifier = %q, want PREFIX-N", gen.IssueIdentifier)
	}

	var issueID, taskID pgtype.UUID
	var reserved int64
	dbfx.QueryRow(t, `SELECT issue_id, task_id, credits_reserved FROM aurora_generation WHERE id = $1`, gen.ID).
		Scan(&issueID, &taskID, &reserved)
	if !issueID.Valid {
		t.Fatal("expected non-null issue_id on the generation row")
	}
	if !taskID.Valid {
		t.Fatal("expected non-null task_id on the generation row")
	}
	if reserved != wantReserved {
		t.Fatalf("row credits_reserved = %d, want %d", reserved, wantReserved)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE issue_id = $1`, issueID)
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
		testPool.Exec(context.Background(), `DELETE FROM aurora_generation WHERE id = $1`, gen.ID)
	})

	// Exactly one task on the issue: the Aurora enqueue, not also the
	// assignee-driven auto-enqueue that SkipAssignedAgentEnqueue suppresses.
	if got := dbfx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1`, issueID); got != 1 {
		t.Fatalf("issue has %d tasks, want exactly 1", got)
	}

	var taskIssueID pgtype.UUID
	var rawCtx []byte
	dbfx.QueryRow(t, `SELECT issue_id, context FROM agent_task_queue WHERE id = $1`, taskID).
		Scan(&taskIssueID, &rawCtx)
	if taskIssueID.String() != issueID.String() {
		t.Fatalf("task issue_id = %s, want %s", taskIssueID.String(), issueID.String())
	}
	var taskCtx struct {
		Type         string `json:"type"`
		GenerationID string `json:"generation_id"`
	}
	if err := json.Unmarshal(rawCtx, &taskCtx); err != nil {
		t.Fatalf("unmarshal task context: %v", err)
	}
	if taskCtx.Type != service.AuroraIssueContextType {
		t.Fatalf("task context type = %q, want %q", taskCtx.Type, service.AuroraIssueContextType)
	}
	if taskCtx.GenerationID != gen.ID {
		t.Fatalf("task generation_id = %q, want %q", taskCtx.GenerationID, gen.ID)
	}

	var title string
	var creatorType pgtype.Text
	var creatorID pgtype.UUID
	dbfx.QueryRow(t, `SELECT title, creator_type, creator_id FROM issue WHERE id = $1`, issueID).
		Scan(&title, &creatorType, &creatorID)
	if want := entry.Name + "：" + prompt; title != want {
		t.Fatalf("issue title = %q, want %q", title, want)
	}
	if !creatorType.Valid || creatorType.String != "member" {
		t.Fatalf("issue creator_type = %q, want member", creatorType.String)
	}
	if creatorID.String() != testUserID {
		t.Fatalf("issue creator_id = %s, want %s", creatorID.String(), testUserID)
	}
}

// TestAuroraReadsExposeIssueIdentifier proves the list and detail endpoints
// carry the issue link their consumers read. Only the create response used to
// set it, so the library, history and detail views could never link to the
// issue even though they already render the link when the field is present.
func TestAuroraReadsExposeIssueIdentifier(t *testing.T) {
	resetAuroraGenerations(t)

	issueID := dbfx.Issue(t, "Aurora issue link")
	bound := insertGeneration(t, "bound to an issue", testutil.Cols{"issue_id": issueID})
	unbound := insertGeneration(t, "no issue")

	var prefix string
	var number int32
	dbfx.QueryRow(t, `SELECT w.issue_prefix, i.number FROM issue i JOIN workspace w ON w.id = i.workspace_id WHERE i.id = $1`, issueID).Scan(&prefix, &number)
	want := service.IssueIdentifier(prefix, number)

	list := testutil.Decode[struct {
		Generations []struct {
			ID              string `json:"id"`
			IssueIdentifier string `json:"issueIdentifier"`
		} `json:"generations"`
	}](t, testHandler.ListAuroraGenerations, newRequest(http.MethodGet, "/api/aurora/generations", nil), http.StatusOK)

	byID := make(map[string]string, len(list.Generations))
	for _, g := range list.Generations {
		byID[g.ID] = g.IssueIdentifier
	}
	if byID[bound] != want {
		t.Fatalf("list issueIdentifier = %q, want %q", byID[bound], want)
	}
	if byID[unbound] != "" {
		t.Fatalf("unbound list issueIdentifier = %q, want empty", byID[unbound])
	}

	detail := testutil.Decode[struct {
		Generation struct {
			ID              string `json:"id"`
			IssueIdentifier string `json:"issueIdentifier"`
		} `json:"generation"`
	}](t, testHandler.GetAuroraGeneration, withURLParam(newRequest(http.MethodGet, "/api/aurora/generations/{id}", nil), "id", bound), http.StatusOK)

	if detail.Generation.IssueIdentifier != want {
		t.Fatalf("detail issueIdentifier = %q, want %q", detail.Generation.IssueIdentifier, want)
	}
}

// TestEnsureAuroraCatalogSeedSeedsEveryExpertOnce covers the one-time full seed
// behind the directory load: a workspace with no carriers gets one per catalog
// expert, and a second entry adds nothing.
//
// A throwaway workspace is required. Seeding 33 carriers into the shared fixture
// workspace would leak into every later test's unfiltered `agent ... LIMIT 1`
// heuristics (the same reason cleanupAuroraSystemAgents exists).
func TestEnsureAuroraCatalogSeedSeedsEveryExpertOnce(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	workspaceID := dbfx.Workspace(t, "Aurora catalog seed", "aurora-catalog-seed-"+uuid.NewString())
	dbfx.Member(t, workspaceID, testUserID, "owner")
	// agent, skill, agent_runtime and member all cascade from the workspace, so
	// removing it removes everything the seed wrote.
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, workspaceID)
	})

	ctx := context.Background()
	const carriers = `SELECT count(*) FROM agent WHERE workspace_id = $1 AND system_key LIKE 'aurora:%'`

	if err := testHandler.ensureAuroraCatalogSeed(ctx, parseUUID(workspaceID)); err != nil {
		t.Fatalf("first seed: %v", err)
	}
	if n := dbfx.Count(t, carriers, workspaceID); n != 33 {
		t.Fatalf("carriers = %d, want 33", n)
	}
	if n := dbfx.Count(t,
		`SELECT count(*) FROM agent_runtime WHERE workspace_id = $1 AND provider = 'aurora_managed'`, workspaceID); n != 1 {
		t.Fatalf("managed runtimes = %d, want 1", n)
	}

	if err := testHandler.ensureAuroraCatalogSeed(ctx, parseUUID(workspaceID)); err != nil {
		t.Fatalf("second seed: %v", err)
	}
	if n := dbfx.Count(t, carriers, workspaceID); n != 33 {
		t.Fatalf("second seed duplicated carriers: %d", n)
	}
	if n := dbfx.Count(t,
		`SELECT count(*) FROM agent_runtime WHERE workspace_id = $1 AND provider = 'aurora_managed'`, workspaceID); n != 1 {
		t.Fatalf("second seed duplicated the managed runtime: %d", n)
	}
}

// TestEnsureAuroraCatalogSeedDoesNotResurrectArchivedCarrier pins the guard's
// key-set semantics: a carrier the member archived still exists, so the
// directory load leaves it alone instead of re-adopting it (AdoptAuroraSystemAgent
// clears archived_at) on every page load. A carrier whose row is gone entirely is
// still the seed's job.
func TestEnsureAuroraCatalogSeedDoesNotResurrectArchivedCarrier(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	workspaceID := dbfx.Workspace(t, "Aurora archived carrier", "aurora-archived-carrier-"+uuid.NewString())
	dbfx.Member(t, workspaceID, testUserID, "owner")
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, workspaceID)
	})

	const (
		archivedSlug = "design-prd-writing"
		removedSlug  = "media-short-video-copy"
	)
	archivedKey := aurora.SystemKey(archivedSlug)
	removedKey := aurora.SystemKey(removedSlug)

	ctx := context.Background()
	if err := testHandler.ensureAuroraCatalogSeed(ctx, parseUUID(workspaceID)); err != nil {
		t.Fatalf("first seed: %v", err)
	}
	dbfx.Exec(t, `UPDATE agent SET archived_at = now() WHERE workspace_id = $1 AND system_key = $2`,
		workspaceID, archivedKey)
	dbfx.Exec(t, `DELETE FROM agent WHERE workspace_id = $1 AND system_key = $2`, workspaceID, removedKey)

	if err := testHandler.ensureAuroraCatalogSeed(ctx, parseUUID(workspaceID)); err != nil {
		t.Fatalf("second seed: %v", err)
	}

	if n := dbfx.Count(t, `SELECT count(*) FROM agent WHERE workspace_id = $1 AND system_key = $2`,
		workspaceID, archivedKey); n != 1 {
		t.Fatalf("archived carrier rows = %d, want 1 (no duplicate)", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM agent WHERE workspace_id = $1 AND system_key = $2 AND archived_at IS NOT NULL`,
		workspaceID, archivedKey); n != 1 {
		t.Fatalf("archived carrier rows still archived = %d, want 1", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM agent WHERE workspace_id = $1 AND system_key = $2 AND archived_at IS NULL`,
		workspaceID, removedKey); n != 1 {
		t.Fatalf("removed carrier live rows = %d, want 1", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM agent WHERE workspace_id = $1 AND system_key LIKE 'aurora:%'`,
		workspaceID); n != 33 {
		t.Fatalf("carriers = %d, want 33", n)
	}
}

// TestAuroraCarrierIsAssignableByANonOwnerMember pins the two properties that
// make an expert carrier usable by the whole workspace without ever belonging
// to an ordinary member: it is public_to with a workspace invocation target,
// and both the carrier and the managed runtime stay owned by the workspace
// owner no matter who triggers the seed.
//
// The second seed deliberately runs through the generation path, which used to
// stamp the calling member's id onto owner_id via AdoptAuroraSystemAgent — and
// the daemon claim path refuses to deliver to a private runtime whose owner
// differs from the agent's.
func TestAuroraCarrierIsAssignableByANonOwnerMember(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	workspaceID := dbfx.Workspace(t, "Aurora carrier perms", "aurora-carrier-perms-"+uuid.NewString())
	dbfx.Member(t, workspaceID, testUserID, "owner")
	memberID := dbfx.User(t, "Aurora carrier member", "aurora-carrier-member-"+uuid.NewString()+"@test.invalid")
	dbfx.Member(t, workspaceID, memberID, "member")
	// agent, agent_runtime, skill and member all cascade from the workspace.
	// agent_invocation_target has no foreign keys by design (migration 130), so
	// the grants the seed writes are removed explicitly.
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = testPool.Exec(ctx,
			`DELETE FROM agent_invocation_target WHERE agent_id IN (SELECT id FROM agent WHERE workspace_id = $1)`, workspaceID)
		_, _ = testPool.Exec(ctx, `DELETE FROM workspace WHERE id = $1`, workspaceID)
	})

	const slug = "media-face-swap"
	systemKey := aurora.SystemKey(slug)
	ctx := context.Background()

	// The owner opens the directory first, which seeds every carrier.
	if err := testHandler.ensureAuroraCatalogSeed(ctx, parseUUID(workspaceID)); err != nil {
		t.Fatalf("owner seed: %v", err)
	}

	// A plain member opens the directory, then triggers the generation-path
	// seed for one expert. Neither may end up owning the carrier.
	req := newRequestAs(memberID, http.MethodGet, "/api/aurora/skills", nil)
	req.Header.Set("X-Workspace-ID", workspaceID)
	testutil.Call(t, testHandler.ListAuroraSkills, req).Want(http.StatusOK)
	if err := testHandler.ensureAuroraExpertSeed(ctx, parseUUID(workspaceID), slug); err != nil {
		t.Fatalf("member seed: %v", err)
	}

	var carrierOwner, runtimeOwner, permissionMode string
	dbfx.QueryRow(t,
		`SELECT owner_id::text, permission_mode FROM agent WHERE workspace_id = $1 AND system_key = $2`,
		workspaceID, systemKey).Scan(&carrierOwner, &permissionMode)
	dbfx.QueryRow(t,
		`SELECT owner_id::text FROM agent_runtime WHERE workspace_id = $1 AND provider = 'aurora_managed'`,
		workspaceID).Scan(&runtimeOwner)
	if carrierOwner != testUserID || runtimeOwner != testUserID {
		t.Fatalf("carrier/runtime owners = %s / %s, want the workspace owner %s", carrierOwner, runtimeOwner, testUserID)
	}
	if permissionMode != "public_to" {
		t.Fatalf("carrier permission_mode = %q, want public_to", permissionMode)
	}
	// One workspace grant per carrier: every member may invoke it.
	if n := dbfx.Count(t,
		`SELECT count(*) FROM agent_invocation_target it JOIN agent a ON a.id = it.agent_id
		 WHERE a.workspace_id = $1 AND a.system_key = $2 AND it.target_type = 'workspace' AND it.target_id = $1`,
		workspaceID, systemKey); n != 1 {
		t.Fatalf("workspace invocation targets = %d, want 1", n)
	}
}

// TestAuroraSeedRehomesAManagedRuntimeOwnedByAMember covers the ownership
// transfer: the managed runtime and several carriers already exist under an
// ordinary member — what an earlier ownership arrangement leaves behind — and
// one generation's seed must move the runtime and every carrier onto the
// workspace owner together. Moving only the seeded expert would leave the rest
// on the member, where the daemon claim path's private-runtime owner check
// refuses them.
func TestAuroraSeedRehomesAManagedRuntimeOwnedByAMember(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	workspaceID := dbfx.Workspace(t, "Aurora runtime owner", "aurora-runtime-owner-"+uuid.NewString())
	dbfx.Member(t, workspaceID, testUserID, "owner")
	memberID := dbfx.User(t, "Aurora runtime member", "aurora-runtime-member-"+uuid.NewString()+"@test.invalid")
	dbfx.Member(t, workspaceID, memberID, "member")
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = testPool.Exec(ctx,
			`DELETE FROM agent_invocation_target WHERE agent_id IN (SELECT id FROM agent WHERE workspace_id = $1)`, workspaceID)
		_, _ = testPool.Exec(ctx, `DELETE FROM workspace WHERE id = $1`, workspaceID)
	})

	runtimeID := dbfx.Insert(t, "agent_runtime", testutil.Cols{
		"workspace_id": workspaceID,
		"name":         "Aurora Managed Runtime",
		"runtime_mode": "cloud",
		"provider":     "aurora_managed",
		"status":       "offline",
		"device_info":  "",
		"metadata":     testutil.Raw("'{}'::jsonb"),
		"owner_id":     memberID,
		"visibility":   "private",
	})
	// Two experts the member's generation already seeded, already migrated to
	// the public-to-workspace shape. The seed below touches neither of them.
	untouched := []string{"design-prd-writing", "media-short-video-copy"}
	for _, stale := range untouched {
		dbfx.Agent(t, "Aurora carrier "+stale, runtimeID, testutil.Cols{
			"workspace_id":    workspaceID,
			"owner_id":        memberID,
			"system_key":      aurora.SystemKey(stale),
			"visibility":      "workspace",
			"permission_mode": "public_to",
		})
	}

	const slug = "media-face-swap"
	if err := testHandler.ensureAuroraExpertSeed(context.Background(), parseUUID(workspaceID), slug); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var runtimeOwner, carrierOwner string
	dbfx.QueryRow(t,
		`SELECT owner_id::text FROM agent_runtime WHERE workspace_id = $1 AND provider = 'aurora_managed'`,
		workspaceID).Scan(&runtimeOwner)
	dbfx.QueryRow(t,
		`SELECT owner_id::text FROM agent WHERE workspace_id = $1 AND system_key = $2`,
		workspaceID, aurora.SystemKey(slug)).Scan(&carrierOwner)
	if runtimeOwner != testUserID || carrierOwner != testUserID {
		t.Fatalf("runtime/seeded carrier owners = %s / %s, want the workspace owner %s", runtimeOwner, carrierOwner, testUserID)
	}
	if n := dbfx.Count(t,
		`SELECT count(*) FROM agent WHERE workspace_id = $1 AND system_key LIKE 'aurora:%' AND owner_id <> $2`,
		workspaceID, testUserID); n != 0 {
		t.Fatalf("%d carriers still owned by someone else, want 0", n)
	}
	// Every untouched carrier is still there: convergence moves ownership, it
	// does not remove the rows.
	for _, stale := range untouched {
		var owner string
		dbfx.QueryRow(t,
			`SELECT owner_id::text FROM agent WHERE workspace_id = $1 AND system_key = $2`,
			workspaceID, aurora.SystemKey(stale)).Scan(&owner)
		if owner != testUserID {
			t.Fatalf("untouched carrier %q owner = %s, want the workspace owner %s", stale, owner, testUserID)
		}
	}
}
