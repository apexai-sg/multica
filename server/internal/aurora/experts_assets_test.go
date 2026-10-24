package aurora_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/aurora"
)

// TestEveryCatalogExpertHasARuntimeTree keeps the node image honest. The daemon
// copies aurora-experts/<slug>/ into the task's skills directory and treats a
// missing directory as a hard prepare failure. A locally authored expert with
// no sub-skills still needs the directory to exist, or every task assigned to
// its carrier fails before the agent runs.
func TestEveryCatalogExpertHasARuntimeTree(t *testing.T) {
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve the test file path")
	}
	root := filepath.Join(filepath.Dir(self), "..", "..", "..", "aurora-experts")
	for _, expert := range aurora.Experts() {
		info, err := os.Stat(filepath.Join(root, expert.Slug))
		if err != nil || !info.IsDir() {
			t.Errorf("expert %q has no runtime tree at aurora-experts/%s", expert.Slug, expert.Slug)
		}
	}
}

// TestExpertBriefsMatchTheRuntimeDirectories is the CI guard for the split
// library. The API embeds the brief and the index. Sub-skill files live in
// aurora-experts/ and must not be compiled in. Each brief names the directory
// the daemon copies, and that name is a fixed point of sanitizeSkillName.
func TestExpertBriefsMatchTheRuntimeDirectories(t *testing.T) {
	experts := aurora.Experts()
	if len(experts) != 33 {
		t.Fatalf("experts = %d, want 33", len(experts))
	}
	for _, expert := range experts {
		brief, ok := aurora.ExpertWorkflow(expert.Slug)
		if !ok || brief == "" {
			t.Errorf("expert %q has no embedded brief", expert.Slug)
			continue
		}
		if strings.Contains(brief, expert.Slug+"--") {
			t.Errorf("expert %q brief still names a double-hyphen skill directory", expert.Slug)
		}
		for _, sub := range expert.Subskills {
			want := ".claude/skills/" + sub.Name + "/SKILL.md"
			if !strings.Contains(brief, want) {
				t.Errorf("expert %q brief does not name %s", expert.Slug, want)
			}
			if strings.Contains(sub.Name, "--") || sub.Name != strings.ToLower(sub.Name) {
				t.Errorf("expert %q sub-skill directory %q is not a sanitizeSkillName fixed point", expert.Slug, sub.Name)
			}
		}
	}

	embedRoot := filepath.Join("experts")
	err := filepath.WalkDir(embedRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && entry.Name() == "subskills" {
			t.Errorf("embedded tree still contains %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
