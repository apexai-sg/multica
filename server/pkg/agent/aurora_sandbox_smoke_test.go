//go:build agentintegration

// aurora_sandbox_smoke_test.go implements the explicitly gated real agent and
// provider smokes for the Aurora managed sandbox (Plan D Task 6, issue #117).
//
// Every smoke is opt-in at two levels:
//
//  1. the repository-wide real-agent gate, MULTICA_RUN_REAL_AGENT_SMOKE=1,
//     checked as the first statement of TestAuroraSandboxRealProviderSmoke
//     before any executable lookup, Docker image pull, credential-file read,
//     account access, or network call; and
//  2. one provider-specific AURORA_RUN_*_SMOKE=1 variable per subtest, checked
//     inside that subtest before its credential file is read.
//
// A missing opt-in skips its subtest, and there is no "run every provider"
// implicit default. The file carries the `agentintegration` build tag, so the
// default test binaries never compile it.
//
// Each enabled subtest drives a live Multica stack through the public Aurora
// HTTP API only: it creates a fresh workspace/generation, waits conditionally
// for a terminal status, verifies the expected artifact kind/format/hash and
// nonzero size, verifies the exact credit settlement from the ledger, and then
// deletes its objects. It never executes a vendor script or a provider CLI on
// the host, and it uses the digest-pinned sandbox image named by
// AURORA_RUNTIME_IMAGE (rejected unless it is an @sha256 reference).
//
// Output is deliberately sanitized and bounded. Tests log only the test name,
// provider and model ID, Multica generation/asset IDs, duration, status, byte
// count, and cost/credit delta. They never log prompts, user document text,
// keys, task tokens, signed URLs, full provider responses, or artifact bytes.
// An outer 35-minute deadline bounds every subtest and each operation issues at
// most one provider create request.

package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

const (
	// auroraSmokeGlobalOptIn is the repository-wide real-agent gate. It is
	// checked before any other work in the top-level test.
	auroraSmokeGlobalOptIn = "MULTICA_RUN_REAL_AGENT_SMOKE"

	// auroraSmokeOuterDeadline bounds one provider subtest end to end.
	auroraSmokeOuterDeadline = 35 * time.Minute
	// auroraSmokeTerminalWait bounds how long one generation may run before the
	// test gives up, leaving headroom inside the outer deadline for cleanup.
	auroraSmokeTerminalWait = 30 * time.Minute
	// auroraSmokePollInterval is the conditional wait between status reads.
	auroraSmokePollInterval = 10 * time.Second

	// auroraSmokeMaxResponseBytes caps a decoded JSON response so a hostile or
	// broken server cannot exhaust the test process.
	auroraSmokeMaxResponseBytes = 4 << 20
	// auroraSmokeMaxArtifactBytes caps a downloaded artifact. The sandbox
	// artifact contract's absolute cap is 600 MiB total; 256 MiB is enough for
	// any single smoke output and keeps a runaway response bounded.
	auroraSmokeMaxArtifactBytes = 256 << 20

	// auroraSmokeMicroPerCredit is the ledger unit: 1 credit = 1e6 micro.
	auroraSmokeMicroPerCredit = 1_000_000

	// Fixture paths are relative to the repository root.
	auroraSmokeWAVFixture      = "server/pkg/agent/testdata/aurora/audio/short.wav"
	auroraSmokeImageFixture    = "server/pkg/agent/testdata/aurora/image/reference.png"
	auroraSmokeVideoFixture    = "server/pkg/agent/testdata/aurora/video/short.mp4"
	auroraSmokeDocumentFixture = "server/pkg/agent/testdata/aurora/document/resume.md"
)

var (
	auroraSmokeImageDigestRE = regexp.MustCompile("^[A-Za-z0-9][A-Za-z0-9._/:-]*@sha256:[0-9a-f]{64}$")
	auroraSmokeSHA256RE      = regexp.MustCompile("^[0-9a-f]{64}$")
)

// TestAuroraSandboxRealProviderSmoke is the one entry point for the real
// provider smokes. The global gate is deliberately the first statement: with
// MULTICA_RUN_REAL_AGENT_SMOKE unset nothing below it is reached, so the
// default tagged run compiles this file but performs no lookup or access.
func TestAuroraSandboxRealProviderSmoke(t *testing.T) {
	if os.Getenv(auroraSmokeGlobalOptIn) != "1" {
		t.Skip("set MULTICA_RUN_REAL_AGENT_SMOKE=1 to allow real provider and agent account access")
	}
	// Reaching this point is itself an explicit authorization. Log the fact so
	// a CI log cannot imply an accidental provider call.
	t.Log("REAL PROVIDER SMOKE: explicit opt-in present; subtests still require their own AURORA_RUN_*_SMOKE=1")
	for _, provider := range auroraSmokeProviders() {
		provider := provider
		t.Run(provider.name, func(t *testing.T) {
			runAuroraSandboxProviderSmoke(t, provider)
		})
	}
}

// auroraSmokeOperation is one provider create in one skill route.
type auroraSmokeOperation struct {
	skillID     string
	kind        string
	formats     []string
	attachments []string
	// prompt is a short synthetic instruction. It is never logged: the
	// sanitized output set excludes prompts.
	prompt string
}

// auroraSmokeProvider is one gated subtest. secretEnv names the environment
// variable holding the mode-0400 credential file for the route; it is empty
// only for a route that requires no provider credential.
type auroraSmokeProvider struct {
	name       string
	optIn      string
	secretEnv  string
	provider   string
	model      string
	operations []auroraSmokeOperation
}

// auroraSmokeProviders is the fixed subtest table. Adding a provider here
// without its own opt-in would make it run under another provider's consent,
// so each entry carries a distinct variable.
func auroraSmokeProviders() []auroraSmokeProvider {
	return []auroraSmokeProvider{
		{
			name:      "claude-text",
			optIn:     "AURORA_RUN_CLAUDE_SMOKE",
			secretEnv: "AURORA_SMOKE_ANTHROPIC_KEY_FILE",
			provider:  "anthropic",
			model:     "claude (CLI default)",
			operations: []auroraSmokeOperation{{
				skillID: "xhs-copy",
				kind:    "text",
				formats: []string{"md", "markdown", "txt"},
				prompt:  "Write one short Xiaohongshu post about a reusable water bottle.",
			}},
		},
		{
			name:      "seedream-image",
			optIn:     "AURORA_RUN_SEEDREAM_SMOKE",
			secretEnv: "AURORA_SMOKE_ARK_KEY_FILE",
			provider:  "volcengine-ark",
			model:     "doubao-seedream-5-0-pro-260628",
			operations: []auroraSmokeOperation{
				{
					skillID: "xhs-image",
					kind:    "image",
					formats: []string{"png", "jpg", "jpeg", "webp"},
					prompt:  "One low-count 2K image of a single reusable water bottle on a plain background.",
				},
				{
					skillID: "product-image",
					kind:    "image",
					formats: []string{"png", "jpg", "jpeg", "webp"},
					prompt:  "One low-count image of a reusable water bottle on a plain background.",
				},
				{
					skillID:     "image-edit",
					kind:        "image",
					formats:     []string{"png", "jpg", "jpeg", "webp"},
					attachments: []string{auroraSmokeImageFixture},
					prompt:      "Recolor the object in the reference image to a single flat color.",
				},
			},
		},
		{
			name:      "seedance-video",
			optIn:     "AURORA_RUN_SEEDANCE_SMOKE",
			secretEnv: "AURORA_SMOKE_ARK_KEY_FILE",
			provider:  "volcengine-ark",
			model:     "doubao-seedance-2-5-260628",
			// The Seedance tree is vendored and merged, so this route is live
			// rather than fail-closed. The broker's fixed policy selects the
			// shortest supported low-resolution output; the test cannot and
			// must not choose provider arguments.
			operations: []auroraSmokeOperation{{
				skillID: "text-video",
				kind:    "video",
				formats: []string{"mp4", "mov", "webm"},
				prompt:  "A single short clip of a reusable water bottle rotating on a plain background.",
			}},
		},
		{
			name:      "volc-asr",
			optIn:     "AURORA_RUN_VOLC_ASR_SMOKE",
			secretEnv: "AURORA_SMOKE_VOLC_ASR_KEY_FILE",
			provider:  "volcengine-asr",
			model:     "bigmodel",
			operations: []auroraSmokeOperation{{
				skillID:     "transcription",
				kind:        "text",
				formats:     []string{"txt", "md"},
				attachments: []string{auroraSmokeWAVFixture},
				prompt:      "Transcribe this short audio clip.",
			}},
		},
		{
			name:      "hyperframes-captions",
			optIn:     "AURORA_RUN_HYPERFRAMES_SMOKE",
			secretEnv: "AURORA_SMOKE_VOLC_ASR_KEY_FILE",
			provider:  "hyperframes-ffmpeg",
			model:     "hyperframes-0.8.75",
			// video-captions runs the fixed ASR + HyperFrames/FFmpeg render.
			// Chromium and FFmpeg are image-resident; the host runs neither.
			operations: []auroraSmokeOperation{{
				skillID:     "video-captions",
				kind:        "video",
				formats:     []string{"mp4", "mov", "webm"},
				attachments: []string{auroraSmokeVideoFixture},
				prompt:      "Render the fixed caption track over this short clip.",
			}},
		},
		{
			name:      "chromium-resume",
			optIn:     "AURORA_RUN_CHROMIUM_SMOKE",
			secretEnv: "AURORA_SMOKE_ANTHROPIC_KEY_FILE",
			provider:  "claude-html-pdf",
			model:     "claude (CLI default)",
			operations: []auroraSmokeOperation{{
				skillID:     "resume",
				kind:        "pdf",
				formats:     []string{"pdf"},
				attachments: []string{auroraSmokeDocumentFixture},
				prompt:      "Render the attached resume fixture as a one-page PDF.",
			}},
		},
	}
}

// runAuroraSandboxProviderSmoke is the order-critical gate for one provider.
// The opt-in is checked before the credential file is read, so a missing
// opt-in never touches the provider secret.
func runAuroraSandboxProviderSmoke(t *testing.T, provider auroraSmokeProvider) {
	t.Helper()
	if os.Getenv(provider.optIn) != "1" {
		t.Skipf("set %s=1 to run the real %s smoke", provider.optIn, provider.name)
	}
	if provider.secretEnv != "" {
		requireAuroraSmokeSecretFile(t, provider.secretEnv)
	}
	cfg, err := loadAuroraSmokeConfig()
	if err != nil {
		t.Fatalf("aurora smoke configuration: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), auroraSmokeOuterDeadline)
	defer cancel()

	harness := &auroraSmokeHarness{
		t:        t,
		cfg:      cfg,
		client:   newAuroraSmokeClient(cfg),
		repoRoot: auroraSmokeRepoRoot(),
	}
	if err := harness.ensureWorkspace(ctx); err != nil {
		t.Fatalf("aurora smoke workspace: %v", err)
	}
	t.Cleanup(harness.cleanup)

	for i, operation := range provider.operations {
		// The operation index keeps a two-create OpenAI run distinguishable
		// without ever logging the prompt.
		harness.runOperation(ctx, provider, i+1, operation)
	}
}

// ---------------------------------------------------------------------------
// Configuration and credential gates
// ---------------------------------------------------------------------------

type auroraSmokeConfig struct {
	baseURL     string
	apiToken    string
	workspaceID string
	imageDigest string
}

func loadAuroraSmokeConfig() (auroraSmokeConfig, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("AURORA_SMOKE_BASE_URL")), "/")
	if baseURL == "" {
		return auroraSmokeConfig{}, errors.New("AURORA_SMOKE_BASE_URL is required")
	}
	token := strings.TrimSpace(os.Getenv("AURORA_SMOKE_API_TOKEN"))
	if token == "" {
		return auroraSmokeConfig{}, errors.New("AURORA_SMOKE_API_TOKEN is required")
	}
	image := strings.TrimSpace(os.Getenv("AURORA_RUNTIME_IMAGE"))
	if !auroraSmokeImageDigestRE.MatchString(image) {
		return auroraSmokeConfig{}, errors.New("AURORA_RUNTIME_IMAGE must be a digest-pinned @sha256 reference")
	}
	return auroraSmokeConfig{
		baseURL:     baseURL,
		apiToken:    token,
		workspaceID: strings.TrimSpace(os.Getenv("AURORA_SMOKE_WORKSPACE_ID")),
		imageDigest: image,
	}, nil
}

// requireAuroraSmokeSecretFile verifies the provider credential file is
// present, non-empty, and mode 0400 (owner read-only). It runs only after the
// provider opt-in has been confirmed and never logs the file path.
func requireAuroraSmokeSecretFile(t *testing.T, env string) {
	t.Helper()
	path := strings.TrimSpace(os.Getenv(env))
	if path == "" {
		t.Fatalf("%s must name the provider credential file", env)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("%s credential file is not readable", env)
	}
	if info.Mode().Perm() != 0o400 {
		t.Fatalf("%s credential file must be mode 0400, got %04o", env, info.Mode().Perm())
	}
	if info.Size() == 0 {
		t.Fatalf("%s credential file is empty", env)
	}
}

// ---------------------------------------------------------------------------
// HTTP client
// ---------------------------------------------------------------------------

type auroraSmokeClient struct {
	baseURL     string
	apiToken    string
	workspaceID string
	http        *http.Client
}

func newAuroraSmokeClient(cfg auroraSmokeConfig) *auroraSmokeClient {
	return &auroraSmokeClient{
		baseURL:     cfg.baseURL,
		apiToken:    cfg.apiToken,
		workspaceID: cfg.workspaceID,
		http:        &http.Client{Timeout: 90 * time.Second},
	}
}

func (c *auroraSmokeClient) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("Accept", "application/json")
	if c.workspaceID != "" {
		req.Header.Set("X-Workspace-ID", c.workspaceID)
	}
	return req, nil
}

// doJSON issues one request and decodes a bounded JSON response into out. Error
// messages carry only the method, path, and HTTP status: never the response
// body, which could contain provider text or a signed URL.
func (c *auroraSmokeClient) doJSON(ctx context.Context, method, path string, payload, out any) error {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := c.newRequest(ctx, method, path, body)
	if err != nil {
		return err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: transport failure", method, path)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, auroraSmokeMaxResponseBytes))
	if err != nil {
		return fmt.Errorf("%s %s: response read failure", method, path)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: HTTP %d", method, path, resp.StatusCode)
	}
	if out != nil && len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("%s %s: invalid JSON response", method, path)
		}
	}
	return nil
}

// uploadFixture sends one committed fixture as a multipart attachment upload
// and returns its attachment ID. It reads the file from the repository, not
// from the live stack, and never logs the returned URL.
func (c *auroraSmokeClient) uploadFixture(ctx context.Context, path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read fixture %s: %w", filepath.Base(path), err)
	}
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, err := writer.CreateFormFile("file", filepath.Base(path))
	if err != nil {
		return "", err
	}
	if _, err := part.Write(data); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}
	req, err := c.newRequest(ctx, http.MethodPost, "/api/upload-file", &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("upload fixture %s: transport failure", filepath.Base(path))
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, auroraSmokeMaxResponseBytes))
	if err != nil {
		return "", fmt.Errorf("upload fixture %s: response read failure", filepath.Base(path))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("upload fixture %s: HTTP %d", filepath.Base(path), resp.StatusCode)
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.ID == "" {
		return "", fmt.Errorf("upload fixture %s: no attachment id", filepath.Base(path))
	}
	return out.ID, nil
}

// downloadAsset streams one artifact through the authenticated download route,
// following the server's own redirect policy, and computes its SHA-256 and byte
// count. The body is never logged and the signed URL is never surfaced.
func (c *auroraSmokeClient) downloadAsset(ctx context.Context, assetID string) (int64, string, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/api/aurora/assets/"+assetID+"/download", nil)
	if err != nil {
		return 0, "", err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("download asset %s: transport failure", assetID)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, "", fmt.Errorf("download asset %s: HTTP %d", assetID, resp.StatusCode)
	}
	digest := sha256.New()
	n, err := io.Copy(digest, io.LimitReader(resp.Body, auroraSmokeMaxArtifactBytes+1))
	if err != nil {
		return 0, "", fmt.Errorf("download asset %s: body read failure", assetID)
	}
	if n > auroraSmokeMaxArtifactBytes {
		return 0, "", fmt.Errorf("download asset %s: artifact exceeds the smoke byte cap", assetID)
	}
	return n, hex.EncodeToString(digest.Sum(nil)), nil
}

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

type auroraSmokeHarness struct {
	t           *testing.T
	cfg         auroraSmokeConfig
	client      *auroraSmokeClient
	repoRoot    string
	workspaceID string
	owned       bool
	assetIDs    []string
}

type auroraSmokeGeneration struct {
	ID              string  `json:"id"`
	SkillID         string  `json:"skillId"`
	Prompt          string  `json:"prompt"`
	Status          string  `json:"status"`
	CreditsReserved int64   `json:"creditsReserved"`
	CreditsCharged  int64   `json:"creditsCharged"`
	Error           *string `json:"error"`
	CreatedAt       string  `json:"createdAt"`
}

type auroraSmokeAsset struct {
	ID           string  `json:"id"`
	GenerationID string  `json:"generationId"`
	Kind         string  `json:"kind"`
	MediaURL     *string `json:"mediaUrl"`
	Format       *string `json:"format"`
	CreatedAt    string  `json:"createdAt"`
}

type auroraSmokeGenerationDetail struct {
	auroraSmokeGeneration
	Assets []auroraSmokeAsset `json:"assets"`
}

type auroraSmokeTransaction struct {
	ID                string `json:"id"`
	Kind              string `json:"kind"`
	AmountMicro       int64  `json:"amountMicro"`
	BalanceAfterMicro int64  `json:"balanceAfterMicro"`
	Reference         string `json:"reference"`
	CreatedAt         string `json:"createdAt"`
}

// ensureWorkspace creates a fresh workspace through the public API unless the
// caller supplied one. A fresh workspace is what makes the generation, its
// assets, and its sandbox node safe to delete wholesale at the end.
func (h *auroraSmokeHarness) ensureWorkspace(ctx context.Context) error {
	if h.cfg.workspaceID != "" {
		h.client.workspaceID = h.cfg.workspaceID
		h.workspaceID = h.cfg.workspaceID
		h.owned = false
		return nil
	}
	slug := fmt.Sprintf("aurora-smoke-%d", time.Now().UnixNano())
	var workspace struct {
		ID string `json:"id"`
	}
	if err := h.client.doJSON(ctx, http.MethodPost, "/api/workspaces/", map[string]string{
		"name": "Aurora real smoke",
		"slug": slug,
	}, &workspace); err != nil {
		return err
	}
	if workspace.ID == "" {
		return errors.New("workspace create returned no id")
	}
	h.client.workspaceID = workspace.ID
	h.workspaceID = workspace.ID
	h.owned = true
	h.t.Logf("aurora real smoke workspace=%s owned=true", workspace.ID)
	return nil
}

// cleanup deletes the assets the run produced and, when the harness created the
// workspace, the workspace itself. Deleting the workspace removes its
// generation rows and its managed sandbox node binding; the fleet destroys the
// node container from that lifecycle. Cleanup uses a fresh context so an
// expired outer deadline cannot strand the objects.
func (h *auroraSmokeHarness) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	for _, assetID := range h.assetIDs {
		if err := h.client.doJSON(ctx, http.MethodDelete, "/api/aurora/assets/"+assetID, nil, nil); err != nil {
			h.t.Logf("aurora real smoke cleanup asset=%s status=error", assetID)
		}
	}
	if h.owned && h.workspaceID != "" {
		if err := h.client.doJSON(ctx, http.MethodDelete, "/api/workspaces/"+h.workspaceID+"/", nil, nil); err != nil {
			h.t.Logf("aurora real smoke cleanup workspace=%s status=error", h.workspaceID)
			return
		}
		h.t.Logf("aurora real smoke cleanup workspace=%s status=deleted", h.workspaceID)
	}
}

// runOperation executes exactly one provider create through the public API and
// verifies its artifact and settlement.
func (h *auroraSmokeHarness) runOperation(ctx context.Context, provider auroraSmokeProvider, index int, operation auroraSmokeOperation) {
	started := time.Now()
	attachmentIDs := make([]string, 0, len(operation.attachments))
	for _, fixture := range operation.attachments {
		attachmentID, err := h.client.uploadFixture(ctx, filepath.Join(h.repoRoot, filepath.FromSlash(fixture)))
		if err != nil {
			h.t.Fatalf("aurora real smoke provider=%s operation=%d: %v", provider.provider, index, err)
		}
		attachmentIDs = append(attachmentIDs, attachmentID)
	}

	var created struct {
		Generation auroraSmokeGeneration `json:"generation"`
	}
	payload := map[string]any{
		"skillId": operation.skillID,
		"prompt":  operation.prompt,
	}
	if len(attachmentIDs) > 0 {
		payload["attachmentIds"] = attachmentIDs
	}
	if err := h.client.doJSON(ctx, http.MethodPost, "/api/aurora/generations", payload, &created); err != nil {
		h.t.Fatalf("aurora real smoke provider=%s operation=%d create: %v", provider.provider, index, err)
	}
	generationID := created.Generation.ID
	if generationID == "" {
		h.t.Fatalf("aurora real smoke provider=%s operation=%d returned no generation id", provider.provider, index)
	}
	h.t.Logf("aurora real smoke provider=%s model=%s generation=%s skill=%s status=%s",
		provider.provider, provider.model, generationID, operation.skillID, created.Generation.Status)

	detail := h.waitForTerminal(ctx, generationID)
	h.verifyArtifacts(ctx, generationID, operation, detail)
	h.verifySettlement(ctx, generationID, detail)
	h.t.Logf("aurora real smoke provider=%s model=%s generation=%s status=%s duration=%s credits_reserved=%d credits_charged=%d assets=%d",
		provider.provider, provider.model, generationID, detail.Status, time.Since(started).Round(time.Second),
		detail.CreditsReserved, detail.CreditsCharged, len(detail.Assets))
}

// waitForTerminal polls the generation detail endpoint until the status is
// terminal or the wait bound elapses, logging only IDs and statuses.
func (h *auroraSmokeHarness) waitForTerminal(ctx context.Context, generationID string) auroraSmokeGenerationDetail {
	h.t.Helper()
	deadline := time.Now().Add(auroraSmokeTerminalWait)
	for {
		detail, err := h.generation(ctx, generationID)
		if err != nil {
			h.t.Fatalf("aurora real smoke generation=%s status read: %v", generationID, err)
		}
		switch detail.Status {
		case "completed":
			return detail
		case "failed":
			h.t.Fatalf("aurora real smoke generation=%s status=failed", generationID)
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("aurora real smoke generation=%s did not reach a terminal status before the wait bound", generationID)
		}
		select {
		case <-ctx.Done():
			h.t.Fatalf("aurora real smoke generation=%s exceeded the outer deadline", generationID)
		case <-time.After(auroraSmokePollInterval):
		}
	}
}

func (h *auroraSmokeHarness) generation(ctx context.Context, generationID string) (auroraSmokeGenerationDetail, error) {
	var envelope struct {
		Generation auroraSmokeGenerationDetail `json:"generation"`
	}
	if err := h.client.doJSON(ctx, http.MethodGet, "/api/aurora/generations/"+generationID, nil, &envelope); err != nil {
		return auroraSmokeGenerationDetail{}, err
	}
	return envelope.Generation, nil
}

// verifyArtifacts downloads every asset belonging to the generation, confirms
// the expected kind/format is present, and verifies a nonzero size and a
// well-formed SHA-256. Asset bytes and signed URLs are never logged.
func (h *auroraSmokeHarness) verifyArtifacts(ctx context.Context, generationID string, operation auroraSmokeOperation, detail auroraSmokeGenerationDetail) {
	h.t.Helper()
	if len(detail.Assets) == 0 {
		h.t.Fatalf("aurora real smoke generation=%s completed with no assets", generationID)
	}
	matched := false
	for _, asset := range detail.Assets {
		if asset.GenerationID != generationID {
			h.t.Fatalf("aurora real smoke generation=%s returned a foreign asset", generationID)
		}
		h.assetIDs = append(h.assetIDs, asset.ID)
		kind := strings.ToLower(strings.TrimSpace(asset.Kind))
		format := ""
		if asset.Format != nil {
			format = strings.ToLower(strings.TrimSpace(*asset.Format))
		}
		if kind != operation.kind || !auroraSmokeFormatAllowed(format, operation.formats) {
			continue
		}
		size, sum, err := h.client.downloadAsset(ctx, asset.ID)
		if err != nil {
			h.t.Fatalf("aurora real smoke generation=%s asset read: %v", generationID, err)
		}
		if size <= 0 {
			h.t.Fatalf("aurora real smoke generation=%s asset=%s has zero bytes", generationID, asset.ID)
		}
		if !auroraSmokeSHA256RE.MatchString(sum) {
			h.t.Fatalf("aurora real smoke generation=%s asset=%s failed its SHA-256 check", generationID, asset.ID)
		}
		h.t.Logf("aurora real smoke generation=%s asset=%s kind=%s format=%s bytes=%d",
			generationID, asset.ID, kind, format, size)
		matched = true
	}
	if !matched {
		h.t.Fatalf("aurora real smoke generation=%s produced no %s artifact in formats %v", generationID, operation.kind, operation.formats)
	}
}

// verifySettlement confirms the exact credit settlement: the completed
// generation charged its reservation, and the ledger carries one net deduction
// for the generation equal to the charged amount. The available balance is
// logged but not asserted as a delta, because a first-generation free-tier
// grant is applied during the same create.
func (h *auroraSmokeHarness) verifySettlement(ctx context.Context, generationID string, detail auroraSmokeGenerationDetail) {
	h.t.Helper()
	if detail.CreditsCharged < 0 {
		h.t.Fatalf("aurora real smoke generation=%s charged a negative amount", generationID)
	}
	if detail.CreditsCharged != detail.CreditsReserved {
		h.t.Fatalf("aurora real smoke generation=%s charged %d micro but reserved %d micro",
			generationID, detail.CreditsCharged, detail.CreditsReserved)
	}
	var ledger struct {
		Transactions []auroraSmokeTransaction `json:"transactions"`
	}
	if err := h.client.doJSON(ctx, http.MethodGet, "/api/aurora/billing/transactions?limit=200", nil, &ledger); err != nil {
		h.t.Fatalf("aurora real smoke generation=%s ledger read: %v", generationID, err)
	}
	var settled int64
	for _, transaction := range ledger.Transactions {
		if transaction.Reference == generationID {
			settled += transaction.AmountMicro
		}
	}
	if settled != -detail.CreditsCharged {
		h.t.Fatalf("aurora real smoke generation=%s ledger settled %d micro, want %d", generationID, settled, -detail.CreditsCharged)
	}
	h.t.Logf("aurora real smoke generation=%s charged_credits=%d ledger_micro=%d",
		generationID, detail.CreditsCharged/auroraSmokeMicroPerCredit, settled)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func auroraSmokeFormatAllowed(format string, allowed []string) bool {
	for _, candidate := range allowed {
		if format == candidate {
			return true
		}
	}
	return false
}

// auroraSmokeRepoRoot resolves the repository root from this source file
// (<root>/server/pkg/agent/aurora_sandbox_smoke_test.go), so fixture paths do
// not depend on the test working directory.
func auroraSmokeRepoRoot() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}
