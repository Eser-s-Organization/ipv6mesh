//go:build windows

package main

import (
	"path/filepath"
	"testing"

	"github.com/Eser-s-Organization/ipv6mesh/internal/service"
)

func setMachineEnvironmentForTest(t *testing.T, values map[string]string) {
	t.Helper()
	previous := readMachineEnvironmentValue
	readMachineEnvironmentValue = func(name string) string {
		return values[name]
	}
	t.Cleanup(func() {
		readMachineEnvironmentValue = previous
	})
}

func TestServiceEnvironmentPrefersProcessEnvironment(t *testing.T) {
	setMachineEnvironmentForTest(t, map[string]string{
		"IPV6MESH_CONTROL_URL": "http://[2001:db8::2]:8080",
	})
	t.Setenv("IPV6MESH_CONTROL_URL", "http://[2001:db8::1]:8080")
	if got := serviceEnvironment("IPV6MESH_CONTROL_URL"); got != "http://[2001:db8::1]:8080" {
		t.Fatalf("control URL = %q, want process environment value", got)
	}
}

func TestServiceEnvironmentFallsBackToMachineEnvironment(t *testing.T) {
	setMachineEnvironmentForTest(t, map[string]string{
		"IPV6MESH_CONTROL_URL": "http://[2001:db8::2]:8080",
	})
	t.Setenv("IPV6MESH_CONTROL_URL", "")
	if got := serviceEnvironment("IPV6MESH_CONTROL_URL"); got != "http://[2001:db8::2]:8080" {
		t.Fatalf("control URL = %q, want Machine environment value", got)
	}
}

func TestServiceEnvironmentMissingReturnsEmpty(t *testing.T) {
	setMachineEnvironmentForTest(t, nil)
	t.Setenv("IPV6MESH_CONTROL_URL", "")
	if got := serviceEnvironment("IPV6MESH_CONTROL_URL"); got != "" {
		t.Fatalf("missing control URL = %q, want empty", got)
	}
}

func TestServiceDataDirectoryUsesMachineEnvironmentFallback(t *testing.T) {
	setMachineEnvironmentForTest(t, map[string]string{
		"IPV6MESH_DATA_DIR": `C:\IPv6Mesh-Machine`,
	})
	t.Setenv("IPV6MESH_DATA_DIR", "")
	t.Setenv("ProgramData", "")
	if got := serviceDataDirectory(); got != `C:\IPv6Mesh-Machine` {
		t.Fatalf("service data directory = %q, want Machine environment value", got)
	}
}

func TestServiceDataDirectoryUsesExplicitEnvironmentValue(t *testing.T) {
	t.Setenv("IPV6MESH_DATA_DIR", `C:\IPv6Mesh-Test`)
	t.Setenv("ProgramData", `C:\ProgramData`)
	if got := serviceDataDirectory(); got != `C:\IPv6Mesh-Test` {
		t.Fatalf("service data directory = %q, want explicit directory", got)
	}
}

func TestServiceDataDirectoryUsesProgramDataFallback(t *testing.T) {
	setMachineEnvironmentForTest(t, nil)
	t.Setenv("IPV6MESH_DATA_DIR", "")
	t.Setenv("ProgramData", `C:\ProgramData-Test`)
	want := filepath.Join(`C:\ProgramData-Test`, "IPv6Mesh")
	if got := serviceDataDirectory(); got != want {
		t.Fatalf("service data directory = %q, want %q", got, want)
	}
}

func TestLocalServiceOptionsCarryRoomControlURL(t *testing.T) {
	const controlURL = "http://[2001:db8::1]:8080"
	options := localServiceOptions(nil, nil, nil, nil, controlURL)
	if options.ControlURL != controlURL {
		t.Fatalf("service control URL = %q, want %q", options.ControlURL, controlURL)
	}
	var _ service.Options = options
}
