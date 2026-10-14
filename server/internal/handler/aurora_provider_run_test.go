package handler

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// The provider/operation/model triple these tests exercise, and the expert they
// attach it to. Every vendored expert runs the claude-text route, which makes no
// create-once provider call, so that route has no contract of its own:
// installProviderRunContract bolts one on for the duration of a test, which is
// what keeps the lease logic below covered.
const (
	providerRunTestProvider  = "volcengine-agentplan"
	providerRunTestOperation = "seedance.create"
	providerRunTestModel     = "doubao-seedance-2.0"
	providerRunTestSkill     = "ecommerce-bidding-strategy"
)

// providerRunTestRoute is the route the synthetic contract is installed on.
const providerRunTestRoute = "claude-text"

// installProviderRunContract temporarily gives claude-text the provider triple
// above, then restores the table.
func installProviderRunContract(t *testing.T) {
	t.Helper()
	previous, had := auroraProviderRunPolicies[providerRunTestRoute]
	auroraProviderRunPolicies[providerRunTestRoute] = auroraProviderRunPolicy{
		Provider:   providerRunTestProvider,
		Operations: []string{providerRunTestOperation},
		Models:     []string{providerRunTestModel},
	}
	t.Cleanup(func() {
		if had {
			auroraProviderRunPolicies[providerRunTestRoute] = previous
			return
		}
		delete(auroraProviderRunPolicies, providerRunTestRoute)
	})
}

// providerRunTestResponse mirrors the wire contract the broker consumes.
type providerRunTestResponse struct {
	ID            string  `json:"id"`
	Provider      string  `json:"provider"`
	Operation     string  `json:"operation"`
	Model         string  `json:"model"`
	State         string  `json:"state"`
	ExternalID    *string `json:"external_id"`
	ErrorCode     *string `json:"error_code"`
	RequestSHA256 string  `json:"request_sha256"`
	CreateAllowed bool    `json:"create_allowed"`
	CompletedAt   *string `json:"completed_at"`
}

// seedProviderRunTask creates an Aurora task: an agent, a running task, and the
// generation row that marks the task as Aurora work. Provider-run rows are
// removed on cleanup because they have no foreign key to the task.
func seedProviderRunTask(t *testing.T, skillID string) (agentID, taskID, generationID string) {
	t.Helper()
	installProviderRunContract(t)
	agentID = dbfx.Agent(t, "Provider Run Agent "+uuid.NewString(), "")
	// An active task requires a runtime (agent_task_queue_active_requires_runtime).
	taskID = dbfx.Task(t, agentID, testutil.Cols{"status": "running", "runtime_id": testRuntimeID})
	generationID = insertGeneration(t, "provider run", testutil.Cols{
		"skill_id": skillID,
		"task_id":  taskID,
		"status":   "running",
	})
	dbfx.Cleanup(t, `DELETE FROM aurora_provider_run WHERE task_id = $1`, taskID)
	return agentID, taskID, generationID
}

// providerRunRequest builds a task-token request. urlTaskID is the path task;
// tokenTaskID is the task the token is bound to (X-Task-ID), which the handler
// must require to match.
func providerRunRequest(method, path, urlTaskID, agentID, tokenTaskID, operation string, body any) *http.Request {
	req := testutil.JSONRequest(method, path, body)
	req = testutil.WithURLParams(req, "taskID", urlTaskID, "operation", operation)
	return testutil.WithHeaders(req,
		"X-User-ID", testUserID,
		"X-Workspace-ID", testWorkspaceID,
		"X-Actor-Source", "task_token",
		"X-Agent-ID", agentID,
		"X-Task-ID", tokenTaskID,
	)
}

func providerRunBeginBody(provider, operation, model string, arguments map[string]any) map[string]any {
	return map[string]any{
		"provider":  provider,
		"operation": operation,
		"model":     model,
		"arguments": arguments,
	}
}

func beginProviderRun(t *testing.T, agentID, taskID string, body any) *testutil.Response {
	t.Helper()
	return testutil.Call(t, testHandler.BeginAuroraProviderRun,
		providerRunRequest(http.MethodPost,
			"/api/agent/tasks/"+taskID+"/aurora-provider-runs/begin",
			taskID, agentID, taskID, "", body))
}

func recordProviderRunExternal(t *testing.T, agentID, taskID, operation, externalID string) *testutil.Response {
	t.Helper()
	return testutil.Call(t, testHandler.RecordAuroraProviderRunExternal,
		providerRunRequest(http.MethodPut,
			"/api/agent/tasks/"+taskID+"/aurora-provider-runs/"+operation+"/external",
			taskID, agentID, taskID, operation,
			map[string]any{"external_id": externalID}))
}

func getProviderRun(t *testing.T, agentID, taskID, operation string) *testutil.Response {
	t.Helper()
	return testutil.Call(t, testHandler.GetAuroraProviderRun,
		providerRunRequest(http.MethodGet,
			"/api/agent/tasks/"+taskID+"/aurora-provider-runs/"+operation,
			taskID, agentID, taskID, operation, nil))
}

func TestAuroraProviderRunBeginAllowsFirstCreate(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	agentID, taskID, _ := seedProviderRunTask(t, providerRunTestSkill)

	got := testutil.Decode[providerRunTestResponse](t, testHandler.BeginAuroraProviderRun,
		providerRunRequest(http.MethodPost,
			"/api/agent/tasks/"+taskID+"/aurora-provider-runs/begin",
			taskID, agentID, taskID, "",
			providerRunBeginBody(providerRunTestProvider, providerRunTestOperation, providerRunTestModel,
				map[string]any{"prompt": "a cat surfing"})),
		http.StatusOK)

	if got.State != "creating" {
		t.Fatalf("state = %q, want creating", got.State)
	}
	if !got.CreateAllowed {
		t.Fatalf("create_allowed = false on first begin, want true")
	}
	if got.RequestSHA256 == "" {
		t.Fatalf("request_sha256 is empty; want a canonical fingerprint")
	}
	if got.ExternalID != nil {
		t.Fatalf("external_id = %v, want null on first begin", *got.ExternalID)
	}
}

func TestAuroraProviderRunRepeatedFingerprintReturnsSubmittedRun(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	agentID, taskID, _ := seedProviderRunTask(t, providerRunTestSkill)
	body := providerRunBeginBody(providerRunTestProvider, providerRunTestOperation, providerRunTestModel,
		map[string]any{"prompt": "a cat surfing"})

	testutil.Decode[providerRunTestResponse](t, testHandler.BeginAuroraProviderRun,
		providerRunRequest(http.MethodPost, "/api/agent/tasks/"+taskID+"/aurora-provider-runs/begin",
			taskID, agentID, taskID, "", body), http.StatusOK)

	record := testutil.Decode[providerRunTestResponse](t, testHandler.RecordAuroraProviderRunExternal,
		providerRunRequest(http.MethodPut,
			"/api/agent/tasks/"+taskID+"/aurora-provider-runs/"+providerRunTestOperation+"/external",
			taskID, agentID, taskID, providerRunTestOperation,
			map[string]any{"external_id": "cgt-1"}), http.StatusOK)
	if record.State != "submitted" || record.ExternalID == nil || *record.ExternalID != "cgt-1" {
		t.Fatalf("record external = %+v, want submitted/cgt-1", record)
	}

	retry := testutil.Decode[providerRunTestResponse](t, testHandler.BeginAuroraProviderRun,
		providerRunRequest(http.MethodPost, "/api/agent/tasks/"+taskID+"/aurora-provider-runs/begin",
			taskID, agentID, taskID, "", body), http.StatusOK)
	if retry.CreateAllowed {
		t.Fatalf("create_allowed = true on retry, want false")
	}
	if retry.State != "submitted" {
		t.Fatalf("state = %q, want submitted", retry.State)
	}
	if retry.ExternalID == nil || *retry.ExternalID != "cgt-1" {
		t.Fatalf("retry external_id = %v, want cgt-1 for polling", retry.ExternalID)
	}
}

func TestAuroraProviderRunConflictingFingerprintRefused(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	agentID, taskID, _ := seedProviderRunTask(t, providerRunTestSkill)

	beginProviderRun(t, agentID, taskID,
		providerRunBeginBody(providerRunTestProvider, providerRunTestOperation, providerRunTestModel,
			map[string]any{"prompt": "a cat surfing"})).Want(http.StatusOK)

	beginProviderRun(t, agentID, taskID,
		providerRunBeginBody(providerRunTestProvider, providerRunTestOperation, providerRunTestModel,
			map[string]any{"prompt": "a different cat"})).Want(http.StatusConflict)
}

func TestAuroraProviderRunFingerprintIgnoresCredentials(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	agentID, taskID, _ := seedProviderRunTask(t, providerRunTestSkill)

	beginProviderRun(t, agentID, taskID,
		providerRunBeginBody(providerRunTestProvider, providerRunTestOperation, providerRunTestModel,
			map[string]any{"prompt": "a cat surfing", "api_key": "first-secret"})).Want(http.StatusOK)

	recordProviderRunExternal(t, agentID, taskID, providerRunTestOperation, "cgt-1").Want(http.StatusOK)

	// Same arguments with the credential swapped must match the recorded run,
	// not conflict: the fingerprint excludes credentials.
	retry := testutil.Decode[providerRunTestResponse](t, testHandler.BeginAuroraProviderRun,
		providerRunRequest(http.MethodPost, "/api/agent/tasks/"+taskID+"/aurora-provider-runs/begin",
			taskID, agentID, taskID, "",
			providerRunBeginBody(providerRunTestProvider, providerRunTestOperation, providerRunTestModel,
				map[string]any{"prompt": "a cat surfing", "api_key": "second-secret"})), http.StatusOK)
	if retry.CreateAllowed {
		t.Fatalf("create_allowed = true after credential-only change, want false")
	}
	if retry.ExternalID == nil || *retry.ExternalID != "cgt-1" {
		t.Fatalf("external_id = %v, want cgt-1", retry.ExternalID)
	}
}

func TestAuroraProviderRunCrashBeforeExternalIDIsAmbiguous(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	agentID, taskID, _ := seedProviderRunTask(t, providerRunTestSkill)
	body := providerRunBeginBody(providerRunTestProvider, providerRunTestOperation, providerRunTestModel,
		map[string]any{"prompt": "a cat surfing"})

	first := testutil.Decode[providerRunTestResponse](t, testHandler.BeginAuroraProviderRun,
		providerRunRequest(http.MethodPost, "/api/agent/tasks/"+taskID+"/aurora-provider-runs/begin",
			taskID, agentID, taskID, "", body), http.StatusOK)
	if !first.CreateAllowed {
		t.Fatalf("first begin must allow the create")
	}

	retry := testutil.Decode[providerRunTestResponse](t, testHandler.BeginAuroraProviderRun,
		providerRunRequest(http.MethodPost, "/api/agent/tasks/"+taskID+"/aurora-provider-runs/begin",
			taskID, agentID, taskID, "", body), http.StatusOK)
	if retry.CreateAllowed {
		t.Fatalf("create_allowed = true on crash retry; a second billable create must stay unauthorized")
	}
	if retry.State != "ambiguous" {
		t.Fatalf("state = %q, want ambiguous", retry.State)
	}

	// The ambiguity is persisted: a later retry still cannot create.
	third := testutil.Decode[providerRunTestResponse](t, testHandler.BeginAuroraProviderRun,
		providerRunRequest(http.MethodPost, "/api/agent/tasks/"+taskID+"/aurora-provider-runs/begin",
			taskID, agentID, taskID, "", body), http.StatusOK)
	if third.CreateAllowed || third.State != "ambiguous" {
		t.Fatalf("third begin = %+v, want ambiguous/create_allowed=false", third)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM aurora_provider_run WHERE task_id = $1`, taskID); got != 1 {
		t.Fatalf("provider run rows = %d, want exactly 1", got)
	}
}

func TestAuroraProviderRunExternalIDRecordedOnce(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	agentID, taskID, _ := seedProviderRunTask(t, providerRunTestSkill)
	beginProviderRun(t, agentID, taskID,
		providerRunBeginBody(providerRunTestProvider, providerRunTestOperation, providerRunTestModel,
			map[string]any{"prompt": "a cat surfing"})).Want(http.StatusOK)

	first := testutil.Decode[providerRunTestResponse](t, testHandler.RecordAuroraProviderRunExternal,
		providerRunRequest(http.MethodPut,
			"/api/agent/tasks/"+taskID+"/aurora-provider-runs/"+providerRunTestOperation+"/external",
			taskID, agentID, taskID, providerRunTestOperation,
			map[string]any{"external_id": "cgt-1"}), http.StatusOK)
	if first.State != "submitted" || first.ExternalID == nil || *first.ExternalID != "cgt-1" {
		t.Fatalf("first record = %+v, want submitted/cgt-1", first)
	}

	// The same id is an idempotent replay.
	replay := testutil.Decode[providerRunTestResponse](t, testHandler.RecordAuroraProviderRunExternal,
		providerRunRequest(http.MethodPut,
			"/api/agent/tasks/"+taskID+"/aurora-provider-runs/"+providerRunTestOperation+"/external",
			taskID, agentID, taskID, providerRunTestOperation,
			map[string]any{"external_id": "cgt-1"}), http.StatusOK)
	if replay.ExternalID == nil || *replay.ExternalID != "cgt-1" {
		t.Fatalf("replay external_id = %v, want cgt-1", replay.ExternalID)
	}

	// A different id must not overwrite the recorded one.
	recordProviderRunExternal(t, agentID, taskID, providerRunTestOperation, "cgt-2").Want(http.StatusConflict)
}

func TestAuroraProviderRunCompletion(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	agentID, taskID, _ := seedProviderRunTask(t, providerRunTestSkill)
	beginProviderRun(t, agentID, taskID,
		providerRunBeginBody(providerRunTestProvider, providerRunTestOperation, providerRunTestModel,
			map[string]any{"prompt": "a cat surfing"})).Want(http.StatusOK)

	done := testutil.Decode[providerRunTestResponse](t, testHandler.FinishAuroraProviderRun,
		providerRunRequest(http.MethodPut,
			"/api/agent/tasks/"+taskID+"/aurora-provider-runs/"+providerRunTestOperation+"/finish",
			taskID, agentID, taskID, providerRunTestOperation,
			map[string]any{"state": "succeeded"}), http.StatusOK)
	if done.State != "succeeded" {
		t.Fatalf("state = %q, want succeeded", done.State)
	}
	if done.CompletedAt == nil {
		t.Fatalf("completed_at is null, want a timestamp")
	}

	got := testutil.Decode[providerRunTestResponse](t, testHandler.GetAuroraProviderRun,
		providerRunRequest(http.MethodGet,
			"/api/agent/tasks/"+taskID+"/aurora-provider-runs/"+providerRunTestOperation,
			taskID, agentID, taskID, providerRunTestOperation, nil), http.StatusOK)
	if got.State != "succeeded" {
		t.Fatalf("stored state = %q, want succeeded", got.State)
	}
	if got.CreateAllowed {
		t.Fatalf("create_allowed = true on a terminal run, want false")
	}
}

func TestAuroraProviderRunFailure(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	agentID, taskID, _ := seedProviderRunTask(t, providerRunTestSkill)
	beginProviderRun(t, agentID, taskID,
		providerRunBeginBody(providerRunTestProvider, providerRunTestOperation, providerRunTestModel,
			map[string]any{"prompt": "a cat surfing"})).Want(http.StatusOK)

	failed := testutil.Decode[providerRunTestResponse](t, testHandler.FinishAuroraProviderRun,
		providerRunRequest(http.MethodPut,
			"/api/agent/tasks/"+taskID+"/aurora-provider-runs/"+providerRunTestOperation+"/finish",
			taskID, agentID, taskID, providerRunTestOperation,
			map[string]any{"state": "failed", "error_code": "provider_timeout"}), http.StatusOK)
	if failed.State != "failed" {
		t.Fatalf("state = %q, want failed", failed.State)
	}
	if failed.ErrorCode == nil || *failed.ErrorCode != "provider_timeout" {
		t.Fatalf("error_code = %v, want provider_timeout", failed.ErrorCode)
	}

	got := testutil.Decode[providerRunTestResponse](t, testHandler.GetAuroraProviderRun,
		providerRunRequest(http.MethodGet,
			"/api/agent/tasks/"+taskID+"/aurora-provider-runs/"+providerRunTestOperation,
			taskID, agentID, taskID, providerRunTestOperation, nil), http.StatusOK)
	if got.State != "failed" || got.ErrorCode == nil || *got.ErrorCode != "provider_timeout" {
		t.Fatalf("stored run = %+v, want failed/provider_timeout", got)
	}
}

func TestAuroraProviderRunForeignTaskRejected(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	agentID, taskID, _ := seedProviderRunTask(t, providerRunTestSkill)
	_, otherTaskID, _ := seedProviderRunTask(t, providerRunTestSkill)

	// The token is bound to taskID, but the path names another task.
	testutil.Call(t, testHandler.BeginAuroraProviderRun,
		providerRunRequest(http.MethodPost,
			"/api/agent/tasks/"+otherTaskID+"/aurora-provider-runs/begin",
			otherTaskID, agentID, taskID, "",
			providerRunBeginBody(providerRunTestProvider, providerRunTestOperation, providerRunTestModel,
				map[string]any{"prompt": "a cat surfing"}))).Want(http.StatusForbidden)

	if got := dbfx.Count(t, `SELECT count(*) FROM aurora_provider_run WHERE task_id = $1`, otherTaskID); got != 0 {
		t.Fatalf("foreign task wrote %d provider-run rows, want 0", got)
	}
}

func TestAuroraProviderRunOtherAgentsTaskRejected(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	agentID, _, _ := seedProviderRunTask(t, providerRunTestSkill)
	otherAgentID, otherTaskID, _ := seedProviderRunTask(t, providerRunTestSkill)

	// Path and token task agree, but the task belongs to another agent.
	testutil.Call(t, testHandler.BeginAuroraProviderRun,
		providerRunRequest(http.MethodPost,
			"/api/agent/tasks/"+otherTaskID+"/aurora-provider-runs/begin",
			otherTaskID, agentID, otherTaskID, "",
			providerRunBeginBody(providerRunTestProvider, providerRunTestOperation, providerRunTestModel,
				map[string]any{"prompt": "a cat surfing"}))).Want(http.StatusForbidden)

	_ = otherAgentID
}

func TestAuroraProviderRunNonAuroraTaskRejected(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	agentID := dbfx.Agent(t, "Non Aurora Agent "+uuid.NewString(), "")
	taskID := dbfx.Task(t, agentID, testutil.Cols{"status": "running", "runtime_id": testRuntimeID})

	testutil.Call(t, testHandler.BeginAuroraProviderRun,
		providerRunRequest(http.MethodPost,
			"/api/agent/tasks/"+taskID+"/aurora-provider-runs/begin",
			taskID, agentID, taskID, "",
			providerRunBeginBody(providerRunTestProvider, providerRunTestOperation, providerRunTestModel,
				map[string]any{"prompt": "a cat surfing"}))).Want(http.StatusForbidden)
}

func TestAuroraProviderRunPolicyMismatchRejected(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	cases := []struct {
		name      string
		provider  string
		operation string
		model     string
	}{
		{name: "unknown provider", provider: "openai", operation: providerRunTestOperation, model: providerRunTestModel},
		{name: "unknown operation", provider: providerRunTestProvider, operation: "seedance.delete", model: providerRunTestModel},
		{name: "unknown model", provider: providerRunTestProvider, operation: providerRunTestOperation, model: "doubao-seedance-1.5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			agentID, taskID, _ := seedProviderRunTask(t, providerRunTestSkill)
			beginProviderRun(t, agentID, taskID,
				providerRunBeginBody(tc.provider, tc.operation, tc.model,
					map[string]any{"prompt": "a cat surfing"})).Want(http.StatusBadRequest)
		})
	}
}

func TestAuroraProviderRunRequiresTaskToken(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	agentID, taskID, _ := seedProviderRunTask(t, providerRunTestSkill)

	// A member request (no server-set task-token headers) is refused before any
	// persistence.
	req := testutil.JSONRequest(http.MethodPost,
		"/api/agent/tasks/"+taskID+"/aurora-provider-runs/begin",
		providerRunBeginBody(providerRunTestProvider, providerRunTestOperation, providerRunTestModel,
			map[string]any{"prompt": "a cat surfing"}))
	req = testutil.WithURLParams(req, "taskID", taskID, "operation", "")
	req = testutil.WithHeaders(req,
		"X-User-ID", testUserID,
		"X-Workspace-ID", testWorkspaceID,
		"X-Agent-ID", agentID,
		"X-Task-ID", taskID,
	)
	testutil.Call(t, testHandler.BeginAuroraProviderRun, req).Want(http.StatusForbidden)
}

func TestAuroraProviderRunGetUnknownRun(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	agentID, taskID, _ := seedProviderRunTask(t, providerRunTestSkill)
	getProviderRun(t, agentID, taskID, providerRunTestOperation).Want(http.StatusNotFound)
}
