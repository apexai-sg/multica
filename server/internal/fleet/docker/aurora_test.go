package docker

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/multica-ai/multica/server/internal/fleet/model"
)

// auroraEnrollmentToken is a well-formed server-issued enrollment secret.
const auroraEnrollmentToken = "mse_0123456789abcdef0123456789abcdef01234567"

func auroraConfig() model.Config {
	cfg := fixtureConfig()
	cfg.Aurora = &model.AuroraConfig{
		ServerURL:        "http://api.internal:8080",
		AnthropicBaseURL: "https://ark.example.com",
		AnthropicModel:   "ark-model",
		ReadonlyRootfs:   true,
	}
	return cfg
}

func auroraNode() model.Node {
	n := fixtureNode()
	n.Maintenance = false
	n.ContainerID = ""
	return n
}

func auroraBootstrap(n model.Node) model.Bootstrap {
	return model.Bootstrap{EnrollmentToken: auroraEnrollmentToken, ServerURL: "http://api.internal:8080", DaemonID: n.DaemonID}
}

// TestProviderEnsureAuroraProfile drives the full Aurora admission: an owned
// outbound-capable workspace network and one managed node without a proxy.
func TestProviderEnsureAuroraProfile(t *testing.T) {
	cfg := auroraConfig()
	n := auroraNode()
	base := New(fakeCalls{}, cfg)
	nodeName := base.containerName(n)
	workspace := base.workspaceNetwork(n).Name

	type createCall struct {
		cfg  *container.Config
		host container.HostConfig
		net  string
		name string
	}
	var creates []createCall
	var networks []Resource
	inspect := func(_ context.Context, id string) (Inspection, error) {
		switch id {
		case "node-id":
			return Inspection{ID: "node-id", State: "created", Labels: labels(n.Namespace, cfg.FleetID, nodeID(n), "node")}, nil
		default:
			return Inspection{}, errdefs.ErrNotFound
		}
	}
	e := fakeCalls{
		inspect:       inspect,
		ensureNetwork: func(_ context.Context, r Resource) error { networks = append(networks, r); return nil },
		ensureVolume:  func(context.Context, Resource) error { return nil },
		bootstrap:     func(context.Context, []Resource, []byte) error { return nil },
		removeNetwork: func(context.Context, Resource) error { return nil },
		create: func(_ context.Context, c *container.Config, h *container.HostConfig, net, name string) (string, error) {
			creates = append(creates, createCall{cfg: c, host: *h, net: net, name: name})
			return "node-id", nil
		},
		start:  func(context.Context, string) error { return nil },
		health: func(context.Context, string) ([]byte, error) { return []byte(goodHealth), nil },
	}
	p := New(e, cfg)
	if _, err := p.Ensure(context.Background(), n, auroraBootstrap(n)); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if len(networks) != 2 || networks[0].Internal || networks[1].Internal || networks[1].Role != "workspace-network" || networks[1].Name != workspace {
		t.Fatalf("networks = %+v", networks)
	}
	if len(creates) != 1 {
		t.Fatalf("creates = %d", len(creates))
	}
	node := creates[0]
	if node.name != nodeName || node.net != workspace {
		t.Fatalf("node create = %+v", node)
	}
	wantEnv := []string{
		"HOME=" + model.NodeHome,
		"FLEET_NODE_MAX_RUNS=1",
		"MULTICA_MANAGED=1",
		"MULTICA_SERVER_URL=http://api.internal:8080",
		"MULTICA_MANAGED_ENROLLMENT_TOKEN_FILE=" + model.AuroraEnrollmentFile,
		"MULTICA_CLAUDE_PATH=/usr/local/bin/claude",
		"ANTHROPIC_BASE_URL=https://ark.example.com",
		"ANTHROPIC_MODEL=ark-model",
	}
	if !reflect.DeepEqual(node.cfg.Env, wantEnv) {
		t.Fatalf("node env = %v", node.cfg.Env)
	}
	if node.host.NetworkMode != container.NetworkMode(workspace) || !node.host.ReadonlyRootfs || !reflect.DeepEqual(node.host.SecurityOpt, []string{"no-new-privileges:true"}) {
		t.Fatalf("node host = %+v", node.host)
	}
	if len(node.host.Mounts) != 2 || node.host.Mounts[1].Type != "volume" || !node.host.Mounts[1].ReadOnly || node.host.Mounts[1].Target != model.AuroraEnrollmentDir {
		t.Fatalf("node mounts = %+v", node.host.Mounts)
	}
	if !reflect.DeepEqual([]string(node.cfg.Entrypoint), []string{"/usr/local/bin/fleet-node"}) || !reflect.DeepEqual([]string(node.cfg.Cmd), []string{"run"}) {
		t.Fatalf("entrypoint changed: %v %v", node.cfg.Entrypoint, node.cfg.Cmd)
	}
}

func TestProviderRejectsCrossProfileBootstrap(t *testing.T) {
	n := auroraNode()
	notFound := fakeCalls{inspect: func(context.Context, string) (Inspection, error) { return Inspection{}, errdefs.ErrNotFound }}
	aurora := New(notFound, auroraConfig())
	if _, err := aurora.Ensure(context.Background(), n, model.Bootstrap{NodeToken: "mcn_fake", APIKey: "fake", ServerURL: "http://api.internal:8080", DaemonID: n.DaemonID}); !errors.Is(err, model.ErrInvalidRequest) {
		t.Fatalf("claude bootstrap accepted under the aurora profile: %v", err)
	}
	claude := New(notFound, fixtureConfig())
	if _, err := claude.Ensure(context.Background(), n, model.Bootstrap{EnrollmentToken: auroraEnrollmentToken, ServerURL: fixtureConfig().APIURL, DaemonID: n.DaemonID}); !errors.Is(err, model.ErrInvalidRequest) {
		t.Fatalf("enrollment secret accepted under the claude profile: %v", err)
	}
	if _, err := claude.Ensure(context.Background(), n, auroraBootstrap(n)); !errors.Is(err, model.ErrInvalidRequest) {
		t.Fatalf("aurora payload accepted under the claude profile: %v", err)
	}
}

func TestInstallerTarSelectsProfilePayload(t *testing.T) {
	n := auroraNode()
	aurora, err := installerTar(n, auroraConfig(), auroraBootstrap(n))
	if err != nil || !strings.Contains(string(aurora), "secrets/aurora-enrollment") || strings.Contains(string(aurora), "bootstrap.json") {
		t.Fatalf("aurora installer err=%v", err)
	}
	claude, err := installerTar(n, fixtureConfig(), model.Bootstrap{NodeToken: "mcn_fake", APIKey: "fake", ServerURL: fixtureConfig().APIURL, DaemonID: n.DaemonID})
	if err != nil || !strings.Contains(string(claude), "secrets/bootstrap.json") || strings.Contains(string(claude), "aurora-enrollment") {
		t.Fatalf("claude installer err=%v", err)
	}
	if _, err := installerTar(n, auroraConfig(), model.Bootstrap{EnrollmentToken: "nope", DaemonID: n.DaemonID}); !errors.Is(err, model.ErrInvalidRequest) {
		t.Fatalf("invalid enrollment token accepted: %v", err)
	}
}

func TestAuroraBootstrapValidationRejectsForeignPayload(t *testing.T) {
	n := auroraNode()
	raw, err := installerTar(n, auroraConfig(), auroraBootstrap(n))
	if err != nil {
		t.Fatal(err)
	}
	data := Resource{Role: "secrets", Labels: map[string]string{"multica.fleet.node": fixtureLabels("node")["multica.fleet.node"]}}
	engine := &sdkEngine{cfg: auroraConfig()}
	if err := engine.validateBootstrap(raw, data); err != nil {
		t.Fatalf("canonical aurora installer rejected: %v", err)
	}
	claudeRaw, err := installerTar(n, fixtureConfig(), model.Bootstrap{NodeToken: "mcn_fake", APIKey: "fake", ServerURL: fixtureConfig().APIURL, DaemonID: n.DaemonID})
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.validateBootstrap(claudeRaw, data); !errors.Is(err, model.ErrInvalidRequest) {
		t.Fatalf("claude installer accepted by the aurora profile: %v", err)
	}
}

func TestInspectAuroraEnvironment(t *testing.T) {
	base := []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME=" + model.NodeHome, "FLEET_NODE_MAX_RUNS=1"}
	managed := []string{"MULTICA_MANAGED=1", "MULTICA_SERVER_URL=http://api.internal:8080", "MULTICA_MANAGED_ENROLLMENT_TOKEN_FILE=" + model.AuroraEnrollmentFile}
	agent := []string{"MULTICA_CLAUDE_PATH=" + model.AuroraClaudePath}
	full := append(append(append([]string{}, base...), managed...), agent...)
	full = append(full, "ANTHROPIC_BASE_URL=https://ark.example.com", "ANTHROPIC_MODEL=ark-model")
	if !inspectEnvironment(full, 1, auroraConfig().Aurora) {
		t.Fatal("valid aurora environment rejected")
	}
	if inspectEnvironment(full, 1, nil) {
		t.Fatal("aurora environment accepted without the profile")
	}
	if inspectEnvironment(base, 1, auroraConfig().Aurora) {
		t.Fatal("missing managed enrollment environment accepted")
	}
	// The provider supplies the agent path, so an adopted container that omits
	// it would let the daemon fall back to a PATH lookup the image no longer
	// satisfies.
	noAgent := append(append([]string{}, base...), managed...)
	noAgent = append(noAgent, "ANTHROPIC_BASE_URL=https://ark.example.com", "ANTHROPIC_MODEL=ark-model")
	if inspectEnvironment(noAgent, 1, auroraConfig().Aurora) {
		t.Fatal("aurora environment without the provider agent path accepted")
	}
	// The neutral image no longer carries the Node base banners; a container
	// that presents them is drift and must fail closed.
	for _, banner := range []string{"NODE_VERSION=22.23.3", "YARN_VERSION=1.22.22"} {
		if inspectEnvironment(append(append([]string{}, full...), banner), 1, auroraConfig().Aurora) {
			t.Fatalf("aurora environment with baked %s accepted", banner)
		}
	}
	// The node image declares where it placed the vendored expert tree and both
	// the node container and the bootstrap helper inherit it. The helper carries
	// no Aurora profile, so the key must validate with aurora == nil too.
	expert := model.AuroraExpertsDirEnv + "=" + model.AuroraExpertsDir
	if !inspectEnvironment(append(append([]string{}, full...), expert), 1, auroraConfig().Aurora) {
		t.Fatal("image-declared expert directory rejected")
	}
	if !inspectEnvironment([]string{base[0], base[1], expert}, 0, nil) {
		t.Fatal("bootstrap helper environment rejected")
	}
	if inspectEnvironment(append(append([]string{}, full...), model.AuroraExpertsDirEnv+"=/tmp/experts"), 1, auroraConfig().Aurora) {
		t.Fatal("unexpected expert directory accepted")
	}
	credentialed := append(append([]string{}, base...), "MULTICA_MANAGED=1", "MULTICA_SERVER_URL=http://user:pass@api.internal:8080", "MULTICA_MANAGED_ENROLLMENT_TOKEN_FILE="+model.AuroraEnrollmentFile)
	credentialed = append(credentialed, agent...)
	if inspectEnvironment(credentialed, 1, auroraConfig().Aurora) {
		t.Fatal("credentialed server url accepted")
	}
}

// TestInspectAuroraEnvironmentRejectsConfigDrift pins the adoption authority's
// config-derived environment comparison: the values nodeEnv builds from
// cfg.Aurora must match the live container exactly, and an ANTHROPIC_* variable
// the config does not carry is rejected rather than structurally accepted.
func TestInspectAuroraEnvironmentRejectsConfigDrift(t *testing.T) {
	cfg := auroraConfig()
	canonical := []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=" + model.NodeHome,
		"FLEET_NODE_MAX_RUNS=1",
		model.AuroraManagedEnv + "=1",
		model.AuroraServerURLEnv + "=" + cfg.Aurora.ServerURL,
		model.AuroraEnrollmentFileEnv + "=" + model.AuroraEnrollmentFile,
		model.AuroraClaudePathEnv + "=" + model.AuroraClaudePath,
		model.AuroraAnthropicBaseURLEnv + "=" + cfg.Aurora.AnthropicBaseURL,
		model.AuroraAnthropicModelEnv + "=" + cfg.Aurora.AnthropicModel,
	}
	withValue := func(key, value string) []string {
		out := append([]string(nil), canonical...)
		for i, entry := range out {
			if strings.HasPrefix(entry, key+"=") {
				out[i] = key + "=" + value
				return out
			}
		}
		return append(out, key+"="+value)
	}
	without := func(keys ...string) []string {
		drop := map[string]bool{}
		for _, k := range keys {
			drop[k] = true
		}
		out := make([]string, 0, len(canonical))
		for _, entry := range canonical {
			k, _, _ := strings.Cut(entry, "=")
			if !drop[k] {
				out = append(out, entry)
			}
		}
		return out
	}

	if !inspectEnvironment(canonical, 1, cfg.Aurora) {
		t.Fatal("canonical aurora environment rejected")
	}
	if inspectEnvironment(withValue(model.AuroraServerURLEnv, "http://other.internal:9090"), 1, cfg.Aurora) {
		t.Fatal("adopted a node built for a different server url")
	}
	if inspectEnvironment(withValue(model.AuroraAnthropicBaseURLEnv, "https://other.example.com"), 1, cfg.Aurora) {
		t.Fatal("adopted a node built for a different anthropic base url")
	}
	if inspectEnvironment(withValue(model.AuroraAnthropicModelEnv, "other-model"), 1, cfg.Aurora) {
		t.Fatal("adopted a node built for a different anthropic model")
	}
	if inspectEnvironment(without(model.AuroraAnthropicBaseURLEnv), 1, cfg.Aurora) {
		t.Fatal("adopted a node missing its configured anthropic base url")
	}
	if inspectEnvironment(without(model.AuroraAnthropicModelEnv), 1, cfg.Aurora) {
		t.Fatal("adopted a node missing its configured anthropic model")
	}
	if inspectEnvironment(withValue(model.AuroraClaudePathEnv, "/opt/other/claude"), 1, cfg.Aurora) {
		t.Fatal("adopted a node with a non-provider agent path")
	}
	if inspectEnvironment(without(model.AuroraClaudePathEnv), 1, cfg.Aurora) {
		t.Fatal("adopted a node missing the provider agent path")
	}
	// NODE_VERSION/YARN_VERSION come from a Node base image, which the neutral
	// final stage no longer uses; either one is forbidden drift.
	if inspectEnvironment(append(append([]string{}, canonical...), "NODE_VERSION=22.23.3"), 1, cfg.Aurora) {
		t.Fatal("adopted a node carrying NODE_VERSION")
	}
	if inspectEnvironment(append(append([]string{}, canonical...), "YARN_VERSION=1.22.22"), 1, cfg.Aurora) {
		t.Fatal("adopted a node carrying YARN_VERSION")
	}

	// A deployment that carries no override must reject ANTHROPIC_* entirely,
	// and must likewise accept a node that has neither variable.
	noOverride := *cfg.Aurora
	noOverride.AnthropicBaseURL = ""
	noOverride.AnthropicModel = ""
	if inspectEnvironment(canonical, 1, &noOverride) {
		t.Fatal("ANTHROPIC_* accepted when the config carries none")
	}
	if !inspectEnvironment(without(model.AuroraAnthropicBaseURLEnv, model.AuroraAnthropicModelEnv), 1, &noOverride) {
		t.Fatal("override-free config rejected a node without anthropic variables")
	}
}

// TestProviderNodeEnvAuroraClaudeEnv proves the Aurora node environment carries
// exactly the configured claude_env pairs, in deterministic order, after the
// fixed managed variables. The Claude profile stays at its original two entries
// (pinned by TestClaudeProfileEnvUnchanged).
func TestProviderNodeEnvAuroraClaudeEnv(t *testing.T) {
	cfg := auroraConfig()
	cfg.Aurora.ClaudeEnv = map[string]string{
		"ENABLE_TOOL_SEARCH":             "true",
		"API_TIMEOUT_MS":                 "600000",
		"ANTHROPIC_DEFAULT_SONNET_MODEL": "glm-5.3-flash[1M]",
	}
	p := &Provider{cfg: cfg}
	want := []string{
		"HOME=" + model.NodeHome,
		"FLEET_NODE_MAX_RUNS=1",
		"MULTICA_MANAGED=1",
		"MULTICA_SERVER_URL=http://api.internal:8080",
		"MULTICA_MANAGED_ENROLLMENT_TOKEN_FILE=" + model.AuroraEnrollmentFile,
		"MULTICA_CLAUDE_PATH=" + model.AuroraClaudePath,
		"ANTHROPIC_BASE_URL=https://ark.example.com",
		"ANTHROPIC_MODEL=ark-model",
		"ANTHROPIC_DEFAULT_SONNET_MODEL=glm-5.3-flash[1M]",
		"API_TIMEOUT_MS=600000",
		"ENABLE_TOOL_SEARCH=true",
	}
	if got := p.nodeEnv(auroraNode()); !reflect.DeepEqual(got, want) {
		t.Fatalf("aurora node env = %v, want %v", got, want)
	}
	// An empty map adds nothing to the pre-existing environment.
	empty := auroraConfig()
	if got := (&Provider{cfg: empty}).nodeEnv(auroraNode()); len(got) != 8 {
		t.Fatalf("empty claude_env changed env: %v", got)
	}
}

// TestInspectAuroraClaudeEnvExact pins the adoption authority for claude_env:
// exactly the configured key/value pairs are accepted, a missing or mutated
// configured pair is rejected, and any other key -- allowlisted or not -- fails
// closed unless the configuration carries it.
func TestInspectAuroraClaudeEnvExact(t *testing.T) {
	cfg := auroraConfig()
	cfg.Aurora.ClaudeEnv = map[string]string{
		"CLAUDE_CODE_SUBAGENT_MODEL": "m",
		"API_TIMEOUT_MS":             "5",
	}
	fixed := []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=" + model.NodeHome,
		"FLEET_NODE_MAX_RUNS=1",
		"MULTICA_MANAGED=1",
		"MULTICA_SERVER_URL=" + cfg.Aurora.ServerURL,
		"MULTICA_MANAGED_ENROLLMENT_TOKEN_FILE=" + model.AuroraEnrollmentFile,
		"MULTICA_CLAUDE_PATH=" + model.AuroraClaudePath,
		"ANTHROPIC_BASE_URL=" + cfg.Aurora.AnthropicBaseURL,
		"ANTHROPIC_MODEL=" + cfg.Aurora.AnthropicModel,
	}
	with := func(entries ...string) []string { return append(append([]string{}, fixed...), entries...) }
	if !inspectEnvironment(with("API_TIMEOUT_MS=5", "CLAUDE_CODE_SUBAGENT_MODEL=m"), 1, cfg.Aurora) {
		t.Fatal("exact claude_env set rejected")
	}
	if inspectEnvironment(with("CLAUDE_CODE_SUBAGENT_MODEL=m"), 1, cfg.Aurora) {
		t.Fatal("missing configured claude_env key accepted")
	}
	if inspectEnvironment(with("API_TIMEOUT_MS=6", "CLAUDE_CODE_SUBAGENT_MODEL=m"), 1, cfg.Aurora) {
		t.Fatal("mutated claude_env value accepted")
	}
	if inspectEnvironment(with("API_TIMEOUT_MS=5", "CLAUDE_CODE_SUBAGENT_MODEL=m", "ENABLE_TOOL_SEARCH=true"), 1, cfg.Aurora) {
		t.Fatal("allowlisted but unconfigured claude_env key accepted")
	}
	if inspectEnvironment(with("API_TIMEOUT_MS=5", "CLAUDE_CODE_SUBAGENT_MODEL=m", "ANTHROPIC_API_KEY=secret"), 1, cfg.Aurora) {
		t.Fatal("unconfigured credential key accepted")
	}
	// A configured provider credential is adopted exactly; a different value is
	// drift.
	providerCfg := auroraConfig()
	providerCfg.Aurora.ClaudeEnv = map[string]string{"ANTHROPIC_API_KEY": "ark-key", "VOLC_ASR_API_KEY": "asr-key"}
	if !inspectEnvironment(with("ANTHROPIC_API_KEY=ark-key", "VOLC_ASR_API_KEY=asr-key"), 1, providerCfg.Aurora) {
		t.Fatal("configured provider credentials rejected")
	}
	if inspectEnvironment(with("ANTHROPIC_API_KEY=other", "VOLC_ASR_API_KEY=asr-key"), 1, providerCfg.Aurora) {
		t.Fatal("drifted provider credential accepted")
	}
	// A configuration with no claude_env rejects any extra variable and accepts
	// the fixed environment unchanged.
	none := *cfg.Aurora
	none.ClaudeEnv = nil
	if inspectEnvironment(with("API_TIMEOUT_MS=5", "CLAUDE_CODE_SUBAGENT_MODEL=m"), 1, &none) {
		t.Fatal("claude_env accepted when the config carries none")
	}
	if !inspectEnvironment(fixed, 1, &none) {
		t.Fatal("clean node rejected when the config carries no claude_env")
	}
}

// TestNodeHostConfigAuroraTmpfsHardening restores the assertion the retired
// egress test carried: every writable Aurora surface is nosuid, nodev and noexec
// and owned by the sandbox user, and only the Aurora profile adds them. The
// adoption authority compares Tmpfs against NodeHostConfig's own output, so
// constant drift here would otherwise be invisible.
func TestNodeHostConfigAuroraTmpfsHardening(t *testing.T) {
	spec := model.Spec{CPUs: 2, MemoryBytes: 4 << 30, Pids: 256, MaxRuns: 1}
	h := NodeHostConfig(spec, true, auroraConfig().Aurora)
	want := map[string]string{
		model.AuroraWorkspaceMount: auroraWorkspaceTmpfs,
		model.AuroraTmpMount:       auroraTmpTmpfs,
		model.AuroraRunMount:       auroraRunTmpfs,
	}
	if !reflect.DeepEqual(h.Tmpfs, want) {
		t.Fatalf("tmpfs = %v, want %v", h.Tmpfs, want)
	}
	for mount, opts := range h.Tmpfs {
		for _, required := range []string{"nosuid", "nodev", "noexec", "uid=10001", "gid=10001"} {
			if !strings.Contains(opts, required) {
				t.Fatalf("tmpfs %s missing %s: %s", mount, required, opts)
			}
		}
	}
	if !h.ReadonlyRootfs || !reflect.DeepEqual(h.SecurityOpt, []string{"no-new-privileges:true"}) {
		t.Fatalf("aurora host config = %+v", h)
	}
	claude := NodeHostConfig(spec, true, nil)
	if claude.ReadonlyRootfs || len(claude.Tmpfs) != 0 || !reflect.DeepEqual(claude.SecurityOpt, []string{"no-new-privileges:true"}) {
		t.Fatalf("claude hostconfig changed: %+v", claude)
	}
}
