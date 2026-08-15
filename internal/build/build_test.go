package build_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRepositoryBootstrap(t *testing.T) {
	t.Helper()

	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}

	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", ".."))
	requiredFiles := []string{
		"go.mod",
		"cmd/control-server/main.go",
		"cmd/vpn-service/main_windows.go",
		"cmd/vpn-service/main_nonwindows.go",
		"cmd/vpnctl/main.go",
		"cmd/relay-agent/main_linux.go",
		"cmd/relay-agent/main_nonlinux.go",
		".github/workflows/test.yml",
		"Makefile",
	}

	for _, relativePath := range requiredFiles {
		if _, err := os.Stat(filepath.Join(repoRoot, relativePath)); err != nil {
			t.Errorf("required bootstrap file %q is missing: %v", relativePath, err)
		}
	}
}
