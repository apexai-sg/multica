package aurora

import "slices"

// toolExperts is the one authority for which expert executes a tool page's
// task. The Aurora client owns the tool definitions (ids, names, form fields)
// and never decides the executor; it submits a tool id and the server resolves
// the carrier agent through this table.
//
// Two tools may share one expert (poster/copywriting and
// video-transcript/avatar-video each do), but no entry may name a slug outside
// the catalog: ensureExpert refuses unknown ids, so a stale slug would make the
// tool unexecutable.
var toolExperts = map[string]string{
	"face-swap":        "media-face-swap",
	"video-transcript": "media-short-video-copy",
	"product-images":   "ecommerce-product-copywriting",
	"poster":           "marketing-ad-copywriting",
	"rednote-images":   "marketing-social-media-operation",
	"image-retouch":    "media-image-retouch",
	"portraits":        "media-portrait-studio",
	"short-video":      "media-storyboard-design",
	"video-edit":       "media-video-editing",
	"avatar-video":     "media-short-video-copy",
	"video-translate":  "media-video-translation",
	"presentation":     "design-ui-prototype",
	"spreadsheet":      "office-spreadsheet-analysis",
	"pdf-reader":       "office-document-reading",
	"copywriting":      "marketing-ad-copywriting",
	"voiceover":        "media-voiceover",
	"website":          "design-website-build",
	"resume":           "office-resume-optimization",
}

// toolIDs is the tool page order the client ships, matching
// apps/aurora/components/tools/tool-definitions.ts. Fixing the order here keeps
// the two contract tests comparable side by side: both enumerate the same 18
// ids in the same sequence.
var toolIDs = []string{
	"face-swap",
	"video-transcript",
	"product-images",
	"poster",
	"rednote-images",
	"image-retouch",
	"portraits",
	"short-video",
	"video-edit",
	"avatar-video",
	"video-translate",
	"presentation",
	"spreadsheet",
	"pdf-reader",
	"copywriting",
	"voiceover",
	"website",
	"resume",
}

// ToolExpert returns the catalog slug that executes toolID, or false when the
// tool has no mapping. An unmapped tool is a server-side 409, never a fallback
// to an arbitrary agent.
func ToolExpert(toolID string) (string, bool) {
	slug, ok := toolExperts[toolID]
	return slug, ok
}

// ToolIDs returns the 18 tool ids in client display order. Callers must not
// mutate the result; it is the fixed contract, not per-request state.
func ToolIDs() []string {
	return slices.Clone(toolIDs)
}
