package aurora_test

import (
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/aurora"
)

// auroraManifestSchema is the one "aurora.<name>" token a skill document may
// contain. The deleted broker namespaced every reviewed tool as "aurora.<verb>"
// and named them in the briefs; an Aurora task now runs on the ordinary agent
// surface, so a document must carry its own shell steps instead. This schema
// name is not a tool, which is why it is the sole exception.
const auroraManifestSchema = "com.multica.aurora.artifacts"

// auroraToolPattern extracts tool-shaped tokens from a brief. Only
// auroraManifestSchema's tail may match.
var auroraToolPattern = regexp.MustCompile(`aurora.[a-z0-9_]+`)

func availableSkills() []string {
	var ids []string
	for _, entry := range aurora.Catalog() {
		if entry.Available {
			ids = append(ids, entry.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

// TestAvailableSkillsHaveCanonicalWorkflows asserts the vendored expert library
// carries exactly one brief per available expert and that each brief names its
// expert. What a brief may say is TestRewrittenWorkflowsDescribeRealSteps.
func TestAvailableSkillsHaveCanonicalWorkflows(t *testing.T) {
	available := availableSkills()
	if len(available) != 33 {
		t.Fatalf("available skills = %d, want 33", len(available))
	}

	// The vendored index is the "exactly one" ground truth: no expert without a
	// brief, and no brief without an expert. scripts/aurora-import-experts.mjs
	// --check keeps the tree itself in step with the upstream snapshot.
	var vendored []string
	for _, expert := range aurora.Experts() {
		vendored = append(vendored, expert.Slug)
		brief, ok := aurora.ExpertWorkflow(expert.Slug)
		if !ok || strings.TrimSpace(brief) == "" {
			t.Errorf("vendored expert %q has no brief", expert.Slug)
		}
	}
	sort.Strings(vendored)
	if !slices.Equal(vendored, available) {
		t.Fatalf("vendored experts = %v, want exactly %v", vendored, available)
	}

	for _, skill := range available {
		brief, ok := aurora.Workflow(skill)
		if !ok {
			t.Errorf("Workflow(%q) = missing, want a brief", skill)
			continue
		}
		if strings.TrimSpace(brief) == "" {
			t.Errorf("Workflow(%q) is empty", skill)
		}
		if !strings.Contains(brief, skill) {
			t.Errorf("Workflow(%q) does not name its skill", skill)
		}
	}
}

// TestMediaExpertsNameTheirProviderPath holds the locally authored experts to
// the honesty rule: a brief that touches media either names the pinned provider
// model it actually calls, or states plainly that this environment has no media
// provider. A brief that names neither invites a fabricated deliverable.
func TestMediaExpertsNameTheirProviderPath(t *testing.T) {
	pinned := []string{"doubao-seedream-5-0-pro-260628", "doubao-seedance-2-5-260628"}
	const noProvider = "当前环境没有可用的媒体生成 provider"
	for _, slug := range []string{
		"media-face-swap", "media-image-retouch", "media-portrait-studio",
		"media-video-translation", "media-voiceover", "office-spreadsheet-analysis",
		"office-document-reading", "office-resume-optimization", "design-website-build",
	} {
		brief, ok := aurora.Workflow(slug)
		if !ok {
			t.Fatalf("%s has no workflow", slug)
		}
		if !strings.Contains(brief, noProvider) && !slices.ContainsFunc(pinned, func(id string) bool { return strings.Contains(brief, id) }) {
			t.Errorf("%s names neither a pinned model id nor the no-provider clause", slug)
		}
	}
}

// TestUnknownSkillsHaveNoWorkflow keeps a retired or unknown skill
// non-executable: every vendored expert is available, and an id outside the
// catalog must have no brief.
func TestUnknownSkillsHaveNoWorkflow(t *testing.T) {
	for _, entry := range aurora.Catalog() {
		if !entry.Available {
			t.Errorf("catalog entry %q is unavailable", entry.ID)
		}
	}
	for _, id := range []string{"", "unknown", "poster", "avatar-video", "ppt", "excel"} {
		if brief, ok := aurora.Workflow(id); ok || brief != "" {
			t.Errorf("Workflow(%q) = (%q, %v), want no brief", id, brief, ok)
		}
	}
}

// TestWorkflowToolsMatchExecutionPolicy is the contract between the prompt and
// the server-owned policy. The policy's RequiredTools used to be the daemon's
// tool allowlist, so the older form of this test asserted that a brief named
// exactly them; the daemon no longer reads the field (the broker is gone), but
// the table still names the vendored producer each route runs on. A brief must
// name none of them: an Aurora document reaches its provider through the shell,
// not through a brokered tool.
func TestWorkflowToolsMatchExecutionPolicy(t *testing.T) {
	policyTools := map[string]bool{}
	for _, entry := range aurora.Catalog() {
		if !entry.Available {
			continue
		}
		policy, ok := aurora.ExecutionPolicy(entry.ID)
		if !ok {
			t.Fatalf("ExecutionPolicy(%q) not found", entry.ID)
		}
		if len(policy.RequiredTools) == 0 {
			t.Fatalf("ExecutionPolicy(%q) has no required tools", entry.ID)
		}
		if policy.Route == "" {
			t.Fatalf("ExecutionPolicy(%q) has no route", entry.ID)
		}
		for _, tool := range policy.RequiredTools {
			policyTools[tool] = true
		}
	}
	if len(policyTools) == 0 {
		t.Fatal("no execution policy declares required tools")
	}

	for _, entry := range aurora.Catalog() {
		if !entry.Available {
			continue
		}
		brief, ok := aurora.Workflow(entry.ID)
		if !ok {
			t.Fatalf("Workflow(%q) = missing", entry.ID)
		}
		for tool := range policyTools {
			if strings.Contains(brief, tool) {
				t.Errorf("workflow %q still names broker tool %q", entry.ID, tool)
			}
		}
	}
}

// TestRewrittenWorkflowsDescribeRealSteps is the contract for a brief an
// ordinary agent can execute. Every available skill is held to it: the brief
// carries the five fixed sections, describes real steps, and never falls back
// to the deleted broker's vocabulary.
func TestRewrittenWorkflowsDescribeRealSteps(t *testing.T) {
	for _, id := range availableSkills() {
		brief, ok := aurora.Workflow(id)
		if !ok {
			t.Fatalf("skill %q has no workflow document", id)
		}
		for _, banned := range []string{"mcp__aurora__", "aurora.seedream_generate", "aurora.seedance_generate",
			"aurora.openai_image", "aurora.volc_asr_transcribe", "aurora.read_document", "aurora.id_photo",
			"aurora.render_video_captions", "aurora.render_resume", "aurora.write_text_artifact",
			"MCP broker", "brokered tool", "no shell"} {
			if strings.Contains(brief, banned) {
				t.Errorf("skill %q still references %q", id, banned)
			}
		}
		// The banned list only names the tools that existed. A rewritten tool
		// name would slip past it, and past the policy names too, so match the
		// namespace itself and allow only the manifest schema.
		for _, ref := range auroraToolPattern.FindAllString(brief, -1) {
			if ref != "aurora.artifacts" {
				t.Errorf("skill %q names %q: that is a broker tool name, not a step", id, ref)
			}
		}
		if !strings.Contains(brief, auroraManifestSchema) {
			t.Errorf("skill %q does not name the artifact manifest schema %q", id, auroraManifestSchema)
		}
		for _, want := range []string{"## Inputs", "## Steps", "## Required outputs", "## Artifact manifest", "## Failure behavior"} {
			if !strings.Contains(brief, want) {
				t.Errorf("skill %q is missing the %q section", id, want)
			}
		}
	}
}

// TestEveryAvailableSkillHasADocument keeps the catalog and the vendored bundle
// in step.
func TestEveryAvailableSkillHasADocument(t *testing.T) {
	for _, e := range aurora.Catalog() {
		if _, ok := aurora.Workflow(e.ID); e.Available && !ok {
			t.Errorf("available skill %q has no workflow document", e.ID)
		}
	}
}
