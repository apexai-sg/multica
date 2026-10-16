package model

import "testing"

func poolConfigJSON(pool string) []byte {
	extra := ""
	if pool != "" {
		extra = `,"network_pool":"` + pool + `"`
	}
	return []byte(`{"namespace":"ns","fleet_id":"fleet","image":"img","api_url":"http://127.0.0.1:1","max_nodes":1,"specs":{"sandbox":{"cpus":1,"memory_bytes":1,"pids":1,"max_runs":1}}` + extra + `}`)
}

// TestDecodeConfigNetworkPool pins the accepted shape: one IPv4 CIDR of /24 or
// wider, and nothing else. A pool is what keeps node networks off the host's LAN.
func TestDecodeConfigNetworkPool(t *testing.T) {
	cfg, err := DecodeConfig(poolConfigJSON("10.213.0.0/16"))
	if err != nil {
		t.Fatalf("valid pool rejected: %v", err)
	}
	if cfg.NetworkPool != "10.213.0.0/16" {
		t.Fatalf("pool = %q", cfg.NetworkPool)
	}
	if cfg, err = DecodeConfig(poolConfigJSON("")); err != nil || cfg.NetworkPool != "" {
		t.Fatalf("absent pool = %q err=%v", cfg.NetworkPool, err)
	}
	for _, bad := range []string{"10.213.0.0/25", "not-a-cidr", "2001:db8::/64"} {
		if _, err = DecodeConfig(poolConfigJSON(bad)); err == nil {
			t.Fatalf("pool %q was accepted", bad)
		}
	}
}
