package docker

import (
	"net"
	"testing"

	guuid "github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/fleet/model"
	"github.com/multica-ai/multica/server/internal/util"
)

// TestWorkspaceNetworkSubnetIsPinnedInsideTheConfiguredPool is the regression for
// a node network landing on the host's own LAN. Docker's automatic pool once gave
// a node 192.168.0.0/20, which covered the operator's 192.168.15.0/24 and left
// that LAN without a return path until the network was deleted. With
// network_pool pinned, every node owns a deterministic /24 of that pool instead.
func TestWorkspaceNetworkSubnetIsPinnedInsideTheConfiguredPool(t *testing.T) {
	p := &Provider{cfg: model.Config{Namespace: "pool-ns", FleetID: "pool-fleet", NetworkPool: "10.213.0.0/16"}}
	node := auroraNode()
	first := p.workspaceNetwork(node).Subnet
	if first == "" {
		t.Fatal("a pinned pool produced no subnet")
	}
	_, subnet, err := net.ParseCIDR(first)
	if err != nil || subnet.IP.To4() == nil {
		t.Fatalf("subnet %q is not an IPv4 CIDR", first)
	}
	if ones, bits := subnet.Mask.Size(); bits != 32 || ones != 24 {
		t.Fatalf("subnet %q is not a /24", first)
	}
	// Docker rejects a /24 whose host part is non-zero, so the pinned subnet
	// must already be the network address. ParseCIDR normalizes, so comparing
	// its spelling catches an index that landed in the fourth octet.
	if subnet.String() != first {
		t.Fatalf("subnet %q is not a canonical network address (%q)", first, subnet.String())
	}
	_, pool, err := net.ParseCIDR("10.213.0.0/16")
	if err != nil || !pool.Contains(subnet.IP) {
		t.Fatalf("subnet %q escaped the configured pool", first)
	}
	if again := p.workspaceNetwork(node).Subnet; again != first {
		t.Fatalf("subnet is not deterministic: %q then %q", first, again)
	}
	other := node
	id, err := util.ParseUUID(guuid.NewString())
	if err != nil {
		t.Fatalf("fixture node id: %v", err)
	}
	other.ID = id
	if otherSubnet := p.workspaceNetwork(other).Subnet; otherSubnet == first {
		t.Fatalf("two nodes share the subnet %q", first)
	}
}

// TestWorkspaceNetworkSubnetStaysDockerOwnedWithoutAPool proves the default keeps
// today's behavior: no pool configured, no subnet requested.
func TestWorkspaceNetworkSubnetStaysDockerOwnedWithoutAPool(t *testing.T) {
	p := &Provider{cfg: model.Config{Namespace: "pool-ns", FleetID: "pool-fleet"}}
	if got := p.workspaceNetwork(auroraNode()).Subnet; got != "" {
		t.Fatalf("subnet without a pool = %q, want empty", got)
	}
}
