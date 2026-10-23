package handler

import (
	"net/http"
	"testing"

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
