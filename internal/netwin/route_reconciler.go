// Package netwin owns the virtual IPv4 address and /32 route entries created
// for the overlay. It deliberately has no default-route operation.
package netwin

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sync"
)

var (
	ErrUnsupportedPlatform = errors.New("Windows IP Helper is unsupported on this platform")
	ErrInvalidRoute        = errors.New("invalid overlay route")
	ErrDefaultRoute        = errors.New("default routes are forbidden for the overlay")
	ErrInvalidBackend      = errors.New("route reconciler backend is missing")
)

type Address struct {
	IP            netip.Addr
	PrefixLength  uint8
	InterfaceLUID uint64
}

type Route struct {
	Destination   netip.Prefix
	InterfaceLUID uint64
	NextHop       netip.Addr
	Metric        uint32
}

// Backend applies one operation at a time to Windows IP Helper. The bool
// result is true only when this process created the resource; a pre-existing
// user-owned address or route is never claimed for later deletion.
type Backend interface {
	EnsureAddress(context.Context, Address) (bool, error)
	RemoveAddress(context.Context, Address) error
	EnsureRoute(context.Context, Route) (bool, error)
	RemoveRoute(context.Context, Route) error
}

type RouteReconciler struct {
	mu           sync.Mutex
	backend      Backend
	address      *Address
	addressOwned bool
	ownedRoutes  map[string]Route
}

func NewRouteReconciler(backend Backend) (*RouteReconciler, error) {
	if backend == nil {
		return nil, ErrInvalidBackend
	}
	return &RouteReconciler{backend: backend, ownedRoutes: make(map[string]Route)}, nil
}

// Reconcile applies the requested virtual address and host routes. Only
// resources created by this reconciler instance are eligible for removal.
func (reconciler *RouteReconciler) Reconcile(ctx context.Context, address Address, routes []Route) error {
	if reconciler == nil || reconciler.backend == nil {
		return ErrInvalidBackend
	}
	if err := validateAddress(address); err != nil {
		return err
	}
	desired := make(map[string]Route, len(routes))
	for _, route := range routes {
		if err := validateRoute(route); err != nil {
			return err
		}
		key := routeKey(route)
		if _, exists := desired[key]; exists {
			return fmt.Errorf("%w: duplicate route", ErrInvalidRoute)
		}
		desired[key] = route
	}

	reconciler.mu.Lock()
	defer reconciler.mu.Unlock()
	if err := contextErr(ctx); err != nil {
		return err
	}
	previousAddress, previousAddressOwned := copyAddress(reconciler.address), reconciler.addressOwned
	previousRoutes := cloneRoutes(reconciler.ownedRoutes)
	addedRoutes := make(map[string]Route)
	removedRoutes := make(map[string]Route)
	newAddressOwned := previousAddressOwned && previousAddress != nil && *previousAddress == address
	addressChanged := previousAddressOwned && previousAddress != nil && *previousAddress != address

	if addressChanged || !newAddressOwned {
		created, err := reconciler.backend.EnsureAddress(ctx, address)
		if err != nil {
			return err
		}
		newAddressOwned = created
	}

	for key, route := range previousRoutes {
		if _, keep := desired[key]; keep {
			continue
		}
		if err := reconciler.backend.RemoveRoute(ctx, route); err != nil {
			return reconciler.rollback(ctx, previousAddress, previousAddressOwned, addedRoutes, removedRoutes, address, newAddressOwned, err)
		}
		removedRoutes[key] = route
	}

	for key, route := range desired {
		if _, alreadyOwned := previousRoutes[key]; alreadyOwned {
			continue
		}
		created, err := reconciler.backend.EnsureRoute(ctx, route)
		if err != nil {
			return reconciler.rollback(ctx, previousAddress, previousAddressOwned, addedRoutes, removedRoutes, address, newAddressOwned, err)
		}
		if created {
			addedRoutes[key] = route
		}
	}

	if addressChanged {
		if err := reconciler.backend.RemoveAddress(ctx, *previousAddress); err != nil {
			return reconciler.rollback(ctx, previousAddress, previousAddressOwned, addedRoutes, removedRoutes, address, newAddressOwned, err)
		}
	}
	reconciler.address = nil
	if newAddressOwned {
		addressCopy := address
		reconciler.address = &addressCopy
	}
	reconciler.addressOwned = newAddressOwned
	reconciler.ownedRoutes = addedRoutes
	for key, route := range previousRoutes {
		if _, keep := desired[key]; keep {
			reconciler.ownedRoutes[key] = route
		}
	}
	return nil
}

// Clear removes only resources that this reconciler has created successfully.
func (reconciler *RouteReconciler) Clear(ctx context.Context) error {
	if reconciler == nil || reconciler.backend == nil {
		return ErrInvalidBackend
	}
	reconciler.mu.Lock()
	defer reconciler.mu.Unlock()
	if err := contextErr(ctx); err != nil {
		return err
	}
	var joined error
	for key, route := range reconciler.ownedRoutes {
		if err := reconciler.backend.RemoveRoute(ctx, route); err != nil {
			joined = errors.Join(joined, err)
			continue
		}
		delete(reconciler.ownedRoutes, key)
	}
	if reconciler.addressOwned && reconciler.address != nil {
		if err := reconciler.backend.RemoveAddress(ctx, *reconciler.address); err != nil {
			joined = errors.Join(joined, err)
		} else {
			reconciler.address = nil
			reconciler.addressOwned = false
		}
	}
	return joined
}

func (reconciler *RouteReconciler) rollback(ctx context.Context, previousAddress *Address, previousAddressOwned bool, addedRoutes, removedRoutes map[string]Route, newAddress Address, newAddressOwned bool, original error) error {
	var rollbackError error
	for _, route := range addedRoutes {
		rollbackError = errors.Join(rollbackError, reconciler.backend.RemoveRoute(ctx, route))
	}
	for _, route := range removedRoutes {
		_, err := reconciler.backend.EnsureRoute(ctx, route)
		rollbackError = errors.Join(rollbackError, err)
	}
	if newAddressOwned && (previousAddress == nil || *previousAddress != newAddress) {
		rollbackError = errors.Join(rollbackError, reconciler.backend.RemoveAddress(ctx, newAddress))
	}
	if previousAddressOwned && previousAddress != nil && (newAddressOwned == false || *previousAddress != newAddress) {
		_, err := reconciler.backend.EnsureAddress(ctx, *previousAddress)
		rollbackError = errors.Join(rollbackError, err)
	}
	return errors.Join(original, rollbackError)
}

func validateAddress(address Address) error {
	if !address.IP.IsValid() || !address.IP.Is4() || address.PrefixLength != 32 || address.InterfaceLUID == 0 {
		return fmt.Errorf("%w: overlay address must be an IPv4 /32 with a non-zero interface LUID", ErrInvalidRoute)
	}
	return nil
}

func validateRoute(route Route) error {
	if route.Destination.IsValid() && route.Destination.Bits() == 0 {
		return fmt.Errorf("%w: default route is not an overlay route", ErrDefaultRoute)
	}
	if !route.Destination.IsValid() || !route.Destination.Addr().Is4() || route.Destination.Bits() != 32 || route.InterfaceLUID == 0 {
		return fmt.Errorf("%w: route must be an IPv4 /32 with a non-zero interface LUID", ErrInvalidRoute)
	}
	if route.NextHop.IsValid() && !route.NextHop.Is4() {
		return fmt.Errorf("%w: next hop must be IPv4", ErrInvalidRoute)
	}
	return nil
}

func routeKey(route Route) string {
	nextHop := ""
	if route.NextHop.IsValid() {
		nextHop = route.NextHop.String()
	}
	return fmt.Sprintf("%d|%s|%s|%d", route.InterfaceLUID, route.Destination.String(), nextHop, route.Metric)
}

func copyAddress(address *Address) *Address {
	if address == nil {
		return nil
	}
	copy := *address
	return &copy
}

func cloneRoutes(routes map[string]Route) map[string]Route {
	clone := make(map[string]Route, len(routes))
	for key, route := range routes {
		clone[key] = route
	}
	return clone
}

func contextErr(ctx context.Context) error {
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
