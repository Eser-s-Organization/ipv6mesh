//go:build windows

package netwin

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type rawSockaddrInet struct {
	Family uint16
	Data   [26]byte
}

type ipAddressPrefix struct {
	Prefix       rawSockaddrInet
	PrefixLength uint8
	_            [2]byte
}

type unicastIPAddressRow struct {
	Address            rawSockaddrInet
	InterfaceLUID      uint64
	InterfaceIndex     uint32
	PrefixOrigin       uint32
	SuffixOrigin       uint32
	ValidLifetime      uint32
	PreferredLifetime  uint32
	OnLinkPrefixLength uint8
	SkipAsSource       bool
	DadState           uint32
	ScopeID            uint32
	CreationTimeStamp  int64
}

type ipForwardRow2 struct {
	InterfaceLUID        uint64
	InterfaceIndex       uint32
	DestinationPrefix    ipAddressPrefix
	NextHop              rawSockaddrInet
	SitePrefixLength     uint8
	ValidLifetime        uint32
	PreferredLifetime    uint32
	Metric               uint32
	Protocol             uint32
	Loopback             bool
	AutoconfigureAddress bool
	Publish              bool
	Immortal             bool
	Age                  uint32
	Origin               uint32
}

type iphelperAPI struct {
	initializeUnicast *windows.LazyProc
	createUnicast     *windows.LazyProc
	deleteUnicast     *windows.LazyProc
	initializeRoute   *windows.LazyProc
	createRoute       *windows.LazyProc
	deleteRoute       *windows.LazyProc
}

type IPHelper struct {
	api iphelperAPI
}

func NewIPHelper() *IPHelper {
	dll := windows.NewLazySystemDLL("iphlpapi.dll")
	return &IPHelper{api: iphelperAPI{
		initializeUnicast: dll.NewProc("InitializeUnicastIpAddressEntry"),
		createUnicast:     dll.NewProc("CreateUnicastIpAddressEntry"),
		deleteUnicast:     dll.NewProc("DeleteUnicastIpAddressEntry"),
		initializeRoute:   dll.NewProc("InitializeIpForwardEntry"),
		createRoute:       dll.NewProc("CreateIpForwardEntry2"),
		deleteRoute:       dll.NewProc("DeleteIpForwardEntry2"),
	}}
}

func (helper *IPHelper) EnsureAddress(ctx context.Context, address Address) (bool, error) {
	if err := contextErr(ctx); err != nil {
		return false, err
	}
	if err := validateAddress(address); err != nil {
		return false, err
	}
	if err := helper.find(helper.api.initializeUnicast, helper.api.createUnicast, helper.api.deleteUnicast); err != nil {
		return false, err
	}
	var row unicastIPAddressRow
	_, _, _ = helper.api.initializeUnicast.Call(uintptr(unsafe.Pointer(&row)))
	row.InterfaceLUID = address.InterfaceLUID
	row.OnLinkPrefixLength = address.PrefixLength
	if err := setRawAddress(&row.Address, address.IP, 0); err != nil {
		return false, err
	}
	if err := callWin32(helper.api.createUnicast, unsafe.Pointer(&row)); err != nil {
		if errors.Is(err, windows.ERROR_OBJECT_ALREADY_EXISTS) || errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			return false, nil
		}
		return false, fmt.Errorf("create overlay IPv4 address: %w", err)
	}
	return true, nil
}

func (helper *IPHelper) RemoveAddress(ctx context.Context, address Address) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if err := validateAddress(address); err != nil {
		return err
	}
	if err := helper.find(helper.api.deleteUnicast); err != nil {
		return err
	}
	var row unicastIPAddressRow
	row.InterfaceLUID = address.InterfaceLUID
	row.OnLinkPrefixLength = address.PrefixLength
	if err := setRawAddress(&row.Address, address.IP, 0); err != nil {
		return err
	}
	if err := callWin32(helper.api.deleteUnicast, unsafe.Pointer(&row)); err != nil && !isMissing(err) {
		return fmt.Errorf("delete overlay IPv4 address: %w", err)
	}
	return nil
}

func (helper *IPHelper) EnsureRoute(ctx context.Context, route Route) (bool, error) {
	if err := contextErr(ctx); err != nil {
		return false, err
	}
	if err := validateRoute(route); err != nil {
		return false, err
	}
	if err := helper.find(helper.api.initializeRoute, helper.api.createRoute, helper.api.deleteRoute); err != nil {
		return false, err
	}
	row, err := helper.makeRouteRow(route)
	if err != nil {
		return false, err
	}
	if err := callWin32(helper.api.createRoute, unsafe.Pointer(&row)); err != nil {
		if errors.Is(err, windows.ERROR_OBJECT_ALREADY_EXISTS) || errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			// Do not rewrite or claim a route that existed before this
			// process. The reconciler's owned registry controls cleanup.
			return false, nil
		}
		return false, fmt.Errorf("create overlay route %s: %w", route.Destination, err)
	}
	return true, nil
}

func (helper *IPHelper) RemoveRoute(ctx context.Context, route Route) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if err := validateRoute(route); err != nil {
		return err
	}
	if err := helper.find(helper.api.initializeRoute, helper.api.deleteRoute); err != nil {
		return err
	}
	row, err := helper.makeRouteRow(route)
	if err != nil {
		return err
	}
	if err := callWin32(helper.api.deleteRoute, unsafe.Pointer(&row)); err != nil && !isMissing(err) {
		return fmt.Errorf("delete overlay route %s: %w", route.Destination, err)
	}
	return nil
}

func (helper *IPHelper) find(procedures ...*windows.LazyProc) error {
	if helper == nil {
		return ErrUnsupportedPlatform
	}
	for _, procedure := range procedures {
		if err := procedure.Find(); err != nil {
			return fmt.Errorf("load Windows IP Helper API: %w", err)
		}
	}
	return nil
}

func (helper *IPHelper) makeRouteRow(route Route) (ipForwardRow2, error) {
	var row ipForwardRow2
	_, _, _ = helper.api.initializeRoute.Call(uintptr(unsafe.Pointer(&row)))
	row.InterfaceLUID = route.InterfaceLUID
	row.Metric = route.Metric
	if row.Metric == 0 {
		row.Metric = 1
	}
	row.Protocol = 10006 // PROTO_IP_NETMGMT / NT static route
	row.Origin = 0       // NL_ROUTE_ORIGIN_MANUAL
	row.ValidLifetime = ^uint32(0)
	row.PreferredLifetime = ^uint32(0)
	if err := setPrefix(&row.DestinationPrefix, route.Destination); err != nil {
		return row, err
	}
	if route.NextHop.IsValid() {
		if err := setRawAddress(&row.NextHop, route.NextHop, 0); err != nil {
			return row, err
		}
	} else if err := setRawAddress(&row.NextHop, route.Destination.Addr(), 0); err != nil {
		return row, err
	}
	if route.NextHop.IsValid() {
		// A route with a valid next hop is encoded above. Direct WireGuard
		// overlay routes use a zero IPv4 next hop with the family set.
		return row, nil
	}
	if raw := (*windows.RawSockaddrInet4)(unsafe.Pointer(&row.NextHop)); route.Destination.Addr().Is4() {
		raw.Addr = [4]byte{}
	}
	return row, nil
}

func setPrefix(prefix *ipAddressPrefix, value netip.Prefix) error {
	if err := setRawAddress(&prefix.Prefix, value.Addr(), 0); err != nil {
		return err
	}
	prefix.PrefixLength = uint8(value.Bits())
	return nil
}

func setRawAddress(raw *rawSockaddrInet, address netip.Addr, port uint16) error {
	*raw = rawSockaddrInet{}
	if address.Is4() {
		raw4 := (*windows.RawSockaddrInet4)(unsafe.Pointer(raw))
		raw4.Family = windows.AF_INET
		raw4.Port = htons(port)
		value := address.As4()
		copy(raw4.Addr[:], value[:])
		return nil
	}
	if address.Is6() {
		raw6 := (*windows.RawSockaddrInet6)(unsafe.Pointer(raw))
		raw6.Family = windows.AF_INET6
		raw6.Port = htons(port)
		value := address.As16()
		copy(raw6.Addr[:], value[:])
		return nil
	}
	return fmt.Errorf("invalid IP address")
}

func callWin32(procedure *windows.LazyProc, pointer unsafe.Pointer) error {
	result, _, callErr := procedure.Call(uintptr(pointer))
	if result != 0 {
		return syscall.Errno(result)
	}
	if callErr != nil && !errors.Is(callErr, syscall.Errno(0)) {
		return callErr
	}
	return nil
}

func isMissing(err error) bool {
	return errors.Is(err, windows.ERROR_NOT_FOUND) || errors.Is(err, windows.ERROR_FILE_NOT_FOUND)
}

func htons(value uint16) uint16 { return value<<8 | value>>8 }
