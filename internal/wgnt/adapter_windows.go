//go:build windows

package wgnt

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	interfaceHasPublicKey  = 1 << 0
	interfaceHasPrivateKey = 1 << 1
	interfaceHasListenPort = 1 << 2
	interfaceReplacePeers  = 1 << 3
	peerHasPublicKey       = 1 << 0
	peerHasPersistent      = 1 << 2
	peerHasEndpoint        = 1 << 3
	peerReplaceAllowedIPs  = 1 << 5
	maxConfigurationBytes  = 16 << 20
)

// These layouts mirror the official wireguard.h ABI. The padding fields are
// intentional and are kept in sync with the official WireGuard for Windows
// driver wrapper.
type wireguardInterface struct {
	Flags      uint32
	ListenPort uint16
	PrivateKey [KeySize]byte
	PublicKey  [KeySize]byte
	PeersCount uint32
	_          [4]byte
}

type wireguardAllowedIP struct {
	Address       [16]byte
	AddressFamily uint16
	Cidr          uint8
	Flags         uint32
}

type wireguardPeer struct {
	Flags               uint32
	_                   uint32
	PublicKey           [KeySize]byte
	PresharedKey        [KeySize]byte
	PersistentKeepalive uint16
	_2                  uint16
	Endpoint            rawSockaddrInet
	TxBytes             uint64
	RxBytes             uint64
	LastHandshake       uint64
	AllowedIPsCount     uint32
	_3                  [4]byte
}

type rawSockaddrInet struct {
	Family uint16
	Data   [26]byte
}

type nativeAdapter struct {
	handle uintptr
	name   string
	luid   uint64
}

type wireguardAPI struct {
	dll              *windows.LazyDLL
	createAdapter    *windows.LazyProc
	openAdapter      *windows.LazyProc
	closeAdapter     *windows.LazyProc
	getAdapterLUID   *windows.LazyProc
	setConfiguration *windows.LazyProc
	getConfiguration *windows.LazyProc
	setAdapterState  *windows.LazyProc
	getAdapterState  *windows.LazyProc
}

type Client struct {
	api      wireguardAPI
	mu       sync.Mutex
	next     Handle
	adapters map[Handle]nativeAdapter
	byName   map[string]Handle
}

func New() *Client { return NewWithDLL(defaultDLLPath()) }

func NewWithDLL(path string) *Client {
	if strings.TrimSpace(path) == "" {
		path = defaultDLLPath()
	}
	dll := windows.NewLazyDLL(path)
	return &Client{
		api: wireguardAPI{
			dll:              dll,
			createAdapter:    dll.NewProc("WireGuardCreateAdapter"),
			openAdapter:      dll.NewProc("WireGuardOpenAdapter"),
			closeAdapter:     dll.NewProc("WireGuardCloseAdapter"),
			getAdapterLUID:   dll.NewProc("WireGuardGetAdapterLUID"),
			setConfiguration: dll.NewProc("WireGuardSetConfiguration"),
			getConfiguration: dll.NewProc("WireGuardGetConfiguration"),
			setAdapterState:  dll.NewProc("WireGuardSetAdapterState"),
			getAdapterState:  dll.NewProc("WireGuardGetAdapterState"),
		},
		next:     1,
		adapters: make(map[Handle]nativeAdapter),
		byName:   make(map[string]Handle),
	}
}

func defaultDLLPath() string {
	if executable, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(executable), "wireguard.dll")
	}
	return "wireguard.dll"
}

func (client *Client) Ensure(name string) (Handle, error) {
	if client == nil {
		return 0, ErrInvalidHandle
	}
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 255 {
		return 0, fmt.Errorf("%w: invalid adapter name", ErrInvalidConfig)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if identifier, ok := client.byName[name]; ok {
		if native, exists := client.adapters[identifier]; exists && native.handle != 0 {
			return identifier, nil
		}
		delete(client.byName, name)
	}
	if err := client.api.findAll(); err != nil {
		return 0, err
	}
	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, fmt.Errorf("invalid adapter name: %w", err)
	}
	tunnelTypePtr, err := windows.UTF16PtrFromString(TunnelType)
	if err != nil {
		return 0, err
	}
	nativeHandle, _, openErr := client.api.openAdapter.Call(uintptr(unsafe.Pointer(namePtr)))
	if nativeHandle == 0 {
		nativeHandle, _, openErr = client.api.createAdapter.Call(
			uintptr(unsafe.Pointer(namePtr)),
			uintptr(unsafe.Pointer(tunnelTypePtr)),
			0,
		)
	}
	if nativeHandle == 0 {
		return 0, fmt.Errorf("open or create WireGuard adapter %q: %w", name, callError(openErr))
	}
	var luid uint64
	_, _, luidErr := client.api.getAdapterLUID.Call(nativeHandle, uintptr(unsafe.Pointer(&luid)))
	if luid == 0 {
		_, _, _ = client.api.closeAdapter.Call(nativeHandle)
		return 0, fmt.Errorf("get WireGuard adapter LUID: %w", callError(luidErr))
	}
	identifier := client.next
	client.next++
	client.adapters[identifier] = nativeAdapter{handle: nativeHandle, name: name, luid: luid}
	client.byName[name] = identifier
	return identifier, nil
}

func (client *Client) Configure(ctx context.Context, handle Handle, configuration Configuration) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if err := configuration.Validate(); err != nil {
		return err
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	native, err := client.nativeLocked(handle)
	if err != nil {
		return err
	}
	if err := client.api.findAll(); err != nil {
		return err
	}
	buffer, err := buildConfiguration(configuration)
	if err != nil {
		return err
	}
	defer clearBytes(buffer)
	result, _, callErr := client.api.setConfiguration.Call(native.handle, uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
	runtime.KeepAlive(buffer)
	if result == 0 {
		return fmt.Errorf("set WireGuard adapter configuration: %w", callError(callErr))
	}
	return nil
}

func (client *Client) SetUp(ctx context.Context, handle Handle) error {
	return client.setState(ctx, handle, 1)
}

func (client *Client) SetDown(ctx context.Context, handle Handle) error {
	return client.setState(ctx, handle, 0)
}

func (client *Client) setState(ctx context.Context, handle Handle, state uintptr) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	native, err := client.nativeLocked(handle)
	if err != nil {
		return err
	}
	if err := client.api.findAll(); err != nil {
		return err
	}
	result, _, callErr := client.api.setAdapterState.Call(native.handle, state)
	if result == 0 {
		return fmt.Errorf("set WireGuard adapter state: %w", callError(callErr))
	}
	return nil
}

func (client *Client) Delete(ctx context.Context, handle Handle) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	native, err := client.nativeLocked(handle)
	if err != nil {
		return err
	}
	if err := client.api.findAll(); err != nil {
		return err
	}
	_, _, callErr := client.api.closeAdapter.Call(native.handle)
	if callErr != nil && !errors.Is(callErr, syscall.Errno(0)) {
		return fmt.Errorf("close WireGuard adapter: %w", callErr)
	}
	delete(client.adapters, handle)
	delete(client.byName, native.name)
	return nil
}

func (client *Client) Status(ctx context.Context, handle Handle) (Status, error) {
	if err := checkContext(ctx); err != nil {
		return Status{}, err
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	native, err := client.nativeLocked(handle)
	if err != nil {
		return Status{}, err
	}
	if err := client.api.findAll(); err != nil {
		return Status{}, err
	}
	var adapterState uint32
	result, _, callErr := client.api.getAdapterState.Call(native.handle, uintptr(unsafe.Pointer(&adapterState)))
	if result == 0 {
		return Status{}, fmt.Errorf("get WireGuard adapter state: %w", callError(callErr))
	}
	configuration, err := client.getConfigurationLocked(native.handle)
	if err != nil {
		return Status{}, err
	}
	return parseStatus(native, adapterState != 0, configuration)
}

func (client *Client) nativeLocked(handle Handle) (nativeAdapter, error) {
	if !handle.Valid() {
		return nativeAdapter{}, ErrInvalidHandle
	}
	native, ok := client.adapters[handle]
	if !ok || native.handle == 0 {
		return nativeAdapter{}, ErrInvalidHandle
	}
	return native, nil
}

func (api *wireguardAPI) findAll() error {
	procedures := []*windows.LazyProc{
		api.createAdapter, api.openAdapter, api.closeAdapter, api.getAdapterLUID,
		api.setConfiguration, api.getConfiguration, api.setAdapterState, api.getAdapterState,
	}
	for _, procedure := range procedures {
		if err := procedure.Find(); err != nil {
			return fmt.Errorf("load WireGuardNT API: %w", err)
		}
	}
	return nil
}

func (client *Client) getConfigurationLocked(nativeHandle uintptr) ([]byte, error) {
	size := uint32(1024)
	for {
		if size == 0 || size > maxConfigurationBytes {
			return nil, fmt.Errorf("WireGuard configuration is too large")
		}
		buffer := make([]byte, size)
		result, _, callErr := client.api.getConfiguration.Call(nativeHandle, uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&size)))
		if result != 0 {
			if size > uint32(len(buffer)) {
				return nil, errors.New("WireGuard returned an invalid configuration size")
			}
			return buffer[:size], nil
		}
		if !errors.Is(callErr, windows.ERROR_MORE_DATA) {
			return nil, fmt.Errorf("get WireGuard adapter configuration: %w", callError(callErr))
		}
		if size <= uint32(len(buffer)) {
			size = uint32(len(buffer)) * 2
		}
	}
}

func buildConfiguration(configuration Configuration) ([]byte, error) {
	var iface wireguardInterface
	iface.Flags = interfaceHasPrivateKey | interfaceHasListenPort
	if configuration.ReplacePeers {
		iface.Flags |= interfaceReplacePeers
	}
	iface.ListenPort = configuration.ListenPort
	iface.PrivateKey = configuration.PrivateKey
	iface.PeersCount = uint32(len(configuration.Peers))
	buffer := make([]byte, 0, int(unsafe.Sizeof(iface))+len(configuration.Peers)*int(unsafe.Sizeof(wireguardPeer{})))
	buffer = appendUnsafe(buffer, unsafe.Pointer(&iface), unsafe.Sizeof(iface))
	for index, peer := range configuration.Peers {
		if len(peer.AllowedIPs) > int(^uint32(0)) {
			return nil, fmt.Errorf("%w: peer %d has too many allowed IPs", ErrInvalidConfig, index)
		}
		var nativePeer wireguardPeer
		nativePeer.Flags = peerHasPublicKey | peerReplaceAllowedIPs
		nativePeer.PublicKey = peer.PublicKey
		if peer.PersistentKeepalive > 0 {
			nativePeer.Flags |= peerHasPersistent
			nativePeer.PersistentKeepalive = uint16(peer.PersistentKeepalive / time.Second)
		}
		if peer.Endpoint.IsValid() {
			nativePeer.Flags |= peerHasEndpoint
			if err := setEndpoint(&nativePeer.Endpoint, peer.Endpoint); err != nil {
				return nil, fmt.Errorf("%w: peer %d endpoint: %v", ErrInvalidConfig, index, err)
			}
		}
		nativePeer.AllowedIPsCount = uint32(len(peer.AllowedIPs))
		buffer = appendUnsafe(buffer, unsafe.Pointer(&nativePeer), unsafe.Sizeof(nativePeer))
		for _, allowed := range peer.AllowedIPs {
			nativeAllowed, err := makeAllowedIP(allowed)
			if err != nil {
				return nil, fmt.Errorf("%w: peer %d allowed IP: %v", ErrInvalidConfig, index, err)
			}
			buffer = appendUnsafe(buffer, unsafe.Pointer(&nativeAllowed), unsafe.Sizeof(nativeAllowed))
		}
	}
	return buffer, nil
}

func makeAllowedIP(prefix netip.Prefix) (wireguardAllowedIP, error) {
	var allowed wireguardAllowedIP
	if !prefix.IsValid() {
		return allowed, ErrInvalidConfig
	}
	address := prefix.Addr()
	if address.Is4() {
		address4 := address.As4()
		copy(allowed.Address[:4], address4[:])
		allowed.AddressFamily = windows.AF_INET
	} else if address.Is6() {
		address16 := address.As16()
		copy(allowed.Address[:], address16[:])
		allowed.AddressFamily = windows.AF_INET6
	} else {
		return allowed, ErrInvalidConfig
	}
	allowed.Cidr = uint8(prefix.Bits())
	return allowed, nil
}

func setEndpoint(endpoint *rawSockaddrInet, address netip.AddrPort) error {
	*endpoint = rawSockaddrInet{}
	port := htons(address.Port())
	if address.Addr().Is4() {
		raw := (*windows.RawSockaddrInet4)(unsafe.Pointer(endpoint))
		raw.Family = windows.AF_INET
		raw.Port = port
		address4 := address.Addr().As4()
		copy(raw.Addr[:], address4[:])
		return nil
	}
	if address.Addr().Is6() {
		raw := (*windows.RawSockaddrInet6)(unsafe.Pointer(endpoint))
		raw.Family = windows.AF_INET6
		raw.Port = port
		address16 := address.Addr().As16()
		copy(raw.Addr[:], address16[:])
		if zone := address.Addr().Zone(); zone != "" {
			value, err := strconv.ParseUint(zone, 10, 32)
			if err != nil {
				return fmt.Errorf("IPv6 endpoint zone must be a numeric interface index")
			}
			raw.Scope_id = uint32(value)
		}
		return nil
	}
	return ErrInvalidConfig
}

func parseStatus(native nativeAdapter, up bool, buffer []byte) (Status, error) {
	if len(buffer) < int(unsafe.Sizeof(wireguardInterface{})) {
		return Status{}, errors.New("short WireGuard configuration")
	}
	iface := (*wireguardInterface)(unsafe.Pointer(&buffer[0]))
	status := Status{Name: native.name, LUID: native.luid, Up: up, ListenPort: iface.ListenPort}
	if iface.Flags&interfaceHasPublicKey != 0 {
		status.PublicKey = Key(iface.PublicKey)
	}
	offset := unsafe.Sizeof(wireguardInterface{})
	peerSize := unsafe.Sizeof(wireguardPeer{})
	allowedSize := unsafe.Sizeof(wireguardAllowedIP{})
	for peerIndex := uint32(0); peerIndex < iface.PeersCount; peerIndex++ {
		if offset+peerSize > uintptr(len(buffer)) {
			return Status{}, errors.New("truncated WireGuard peer configuration")
		}
		peer := (*wireguardPeer)(unsafe.Pointer(uintptr(unsafe.Pointer(&buffer[0])) + offset))
		statusPeer := PeerStatus{PublicKey: Key(peer.PublicKey), TxBytes: peer.TxBytes, RxBytes: peer.RxBytes, LastHandshake: wireguardTime(peer.LastHandshake)}
		if peer.Flags&peerHasEndpoint != 0 {
			statusPeer.Endpoint = parseEndpoint(peer.Endpoint)
		}
		offset += peerSize
		if uint64(peer.AllowedIPsCount) > uint64((len(buffer)-int(offset))/int(allowedSize)) {
			return Status{}, errors.New("truncated WireGuard allowed IP configuration")
		}
		for allowedIndex := uint32(0); allowedIndex < peer.AllowedIPsCount; allowedIndex++ {
			allowed := (*wireguardAllowedIP)(unsafe.Pointer(uintptr(unsafe.Pointer(&buffer[0])) + offset))
			if prefix, ok := parseAllowedIP(*allowed); ok {
				statusPeer.AllowedIPs = append(statusPeer.AllowedIPs, prefix)
			}
			offset += allowedSize
		}
		status.Peers = append(status.Peers, statusPeer)
	}
	return status, nil
}

func parseEndpoint(endpoint rawSockaddrInet) netip.AddrPort {
	if endpoint.Family == windows.AF_INET {
		raw := (*windows.RawSockaddrInet4)(unsafe.Pointer(&endpoint))
		return netip.AddrPortFrom(netip.AddrFrom4(raw.Addr), ntohs(raw.Port))
	}
	if endpoint.Family == windows.AF_INET6 {
		raw := (*windows.RawSockaddrInet6)(unsafe.Pointer(&endpoint))
		address := netip.AddrFrom16(raw.Addr)
		if raw.Scope_id != 0 {
			address = address.WithZone(strconv.FormatUint(uint64(raw.Scope_id), 10))
		}
		return netip.AddrPortFrom(address, ntohs(raw.Port))
	}
	return netip.AddrPort{}
}

func parseAllowedIP(allowed wireguardAllowedIP) (netip.Prefix, bool) {
	switch allowed.AddressFamily {
	case windows.AF_INET:
		var address [4]byte
		copy(address[:], allowed.Address[:4])
		return netip.PrefixFrom(netip.AddrFrom4(address), int(allowed.Cidr)), true
	case windows.AF_INET6:
		var address [16]byte
		copy(address[:], allowed.Address[:])
		return netip.PrefixFrom(netip.AddrFrom16(address), int(allowed.Cidr)), true
	default:
		return netip.Prefix{}, false
	}
}

func appendUnsafe(buffer []byte, pointer unsafe.Pointer, size uintptr) []byte {
	return append(buffer, unsafe.Slice((*byte)(pointer), size)...)
}

func clearBytes(buffer []byte) {
	for index := range buffer {
		buffer[index] = 0
	}
}

func wireguardTime(value uint64) time.Time {
	if value == 0 {
		return time.Time{}
	}
	const windowsToUnixSeconds = 11644473600
	seconds := int64(value / 10_000_000)
	nanoseconds := int64(value%10_000_000) * 100
	return time.Unix(seconds-windowsToUnixSeconds, nanoseconds)
}

func htons(value uint16) uint16 { return value<<8 | value>>8 }
func ntohs(value uint16) uint16 { return htons(value) }

func callError(err error) error {
	if err == nil || errors.Is(err, syscall.Errno(0)) {
		if last := windows.GetLastError(); last != nil && !errors.Is(last, syscall.Errno(0)) {
			return last
		}
		return syscall.EINVAL
	}
	return err
}
