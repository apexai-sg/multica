package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/multica-ai/multica/server/internal/fleet/model"
)

type deletionContext struct {
	node model.Node
	ref  model.OperationRef
}
type Resource struct {
	deletion       *deletionContext
	ID, Name, Role string
	Labels         map[string]string
	// Internal is inspected exactly; node networks must support direct outbound access.
	Internal bool
	// Subnet, when set, is the IPv4 CIDR the network is created in. Empty keeps
	// Docker's automatic pool.
	Subnet string
}
type Inspection struct {
	ID, State, StartedAt string
	Labels               map[string]string
	HealthJSON           []byte
	sdk                  *container.InspectResponse
}
type Engine interface {
	Find(context.Context, map[string]string) ([]Resource, error)
	Inspect(context.Context, string) (Inspection, error)
	EnsureNetwork(context.Context, Resource) error
	RemoveNetwork(context.Context, Resource) error
	EnsureVolume(context.Context, Resource) error
	Create(context.Context, *container.Config, *container.HostConfig, string, string) (string, error)
	InstallBootstrap(context.Context, []Resource, []byte) error
	Start(context.Context, string) error
	Stop(context.Context, string) error
	Remove(context.Context, string) error
	RemoveVolume(context.Context, Resource) error
	FixedHealth(context.Context, string) ([]byte, error)
	FixedOfflineReports(context.Context, model.Node, model.OperationRef) ([]byte, error)
}
type sdkEngine struct {
	client  *client.Client
	cfg     model.Config
	helpers *helperLifecycles
}

// Lifecycle arbitration stores no approval, ref, reports, or deletion permit.
// Configured Providers share only this explicit Engine's synchronized registry.
type helperLifecycle struct {
	identity            string
	firstSeen, lastSeen time.Time
	node                string
}
type helperLifecycles struct {
	mu      sync.Mutex
	now     func() time.Time
	seen    map[string]helperLifecycle
	cursors map[string]string
}

const helperQuiescence = 65 * time.Second
const helperTrackingLimit = 128

func (l *helperLifecycles) quiescent(r container.InspectResponse, node string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	stamp, _ := json.Marshal(struct {
		Created, StartedAt, FinishedAt, Status, Name string
		Running                                      bool
		RestartCount                                 int
	}{r.Created, r.State.StartedAt, r.State.FinishedAt, r.State.Status, r.Name, r.State.Running, r.RestartCount})
	identity := string(stamp)
	previous, ok := l.seen[r.ID]
	if !ok || previous.identity != identity || previous.node != node {
		// Still-present lifecycles must not lose their waiting interval merely
		// because a large inventory takes multiple bounded batches to revisit.
		// Disappearance pruning and verified removal free capacity instead.
		if !ok && len(l.seen) >= helperTrackingLimit {
			return false
		}
		l.seen[r.ID] = helperLifecycle{identity: identity, firstSeen: now, lastSeen: now, node: node}
		return false
	}
	previous.lastSeen = now
	l.seen[r.ID] = previous
	return now.Sub(previous.firstSeen) >= helperQuiescence
}
func (l *helperLifecycles) prune(node string, present map[string]bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(present) == 0 {
		delete(l.cursors, node)
	}
	for id, v := range l.seen {
		if v.node == node && !present[id] {
			delete(l.seen, id)
		}
	}
}

// Batch cursors affect scheduling only, never elapsed waiting or deletion authority.
func (l *helperLifecycles) batch(node string, candidates []Resource) []Resource {
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cursors == nil {
		l.cursors = map[string]string{}
	}
	if len(candidates) == 0 {
		delete(l.cursors, node)
		return nil
	}
	if _, ok := l.cursors[node]; !ok && len(l.cursors) >= helperTrackingLimit {
		keys := make([]string, 0, len(l.cursors))
		for k := range l.cursors {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		delete(l.cursors, keys[0]) // Losing a scheduling cursor cannot grant quiescence.
	}
	start := sort.Search(len(candidates), func(i int) bool { return candidates[i].ID > l.cursors[node] }) % len(candidates)
	batch := make([]Resource, 0, min(5, len(candidates)))
	for i := 0; i < min(5, len(candidates)); i++ {
		batch = append(batch, candidates[(start+i)%len(candidates)])
	}
	l.cursors[node] = batch[len(batch)-1].ID
	return batch
}
func (l *helperLifecycles) forget(id string) { l.mu.Lock(); defer l.mu.Unlock(); delete(l.seen, id) }

// NewEngine adapts only the supplied client; it never discovers hosts, credentials, or options.
func NewEngine(c *client.Client) Engine {
	return &sdkEngine{client: c, helpers: &helperLifecycles{now: time.Now, seen: map[string]helperLifecycle{}}}
}

func (e *sdkEngine) Find(ctx context.Context, labels map[string]string) ([]Resource, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if e.client == nil {
		return nil, model.ErrUnavailable
	}
	f := filters.NewArgs()
	for k, v := range labels {
		f.Add("label", k+"="+v)
	}
	list, err := e.client.ContainerList(ctx, container.ListOptions{All: true, Filters: f})
	if err != nil {
		return nil, err
	}
	out := make([]Resource, 0, len(list))
	for _, r := range list {
		out = append(out, Resource{ID: r.ID, Role: r.Labels["multica.fleet.role"], Labels: r.Labels})
	}
	return out, nil
}
func (e *sdkEngine) Inspect(ctx context.Context, id string) (Inspection, error) {
	c, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if e.client == nil {
		return Inspection{}, model.ErrUnavailable
	}
	r, err := e.client.ContainerInspect(c, id)
	if err != nil {
		return Inspection{}, err
	}
	if r.ContainerJSONBase == nil || r.State == nil || r.Config == nil {
		return Inspection{}, model.ErrUnknownHealth
	}
	out := Inspection{ID: r.ID, State: r.State.Status, StartedAt: r.State.StartedAt, Labels: r.Config.Labels, sdk: &r}
	return out, nil
}
func (e *sdkEngine) EnsureNetwork(ctx context.Context, r Resource) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if e.client == nil {
		return model.ErrUnavailable
	}
	if r.Name == "" {
		return model.ErrForbidden
	}
	switch r.Role {
	case "network":
		if !Owns(r.Labels, e.cfg.Namespace, e.cfg.FleetID, "namespace", "network") {
			return model.ErrForbidden
		}
	case "workspace-network":
		// A workspace network is an outbound-capable bridge owned by
		// exactly one node.
		if r.Internal || !Owns(r.Labels, e.cfg.Namespace, e.cfg.FleetID, r.Labels["multica.fleet.node"], "workspace-network") {
			return model.ErrForbidden
		}
	default:
		return model.ErrForbidden
	}
	check := func() error {
		n, err := e.client.NetworkInspect(ctx, r.Name, network.InspectOptions{})
		if err != nil {
			return err
		}
		if n.Name != r.Name || n.ID == "" || n.Driver != "bridge" || n.Internal != r.Internal || !sameLabels(n.Labels, r.Labels) {
			return model.ErrForbidden
		}
		return nil
	}
	err := check()
	if err == nil {
		return nil
	}
	if !errdefs.IsNotFound(err) {
		return err
	}
	opts := network.CreateOptions{Driver: "bridge", Labels: r.Labels, Internal: r.Internal}
	if r.Subnet != "" {
		opts.IPAM = &network.IPAM{Driver: "default", Config: []network.IPAMConfig{{Subnet: r.Subnet}}}
	}
	_, createErr := e.client.NetworkCreate(ctx, r.Name, opts)
	if err = check(); err == nil {
		return nil
	}
	if createErr != nil {
		return createErr
	}
	return err
}

// RemoveNetwork removes one owned network. A missing network is already clean.
func (e *sdkEngine) RemoveNetwork(ctx context.Context, r Resource) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if e.client == nil {
		return model.ErrUnavailable
	}
	if r.Name == "" {
		return model.ErrForbidden
	}
	n, err := e.client.NetworkInspect(ctx, r.Name, network.InspectOptions{})
	if errdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if n.Name != r.Name || n.ID == "" || !sameLabels(n.Labels, r.Labels) {
		return model.ErrForbidden
	}
	return e.client.NetworkRemove(ctx, r.Name)
}

func (e *sdkEngine) EnsureVolume(ctx context.Context, r Resource) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if e.client == nil {
		return model.ErrUnavailable
	}
	if !e.ownsVolume(r) {
		return model.ErrForbidden
	}
	check := func() error {
		v, err := e.client.VolumeInspect(ctx, r.ID)
		if err != nil {
			return err
		}
		return validateVolume(v, r)
	}
	err := check()
	if err == nil {
		return nil
	}
	if !errdefs.IsNotFound(err) {
		return err
	}
	_, createErr := e.client.VolumeCreate(ctx, volume.CreateOptions{Name: r.Name, Driver: "local", Labels: r.Labels})
	if err = check(); err == nil {
		return nil
	}
	if createErr != nil {
		return createErr
	}
	return err
}
func (e *sdkEngine) ownsVolume(r Resource) bool {
	return r.ID == r.Name && volumeName.MatchString(r.ID) && (r.Role == "data" || r.Role == "secrets") && Owns(r.Labels, e.cfg.Namespace, e.cfg.FleetID, r.Labels["multica.fleet.node"], r.Role)
}
func validateVolume(v volume.Volume, r Resource) error {
	if v.Name != r.ID || v.Driver != "local" || len(v.Options) != 0 || !sameLabels(v.Labels, r.Labels) {
		return model.ErrForbidden
	}
	return nil
}
func (e *sdkEngine) Create(ctx context.Context, c *container.Config, h *container.HostConfig, networkName, name string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if e.client == nil {
		return "", model.ErrUnavailable
	}
	var nc *network.NetworkingConfig
	if networkName != "" {
		nc = &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{networkName: {}}}
	}
	r, err := e.client.ContainerCreate(ctx, c, h, nc, nil, name)
	return r.ID, err
}
func (e *sdkEngine) Start(ctx context.Context, id string) error {
	c, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if e.client == nil {
		return model.ErrUnavailable
	}
	return e.client.ContainerStart(c, id, container.StartOptions{})
}
func (e *sdkEngine) Stop(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if e.client == nil {
		return model.ErrUnavailable
	}
	err := e.client.ContainerStop(ctx, id, container.StopOptions{})
	if errdefs.IsNotFound(err) {
		return nil
	}
	return err
}
func (e *sdkEngine) Remove(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if e.client == nil {
		return model.ErrUnavailable
	}
	err := e.client.ContainerRemove(ctx, id, container.RemoveOptions{})
	if errdefs.IsNotFound(err) {
		return nil
	}
	return err
}
func (e *sdkEngine) RemoveVolume(ctx context.Context, r Resource) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if e.client == nil {
		return model.ErrUnavailable
	}
	if !e.ownsVolume(r) {
		return model.ErrForbidden
	}
	v, err := e.client.VolumeInspect(ctx, r.ID)
	if errdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = validateVolume(v, r); err != nil {
		return err
	}
	if err = e.noWriter(ctx, r.ID); err != nil {
		return err
	}
	if r.Role == "data" {
		// The final physical data step binds the original call-scoped SQL node/ref, never a cached permit.
		if r.deletion == nil {
			return model.ErrUnknownHealth
		}
		n, ref := r.deletion.node, r.deletion.ref
		if n.DataVolume != r.ID || !validRef(n, ref) || ref.Action != model.Delete || !n.Revoked || n.Desired != "terminating" {
			return model.ErrForbidden
		}
		// A container-less node has no report queue to drain; Delete already ran
		// reclaimContainerlessData to prove there is no writer, and the ownership
		// and noWriter checks below still apply. A node that ever confirmed a
		// container keeps the full offline-report re-proof.
		if n.ContainerID != "" {
			raw, proofErr := e.FixedOfflineReports(ctx, n, ref)
			if proofErr != nil {
				return proofErr
			}
			o, proofErr := parseOffline(raw, n, e.cfg)
			if proofErr != nil || !o.ReportStatsKnown {
				return model.ErrUnknownHealth
			}
			if o.PendingReports != 0 || o.FailedReports != 0 {
				return model.ErrBusy
			}
		}
	}
	err = e.client.VolumeRemove(ctx, r.ID, false)
	if errdefs.IsNotFound(err) {
		return nil
	}
	return err
}

// FixedHealth supports explicit unauthenticated local Unix/plain-TCP clients only.
// The pinned SDK raw hijack ignores handshake cancellation and can bypass supplied transports.
// One literal exec-start upgrade uses the ORIGINAL HTTP transport instead; no raw dial fallback.
func (e *sdkEngine) FixedHealth(ctx context.Context, id string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if e.client == nil {
		return nil, model.ErrUnknownHealth
	}
	host, err := client.ParseHostURL(e.client.DaemonHost())
	if err != nil || (host.Scheme != "tcp" && host.Scheme != "unix") {
		return nil, model.ErrUnknownHealth
	}
	created, err := e.client.ContainerExecCreate(ctx, id, container.ExecOptions{User: "10001:10001", AttachStdout: true, AttachStderr: true, Cmd: []string{"/usr/local/bin/fleet-node", "health"}})
	if err != nil || !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`).MatchString(created.ID) {
		return nil, model.ErrUnknownHealth
	}
	version := e.client.ClientVersion()
	if !regexp.MustCompile(`^[0-9]+[.][0-9]+$`).MatchString(version) {
		return nil, model.ErrUnknownHealth
	}
	endpoint := url.URL{Scheme: "http", Host: host.Host, Path: path.Join(host.Path, "v"+version, "exec", created.ID, "start")}
	if host.Scheme == "unix" {
		endpoint.Host = client.DummyHost
		endpoint.Path = path.Join("/v"+version, "exec", created.ID, "start")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), strings.NewReader(`{"Detach":false,"Tty":false}`))
	if err != nil {
		return nil, model.ErrUnknownHealth
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "tcp")
	hc := e.client.HTTPClient()
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := hc.Do(req)
	if err != nil {
		return nil, model.ErrUnknownHealth
	}
	defer response.Body.Close()
	stop := context.AfterFunc(ctx, func() { response.Body.Close() })
	defer stop()
	if response.StatusCode != http.StatusSwitchingProtocols || !strings.EqualFold(response.Header.Get("Upgrade"), "tcp") || !headerToken(response.Header.Get("Connection"), "upgrade") {
		return nil, model.ErrUnknownHealth
	}
	raw, err := boundedStdout(response.Body)
	if err != nil || ctx.Err() != nil || len(bytes.TrimSpace(raw)) == 0 || !json.Valid(raw) {
		return nil, model.ErrUnknownHealth
	}
	done, err := e.client.ContainerExecInspect(ctx, created.ID)
	if err != nil || done.ExecID != created.ID || done.ContainerID != id || done.Running || done.ExitCode != 0 || ctx.Err() != nil {
		return nil, model.ErrUnknownHealth
	}
	return raw, nil
}
func headerToken(value, want string) bool {
	for _, token := range strings.Split(value, ",") {
		if strings.EqualFold(strings.TrimSpace(token), want) {
			return true
		}
	}
	return false
}
