package aurora_test

import (
	"reflect"
	"testing"

	"github.com/multica-ai/multica/server/internal/aurora"
)

// expertIDs is the pinned catalog order. The vendored index is alphabetical by
// slug, which groups the six SkillHub scenes; a change here is a user-visible
// directory change and must be deliberate.
var expertIDs = []string{
	"content-creation-lyrics-songwriting",
	"content-creation-novel-writing",
	"content-creation-screenwriting",
	"design-prd-writing",
	"design-ui-prototype",
	"design-website-build",
	"ecommerce-bidding-strategy",
	"ecommerce-pricing-analysis",
	"ecommerce-product-copywriting",
	"ecommerce-product-selection",
	"ecommerce-promotion-planning",
	"ecommerce-user-profiling",
	"legal-contract-drafting",
	"legal-contract-generation",
	"legal-legal-research",
	"legal-litigation-strategy",
	"marketing-ad-copywriting",
	"marketing-competitor-analysis",
	"marketing-event-planning",
	"marketing-social-media-operation",
	"marketing-user-growth",
	"media-face-swap",
	"media-image-retouch",
	"media-portrait-studio",
	"media-script-breakdown",
	"media-short-video-copy",
	"media-storyboard-design",
	"media-video-editing",
	"media-video-translation",
	"media-voiceover",
	"office-document-reading",
	"office-resume-optimization",
	"office-spreadsheet-analysis",
}

func TestCatalogHasThirtyThreeExpertsAndUniqueIDs(t *testing.T) {
	cat := aurora.Catalog()
	if len(cat) != len(expertIDs) {
		t.Fatalf("expected %d experts, got %d", len(expertIDs), len(cat))
	}
	seen := map[string]bool{}
	for i, e := range cat {
		if e.ID == "" || seen[e.ID] {
			t.Fatalf("empty or duplicate skill id %q", e.ID)
		}
		seen[e.ID] = true
		if e.ID != expertIDs[i] {
			t.Errorf("catalog[%d].ID = %q, want %q", i, e.ID, expertIDs[i])
		}
		if e.Credits <= 0 {
			t.Errorf("skill %q has non-positive credits %d", e.ID, e.Credits)
		}
		if e.Name == "" || e.NameEn == "" {
			t.Errorf("skill %q missing bilingual name", e.ID)
		}
		if e.Category == "" {
			t.Errorf("skill %q has no category", e.ID)
		}
	}
}

// TestCatalogMirrorsTheVendoredIndex keeps the curated table and the vendored
// expert library from drifting: every expert has an entry and every entry has
// an expert.
func TestCatalogMirrorsTheVendoredIndex(t *testing.T) {
	experts := aurora.Experts()
	if len(experts) != len(expertIDs) {
		t.Fatalf("vendored experts = %d, want %d", len(experts), len(expertIDs))
	}
	for i, expert := range experts {
		if expert.Slug != expertIDs[i] {
			t.Errorf("vendored expert[%d] = %q, want %q", i, expert.Slug, expertIDs[i])
		}
		entry, ok := aurora.Lookup(expert.Slug)
		if !ok {
			t.Errorf("Lookup(%q) = not found", expert.Slug)
			continue
		}
		if entry.Name != expert.NameZh || entry.NameEn != expert.NameEn {
			t.Errorf("expert %q name = %q/%q, want %q/%q", expert.Slug, entry.Name, entry.NameEn, expert.NameZh, expert.NameEn)
		}
	}
}

func TestCatalogAvailability(t *testing.T) {
	for _, e := range aurora.Catalog() {
		if !e.Available {
			t.Errorf("expert %q is unavailable; every vendored expert is runnable", e.ID)
		}
		if len(e.AttachmentRules) == 0 {
			t.Errorf("expert %q exposes no attachment rules", e.ID)
		}
	}
	for _, id := range []string{"poster", "xhs-image", "avatar-video", "ppt", "excel"} {
		if aurora.Exists(id) {
			t.Errorf("retired skill %q is still in the catalog", id)
		}
	}
}

// TestCatalogCreditPrices pins every expert's credit price. The catalog is the
// single source of truth the API serves and every client displays, so a change
// here is user-visible pricing and must be deliberate.
func TestCatalogCreditPrices(t *testing.T) {
	want := map[string]int{
		"content-creation-lyrics-songwriting": 60,
		"content-creation-novel-writing":      60,
		"content-creation-screenwriting":      60,
		"design-prd-writing":                  90,
		"design-ui-prototype":                 90,
		"design-website-build":                90,
		"ecommerce-bidding-strategy":          80,
		"ecommerce-pricing-analysis":          80,
		"ecommerce-product-copywriting":       80,
		"ecommerce-product-selection":         80,
		"ecommerce-promotion-planning":        80,
		"ecommerce-user-profiling":            80,
		"legal-contract-drafting":             100,
		"legal-contract-generation":           100,
		"legal-legal-research":                100,
		"legal-litigation-strategy":           100,
		"marketing-ad-copywriting":            80,
		"marketing-competitor-analysis":       80,
		"marketing-event-planning":            80,
		"marketing-social-media-operation":    80,
		"marketing-user-growth":               80,
		"media-face-swap":                     120,
		"media-image-retouch":                 120,
		"media-portrait-studio":               120,
		"media-script-breakdown":              120,
		"media-short-video-copy":              120,
		"media-storyboard-design":             120,
		"media-video-editing":                 120,
		"media-video-translation":             120,
		"media-voiceover":                     120,
		"office-document-reading":             60,
		"office-resume-optimization":          60,
		"office-spreadsheet-analysis":         60,
	}
	cat := aurora.Catalog()
	if len(cat) != len(want) {
		t.Fatalf("catalog has %d skills, want %d", len(cat), len(want))
	}
	for _, e := range cat {
		credits, ok := want[e.ID]
		if !ok {
			t.Errorf("unexpected skill %q in catalog", e.ID)
			continue
		}
		if e.Credits != credits {
			t.Errorf("skill %q credits = %d, want %d", e.ID, e.Credits, credits)
		}
	}
}

func TestExists(t *testing.T) {
	for _, e := range aurora.Catalog() {
		if !aurora.Exists(e.ID) {
			t.Errorf("Exists(%q) = false, want true", e.ID)
		}
		got, ok := aurora.Lookup(e.ID)
		if !ok || !reflect.DeepEqual(got, e) {
			t.Errorf("Lookup(%q) = %#v, %v; want %#v, true", e.ID, got, ok, e)
		}
	}
	for _, id := range []string{"nope", "", "XHS-IMAGE"} {
		if aurora.Exists(id) {
			t.Errorf("Exists(%q) = true, want false", id)
		}
		if got, ok := aurora.Lookup(id); ok || !reflect.DeepEqual(got, aurora.SkillCatalogEntry{}) {
			t.Errorf("Lookup(%q) = %#v, %v; want zero entry, false", id, got, ok)
		}
	}
}

func TestCatalogCopiesCannotChangeOtherCallers(t *testing.T) {
	first := expertIDs[0]
	entries := aurora.Catalog()
	entries[0].ID = "changed"
	entries[0].Input[0] = "changed"
	entries[0].Output[0] = "changed"
	entry, ok := aurora.Lookup(first)
	if !ok || entry.Input[0] != "text" || entry.Output[0] != "text" {
		t.Fatalf("mutating Catalog changed Lookup: %#v, %v", entry, ok)
	}
	entry.Input[0] = "changed"
	entry.Output[0] = "changed"
	fresh := aurora.Catalog()[0]
	if fresh.ID != first || fresh.Input[0] != "text" || fresh.Output[0] != "text" {
		t.Errorf("mutating Lookup changed Catalog: %#v", fresh)
	}
}

// TestCatalogAttachmentRulesMirrorExecutionPolicy keeps the wire-facing rules
// and the executable policy from drifting: the catalog is only a projection of
// the one policy table, so every response carries exactly that skill's rules.
func TestCatalogAttachmentRulesMirrorExecutionPolicy(t *testing.T) {
	for _, entry := range aurora.Catalog() {
		policy, ok := aurora.ExecutionPolicy(entry.ID)
		if !ok {
			t.Errorf("available expert %q has no execution policy", entry.ID)
			continue
		}
		if !reflect.DeepEqual(entry.AttachmentRules, policy.Attachments) {
			t.Errorf("skill %q attachment rules = %#v, want %#v", entry.ID, entry.AttachmentRules, policy.Attachments)
		}
	}

	entry, ok := aurora.Lookup(expertIDs[0])
	if !ok || len(entry.AttachmentRules) != 1 {
		t.Fatalf("Lookup(%q) attachment rules = %#v, %v; want one document constraint", expertIDs[0], entry.AttachmentRules, ok)
	}
	if entry.AttachmentRules[0].MaxBytes != 25<<20 || entry.AttachmentRules[0].Max != 1 {
		t.Errorf("expert document cap = %+v, want one document up to 25 MiB", entry.AttachmentRules[0])
	}
}

// TestAssetKindRejectsUnlistedFormat pins the strict half of the output
// mapping. An expert declares text only, so a document or media format is
// refused rather than silently recorded as the primary output.
func TestAssetKindRejectsUnlistedFormat(t *testing.T) {
	entry, ok := aurora.Lookup(expertIDs[0])
	if !ok {
		t.Fatalf("%s not found", expertIDs[0])
	}
	if kind, ok := aurora.AssetKind(entry, "md"); !ok || kind != "text" {
		t.Errorf("AssetKind(%s, md) = %q, %v; want text, true", entry.ID, kind, ok)
	}
	if kind, ok := aurora.AssetKind(entry, ""); !ok || kind != "text" {
		t.Errorf("AssetKind(%s, empty) = %q, %v; want the primary output text, true", entry.ID, kind, ok)
	}
	for _, format := range []string{"docx", "png", "mp4", "wav"} {
		if _, ok := aurora.AssetKind(entry, format); ok {
			t.Errorf("AssetKind(%s, %s) = ok, want rejected", entry.ID, format)
		}
	}
}
