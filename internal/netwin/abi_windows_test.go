//go:build windows

package netwin

import (
	"testing"
	"unsafe"
)

func TestIPHelperABITypeSizes(t *testing.T) {
	checks := map[string]struct {
		got  uintptr
		want uintptr
	}{
		"sockaddr":   {unsafe.Sizeof(rawSockaddrInet{}), 28},
		"prefix":     {unsafe.Sizeof(ipAddressPrefix{}), 32},
		"unicast":    {unsafe.Sizeof(unicastIPAddressRow{}), 80},
		"ip_forward": {unsafe.Sizeof(ipForwardRow2{}), 104},
	}
	for name, check := range checks {
		if check.got != check.want {
			t.Errorf("%s size = %d, want %d", name, check.got, check.want)
		}
	}
}
