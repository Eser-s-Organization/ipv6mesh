package relay

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"github.com/Eser-s-Organization/ipv6mesh/internal/wgnt"
)

func TestRuntimeAppliesWireGuardBeforeOverlayRoutes(t *testing.T) {
	wireGuard := &fakeWireGuard{}
	routes := &fakeRelayRoutes{}
	runtime, err := NewRuntime(wireGuard, routes)
	if err != nil {
		t.Fatal(err)
	}
	config := Config{NetworkID: "network-1", InterfaceName: "wg-mesh", ListenPort: 51820, VirtualIPv4: netip.MustParseAddr("10.42.0.1"), PrivateKey: testRelayKey(1), Peers: []Peer{{NodeID: "node-a", PublicKey: testRelayKey(2).Base64(), VirtualIPv4: netip.MustParseAddr("10.42.0.2")}}}
	if err := runtime.Apply(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	if wireGuard.applyCalls != 1 || routes.applyCalls != 1 || routes.local != netip.MustParseAddr("10.42.0.1") || len(routes.peers) != 1 {
		t.Fatalf("unexpected runtime apply calls: %#v %#v", wireGuard, routes)
	}
}

func TestRuntimeRollsWireGuardDownWhenRouteApplyFails(t *testing.T) {
	wireGuard := &fakeWireGuard{}
	routes := &fakeRelayRoutes{applyErr: errors.New("route failed")}
	runtime, err := NewRuntime(wireGuard, routes)
	if err != nil {
		t.Fatal(err)
	}
	config := Config{NetworkID: "network-1", InterfaceName: "wg-mesh", ListenPort: 51820, VirtualIPv4: netip.MustParseAddr("10.42.0.1"), PrivateKey: testRelayKey(1), Peers: []Peer{{NodeID: "node-a", PublicKey: testRelayKey(2).Base64(), VirtualIPv4: netip.MustParseAddr("10.42.0.2")}}}
	if err := runtime.Apply(context.Background(), config); err == nil {
		t.Fatal("expected route failure")
	}
	if wireGuard.downCalls != 1 {
		t.Fatalf("WireGuard down calls = %d, want rollback", wireGuard.downCalls)
	}
}

type fakeWireGuard struct {
	applyCalls int
	downCalls  int
}

func (wireGuard *fakeWireGuard) Apply(context.Context, string, wgnt.Configuration) error {
	wireGuard.applyCalls++
	return nil
}
func (wireGuard *fakeWireGuard) Down(context.Context, string) error {
	wireGuard.downCalls++
	return nil
}

type fakeRelayRoutes struct {
	applyCalls int
	local      netip.Addr
	peers      []netip.Prefix
	applyErr   error
}

func (routes *fakeRelayRoutes) Apply(_ context.Context, _ string, local netip.Addr, peers []netip.Prefix) error {
	routes.applyCalls++
	routes.local = local
	routes.peers = append([]netip.Prefix(nil), peers...)
	return routes.applyErr
}
func (routes *fakeRelayRoutes) Clear(context.Context, string) error { return nil }
