//go:build windows

package service

import (
	"context"
	"testing"

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
