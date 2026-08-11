//go:build linux

package relay

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Eser-s-Organization/ipv6mesh/internal/wgnt"
)

func NewWireGuardManager() *WireGuardManager {
	return NewWireGuardManagerWithRunner(execRunner{}, "")
}

func (manager *WireGuardManager) Apply(ctx context.Context, interfaceName string, configuration wgnt.Configuration) error {
	if manager == nil || manager.runner == nil {
		return ErrUnsupportedPlatform
	}
	if err := configuration.Validate(); err != nil {
		return err
	}
	if !validInterfaceName(interfaceName) {
		return fmt.Errorf("%w: invalid relay interface name", ErrInvalidConfig)
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	if _, err := manager.runner.Run(ctx, "ip", "link", "show", "dev", interfaceName); err != nil {
		if _, createErr := manager.runner.Run(ctx, "ip", "link", "add", "dev", interfaceName, "type", "wireguard"); createErr != nil {
			return fmt.Errorf("create relay WireGuard interface: %w", createErr)
		}
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	keyPath, cleanup, err := manager.writePrivateKey(configuration.PrivateKey)
	if err != nil {
		return err
	}
	defer cleanup()
	desiredKeys := make(map[string]struct{}, len(configuration.Peers))
	for _, peer := range configuration.Peers {
		desiredKeys[peer.PublicKey.Base64()] = struct{}{}
	}
	if existing, showErr := manager.runner.Run(ctx, "wg", "show", interfaceName, "peers"); showErr == nil {
		for _, publicKey := range strings.Fields(string(existing)) {
			if _, keep := desiredKeys[publicKey]; keep {
				continue
			}
			if _, removeErr := manager.runner.Run(ctx, "wg", "set", interfaceName, "peer", publicKey, "remove"); removeErr != nil {
				return fmt.Errorf("remove stale relay WireGuard peer: %w", removeErr)
			}
		}
	}
	args := []string{"set", interfaceName, "private-key", keyPath, "listen-port", strconv.Itoa(int(configuration.ListenPort))}
	for _, peer := range configuration.Peers {
		if len(peer.AllowedIPs) != 1 || !peer.AllowedIPs[0].IsValid() || !peer.AllowedIPs[0].Addr().Is4() || peer.AllowedIPs[0].Bits() != 32 {
			return fmt.Errorf("%w: relay peer must contain one IPv4 /32 AllowedIP", ErrInvalidConfig)
		}
		args = append(args, "peer", peer.PublicKey.Base64(), "allowed-ips", peer.AllowedIPs[0].String())
		if peer.Endpoint.IsValid() {
			args = append(args, "endpoint", peer.Endpoint.String())
		}
	}
	if _, err := manager.runner.Run(ctx, "wg", args...); err != nil {
		return fmt.Errorf("configure relay WireGuard interface: %w", err)
	}
	if _, err := manager.runner.Run(ctx, "ip", "link", "set", "up", "dev", interfaceName); err != nil {
		return fmt.Errorf("bring relay WireGuard interface up: %w", err)
	}
	return nil
}

func (manager *WireGuardManager) Down(ctx context.Context, interfaceName string) error {
	if manager == nil || manager.runner == nil {
		return ErrUnsupportedPlatform
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	if !validInterfaceName(interfaceName) {
		return fmt.Errorf("%w: relay interface name is required", ErrInvalidConfig)
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if _, err := manager.runner.Run(ctx, "ip", "link", "set", "down", "dev", interfaceName); err != nil {
		return fmt.Errorf("bring relay WireGuard interface down: %w", err)
	}
	return nil
}

func (manager *WireGuardManager) writePrivateKey(key wgnt.Key) (string, func(), error) {
	tempDir := manager.tempDir
	if tempDir != "" {
		if err := os.MkdirAll(tempDir, 0o700); err != nil {
			return "", func() {}, err
		}
	}
	file, err := os.CreateTemp(tempDir, ".ipv6mesh-relay-key-*")
	if err != nil {
		return "", func() {}, fmt.Errorf("create temporary relay key file: %w", err)
	}
	path := filepath.Clean(file.Name())
	cleanup := func() { _ = os.Remove(path) }
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		cleanup()
		return "", func() {}, err
	}
	if _, err := file.WriteString(key.Base64() + "\n"); err != nil {
		_ = file.Close()
		cleanup()
		return "", func() {}, err
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return path, cleanup, nil
}
