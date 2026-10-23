package handler

import (
	"net/http"

	"github.com/multica-ai/multica/server/internal/aurora"
)

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
// This is the read side of the mapping CreateAuroraGeneration resolves; nothing
// in it is workspace-specific, so the payload is the constant table plus each
// expert's catalog name.
func (h *Handler) ListAuroraTools(w http.ResponseWriter, r *http.Request) {
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
