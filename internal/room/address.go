package room

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

var ErrInvalidHostIPv6 = errors.New("invalid host IPv6 address")

func ControlURL(input string) (string, error) {
	value := strings.TrimSpace(input)
	if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		value = strings.TrimSuffix(strings.TrimPrefix(value, "["), "]")
	}
	if value == "" || strings.ContainsAny(value, "/%") || strings.Contains(value, "://") {
		return "", ErrInvalidHostIPv6
	}
	address, err := netip.ParseAddr(value)
	if err != nil || !address.Is6() || address.Is4In6() ||
		!address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() ||
		address.IsLinkLocalUnicast() || address.IsMulticast() || address.IsUnspecified() {
		return "", ErrInvalidHostIPv6
	}
	return fmt.Sprintf("http://[%s]:8080", address.String()), nil
}
