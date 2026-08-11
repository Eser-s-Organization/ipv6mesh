// Package relay defines the narrowly scoped membership configuration accepted
// by a trusted Linux WireGuard relay.
package relay

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"strings"

	"github.com/Eser-s-Organization/ipv6mesh/internal/wgnt"
)

var (
	ErrInvalidConfig    = errors.New("invalid relay configuration")
	ErrDuplicatePeer    = errors.New("relay peer public key is duplicated")
	ErrDuplicateAddress = errors.New("relay peer virtual address is duplicated")
)

const maxConfigBytes = 1 << 20
const maxPeers = 4096

type Config struct {
	NetworkID     string
	InterfaceName string
	ListenPort    uint16
	VirtualIPv4   netip.Addr
	PrivateKey    wgnt.Key
	Peers         []Peer
}

type Peer struct {
	NodeID      string
	PublicKey   string
	VirtualIPv4 netip.Addr
	Endpoint    netip.AddrPort
}

func (config Config) Validate() error {
	if strings.TrimSpace(config.NetworkID) == "" || strings.TrimSpace(config.InterfaceName) == "" || config.ListenPort == 0 || config.PrivateKey.IsZero() {
		return fmt.Errorf("%w: network, interface, listen port, and private key are required", ErrInvalidConfig)
	}
	if !validInterfaceName(config.InterfaceName) {
		return fmt.Errorf("%w: interface name is invalid", ErrInvalidConfig)
	}
	if !config.VirtualIPv4.IsValid() || !config.VirtualIPv4.Is4() || config.VirtualIPv4.IsUnspecified() || config.VirtualIPv4.IsMulticast() {
		return fmt.Errorf("%w: relay virtual address must be a usable IPv4 address", ErrInvalidConfig)
	}
	if len(config.Peers) > maxPeers {
		return fmt.Errorf("%w: peer count exceeds %d", ErrInvalidConfig, maxPeers)
	}
	seenKeys := make(map[wgnt.Key]struct{}, len(config.Peers))
	seenAddresses := make(map[netip.Addr]struct{}, len(config.Peers))
	for index, peer := range config.Peers {
		if strings.TrimSpace(peer.NodeID) == "" {
			return fmt.Errorf("%w: peer %d node ID is required", ErrInvalidConfig, index)
		}
		publicKey, err := wgnt.ParseKey(peer.PublicKey)
		if err != nil || publicKey.IsZero() {
			return fmt.Errorf("%w: peer %s public key is invalid", ErrInvalidConfig, peer.NodeID)
		}
		if _, exists := seenKeys[publicKey]; exists {
			return ErrDuplicatePeer
		}
		if !peer.VirtualIPv4.IsValid() || !peer.VirtualIPv4.Is4() || peer.VirtualIPv4.IsUnspecified() || peer.VirtualIPv4.IsMulticast() {
			return fmt.Errorf("%w: peer %s virtual address must be a usable IPv4 address", ErrInvalidConfig, peer.NodeID)
		}
		if peer.VirtualIPv4 == config.VirtualIPv4 {
			return ErrDuplicateAddress
		}
		if _, exists := seenAddresses[peer.VirtualIPv4]; exists {
			return ErrDuplicateAddress
		}
		if peer.Endpoint.IsValid() {
			if peer.Endpoint.Port() == 0 || !peer.Endpoint.Addr().IsGlobalUnicast() || peer.Endpoint.Addr().IsLinkLocalUnicast() {
				return fmt.Errorf("%w: peer %s endpoint is not a usable global address", ErrInvalidConfig, peer.NodeID)
			}
		}
		seenKeys[publicKey] = struct{}{}
		seenAddresses[peer.VirtualIPv4] = struct{}{}
	}
	return nil
}

func (config Config) WireGuardConfiguration() (wgnt.Configuration, error) {
	if err := config.Validate(); err != nil {
		return wgnt.Configuration{}, err
	}
	result := wgnt.Configuration{PrivateKey: config.PrivateKey, ListenPort: config.ListenPort, ReplacePeers: true, Peers: make([]wgnt.Peer, len(config.Peers))}
	for index, peer := range config.Peers {
		publicKey, _ := wgnt.ParseKey(peer.PublicKey)
		result.Peers[index] = wgnt.Peer{PublicKey: publicKey, Endpoint: peer.Endpoint, AllowedIPs: []netip.Prefix{netip.PrefixFrom(peer.VirtualIPv4, 32)}}
	}
	return result, result.Validate()
}

func ParseConfig(data []byte) (Config, error) {
	if len(data) == 0 || len(data) > maxConfigBytes {
		return Config{}, fmt.Errorf("%w: JSON body size is invalid", ErrInvalidConfig)
	}
	var wire configWire
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return Config{}, fmt.Errorf("%w: decode JSON: %v", ErrInvalidConfig, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Config{}, fmt.Errorf("%w: trailing JSON is not allowed", ErrInvalidConfig)
	}
	privateKey, err := wgnt.ParseKey(wire.PrivateKey)
	if err != nil {
		return Config{}, fmt.Errorf("%w: private key is invalid", ErrInvalidConfig)
	}
	config := Config{NetworkID: wire.NetworkID, InterfaceName: wire.InterfaceName, ListenPort: wire.ListenPort, PrivateKey: privateKey, Peers: make([]Peer, len(wire.Peers))}
	config.VirtualIPv4, err = netip.ParseAddr(wire.VirtualIPv4)
	if err != nil {
		return Config{}, fmt.Errorf("%w: relay virtual address is invalid", ErrInvalidConfig)
	}
	for index, peer := range wire.Peers {
		var endpoint netip.AddrPort
		if strings.TrimSpace(peer.Endpoint) != "" {
			endpoint, err = netip.ParseAddrPort(peer.Endpoint)
			if err != nil {
				return Config{}, fmt.Errorf("%w: peer %d endpoint is invalid", ErrInvalidConfig, index)
			}
		}
		address, err := netip.ParseAddr(peer.VirtualIPv4)
		if err != nil {
			return Config{}, fmt.Errorf("%w: peer %d virtual address is invalid", ErrInvalidConfig, index)
		}
		config.Peers[index] = Peer{NodeID: peer.NodeID, PublicKey: peer.PublicKey, VirtualIPv4: address, Endpoint: endpoint}
	}
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

type configWire struct {
	NetworkID     string     `json:"network_id"`
	InterfaceName string     `json:"interface"`
	ListenPort    uint16     `json:"listen_port"`
	VirtualIPv4   string     `json:"virtual_ipv4"`
	PrivateKey    string     `json:"private_key"`
	Peers         []peerWire `json:"peers"`
}

type peerWire struct {
	NodeID      string `json:"node_id"`
	PublicKey   string `json:"public_key"`
	VirtualIPv4 string `json:"virtual_ipv4"`
	Endpoint    string `json:"endpoint,omitempty"`
}
