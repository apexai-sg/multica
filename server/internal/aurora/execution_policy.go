package aurora

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// AttachmentKind is the category of one user-supplied input file. The sandbox
// broker reasons about kinds, not concrete formats; the format is only used to
// classify and validate the file on the way in.
type AttachmentKind string

const (
	AttachmentImage    AttachmentKind = "image"
	AttachmentDocument AttachmentKind = "document"
	AttachmentAudio    AttachmentKind = "audio"
	AttachmentVideo    AttachmentKind = "video"
)

// documentAttachmentMaxBytes caps one expert input document: a written request
// plus an optional document.
const documentAttachmentMaxBytes = 25 << 20

// mediaAttachmentMaxBytes caps one expert input image, audio or video file.
// It matches the upload path's own per-file ceiling (maxUploadSize), so a media
// expert accepts every file the platform lets a user upload.
const mediaAttachmentMaxBytes = 100 << 20

// AttachmentConstraint is one rule over a set of input files. All kinds listed
// in one constraint count together, so transcription's "exactly one audio or
// video" is one constraint with two alternative kinds.
type AttachmentConstraint struct {
	Kinds    []AttachmentKind `json:"kinds"`
	Min      int              `json:"min"`
	Max      int              `json:"max"`
	MaxBytes int64            `json:"max_bytes"`
}

// SkillExecutionPolicy is the fixed, server-owned contract for one skill: the
// execution route, the input rules, and the output kinds the skill may produce.
type SkillExecutionPolicy struct {
	SkillID     string
	Route       string
	Attachments []AttachmentConstraint
	OutputKinds []string
	// RequiredTools names the provider operations this skill performs. It is no
	// longer a tool allowlist: Aurora runs execute on the ordinary agent surface,
	// so nothing narrows the model's tools to this set. It documents what the
	// skill's steps must produce, and the producer contract for the run's
	// artifact manifest is derived from Route, not from here.
	RequiredTools []string
}

// executionPolicies is the one policy table. Every curated expert runs the
// ordinary-agent text route (expertRoute): the catalog projects its attachments
// onto the wire, and the run's artifact manifest is validated against that
// route (see auroraManifestProducersByRoute).
var executionPolicies = buildExecutionPolicies()

func buildExecutionPolicies() map[string]SkillExecutionPolicy {
	policies := make(map[string]SkillExecutionPolicy, len(expertDefs))
	for _, def := range expertDefs {
		policies[def.Slug] = SkillExecutionPolicy{
			Route:         expertRoute,
			Attachments:   def.Attachments,
			RequiredTools: []string{expertRequiredTool},
		}
	}
	return policies
}

func documentConstraint(min, max int) []AttachmentConstraint {
	return []AttachmentConstraint{{
		Kinds:    []AttachmentKind{AttachmentDocument},
		Min:      min,
		Max:      max,
		MaxBytes: documentAttachmentMaxBytes,
	}}
}

// mediaConstraint is one rule over the given media kinds. Several kinds in one
// call count together, so "at most one video or audio file" is a single
// constraint with two alternative kinds rather than two rules.
func mediaConstraint(min, max int, kinds ...AttachmentKind) []AttachmentConstraint {
	return []AttachmentConstraint{{
		Kinds:    kinds,
		Min:      min,
		Max:      max,
		MaxBytes: mediaAttachmentMaxBytes,
	}}
}

// ExecutionPolicy returns an independent copy of the policy for an available
// skill. An unknown skill reports false, and so does an expert the curated
// table does not define.
func ExecutionPolicy(skillID string) (SkillExecutionPolicy, bool) {
	base, ok := executionPolicies[skillID]
	if !ok {
		return SkillExecutionPolicy{}, false
	}
	entry, ok := lookupCatalogEntry(skillID)
	if !ok || !entry.Available {
		return SkillExecutionPolicy{}, false
	}
	return SkillExecutionPolicy{
		SkillID:       skillID,
		Route:         base.Route,
		Attachments:   cloneConstraints(base.Attachments),
		OutputKinds:   slices.Clone(entry.Output),
		RequiredTools: slices.Clone(base.RequiredTools),
	}, true
}

// attachmentFormat is the accepted extension → (kind, sniffed MIME) mapping
// from the plan's Input Rules. MIMEs are the values the upload path's sniffer
// or its extension override can produce.
type attachmentFormat struct {
	Kind  AttachmentKind
	MIMEs []string
}

var attachmentFormats = map[string]attachmentFormat{
	".png":      {AttachmentImage, []string{"image/png"}},
	".jpg":      {AttachmentImage, []string{"image/jpeg"}},
	".jpeg":     {AttachmentImage, []string{"image/jpeg"}},
	".txt":      {AttachmentDocument, []string{"text/plain"}},
	".md":       {AttachmentDocument, []string{"text/markdown", "text/x-markdown", "text/plain"}},
	".markdown": {AttachmentDocument, []string{"text/markdown", "text/x-markdown", "text/plain"}},
	".pdf":      {AttachmentDocument, []string{"application/pdf"}},
	".docx":     {AttachmentDocument, []string{"application/vnd.openxmlformats-officedocument.wordprocessingml.document", "application/zip"}},
	".wav":      {AttachmentAudio, []string{"audio/wav", "audio/x-wav", "audio/wave"}},
	".mp3":      {AttachmentAudio, []string{"audio/mpeg", "audio/mp3"}},
	".ogg":      {AttachmentAudio, []string{"audio/ogg", "application/ogg"}},
	".opus":     {AttachmentAudio, []string{"audio/opus", "audio/ogg"}},
	".mp4":      {AttachmentVideo, []string{"video/mp4"}},
	".mov":      {AttachmentVideo, []string{"video/quicktime"}},
	".webm":     {AttachmentVideo, []string{"video/webm"}},
}

// classifyAttachment validates one stored attachment row and returns its kind
// and recorded size. The row only exists after a successful storage write, so
// an empty URL or non-positive size means the object was never recorded.
func classifyAttachment(file db.Attachment) (AttachmentKind, int64, error) {
	if !file.ID.Valid {
		return "", 0, errors.New("missing attachment id")
	}
	if strings.TrimSpace(file.Filename) == "" {
		return "", 0, errors.New("missing filename")
	}
	if strings.TrimSpace(file.Url) == "" {
		return "", 0, errors.New("missing storage object")
	}
	if file.SizeBytes <= 0 {
		return "", 0, errors.New("empty storage object")
	}

	ext := strings.ToLower(filepath.Ext(file.Filename))
	format, ok := attachmentFormats[ext]
	if !ok {
		return "", 0, fmt.Errorf("unsupported file extension %q", ext)
	}

	contentType := normalizeContentType(file.ContentType)
	if contentType == "" {
		return "", 0, errors.New("missing content type")
	}
	if mimeKind, known := attachmentKindForMIME(contentType); known {
		if mimeKind != format.Kind {
			return "", 0, fmt.Errorf("content type %q does not match extension %q", contentType, ext)
		}
		if !slices.Contains(format.MIMEs, contentType) {
			return "", 0, fmt.Errorf("content type %q is not accepted for extension %q", contentType, ext)
		}
	}
	return format.Kind, file.SizeBytes, nil
}

// normalizeContentType strips parameters and casing so "text/plain; charset=utf-8"
// compares as "text/plain".
func normalizeContentType(raw string) string {
	if i := strings.IndexByte(raw, ';'); i >= 0 {
		raw = raw[:i]
	}
	return strings.ToLower(strings.TrimSpace(raw))
}

// attachmentKindForMIME maps a sniffed MIME onto its kind. Generic containers
// (application/zip, application/octet-stream) are deliberately unknown, so the
// extension governs them rather than a sniff that cannot see inside.
func attachmentKindForMIME(contentType string) (AttachmentKind, bool) {
	switch {
	case strings.HasPrefix(contentType, "image/"):
		return AttachmentImage, true
	case strings.HasPrefix(contentType, "audio/"):
		return AttachmentAudio, true
	case strings.HasPrefix(contentType, "video/"):
		return AttachmentVideo, true
	case strings.HasPrefix(contentType, "text/"),
		contentType == "application/pdf",
		contentType == "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		return AttachmentDocument, true
	}
	return "", false
}

// ValidateSkillInputs enforces a skill's attachment rules over already-scoped
// rows: counts per constraint, accepted kinds, extension/sniffed-MIME
// agreement, and the per-file size cap. It is pure so the handler can run it
// before any provisioning, generation insert, or credit reservation.
func ValidateSkillInputs(policy SkillExecutionPolicy, files []db.Attachment) error {
	counts := make([]int, len(policy.Attachments))
	for i, file := range files {
		kind, size, err := classifyAttachment(file)
		if err != nil {
			return fmt.Errorf("attachment %d (%s): %w", i+1, file.Filename, err)
		}
		index := -1
		for j, constraint := range policy.Attachments {
			if slices.Contains(constraint.Kinds, kind) {
				index = j
				break
			}
		}
		if index < 0 {
			return fmt.Errorf("attachment %d (%s): %s files are not accepted by %s", i+1, file.Filename, kind, policy.SkillID)
		}
		if max := policy.Attachments[index].MaxBytes; max > 0 && size > max {
			return fmt.Errorf("attachment %d (%s): %d bytes exceeds the %d byte cap", i+1, file.Filename, size, max)
		}
		counts[index]++
	}

	for i, constraint := range policy.Attachments {
		if counts[i] < constraint.Min {
			return fmt.Errorf("%s requires at least %d %s file(s), got %d", policy.SkillID, constraint.Min, constraint.Kinds, counts[i])
		}
		if counts[i] > constraint.Max {
			return fmt.Errorf("%s accepts at most %d %s file(s), got %d", policy.SkillID, constraint.Max, constraint.Kinds, counts[i])
		}
	}
	return nil
}

// ValidateSkillAttachmentScope proves every row belongs to the requesting
// workspace and was uploaded by the requesting human. The handler loads rows by
// workspace id, so this is the second half of the ownership boundary.
func ValidateSkillAttachmentScope(files []db.Attachment, workspaceID, uploaderID pgtype.UUID) error {
	for i, file := range files {
		if file.WorkspaceID != workspaceID {
			return fmt.Errorf("attachment %d (%s) is not in this workspace", i+1, file.Filename)
		}
		if file.UploaderType != "member" || file.UploaderID != uploaderID {
			return fmt.Errorf("attachment %d (%s) was not uploaded by this user", i+1, file.Filename)
		}
	}
	return nil
}

func cloneConstraints(in []AttachmentConstraint) []AttachmentConstraint {
	if len(in) == 0 {
		// An empty policy is an empty array on the wire, never null, so the
		// client can trust the field's shape on every entry.
		return []AttachmentConstraint{}
	}
	out := make([]AttachmentConstraint, len(in))
	for i, constraint := range in {
		out[i] = constraint
		out[i].Kinds = slices.Clone(constraint.Kinds)
	}
	return out
}
