package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// TestListAuroraToolsReturnsEveryMapping pins the tool directory payload: every
// tool page the client ships resolves to a catalog expert, and each entry names
// both the tool and the expert's display name so the client never has to hold
// the mapping itself.
func TestListAuroraToolsReturnsEveryMapping(t *testing.T) {
	var out struct {
		Tools []struct {
			ID         string `json:"id"`
			ExpertSlug string `json:"expert_slug"`
			ExpertName string `json:"expert_name"`
		} `json:"tools"`
	}
	testutil.Call(t, testHandler.ListAuroraTools, newRequest(http.MethodGet, "/api/aurora/tools", nil)).Want(http.StatusOK).JSON(&out)
	if len(out.Tools) != 18 {
		t.Fatalf("tools = %d, want 18", len(out.Tools))
	}
	for _, tool := range out.Tools {
		if tool.ID == "" || tool.ExpertSlug == "" || tool.ExpertName == "" {
			t.Errorf("incomplete mapping %#v", tool)
		}
	}
}

// auroraToolWorkspace creates a workspace owned by testUserID plus an ordinary
// member, and seeds every carrier. Returns (workspaceID, memberUserID).
//
// The rows a run creates — issues, carriers, skills, the managed runtime — are
// removed before the fixture's own workspace delete, because this schema has no
// cascading deletes (AGENTS.md, "Database and Migration Rules").
func auroraToolWorkspace(t *testing.T) (string, string) {
	t.Helper()
	ws := dbfx.Workspace(t, "aurora tools", "aurora-tools-"+uuid.NewString())
	t.Cleanup(func() {
		ctx := context.Background()
		for _, stmt := range []string{
			`DELETE FROM issue WHERE workspace_id = $1`,
			`DELETE FROM agent WHERE workspace_id = $1`,
			`DELETE FROM agent_runtime WHERE workspace_id = $1`,
			`DELETE FROM skill WHERE workspace_id = $1`,
		} {
			if _, err := testPool.Exec(ctx, stmt, ws); err != nil {
				t.Errorf("cleanup aurora tool workspace: %v", err)
			}
		}
	})
	dbfx.Member(t, ws, testUserID, "owner")
	member := dbfx.User(t, "aurora tool member", "aurora-tool-"+uuid.NewString()+"@test.invalid")
	dbfx.Member(t, ws, member, "member")
	if err := testHandler.ensureAuroraCatalogSeed(context.Background(), parseUUID(ws)); err != nil {
		t.Fatal(err)
	}
	return ws, member
}

func postToolRun(t *testing.T, ws, user, toolID string, body map[string]any) *testutil.Response {
	t.Helper()
	req := withURLParam(newRequestAs(user, http.MethodPost, "/api/aurora/tools/"+toolID+"/runs", body), "toolId", toolID)
	req.Header.Set("X-Workspace-ID", ws)
	return testutil.Call(t, testHandler.CreateAuroraToolRun, req)
}

func TestCreateAuroraToolRunAssignsTheMappedExpert(t *testing.T) {
	ws, member := auroraToolWorkspace(t)
	var out struct {
		Identifier string `json:"identifier"`
		IssueID    string `json:"issue_id"`
		Expert     struct{ Slug, Name string }
	}
	postToolRun(t, ws, member, "poster", map[string]any{"source": "秋季新品海报"}).Want(http.StatusCreated).JSON(&out)
	if out.Identifier == "" || out.Expert.Slug != "marketing-ad-copywriting" {
		t.Fatalf("run = %#v", out)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM issue WHERE workspace_id=$1 AND assignee_id=(SELECT id FROM agent WHERE workspace_id=$1 AND system_key='aurora:marketing-ad-copywriting')`, ws); n != 1 {
		t.Fatalf("issues assigned to the carrier = %d, want 1", n)
	}
	postToolRun(t, ws, member, "poster", map[string]any{"source": "再来一版"}).Want(http.StatusCreated)
	if n := dbfx.Count(t, `SELECT count(*) FROM agent WHERE workspace_id=$1 AND system_key='aurora:marketing-ad-copywriting'`, ws); n != 1 {
		t.Fatalf("carriers = %d, want 1", n)
	}
}

func TestCreateAuroraToolRunRejectsAnUnknownTool(t *testing.T) {
	ws, member := auroraToolWorkspace(t)
	before := dbfx.Count(t, `SELECT count(*) FROM issue WHERE workspace_id=$1`, ws)
	postToolRun(t, ws, member, "no-such-tool", map[string]any{}).Want(http.StatusConflict)
	if after := dbfx.Count(t, `SELECT count(*) FROM issue WHERE workspace_id=$1`, ws); after != before {
		t.Fatalf("issues changed: %d -> %d", before, after)
	}
}

func TestCreateAuroraToolRunRejectsForeignAttachments(t *testing.T) {
	ws, member := auroraToolWorkspace(t)
	postToolRun(t, ws, member, "poster", map[string]any{"attachment_ids": []string{uuid.NewString()}}).Want(http.StatusBadRequest)
}

func TestCreateAuroraToolRunRefusesWhenTheRuntimeIsUnavailable(t *testing.T) {
	ws, member := auroraToolWorkspace(t)
	previous := testHandler.SandboxManager
	testHandler.SandboxManager = nil
	t.Cleanup(func() { testHandler.SandboxManager = previous })
	before := dbfx.Count(t, `SELECT count(*) FROM issue WHERE workspace_id=$1`, ws)
	postToolRun(t, ws, member, "poster", map[string]any{"source": "海报"}).Want(http.StatusServiceUnavailable)
	if after := dbfx.Count(t, `SELECT count(*) FROM issue WHERE workspace_id=$1`, ws); after != before {
		t.Fatalf("issues changed: %d -> %d", before, after)
	}
}

func TestCreateAuroraToolRunAllowsANonOwnerMember(t *testing.T) {
	ws, member := auroraToolWorkspace(t)
	postToolRun(t, ws, member, "copywriting", map[string]any{"source": "写一段文案"}).Want(http.StatusCreated)
}
