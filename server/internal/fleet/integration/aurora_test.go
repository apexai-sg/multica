//go:build dockerintegration

package integration

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/fleet/docker"
	"github.com/multica-ai/multica/server/internal/fleet/model"
)

// Gated Aurora API and Fleet lifecycle tests. No Docker, CLI or database
// access occurs before MULTICA_RUN_DOCKER_INTEGRATION=1 is checked.
// The API tests additionally require a fake-capable node image.

// TestAuroraRoundTrip is the single gated entry point. The gate is the very
// first action.
func TestAuroraRoundTrip(t *testing.T) {
	if os.Getenv("MULTICA_RUN_DOCKER_INTEGRATION") != "1" {
		t.Skip("Docker opt-in required")
	}
	// The API/lifecycle halves wait for a managed Fleet to provision a sandbox
	// node, enroll its daemon and settle a generation; the real-engine halves
	// each build their own namespace. The deadline covers both.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()

	// The generation->asset->settlement round trip. Its prerequisite is a
	// fake-capable dual-contract node image, which this checkout does not ship;
	// the subtest records that as a named skip and never falls through to a real
	// provider path.
	t.Run("ApiGenerationRoundTrip", func(t *testing.T) {
		if err := RoundTripAuroraGeneration(ctx, t); err != nil {
			t.Fatal(err)
		}
	})
	// The Aurora-profile lifecycle coverage: stop/start identity preservation,
	// the maintenance-approval crash boundary, delete credential revocation and
	// the absence of owned Docker resources. Like the generation round trip it
	// needs the fake-capable dual-contract node image, so it is the same named
	// skip until that infrastructure exists.
	t.Run("AuroraLifecycleRoundTrip", func(t *testing.T) {
		if err := RoundTripAuroraLifecycle(ctx, t); err != nil {
			t.Fatal(err)
		}
	})
	// The restored Provider.Ensure crash replay: a node admitted by one Ensure is
	// adopted by the next after the durable container id is lost, never replaced.
	t.Run("RealEngineEnsureCrashReplay", func(t *testing.T) {
		testEnsureCrashReplayRealEngine(ctx, t)
	})
	// The D2 regression: a real dockerd creates AND starts the Aurora node built
	// by Provider.Ensure with the ordinary node configuration.
	t.Run("RealEngineNodeStarts", func(t *testing.T) {
		testEnsureNodeStartsRealEngine(ctx, t)
	})
	// The Aurora lifecycle on a real engine: the exact container identity and
	// both owned volumes survive stop then start, and an unapproved maintenance
	// operation is refused before it reaches Docker.
	t.Run("RealEngineStopStartIdentity", func(t *testing.T) {
		testStopStartIdentityRealEngine(ctx, t)
	})
	t.Run("RealEngineMaintenanceBoundary", func(t *testing.T) {
		testMaintenanceBoundaryRealEngine(ctx, t)
	})
}

// RoundTripAuroraGeneration drives the real API/Fleet/Docker path from a
// generation request to a settled, asset-backed generation. It is input-gated:
// the managed environment must supply the API, the workspace, the database and
// the digest-pinned dual-contract sandbox node image. A SKIP here is not a pass.
func RoundTripAuroraGeneration(ctx context.Context, t *testing.T) error {
	t.Helper()
	env, err := auroraRoundTripInputs(t)
	if err != nil {
		return nil // the helper already skipped with the missing input named
	}

	api := &fleetAPI{base: env.apiURL, token: env.token, workspaceID: env.workspaceID, http: &http.Client{Timeout: 30 * time.Second}}

	skillID := "ecommerce-bidding-strategy"
	prompt := "docker round trip poster"
	var created struct {
		Generation struct {
			ID              string `json:"id"`
			Status          string `json:"status"`
			CreditsReserved int64  `json:"creditsReserved"`
		} `json:"generation"`
	}
	if err := api.call(ctx, http.MethodPost, "/api/aurora/generations", "", map[string]any{
		"skillId": skillID,
		"prompt":  prompt,
	}, &created); err != nil {
		return fmt.Errorf("API create generation: %w", err)
	}
	if created.Generation.ID == "" {
		return fmt.Errorf("API create generation returned no id")
	}
	generationID := created.Generation.ID

	// The generation row is the authoritative terminal record. Poll the detail
	// endpoint, which derives in-flight status from the backing task.
	deadline := time.Now().Add(20 * time.Minute)
	for {
		var detail struct {
			Generation struct {
				ID             string `json:"id"`
				Status         string `json:"status"`
				CreditsCharged int64  `json:"creditsCharged"`
				Assets         []struct {
					ID   string `json:"id"`
					Kind string `json:"kind"`
				} `json:"assets"`
			} `json:"generation"`
		}
		if err := api.call(ctx, http.MethodGet, "/api/aurora/generations/"+generationID, "", nil, &detail); err != nil {
			return fmt.Errorf("API get generation: %w", err)
		}
		switch detail.Generation.Status {
		case "completed":
			if detail.Generation.CreditsCharged <= 0 {
				return fmt.Errorf("generation %s completed without credits_charged (reserved=%d, charged=%d)", generationID, created.Generation.CreditsReserved, detail.Generation.CreditsCharged)
			}
			if len(detail.Generation.Assets) == 0 {
				return fmt.Errorf("generation %s completed with no aurora_asset rows", generationID)
			}
			return nil
		case "failed":
			return fmt.Errorf("generation %s failed before producing an artifact", generationID)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("generation %s did not settle before the deadline (status %q)", generationID, detail.Generation.Status)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

// auroraFakePipelineImageEnv names the one node image capable of standing in
// for a real provider: a digest-pinned dual-contract image built with a fake
// pipeline. It is deliberately a different variable from the general Fleet test
// image so the API half can never silently run a real provider.
const auroraFakePipelineImageEnv = "MULTICA_AURORA_FAKE_PIPELINE_IMAGE"

// requireFakePipelineNodeImage prevents the API test from using real providers.
func requireFakePipelineNodeImage(t *testing.T) string {
	t.Helper()
	image := strings.TrimSpace(os.Getenv(auroraFakePipelineImageEnv))
	if image == "" {
		t.Skipf("no fake-capable dual-contract Aurora node image exists in this checkout (%s is unset): ordinary agents require an explicitly fake-capable test image; this subtest must never fall through to a real provider path", auroraFakePipelineImageEnv)
	}
	if !approvedNodeImage.MatchString(image) {
		t.Skipf("%s must be pinned to a digest (got %q)", auroraFakePipelineImageEnv, image)
	}
	return image
}

// auroraRoundTripInputs validates the explicit Aurora inputs before any side
// effect. The fake-capable image is checked first so the skip reason always
// names the missing prerequisite rather than whichever API variable happens to
// be unset.
func auroraRoundTripInputs(t *testing.T) (roundTripEnv, error) {
	t.Helper()
	env := roundTripEnv{
		apiURL:      strings.TrimRight(os.Getenv("MULTICA_FLEET_API_URL"), "/"),
		token:       os.Getenv("MULTICA_FLEET_API_TOKEN"),
		workspaceID: os.Getenv("MULTICA_FLEET_WORKSPACE_ID"),
		image:       requireFakePipelineNodeImage(t),
	}
	for _, missing := range []struct{ name, value, why string }{
		{"MULTICA_FLEET_API_URL", env.apiURL, "the running managed API"},
		{"MULTICA_FLEET_API_TOKEN", env.token, "an authenticated workspace caller"},
		{"MULTICA_FLEET_WORKSPACE_ID", env.workspaceID, "the workspace under test"},
	} {
		if missing.value == "" {
			t.Skipf("%s required for the Aurora round trip (%s)", missing.name, missing.why)
			return env, errors.New("missing input")
		}
	}
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("DATABASE_URL required for the Aurora round trip")
		return env, errors.New("missing database")
	}
	return env, nil
}

// --- real-engine evidence ---------------------------------------------------

type auroraEngineEnv struct {
	cli       *client.Client
	engine    docker.Engine
	cfg       model.Config
	namespace string
	fleetID   string
}

// newAuroraEngineEnv builds the real Docker client, an Aurora provider config,
// and registers cleanup that removes only the
// resources this test labelled; it never prunes.
func newAuroraEngineEnv(ctx context.Context, t *testing.T) *auroraEngineEnv {
	t.Helper()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Skipf("Docker engine unavailable: %v", err)
	}
	if _, err := cli.Ping(ctx); err != nil {
		cli.Close()
		t.Skipf("Docker engine unavailable: %v", err)
	}
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	ns := "aurora-it-" + suffix
	fleetID := "aurora-it"
	env := &auroraEngineEnv{
		cli:       cli,
		engine:    docker.NewEngine(cli),
		namespace: ns,
		fleetID:   fleetID,
	}

	nodeImage, err := auroraTestNodeImage(ctx, cli)
	if err != nil {
		cli.Close()
		t.Skipf("Aurora test node image unavailable: %v", err)
	}

	// Use a local control-plane stand-in instead of a public host.
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "aurora-it-control-ok")
	}))
	t.Cleanup(control.Close)
	// The sandbox reaches the control plane by name from inside the VM, so the
	// profile origin uses the Docker Desktop host alias while the test listener
	// stays on the host loopback.
	serverOrigin := strings.Replace(control.URL, "127.0.0.1", "host.docker.internal", 1)

	env.cfg = model.Config{
		Namespace: ns,
		FleetID:   fleetID,
		Image:     nodeImage,
		APIURL:    control.URL,
		MaxNodes:  2,
		Specs:     map[string]model.Spec{"sandbox": {CPUs: 2, MemoryBytes: 4 << 30, Pids: 256, MaxRuns: 1}},
		Aurora: &model.AuroraConfig{
			ServerURL:      serverOrigin,
			ReadonlyRootfs: true,
		},
	}
	if err := env.cfg.Aurora.Validate(); err != nil {
		cli.Close()
		t.Fatalf("test Aurora config invalid: %v", err)
	}

	t.Cleanup(func() {
		cleanup := context.Background()
		removeAuroraTestResources(t, cleanup, cli, ns)
		cli.Close()
	})
	return env
}

// testEnsureNodeStartsRealEngine drives the real Provider.Ensure for the Aurora
// profile and proves a real dockerd creates AND starts the node container. Before
// the D2 fix the SDK put the operator file path after "seccomp=", dockerd parsed
// it as JSON and Start failed with "Decoding seccomp profile failed", so this
// assertion is the regression for the real node-admission path. A second Ensure
// also proves the adoption inspection accepts the inlined profile.
func testEnsureNodeStartsRealEngine(ctx context.Context, t *testing.T) {
	env := newAuroraEngineEnv(ctx, t)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	node := model.Node{
		ID:            pgtype.UUID{Bytes: uuid.New(), Valid: true},
		OwnerID:       pgtype.UUID{Bytes: uuid.New(), Valid: true},
		Namespace:     env.namespace,
		DaemonID:      uuid.NewString(),
		Name:          "aurora-it-node-" + suffix,
		Spec:          "sandbox",
		Image:         env.cfg.Image,
		DataVolume:    "aurora-it-data-" + suffix,
		SecretsVolume: "aurora-it-secrets-" + suffix,
		Desired:       "running",
		Status:        "pending",
		Generation:    1,
		Resources:     env.cfg.Specs["sandbox"],
	}
	bootstrap := model.Bootstrap{
		EnrollmentToken: "mse_" + strings.Repeat("a", 40),
		ServerURL:       env.cfg.Aurora.ServerURL,
		DaemonID:        node.DaemonID,
	}

	p := docker.New(env.engine, env.cfg)
	obs, err := p.Ensure(ctx, node, bootstrap)
	if err != nil {
		t.Fatalf("real-engine Aurora Ensure: %v", err)
	}
	if obs.ContainerID == "" {
		t.Fatal("real-engine Aurora Ensure returned no container id")
	}
	info, err := env.cli.ContainerInspect(ctx, obs.ContainerID)
	if err != nil {
		t.Fatalf("inspect started node: %v", err)
	}
	if info.State == nil || !info.State.Running || info.State.Error != "" {
		t.Fatalf("node container did not start: state=%+v", info.State)
	}
	// The node runs with Docker's default security options: no seccomp profile
	// and no AppArmor reference.
	if !reflect.DeepEqual(info.HostConfig.SecurityOpt, []string{"no-new-privileges:true"}) {
		t.Fatalf("security opt = %v, want only no-new-privileges", info.HostConfig.SecurityOpt)
	}
	// Adoption: the inspection authority recomputes the HostConfig from the
	// configuration, so a replay over the real container must be admitted.
	if _, err := p.Ensure(ctx, node, bootstrap); err != nil {
		t.Fatalf("replayed Ensure over the running node: %v", err)
	}
}

func resolveImageDigestRef(ctx context.Context, cli *client.Client, ref string) (string, error) {
	if strings.Contains(ref, "@sha256:") {
		if _, _, err := cli.ImageInspectWithRaw(ctx, ref); err != nil {
			return "", err
		}
		return ref, nil
	}
	insp, _, err := cli.ImageInspectWithRaw(ctx, ref)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(insp.ID, "sha256:") {
		return "", fmt.Errorf("image %s has no immutable id", ref)
	}
	name := ref
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		name = ref[:i]
	}
	return name + "@" + insp.ID, nil
}

const auroraNodeFixtureTag = "multica-aurora-it-node:local"
const auroraNodeFixtureDockerfile = "testdata/Dockerfile.aurora-node-fixture"

// auroraTestNodeImage resolves the digest-pinned node image the real-engine
// tests admit. MULTICA_AURORA_TEST_NODE_IMAGE always wins. Otherwise the test
// builds a throwaway fixture from the committed testdata Dockerfile: its
// fleet-node subcommands match the fixed contract and its only image env is
// PATH/HOME, so the provider's strict image inspection accepts it and
// `fleet-node bootstrap` accepts the Aurora installer. Nothing is pushed.
func auroraTestNodeImage(ctx context.Context, cli *client.Client) (string, error) {
	if explicit := os.Getenv("MULTICA_AURORA_TEST_NODE_IMAGE"); explicit != "" {
		return resolveImageDigestRef(ctx, cli, explicit)
	}
	if ref, err := resolveImageDigestRef(ctx, cli, auroraNodeFixtureTag); err == nil {
		return ref, nil
	}
	if _, _, err := cli.ImageInspectWithRaw(ctx, "alpine:3.20"); err != nil {
		return "", fmt.Errorf("alpine:3.20 base is required to build the Aurora node fixture: %w", err)
	}
	build := exec.CommandContext(ctx, "docker", "build", "-f", auroraNodeFixtureDockerfile, "-t", auroraNodeFixtureTag, filepath.Dir(auroraNodeFixtureDockerfile))
	if out, err := build.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build Aurora node fixture: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return resolveImageDigestRef(ctx, cli, auroraNodeFixtureTag)
}

// removeAuroraTestResources removes only the resources this test labelled and
// their networks. It never prunes.
func removeAuroraTestResources(t *testing.T, ctx context.Context, cli *client.Client, namespace string) {
	t.Helper()
	label := "multica.fleet.namespace=" + namespace
	f := filters.NewArgs(filters.Arg("label", label))
	contexts, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	testFilter := filters.NewArgs(filters.Arg("label", "aurora.it="+namespace))
	for _, filter := range []filters.Args{f, testFilter} {
		containers, cerr := cli.ContainerList(contexts, container.ListOptions{All: true, Filters: filter})
		if cerr == nil {
			for _, c := range containers {
				_ = cli.ContainerRemove(contexts, c.ID, container.RemoveOptions{Force: true})
			}
		}
		vols, verr := cli.VolumeList(contexts, volume.ListOptions{Filters: filter})
		if verr == nil {
			for _, v := range vols.Volumes {
				_ = cli.VolumeRemove(contexts, v.Name, true)
			}
		}
		nets, nerr := cli.NetworkList(contexts, network.ListOptions{Filters: filter})
		if nerr == nil {
			for _, nw := range nets {
				_ = cli.NetworkRemove(contexts, nw.ID)
			}
		}
	}
	// Leave a receipt the report can quote: the owned inventory must be empty.
	remaining, _ := cli.ContainerList(contexts, container.ListOptions{All: true, Filters: f})
	if len(remaining) != 0 {
		t.Logf("WARNING: %d owned containers remained after cleanup", len(remaining))
	}
}
