package aurora_test

import (
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/aurora"
)

// TestEveryShippedToolHasACatalogExpert pins the tool → expert contract: the
// 18 tool pages the Aurora client ships each resolve to a catalog expert, so a
// tool added on the client without a server-side executor fails here instead of
// silently falling back to an arbitrary workspace agent.
func TestEveryShippedToolHasACatalogExpert(t *testing.T) {
	if got := len(aurora.ToolIDs()); got != 18 {
		t.Fatalf("tools = %d, want 18", got)
	}
	for _, id := range aurora.ToolIDs() {
		slug, ok := aurora.ToolExpert(id)
		if !ok {
			t.Errorf("%s has no expert mapping", id)
			continue
		}
		if _, ok := aurora.Lookup(slug); !ok {
			t.Errorf("%s maps to unknown expert %q", id, slug)
		}
		if strings.HasPrefix(slug, "aurora:") {
			t.Errorf("%s keeps a retired skill id", id)
		}
	}
}

func TestToolIDsAreUnique(t *testing.T) {
	seen := make(map[string]bool, len(aurora.ToolIDs()))
	for _, id := range aurora.ToolIDs() {
		if seen[id] {
			t.Errorf("duplicate tool id %q", id)
		}
		seen[id] = true
	}
}

func TestUnknownToolHasNoExpert(t *testing.T) {
	if _, ok := aurora.ToolExpert("no-such-tool"); ok {
		t.Fatal("unknown tool resolved")
	}
}
