// Package docker manages only explicitly owned fleet resources through the Engine seam.
package docker

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net"
	"regexp"
	"strconv"
	"time"

	"github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/multica-ai/multica/server/internal/fleet/model"
	"github.com/multica-ai/multica/server/internal/util"
)

type Provider struct {
	engine Engine
	cfg    model.Config
}

func New(e Engine, cfg model.Config) *Provider {
	cfg.Specs = nil // Node snapshots, not mutable administrator specs, drive existing resources.
	if sdk, ok := e.(*sdkEngine); ok {
		copy := *sdk
		copy.cfg = cfg
		e = &copy
	}
	return &Provider{engine: e, cfg: cfg}
}

var approvedImage = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/-]*@sha256:[a-f0-9]{64}$`)
var volumeName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,254}$`)

func nodeID(n model.Node) string { return util.UUIDToString(n.ID) }
func labels(namespace, fleetID, node, role string) map[string]string {
	return map[string]string{"multica.fleet.namespace": namespace, "multica.fleet.fleet_id": fleetID, "multica.fleet.node": node, "multica.fleet.role": role}
}
func (p *Provider) validNode(n model.Node) error {
	if p.engine == nil {
		return model.ErrUnavailable
	}
	if p.cfg.Namespace == "" || p.cfg.FleetID == "" || n.Namespace != p.cfg.Namespace || !n.ID.Valid || n.ID.Bytes == [16]byte{} || !n.OwnerID.Valid || n.OwnerID.Bytes == [16]byte{} {
		return model.ErrForbidden
	}
	return nil
}
func safeError(e error) error {
	for _, known := range []error{model.ErrForbidden, model.ErrBusy, model.ErrConflict, model.ErrUnknownHealth, model.ErrInvalidRequest} {
		if errors.Is(e, known) {
			return known
		}
	}
	return model.ErrUnavailable
}
func (p *Provider) networkName() string {
	sum := sha256.Sum256([]byte(p.cfg.Namespace + "\x00" + p.cfg.FleetID))
	return "multica-fleet-" + hex.EncodeToString(sum[:12])
}
func (p *Provider) containerName(n model.Node) string { return p.networkName() + "-" + nodeID(n) }

// workspaceNetwork is the per-node, outbound-capable workspace network an Aurora node
// joins. It is derived deterministically from the namespace, Fleet and node, so
// workspace-controlled input never reaches a Docker name or label.
func (p *Provider) workspaceNetwork(n model.Node) Resource {
	sum := sha256.Sum256([]byte(p.cfg.Namespace + "\x00" + p.cfg.FleetID + "\x00" + nodeID(n) + "\x00workspace"))
	return Resource{
		Name:     "multica-fleet-ws-" + hex.EncodeToString(sum[:9]),
		Role:     "workspace-network",
		Internal: false,
		Subnet:   p.workspaceSubnet(n),
		Labels:   labels(n.Namespace, p.cfg.FleetID, nodeID(n), "workspace-network"),
	}
}

// workspaceSubnet returns the /24 a node network is created in, or "" to keep
// Docker's automatic pool. A pinned pool makes each node's address space
// deterministic, so a node network can never land on the host's own LAN.
func (p *Provider) workspaceSubnet(n model.Node) string {
	_, pool, err := net.ParseCIDR(p.cfg.NetworkPool)
	if err != nil {
		return ""
	}
	ones, bits := pool.Mask.Size()
	if bits != 32 || ones > 24 {
		return ""
	}
	count := 1 << uint(24-ones)
	sum := sha256.Sum256([]byte(p.cfg.Namespace + "\x00" + p.cfg.FleetID + "\x00" + nodeID(n) + "\x00subnet"))
	index := int(binary.BigEndian.Uint32(sum[:4]) % uint32(count))
	subnet := make(net.IP, net.IPv4len)
	copy(subnet, pool.IP.To4())
	subnet[2] = byte(index >> 8)
	subnet[3] = byte(index)
	return (&net.IPNet{IP: subnet, Mask: net.CIDRMask(24, 32)}).String()
}

func (p *Provider) volume(n model.Node, role string) Resource {
	name := n.DataVolume
	if role == "secrets" {
		name = n.SecretsVolume
	}
	return Resource{ID: name, Name: name, Role: role, Labels: labels(n.Namespace, p.cfg.FleetID, nodeID(n), role)}
}
func (p *Provider) CheckAvailability(ctx context.Context) error {
	if p.engine == nil || p.cfg.Namespace == "" || p.cfg.FleetID == "" {
		return model.ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, e := p.engine.Find(ctx, map[string]string{"multica.fleet.namespace": p.cfg.Namespace, "multica.fleet.fleet_id": p.cfg.FleetID})
	if e != nil {
		return model.ErrUnavailable
	}
	return nil
}
func (p *Provider) Ensure(ctx context.Context, n model.Node, b model.Bootstrap) (model.Observation, error) {
	if n.Revoked || n.Maintenance || n.Desired == "terminating" || n.Desired == "terminated" {
		return model.Observation{}, model.ErrConflict
	}
	if e := p.validNode(n); e != nil {
		return model.Observation{}, e
	}
	if !approvedImage.MatchString(p.cfg.Image) || n.Image == "" || !volumeName.MatchString(n.DataVolume) || !volumeName.MatchString(n.SecretsVolume) || n.DataVolume == n.SecretsVolume || n.Resources.CPUs <= 0 || n.Resources.MemoryBytes <= 0 || n.Resources.Pids <= 0 || n.Resources.MaxRuns <= 0 {
		return model.Observation{}, model.ErrInvalidRequest
	}
	id := n.ContainerID
	if id == "" {
		id = p.containerName(n)
	}
	i, e := p.inspectOwned(ctx, n, id)
	if e == nil {
		return p.ensureStarted(ctx, n, i)
	}
	if !errdefs.IsNotFound(e) {
		return model.Observation{}, safeError(e)
	}
	// A persisted container that disappeared is never replaced with a new identity.
	if n.ContainerID != "" {
		return model.Observation{Status: "missing", ObservedAt: time.Now()}, model.ErrConflict
	}
	if e := p.validBootstrap(n, b); e != nil {
		return model.Observation{}, e
	}
	network := Resource{Name: p.networkName(), Role: "network", Labels: labels(n.Namespace, p.cfg.FleetID, "namespace", "network")}
	if e = bounded(ctx, func(c context.Context) error { return p.engine.EnsureNetwork(c, network) }); e != nil {
		return model.Observation{}, safeError(e)
	}
	workspace := network
	if p.cfg.Aurora != nil {
		// Keep per-node ownership while allowing direct outbound traffic.
		workspace = p.workspaceNetwork(n)
		if e = bounded(ctx, func(c context.Context) error { return p.engine.EnsureNetwork(c, workspace) }); e != nil {
			return model.Observation{}, safeError(e)
		}
	}
	vols := []Resource{p.volume(n, "data"), p.volume(n, "secrets")}
	for _, r := range vols {
		if e = bounded(ctx, func(c context.Context) error { return p.engine.EnsureVolume(c, r) }); e != nil {
			return model.Observation{}, safeError(e)
		}
	}
	tar, e := installerTar(n, p.cfg, b)
	if e != nil {
		return model.Observation{}, e
	}
	if e = bounded(ctx, func(c context.Context) error { return p.engine.InstallBootstrap(c, vols, tar) }); e != nil {
		return model.Observation{}, safeError(e)
	}
	h := NodeHostConfig(n.Resources, true, p.cfg.Aurora)
	h.NetworkMode = container.NetworkMode(workspace.Name)
	h.Mounts = []mount.Mount{{Type: mount.TypeVolume, Source: n.DataVolume, Target: model.DataMount}, {Type: mount.TypeVolume, Source: n.SecretsVolume, Target: model.AuroraEnrollmentDir, ReadOnly: true}}
	c := &container.Config{Image: n.Image, User: "10001:10001", Labels: labels(n.Namespace, p.cfg.FleetID, nodeID(n), "node"), Env: p.nodeEnv(n), Entrypoint: []string{"/usr/local/bin/fleet-node"}, Cmd: []string{"run"}, Healthcheck: &container.HealthConfig{Test: []string{"CMD", "/usr/local/bin/fleet-node", "health"}, Interval: 5 * time.Second, Timeout: 5 * time.Second, Retries: 3}}
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	created, createErr := p.engine.Create(callCtx, c, &h, workspace.Name, p.containerName(n))
	cancel()
	// Even an uncertain create is followed by a new independent inspect before adoption/retry.
	lookup := created
	if lookup == "" {
		lookup = p.containerName(n)
	}
	i, e = p.inspectOwned(ctx, n, lookup)
	if e != nil {
		if createErr != nil {
			return model.Observation{}, safeError(createErr)
		}
		return model.Observation{}, safeError(e)
	}
	return p.ensureStarted(ctx, n, i)
}

// validBootstrap enforces the private payload for the configured profile. The
// two profiles never share an installer: a Claude node must carry a node token
// and API key, while an Aurora node must carry exactly one enrollment secret
// and no model credentials.
func (p *Provider) validBootstrap(n model.Node, b model.Bootstrap) error {
	if n.DaemonID == "" || b.DaemonID != n.DaemonID {
		return model.ErrInvalidRequest
	}
	if p.cfg.Aurora != nil {
		if !model.ValidEnrollmentToken(b.EnrollmentToken) || b.ServerURL != p.cfg.Aurora.ServerURL ||
			b.NodeToken != "" || b.APIKey != "" || b.BaseURL != "" || b.Model != "" {
			return model.ErrInvalidRequest
		}
		return nil
	}
	if b.EnrollmentToken != "" || b.NodeToken == "" || b.APIKey == "" || b.ServerURL != p.cfg.APIURL {
		return model.ErrInvalidRequest
	}
	return nil
}

// nodeEnv is the exact container environment for a node. The managed Aurora
// profile adds the fixed enrollment and agent-path variables plus
// the operator's claude_env pairs, which carry the provider credentials the
// daemon reads from this environment. The image bakes neither the agent path nor
// any Node version banner.
func (p *Provider) nodeEnv(n model.Node) []string {
	env := []string{"HOME=" + model.NodeHome, "FLEET_NODE_MAX_RUNS=" + strconv.Itoa(n.Resources.MaxRuns)}
	if p.cfg.Aurora != nil {
		env = append(env,
			model.AuroraManagedEnv+"=1",
			model.AuroraServerURLEnv+"="+p.cfg.Aurora.ServerURL,
			model.AuroraEnrollmentFileEnv+"="+model.AuroraEnrollmentFile,
			model.AuroraClaudePathEnv+"="+model.AuroraClaudePath,
		)
		if p.cfg.Aurora.AnthropicBaseURL != "" {
			env = append(env, model.AuroraAnthropicBaseURLEnv+"="+p.cfg.Aurora.AnthropicBaseURL)
		}
		if p.cfg.Aurora.AnthropicModel != "" {
			env = append(env, model.AuroraAnthropicModelEnv+"="+p.cfg.Aurora.AnthropicModel)
		}
		// Operator-configured extra node variables (including the provider
		// credentials), in deterministic key order. An empty map appends nothing,
		// so the node env stays byte-identical when claude_env is absent.
		env = append(env, p.cfg.Aurora.ClaudeEnvPairs()...)
	}
	return env
}

// removeWorkspaceNetwork removes the per-node workspace network. The
// Engine validates the network's ownership labels, so a foreign network is never
// removed. A missing network is already clean.
func (p *Provider) removeWorkspaceNetwork(ctx context.Context, n model.Node) error {
	return bounded(ctx, func(c context.Context) error { return p.engine.RemoveNetwork(c, p.workspaceNetwork(n)) })
}

func bounded(ctx context.Context, f func(context.Context) error) error {
	c, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return f(c)
}
func (p *Provider) ensureStarted(ctx context.Context, n model.Node, i Inspection) (model.Observation, error) {
	if e := p.validSDKSnapshot(n, i); e != nil {
		return model.Observation{}, e
	}
	if i.State != "running" {
		if e := bounded(ctx, func(c context.Context) error { return p.engine.Start(c, i.ID) }); e != nil {
			return model.Observation{}, safeError(e)
		}
	}
	n.ContainerID = i.ID
	o := p.observation(ctx, n, i)
	if i.State != "running" {
		o.Status = "starting"
	}
	return o, nil
}
func (p *Provider) Apply(ctx context.Context, n model.Node, action model.Action) (model.Observation, error) {
	if e := p.validNode(n); e != nil {
		return model.Observation{}, e
	}
	if n.Revoked || n.Desired == "terminating" || n.Desired == "terminated" {
		return model.Observation{}, model.ErrConflict
	}
	if action != model.Start && action != model.Stop && action != model.Reboot {
		return model.Observation{}, model.ErrInvalidRequest
	}
	if (action == model.Start && n.Maintenance) || (action != model.Start && !n.Maintenance) {
		return model.Observation{}, model.ErrConflict
	}
	if n.ContainerID == "" {
		return model.Observation{Status: "missing", ObservedAt: time.Now()}, model.ErrConflict
	}
	i, e := p.inspectOwned(ctx, n, n.ContainerID)
	if e != nil {
		if errdefs.IsNotFound(e) && action == model.Stop {
			return model.Observation{Status: "missing", ObservedAt: time.Now()}, nil
		}
		return model.Observation{}, safeError(e)
	}
	if e = p.validSDKSnapshot(n, i); e != nil {
		return model.Observation{}, e
	}
	if action == model.Stop || action == model.Reboot {
		if e = bounded(ctx, func(c context.Context) error { return p.engine.Stop(c, n.ContainerID) }); e != nil {
			return model.Observation{}, safeError(e)
		}
	}
	if action == model.Start || action == model.Reboot {
		if e = bounded(ctx, func(c context.Context) error { return p.engine.Start(c, n.ContainerID) }); e != nil {
			return model.Observation{}, safeError(e)
		}
		n.StartEpoch = ""
	}
	return p.Inspect(ctx, n)
}

// hostGatewayExtraHosts is the one host-gateway mapping rule shared by every
// Fleet container that must dial the host: nodes
// need to resolve host.docker.internal, which on Linux Docker Engine only
// resolves when the container carries the mapping. Docker Desktop resolves it
// regardless, so the mapping is additive and never widens reachability.
func hostGatewayExtraHosts(linuxHostGateway bool) []string {
	if !linuxHostGateway {
		return nil
	}
	return []string{"host.docker.internal:host-gateway"}
}

// NodeHostConfig is the single HostConfig builder for every Fleet node. The
// Claude profile (aurora == nil) is unchanged. The Aurora profile adds the
// read-only root filesystem and the fixed writable tmpfs surfaces. AppArmor and
// the custom seccomp profile are no longer used: the node runs with Docker's
// default security settings, so neither option is sent.
func NodeHostConfig(spec model.Spec, linuxHostGateway bool, aurora *model.AuroraConfig) container.HostConfig {
	h := container.HostConfig{Resources: container.Resources{NanoCPUs: int64(spec.CPUs) * 1e9, Memory: spec.MemoryBytes, PidsLimit: &spec.Pids}, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges:true"}, RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyDisabled}}
	h.ExtraHosts = hostGatewayExtraHosts(linuxHostGateway)
	if aurora != nil {
		h.ReadonlyRootfs = aurora.ReadonlyRootfs
		h.Tmpfs = map[string]string{
			model.AuroraWorkspaceMount: auroraWorkspaceTmpfs,
			model.AuroraTmpMount:       auroraTmpTmpfs,
			model.AuroraRunMount:       auroraRunTmpfs,
		}
	}
	return h
}
func Owns(labels map[string]string, namespace, fleetID, nodeID, role string) bool {
	return namespace != "" && fleetID != "" && nodeID != "" && role != "" && labels["multica.fleet.namespace"] == namespace && labels["multica.fleet.fleet_id"] == fleetID && labels["multica.fleet.node"] == nodeID && labels["multica.fleet.role"] == role
}
