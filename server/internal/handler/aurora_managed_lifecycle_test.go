package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/aurora"
	"github.com/multica-ai/multica/server/internal/daemon"
	"github.com/multica-ai/multica/server/internal/daemon/execenv"
	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// init serves the private execution-environment helper protocol. The real
// managed daemon runs execenv.Prepare/Reuse in a short-lived child of its own
// binary; when the daemon object is started from this test binary, that child
// is this same test binary. The helper must answer before the Go test framework
// parses arguments and runs the suite, or the daemon would receive a nested test
// run's output instead of a prepared environment.
func init() {
	if len(os.Args) == 2 && os.Args[1] == execenv.PreparationHelperArg {
		logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
		if err := execenv.RunPreparationHelper(os.Stdin, os.Stdout, logger); err != nil {
			fmt.Fprintln(os.Stderr, "execenv preparation helper:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
}

// managedClaimObservation is the parsed body of one daemon claim request.
type managedClaimObservation struct {
	DaemonID   string   `json:"daemon_id"`
	RuntimeIDs []string `json:"runtime_ids"`
	MaxTasks   int      `json:"max_tasks"`
}

// managedLifecycleObservation records the control-plane traffic a managed
// daemon emits so the test can assert enrollment, claim, and execution-start
// shape without reaching into daemon internals.
type managedLifecycleObservation struct {
	mu          sync.Mutex
	enrollCount int
	startCount  int
	claimBodies []managedClaimObservation
}

func (o *managedLifecycleObservation) recordEnroll() {
	o.mu.Lock()
	o.enrollCount++
	o.mu.Unlock()
}

func (o *managedLifecycleObservation) recordStart() {
	o.mu.Lock()
	o.startCount++
	o.mu.Unlock()
}

func (o *managedLifecycleObservation) recordClaim(body managedClaimObservation) {
	o.mu.Lock()
	o.claimBodies = append(o.claimBodies, body)
	o.mu.Unlock()
}

func (o *managedLifecycleObservation) snapshot() (int, int, []managedClaimObservation) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.enrollCount, o.startCount, append([]managedClaimObservation(nil), o.claimBodies...)
}

// handleClaim records one claim body before delegating to the real handler.
func (o *managedLifecycleObservation) handleClaim(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read claim body", http.StatusInternalServerError)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		var parsed managedClaimObservation
		if err := json.Unmarshal(body, &parsed); err == nil {
			o.recordClaim(parsed)
		}
		next(w, r)
	}
}

func (o *managedLifecycleObservation) handleStart(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		o.recordStart()
		next(w, r)
	}
}

// router serves exactly the daemon control-plane routes the managed lifecycle
// uses, bound to the production handler and production DaemonAuth middleware.
func (o *managedLifecycleObservation) router() http.Handler {
	r := chi.NewRouter()
	r.Post("/api/daemon/managed/enroll", func(w http.ResponseWriter, req *http.Request) {
		o.recordEnroll()
		testHandler.ManagedRuntimeEnroll(w, req)
	})
	r.Route("/api/daemon", func(r chi.Router) {
		r.Use(middleware.DaemonAuth(db.New(testPool), nil, nil, nil))
		r.Post("/managed/shutdown", testHandler.ManagedRuntimeShutdown)
		r.Post("/heartbeat", testHandler.DaemonHeartbeat)
		r.Post("/tasks/claim", o.handleClaim(testHandler.ClaimTasksByRuntime))
		r.Post("/claim", o.handleClaim(testHandler.ClaimTasksByRuntime))
		r.Get("/tasks/{taskId}/status", testHandler.GetTaskStatus)
		r.Post("/tasks/{taskId}/start", o.handleStart(testHandler.StartTask))
		r.Post("/tasks/{taskId}/wait-local-directory", testHandler.MarkTaskWaitingLocalDirectory)
		r.Post("/tasks/{taskId}/progress", testHandler.ReportTaskProgress)
		r.Post("/tasks/{taskId}/complete", testHandler.CompleteTask)
		r.Post("/tasks/{taskId}/fail", testHandler.FailTask)
		r.Post("/tasks/{taskId}/usage", testHandler.ReportTaskUsage)
		r.Post("/tasks/{taskId}/artifacts", testHandler.ReportTaskArtifacts)
		r.Post("/tasks/{taskId}/messages", testHandler.ReportTaskMessages)
		r.Post("/tasks/{taskId}/cancel-ack", testHandler.AckTaskCancelled)
		r.Post("/tasks/{taskId}/session", testHandler.PinTaskSession)
		r.Get("/tasks/{taskId}/gc-check", testHandler.GetTaskGCCheck)
		r.Post("/runtimes/{runtimeId}/tasks/{taskId}/prepare-lease", testHandler.ExtendTaskPrepareLease)
		r.Post("/runtimes/{runtimeId}/tasks/{taskId}/skill-bundles/resolve", testHandler.ResolveTaskSkillBundles)
		r.Post("/runtimes/{runtimeId}/recover-orphans", testHandler.RecoverOrphanedTasks)
	})
	return r
}

// freeLoopbackPort reserves an ephemeral loopback port for the daemon health
// listener and releases it immediately, so each -count run binds its own port.
func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve loopback port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// writeFakeClaude writes the minimum supported successful Claude stream. It
// records each invocation so the test can prove the provider executed exactly
// once and no user-installed CLI was resolved.
func writeFakeClaude(t *testing.T, dir, countPath string) string {
	t.Helper()
	path := filepath.Join(dir, "claude-fake")
	const scriptTemplate = `#!/bin/sh
echo run >> __COUNT__
IFS= read -r _
echo '{"type":"system","session_id":"session-managed-lifecycle"}'
echo '{"type":"result","subtype":"success","is_error":false,"session_id":"session-managed-lifecycle","result":"done"}'
`
	script := strings.ReplaceAll(scriptTemplate, "__COUNT__", countPath)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	return path
}

func fakeExecutionCount(t *testing.T, countPath string) int {
	t.Helper()
	data, err := os.ReadFile(countPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("read fake execution count: %v", err)
	}
	return strings.Count(string(data), "run")
}

// managedHealthPayload is the subset of the daemon's local /health response the
// test gates startup on.
type managedHealthPayload struct {
	Status  string `json:"status"`
	Managed *struct {
		Ready           bool       `json:"ready"`
		LastHeartbeatAt *time.Time `json:"last_heartbeat_at"`
	} `json:"managed"`
}

// waitForManagedHealth waits for enrollment plus one acknowledged heartbeat,
// the condition that turns a managed daemon's /health from starting to running.
// It never assumes a fixed sleep is long enough.
func waitForManagedHealth(t *testing.T, port int, runDone <-chan struct{}, timeout time.Duration) managedHealthPayload {
	t.Helper()
	url := fmt.Sprintf("http://127.0.0.1:%d/health", port)
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-runDone:
			t.Fatalf("managed daemon exited before becoming ready")
		default:
		}
		resp, err := client.Get(url)
		if err == nil {
			var payload managedHealthPayload
			decodeErr := json.NewDecoder(resp.Body).Decode(&payload)
			resp.Body.Close()
			if decodeErr == nil && payload.Status == "running" && payload.Managed != nil && payload.Managed.Ready {
				return payload
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for managed daemon health at %s", timeout, url)
		}
		<-ticker.C
	}
}

// waitForTaskStatus polls the observable task row until it reaches want, with a
// bounded deadline and diagnostics on failure.
func waitForTaskStatus(t *testing.T, taskID, want string, timeout time.Duration, diagnostics func()) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	last := ""
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := testPool.QueryRow(ctx, `SELECT status FROM agent_task_queue WHERE id = $1`, taskID).Scan(&last)
		cancel()
		if err == nil && last == want {
			return
		}
		if last == "failed" || last == "cancelled" {
			if diagnostics != nil {
				diagnostics()
			}
			t.Fatalf("task %s reached terminal status %q, want %q", taskID, last, want)
		}
		if time.Now().After(deadline) {
			if diagnostics != nil {
				diagnostics()
			}
			t.Fatalf("timed out after %s waiting for task %s status %q (last %q)", timeout, taskID, want, last)
		}
		<-ticker.C
	}
}

// waitForManagedShutdown waits until graceful shutdown has released the node,
// deflected the runtime, and deleted the daemon credential.
func waitForManagedShutdown(t *testing.T, workspaceID, runtimeID string, timeout time.Duration, diagnostics func()) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	last := ""
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		var runtimeStatus string
		var runtimeDaemon pgtype.Text
		err := testPool.QueryRow(ctx, `SELECT status, daemon_id FROM agent_runtime WHERE id = $1 AND workspace_id = $2`, runtimeID, workspaceID).Scan(&runtimeStatus, &runtimeDaemon)
		var nodeState string
		nodeErr := testPool.QueryRow(ctx, `SELECT state FROM aurora_sandbox_node WHERE workspace_id = $1 AND runtime_id = $2`, workspaceID, runtimeID).Scan(&nodeState)
		var tokenCount int
		tokenErr := testPool.QueryRow(ctx, `SELECT count(*) FROM daemon_token WHERE workspace_id = $1`, workspaceID).Scan(&tokenCount)
		cancel()
		last = fmt.Sprintf("runtime_status=%q runtime_daemon_valid=%t node_state=%q tokens=%d", runtimeStatus, runtimeDaemon.Valid, nodeState, tokenCount)
		if err == nil && nodeErr == nil && tokenErr == nil && runtimeStatus == "offline" && !runtimeDaemon.Valid && nodeState == "stopped" && tokenCount == 0 {
			return
		}
		if time.Now().After(deadline) {
			if diagnostics != nil {
				diagnostics()
			}
			t.Fatalf("timed out after %s waiting for managed shutdown (%s)", timeout, last)
		}
		<-ticker.C
	}
}

// managedSecretPattern matches a live daemon credential or enrollment secret.
// Timeout diagnostics must stay useful without ever printing one.
var managedSecretPattern = regexp.MustCompile(`m[ds][te]_[A-Za-z0-9]+`)

// logManagedLifecycleState prints identifiers and status only. It must never
// print a token, an enrollment secret, or a prompt.
func logManagedLifecycleState(t *testing.T, workspaceID, runtimeID, taskID string, healthPort int, daemonLog string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var taskStatus, taskAgent, taskRuntime string
	_ = testPool.QueryRow(ctx, `SELECT status, agent_id::text, runtime_id::text FROM agent_task_queue WHERE id = $1`, taskID).Scan(&taskStatus, &taskAgent, &taskRuntime)
	var runtimeStatus, runtimeProvider string
	var runtimeDaemon pgtype.Text
	_ = testPool.QueryRow(ctx, `SELECT status, provider, daemon_id FROM agent_runtime WHERE id = $1`, runtimeID).Scan(&runtimeStatus, &runtimeProvider, &runtimeDaemon)
	var nodeState string
	var nodeWorkspace, nodeRuntime, nodeDaemon string
	_ = testPool.QueryRow(ctx, `SELECT state, workspace_id::text, runtime_id::text, daemon_id FROM aurora_sandbox_node WHERE workspace_id = $1`, workspaceID).Scan(&nodeState, &nodeWorkspace, &nodeRuntime, &nodeDaemon)
	var tokenCount int
	_ = testPool.QueryRow(ctx, `SELECT count(*) FROM daemon_token WHERE workspace_id = $1`, workspaceID).Scan(&tokenCount)
	health := ""
	if resp, err := (&http.Client{Timeout: 2 * time.Second}).Get(fmt.Sprintf("http://127.0.0.1:%d/health", healthPort)); err == nil {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		health = string(body)
	}
	t.Logf("managed lifecycle diagnostics: task_status=%q task_agent=%q task_runtime=%q runtime_status=%q runtime_provider=%q runtime_daemon_valid=%t node_state=%q node_workspace=%q node_runtime=%q node_daemon=%q tokens=%d daemon_health=%s\ndaemon log:\n%s",
		taskStatus, taskAgent, taskRuntime, runtimeStatus, runtimeProvider, runtimeDaemon.Valid, nodeState, nodeWorkspace, nodeRuntime, nodeDaemon, tokenCount,
		managedSecretPattern.ReplaceAllString(health, "[redacted]"), managedSecretPattern.ReplaceAllString(daemonLog, "[redacted]"))
}

// TestAuroraManagedDaemonEnrollsClaimsAndCompletesWithFakeClaude is the
// canonical non-provider end-to-end regression for the managed sandbox
// control plane: a real daemon object enrolls a scoped runtime, claims exactly
// the Aurora quick-create task on it, executes a test-created fake Claude
// executable, reports completion to the real server, and releases the node and
// credential on shutdown.
func TestAuroraManagedDaemonEnrollsClaimsAndCompletesWithFakeClaude(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	withSandboxEnrollment(t, newSandboxEnrollmentService())
	ctx := context.Background()

	// A throwaway workspace keeps the one-managed-runtime, one-node, and
	// one-daemon indexes from colliding with other handler tests that seed the
	// shared fixture workspace.
	ownerID := dbfx.User(t, "Aurora managed lifecycle owner", "aurora-managed-lifecycle-"+uuid.NewString()+"@example.com")
	workspaceID := dbfx.Workspace(t, "Aurora managed lifecycle", "aurora-managed-lifecycle-"+uuid.NewString())
	dbfx.Member(t, workspaceID, ownerID, "owner")
	workspaceUUID := parseUUID(workspaceID)
	ownerUUID := parseUUID(ownerID)

	if err := aurora.EnsureSystemAgents(ctx, testHandler.Queries, workspaceUUID, ownerUUID); err != nil {
		t.Fatalf("seed Aurora system agents: %v", err)
	}
	runtimeUUID, err := aurora.ManagedRuntimeID(ctx, testHandler.Queries, workspaceUUID)
	if err != nil {
		t.Fatalf("load managed runtime: %v", err)
	}
	runtimeID := uuidToString(runtimeUUID)

	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM aurora_generation WHERE workspace_id = $1`, workspaceID)
		testPool.Exec(ctx, `DELETE FROM daemon_token WHERE workspace_id = $1`, workspaceID)
		testPool.Exec(ctx, `DELETE FROM aurora_sandbox_node WHERE workspace_id = $1`, workspaceID)
		testPool.Exec(ctx, `DELETE FROM task_token WHERE task_id IN (SELECT id FROM agent_task_queue WHERE agent_id IN (SELECT id FROM agent WHERE workspace_id = $1))`, workspaceID)
		testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE agent_id IN (SELECT id FROM agent WHERE workspace_id = $1)`, workspaceID)
		testPool.Exec(ctx, `DELETE FROM agent_skill WHERE agent_id IN (SELECT id FROM agent WHERE workspace_id = $1)`, workspaceID)
		testPool.Exec(ctx, `DELETE FROM agent WHERE workspace_id = $1`, workspaceID)
		testPool.Exec(ctx, `DELETE FROM skill WHERE workspace_id = $1`, workspaceID)
		testPool.Exec(ctx, `DELETE FROM agent_runtime WHERE workspace_id = $1`, workspaceID)
	})

	// The quick-create task inherits the managed runtime, so it is exactly the
	// work a managed sandbox node is supposed to claim.
	systemAgent, err := testHandler.Queries.GetAgentBySystemKey(ctx, db.GetAgentBySystemKeyParams{
		WorkspaceID: workspaceUUID,
		SystemKey:   pgtype.Text{String: "aurora:ecommerce-bidding-strategy", Valid: true},
	})
	if err != nil {
		t.Fatalf("load Aurora system agent: %v", err)
	}
	task, err := testHandler.TaskService.EnqueueQuickCreateTask(ctx, workspaceUUID, ownerUUID, systemAgent.ID, pgtype.UUID{}, "A poster of a lighthouse at dusk", "high", "", pgtype.UUID{}, pgtype.UUID{}, nil)
	if err != nil {
		t.Fatalf("enqueue Aurora quick-create task: %v", err)
	}
	taskID := uuidToString(task.ID)
	if task.RuntimeID != runtimeUUID {
		t.Fatalf("quick-create task runtime = %s, want managed runtime %s", uuidToString(task.RuntimeID), runtimeID)
	}
	// The claim payload resolves the real generation id from this row and the
	// daemon fails an Aurora task whose generation id is empty, so the fixture
	// must carry the generation the production create path would have written.
	if _, err := testPool.Exec(ctx, `
		INSERT INTO aurora_generation (workspace_id, user_id, skill_id, prompt, status, task_id)
		VALUES ($1, $2, 'ecommerce-bidding-strategy', $3, 'queued', $4)
	`, workspaceID, ownerID, "A poster of a lighthouse at dusk", taskID); err != nil {
		t.Fatalf("seed aurora generation: %v", err)
	}

	// Issue the single-use enrollment secret through the real service and stage
	// it where the daemon configuration points.
	issued, err := newSandboxEnrollmentService().Issue(ctx, workspaceUUID, runtimeUUID, auroraEnrollmentImageDigest)
	if err != nil {
		t.Fatalf("issue managed enrollment: %v", err)
	}
	tokenPath := filepath.Join(t.TempDir(), "managed-enrollment-token")
	if err := os.WriteFile(tokenPath, []byte(issued.Token), 0o600); err != nil {
		t.Fatalf("write enrollment token file: %v", err)
	}

	// The fake executable is injected through daemon configuration by absolute
	// path. Nothing searches PATH, $HOME, or a user agent config.
	fakeDir := t.TempDir()
	countPath := filepath.Join(fakeDir, "executions.txt")
	fakeClaude := writeFakeClaude(t, fakeDir, countPath)

	obs := &managedLifecycleObservation{}
	srv := httptest.NewServer(obs.router())
	t.Cleanup(srv.Close)

	healthPort := freeLoopbackPort(t)
	cfg := daemon.Config{
		ServerBaseURL:       srv.URL,
		DeviceName:          "aurora-managed-lifecycle",
		RuntimeName:         "aurora-managed-lifecycle",
		CLIVersion:          "test",
		WorkspacesRoot:      t.TempDir(),
		Agents:              map[string]daemon.AgentEntry{"claude": {Path: fakeClaude, Command: fakeClaude}},
		MaxConcurrentTasks:  1,
		PollInterval:        50 * time.Millisecond,
		WSClaimPollInterval: 50 * time.Millisecond,
		HeartbeatInterval:   50 * time.Millisecond,
		HealthPort:          healthPort,
		AgentTimeout:        60 * time.Second,
		GCEnabled:           false,
		Managed:             daemon.ManagedConfig{Enabled: true, EnrollmentTokenFile: tokenPath},
	}
	daemonLog := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(daemonLog, &slog.HandlerOptions{Level: slog.LevelDebug}))
	d := daemon.New(cfg, logger)

	runCtx, cancelRun := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	var runErr error
	go func() {
		runErr = d.Run(runCtx)
		close(runDone)
	}()
	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			cancelRun()
			select {
			case <-runDone:
			case <-time.After(15 * time.Second):
			}
		})
	}
	t.Cleanup(stop)

	diagnostics := func() {
		logManagedLifecycleState(t, workspaceID, runtimeID, taskID, healthPort, daemonLog.String())
	}

	waitForManagedHealth(t, healthPort, runDone, 20*time.Second)
	waitForTaskStatus(t, taskID, "completed", 60*time.Second, diagnostics)

	// Enrollment was consumed exactly once.
	enrollCount, startCount, claimBodies := obs.snapshot()
	if enrollCount != 1 {
		t.Fatalf("managed enroll requests = %d, want exactly 1", enrollCount)
	}
	var nodeState string
	var enrollmentHash pgtype.Text
	var consumedAt pgtype.Timestamptz
	if err := testPool.QueryRow(ctx, `SELECT state, enrollment_token_hash, enrollment_consumed_at FROM aurora_sandbox_node WHERE workspace_id = $1`, workspaceID).Scan(&nodeState, &enrollmentHash, &consumedAt); err != nil {
		t.Fatalf("load sandbox node after enrollment: %v", err)
	}
	if enrollmentHash.Valid {
		t.Fatalf("enrollment token hash is still live after one enroll")
	}
	if !consumedAt.Valid {
		t.Fatal("sandbox node has no enrollment_consumed_at after enroll")
	}
	if nodeState != "online" {
		t.Fatalf("sandbox node state after enrollment = %q, want online", nodeState)
	}
	// Exactly-once consumption: replaying the spent secret matches nothing.
	if _, _, err := newSandboxEnrollmentService().Consume(ctx, issued.Token); !errors.Is(err, aurora.ErrInvalidManagedEnrollment) {
		t.Fatalf("replaying the consumed enrollment secret = %v, want ErrInvalidManagedEnrollment", err)
	}

	// The claim set is exactly this runtime and exactly one slot.
	if len(claimBodies) == 0 {
		t.Fatal("managed daemon never issued a claim request")
	}
	for i, body := range claimBodies {
		if len(body.RuntimeIDs) != 1 || body.RuntimeIDs[0] != runtimeID {
			t.Fatalf("claim %d runtime_ids = %v, want exactly [%s]", i, body.RuntimeIDs, runtimeID)
		}
		if body.MaxTasks != 1 {
			t.Fatalf("claim %d max_tasks = %d, want 1", i, body.MaxTasks)
		}
		if body.DaemonID != issued.Identity.DaemonID {
			t.Fatalf("claim %d daemon_id = %q, want %q", i, body.DaemonID, issued.Identity.DaemonID)
		}
	}

	// Execution started once and the fake provider ran once.
	if startCount != 1 {
		t.Fatalf("managed start requests = %d, want exactly 1", startCount)
	}
	if got := fakeExecutionCount(t, countPath); got != 1 {
		t.Fatalf("fake claude executions = %d, want exactly 1", got)
	}

	// Runtime, node, and credential identity stay inside the workspace.
	var nodeWorkspace, nodeRuntime, nodeDaemon string
	if err := testPool.QueryRow(ctx, `SELECT workspace_id::text, runtime_id::text, daemon_id FROM aurora_sandbox_node WHERE workspace_id = $1`, workspaceID).Scan(&nodeWorkspace, &nodeRuntime, &nodeDaemon); err != nil {
		t.Fatalf("load sandbox node identity: %v", err)
	}
	if nodeWorkspace != workspaceID || nodeRuntime != runtimeID || nodeDaemon != issued.Identity.DaemonID {
		t.Fatalf("node identity = (%s, %s, %s), want workspace %s runtime %s daemon %s", nodeWorkspace, nodeRuntime, nodeDaemon, workspaceID, runtimeID, issued.Identity.DaemonID)
	}
	var runtimeWorkspace string
	var runtimeDaemon pgtype.Text
	if err := testPool.QueryRow(ctx, `SELECT workspace_id::text, daemon_id FROM agent_runtime WHERE id = $1`, runtimeID).Scan(&runtimeWorkspace, &runtimeDaemon); err != nil {
		t.Fatalf("load managed runtime identity: %v", err)
	}
	if runtimeWorkspace != workspaceID {
		t.Fatalf("managed runtime workspace = %s, want %s", runtimeWorkspace, workspaceID)
	}
	if !runtimeDaemon.Valid || runtimeDaemon.String != issued.Identity.DaemonID {
		t.Fatalf("managed runtime daemon = %v, want %s", runtimeDaemon, issued.Identity.DaemonID)
	}
	var enrolledTokens int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM daemon_token WHERE workspace_id = $1 AND daemon_id = $2`, workspaceID, issued.Identity.DaemonID).Scan(&enrolledTokens); err != nil {
		t.Fatalf("count enrolled daemon tokens: %v", err)
	}
	if enrolledTokens != 1 {
		t.Fatalf("daemon tokens for enrolled identity = %d, want exactly 1", enrolledTokens)
	}

	// Graceful shutdown revokes the credential and releases the node.
	stop()
	select {
	case <-runDone:
	default:
		t.Fatal("managed daemon did not stop")
	}
	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		diagnostics()
		t.Fatalf("managed daemon Run returned %v, want context.Canceled", runErr)
	}
	waitForManagedShutdown(t, workspaceID, runtimeID, 15*time.Second, diagnostics)
}
