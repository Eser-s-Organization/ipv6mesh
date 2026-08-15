package relay

import (
	"net/netip"
	"sync"
)

type RouteManager struct {
	runner  CommandRunner
	mu      sync.Mutex
	owned   map[string]netip.Prefix
	address netip.Addr
	iface   string
}

func NewRouteManagerWithRunner(runner CommandRunner) *RouteManager {
	return &RouteManager{runner: runner, owned: make(map[string]netip.Prefix)}
}

func routeKey(prefix netip.Prefix) string { return prefix.Masked().String() }

func validateRelayRoutes(interfaceName string, local netip.Addr, peers []netip.Prefix) error {
	if !validInterfaceName(interfaceName) || !local.IsValid() || !local.Is4() || local.IsUnspecified() {
		return ErrInvalidConfig
	}
	seen := make(map[string]struct{}, len(peers))
	for _, peer := range peers {
		if !peer.IsValid() || !peer.Addr().Is4() || peer.Bits() != 32 || peer.Addr().IsUnspecified() || peer.Addr() == local {
			return ErrInvalidConfig
		}
		key := routeKey(peer)
		if _, exists := seen[key]; exists {
			return ErrDuplicateAddress
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validInterfaceName(value string) bool {
	if value == "" || len(value) > 15 {
		return false
	}
	for _, character := range value {
		if character <= ' ' || character == '/' || character == '\\' {
			return false
		}
	}
	return true
}
