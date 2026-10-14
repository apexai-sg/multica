package daemon

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

func TestResolveAgentExecutablePath_PreservesDispatchShimName(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires elevated privileges on Windows")
	}

	for _, shimName := range []string{"volta-shim", "vp"} {
		t.Run(shimName, func(t *testing.T) {
			managerDir := t.TempDir()
			manager := filepath.Join(managerDir, shimName)
			if err := os.WriteFile(manager, []byte("#!/bin/sh\nprintf '%s\\n' \"${0##*/}\"\n"), 0o755); err != nil {
				t.Fatalf("write dispatcher: %v", err)
			}

			binDir := t.TempDir()
			entrypoint := filepath.Join(binDir, "claude")
			if err := os.Symlink(manager, entrypoint); err != nil {
				t.Fatalf("symlink dispatcher: %v", err)
			}
			t.Setenv("PATH", binDir)

			got, err := resolveAgentExecutablePath("claude")
			if err != nil {
				t.Fatalf("resolveAgentExecutablePath: %v", err)
			}
			realBinDir, err := filepath.EvalSymlinks(binDir)
			if err != nil {
				t.Fatalf("resolve bin directory: %v", err)
			}
			want := filepath.Join(realBinDir, "claude")
			if got != want {
				t.Fatalf("resolved path = %q, want command-preserving entrypoint %q", got, want)
			}
			output, err := exec.Command(got, "--version").CombinedOutput()
			if err != nil {
				t.Fatalf("run resolved entrypoint: %v: %s", err, output)
			}
			if got := strings.TrimSpace(string(output)); got != "claude" {
				t.Fatalf("dispatcher observed command name %q, want claude", got)
			}
		})
	}
}

func TestResolveAgentExecutablePath_CanonicalizesOrdinaryVersionTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires elevated privileges on Windows")
	}

	versionDir := t.TempDir()
	target := filepath.Join(versionDir, "claude-2.1.216")
	if err := os.WriteFile(target, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write versioned executable: %v", err)
	}

	binDir := t.TempDir()
	entrypoint := filepath.Join(binDir, "claude")
	if err := os.Symlink(target, entrypoint); err != nil {
		t.Fatalf("symlink versioned executable: %v", err)
	}
	t.Setenv("PATH", binDir)

	got, err := resolveAgentExecutablePath("claude")
	if err != nil {
		t.Fatalf("resolveAgentExecutablePath: %v", err)
	}
	want, err := filepath.EvalSymlinks(entrypoint)
	if err != nil {
		t.Fatalf("resolve versioned executable: %v", err)
	}
	if got != want {
		t.Fatalf("resolved path = %q, want pinned version target %q", got, want)
	}
}

func TestPatternsFromEnv_DefaultsWhenUnset(t *testing.T) {
	t.Setenv("MULTICA_GC_ARTIFACT_PATTERNS", "")
	defaults := []string{"node_modules", ".next", ".turbo"}
	got := patternsFromEnv("MULTICA_GC_ARTIFACT_PATTERNS", defaults)
	if !reflect.DeepEqual(got, defaults) {
		t.Fatalf("expected defaults %v, got %v", defaults, got)
	}
	// Ensure callers get a copy, not a shared backing array.
	got[0] = "mutated"
	if defaults[0] == "mutated" {
		t.Fatal("patternsFromEnv must not return a slice aliased with defaults")
	}
}

// A localhost server URL is not the official cloud host, so this exercises the
// self-host branch of defaultGCCompletedTaskTTL: retention stays unbounded until
// an operator opts in, and a daemon upgrade never starts deleting on its own.
func TestLoadConfig_CompletedTaskTTLDefaultsDisabledOnSelfHostAndReadsEnv(t *testing.T) {
	stageFakeAgent(t)
	redirectTestHome(t, t.TempDir())
	t.Setenv("SHELL", filepath.Join(t.TempDir(), "missing-shell"))
	t.Setenv("MULTICA_GC_COMPLETED_TASK_TTL", "")

	overrides := Overrides{
		ServerURL:      "http://localhost:0",
		WorkspacesRoot: t.TempDir(),
	}
	cfg, err := LoadConfig(overrides)
	if err != nil {
		t.Fatalf("LoadConfig with default completed-task TTL: %v", err)
	}
	if cfg.GCCompletedTaskTTL != 0 {
		t.Fatalf("GCCompletedTaskTTL = %s, want disabled", cfg.GCCompletedTaskTTL)
	}

	t.Setenv("MULTICA_GC_COMPLETED_TASK_TTL", "36h")
	cfg, err = LoadConfig(overrides)
	if err != nil {
		t.Fatalf("LoadConfig with completed-task TTL: %v", err)
	}
	if cfg.GCCompletedTaskTTL != 36*time.Hour {
		t.Fatalf("GCCompletedTaskTTL = %s, want 36h", cfg.GCCompletedTaskTTL)
	}

	t.Setenv("MULTICA_GC_COMPLETED_TASK_TTL", "not-a-duration")
	if _, err := LoadConfig(overrides); err == nil || !strings.Contains(err.Error(), "MULTICA_GC_COMPLETED_TASK_TTL") {
		t.Fatalf("LoadConfig invalid completed-task TTL error = %v, want named validation error", err)
	}
}

func TestLoadConfig_WSClaimPollIntervalPrecedence(t *testing.T) {
	stageFakeAgent(t)
	redirectTestHome(t, t.TempDir())
	t.Setenv("SHELL", filepath.Join(t.TempDir(), "missing-shell"))
	t.Setenv("MULTICA_DAEMON_WS_CLAIM_POLL_INTERVAL", "")
	base := Overrides{ServerURL: "http://localhost:0", WorkspacesRoot: t.TempDir()}

	cfg, err := LoadConfig(base)
	if err != nil {
		t.Fatalf("LoadConfig default: %v", err)
	}
	if cfg.WSClaimPollInterval != DefaultWSClaimPollInterval {
		t.Fatalf("default WSClaimPollInterval = %s, want %s", cfg.WSClaimPollInterval, DefaultWSClaimPollInterval)
	}

	t.Setenv("MULTICA_DAEMON_WS_CLAIM_POLL_INTERVAL", "75s")
	cfg, err = LoadConfig(base)
	if err != nil {
		t.Fatalf("LoadConfig env: %v", err)
	}
	if cfg.WSClaimPollInterval != 75*time.Second {
		t.Fatalf("env WSClaimPollInterval = %s, want 75s", cfg.WSClaimPollInterval)
	}

	base.WSClaimPollInterval = 2 * time.Minute
	cfg, err = LoadConfig(base)
	if err != nil {
		t.Fatalf("LoadConfig override: %v", err)
	}
	if cfg.WSClaimPollInterval != 2*time.Minute {
		t.Fatalf("override WSClaimPollInterval = %s, want 2m", cfg.WSClaimPollInterval)
	}

	base.WSClaimPollInterval = 0
	t.Setenv("MULTICA_DAEMON_WS_CLAIM_POLL_INTERVAL", "0s")
	if _, err := LoadConfig(base); err == nil || !strings.Contains(err.Error(), "must be positive") {
		t.Fatalf("LoadConfig zero WS claim poll error = %v, want positive-duration validation", err)
	}
}

func TestLoadConfig_CompletedTaskTTLDefaultsBoundedOnOfficialCloud(t *testing.T) {
	stageFakeAgent(t)
	redirectTestHome(t, t.TempDir())
	t.Setenv("SHELL", filepath.Join(t.TempDir(), "missing-shell"))
	t.Setenv("MULTICA_GC_COMPLETED_TASK_TTL", "")

	overrides := Overrides{
		ServerURL:      "https://" + officialCloudHost,
		WorkspacesRoot: t.TempDir(),
	}
	cfg, err := LoadConfig(overrides)
	if err != nil {
		t.Fatalf("LoadConfig on official cloud: %v", err)
	}
	if cfg.GCCompletedTaskTTL != 14*24*time.Hour {
		t.Fatalf("GCCompletedTaskTTL = %s, want 14d on official cloud", cfg.GCCompletedTaskTTL)
	}

	// An explicit 0 has to win on cloud too — otherwise the only way back to the
	// previous retention behavior would be downgrading the daemon.
	t.Setenv("MULTICA_GC_COMPLETED_TASK_TTL", "0")
	cfg, err = LoadConfig(overrides)
	if err != nil {
		t.Fatalf("LoadConfig with cloud opt-out: %v", err)
	}
	if cfg.GCCompletedTaskTTL != 0 {
		t.Fatalf("GCCompletedTaskTTL = %s, want an explicit 0 to disable the cloud default", cfg.GCCompletedTaskTTL)
	}

	t.Setenv("MULTICA_GC_COMPLETED_TASK_TTL", "36h")
	cfg, err = LoadConfig(overrides)
	if err != nil {
		t.Fatalf("LoadConfig with cloud override: %v", err)
	}
	if cfg.GCCompletedTaskTTL != 36*time.Hour {
		t.Fatalf("GCCompletedTaskTTL = %s, want the env override to win on cloud", cfg.GCCompletedTaskTTL)
	}
}

func TestDefaultGCCompletedTaskTTLOnlyBoundsOfficialCloudHost(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		serverURL string
		want      time.Duration
	}{
		{"official cloud", "https://api.multica.ai", DefaultGCCompletedTaskTTLCloud},
		{"official cloud with port and path", "https://API.Multica.AI:443/api", DefaultGCCompletedTaskTTLCloud},
		// Staging and previews inherit the self-host value for the same reason
		// officialCloudHost excludes them from the auto-update default.
		{"staging", "https://api-staging.multica.ai", DefaultGCCompletedTaskTTLSelfHost},
		{"self-host", "https://multica.example.com", DefaultGCCompletedTaskTTLSelfHost},
		{"localhost", "http://localhost:8080", DefaultGCCompletedTaskTTLSelfHost},
		{"unparseable", "://nope", DefaultGCCompletedTaskTTLSelfHost},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := defaultGCCompletedTaskTTL(tc.serverURL); got != tc.want {
				t.Fatalf("defaultGCCompletedTaskTTL(%q) = %s, want %s", tc.serverURL, got, tc.want)
			}
		})
	}
}

func TestRepoMaintenanceKillSwitchDefaultsOnAndCanDisable(t *testing.T) {
	t.Setenv("MULTICA_GC_REPO_MAINTENANCE_ENABLED", "")
	if !boolFromEnv("MULTICA_GC_REPO_MAINTENANCE_ENABLED", true) {
		t.Fatal("repo maintenance kill switch should default to enabled")
	}

	t.Setenv("MULTICA_GC_REPO_MAINTENANCE_ENABLED", "false")
	if boolFromEnv("MULTICA_GC_REPO_MAINTENANCE_ENABLED", true) {
		t.Fatal("repo maintenance kill switch should accept false")
	}
}

func TestPatternsFromEnv_DropsSeparatorBearingEntries(t *testing.T) {
	t.Setenv("MULTICA_GC_ARTIFACT_PATTERNS", "node_modules, .next ,foo/bar, ../etc, ,target")
	got := patternsFromEnv("MULTICA_GC_ARTIFACT_PATTERNS", nil)
	want := []string{"node_modules", ".next", "target"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
}

func TestIsSafeAgentName(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"claude", true},
		{"cursor-agent", true},
		{"kiro_cli", true},
		{"v1.2", true},
		{"Claude2", true},
		{"", false},
		{"a b", false},
		{"a/b", false},
		{"a;b", false},
		{"a$b", false},
		{"a`b", false},
		{"a'b", false},
		{`a"b`, false},
	} {
		if got := isSafeAgentName(tc.in); got != tc.want {
			t.Errorf("isSafeAgentName(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestBuildLoginShellResolveScript_ShapeAndContent(t *testing.T) {
	got := buildLoginShellResolveScript([]string{"claude", "cursor-agent"})
	// Must list exactly the names we asked for, in order.
	if !strings.Contains(got, "for n in claude cursor-agent;") {
		t.Errorf("script missing expected for-loop header:\n%s", got)
	}
	// Must strip aliases AND functions before `command -v` — otherwise
	// `alias claude=...` in .zshrc shadows the real binary, which is the
	// exact case behind #2512. The order matters (unalias/unset -f BEFORE
	// command -v); we assert by relative position.
	idxUnalias := strings.Index(got, `unalias "$n" 2>/dev/null`)
	idxUnsetFn := strings.Index(got, `unset -f "$n" 2>/dev/null`)
	idxLookup := strings.Index(got, `command -v "$n"`)
	if idxUnalias < 0 || idxUnsetFn < 0 || idxLookup < 0 {
		t.Fatalf("script missing unalias/unset -f/command -v steps:\n%s", got)
	}
	if !(idxUnalias < idxLookup && idxUnsetFn < idxLookup) {
		t.Errorf("unalias/unset -f must precede command -v:\n%s", got)
	}
	// Must canonicalise via `cd ... && pwd -P` to break out of symlinked
	// per-shell prefix dirs (fnm/nvm/volta) before the spawned shell exits.
	if !strings.Contains(got, "pwd -P") {
		t.Errorf("script missing pwd -P canonicalisation:\n%s", got)
	}
	// Output must be tab-separated `<name>\t<path>` so the parser can split.
	if !strings.Contains(got, `printf '%s\t%s\n'`) {
		t.Errorf("script missing tab-separated printf:\n%s", got)
	}
}

// TestResolveAgentsViaLoginShell_ResolvesViaInteractiveShell verifies the
// motivating bug scenario: a binary that lives in a directory which is NOT on
// the daemon's PATH but IS added to PATH by the user's interactive shell rc
// file gets resolved to a canonical absolute path.
//
// We simulate this by:
//   - creating a temp dir containing an executable named "fakeclaude"
//   - removing every other dir from PATH (so exec.LookPath misses)
//   - pointing SHELL at an owned fake that accepts -ilc but runs the real
//     discovery script with a fake-only PATH and a non-login interpreter.
//
// System and user login profiles are never loaded.
func TestResolveAgentsViaLoginShell_ResolvesViaInteractiveShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell not available on Windows")
	}
	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "fakeclaude")
	// A trivially executable script. We only need it to exist and be
	// marked +x; the resolver never runs it.
	if err := os.WriteFile(binPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}

	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	if _, err := lookPathInPath("fakeclaude"); err == nil {
		t.Fatal("fixture binary visible before shell discovery")
	}
	sh := ownedLoginShell(t, binDir)
	t.Setenv("SHELL", sh)

	got := resolveAgentsViaLoginShell([]string{"fakeclaude", "kiro-cli"})
	if len(got) != 1 {
		t.Fatalf("discovery escaped fake-only PATH (kiro-cli must be missing): %v", got)
	}
	resolved, ok := got["fakeclaude"]
	if !ok {
		t.Fatalf("expected fakeclaude in resolved map, got %v", got)
	}
	// Must be an absolute path, must exist, must point at our fake binary
	// (resolving any symlinks t.TempDir may have introduced — macOS's
	// /var → /private/var symlink is the usual culprit).
	if !filepath.IsAbs(resolved) {
		t.Errorf("expected absolute path, got %q", resolved)
	}
	wantCanonical, err := filepath.EvalSymlinks(binPath)
	if err != nil {
		t.Fatalf("eval symlinks for expected path: %v", err)
	}
	if resolved != wantCanonical {
		t.Errorf("resolved = %q, want canonical %q", resolved, wantCanonical)
	}
}

func TestResolveAgentsViaLoginShell_SkipsUnsupportedShell(t *testing.T) {
	t.Setenv("SHELL", "/usr/bin/fish")
	got := resolveAgentsViaLoginShell([]string{"claude"})
	if len(got) != 0 {
		t.Errorf("expected empty map for unsupported shell, got %v", got)
	}
}

func TestResolveAgentsViaLoginShell_EmptyShellNoCrash(t *testing.T) {
	t.Setenv("SHELL", "")
	got := resolveAgentsViaLoginShell([]string{"claude"})
	if len(got) != 0 {
		t.Errorf("expected empty map when SHELL unset, got %v", got)
	}
}

func TestResolveAgentsViaLoginShell_EmptyInput(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	got := resolveAgentsViaLoginShell(nil)
	if len(got) != 0 {
		t.Errorf("expected empty map for nil input, got %v", got)
	}
}

// lookPathInPath is a thin wrapper used by the test above; matches what
// exec.LookPath would do but lets the test be explicit about which call it's
// asserting against.
func lookPathInPath(name string) (string, error) {
	return exec.LookPath(name)
}

func TestIsOfficialCloudServer(t *testing.T) {
	for _, tc := range []struct {
		name string
		url  string
		want bool
	}{
		{"canonical cloud https", "https://api.multica.ai", true},
		{"canonical cloud with trailing slash stripped", "https://api.multica.ai/", true},
		{"canonical cloud case-insensitive", "https://API.Multica.AI", true},
		{"cloud over plain http (unusual but match host)", "http://api.multica.ai", true},
		{"localhost is self-host", "http://localhost:8080", false},
		{"loopback ip is self-host", "http://127.0.0.1:8080", false},
		{"lan ip is self-host", "http://192.168.0.28:8080", false},
		{"third-party host is self-host", "https://multica.example.com", false},
		// Staging / preview / future subdomains deliberately follow the
		// safer self-host default until explicitly opted in.
		{"multica.ai apex is not the api host", "https://multica.ai", false},
		{"staging subdomain is self-host", "https://staging.multica.ai", false},
		{"preview subdomain is self-host", "https://api-preview.multica.ai", false},
		// Malformed inputs must not falsely match.
		{"empty string is self-host", "", false},
		{"garbage string is self-host", "::not a url::", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isOfficialCloudServer(tc.url); got != tc.want {
				t.Errorf("isOfficialCloudServer(%q) = %v, want %v", tc.url, got, tc.want)
			}
		})
	}
}

// stageFakeAgent writes an executable `claude` script into a temp dir and
// points PATH (and the daemon-id env var) so LoadConfig can run end-to-end
// without poking the host's real agent installation. Returns the staged PATH
// so tests that need to add their own dirs can extend it.
func stageFakeAgent(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell not available on Windows")
	}
	binDir := t.TempDir()
	fake := filepath.Join(binDir, "claude")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	t.Setenv("PATH", binDir)
	t.Setenv("MULTICA_DAEMON_ID", "11111111-1111-1111-1111-111111111111")
	// Clear any inherited env-var override so the test sees the URL-based
	// default, not whatever the developer happens to have exported.
	t.Setenv("MULTICA_DAEMON_AUTO_UPDATE", "")
	return binDir
}

func TestLoadConfig_DiscoversQwenCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture is unavailable on Windows")
	}
	binDir := stageFakeAgent(t)
	qwen := filepath.Join(binDir, "qwen")
	if err := os.WriteFile(qwen, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake qwen: %v", err)
	}
	// Avoid consulting an inherited interactive shell for all deliberately
	// absent providers; this test is about ordinary PATH discovery.
	t.Setenv("SHELL", "/usr/bin/fish")
	t.Setenv("MULTICA_QWEN_MODEL", "qwen3.8-max-preview")
	t.Setenv("MULTICA_QWEN_ARGS", "--verbose --foo=bar")

	cfg, err := LoadConfig(Overrides{
		ServerURL:      "http://localhost:0",
		WorkspacesRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	entry, ok := cfg.Agents["qwen"]
	if !ok {
		t.Fatalf("qwen was not discovered: %v", cfg.Agents)
	}
	wantPath, err := filepath.EvalSymlinks(qwen)
	if err != nil {
		t.Fatalf("eval symlinks for qwen: %v", err)
	}
	if entry.Path != wantPath || entry.Command != "qwen" || entry.Model != "qwen3.8-max-preview" {
		t.Fatalf("qwen entry = %+v, want path=%q command=qwen model=qwen3.8-max-preview", entry, wantPath)
	}
	if got, want := strings.Join(cfg.QwenArgs, " "), "--verbose --foo=bar"; got != want {
		t.Fatalf("QwenArgs = %q, want %q", got, want)
	}
}

func TestLoadConfig_SkipsMulticaHooksShadowingAgentBinaries(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell not available on Windows")
	}

	home := t.TempDir()
	redirectTestHome(t, home)
	hooksDir := filepath.Join(home, ".multica", "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("create hooks dir: %v", err)
	}
	realBinDir := t.TempDir()

	for _, name := range []string{"claude", "codex", "hermes"} {
		hookPath := filepath.Join(hooksDir, name)
		hookBody := "#!/bin/sh\nexec " + name + " \"$@\"\n"
		if err := os.WriteFile(hookPath, []byte(hookBody), 0o755); err != nil {
			t.Fatalf("write hook wrapper %s: %v", name, err)
		}
		realPath := filepath.Join(realBinDir, name)
		if err := os.WriteFile(realPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatalf("write real binary %s: %v", name, err)
		}
	}

	t.Setenv("PATH", hooksDir+string(os.PathListSeparator)+realBinDir)
	t.Setenv("SHELL", filepath.Join(t.TempDir(), "fish"))
	t.Setenv("MULTICA_DAEMON_ID", "11111111-1111-1111-1111-111111111111")

	// This fixture owns PATH/bundle candidates; do not inherit the runner
	// override that deliberately hides actual desktop installations.
	t.Setenv("MULTICA_CODEX_PATH", "")
	cfg, err := LoadConfig(Overrides{
		ServerURL:      "http://localhost:0",
		WorkspacesRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	for provider, binary := range map[string]string{
		"claude": "claude",
		"codex":  "codex",
		"hermes": "hermes",
	} {
		got, ok := cfg.Agents[provider]
		if !ok {
			t.Fatalf("expected %s agent in config, got %#v", provider, cfg.Agents)
		}
		want := canonicalExecutablePath(filepath.Join(realBinDir, binary))
		if got.Path != want {
			t.Errorf("%s path = %q, want unshadowed real binary %q", provider, got.Path, want)
		}
		if strings.HasPrefix(got.Path, hooksDir) {
			t.Errorf("%s path still points into hooks dir: %q", provider, got.Path)
		}
	}
}

func TestLoadConfig_SkipsMulticaHooksFromLoginShellFallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell not available on Windows")
	}
	home := t.TempDir()
	redirectTestHome(t, home)
	hooksDir := filepath.Join(home, ".multica", "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("create hooks dir: %v", err)
	}
	realBinDir := t.TempDir()
	hookPath := filepath.Join(hooksDir, "codex")
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\nexec codex \"$@\"\n"), 0o755); err != nil {
		t.Fatalf("write hook wrapper: %v", err)
	}
	realPath := filepath.Join(realBinDir, "codex")
	if err := os.WriteFile(realPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write real codex: %v", err)
	}

	t.Setenv("PATH", t.TempDir())
	if _, err := exec.LookPath("codex"); err == nil {
		t.Fatal("fixture codex visible before shell discovery")
	}
	t.Setenv("MULTICA_CODEX_PATH", "")
	sh := ownedLoginShell(t, hooksDir, realBinDir)
	t.Setenv("SHELL", sh)
	t.Setenv("MULTICA_DAEMON_ID", "11111111-1111-1111-1111-111111111111")
	pinNonCodexAgentsToMissingPaths(t)
	oldBundlePaths := codexDesktopAppBundlePaths
	codexDesktopAppBundlePaths = func() []string { return nil }
	t.Cleanup(func() { codexDesktopAppBundlePaths = oldBundlePaths })

	cfg, err := LoadConfig(Overrides{
		ServerURL:      "http://localhost:0",
		WorkspacesRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	got, ok := cfg.Agents["codex"]
	if !ok {
		t.Fatalf("expected codex from login-shell fallback, got %#v", cfg.Agents)
	}
	want := canonicalExecutablePath(realPath)
	if got.Path != want {
		t.Fatalf("codex path = %q, want unshadowed real binary %q", got.Path, want)
	}
	if strings.HasPrefix(got.Path, hooksDir) {
		t.Fatalf("codex path still points into hooks dir: %q", got.Path)
	}
}

// TestLoadConfig_AutoUpdateDefault_SelfHostOff is the regression guard for
// MUL-2381: a daemon pointed at any non-cloud server URL must default
// AutoUpdateEnabled to false, because self-host operators frequently run a
// fork and the upstream GitHub release would silently overwrite it.
func TestLoadConfig_AutoUpdateDefault_SelfHostOff(t *testing.T) {
	stageFakeAgent(t)
	cfg, err := LoadConfig(Overrides{
		ServerURL:      "http://localhost:8080",
		WorkspacesRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.AutoUpdateEnabled {
		t.Fatalf("AutoUpdateEnabled = true for self-host (localhost) server, want false")
	}
}

func TestLoadConfig_CodexHandshakeTimeout(t *testing.T) {
	stageFakeAgent(t)
	t.Setenv("MULTICA_CODEX_HANDSHAKE_TIMEOUT", "")

	cfg, err := LoadConfig(Overrides{
		ServerURL:      "http://localhost:8080",
		WorkspacesRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("LoadConfig with default: %v", err)
	}
	if cfg.CodexHandshakeTimeout != DefaultCodexHandshakeTimeout {
		t.Fatalf("CodexHandshakeTimeout = %s, want default %s", cfg.CodexHandshakeTimeout, DefaultCodexHandshakeTimeout)
	}
	if cfg.CodexThreadHandshakeTimeout != DefaultCodexThreadHandshakeTimeout {
		t.Fatalf("CodexThreadHandshakeTimeout = %s, want default %s", cfg.CodexThreadHandshakeTimeout, DefaultCodexThreadHandshakeTimeout)
	}

	t.Setenv("MULTICA_CODEX_HANDSHAKE_TIMEOUT", "47s")

	cfg, err = LoadConfig(Overrides{
		ServerURL:      "http://localhost:8080",
		WorkspacesRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("LoadConfig with env: %v", err)
	}
	if cfg.CodexHandshakeTimeout != 47*time.Second {
		t.Fatalf("CodexHandshakeTimeout = %s, want 47s from env", cfg.CodexHandshakeTimeout)
	}
	if cfg.CodexThreadHandshakeTimeout != 47*time.Second {
		t.Fatalf("CodexThreadHandshakeTimeout = %s, want legacy 47s env override", cfg.CodexThreadHandshakeTimeout)
	}

	t.Setenv("MULTICA_CODEX_HANDSHAKE_TIMEOUT", "1d")
	cfg, err = LoadConfig(Overrides{
		ServerURL:      "http://localhost:8080",
		WorkspacesRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("LoadConfig with day-unit env: %v", err)
	}
	if cfg.CodexHandshakeTimeout != 24*time.Hour {
		t.Fatalf("CodexHandshakeTimeout = %s, want 24h from 1d env", cfg.CodexHandshakeTimeout)
	}
	if cfg.CodexThreadHandshakeTimeout != 24*time.Hour {
		t.Fatalf("CodexThreadHandshakeTimeout = %s, want legacy 24h env override", cfg.CodexThreadHandshakeTimeout)
	}

	t.Setenv("MULTICA_CODEX_HANDSHAKE_TIMEOUT", "0")
	cfg, err = LoadConfig(Overrides{
		ServerURL:      "http://localhost:8080",
		WorkspacesRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("LoadConfig with zero env: %v", err)
	}
	if cfg.CodexHandshakeTimeout != DefaultCodexHandshakeTimeout {
		t.Fatalf("CodexHandshakeTimeout = %s, want default %s for zero env", cfg.CodexHandshakeTimeout, DefaultCodexHandshakeTimeout)
	}
	if cfg.CodexThreadHandshakeTimeout != DefaultCodexThreadHandshakeTimeout {
		t.Fatalf("CodexThreadHandshakeTimeout = %s, want default %s for zero env", cfg.CodexThreadHandshakeTimeout, DefaultCodexThreadHandshakeTimeout)
	}

	cfg, err = LoadConfig(Overrides{
		ServerURL:             "http://localhost:8080",
		WorkspacesRoot:        t.TempDir(),
		CodexHandshakeTimeout: 12 * time.Second,
	})
	if err != nil {
		t.Fatalf("LoadConfig with override: %v", err)
	}
	if cfg.CodexHandshakeTimeout != 12*time.Second {
		t.Fatalf("CodexHandshakeTimeout = %s, want 12s from override", cfg.CodexHandshakeTimeout)
	}
	if cfg.CodexThreadHandshakeTimeout != 12*time.Second {
		t.Fatalf("CodexThreadHandshakeTimeout = %s, want legacy 12s override", cfg.CodexThreadHandshakeTimeout)
	}
}

func TestLoadConfig_CodexTurnInterruptTimeout(t *testing.T) {
	stageFakeAgent(t)
	t.Setenv("MULTICA_CODEX_TURN_INTERRUPT_TIMEOUT", "750ms")

	cfg, err := LoadConfig(Overrides{
		ServerURL:      "http://localhost:8080",
		WorkspacesRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("LoadConfig with interrupt timeout: %v", err)
	}
	if cfg.CodexTurnInterruptTimeout != 750*time.Millisecond {
		t.Fatalf("CodexTurnInterruptTimeout = %s, want 750ms", cfg.CodexTurnInterruptTimeout)
	}

	t.Setenv("MULTICA_CODEX_TURN_INTERRUPT_TIMEOUT", "0")
	cfg, err = LoadConfig(Overrides{
		ServerURL:      "http://localhost:8080",
		WorkspacesRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("LoadConfig with zero interrupt timeout: %v", err)
	}
	if cfg.CodexTurnInterruptTimeout != DefaultCodexTurnInterruptTimeout {
		t.Fatalf("CodexTurnInterruptTimeout = %s, want default %s", cfg.CodexTurnInterruptTimeout, DefaultCodexTurnInterruptTimeout)
	}
}

// TestLoadConfig_CodexFirstTurnNoProgressTimeout pins the env-only
// MULTICA_CODEX_FIRST_TURN_TIMEOUT resolution (GH #3262 / #5959): unset and an
// explicit "0" both mean "keep the backend default" (0 = unset), while a positive
// value is honored verbatim. There is deliberately no Overrides/CLI parity — this
// knob is environment-only.
func TestLoadConfig_CodexFirstTurnNoProgressTimeout(t *testing.T) {
	stageFakeAgent(t)
	t.Setenv("MULTICA_CODEX_FIRST_TURN_TIMEOUT", "")

	cfg, err := LoadConfig(Overrides{
		ServerURL:      "http://localhost:8080",
		WorkspacesRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("LoadConfig with unset: %v", err)
	}
	if cfg.CodexFirstTurnNoProgressTimeout != 0 {
		t.Fatalf("CodexFirstTurnNoProgressTimeout = %s, want 0 when unset", cfg.CodexFirstTurnNoProgressTimeout)
	}

	t.Setenv("MULTICA_CODEX_FIRST_TURN_TIMEOUT", "30m")
	cfg, err = LoadConfig(Overrides{
		ServerURL:      "http://localhost:8080",
		WorkspacesRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("LoadConfig with env: %v", err)
	}
	if cfg.CodexFirstTurnNoProgressTimeout != 30*time.Minute {
		t.Fatalf("CodexFirstTurnNoProgressTimeout = %s, want 30m from env", cfg.CodexFirstTurnNoProgressTimeout)
	}

	t.Setenv("MULTICA_CODEX_FIRST_TURN_TIMEOUT", "0")
	cfg, err = LoadConfig(Overrides{
		ServerURL:      "http://localhost:8080",
		WorkspacesRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("LoadConfig with zero env: %v", err)
	}
	if cfg.CodexFirstTurnNoProgressTimeout != 0 {
		t.Fatalf("CodexFirstTurnNoProgressTimeout = %s, want 0 for explicit zero", cfg.CodexFirstTurnNoProgressTimeout)
	}
}

// TestLoadConfig_CodexFirstTurnTimeoutEqualToSemanticWarns pins the equality
// edge (multica-eve review on #6753): because the semantic-inactivity timer is
// armed before the first-turn timer, equal durations still let the semantic
// deadline win and drop the #3291 startup retry. LoadConfig must warn when the
// first-turn timeout is >= the semantic timeout, and stay quiet only when the
// semantic timeout is strictly greater.
func TestLoadConfig_CodexFirstTurnTimeoutEqualToSemanticWarns(t *testing.T) {
	stageFakeAgent(t)

	const warnNeedle = "MULTICA_CODEX_FIRST_TURN_TIMEOUT is greater than or equal to the semantic-inactivity timeout"

	loadWithLoggedWarnings := func(t *testing.T, semantic, firstTurn string) string {
		t.Helper()
		var buf bytes.Buffer
		prev := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
		t.Cleanup(func() { slog.SetDefault(prev) })

		t.Setenv("MULTICA_CODEX_SEMANTIC_INACTIVITY_TIMEOUT", semantic)
		t.Setenv("MULTICA_CODEX_FIRST_TURN_TIMEOUT", firstTurn)
		if _, err := LoadConfig(Overrides{
			ServerURL:      "http://localhost:8080",
			WorkspacesRoot: t.TempDir(),
		}); err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		return buf.String()
	}

	// Equal durations: the semantic timer is armed first, so the retry can still
	// be lost. The warning MUST fire — this is the edge the earlier `>`-only
	// check missed.
	if logs := loadWithLoggedWarnings(t, "15m", "15m"); !strings.Contains(logs, warnNeedle) {
		t.Fatalf("equal durations must warn; logs = %q", logs)
	}

	// First-turn strictly above semantic: also warns (the original truncation case).
	if logs := loadWithLoggedWarnings(t, "10m", "30m"); !strings.Contains(logs, warnNeedle) {
		t.Fatalf("first-turn above semantic must warn; logs = %q", logs)
	}

	// Semantic strictly above first-turn: the recommended safe configuration —
	// no warning.
	if logs := loadWithLoggedWarnings(t, "30m", "10m"); strings.Contains(logs, warnNeedle) {
		t.Fatalf("semantic strictly above first-turn must not warn; logs = %q", logs)
	}
}

// TestLoadConfig_ToolWatchdogDefaultsToIdleWatchdog pins the merge: the
// in-flight-tool budget is no longer an independent constant, so an operator
// who raises the idle budget cannot accidentally leave tool calls capped at a
// lower, invisible ceiling.
func TestLoadConfig_ToolWatchdogDefaultsToIdleWatchdog(t *testing.T) {
	stageFakeAgent(t)
	t.Setenv("MULTICA_AGENT_IDLE_WATCHDOG", "")
	t.Setenv("MULTICA_AGENT_TOOL_WATCHDOG", "")

	load := func(t *testing.T) Config {
		t.Helper()
		cfg, err := LoadConfig(Overrides{
			ServerURL:      "http://localhost:8080",
			WorkspacesRoot: t.TempDir(),
		})
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		return cfg
	}

	cfg := load(t)
	if cfg.AgentIdleWatchdog != DefaultAgentIdleWatchdog {
		t.Fatalf("AgentIdleWatchdog = %s, want default %s", cfg.AgentIdleWatchdog, DefaultAgentIdleWatchdog)
	}
	if cfg.AgentToolWatchdog != cfg.AgentIdleWatchdog {
		t.Fatalf("AgentToolWatchdog = %s, want it to track AgentIdleWatchdog %s", cfg.AgentToolWatchdog, cfg.AgentIdleWatchdog)
	}

	// Raising only the idle budget must carry the tool budget with it.
	t.Setenv("MULTICA_AGENT_IDLE_WATCHDOG", "6h")
	cfg = load(t)
	if cfg.AgentIdleWatchdog != 6*time.Hour || cfg.AgentToolWatchdog != 6*time.Hour {
		t.Fatalf("idle=%s tool=%s, want both 6h", cfg.AgentIdleWatchdog, cfg.AgentToolWatchdog)
	}

	// An explicit tool override still wins, so "tools may run longer than the
	// model may think" stays expressible.
	t.Setenv("MULTICA_AGENT_TOOL_WATCHDOG", "12h")
	cfg = load(t)
	if cfg.AgentIdleWatchdog != 6*time.Hour {
		t.Fatalf("AgentIdleWatchdog = %s, want 6h", cfg.AgentIdleWatchdog)
	}
	if cfg.AgentToolWatchdog != 12*time.Hour {
		t.Fatalf("AgentToolWatchdog = %s, want 12h from env", cfg.AgentToolWatchdog)
	}

	// Zero keeps its distinct meaning: never force-stop while a tool is in
	// flight. It must NOT be re-derived from the idle budget.
	t.Setenv("MULTICA_AGENT_TOOL_WATCHDOG", "0")
	cfg = load(t)
	if cfg.AgentToolWatchdog != 0 {
		t.Fatalf("AgentToolWatchdog = %s, want 0 from env", cfg.AgentToolWatchdog)
	}

	// Disabling the suite disables both.
	t.Setenv("MULTICA_AGENT_IDLE_WATCHDOG", "0")
	t.Setenv("MULTICA_AGENT_TOOL_WATCHDOG", "")
	cfg = load(t)
	if cfg.AgentIdleWatchdog != 0 || cfg.AgentToolWatchdog != 0 {
		t.Fatalf("idle=%s tool=%s, want both 0", cfg.AgentIdleWatchdog, cfg.AgentToolWatchdog)
	}
}

// TestLoadConfig_CodexSemanticInactivityDerivesFromWatchdog pins the fix for
// the gap the 2h change left behind: Codex's own semantic-inactivity timer is
// not tool-aware, so if it keeps a 10m ceiling it kills a quiet build long
// before the daemon budget that was supposed to protect it, and the raised
// budget is a lie for Codex users.
func TestLoadConfig_CodexSemanticInactivityDerivesFromWatchdog(t *testing.T) {
	stageFakeAgent(t)
	for _, key := range []string{
		"MULTICA_AGENT_IDLE_WATCHDOG",
		"MULTICA_AGENT_TOOL_WATCHDOG",
		"MULTICA_CODEX_SEMANTIC_INACTIVITY_TIMEOUT",
	} {
		t.Setenv(key, "")
	}

	load := func(t *testing.T) Config {
		t.Helper()
		cfg, err := LoadConfig(Overrides{
			ServerURL:      "http://localhost:8080",
			WorkspacesRoot: t.TempDir(),
		})
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		return cfg
	}

	cfg := load(t)
	if cfg.CodexSemanticInactivityTimeout != DefaultAgentIdleWatchdog {
		t.Fatalf("CodexSemanticInactivityTimeout = %s, want the idle budget %s", cfg.CodexSemanticInactivityTimeout, DefaultAgentIdleWatchdog)
	}

	// Raising the idle budget carries Codex with it.
	t.Setenv("MULTICA_AGENT_IDLE_WATCHDOG", "6h")
	if got := load(t).CodexSemanticInactivityTimeout; got != 6*time.Hour {
		t.Fatalf("CodexSemanticInactivityTimeout = %s, want 6h", got)
	}

	// A wider tool budget wins: this timer cannot see that a tool is in flight,
	// so it has to be sized like the larger of the two or it re-creates the very
	// bug being fixed, one tier up.
	t.Setenv("MULTICA_AGENT_TOOL_WATCHDOG", "9h")
	if got := load(t).CodexSemanticInactivityTimeout; got != 9*time.Hour {
		t.Fatalf("CodexSemanticInactivityTimeout = %s, want the wider tool budget 9h", got)
	}

	// A tool budget of 0 means "never force-stop during a tool", which this
	// timer cannot express; it falls back to the idle budget rather than
	// silently running unbounded.
	t.Setenv("MULTICA_AGENT_TOOL_WATCHDOG", "0")
	if got := load(t).CodexSemanticInactivityTimeout; got != 6*time.Hour {
		t.Fatalf("CodexSemanticInactivityTimeout = %s, want the idle budget 6h when the tool budget is unbounded", got)
	}

	// The explicit env still wins over the derivation.
	t.Setenv("MULTICA_CODEX_SEMANTIC_INACTIVITY_TIMEOUT", "20m")
	if got := load(t).CodexSemanticInactivityTimeout; got != 20*time.Minute {
		t.Fatalf("CodexSemanticInactivityTimeout = %s, want 20m from env", got)
	}

	// Disabling the watchdog suite has never disabled this timer; Codex keeps
	// its own built-in default rather than becoming unbounded.
	t.Setenv("MULTICA_CODEX_SEMANTIC_INACTIVITY_TIMEOUT", "")
	t.Setenv("MULTICA_AGENT_IDLE_WATCHDOG", "0")
	t.Setenv("MULTICA_AGENT_TOOL_WATCHDOG", "")
	if got := load(t).CodexSemanticInactivityTimeout; got != DefaultCodexSemanticInactivityTimeout {
		t.Fatalf("CodexSemanticInactivityTimeout = %s, want the codex built-in %s when watchdogs are off", got, DefaultCodexSemanticInactivityTimeout)
	}
}

func TestLoadConfig_OpenCodeIdleWatchdog(t *testing.T) {
	stageFakeAgent(t)
	t.Setenv("MULTICA_OPENCODE_IDLE_WATCHDOG", "")

	cfg, err := LoadConfig(Overrides{
		ServerURL:      "http://localhost:8080",
		WorkspacesRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("LoadConfig with default: %v", err)
	}
	if cfg.OpenCodeIdleWatchdog != DefaultOpenCodeIdleWatchdog {
		t.Fatalf("OpenCodeIdleWatchdog = %s, want default %s", cfg.OpenCodeIdleWatchdog, DefaultOpenCodeIdleWatchdog)
	}

	t.Setenv("MULTICA_OPENCODE_IDLE_WATCHDOG", "7m")
	cfg, err = LoadConfig(Overrides{
		ServerURL:      "http://localhost:8080",
		WorkspacesRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("LoadConfig with env: %v", err)
	}
	if cfg.OpenCodeIdleWatchdog != 7*time.Minute {
		t.Fatalf("OpenCodeIdleWatchdog = %s, want 7m from env", cfg.OpenCodeIdleWatchdog)
	}

	// Zero disables the OpenCode-specific override while leaving the generic
	// AgentIdleWatchdog as the fallback for OpenCode runs.
	t.Setenv("MULTICA_OPENCODE_IDLE_WATCHDOG", "0")
	cfg, err = LoadConfig(Overrides{
		ServerURL:      "http://localhost:8080",
		WorkspacesRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("LoadConfig with zero env: %v", err)
	}
	if cfg.OpenCodeIdleWatchdog != 0 {
		t.Fatalf("OpenCodeIdleWatchdog = %s, want zero from env", cfg.OpenCodeIdleWatchdog)
	}
}

// TestLoadConfig_AutoUpdateDefault_CloudOn confirms the symmetric case: a
// daemon pointed at Multica's hosted cloud keeps the historical opt-in
// auto-update default. We pass the WSS form of the URL to also exercise that
// NormalizeServerBaseURL maps it through to the http host the detector
// inspects.
func TestLoadConfig_AutoUpdateDefault_CloudOn(t *testing.T) {
	stageFakeAgent(t)
	cfg, err := LoadConfig(Overrides{
		ServerURL:      "wss://api.multica.ai/ws",
		WorkspacesRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg.AutoUpdateEnabled {
		t.Fatalf("AutoUpdateEnabled = false for Multica Cloud server, want true")
	}
}

// TestLoadConfig_AutoUpdateEnv_ForcesOnForSelfHost lets a self-host operator
// re-enable auto-update via env var, overriding the new conservative default.
func TestLoadConfig_AutoUpdateEnv_ForcesOnForSelfHost(t *testing.T) {
	stageFakeAgent(t)
	t.Setenv("MULTICA_DAEMON_AUTO_UPDATE", "true")
	cfg, err := LoadConfig(Overrides{
		ServerURL:      "http://localhost:8080",
		WorkspacesRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg.AutoUpdateEnabled {
		t.Fatalf("AutoUpdateEnabled = false after explicit MULTICA_DAEMON_AUTO_UPDATE=true, want true")
	}
}

// TestLoadConfig_AutoUpdateEnv_ForcesOffForCloud covers the inverse: a cloud
// user can still opt out via env var.
func TestLoadConfig_AutoUpdateEnv_ForcesOffForCloud(t *testing.T) {
	stageFakeAgent(t)
	t.Setenv("MULTICA_DAEMON_AUTO_UPDATE", "false")
	cfg, err := LoadConfig(Overrides{
		ServerURL:      "https://api.multica.ai",
		WorkspacesRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.AutoUpdateEnabled {
		t.Fatalf("AutoUpdateEnabled = true after explicit MULTICA_DAEMON_AUTO_UPDATE=false, want false")
	}
}

// TestLoadConfig_AutoUpdate_NoFlagWinsOverCloudDefault keeps the legacy CLI
// flag working: --no-auto-update (translated into overrides.DisableAutoUpdate)
// forces auto-update off even when the cloud default and env var would enable.
func TestLoadConfig_AutoUpdate_NoFlagWinsOverCloudDefault(t *testing.T) {
	stageFakeAgent(t)
	t.Setenv("MULTICA_DAEMON_AUTO_UPDATE", "true")
	cfg, err := LoadConfig(Overrides{
		ServerURL:         "https://api.multica.ai",
		WorkspacesRoot:    t.TempDir(),
		DisableAutoUpdate: true,
	})
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.AutoUpdateEnabled {
		t.Fatalf("AutoUpdateEnabled = true with --no-auto-update set; flag must win")
	}
}

// TestLoadConfig_AutoReload_DefaultsOnEvenForSelfHost is the review's first
// product decision, encoded: "don't pull new versions from GitHub" and "follow
// the binary I replaced myself" are separate concerns. Self-host defaults
// auto-update OFF (MUL-2381) because upgrading a fork from an upstream release
// would clobber it — an argument that says nothing about a binary the operator
// installed by hand.
func TestLoadConfig_AutoReload_DefaultsOnEvenForSelfHost(t *testing.T) {
	stageFakeAgent(t)
	t.Setenv("MULTICA_DAEMON_AUTO_UPDATE", "")
	t.Setenv("MULTICA_DAEMON_AUTO_RELOAD", "")
	cfg, err := LoadConfig(Overrides{
		ServerURL:      "http://localhost:8080",
		WorkspacesRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.AutoUpdateEnabled {
		t.Fatalf("AutoUpdateEnabled = true for self-host, want false (MUL-2381)")
	}
	if !cfg.AutoReloadEnabled {
		t.Fatalf("AutoReloadEnabled = false for self-host; the on-disk watcher must not ride on the auto-update default")
	}
}

// TestLoadConfig_AutoReload_NotGatedOnAutoUpdateEnv is the same decoupling at
// the env layer: turning GitHub polling off must not silently stop the daemon
// from following a hand-installed binary.
func TestLoadConfig_AutoReload_NotGatedOnAutoUpdateEnv(t *testing.T) {
	stageFakeAgent(t)
	t.Setenv("MULTICA_DAEMON_AUTO_UPDATE", "false")
	t.Setenv("MULTICA_DAEMON_AUTO_RELOAD", "")
	cfg, err := LoadConfig(Overrides{
		ServerURL:      "https://api.multica.ai",
		WorkspacesRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg.AutoReloadEnabled {
		t.Fatalf("MULTICA_DAEMON_AUTO_UPDATE=false disabled auto-reload; the two switches are independent")
	}
}

// TestLoadConfig_AutoReload_OffSwitches pins the escape hatch across both layers
// it can be turned off from. The config-file layer resolves to
// overrides.DisableAutoReload in cmd_daemon.go, so it is covered by the same
// assertion as the flag.
func TestLoadConfig_AutoReload_OffSwitches(t *testing.T) {
	cases := []struct {
		name      string
		env       string
		overrides Overrides
	}{
		{name: "env false", env: "false"},
		{name: "env 0", env: "0"},
		{name: "env off", env: "off"},
		{name: "flag or config file", env: "", overrides: Overrides{DisableAutoReload: true}},
		{name: "flag beats a truthy env", env: "true", overrides: Overrides{DisableAutoReload: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stageFakeAgent(t)
			t.Setenv("MULTICA_DAEMON_AUTO_RELOAD", tc.env)
			overrides := tc.overrides
			overrides.ServerURL = "https://api.multica.ai"
			overrides.WorkspacesRoot = t.TempDir()
			cfg, err := LoadConfig(overrides)
			if err != nil {
				t.Fatalf("LoadConfig: %v", err)
			}
			if cfg.AutoReloadEnabled {
				t.Fatalf("AutoReloadEnabled = true, want false")
			}
		})
	}
}

// TestResolveAgentsViaLoginShell_StripsAliasShadowing locks down the fix for
// #2512: when the user's rc file declares an alias with the same name as the
// agent CLI, the resolver must still return the real binary on PATH, not the
// alias text. The previous revision of this code passed the rest of the test
// suite but silently dropped this case (alias text is not absolute, so the
// `case "$p" in /*)` filter rejected it).
func TestResolveAgentsViaLoginShell_StripsAliasShadowing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell not available on Windows")
	}
	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "fakeclaude")
	aliasPath := filepath.Join(binDir, "alias-wrapper")
	for _, path := range []string{binPath, aliasPath} {
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	// Parse the real script only after defining the alias, without ENV or login rc.
	prelude := "exec /bin/sh -c 'alias fakeclaude=\"" + aliasPath + "\"; eval \"$1\"' sh \"$2\"\n"
	sh := ownedLoginShellWithPrelude(t, prelude, binDir)
	t.Setenv("SHELL", sh)
	t.Setenv("PATH", t.TempDir())
	if sh == "/bin/sh" || !filepath.IsAbs(sh) {
		t.Fatal("fixture must use an owned shell before login discovery")
	}
	if _, err := lookPathInPath("fakeclaude"); err == nil {
		t.Fatal("fakeclaude must be absent from the daemon PATH")
	}
	probe := func(script string) string {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, sh, "-ilc", script)
		cmd.WaitDelay = 100 * time.Millisecond
		raw, err := cmd.Output()
		if err != nil {
			t.Fatalf("owned alias control: %v", err)
		}
		return string(raw)
	}
	if got := probe("for n in fakeclaude; do command -v \"$n\"; done"); !strings.Contains(got, aliasPath) {
		t.Fatalf("alias precondition not established: %q", got)
	}
	// Negative control runs the actual script with just alias removal disabled.
	script := buildLoginShellResolveScript([]string{"fakeclaude"})
	withoutUnalias := strings.ReplaceAll(script, "  unalias \"$n\" 2>/dev/null\n", "")
	if got := probe(withoutUnalias); strings.TrimSpace(got) != "" {
		t.Fatalf("alias-shadowed script unexpectedly found PATH binary: %q", got)
	}
	got := resolveAgentsViaLoginShell([]string{"fakeclaude"})
	wantCanonical, err := filepath.EvalSymlinks(binPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["fakeclaude"] != wantCanonical {
		t.Fatalf("alias-unshadowed resolution = %v, want only fakeclaude=%q", got, wantCanonical)
	}
}

// The owned shell exits after running the real discovery script. Its supervised
// child keeps stdout open beyond the asserted cap; cleanup kills and reaps that
// child before waiting for the resolver, even when the deadline assertion fails.
func TestResolveAgentsViaLoginShell_HardTimeoutOnBackgroundedStdout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell not available on Windows")
	}
	root := t.TempDir()
	if strings.ContainsAny(root, "'\n\r") {
		t.Fatal("unsafe fixture path")
	}
	pidFile, childFile := filepath.Join(root, "supervisor-pid"), filepath.Join(root, "child-pid")
	ready, cleaned := filepath.Join(root, "ready"), filepath.Join(root, "cleaned")
	supervisor := filepath.Join(root, "supervisor")
	body := "#!/bin/sh\nunset ENV BASH_ENV\n" +
		"/bin/sleep 15 &\nchild=$!\n" +
		"cleanup() { kill \"$child\" 2>/dev/null; wait \"$child\" 2>/dev/null; : > '" + cleaned + "'; exit 0; }\ntrap cleanup TERM\n" +
		"printf '%s\\n' \"$child\" > '" + childFile + "'\n" +
		": > '" + ready + "'\nwait \"$child\"\n: > '" + cleaned + "'\n"
	if err := os.WriteFile(supervisor, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	prelude := "'" + supervisor + "' &\nprintf '%s\\n' \"$!\" > '" + pidFile + "'\n" +
		"while [ ! -f '" + ready + "' ]; do /bin/sleep 0.01; done\n"
	sh := ownedLoginShellWithPrelude(t, prelude, root)
	t.Setenv("SHELL", sh)
	t.Setenv("PATH", t.TempDir())
	if sh == "/bin/sh" || !filepath.IsAbs(sh) {
		t.Fatal("fixture must use an owned shell before login discovery")
	}
	origWaitDelay := loginShellResolveWaitDelay
	loginShellResolveWaitDelay = 100 * time.Millisecond
	defer func() { loginShellResolveWaitDelay = origWaitDelay }()
	cap := loginShellResolveTimeout + loginShellResolveWaitDelay + 3*time.Second
	if 15*time.Second <= cap {
		t.Fatal("survivor lifetime must exceed asserted resolver cap")
	}
	done := make(chan map[string]string, 1)
	// Cleanup is registered before launching any child and waits on the shell's
	// own wait receipt, not a guessed PID or a global process-name kill.
	defer func() {
		raw, err := os.ReadFile(pidFile)
		if err != nil {
			t.Errorf("owned supervisor PID missing: %v", err)
			return
		}
		pid := strings.TrimSpace(string(raw))
		if pid == "" || pid == "0" || pid == "1" || strings.Trim(pid, "0123456789") != "" {
			t.Errorf("invalid owned supervisor PID: %q", pid)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := exec.CommandContext(ctx, "/bin/kill", "-TERM", pid).Run(); err != nil {
			t.Errorf("stop owned supervisor %s: %v", pid, err)
		}
		deadline := time.Now().Add(2 * time.Second)
		for {
			if _, err := os.Stat(cleaned); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Error("owned supervisor did not reap stdout child within cleanup bound")
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		child, err := os.ReadFile(childFile)
		if err != nil || strings.TrimSpace(string(child)) == "" {
			t.Errorf("owned stdout child PID missing: %v", err)
		} else if err := exec.CommandContext(ctx, "/bin/kill", "-0", strings.TrimSpace(string(child))).Run(); err == nil {
			t.Error("stdout child still alive after supervisor wait")
		}
		// The supervisor exits immediately after writing its reaping receipt.
		deadline = time.Now().Add(2 * time.Second)
		for exec.Command("/bin/kill", "-0", pid).Run() == nil {
			if time.Now().After(deadline) {
				t.Error("owned supervisor still alive after cleanup")
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("resolver did not finish after owned child cleanup")
		}
		t.Logf("owned supervisor %s killed and stdout child %s reaped", pid, strings.TrimSpace(string(child)))
	}()
	start := time.Now()
	go func() { done <- resolveAgentsViaLoginShell([]string{"missing-fixture-agent"}) }()
	select {
	case got := <-done:
		done <- got // Preserve the completion receipt for deferred cleanup.
		if len(got) != 0 {
			t.Errorf("missing owned agent resolved unexpectedly: %v", got)
		}
		if elapsed := time.Since(start); elapsed < loginShellResolveWaitDelay || elapsed > cap {
			t.Errorf("resolver took %v, want WaitDelay <= elapsed <= %v", elapsed, cap)
		}
		if _, err := os.Stat(ready); err != nil {
			t.Errorf("stdout survivor never became ready: %v", err)
		}
		if _, err := os.Stat(cleaned); !os.IsNotExist(err) {
			t.Error("stdout survivor exited before the deadline assertion")
		}
	case <-time.After(cap):
		t.Errorf("resolver did not return within %v — WaitDelay is not enforcing a hard ceiling", cap)
	}
}

// TestLoadConfig_SkipsLoginShellWhenLookPathSucceeds proves the laziness
// requirement: if every agent CLI the operator cares about is already
// resolvable via the daemon's PATH (or pinned to an explicit MULTICA_*_PATH),
// the shell-fallback path must not run. We assert this by pointing SHELL at
// a sentinel script that touches a marker file when invoked.
func TestLoadConfig_SkipsLoginShellWhenLookPathSucceeds(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell not available on Windows")
	}

	// Stage 1: a fake `claude` binary the daemon's bare exec.LookPath
	// definitely sees, so the probe loop never has reason to consult
	// shellResolved.
	pathDir := t.TempDir()
	fakeClaude := filepath.Join(pathDir, "claude")
	if err := os.WriteFile(fakeClaude, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}

	// Stage 2: a SHELL that writes a marker file when invoked. If
	// LoadConfig's getShellResolved closure fires, the marker appears.
	shellDir := t.TempDir()
	shellPath := filepath.Join(shellDir, "bash") // pick a name the resolver's allowlist accepts
	marker := filepath.Join(shellDir, "invoked.marker")
	shellBody := "#!/bin/sh\ntouch \"" + marker + "\"\n"
	if err := os.WriteFile(shellPath, []byte(shellBody), 0o755); err != nil {
		t.Fatalf("write sentinel shell: %v", err)
	}

	t.Setenv("PATH", pathDir)
	t.Setenv("SHELL", shellPath)
	// Pin a non-existent agent to a bare name so it would normally trip
	// the fallback — except `claude` already resolves, and the user hasn't
	// configured anything else, so the probe loop should be satisfied
	// after the first probe alone.
	t.Setenv("MULTICA_DAEMON_ID", "11111111-1111-1111-1111-111111111111")

	if _, err := LoadConfig(Overrides{
		ServerURL:      "http://localhost:0",
		WorkspacesRoot: t.TempDir(),
	}); err != nil {
		// Some daemon-id / workspace bookkeeping outside our concern may
		// fail in CI; the marker assertion below is what matters either
		// way, so we don't fail on LoadConfig errors directly.
		t.Logf("LoadConfig returned %v (non-fatal for this test)", err)
	}
	// Brief wait for any goroutine the resolver might have leaked. The
	// sync.Once-guarded resolver runs synchronously today, so this should
	// be immediate; the sleep is just to avoid a flake if that ever
	// changes.
	time.Sleep(50 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("login shell was invoked even though exec.LookPath found every agent — laziness broken")
	} else if !os.IsNotExist(err) {
		t.Fatalf("unexpected error stat-ing marker file: %v", err)
	}
}

func TestLoadConfig_UsesCodexDesktopAppBundleFallback(t *testing.T) {
	pathDir := t.TempDir()
	fakeCodex := filepath.Join(pathDir, "Codex.app", "Contents", "Resources", "codex")
	if err := os.MkdirAll(filepath.Dir(fakeCodex), 0o755); err != nil {
		t.Fatalf("mkdir fake Codex bundle: %v", err)
	}
	if err := os.WriteFile(fakeCodex, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake Codex bundle CLI: %v", err)
	}

	oldBundlePaths := codexDesktopAppBundlePaths
	codexDesktopAppBundlePaths = func() []string { return []string{fakeCodex} }
	t.Cleanup(func() { codexDesktopAppBundlePaths = oldBundlePaths })

	t.Setenv("PATH", t.TempDir())
	t.Setenv("SHELL", filepath.Join(t.TempDir(), "fish"))
	t.Setenv("MULTICA_DAEMON_ID", "11111111-1111-1111-1111-111111111111")
	t.Setenv("MULTICA_CODEX_MODEL", "gpt-5")
	pinNonCodexAgentsToMissingPaths(t)

	t.Setenv("MULTICA_CODEX_PATH", "") // Only the owned bundle candidates may rescue the default.
	cfg, err := LoadConfig(Overrides{
		ServerURL:      "http://localhost:0",
		WorkspacesRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	got, ok := cfg.Agents["codex"]
	if !ok {
		t.Fatalf("expected codex agent from Desktop app bundle fallback, got %#v", cfg.Agents)
	}
	if got.Path != fakeCodex {
		t.Fatalf("codex path = %q, want %q", got.Path, fakeCodex)
	}
	if got.Model != "gpt-5" {
		t.Fatalf("codex model = %q, want gpt-5", got.Model)
	}
}

// Regression for #5205: after OpenAI moved the Desktop app to ChatGPT.app,
// Multica must resolve the bundled CLI under ChatGPT.app (and prefer it over
// the legacy Codex.app path when both exist).
func TestLoadConfig_UsesChatGPTAppBundleCodexPath(t *testing.T) {
	pathDir := t.TempDir()
	fakeChatGPT := filepath.Join(pathDir, "ChatGPT.app", "Contents", "Resources", "codex")
	fakeLegacy := filepath.Join(pathDir, "Codex.app", "Contents", "Resources", "codex")
	for _, p := range []string{fakeChatGPT, fakeLegacy} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatalf("write fake CLI: %v", err)
		}
	}

	oldBundlePaths := codexDesktopAppBundlePaths
	// Prefer ChatGPT first, matching production ordering.
	codexDesktopAppBundlePaths = func() []string { return []string{fakeChatGPT, fakeLegacy} }
	t.Cleanup(func() { codexDesktopAppBundlePaths = oldBundlePaths })

	t.Setenv("PATH", t.TempDir())
	t.Setenv("SHELL", filepath.Join(t.TempDir(), "fish"))
	t.Setenv("MULTICA_DAEMON_ID", "11111111-1111-1111-1111-111111111111")
	pinNonCodexAgentsToMissingPaths(t)
	t.Setenv("MULTICA_CODEX_PATH", "") // Discovery is restricted to owned bundle candidates.

	cfg, err := LoadConfig(Overrides{
		ServerURL:      "http://localhost:0",
		WorkspacesRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	got, ok := cfg.Agents["codex"]
	if !ok {
		t.Fatalf("expected codex agent from ChatGPT.app bundle, got %#v", cfg.Agents)
	}
	if got.Path != fakeChatGPT {
		t.Fatalf("codex path = %q, want ChatGPT.app path %q", got.Path, fakeChatGPT)
	}
}

// Regression for #8941: ChatGPT.app 26.924+ ships the CLI at
// Resources/codex-cli/bin/codex. Discovery must use that path when the older
// flat Resources/codex file is absent.
func TestLoadConfig_UsesNestedChatGPTCodexCLIPath(t *testing.T) {
	pathDir := t.TempDir()
	nested := filepath.Join(pathDir, "ChatGPT.app", "Contents", "Resources", "codex-cli", "bin", "codex")
	if err := os.MkdirAll(filepath.Dir(nested), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(nested, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake CLI: %v", err)
	}

	oldBundlePaths := codexDesktopAppBundlePaths
	codexDesktopAppBundlePaths = func() []string { return []string{nested} }
	t.Cleanup(func() { codexDesktopAppBundlePaths = oldBundlePaths })

	t.Setenv("PATH", t.TempDir())
	t.Setenv("SHELL", filepath.Join(t.TempDir(), "fish"))
	t.Setenv("MULTICA_DAEMON_ID", "11111111-1111-1111-1111-111111111111")
	pinNonCodexAgentsToMissingPaths(t)

	cfg, err := LoadConfig(Overrides{
		ServerURL:      "http://localhost:0",
		WorkspacesRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	got, ok := cfg.Agents["codex"]
	if !ok {
		t.Fatalf("expected codex agent from nested ChatGPT.app path, got %#v", cfg.Agents)
	}
	if got.Path != nested {
		t.Fatalf("codex path = %q, want nested path %q", got.Path, nested)
	}
}

// A bundled CLI that exists but cannot be spawned must stay unregistered:
// registering it would advertise a healthy runtime whose every task fails.
func TestProbeAgentCLIsIgnoresNonExecutableCodexBundle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the Codex Desktop app bundle fallback is macOS-only")
	}

	pathDir := t.TempDir()
	nested := filepath.Join(pathDir, "ChatGPT.app", "Contents", "Resources", "codex-cli", "bin", "codex")
	if err := os.MkdirAll(filepath.Dir(nested), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(nested, []byte("#!/bin/sh\nexit 0\n"), 0o644); err != nil {
		t.Fatalf("write non-executable fake CLI: %v", err)
	}

	oldBundlePaths := codexDesktopAppBundlePaths
	codexDesktopAppBundlePaths = func() []string { return []string{nested} }
	t.Cleanup(func() { codexDesktopAppBundlePaths = oldBundlePaths })

	t.Setenv("PATH", t.TempDir())
	t.Setenv("SHELL", filepath.Join(t.TempDir(), "fish"))
	pinNonCodexAgentsToMissingPaths(t)

	if _, found := probeAgentCLIs()["codex"]; found {
		t.Fatal("codex was registered from a non-executable app bundle path")
	}
}

// When both the nested CLI and the older flat binary exist, the nested path
// is the current ChatGPT.app layout and must win.
func TestLoadConfig_PrefersNestedChatGPTCodexCLIPath(t *testing.T) {
	pathDir := t.TempDir()
	nested := filepath.Join(pathDir, "ChatGPT.app", "Contents", "Resources", "codex-cli", "bin", "codex")
	flat := filepath.Join(pathDir, "ChatGPT.app", "Contents", "Resources", "codex")
	for _, p := range []string{nested, flat} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatalf("write fake CLI: %v", err)
		}
	}

	oldBundlePaths := codexDesktopAppBundlePaths
	codexDesktopAppBundlePaths = func() []string { return []string{nested, flat} }
	t.Cleanup(func() { codexDesktopAppBundlePaths = oldBundlePaths })

	t.Setenv("PATH", t.TempDir())
	t.Setenv("SHELL", filepath.Join(t.TempDir(), "fish"))
	t.Setenv("MULTICA_DAEMON_ID", "11111111-1111-1111-1111-111111111111")
	pinNonCodexAgentsToMissingPaths(t)

	cfg, err := LoadConfig(Overrides{
		ServerURL:      "http://localhost:0",
		WorkspacesRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	got, ok := cfg.Agents["codex"]
	if !ok {
		t.Fatalf("expected codex agent, got %#v", cfg.Agents)
	}
	if got.Path != nested {
		t.Fatalf("codex path = %q, want nested path %q", got.Path, nested)
	}
}

func TestCodexDesktopAppBundlePaths_IncludesChatGPTAndLegacy(t *testing.T) {
	paths := codexDesktopAppBundlePaths()
	const (
		nestedSuffix = "ChatGPT.app/Contents/Resources/codex-cli/bin/codex"
		flatSuffix   = "ChatGPT.app/Contents/Resources/codex"
		legacySuffix = "Codex.app/Contents/Resources/codex"
	)
	nestedIdx, flatIdx, legacyIdx := -1, -1, -1
	for i, p := range paths {
		slash := filepath.ToSlash(p)
		switch {
		case nestedIdx < 0 && strings.HasSuffix(slash, nestedSuffix):
			nestedIdx = i
		case flatIdx < 0 && strings.HasSuffix(slash, flatSuffix):
			flatIdx = i
		case legacyIdx < 0 && strings.HasSuffix(slash, legacySuffix):
			legacyIdx = i
		}
	}
	if nestedIdx < 0 || flatIdx < 0 || legacyIdx < 0 {
		t.Fatalf("codexDesktopAppBundlePaths missing nested, flat, or legacy entry: %#v", paths)
	}
	if nestedIdx > flatIdx || flatIdx > legacyIdx {
		t.Fatalf("expected nested ChatGPT path before flat ChatGPT path before Codex.app, got nested=%d flat=%d legacy=%d paths=%#v", nestedIdx, flatIdx, legacyIdx, paths)
	}
}

func TestLoadConfig_CodexDesktopFallbackDoesNotOverrideExplicitPath(t *testing.T) {
	pathDir := t.TempDir()
	fakeCodex := filepath.Join(pathDir, "Codex.app", "Contents", "Resources", "codex")
	if err := os.MkdirAll(filepath.Dir(fakeCodex), 0o755); err != nil {
		t.Fatalf("mkdir fake Codex bundle: %v", err)
	}
	if err := os.WriteFile(fakeCodex, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake Codex bundle CLI: %v", err)
	}

	oldBundlePaths := codexDesktopAppBundlePaths
	codexDesktopAppBundlePaths = func() []string { return []string{fakeCodex} }
	t.Cleanup(func() { codexDesktopAppBundlePaths = oldBundlePaths })

	t.Setenv("PATH", t.TempDir())
	t.Setenv("SHELL", filepath.Join(t.TempDir(), "fish"))
	t.Setenv("MULTICA_DAEMON_ID", "11111111-1111-1111-1111-111111111111")
	t.Setenv("MULTICA_CODEX_PATH", filepath.Join(t.TempDir(), "missing-codex"))
	pinNonCodexAgentsToMissingPaths(t)
	fakeClaude := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(fakeClaude, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	t.Setenv("MULTICA_CLAUDE_PATH", fakeClaude)

	cfg, err := LoadConfig(Overrides{
		ServerURL:      "http://localhost:0",
		WorkspacesRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got, ok := cfg.Agents["codex"]; ok {
		t.Fatalf("explicit missing MULTICA_CODEX_PATH should not fall back to Desktop bundle, got %#v", got)
	}
}

func pinNonCodexAgentsToMissingPaths(t *testing.T) {
	t.Helper()
	missingDir := t.TempDir()
	for _, name := range []string{
		"MULTICA_CLAUDE_PATH",
		"MULTICA_OPENCODE_PATH",
		"MULTICA_OPENCLAW_PATH",
		"MULTICA_HERMES_PATH",
		"MULTICA_PI_PATH",
		"MULTICA_CURSOR_PATH",
		"MULTICA_COPILOT_PATH",
		"MULTICA_KIMI_PATH",
		"MULTICA_REASONIX_PATH",
		"MULTICA_DSH_PATH",
		"MULTICA_KIRO_PATH",
		"MULTICA_GROK_PATH",
	} {
		t.Setenv(name, filepath.Join(missingDir, strings.ToLower(name)))
	}
}

// =============================================================================
// CLI config Backends.OpenClaw overrides (issue #3875)
// =============================================================================

// writeCLIConfigForProfile is a minimal helper for the override tests:
// stages a HOME, writes a config.json under the given profile (empty profile
// = default), and returns the resolved path so tests can assert against it.
func writeCLIConfigForProfile(t *testing.T, profile string, cfg cli.CLIConfig) {
	t.Helper()
	tmp := t.TempDir()
	redirectTestHome(t, tmp)
	if err := cli.SaveCLIConfigForProfile(cfg, profile); err != nil {
		t.Fatalf("write cli config: %v", err)
	}
}

// TestApplyOpenclawOverride_DoesNothingWhenNil verifies the early-return
// path. A daemon started with no override should not Setenv anything; the
// existing probe / spawn flow remains undisturbed.
func TestApplyOpenclawOverride_DoesNothingWhenNil(t *testing.T) {
	// Pre-set both env vars to known values; verify they survive untouched.
	t.Setenv("MULTICA_OPENCLAW_PATH", "/before/openclaw")
	t.Setenv("OPENCLAW_STATE_DIR", "/before/state")

	applyOpenclawOverride(nil)

	if got := os.Getenv("MULTICA_OPENCLAW_PATH"); got != "/before/openclaw" {
		t.Errorf("MULTICA_OPENCLAW_PATH mutated: got %q, want /before/openclaw", got)
	}
	if got := os.Getenv("OPENCLAW_STATE_DIR"); got != "/before/state" {
		t.Errorf("OPENCLAW_STATE_DIR mutated: got %q, want /before/state", got)
	}
}

// TestApplyOpenclawOverride_SetsBothWhenEnvUnset verifies the happy path:
// neither env var is set, the override has both fields, both env vars get
// set to the override values.
func TestApplyOpenclawOverride_SetsBothWhenEnvUnset(t *testing.T) {
	t.Setenv("MULTICA_OPENCLAW_PATH", "")
	t.Setenv("OPENCLAW_STATE_DIR", "")
	os.Unsetenv("MULTICA_OPENCLAW_PATH")
	os.Unsetenv("OPENCLAW_STATE_DIR")
	t.Cleanup(func() {
		os.Unsetenv("MULTICA_OPENCLAW_PATH")
		os.Unsetenv("OPENCLAW_STATE_DIR")
	})

	applyOpenclawOverride(&cli.OpenClawOverride{
		BinaryPath: "/from/config/openclaw",
		StateDir:   "/from/config/state",
	})

	if got := os.Getenv("MULTICA_OPENCLAW_PATH"); got != "/from/config/openclaw" {
		t.Errorf("MULTICA_OPENCLAW_PATH: got %q, want /from/config/openclaw", got)
	}
	if got := os.Getenv("OPENCLAW_STATE_DIR"); got != "/from/config/state" {
		t.Errorf("OPENCLAW_STATE_DIR: got %q, want /from/config/state", got)
	}
}

// TestApplyOpenclawOverride_EnvWinsOverConfig is the precedence test
// agreed with @YOMXXX in #3875 review: an env var set upstream by the user
// (shell export, launchctl, systemd unit) MUST take precedence over the
// config-file value. This is the back-compat contract — anyone with
// MULTICA_OPENCLAW_PATH already in their environment must not see the
// daemon silently change its meaning when they later add a config file.
func TestApplyOpenclawOverride_EnvWinsOverConfig(t *testing.T) {
	// User has already exported these in their shell.
	t.Setenv("MULTICA_OPENCLAW_PATH", "/from/env/openclaw")
	t.Setenv("OPENCLAW_STATE_DIR", "/from/env/state")

	applyOpenclawOverride(&cli.OpenClawOverride{
		BinaryPath: "/from/config/openclaw",
		StateDir:   "/from/config/state",
	})

	if got := os.Getenv("MULTICA_OPENCLAW_PATH"); got != "/from/env/openclaw" {
		t.Errorf("MULTICA_OPENCLAW_PATH: env should win, got %q want /from/env/openclaw", got)
	}
	if got := os.Getenv("OPENCLAW_STATE_DIR"); got != "/from/env/state" {
		t.Errorf("OPENCLAW_STATE_DIR: env should win, got %q want /from/env/state", got)
	}
}

// TestApplyOpenclawOverride_PartialFields_OnlySetsConfigured verifies that
// an override with only one field set leaves the other env var alone (does
// not Setenv to ""). This matters: a user who only configures state_dir
// must not have their MULTICA_OPENCLAW_PATH discovery path forcibly
// short-circuited to an empty string.
func TestApplyOpenclawOverride_PartialFields_OnlySetsConfigured(t *testing.T) {
	os.Unsetenv("MULTICA_OPENCLAW_PATH")
	os.Unsetenv("OPENCLAW_STATE_DIR")
	t.Cleanup(func() {
		os.Unsetenv("MULTICA_OPENCLAW_PATH")
		os.Unsetenv("OPENCLAW_STATE_DIR")
	})

	applyOpenclawOverride(&cli.OpenClawOverride{
		StateDir: "/from/config/state",
		// BinaryPath intentionally empty — must NOT call Setenv("MULTICA_OPENCLAW_PATH", "")
	})

	if _, set := os.LookupEnv("MULTICA_OPENCLAW_PATH"); set {
		t.Errorf("MULTICA_OPENCLAW_PATH should remain unset when BinaryPath is empty; got %q", os.Getenv("MULTICA_OPENCLAW_PATH"))
	}
	if got := os.Getenv("OPENCLAW_STATE_DIR"); got != "/from/config/state" {
		t.Errorf("OPENCLAW_STATE_DIR: got %q, want /from/config/state", got)
	}
}

// TestOpenclawOverrideFrom_NavigationCases verifies the nullable-pointer
// chain into Backends.OpenClaw. Three cases that all must safely return
// nil without panicking: nil Backends, nil OpenClaw inside Backends, and
// the happy path (returns the inner override unchanged).
func TestOpenclawOverrideFrom_NavigationCases(t *testing.T) {
	if got := openclawOverrideFrom(cli.CLIConfig{}); got != nil {
		t.Errorf("nil Backends should produce nil override, got %+v", got)
	}
	if got := openclawOverrideFrom(cli.CLIConfig{Backends: &cli.BackendOverrides{}}); got != nil {
		t.Errorf("nil OpenClaw inside Backends should produce nil override, got %+v", got)
	}
	want := &cli.OpenClawOverride{StateDir: "/x"}
	got := openclawOverrideFrom(cli.CLIConfig{Backends: &cli.BackendOverrides{OpenClaw: want}})
	if got != want {
		t.Errorf("happy path should return inner pointer; got %p want %p", got, want)
	}
}

// TestLoadConfig_AppliesBackendOverridesFromConfigFile is the integration
// test that ties commit 1's schema to commit 2's wire-up: write a config
// file with backends.openclaw.{binary_path,state_dir}, call LoadConfig
// (with no env vars set), and verify the openclaw probe picked up the
// configured BinaryPath and the OPENCLAW_STATE_DIR env var was injected.
func TestLoadConfig_AppliesBackendOverridesFromConfigFile(t *testing.T) {
	stageFakeAgent(t)
	// stageFakeAgent left "claude" on PATH; we also need a fake "openclaw"
	// at a custom path that the config file points at (mimicking a non-default
	// installation: another bundled / isolated / CI deployment, etc).
	customDir := t.TempDir()
	customOpenclaw := filepath.Join(customDir, "non-default-openclaw")
	if err := os.WriteFile(customOpenclaw, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake openclaw: %v", err)
	}

	// Make sure no env-var override is leaking in from the test runner.
	os.Unsetenv("MULTICA_OPENCLAW_PATH")
	os.Unsetenv("OPENCLAW_STATE_DIR")
	t.Cleanup(func() {
		os.Unsetenv("MULTICA_OPENCLAW_PATH")
		os.Unsetenv("OPENCLAW_STATE_DIR")
	})

	// Drop a CLI config under the user's HOME (already pointed at TempDir
	// by stageFakeAgent's t.Setenv chain — but reassert here for clarity).
	homeForCLIConfig := t.TempDir()
	redirectTestHome(t, homeForCLIConfig)
	cfg := cli.CLIConfig{
		ServerURL: "http://localhost:8080",
		Backends: &cli.BackendOverrides{
			OpenClaw: &cli.OpenClawOverride{
				BinaryPath: customOpenclaw,
				StateDir:   "/var/lib/openclaw-isolated",
			},
		},
	}
	if err := cli.SaveCLIConfig(cfg); err != nil {
		t.Fatalf("save cli config: %v", err)
	}

	loaded, err := LoadConfig(Overrides{
		ServerURL:      "http://localhost:8080",
		WorkspacesRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	openclaw, ok := loaded.Agents["openclaw"]
	if !ok {
		t.Fatalf("agents map missing 'openclaw' key; got keys=%v", agentKeys(loaded.Agents))
	}
	if openclaw.Path != customOpenclaw {
		t.Errorf("openclaw.Path: got %q, want %q (the binary configured in CLI config)", openclaw.Path, customOpenclaw)
	}
	if got := os.Getenv("OPENCLAW_STATE_DIR"); got != "/var/lib/openclaw-isolated" {
		t.Errorf("OPENCLAW_STATE_DIR: got %q, want injected from config", got)
	}
}

// TestLoadConfig_BackendOverrides_BackwardCompat_NoConfigFile verifies that
// the override mechanism is purely additive: a daemon started without any
// CLI config file (or with an empty one) behaves identically to before
// commit 1 — agents discovered from PATH, no env injection.
func TestLoadConfig_BackendOverrides_BackwardCompat_NoConfigFile(t *testing.T) {
	stageFakeAgent(t)

	// Point HOME at an empty dir — no config.json present.
	redirectTestHome(t, t.TempDir())
	os.Unsetenv("MULTICA_OPENCLAW_PATH")
	os.Unsetenv("OPENCLAW_STATE_DIR")
	t.Cleanup(func() {
		os.Unsetenv("MULTICA_OPENCLAW_PATH")
		os.Unsetenv("OPENCLAW_STATE_DIR")
	})

	_, err := LoadConfig(Overrides{
		ServerURL:      "http://localhost:8080",
		WorkspacesRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("LoadConfig with no config file should not fail: %v", err)
	}

	if _, set := os.LookupEnv("OPENCLAW_STATE_DIR"); set {
		t.Errorf("OPENCLAW_STATE_DIR should remain unset when no config file is present; got %q", os.Getenv("OPENCLAW_STATE_DIR"))
	}
}

// TestLoadConfig_BackendOverrides_MalformedConfigFileNonFatal verifies the
// fail-soft contract documented inline in LoadConfig: a corrupt config.json
// must not prevent daemon startup. This matters for diskcorruption /
// partial-write recovery — the daemon should log and proceed using
// env-var-only configuration.
func TestLoadConfig_BackendOverrides_MalformedConfigFileNonFatal(t *testing.T) {
	stageFakeAgent(t)
	homeDir := t.TempDir()
	redirectTestHome(t, homeDir)

	// Write malformed JSON.
	cfgDir := filepath.Join(homeDir, ".multica")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte("{not valid json"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := LoadConfig(Overrides{
		ServerURL:      "http://localhost:8080",
		WorkspacesRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("LoadConfig should not fail on malformed config.json: %v", err)
	}
	// Should also have logged a slog Warn — we don't assert on the log
	// output here (avoids brittle string matching), but the build does
	// make sure log/slog stays imported.
}

// agentKeys is a tiny helper to make agent-map missing-key error messages
// readable. Returns sorted keys.
func agentKeys(m map[string]AgentEntry) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// TestApplyOpenclawOverride_CLITimeout covers the #7112 knob on the same
// precedence contract as binary_path / state_dir: the config file supplies it
// when the environment does not, and an environment value the user exported
// upstream always wins.
func TestApplyOpenclawOverride_CLITimeout(t *testing.T) {
	t.Run("config file supplies the value", func(t *testing.T) {
		os.Unsetenv(execenv.OpenclawCLITimeoutEnv)
		t.Cleanup(func() { os.Unsetenv(execenv.OpenclawCLITimeoutEnv) })

		applyOpenclawOverride(&cli.OpenClawOverride{CLITimeout: "45s"})

		if got := os.Getenv(execenv.OpenclawCLITimeoutEnv); got != "45s" {
			t.Errorf("%s: got %q, want 45s", execenv.OpenclawCLITimeoutEnv, got)
		}
	})

	t.Run("env wins over config", func(t *testing.T) {
		t.Setenv(execenv.OpenclawCLITimeoutEnv, "20s")

		applyOpenclawOverride(&cli.OpenClawOverride{CLITimeout: "45s"})

		if got := os.Getenv(execenv.OpenclawCLITimeoutEnv); got != "20s" {
			t.Errorf("%s: env should win, got %q want 20s", execenv.OpenclawCLITimeoutEnv, got)
		}
	})

	t.Run("unset field leaves the env alone", func(t *testing.T) {
		os.Unsetenv(execenv.OpenclawCLITimeoutEnv)
		t.Cleanup(func() { os.Unsetenv(execenv.OpenclawCLITimeoutEnv) })

		applyOpenclawOverride(&cli.OpenClawOverride{StateDir: "/from/config/state"})

		if _, set := os.LookupEnv(execenv.OpenclawCLITimeoutEnv); set {
			t.Errorf("%s must not be set when the field is empty", execenv.OpenclawCLITimeoutEnv)
		}
	})
}

// TestManagedConfigRejectsMissingOrOversizedTokenFile pins the fail-closed
// enrollment-secret file contract: absolute path, regular non-symlink file,
// owner-only permissions, bounded size, and an mse_<40 hex> body.
func TestManagedConfigRejectsMissingOrOversizedTokenFile(t *testing.T) {
	fakeClaude := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(fakeClaude, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	t.Setenv("MULTICA_CLAUDE_PATH", fakeClaude)
	t.Setenv("MULTICA_DAEMON_ID", "")
	t.Setenv("MULTICA_LAUNCHED_BY", "")

	dir := t.TempDir()
	writeMode := func(name, content string, mode os.FileMode) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatalf("chmod %s: %v", name, err)
		}
		return path
	}

	missing := filepath.Join(dir, "absent")
	directory := filepath.Join(dir, "as-directory")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	oversized := writeMode("oversized", strings.Repeat("a", 257), 0o600)
	groupReadable := writeMode("group-readable", testManagedEnrollmentToken, 0o644)
	notOwnerReadable := writeMode("not-owner-readable", testManagedEnrollmentToken, 0o000)
	badContent := writeMode("bad-content", "not-an-enrollment-token", 0o600)
	target := writeMode("symlink-target", testManagedEnrollmentToken, 0o600)
	symlink := filepath.Join(dir, "symlink")
	if err := os.Symlink(target, symlink); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	valid := writeMode("valid", testManagedEnrollmentToken+"\n", 0o600)
	// Managed startup also requires the four provider credential files.
	stageManagedProviderSecrets(t)

	cases := map[string]string{
		"missing path":       "",
		"relative path":      filepath.Join("relative", "token"),
		"absent file":        missing,
		"directory":          directory,
		"oversized":          oversized,
		"group readable":     groupReadable,
		"not owner readable": notOwnerReadable,
		"symlink":            symlink,
		"wrong content":      badContent,
	}
	for name, tokenFile := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := LoadConfig(Overrides{
				Managed:                    true,
				ManagedEnrollmentTokenFile: tokenFile,
				Foreground:                 true,
				ServerURL:                  "http://localhost:0",
				WorkspacesRoot:             t.TempDir(),
			})
			if err == nil {
				t.Fatalf("LoadConfig(managed) with %s token file = nil, want error", name)
			}
		})
	}

	t.Run("valid file loads managed config", func(t *testing.T) {
		cfg, err := LoadConfig(Overrides{
			Managed:                    true,
			ManagedEnrollmentTokenFile: valid,
			Foreground:                 true,
			ServerURL:                  "http://localhost:0",
			WorkspacesRoot:             t.TempDir(),
		})
		if err != nil {
			t.Fatalf("LoadConfig(managed) = %v", err)
		}
		if !cfg.Managed.Enabled {
			t.Error("managed config not enabled")
		}
		if cfg.Managed.EnrollmentTokenFile != valid {
			t.Errorf("managed enrollment token file = %q, want %q", cfg.Managed.EnrollmentTokenFile, valid)
		}
	})
}

// TestManagedConfigRejectsWorkstationIdentity keeps a managed process from
// inheriting a workstation profile, daemon id, desktop launch, or background
// start.
func TestManagedConfigRejectsWorkstationIdentity(t *testing.T) {
	fakeClaude := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(fakeClaude, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	t.Setenv("MULTICA_CLAUDE_PATH", fakeClaude)
	t.Setenv("MULTICA_DAEMON_ID", "")
	t.Setenv("MULTICA_LAUNCHED_BY", "")
	valid := writeManagedTokenFile(t, testManagedEnrollmentToken)

	base := func() Overrides {
		return Overrides{
			Managed:                    true,
			ManagedEnrollmentTokenFile: valid,
			Foreground:                 true,
			ServerURL:                  "http://localhost:0",
			WorkspacesRoot:             t.TempDir(),
		}
	}

	t.Run("local profile", func(t *testing.T) {
		overrides := base()
		overrides.Profile = "workstation"
		if _, err := LoadConfig(overrides); err == nil {
			t.Fatal("managed mode accepted a local profile")
		}
	})

	t.Run("daemon id override", func(t *testing.T) {
		overrides := base()
		overrides.DaemonID = "11111111-1111-1111-1111-111111111111"
		if _, err := LoadConfig(overrides); err == nil {
			t.Fatal("managed mode accepted an explicit daemon id")
		}
	})

	t.Run("daemon id env", func(t *testing.T) {
		t.Setenv("MULTICA_DAEMON_ID", "11111111-1111-1111-1111-111111111111")
		if _, err := LoadConfig(base()); err == nil {
			t.Fatal("managed mode accepted MULTICA_DAEMON_ID")
		}
	})

	t.Run("desktop launch", func(t *testing.T) {
		t.Setenv("MULTICA_LAUNCHED_BY", "desktop")
		if _, err := LoadConfig(base()); err == nil {
			t.Fatal("managed mode accepted a desktop launch")
		}
	})

	t.Run("background start", func(t *testing.T) {
		overrides := base()
		overrides.Foreground = false
		if _, err := LoadConfig(overrides); err == nil {
			t.Fatal("managed mode accepted a background start")
		}
	})
}
