//go:build windows

package main

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Eser-s-Organization/ipv6mesh/internal/ipc"
)

func TestResolveControlURL(t *testing.T) {
	valid := []string{
		"http://[2001:db8::1]:8080",
		"https://control.example.test:8443",
	}
	for _, value := range valid {
		if got, err := resolveControlURL(value); err != nil || got != value {
			t.Fatalf("resolveControlURL(%q) = %q, %v", value, got, err)
		}
	}
	invalid := []string{"", "2001:db8::1", "ftp://control.example.test", "http://"}
	for _, value := range invalid[1:] {
		if _, err := resolveControlURL(value); err == nil {
			t.Fatalf("resolveControlURL(%q) unexpectedly succeeded", value)
		}
	}
}

func TestSafeZipPath(t *testing.T) {
	for _, value := range []string{"install.ps1", "nested\\file.txt", "nested/file.txt"} {
		if _, err := safeZipPath(value); err != nil {
			t.Fatalf("safeZipPath(%q) failed: %v", value, err)
		}
	}
	for _, value := range []string{"..\\escape.txt", "../escape.txt", "C:\\escape.txt", "\\\\server\\share\\escape.txt"} {
		if _, err := safeZipPath(value); err == nil {
			t.Fatalf("safeZipPath(%q) unexpectedly succeeded", value)
		}
	}
}

func TestPromptValueUsesFallback(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("\n"))
	value, err := promptValue(reader, "Device name", "", "test-device", false)
	if err != nil {
		t.Fatalf("promptValue returned error: %v", err)
	}
	if value != "test-device" {
		t.Fatalf("promptValue = %q, want fallback", value)
	}
}

func TestPromptValueRejectsMissingNonInteractiveValue(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader(""))
	if _, err := promptValue(reader, "Invite", "", "", true); err == nil {
		t.Fatal("promptValue unexpectedly accepted a missing non-interactive value")
	}
}

func TestServiceResponseErrorIncludesCodeAndMessage(t *testing.T) {
	err := serviceResponseError("join", ipc.Response{Error: &ipc.Error{Code: "control_failed", Message: "control plane unavailable"}})
	if err == nil || !strings.Contains(err.Error(), "control_failed") || !strings.Contains(err.Error(), "control plane unavailable") {
		t.Fatalf("serviceResponseError = %v", err)
	}
}

func TestBuildInstallArgumentsPassesPackageDirectory(t *testing.T) {
	args := buildInstallArguments(installerOptions{startService: true}, `C:\Temp\ipv6mesh-installer`, "http://[2001:db8::1]:8080")
	for i, arg := range args {
		if arg == "-PackageDirectory" {
			if i+1 >= len(args) {
				t.Fatal("-PackageDirectory has no value")
			}
			if args[i+1] != `C:\Temp\ipv6mesh-installer` {
				t.Fatalf("-PackageDirectory value = %q, want extracted package directory", args[i+1])
			}
			return
		}
	}
	t.Fatal("install arguments do not include -PackageDirectory")
}

func TestBuildGraphicalArgumentsPassesUIPayloadAndInitialValues(t *testing.T) {
	args := buildGraphicalArguments(installerOptions{
		controlURL:       "http://[2001:db8::1]:8080",
		invite:           "invite-value",
		deviceName:       "device-a",
		networkID:        "network-a",
		installDirectory: `C:\Program Files\IPv6Mesh`,
	}, `C:\Temp\ipv6mesh-installer`, "0.1.0-debug.5")
	want := []string{
		"-STA",
		"-WindowStyle", "Hidden",
		filepath.Join(`C:\Temp\ipv6mesh-installer`, "ui.ps1"),
		"-PackageDirectory", `C:\Temp\ipv6mesh-installer`,
		"-Version", "0.1.0-debug.5",
		"-ControlUrl", "http://[2001:db8::1]:8080",
		"-Invite", "invite-value",
		"-DeviceName", "device-a",
		"-Network", "network-a",
	}
	for _, value := range want {
		found := false
		for _, arg := range args {
			if arg == value {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("graphical arguments missing %q: %v", value, args)
		}
	}
}

func TestWindowsPackageIncludesChineseUI(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	packageRoot := filepath.Join(filepath.Dir(sourceFile), "..", "..", "packaging", "windows")
	buildScript, err := os.ReadFile(filepath.Join(packageRoot, "build.ps1"))
	if err != nil {
		t.Fatalf("read build script: %v", err)
	}
	if !strings.Contains(string(buildScript), `"ui.ps1"`) {
		t.Fatal("build script does not include ui.ps1")
	}
	uiScript, err := os.ReadFile(filepath.Join(packageRoot, "ui.ps1"))
	if err != nil {
		t.Fatalf("read UI script: %v", err)
	}
	if !bytes.HasPrefix(uiScript, []byte{0xEF, 0xBB, 0xBF}) {
		t.Fatal("UI script must be UTF-8 with BOM for Windows PowerShell 5.1")
	}
	contents := string(uiScript)
	for _, required := range []string{
		"System.Windows.Forms",
		"控制面管理员",
		"游戏房主",
		"游戏成员",
		"network",
		"invite",
		"复制日志",
		"导出日志",
	} {
		if !strings.Contains(contents, required) {
			t.Fatalf("UI script missing %q", required)
		}
	}
}

func TestInstallScriptStopsExistingServiceBeforeCopyingFiles(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	scriptPath := filepath.Join(filepath.Dir(sourceFile), "..", "..", "packaging", "windows", "install.ps1")
	script, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read install script: %v", err)
	}
	contents := string(script)
	stopIndex := strings.Index(contents, "Stop-Service")
	copyIndex := strings.Index(contents, "Copy-Item -LiteralPath")
	if stopIndex < 0 {
		t.Fatal("install script does not stop an existing service")
	}
	if copyIndex < 0 {
		t.Fatal("install script does not copy package files")
	}
	if stopIndex > copyIndex {
		t.Fatalf("Stop-Service occurs after Copy-Item (stop=%d, copy=%d)", stopIndex, copyIndex)
	}
}
