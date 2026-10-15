package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/fleet/model"
	"github.com/multica-ai/multica/server/internal/fleet/store"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
)

// Removing secret validation permits caller-forged owners on every business route.
func TestServiceRequiresSecret(t *testing.T) {
	for _, path := range []string{"/api/v1/", "/api/v1/nodes", "/api/v1/pat/verify", "/internal/v1/nodes/diagnose", "/api/v1/nodes/exec"} {
		for _, secret := range [][]byte{nil, []byte("test-only-service-secret")} {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("X-User-ID", "11111111-1111-4111-8111-111111111111")
			w := httptest.NewRecorder()
			NewService(nil, model.Config{}, nil).Handler(secret).ServeHTTP(w, req)
			if w.Code != 401 {
				t.Fatalf("path=%s status=%d", path, w.Code)
			}
		}
	}
}

// Dropping owner/namespace filters or exposing domain fields leaks private node state.
func TestHTTPNodeListIsolationAndPublicDTO(t *testing.T) {
	pool, f := testutil.NewFleetFixture(t)
	ns := "http-list-" + f.UserID
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	updated := created.Add(time.Hour)
	id := f.FleetNode(t, ns, testutil.Cols{"created_at": created, "updated_at": updated, "container_id": "container-test", "profile_ref": "private-path-marker", "secrets_volume": "private-volume-marker", "error_message": "private-error-marker", "image": "approved-test-image", "spec": "small"})
	other := f.User(t, "other", "http-other-"+id+"@test.invalid")
	f.FleetNode(t, ns, testutil.Cols{"owner_id": other})
	f.FleetNode(t, ns+"-other")
	h := NewService(store.New(pool, ns), model.Config{Namespace: ns}, nil).Handler([]byte("test-only-service-secret"))
	for _, owner := range []string{"", "bad-uuid", "00000000-0000-0000-0000-000000000000"} {
		if w := call(t, h, "GET", "/api/v1/nodes", owner, ""); w.Code != 401 {
			t.Fatalf("owner=%s status=%d", owner, w.Code)
		}
	}
	w := call(t, h, "GET", "/api/v1/nodes?limit=1&offset=0", f.UserID, "")
	var rows []map[string]any
	w.Want(http.StatusOK).JSON(&rows)
	if len(rows) != 1 || rows[0]["id"] != id || rows[0]["region"] != "local" || rows[0]["instance_id"] != "container-test" || rows[0]["created_at"] != "2026-01-02T03:04:05Z" || rows[0]["updated_at"] != "2026-01-02T04:04:05Z" {
		t.Fatalf("rows=%v", rows)
	}
	for _, key := range []string{"id", "owner_id", "instance_id", "region", "instance_type", "image_id", "subnet_id", "name", "status", "created_at", "updated_at"} {
		if _, ok := rows[0][key].(string); !ok {
			t.Fatalf("legacy string missing %s", key)
		}
	}
	for _, marker := range []string{"private-path-marker", "private-volume-marker", "private-error-marker", "profile_ref", "bootstrap", "secrets_volume"} {
		if strings.Contains(w.Body.String(), marker) {
			t.Fatalf("leaked %s", marker)
		}
	}
	if w = call(t, h, "GET", "/api/v1/nodes?limit=1&offset=1", f.UserID, ""); w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("page=%d %s", w.Code, w.Body.String())
	}
	for _, q := range []string{"limit=-1", "offset=-1", "limit=bad", "limit=2147483648", "limit=1&limit=2"} {
		if w = call(t, h, "GET", "/api/v1/nodes?"+q, f.UserID, ""); w.Code != 400 {
			t.Fatalf("query=%s status=%d", q, w.Code)
		}
	}
}

// Accepting undeclared fields or rebuilding retries permits untrusted provisioning and duplicate nodes.
func TestHTTPCreateStrictIntentAndReplay(t *testing.T) {
	pool, f := testutil.NewFleetFixture(t)
	ns := "http-create-" + f.UserID
	cleanHTTPProduced(t, f, ns)
	f.FleetProfile(t, ns)
	cfg := model.Config{Namespace: ns, Image: "approved-test-image", MaxNodes: 2, Specs: map[string]model.Spec{"small": {CPUs: 2, MemoryBytes: 4294967296, Pids: 256, MaxRuns: 1}}}
	repo := store.New(pool, ns, store.WithProvisioningConfig(cfg), store.WithMaxNodes(cfg.MaxNodes))
	h := NewService(repo, cfg, nil).Handler([]byte("test-only-service-secret"))
	create := func(body, key string) *testutil.Response {
		req := httptest.NewRequest("POST", "/api/v1/nodes", strings.NewReader(body))
		req.Header.Set("X-Fleet-Service-Key", "test-only-service-secret")
		req.Header.Set("X-User-ID", f.UserID)
		req.Header.Set("Idempotency-Key", key)
		return testutil.Call(t, h.ServeHTTP, req)
	}
	w := create(`{"name":"node","spec":"small"}`, "once")
	if w.Code != 202 {
		t.Fatalf("create=%d %s", w.Code, w.Body.String())
	}
	var n map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &n)
	if n["image_id"] != "approved-test-image" || n["ready"] != false || n["operation_id"] == "" || n["instance_id"] != "" {
		t.Fatalf("accepted=%v", n)
	}
	replay := create(`{"name":"node","spec":"small"}`, "once")
	if replay.Code != 202 || replay.Body.String() != w.Body.String() {
		t.Fatalf("replay=%d %s", replay.Code, replay.Body.String())
	}
	if w = create(`{"name":"different","spec":"small"}`, "once"); w.Code != 409 {
		t.Fatalf("different replay=%d", w.Code)
	}
	for _, body := range []string{`{"name":"node","spec":"ec2.large"}`, `{"name":"node","spec":"small","image":"evil"}`, `{"name":"node","spec":"small","path":"/tmp"}`, `{"name":"node","spec":"small","tags":{}}`, `{"name":"node","spec":"small","region":"aws"}`, `{"name":"node","spec":"small","instance_type":"small"}`, `{"name":"node","spec":"small","spec":"small"}`, `{"Name":"node","spec":"small"}`, `{"name":null,"spec":"small"}`, `{"name":"node","spec":"small"} {}`, `[]`, strings.Repeat("x", (1<<20)+1)} {
		if w = create(body, "bad"); w.Code != 400 {
			t.Fatalf("body=%s status=%d", body, w.Code)
		}
	}
	if w = create(`{"name":"node","spec":"small"}`, ""); w.Code != 400 {
		t.Fatalf("missing key=%d", w.Code)
	}
	if w = call(t, h, "POST", "/api/v1/nodes/exec", f.UserID, `{"command":"private-exec-marker"}`); w.Code != 501 {
		t.Fatalf("exec=%d", w.Code)
	}
}

// PAT identity comes from the credential, never the unverified owner header; revocation has no positive cache.
func TestHTTPPATSQLIdentityAndRevocation(t *testing.T) {
	pool, f := testutil.NewFleetFixture(t)
	ns := "http-pat-" + f.UserID
	cleanHTTPProduced(t, f, ns)
	repo := store.New(pool, ns)
	id := f.FleetNode(t, ns, testutil.Cols{"container_id": ""})
	nodeID, e := util.ParseUUID(id)
	if e != nil {
		t.Fatal(e)
	}
	token, _, e := repo.MintNodeToken(context.Background(), nodeID)
	if e != nil {
		t.Fatal(e)
	}
	h := NewService(repo, model.Config{Namespace: ns}, nil).Handler([]byte("test-only-service-secret"))
	body := `{"token":"` + token + `"}`
	for _, owner := range []string{"", "forged-owner"} {
		w := call(t, h, "POST", "/api/v1/pat/verify", owner, body)
		if w.Code != 200 {
			t.Fatalf("verify=%d %s", w.Code, w.Body.String())
		}
		var got map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &got)
		if got["valid"] != true || got["owner_id"] != f.UserID || got["instance_record_id"] != id || got["instance_id"] != "" {
			t.Fatalf("identity=%v", got)
		}
		if strings.Contains(w.Body.String(), token) {
			t.Fatal("raw token leaked")
		}
	}
	if w := call(t, h, "POST", "/api/v1/pat/verify", f.UserID, `{"token":"fake","owner_id":"forged"}`); w.Code != 400 {
		t.Fatalf("unknown PAT field=%d", w.Code)
	}
	for _, state := range []string{"stopped", "terminated", "revoked"} {
		if state == "revoked" {
			e = repo.RevokeNodeToken(context.Background(), nodeID)
		} else {
			_, e = pool.Exec(context.Background(), "UPDATE fleet_nodes SET status=$2 WHERE id=$1", id, state)
		}
		if e != nil {
			t.Fatal(e)
		}
		w := call(t, h, "POST", "/api/v1/pat/verify", "forged-owner", body)
		var got map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &got)
		want := state == "stopped"
		if w.Code != 200 || got["valid"] != want {
			t.Fatalf("state=%s status=%d result=%v", state, w.Code, got)
		}
		if !want && got["owner_id"] != nil {
			t.Fatal("invalid token disclosed owner")
		}
	}
}

// Liveness is independent; readiness must check real schema and availability, never inspect an empty node.
func TestHTTPHealthReadinessAndCapabilities(t *testing.T) {
	pool, _ := testutil.NewFleetFixture(t)
	repo := store.New(pool, "empty-ready")
	for _, tc := range []struct {
		name string
		repo *store.Store
		p    model.Provider
		want int
	}{{"nil provider", repo, nil, 503}, {"nil store", nil, &fakeProvider{}, 503}, {"healthy", repo, &fakeProvider{}, 200}, {"provider unavailable", repo, &fakeProvider{availability: func(context.Context) error { return errors.New("private-provider-error-marker") }}, 503}} {
		t.Run(tc.name, func(t *testing.T) {
			h := NewService(tc.repo, model.Config{}, tc.p).Handler(nil)
			for _, path := range []string{"/healthz", "/readyz"} {
				w := httptest.NewRecorder()
				h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
				want := tc.want
				if path == "/healthz" {
					want = 200
				}
				if w.Code != want {
					t.Fatalf("path=%s got=%d body=%s", path, w.Code, w.Body.String())
				}
				for _, marker := range []string{"private-provider-error-marker", "postgres", "empty-ready"} {
					if strings.Contains(w.Body.String(), marker) {
						t.Fatal("probe leaked details")
					}
				}
			}
			if p, ok := tc.p.(*fakeProvider); ok {
				for _, action := range p.actions {
					if action != "availability" {
						t.Fatalf("readiness invoked %s", action)
					}
				}
			}
		})
	}
	h := NewService(repo, model.Config{Namespace: "private-ns-marker", APIURL: "private-url-marker", Image: "private-image-marker", Specs: map[string]model.Spec{"small": {CPUs: 2, MemoryBytes: 4294967296, Pids: 256, MaxRuns: 1}}}, &fakeProvider{}).Handler([]byte("test-only-service-secret"))
	w := call(t, h, "GET", "/api/v1/", "", "")
	if w.Code != 200 {
		t.Fatalf("capabilities=%d", w.Code)
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	specs, ok := got["specs"].([]any)
	if !ok || len(specs) != 1 {
		t.Fatalf("capability specs must be an array: %v", got)
	}
	spec := specs[0].(map[string]any)
	if spec["id"] != "small" || spec["cpus"] != float64(2) || spec["memory_bytes"] != float64(4294967296) || spec["pids"] != float64(256) {
		t.Fatalf("spec=%v", spec)
	}
	operations, ok := got["operations"].([]any)
	if !ok || len(operations) != 5 {
		t.Fatalf("not the five node actions: %v", got)
	}
	if got["provider"] != "docker" || got["persistent_storage"] != true || got["disk_quota_supported"] != false {
		t.Fatalf("capabilities=%v", got)
	}
	for _, marker := range []string{"exec", "private-ns-marker", "private-url-marker", "private-image-marker", "test-only-service-secret"} {
		if strings.Contains(w.Body.String(), marker) {
			t.Fatalf("capability leaked %s", marker)
		}
	}
}

// Wrong SQL identity, stale epochs or future health cannot certify maintenance; diagnosis is read-only.
func TestDiagnoseOperationIdentityAndEpoch(t *testing.T) {
	pool, f := testutil.NewFleetFixture(t)
	ns := "http-diagnose-" + f.UserID
	id := f.FleetNode(t, ns, testutil.Cols{"container_id": "diagnostic-container", "start_epoch": "epoch-current", "data_volume": "owned-data-volume"})
	op := f.Insert(t, "fleet_node_operations", testutil.Cols{"namespace": ns, "owner_id": f.UserID, "node_id": id, "action": "stop", "idempotency_key": "diagnose", "request_hash": "private-hash-marker", "phase": "preparing"})
	nodeID, _ := util.ParseUUID(id)
	repo := store.New(pool, ns)
	node, e := repo.GetNode(context.Background(), mustUUID(t, f.UserID), nodeID)
	if e != nil {
		t.Fatal(e)
	}
	good := OperationRequestDTO{Namespace: ns, NodeID: id, OperationID: op, Generation: 1, Action: model.Stop}
	observation := model.Observation{ContainerID: "diagnostic-container", DaemonID: node.DaemonID, StartEpoch: "epoch-current", Ready: true, Status: "ready", ReportStatsKnown: true, ObservedAt: time.Now().UTC(), DataVolume: "owned-data-volume", LayoutVersion: "1"}
	p := &fakeProvider{diagnose: func(ctx context.Context, n model.Node, ref model.OperationRef) (model.Observation, error) {
		if n.ID != nodeID || ref.OperationID != mustUUID(t, op) {
			return model.Observation{}, model.ErrForbidden
		}
		return observation, nil
	}}
	h := NewService(repo, model.Config{Namespace: ns}, p).Handler([]byte("test-only-service-secret"))
	invoke := func(dto OperationRequestDTO, owner string) *testutil.Response {
		raw, _ := json.Marshal(dto)
		return call(t, h, "POST", "/internal/v1/nodes/diagnose", owner, string(raw))
	}
	w := invoke(good, f.UserID)
	if w.Code != 200 {
		t.Fatalf("diagnose=%d %s", w.Code, w.Body.String())
	}
	var response DiagnosticResponseDTO
	if e = json.Unmarshal(w.Body.Bytes(), &response); e != nil || response.Request != good || response.Observation.StartEpoch != "epoch-current" {
		t.Fatalf("response=%s err=%v", w.Body.String(), e)
	}
	other := f.User(t, "diagnose other", "diagnose-other-"+id+"@test.invalid")
	for _, tc := range []struct {
		name   string
		change func(*OperationRequestDTO)
		owner  string
		want   int
	}{{"owner", func(*OperationRequestDTO) {}, other, 403}, {"namespace", func(d *OperationRequestDTO) { d.Namespace += "-wrong" }, f.UserID, 403}, {"generation", func(d *OperationRequestDTO) { d.Generation++ }, f.UserID, 409}, {"action", func(d *OperationRequestDTO) { d.Action = model.Reboot }, f.UserID, 409}, {"operation", func(d *OperationRequestDTO) { d.OperationID = id }, f.UserID, 403}, {"node", func(d *OperationRequestDTO) { d.NodeID = op }, f.UserID, 403}, {"invalid UUID", func(d *OperationRequestDTO) { d.NodeID = "invalid" }, f.UserID, 400}} {
		t.Run(tc.name, func(t *testing.T) {
			bad := good
			tc.change(&bad)
			before := len(p.actions)
			w := invoke(bad, tc.owner)
			if w.Code != tc.want || len(p.actions) != before {
				t.Fatalf("status=%d want=%d actions=%v", w.Code, tc.want, p.actions)
			}
		})
	}
	original := observation
	for _, tc := range []struct {
		name   string
		change func(*model.Observation)
	}{{"future timestamp", func(o *model.Observation) { o.ObservedAt = time.Now().Add(time.Hour) }}, {"old epoch", func(o *model.Observation) { o.StartEpoch = "old" }}, {"wrong daemon", func(o *model.Observation) { o.DaemonID = "wrong" }}, {"expired", func(o *model.Observation) { o.ObservedAt = time.Now().Add(-time.Minute) }}} {
		t.Run(tc.name, func(t *testing.T) {
			observation = original
			tc.change(&observation)
			if w := invoke(good, f.UserID); w.Code != 409 {
				t.Fatalf("stale diagnosis=%d %s", w.Code, w.Body.String())
			}
		})
	}
	observation = original
	observation.Offline = true
	observation.ReportStatsKnown = false
	observation.StartEpoch = ""
	observation.DaemonID = ""
	observation.ContainerID = ""
	w = invoke(good, f.UserID)
	if w.Code != 200 {
		t.Fatalf("offline=%d", w.Code)
	}
	_ = json.Unmarshal(w.Body.Bytes(), &response)
	if response.Observation.Ready || response.Observation.ReportStatsKnown {
		t.Fatal("offline unknown authorized ready health")
	}
	logs := captureSlog(t)
	p.diagnose = func(context.Context, model.Node, model.OperationRef) (model.Observation, error) {
		return model.Observation{}, errors.New("private-provider-error-marker /private/path api_key-marker")
	}
	w = invoke(good, f.UserID)
	if w.Code != 503 || strings.Contains(w.Body.String()+logs.String(), "private") || strings.Contains(w.Body.String()+logs.String(), "api_key") {
		t.Fatalf("unsafe provider error=%d %s", w.Code, w.Body.String())
	}
}
func mustUUID(t *testing.T, s string) pgtype.UUID {
	t.Helper()
	id, e := util.ParseUUID(s)
	if e != nil {
		t.Fatal(e)
	}
	return id
}

// Status lookup must resolve container/UUID within the authenticated owner and namespace.
func TestHTTPStatusOwnedSnapshot(t *testing.T) {
	pool, f := testutil.NewFleetFixture(t)
	ns := "http-status-" + f.UserID
	id := f.FleetNode(t, ns, testutil.Cols{"container_id": "status-container", "status": "stopped", "ready": false, "error_message": "private-status-error-marker"})
	repo := store.New(pool, ns)
	h := NewService(repo, model.Config{Namespace: ns}, nil).Handler([]byte("test-only-service-secret"))
	for _, ref := range []string{id, "status-container"} {
		body, _ := json.Marshal(map[string]string{"instance_id": ref})
		w := call(t, h, "POST", "/api/v1/nodes/status", f.UserID, string(body))
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		if w.Code != 200 || out["id"] != id || out["status"] != "stopped" || out["ready"] != false || strings.Contains(w.Body.String(), "private-status-error-marker") {
			t.Fatalf("snapshot=%d %s", w.Code, w.Body.String())
		}
	}
	other := f.User(t, "status other", "status-other-"+id+"@test.invalid")
	body := "{\"instance_id\":\"status-container\"}"
	if w := call(t, h, "POST", "/api/v1/nodes/status", other, body); w.Code != 403 {
		t.Fatalf("cross-owner=%d", w.Code)
	}
	if _, e := pool.Exec(context.Background(), "UPDATE fleet_nodes SET status='terminated',desired='terminated',ready=false WHERE id=$1", id); e != nil {
		t.Fatal(e)
	}
	if w := call(t, h, "POST", "/api/v1/nodes/status", f.UserID, body); w.Code != 200 || !strings.Contains(w.Body.String(), "\"status\":\"terminated\"") {
		t.Fatalf("tombstone=%d %s", w.Code, w.Body.String())
	}
	for _, body := range []string{"{\"instance_id\":\"\"}", "{\"instance_id\":\"status-container\",\"path\":\"/private\"}"} {
		if w := call(t, h, "POST", "/api/v1/nodes/status", f.UserID, body); w.Code != 400 {
			t.Fatalf("bad status body=%d", w.Code)
		}
	}
}

// Notifications accept only an already approved SQL operation and never execute provider mutations.
func TestHTTPApprovedMaintenanceAcceptance(t *testing.T) {
	for _, action := range []model.Action{model.Start, model.Stop, model.Reboot, model.Delete} {
		t.Run(string(action), func(t *testing.T) {
			pool, f := testutil.NewFleetFixture(t)
			ns := "http-accept-" + f.UserID
			id := f.FleetNode(t, ns, testutil.Cols{"maintenance": true})
			repo := store.New(pool, ns)
			op := f.Insert(t, "fleet_node_operations", testutil.Cols{"namespace": ns, "owner_id": f.UserID, "node_id": id, "action": string(action), "idempotency_key": "accept-key", "request_hash": "private-request-hash", "phase": "prepared"})
			p := &fakeProvider{}
			h := NewService(repo, model.Config{Namespace: ns}, p).Handler([]byte("test-only-service-secret"))
			dto := OperationRequestDTO{Namespace: ns, NodeID: id, OperationID: op, Generation: 1, Action: action}
			path := "/api/v1/nodes/" + string(action)
			method := "POST"
			if action == model.Delete {
				path = "/api/v1/nodes"
				method = "DELETE"
			}
			invoke := func(d OperationRequestDTO, key string) *httptest.ResponseRecorder {
				raw, _ := json.Marshal(d)
				req := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
				req.Header.Set("X-Fleet-Service-Key", "test-only-service-secret")
				req.Header.Set("X-User-ID", f.UserID)
				req.Header.Set("Idempotency-Key", key)
				w := httptest.NewRecorder()
				h.ServeHTTP(w, req)
				return w
			}
			legacy, _ := json.Marshal(map[string]string{"instance_id": id})
			if w := call(t, h, method, path, f.UserID, string(legacy)); w.Code != 409 {
				t.Fatalf("unapproved legacy notification=%d %s", w.Code, w.Body.String())
			}
			if w := invoke(dto, "accept-key"); w.Code != 409 {
				t.Fatalf("unapproved=%d %s", w.Code, w.Body.String())
			}
			if _, e := pool.Exec(context.Background(), "UPDATE fleet_node_operations SET approved=true WHERE id=$1", op); e != nil {
				t.Fatal(e)
			}
			w := invoke(dto, "accept-key")
			if w.Code != 202 {
				t.Fatalf("approved=%d %s", w.Code, w.Body.String())
			}
			var got map[string]any
			_ = json.Unmarshal(w.Body.Bytes(), &got)
			if got["operation_id"] != op || got["ready"] != false {
				t.Fatalf("accept snapshot=%v", got)
			}
			if replay := invoke(dto, "accept-key"); replay.Code != 202 || replay.Body.String() != w.Body.String() {
				t.Fatalf("duplicate=%d %s", replay.Code, replay.Body.String())
			}
			for _, key := range []string{"", "wrong"} {
				if w := invoke(dto, key); w.Code != 409 {
					t.Fatalf("key=%s status=%d", key, w.Code)
				}
			}
			wrong := dto
			wrong.Action = model.Create
			if w := invoke(wrong, "accept-key"); w.Code != 409 {
				t.Fatalf("wrong action=%d", w.Code)
			}
			wrong = dto
			wrong.Generation++
			if w := invoke(wrong, "accept-key"); w.Code != 409 {
				t.Fatalf("wrong generation=%d", w.Code)
			}
			if _, e := pool.Exec(context.Background(), "UPDATE fleet_node_operations SET phase='completed' WHERE id=$1", op); e != nil {
				t.Fatal(e)
			}
			if w := invoke(dto, "accept-key"); w.Code != 200 {
				t.Fatalf("completed replay=%d", w.Code)
			}
			if len(p.actions) != 0 {
				t.Fatalf("HTTP executed provider actions=%v", p.actions)
			}
		})
	}
}

// Stale persisted health cannot make the public node executable; raw internal error codes stay private.
func TestHTTPPublicHealthAndErrorProjection(t *testing.T) {
	pool, f := testutil.NewFleetFixture(t)
	ns := "http-health-projection-" + f.UserID
	id := f.FleetNode(t, ns, testutil.Cols{"status": "running", "health_at": time.Now().Add(-time.Minute), "error_code": "private-error-code-marker", "error_message": "private-message-marker"})
	h := NewService(store.New(pool, ns), model.Config{Namespace: ns}, nil).Handler([]byte("test-only-service-secret"))
	body, _ := json.Marshal(map[string]string{"instance_id": id})
	w := call(t, h, "POST", "/api/v1/nodes/status", f.UserID, string(body))
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != 200 || out["ready"] != false || out["error_code"] != "unavailable" || strings.Contains(w.Body.String(), "private-") {
		t.Fatalf("unsafe stale projection=%d %s", w.Code, w.Body.String())
	}
	if _, e := pool.Exec(context.Background(), "UPDATE fleet_nodes SET health_at=now(),error_code='initialization_failed' WHERE id=$1", id); e != nil {
		t.Fatal(e)
	}
	w = call(t, h, "GET", "/api/v1/nodes", f.UserID, "")
	var rows []map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &rows)
	if w.Code != 200 || len(rows) != 1 || rows[0]["error_code"] != "initialization_failed" {
		t.Fatalf("stable error projection=%d %s", w.Code, w.Body.String())
	}
}

// A configured service without a Store must return a generic failure rather than panic or echo input.
func TestHTTPUnavailableStore(t *testing.T) {
	h := NewService(nil, model.Config{}, nil).Handler([]byte("test-only-service-secret"))
	owner := "11111111-1111-4111-8111-111111111111"
	for _, tc := range []struct{ method, path, body string }{{"GET", "/api/v1/nodes", ""}, {"POST", "/api/v1/nodes", "{\"name\":\"private-name-marker\",\"spec\":\"small\"}"}, {"POST", "/api/v1/pat/verify", "{\"token\":\"private-token-marker\"}"}} {
		w := call(t, h, tc.method, tc.path, owner, tc.body)
		if w.Code != 503 || strings.Contains(w.Body.String(), "private-") {
			t.Fatalf("unavailable=%d %s", w.Code, w.Body.String())
		}
	}
}

// Task5 injection is deliberately absent: this fake transport pins the required header now.
type handlerTransport struct{ h http.Handler }

func (tr handlerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	tr.h.ServeHTTP(w, r)
	return w.Result(), nil
}
func TestHTTPPATTransportRequiresServiceKey(t *testing.T) {
	pool, _ := testutil.NewFleetFixture(t)
	h := NewService(store.New(pool, "transport"), model.Config{}, nil).Handler([]byte("test-only-service-secret"))
	client := http.Client{Transport: handlerTransport{h}}
	for _, key := range []string{"", "forged-service-key", "test-only-service-secret"} {
		req, e := http.NewRequest("POST", "http://fleet.test/api/v1/pat/verify", strings.NewReader("{\"token\":\"private-token-marker\"}"))
		if e != nil {
			t.Fatal(e)
		}
		req.Header.Set("X-Fleet-Service-Key", key)
		req.Header.Set("X-User-ID", "untrusted-owner-marker")
		res, e := client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		res.Body.Close()
		want := 401
		if key == "test-only-service-secret" {
			want = 200
		}
		if res.StatusCode != want {
			t.Fatalf("key=%s got=%d want=%d", key, res.StatusCode, want)
		}
	}
}

// Availability has an independent five-second budget after schema, without exposing errors.
func TestHTTPReadinessAvailabilityBudget(t *testing.T) {
	pool, _ := testutil.NewFleetFixture(t)
	cfg := pool.Config().Copy()
	cfg.MaxConns = 1
	scoped, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer scoped.Close()
	held, err := scoped.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	timer := time.AfterFunc(300*time.Millisecond, held.Release)
	defer func() {
		if timer.Stop() {
			held.Release()
		}
	}()
	entered := false
	p := &fakeProvider{availability: func(ctx context.Context) error {
		entered = true
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 5*time.Second || time.Until(deadline) < 4500*time.Millisecond {
			t.Error("readiness lacks independent five-second availability budget")
		}
		<-ctx.Done()
		return errors.New("private-availability-marker")
	}}
	h := NewService(store.New(scoped, "empty-budget"), model.Config{}, p).Handler(nil)
	w := httptest.NewRecorder()
	start := time.Now()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil))
	if !entered || w.Code != 503 || time.Since(start) > 6*time.Second || time.Since(start) < 4500*time.Millisecond || strings.Contains(w.Body.String(), "private-") {
		t.Fatalf("entered=%v status=%d elapsed=%s", entered, w.Code, time.Since(start))
	}
	t.Run("caller-cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		cancelled := NewService(store.New(pool, "cancel-budget"), model.Config{}, &fakeProvider{availability: func(ctx context.Context) error { cancel(); <-ctx.Done(); return nil }}).Handler(nil)
		w := httptest.NewRecorder()
		start := time.Now()
		cancelled.ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil).WithContext(ctx))
		if w.Code != 503 || time.Since(start) > time.Second {
			t.Fatalf("caller cancellation status=%d elapsed=%s", w.Code, time.Since(start))
		}
	})
	broken := NewService(store.New(nil, "broken"), model.Config{}, &fakeProvider{}).Handler(nil)
	w = httptest.NewRecorder()
	broken.ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil))
	if w.Code != 503 {
		t.Fatalf("missing schema returned %d", w.Code)
	}
}

// Store-produced rows are not inserted through Fixture.Insert, so register their exact owned scope.
func cleanHTTPProduced(t *testing.T, f *testutil.Fixture, ns string) {
	t.Helper()
	for _, table := range []string{"fleet_nodes", "fleet_node_operations", "fleet_node_credentials"} {
		f.Cleanup(t, "DELETE FROM "+table+" WHERE namespace=$1 AND owner_id=$2", ns, f.UserID)
	}
}

// Omitting private-file admission persists an intent for a known unusable profile.
func TestHTTPCreateRejectsInvalidPrivateProfile(t *testing.T) {
	for _, kind := range []string{"missing", "insecure", "unreadable", "malformed"} {
		t.Run(kind, func(t *testing.T) {
			pool, f := testutil.NewFleetFixture(t)
			ns := "fix1-admission-" + f.UserID
			cleanHTTPProduced(t, f, ns)
			path := filepath.Join(t.TempDir(), "private-path-marker.json")
			if kind != "missing" {
				mode := os.FileMode(0600)
				if kind == "insecure" {
					mode = 0644
				}
				if kind == "unreadable" {
					mode = 0200
				}
				raw := `{"api_key":"fake-fixture-marker"}`
				if kind == "malformed" {
					raw = `{"api_key":"private-key-marker", "unknown":true}`
				}
				if e := os.WriteFile(path, []byte(raw), mode); e != nil {
					t.Fatal(e)
				}
			}
			f.FleetProfile(t, ns, testutil.Cols{"profile_ref": path})
			cfg := model.Config{Namespace: ns, Image: "approved-test-image", Specs: map[string]model.Spec{"small": {CPUs: 2, MemoryBytes: 4294967296, Pids: 256, MaxRuns: 1}}}
			repo := store.New(pool, ns, store.WithProvisioningConfig(cfg))
			h := NewService(repo, cfg, nil).Handler([]byte("test-only-service-secret"))
			w := createHTTP(t, h, f.UserID, "new-key", `{"name":"node","spec":"small"}`)
			if w.Code != 503 || !strings.Contains(w.Body.String(), "profile_missing") {
				t.Errorf("status=%d body=%s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "private-") || strings.Contains(w.Body.String(), "fake-fixture-marker") {
				t.Error("private profile leaked")
			}
			for _, table := range []string{"fleet_nodes", "fleet_node_operations", "fleet_node_credentials"} {
				var count int
				f.QueryRow(t, "SELECT count(*) FROM "+table+" WHERE namespace=$1 AND owner_id=$2", ns, f.UserID).Scan(&count)
				if count != 0 {
					t.Errorf("%s persisted %d rejected rows", table, count)
				}
			}
		})
	}
}

// A caller map mutation must change neither public capabilities nor persisted approved resources.
func TestHTTPServiceSnapshotsSpecsAndReplaysAfterProfileLoss(t *testing.T) {
	pool, f := testutil.NewFleetFixture(t)
	ns := "fix1-snapshot-" + f.UserID
	cleanHTTPProduced(t, f, ns)
	f.FleetProfile(t, ns)
	cfg := model.Config{Namespace: ns, Image: "approved-test-image", Specs: map[string]model.Spec{"small": {CPUs: 2, MemoryBytes: 4294967296, Pids: 256, MaxRuns: 1}}}
	repo := store.New(pool, ns, store.WithProvisioningConfig(cfg))
	h := NewService(repo, cfg, nil).Handler([]byte("test-only-service-secret"))
	delete(cfg.Specs, "small")
	cfg.Specs["unapproved"] = model.Spec{CPUs: 99}
	w := call(t, h, "GET", "/api/v1/", "", "")
	var caps struct {
		Specs []struct {
			ID   string `json:"id"`
			CPUs int    `json:"cpus"`
		} `json:"specs"`
	}
	w.Want(200).JSON(&caps)
	if len(caps.Specs) != 1 || caps.Specs[0].ID != "small" || caps.Specs[0].CPUs != 2 {
		t.Errorf("mutated capabilities: %+v", caps)
	}
	first := createHTTP(t, h, f.UserID, "once", `{"name":"node","spec":"small"}`)
	first.Want(202)
	nodes, e := repo.ListNodes(context.Background(), mustUUID(t, f.UserID), 10, 0)
	if e != nil || len(nodes) != 1 || nodes[0].Resources.CPUs != 2 || nodes[0].Resources.Pids != 256 {
		t.Fatalf("snapshot nodes=%+v err=%v", nodes, e)
	}
	profile, e := repo.GetProfile(context.Background(), mustUUID(t, f.UserID))
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(profile.Ref); e != nil {
		t.Fatal(e)
	}
	if e = repo.UpsertProfiles(context.Background(), map[pgtype.UUID]string{mustUUID(t, f.UserID): ""}, 2); e != nil {
		t.Fatal(e)
	}
	replay := createHTTP(t, h, f.UserID, "once", `{"name":"node","spec":"small"}`)
	replay.Want(202)
	if replay.Body.String() != first.Body.String() {
		t.Error("profile loss changed matching replay")
	}
	createHTTP(t, h, f.UserID, "once", `{"name":"different","spec":"small"}`).Want(409)
	createHTTP(t, h, f.UserID, "new", `{"name":"node","spec":"small"}`).Want(503)
}

func createHTTP(t *testing.T, h http.Handler, owner, key, body string) *testutil.Response {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/nodes", strings.NewReader(body))
	req.Header.Set("X-Fleet-Service-Key", "test-only-service-secret")
	req.Header.Set("X-User-ID", owner)
	req.Header.Set("Idempotency-Key", key)
	return testutil.Call(t, h.ServeHTTP, req)
}

func call(t *testing.T, h http.Handler, method, path, owner, body string) *testutil.Response {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("X-Fleet-Service-Key", "test-only-service-secret")
	if owner != "" {
		r.Header.Set("X-User-ID", owner)
	}
	return testutil.Call(t, h.ServeHTTP, r)
}
