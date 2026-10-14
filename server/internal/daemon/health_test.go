package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/repocache"
)

func TestHealthHandlerReportsEmptyCLICommitWithoutFallback(t *testing.T) {
	d := &Daemon{
		cfg:        Config{CLIVersion: "v9.9.9", DaemonID: "daemon-test", ServerBaseURL: "https://api.example.test"},
		workspaces: map[string]*workspaceState{},
		logger:     slog.Default(),
	}
	rec := httptest.NewRecorder()
	d.healthHandler(time.Now()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode health response: %v", err)
	}
	if got, present := raw["cli_commit"]; !present || got != "" {
		t.Fatalf("cli_commit: got %v, present %v; want an explicit empty string without version fallback", got, present)
	}
}

func TestHealthHandlerReportsCLICommitAndSameIdentitySnapshot(t *testing.T) {
	for _, commit := range []string{strings.Repeat("a", 40), "", "unknown", "2ae2dbb", "malformed"} {
		t.Run("commit="+commit, func(t *testing.T) {
			d := New(Config{
				WorkspacesRoot: t.TempDir(), CLICommit: commit, CLIVersion: "v9.9.9",
				Profile: "profile-test", DaemonID: "daemon-test", DeviceName: "device-test",
				ServerBaseURL: "https://api.example.test",
			}, slog.New(slog.NewTextHandler(io.Discard, nil)))
			d.workspaces["workspace-test"] = &workspaceState{runtimeIDs: []string{"runtime-test"}}
			d.ready.Store(true)
			d.activeTasks.Store(2)
			d.runningTasks.Store(1)
			d.resourceWaitTasks.Store(1)
			rec := httptest.NewRecorder()
			d.healthHandler(time.Now()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("health status: got %d, want 200", rec.Code)
			}
			var raw map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
				t.Fatalf("decode raw health response: %v", err)
			}
			for key, want := range map[string]any{
				"cli_commit": commit, "cli_version": "v9.9.9", "profile": "profile-test",
				"daemon_id": "daemon-test", "server_url": "https://api.example.test",
				"device_name": "device-test", "status": "running", "os": runtime.GOOS,
				"active_task_count": float64(2), "running_task_count": float64(1),
				"resource_wait_task_count": float64(1),
			} {
				if got, present := raw[key]; !present || got != want {
					t.Errorf("%s: got %v, present %v; want %v", key, got, present, want)
				}
			}
			var typed HealthResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &typed); err != nil {
				t.Fatalf("decode typed health response: %v", err)
			}
			if typed.CLICommit != commit || len(typed.Workspaces) != 1 ||
				typed.Workspaces[0].ID != "workspace-test" || len(typed.Workspaces[0].Runtimes) != 1 ||
				typed.Workspaces[0].Runtimes[0] != "runtime-test" {
				t.Fatalf("commit or workspace/runtime projection changed: %+v", typed)
			}
		})
	}
}

func TestHealthHandlerCLICommitIsCopiedAtDaemonConstruction(t *testing.T) {
	cfg := Config{WorkspacesRoot: t.TempDir(), CLICommit: strings.Repeat("a", 40),
		CLIVersion: "v9.9.9", Profile: "profile-test", DaemonID: "daemon-test",
		ServerBaseURL: "https://api.example.test"}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	d := New(cfg, logger)
	read := func(d *Daemon) HealthResponse {
		t.Helper()
		rec := httptest.NewRecorder()
		d.healthHandler(time.Now()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
		var resp HealthResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode health response: %v", err)
		}
		return resp
	}
	// Changing the input for a later instance cannot change the running instance's build identity.
	cfg.CLICommit = strings.Repeat("b", 40)
	cfg.CLIVersion = "v10.0.0"
	if got := read(d); got.CLICommit != strings.Repeat("a", 40) || got.CLIVersion != "v9.9.9" {
		t.Fatalf("original daemon identity changed: %+v", got)
	}
	if got := read(New(cfg, logger)); got.CLICommit != strings.Repeat("b", 40) || got.CLIVersion != "v10.0.0" {
		t.Fatalf("new daemon did not use new build identity: %+v", got)
	}
}

func TestHealthHandlerReportsCLIVersionAndTaskCounts(t *testing.T) {
	t.Parallel()

	d := &Daemon{
		cfg: Config{
			CLIVersion:    "v9.9.9",
			DaemonID:      "daemon-test",
			DeviceName:    "dev",
			ServerBaseURL: "http://localhost:8080",
		},
		workspaces: map[string]*workspaceState{},
		logger:     slog.Default(),
	}
	d.activeTasks.Store(2)
	d.runningTasks.Store(1)
	d.resourceWaitTasks.Store(1)
	d.ready.Store(true) // preflight done -> status should be "running"

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	d.healthHandler(time.Now()).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	// Decode into a raw map so the test locks in the exact wire-level JSON
	// keys — the desktop TS client depends on snake_case (cli_version,
	// active_task_count), so a silent struct-tag rename must fail here. The
	// execution/wait split is additive: active_task_count keeps its ownership
	// semantics for old clients and restart barriers.
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw response: %v", err)
	}
	if got, want := raw["cli_version"], "v9.9.9"; got != want {
		t.Errorf("cli_version key: got %v, want %q", got, want)
	}
	// JSON numbers decode to float64 through map[string]any.
	if got, want := raw["active_task_count"], float64(2); got != want {
		t.Errorf("active_task_count key: got %v, want %v", got, want)
	}
	if got, want := raw["running_task_count"], float64(1); got != want {
		t.Errorf("running_task_count key: got %v, want %v", got, want)
	}
	if got, want := raw["resource_wait_task_count"], float64(1); got != want {
		t.Errorf("resource_wait_task_count key: got %v, want %v", got, want)
	}
	if got, want := raw["status"], "running"; got != want {
		t.Errorf("status key: got %v, want %q", got, want)
	}
	// The desktop relies on the `os` key (runtime.GOOS) to detect a daemon it
	// can't manage (e.g. Linux-in-WSL behind a Windows desktop). A rename or
	// drop would silently re-break #3916, so lock both the key and its value.
	if got, want := raw["os"], runtime.GOOS; got != want {
		t.Errorf("os key: got %v, want %q", got, want)
	}

	// Also round-trip into the typed struct as a separate check that the
	// field values match, independent of key naming.
	var resp HealthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode typed response: %v", err)
	}
	if resp.CLIVersion != "v9.9.9" {
		t.Errorf("CLIVersion: got %q, want %q", resp.CLIVersion, "v9.9.9")
	}
	if resp.ActiveTaskCount != 2 {
		t.Errorf("ActiveTaskCount: got %d, want 2", resp.ActiveTaskCount)
	}
	if resp.RunningTaskCount != 1 {
		t.Errorf("RunningTaskCount: got %d, want 1", resp.RunningTaskCount)
	}
	if resp.ResourceWaitTaskCount != 1 {
		t.Errorf("ResourceWaitTaskCount: got %d, want 1", resp.ResourceWaitTaskCount)
	}
}

func TestHealthHandlerReportsTerminalReportQueueCountsAndBytes(t *testing.T) {
	d := New(Config{
		WorkspacesRoot: t.TempDir(),
		ServerBaseURL:  "https://api.example.test",
		DaemonID:       "health-terminal-reports",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	d.ready.Store(true)
	report := terminalTaskReport{kind: terminalTaskReportComplete, taskID: "pending", output: "private output"}
	if err := d.terminalReports.enqueue(report); err != nil {
		t.Fatalf("enqueue pending report: %v", err)
	}
	if err := os.MkdirAll(d.terminalReports.failedDir(), 0o700); err != nil {
		t.Fatalf("create failed queue: %v", err)
	}
	if err := os.WriteFile(filepath.Join(d.terminalReports.failedDir(), "failed.json"), []byte("failed payload"), 0o600); err != nil {
		t.Fatalf("write failed report: %v", err)
	}

	rec := httptest.NewRecorder()
	d.healthHandler(time.Now()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	var resp HealthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode health response: %v", err)
	}
	if resp.PendingTerminalReportCount != 1 || resp.PendingTerminalReportBytes == 0 ||
		resp.FailedTerminalReportCount != 1 || resp.FailedTerminalReportBytes != int64(len("failed payload")) {
		t.Fatalf("terminal report health = pending:%d/%d failed:%d/%d",
			resp.PendingTerminalReportCount, resp.PendingTerminalReportBytes,
			resp.FailedTerminalReportCount, resp.FailedTerminalReportBytes)
	}
}

// TestHealthHandlerReportsDeferredReload covers the "while waiting to restart,
// the reason and state are visible" criterion. When trySelfReload has confirmed
// a multica version change but the daemon was busy at the barrier check, the
// only way a user can tell why the daemon is still on the old version is this
// field. It is omitempty, so an idle daemon must not emit the key at all.
func TestHealthHandlerReportsDeferredReload(t *testing.T) {
	t.Parallel()

	newHealthProbe := func(t *testing.T) (*Daemon, func() map[string]any) {
		t.Helper()
		d := &Daemon{
			cfg:        Config{CLIVersion: "0.3.7"},
			workspaces: map[string]*workspaceState{},
			logger:     slog.Default(),
		}
		d.ready.Store(true)
		return d, func() map[string]any {
			rec := httptest.NewRecorder()
			d.healthHandler(time.Now()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
			var raw map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
				t.Fatalf("decode raw response: %v", err)
			}
			return raw
		}
	}

	t.Run("absent when nothing pending", func(t *testing.T) {
		_, probe := newHealthProbe(t)
		if _, present := probe()["reload_pending_reason"]; present {
			t.Error("reload_pending_reason must be omitted when no restart is pending")
		}
	})

	t.Run("explains a deferred restart", func(t *testing.T) {
		d, probe := newHealthProbe(t)
		d.setReloadPending("multica binary on disk reports 0.3.8, running 0.3.7")

		got, _ := probe()["reload_pending_reason"].(string)
		if !strings.Contains(got, "0.3.8") {
			t.Errorf("reload_pending_reason = %q, want it to name the version on disk", got)
		}
	})
}

// TestHealthHandlerReportsStartingUntilReady pins the liveness/readiness split:
// the health server binds and answers before preflight finishes, but it must
// report "starting" until d.ready is set, and only then "running". Otherwise a
// slow or failing preflight would be misreported to `daemon start` (and the
// desktop) as a fully started daemon.
func TestHealthHandlerReportsStartingUntilReady(t *testing.T) {
	t.Parallel()

	d := &Daemon{
		cfg:        Config{CLIVersion: "v1.0.0"},
		workspaces: map[string]*workspaceState{},
		logger:     slog.Default(),
	}
	handler := d.healthHandler(time.Now())

	readStatus := func() string {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
		var resp HealthResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		return resp.Status
	}

	if got := readStatus(); got != "starting" {
		t.Fatalf("status before ready: got %q, want \"starting\"", got)
	}

	d.ready.Store(true)

	if got := readStatus(); got != "running" {
		t.Fatalf("status after ready: got %q, want \"running\"", got)
	}
}

func TestHealthHandlerActiveTaskCountTracksCounter(t *testing.T) {
	t.Parallel()

	d := &Daemon{
		cfg:        Config{CLIVersion: "v1.0.0"},
		workspaces: map[string]*workspaceState{},
		logger:     slog.Default(),
	}
	handler := d.healthHandler(time.Now())

	// Simulate the pollLoop increment/decrement protocol.
	d.activeTasks.Add(1)
	d.activeTasks.Add(1)
	assertActiveTaskCount(t, handler, 2)

	d.activeTasks.Add(-1)
	assertActiveTaskCount(t, handler, 1)

	d.activeTasks.Add(-1)
	assertActiveTaskCount(t, handler, 0)
}

func TestHealthHandlerReportsRepoCoordinationActivity(t *testing.T) {
	t.Parallel()

	cache := &activityRepoCache{
		activity: repocache.Activity{MaintenanceActive: 1, ForegroundWaiters: 3},
	}
	d := &Daemon{
		cfg:        Config{CLIVersion: "v1.0.0"},
		repoCache:  cache,
		workspaces: map[string]*workspaceState{},
		logger:     slog.Default(),
	}
	rec := httptest.NewRecorder()
	d.healthHandler(time.Now()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	var resp HealthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.RepoMaintenanceActive != 1 || resp.RepoCheckoutWaiters != 3 {
		t.Fatalf("repo activity = maintenance:%d waiters:%d, want 1/3", resp.RepoMaintenanceActive, resp.RepoCheckoutWaiters)
	}
}

func TestShutdownHandlerPostCancelsDaemonContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := &Daemon{cancelFunc: cancel}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/shutdown", nil)
	d.shutdownHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("daemon context was not cancelled after POST /shutdown")
	}
}

func TestShutdownHandlerRejectsNonPost(t *testing.T) {
	t.Parallel()

	cancelled := false
	d := &Daemon{cancelFunc: func() { cancelled = true }}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/shutdown", nil)
	d.shutdownHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
	// Give the handler's deferred cancel goroutine a moment to fire
	// in case a bug causes it to run anyway.
	time.Sleep(10 * time.Millisecond)
	if cancelled {
		t.Fatal("GET request should not trigger cancellation")
	}
}

func TestHealthHandlerRespondsWhileTaskRepoLookupWaits(t *testing.T) {
	const workspaceID = "ws-health"
	const repoURL = "https://github.com/org/repo.git"
	cache := newBlockingLookupRepoCache("/cache/org/repo.git")
	d := &Daemon{
		cfg: Config{CLIVersion: "v1.0.0"},
		workspaces: map[string]*workspaceState{
			workspaceID: {
				workspaceID:     workspaceID,
				runtimeIDs:      []string{"rt-1"},
				allowedRepoURLs: map[string]struct{}{repoURL: {}},
				taskRepoURLs:    map[string]struct{}{},
			},
		},
		repoCache: cache,
		logger:    slog.Default(),
	}
	defer cache.release()

	registerDone := make(chan struct{})
	go func() {
		d.registerTaskRepos(workspaceID, "task-health", []RepoData{{URL: repoURL}})
		close(registerDone)
	}()
	cache.waitForLookup(t)

	rec := httptest.NewRecorder()
	healthDone := make(chan struct{})
	go func() {
		d.healthHandler(time.Now()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
		close(healthDone)
	}()

	select {
	case <-healthDone:
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
	case <-time.After(time.Second):
		t.Fatal("/health blocked behind task repo cache lookup")
	}

	cache.release()
	select {
	case <-registerDone:
	case <-time.After(time.Second):
		t.Fatal("registerTaskRepos did not unblock after repo lookup finished")
	}
}

func TestRepoCheckoutUsesTaskScopedProjectRefByDefault(t *testing.T) {
	t.Parallel()

	const workspaceID = "ws-checkout"
	const repoURL = "https://github.com/org/repo.git"
	cache := &recordingRepoCache{lookupPath: "/cache/org/repo.git"}
	workDir := t.TempDir()
	d := newRepoCheckoutTestDaemon(t, workspaceID, repoURL, workDir, cache)
	d.registerTaskRepos(workspaceID, "task-1", []RepoData{{URL: repoURL, Ref: "release/v2"}})

	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"url":"` + repoURL + `","workspace_id":"` + workspaceID + `","workdir":"` + workDir + `","agent_name":"Other Agent","task_id":"task-1"}`)
	d.repoCheckoutHandler().ServeHTTP(rec, authorizedRepoCheckoutRequest(body))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := cache.lastCreateParams().Ref; got != "release/v2" {
		t.Fatalf("CreateWorktree Ref = %q, want release/v2", got)
	}
	if got := cache.lastCreateParams().AgentName; got != "Test Agent" {
		t.Fatalf("CreateWorktree AgentName = %q, want token-bound active agent", got)
	}
}

// A request with no Authorization header can only come from a CLI older than
// repoCheckoutMinCLIVersion, which is a permanent failure. The rejection has to
// say so: the agent sees this string and nothing else (#7520).
func TestRepoCheckoutRejectsMissingTaskCredential(t *testing.T) {
	t.Parallel()

	const workspaceID = "ws-checkout"
	const repoURL = "https://github.com/org/repo.git"
	cache := &recordingRepoCache{lookupPath: "/cache/org/repo.git"}
	workDir := t.TempDir()
	d := newRepoCheckoutTestDaemon(t, workspaceID, repoURL, workDir, cache)
	var logs bytes.Buffer
	d.logger = captureLogger(&logs)

	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"url":"` + repoURL + `","workspace_id":"` + workspaceID + `","workdir":"` + workDir + `","task_id":"task-1"}`)
	d.repoCheckoutHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/repo/checkout", body))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := cache.lastCreateParams(); got != (repocache.WorktreeParams{}) {
		t.Fatalf("unauthorized checkout reached repo cache: %+v", got)
	}
	for _, want := range []string{
		repoCheckoutMinCLIVersion,
		repoCheckoutListBinariesCommand(),
		"multica update",
		"v1.0.0",
	} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("rejection body missing %q: %s", want, rec.Body.String())
		}
	}
	// The endpoint serves Windows and Linux too, so the self-help commands must
	// not be a macOS/Homebrew recipe. `multica update` resolves the install
	// method itself; a literal `brew upgrade` is unrunnable for most readers.
	if strings.Contains(rec.Body.String(), "brew upgrade") {
		t.Fatalf("rejection body hardcodes a platform-specific upgrade command: %s", rec.Body.String())
	}
	// The silent 401 branch is what made this undiagnosable from daemon.log.
	if !strings.Contains(logs.String(), "repo checkout rejected") || !strings.Contains(logs.String(), "no_credential") {
		t.Fatalf("expected a log line naming the reason, got: %s", logs.String())
	}
	// Asserted explicitly so a later downgrade to Debug — filtered out of
	// daemon.log by default — cannot silently pass this test.
	if !strings.Contains(logs.String(), "level=WARN") {
		t.Fatalf("rejection must be logged at WARN, got: %s", logs.String())
	}
}

// A token that no active task owns is a different failure with a different fix,
// so it must not be reported as (or advised like) a stale-CLI problem.
func TestRepoCheckoutRejectsUnknownTaskCredential(t *testing.T) {
	t.Parallel()

	const workspaceID = "ws-checkout"
	const repoURL = "https://github.com/org/repo.git"
	cache := &recordingRepoCache{lookupPath: "/cache/org/repo.git"}
	workDir := t.TempDir()
	d := newRepoCheckoutTestDaemon(t, workspaceID, repoURL, workDir, cache)
	var logs bytes.Buffer
	d.logger = captureLogger(&logs)

	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"url":"` + repoURL + `","workspace_id":"` + workspaceID + `","workdir":"` + workDir + `","task_id":"task-1"}`)
	req := httptest.NewRequest(http.MethodPost, "/repo/checkout", body)
	req.Header.Set("Authorization", "Bearer mat_not_an_active_task")
	d.repoCheckoutHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := cache.lastCreateParams(); got != (repocache.WorktreeParams{}) {
		t.Fatalf("unauthorized checkout reached repo cache: %+v", got)
	}
	if !strings.Contains(rec.Body.String(), "not bound to a task running in this daemon") {
		t.Fatalf("rejection body should explain the task is gone: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "multica update") {
		t.Fatalf("a live CLI must not be told to upgrade: %s", rec.Body.String())
	}
	if !strings.Contains(logs.String(), "unknown_credential") {
		t.Fatalf("expected a WARN naming the reason, got: %s", logs.String())
	}
	// The token is a live task credential and must never reach the log.
	if strings.Contains(logs.String(), "mat_not_an_active_task") {
		t.Fatalf("task credential leaked into daemon.log: %s", logs.String())
	}
}

// The daemon can name where it started from, which narrows the search — but it
// has not probed that file's version, and the daemon explicitly supports the
// binary being replaced out of band. So the message must describe provenance
// and ask the reader to verify, never assert a match.
func TestRepoCheckoutAuthErrorNamesDaemonBinaryWithoutClaimingAMatch(t *testing.T) {
	original := resolveSelfExecutable
	t.Cleanup(func() { resolveSelfExecutable = original })
	resolveSelfExecutable = func() (string, error) { return "/opt/multica/bin/multica", nil }

	d := &Daemon{cfg: Config{CLIVersion: "v0.4.33"}}
	message := d.repoCheckoutAuthErrorMessage(repoCheckoutAuthNoCredential)
	if !strings.Contains(message, "/opt/multica/bin/multica") {
		t.Fatalf("rejection should name the daemon's own binary: %s", message)
	}
	if !strings.Contains(message, "check that copy's version") {
		t.Fatalf("rejection should ask the reader to verify the version: %s", message)
	}
	for _, unwanted := range []string{"version-matched", "matches this daemon"} {
		if strings.Contains(message, unwanted) {
			t.Fatalf("rejection must not assert an unverified version match (%q): %s", unwanted, message)
		}
	}

	resolveSelfExecutable = func() (string, error) { return "", errors.New("unresolvable") }
	if message := d.repoCheckoutAuthErrorMessage(repoCheckoutAuthNoCredential); !strings.Contains(message, repoCheckoutMinCLIVersion) {
		t.Fatalf("rejection must stay useful when the daemon path is unknown: %s", message)
	}
}

// When the daemon already knows its on-disk copy drifted (a reload deferred
// because tasks were running), staying silent would point a version-skew victim
// at a second stale binary.
func TestRepoCheckoutAuthErrorSurfacesDeferredReload(t *testing.T) {
	original := resolveSelfExecutable
	t.Cleanup(func() { resolveSelfExecutable = original })
	resolveSelfExecutable = func() (string, error) { return "/opt/multica/bin/multica", nil }

	d := &Daemon{cfg: Config{CLIVersion: "v0.4.33"}}
	d.setReloadPending("multica binary on disk reports v0.4.20, running v0.4.33")

	message := d.repoCheckoutAuthErrorMessage(repoCheckoutAuthNoCredential)
	if !strings.Contains(message, "on-disk copy has since changed") {
		t.Fatalf("rejection should warn that the daemon's own binary drifted: %s", message)
	}
	if !strings.Contains(message, "reports v0.4.20, running v0.4.33") {
		t.Fatalf("rejection should carry the drift detail the daemon already has: %s", message)
	}
}

func TestRepoCheckoutRejectsAnotherTaskWorkdir(t *testing.T) {
	t.Parallel()

	const workspaceID = "ws-checkout"
	const repoURL = "https://github.com/org/repo.git"
	cache := &recordingRepoCache{lookupPath: "/cache/org/repo.git"}
	workDir := t.TempDir()
	d := newRepoCheckoutTestDaemon(t, workspaceID, repoURL, workDir, cache)
	var logs bytes.Buffer
	d.logger = captureLogger(&logs)
	otherWorkDir := t.TempDir()

	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"url":"` + repoURL + `","workspace_id":"` + workspaceID + `","workdir":"` + otherWorkDir + `","task_id":"task-1"}`)
	d.repoCheckoutHandler().ServeHTTP(rec, authorizedRepoCheckoutRequest(body))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := cache.lastCreateParams(); got != (repocache.WorktreeParams{}) {
		t.Fatalf("cross-task workdir checkout reached repo cache: %+v", got)
	}
	// #8946: a bare "not owned" with nothing in daemon.log hid that the real
	// failure was resolving a junctioned path. The agent and the log both get
	// the reason now.
	if !strings.Contains(rec.Body.String(), "outside the active task workdir") {
		t.Fatalf("rejection body should say why the workdir was refused: %s", rec.Body.String())
	}
	for _, want := range []string{"repo checkout rejected", "workdir_not_owned", "level=WARN"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("rejection log missing %q, got: %s", want, logs.String())
		}
	}
}

// A workdir that cannot be resolved at all is refused, and the refusal names
// the resolution failure instead of reading as an ownership question.
func TestRepoCheckoutRejectsUnresolvableWorkdirWithTheReason(t *testing.T) {
	t.Parallel()

	const workspaceID = "ws-checkout"
	const repoURL = "https://github.com/org/repo.git"
	cache := &recordingRepoCache{lookupPath: "/cache/org/repo.git"}
	workDir := t.TempDir()
	d := newRepoCheckoutTestDaemon(t, workspaceID, repoURL, workDir, cache)
	missing := filepath.Join(workDir, "does-not-exist")

	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"url":"` + repoURL + `","workspace_id":"` + workspaceID + `","workdir":"` + missing + `","task_id":"task-1"}`)
	d.repoCheckoutHandler().ServeHTTP(rec, authorizedRepoCheckoutRequest(body))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := cache.lastCreateParams(); got != (repocache.WorktreeParams{}) {
		t.Fatalf("unresolvable workdir checkout reached repo cache: %+v", got)
	}
	if !strings.Contains(rec.Body.String(), "resolve requested workdir") {
		t.Fatalf("rejection body should name the resolution failure: %s", rec.Body.String())
	}
}

func TestRepoCheckoutExplicitRefOverridesProjectDefault(t *testing.T) {
	t.Parallel()

	const workspaceID = "ws-checkout"
	const repoURL = "https://github.com/org/repo.git"
	cache := &recordingRepoCache{lookupPath: "/cache/org/repo.git"}
	workDir := t.TempDir()
	d := newRepoCheckoutTestDaemon(t, workspaceID, repoURL, workDir, cache)
	d.registerTaskRepos(workspaceID, "task-1", []RepoData{{URL: repoURL, Ref: "release/v2"}})

	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"url":"` + repoURL + `","workspace_id":"` + workspaceID + `","workdir":"` + workDir + `","task_id":"task-1","ref":"hotfix"}`)
	d.repoCheckoutHandler().ServeHTTP(rec, authorizedRepoCheckoutRequest(body))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := cache.lastCreateParams().Ref; got != "hotfix" {
		t.Fatalf("CreateWorktree Ref = %q, want explicit hotfix", got)
	}
}

func TestRepoCheckoutForwardsIsolatedMode(t *testing.T) {
	t.Parallel()

	const workspaceID = "ws-checkout"
	const repoURL = "https://github.com/org/repo.git"
	cache := &recordingRepoCache{lookupPath: "/cache/org/repo.git"}
	workDir := t.TempDir()
	d := newRepoCheckoutTestDaemon(t, workspaceID, repoURL, workDir, cache)

	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"url":"` + repoURL + `","workspace_id":"` + workspaceID + `","workdir":"` + workDir + `","task_id":"task-1","checkout_mode":"isolated"}`)
	d.repoCheckoutHandler().ServeHTTP(rec, authorizedRepoCheckoutRequest(body))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !cache.lastCreateParams().IsolatedGitMetadata {
		t.Fatal("isolated checkout_mode was not forwarded to repo cache")
	}
}

func TestRepoCheckoutForwardsFresh(t *testing.T) {
	t.Parallel()

	const workspaceID = "ws-checkout"
	const repoURL = "https://github.com/org/repo.git"
	cache := &recordingRepoCache{lookupPath: "/cache/org/repo.git"}
	workDir := t.TempDir()
	d := newRepoCheckoutTestDaemon(t, workspaceID, repoURL, workDir, cache)

	for _, tc := range []struct {
		body string
		want bool
	}{
		{body: `{"url":"` + repoURL + `","workspace_id":"` + workspaceID + `","workdir":"` + workDir + `","task_id":"task-1"}`, want: false},
		{body: `{"url":"` + repoURL + `","workspace_id":"` + workspaceID + `","workdir":"` + workDir + `","task_id":"task-1","fresh":true}`, want: true},
	} {
		rec := httptest.NewRecorder()
		d.repoCheckoutHandler().ServeHTTP(rec, authorizedRepoCheckoutRequest(strings.NewReader(tc.body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
		if got := cache.lastCreateParams().Fresh; got != tc.want {
			t.Fatalf("CreateWorktree Fresh = %v, want %v for %s", got, tc.want, tc.body)
		}
	}
}

func TestRepoCheckoutRejectsUnknownMode(t *testing.T) {
	t.Parallel()

	const workspaceID = "ws-checkout"
	const repoURL = "https://github.com/org/repo.git"
	cache := &recordingRepoCache{lookupPath: "/cache/org/repo.git"}
	workDir := t.TempDir()
	d := newRepoCheckoutTestDaemon(t, workspaceID, repoURL, workDir, cache)

	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"url":"` + repoURL + `","workspace_id":"` + workspaceID + `","workdir":"` + workDir + `","task_id":"task-1","checkout_mode":"unsafe"}`)
	d.repoCheckoutHandler().ServeHTTP(rec, authorizedRepoCheckoutRequest(body))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := cache.lastCreateParams(); got != (repocache.WorktreeParams{}) {
		t.Fatalf("invalid checkout mode reached repo cache: %+v", got)
	}
}

func TestRepoCheckoutReturnsRetryableBusyToCapableClient(t *testing.T) {
	t.Parallel()

	const workspaceID = "ws-checkout"
	const repoURL = "https://github.com/org/repo.git"
	cache := &busyRepoCache{recordingRepoCache: recordingRepoCache{lookupPath: "/cache/org/repo.git"}}
	workDir := t.TempDir()
	d := newRepoCheckoutTestDaemon(t, workspaceID, repoURL, workDir, cache)

	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"url":"` + repoURL + `","workspace_id":"` + workspaceID + `","workdir":"` + workDir + `","task_id":"task-1","retry_busy":true}`)
	d.repoCheckoutHandler().ServeHTTP(rec, authorizedRepoCheckoutRequest(body))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Retry-After"); got != "2" {
		t.Fatalf("Retry-After = %q, want 2", got)
	}
	if got := rec.Header().Get(repoCheckoutRetryHeader); got != repoCheckoutRetryValueBusy {
		t.Fatalf("%s = %q, want %q", repoCheckoutRetryHeader, got, repoCheckoutRetryValueBusy)
	}
	if got := cache.lastCreateParams().LockWaitTimeout; got != repoCheckoutLockWaitTimeout {
		t.Fatalf("lock wait timeout = %s, want %s", got, repoCheckoutLockWaitTimeout)
	}
}

func newRepoCheckoutTestDaemon(t *testing.T, workspaceID, repoURL, workDir string, cache repoCacheBackend) *Daemon {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/daemon/workspaces/"+workspaceID+"/repos" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(WorkspaceReposResponse{
			WorkspaceID:  workspaceID,
			Repos:        []RepoData{{URL: repoURL}},
			ReposVersion: "v1",
		})
	}))
	t.Cleanup(srv.Close)
	d := &Daemon{
		cfg:       Config{CLIVersion: "v1.0.0"},
		client:    NewClient(srv.URL),
		repoCache: cache,
		workspaces: map[string]*workspaceState{
			workspaceID: newWorkspaceState(workspaceID, nil, "", []RepoData{{URL: repoURL}}, nil),
		},
		logger: slog.Default(),
	}
	d.registerActiveRepoCheckoutTask("mat_repo_checkout_test", activeRepoCheckoutTask{
		WorkspaceID: workspaceID,
		TaskID:      "task-1",
		AgentID:     "agent-1",
		AgentName:   "Test Agent",
		WorkDir:     workDir,
	})
	return d
}

func authorizedRepoCheckoutRequest(body io.Reader) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/repo/checkout", body)
	req.Header.Set("Authorization", "Bearer mat_repo_checkout_test")
	return req
}

type busyRepoCache struct {
	recordingRepoCache
}

type activityRepoCache struct {
	recordingRepoCache
	activity repocache.Activity
}

func (c *activityRepoCache) Activity() repocache.Activity { return c.activity }

func (c *busyRepoCache) CreateWorktreeContext(_ context.Context, params repocache.WorktreeParams) (*repocache.WorktreeResult, error) {
	c.mu.Lock()
	c.params = append(c.params, params)
	c.mu.Unlock()
	return nil, repocache.ErrRepoBusy
}

type blockingLookupRepoCache struct {
	path          string
	lookupSeen    chan struct{}
	releaseLookup chan struct{}
	releaseOnce   sync.Once
}

func newBlockingLookupRepoCache(path string) *blockingLookupRepoCache {
	return &blockingLookupRepoCache{
		path:          path,
		lookupSeen:    make(chan struct{}),
		releaseLookup: make(chan struct{}),
	}
}

func (c *blockingLookupRepoCache) BarePath(_, _ string) string {
	return ""
}

func (c *blockingLookupRepoCache) Lookup(_, _ string) string {
	select {
	case <-c.lookupSeen:
	default:
		close(c.lookupSeen)
	}
	<-c.releaseLookup
	return c.path
}

func (c *blockingLookupRepoCache) Sync(string, []repocache.RepoInfo) error {
	return nil
}

func (c *blockingLookupRepoCache) WithRepoLock(_ string, fn func() error) error {
	return fn()
}

func (c *blockingLookupRepoCache) CreateWorktree(repocache.WorktreeParams) (*repocache.WorktreeResult, error) {
	return nil, nil
}

type recordingRepoCache struct {
	lookupPath string
	mu         sync.Mutex
	params     []repocache.WorktreeParams
}

func (c *recordingRepoCache) Lookup(_, _ string) string {
	return c.lookupPath
}

func (c *recordingRepoCache) BarePath(_, _ string) string {
	return c.lookupPath
}

func (c *recordingRepoCache) Sync(string, []repocache.RepoInfo) error {
	return nil
}

func (c *recordingRepoCache) WithRepoLock(_ string, fn func() error) error {
	return fn()
}

func (c *recordingRepoCache) CreateWorktree(params repocache.WorktreeParams) (*repocache.WorktreeResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.params = append(c.params, params)
	return &repocache.WorktreeResult{Path: params.WorkDir, BranchName: "agent/test"}, nil
}

func (c *recordingRepoCache) lastCreateParams() repocache.WorktreeParams {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.params) == 0 {
		return repocache.WorktreeParams{}
	}
	return c.params[len(c.params)-1]
}

func (c *blockingLookupRepoCache) waitForLookup(t *testing.T) {
	t.Helper()
	select {
	case <-c.lookupSeen:
	case <-time.After(time.Second):
		t.Fatal("registerTaskRepos did not call repo lookup")
	}
}

func (c *blockingLookupRepoCache) release() {
	c.releaseOnce.Do(func() {
		close(c.releaseLookup)
	})
}

func assertActiveTaskCount(t *testing.T, h http.HandlerFunc, want int64) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	var resp HealthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ActiveTaskCount != want {
		t.Errorf("active_task_count: got %d, want %d", resp.ActiveTaskCount, want)
	}
}

// The health port is a hash of the profile name, so distinct names collide and
// a caller cannot otherwise tell whose daemon answered. These pin the wire
// contract the CLI's collision check depends on (#6694).
func TestHealthHandlerReportsProfileIdentity(t *testing.T) {
	t.Parallel()

	rawHealth := func(t *testing.T, cfg Config) map[string]any {
		t.Helper()
		d := &Daemon{cfg: cfg, workspaces: map[string]*workspaceState{}, logger: slog.Default()}
		d.ready.Store(true)

		rec := httptest.NewRecorder()
		d.healthHandler(time.Now()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		var raw map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
			t.Fatalf("decode raw response: %v", err)
		}
		return raw
	}

	t.Run("named profile reports its name", func(t *testing.T) {
		t.Parallel()
		raw := rawHealth(t, Config{Profile: "desktop-api.multica.ai", LaunchedBy: "desktop"})
		if got, want := raw["profile"], "desktop-api.multica.ai"; got != want {
			t.Errorf("profile key: got %v, want %q", got, want)
		}
		if got, want := raw["launched_by"], "desktop"; got != want {
			t.Errorf("launched_by key: got %v, want %q", got, want)
		}
	})

	// The empty string is the default profile identifying itself. It has to
	// stay on the wire: a caller distinguishes "I am the default daemon" from
	// "I am too old to say" by whether the key is present at all, so omitempty
	// here would make every default daemon look unidentifiable.
	t.Run("default profile still emits the key", func(t *testing.T) {
		t.Parallel()
		raw := rawHealth(t, Config{Profile: ""})
		got, ok := raw["profile"]
		if !ok {
			t.Fatal("profile key missing for the default profile; it must be present and empty")
		}
		if got != "" {
			t.Errorf("profile key: got %v, want the empty string", got)
		}
	})

	// launched_by is display-only, so absence and empty mean the same thing
	// and omitempty keeps a standalone daemon's payload unchanged.
	t.Run("standalone daemon omits launched_by", func(t *testing.T) {
		t.Parallel()
		raw := rawHealth(t, Config{Profile: "dev"})
		if _, ok := raw["launched_by"]; ok {
			t.Errorf("launched_by should be omitted for a standalone daemon, got %v", raw["launched_by"])
		}
	})
}

// managedHealthSecretToken is a distinctive daemon credential the managed
// health tests install so any accidental exposure is unambiguous.
const managedHealthSecretToken = "mdt_lifecycle_secret_value_do_not_leak"

// newManagedHealthDaemon builds a managed daemon with one installed workspace
// and runtime, mirroring what bootstrapManaged leaves behind: the enrollment is
// validated, the mdt_ credential is live, and no heartbeat has been
// acknowledged yet. Enrollment acknowledgement is left to the test so it can
// assert the transition.
func newManagedHealthDaemon(t *testing.T) *Daemon {
	t.Helper()
	d := newManagedTestDaemon(t)
	d.cfg.Managed = ManagedConfig{
		Enabled:             true,
		EnrollmentTokenFile: "/run/secrets/aurora-enrollment",
	}
	d.cfg.DaemonID = testManagedDaemonID
	d.client = NewClient("https://api.example.test")
	d.client.SetToken(managedHealthSecretToken)

	resp := validManagedEnrollmentResponse()
	resp.DaemonToken = managedHealthSecretToken
	if err := d.installManagedEnrollment(resp); err != nil {
		t.Fatalf("installManagedEnrollment() = %v, want nil", err)
	}
	return d
}

// managedHealthStatus reads /health and returns the readiness status plus the
// decoded response, so a test can assert both the transition and the payload.
func managedHealthStatus(t *testing.T, d *Daemon) (string, HealthResponse, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	d.healthHandler(time.Now()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	var resp HealthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp.Status, resp, body
}

// TestManagedHealthStartsUnreadyUntilEnrollmentAndHeartbeat pins the managed
// readiness gate: a managed daemon is not ready merely because its enrollment
// was validated. It becomes ready only after a heartbeat is acknowledged, and
// it falls back to unready on repeated authorization failures and on context
// cancellation.
func TestManagedHealthStartsUnreadyUntilEnrollmentAndHeartbeat(t *testing.T) {
	t.Parallel()

	t.Run("enrollment alone is not ready", func(t *testing.T) {
		t.Parallel()
		d := newManagedHealthDaemon(t)

		if status, _, _ := managedHealthStatus(t, d); status != "starting" {
			t.Fatalf("status before the enrollment is acknowledged = %q, want starting", status)
		}
		d.markManagedEnrolled()
		if status, _, _ := managedHealthStatus(t, d); status != "starting" {
			t.Fatalf("status after enrollment but before a heartbeat = %q, want starting", status)
		}
		d.markManagedHeartbeatAcknowledged(time.Now())
		if status, _, _ := managedHealthStatus(t, d); status != "running" {
			t.Fatalf("status after an acknowledged heartbeat = %q, want running", status)
		}
	})

	t.Run("repeated heartbeat authorization failure unready", func(t *testing.T) {
		t.Parallel()
		d := newManagedHealthDaemon(t)
		d.markManagedEnrolled()
		d.markManagedHeartbeatAcknowledged(time.Now())

		for i := 1; i < managedHeartbeatAuthFailureLimit; i++ {
			d.recordManagedHeartbeatAuthFailure()
			if status, _, _ := managedHealthStatus(t, d); status != "running" {
				t.Fatalf("status after %d authorization failure(s) = %q, want running", i, status)
			}
		}
		d.recordManagedHeartbeatAuthFailure()
		if status, _, _ := managedHealthStatus(t, d); status != "starting" {
			t.Fatalf("status after %d authorization failures = %q, want starting", managedHeartbeatAuthFailureLimit, status)
		}
	})

	t.Run("context cancellation unready", func(t *testing.T) {
		t.Parallel()
		d := newManagedHealthDaemon(t)
		d.markManagedEnrolled()
		d.markManagedHeartbeatAcknowledged(time.Now())
		if status, _, _ := managedHealthStatus(t, d); status != "running" {
			t.Fatalf("status before cancellation = %q, want running", status)
		}
		d.markManagedUnready("daemon context cancelled")
		if status, _, _ := managedHealthStatus(t, d); status != "starting" {
			t.Fatalf("status after cancellation = %q, want starting", status)
		}
	})
}

// TestManagedHealthReportsOneRuntimeAndNoCredential pins the managed /health
// payload: it names the single enrolled runtime and its heartbeat, and it never
// carries the daemon credential, the enrollment secret-file path, a prompt, a
// provider URL, or a task token.
func TestManagedHealthReportsOneRuntimeAndNoCredential(t *testing.T) {
	t.Parallel()

	d := newManagedHealthDaemon(t)
	d.markManagedEnrolled()
	heartbeatAt := time.Now().UTC().Truncate(time.Microsecond)
	d.markManagedHeartbeatAcknowledged(heartbeatAt)

	status, resp, body := managedHealthStatus(t, d)
	if status != "running" {
		t.Fatalf("status = %q, want running", status)
	}
	if len(resp.Workspaces) != 1 {
		t.Fatalf("workspaces = %d, want exactly the one enrolled workspace", len(resp.Workspaces))
	}
	workspace := resp.Workspaces[0]
	if workspace.ID != testManagedWorkspaceID {
		t.Errorf("workspace id = %q, want %q", workspace.ID, testManagedWorkspaceID)
	}
	if len(workspace.Runtimes) != 1 || workspace.Runtimes[0] != testManagedRuntimeID {
		t.Fatalf("workspace runtimes = %v, want only [%s]", workspace.Runtimes, testManagedRuntimeID)
	}
	if resp.Managed == nil {
		t.Fatal("managed health block is absent for a managed daemon")
	}
	if !resp.Managed.Ready {
		t.Error("managed health reports not ready after enrollment and a heartbeat")
	}
	if resp.Managed.LastHeartbeatAt == nil || !resp.Managed.LastHeartbeatAt.Equal(heartbeatAt) {
		t.Fatalf("last heartbeat = %v, want %v", resp.Managed.LastHeartbeatAt, heartbeatAt)
	}

	// The absence assertions are explicit: a future field must not be able to
	// smuggle a credential into the readiness payload.
	for _, secret := range []string{
		managedHealthSecretToken,
		"mdt_",
		"mse_",
		"aurora-enrollment",
		"api.anthropic.com",
		"ark.cn-beijing.volces.com",
		"sk-",
	} {
		if strings.Contains(body, secret) {
			t.Errorf("health body leaks %q: %s", secret, body)
		}
	}
}

func TestHealthReportStatsErrorPreservesLivenessAndLegacyCounters(t *testing.T) {
	root := t.TempDir()
	store := newTerminalReportStore(Config{WorkspacesRoot: root, DaemonID: "unit"})
	if err := os.MkdirAll(store.dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.dir, "report"), []byte("abc"), 0600); err != nil {
		t.Fatal(err)
	}
	// A non-directory namespace's failed path is unreadable, while the legacy namespace remains valid.
	if err := os.MkdirAll(filepath.Join(store.root, "foreign"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.root, "foreign", "failed"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: Config{WorkspacesRoot: root}, workspaces: map[string]*workspaceState{}, logger: slog.Default(), terminalReports: store}
	d.ready.Store(true)
	d.activeTasks.Store(2)
	d.runningTasks.Store(1)
	d.resourceWaitTasks.Store(1)
	rec := httptest.NewRecorder()
	d.healthHandler(time.Now()).ServeHTTP(rec, httptest.NewRequest("GET", "/health", nil))
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	var stats struct {
		Known           bool
		Pending, Failed int
	}
	if len(raw["report_queue_stats"]) == 0 {
		t.Fatal("missing additive report_queue_stats")
	}
	if err := json.Unmarshal(raw["report_queue_stats"], &stats); err != nil {
		t.Fatal(err)
	}
	if stats.Known {
		t.Fatal("scan error must be unknown")
	}
	var response HealthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || response.Status != "running" || response.ActiveTaskCount != 2 || response.RunningTaskCount != 1 || response.ResourceWaitTaskCount != 1 || response.PendingTerminalReportCount != 1 || response.PendingTerminalReportBytes != 3 {
		t.Fatalf("legacy health changed: %+v", response)
	}
}
