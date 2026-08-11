package reconcile

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/Eser-s-Organization/ipv6mesh/internal/control"
	"github.com/Eser-s-Organization/ipv6mesh/internal/netwin"
	"github.com/Eser-s-Organization/ipv6mesh/internal/wgnt"
)

func TestApplierBuildsIPv6FirstWireGuardPeersAndHostRoutes(t *testing.T) {
	adapter := &fakeAdapter{status: wgnt.Status{Name: "IPv6Mesh", LUID: 77}}
	routes := &fakeRoutes{}
	privateKey := testKey(1)
	applier, err := NewApplier(Options{Adapter: adapter, Routes: routes, PrivateKey: privateKey, InterfaceName: "IPv6Mesh", ListenPort: 51820, PersistentKeepalive: 25 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := testSnapshot(2)
	snapshot.Peers = []control.Peer{{
		NodeID:      "peer-1",
		PublicKey:   testKey(2).Base64(),
		VirtualIPv4: net.ParseIP("10.42.0.3"),
		Endpoints: []control.EndpointCandidate{
			{NodeID: "peer-1", Address: net.ParseIP("192.0.2.3"), Port: 51820, Family: control.FamilyIPv4, Interface: "Ethernet", Priority: 0, ObservedAt: snapshot.GeneratedAt},
			{NodeID: "peer-1", Address: net.ParseIP("2001:db8::3"), Port: 51821, Family: control.FamilyIPv6, Interface: "Ethernet", Priority: 5, ObservedAt: snapshot.GeneratedAt},
			{NodeID: "peer-1", Address: net.ParseIP("2001:db8::4"), Port: 51822, Family: control.FamilyIPv6, Interface: "WiFi", Priority: 1, ObservedAt: snapshot.GeneratedAt},
		},
	}}

	if err := applier.Apply(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	if len(adapter.configs) != 1 || adapter.configs[0].ListenPort != 51820 || adapter.configs[0].PrivateKey != privateKey {
		t.Fatalf("unexpected adapter configuration: %#v", adapter.configs)
	}
	if len(adapter.configs[0].Peers) != 1 {
		t.Fatalf("peer count = %d", len(adapter.configs[0].Peers))
	}
	peer := adapter.configs[0].Peers[0]
	if peer.Endpoint != netip.MustParseAddrPort("[2001:db8::4]:51822") {
		t.Fatalf("expected best IPv6 endpoint, got %s", peer.Endpoint)
	}
	if len(peer.AllowedIPs) != 1 || peer.AllowedIPs[0] != netip.MustParsePrefix("10.42.0.3/32") {
		t.Fatalf("unexpected peer allowed IPs: %#v", peer.AllowedIPs)
	}
	if routes.address.IP != netip.MustParseAddr("10.42.0.2") || routes.address.InterfaceLUID != 77 || len(routes.routes) != 1 || routes.routes[0].Destination != netip.MustParsePrefix("10.42.0.3/32") {
		t.Fatalf("unexpected route reconciliation: %#v %#v", routes.address, routes.routes)
	}
}

func TestApplierRejectsOlderGenerationAndDoesNotReapplyDuplicate(t *testing.T) {
	adapter := &fakeAdapter{status: wgnt.Status{LUID: 77}}
	routes := &fakeRoutes{}
	applier, err := NewApplier(Options{Adapter: adapter, Routes: routes, PrivateKey: testKey(1)})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := testSnapshot(4)
	if err := applier.Apply(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	if err := applier.Apply(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	if len(adapter.configs) != 1 {
		t.Fatalf("duplicate generation configured %d times", len(adapter.configs))
	}
	if err := applier.Apply(context.Background(), testSnapshot(3)); !errors.Is(err, ErrStaleSnapshot) {
		t.Fatalf("older generation error = %v", err)
	}
}

func TestApplierClearsNewRoutesWhenWireGuardConfigurationFails(t *testing.T) {
	adapter := &fakeAdapter{status: wgnt.Status{LUID: 77}, configureErr: errors.New("configure failed")}
	routes := &fakeRoutes{}
	applier, err := NewApplier(Options{Adapter: adapter, Routes: routes, PrivateKey: testKey(1)})
	if err != nil {
		t.Fatal(err)
	}
	if err := applier.Apply(context.Background(), testSnapshot(1)); err == nil {
		t.Fatal("expected configuration failure")
	}
	if routes.clearCalls != 1 {
		t.Fatalf("route clear calls = %d, want one rollback", routes.clearCalls)
	}
	if applier.Generation() != 0 {
		t.Fatalf("failed apply advanced generation to %d", applier.Generation())
	}
}

func TestApplierRestoresLastGoodStateAfterReplacementFailure(t *testing.T) {
	adapter := &fakeAdapter{status: wgnt.Status{LUID: 77}, configureErrs: []error{nil, errors.New("replacement failed"), nil}}
	routes := &fakeRoutes{}
	applier, err := NewApplier(Options{Adapter: adapter, Routes: routes, PrivateKey: testKey(1)})
	if err != nil {
		t.Fatal(err)
	}
	first := testSnapshot(1)
	first.Peers = []control.Peer{{NodeID: "peer-1", PublicKey: testKey(2).Base64(), VirtualIPv4: net.ParseIP("10.42.0.3")}}
	if err := applier.Apply(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := testSnapshot(2)
	second.Peers = []control.Peer{{NodeID: "peer-2", PublicKey: testKey(3).Base64(), VirtualIPv4: net.ParseIP("10.42.0.4")}}
	if err := applier.Apply(context.Background(), second); err == nil {
		t.Fatal("expected replacement failure")
	}
	if applier.Generation() != 1 {
		t.Fatalf("replacement failure advanced generation to %d", applier.Generation())
	}
	if len(routes.routes) != 1 || routes.routes[0].Destination != netip.MustParsePrefix("10.42.0.3/32") {
		t.Fatalf("last good routes were not restored: %#v", routes.routes)
	}
	if len(adapter.configs) != 3 || len(adapter.configs[2].Peers) != 1 || adapter.configs[2].Peers[0].PublicKey != testKey(2) {
		t.Fatalf("last good WireGuard configuration was not restored: %#v", adapter.configs)
	}
}

func TestApplierReplacesEndpointsAndRemovesDeletedMembers(t *testing.T) {
	adapter := &fakeAdapter{status: wgnt.Status{LUID: 77}}
	routes := &fakeRoutes{}
	applier, err := NewApplier(Options{Adapter: adapter, Routes: routes, PrivateKey: testKey(1)})
	if err != nil {
		t.Fatal(err)
	}
	first := testSnapshot(1)
	first.Peers = []control.Peer{
		{NodeID: "peer-1", PublicKey: testKey(2).Base64(), VirtualIPv4: net.ParseIP("10.42.0.3"), Endpoints: []control.EndpointCandidate{{NodeID: "peer-1", Address: net.ParseIP("2001:db8::3"), Port: 51820, Family: control.FamilyIPv6, Interface: "Ethernet", ObservedAt: first.GeneratedAt}}},
		{NodeID: "peer-2", PublicKey: testKey(3).Base64(), VirtualIPv4: net.ParseIP("10.42.0.4")},
	}
	if err := applier.Apply(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := testSnapshot(2)
	second.Peers = []control.Peer{{NodeID: "peer-1", PublicKey: testKey(2).Base64(), VirtualIPv4: net.ParseIP("10.42.0.3"), Endpoints: []control.EndpointCandidate{{NodeID: "peer-1", Address: net.ParseIP("2001:db8::33"), Port: 51830, Family: control.FamilyIPv6, Interface: "WiFi", ObservedAt: second.GeneratedAt}}}}
	if err := applier.Apply(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if len(adapter.configs) != 2 || len(adapter.configs[1].Peers) != 1 || adapter.configs[1].Peers[0].Endpoint != netip.MustParseAddrPort("[2001:db8::33]:51830") {
		t.Fatalf("endpoint replacement or member deletion was not applied: %#v", adapter.configs)
	}
	if len(routes.routes) != 1 || routes.routes[0].Destination != netip.MustParsePrefix("10.42.0.3/32") {
		t.Fatalf("deleted member route remained: %#v", routes.routes)
	}
}

func testSnapshot(generation int64) control.NetworkSnapshot {
	return control.NetworkSnapshot{
		NetworkID:        "network-1",
		Generation:       generation,
		ConfigVersion:    generation,
		LocalNodeID:      "node-1",
		LocalVirtualIPv4: net.ParseIP("10.42.0.2"),
		GeneratedAt:      time.Date(2026, time.August, 11, 12, 0, 0, 0, time.UTC),
	}
}

func testKey(value byte) wgnt.Key {
	var key wgnt.Key
	key[0] = value
	return key
}

type fakeAdapter struct {
	status        wgnt.Status
	configs       []wgnt.Configuration
	configureErr  error
	configureErrs []error
}

func (adapter *fakeAdapter) Ensure(string) (wgnt.Handle, error) { return 1, nil }
func (adapter *fakeAdapter) Configure(_ context.Context, _ wgnt.Handle, configuration wgnt.Configuration) error {
	adapter.configs = append(adapter.configs, configuration)
	if len(adapter.configureErrs) > 0 {
		err := adapter.configureErrs[0]
		adapter.configureErrs = adapter.configureErrs[1:]
		return err
	}
	return adapter.configureErr
}
func (adapter *fakeAdapter) SetUp(context.Context, wgnt.Handle) error   { return nil }
func (adapter *fakeAdapter) SetDown(context.Context, wgnt.Handle) error { return nil }
func (adapter *fakeAdapter) Delete(context.Context, wgnt.Handle) error  { return nil }
func (adapter *fakeAdapter) Status(context.Context, wgnt.Handle) (wgnt.Status, error) {
	return adapter.status, nil
}

type fakeRoutes struct {
	address    netwin.Address
	routes     []netwin.Route
	clearCalls int
}

func (routes *fakeRoutes) Reconcile(_ context.Context, address netwin.Address, desired []netwin.Route) error {
	routes.address = address
	routes.routes = append([]netwin.Route(nil), desired...)
	return nil
}
func (routes *fakeRoutes) Clear(context.Context) error {
	routes.clearCalls++
	routes.address = netwin.Address{}
	routes.routes = nil
	return nil
}
