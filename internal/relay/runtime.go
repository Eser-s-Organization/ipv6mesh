package relay

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sync"

	"github.com/Eser-s-Organization/ipv6mesh/internal/wgnt"
)

var ErrInvalidRuntime = errors.New("relay runtime dependencies are missing")

type WireGuard interface {
	Apply(context.Context, string, wgnt.Configuration) error
	Down(context.Context, string) error
}

type Routes interface {
	Apply(context.Context, string, netip.Addr, []netip.Prefix) error
	Clear(context.Context, string) error
}

type Runtime struct {
	mu            sync.Mutex
	wireGuard     WireGuard
	routes        Routes
	active        bool
	interfaceName string
}

func NewRuntime(wireGuard WireGuard, routes Routes) (*Runtime, error) {
	if wireGuard == nil || routes == nil {
		return nil, ErrInvalidRuntime
	}
	return &Runtime{wireGuard: wireGuard, routes: routes}, nil
}

func (runtime *Runtime) Apply(ctx context.Context, config Config) error {
	if runtime == nil || runtime.wireGuard == nil || runtime.routes == nil {
		return ErrInvalidRuntime
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	wireGuardConfig, err := config.WireGuardConfiguration()
	if err != nil {
		return err
	}
	if err := runtime.wireGuard.Apply(ctx, config.InterfaceName, wireGuardConfig); err != nil {
		return fmt.Errorf("apply relay WireGuard configuration: %w", err)
	}
	allowed := make([]netip.Prefix, len(config.Peers))
	for index, peer := range config.Peers {
		allowed[index] = netip.PrefixFrom(peer.VirtualIPv4, 32)
	}
	if err := runtime.routes.Apply(ctx, config.InterfaceName, config.VirtualIPv4, allowed); err != nil {
		rollbackErr := runtime.wireGuard.Down(ctx, config.InterfaceName)
		return errors.Join(fmt.Errorf("apply relay overlay routes: %w", err), rollbackErr)
	}
	runtime.active = true
	runtime.interfaceName = config.InterfaceName
	return nil
}

func (runtime *Runtime) Clear(ctx context.Context) error {
	if runtime == nil || runtime.wireGuard == nil || runtime.routes == nil {
		return ErrInvalidRuntime
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if !runtime.active {
		return nil
	}
	var joined error
	if err := runtime.routes.Clear(ctx, runtime.interfaceName); err != nil {
		joined = errors.Join(joined, err)
	}
	if err := runtime.wireGuard.Down(ctx, runtime.interfaceName); err != nil {
		joined = errors.Join(joined, err)
	}
	if joined == nil {
		runtime.active = false
		runtime.interfaceName = ""
	}
	return joined
}
