//go:build windows

package main

import (
	"bufio"
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
