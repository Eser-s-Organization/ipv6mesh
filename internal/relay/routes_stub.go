//go:build !linux

package relay

import (
	"context"
	"net/netip"
)

func NewRouteManager() *RouteManager { return NewRouteManagerWithRunner(nil) }

func (*RouteManager) Apply(context.Context, string, netip.Addr, []netip.Prefix) error {
	return ErrUnsupportedPlatform
}
func (*RouteManager) Clear(context.Context, string) error { return ErrUnsupportedPlatform }
