//go:build linux

package relay

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sort"
)

func NewRouteManager() *RouteManager { return NewRouteManagerWithRunner(execRunner{}) }

func (manager *RouteManager) Apply(ctx context.Context, interfaceName string, local netip.Addr, peers []netip.Prefix) error {
	if manager == nil || manager.runner == nil {
		return ErrUnsupportedPlatform
	}
	if err := validateRelayRoutes(interfaceName, local, peers); err != nil {
		return err
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	desired := make(map[string]netip.Prefix, len(peers))
	for _, peer := range peers {
		desired[routeKey(peer)] = peer.Masked()
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	added := make([]netip.Prefix, 0)
	if manager.address != local || manager.iface != interfaceName {
		if _, err := manager.runner.Run(ctx, "ip", "-4", "addr", "add", local.String()+"/32", "dev", interfaceName); err != nil {
			return fmt.Errorf("add relay virtual address: %w", err)
		}
		added = append(added, netip.PrefixFrom(local, 32))
	}
	keys := make([]string, 0, len(desired))
	for key := range desired {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, exists := manager.owned[key]; exists && manager.iface == interfaceName && manager.address == local {
			continue
		}
		if _, err := manager.runner.Run(ctx, "ip", "-4", "route", "add", desired[key].String(), "dev", interfaceName); err != nil {
			for _, prefix := range added {
				if prefix.Addr() == local {
					_, _ = manager.runner.Run(ctx, "ip", "-4", "addr", "del", prefix.String(), "dev", interfaceName)
				} else {
					_, _ = manager.runner.Run(ctx, "ip", "-4", "route", "del", prefix.String(), "dev", interfaceName)
				}
			}
			return fmt.Errorf("add relay overlay route %s: %w", desired[key], err)
		}
		added = append(added, desired[key])
	}
	for key, prefix := range manager.owned {
		if _, keep := desired[key]; keep && manager.iface == interfaceName && manager.address == local {
			continue
		}
		if _, err := manager.runner.Run(ctx, "ip", "-4", "route", "del", prefix.String(), "dev", manager.iface); err != nil {
			return errors.Join(fmt.Errorf("remove stale relay route %s: %w", prefix, err), manager.rollbackAdded(ctx, interfaceName, local, added))
		}
	}
	if manager.address.IsValid() && (manager.address != local || manager.iface != interfaceName) {
		if _, err := manager.runner.Run(ctx, "ip", "-4", "addr", "del", manager.address.String()+"/32", "dev", manager.iface); err != nil {
			return errors.Join(fmt.Errorf("remove stale relay virtual address: %w", err), manager.rollbackAdded(ctx, interfaceName, local, added))
		}
	}
	manager.owned = desired
	manager.address = local
	manager.iface = interfaceName
	return nil
}

func (manager *RouteManager) rollbackAdded(ctx context.Context, interfaceName string, local netip.Addr, added []netip.Prefix) error {
	var joined error
	for _, prefix := range added {
		if prefix.Addr() == local {
			_, err := manager.runner.Run(ctx, "ip", "-4", "addr", "del", prefix.String(), "dev", interfaceName)
			joined = errors.Join(joined, err)
		} else {
			_, err := manager.runner.Run(ctx, "ip", "-4", "route", "del", prefix.String(), "dev", interfaceName)
			joined = errors.Join(joined, err)
		}
	}
	return joined
}

func (manager *RouteManager) Clear(ctx context.Context, interfaceName string) error {
	if manager == nil || manager.runner == nil {
		return ErrUnsupportedPlatform
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if interfaceName == "" {
		interfaceName = manager.iface
	}
	if manager.iface != "" && interfaceName != manager.iface {
		return ErrInvalidConfig
	}
	var joined error
	for _, prefix := range manager.owned {
		if _, err := manager.runner.Run(ctx, "ip", "-4", "route", "del", prefix.String(), "dev", interfaceName); err != nil {
			joined = errors.Join(joined, err)
		}
	}
	if manager.address.IsValid() {
		if _, err := manager.runner.Run(ctx, "ip", "-4", "addr", "del", manager.address.String()+"/32", "dev", interfaceName); err != nil {
			joined = errors.Join(joined, err)
		}
	}
	if joined == nil {
		manager.owned = make(map[string]netip.Prefix)
		manager.address = netip.Addr{}
		manager.iface = ""
	}
	return joined
}
