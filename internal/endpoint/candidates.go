// Package endpoint discovers and ranks transport endpoints for the IPv6-first
// mesh. It reports only transport metadata; no private key or VPN payload is
// handled here.
package endpoint

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"time"

	"github.com/Eser-s-Organization/ipv6mesh/internal/control"
)

var (
	ErrUnsupportedPlatform = errors.New("endpoint discovery is unsupported on this platform")
	ErrInvalidEnumerator   = errors.New("endpoint enumerator is missing")
	ErrInvalidReporter     = errors.New("endpoint reporter is missing")
)

type Candidate struct {
	Address        netip.Addr
	Port           uint16
	Interface      string
	InterfaceLUID  uint64
	Priority       int
	SkipAsSource   bool
	Loopback       bool
	LinkLocal      bool
	Virtual        bool
	ValidUntil     time.Time
	PreferredUntil time.Time
}

type Enumerator interface {
	Enumerate(context.Context, uint16) ([]Candidate, error)
}

type Discoverer struct {
	enumerator Enumerator
	clock      func() time.Time
	max        int
}

func NewDiscoverer(enumerator Enumerator) (*Discoverer, error) {
	if enumerator == nil {
		return nil, ErrInvalidEnumerator
	}
	return &Discoverer{enumerator: enumerator, clock: func() time.Time { return time.Now().UTC() }, max: 16}, nil
}

func (discoverer *Discoverer) Discover(ctx context.Context, port uint16) ([]control.EndpointCandidate, error) {
	if discoverer == nil || discoverer.enumerator == nil {
		return nil, ErrInvalidEnumerator
	}
	if ctx == nil {
		return nil, context.Canceled
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	now := discoverer.clock().UTC()
	candidates, err := discoverer.enumerator.Enumerate(ctx, port)
	if err != nil {
		return nil, err
	}
	filtered := FilterIPv6Candidates(candidates, now)
	if discoverer.max > 0 && len(filtered) > discoverer.max {
		filtered = filtered[:discoverer.max]
	}
	result := make([]control.EndpointCandidate, len(filtered))
	for index, candidate := range filtered {
		result[index] = control.EndpointCandidate{
			Address:    netipToNetIP(candidate.Address),
			Port:       candidate.Port,
			Family:     control.FamilyIPv6,
			Interface:  candidate.Interface,
			Priority:   candidate.Priority,
			ObservedAt: now,
		}
	}
	return result, nil
}

func FilterIPv6Candidates(candidates []Candidate, now time.Time) []Candidate {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	filtered := make([]Candidate, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if !candidate.Address.IsValid() || !candidate.Address.Is6() || candidate.Port == 0 || candidate.SkipAsSource || candidate.Loopback || candidate.LinkLocal || candidate.Virtual {
			continue
		}
		if !candidate.Address.IsGlobalUnicast() || candidate.Address.IsUnspecified() || candidate.Address.IsMulticast() {
			continue
		}
		if !candidate.ValidUntil.IsZero() && !now.Before(candidate.ValidUntil) {
			continue
		}
		if !candidate.PreferredUntil.IsZero() && !now.Before(candidate.PreferredUntil) {
			continue
		}
		if strings.TrimSpace(candidate.Interface) == "" {
			continue
		}
		if candidate.Priority < 0 {
			continue
		}
		key := fmt.Sprintf("%s|%d|%s", candidate.Address, candidate.Port, candidate.Interface)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		filtered = append(filtered, candidate)
	}
	sort.SliceStable(filtered, func(left, right int) bool {
		if filtered[left].Priority != filtered[right].Priority {
			return filtered[left].Priority < filtered[right].Priority
		}
		if filtered[left].Address != filtered[right].Address {
			return filtered[left].Address.String() < filtered[right].Address.String()
		}
		if filtered[left].Interface != filtered[right].Interface {
			return filtered[left].Interface < filtered[right].Interface
		}
		return filtered[left].Port < filtered[right].Port
	})
	return filtered
}

func netipToNetIP(address netip.Addr) []byte {
	if address.Is4() {
		value := address.As4()
		return append([]byte(nil), value[:]...)
	}
	value := address.As16()
	return append([]byte(nil), value[:]...)
}
