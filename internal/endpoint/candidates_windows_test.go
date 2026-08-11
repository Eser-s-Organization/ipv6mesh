//go:build windows

package endpoint

import (
	"net/netip"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestWindowsEndpointABIAndLifetimeLayout(t *testing.T) {
	if got := unsafe.Sizeof(rawSockaddrInet{}); got != 28 {
		t.Fatalf("raw sockaddr size = %d, want 28", got)
	}
	if got := unsafe.Sizeof(unicastAddressRow{}); got != 80 {
		t.Fatalf("unicast row size = %d, want 80", got)
	}
	now := time.Date(2026, time.August, 11, 12, 0, 0, 0, time.UTC)
	if !lifetime(now, 0).Equal(now) || !lifetime(now, ^uint32(0)).IsZero() || !lifetime(now, 10).Equal(now.Add(10*time.Second)) {
		t.Fatal("unexpected Windows address lifetime conversion")
	}
}

func TestWindowsEndpointParsingAndVirtualInterfaceHeuristic(t *testing.T) {
	var raw rawSockaddrInet
	raw.Family = windows.AF_INET6
	native := (*windows.RawSockaddrInet6)(unsafe.Pointer(&raw))
	native.Addr = netip.MustParseAddr("2001:db8::20").As16()
	native.Scope_id = 12
	parsed := parseIPv6(raw)
	if parsed.String() != "2001:db8::20%12" {
		t.Fatalf("parsed IPv6 address = %s", parsed)
	}
	if virtualInterface("Laptop", 6) {
		t.Fatal("physical adapter name Laptop was classified as virtual")
	}
	if !virtualInterface("TAP-Windows Adapter V9", 6) || !virtualInterface("WireGuard Tunnel", 6) {
		t.Fatal("known virtual adapter was not classified as virtual")
	}
}
