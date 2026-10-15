package aurora

// expertDef is the product-owned half of one expert's catalog entry. The
// vendored SkillHub skillset supplies the slug, the bilingual name, the
// description and the brief; pricing, category, feature flag and input rules
// are decisions this repository owns and curates here.
type expertDef struct {
	Slug        string
	Category    string
	Credits     int
	Featured    bool
	Attachments []AttachmentConstraint
}

// expertRoute is the execution route every expert runs on: the ordinary agent
// surface with the text-artifact contract the daemon already implements.
const expertRoute = "claude-text"

// expertRequiredTool documents what an expert brief produces. It is not a tool
// allowlist (see SkillExecutionPolicy.RequiredTools), and a brief must never
// name it.
const expertRequiredTool = "aurora.write_text_artifact"

// expertInputs is the modality set every expert accepts: a written request plus
// an optional document. expertOutputs is the single text deliverable.
var (
	expertInputs  = []string{"text", "document"}
	expertOutputs = []string{"text"}
)

// expertDefs is the curated table: one row per vendored expert, in the
// alphabetical order the vendored index already carries (which groups the six
// SkillHub scenes). Credits are deliberately scene-priced: a media workflow
// costs more than a copywriting one. A row with no vendored expert, or an
// expert with no row, fails at init (see buildCatalog).
var expertDefs = []expertDef{
	// content-creation
	{Slug: "content-creation-lyrics-songwriting", Category: "content-creation", Credits: 60, Featured: true, Attachments: documentConstraint(0, 1)},
	{Slug: "content-creation-novel-writing", Category: "content-creation", Credits: 60, Attachments: documentConstraint(0, 1)},
	{Slug: "content-creation-screenwriting", Category: "content-creation", Credits: 60, Attachments: documentConstraint(0, 1)},
	// design
	{Slug: "design-prd-writing", Category: "design", Credits: 90, Featured: true, Attachments: documentConstraint(0, 1)},
	{Slug: "design-ui-prototype", Category: "design", Credits: 90, Attachments: documentConstraint(0, 1)},
	// ecommerce
	{Slug: "ecommerce-bidding-strategy", Category: "ecommerce", Credits: 80, Featured: true, Attachments: documentConstraint(0, 1)},
	{Slug: "ecommerce-pricing-analysis", Category: "ecommerce", Credits: 80, Attachments: documentConstraint(0, 1)},
	{Slug: "ecommerce-product-copywriting", Category: "ecommerce", Credits: 80, Attachments: documentConstraint(0, 1)},
	{Slug: "ecommerce-product-selection", Category: "ecommerce", Credits: 80, Attachments: documentConstraint(0, 1)},
	{Slug: "ecommerce-promotion-planning", Category: "ecommerce", Credits: 80, Attachments: documentConstraint(0, 1)},
	{Slug: "ecommerce-user-profiling", Category: "ecommerce", Credits: 80, Attachments: documentConstraint(0, 1)},
	// legal
	{Slug: "legal-contract-drafting", Category: "legal", Credits: 100, Featured: true, Attachments: documentConstraint(0, 1)},
	{Slug: "legal-contract-generation", Category: "legal", Credits: 100, Attachments: documentConstraint(0, 1)},
	{Slug: "legal-legal-research", Category: "legal", Credits: 100, Attachments: documentConstraint(0, 1)},
	{Slug: "legal-litigation-strategy", Category: "legal", Credits: 100, Attachments: documentConstraint(0, 1)},
	// marketing
	{Slug: "marketing-ad-copywriting", Category: "marketing", Credits: 80, Featured: true, Attachments: documentConstraint(0, 1)},
	{Slug: "marketing-competitor-analysis", Category: "marketing", Credits: 80, Attachments: documentConstraint(0, 1)},
	{Slug: "marketing-event-planning", Category: "marketing", Credits: 80, Attachments: documentConstraint(0, 1)},
	{Slug: "marketing-social-media-operation", Category: "marketing", Credits: 80, Attachments: documentConstraint(0, 1)},
	{Slug: "marketing-user-growth", Category: "marketing", Credits: 80, Attachments: documentConstraint(0, 1)},
	// media
	{Slug: "media-script-breakdown", Category: "media", Credits: 120, Featured: true, Attachments: documentConstraint(0, 1)},
	{Slug: "media-short-video-copy", Category: "media", Credits: 120, Attachments: documentConstraint(0, 1)},
	{Slug: "media-storyboard-design", Category: "media", Credits: 120, Attachments: documentConstraint(0, 1)},
	{Slug: "media-video-editing", Category: "media", Credits: 120, Attachments: documentConstraint(0, 1)},
}

// expertDefFor resolves one curated row.
func expertDefFor(slug string) (expertDef, bool) {
	for _, def := range expertDefs {
		if def.Slug == slug {
			return def, true
		}
	}
	return expertDef{}, false
}
