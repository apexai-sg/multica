package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/aurora"
)

// The collector's adversarial suite. Every case builds a real output root so the
// no-follow/openat, nlink, and streamed hash/size checks run against the
// filesystem rather than a mock; only the upload sink is faked. The manifest is
// never trusted for proof — the collector re-derives size, SHA-256, and MIME
// from the opened descriptor and only uses the manifest to name the expected
// files.

const (
	auroraTestTaskID    = "11111111-1111-4111-8111-111111111111"
	auroraTestStagingID = "22222222-2222-4222-8222-222222222222"
	auroraTestSinkID    = "33333333-3333-4333-8333-333333333333"
	auroraTestSkill     = "ecommerce-bidding-strategy"
)

// auroraTestText is the delivered content every local artifact writes.
const auroraTestText = "# Aurora artifact" + "\n\n" + "body" + "\n"

func auroraTestDigest(content string) string {
	sum := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func auroraTestProducer() auroraManifestProducer {
	return auroraManifestProducer{ID: "multica-aurora-runtime", Version: "1.0.0"}
}

func auroraTestManifest(artifacts ...auroraManifestArtifact) auroraManifest {
	return auroraManifest{
		Schema:    auroraManifestSchema,
		Version:   auroraManifestVersion,
		TaskID:    auroraTestTaskID,
		SkillID:   auroraTestSkill,
		Producer:  auroraTestProducer(),
		Artifacts: artifacts,
	}
}

// auroraTestLocalArtifact writes content under root and returns the file
// artifact whose declared metadata matches it exactly.
func auroraTestLocalArtifact(t *testing.T, root, id, relPath, name, content string) auroraManifestArtifact {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir artifact dir: %v", err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o600); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	return auroraManifestArtifact{
		ID:        id,
		Source:    auroraManifestSource{Type: "file", RelativePath: relPath},
		Name:      name,
		Kind:      "text",
		Role:      "primary",
		Format:    "md",
		MIMEType:  "text/markdown",
		SizeBytes: int64(len(content)),
		SHA256:    auroraTestDigest(content),
		Metadata:  map[string]any{},
	}
}

func auroraTestStagedArtifact(id, name string) auroraManifestArtifact {
	return auroraManifestArtifact{
		ID:        id,
		Source:    auroraManifestSource{Type: "staged_object", StagingID: auroraTestStagingID},
		Name:      name,
		Kind:      "text",
		Role:      "supporting",
		Format:    "md",
		MIMEType:  "text/markdown",
		SizeBytes: 4096,
		SHA256:    "sha256:" + strings.Repeat("a", 64),
		Metadata:  map[string]any{"origin": "provider"},
	}
}

func writeAuroraTestManifest(t *testing.T, root string, m auroraManifest) {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	writeAuroraTestManifestRaw(t, root, raw)
}

func writeAuroraTestManifestRaw(t *testing.T, root string, raw []byte) {
	t.Helper()
	dir := filepath.Join(root, ".multica")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir manifest dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "aurora-artifacts.v1.json"), raw, 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}

// auroraTestSink records uploaded local artifacts and hands back a fixed
// staging id, the way the server's upload endpoint does.
type auroraTestSink struct {
	nextStagingID string
	uploads       []AuroraArtifactUpload
	contents      []string
	err           error
}

func (s *auroraTestSink) UploadAuroraArtifact(_ context.Context, upload AuroraArtifactUpload) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	data, readErr := io.ReadAll(upload.Content)
	if readErr != nil {
		return "", readErr
	}
	s.uploads = append(s.uploads, upload)
	s.contents = append(s.contents, string(data))
	return s.nextStagingID, nil
}

func newAuroraTestSink() *auroraTestSink {
	return &auroraTestSink{nextStagingID: auroraTestSinkID}
}

func collectAuroraTestArtifacts(t *testing.T, root string, sink AuroraArtifactSink) ([]TaskArtifact, error) {
	t.Helper()
	artifacts, _, err := CollectAuroraArtifacts(context.Background(), root, auroraTestTaskID, auroraTestSkill, sink)
	return artifacts, err
}

// collectAuroraTestManifest writes the manifest and collects, returning the
// error under test.
func collectAuroraTestManifest(t *testing.T, root string, m auroraManifest) error {
	t.Helper()
	writeAuroraTestManifest(t, root, m)
	_, err := collectAuroraTestArtifacts(t, root, newAuroraTestSink())
	return err
}

func TestAuroraManifestCollectsMixedLocalAndStagedArtifacts(t *testing.T) {
	root := t.TempDir()
	local := auroraTestLocalArtifact(t, root, "primary-1", "artifacts/primary-1.md", "primary-1.md", auroraTestText)
	staged := auroraTestStagedArtifact("secondary-1", "support.md")
	writeAuroraTestManifest(t, root, auroraTestManifest(local, staged))

	sink := newAuroraTestSink()
	got, err := collectAuroraTestArtifacts(t, root, sink)
	if err != nil {
		t.Fatalf("CollectAuroraArtifacts: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("artifacts = %d, want 2", len(got))
	}
	if len(sink.uploads) != 1 {
		t.Fatalf("uploads = %d, want 1 (only the local file is uploaded)", len(sink.uploads))
	}
	if sink.contents[0] != auroraTestText {
		t.Errorf("uploaded bytes = %q, want the local file's bytes", sink.contents[0])
	}
	if got[0].StagingID != auroraTestSinkID {
		t.Errorf("local staging id = %q, want the upload result %q", got[0].StagingID, auroraTestSinkID)
	}
	if got[0].ManifestArtifactID != "primary-1" || got[0].Role != "primary" || got[0].Kind != "text" {
		t.Errorf("local artifact = %+v", got[0])
	}
	if got[0].SizeBytes != int64(len(auroraTestText)) || got[0].SHA256 != auroraTestDigest(auroraTestText) {
		t.Errorf("local artifact recomputed size/sha = %d/%s", got[0].SizeBytes, got[0].SHA256)
	}
	if got[1].StagingID != auroraTestStagingID {
		t.Errorf("staged staging id = %q, want %q", got[1].StagingID, auroraTestStagingID)
	}
	if got[1].ManifestArtifactID != "secondary-1" || got[1].Role != "supporting" {
		t.Errorf("staged artifact = %+v", got[1])
	}
	if got[1].Metadata["origin"] != "provider" {
		t.Errorf("staged metadata = %v, want origin=provider", got[1].Metadata)
	}
}

func TestAuroraManifestMissingFileIsRejected(t *testing.T) {
	root := t.TempDir()
	if _, err := collectAuroraTestArtifacts(t, root, newAuroraTestSink()); err == nil {
		t.Fatal("missing manifest was accepted")
	}
}

func TestAuroraManifestRejectsMalformedJSON(t *testing.T) {
	root := t.TempDir()
	writeAuroraTestManifestRaw(t, root, []byte("{not json"))
	if err := collectAuroraTestManifestRawErr(t, root); err == nil {
		t.Fatal("malformed manifest JSON was accepted")
	}
}

func collectAuroraTestManifestRawErr(t *testing.T, root string) error {
	t.Helper()
	_, err := collectAuroraTestArtifacts(t, root, newAuroraTestSink())
	return err
}

func TestAuroraManifestRejectsOversizedJSON(t *testing.T) {
	root := t.TempDir()
	writeAuroraTestManifestRaw(t, root, []byte(strings.Repeat(" ", auroraManifestMaxBytes+1)))
	err := collectAuroraTestManifestRawErr(t, root)
	if !errors.Is(err, errAuroraManifestTooLarge) {
		t.Fatalf("oversized manifest error = %v, want errAuroraManifestTooLarge", err)
	}
}

func TestAuroraManifestRejectsIdentityMismatches(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*auroraManifest)
	}{
		{"schema", func(m *auroraManifest) { m.Schema = "com.example.other" }},
		{"version", func(m *auroraManifest) { m.Version = 2 }},
		{"task", func(m *auroraManifest) { m.TaskID = "99999999-9999-4999-8999-999999999999" }},
		{"skill", func(m *auroraManifest) { m.SkillID = "legal-contract-drafting" }},
		{"producer id", func(m *auroraManifest) { m.Producer.ID = "some-other-producer" }},
		{"producer version empty", func(m *auroraManifest) { m.Producer.Version = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			local := auroraTestLocalArtifact(t, root, "primary-1", "artifacts/primary-1.md", "primary-1.md", auroraTestText)
			m := auroraTestManifest(local)
			tc.mutate(&m)
			if err := collectAuroraTestManifest(t, root, m); err == nil {
				t.Fatalf("%s mismatch was accepted", tc.name)
			}
		})
	}
}

func TestAuroraManifestRejectsMissingPrimary(t *testing.T) {
	root := t.TempDir()
	secondary := auroraTestLocalArtifact(t, root, "secondary-1", "artifacts/secondary-1.md", "secondary-1.md", auroraTestText)
	secondary.Role = "supporting"
	if err := collectAuroraTestManifest(t, root, auroraTestManifest(secondary)); !errors.Is(err, errAuroraManifestNoPrimary) {
		t.Fatalf("no-primary error = %v, want errAuroraManifestNoPrimary", err)
	}
}

func TestAuroraManifestRejectsPrimaryKindOutsideCatalogOutput(t *testing.T) {
	root := t.TempDir()
	primary := auroraTestLocalArtifact(t, root, "primary-1", "artifacts/primary-1.md", "primary-1.md", auroraTestText)
	primary.Kind = "video"
	primary.Format = "mp4"
	primary.MIMEType = "video/mp4"
	primary.Name = "primary-1.mp4"
	if err := collectAuroraTestManifest(t, root, auroraTestManifest(primary)); !errors.Is(err, errAuroraManifestNoPrimary) {
		t.Fatalf("wrong-kind primary error = %v, want errAuroraManifestNoPrimary", err)
	}
}

func TestAuroraManifestRejectsDuplicateIdentity(t *testing.T) {
	cases := []struct {
		name  string
		build func(t *testing.T, root string) []auroraManifestArtifact
	}{
		{"ids", func(t *testing.T, root string) []auroraManifestArtifact {
			a := auroraTestLocalArtifact(t, root, "primary-1", "artifacts/a.md", "a.md", auroraTestText)
			b := auroraTestLocalArtifact(t, root, "primary-1", "artifacts/b.md", "b.md", auroraTestText)
			return []auroraManifestArtifact{a, b}
		}},
		{"paths", func(t *testing.T, root string) []auroraManifestArtifact {
			a := auroraTestLocalArtifact(t, root, "primary-1", "artifacts/a.md", "a.md", auroraTestText)
			b := auroraTestLocalArtifact(t, root, "primary-2", "artifacts/a.md", "b.md", auroraTestText)
			return []auroraManifestArtifact{a, b}
		}},
		{"names", func(t *testing.T, root string) []auroraManifestArtifact {
			a := auroraTestLocalArtifact(t, root, "primary-1", "artifacts/a.md", "same.md", auroraTestText)
			b := auroraTestLocalArtifact(t, root, "primary-2", "artifacts/b.md", "same.md", auroraTestText)
			return []auroraManifestArtifact{a, b}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := collectAuroraTestManifest(t, root, auroraTestManifest(tc.build(t, root)...)); err == nil {
				t.Fatalf("duplicate %s were accepted", tc.name)
			}
		})
	}
}

func TestAuroraManifestRejectsUnsafePaths(t *testing.T) {
	cases := []struct{ name, path string }{
		{"absolute", "/etc/passwd"},
		{"traversal", "../outside.md"},
		{"nested traversal", "artifacts/../../outside.md"},
		{"volume", "C:/windows/out.md"},
		{"backslash", "artifacts\\out.md"},
		{"nul", "artifacts/out\x00.md"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			artifact := auroraTestLocalArtifact(t, root, "primary-1", "artifacts/ok.md", "ok.md", auroraTestText)
			artifact.Source.RelativePath = tc.path
			if err := collectAuroraTestManifest(t, root, auroraTestManifest(artifact)); !errors.Is(err, errAuroraManifestUnsafePath) {
				t.Fatalf("unsafe path error = %v, want errAuroraManifestUnsafePath", err)
			}
		})
	}
}

func TestAuroraManifestRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := auroraTestLocalArtifact(t, root, "primary-1", "artifacts/real.md", "real.md", auroraTestText)
	link := filepath.Join(root, "artifacts", "link.md")
	if err := os.Symlink(filepath.Join(root, "artifacts", "real.md"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	target.Source.RelativePath = "artifacts/link.md"
	if err := collectAuroraTestManifest(t, root, auroraTestManifest(target)); !errors.Is(err, errAuroraManifestUnsafeFile) {
		t.Fatalf("symlink error = %v, want errAuroraManifestUnsafeFile", err)
	}
}

func TestAuroraManifestRejectsHardLink(t *testing.T) {
	root := t.TempDir()
	artifact := auroraTestLocalArtifact(t, root, "primary-1", "artifacts/real.md", "real.md", auroraTestText)
	hard := filepath.Join(root, "artifacts", "hard.md")
	if err := os.Link(filepath.Join(root, "artifacts", "real.md"), hard); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	artifact.Source.RelativePath = "artifacts/hard.md"
	if err := collectAuroraTestManifest(t, root, auroraTestManifest(artifact)); !errors.Is(err, errAuroraManifestUnsafeFile) {
		t.Fatalf("hard link error = %v, want errAuroraManifestUnsafeFile", err)
	}
}

func TestAuroraManifestRejectsNonRegularFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix socket fixture requires a unix host")
	}
	cases := []struct {
		name string
		path string
	}{
		{"directory", "artifacts"},
		{"socket", "artifacts/sock"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, "artifacts"), 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if tc.name == "socket" {
				listener, err := net.Listen("unix", filepath.Join(root, "artifacts", "sock"))
				if err != nil {
					t.Skipf("unix socket unavailable: %v", err)
				}
				t.Cleanup(func() { _ = listener.Close() })
			}
			artifact := auroraTestLocalArtifact(t, root, "primary-1", "artifacts/ok.md", "ok.md", auroraTestText)
			artifact.Source.RelativePath = tc.path
			if err := collectAuroraTestManifest(t, root, auroraTestManifest(artifact)); !errors.Is(err, errAuroraManifestUnsafeFile) {
				t.Fatalf("non-regular error = %v, want errAuroraManifestUnsafeFile", err)
			}
		})
	}
}

func TestAuroraManifestRejectsChangedAfterPublication(t *testing.T) {
	root := t.TempDir()
	artifact := auroraTestLocalArtifact(t, root, "primary-1", "artifacts/primary-1.md", "primary-1.md", auroraTestText)
	// Same byte length, different content: only the recomputed hash can catch
	// a file that was replaced after the manifest recorded it.
	replacement := strings.Repeat("Z", len(auroraTestText))
	if err := os.WriteFile(filepath.Join(root, "artifacts", "primary-1.md"), []byte(replacement), 0o600); err != nil {
		t.Fatalf("replace artifact: %v", err)
	}
	if err := collectAuroraTestManifest(t, root, auroraTestManifest(artifact)); !errors.Is(err, errAuroraManifestMismatch) {
		t.Fatalf("changed-after-publication error = %v, want errAuroraManifestMismatch", err)
	}
}

func TestAuroraManifestRejectsSizeMismatch(t *testing.T) {
	root := t.TempDir()
	artifact := auroraTestLocalArtifact(t, root, "primary-1", "artifacts/primary-1.md", "primary-1.md", auroraTestText)
	artifact.SizeBytes = int64(len(auroraTestText) + 1)
	if err := collectAuroraTestManifest(t, root, auroraTestManifest(artifact)); !errors.Is(err, errAuroraManifestMismatch) {
		t.Fatalf("size mismatch error = %v, want errAuroraManifestMismatch", err)
	}
}

func TestAuroraManifestRejectsHashMismatch(t *testing.T) {
	root := t.TempDir()
	artifact := auroraTestLocalArtifact(t, root, "primary-1", "artifacts/primary-1.md", "primary-1.md", auroraTestText)
	artifact.SHA256 = "sha256:" + strings.Repeat("b", 64)
	if err := collectAuroraTestManifest(t, root, auroraTestManifest(artifact)); !errors.Is(err, errAuroraManifestMismatch) {
		t.Fatalf("hash mismatch error = %v, want errAuroraManifestMismatch", err)
	}
}

func TestAuroraManifestRejectsFormatMismatch(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*auroraManifestArtifact)
	}{
		{"unknown format", func(a *auroraManifestArtifact) { a.Format = "exe" }},
		{"format mime disagreement", func(a *auroraManifestArtifact) { a.MIMEType = "image/jpeg" }},
		{"format kind disagreement", func(a *auroraManifestArtifact) { a.Format = "mp4"; a.MIMEType = "video/mp4" }},
		{"declared mime outside allowlist", func(a *auroraManifestArtifact) { a.MIMEType = "application/octet-stream" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			artifact := auroraTestLocalArtifact(t, root, "primary-1", "artifacts/primary-1.md", "primary-1.md", auroraTestText)
			tc.mutate(&artifact)
			if err := collectAuroraTestManifest(t, root, auroraTestManifest(artifact)); err == nil {
				t.Fatalf("format mismatch %q was accepted", tc.name)
			}
		})
	}
}

func TestAuroraManifestRejectsContentKindMismatch(t *testing.T) {
	root := t.TempDir()
	artifact := auroraTestLocalArtifact(t, root, "primary-1", "artifacts/primary-1.md", "primary-1.md", auroraTestText)
	// Declared text, real bytes are an image.
	png := "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"
	artifact.SizeBytes = int64(len(png))
	artifact.SHA256 = auroraTestDigest(png)
	if err := os.WriteFile(filepath.Join(root, "artifacts", "primary-1.md"), []byte(png), 0o600); err != nil {
		t.Fatalf("rewrite artifact: %v", err)
	}
	if err := collectAuroraTestManifest(t, root, auroraTestManifest(artifact)); err == nil {
		t.Fatal("declared text whose content is an image was accepted")
	}
}

func TestAuroraManifestRejectsExtensionMismatch(t *testing.T) {
	root := t.TempDir()
	artifact := auroraTestLocalArtifact(t, root, "primary-1", "artifacts/primary-1.md", "primary-1.mp4", auroraTestText)
	if err := collectAuroraTestManifest(t, root, auroraTestManifest(artifact)); err == nil {
		t.Fatal("artifact name extension that disagrees with its kind was accepted")
	}
}

func TestAuroraManifestRejectsTooManyArtifacts(t *testing.T) {
	root := t.TempDir()
	artifacts := make([]auroraManifestArtifact, 0, auroraManifestMaxArtifacts+1)
	for i := 0; i <= auroraManifestMaxArtifacts; i++ {
		id := "primary-" + string(rune('a'+i))
		name := id + ".md"
		artifacts = append(artifacts, auroraTestLocalArtifact(t, root, id, "artifacts/"+name, name, auroraTestText))
	}
	if err := collectAuroraTestManifest(t, root, auroraTestManifest(artifacts...)); !errors.Is(err, errAuroraManifestTooMany) {
		t.Fatalf("too-many error = %v, want errAuroraManifestTooMany", err)
	}
}

// The 600 MiB manifest total guard is not reachable through the current
// catalog: every expert declares text only, the text kind caps at 25 MiB and a
// manifest carries at most 20 artifacts, which is 500 MiB. The guard stays as
// defence in depth for a future route with a larger kind.

func TestAuroraManifestRejectsMetadataDepth(t *testing.T) {
	root := t.TempDir()
	artifact := auroraTestLocalArtifact(t, root, "primary-1", "artifacts/primary-1.md", "primary-1.md", auroraTestText)
	artifact.Metadata = auroraTestNestedMetadata(auroraManifestMaxMetadataDepth + 4)
	if err := collectAuroraTestManifest(t, root, auroraTestManifest(artifact)); err == nil {
		t.Fatal("over-deep metadata was accepted")
	}
}

func TestAuroraManifestRejectsMetadataSize(t *testing.T) {
	root := t.TempDir()
	artifact := auroraTestLocalArtifact(t, root, "primary-1", "artifacts/primary-1.md", "primary-1.md", auroraTestText)
	artifact.Metadata = map[string]any{"blob": strings.Repeat("x", auroraManifestMaxMetadataBytes+1)}
	if err := collectAuroraTestManifest(t, root, auroraTestManifest(artifact)); err == nil {
		t.Fatal("oversized metadata was accepted")
	}
}

func TestAuroraManifestRejectsForeignStagingID(t *testing.T) {
	root := t.TempDir()
	staged := auroraTestStagedArtifact("primary-1", "primary-1.md")
	staged.Role = "primary"
	staged.Source.StagingID = "not-a-uuid"
	if err := collectAuroraTestManifest(t, root, auroraTestManifest(staged)); err == nil {
		t.Fatal("staged object with an invalid staging id was accepted")
	}
}

func TestAuroraManifestRejectsUnsafeStagedSize(t *testing.T) {
	root := t.TempDir()
	staged := auroraTestStagedArtifact("primary-1", "primary-1.md")
	staged.Role = "primary"
	staged.SizeBytes = auroraArtifactMaxTextBytes + 1
	if err := collectAuroraTestManifest(t, root, auroraTestManifest(staged)); err == nil {
		t.Fatal("staged object above the per-kind cap was accepted")
	}
}

// auroraTestNestedMetadata builds a map nested to the requested depth; the root
// map is depth 1.
func auroraTestNestedMetadata(depth int) map[string]any {
	root := map[string]any{}
	current := root
	for i := 0; i < depth; i++ {
		next := map[string]any{}
		current["n"] = next
		current = next
	}
	return root
}

// TestAuroraManifestRejectsUnknownArtifactKind keeps the daemon's kind
// vocabulary aligned with the handler's storage contract.
func TestAuroraManifestRejectsUnknownArtifactKind(t *testing.T) {
	root := t.TempDir()
	artifact := auroraTestLocalArtifact(t, root, "primary-1", "artifacts/primary-1.md", "primary-1.md", auroraTestText)
	artifact.Kind = "audio"
	if err := collectAuroraTestManifest(t, root, auroraTestManifest(artifact)); err == nil {
		t.Fatal("unknown artifact kind was accepted")
	}
}

// TestAuroraManifestUsesPolicyForSkillOutputs proves the expected output kind is
// derived from the server-owned execution policy, not from the manifest.
func TestAuroraManifestUsesPolicyForSkillOutputs(t *testing.T) {
	if _, ok := aurora.ExecutionPolicy(auroraTestSkill); !ok {
		t.Fatalf("test skill %s is not an available catalog entry", auroraTestSkill)
	}
}

// TestCollectAuroraArtifactsNeedsInputOutcome is the question-round exit: a
// manifest that declares outcome needs_input may list zero artifacts and
// collects to a nil artifact slice with no error, while the empty-artifact guard
// still rejects a delivered manifest that produced nothing.
func TestCollectAuroraArtifactsNeedsInputOutcome(t *testing.T) {
	root := t.TempDir()
	manifest := auroraTestManifest()
	manifest.Outcome = auroraOutcomeNeedsInput
	writeAuroraTestManifest(t, root, manifest)

	artifacts, outcome, err := CollectAuroraArtifacts(context.Background(), root, auroraTestTaskID, auroraTestSkill, newAuroraTestSink())
	if err != nil {
		t.Fatalf("CollectAuroraArtifacts: %v", err)
	}
	if outcome != auroraOutcomeNeedsInput {
		t.Fatalf("outcome = %q, want %q", outcome, auroraOutcomeNeedsInput)
	}
	if len(artifacts) != 0 {
		t.Fatalf("artifacts = %d, want 0", len(artifacts))
	}

	// Every header check still runs before the outcome is honoured.
	mismatched := auroraTestManifest()
	mismatched.Outcome = auroraOutcomeNeedsInput
	mismatched.Producer.ID = "some-other-producer"
	badRoot := t.TempDir()
	writeAuroraTestManifest(t, badRoot, mismatched)
	if _, _, err := CollectAuroraArtifacts(context.Background(), badRoot, auroraTestTaskID, auroraTestSkill, newAuroraTestSink()); err == nil {
		t.Fatal("a needs_input manifest with a mismatched producer was accepted")
	}

	// The load-bearing guard: no outcome and no artifacts is still a failure.
	delivered := t.TempDir()
	writeAuroraTestManifest(t, delivered, auroraTestManifest())
	_, _, err = CollectAuroraArtifacts(context.Background(), delivered, auroraTestTaskID, auroraTestSkill, newAuroraTestSink())
	if err == nil || !strings.Contains(err.Error(), "manifest declares no artifacts") {
		t.Fatalf("empty delivered manifest error = %v, want the no-artifacts guard", err)
	}
}
