package wgnt

import (
	"context"
	"errors"
	"net/netip"
	"runtime"
	"testing"
	"time"
)

func testKey(value byte) Key {
	var key Key
	key[0] = value
	return key
}

func TestConfigurationValidationRejectsDefaultRoute(t *testing.T) {
	configuration := Configuration{
		PrivateKey: testKey(1),
		Peers: []Peer{{
			PublicKey:  testKey(2),
			AllowedIPs: []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")},
		}},
	}
	if err := configuration.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("Validate error = %v, want ErrInvalidConfig", err)
	}
}

func TestConfigurationValidationAcceptsNodeHostRoutes(t *testing.T) {
	configuration := Configuration{
		PrivateKey: testKey(1),
		Peers: []Peer{{
			PublicKey:           testKey(2),
			Endpoint:            netip.MustParseAddrPort("[2001:db8::2]:51820"),
			PersistentKeepalive: 25 * time.Second,
			AllowedIPs: []netip.Prefix{
				netip.MustParsePrefix("10.77.0.2/32"),
				netip.MustParsePrefix("2001:db8:77::2/128"),
			},
		}},
	}
	if err := configuration.Validate(); err != nil {
		t.Fatalf("Validate error = %v", err)
	}
}

func TestConfigurationValidationRejectsDuplicatePeersAndFractionalKeepalive(t *testing.T) {
	configuration := Configuration{
		PrivateKey: testKey(1),
		Peers: []Peer{
			{PublicKey: testKey(2), PersistentKeepalive: 1500 * time.Millisecond},
			{PublicKey: testKey(2)},
		},
	}
	if err := configuration.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("Validate error = %v, want ErrInvalidConfig", err)
	}
}

func TestNonWindowsClientIsExplicitlyUnsupported(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the Windows client requires the WireGuardNT DLL and adapter privileges")
	}
	client := New()
	if _, err := client.Ensure("IPv6Mesh"); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("Ensure error = %v, want ErrUnsupportedPlatform", err)
	}
	if err := client.Configure(context.Background(), 0, Configuration{}); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("Configure error = %v, want ErrUnsupportedPlatform", err)
	}
}
