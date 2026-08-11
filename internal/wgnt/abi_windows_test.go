//go:build windows

package wgnt

import (
	"testing"
	"unsafe"
)

func TestWireGuardABITypeSizes(t *testing.T) {
	checks := map[string]struct {
		got  uintptr
		want uintptr
	}{
		"interface":  {unsafe.Sizeof(wireguardInterface{}), 80},
		"allowed_ip": {unsafe.Sizeof(wireguardAllowedIP{}), 24},
		"peer":       {unsafe.Sizeof(wireguardPeer{}), 136},
		"sockaddr":   {unsafe.Sizeof(rawSockaddrInet{}), 28},
	}
	for name, check := range checks {
		if check.got != check.want {
			t.Errorf("%s size = %d, want %d", name, check.got, check.want)
		}
	}
}
