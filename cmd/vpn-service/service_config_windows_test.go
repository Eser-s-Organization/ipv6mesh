//go:build windows

package main

import (
	"path/filepath"
	"testing"
)

func TestServiceDataDirectoryUsesExplicitEnvironmentValue(t *testing.T) {
	t.Setenv("IPV6MESH_DATA_DIR", `C:\IPv6Mesh-Test`)
	t.Setenv("ProgramData", `C:\ProgramData`)
	if got := serviceDataDirectory(); got != `C:\IPv6Mesh-Test` {
		t.Fatalf("service data directory = %q, want explicit directory", got)
	}
}

func TestServiceDataDirectoryUsesProgramDataFallback(t *testing.T) {
	t.Setenv("IPV6MESH_DATA_DIR", "")
	t.Setenv("ProgramData", `C:\ProgramData-Test`)
	want := filepath.Join(`C:\ProgramData-Test`, "IPv6Mesh")
	if got := serviceDataDirectory(); got != want {
		t.Fatalf("service data directory = %q, want %q", got, want)
	}
}
