package daemon

// Safe collection of the sandbox's v1 artifact manifest (Plan C Task 7).
//
// The broker writes a bounded manifest at
// <outputRoot>/.multica/aurora-artifacts.v1.json identifying the files and
// server-staged objects a run produced. The daemon treats that manifest only as
// a list of expected paths and metadata: it re-derives size, SHA-256 and MIME
// from the opened descriptor, refuses anything that is not a single-link regular
// file inside the opened root, and uploads each local file through Task 6's
// staging endpoint. A manifest is never proof that its bytes are what it says.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/multica-ai/multica/server/internal/aurora"
)

// Manifest and artifact limits from the plan's Artifact Manifest Contract.
const (
	auroraManifestSchema        = "com.multica.aurora.artifacts"
	auroraManifestVersion       = 1
	auroraManifestRelativePath  = ".multica/aurora-artifacts.v1.json"
	auroraManifestMaxBytes      = 1 << 20
	auroraManifestMaxArtifacts  = 20
	auroraManifestMaxTotalBytes = 600 << 20

	auroraManifestMaxMetadataBytes = 16 << 10
	auroraManifestMaxMetadataDepth = 8

	// auroraOutcomeNeedsInput is the manifest's declarative "this round stopped
	// to ask a question" ending. A manifest carrying it may list no artifacts.
	auroraOutcomeNeedsInput = "needs_input"

	auroraArtifactMaxImageBytes = 25 << 20
	auroraArtifactMaxTextBytes  = 25 << 20
	auroraArtifactMaxPDFBytes   = 25 << 20
	auroraArtifactMaxVideoBytes = 500 << 20

	auroraArtifactSniffBytes = 512

	// auroraSandboxRoot is the fixed mount a managed sandbox presents to the
	// agent. Its
	// presence is what tells the daemon it is running inside the sandbox and
	// therefore has an artifact tree to validate.
	auroraSandboxRoot = "/workspace"
	// auroraSandboxOutputRoot is the fixed mount the agent writes outputs
	// under.
	auroraSandboxOutputRoot = "/workspace/output"
)

// Sentinel failures so a caller (or a test) can tell which invariant rejected a
// manifest without parsing a message.
var (
	errAuroraManifestMissing       = errors.New("aurora: artifact manifest is missing")
	errAuroraManifestTooLarge      = errors.New("aurora: artifact manifest exceeds the 1 MiB limit")
	errAuroraManifestInvalid       = errors.New("aurora: artifact manifest is invalid")
	errAuroraManifestUnsafePath    = errors.New("aurora: artifact path is unsafe")
	errAuroraManifestUnsafeFile    = errors.New("aurora: artifact file is unsafe")
	errAuroraManifestMismatch      = errors.New("aurora: artifact metadata does not match the file")
	errAuroraManifestNoPrimary     = errors.New("aurora: manifest has no matching primary artifact")
	errAuroraManifestTooMany       = errors.New("aurora: manifest exceeds the artifact count limit")
	errAuroraManifestTotalTooLarge = errors.New("aurora: manifest exceeds the total size limit")

	errAuroraArtifactFileTooLarge = errors.New("aurora: artifact file exceeds its kind cap")
)

// auroraManifest is the wire shape of the v1 manifest.
type auroraManifest struct {
	Schema      string                   `json:"schema"`
	Version     int                      `json:"version"`
	TaskID      string                   `json:"task_id"`
	SkillID     string                   `json:"skill_id"`
	Outcome     string                   `json:"outcome"`
	Producer    auroraManifestProducer   `json:"producer"`
	ProviderRun json.RawMessage          `json:"provider_run"`
	Artifacts   []auroraManifestArtifact `json:"artifacts"`
}

type auroraManifestProducer struct {
	ID         string  `json:"id"`
	Version    string  `json:"version"`
	TreeSHA256 *string `json:"tree_sha256"`
}

type auroraManifestArtifact struct {
	ID        string               `json:"id"`
	Source    auroraManifestSource `json:"source"`
	Name      string               `json:"name"`
	Kind      string               `json:"kind"`
	Role      string               `json:"role"`
	Format    string               `json:"format"`
	MIMEType  string               `json:"mime_type"`
	SizeBytes int64                `json:"size_bytes"`
	SHA256    string               `json:"sha256"`
	Metadata  map[string]any       `json:"metadata"`
}

type auroraManifestSource struct {
	Type         string `json:"type"`
	RelativePath string `json:"relative_path"`
	StagingID    string `json:"staging_id"`
}

// AuroraArtifactUpload is one validated local artifact handed to the staging
// sink. Content is the opened descriptor, positioned at the start.
type AuroraArtifactUpload struct {
	ManifestArtifactID string
	Name               string
	Kind               string
	Role               string
	Format             string
	MIMEType           string
	SizeBytes          int64
	SHA256             string
	Metadata           map[string]any
	Content            io.Reader
}

// AuroraArtifactSink uploads one validated local artifact and returns the
// staging id the server assigned it.
type AuroraArtifactSink interface {
	UploadAuroraArtifact(ctx context.Context, upload AuroraArtifactUpload) (string, error)
}

var (
	auroraArtifactIDPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	auroraArtifactNamePattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	auroraArtifactSHA256Pattern = regexp.MustCompile(`^(?:sha256:)?([0-9a-f]{64})$`)
	auroraUUIDPattern           = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// auroraArtifactFormatSpec is the format -> kind + accepted MIME contract. It
// mirrors the handler's storage contract so a manifest the daemon accepts is one
// the upload endpoint can also stage.
type auroraArtifactFormatSpec struct {
	Kind  string
	MIMEs []string
}

var auroraArtifactFormats = map[string]auroraArtifactFormatSpec{
	"png":      {"image", []string{"image/png"}},
	"jpg":      {"image", []string{"image/jpeg"}},
	"jpeg":     {"image", []string{"image/jpeg"}},
	"webp":     {"image", []string{"image/webp"}},
	"gif":      {"image", []string{"image/gif"}},
	"mp4":      {"video", []string{"video/mp4"}},
	"mov":      {"video", []string{"video/quicktime"}},
	"webm":     {"video", []string{"video/webm"}},
	"txt":      {"text", []string{"text/plain"}},
	"md":       {"text", []string{"text/markdown", "text/x-markdown", "text/plain"}},
	"markdown": {"text", []string{"text/markdown", "text/x-markdown", "text/plain"}},
	"pdf":      {"pdf", []string{"application/pdf"}},
}

var auroraArtifactRoles = map[string]bool{"primary": true, "supporting": true, "transcript": true}

// auroraManifestProducersByRoute is the fixed producer identity each execution
// route's manifest must carry (see the runtime's producerForSkill).
var auroraManifestProducersByRoute = map[string]string{
	"volcengine-seedream":        "byted-ark-seedream-skill",
	"volcengine-seedance":        "byted-ark-seedance-skill",
	"volcengine-asr":             "volcengine-asr",
	"volcengine-asr-hyperframes": "volcengine-asr",
	"local-id-photo":             "multica-aurora-runtime",
	"claude-text":                "multica-aurora-runtime",
	"claude-html-pdf":            "multica-aurora-runtime",
}

// CollectAuroraArtifacts validates the v1 manifest under outputRoot against the
// task and skill, then returns the reportable artifacts and the run's
// declarative outcome: local files are streamed through sink, staged provider
// objects pass their staging id through.
//
// A manifest whose outcome is "needs_input" is the round's third ending: it
// stops to ask a question instead of delivering, so it may list no artifacts.
// Every header check (schema/version/task_id/skill_id/producer) still runs; only
// the empty-artifact guard is skipped. Every other manifest is validated
// unchanged, so a delivered run with no artifacts still fails.
func CollectAuroraArtifacts(ctx context.Context, outputRoot, taskID, skillID string, sink AuroraArtifactSink) ([]TaskArtifact, string, error) {
	if sink == nil {
		return nil, "", fmt.Errorf("%w: no artifact upload sink", errAuroraManifestInvalid)
	}
	policy, ok := aurora.ExecutionPolicy(skillID)
	if !ok {
		return nil, "", fmt.Errorf("%w: skill %q is not executable", errAuroraManifestInvalid, skillID)
	}
	producerID, ok := auroraManifestProducersByRoute[policy.Route]
	if !ok {
		return nil, "", fmt.Errorf("%w: skill %q has no producer contract", errAuroraManifestInvalid, skillID)
	}

	root, err := os.OpenRoot(outputRoot)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", errAuroraManifestMissing, err)
	}
	defer root.Close()

	manifest, err := readAuroraManifest(root)
	if err != nil {
		return nil, "", err
	}
	if err := validateAuroraManifestHeader(manifest, taskID, skillID, producerID); err != nil {
		return nil, "", err
	}

	outcome := strings.TrimSpace(manifest.Outcome)
	if outcome == auroraOutcomeNeedsInput {
		// The question round declares its ending; provider_run may be null and
		// the artifact list empty. Nothing is uploaded and no file is billed.
		return nil, outcome, nil
	}
	if err := validateAuroraManifestArtifacts(manifest, policy); err != nil {
		return nil, "", err
	}

	out := make([]TaskArtifact, 0, len(manifest.Artifacts))
	for i := range manifest.Artifacts {
		artifact := manifest.Artifacts[i]
		switch artifact.Source.Type {
		case "file":
			collected, err := collectAuroraLocalArtifact(ctx, root, artifact, sink)
			if err != nil {
				return nil, "", err
			}
			out = append(out, collected)
		case "staged_object":
			out = append(out, collectAuroraStagedArtifact(artifact))
		default:
			return nil, "", fmt.Errorf("%w: unknown source type %q", errAuroraManifestInvalid, artifact.Source.Type)
		}
	}
	if len(out) == 0 {
		return nil, "", fmt.Errorf("%w: manifest declares no artifacts", errAuroraManifestInvalid)
	}
	return out, outcome, nil
}

// readAuroraManifest opens the manifest inside the already-opened root and reads
// at most 1 MiB; a larger file is refused before it is parsed.
func readAuroraManifest(root *os.Root) (auroraManifest, error) {
	info, err := root.Lstat(auroraManifestRelativePath)
	if err != nil {
		return auroraManifest{}, fmt.Errorf("%w: %v", errAuroraManifestMissing, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return auroraManifest{}, fmt.Errorf("%w: manifest must be a single regular file", errAuroraManifestUnsafeFile)
	}
	if nlink, ok := auroraFileNlink(info); ok && nlink != 1 {
		return auroraManifest{}, fmt.Errorf("%w: manifest must not be hard linked", errAuroraManifestUnsafeFile)
	}
	if info.Size() > auroraManifestMaxBytes {
		return auroraManifest{}, errAuroraManifestTooLarge
	}
	f, err := root.Open(auroraManifestRelativePath)
	if err != nil {
		return auroraManifest{}, fmt.Errorf("%w: %v", errAuroraManifestMissing, err)
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, auroraManifestMaxBytes+1))
	if err != nil {
		return auroraManifest{}, fmt.Errorf("%w: %v", errAuroraManifestInvalid, err)
	}
	if len(data) > auroraManifestMaxBytes {
		return auroraManifest{}, errAuroraManifestTooLarge
	}
	var manifest auroraManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return auroraManifest{}, fmt.Errorf("%w: %v", errAuroraManifestInvalid, err)
	}
	return manifest, nil
}

func validateAuroraManifestHeader(manifest auroraManifest, taskID, skillID, producerID string) error {
	switch {
	case manifest.Schema != auroraManifestSchema:
		return fmt.Errorf("%w: unexpected schema %q", errAuroraManifestInvalid, manifest.Schema)
	case manifest.Version != auroraManifestVersion:
		return fmt.Errorf("%w: unsupported version %d", errAuroraManifestInvalid, manifest.Version)
	case manifest.TaskID != taskID:
		return fmt.Errorf("%w: manifest task %q does not match %q", errAuroraManifestInvalid, manifest.TaskID, taskID)
	case manifest.SkillID != skillID:
		return fmt.Errorf("%w: manifest skill %q does not match %q", errAuroraManifestInvalid, manifest.SkillID, skillID)
	case manifest.Producer.ID != producerID:
		return fmt.Errorf("%w: producer %q does not match %q", errAuroraManifestInvalid, manifest.Producer.ID, producerID)
	case strings.TrimSpace(manifest.Producer.Version) == "":
		return fmt.Errorf("%w: producer version is required", errAuroraManifestInvalid)
	}
	return nil
}

// validateAuroraManifestArtifacts validates the whole manifest shape before any
// file is opened: identity uniqueness, paths, kind/format/MIME agreement, size
// caps, metadata bounds, and the mandatory primary output.
func validateAuroraManifestArtifacts(manifest auroraManifest, policy aurora.SkillExecutionPolicy) error {
	if len(manifest.Artifacts) == 0 {
		return fmt.Errorf("%w: manifest declares no artifacts", errAuroraManifestInvalid)
	}
	if len(manifest.Artifacts) > auroraManifestMaxArtifacts {
		return errAuroraManifestTooMany
	}

	ids := make(map[string]bool, len(manifest.Artifacts))
	names := make(map[string]bool, len(manifest.Artifacts))
	paths := make(map[string]bool, len(manifest.Artifacts))
	var total int64
	hasPrimary := false

	for i := range manifest.Artifacts {
		artifact := &manifest.Artifacts[i]
		if !auroraArtifactIDPattern.MatchString(artifact.ID) || ids[artifact.ID] {
			return fmt.Errorf("%w: artifact id %q is invalid or duplicated", errAuroraManifestInvalid, artifact.ID)
		}
		ids[artifact.ID] = true

		if !auroraArtifactNamePattern.MatchString(artifact.Name) || strings.Contains(artifact.Name, "..") || names[artifact.Name] {
			return fmt.Errorf("%w: artifact name %q is invalid or duplicated", errAuroraManifestInvalid, artifact.Name)
		}
		names[artifact.Name] = true

		if !auroraArtifactRoles[artifact.Role] {
			return fmt.Errorf("%w: artifact %q has an invalid role %q", errAuroraManifestInvalid, artifact.ID, artifact.Role)
		}
		spec, ok := auroraArtifactFormats[artifact.Format]
		if !ok {
			return fmt.Errorf("%w: artifact %q has an unsupported format %q", errAuroraManifestInvalid, artifact.ID, artifact.Format)
		}
		if artifact.Kind != spec.Kind {
			return fmt.Errorf("%w: artifact %q format %q does not match kind %q", errAuroraManifestInvalid, artifact.ID, artifact.Format, artifact.Kind)
		}
		if !auroraArtifactExtensionMatches(artifact.Name, artifact.Kind) {
			return fmt.Errorf("%w: artifact %q extension does not match kind %q", errAuroraManifestInvalid, artifact.ID, artifact.Kind)
		}
		if !slices.Contains(spec.MIMEs, normalizeAuroraMIME(artifact.MIMEType)) {
			return fmt.Errorf("%w: artifact %q mime %q is not accepted for format %q", errAuroraManifestInvalid, artifact.ID, artifact.MIMEType, artifact.Format)
		}
		if artifact.SizeBytes < 0 || artifact.SizeBytes > auroraArtifactKindMaxBytes(artifact.Kind) {
			return fmt.Errorf("%w: artifact %q exceeds the %s size cap", errAuroraManifestInvalid, artifact.ID, artifact.Kind)
		}
		if !auroraArtifactSHA256Pattern.MatchString(strings.ToLower(strings.TrimSpace(artifact.SHA256))) {
			return fmt.Errorf("%w: artifact %q has an invalid sha256", errAuroraManifestInvalid, artifact.ID)
		}
		if err := validateAuroraMetadata(artifact.Metadata); err != nil {
			return fmt.Errorf("%w: artifact %q: %v", errAuroraManifestInvalid, artifact.ID, err)
		}

		switch artifact.Source.Type {
		case "file":
			if err := validateAuroraRelativePath(artifact.Source.RelativePath); err != nil {
				return err
			}
			if paths[artifact.Source.RelativePath] {
				return fmt.Errorf("%w: duplicate artifact path %q", errAuroraManifestInvalid, artifact.Source.RelativePath)
			}
			paths[artifact.Source.RelativePath] = true
		case "staged_object":
			if !auroraUUIDPattern.MatchString(artifact.Source.StagingID) {
				return fmt.Errorf("%w: artifact %q staging id is invalid", errAuroraManifestInvalid, artifact.ID)
			}
		default:
			return fmt.Errorf("%w: artifact %q has an unknown source type %q", errAuroraManifestInvalid, artifact.ID, artifact.Source.Type)
		}

		if artifact.Role == "primary" && slices.Contains(policy.OutputKinds, artifact.Kind) {
			hasPrimary = true
		}
		total += artifact.SizeBytes
	}

	if total > auroraManifestMaxTotalBytes {
		return errAuroraManifestTotalTooLarge
	}
	if !hasPrimary {
		return errAuroraManifestNoPrimary
	}
	return nil
}

// collectAuroraLocalArtifact re-derives the file's facts from the opened
// descriptor and uploads it. The manifest's size and hash are only compared
// against, never trusted as the answer.
func collectAuroraLocalArtifact(ctx context.Context, root *os.Root, artifact auroraManifestArtifact, sink AuroraArtifactSink) (TaskArtifact, error) {
	relative := artifact.Source.RelativePath
	info, err := root.Lstat(relative)
	if err != nil {
		return TaskArtifact{}, fmt.Errorf("%w: %v", errAuroraManifestMissing, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return TaskArtifact{}, fmt.Errorf("%w: %q is not a regular file", errAuroraManifestUnsafeFile, relative)
	}

	f, err := root.OpenFile(relative, os.O_RDONLY, 0)
	if err != nil {
		return TaskArtifact{}, fmt.Errorf("%w: %v", errAuroraManifestUnsafeFile, err)
	}
	defer f.Close()

	fdInfo, err := f.Stat()
	if err != nil {
		return TaskArtifact{}, fmt.Errorf("%w: %v", errAuroraManifestUnsafeFile, err)
	}
	if !fdInfo.Mode().IsRegular() {
		return TaskArtifact{}, fmt.Errorf("%w: %q is not a regular file", errAuroraManifestUnsafeFile, relative)
	}
	if nlink, ok := auroraFileNlink(fdInfo); ok && nlink != 1 {
		return TaskArtifact{}, fmt.Errorf("%w: %q has %d hard links", errAuroraManifestUnsafeFile, relative, nlink)
	}

	sniff := make([]byte, auroraArtifactSniffBytes)
	n, err := io.ReadFull(f, sniff)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return TaskArtifact{}, fmt.Errorf("%w: %v", errAuroraManifestInvalid, err)
	}
	if !auroraSniffedKindMatches(artifact.Kind, sniff[:n]) {
		return TaskArtifact{}, fmt.Errorf("%w: %q content does not match kind %q", errAuroraManifestMismatch, relative, artifact.Kind)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return TaskArtifact{}, fmt.Errorf("%w: %v", errAuroraManifestInvalid, err)
	}

	hasher := sha256.New()
	limit := auroraArtifactKindMaxBytes(artifact.Kind)
	size, err := io.Copy(hasher, &auroraCappedReader{source: f, remaining: limit})
	if err != nil {
		if errors.Is(err, errAuroraArtifactFileTooLarge) {
			return TaskArtifact{}, fmt.Errorf("%w: %q exceeds the %s cap", errAuroraManifestMismatch, relative, artifact.Kind)
		}
		return TaskArtifact{}, fmt.Errorf("%w: %v", errAuroraManifestInvalid, err)
	}
	digest := "sha256:" + hex.EncodeToString(hasher.Sum(nil))
	if size != artifact.SizeBytes {
		return TaskArtifact{}, fmt.Errorf("%w: %q size %d does not match declared %d", errAuroraManifestMismatch, relative, size, artifact.SizeBytes)
	}
	if want := normalizeAuroraSHA256(artifact.SHA256); digest != want {
		return TaskArtifact{}, fmt.Errorf("%w: %q sha256 does not match declared", errAuroraManifestMismatch, relative)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return TaskArtifact{}, fmt.Errorf("%w: %v", errAuroraManifestInvalid, err)
	}

	stagingID, err := sink.UploadAuroraArtifact(ctx, AuroraArtifactUpload{
		ManifestArtifactID: artifact.ID,
		Name:               artifact.Name,
		Kind:               artifact.Kind,
		Role:               artifact.Role,
		Format:             artifact.Format,
		MIMEType:           normalizeAuroraMIME(artifact.MIMEType),
		SizeBytes:          size,
		SHA256:             digest,
		Metadata:           artifact.Metadata,
		Content:            f,
	})
	if err != nil {
		return TaskArtifact{}, fmt.Errorf("%w: upload %q: %v", errAuroraManifestInvalid, artifact.Name, err)
	}
	if !auroraUUIDPattern.MatchString(stagingID) {
		return TaskArtifact{}, fmt.Errorf("%w: upload of %q returned an invalid staging id", errAuroraManifestInvalid, artifact.Name)
	}

	return TaskArtifact{
		ManifestArtifactID: artifact.ID,
		StagingID:          stagingID,
		Name:               artifact.Name,
		Kind:               artifact.Kind,
		Role:               artifact.Role,
		Format:             artifact.Format,
		MIMEType:           normalizeAuroraMIME(artifact.MIMEType),
		SizeBytes:          size,
		SHA256:             digest,
		Metadata:           artifact.Metadata,
	}, nil
}

func collectAuroraStagedArtifact(artifact auroraManifestArtifact) TaskArtifact {
	return TaskArtifact{
		ManifestArtifactID: artifact.ID,
		StagingID:          artifact.Source.StagingID,
		Name:               artifact.Name,
		Kind:               artifact.Kind,
		Role:               artifact.Role,
		Format:             artifact.Format,
		MIMEType:           normalizeAuroraMIME(artifact.MIMEType),
		SizeBytes:          artifact.SizeBytes,
		SHA256:             normalizeAuroraSHA256(artifact.SHA256),
		Metadata:           artifact.Metadata,
	}
}

// auroraArtifactExtensionMatches proves an artifact's declared name carries the
// extension its kind implies, so a manifest cannot label a video as a PNG.
func auroraArtifactExtensionMatches(name, kind string) bool {
	dot := strings.LastIndexByte(name, '.')
	if dot <= 0 || dot == len(name)-1 {
		return false
	}
	spec, ok := auroraArtifactFormats[strings.ToLower(name[dot+1:])]
	return ok && spec.Kind == kind
}

func validateAuroraRelativePath(relative string) error {
	if relative == "" || strings.ContainsRune(relative, 0) || strings.ContainsAny(relative, ":\\") || path.IsAbs(relative) {
		return fmt.Errorf("%w: %q", errAuroraManifestUnsafePath, relative)
	}
	cleaned := path.Clean(relative)
	if cleaned != relative || cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf("%w: %q", errAuroraManifestUnsafePath, relative)
	}
	return nil
}

func validateAuroraMetadata(metadata map[string]any) error {
	if len(metadata) == 0 {
		return nil
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return errors.New("metadata is not encodable")
	}
	if len(encoded) > auroraManifestMaxMetadataBytes {
		return errors.New("metadata exceeds the size cap")
	}
	if auroraMetadataDepth(metadata) > auroraManifestMaxMetadataDepth {
		return errors.New("metadata exceeds the depth cap")
	}
	return nil
}

func auroraMetadataDepth(value any) int {
	switch typed := value.(type) {
	case map[string]any:
		depth := 1
		for _, child := range typed {
			if d := 1 + auroraMetadataDepth(child); d > depth {
				depth = d
			}
		}
		return depth
	case []any:
		depth := 1
		for _, child := range typed {
			if d := 1 + auroraMetadataDepth(child); d > depth {
				depth = d
			}
		}
		return depth
	default:
		return 0
	}
}

func auroraSniffedKindMatches(kind string, head []byte) bool {
	sniffed, ok := auroraKindForSniffedMIME(http.DetectContentType(head))
	return ok && sniffed == kind
}

func auroraKindForSniffedMIME(contentType string) (string, bool) {
	switch contentType = normalizeAuroraMIME(contentType); {
	case strings.HasPrefix(contentType, "image/"):
		return "image", true
	case strings.HasPrefix(contentType, "video/"):
		return "video", true
	case contentType == "application/pdf":
		return "pdf", true
	case strings.HasPrefix(contentType, "text/"):
		return "text", true
	default:
		return "", false
	}
}

func auroraArtifactKindMaxBytes(kind string) int64 {
	switch kind {
	case "image":
		return auroraArtifactMaxImageBytes
	case "text":
		return auroraArtifactMaxTextBytes
	case "pdf":
		return auroraArtifactMaxPDFBytes
	case "video":
		return auroraArtifactMaxVideoBytes
	default:
		return 0
	}
}

func normalizeAuroraMIME(raw string) string {
	if i := strings.IndexByte(raw, ';'); i >= 0 {
		raw = raw[:i]
	}
	return strings.ToLower(strings.TrimSpace(raw))
}

func normalizeAuroraSHA256(raw string) string {
	match := auroraArtifactSHA256Pattern.FindStringSubmatch(strings.ToLower(strings.TrimSpace(raw)))
	if match == nil {
		return strings.ToLower(strings.TrimSpace(raw))
	}
	return "sha256:" + match[1]
}

// auroraCappedReader refuses to read past a per-kind byte cap so an oversized
// file cannot be streamed into memory or storage.
type auroraCappedReader struct {
	source    io.Reader
	remaining int64
}

func (r *auroraCappedReader) Read(p []byte) (int, error) {
	if r.remaining < 0 {
		r.remaining = 0
	}
	if r.remaining == 0 {
		var probe [1]byte
		n, err := r.source.Read(probe[:])
		if n > 0 {
			return 0, errAuroraArtifactFileTooLarge
		}
		return 0, err
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.source.Read(p)
	r.remaining -= int64(n)
	return n, err
}

// auroraFileNlink reads the link count from the platform's stat structure. It
// reports false on platforms (Windows) whose stat has no such field, where the
// single-link check is handled by the regular-file and containment checks.
func auroraFileNlink(info os.FileInfo) (uint64, bool) {
	value := reflect.ValueOf(info.Sys())
	if !value.IsValid() {
		return 0, false
	}
	if value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return 0, false
	}
	field := value.FieldByName("Nlink")
	if !field.IsValid() {
		return 0, false
	}
	switch field.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return field.Uint(), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return uint64(field.Int()), true
	default:
		return 0, false
	}
}

// auroraManifestClientSink binds the daemon client to one task and its
// task-scoped token so the collector can stream local files through Task 6's
// staging endpoint.
type auroraManifestClientSink struct {
	client    *Client
	taskToken string
	taskID    string
}

func (s auroraManifestClientSink) UploadAuroraArtifact(ctx context.Context, upload AuroraArtifactUpload) (string, error) {
	return s.client.UploadAuroraArtifact(ctx, s.taskToken, s.taskID, upload)
}

// cleanAuroraSandboxIO removes the fixed input/output mounts after a terminal
// report so one task's files never bleed into the next.
func cleanAuroraSandboxIO() {
	for _, dir := range []string{"/workspace/input", auroraSandboxOutputRoot} {
		_ = os.RemoveAll(dir)
	}
}
