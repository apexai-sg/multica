//go:build dockerintegration

package integration

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
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
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/fleet/docker"
	"github.com/multica-ai/multica/server/internal/fleet/model"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// This file owns the Aurora-profile lifecycle coverage Task 7's review required.
//
// Two layers, both under the dockerintegration build tag and the
// MULTICA_RUN_DOCKER_INTEGRATION=1 gate:
//
//   - Real-engine provider subtests run against the local Docker Desktop engine
//     through the production Provider. They restore the Ensure crash-replay that
//     D2 blocked and add the stop/start identity and maintenance-approval
//     boundary behaviour on the real Aurora node.
//   - RoundTripAuroraLifecycle is the API/Fleet equivalent of the generic
//     RoundTripFakeNode lifecycle (stop/start identity, the maintenance crash
//     boundary, delete credential revocation and the absence of owned Docker
//     resources). It is input-gated and requires the fake-capable dual-contract
//     node image named by requireFakePipelineNodeImage; without that image it is
//     the same named skip as the generation round trip and never reaches a real
//     provider.

// auroraProvider is the production Provider over the real engine with the test
// Aurora config, including the seccomp profile file the config points at.
func auroraProvider(env *auroraEngineEnv) *docker.Provider {
	return docker.New(env.engine, env.cfg)
}

func testNodeUUID(n model.Node) string { return uuid.UUID(n.ID.Bytes).String() }

// ensureAuroraTestNode admits one Aurora node through the production Ensure and
// returns it with its durable container id recorded, mirroring what the
// reconciler persists after a successful admission.
func ensureAuroraTestNode(ctx context.Context, t *testing.T, env *auroraEngineEnv) (model.Node, model.Bootstrap, string) {
	t.Helper()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	node := model.Node{
		ID:            pgtype.UUID{Bytes: uuid.New(), Valid: true},
		OwnerID:       pgtype.UUID{Bytes: uuid.New(), Valid: true},
		Namespace:     env.namespace,
		DaemonID:      uuid.NewString(),
		Name:          "aurora-it-life-" + suffix,
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
	obs, err := auroraProvider(env).Ensure(ctx, node, bootstrap)
	if err != nil {
		t.Fatalf("real-engine Aurora Ensure: %v", err)
	}
	if obs.ContainerID == "" {
		t.Fatal("real-engine Aurora Ensure returned no container id")
	}
	node.ContainerID = obs.ContainerID
	return node, bootstrap, obs.ContainerID
}

// assertTestNodeContainerCount proves how many containers carry this node's
// ownership label, so a replay that created a replacement cannot pass as
// adoption.
func assertTestNodeContainerCount(ctx context.Context, t *testing.T, env *auroraEngineEnv, n model.Node, want int) {
	t.Helper()
	// The node and its egress sidecar share the node label; only role=node is
	// the node container whose duplication a replay must never cause.
	list, err := env.cli.ContainerList(ctx, container.ListOptions{
		All: true,
		Filters: filters.NewArgs(
			filters.Arg("label", "multica.fleet.node="+testNodeUUID(n)),
			filters.Arg("label", "multica.fleet.role=node"),
		),
	})
	if err != nil {
		t.Fatalf("list node containers: %v", err)
	}
	if len(list) != want {
		t.Fatalf("node-labelled containers = %d, want %d", len(list), want)
	}
}

// testEnsureCrashReplayRealEngine restores the Provider.Ensure crash replay the
// D2 seccomp defect removed. A reconciler can die after Ensure created and
// started the container but before the durable container id was written. The
// replay is handed a node whose ContainerID is empty, so it can only recover by
// adopting the deterministic container name. The same container identity and
// its volumes must survive, and no replacement may be created.
func testEnsureCrashReplayRealEngine(ctx context.Context, t *testing.T) {
	env := newAuroraEngineEnv(ctx, t)
	p := auroraProvider(env)
	node, bootstrap, containerID := ensureAuroraTestNode(ctx, t, env)

	created, err := env.cli.ContainerInspect(ctx, containerID)
	if err != nil {
		t.Fatalf("inspect admitted node: %v", err)
	}
	if created.State == nil || !created.State.Running {
		t.Fatalf("admitted node is not running: %+v", created.State)
	}

	node.ContainerID = ""
	obs, err := p.Ensure(ctx, node, bootstrap)
	if err != nil {
		t.Fatalf("replayed Ensure after the lost container id: %v", err)
	}
	if obs.ContainerID != containerID {
		t.Fatalf("replayed Ensure adopted %q, want the original %q", obs.ContainerID, containerID)
	}
	replayed, err := env.cli.ContainerInspect(ctx, containerID)
	if err != nil {
		t.Fatalf("inspect replayed node: %v", err)
	}
	if replayed.ID != containerID || replayed.State == nil || !replayed.State.Running {
		t.Fatalf("replayed node identity/state changed: id=%q state=%+v", replayed.ID, replayed.State)
	}
	if replayed.State.StartedAt != created.State.StartedAt {
		t.Fatalf("replayed Ensure restarted the node: %q -> %q", created.State.StartedAt, replayed.State.StartedAt)
	}
	if err := assertIdentityPreserved(created, replayed); err != nil {
		t.Fatalf("replayed Ensure identity/volumes: %v", err)
	}
	assertTestNodeContainerCount(ctx, t, env, node, 1)
}

// testStopStartIdentityRealEngine proves on a real engine that an approved stop
// followed by an approved start keeps the exact container identity and both
// owned volume mounts. It is the provider-level half of the brief's stop/start
// identity preservation.
func testStopStartIdentityRealEngine(ctx context.Context, t *testing.T) {
	env := newAuroraEngineEnv(ctx, t)
	p := auroraProvider(env)
	node, _, containerID := ensureAuroraTestNode(ctx, t, env)

	created, err := env.cli.ContainerInspect(ctx, containerID)
	if err != nil {
		t.Fatalf("inspect admitted node: %v", err)
	}

	node.Maintenance = true
	if _, err := p.Apply(ctx, node, model.Stop); err != nil {
		t.Fatalf("approved stop: %v", err)
	}
	stopped, err := env.cli.ContainerInspect(ctx, containerID)
	if err != nil {
		t.Fatalf("inspect stopped node: %v", err)
	}
	if stopped.ID != containerID || stopped.State == nil || stopped.State.Status != "exited" {
		t.Fatalf("stopped node identity/state = %q/%+v, want the original exited container", stopped.ID, stopped.State)
	}

	node.Maintenance = false
	if _, err := p.Apply(ctx, node, model.Start); err != nil {
		t.Fatalf("approved start: %v", err)
	}
	started, err := env.cli.ContainerInspect(ctx, containerID)
	if err != nil {
		t.Fatalf("inspect started node: %v", err)
	}
	if started.ID != containerID || started.State == nil || !started.State.Running {
		t.Fatalf("started node identity/state = %q/%+v, want the original running container", started.ID, started.State)
	}
	if err := assertIdentityPreserved(created, started); err != nil {
		t.Fatalf("stop/start identity/volumes: %v", err)
	}
	assertTestNodeContainerCount(ctx, t, env, node, 1)
}

// testMaintenanceBoundaryRealEngine proves both halves of the maintenance
// approval boundary on a real engine: an unapproved stop is refused before it
// reaches Docker (the container keeps running), and once approved, a crash
// between approve and apply is survived by replaying the approved stop on a
// fresh Provider, which recovers the same container rather than minting one.
func testMaintenanceBoundaryRealEngine(ctx context.Context, t *testing.T) {
	env := newAuroraEngineEnv(ctx, t)
	p := auroraProvider(env)
	node, _, containerID := ensureAuroraTestNode(ctx, t, env)

	created, err := env.cli.ContainerInspect(ctx, containerID)
	if err != nil {
		t.Fatalf("inspect admitted node: %v", err)
	}

	if _, err := p.Apply(ctx, node, model.Stop); !errors.Is(err, model.ErrConflict) {
		t.Fatalf("unapproved stop = %v, want ErrConflict", err)
	}
	still, err := env.cli.ContainerInspect(ctx, containerID)
	if err != nil {
		t.Fatalf("inspect after unapproved stop: %v", err)
	}
	if still.State == nil || !still.State.Running {
		t.Fatalf("unapproved stop mutated the container: %+v", still.State)
	}

	node.Maintenance = true
	if _, err := p.Apply(ctx, node, model.Stop); err != nil {
		t.Fatalf("approved stop: %v", err)
	}
	stopped, err := env.cli.ContainerInspect(ctx, containerID)
	if err != nil {
		t.Fatalf("inspect approved stop: %v", err)
	}
	if stopped.State == nil || stopped.State.Status != "exited" {
		t.Fatalf("approved stop state = %+v, want exited", stopped.State)
	}

	// A fresh Provider is a fresh reconciler process replaying the approved,
	// durable operation; the recovery must be the same container and volumes.
	replay := auroraProvider(env)
	if _, err := replay.Apply(ctx, node, model.Stop); err != nil {
		t.Fatalf("replayed approved stop: %v", err)
	}
	recovered, err := env.cli.ContainerInspect(ctx, containerID)
	if err != nil {
		t.Fatalf("inspect recovered node: %v", err)
	}
	if recovered.ID != stopped.ID {
		t.Fatalf("replayed stop changed container identity from %q to %q", stopped.ID, recovered.ID)
	}
	if recovered.State == nil || recovered.State.Status != "exited" {
		t.Fatalf("recovered node state = %+v, want exited", recovered.State)
	}
	if err := assertIdentityPreserved(created, recovered); err != nil {
		t.Fatalf("crash-boundary identity/volumes: %v", err)
	}
	assertTestNodeContainerCount(ctx, t, env, node, 1)
}

// --- Aurora API lifecycle round trip ----------------------------------------

// auroraLifecycleInputs validates the managed-environment inputs the lifecycle
// round trip needs. The fake-capable image and the restart hook are named
// first-class inputs so a missing one is a skip, never an implicit guess.
func auroraLifecycleInputs(t *testing.T) (roundTripEnv, error) {
	t.Helper()
	env := roundTripEnv{
		apiURL:      strings.TrimRight(os.Getenv("MULTICA_FLEET_API_URL"), "/"),
		token:       os.Getenv("MULTICA_FLEET_API_TOKEN"),
		workspaceID: os.Getenv("MULTICA_FLEET_WORKSPACE_ID"),
		image:       requireFakePipelineNodeImage(t),
		restartURL:  strings.TrimRight(os.Getenv("MULTICA_FLEET_RESTART_URL"), "/"),
	}
	for _, missing := range []struct{ name, value, why string }{
		{"MULTICA_FLEET_API_URL", env.apiURL, "the running managed API"},
		{"MULTICA_FLEET_API_TOKEN", env.token, "an authenticated workspace caller"},
		{"MULTICA_FLEET_WORKSPACE_ID", env.workspaceID, "the workspace under test"},
		{"MULTICA_FLEET_RESTART_URL", env.restartURL, "the operator-owned Fleet restart hook"},
	} {
		if missing.value == "" {
			t.Skipf("%s required for the Aurora lifecycle round trip (%s)", missing.name, missing.why)
			return env, errors.New("missing input")
		}
	}
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("DATABASE_URL required for the Aurora lifecycle round trip")
		return env, errors.New("missing database")
	}
	return env, nil
}

// awaitAuroraGenerationTerminal waits for the generation to settle so the
// sandbox node is idle before the lifecycle operations. It deliberately does
// not assert the artifact or the settlement; those stay the generation round
// trip's assertions.
func awaitAuroraGenerationTerminal(ctx context.Context, api *fleetAPI, generationID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		var detail struct {
			Generation struct {
				Status string `json:"status"`
			} `json:"generation"`
		}
		if err := api.call(ctx, http.MethodGet, "/api/aurora/generations/"+generationID, "", nil, &detail); err != nil {
			return err
		}
		switch detail.Generation.Status {
		case "completed", "failed":
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("generation %s did not settle before the lifecycle deadline (status %q)", generationID, detail.Generation.Status)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

// awaitAuroraBackendNode polls the one-row-per-workspace sandbox record until
// the provisioner has recorded the Fleet node identity the lifecycle routes
// address.
func awaitAuroraBackendNode(ctx context.Context, pool *pgxpool.Pool, workspaceID string) (string, error) {
	deadline := time.Now().Add(10 * time.Minute)
	for {
		var id string
		err := pool.QueryRow(ctx, `SELECT COALESCE(backend_node_id, '') FROM aurora_sandbox_node WHERE workspace_id = $1`, workspaceID).Scan(&id)
		if err == nil && id != "" {
			return id, nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("Aurora sandbox node for workspace %s never recorded a backend node id", workspaceID)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// awaitOwnedResourcesGone proves Delete removed every Docker resource this node
// owned. It is label-scoped and never prunes anything else.
func awaitOwnedResourcesGone(ctx context.Context, cli *client.Client, nodeID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	label := filters.NewArgs(filters.Arg("label", "multica.fleet.node="+nodeID))
	for {
		containers, cerr := cli.ContainerList(ctx, container.ListOptions{All: true, Filters: label})
		vols, verr := cli.VolumeList(ctx, volume.ListOptions{Filters: label})
		nets, nerr := cli.NetworkList(ctx, network.ListOptions{Filters: label})
		if cerr == nil && verr == nil && nerr == nil && len(containers) == 0 && len(vols.Volumes) == 0 && len(nets) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("Aurora node %s still owns Docker resources after delete", nodeID)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// RoundTripAuroraLifecycle is the Aurora-profile equivalent of the generic
// RoundTripFakeNode lifecycle. It provisions an Aurora node through the real
// generation path, then drives stop/start identity preservation, the
// approved-maintenance crash boundary, and delete with credential revocation
// and no leftover owned resources. It is input-gated; a SKIP is not a pass.
func RoundTripAuroraLifecycle(ctx context.Context, t *testing.T) error {
	t.Helper()
	env, err := auroraLifecycleInputs(t)
	if err != nil {
		return nil // the helper already skipped with the missing input named
	}
	pool, _ := testutil.NewFleetFixture(t)
	api := &fleetAPI{base: env.apiURL, token: env.token, workspaceID: env.workspaceID, http: &http.Client{Timeout: 30 * time.Second}}
	dockerCLI, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return fmt.Errorf("create docker client: %w", err)
	}
	defer dockerCLI.Close()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	// The create call provisions the workspace sandbox and enqueues the skill.
	// The lifecycle needs the node idle, so it waits for the generation to settle
	// first (the fake pipeline is what makes that possible) but makes no artifact
	// assertion of its own.
	var created struct {
		Generation struct {
			ID string `json:"id"`
		} `json:"generation"`
	}
	if err := api.call(ctx, http.MethodPost, "/api/aurora/generations", "", map[string]any{
		"skillId": "ecommerce-bidding-strategy",
		"prompt":  "aurora lifecycle " + suffix,
	}, &created); err != nil {
		return fmt.Errorf("API create generation: %w", err)
	}
	if created.Generation.ID == "" {
		return fmt.Errorf("API create generation returned no id")
	}
	if err := awaitAuroraGenerationTerminal(ctx, api, created.Generation.ID, 20*time.Minute); err != nil {
		return fmt.Errorf("await generation settle before lifecycle: %w", err)
	}

	backendNodeID, err := awaitAuroraBackendNode(ctx, pool, env.workspaceID)
	if err != nil {
		return err
	}
	ready, err := api.awaitNode(ctx, backendNodeID, func(n roundTripNode) bool { return n.Ready }, 10*time.Minute)
	if err != nil {
		return fmt.Errorf("await Aurora node ready: %w", err)
	}
	if ready.InstanceID == "" {
		return fmt.Errorf("ready Aurora node %s has no container instance id", ready.ID)
	}
	if !approvedNodeImage.MatchString(ready.ImageID) {
		return fmt.Errorf("Aurora node image %q is not digest pinned", ready.ImageID)
	}
	if ready.ImageID != env.image {
		return fmt.Errorf("Aurora node image %q is not the fake-capable test image %q", ready.ImageID, env.image)
	}
	createdInfo, err := inspectNodeContainer(ctx, dockerCLI, ready.InstanceID)
	if err != nil {
		return fmt.Errorf("inspect provisioned Aurora node: %w", err)
	}
	if createdInfo.State == nil || createdInfo.State.Status != "running" {
		return fmt.Errorf("provisioned Aurora node is not running: %+v", createdInfo.State)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		var ignored roundTripNode
		_ = api.call(cleanup, http.MethodDelete, "/api/cloud-runtime/nodes", "aurora-it-cleanup-"+suffix, map[string]any{"instance_id": ready.ID}, &ignored)
	}()

	// Stop is accepted before the restart, and the same intent key is replayed:
	// the operation must be the original one, not a second stop.
	stopKey := "aurora-it-stop-" + suffix
	var stopped roundTripNode
	if err := api.call(ctx, http.MethodPost, "/api/cloud-runtime/nodes/stop", stopKey, map[string]any{"instance_id": ready.ID}, &stopped); err != nil {
		return fmt.Errorf("API stop Aurora node: %w", err)
	}
	var stopReplay roundTripNode
	if err := api.call(ctx, http.MethodPost, "/api/cloud-runtime/nodes/stop", stopKey, map[string]any{"instance_id": ready.ID}, &stopReplay); err != nil {
		return fmt.Errorf("API replay stop Aurora node: %w", err)
	}
	if stopped.OperationID == "" || stopReplay.OperationID != stopped.OperationID {
		return fmt.Errorf("Aurora stop replay operation = %q, want the original %q", stopReplay.OperationID, stopped.OperationID)
	}
	if _, err := api.awaitNode(ctx, ready.ID, func(n roundTripNode) bool { return n.Status == "stopped" }, 3*time.Minute); err != nil {
		return fmt.Errorf("await Aurora node stopped: %w", err)
	}
	stoppedInfo, err := inspectNodeContainer(ctx, dockerCLI, ready.InstanceID)
	if err != nil {
		return fmt.Errorf("inspect stopped Aurora node: %w", err)
	}
	if stoppedInfo.State == nil || stoppedInfo.State.Status != "exited" {
		return fmt.Errorf("stopped Aurora node state = %+v, want exited", stoppedInfo.State)
	}

	if err := api.call(ctx, http.MethodPost, "/api/cloud-runtime/nodes/start", "aurora-it-start-"+suffix, map[string]any{"instance_id": ready.ID}, new(roundTripNode)); err != nil {
		return fmt.Errorf("API start Aurora node: %w", err)
	}
	if _, err := api.awaitNode(ctx, ready.ID, func(n roundTripNode) bool { return n.Ready }, 3*time.Minute); err != nil {
		return fmt.Errorf("await restarted Aurora node: %w", err)
	}
	startedInfo, err := inspectNodeContainer(ctx, dockerCLI, ready.InstanceID)
	if err != nil {
		return fmt.Errorf("inspect restarted Aurora node: %w", err)
	}
	if err := assertIdentityPreserved(createdInfo, startedInfo); err != nil {
		return fmt.Errorf("Aurora start identity/volumes: %w", err)
	}

	// Fleet restart then the approved-maintenance crash boundary: a stop accepted
	// before the restart must still be at-most-once after it.
	if err := postRestart(ctx, env.restartURL); err != nil {
		return fmt.Errorf("restart Fleet: %w", err)
	}
	if _, err := api.awaitNode(ctx, ready.ID, func(n roundTripNode) bool { return n.Ready }, 5*time.Minute); err != nil {
		return fmt.Errorf("await Aurora node after Fleet restart: %w", err)
	}
	crashKey := "aurora-it-crash-stop-" + suffix
	var crashStop roundTripNode
	if err := api.call(ctx, http.MethodPost, "/api/cloud-runtime/nodes/stop", crashKey, map[string]any{"instance_id": ready.ID}, &crashStop); err != nil {
		return fmt.Errorf("API crash-boundary stop: %w", err)
	}
	var crashReplay roundTripNode
	if err := api.call(ctx, http.MethodPost, "/api/cloud-runtime/nodes/stop", crashKey, map[string]any{"instance_id": ready.ID}, &crashReplay); err != nil {
		return fmt.Errorf("API crash-boundary replay: %w", err)
	}
	if crashStop.OperationID == "" || crashReplay.OperationID != crashStop.OperationID {
		return fmt.Errorf("Aurora crash-boundary replay operation = %q, want %q", crashReplay.OperationID, crashStop.OperationID)
	}
	if _, err := api.awaitNode(ctx, ready.ID, func(n roundTripNode) bool { return n.Status == "stopped" }, 3*time.Minute); err != nil {
		return fmt.Errorf("await crash-boundary Aurora stopped: %w", err)
	}
	postCrashInfo, err := inspectNodeContainer(ctx, dockerCLI, ready.InstanceID)
	if err != nil {
		return fmt.Errorf("inspect Aurora node after crash boundary: %w", err)
	}
	if err := assertIdentityPreserved(createdInfo, postCrashInfo); err != nil {
		return fmt.Errorf("Aurora crash-boundary identity/volumes: %w", err)
	}

	// Delete the owned node and prove the enrollment credential was revoked, the
	// runtime is offline, and no owned Docker resource remains.
	if err := api.call(ctx, http.MethodDelete, "/api/cloud-runtime/nodes", "aurora-it-delete-"+suffix, map[string]any{"instance_id": ready.ID}, nil); err != nil {
		return fmt.Errorf("API delete Aurora node: %w", err)
	}
	if err := api.awaitNodeGone(ctx, ready.ID, 5*time.Minute); err != nil {
		return fmt.Errorf("await Aurora node deletion: %w", err)
	}
	var revoked int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM fleet_node_credentials WHERE node_id = $1 AND revoked_at IS NOT NULL`, ready.ID).Scan(&revoked); err != nil {
		return fmt.Errorf("query revoked Aurora credential: %w", err)
	}
	if revoked == 0 {
		return fmt.Errorf("Aurora node %s enrollment credential was not revoked after delete", ready.ID)
	}
	var daemonID string
	if err := pool.QueryRow(ctx, `SELECT COALESCE(daemon_id, '') FROM fleet_nodes WHERE id = $1`, ready.ID).Scan(&daemonID); err != nil {
		return fmt.Errorf("load Aurora node daemon id: %w", err)
	}
	if daemonID != "" {
		var online int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_runtime WHERE daemon_id = $1 AND status = 'online'`, daemonID).Scan(&online); err != nil {
			return fmt.Errorf("query Aurora runtime status: %w", err)
		}
		if online != 0 {
			return fmt.Errorf("Aurora runtime for daemon %s is still online after delete", daemonID)
		}
	}
	if err := awaitOwnedResourcesGone(ctx, dockerCLI, backendNodeID, 3*time.Minute); err != nil {
		return err
	}
	return nil
}
