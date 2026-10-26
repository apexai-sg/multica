package docker

import (
	"context"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/multica-ai/multica/server/internal/fleet/model"
)

const goodHealth = `{"daemon_id":"04000000-0000-0000-0000-000000000000","ready":true,"runtime_count":1,"active_runs":0,"agents":["claude"],"report_queue_stats":{"known":true,"pending":0,"failed":0}}`

func TestInspectAdoptionPreservesExactSnapshotAndIsolation(t *testing.T) {
	for _, kind := range []string{"valid", "image", "resources", "ports", "network", "extra-network", "secret-writable", "wrong-data", "user", "host-pid", "privileged", "caps", "restart", "env"} {
		t.Run(kind, func(t *testing.T) {
			n := fixtureNode()
			h := NodeHostConfig(n.Resources, true, nil)
			h.NetworkMode = "node-net"
			r := container.InspectResponse{ContainerJSONBase: &container.ContainerJSONBase{ID: "cid", HostConfig: &h}, Config: &container.Config{Image: n.Image, User: "10001:10001", Env: []string{"HOME=/data/home", "FLEET_NODE_MAX_RUNS=1"}, Entrypoint: []string{"/usr/local/bin/fleet-node"}, Cmd: []string{"run"}}, NetworkSettings: &container.NetworkSettings{Networks: map[string]*network.EndpointSettings{"node-net": {}}}, Mounts: []container.MountPoint{{Type: mount.TypeVolume, Name: n.DataVolume, Destination: model.DataMount, RW: true}, {Type: mount.TypeVolume, Name: n.SecretsVolume, Destination: "/secrets", RW: false}}}
			switch kind {
			case "image":
				r.Config.Image = "changed"
			case "resources":
				h.Memory = 1
			case "ports":
				h.PublishAllPorts = true
			case "network":
				h.NetworkMode = "host"
			case "extra-network":
				r.NetworkSettings.Networks["postgres"] = &network.EndpointSettings{}
			case "secret-writable":
				r.Mounts[1].RW = true
			case "wrong-data":
				r.Mounts[0].Name = "foreign"
			case "user":
				r.Config.User = "root"
			case "host-pid":
				h.PidMode = "host"
			case "privileged":
				h.Privileged = true
			case "caps":
				h.CapDrop = nil
			case "restart":
				h.RestartPolicy.Name = "always"
			case "env":
				r.Config.Env = append(r.Config.Env, "PGPASSWORD=credential")
			}
			err := validateNodeInspection(r, n, "node-net", fixtureConfig())
			if kind == "valid" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatalf("unsafe %s snapshot adopted", kind)
			}
		})
	}
}

func TestInspectInvalidSQLDaemonUUIDIsUnknown(t *testing.T) {
	n := fixtureNode()
	n.DaemonID = "not-a-uuid"
	raw := strings.Replace(goodHealth, fixtureNode().DaemonID, n.DaemonID, 1)
	if _, e := parseHealth([]byte(raw), n, "epoch"); e == nil {
		t.Fatal("invalid SQL daemon UUID accepted")
	}
}
func TestInspectUnknownStateNeverLooksStopped(t *testing.T) {
	for _, state := range []string{"paused", "restarting", "dead", "corrupt"} {
		t.Run(state, func(t *testing.T) {
			p := New(fakeCalls{inspect: func(context.Context, string) (Inspection, error) {
				return Inspection{ID: "cid", State: state, Labels: fixtureLabels("node")}, nil
			}}, fixtureConfig())
			o, e := p.Inspect(context.Background(), fixtureNode())
			if e != nil || o.Status == "stopped" || o.ReportStatsKnown || o.Ready {
				t.Fatalf("unknown state %s mapped as stopped: %+v err=%v", state, o, e)
			}
		})
	}
}
func TestInspectInheritedEnvironmentIsFixedAndCredentialFree(t *testing.T) {
	path := "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	home := "HOME=/data/home"
	max := "FLEET_NODE_MAX_RUNS=1"
	for _, tc := range []struct {
		name    string
		env     []string
		maxRuns int
		want    bool
	}{
		{"helper-empty", nil, 0, true}, {"helper-inherited", []string{path, home}, 0, true}, {"node-inherited", []string{max, path, home}, 1, true}, {"node-fixed", []string{home, max}, 1, true}, {"credentials", []string{home, "ANTHROPIC_API_KEY=private"}, 0, false}, {"duplicate-path", []string{path, path}, 0, false}, {"duplicate-home", []string{home, home}, 0, false}, {"bare", []string{"PATH"}, 0, false}, {"wrong-path", []string{"PATH=/host/bin"}, 0, false}, {"wrong-home", []string{"HOME=/root"}, 0, false}, {"version", []string{"NODE_VERSION=22"}, 0, false}, {"yarn-version", []string{"YARN_VERSION=1.22.22"}, 0, false}, {"claude-path-without-aurora", []string{"MULTICA_CLAUDE_PATH=" + model.AuroraClaudePath}, 0, false}, {"proxy", []string{"HTTP_PROXY=http://host.invalid"}, 0, false}, {"helper-maxruns", []string{max}, 0, false}, {"missing-max", []string{home, path}, 1, false}, {"override-max", []string{home, "FLEET_NODE_MAX_RUNS=2"}, 1, false}, {"duplicate-max", []string{home, max, max}, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := inspectEnvironment(tc.env, tc.maxRuns, nil); got != tc.want {
				t.Fatalf("fixed environment got=%v want=%v", got, tc.want)
			}
		})
	}
}
func TestInspectHelperCannotGainPrivilegesOrHostResources(t *testing.T) {
	for _, kind := range []string{"valid", "cap-add", "restart", "extra-host", "device", "volume-from", "pid-sharing", "missing-network", "extra-network", "stdin", "network-enabled"} {
		t.Run(kind, func(t *testing.T) {
			h := diagnosticHost()
			h.Mounts = []mount.Mount{{Type: mount.TypeVolume, Source: "data-vol", Target: "/data", ReadOnly: true}}
			want := h
			c := &container.Config{Image: fixtureConfig().Image, User: helperUser, NetworkDisabled: true, Entrypoint: []string{"/usr/local/bin/fleet-node"}, Cmd: []string{"report-stats"}}
			actual := container.InspectResponse{ContainerJSONBase: &container.ContainerJSONBase{ID: "helper", HostConfig: &h}, Config: c, NetworkSettings: &container.NetworkSettings{}, Mounts: []container.MountPoint{{Type: mount.TypeVolume, Name: "data-vol", Destination: "/data", RW: false}}}
			switch kind {
			case "missing-network":
				actual.NetworkSettings = nil
			case "extra-network":
				actual.NetworkSettings.Networks = map[string]*network.EndpointSettings{"foreign": {}}
			case "stdin":
				actual.Config.OpenStdin = true
			case "network-enabled":
				actual.Config.NetworkDisabled = false
			case "cap-add":
				h.CapAdd = []string{"SYS_ADMIN"}
			case "restart":
				h.RestartPolicy.Name = "always"
			case "extra-host":
				h.ExtraHosts = []string{"host:host-gateway"}
			case "device":
				h.Devices = []container.DeviceMapping{{PathOnHost: "/dev/sda"}}
			case "volume-from":
				h.VolumesFrom = []string{"foreign"}
			case "pid-sharing":
				h.PidMode = "container:foreign"
			}
			e := validateHelper(actual, c, &want)
			if kind == "valid" {
				if e != nil {
					t.Fatal(e)
				}
			} else if e == nil {
				t.Fatalf("helper accepted %s", kind)
			}
		})
	}
}
func TestInspectStrictHealthIdentityAndCounts(t *testing.T) {
	n := fixtureNode()
	n.StartEpoch = "epoch"
	o, err := parseHealth([]byte(goodHealth), n, "epoch")
	if err != nil || !o.Ready || !o.ReportStatsKnown || o.RuntimeCount != 1 {
		t.Fatalf("valid health error=%v observation=%+v", err, o)
	}
	cases := []string{strings.Replace(goodHealth, fixtureNode().DaemonID+"\"", "foreign\"", 1), strings.Replace(goodHealth, "\"ready\":true", "\"ready\":true,\"start_epoch\":\"old\"", 1), strings.Replace(goodHealth, "\"pending\":0", "\"pending\":-1", 1), strings.Replace(goodHealth, "\"pending\":0", "\"pending\":0.5", 1), strings.Replace(goodHealth, "\"known\":true,", "", 1), strings.Replace(goodHealth, "\"ready\":true", "\"ready\":true,\"ready\":true", 1), goodHealth + "{}", strings.Replace(goodHealth, "\"agents\":[\"claude\"]", "\"agents\":null", 1)}
	for _, raw := range cases {
		if o, e := parseHealth([]byte(raw), n, "epoch"); e == nil || o.ReportStatsKnown {
			t.Fatalf("bad health accepted: %s", raw)
		}
	}
	if _, e := parseHealth([]byte(goodHealth), n, "other"); e == nil {
		t.Fatal("inspect epoch drift accepted")
	}
}

// decideAdoption decides whether an already-running container is this Fleet's
// own, built from the previous image, and may be replaced in place. Only the
// image may differ; every other field validateNodeInspection compares must
// match, or a drift the operator did not approve would be silently destroyed.
func TestDecideAdoptionReplacesOnlyAnImageChange(t *testing.T) {
	node := func() model.Node {
		n := fixtureNode()
		n.ContainerID = ""
		n.Maintenance = false
		n.Status = "creating"
		return n
	}
	snapshot := func(n model.Node, mutate func(*container.InspectResponse, *container.HostConfig)) Inspection {
		h := NodeHostConfig(n.Resources, true, nil)
		h.NetworkMode = "node-net"
		r := &container.InspectResponse{
			ContainerJSONBase: &container.ContainerJSONBase{ID: "cid", HostConfig: &h},
			Config:            &container.Config{Image: n.Image, User: "10001:10001", Env: []string{"HOME=/data/home", "FLEET_NODE_MAX_RUNS=1"}, Entrypoint: []string{"/usr/local/bin/fleet-node"}, Cmd: []string{"run"}},
			NetworkSettings:   &container.NetworkSettings{Networks: map[string]*network.EndpointSettings{"node-net": {}}},
			Mounts:            []container.MountPoint{{Type: mount.TypeVolume, Name: n.DataVolume, Destination: model.DataMount, RW: true}, {Type: mount.TypeVolume, Name: n.SecretsVolume, Destination: "/secrets", RW: false}},
		}
		if mutate != nil {
			mutate(r, &h)
		}
		return Inspection{ID: "cid", State: "running", sdk: r}
	}

	cases := []struct {
		name   string
		mutate func(*container.InspectResponse, *container.HostConfig)
		node   func(model.Node) model.Node
		want   adoptionDecision
	}{
		{name: "new image only", mutate: func(r *container.InspectResponse, _ *container.HostConfig) { r.Config.Image = "new-image" }, want: adoptionReplaceImage},
		{name: "same image", want: adoptionRefuse},
		{name: "image and resources", mutate: func(r *container.InspectResponse, h *container.HostConfig) {
			r.Config.Image = "new-image"
			h.Memory = 1
		}, want: adoptionRefuse},
		{name: "image and environment", mutate: func(r *container.InspectResponse, _ *container.HostConfig) {
			r.Config.Image = "new-image"
			r.Config.Env = append(r.Config.Env, "EXTRA=1")
		}, want: adoptionRefuse},
		{name: "image and network", mutate: func(r *container.InspectResponse, h *container.HostConfig) {
			r.Config.Image = "new-image"
			h.NetworkMode = "host"
		}, want: adoptionRefuse},
		{name: "image and volume", mutate: func(r *container.InspectResponse, _ *container.HostConfig) {
			r.Config.Image = "new-image"
			r.Mounts[0].Name = "foreign"
		}, want: adoptionRefuse},
		{name: "empty image", mutate: func(r *container.InspectResponse, _ *container.HostConfig) { r.Config.Image = "" }, want: adoptionRefuse},
		{name: "already confirmed container", node: func(n model.Node) model.Node {
			n.ContainerID = "cid"
			return n
		}, want: adoptionRefuse},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := node()
			if tc.node != nil {
				n = tc.node(n)
			}
			if got := decideAdoption(n, snapshot(n, tc.mutate), "node-net", fixtureConfig()); got != tc.want {
				t.Fatalf("decideAdoption = %v, want %v", got, tc.want)
			}
		})
	}
}
