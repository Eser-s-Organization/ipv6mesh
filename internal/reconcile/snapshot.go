// Package reconcile turns a versioned control-plane snapshot into a narrowly
// scoped WireGuardNT configuration and IPv4 /32 route set.
package reconcile

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Eser-s-Organization/ipv6mesh/internal/control"
	"github.com/Eser-s-Organization/ipv6mesh/internal/netwin"
	"github.com/Eser-s-Organization/ipv6mesh/internal/wgnt"
)

var (
	ErrInvalidOptions  = errors.New("snapshot applier options are invalid")
	ErrInvalidSnapshot = errors.New("control-plane snapshot is invalid")
	ErrStaleSnapshot   = errors.New("control-plane snapshot generation is stale")
)

type RouteManager interface {
	Reconcile(context.Context, netwin.Address, []netwin.Route) error
	Clear(context.Context) error
}

type Options struct {
	Adapter             wgnt.Adapter
	Routes              RouteManager
	PrivateKey          wgnt.Key
	InterfaceName       string
	ListenPort          uint16
	PersistentKeepalive time.Duration
}

type Applier struct {
	mu         sync.Mutex
	options    Options
	state      appliedState
	generation int64
}

type appliedState struct {
	applied   bool
	handle    wgnt.Handle
	up        bool
	config    wgnt.Configuration
	address   netwin.Address
	routes    []netwin.Route
	networkID string
}

func NewApplier(options Options) (*Applier, error) {
	if options.Adapter == nil || options.Routes == nil || options.PrivateKey.IsZero() {
		return nil, ErrInvalidOptions
	}
	if strings.TrimSpace(options.InterfaceName) == "" {
		options.InterfaceName = "IPv6Mesh"
	}
	if options.ListenPort == 0 {
		options.ListenPort = 51820
	}
	if options.PersistentKeepalive < 0 || options.PersistentKeepalive > 65535*time.Second || options.PersistentKeepalive%time.Second != 0 {
		return nil, fmt.Errorf("%w: persistent keepalive is out of range", ErrInvalidOptions)
	}
	return &Applier{options: options}, nil
}

func (applier *Applier) Apply(ctx context.Context, snapshot control.NetworkSnapshot) error {
	if applier == nil {
		return ErrInvalidOptions
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	applier.mu.Lock()
	defer applier.mu.Unlock()
	if snapshot.Generation <= 0 {
		return fmt.Errorf("%w: generation must be positive", ErrInvalidSnapshot)
	}
	if snapshot.Generation < applier.generation {
		return fmt.Errorf("%w: got %d after %d", ErrStaleSnapshot, snapshot.Generation, applier.generation)
	}
	if snapshot.Generation == applier.generation {
		return nil
	}

	configuration, address, routes, err := applier.plan(snapshot)
	if err != nil {
		return err
	}
	handle, err := applier.options.Adapter.Ensure(applier.options.InterfaceName)
	if err != nil {
		return fmt.Errorf("ensure WireGuard adapter: %w", err)
	}
	if !handle.Valid() {
		return fmt.Errorf("%w: adapter returned an invalid handle", ErrInvalidOptions)
	}
	status, err := applier.options.Adapter.Status(ctx, handle)
	if err != nil {
		return fmt.Errorf("read WireGuard adapter status: %w", err)
	}
	if status.LUID == 0 {
		return fmt.Errorf("%w: adapter has no interface LUID", ErrInvalidSnapshot)
	}
	address.InterfaceLUID = status.LUID
	for index := range routes {
		routes[index].InterfaceLUID = status.LUID
	}

	previous := cloneState(applier.state)
	if err := applier.options.Routes.Reconcile(ctx, address, routes); err != nil {
		return fmt.Errorf("reconcile overlay routes: %w", err)
	}
	if err := applier.options.Adapter.Configure(ctx, handle, configuration); err != nil {
		return applier.rollback(ctx, previous, handle, fmt.Errorf("configure WireGuard adapter: %w", err))
	}
	if err := applier.options.Adapter.SetUp(ctx, handle); err != nil {
		return applier.rollback(ctx, previous, handle, fmt.Errorf("bring WireGuard adapter up: %w", err))
	}

	applier.state = appliedState{applied: true, handle: handle, up: true, config: cloneConfiguration(configuration), address: address, routes: cloneRoutes(routes), networkID: snapshot.NetworkID}
	applier.generation = snapshot.Generation
	return nil
}

func (applier *Applier) Clear(ctx context.Context) error {
	if applier == nil {
		return ErrInvalidOptions
	}
	applier.mu.Lock()
	defer applier.mu.Unlock()
	if err := contextError(ctx); err != nil {
		return err
	}
	if !applier.state.applied {
		return nil
	}
	var joined error
	if err := applier.options.Routes.Clear(ctx); err != nil {
		joined = errors.Join(joined, err)
	}
	if err := applier.options.Adapter.SetDown(ctx, applier.state.handle); err != nil {
		joined = errors.Join(joined, err)
	}
	if joined == nil {
		applier.state = appliedState{}
		applier.generation = 0
	}
	return joined
}

func (applier *Applier) Generation() int64 {
	if applier == nil {
		return 0
	}
	applier.mu.Lock()
	defer applier.mu.Unlock()
	return applier.generation
}

func (applier *Applier) plan(snapshot control.NetworkSnapshot) (wgnt.Configuration, netwin.Address, []netwin.Route, error) {
	if strings.TrimSpace(snapshot.NetworkID) == "" || strings.TrimSpace(snapshot.LocalNodeID) == "" {
		return wgnt.Configuration{}, netwin.Address{}, nil, fmt.Errorf("%w: network and local node are required", ErrInvalidSnapshot)
	}
	localIP, ok := ipv4Addr(snapshot.LocalVirtualIPv4)
	if !ok {
		return wgnt.Configuration{}, netwin.Address{}, nil, fmt.Errorf("%w: local virtual address must be IPv4", ErrInvalidSnapshot)
	}
	configuration := wgnt.Configuration{PrivateKey: applier.options.PrivateKey, ListenPort: applier.options.ListenPort, ReplacePeers: true, Peers: make([]wgnt.Peer, 0, len(snapshot.Peers))}
	routes := make([]netwin.Route, 0, len(snapshot.Peers))
	seenKeys := make(map[wgnt.Key]struct{}, len(snapshot.Peers))
	seenAddresses := make(map[netip.Addr]struct{}, len(snapshot.Peers))
	for index, peer := range snapshot.Peers {
		if strings.TrimSpace(peer.NodeID) == "" || peer.NodeID == snapshot.LocalNodeID {
			return wgnt.Configuration{}, netwin.Address{}, nil, fmt.Errorf("%w: peer %d has an invalid node identity", ErrInvalidSnapshot, index)
		}
		publicKey, err := wgnt.ParseKey(peer.PublicKey)
		if err != nil {
			return wgnt.Configuration{}, netwin.Address{}, nil, fmt.Errorf("%w: peer %s public key: %v", ErrInvalidSnapshot, peer.NodeID, err)
		}
		if publicKey.IsZero() {
			return wgnt.Configuration{}, netwin.Address{}, nil, fmt.Errorf("%w: peer %s public key is empty", ErrInvalidSnapshot, peer.NodeID)
		}
		if _, exists := seenKeys[publicKey]; exists {
			return wgnt.Configuration{}, netwin.Address{}, nil, fmt.Errorf("%w: duplicate peer public key", ErrInvalidSnapshot)
		}
		virtualIP, ok := ipv4Addr(peer.VirtualIPv4)
		if !ok || virtualIP == localIP {
			return wgnt.Configuration{}, netwin.Address{}, nil, fmt.Errorf("%w: peer %s virtual address is invalid", ErrInvalidSnapshot, peer.NodeID)
		}
		if _, exists := seenAddresses[virtualIP]; exists {
			return wgnt.Configuration{}, netwin.Address{}, nil, fmt.Errorf("%w: duplicate peer virtual address", ErrInvalidSnapshot)
		}
		seenKeys[publicKey] = struct{}{}
		seenAddresses[virtualIP] = struct{}{}
		relay := snapshot.RelayAssignment
		if relay == nil {
			relay = snapshot.Relay
		}
		endpoint := selectEndpoint(peer, relay)
		configuration.Peers = append(configuration.Peers, wgnt.Peer{PublicKey: publicKey, Endpoint: endpoint, PersistentKeepalive: applier.options.PersistentKeepalive, AllowedIPs: []netip.Prefix{netip.PrefixFrom(virtualIP, 32)}})
		routes = append(routes, netwin.Route{Destination: netip.PrefixFrom(virtualIP, 32)})
	}
	if err := configuration.Validate(); err != nil {
		return wgnt.Configuration{}, netwin.Address{}, nil, err
	}
	return configuration, netwin.Address{IP: localIP, PrefixLength: 32}, routes, nil
}

type endpointChoice struct {
	address  netip.AddrPort
	family   int
	priority int
	observed time.Time
}

func selectEndpoint(peer control.Peer, relay *control.RelayAssignment) netip.AddrPort {
	candidates := append([]control.EndpointCandidate(nil), peer.Endpoints...)
	if relay != nil && relay.Status == control.RelayAssignmentActive && relay.RelayNodeID == peer.NodeID && relay.Address != nil && relay.Port != 0 {
		candidates = append(candidates, control.EndpointCandidate{NodeID: peer.NodeID, Address: relay.Address, Port: relay.Port, Family: relay.Family, Interface: "relay", Priority: 0, ObservedAt: relay.AssignedAt})
	}
	choices := make([]endpointChoice, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Address == nil || candidate.Port == 0 || candidate.ObservedAt.IsZero() || candidate.Priority < 0 {
			continue
		}
		address, err := netip.ParseAddr(candidate.Address.String())
		if err != nil || address.IsUnspecified() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsMulticast() || !address.IsGlobalUnicast() {
			continue
		}
		family := 1
		if candidate.Family == control.FamilyIPv6 && address.Is6() {
			family = 0
		} else if candidate.Family != control.FamilyIPv4 || !address.Is4() {
			continue
		}
		choices = append(choices, endpointChoice{address: netip.AddrPortFrom(address, candidate.Port), family: family, priority: candidate.Priority, observed: candidate.ObservedAt})
	}
	sort.SliceStable(choices, func(left, right int) bool {
		if choices[left].family != choices[right].family {
			return choices[left].family < choices[right].family
		}
		if choices[left].priority != choices[right].priority {
			return choices[left].priority < choices[right].priority
		}
		if !choices[left].observed.Equal(choices[right].observed) {
			return choices[left].observed.After(choices[right].observed)
		}
		return choices[left].address.String() < choices[right].address.String()
	})
	if len(choices) == 0 {
		return netip.AddrPort{}
	}
	return choices[0].address
}

func (applier *Applier) rollback(ctx context.Context, previous appliedState, handle wgnt.Handle, original error) error {
	var joined error = original
	if previous.applied {
		if err := applier.options.Routes.Reconcile(ctx, previous.address, previous.routes); err != nil {
			joined = errors.Join(joined, fmt.Errorf("restore previous routes: %w", err))
		}
		if err := applier.options.Adapter.Configure(ctx, previous.handle, previous.config); err != nil {
			joined = errors.Join(joined, fmt.Errorf("restore previous WireGuard configuration: %w", err))
		} else if previous.up {
			if err := applier.options.Adapter.SetUp(ctx, previous.handle); err != nil {
				joined = errors.Join(joined, fmt.Errorf("restore previous WireGuard state: %w", err))
			}
		}
		return joined
	}
	if err := applier.options.Routes.Clear(ctx); err != nil {
		joined = errors.Join(joined, fmt.Errorf("clear failed overlay routes: %w", err))
	}
	if err := applier.options.Adapter.SetDown(ctx, handle); err != nil {
		joined = errors.Join(joined, fmt.Errorf("restore WireGuard down state: %w", err))
	}
	return joined
}

func ipv4Addr(value []byte) (netip.Addr, bool) {
	address := net.IP(value).To4()
	if address == nil {
		return netip.Addr{}, false
	}
	return netip.AddrFrom4([4]byte{address[0], address[1], address[2], address[3]}), true
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return context.Canceled
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func cloneState(state appliedState) appliedState {
	clone := state
	clone.config = cloneConfiguration(state.config)
	clone.routes = cloneRoutes(state.routes)
	return clone
}

func cloneConfiguration(configuration wgnt.Configuration) wgnt.Configuration {
	clone := configuration
	clone.Peers = append([]wgnt.Peer(nil), configuration.Peers...)
	for index := range clone.Peers {
		clone.Peers[index].AllowedIPs = append([]netip.Prefix(nil), configuration.Peers[index].AllowedIPs...)
	}
	return clone
}

func cloneRoutes(routes []netwin.Route) []netwin.Route {
	return append([]netwin.Route(nil), routes...)
}
