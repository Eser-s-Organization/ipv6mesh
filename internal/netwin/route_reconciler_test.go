package netwin

import (
	"context"
	"errors"
	"net/netip"
	"testing"
)

type fakeRouteBackend struct {
	addresses       map[string]Address
	routes          map[string]Route
	preexisting     map[string]bool
	failAddress     error
	failRoute       error
	removeAddresses int
	removeRoutes    int
}

func newFakeRouteBackend() *fakeRouteBackend {
	return &fakeRouteBackend{
		addresses:   make(map[string]Address),
		routes:      make(map[string]Route),
		preexisting: make(map[string]bool),
	}
}

func (backend *fakeRouteBackend) EnsureAddress(_ context.Context, address Address) (bool, error) {
	if backend.failAddress != nil {
		return false, backend.failAddress
	}
	key := addressKey(address)
	if _, exists := backend.addresses[key]; exists {
		return false, nil
	}
	backend.addresses[key] = address
	return true, nil
}

func (backend *fakeRouteBackend) RemoveAddress(_ context.Context, address Address) error {
	delete(backend.addresses, addressKey(address))
	backend.removeAddresses++
	return nil
}

func (backend *fakeRouteBackend) EnsureRoute(_ context.Context, route Route) (bool, error) {
	if backend.failRoute != nil {
		return false, backend.failRoute
	}
	key := routeKey(route)
	if backend.preexisting[key] {
		return false, nil
	}
	if _, exists := backend.routes[key]; exists {
		return false, nil
	}
	backend.routes[key] = route
	return true, nil
}

func (backend *fakeRouteBackend) RemoveRoute(_ context.Context, route Route) error {
	delete(backend.routes, routeKey(route))
	backend.removeRoutes++
	return nil
}

func addressKey(address Address) string {
	return address.IP.String()
}

func testOverlayAddress() Address {
	return Address{IP: netip.MustParseAddr("10.77.0.1"), PrefixLength: 32, InterfaceLUID: 42}
}

func testOverlayRoute(host string) Route {
	return Route{Destination: netip.MustParsePrefix(host + "/32"), InterfaceLUID: 42, Metric: 1}
}

func TestRouteReconcilerCreatesReplacesAndClearsOwnedResources(t *testing.T) {
	backend := newFakeRouteBackend()
	reconciler, err := NewRouteReconciler(backend)
	if err != nil {
		t.Fatal(err)
	}
	first := testOverlayRoute("10.77.0.2")
	second := testOverlayRoute("10.77.0.3")
	if err := reconciler.Reconcile(context.Background(), testOverlayAddress(), []Route{first}); err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	if len(backend.addresses) != 1 || len(backend.routes) != 1 {
		t.Fatalf("created address/routes = %d/%d, want 1/1", len(backend.addresses), len(backend.routes))
	}
	if err := reconciler.Reconcile(context.Background(), testOverlayAddress(), []Route{second}); err != nil {
		t.Fatalf("replacement Reconcile: %v", err)
	}
	if len(backend.routes) != 1 || backend.removeRoutes != 1 {
		t.Fatalf("routes after replacement = %d, removals = %d; want 1/1", len(backend.routes), backend.removeRoutes)
	}
	if err := reconciler.Clear(context.Background()); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if len(backend.addresses) != 0 || len(backend.routes) != 0 {
		t.Fatalf("resources after Clear = %d/%d, want 0/0", len(backend.addresses), len(backend.routes))
	}
}

func TestRouteReconcilerDoesNotClaimPreexistingRoutes(t *testing.T) {
	backend := newFakeRouteBackend()
	route := testOverlayRoute("10.77.0.2")
	backend.preexisting[routeKey(route)] = true
	reconciler, err := NewRouteReconciler(backend)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Reconcile(context.Background(), testOverlayAddress(), []Route{route}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := reconciler.Clear(context.Background()); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if backend.removeRoutes != 0 {
		t.Fatalf("pre-existing route was removed")
	}
}

func TestRouteReconcilerRollsBackPartialApply(t *testing.T) {
	backend := newFakeRouteBackend()
	backend.failRoute = errors.New("route apply failed")
	reconciler, err := NewRouteReconciler(backend)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Reconcile(context.Background(), testOverlayAddress(), []Route{testOverlayRoute("10.77.0.2")}); !errors.Is(err, backend.failRoute) {
		t.Fatalf("Reconcile error = %v, want route failure", err)
	}
	if len(backend.addresses) != 0 || len(backend.routes) != 0 {
		t.Fatalf("resources after rollback = %d/%d, want 0/0", len(backend.addresses), len(backend.routes))
	}
}

func TestRouteReconcilerRejectsDefaultAndForeignFamilyRoutes(t *testing.T) {
	backend := newFakeRouteBackend()
	reconciler, err := NewRouteReconciler(backend)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Reconcile(context.Background(), testOverlayAddress(), []Route{{Destination: netip.MustParsePrefix("0.0.0.0/0"), InterfaceLUID: 42}}); !errors.Is(err, ErrDefaultRoute) {
		t.Fatalf("default route error = %v, want ErrDefaultRoute", err)
	}
	if err := reconciler.Reconcile(context.Background(), testOverlayAddress(), []Route{{Destination: netip.MustParsePrefix("2001:db8::/128"), InterfaceLUID: 42}}); !errors.Is(err, ErrInvalidRoute) {
		t.Fatalf("IPv6 route error = %v, want ErrInvalidRoute", err)
	}
}

func TestRouteReconcilerRequiresBackend(t *testing.T) {
	if _, err := NewRouteReconciler(nil); !errors.Is(err, ErrInvalidBackend) {
		t.Fatalf("NewRouteReconciler error = %v, want ErrInvalidBackend", err)
	}
}
