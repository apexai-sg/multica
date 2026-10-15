package docker

import (
	"context"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/multica-ai/multica/server/internal/fleet/model"
	"github.com/multica-ai/multica/server/internal/util"
)

type reportStats struct {
	Known   bool `json:"known"`
	Pending int  `json:"pending"`
	Failed  int  `json:"failed"`
}
type healthWire struct {
	DaemonID     string          `json:"daemon_id"`
	Ready        bool            `json:"ready"`
	RuntimeCount int             `json:"runtime_count"`
	ActiveRuns   int             `json:"active_runs"`
	Agents       []string        `json:"agents"`
	Reports      json.RawMessage `json:"report_queue_stats"`
}

func decodeReports(raw []byte) (reportStats, error) {
	var s reportStats
	f, e := model.DecodeStrictObject(raw, &s)
	if e != nil || len(f) != 3 || s.Pending < 0 || s.Failed < 0 {
		return s, model.ErrUnknownHealth
	}
	return s, nil
}

// Image defaults are inspected against one fixed whitelist, never administrator or caller env.
func inspectEnvironment(env []string, maxRuns int, aurora *model.AuroraConfig) bool {
	var claudeEnv map[string]string
	if aurora != nil {
		claudeEnv = aurora.ClaudeEnv
	}
	seen := map[string]bool{}
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || seen[key] {
			return false
		}
		seen[key] = true
		switch key {
		case "PATH":
			if value != "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin" {
				return false
			}
		case "HOME":
			if value != model.NodeHome {
				return false
			}
		case "FLEET_NODE_MAX_RUNS":
			if maxRuns <= 0 || value != strconv.Itoa(maxRuns) {
				return false
			}
		case model.AuroraManagedEnv:
			if aurora == nil || value != "1" {
				return false
			}
		case model.AuroraServerURLEnv:
			// Compare against the configured origin exactly. A node built for a
			// different server url would enroll and call back to the wrong API,
			// so structural validity alone must not adopt it.
			if aurora == nil || value != aurora.ServerURL {
				return false
			}
		case model.AuroraEnrollmentFileEnv:
			if aurora == nil || value != model.AuroraEnrollmentFile {
				return false
			}
		case model.AuroraClaudePathEnv:
			// The provider supplies the one agent executable path at container
			// start; the neutral image deliberately bakes no MULTICA_CLAUDE_PATH.
			// Adopting a node must require exactly the fixed provider path, never
			// an arbitrary value from a stale or hand-built container.
			if aurora == nil || value != model.AuroraClaudePath {
				return false
			}
		case model.AuroraExpertsDirEnv:
			// The image declares where it placed the vendored expert tree, and the
			// bootstrap helper - built from that same image - carries the variable
			// with aurora == nil, so this one is validated rather than required. It
			// is the same default the daemon falls back to; a different path would
			// send a run to a tree the operator did not place.
			if value != model.AuroraExpertsDir {
				return false
			}
		case model.AuroraAnthropicBaseURLEnv:
			// The operator-configured endpoint override is adoption-relevant:
			// a container built for a different endpoint keeps talking to the
			// old provider, so only an exact match of a configured value is
			// acceptable. When the config carries none, the variable is drift.
			if aurora == nil || aurora.AnthropicBaseURL == "" || value != aurora.AnthropicBaseURL {
				return false
			}
		case model.AuroraAnthropicModelEnv:
			if aurora == nil || aurora.AnthropicModel == "" || value != aurora.AnthropicModel {
				return false
			}
		default:
			// Every key outside the fixed built-ins must be exactly one of the
			// operator-configured claude_env pairs. An allowlisted key the
			// configuration does not carry is drift, not an acceptable extra.
			want, configured := claudeEnv[key]
			if !configured || value != want {
				return false
			}
		}
	}
	if maxRuns == 0 {
		return true
	}
	if !seen["HOME"] || !seen["FLEET_NODE_MAX_RUNS"] {
		return false
	}
	if aurora != nil && (!seen[model.AuroraManagedEnv] || !seen[model.AuroraServerURLEnv] || !seen[model.AuroraEnrollmentFileEnv] || !seen[model.AuroraClaudePathEnv]) {
		return false
	}
	if aurora != nil {
		// A configured endpoint override must actually be present: adopting a
		// container without it would silently fall back to the provider default
		// the operator overrode.
		if aurora.AnthropicBaseURL != "" && !seen[model.AuroraAnthropicBaseURLEnv] {
			return false
		}
		if aurora.AnthropicModel != "" && !seen[model.AuroraAnthropicModelEnv] {
			return false
		}
		// Every configured claude_env pair must actually be present: adopting a
		// container missing one would silently drop operator configuration.
		for key := range aurora.ClaudeEnv {
			if !seen[key] {
				return false
			}
		}
	}
	return true
}

// validateNodeInspection is the adoption authority. It reconstructs the exact
// HostConfig and mount set for the configured profile, so any drift between the
// builder and a live container is rejected. The Claude profile (cfg.Aurora nil)
// keeps its original two-volume, network-scoped shape.
func validateNodeInspection(r container.InspectResponse, n model.Node, networkName string, cfg model.Config) error {
	if r.ContainerJSONBase == nil || r.Config == nil || r.HostConfig == nil || r.NetworkSettings == nil {
		return model.ErrForbidden
	}
	c, h := r.Config, r.HostConfig
	want := NodeHostConfig(n.Resources, true, cfg.Aurora)
	if c.Image != n.Image || c.User != "10001:10001" || c.Tty || c.OpenStdin || len(c.ExposedPorts) != 0 || !reflect.DeepEqual([]string(c.Entrypoint), []string{"/usr/local/bin/fleet-node"}) || !reflect.DeepEqual([]string(c.Cmd), []string{"run"}) {
		return model.ErrForbidden
	}
	if !inspectEnvironment(c.Env, n.Resources.MaxRuns, cfg.Aurora) {
		return model.ErrForbidden
	}
	if h.ReadonlyRootfs != want.ReadonlyRootfs || h.NanoCPUs != want.NanoCPUs || h.Memory != want.Memory || h.PidsLimit == nil || *h.PidsLimit != *want.PidsLimit || h.Privileged || h.PidMode != "" || len(h.Binds) != 0 || len(h.Devices) != 0 || len(h.DeviceRequests) != 0 || len(h.VolumesFrom) != 0 || len(h.PortBindings) != 0 || h.PublishAllPorts || h.NetworkMode != container.NetworkMode(networkName) || h.RestartPolicy.Name != container.RestartPolicyDisabled || len(h.CapAdd) != 0 || !reflect.DeepEqual(h.CapDrop, want.CapDrop) || !reflect.DeepEqual(h.SecurityOpt, want.SecurityOpt) || !reflect.DeepEqual(h.ExtraHosts, want.ExtraHosts) || !reflect.DeepEqual(h.Tmpfs, want.Tmpfs) {
		return model.ErrForbidden
	}
	if len(r.NetworkSettings.Networks) != 1 || r.NetworkSettings.Networks[networkName] == nil {
		return model.ErrForbidden
	}
	expected := []container.MountPoint{
		{Type: mount.TypeVolume, Name: n.DataVolume, Destination: model.DataMount, RW: true},
		{Type: mount.TypeVolume, Name: n.SecretsVolume, Destination: model.AuroraEnrollmentDir, RW: false},
	}
	if len(r.Mounts) != len(expected) {
		return model.ErrForbidden
	}
	for _, wantMount := range expected {
		matched := false
		for _, m := range r.Mounts {
			if m.Type != wantMount.Type || m.Destination != wantMount.Destination || m.RW != wantMount.RW {
				continue
			}
			matched = m.Name == wantMount.Name
			if matched {
				break
			}
		}
		if !matched {
			return model.ErrForbidden
		}
	}
	return nil
}
func (p *Provider) validSDKSnapshot(n model.Node, i Inspection) error {
	if _, ok := p.engine.(*sdkEngine); ok {
		if i.sdk == nil {
			return model.ErrForbidden
		}
		network := p.networkName()
		if p.cfg.Aurora != nil {
			network = p.workspaceNetwork(n).Name
		}
		return validateNodeInspection(*i.sdk, n, network, p.cfg)
	}
	return nil
}
func parseHealth(raw []byte, n model.Node, epoch string) (model.Observation, error) {
	if id, e := util.ParseUUID(n.DaemonID); e != nil || id.Bytes == [16]byte{} {
		return model.Observation{}, model.ErrUnknownHealth
	}
	var h healthWire
	f, e := model.DecodeStrictObject(raw, &h)
	if e != nil || len(f) != 6 || n.DaemonID == "" || epoch == "" || h.DaemonID != n.DaemonID || h.RuntimeCount < 0 || h.ActiveRuns < 0 {
		return model.Observation{}, model.ErrUnknownHealth
	}
	// Persisted epochs must agree during diagnosis; a newly started node may not yet have one.
	if n.StartEpoch != "" && n.StartEpoch != epoch {
		return model.Observation{}, model.ErrUnknownHealth
	}
	s, e := decodeReports(h.Reports)
	if e != nil {
		return model.Observation{}, e
	}
	return model.Observation{ContainerID: n.ContainerID, Status: "running", DaemonID: h.DaemonID, StartEpoch: epoch, Ready: h.Ready, Agents: h.Agents, RuntimeCount: h.RuntimeCount, ActiveRuns: h.ActiveRuns, ReportStatsKnown: s.Known, PendingReports: s.Pending, FailedReports: s.Failed}, nil
}
func (p *Provider) inspectOwned(ctx context.Context, n model.Node, id string) (Inspection, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	i, e := p.engine.Inspect(ctx, id)
	if e != nil {
		return i, e
	}
	if i.ID == "" || (n.ContainerID != "" && i.ID != n.ContainerID) || !Owns(i.Labels, n.Namespace, p.cfg.FleetID, nodeID(n), "node") {
		return Inspection{}, model.ErrForbidden
	}
	return i, nil
}
func (p *Provider) Inspect(ctx context.Context, n model.Node) (model.Observation, error) {
	if e := p.validNode(n); e != nil {
		return model.Observation{}, e
	}
	if n.ContainerID == "" {
		return model.Observation{Status: "missing", ObservedAt: time.Now()}, nil
	}
	i, e := p.inspectOwned(ctx, n, n.ContainerID)
	if errdefs.IsNotFound(e) {
		return model.Observation{Status: "missing", ObservedAt: time.Now()}, nil
	}
	if e != nil {
		return model.Observation{}, safeError(e)
	}
	if e = p.validSDKSnapshot(n, i); e != nil {
		return model.Observation{}, e
	}
	return p.observation(ctx, n, i), nil
}
func (p *Provider) observation(ctx context.Context, n model.Node, i Inspection) model.Observation {
	o := model.Observation{ContainerID: i.ID, Status: "unknown", ObservedAt: time.Now()}
	if i.State == "exited" || i.State == "created" {
		o.Status = "stopped"
	}
	if i.State == "running" {
		o.Status = "running"
		n.ContainerID = i.ID
		if p.validSDKSnapshot(n, i) != nil {
			return o
		}
		if i.StartedAt == "" || (n.StartEpoch != "" && n.StartEpoch != i.StartedAt) {
			return o
		}
		healthCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		raw, e := p.engine.FixedHealth(healthCtx, i.ID)
		if e != nil {
			return o
		}
		after, e := p.inspectOwned(healthCtx, n, i.ID)
		if e != nil || after.ID != i.ID || after.State != "running" || after.StartedAt != i.StartedAt {
			return o
		}
		if h, e := parseHealth(raw, n, i.StartedAt); e == nil {
			o = h
			o.ObservedAt = time.Now()
		}
	}
	return o
}
