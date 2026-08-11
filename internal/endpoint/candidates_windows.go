//go:build windows

package endpoint

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type unicastAddressRow struct {
	Address            rawSockaddrInet
	InterfaceLUID      uint64
	InterfaceIndex     uint32
	PrefixOrigin       uint32
	SuffixOrigin       uint32
	ValidLifetime      uint32
	PreferredLifetime  uint32
	OnLinkPrefixLength uint8
	SkipAsSource       uint8
	DadState           uint32
	ScopeID            uint32
	CreationTimeStamp  int64
}

type rawSockaddrInet struct {
	Family uint16
	Data   [26]byte
}

type unicastAddressTable struct {
	NumEntries uint32
	Table      [1]unicastAddressRow
}

type adapterInfo struct {
	name    string
	ifType  uint32
	virtual bool
}

type WindowsEnumerator struct {
	getTable  *windows.LazyProc
	freeTable *windows.LazyProc
}

func NewWindowsEnumerator() *WindowsEnumerator {
	dll := windows.NewLazySystemDLL("iphlpapi.dll")
	return &WindowsEnumerator{getTable: dll.NewProc("GetUnicastIpAddressTable"), freeTable: dll.NewProc("FreeMibTable")}
}

func (enumerator *WindowsEnumerator) Enumerate(ctx context.Context, port uint16) ([]Candidate, error) {
	if enumerator == nil || enumerator.getTable == nil || enumerator.freeTable == nil {
		return nil, ErrUnsupportedPlatform
	}
	if ctx == nil {
		return nil, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if port == 0 {
		return nil, fmt.Errorf("endpoint port must be non-zero")
	}
	adapters, err := enumerateAdapters()
	if err != nil {
		return nil, err
	}
	if err := enumerator.getTable.Find(); err != nil {
		return nil, fmt.Errorf("load GetUnicastIpAddressTable: %w", err)
	}
	if err := enumerator.freeTable.Find(); err != nil {
		return nil, fmt.Errorf("load FreeMibTable: %w", err)
	}
	var table *unicastAddressTable
	result, _, _ := enumerator.getTable.Call(uintptr(windows.AF_INET6), uintptr(unsafe.Pointer(&table)))
	if result != 0 {
		return nil, fmt.Errorf("enumerate IPv6 unicast addresses: %w", syscall.Errno(result))
	}
	if table == nil {
		return nil, nil
	}
	defer func() { _, _, _ = enumerator.freeTable.Call(uintptr(unsafe.Pointer(table))) }()
	now := time.Now().UTC()
	rows := unsafe.Slice(&table.Table[0], int(table.NumEntries))
	candidates := make([]Candidate, 0, len(rows))
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		address := parseIPv6(row.Address)
		if !address.IsValid() {
			continue
		}
		info := adapters[row.InterfaceLUID]
		name := info.name
		if name == "" {
			name = "luid-" + strconv.FormatUint(row.InterfaceLUID, 10)
		}
		candidates = append(candidates, Candidate{
			Address:        address,
			Port:           port,
			Interface:      name,
			InterfaceLUID:  row.InterfaceLUID,
			Priority:       interfacePriority(info),
			SkipAsSource:   row.SkipAsSource != 0,
			Loopback:       info.ifType == 24,
			LinkLocal:      address.IsLinkLocalUnicast(),
			Virtual:        info.virtual,
			ValidUntil:     lifetime(now, row.ValidLifetime),
			PreferredUntil: lifetime(now, row.PreferredLifetime),
		})
	}
	return candidates, nil
}

func enumerateAdapters() (map[uint64]adapterInfo, error) {
	size := uint32(15 * 1024)
	var buffer []byte
	for {
		buffer = make([]byte, size)
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC, windows.GAA_FLAG_INCLUDE_ALL_INTERFACES|windows.GAA_FLAG_SKIP_ANYCAST|windows.GAA_FLAG_SKIP_MULTICAST|windows.GAA_FLAG_SKIP_DNS_SERVER, 0, (*windows.IpAdapterAddresses)(unsafe.Pointer(&buffer[0])), &size)
		if err == nil {
			break
		}
		if !errors.Is(err, windows.ERROR_BUFFER_OVERFLOW) || size <= uint32(len(buffer)) {
			return nil, fmt.Errorf("enumerate Windows adapters: %w", err)
		}
	}
	result := make(map[uint64]adapterInfo)
	for adapter := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buffer[0])); adapter != nil; adapter = adapter.Next {
		name := ""
		if adapter.FriendlyName != nil {
			name = windows.UTF16PtrToString(adapter.FriendlyName)
		}
		result[adapter.Luid] = adapterInfo{name: name, ifType: adapter.IfType, virtual: virtualInterface(name, adapter.IfType)}
	}
	return result, nil
}

func parseIPv6(raw rawSockaddrInet) netip.Addr {
	if raw.Family != windows.AF_INET6 {
		return netip.Addr{}
	}
	value := (*windows.RawSockaddrInet6)(unsafe.Pointer(&raw))
	address := netip.AddrFrom16(value.Addr)
	if value.Scope_id != 0 {
		address = address.WithZone(strconv.FormatUint(uint64(value.Scope_id), 10))
	}
	return address
}

func lifetime(now time.Time, seconds uint32) time.Time {
	if seconds == ^uint32(0) {
		return time.Time{}
	}
	return now.Add(time.Duration(seconds) * time.Second)
}

func interfacePriority(info adapterInfo) int {
	if info.virtual {
		return 1000
	}
	return 10
}

func virtualInterface(name string, ifType uint32) bool {
	if ifType == 24 || ifType == 131 {
		return true
	}
	name = strings.ToLower(name)
	for _, marker := range []string{"wireguard", "wintun", "tap", "vpn", "loopback"} {
		if strings.Contains(name, marker) {
			return true
		}
	}
	return false
}
