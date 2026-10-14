package operator

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/fleet/model"
	"github.com/multica-ai/multica/server/internal/fleet/store"
)

// TestMain supplies the HOME-free private invocation this package's
// private-input tests require. The operator child never receives HOME (the
// design forbids ambient credential discovery), so the package normalizes its
// own environment rather than failing when a developer shell exports HOME.
// Tests that need to prove the denial still set HOME explicitly with t.Setenv.
func TestMain(m *testing.M) {
	os.Unsetenv("HOME")
	os.Exit(m.Run())
}

type fakeRepo struct {
	fence           store.NamespaceFence
	nodes           []model.Node
	op              model.Operation
	openCalls       int
	completionErr   error
	finalErr        error
	beginCalls      int
	receipts        []store.NamespaceCompletion
	manifestAction  model.Action
	manifestPresent bool
	completion      func(store.NamespaceFence, pgtype.UUID, model.OperationRef, string) error
}

func (r *fakeRepo) CloseNamespace(_ context.Context, f, k string) (store.NamespaceFence, error) {
	if r.fence.Closed {
		if r.fence.FleetID != f || r.fence.OperationKey != k {
			return store.NamespaceFence{}, model.ErrConflict
		}
		return r.fence, nil
	}
	r.fence = store.NamespaceFence{Namespace: "fixture", FleetID: f, OperationKey: k, Generation: r.fence.Generation + 1, Closed: true}
	return r.fence, nil
}
func (r *fakeRepo) OpenNamespace(_ context.Context, f store.NamespaceFence) error {
	r.openCalls++
	r.fence.Closed = false
	return nil
}
func (r *fakeRepo) GetNamespaceFence(context.Context) (store.NamespaceFence, error) {
	return r.fence, nil
}
func (r *fakeRepo) ListNamespaceNodes(_ context.Context, after pgtype.UUID, limit int32) ([]model.Node, error) {
	out := []model.Node{}
	for _, n := range r.nodes {
		if !after.Valid || bytes.Compare(n.ID.Bytes[:], after.Bytes[:]) > 0 {
			out = append(out, n)
			if len(out) == int(limit) {
				break
			}
		}
	}
	return out, nil
}
func (r *fakeRepo) UpsertProfiles(context.Context, map[pgtype.UUID]string, int64) error { return nil }
func (r *fakeRepo) CheckNamespaceCompletion(_ context.Context, f store.NamespaceFence, expected model.Node, ref model.OperationRef, key string) error {
	owner := expected.OwnerID
	if r.completion != nil {
		return r.completion(f, owner, ref, key)
	}
	return r.completionErr
}
func (r *fakeRepo) CheckNamespaceCompletions(_ context.Context, f store.NamespaceFence, action model.Action, cs []store.NamespaceCompletion) error {
	if r.finalErr != nil {
		return r.finalErr
	}
	if f != r.fence {
		return model.ErrConflict
	}
	if len(cs) != len(r.nodes) {
		return model.ErrUnknownHealth
	}
	for _, c := range cs {
		for _, n := range r.nodes {
			if n.ID == c.Node.ID && (n.Generation != c.Ref.Generation || n.Image != c.Node.Image) {
				return model.ErrConflict
			}
		}
	}
	if !f.Finalized {
		r.receipts = append([]store.NamespaceCompletion(nil), cs...)
		r.manifestAction = action
		r.manifestPresent = true
	}
	r.fence.Finalized = true
	return nil
}
func (r *fakeRepo) GetNamespaceCompletions(_ context.Context, f store.NamespaceFence, a model.Action) ([]store.NamespaceCompletion, bool, error) {
	if f != r.fence || !r.manifestPresent {
		return nil, false, model.ErrUnknownHealth
	}
	if a != r.manifestAction {
		return nil, false, nil
	}
	return append([]store.NamespaceCompletion(nil), r.receipts...), true, nil
}
func (r *fakeRepo) BeginNamespaceDestroy(_ context.Context, f store.NamespaceFence) (store.NamespaceFence, error) {
	if f != r.fence || !f.Closed || !f.Finalized {
		return store.NamespaceFence{}, model.ErrConflict
	}
	r.beginCalls++
	r.fence.Generation++
	r.fence.Finalized = false
	return r.fence, nil
}
func (r *fakeRepo) LookupNamespaceOperation(_ context.Context, n model.Node, a model.Action, key string) (model.Operation, bool, error) {
	for _, c := range r.receipts {
		if c.Node.ID == n.ID && c.Key == key && c.Ref.Action == a {
			return model.Operation{ID: c.Ref.OperationID, OwnerID: n.OwnerID, NodeID: n.ID, Generation: c.Ref.Generation, Action: a, Approved: true, Phase: "completed"}, true, nil
		}
	}
	return model.Operation{}, false, nil
}
func privateConfig(t *testing.T) Config {
	t.Helper()
	cfg := testConfig()
	cfg.FleetURL = "http://127.0.0.1:1"
	cfg.DatabaseURL = "postgres://fixture:synthetic-password@127.0.0.1:1/fixture?sslmode=disable"
	cfg.ProfilesFile = privateMap(t)
	dir := t.TempDir()
	cfg.ConfigFile = filepath.Join(dir, "config.json")
	cfg.ServiceKeyFile = filepath.Join(dir, "key")
	for path, raw := range map[string]string{cfg.ConfigFile: `{"namespace":"fixture","fleet_id":"fleet","image":"fake-image","api_url":"http://127.0.0.1:1","specs":{"small":{}}}`, cfg.ServiceKeyFile: "synthetic-key"} {
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return cfg
}
func TestPrivateConfigConsumesCheckedBytes(t *testing.T) {
	for _, replacement := range configReplacementCases {
		t.Run(replacement, func(t *testing.T) {
			cfg := privateConfig(t)
			reader := func(path string) ([]byte, error) {
				raw, err := readPrivate(path)
				if path == cfg.ConfigFile && err == nil {
					if e := os.Remove(path); e != nil {
						t.Fatal(e)
					}
					if replacement == "symlink" {
						foreign := filepath.Join(t.TempDir(), "replacement.json")
						if e := os.WriteFile(foreign, []byte(`{"namespace":"foreign"}`), 0644); e != nil {
							t.Fatal(e)
						}
						if e := os.Symlink(foreign, path); e != nil {
							t.Fatal(e)
						}
					} else {
						if e := makeFifo(path, 0600); e != nil {
							t.Fatal(e)
						}
					}
				}
				return raw, err
			}
			done := make(chan error, 1)
			go func() {
				public, _, err := validateInputsWithReader("status", cfg, reader)
				if err == nil && public.Namespace != cfg.Namespace {
					err = model.ErrConflict
				}
				done <- err
			}()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("checked bytes replaced by second open: %v", err)
				}
			case <-time.After(time.Second):
				// Release an unsafe second FIFO open so RED leaves no blocked goroutine.
				releaseBlockedFifo(cfg.ConfigFile)
				<-done
				t.Fatal("unchecked second FIFO open blocked validation")
			}
		})
	}
}

// Removing the ambient HOME gate must fail before the parser can acquire paths.
func TestPrivateDatabaseRejectsHomeBeforeParser(t *testing.T) {
	for _, root := range homeRootCases {
		t.Run(root, func(t *testing.T) {
			home := t.TempDir()
			dir := filepath.Join(home, ".postgresql")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "root.crt")
			switch root {
			case "malformed":
				if err := os.WriteFile(path, []byte("not a certificate"), 0600); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := makeFifo(path, 0600); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("HOME", home)
			cfg := privateConfig(t)
			calls := 0
			start := time.Now()
			// Never invoke the real parser with HOME set, even during RED.
			parsed, err := localPoolConfigWithParser(cfg.DatabaseURL, func(string) (*pgxpool.Config, error) {
				calls++
				return nil, errors.New("parser boundary reached")
			})
			if !errors.Is(err, model.ErrInvalidRequest) || parsed != nil || calls != 0 {
				t.Errorf("HOME reached parser: calls=%d config=%v error=%v", calls, parsed != nil, err)
			}
			if err := Validate("status", cfg); !errors.Is(err, model.ErrInvalidRequest) {
				t.Errorf("HOME accepted by Validate: %v", err)
				return // Do not let pre-fix Run invoke pgx with the synthetic FIFO.
			}
			if err := Run(context.Background(), "status", cfg); !errors.Is(err, model.ErrInvalidRequest) {
				t.Errorf("HOME accepted by Run: %v", err)
			}
			if time.Since(start) > time.Second {
				t.Fatal("HOME denial did not return promptly")
			}
			if os.Getenv("HOME") != home {
				t.Fatal("operator mutated HOME")
			}
		})
	}
}

func TestPrivateDatabaseHomeFreeExplicitIdentity(t *testing.T) {
	if os.Getenv("HOME") != "" {
		t.Fatal("test requires the HOME-free private invocation")
	}
	for _, options := range []string{"", "&application_name=fixture%20operator&connect_timeout=2"} {
		raw := "postgres://fixture:synthetic-password@127.0.0.1:1234/fixture?sslmode=disable" + options
		calls := 0
		cfg, err := localPoolConfigWithParser(raw, func(uri string) (*pgxpool.Config, error) {
			calls++
			return pgxpool.ParseConfig(uri)
		})
		if err != nil {
			t.Fatal(err)
		}
		c := cfg.ConnConfig
		if calls != 1 || c.Host != "127.0.0.1" || c.Port != 1234 || c.Database != "fixture" || c.User != "fixture" || c.Password != "synthetic-password" || c.TLSConfig != nil || len(c.Fallbacks) != 0 {
			t.Fatal("HOME-free parsing changed explicit identity or transport")
		}
		if options != "" && (c.RuntimeParams["application_name"] != "fixture operator" || c.ConnectTimeout != 2*time.Second) {
			t.Fatal("approved options changed")
		}
	}
}

func TestPrivateDatabasePreservesApprovedOptions(t *testing.T) {
	raw := "postgres://fixture:synthetic-password@127.0.0.1:1234/fixture?sslmode=disable&application_name=fixture%20operator&connect_timeout=2"
	cfg, err := localPoolConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConnConfig.RuntimeParams["application_name"] != "fixture operator" || cfg.ConnConfig.ConnectTimeout != 2*time.Second || cfg.ConnConfig.Port != 1234 {
		t.Fatal("approved options or explicit port changed")
	}
	for _, suffix := range []string{"application_name=", "application_name=a&application_name=b", "application_name=%00", "application_name=%FF", "connect_timeout=0", "connect_timeout=601", "connect_timeout=1.5", "connect_timeout=1&connect_timeout=2", "application_name=%ZZ"} {
		if _, err := localPoolConfig("postgres://fixture:synthetic-password@127.0.0.1:1/fixture?sslmode=disable&" + suffix); !errors.Is(err, model.ErrInvalidRequest) {
			t.Fatalf("invalid allowed option accepted: %v", err)
		}
	}
}

func TestPrivateDatabaseRejectsDiscovery(t *testing.T) {
	cfg := privateConfig(t)
	for _, query := range []string{"sslmode=disable&sslmode=require", "sslmode=disable&sslmode=disable", "sslmode=disable&ssl=true", "sslmode=disable&database=foreign", "sslmode=disable&password=", "sslmode=disable&host=foreign", "sslmode=disable&dbname=foreign", "sslmode=disable&user=foreign", "sslmode=disable&service=fixture", "sslmode=disable&servicefile=/synthetic", "sslmode=disable&passfile=/synthetic", "sslmode=disable&sslcert=/synthetic", "sslmode=disable&sslkey=/synthetic", "sslmode=disable&sslrootcert=/synthetic", "sslmode=disable&SSLMode=require", "sslmode=disable&options=unsafe", "sslmode=disable&port=2"} {
		t.Run(query, func(t *testing.T) {
			bad := cfg
			bad.DatabaseURL = "postgres://fixture:synthetic-password@127.0.0.1:1/fixture?" + query
			if err := Validate("status", bad); !errors.Is(err, model.ErrInvalidRequest) {
				t.Fatalf("unsafe database interpretation accepted: %v", err)
			}
		})
	}
	for _, key := range []string{"PGSERVICE", "PGSERVICEFILE", "PGPASSFILE", "PGSSLCERT", "PGSSLKEY", "PGSSLROOTCERT", "PGPASSWORD", "PGHOST"} {
		t.Run(key, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "synthetic-settings")
			if err := os.WriteFile(path, []byte("[fixture]\npassword=synthetic\n"), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv(key, path)
			if err := Validate("status", cfg); !errors.Is(err, model.ErrInvalidRequest) {
				t.Fatalf("inherited discovery accepted: %v", err)
			}
			if _, err := localPoolConfig(cfg.DatabaseURL); !errors.Is(err, model.ErrInvalidRequest) {
				t.Fatalf("inherited settings reached pgx parse: %v", err)
			}
		})
	}
}

func testID(b byte) pgtype.UUID { return pgtype.UUID{Bytes: [16]byte{15: b}, Valid: true} }
func testConfig() Config {
	return Config{Namespace: "fixture", FleetID: "fleet", OperationKey: "shutdown", Timeout: time.Second}
}
func fixtureRepo() *fakeRepo {
	n := model.Node{Namespace: "fixture", ID: testID(1), OwnerID: testID(2), Generation: 2, Status: "stopped", Desired: "stopped"}
	return &fakeRepo{nodes: []model.Node{n}, op: model.Operation{ID: testID(3), OwnerID: n.OwnerID, NodeID: n.ID, Generation: 2, Action: model.Stop, Approved: true, Phase: "completed"}}
}

// Returning nil on a busy owner would authorize the caller to stop the API.
func privateMap(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "profiles.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"owners":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func TestClosedFencePrepareAndResumeCAS(t *testing.T) {
	r := fixtureRepo()
	r.fence = store.NamespaceFence{Namespace: "fixture", FleetID: "foreign", OperationKey: "shutdown", Generation: 9, Closed: true}
	cfg := testConfig()
	cfg.ProfilesFile = privateMap(t)
	if err := run(context.Background(), "prepare", cfg, dependencies{repo: r}); !errors.Is(err, model.ErrConflict) {
		t.Fatalf("foreign fence accepted profile prepare: %v", err)
	}
	r.fence.FleetID = cfg.FleetID
	if err := run(context.Background(), "prepare", cfg, dependencies{repo: r}); err != nil {
		t.Fatal(err)
	}
	if r.openCalls != 0 || !r.fence.Closed {
		t.Fatal("prepare reopened fence")
	}
	bad := cfg
	bad.OperationKey = "wrong"
	if err := run(context.Background(), "resume", bad, dependencies{repo: r}); !errors.Is(err, model.ErrConflict) {
		t.Fatalf("wrong workflow reopened fence: %v", err)
	}
	if err := run(context.Background(), "status", cfg, dependencies{repo: r}); err != nil {
		t.Fatal(err)
	}
	if r.openCalls != 0 || !r.fence.Closed {
		t.Fatal("status reopened fence")
	}
	if err := run(context.Background(), "resume", cfg, dependencies{repo: r}); err != nil || r.openCalls != 1 || r.fence.Closed {
		t.Fatalf("deliberate resume failed: %v", err)
	}
}

func TestBusyQuiesceKeepsAPIAndFence(t *testing.T) {
	r := fixtureRepo()
	d := dependencies{repo: r, request: func(context.Context, pgtype.UUID, pgtype.UUID, model.Action, string) (model.Operation, error) {
		return model.Operation{}, model.ErrBusy
	}}
	err := run(context.Background(), "quiesce", testConfig(), d)
	if !errors.Is(err, model.ErrBusy) || !r.fence.Closed || r.openCalls != 0 {
		t.Fatalf("busy shutdown returned permission to stop API: err=%v closed=%v opens=%d", err, r.fence.Closed, r.openCalls)
	}
}

func TestQuiesceWaitsSameRefAcrossAllOwnerPages(t *testing.T) {
	r := fixtureRepo()
	r.nodes = nil
	for i := 1; i <= 101; i++ {
		r.nodes = append(r.nodes, model.Node{Namespace: "fixture", ID: testID(byte(i)), OwnerID: testID(byte(200 + i%2)), Generation: 2})
	}
	requests := map[pgtype.UUID]string{}
	refs := map[pgtype.UUID]model.OperationRef{}
	polls := 0
	d := dependencies{repo: r, request: func(_ context.Context, owner, node pgtype.UUID, a model.Action, key string) (model.Operation, error) {
		if !r.fence.Closed {
			t.Fatal("node request preceded namespace close")
		}
		if a != model.Stop {
			t.Fatal("quiesce requested another action")
		}
		requests[node] = key
		return model.Operation{ID: testID(222), NodeID: node, OwnerID: owner, Action: a, Generation: 2, Approved: true, Phase: "queued"}, nil
	}}
	r.completion = func(f store.NamespaceFence, owner pgtype.UUID, ref model.OperationRef, key string) error {
		polls++
		if old, ok := refs[ref.NodeID]; ok && old != ref {
			t.Fatal("poll reconstructed operation ref")
		}
		refs[ref.NodeID] = ref
		if !f.Closed || owner != r.nodes[int(ref.NodeID.Bytes[15])-1].OwnerID || key != requests[ref.NodeID] {
			t.Fatal("completion lost SQL owner/original key")
		}
		if polls == 1 {
			return model.ErrBusy
		}
		return nil
	}
	cfg := testConfig()
	cfg.Timeout = 2 * time.Second
	if err := run(context.Background(), "quiesce", cfg, d); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 101 || len(refs) != 101 || polls != 102 {
		t.Fatalf("whole-owner completion incomplete: requests=%d refs=%d polls=%d", len(requests), len(refs), polls)
	}
	first := requests[testID(1)]
	if err := run(context.Background(), "quiesce", cfg, d); err != nil {
		t.Fatal(err)
	}
	if requests[testID(1)] != first {
		t.Fatal("workflow retry reminted node action key")
	}
}

func TestUnknownNodeIdentityPreservesClosedNamespace(t *testing.T) {
	r := fixtureRepo()
	r.nodes[0].ID = pgtype.UUID{Valid: true}
	r.op.NodeID = r.nodes[0].ID
	d := dependencies{repo: r, request: func(context.Context, pgtype.UUID, pgtype.UUID, model.Action, string) (model.Operation, error) {
		return r.op, nil
	}}
	if err := run(context.Background(), "quiesce", testConfig(), d); !errors.Is(err, model.ErrConflict) || !r.fence.Closed {
		t.Fatalf("zero SQL node identity authorized cleanup: %v", err)
	}
}

func TestQuiesceRequiresWholeNamespaceCompletionAtReturn(t *testing.T) {
	r := fixtureRepo()
	later := r.nodes[0]
	later.ID = testID(4)
	r.nodes = append(r.nodes, later)
	changed := false
	r.completion = func(_ store.NamespaceFence, _ pgtype.UUID, ref model.OperationRef, _ string) error {
		if ref.NodeID == later.ID && !changed {
			changed = true
			r.nodes[0].Generation++
			r.nodes[0].Status = "terminating"
			return model.ErrBusy
		}
		return nil
	}
	d := dependencies{repo: r, request: func(_ context.Context, owner, node pgtype.UUID, a model.Action, _ string) (model.Operation, error) {
		return model.Operation{ID: testID(node.Bytes[15] + 10), OwnerID: owner, NodeID: node, Generation: 2, Action: a, Approved: true, Phase: "completed"}, nil
	}}
	if err := run(context.Background(), "quiesce", testConfig(), d); !errors.Is(err, model.ErrConflict) || !r.fence.Closed || r.fence.Finalized || !changed {
		t.Fatalf("earlier stopped node changed during later polling: %v", err)
	}
}
func TestFinalizedReplayDeniesOriginalDriftWithoutRequestsOrCAS(t *testing.T) {
	for _, mutation := range []string{"missing", "image", "legacy"} {
		t.Run(mutation, func(t *testing.T) {
			r := fixtureRepo()
			b := r.nodes[0]
			b.ID = testID(4)
			r.nodes = append(r.nodes, b)
			calls := 0
			d := dependencies{repo: r, request: func(_ context.Context, owner, node pgtype.UUID, a model.Action, _ string) (model.Operation, error) {
				calls++
				return model.Operation{ID: testID(node.Bytes[15] + 10), OwnerID: owner, NodeID: node, Generation: 2, Action: a, Approved: true, Phase: "completed"}, nil
			}}
			if e := run(context.Background(), "quiesce", testConfig(), d); e != nil {
				t.Fatal(e)
			}
			if mutation == "missing" {
				r.nodes = r.nodes[:1]
			} else if mutation == "image" {
				r.nodes[1].Image = "changed"
			} else {
				r.manifestPresent = false
			}
			before := calls
			fence := r.fence
			for _, action := range []string{"quiesce", "destroy"} {
				if e := run(context.Background(), action, testConfig(), d); e == nil {
					t.Fatalf("%s accepted original proof drift", action)
				}
				if calls != before || r.beginCalls != 0 || r.fence != fence {
					t.Fatal("denial minted Request/CAS or changed barrier")
				}
			}
		})
	}
}
func TestFinalizedOriginallyEmptyNamespace(t *testing.T) {
	r := fixtureRepo()
	r.nodes = nil
	calls := 0
	d := dependencies{repo: r, request: func(context.Context, pgtype.UUID, pgtype.UUID, model.Action, string) (model.Operation, error) {
		calls++
		return model.Operation{}, model.ErrUnknownHealth
	}}
	for _, action := range []string{"quiesce", "quiesce", "destroy", "destroy"} {
		if e := run(context.Background(), action, testConfig(), d); e != nil {
			t.Fatal(e)
		}
	}
	if calls != 0 || r.beginCalls != 1 || !r.fence.Finalized || !r.manifestPresent {
		t.Fatal("original empty proof lost or minted node work")
	}
}
func TestDestroyFromFinalizedStopUsesTrustedCycleCAS(t *testing.T) {
	r := fixtureRepo()
	requests := 0
	d := dependencies{repo: r, request: func(_ context.Context, owner, node pgtype.UUID, a model.Action, _ string) (model.Operation, error) {
		requests++
		if a == model.Delete {
			if r.fence.Finalized || r.beginCalls != 1 || r.fence.Generation != 2 {
				t.Fatal("destroy minted negative work without trusted closed cycle CAS")
			}
			r.nodes[0].Generation = 3
			return model.Operation{ID: testID(5), OwnerID: owner, NodeID: node, Generation: 3, Action: a, Approved: true, Phase: "completed"}, nil
		}
		return r.op, nil
	}}
	if e := run(context.Background(), "quiesce", testConfig(), d); e != nil || !r.fence.Finalized {
		t.Fatalf("stop finalization=%v", e)
	}
	if e := run(context.Background(), "destroy", testConfig(), d); e != nil || !r.fence.Finalized || !r.fence.Closed || r.beginCalls != 1 {
		t.Fatalf("destroy cycle=%v", e)
	}
	before := requests
	if e := run(context.Background(), "destroy", testConfig(), d); e != nil || requests != before || r.beginCalls != 1 {
		t.Fatalf("completed destroy reminted cycle/action: %v", e)
	}
}

// A completed SQL row alone is not proof that resources can be discarded.
func TestUnknownCleanupPreservesDatabase(t *testing.T) {
	r := fixtureRepo()
	r.completionErr = model.ErrUnknownHealth
	r.op.Action = model.Delete
	r.nodes[0].Status = "terminated"
	r.nodes[0].Desired = "terminated"
	d := dependencies{repo: r, request: func(context.Context, pgtype.UUID, pgtype.UUID, model.Action, string) (model.Operation, error) {
		return r.op, nil
	}}
	err := run(context.Background(), "destroy", testConfig(), d)
	if !errors.Is(err, model.ErrUnknownHealth) || !r.fence.Closed || r.openCalls != 0 {
		t.Fatalf("unknown resources authorized DB cleanup: err=%v closed=%v", err, r.fence.Closed)
	}
}
