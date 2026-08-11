//go:build windows

package service

import (
	"context"
	"errors"

	"github.com/Eser-s-Organization/ipv6mesh/internal/ipc"
)

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
	handler := NewHandler(service, authorizer)
	server, err := ipc.NewServer(path, handler, authorizer)
	if err != nil {
		return err
	}
	defer server.Close()
	return server.Serve(ctx)
}
