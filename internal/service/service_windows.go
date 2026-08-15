//go:build windows

package service

import (
	"context"
	"errors"
	"time"

	"github.com/Eser-s-Organization/ipv6mesh/internal/control"
	"github.com/Eser-s-Organization/ipv6mesh/internal/endpoint"
	"github.com/Eser-s-Organization/ipv6mesh/internal/ipc"
	"github.com/Eser-s-Organization/ipv6mesh/internal/netwin"
	"github.com/Eser-s-Organization/ipv6mesh/internal/reconcile"
	"github.com/Eser-s-Organization/ipv6mesh/internal/wgnt"
)

// WindowsDataPlane owns the privileged Windows networking components used by
// snapshot reconciliation. Construction is lazy with respect to the vendor
// DLL: it does not create an adapter or modify routes until Apply is called.
type WindowsDataPlane struct {
	WireGuard *wgnt.Client
	Routes    *netwin.RouteReconciler
	Applier   *reconcile.Applier
	Endpoints *endpoint.Discoverer
}

func NewWindowsDataPlane(privateKey wgnt.Key, interfaceName string, listenPort uint16) (*WindowsDataPlane, error) {
	wireGuard := wgnt.New()
	routes, err := netwin.NewRouteReconciler(netwin.NewIPHelper())
	if err != nil {
		return nil, err
	}
	applier, err := reconcile.NewApplier(reconcile.Options{
		Adapter:       wireGuard,
		Routes:        routes,
		PrivateKey:    privateKey,
		InterfaceName: interfaceName,
		ListenPort:    listenPort,
	})
	if err != nil {
		return nil, err
	}
	discoverer, err := endpoint.NewDiscoverer(endpoint.NewWindowsEnumerator())
	if err != nil {
		return nil, err
	}
	return &WindowsDataPlane{WireGuard: wireGuard, Routes: routes, Applier: applier, Endpoints: discoverer}, nil
}

func (dataPlane *WindowsDataPlane) Clear(ctx context.Context) error {
	if dataPlane == nil || dataPlane.Applier == nil {
		return errors.New("Windows data plane is unavailable")
	}
	return dataPlane.Applier.Clear(ctx)
}

func (dataPlane *WindowsDataPlane) Connect(context.Context, string) error {
	if dataPlane == nil || dataPlane.Applier == nil {
		return errors.New("Windows data plane is unavailable")
	}
	if dataPlane.Applier.Generation() == 0 {
		return errors.New("Windows data plane has no applied snapshot")
	}
	return nil
}

func (dataPlane *WindowsDataPlane) Disconnect(ctx context.Context, _ string) error {
	return dataPlane.Clear(ctx)
}

func (dataPlane *WindowsDataPlane) Discover(ctx context.Context, port uint16) ([]control.EndpointCandidate, error) {
	if dataPlane == nil || dataPlane.Endpoints == nil {
		return nil, errors.New("Windows endpoint discovery is unavailable")
	}
	return dataPlane.Endpoints.Discover(ctx, port)
}

// ServeWindows starts the local Named Pipe boundary for an already-created
// service. Data-plane adapters are intentionally injected by the caller and
// remain outside this Task 4 boundary.
func ServeWindows(ctx context.Context, service *Service, path string, authorizer ipc.CallerAuthorizer) error {
	if service == nil || authorizer == nil {
		return errors.New("Windows service requires service and caller authorizer")
	}
	if err := service.Start(ctx); err != nil {
		return err
	}
	heartbeatContext, cancelHeartbeat := context.WithCancel(ctx)
	defer cancelHeartbeat()
	if source, ok := service.options.Adapter.(EndpointSource); ok {
		if reporter, ok := service.options.Control.(EndpointReporter); ok {
			go runEndpointHeartbeat(heartbeatContext, source, reporter, 51820)
		}
	}
	handler := NewHandler(service, authorizer)
	server, err := ipc.NewServer(path, handler, authorizer)
	if err != nil {
		return err
	}
	defer server.Close()
	return server.Serve(ctx)
}

func runEndpointHeartbeat(ctx context.Context, source EndpointSource, reporter EndpointReporter, port uint16) {
	if source == nil || reporter == nil || port == 0 {
		return
	}
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		candidates, err := source.Discover(ctx, port)
		if err == nil {
			_ = reporter.Heartbeat(ctx, candidates)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
