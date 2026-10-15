package aurora

import (
	"embed"
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strings"
)

// expertBundle holds the Aurora briefs and the catalog index. Sub-skill
// bodies, references and scripts live in aurora-experts/ and are copied onto
// the cloud runtime by scripts/aurora-import-experts.mjs --import. Nothing
// here is edited by hand.
//
//go:embed experts
var expertBundle embed.FS

const (
	expertRoot        = "experts"
	expertIndexPath   = "experts/index.json"
	expertWorkflow    = "workflow.md"
	expertSubskillDir = "subskills"
)

// Expert is one vendored SkillHub skillset: the persona, the skillset workflow
// and the sub-skills the workflow orchestrates.
type Expert struct {
	Slug          string `json:"slug"`
	NameZh        string `json:"name_zh"`
	NameEn        string `json:"name_en"`
	Scene         string `json:"scene"`
	DescriptionZh string `json:"description_zh"`
	DescriptionEn string `json:"description_en"`
	// MissingSubskills names sub-skill directories the upstream download left
	// empty (marketing-social-media-operation ships an empty content-writer).
	MissingSubskills []string            `json:"missing_subskills"`
	QuickPrompts     []ExpertQuickPrompt `json:"quick_prompts"`
	Subskills        []ExpertSubskill    `json:"subskills"`
}

// ExpertQuickPrompt is one upstream quick-start card: a label and a
// fill-in-the-blanks prompt, in both languages. The seeder maps the first ones
// onto the agent's conversation starters.
type ExpertQuickPrompt struct {
	LabelZh  string `json:"label_zh"`
	LabelEn  string `json:"label_en"`
	PromptZh string `json:"prompt_zh"`
	PromptEn string `json:"prompt_en"`
}

// ExpertSubskill names one sub-skill directory the runtime import installs.
// Name is the directory under .claude/skills and is a fixed point of
// execenv.sanitizeSkillName.
type ExpertSubskill struct {
	Slug      string `json:"slug"`
	Name      string `json:"name"`
	DisplayZh string `json:"display_zh"`
	DisplayEn string `json:"display_en"`
}

type expertIndex struct {
	Schema  int      `json:"schema"`
	Source  string   `json:"source"`
	Experts []Expert `json:"experts"`
}

// expertLibrary is parsed once at init: it is compiled-in data, so a parse
// failure is a build-time defect and panics the way a broken embed would.
var expertLibrary = mustLoadExpertLibrary()

func mustLoadExpertLibrary() expertIndex {
	raw, err := expertBundle.ReadFile(expertIndexPath)
	if err != nil {
		panic(fmt.Sprintf("aurora: read expert index: %v", err))
	}
	var index expertIndex
	if err := json.Unmarshal(raw, &index); err != nil {
		panic(fmt.Sprintf("aurora: parse expert index: %v", err))
	}
	if index.Schema != 2 {
		panic(fmt.Sprintf("aurora: expert index schema = %d, want 2", index.Schema))
	}
	if len(index.Experts) == 0 {
		panic("aurora: expert index is empty")
	}
	return index
}

// Experts returns an independent copy of the vendored expert index.
func Experts() []Expert {
	out := make([]Expert, len(expertLibrary.Experts))
	for i, expert := range expertLibrary.Experts {
		out[i] = cloneExpert(expert)
	}
	return out
}

// ExpertBySlug returns an independent copy of one expert.
func ExpertBySlug(slug string) (Expert, bool) {
	for _, expert := range expertLibrary.Experts {
		if expert.Slug == slug {
			return cloneExpert(expert), true
		}
	}
	return Expert{}, false
}

// ExpertWorkflow returns the generated Aurora brief for an expert. The returned
// string is a copy the caller owns.
func ExpertWorkflow(slug string) (string, bool) {
	if !validExpertSlug(slug) {
		return "", false
	}
	if _, ok := ExpertBySlug(slug); !ok {
		return "", false
	}
	raw, err := expertBundle.ReadFile(path.Join(expertRoot, slug, expertWorkflow))
	if err != nil {
		return "", false
	}
	brief := strings.TrimSpace(string(raw))
	if brief == "" {
		return "", false
	}
	return brief, true
}

// validExpertSlug rejects anything that could escape the embedded tree: slugs
// are directory names, never paths.
func validExpertSlug(slug string) bool {
	if slug == "" || strings.ContainsAny(slug, "/\\.") {
		return false
	}
	return !strings.Contains(slug, "..")
}

func cloneExpert(expert Expert) Expert {
	expert.MissingSubskills = slices.Clone(expert.MissingSubskills)
	expert.QuickPrompts = slices.Clone(expert.QuickPrompts)
	expert.Subskills = slices.Clone(expert.Subskills)
	return expert
}
