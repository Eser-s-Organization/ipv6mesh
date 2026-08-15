package relay

import (
	"errors"
	"net/netip"
	"testing"

	"github.com/Eser-s-Organization/ipv6mesh/internal/wgnt"
)

func TestConfigRejectsDefaultRouteAndDuplicateOverlayMembers(t *testing.T) {
	privateKey := testRelayKey(1)
	config := Config{NetworkID: "network-1", InterfaceName: "wg-mesh", ListenPort: 51820, VirtualIPv4: netip.MustParseAddr("10.42.0.1"), PrivateKey: privateKey, Peers: []Peer{
		{NodeID: "node-a", PublicKey: testRelayKey(2).Base64(), VirtualIPv4: netip.MustParseAddr("10.42.0.2")},
		{NodeID: "node-b", PublicKey: testRelayKey(2).Base64(), VirtualIPv4: netip.MustParseAddr("10.42.0.3")},
	}}
	if err := config.Validate(); !errors.Is(err, ErrDuplicatePeer) {
		t.Fatalf("duplicate public key error = %v", err)
	}
	config.Peers[1].PublicKey = testRelayKey(3).Base64()
	config.Peers[1].VirtualIPv4 = config.Peers[0].VirtualIPv4
	if err := config.Validate(); !errors.Is(err, ErrDuplicateAddress) {
		t.Fatalf("duplicate virtual address error = %v", err)
	}
}

func TestConfigBuildsHostOnlyWireGuardHubConfiguration(t *testing.T) {
	config := Config{NetworkID: "network-1", InterfaceName: "wg-mesh", ListenPort: 51820, VirtualIPv4: netip.MustParseAddr("10.42.0.1"), PrivateKey: testRelayKey(1), Peers: []Peer{{
		NodeID: "node-a", PublicKey: testRelayKey(2).Base64(), VirtualIPv4: netip.MustParseAddr("10.42.0.2"), Endpoint: netip.MustParseAddrPort("[2001:db8::2]:51820"),
	}}}
	wireGuard, err := config.WireGuardConfiguration()
	if err != nil {
		t.Fatal(err)
	}
	if !wireGuard.ReplacePeers || len(wireGuard.Peers) != 1 || wireGuard.Peers[0].AllowedIPs[0] != netip.MustParsePrefix("10.42.0.2/32") {
		t.Fatalf("unexpected relay WireGuard configuration: %#v", wireGuard)
	}
	if wireGuard.Peers[0].Endpoint != netip.MustParseAddrPort("[2001:db8::2]:51820") {
		t.Fatalf("unexpected relay endpoint: %s", wireGuard.Peers[0].Endpoint)
	}
}

func TestParseConfigUsesStrictJSONAndNeverAcceptsUnboundedRoutes(t *testing.T) {
	privateKey := testRelayKey(1).Base64()
	data := []byte(`{"network_id":"network-1","interface":"wg-mesh","listen_port":51820,"virtual_ipv4":"10.42.0.1","private_key":"` + privateKey + `","peers":[]}`)
	config, err := ParseConfig(data)
	if err != nil {
		t.Fatal(err)
	}
	if config.NetworkID != "network-1" || config.InterfaceName != "wg-mesh" {
		t.Fatalf("unexpected parsed relay config: %#v", config)
	}
	if _, err := ParseConfig(append(data, []byte(`{"unknown":true}`)...)); err == nil {
		t.Fatal("expected trailing JSON to fail")
	}
}

func TestConfigRejectsUnsafeInterfaceAndEndpointValues(t *testing.T) {
	base := Config{NetworkID: "network-1", InterfaceName: "wg-mesh", ListenPort: 51820, VirtualIPv4: netip.MustParseAddr("10.42.0.1"), PrivateKey: testRelayKey(1), Peers: []Peer{{NodeID: "node-a", PublicKey: testRelayKey(2).Base64(), VirtualIPv4: netip.MustParseAddr("10.42.0.2")}}}
	base.InterfaceName = "wg mesh"
	if err := base.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("unsafe interface error = %v", err)
	}
	base.InterfaceName = "wg-mesh"
	base.Peers[0].Endpoint = netip.MustParseAddrPort("[fe80::2%3]:51820")
	if err := base.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("link-local endpoint error = %v", err)
	}
	base.Peers[0].Endpoint = netip.AddrPort{}
	base.Peers[0].VirtualIPv4 = base.VirtualIPv4
	if err := base.Validate(); !errors.Is(err, ErrDuplicateAddress) {
		t.Fatalf("relay address collision error = %v", err)
	}
}

func TestRelayRouteValidationRejectsDefaultAndForeignFamilyRoutes(t *testing.T) {
	local := netip.MustParseAddr("10.42.0.1")
	if err := validateRelayRoutes("wg-mesh", local, []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("default route validation error = %v", err)
	}
	if err := validateRelayRoutes("wg-mesh", local, []netip.Prefix{netip.MustParsePrefix("2001:db8::/128")}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("IPv6 route validation error = %v", err)
	}
}

func testRelayKey(value byte) wgnt.Key {
	var key wgnt.Key
	key[0] = value
	return key
}
