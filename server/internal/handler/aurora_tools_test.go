package handler

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/aurora"
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

// auroraEmptyToolWorkspace creates a workspace owned by testUserID plus an
// ordinary member, with no Aurora rows. Returns (workspaceID, memberUserID).
//
// The rows a read or a run creates — issues, carriers, skills, the managed
// runtime — are removed before the fixture's own workspace delete, because this
// schema has no cascading deletes (AGENTS.md, "Database and Migration Rules").
func auroraEmptyToolWorkspace(t *testing.T) (string, string) {
	t.Helper()
	ws := dbfx.Workspace(t, "aurora tools", "aurora-tools-"+uuid.NewString())
	t.Cleanup(func() {
		ctx := context.Background()
		for _, stmt := range []string{
			`DELETE FROM issue WHERE workspace_id = $1`,
			`DELETE FROM attachment WHERE workspace_id = $1`,
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
	return ws, member
}

// auroraToolWorkspace is auroraEmptyToolWorkspace with every carrier seeded.
func auroraToolWorkspace(t *testing.T) (string, string) {
	t.Helper()
	ws, member := auroraEmptyToolWorkspace(t)
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
	// Deliberately unseeded: the rejection must also write nothing, so a random
	// attachment id cannot provision the 33 carriers and a managed runtime (which
	// the sandbox sweeper would then keep alive).
	ws, member := auroraEmptyToolWorkspace(t)
	postToolRun(t, ws, member, "poster", map[string]any{"attachment_ids": []string{uuid.NewString()}}).Want(http.StatusBadRequest)
	if n := dbfx.Count(t, `SELECT count(*) FROM agent WHERE workspace_id=$1`, ws); n != 0 {
		t.Fatalf("carriers after a rejected attachment = %d, want 0", n)
	}
}

// TestListAuroraToolsSeedsTheWorkspaceCarriers pins the first-entry path the
// tool pages actually take: the dashboard's tool grid is static, so a fresh
// workspace reaches GET /api/aurora/tools without ever loading the skill
// catalog. If this read did not seed, the submission pre-check would find no
// aurora:<slug> carrier and every tool page would stay disabled.
func TestListAuroraToolsSeedsTheWorkspaceCarriers(t *testing.T) {
	ws, _ := auroraEmptyToolWorkspace(t)
	req := newRequest(http.MethodGet, "/api/aurora/tools", nil)
	req.Header.Set("X-Workspace-ID", ws)
	testutil.Call(t, testHandler.ListAuroraTools, req).Want(http.StatusOK)

	if n := dbfx.Count(t, `SELECT count(*) FROM agent WHERE workspace_id=$1 AND system_key LIKE 'aurora:%'`, ws); n != len(aurora.Catalog()) {
		t.Fatalf("carriers = %d, want %d", n, len(aurora.Catalog()))
	}
}

// TestEveryToolHasADisplayName keeps the server's title/description names in
// step with the tool ids: a missing name would silently title the issue with the
// raw id.
func TestEveryToolHasADisplayName(t *testing.T) {
	ids := aurora.ToolIDs()
	for _, id := range ids {
		if auroraToolNames[id] == "" {
			t.Errorf("tool %q has no display name", id)
		}
	}
	if len(auroraToolNames) != len(ids) {
		t.Errorf("display names = %d, tools = %d", len(auroraToolNames), len(ids))
	}
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

// insertToolAttachment stands in for the authenticated upload path: one row in
// this workspace, uploaded by uploader.
func insertToolAttachment(t *testing.T, workspaceID, uploader, filename, contentType string, size int64) string {
	t.Helper()
	return dbfx.Insert(t, "attachment", testutil.Cols{
		"workspace_id":  workspaceID,
		"uploader_type": "member",
		"uploader_id":   uploader,
		"filename":      filename,
		"url":           "https://storage.test/" + filename,
		"content_type":  contentType,
		"size_bytes":    size,
	})
}

// TestCreateAuroraToolRunRejectsAnotherMembersAttachment covers the ownership
// half of the attachment check: a member may only attach files they uploaded
// themselves. Without it, any workspace member could bind someone else's upload
// to a task the shared carrier then reads.
func TestCreateAuroraToolRunRejectsAnotherMembersAttachment(t *testing.T) {
	ws, member := auroraToolWorkspace(t)
	other := dbfx.User(t, "aurora other member", "aurora-other-"+uuid.NewString()+"@test.invalid")
	dbfx.Member(t, ws, other, "member")
	attachment := insertToolAttachment(t, ws, other, "theirs.png", "image/png", 1024)
	before := dbfx.Count(t, `SELECT count(*) FROM issue WHERE workspace_id=$1`, ws)
	postToolRun(t, ws, member, "poster", map[string]any{"attachment_ids": []string{attachment}}).Want(http.StatusBadRequest)
	if after := dbfx.Count(t, `SELECT count(*) FROM issue WHERE workspace_id=$1`, ws); after != before {
		t.Fatalf("issues changed: %d -> %d", before, after)
	}
}

// TestCreateAuroraToolRunAppliesTheExpertInputRules keeps the tool path and the
// catalog in one contract: face-swap submits a source video plus a face photo,
// and a third file exceeds the expert's two-file slot.
func TestCreateAuroraToolRunAppliesTheExpertInputRules(t *testing.T) {
	ws, member := auroraToolWorkspace(t)
	video := insertToolAttachment(t, ws, member, "clip.mp4", "video/mp4", 1<<20)
	photo := insertToolAttachment(t, ws, member, "face.png", "image/png", 1024)
	postToolRun(t, ws, member, "face-swap", map[string]any{"attachment_ids": []string{video, photo}}).Want(http.StatusCreated)

	extra := insertToolAttachment(t, ws, member, "extra.png", "image/png", 1024)
	before := dbfx.Count(t, `SELECT count(*) FROM issue WHERE workspace_id=$1`, ws)
	postToolRun(t, ws, member, "face-swap", map[string]any{"attachment_ids": []string{video, photo, extra}}).Want(http.StatusBadRequest)
	if after := dbfx.Count(t, `SELECT count(*) FROM issue WHERE workspace_id=$1`, ws); after != before {
		t.Fatalf("a refused third file created an issue: %d -> %d", before, after)
	}
}

// TestAuroraToolDescriptionNamesTheStoredFiles pins the one part of the issue
// body this package derives rather than receives: the 素材 line is built from
// the attachment rows, in the order the caller listed them, because the request
// no longer carries file names. A regression here is silent — the issue opens,
// the agent runs, and only the file list in the prompt is wrong.
func TestAuroraToolDescriptionNamesTheStoredFiles(t *testing.T) {
	expert := aurora.SkillCatalogEntry{ID: "marketing-ad-copywriting", Name: "广告文案专家"}

	t.Run("lists the resolved names in request order", func(t *testing.T) {
		got := auroraToolDescription("海报制作", auroraToolRunRequest{Source: "秋季新品"}, []string{"clip.mp4", "face.png"}, expert)

		want := "## 素材\nclip.mp4、face.png（见附件）"
		if !strings.Contains(got, want) {
			t.Fatalf("description missing %q:\n%s", want, got)
		}
	})

	t.Run("omits the section when there are no attachments", func(t *testing.T) {
		got := auroraToolDescription("海报制作", auroraToolRunRequest{}, nil, expert)
		if strings.Contains(got, "## 素材") {
			t.Fatalf("description invented a 素材 section with no attachments:\n%s", got)
		}
	})

	t.Run("sorts settings by field id so the same run describes itself the same way", func(t *testing.T) {
		got := auroraToolDescription("海报制作", auroraToolRunRequest{Values: map[string]string{
			"ratio": "1:1",
			"style": "简约",
		}}, nil, expert)

		style := strings.Index(got, "- ratio: 1:1")
		ratio := strings.Index(got, "- style: 简约")
		if style < 0 || ratio < 0 {
			t.Fatalf("description missing settings lines:\n%s", got)
		}
		if style > ratio {
			t.Fatalf("settings are not sorted by field id:\n%s", got)
		}
	})
}
