package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/aurora"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// auroraToolNames is the display name each tool page submits under. The server
// owns these strings because it builds the issue title and description, and they
// must match the name on the button the person clicked (`tool-definitions.ts`);
// `TestEveryToolHasADisplayName` holds the two id sets together.
var auroraToolNames = map[string]string{
	"face-swap":        "图片／视频换脸",
	"video-transcript": "视频文案提取",
	"product-images":   "商品图制作",
	"poster":           "海报制作",
	"rednote-images":   "小红书图片",
	"image-retouch":    "图片精修",
	"portraits":        "人像写真",
	"short-video":      "短视频制作",
	"video-edit":       "视频剪辑",
	"avatar-video":     "数字人口播",
	"video-translate":  "视频翻译",
	"presentation":     "PPT 制作",
	"spreadsheet":      "Excel 助手",
	"pdf-reader":       "PDF 阅读助手",
	"copywriting":      "文案创作",
	"voiceover":        "音频配音",
	"website":          "网站设计助手",
	"resume":           "简历优化",
}

// auroraToolRunRequest is one tool page's submission. The wire keeps the spec's
// snake_case shape (§8.1); the TypeScript client maps its camelCase request onto
// it at the boundary.
type auroraToolRunRequest struct {
	Source        string            `json:"source"`
	Values        map[string]string `json:"values"`
	AttachmentIDs []string          `json:"attachment_ids"`
}

// auroraToolRunResponse answers a created run with what the client needs to
// open the issue and name the executor.
type auroraToolRunResponse struct {
	Identifier string              `json:"identifier"`
	IssueID    string              `json:"issue_id"`
	Expert     auroraToolExpertRef `json:"expert"`
}

type auroraToolExpertRef struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// auroraToolExpert is one tool page's executor: the tool id the client submits
// plus the catalog expert that runs it and that expert's display name.
type auroraToolExpert struct {
	ID         string `json:"id"`
	ExpertSlug string `json:"expert_slug"`
	ExpertName string `json:"expert_name"`
}

// ListAuroraTools returns the tool → expert directory in the client's display
// order. The router requires authentication and workspace membership.
//
// This is the read side of the mapping CreateAuroraToolRun resolves; the payload
// itself is the constant table plus each expert's catalog name.
//
// The read also seeds the workspace's carriers. The dashboard's tool grid is
// static client data, so a new workspace reaches a tool page without ever
// fetching the skill catalog — and the submission pre-check needs the mapped
// carrier to exist already. Seeding here is what makes the first tool page
// usable; the client reads the agent list only after this response, so the
// carriers are committed before that list is queried. A failure is logged and
// swallowed for the same reason ListAuroraSkills swallows it: the mapping is a
// constant, and a 500 would take the tool directory down over decoration.
func (h *Handler) ListAuroraTools(w http.ResponseWriter, r *http.Request) {
	if workspaceID, err := util.ParseUUID(h.resolveWorkspaceID(r)); err == nil {
		if err := h.ensureAuroraCatalogSeed(r.Context(), workspaceID); err != nil {
			slog.Warn("aurora catalog seed", "workspace_id", util.UUIDToString(workspaceID), "error", err)
		}
	}
	toolIDs := aurora.ToolIDs()
	tools := make([]auroraToolExpert, 0, len(toolIDs))
	for _, id := range toolIDs {
		slug, ok := aurora.ToolExpert(id)
		if !ok {
			// ToolIDs and toolExperts are held to the same key set by
			// TestToolExpertsAndToolIDsShareOneKeySet, so this is a broken
			// build rather than a data condition. Failing closed keeps a tool
			// page from silently resolving to an arbitrary agent.
			writeError(w, http.StatusInternalServerError, "tool mapping incomplete")
			return
		}
		entry, ok := aurora.Lookup(slug)
		if !ok {
			writeError(w, http.StatusInternalServerError, "tool mapping incomplete")
			return
		}
		tools = append(tools, auroraToolExpert{
			ID:         id,
			ExpertSlug: slug,
			ExpertName: entry.Name,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"tools": tools})
}

// auroraToolMediaNote is the closing instruction every run carries. The catalog
// experts are text agents: a tool whose provider is unavailable must say so and
// offer a workable substitute rather than fabricate an artifact.
const auroraToolMediaNote = "请完成这个任务，把生成内容作为评论或附件返回。若当前环境无法生成真实媒体文件（图片/视频/音频），请明确说明限制，并给出可执行的方案、脚本或文案作为替代。"

// CreateAuroraToolRun turns one tool page submission into an ordinary issue
// assigned to the tool's mapped expert.
//
// The order is fixed: membership, the mapping (409), the attachment check
// (400), the seed and carrier lookup, the runtime (503), then the issue. The
// checks a bad request can fail cheaply — an unknown tool, a foreign or
// malformed attachment — run before any write, so a rejected file never pays
// for 33 carriers and a managed runtime. An unreachable runtime is checked
// after the seed on purpose: the workspace needs those carriers for its next
// visit anyway, and the seed is what makes the runtime exist to test. The 503
// therefore leaves the seeded workspace in place; it does not create an issue.
//
// The issue is an ordinary one — deliberately without
// SkipAssignedAgentEnqueue — so the carrier's assigned-agent path enqueues the
// task, and the run costs no Aurora credits. Repeated submissions reuse the
// same carrier because GetAgentBySystemKey resolves the seeded one.
func (h *Handler) CreateAuroraToolRun(w http.ResponseWriter, r *http.Request) {
	workspaceID, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace_id")
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	userUUID, ok := parseUUIDOrBadRequest(w, userID, "user_id")
	if !ok {
		return
	}
	if _, ok := h.workspaceMember(w, r, util.UUIDToString(workspaceID)); !ok {
		return
	}

	toolID := chi.URLParam(r, "toolId")
	slug, ok := aurora.ToolExpert(toolID)
	if !ok {
		writeErrorCode(w, http.StatusConflict, "tool_executor_unmapped", "tool has no executor")
		return
	}
	expert, ok := aurora.Lookup(slug)
	if !ok || !expert.Available {
		// ToolIDs and the catalog are held together by the mapping tests, and
		// every catalog expert is available, so this is a broken build. Failing
		// closed beats assigning the run to an arbitrary agent.
		slog.Error("aurora tool executor unavailable", "tool_id", toolID, "expert", slug)
		writeError(w, http.StatusInternalServerError, "tool executor unavailable")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxAuroraGenerationBodyBytes)
	var req auroraToolRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Attachment validation runs before anything is written: the seed creates 33
	// carriers, 33 skills and a managed runtime, and the runtime is exactly what
	// the sandbox sweeper keeps a container alive for. A rejected file must not
	// pay that.
	attachmentIDs, ok := parseAuroraAttachmentIDs(w, req.AttachmentIDs)
	if !ok {
		return
	}
	fileNames, err := h.validateAuroraToolAttachments(r.Context(), workspaceID, userUUID, slug, attachmentIDs)
	if err != nil {
		var invalid auroraInputError
		if errors.As(err, &invalid) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		slog.Error("failed to validate tool attachments", "workspace_id", util.UUIDToString(workspaceID), "error", err)
		writeError(w, http.StatusInternalServerError, "failed to validate attachments")
		return
	}

	// Seeding is defensive on this path: the tool directory read seeds too, and
	// the client pre-checks the carrier before submitting, but a workspace seeded
	// by an older build still converges here.
	if err := h.ensureAuroraCatalogSeed(r.Context(), workspaceID); err != nil {
		slog.Error("failed to seed aurora catalog", "workspace_id", util.UUIDToString(workspaceID), "error", err)
		writeError(w, http.StatusInternalServerError, "failed to prepare tool run")
		return
	}
	if err := h.ensureAuroraExpertSeed(r.Context(), workspaceID, slug); err != nil {
		slog.Error("failed to seed aurora expert", "workspace_id", util.UUIDToString(workspaceID), "expert", slug, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to prepare tool run")
		return
	}
	carrier, err := h.Queries.GetAgentBySystemKey(r.Context(), db.GetAgentBySystemKeyParams{
		WorkspaceID: workspaceID,
		SystemKey:   pgtype.Text{String: aurora.SystemKey(slug), Valid: true},
	})
	if err != nil {
		slog.Error("aurora tool carrier missing", "workspace_id", util.UUIDToString(workspaceID), "expert", slug, "error", err)
		writeError(w, http.StatusInternalServerError, "tool executor unavailable")
		return
	}

	runtimeID, err := aurora.ManagedRuntimeID(r.Context(), h.Queries, workspaceID)
	if err != nil {
		slog.Error("failed to load aurora managed runtime", "workspace_id", util.UUIDToString(workspaceID), "error", err)
		writeError(w, http.StatusInternalServerError, "failed to prepare tool run")
		return
	}
	if err := h.ensureWorkspaceSandbox(r.Context(), workspaceID, runtimeID); err != nil {
		slog.Error("aurora sandbox unavailable", "workspace_id", util.UUIDToString(workspaceID), "error", err)
		writeErrorCode(w, http.StatusServiceUnavailable, auroraRuntimeUnavailableCode, errAuroraRuntimeUnavailable.Error())
		return
	}

	toolName := auroraToolName(toolID)
	created, err := h.IssueService.Create(r.Context(), service.IssueCreateParams{
		WorkspaceID:   workspaceID,
		Title:         auroraToolTitle(toolName, req.Source),
		Description:   pgtype.Text{String: auroraToolDescription(toolName, req, fileNames, expert), Valid: true},
		Status:        "todo",
		Priority:      "none",
		AssigneeType:  pgtype.Text{String: "agent", Valid: true},
		AssigneeID:    carrier.ID,
		CreatorType:   "member",
		CreatorID:     userUUID,
		AttachmentIDs: attachmentIDs,
		// Consumer tool tasks are repeatable; the duplicate-title guard would
		// otherwise reject the same prompt a second time.
		AllowDuplicate: true,
	}, service.IssueCreateOpts{})
	if err != nil {
		slog.Error("failed to create aurora tool issue", "workspace_id", util.UUIDToString(workspaceID), "tool_id", toolID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create tool run")
		return
	}
	issue := created.Issue
	writeJSON(w, http.StatusCreated, auroraToolRunResponse{
		Identifier: service.IssueIdentifier(h.getIssuePrefix(r.Context(), issue.WorkspaceID), issue.Number),
		IssueID:    util.UUIDToString(issue.ID),
		Expert:     auroraToolExpertRef{Slug: slug, Name: expert.Name},
	})
}

// validateAuroraToolAttachments proves every requested id resolves to a row in
// this workspace uploaded by this user, then applies the mapped expert's input
// rules — the same two checks the generation path performs. A foreign or
// unknown id is a 400 rather than a silently dropped attachment; the file list
// keeps the request order so the rules (and the agent) see the files the way the
// caller selected them.
//
// The returned names come from the stored rows, not from the request: the client
// has no reason to send them, and a second copy would let the description name a
// file the attachment is not.
func (h *Handler) validateAuroraToolAttachments(ctx context.Context, workspaceID, userID pgtype.UUID, expertSlug string, ids []pgtype.UUID) ([]string, error) {
	policy, ok := aurora.ExecutionPolicy(expertSlug)
	if !ok {
		return nil, invalidAuroraInput("expert not available")
	}
	if len(ids) == 0 {
		if err := aurora.ValidateSkillInputs(policy, nil); err != nil {
			return nil, invalidAuroraInput("%s", err)
		}
		return nil, nil
	}
	files, err := h.loadAuroraAttachments(ctx, workspaceID, ids)
	if err != nil {
		return nil, err
	}
	if err := aurora.ValidateSkillAttachmentScope(files, workspaceID, userID); err != nil {
		return nil, invalidAuroraInput("%s", err)
	}
	if err := aurora.ValidateSkillInputs(policy, files); err != nil {
		return nil, invalidAuroraInput("%s", err)
	}
	names := make([]string, 0, len(files))
	for _, row := range files {
		names = append(names, row.Filename)
	}
	return names, nil
}

// auroraToolName is the display name for a mapped tool id. An id without a name
// falls back to the id itself: the mapping test holds the two tables to the same
// key set, so this only happens in a broken build, and the run should still be
// understandable.
func auroraToolName(toolID string) string {
	if name, ok := auroraToolNames[toolID]; ok {
		return name
	}
	return toolID
}

// auroraToolTitle is "<tool> · <source, first 80 runes or 任务>", capped at 120.
// Runes, not bytes: the title is Chinese, and a byte slice would cut a
// multi-byte character in half.
func auroraToolTitle(toolName, source string) string {
	subject := aurora.TruncateRunes(strings.TrimSpace(source), 80)
	if subject == "" {
		return toolName + " · 任务"
	}
	return aurora.TruncateRunes(toolName+" · "+subject, 120)
}

// auroraToolDescription builds the issue body. The client used to do this; it
// moved here so the mapping, the executor and the instruction have one owner.
// Settings are sorted by field id so the same submission always describes
// itself the same way (map iteration would not).
func auroraToolDescription(toolName string, req auroraToolRunRequest, fileNames []string, expert aurora.SkillCatalogEntry) string {
	sections := []string{"## 工具\n" + toolName}
	if source := strings.TrimSpace(req.Source); source != "" {
		sections = append(sections, "## 需求\n"+source)
	}
	if len(req.Values) > 0 {
		keys := make([]string, 0, len(req.Values))
		for key := range req.Values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		lines := make([]string, 0, len(keys))
		for _, key := range keys {
			lines = append(lines, "- "+key+": "+req.Values[key])
		}
		sections = append(sections, "## 设置\n"+strings.Join(lines, "\n"))
	}
	if len(fileNames) > 0 {
		sections = append(sections, "## 素材\n"+strings.Join(fileNames, "、")+"（见附件）")
	}
	sections = append(sections, "## 执行专家\n"+expert.Name+"（"+expert.ID+"）")
	sections = append(sections, auroraToolMediaNote)
	return strings.Join(sections, "\n\n")
}
