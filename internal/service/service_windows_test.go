//go:build windows

package service

import (
	"context"
	"testing"

	"github.com/Eser-s-Organization/ipv6mesh/internal/control"
	"github.com/Eser-s-Organization/ipv6mesh/internal/wgnt"
)

func TestNewWindowsDataPlaneBuildsLazyRuntime(t *testing.T) {
	var privateKey wgnt.Key
	privateKey[0] = 1
	dataPlane, err := NewWindowsDataPlane(privateKey, "IPv6Mesh", 51820)
	if err != nil {
		t.Fatal(err)
	}
	if dataPlane.WireGuard == nil || dataPlane.Routes == nil || dataPlane.Applier == nil || dataPlane.Endpoints == nil {
		t.Fatalf("incomplete Windows data plane: %#v", dataPlane)
	}
	if err := dataPlane.Clear(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestEndpointHeartbeatReportsImmediatelyAndStopsOnContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	source := &fakeEndpointSource{cancel: cancel}
	reporter := &fakeEndpointReporter{}
	runEndpointHeartbeat(ctx, source, reporter, 51820)
	if source.calls != 1 || reporter.calls != 1 {
		t.Fatalf("heartbeat calls = source %d reporter %d", source.calls, reporter.calls)
	}
}

type fakeEndpointSource struct {
	calls  int
	cancel context.CancelFunc
}

func (source *fakeEndpointSource) Discover(context.Context, uint16) ([]control.EndpointCandidate, error) {
	source.calls++
	source.cancel()
	return nil, nil
}

type fakeEndpointReporter struct{ calls int }

func (reporter *fakeEndpointReporter) Heartbeat(context.Context, []control.EndpointCandidate) error {
	reporter.calls++
	return nil
}
