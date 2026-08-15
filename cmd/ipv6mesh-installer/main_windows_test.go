//go:build windows

package main

import "testing"

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
