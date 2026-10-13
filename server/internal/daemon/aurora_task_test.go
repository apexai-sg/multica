package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Fixed UUIDs so the task shape matches what the claim endpoint forwards.
const (
	auroraTaskTestID          = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	auroraTaskTestGeneration  = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	auroraTaskTestWorkspaceID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	auroraTaskTestAgentID     = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	auroraTaskTestSkill       = "text-image"
)

// auroraTaskTestTask is a claimed Aurora system-agent run: the trusted skill
// identity lives in the system key, never in the generation prompt.
func auroraTaskTestTask() Task {
	return Task{
		ID:                auroraTaskTestID,
		GenerationID:      auroraTaskTestGeneration,
		WorkspaceID:       auroraTaskTestWorkspaceID,
		RuntimeID:         "rt-aurora",
		AgentID:           auroraTaskTestAgentID,
		AuthToken:         "mat_aurora_test",
		QuickCreatePrompt: "a poster about clouds",
		Agent: &AgentData{
			ID:        auroraTaskTestAgentID,
			Name:      "aurora-text-image",
			SystemKey: "aurora:" + auroraTaskTestSkill,
			Skills:    []SkillData{{ID: "text-image-skill", Source: "workspace", Name: "文字生成图片", Content: "# Text to Image\nCreate the requested image and artifact manifest."}},
			// An agent-level MCP server is an ordinary feature. An Aurora task
			// must keep it exactly as any other task does.
			McpConfig: json.RawMessage(`{"mcpServers":{"team-tools":{"command":"node","args":["server.js"]}}}`),
		},
	}
}

// newAuroraTaskTestDaemon builds a daemon whose claude entry is a fake CLI that
// records its argv and the file named by --mcp-config. That is the seam which
// lets a test read the exact launch options runTask assembled, without running
// a real agent.
func newAuroraTaskTestDaemon(t *testing.T) (*Daemon, string, string, func()) {
	t.Helper()

	testDir := t.TempDir()
	fakeBin := filepath.Join(testDir, "claude")
	argsFile := filepath.Join(testDir, "claude-args.txt")
	mcpFile := filepath.Join(testDir, "claude-mcp.json")
	script := "#!/bin/sh\n" +
		"{\n" +
		"  for arg in \"$@\"; do echo \"$arg\"; done\n" +
		"  echo --invocation-end--\n" +
		"} >> \"" + argsFile + "\"\n" +
		"mcp=\"\"\n" +
		"prev=\"\"\n" +
		"for arg in \"$@\"; do\n" +
		"  if [ \"$prev\" = \"--mcp-config\" ]; then mcp=\"$arg\"; fi\n" +
		"  prev=\"$arg\"\n" +
		"done\n" +
		"[ -n \"$mcp\" ] && cp \"$mcp\" \"" + mcpFile + "\"\n" +
		"IFS= read -r _\n" +
		"echo '{\"type\":\"system\",\"session_id\":\"s\"}'\n" +
		"echo '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"session_id\":\"s\",\"result\":\"done\"}'\n"
	writeTestExecutable(t, fakeBin, []byte(script))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("{}"))
	}))

	d := &Daemon{
		client:         NewClient(srv.URL),
		logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		workspaces:     make(map[string]*workspaceState),
		runtimeIndex:   map[string]Runtime{"rt-aurora": {ID: "rt-aurora", Provider: "claude"}},
		activeEnvRoots: make(map[string]int),
		cfg: Config{
			WorkspacesRoot: t.TempDir(),
			// The fake CLI answers immediately; a tight budget only adds a
			// timeout-shaped flake when the machine is busy.
			AgentTimeout:  60 * time.Second,
			ServerBaseURL: "https://api.aurora.example.test",
			Agents: map[string]AgentEntry{
				"claude": {Path: fakeBin},
			},
		},
	}
	return d, argsFile, mcpFile, srv.Close
}

func readRecordedArgv(t *testing.T, argsFile string) []string {
	t.Helper()
	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read recorded argv: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

// flagValue returns the argument that follows flag, or "" when flag is absent.
func flagValue(argv []string, flag string) string {
	for i, arg := range argv {
		if arg == flag && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	return ""
}

func TestIsAuroraTask(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		agent      *AgentData
		wantAurora bool
	}{
		{name: "aurora system agent", agent: &AgentData{SystemKey: "aurora:image"}, wantAurora: true},
		{name: "aurora skill key", agent: &AgentData{SystemKey: "aurora:video-generate"}, wantAurora: true},
		{name: "mika built-in", agent: &AgentData{SystemKey: "mika"}, wantAurora: false},
		{name: "no system key", agent: &AgentData{}, wantAurora: false},
		{name: "no agent", agent: nil, wantAurora: false},
		{name: "prefix only is not aurora", agent: &AgentData{SystemKey: "aurorafoo"}, wantAurora: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			task := Task{Agent: tc.agent}
			if got := isAuroraTask(task); got != tc.wantAurora {
				t.Fatalf("isAuroraTask() = %v, want %v", got, tc.wantAurora)
			}
		})
	}
}

// Missing required skill documents fail before any provider CLI is launched.
func TestAuroraTaskMissingDocumentDoesNotLaunchCLI(t *testing.T) {
	for _, ordinaryIssue := range []bool{false, true} {
		t.Run(fmt.Sprintf("ordinary_issue=%v", ordinaryIssue), func(t *testing.T) {
			d, argsFile, _, cleanup := newAuroraTaskTestDaemon(t)
			defer cleanup()
			task := auroraTaskTestTask()
			if ordinaryIssue {
				task.GenerationID = ""
				task.QuickCreatePrompt = ""
				task.IssueID = "ordinary-assigned-issue"
			}
			task.Agent.Skills = nil
			_, err := d.runTask(context.Background(), task, "claude", 0, d.logger)
			if err == nil || !strings.Contains(err.Error(), "required Aurora skill document") {
				t.Fatalf("expected readable missing-document error, got %v", err)
			}
			if _, err := os.Stat(argsFile); !os.IsNotExist(err) {
				t.Fatalf("CLI launched without required document: %v", err)
			}
		})
	}
}

// Aurora still gets the ordinary tool surface; its delivered skill document
// guides execution rather than a broker, tool allowlist or turn cap.
func TestAuroraTaskGetsTheOrdinaryExecutionSurface(t *testing.T) {
	t.Parallel()

	d, argsFile, mcpFile, cleanup := newAuroraTaskTestDaemon(t)
	defer cleanup()

	task := auroraTaskTestTask()
	result, err := d.runTask(context.Background(), task, "claude", 0, d.logger)
	if err != nil {
		t.Fatalf("runTask: %v", err)
	}
	if result.Status != "completed" {
		t.Fatalf("result status = %q, want completed: %+v", result.Status, result)
	}

	argv := readRecordedArgv(t, argsFile)
	joined := strings.Join(argv, " ")

	if flagValue(argv, "--max-turns") != "" {
		t.Errorf("aurora task still gets a turn cap:\n%s", joined)
	}
	if flagValue(argv, "--allowedTools") != "" {
		t.Errorf("aurora task still gets a tool allowlist:\n%s", joined)
	}
	// The only denied tool must be the Claude backend's own default. A deny list
	// that still names the general-purpose tools would take the shell, the file
	// tools and the network away from a skill that now needs them.
	if got := flagValue(argv, "--disallowedTools"); got != "AskUserQuestion" {
		t.Errorf("--disallowedTools = %q, want only the backend default", got)
	}
	// Empty PermissionMode preserves the provider default, which for Claude is
	// bypassPermissions. The narrowed surface used to force "default" here.
	if got := flagValue(argv, "--permission-mode"); got != "bypassPermissions" {
		t.Errorf("--permission-mode = %q, want the ordinary provider default", got)
	}

	// The broker no longer replaces the agent's MCP configuration. Assert on the
	// broker's own markers rather than on a server key alone: the merged config
	// also carries whatever the host machine has configured, so a key-name
	// assertion would be host-dependent.
	mcpRaw, err := os.ReadFile(mcpFile)
	if err != nil {
		t.Fatalf("no --mcp-config reached the launch: %v", err)
	}
	for _, brokerMarker := range []string{"server.mjs", "AURORA_TASK_CONTEXT_FILE", "aurora-broker"} {
		if strings.Contains(string(mcpRaw), brokerMarker) {
			t.Errorf("the merged MCP config still carries the broker marker %q:\n%s", brokerMarker, mcpRaw)
		}
	}
	if !strings.Contains(string(mcpRaw), `"team-tools"`) {
		t.Errorf("the agent's own MCP server was dropped:\n%s", mcpRaw)
	}
}

// TestAttachAuroraArtifactsNeedsInputDoesNotBlock is the daemon half of the
// question-round exit: a manifest that declares outcome needs_input with no
// artifacts leaves the result completed (not blocked) and carries the outcome so
// the report happens before the terminal callback.
func TestAttachAuroraArtifactsNeedsInputDoesNotBlock(t *testing.T) {
	outputRoot := t.TempDir()
	sandboxRoot := t.TempDir()
	manifest := auroraTestManifest()
	manifest.Outcome = auroraOutcomeNeedsInput
	writeAuroraTestManifest(t, outputRoot, manifest)

	task := Task{
		ID:    auroraTestTaskID,
		Agent: &AgentData{SystemKey: "aurora:" + auroraTestSkill},
	}
	result := TaskResult{Status: "completed"}
	d := &Daemon{}
	d.attachAuroraArtifacts(context.Background(), task, &result, slog.Default(), sandboxRoot, outputRoot)

	if result.Status != "completed" {
		t.Fatalf("status = %q, want completed: a question round must not be blocked", result.Status)
	}
	if result.Outcome != auroraOutcomeNeedsInput {
		t.Fatalf("outcome = %q, want %q", result.Outcome, auroraOutcomeNeedsInput)
	}
	if len(result.Artifacts) != 0 {
		t.Fatalf("artifacts = %d, want 0", len(result.Artifacts))
	}
}
