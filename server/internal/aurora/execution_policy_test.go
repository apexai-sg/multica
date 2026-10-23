package aurora_test

import (
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/aurora"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Fixed fixture identities for the attachment scope checks. They never touch a
// database: ValidateSkillAttachmentScope is a pure comparison over the loaded
// rows.
const (
	attachmentTestWorkspace = "11111111-1111-1111-1111-111111111111"
	attachmentTestUser      = "22222222-2222-2222-2222-222222222222"
	attachmentTestOtherUser = "33333333-3333-3333-3333-333333333333"
)

func pgUUID(t *testing.T, raw string) pgtype.UUID {
	t.Helper()
	parsed, err := uuid.Parse(raw)
	if err != nil {
		t.Fatalf("parse uuid %q: %v", raw, err)
	}
	return pgtype.UUID{Bytes: parsed, Valid: true}
}

// kindFixture is the filename + sniffed MIME a valid upload of each kind
// carries. The validator must accept exactly these pairings.
var kindFixture = map[string]struct {
	filename    string
	contentType string
}{
	"image":    {"photo.png", "image/png"},
	"document": {"notes.md", "text/markdown"},
	"audio":    {"voice.mp3", "audio/mpeg"},
	"video":    {"clip.mp4", "video/mp4"},
}

func attachmentRow(id, filename, contentType string, size int64) db.Attachment {
	parsed := uuid.MustParse(id)
	workspace := uuid.MustParse(attachmentTestWorkspace)
	user := uuid.MustParse(attachmentTestUser)
	return db.Attachment{
		ID:           pgtype.UUID{Bytes: parsed, Valid: true},
		WorkspaceID:  pgtype.UUID{Bytes: workspace, Valid: true},
		UploaderType: "member",
		UploaderID:   pgtype.UUID{Bytes: user, Valid: true},
		Filename:     filename,
		Url:          "https://storage.test/" + id + filepath.Ext(filename),
		ContentType:  contentType,
		SizeBytes:    size,
	}
}

func attachmentOfKind(id, kind string) db.Attachment {
	fixture, ok := kindFixture[kind]
	if !ok {
		panic("unknown attachment kind " + kind)
	}
	return attachmentRow(id, fixture.filename, fixture.contentType, 1<<20)
}

func attachmentID(i int) string {
	return fmt.Sprintf("aaaaaaaa-0000-0000-0000-%012d", i)
}

// policyFor builds a skill policy directly. The generic input validator must
// stay testable on its own: the rules every vendored expert ships with are
// pinned separately, by TestExpertInputRulesMatchTheCuratedTable.
func policyFor(skillID string, constraints ...aurora.AttachmentConstraint) aurora.SkillExecutionPolicy {
	return aurora.SkillExecutionPolicy{
		SkillID:     skillID,
		Route:       "claude-text",
		Attachments: constraints,
		OutputKinds: []string{"text"},
	}
}

func documentConstraint(min, max int) aurora.AttachmentConstraint {
	return aurora.AttachmentConstraint{
		Kinds:    []aurora.AttachmentKind{aurora.AttachmentDocument},
		Min:      min,
		Max:      max,
		MaxBytes: 25 << 20,
	}
}

func imageConstraint(min, max int) aurora.AttachmentConstraint {
	return aurora.AttachmentConstraint{
		Kinds:    []aurora.AttachmentKind{aurora.AttachmentImage},
		Min:      min,
		Max:      max,
		MaxBytes: 25 << 20,
	}
}

func mediaConstraint(kinds ...aurora.AttachmentKind) aurora.AttachmentConstraint {
	return aurora.AttachmentConstraint{Kinds: kinds, Min: 1, Max: 1, MaxBytes: 100 << 20}
}

// TestValidateSkillInputsMatrix is the acceptance matrix for the input rules:
// every shape the validator must accept or reject, over documents, images and
// audio-or-video constraints.
func TestValidateSkillInputsMatrix(t *testing.T) {
	documents := policyFor("doc-skill", documentConstraint(0, 1))
	images := policyFor("image-skill", imageConstraint(0, 4))
	requiredImage := policyFor("image-required", imageConstraint(1, 1))
	audioOrVideo := policyFor("media-skill", mediaConstraint(aurora.AttachmentAudio, aurora.AttachmentVideo))

	cases := []struct {
		name   string
		policy aurora.SkillExecutionPolicy
		kinds  []string
		want   bool
	}{
		{"document skill accepts no files", documents, nil, true},
		{"document skill accepts one document", documents, []string{"document"}, true},
		{"document skill rejects two documents", documents, []string{"document", "document"}, false},
		{"document skill rejects an image", documents, []string{"image"}, false},
		{"image skill accepts no files", images, nil, true},
		{"image skill accepts four images", images, []string{"image", "image", "image", "image"}, true},
		{"image skill rejects five images", images, []string{"image", "image", "image", "image", "image"}, false},
		{"image skill rejects a mixed batch", images, []string{"image", "document"}, false},
		{"required image rejects no files", requiredImage, nil, false},
		{"required image accepts one image", requiredImage, []string{"image"}, true},
		{"required image rejects two images", requiredImage, []string{"image", "image"}, false},
		{"media skill accepts audio", audioOrVideo, []string{"audio"}, true},
		{"media skill accepts video", audioOrVideo, []string{"video"}, true},
		{"media skill rejects audio and video together", audioOrVideo, []string{"audio", "video"}, false},
		{"media skill rejects a document", audioOrVideo, []string{"document"}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files := make([]db.Attachment, 0, len(tc.kinds))
			for i, kind := range tc.kinds {
				files = append(files, attachmentOfKind(attachmentID(i), kind))
			}

			err := aurora.ValidateSkillInputs(tc.policy, files)
			if tc.want && err != nil {
				t.Fatalf("ValidateSkillInputs(%s) = %v, want nil", tc.name, err)
			}
			if !tc.want && err == nil {
				t.Fatalf("ValidateSkillInputs(%s) = nil, want an error", tc.name)
			}
		})
	}

	// The remaining rejection classes live here too so the named matrix is
	// self-contained; the sibling tests below exercise their variations.
	t.Run("retired and unknown skills have no policy", func(t *testing.T) {
		for _, id := range []string{"poster", "avatar-video", "nope"} {
			if _, ok := aurora.ExecutionPolicy(id); ok {
				t.Fatalf("ExecutionPolicy(%q) = ok, want absent", id)
			}
		}
	})

	t.Run("MIME and extension mismatch is rejected", func(t *testing.T) {
		file := attachmentRow(attachmentID(0), "photo.png", "application/pdf", 1024)
		if err := aurora.ValidateSkillInputs(documents, []db.Attachment{file}); err == nil {
			t.Fatal("mismatched MIME accepted")
		}
	})

	t.Run("missing storage object is rejected", func(t *testing.T) {
		file := attachmentOfKind(attachmentID(0), "document")
		file.Url = ""
		if err := aurora.ValidateSkillInputs(documents, []db.Attachment{file}); err == nil {
			t.Fatal("row without a storage object accepted")
		}
	})

	t.Run("foreign workspace and owner are rejected", func(t *testing.T) {
		file := attachmentOfKind(attachmentID(0), "image")
		workspace := pgUUID(t, attachmentTestWorkspace)
		user := pgUUID(t, attachmentTestUser)
		if err := aurora.ValidateSkillAttachmentScope([]db.Attachment{file}, pgUUID(t, "44444444-4444-4444-4444-444444444444"), user); err == nil {
			t.Fatal("foreign workspace accepted")
		}
		if err := aurora.ValidateSkillAttachmentScope([]db.Attachment{file}, workspace, pgUUID(t, "55555555-5555-5555-5555-555555555555")); err == nil {
			t.Fatal("foreign owner accepted")
		}
	})
}

// TestValidateSkillInputsRejectsBadFiles covers the per-file failures the
// sniffed metadata must catch before provisioning.
func TestValidateSkillInputsRejectsBadFiles(t *testing.T) {
	const mib = 1 << 20
	documents := policyFor("doc-skill", documentConstraint(0, 1))
	images := policyFor("image-skill", imageConstraint(0, 4))
	media := policyFor("media-skill", mediaConstraint(aurora.AttachmentAudio, aurora.AttachmentVideo))

	missingObject := attachmentRow(attachmentID(0), "notes.md", "text/markdown", 1024)
	missingObject.Url = ""
	anonymous := attachmentRow(attachmentID(0), "notes.md", "text/markdown", 1024)
	anonymous.ID = pgtype.UUID{}

	cases := []struct {
		name   string
		policy aurora.SkillExecutionPolicy
		file   db.Attachment
	}{
		{"extension and sniffed MIME disagree", documents, attachmentRow(attachmentID(0), "photo.png", "application/pdf", 1024)},
		{"unknown extension", documents, attachmentRow(attachmentID(0), "photo.gif", "image/gif", 1024)},
		{"missing storage object", documents, missingObject},
		{"missing row id", documents, anonymous},
		{"zero size", documents, attachmentRow(attachmentID(0), "notes.md", "text/markdown", 0)},
		{"document over the 25 MiB cap", documents, attachmentRow(attachmentID(1), "notes.pdf", "application/pdf", 25*mib+1)},
		{"image over the 25 MiB cap", images, attachmentRow(attachmentID(0), "photo.png", "image/png", 25*mib+1)},
		{"audio over the 100 MiB cap", media, attachmentRow(attachmentID(2), "voice.mp3", "audio/mpeg", 100*mib+1)},
		{"document format on an audio-or-video skill", media, attachmentRow(attachmentID(3), "notes.md", "text/markdown", 1024)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := aurora.ValidateSkillInputs(tc.policy, []db.Attachment{tc.file}); err == nil {
				t.Fatalf("ValidateSkillInputs(%s) = nil, want an error", tc.name)
			}
		})
	}

	// The boundary itself is accepted: exactly 25 MiB is inside the cap.
	if err := aurora.ValidateSkillInputs(documents, []db.Attachment{
		attachmentRow(attachmentID(0), "notes.pdf", "application/pdf", 25*mib),
	}); err != nil {
		t.Fatalf("25 MiB document rejected: %v", err)
	}
}

// TestValidateSkillInputsRejectsForeignWorkspaceOrOwner pins the scope half of
// the input contract: an attachment row only counts when it belongs to the
// requesting workspace and the requesting human uploader.
func TestValidateSkillInputsRejectsForeignWorkspaceOrOwner(t *testing.T) {
	workspace := pgUUID(t, attachmentTestWorkspace)
	user := pgUUID(t, attachmentTestUser)
	file := attachmentOfKind(attachmentID(0), "image")

	if err := aurora.ValidateSkillAttachmentScope([]db.Attachment{file}, workspace, user); err != nil {
		t.Fatalf("same-workspace, same-owner row rejected: %v", err)
	}

	otherWorkspace := pgUUID(t, "44444444-4444-4444-4444-444444444444")
	if err := aurora.ValidateSkillAttachmentScope([]db.Attachment{file}, otherWorkspace, user); err == nil {
		t.Fatal("row from a foreign workspace accepted")
	}

	otherUser := pgUUID(t, "55555555-5555-5555-5555-555555555555")
	if err := aurora.ValidateSkillAttachmentScope([]db.Attachment{file}, workspace, otherUser); err == nil {
		t.Fatal("row uploaded by another user accepted")
	}

	agentUpload := file
	agentUpload.UploaderType = "agent"
	if err := aurora.ValidateSkillAttachmentScope([]db.Attachment{agentUpload}, workspace, user); err == nil {
		t.Fatal("agent-uploaded row accepted as a human input")
	}
}

// TestExpertInputRulesMatchTheCuratedTable pins what every expert accepts and
// where it runs. A text expert takes a written request plus at most one
// document; a media expert takes one media file at the upload path's own
// ceiling instead. Every expert runs the ordinary-agent text route.
func TestExpertInputRulesMatchTheCuratedTable(t *testing.T) {
	// mediaInputs pins the experts whose input rule is not the package default.
	// A media expert takes one media file at the upload path's own ceiling.
	mediaOne := func(kinds ...aurora.AttachmentKind) []aurora.AttachmentConstraint {
		return []aurora.AttachmentConstraint{{Kinds: kinds, Min: 0, Max: 1, MaxBytes: 100 << 20}}
	}
	mediaInputs := map[string][]aurora.AttachmentConstraint{
		"media-face-swap":         mediaOne(aurora.AttachmentImage),
		"media-image-retouch":     mediaOne(aurora.AttachmentImage),
		"media-portrait-studio":   mediaOne(aurora.AttachmentImage),
		"media-video-translation": mediaOne(aurora.AttachmentVideo, aurora.AttachmentAudio),
	}
	for _, entry := range aurora.Catalog() {
		policy, ok := aurora.ExecutionPolicy(entry.ID)
		if !ok {
			t.Fatalf("expert %q has no execution policy", entry.ID)
		}
		if policy.Route != "claude-text" {
			t.Errorf("expert %q route = %q, want claude-text", entry.ID, policy.Route)
		}
		want, pinned := mediaInputs[entry.ID]
		if !pinned {
			want = []aurora.AttachmentConstraint{documentConstraint(0, 1)}
		}
		if !reflect.DeepEqual(policy.Attachments, want) {
			t.Errorf("expert %q attachments = %#v, want %#v", entry.ID, policy.Attachments, want)
		}
	}
}

// TestExecutionPolicyCoversEveryAvailableSkill asserts the policy table is
// complete for the 33 runnable experts and absent for everything else.
func TestExecutionPolicyCoversEveryAvailableSkill(t *testing.T) {
	available := 0
	for _, entry := range aurora.Catalog() {
		policy, ok := aurora.ExecutionPolicy(entry.ID)
		if !entry.Available {
			if ok {
				t.Errorf("unavailable skill %q has an execution policy", entry.ID)
			}
			continue
		}
		available++
		if !ok {
			t.Errorf("available skill %q has no execution policy", entry.ID)
			continue
		}
		if policy.SkillID != entry.ID {
			t.Errorf("policy SkillID = %q, want %q", policy.SkillID, entry.ID)
		}
		if policy.Route == "" {
			t.Errorf("skill %q has an empty route", entry.ID)
		}
		if !reflect.DeepEqual(policy.OutputKinds, entry.Output) {
			t.Errorf("skill %q output kinds = %#v, want %#v", entry.ID, policy.OutputKinds, entry.Output)
		}
	}
	if available != len(expertIDs) {
		t.Fatalf("available experts with policies = %d, want %d", available, len(expertIDs))
	}

	for _, id := range []string{"", "nope", "poster", "avatar-video", "ppt", "excel"} {
		if _, ok := aurora.ExecutionPolicy(id); ok {
			t.Errorf("ExecutionPolicy(%q) = ok, want absent", id)
		}
	}
}
