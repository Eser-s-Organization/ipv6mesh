package room

import (
	"errors"
	"testing"
)

func TestControlURLAcceptsGlobalIPv6(t *testing.T) {
	tests := map[string]string{
		"2001:db8::1":          "http://[2001:db8::1]:8080",
		" [2001:db8:0:0::2] ":  "http://[2001:db8::2]:8080",
		"2001:4860:4860::8888": "http://[2001:4860:4860::8888]:8080",
	}
	for input, want := range tests {
		got, err := ControlURL(input)
		if err != nil {
			t.Fatalf("ControlURL(%q): %v", input, err)
		}
		if got != want {
			t.Fatalf("ControlURL(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestControlURLRejectsNonPublicOrStructuredInput(t *testing.T) {
	for _, input := range []string{
		"", "192.0.2.1", "::", "::1", "fe80::1", "fc00::1",
		"::ffff:192.0.2.1", "ff02::1", "fe80::1%12",
		"http://[2001:db8::1]:8080", "[2001:db8::1]:9000", "2001:db8::1/path",
	} {
		if _, err := ControlURL(input); !errors.Is(err, ErrInvalidHostIPv6) {
			t.Fatalf("ControlURL(%q) error = %v", input, err)
		}
	}
}
