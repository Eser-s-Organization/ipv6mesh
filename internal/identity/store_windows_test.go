//go:build windows

package identity

import "testing"

func TestDPAPIProtectsAndRestoresWithMachineScope(t *testing.T) {
	protector, err := newDefaultProtector()
	if err != nil {
		t.Fatalf("newDefaultProtector: %v", err)
	}
	protected, err := protector.Protect([]byte("identity-test"))
	if err != nil {
		t.Fatalf("Protect: %v", err)
	}
	restored, err := protector.Unprotect(protected)
	if err != nil {
		t.Fatalf("Unprotect: %v", err)
	}
	if string(restored) != "identity-test" {
		t.Fatalf("restored plaintext = %q", restored)
	}
}
