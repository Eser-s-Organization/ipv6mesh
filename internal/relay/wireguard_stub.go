//go:build !linux

package relay

import (
	"context"

	"github.com/Eser-s-Organization/ipv6mesh/internal/wgnt"
)

func NewWireGuardManager() *WireGuardManager { return NewWireGuardManagerWithRunner(nil, "") }

func (*WireGuardManager) Apply(context.Context, string, wgnt.Configuration) error {
	return ErrUnsupportedPlatform
}
func (*WireGuardManager) Down(context.Context, string) error { return ErrUnsupportedPlatform }
