package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

const (
	testManagedWorkspaceID = "11111111-1111-1111-1111-111111111111"
	testManagedRuntimeID   = "22222222-2222-2222-2222-222222222222"
	testManagedDaemonID    = "daemon-managed"
	// A shape-valid enrollment secret: prefix plus 40 lowercase hex characters.
	testManagedEnrollmentToken = "mse_0123456789abcdef0123456789abcdef01234567"
)

// validManagedEnrollmentResponse is the Task 3 response shape: the persisted
// runtime projection still says aurora_managed/cloud, while the execution
// projection says claude with one slot.
func validManagedEnrollmentResponse() ManagedEnrollmentResponse {
	return ManagedEnrollmentResponse{
		WorkspaceID: testManagedWorkspaceID,
		DaemonID:    testManagedDaemonID,
		Runtime: Runtime{
			ID:          testManagedRuntimeID,
			WorkspaceID: testManagedWorkspaceID,
			Name:        "aurora-managed",
			Provider:    "aurora_managed",
			RuntimeMode: "cloud",
			Status:      "online",
			DaemonID:    testManagedDaemonID,
		},
		ExecutionProvider:    "claude",
		MaxConcurrency:       1,
		DaemonToken:          "mdt_managed",
		DaemonTokenExpiresAt: time.Now().Add(8 * time.Hour).Truncate(time.Microsecond),
	}
}

func newManagedTestDaemon(t *testing.T) *Daemon {
	t.Helper()
	return &Daemon{
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		workspaces:   make(map[string]*workspaceState),
		runtimeIndex: make(map[string]Runtime),
	}
}

// writeManagedTokenFile stages an enrollment token file with the owner-only
// permissions the managed config requires.
func writeManagedTokenFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "enrollment-token")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write enrollment token file: %v", err)
	}
	return path
}

// TestInstallManagedEnrollmentAddsOneWorkspaceAndRuntime pins the one-workspace,
// one-runtime install and the carrier/execution provider split: the response's
// persisted projection is aurora_managed, but the installed in-memory runtime
// must launch as claude.
func TestInstallManagedEnrollmentAddsOneWorkspaceAndRuntime(t *testing.T) {
	d := newManagedTestDaemon(t)
	resp := validManagedEnrollmentResponse()

	if err := d.installManagedEnrollment(resp); err != nil {
		t.Fatalf("installManagedEnrollment() = %v, want nil", err)
	}

	if got := d.allRuntimeIDs(); len(got) != 1 || got[0] != testManagedRuntimeID {
		t.Fatalf("allRuntimeIDs() = %v, want only [%s]", got, testManagedRuntimeID)
	}
	rt := d.findRuntime(testManagedRuntimeID)
	if rt == nil {
		t.Fatalf("findRuntime(%s) = nil, want the enrolled runtime", testManagedRuntimeID)
	}
	if rt.Provider != "claude" {
		t.Errorf("installed runtime provider = %q, want claude even though the persisted projection says aurora_managed", rt.Provider)
	}
	if rt.DaemonID != testManagedDaemonID {
		t.Errorf("installed runtime daemon id = %q, want %q", rt.DaemonID, testManagedDaemonID)
	}
	if resp.Runtime.Provider != "aurora_managed" {
		t.Fatalf("fixture drift: persisted projection provider = %q, want aurora_managed", resp.Runtime.Provider)
	}

	d.mu.Lock()
	ws := d.workspaces[testManagedWorkspaceID]
	d.mu.Unlock()
	if ws == nil {
		t.Fatalf("workspace %s was not installed", testManagedWorkspaceID)
	}
	if len(ws.runtimeIDs) != 1 || ws.runtimeIDs[0] != testManagedRuntimeID {
		t.Errorf("workspace runtime ids = %v, want [%s]", ws.runtimeIDs, testManagedRuntimeID)
	}
}

// TestInstallManagedEnrollmentRejectsWrongProvider covers both provider
// identities: the execution projection (must be claude) and the persisted
// runtime projection (must stay aurora_managed).
func TestInstallManagedEnrollmentRejectsWrongProvider(t *testing.T) {
	cases := map[string]func(*ManagedEnrollmentResponse){
		"execution projection is not claude": func(r *ManagedEnrollmentResponse) {
			r.ExecutionProvider = "aurora_managed"
		},
		"persisted projection is not aurora_managed": func(r *ManagedEnrollmentResponse) {
			r.Runtime.Provider = "claude"
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			d := newManagedTestDaemon(t)
			resp := validManagedEnrollmentResponse()
			mutate(&resp)

			if err := d.installManagedEnrollment(resp); !errors.Is(err, ErrInvalidManagedEnrollmentResponse) {
				t.Fatalf("installManagedEnrollment() = %v, want ErrInvalidManagedEnrollmentResponse", err)
			}
			if got := d.allRuntimeIDs(); len(got) != 0 {
				t.Fatalf("a rejected enrollment installed runtimes: %v", got)
			}
			if d.trackedWorkspaceCount() != 0 {
				t.Fatalf("a rejected enrollment installed a workspace")
			}
		})
	}
}

func TestInstallManagedEnrollmentRejectsConcurrencyOtherThanOne(t *testing.T) {
	d := newManagedTestDaemon(t)
	resp := validManagedEnrollmentResponse()
	resp.MaxConcurrency = 2

	if err := d.installManagedEnrollment(resp); !errors.Is(err, ErrInvalidManagedEnrollmentResponse) {
		t.Fatalf("installManagedEnrollment() = %v, want ErrInvalidManagedEnrollmentResponse", err)
	}
	if got := d.allRuntimeIDs(); len(got) != 0 {
		t.Fatalf("a rejected enrollment installed runtimes: %v", got)
	}
}

// TestInstallManagedEnrollmentRejectsIdentityMismatch rejects a runtime
// workspace that differs from the enrolled workspace, a non-cloud runtime
// mode, and installing over prior state.
func TestInstallManagedEnrollmentRejectsIdentityMismatch(t *testing.T) {
	t.Run("runtime workspace differs from enrolled workspace", func(t *testing.T) {
		d := newManagedTestDaemon(t)
		resp := validManagedEnrollmentResponse()
		resp.Runtime.WorkspaceID = "33333333-3333-3333-3333-333333333333"

		if err := d.installManagedEnrollment(resp); !errors.Is(err, ErrInvalidManagedEnrollmentResponse) {
			t.Fatalf("installManagedEnrollment() = %v, want ErrInvalidManagedEnrollmentResponse", err)
		}
		if got := d.allRuntimeIDs(); len(got) != 0 {
			t.Fatalf("a rejected enrollment installed runtimes: %v", got)
		}
	})

	t.Run("persisted runtime mode is not cloud", func(t *testing.T) {
		d := newManagedTestDaemon(t)
		resp := validManagedEnrollmentResponse()
		resp.Runtime.RuntimeMode = "local"

		if err := d.installManagedEnrollment(resp); !errors.Is(err, ErrInvalidManagedEnrollmentResponse) {
			t.Fatalf("installManagedEnrollment() = %v, want ErrInvalidManagedEnrollmentResponse", err)
		}
	})

	t.Run("prior runtime state is non-empty", func(t *testing.T) {
		d := newManagedTestDaemon(t)
		d.runtimeIndex["existing"] = Runtime{ID: "existing"}
		resp := validManagedEnrollmentResponse()

		if err := d.installManagedEnrollment(resp); !errors.Is(err, ErrInvalidManagedEnrollmentResponse) {
			t.Fatalf("installManagedEnrollment() = %v, want ErrInvalidManagedEnrollmentResponse", err)
		}
	})
}

// TestManagedModeDoesNotDiscoverWorkstationWorkspaces covers both halves of the
// managed isolation rule: configuration never runs the workstation agent
// discovery sweep, and bootstrap contacts only the enrollment endpoint.
func TestManagedModeDoesNotDiscoverWorkstationWorkspaces(t *testing.T) {
	t.Run("config probes only claude", func(t *testing.T) {
		oldProbe := probeAgentCLIs
		probeCalls := 0
		probeAgentCLIs = func() map[string]AgentEntry {
			probeCalls++
			return map[string]AgentEntry{
				"claude": {Path: "/workstation/claude"},
				"codex":  {Path: "/workstation/codex"},
			}
		}
		t.Cleanup(func() { probeAgentCLIs = oldProbe })

		fakeClaude := filepath.Join(t.TempDir(), "claude")
		if err := os.WriteFile(fakeClaude, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatalf("write fake claude: %v", err)
		}
		t.Setenv("MULTICA_CLAUDE_PATH", fakeClaude)
		t.Setenv("MULTICA_DAEMON_ID", "")
		t.Setenv("MULTICA_LAUNCHED_BY", "")
		stageManagedProviderSecrets(t)

		cfg, err := LoadConfig(Overrides{
			Managed:                    true,
			ManagedEnrollmentTokenFile: writeManagedTokenFile(t, testManagedEnrollmentToken+"\n"),
			Foreground:                 true,
			ServerURL:                  "http://localhost:0",
			WorkspacesRoot:             t.TempDir(),
		})
		if err != nil {
			t.Fatalf("LoadConfig(managed) = %v", err)
		}
		if probeCalls != 0 {
			t.Errorf("managed config ran the workstation agent probe %d time(s)", probeCalls)
		}
		if len(cfg.Agents) != 1 {
			t.Fatalf("managed agents = %v, want only claude", cfg.Agents)
		}
		if _, ok := cfg.Agents["claude"]; !ok {
			t.Fatalf("managed agents = %v, want claude", cfg.Agents)
		}
		if cfg.MaxConcurrentTasks != 1 {
			t.Errorf("managed MaxConcurrentTasks = %d, want 1", cfg.MaxConcurrentTasks)
		}
		if !cfg.GCEnabled {
			t.Errorf("managed GCEnabled = false, want true")
		}
	})

	t.Run("bootstrap hits no workspace route", func(t *testing.T) {
		var mu sync.Mutex
		var paths []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			paths = append(paths, r.URL.Path)
			mu.Unlock()
			if r.URL.Path != "/api/daemon/managed/enroll" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(validManagedEnrollmentResponse())
		}))
		defer srv.Close()

		d := newManagedTestDaemon(t)
		d.cfg = Config{
			ServerBaseURL: srv.URL,
			Managed: ManagedConfig{
				Enabled:             true,
				EnrollmentTokenFile: writeManagedTokenFile(t, testManagedEnrollmentToken),
			},
		}
		d.client = NewClient(srv.URL)

		if err := d.bootstrapManaged(context.Background()); err != nil {
			t.Fatalf("bootstrapManaged() = %v", err)
		}

		mu.Lock()
		gotPaths := append([]string(nil), paths...)
		mu.Unlock()
		for _, p := range gotPaths {
			if p != "/api/daemon/managed/enroll" {
				t.Errorf("managed bootstrap contacted %q; it must never discover workstation workspaces", p)
			}
		}
		if len(gotPaths) != 1 {
			t.Fatalf("managed bootstrap made %d requests %v, want exactly the enroll request", len(gotPaths), gotPaths)
		}
		if d.client.Token() != "mdt_managed" {
			t.Errorf("client token = %q, want the enrolled mdt_ token", d.client.Token())
		}
		if d.cfg.DaemonID != testManagedDaemonID {
			t.Errorf("daemon id = %q, want %q", d.cfg.DaemonID, testManagedDaemonID)
		}
	})
}

// TestManagedBootstrapKeepsTokenUntilEnrollmentValidates pins the ordering
// rule: the long-lived token is replaced exactly once, and only after the
// response has passed install validation.
func TestManagedBootstrapKeepsTokenUntilEnrollmentValidates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := validManagedEnrollmentResponse()
		resp.ExecutionProvider = "aurora_managed"
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	d := newManagedTestDaemon(t)
	d.cfg = Config{
		ServerBaseURL: srv.URL,
		Managed: ManagedConfig{
			Enabled:             true,
			EnrollmentTokenFile: writeManagedTokenFile(t, testManagedEnrollmentToken),
		},
	}
	d.client = NewClient(srv.URL)
	d.client.SetToken("mul_long_lived")

	if err := d.bootstrapManaged(context.Background()); !errors.Is(err, ErrInvalidManagedEnrollmentResponse) {
		t.Fatalf("bootstrapManaged() = %v, want ErrInvalidManagedEnrollmentResponse", err)
	}
	if d.client.Token() != "mul_long_lived" {
		t.Errorf("client token = %q, want the long-lived token preserved after a rejected enrollment", d.client.Token())
	}
	if got := d.allRuntimeIDs(); len(got) != 0 {
		t.Fatalf("a rejected enrollment installed runtimes: %v", got)
	}
}
