// Package aurora holds the Aurora content-creation domain: the static skill
// catalog, and the credit accounting that pays for generations.
package aurora

import (
	"fmt"
	"slices"
	"strings"
)

// SkillCatalogEntry is one consumer-facing skill in the directory.
type SkillCatalogEntry struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`     // Chinese display name
	NameEn    string   `json:"name_en"`  // English display name
	Category  string   `json:"category"` // image | video | content | office
	Credits   int      `json:"credits"`
	Input     []string `json:"input"`
	Output    []string `json:"output"`
	Featured  bool     `json:"featured"`
	Available bool     `json:"available"` // false for phase-2 skills
	// AttachmentRules is the input policy projected onto the wire. It is always
	// an array (empty for skills that take no attachments) so the client never
	// has to distinguish "none" from "an older server".
	AttachmentRules []AttachmentConstraint `json:"attachment_rules"`
	// AvatarURL is the portrait this workspace set on the skill's carrier agent
	// ("aurora:<id>" in agent.system_key), already resolved for the browser.
	// Empty when the workspace never seeded that carrier or left it without an
	// avatar; the client then draws its own initials tile. Omitted rather than
	// sent empty so a client that predates the field sees the same body as
	// before, and so does a response with nothing to decorate.
	AvatarURL string `json:"avatar_url,omitempty"`
}

// catalog is the one product-facing skill directory: the 24 vendored experts in
// fixed display order (the vendored index is alphabetical by slug, which groups
// the six SkillHub scenes). It is built once from that index plus the curated
// table in experts_catalog.go, so an expert without a definition — or a
// definition without an expert — fails loudly at init.
var catalog = buildCatalog()

func buildCatalog() []SkillCatalogEntry {
	experts := Experts()
	entries := make([]SkillCatalogEntry, 0, len(experts))
	for _, expert := range experts {
		def, ok := expertDefFor(expert.Slug)
		if !ok {
			panic(fmt.Sprintf("aurora: expert %q has no catalog definition", expert.Slug))
		}
		entries = append(entries, SkillCatalogEntry{
			ID:        expert.Slug,
			Name:      expert.NameZh,
			NameEn:    expert.NameEn,
			Category:  def.Category,
			Credits:   def.Credits,
			Input:     slices.Clone(expertInputs),
			Output:    slices.Clone(expertOutputs),
			Featured:  def.Featured,
			Available: true,
		})
	}
	if len(entries) != len(expertDefs) {
		panic(fmt.Sprintf("aurora: %d experts but %d catalog definitions", len(entries), len(expertDefs)))
	}
	return entries
}

// Catalog returns an independent copy of the 24 experts in fixed display order,
// each carrying its execution policy's attachment rules. Every entry is
// available: the three phase-2 skills the catalog used to list are gone.
func Catalog() []SkillCatalogEntry {
	entries := make([]SkillCatalogEntry, len(catalog))
	for i, entry := range catalog {
		entries[i] = attachAttachmentRules(cloneEntry(entry))
	}
	return entries
}

// Lookup returns an independent copy of the entry for id, or false when unknown.
func Lookup(id string) (SkillCatalogEntry, bool) {
	entry, ok := lookupCatalogEntry(id)
	if !ok {
		return SkillCatalogEntry{}, false
	}
	return attachAttachmentRules(entry), true
}

// lookupCatalogEntry is Lookup without the policy projection. ExecutionPolicy
// uses it so the catalog and the policy cannot recurse into each other.
func lookupCatalogEntry(id string) (SkillCatalogEntry, bool) {
	for _, entry := range catalog {
		if entry.ID == id {
			return cloneEntry(entry), true
		}
	}
	return SkillCatalogEntry{}, false
}

// attachAttachmentRules projects the one policy table onto an entry. The result
// is always a non-nil slice: the wire contract is an array, never null.
func attachAttachmentRules(entry SkillCatalogEntry) SkillCatalogEntry {
	entry.AttachmentRules = []AttachmentConstraint{}
	if policy, ok := executionPolicies[entry.ID]; ok && entry.Available {
		if rules := cloneConstraints(policy.Attachments); len(rules) > 0 {
			entry.AttachmentRules = rules
		}
	}
	return entry
}

// Exists reports whether id names a catalog entry, including unavailable skills.
func Exists(id string) bool {
	for i := range catalog {
		if catalog[i].ID == id {
			return true
		}
	}
	return false
}

// AssetKind maps a reported artifact onto the aurora_asset.kind category. The
// daemon reports a file format; the catalog's Output names the categories the
// skill may produce. An exact format match among the outputs wins — it
// disambiguates multi-output skills (resume → pdf|text) — otherwise the format
// is mapped to its category and accepted only when the skill lists it. An
// unlisted format is rejected rather than silently recorded as the primary
// output. An empty format still falls back to the primary output.
func AssetKind(entry SkillCatalogEntry, format string) (string, bool) {
	if format != "" {
		for _, out := range entry.Output {
			if strings.EqualFold(out, format) {
				return out, true
			}
		}
		category, ok := assetCategoryForFormat(format)
		if !ok || !slices.Contains(entry.Output, category) {
			return "", false
		}
		return category, true
	}
	if len(entry.Output) > 0 {
		return entry.Output[0], true
	}
	return entry.Category, true
}

// assetCategoryForFormat maps a concrete file format onto the catalog output
// category it belongs to.
func assetCategoryForFormat(format string) (string, bool) {
	switch strings.ToLower(format) {
	case "png", "jpg", "jpeg", "webp", "gif":
		return "image", true
	case "mp4", "mov", "webm":
		return "video", true
	case "md", "markdown", "txt", "text":
		return "text", true
	case "pdf":
		return "pdf", true
	case "wav", "mp3", "ogg", "opus":
		return "audio", true
	case "docx", "doc":
		return "document", true
	}
	return "", false
}

func cloneEntry(entry SkillCatalogEntry) SkillCatalogEntry {
	entry.Input = slices.Clone(entry.Input)
	entry.Output = slices.Clone(entry.Output)
	entry.AttachmentRules = cloneConstraints(entry.AttachmentRules)
	return entry
}
