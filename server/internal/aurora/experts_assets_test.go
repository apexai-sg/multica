package aurora_test

import (
	"testing"

	"github.com/multica-ai/multica/server/internal/aurora"
)

// TestEveryIndexedFileIsEmbedded walks the vendored index and proves that every
// expert brief and every sub-skill file is readable from the embedded tree.
//
// This is the CI-side guard: a vendored file that git ignores is absent from a
// fresh checkout, and a single unreadable file makes LoadExpertSubskill drop the
// whole sub-skill. The generator's --check catches that too, but it recomputes
// from the upstream snapshot, which CI does not have.
func TestEveryIndexedFileIsEmbedded(t *testing.T) {
	experts := aurora.Experts()
	if len(experts) == 0 {
		t.Fatal("the vendored expert index is empty")
	}
	for _, expert := range experts {
		brief, ok := aurora.ExpertWorkflow(expert.Slug)
		if !ok || brief == "" {
			t.Errorf("expert %q has no embedded brief", expert.Slug)
		}
		for _, sub := range expert.Subskills {
			content, ok := aurora.LoadExpertSubskill(expert.Slug, sub.Slug)
			if !ok {
				t.Errorf("expert %q sub-skill %q is not fully embedded", expert.Slug, sub.Slug)
				continue
			}
			if content.Content == "" {
				t.Errorf("expert %q sub-skill %q has an empty SKILL.md", expert.Slug, sub.Slug)
			}
			// SKILL.md is the skill body, so it never appears among the files.
			if want := len(sub.Files) - 1; len(content.Files) != want {
				t.Errorf("expert %q sub-skill %q supporting files = %d, want %d", expert.Slug, sub.Slug, len(content.Files), want)
			}
		}
	}
}
