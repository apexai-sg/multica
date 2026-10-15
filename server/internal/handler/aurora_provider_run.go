package handler

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/aurora"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// Aurora provider-run bookkeeping.
//
// A run starts in creating (the create lease is open), moves to submitted once
// the provider returned an external id, and ends in succeeded or failed.
// ambiguous is the server's verdict when a retry of a live create lease finds no
// external id: the first create may have reached the provider, so the run is
// failed/refunded rather than submitted twice.
//
// These routes are bookkeeping and observability only. The model now calls the
// provider directly, so nothing here can stop a second billable create — the
// create lease is advisory. Treat the recorded state as a record of what the run
// reported, not as a guarantee that no duplicate request was made.
const (
	auroraProviderRunStateCreating  = "creating"
	auroraProviderRunStateSucceeded = "succeeded"
	auroraProviderRunStateFailed    = "failed"
)

// auroraProviderRunMaxBodyBytes bounds the begin body. Canonical tool arguments
// are small; anything larger is a client bug or an attempt to make the server
// hash unbounded input.
const auroraProviderRunMaxBodyBytes = 256 << 10

// auroraProviderRunMaxExternalIDLen bounds a provider-assigned id before it is
// persisted.
const auroraProviderRunMaxExternalIDLen = 512

// auroraProviderRunMaxOperationLen bounds the operation path segment before it
// is matched against policy.
const auroraProviderRunMaxOperationLen = 64

// auroraProviderRunPolicy is the server-owned provider/operation/model contract
// for one fixed Aurora execution route. The route is derived from the
// generation's skill through aurora.ExecutionPolicy; the broker may not
// substitute another provider, operation, or model, and the model a request
// names must be on the route's allowlist.
type auroraProviderRunPolicy struct {
	Provider   string
	Operations []string
	Models     []string
}

// auroraProviderRunPolicies mirrors the Fixed Provider Contracts in the Plan C
// brief. Routes with no entry here (local transforms and Claude text) make no
// create-once provider call, so their tasks cannot open a run.
var auroraProviderRunPolicies = map[string]auroraProviderRunPolicy{
	"volcengine-seedream": {
		Provider:   "volcengine-ark",
		Operations: []string{"seedream.generate"},
		Models:     []string{"doubao-seedream-5-0-pro-260628", "doubao-seedream-5-0-flash-260915"},
	},
	"volcengine-seedance": {
		// Polling resumes the recorded create run through GetAuroraProviderRun;
		// it is deliberately not a second operation that could open a new lease.
		Provider:   "volcengine-ark",
		Operations: []string{"seedance.create"},
		Models: []string{
			"doubao-seedance-2-5-260628",
			"doubao-seedance-2-0-260128",
		},
	},
	"volcengine-asr": {
		Provider:   "volcengine-asr",
		Operations: []string{"asr.recognize"},
		Models:     []string{"bigmodel"},
	},
	"volcengine-asr-hyperframes": {
		Provider:   "volcengine-asr",
		Operations: []string{"asr.recognize"},
		Models:     []string{"bigmodel"},
	},
}

// providerRunPolicyForSkill resolves a generation's skill to its route policy.
// An unavailable skill or a route with no provider contract reports false.
func providerRunPolicyForSkill(skillID string) (auroraProviderRunPolicy, bool) {
	execution, ok := aurora.ExecutionPolicy(skillID)
	if !ok {
		return auroraProviderRunPolicy{}, false
	}
	policy, ok := auroraProviderRunPolicies[execution.Route]
	return policy, ok
}

func (p auroraProviderRunPolicy) allows(provider, operation, model string) bool {
	return p.Provider == provider &&
		slices.Contains(p.Operations, operation) &&
		slices.Contains(p.Models, model)
}

// auroraTaskScope is the verified task-token identity behind one task-token
// request: the token's task, the agent, and the workspace all agree, and the
// task is an Aurora task whose generation is in the token's workspace. The
// provider-run and artifact-staging endpoints share it so the same identity
// boundary guards every write.
type auroraTaskScope struct {
	taskID      pgtype.UUID
	generation  db.AuroraGeneration
	workspaceID pgtype.UUID
}

// auroraTaskScope validates the path task against the task-token
// identity the auth middleware stamped. The token alone is authoritative: the
// middleware strips client-supplied X-Actor-Source / X-Agent-ID / X-Task-ID and
// re-stamps them from the mat_ token row, so a request that did not authenticate
// with a task token fails the X-Actor-Source check even if it forges the rest.
func (h *Handler) auroraTaskScope(w http.ResponseWriter, r *http.Request) (auroraTaskScope, bool) {
	if r.Header.Get("X-Actor-Source") != "task_token" {
		writeError(w, http.StatusForbidden, "aurora endpoints are only available from within an agent task")
		return auroraTaskScope{}, false
	}

	pathTaskID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "taskID"), "taskID")
	if !ok {
		return auroraTaskScope{}, false
	}
	tokenTaskID, err := util.ParseUUID(r.Header.Get("X-Task-ID"))
	if err != nil || tokenTaskID != pathTaskID {
		writeError(w, http.StatusForbidden, "task identity does not match")
		return auroraTaskScope{}, false
	}
	tokenAgentID, err := util.ParseUUID(r.Header.Get("X-Agent-ID"))
	if err != nil {
		writeError(w, http.StatusForbidden, "agent identity is missing")
		return auroraTaskScope{}, false
	}
	tokenWorkspaceID, err := util.ParseUUID(r.Header.Get("X-Workspace-ID"))
	if err != nil {
		writeError(w, http.StatusForbidden, "workspace identity is missing")
		return auroraTaskScope{}, false
	}

	task, err := h.Queries.GetAgentTask(r.Context(), pathTaskID)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "task not found")
			return auroraTaskScope{}, false
		}
		writeError(w, http.StatusInternalServerError, "failed to load task")
		return auroraTaskScope{}, false
	}
	if task.AgentID != tokenAgentID {
		writeError(w, http.StatusForbidden, "task does not belong to this agent")
		return auroraTaskScope{}, false
	}
	agent, err := h.Queries.GetAgent(r.Context(), task.AgentID)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusForbidden, "task agent is not in this workspace")
			return auroraTaskScope{}, false
		}
		writeError(w, http.StatusInternalServerError, "failed to load task agent")
		return auroraTaskScope{}, false
	}
	if agent.WorkspaceID != tokenWorkspaceID {
		writeError(w, http.StatusForbidden, "task is not in this workspace")
		return auroraTaskScope{}, false
	}

	generation, err := h.Queries.GetAuroraGenerationByTaskID(r.Context(), task.ID)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusForbidden, "task is not an Aurora task")
			return auroraTaskScope{}, false
		}
		writeError(w, http.StatusInternalServerError, "failed to load Aurora generation")
		return auroraTaskScope{}, false
	}
	if generation.WorkspaceID != tokenWorkspaceID {
		writeError(w, http.StatusForbidden, "Aurora generation is not in this workspace")
		return auroraTaskScope{}, false
	}

	return auroraTaskScope{taskID: task.ID, generation: generation, workspaceID: tokenWorkspaceID}, true
}

// auroraProviderRunOperation reads and policy-checks the {operation} path
// segment against the skill's fixed route.
func (h *Handler) auroraProviderRunOperation(w http.ResponseWriter, r *http.Request, skillID string) (string, bool) {
	operation := strings.TrimSpace(chi.URLParam(r, "operation"))
	if operation == "" || len(operation) > auroraProviderRunMaxOperationLen {
		writeError(w, http.StatusBadRequest, "invalid provider run operation")
		return "", false
	}
	policy, ok := providerRunPolicyForSkill(skillID)
	if !ok || !slices.Contains(policy.Operations, operation) {
		writeError(w, http.StatusBadRequest, "provider run operation is not allowed for this skill")
		return "", false
	}
	return operation, true
}

// auroraProviderRunResponse is the wire shape the sandbox broker consumes.
// create_allowed is the lease decision: true only on the call that won the
// begin insert, so the broker may issue exactly one billable create.
type auroraProviderRunResponse struct {
	ID            string     `json:"id"`
	GenerationID  string     `json:"generation_id"`
	Provider      string     `json:"provider"`
	Operation     string     `json:"operation"`
	Model         string     `json:"model"`
	State         string     `json:"state"`
	ExternalID    *string    `json:"external_id"`
	ErrorCode     *string    `json:"error_code"`
	RequestSHA256 string     `json:"request_sha256"`
	CreateAllowed bool       `json:"create_allowed"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	CompletedAt   *time.Time `json:"completed_at"`
}

func newAuroraProviderRunResponse(run db.AuroraProviderRun, createAllowed bool) auroraProviderRunResponse {
	resp := auroraProviderRunResponse{
		ID:            uuidToString(run.ID),
		GenerationID:  uuidToString(run.GenerationID),
		Provider:      run.Provider,
		Operation:     run.Operation,
		Model:         run.Model,
		State:         run.State,
		RequestSHA256: run.RequestSha256,
		CreateAllowed: createAllowed,
		CreatedAt:     run.CreatedAt.Time,
		UpdatedAt:     run.UpdatedAt.Time,
	}
	if run.ExternalID.Valid {
		externalID := run.ExternalID.String
		resp.ExternalID = &externalID
	}
	if run.ErrorCode.Valid {
		errorCode := run.ErrorCode.String
		resp.ErrorCode = &errorCode
	}
	if run.CompletedAt.Valid {
		completedAt := run.CompletedAt.Time
		resp.CompletedAt = &completedAt
	}
	return resp
}

// BeginAuroraProviderRun opens the create lease for one (task, operation). The
// first begin wins the insert and returns create_allowed=true; every retry
// reloads the winning row and returns create_allowed=false. A retry that finds
// the run still creating with no external id freezes it as ambiguous, because
// the first create may already have reached the provider.
func (h *Handler) BeginAuroraProviderRun(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.auroraTaskScope(w, r)
	if !ok {
		return
	}

	var req struct {
		Provider  string          `json:"provider"`
		Operation string          `json:"operation"`
		Model     string          `json:"model"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if !decodeAuroraProviderRunBody(w, r, &req) {
		return
	}

	policy, ok := providerRunPolicyForSkill(scope.generation.SkillID)
	if !ok {
		writeError(w, http.StatusBadRequest, "skill has no create-once provider operation")
		return
	}
	req.Provider = strings.TrimSpace(req.Provider)
	req.Operation = strings.TrimSpace(req.Operation)
	req.Model = strings.TrimSpace(req.Model)
	if !policy.allows(req.Provider, req.Operation, req.Model) {
		writeError(w, http.StatusBadRequest, "provider, operation, and model do not match server policy")
		return
	}

	fingerprint, err := canonicalProviderRunFingerprint(req.Arguments)
	if err != nil {
		writeError(w, http.StatusBadRequest, "arguments must be a JSON object")
		return
	}

	run, err := h.Queries.CreateAuroraProviderRun(r.Context(), db.CreateAuroraProviderRunParams{
		ID:            dbid.NewV7(),
		GenerationID:  scope.generation.ID,
		TaskID:        scope.taskID,
		WorkspaceID:   scope.workspaceID,
		Provider:      req.Provider,
		Operation:     req.Operation,
		Model:         req.Model,
		RequestSha256: fingerprint,
	})
	if err == nil {
		writeJSON(w, http.StatusOK, newAuroraProviderRunResponse(run, true))
		return
	}
	if !isUniqueViolation(err) {
		writeError(w, http.StatusInternalServerError, "failed to begin provider run")
		return
	}

	existing, err := h.Queries.GetAuroraProviderRun(r.Context(), db.GetAuroraProviderRunParams{
		TaskID:    scope.taskID,
		Operation: req.Operation,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load existing provider run")
		return
	}
	if existing.Provider != req.Provider || existing.Model != req.Model || existing.RequestSha256 != fingerprint {
		writeError(w, http.StatusConflict, "provider run already exists with a different request")
		return
	}
	if existing.State == auroraProviderRunStateCreating && !existing.ExternalID.Valid {
		marked, err := h.Queries.MarkAuroraProviderRunAmbiguous(r.Context(), db.MarkAuroraProviderRunAmbiguousParams{
			TaskID:    scope.taskID,
			Operation: req.Operation,
		})
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusInternalServerError, "failed to record ambiguous provider run")
				return
			}
			// Another retry froze the run first; return its frozen state.
			marked, err = h.Queries.GetAuroraProviderRun(r.Context(), db.GetAuroraProviderRunParams{
				TaskID:    scope.taskID,
				Operation: req.Operation,
			})
			if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to load existing provider run")
				return
			}
		}
		writeJSON(w, http.StatusOK, newAuroraProviderRunResponse(marked, false))
		return
	}
	writeJSON(w, http.StatusOK, newAuroraProviderRunResponse(existing, false))
}

// RecordAuroraProviderRunExternal records the provider's external id exactly
// once. The same id is an idempotent replay; a different id is refused.
func (h *Handler) RecordAuroraProviderRunExternal(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.auroraTaskScope(w, r)
	if !ok {
		return
	}
	operation, ok := h.auroraProviderRunOperation(w, r, scope.generation.SkillID)
	if !ok {
		return
	}

	var req struct {
		ExternalID string `json:"external_id"`
	}
	if !decodeAuroraProviderRunBody(w, r, &req) {
		return
	}
	externalID := strings.TrimSpace(req.ExternalID)
	if externalID == "" || len(externalID) > auroraProviderRunMaxExternalIDLen {
		writeError(w, http.StatusBadRequest, "external_id is required")
		return
	}

	run, err := h.Queries.ClaimAuroraProviderRunExternal(r.Context(), db.ClaimAuroraProviderRunExternalParams{
		TaskID:     scope.taskID,
		Operation:  operation,
		ExternalID: pgtype.Text{String: externalID, Valid: true},
	})
	if err == nil {
		writeJSON(w, http.StatusOK, newAuroraProviderRunResponse(run, false))
		return
	}
	if isUniqueViolation(err) {
		writeError(w, http.StatusConflict, "external id is already recorded for this provider")
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "failed to record provider external id")
		return
	}

	current, err := h.Queries.GetAuroraProviderRun(r.Context(), db.GetAuroraProviderRunParams{
		TaskID:    scope.taskID,
		Operation: operation,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "provider run not found")
		return
	}
	if current.ExternalID.Valid && current.ExternalID.String == externalID {
		writeJSON(w, http.StatusOK, newAuroraProviderRunResponse(current, false))
		return
	}
	writeError(w, http.StatusConflict, "provider run already has a different external id")
}

// FinishAuroraProviderRun records the terminal outcome of a run that reached
// the provider. A replay of the same terminal state is idempotent; any other
// transition, including out of ambiguous, is refused.
func (h *Handler) FinishAuroraProviderRun(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.auroraTaskScope(w, r)
	if !ok {
		return
	}
	operation, ok := h.auroraProviderRunOperation(w, r, scope.generation.SkillID)
	if !ok {
		return
	}

	var req struct {
		State     string `json:"state"`
		ErrorCode string `json:"error_code"`
	}
	if !decodeAuroraProviderRunBody(w, r, &req) {
		return
	}
	req.State = strings.TrimSpace(req.State)
	if req.State != auroraProviderRunStateSucceeded && req.State != auroraProviderRunStateFailed {
		writeError(w, http.StatusBadRequest, "state must be succeeded or failed")
		return
	}
	errorCode := pgtype.Text{}
	if code := strings.TrimSpace(req.ErrorCode); code != "" {
		errorCode = pgtype.Text{String: code, Valid: true}
	}

	run, err := h.Queries.FinishAuroraProviderRun(r.Context(), db.FinishAuroraProviderRunParams{
		TaskID:    scope.taskID,
		Operation: operation,
		State:     req.State,
		ErrorCode: errorCode,
	})
	if err == nil {
		writeJSON(w, http.StatusOK, newAuroraProviderRunResponse(run, false))
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "failed to finish provider run")
		return
	}

	current, err := h.Queries.GetAuroraProviderRun(r.Context(), db.GetAuroraProviderRunParams{
		TaskID:    scope.taskID,
		Operation: operation,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "provider run not found")
		return
	}
	if current.State == req.State {
		writeJSON(w, http.StatusOK, newAuroraProviderRunResponse(current, false))
		return
	}
	writeError(w, http.StatusConflict, "provider run is already in a different terminal state")
}

// GetAuroraProviderRun returns the task's run so the broker can resume polling
// an already-submitted provider operation.
func (h *Handler) GetAuroraProviderRun(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.auroraTaskScope(w, r)
	if !ok {
		return
	}
	operation, ok := h.auroraProviderRunOperation(w, r, scope.generation.SkillID)
	if !ok {
		return
	}

	run, err := h.Queries.GetAuroraProviderRun(r.Context(), db.GetAuroraProviderRunParams{
		TaskID:    scope.taskID,
		Operation: operation,
	})
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "provider run not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load provider run")
		return
	}
	writeJSON(w, http.StatusOK, newAuroraProviderRunResponse(run, false))
}

// decodeAuroraProviderRunBody reads a bounded JSON object and fails the request
// with a 400 on malformed or oversized input.
func decodeAuroraProviderRunBody(w http.ResponseWriter, r *http.Request, dest any) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, auroraProviderRunMaxBodyBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	if len(body) > auroraProviderRunMaxBodyBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return false
	}
	if err := json.Unmarshal(body, dest); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}

// providerRunKeyNormalizer strips separators before a credential-key match, so
// apiKey, api_key, and api-key are all recognized.
var providerRunKeyNormalizer = strings.NewReplacer("_", "", "-", "")

// providerRunCredentialKeys are argument keys that never contribute to the
// create-once fingerprint. The broker must not leak a credential into the
// request, but hashing must not depend on one either: a rotated key would
// otherwise look like a different create and be refused.
var providerRunCredentialKeys = map[string]bool{
	"credential":    true,
	"credentials":   true,
	"apikey":        true,
	"accesskey":     true,
	"secret":        true,
	"secretkey":     true,
	"token":         true,
	"accesstoken":   true,
	"authorization": true,
	"password":      true,
	"signature":     true,
	"cookie":        true,
}

// canonicalProviderRunFingerprint hashes the canonical tool arguments with
// credential fields removed. map keys are sorted by encoding/json, so two
// argument spellings that differ only in key order produce the same digest.
func canonicalProviderRunFingerprint(raw json.RawMessage) (string, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = json.RawMessage("{}")
	}
	var decoded any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return "", err
	}
	arguments, ok := decoded.(map[string]any)
	if !ok {
		return "", errors.New("arguments must be a JSON object")
	}
	stripProviderRunCredentials(arguments)
	canonical, err := json.Marshal(arguments)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

func stripProviderRunCredentials(value any) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if isProviderRunCredentialKey(key) {
				delete(typed, key)
				continue
			}
			stripProviderRunCredentials(child)
		}
	case []any:
		for _, child := range typed {
			stripProviderRunCredentials(child)
		}
	}
}

func isProviderRunCredentialKey(key string) bool {
	normalized := providerRunKeyNormalizer.Replace(strings.ToLower(strings.TrimSpace(key)))
	return providerRunCredentialKeys[normalized]
}
