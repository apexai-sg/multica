package handler

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
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// artifactTestPNG is the shortest byte string http.DetectContentType reports as
// image/png, so sniff-based MIME tests do not need a real encoder.
const artifactTestPNG = "\x89PNG\r\n\x1a\n"

type artifactTestResponse struct {
	StagingID          string          `json:"staging_id"`
	ID                 string          `json:"id"`
	TaskID             string          `json:"task_id"`
	GenerationID       string          `json:"generation_id"`
	ManifestArtifactID *string         `json:"manifest_artifact_id"`
	Name               string          `json:"name"`
	Kind               string          `json:"kind"`
	Role               *string         `json:"role"`
	Format             *string         `json:"format"`
	MIMEType           string          `json:"mime_type"`
	SizeBytes          int64           `json:"size_bytes"`
	SHA256             string          `json:"sha256"`
	SourceType         string          `json:"source_type"`
	Status             string          `json:"status"`
	Metadata           json.RawMessage `json:"metadata"`
}

// artifactTestStorage records what the handler uploaded and deleted so a test
// can prove cleanup happened (or did not).
type artifactTestStorage struct {
	mockStorage
	failUpload bool
	deleted    []string
}

func (s *artifactTestStorage) UploadStream(ctx context.Context, key string, reader io.Reader, size int64, contentType, filename string) (string, error) {
	if s.failUpload {
		return "", errors.New("artifact test storage: upload refused")
	}
	return s.mockStorage.UploadStream(ctx, key, reader, size, contentType, filename)
}

func (s *artifactTestStorage) DeleteObject(ctx context.Context, key string) error {
	s.deleted = append(s.deleted, key)
	return s.mockStorage.DeleteObject(ctx, key)
}

func withArtifactStorage(t *testing.T, s *artifactTestStorage) {
	t.Helper()
	previous := testHandler.Storage
	testHandler.Storage = s
	t.Cleanup(func() { testHandler.Storage = previous })
}

func withArtifactImportClient(t *testing.T, client *http.Client) {
	t.Helper()
	previous := testHandler.auroraArtifactImportClient
	testHandler.auroraArtifactImportClient = client
	t.Cleanup(func() { testHandler.auroraArtifactImportClient = previous })
}

// artifactTestHTTPSClient is the importer client with the httptest server's
// self-signed certificate trusted.
func artifactTestHTTPSClient(t *testing.T, server *httptest.Server) *http.Client {
	t.Helper()
	serverTransport, ok := server.Client().Transport.(*http.Transport)
	if !ok {
		t.Fatalf("httptest transport = %T, want *http.Transport", server.Client().Transport)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = serverTransport.TLSClientConfig
	return &http.Client{Transport: transport, Timeout: 30 * time.Second}
}

func seedAuroraArtifactTask(t *testing.T, skillID string) (agentID, taskID, generationID string) {
	t.Helper()
	agentID = dbfx.Agent(t, "Aurora Artifact Agent "+uuid.NewString(), "")
	taskID = dbfx.Task(t, agentID, testutil.Cols{"status": "running", "runtime_id": testRuntimeID})
	generationID = insertGeneration(t, "artifact staging", testutil.Cols{
		"skill_id": skillID,
		"task_id":  taskID,
		"status":   "running",
	})
	dbfx.Cleanup(t, `DELETE FROM aurora_artifact_staging WHERE task_id = $1`, taskID)
	return agentID, taskID, generationID
}

func artifactSHA256Field(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func artifactUploadFields(artifactID, name, kind, role, format, mime string, size int, digest string) map[string]string {
	return map[string]string{
		"manifest_artifact_id": artifactID,
		"name":                 name,
		"kind":                 kind,
		"role":                 role,
		"format":               format,
		"mime_type":            mime,
		"size_bytes":           fmt.Sprintf("%d", size),
		"sha256":               digest,
	}
}

// artifactUploadRequest builds a metadata-then-file multipart request. Metadata
// precedes the file because the handler learns the kind (and its cap) from the
// fields before it streams the body.
func artifactUploadRequest(t *testing.T, taskID, agentID, tokenTaskID string, fields map[string]string, body []byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatalf("write form field %q: %v", key, err)
		}
	}
	part, err := writer.CreateFormFile("file", fields["name"])
	if err != nil {
		t.Fatalf("create file part: %v", err)
	}
	if _, err := part.Write(body); err != nil {
		t.Fatalf("write file part: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/agent/tasks/"+taskID+"/aurora-artifacts/upload", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req = testutil.WithURLParams(req, "taskID", taskID)
	return testutil.WithHeaders(req,
		"X-User-ID", testUserID,
		"X-Workspace-ID", testWorkspaceID,
		"X-Actor-Source", "task_token",
		"X-Agent-ID", agentID,
		"X-Task-ID", tokenTaskID,
	)
}

func artifactTokenHeaders(req *http.Request, agentID, tokenTaskID string) *http.Request {
	return testutil.WithHeaders(req,
		"X-User-ID", testUserID,
		"X-Workspace-ID", testWorkspaceID,
		"X-Actor-Source", "task_token",
		"X-Agent-ID", agentID,
		"X-Task-ID", tokenTaskID,
	)
}

func artifactImportRequest(taskID, agentID, tokenTaskID string, body any) *http.Request {
	req := testutil.JSONRequest(http.MethodPost, "/api/agent/tasks/"+taskID+"/aurora-artifacts/import", body)
	req = testutil.WithURLParams(req, "taskID", taskID)
	return artifactTokenHeaders(req, agentID, tokenTaskID)
}

func artifactImportBody(url string, over map[string]any) map[string]any {
	body := map[string]any{
		"url":       url,
		"kind":      "image",
		"name":      "primary-1.png",
		"mime_type": "image/png",
		"metadata":  map[string]any{},
	}
	for key, value := range over {
		body[key] = value
	}
	return body
}

func storedArtifactCount(t *testing.T, taskID string) int {
	t.Helper()
	return dbfx.Count(t, `SELECT count(*) FROM aurora_artifact_staging WHERE task_id = $1`, taskID)
}

// ---------------------------------------------------------------------------
// Local stream upload
// ---------------------------------------------------------------------------

func TestAuroraArtifactUploadStreamsFileIntoStaging(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	store := &artifactTestStorage{}
	withArtifactStorage(t, store)
	agentID, taskID, generationID := seedAuroraArtifactTask(t, "ecommerce-bidding-strategy")

	content := []byte(artifactTestPNG + "primary image payload")
	fields := artifactUploadFields("primary-1", "primary-1.png", "image", "primary", "png", "image/png", len(content), artifactSHA256Field(content))

	got := testutil.Decode[artifactTestResponse](t, testHandler.UploadAuroraArtifact,
		artifactUploadRequest(t, taskID, agentID, taskID, fields, content), http.StatusOK)

	if got.StagingID == "" || got.ID != got.StagingID {
		t.Fatalf("staging id = %q / id = %q, want a non-empty stable uuid", got.StagingID, got.ID)
	}
	if got.TaskID != taskID || got.GenerationID != generationID {
		t.Fatalf("task/generation = %q/%q, want %q/%q", got.TaskID, got.GenerationID, taskID, generationID)
	}
	if got.ManifestArtifactID == nil || *got.ManifestArtifactID != "primary-1" {
		t.Fatalf("manifest_artifact_id = %v, want primary-1", got.ManifestArtifactID)
	}
	if got.SizeBytes != int64(len(content)) {
		t.Fatalf("size_bytes = %d, want %d", got.SizeBytes, len(content))
	}
	if got.SHA256 != artifactSHA256Field(content) {
		t.Fatalf("sha256 = %q, want %q", got.SHA256, artifactSHA256Field(content))
	}
	if got.SourceType != "upload" || got.Status != "staged" {
		t.Fatalf("source/status = %q/%q, want upload/staged", got.SourceType, got.Status)
	}
	if len(store.files) != 1 {
		t.Fatalf("stored objects = %d, want exactly 1", len(store.files))
	}
	for _, data := range store.files {
		if !bytes.Equal(data, content) {
			t.Fatalf("stored %d bytes differ from the uploaded content", len(data))
		}
	}
	if n := storedArtifactCount(t, taskID); n != 1 {
		t.Fatalf("staging rows = %d, want exactly 1", n)
	}
}

func TestAuroraArtifactUploadRejectsForeignTask(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	withArtifactStorage(t, &artifactTestStorage{})
	agentID, taskID, _ := seedAuroraArtifactTask(t, "ecommerce-bidding-strategy")
	_, otherTaskID, _ := seedAuroraArtifactTask(t, "ecommerce-bidding-strategy")

	content := []byte(artifactTestPNG)
	fields := artifactUploadFields("primary-1", "primary-1.png", "image", "primary", "png", "image/png", len(content), artifactSHA256Field(content))
	testutil.Call(t, testHandler.UploadAuroraArtifact,
		artifactUploadRequest(t, otherTaskID, agentID, taskID, fields, content)).Want(http.StatusForbidden)

	if n := storedArtifactCount(t, otherTaskID); n != 0 {
		t.Fatalf("foreign task wrote %d staging rows, want 0", n)
	}
}

func TestAuroraArtifactUploadRejectsForeignWorkspace(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	withArtifactStorage(t, &artifactTestStorage{})
	agentID, taskID, _ := seedAuroraArtifactTask(t, "ecommerce-bidding-strategy")

	content := []byte(artifactTestPNG)
	fields := artifactUploadFields("primary-1", "primary-1.png", "image", "primary", "png", "image/png", len(content), artifactSHA256Field(content))
	req := artifactUploadRequest(t, taskID, agentID, taskID, fields, content)
	req.Header.Set("X-Workspace-ID", uuid.NewString())
	testutil.Call(t, testHandler.UploadAuroraArtifact, req).Want(http.StatusForbidden)

	if n := storedArtifactCount(t, taskID); n != 0 {
		t.Fatalf("foreign workspace wrote %d staging rows, want 0", n)
	}
}

func TestAuroraArtifactUploadRejectsNonAuroraTask(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	withArtifactStorage(t, &artifactTestStorage{})
	agentID := dbfx.Agent(t, "Aurora Artifact Non Aurora Agent "+uuid.NewString(), "")
	taskID := dbfx.Task(t, agentID, testutil.Cols{"status": "running", "runtime_id": testRuntimeID})

	content := []byte(artifactTestPNG)
	fields := artifactUploadFields("primary-1", "primary-1.png", "image", "primary", "png", "image/png", len(content), artifactSHA256Field(content))
	testutil.Call(t, testHandler.UploadAuroraArtifact,
		artifactUploadRequest(t, taskID, agentID, taskID, fields, content)).Want(http.StatusForbidden)
}

func TestAuroraArtifactUploadRequiresTaskToken(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	withArtifactStorage(t, &artifactTestStorage{})
	agentID, taskID, _ := seedAuroraArtifactTask(t, "ecommerce-bidding-strategy")

	content := []byte(artifactTestPNG)
	fields := artifactUploadFields("primary-1", "primary-1.png", "image", "primary", "png", "image/png", len(content), artifactSHA256Field(content))
	req := artifactUploadRequest(t, taskID, agentID, taskID, fields, content)
	req.Header.Del("X-Actor-Source")
	testutil.Call(t, testHandler.UploadAuroraArtifact, req).Want(http.StatusForbidden)
}

func TestAuroraArtifactUploadRejectsDuplicateArtifactID(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	store := &artifactTestStorage{}
	withArtifactStorage(t, store)
	agentID, taskID, _ := seedAuroraArtifactTask(t, "ecommerce-bidding-strategy")

	content := []byte(artifactTestPNG)
	fields := artifactUploadFields("primary-1", "primary-1.png", "image", "primary", "png", "image/png", len(content), artifactSHA256Field(content))
	testutil.Call(t, testHandler.UploadAuroraArtifact,
		artifactUploadRequest(t, taskID, agentID, taskID, fields, content)).Want(http.StatusOK)

	// The second upload streams to storage first and only then loses the
	// (task_id, manifest_artifact_id) unique index, so the handler must have
	// removed the object it just stored.
	testutil.Call(t, testHandler.UploadAuroraArtifact,
		artifactUploadRequest(t, taskID, agentID, taskID, fields, content)).Want(http.StatusConflict)

	if n := storedArtifactCount(t, taskID); n != 1 {
		t.Fatalf("staging rows = %d, want exactly 1 after a duplicate", n)
	}
	if len(store.deleted) == 0 {
		t.Fatalf("duplicate upload did not delete its stored object")
	}
	if len(store.files) != 1 {
		t.Fatalf("stored objects = %d, want exactly 1 after cleanup", len(store.files))
	}
}

func TestAuroraArtifactUploadRejectsMIMEMismatch(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	store := &artifactTestStorage{}
	withArtifactStorage(t, store)
	agentID, taskID, _ := seedAuroraArtifactTask(t, "ecommerce-bidding-strategy")

	content := []byte("this is plainly text, not a png")
	fields := artifactUploadFields("primary-1", "primary-1.png", "image", "primary", "png", "image/png", len(content), artifactSHA256Field(content))
	testutil.Call(t, testHandler.UploadAuroraArtifact,
		artifactUploadRequest(t, taskID, agentID, taskID, fields, content)).Want(http.StatusUnsupportedMediaType)

	if n := storedArtifactCount(t, taskID); n != 0 {
		t.Fatalf("mismatched upload wrote %d staging rows, want 0", n)
	}
	if len(store.files) != 0 {
		t.Fatalf("mismatched upload stored an object before sniffing the bytes")
	}
}

func TestAuroraArtifactUploadRejectsUnsupportedMIME(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	store := &artifactTestStorage{}
	withArtifactStorage(t, store)
	agentID, taskID, _ := seedAuroraArtifactTask(t, "ecommerce-bidding-strategy")

	content := []byte(artifactTestPNG)
	fields := artifactUploadFields("primary-1", "primary-1.bin", "image", "primary", "png", "application/x-msdownload", len(content), artifactSHA256Field(content))
	testutil.Call(t, testHandler.UploadAuroraArtifact,
		artifactUploadRequest(t, taskID, agentID, taskID, fields, content)).Want(http.StatusUnsupportedMediaType)

	if len(store.files) != 0 {
		t.Fatalf("unsupported MIME stored an object, want none")
	}
}

func TestAuroraArtifactUploadRejectsKindOutsideSkillPolicy(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	store := &artifactTestStorage{}
	withArtifactStorage(t, store)
	// text-image only produces image artifacts; a video is a policy violation.
	agentID, taskID, _ := seedAuroraArtifactTask(t, "ecommerce-bidding-strategy")

	content := []byte(artifactTestPNG)
	fields := artifactUploadFields("primary-1", "primary-1.png", "image", "primary", "png", "image/png", len(content), artifactSHA256Field(content))
	testutil.Call(t, testHandler.UploadAuroraArtifact,
		artifactUploadRequest(t, taskID, agentID, taskID, fields, content)).Want(http.StatusOK)

	video := []byte{0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p', 'm', 'p', '4', '2'}
	videoFields := artifactUploadFields("primary-2", "primary-2.mp4", "video", "primary", "mp4", "video/mp4", len(video), artifactSHA256Field(video))
	testutil.Call(t, testHandler.UploadAuroraArtifact,
		artifactUploadRequest(t, taskID, agentID, taskID, videoFields, video)).Want(http.StatusBadRequest)
}

// TestAuroraArtifactUploadAcceptsTheVideoCaptionsTranscript covers the one
// artifact a skill publishes outside its declared output: video-captions only
// advertises the video, and its ASR transcript travels beside it as role
// `transcript`. The report path already exempts that pair; the upload path must
// exempt the same one, or every captions run fails artifact collection with a
// 400 after the video was already encoded.
func TestAuroraArtifactUploadAcceptsTheVideoCaptionsTranscript(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	store := &artifactTestStorage{}
	withArtifactStorage(t, store)
	agentID, taskID, _ := seedAuroraArtifactTask(t, "ecommerce-bidding-strategy")

	content := []byte("hello world")
	transcript := artifactUploadFields("transcript-1", "transcript.txt", "text", "transcript", "txt", "text/plain", len(content), artifactSHA256Field(content))
	testutil.Call(t, testHandler.UploadAuroraArtifact,
		artifactUploadRequest(t, taskID, agentID, taskID, transcript, content)).Want(http.StatusOK)

	// The exemption is the transcript role, not the text kind: the same kind
	// under any other role is still outside the skill's declared output.
	primary := artifactUploadFields("primary-2", "primary-2.txt", "text", "primary", "txt", "text/plain", len(content), artifactSHA256Field(content))
	testutil.Call(t, testHandler.UploadAuroraArtifact,
		artifactUploadRequest(t, taskID, agentID, taskID, primary, content)).Want(http.StatusBadRequest)
}

func TestAuroraArtifactUploadRejectsOversizeStream(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	store := &artifactTestStorage{}
	withArtifactStorage(t, store)
	agentID, taskID, _ := seedAuroraArtifactTask(t, "ecommerce-bidding-strategy")

	// Declare the cap itself so the declared-size check passes, then stream one
	// byte more than the cap.
	const cap = 25 << 20
	content := make([]byte, cap+1)
	copy(content, artifactTestPNG)
	fields := artifactUploadFields("primary-1", "primary-1.png", "image", "primary", "png", "image/png", cap, artifactSHA256Field(content))
	testutil.Call(t, testHandler.UploadAuroraArtifact,
		artifactUploadRequest(t, taskID, agentID, taskID, fields, content)).Want(http.StatusRequestEntityTooLarge)

	if n := storedArtifactCount(t, taskID); n != 0 {
		t.Fatalf("oversize stream wrote %d staging rows, want 0", n)
	}
}

func TestAuroraArtifactUploadRejectsShortDeclaredSize(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	store := &artifactTestStorage{}
	withArtifactStorage(t, store)
	agentID, taskID, _ := seedAuroraArtifactTask(t, "ecommerce-bidding-strategy")

	content := []byte(artifactTestPNG + "short")
	fields := artifactUploadFields("primary-1", "primary-1.png", "image", "primary", "png", "image/png", len(content)+100, artifactSHA256Field(content))
	testutil.Call(t, testHandler.UploadAuroraArtifact,
		artifactUploadRequest(t, taskID, agentID, taskID, fields, content)).Want(http.StatusBadRequest)

	if n := storedArtifactCount(t, taskID); n != 0 {
		t.Fatalf("short declared size wrote %d staging rows, want 0", n)
	}
	if len(store.deleted) == 0 {
		t.Fatalf("short declared size did not delete its stored object")
	}
}

func TestAuroraArtifactUploadRejectsLongDeclaredSize(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	store := &artifactTestStorage{}
	withArtifactStorage(t, store)
	agentID, taskID, _ := seedAuroraArtifactTask(t, "ecommerce-bidding-strategy")

	content := []byte(artifactTestPNG + "much longer than declared")
	fields := artifactUploadFields("primary-1", "primary-1.png", "image", "primary", "png", "image/png", 4, artifactSHA256Field(content))
	testutil.Call(t, testHandler.UploadAuroraArtifact,
		artifactUploadRequest(t, taskID, agentID, taskID, fields, content)).Want(http.StatusBadRequest)

	if n := storedArtifactCount(t, taskID); n != 0 {
		t.Fatalf("long declared size wrote %d staging rows, want 0", n)
	}
}

func TestAuroraArtifactUploadRejectsHashMismatch(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	store := &artifactTestStorage{}
	withArtifactStorage(t, store)
	agentID, taskID, _ := seedAuroraArtifactTask(t, "ecommerce-bidding-strategy")

	content := []byte(artifactTestPNG + "hash me")
	fields := artifactUploadFields("primary-1", "primary-1.png", "image", "primary", "png", "image/png", len(content), "sha256:"+strings.Repeat("0", 64))
	testutil.Call(t, testHandler.UploadAuroraArtifact,
		artifactUploadRequest(t, taskID, agentID, taskID, fields, content)).Want(http.StatusBadRequest)

	if n := storedArtifactCount(t, taskID); n != 0 {
		t.Fatalf("hash mismatch wrote %d staging rows, want 0", n)
	}
	if len(store.deleted) == 0 {
		t.Fatalf("hash mismatch did not delete its stored object")
	}
}

func TestAuroraArtifactUploadStorageFailureLeavesNoRow(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	store := &artifactTestStorage{failUpload: true}
	withArtifactStorage(t, store)
	agentID, taskID, _ := seedAuroraArtifactTask(t, "ecommerce-bidding-strategy")

	content := []byte(artifactTestPNG)
	fields := artifactUploadFields("primary-1", "primary-1.png", "image", "primary", "png", "image/png", len(content), artifactSHA256Field(content))
	testutil.Call(t, testHandler.UploadAuroraArtifact,
		artifactUploadRequest(t, taskID, agentID, taskID, fields, content)).Want(http.StatusBadGateway)

	if n := storedArtifactCount(t, taskID); n != 0 {
		t.Fatalf("storage failure wrote %d staging rows, want 0", n)
	}
}

// ---------------------------------------------------------------------------
// SSRF-safe provider import
// ---------------------------------------------------------------------------

func TestAuroraArtifactImportStreamsRemoteIntoStaging(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	content := []byte(artifactTestPNG + "remote image")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(content)
	}))
	defer server.Close()

	store := &artifactTestStorage{}
	withArtifactStorage(t, store)
	withArtifactImportClient(t, artifactTestHTTPSClient(t, server))

	agentID, taskID, generationID := seedAuroraArtifactTask(t, "ecommerce-bidding-strategy")
	resp := testutil.Call(t, testHandler.ImportAuroraArtifact,
		artifactImportRequest(taskID, agentID, taskID, artifactImportBody(server.URL+"/file", nil))).Want(http.StatusOK)
	var got artifactTestResponse
	resp.JSON(&got)
	if got.StagingID == "" {
		t.Fatalf("staging_id is empty")
	}
	if got.GenerationID != generationID {
		t.Fatalf("generation_id = %q, want %q", got.GenerationID, generationID)
	}
	if got.SizeBytes != int64(len(content)) {
		t.Fatalf("size_bytes = %d, want %d", got.SizeBytes, len(content))
	}
	if got.SHA256 != artifactSHA256Field(content) {
		t.Fatalf("sha256 = %q, want %q", got.SHA256, artifactSHA256Field(content))
	}
	if got.SourceType != "provider_import" || got.Status != "staged" {
		t.Fatalf("source/status = %q/%q, want provider_import/staged", got.SourceType, got.Status)
	}
	if got.ManifestArtifactID != nil {
		t.Fatalf("manifest_artifact_id = %v, want null for a provider import", *got.ManifestArtifactID)
	}
	if len(store.files) != 1 {
		t.Fatalf("stored objects = %d, want exactly 1", len(store.files))
	}
	for _, data := range store.files {
		if !bytes.Equal(data, content) {
			t.Fatalf("stored %d bytes differ from the provider bytes", len(data))
		}
	}
	// The provider URL must never reach the response or the row it wrote.
	if strings.Contains(resp.Body.String(), "127.0.0.1") {
		t.Fatalf("response leaked the provider URL: %s", resp.Body.String())
	}
	var stored []string
	rows, err := testPool.Query(context.Background(), `SELECT storage_key, name, mime_type, sha256, source_type, status, COALESCE(manifest_artifact_id, ''), COALESCE(role, ''), COALESCE(format, ''), metadata::text FROM aurora_artifact_staging WHERE task_id = $1`, taskID)
	if err != nil {
		t.Fatalf("query staging row: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		values := make([]string, 10)
		dest := make([]any, 10)
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			t.Fatalf("scan staging row: %v", err)
		}
		stored = append(stored, values...)
	}
	for _, value := range stored {
		if strings.Contains(value, "127.0.0.1") {
			t.Fatalf("persisted provider URL in staging row: %q", value)
		}
	}
}

func TestAuroraArtifactImportRejectsForeignTask(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	withArtifactStorage(t, &artifactTestStorage{})
	agentID, taskID, _ := seedAuroraArtifactTask(t, "ecommerce-bidding-strategy")
	_, otherTaskID, _ := seedAuroraArtifactTask(t, "ecommerce-bidding-strategy")

	testutil.Call(t, testHandler.ImportAuroraArtifact,
		artifactImportRequest(otherTaskID, agentID, taskID, artifactImportBody("https://example.invalid/x", nil))).Want(http.StatusForbidden)

	if n := storedArtifactCount(t, otherTaskID); n != 0 {
		t.Fatalf("foreign task wrote %d staging rows, want 0", n)
	}
}

func TestAuroraArtifactImportRequiresTaskToken(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	withArtifactStorage(t, &artifactTestStorage{})
	agentID, taskID, _ := seedAuroraArtifactTask(t, "ecommerce-bidding-strategy")

	req := artifactImportRequest(taskID, agentID, taskID, artifactImportBody("https://example.invalid/x", nil))
	req.Header.Del("X-Actor-Source")
	testutil.Call(t, testHandler.ImportAuroraArtifact, req).Want(http.StatusForbidden)
}

func TestAuroraArtifactImportRejectsURLCredentials(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	withArtifactStorage(t, &artifactTestStorage{})
	agentID, taskID, _ := seedAuroraArtifactTask(t, "ecommerce-bidding-strategy")

	testutil.Call(t, testHandler.ImportAuroraArtifact,
		artifactImportRequest(taskID, agentID, taskID, artifactImportBody("https://user:secret@example.com/x", nil))).Want(http.StatusBadRequest)
}

func TestAuroraArtifactImportRejectsHTTPURL(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	withArtifactStorage(t, &artifactTestStorage{})
	agentID, taskID, _ := seedAuroraArtifactTask(t, "ecommerce-bidding-strategy")

	testutil.Call(t, testHandler.ImportAuroraArtifact,
		artifactImportRequest(taskID, agentID, taskID, artifactImportBody("http://example.com/x", nil))).Want(http.StatusBadRequest)
}

func TestAuroraArtifactImportRevalidatesRedirects(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	content := []byte(artifactTestPNG + "redirected image")
	mux := http.NewServeMux()
	mux.HandleFunc("/file", func(w http.ResponseWriter, r *http.Request) { w.Write(content) })
	mux.HandleFunc("/redirect-file", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/file", http.StatusFound)
	})
	server := httptest.NewTLSServer(mux)
	defer server.Close()

	store := &artifactTestStorage{}
	withArtifactStorage(t, store)
	withArtifactImportClient(t, artifactTestHTTPSClient(t, server))
	agentID, taskID, _ := seedAuroraArtifactTask(t, "ecommerce-bidding-strategy")

	t.Run("allowed redirect is followed", func(t *testing.T) {
		got := testutil.Decode[artifactTestResponse](t, testHandler.ImportAuroraArtifact,
			artifactImportRequest(taskID, agentID, taskID, artifactImportBody(server.URL+"/redirect-file", nil)), http.StatusOK)
		if got.SizeBytes != int64(len(content)) {
			t.Fatalf("size_bytes = %d, want %d", got.SizeBytes, len(content))
		}
	})

}

func TestAuroraArtifactImportRejectsExcessiveResponseBytes(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	const cap = 25 << 20
	mux := http.NewServeMux()
	mux.HandleFunc("/oversize", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		chunk := make([]byte, 1<<20)
		copy(chunk, artifactTestPNG)
		written := 0
		for written <= cap {
			n, err := w.Write(chunk)
			if err != nil {
				return
			}
			written += n
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		}
	})
	server := httptest.NewTLSServer(mux)
	defer server.Close()

	withArtifactStorage(t, &artifactTestStorage{})
	withArtifactImportClient(t, artifactTestHTTPSClient(t, server))
	agentID, taskID, _ := seedAuroraArtifactTask(t, "ecommerce-bidding-strategy")

	testutil.Call(t, testHandler.ImportAuroraArtifact,
		artifactImportRequest(taskID, agentID, taskID, artifactImportBody(server.URL+"/oversize", nil))).Want(http.StatusRequestEntityTooLarge)

	if n := storedArtifactCount(t, taskID); n != 0 {
		t.Fatalf("oversize import wrote %d staging rows, want 0", n)
	}
}

func TestAuroraArtifactImportRejectsUnsupportedMIME(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	withArtifactStorage(t, &artifactTestStorage{})
	agentID, taskID, _ := seedAuroraArtifactTask(t, "ecommerce-bidding-strategy")

	testutil.Call(t, testHandler.ImportAuroraArtifact,
		artifactImportRequest(taskID, agentID, taskID, artifactImportBody("https://example.invalid/x",
			map[string]any{"mime_type": "application/x-msdownload"}))).Want(http.StatusUnsupportedMediaType)
}

func TestAuroraArtifactImportRejectsSniffedMIMEMismatch(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("this response is text, not an image"))
	}))
	defer server.Close()

	withArtifactStorage(t, &artifactTestStorage{})
	withArtifactImportClient(t, artifactTestHTTPSClient(t, server))
	agentID, taskID, _ := seedAuroraArtifactTask(t, "ecommerce-bidding-strategy")

	testutil.Call(t, testHandler.ImportAuroraArtifact,
		artifactImportRequest(taskID, agentID, taskID, artifactImportBody(server.URL+"/file", nil))).Want(http.StatusUnsupportedMediaType)
}

func TestAuroraArtifactImportTimeout(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(400 * time.Millisecond)
		w.Write([]byte(artifactTestPNG))
	})
	server := httptest.NewTLSServer(mux)
	defer server.Close()

	client := artifactTestHTTPSClient(t, server)
	client.Timeout = 50 * time.Millisecond
	withArtifactStorage(t, &artifactTestStorage{})
	withArtifactImportClient(t, client)
	agentID, taskID, _ := seedAuroraArtifactTask(t, "ecommerce-bidding-strategy")

	testutil.Call(t, testHandler.ImportAuroraArtifact,
		artifactImportRequest(taskID, agentID, taskID, artifactImportBody(server.URL+"/slow", nil))).Want(http.StatusGatewayTimeout)

	if n := storedArtifactCount(t, taskID); n != 0 {
		t.Fatalf("timed-out import wrote %d staging rows, want 0", n)
	}
}

func TestAuroraArtifactImportDeletesObjectAfterStagingInsertFailure(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	content := []byte(artifactTestPNG + "duplicate import")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(content)
	}))
	defer server.Close()

	store := &artifactTestStorage{}
	withArtifactStorage(t, store)
	withArtifactImportClient(t, artifactTestHTTPSClient(t, server))
	agentID, taskID, _ := seedAuroraArtifactTask(t, "ecommerce-bidding-strategy")

	body := artifactImportBody(server.URL+"/file", map[string]any{"manifest_artifact_id": "primary-1"})
	testutil.Call(t, testHandler.ImportAuroraArtifact,
		artifactImportRequest(taskID, agentID, taskID, body)).Want(http.StatusOK)
	testutil.Call(t, testHandler.ImportAuroraArtifact,
		artifactImportRequest(taskID, agentID, taskID, body)).Want(http.StatusConflict)

	if n := storedArtifactCount(t, taskID); n != 1 {
		t.Fatalf("staging rows = %d, want exactly 1 after a duplicate import", n)
	}
	if len(store.deleted) == 0 {
		t.Fatalf("duplicate import did not delete its stored object")
	}
}

func TestAuroraArtifactImportStorageFailureLeavesNoRow(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	content := []byte(artifactTestPNG + "storage fails")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(content)
	}))
	defer server.Close()

	withArtifactStorage(t, &artifactTestStorage{failUpload: true})
	withArtifactImportClient(t, artifactTestHTTPSClient(t, server))
	agentID, taskID, _ := seedAuroraArtifactTask(t, "ecommerce-bidding-strategy")

	testutil.Call(t, testHandler.ImportAuroraArtifact,
		artifactImportRequest(taskID, agentID, taskID, artifactImportBody(server.URL+"/file", nil))).Want(http.StatusBadGateway)

	if n := storedArtifactCount(t, taskID); n != 0 {
		t.Fatalf("storage failure wrote %d staging rows, want 0", n)
	}
}
