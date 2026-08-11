//go:build windows

package main

import (
	"context"
	"errors"

	"golang.org/x/sys/windows/svc"
)

const windowsServiceName = "IPv6Mesh"

type serviceServeFunc func(context.Context) error

func executeService(requests <-chan svc.ChangeRequest, changes chan<- svc.Status, serve serviceServeFunc) (bool, uint32) {
	if requests == nil || changes == nil || serve == nil {
		return false, 1
	}
	changes <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveResult := make(chan error, 1)
	go func() { serveResult <- serve(ctx) }()
	changes <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}

	for {
		select {
		case request, ok := <-requests:
			if !ok {
				return stopService(ctx, cancel, serveResult, changes, 1)
			}
			switch request.Cmd {
			case svc.Stop, svc.Shutdown:
				return stopService(ctx, cancel, serveResult, changes, 0)
			}
		case err := <-serveResult:
			if err != nil && !errors.Is(err, context.Canceled) {
				return false, 1
			}
			return false, 0
		}
	}
}

func stopService(ctx context.Context, cancel context.CancelFunc, serveResult <-chan error, changes chan<- svc.Status, initialCode uint32) (bool, uint32) {
	changes <- svc.Status{State: svc.StopPending}
	cancel()
	select {
	case err := <-serveResult:
		if err != nil && !errors.Is(err, context.Canceled) && initialCode == 0 {
			return false, 1
		}
	case <-ctx.Done():
		select {
		case err := <-serveResult:
			if err != nil && !errors.Is(err, context.Canceled) && initialCode == 0 {
				return false, 1
			}
		default:
		}
	}
	return false, initialCode
}
