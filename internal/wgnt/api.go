// Package wgnt contains the platform-neutral WireGuardNT adapter contract.
// The Windows implementation is a thin ABI wrapper around the official
// wireguard.dll API; it does not implement cryptography or tunnel protocol
// logic itself.
package wgnt

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"time"
)

const (
	KeySize    = 32
	TunnelType = "WireGuard"
)

var (
	ErrUnsupportedPlatform = errors.New("WireGuardNT is unsupported on this platform")
	ErrInvalidConfig       = errors.New("invalid WireGuard configuration")
	ErrInvalidHandle       = errors.New("invalid WireGuard adapter handle")
)

// Key is a WireGuard key in its raw 32-byte form. PrivateKey is accepted only
// by Configuration and is never included in Status or any IPC response.
type Key [KeySize]byte

func (key Key) IsZero() bool {
	var zero Key
	return key == zero
}

func (key Key) Base64() string {
	return base64.StdEncoding.EncodeToString(key[:])
}

func ParseKey(value string) (Key, error) {
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(decoded) != KeySize {
		return Key{}, fmt.Errorf("%w: key must be base64-encoded 32 bytes", ErrInvalidConfig)
	}
	var key Key
	copy(key[:], decoded)
	return key, nil
}

// Handle is an opaque adapter handle owned by an Adapter implementation.
// Callers must treat zero as invalid and must not persist handles across a
// process restart.
type Handle uint64

func (handle Handle) Valid() bool { return handle != 0 }

type Adapter interface {
	Ensure(name string) (Handle, error)
	Configure(context.Context, Handle, Configuration) error
	SetUp(context.Context, Handle) error
	SetDown(context.Context, Handle) error
	Delete(context.Context, Handle) error
	Status(context.Context, Handle) (Status, error)
}

type Configuration struct {
	PrivateKey   Key
	ListenPort   uint16
	Peers        []Peer
	ReplacePeers bool
}

type Peer struct {
	PublicKey           Key
	Endpoint            netip.AddrPort
	PersistentKeepalive time.Duration
	AllowedIPs          []netip.Prefix
}

type Status struct {
	Name       string
	LUID       uint64
	Up         bool
	PublicKey  Key
	ListenPort uint16
	Peers      []PeerStatus
}

type PeerStatus struct {
	PublicKey     Key
	Endpoint      netip.AddrPort
	AllowedIPs    []netip.Prefix
	TxBytes       uint64
	RxBytes       uint64
	LastHandshake time.Time
}

func (configuration Configuration) Validate() error {
	if configuration.PrivateKey.IsZero() {
		return fmt.Errorf("%w: private key is empty", ErrInvalidConfig)
	}
	seen := make(map[Key]struct{}, len(configuration.Peers))
	for index, peer := range configuration.Peers {
		if peer.PublicKey.IsZero() {
			return fmt.Errorf("%w: peer %d has an empty public key", ErrInvalidConfig, index)
		}
		if _, exists := seen[peer.PublicKey]; exists {
			return fmt.Errorf("%w: duplicate peer public key", ErrInvalidConfig)
		}
		seen[peer.PublicKey] = struct{}{}
		if peer.Endpoint.IsValid() && peer.Endpoint.Port() == 0 {
			return fmt.Errorf("%w: peer %d endpoint has no port", ErrInvalidConfig, index)
		}
		if peer.PersistentKeepalive < 0 || peer.PersistentKeepalive > 65535*time.Second || peer.PersistentKeepalive%time.Second != 0 {
			return fmt.Errorf("%w: peer %d keepalive is out of range", ErrInvalidConfig, index)
		}
		for _, allowed := range peer.AllowedIPs {
			if !allowed.IsValid() {
				return fmt.Errorf("%w: peer %d has an invalid allowed IP", ErrInvalidConfig, index)
			}
			// v0.1 is a node-to-node overlay. Host routes are deliberate:
			// accepting a default prefix here could take over the host's
			// default route.
			bits := allowed.Bits()
			if (allowed.Addr().Is4() && bits != 32) || (allowed.Addr().Is6() && bits != 128) {
				return fmt.Errorf("%w: peer %d allowed IP must be a host route", ErrInvalidConfig, index)
			}
		}
	}
	return nil
}

func checkContext(ctx context.Context) error {
	if ctx == nil {
		return context.Canceled
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
