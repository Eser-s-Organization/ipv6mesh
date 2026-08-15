package endpoint

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"reflect"
	"testing"
	"time"

	"github.com/Eser-s-Organization/ipv6mesh/internal/control"
)

type candidateEnumeratorFunc func(context.Context, uint16) ([]Candidate, error)

func (function candidateEnumeratorFunc) Enumerate(ctx context.Context, port uint16) ([]Candidate, error) {
	return function(ctx, port)
}

func TestFilterIPv6CandidatesRejectsUnsafeAndExpiredAddresses(t *testing.T) {
	now := time.Date(2026, time.August, 11, 12, 0, 0, 0, time.UTC)
	valid := func(address string, priority int) Candidate {
		return Candidate{
			Address:   netip.MustParseAddr(address),
			Port:      51820,
			Interface: "Ethernet",
			Priority:  priority,
		}
	}
	candidates := []Candidate{
		valid("2001:db8::20", 2),
		valid("2001:db8::10", 1),
		valid("2001:db8::10", 1),
		{Address: netip.MustParseAddr("fe80::10"), Port: 51820, Interface: "Ethernet"},
		{Address: netip.MustParseAddr("2001:db8::11"), Port: 51820, Interface: "Ethernet", SkipAsSource: true},
		{Address: netip.MustParseAddr("2001:db8::12"), Port: 51820, Interface: "WireGuard", Virtual: true},
		{Address: netip.MustParseAddr("2001:db8::13"), Port: 51820, Interface: "Ethernet", ValidUntil: now},
		{Address: netip.MustParseAddr("2001:db8::14"), Port: 51820, Interface: "Ethernet", PreferredUntil: now},
		{Address: netip.MustParseAddr("192.0.2.1"), Port: 51820, Interface: "Ethernet"},
		{Address: netip.MustParseAddr("2001:db8::15"), Port: 0, Interface: "Ethernet"},
		{Address: netip.MustParseAddr("2001:db8::16"), Port: 51820},
	}

	filtered := FilterIPv6Candidates(candidates, now)
	if len(filtered) != 2 {
		t.Fatalf("expected two safe candidates, got %#v", filtered)
	}
	if got := filtered[0].Address.String(); got != "2001:db8::10" {
		t.Fatalf("expected lowest priority address first, got %s", got)
	}
	if got := filtered[1].Address.String(); got != "2001:db8::20" {
		t.Fatalf("expected second address, got %s", got)
	}
}

func TestDiscovererConvertsAndCapsCandidates(t *testing.T) {
	now := time.Date(2026, time.August, 11, 12, 0, 0, 0, time.UTC)
	discoverer, err := NewDiscoverer(candidateEnumeratorFunc(func(_ context.Context, port uint16) ([]Candidate, error) {
		if port != 51820 {
			t.Fatalf("unexpected port %d", port)
		}
		result := make([]Candidate, 17)
		for index := range result {
			result[index] = Candidate{
				Address:   netip.MustParseAddr(fmt.Sprintf("2001:db8::%x", index+10)),
				Port:      port,
				Interface: "Ethernet",
				Priority:  index,
			}
		}
		return result, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	discoverer.clock = func() time.Time { return now }

	got, err := discoverer.Discover(context.Background(), 51820)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 16 {
		t.Fatalf("expected discoverer cap of 16, got %d", len(got))
	}
	if got[0].Family != control.FamilyIPv6 || got[0].Port != 51820 || !got[0].ObservedAt.Equal(now) {
		t.Fatalf("unexpected converted candidate: %#v", got[0])
	}
	if !reflect.DeepEqual(got[0].Address, net.ParseIP("2001:db8::a")) {
		t.Fatalf("unexpected IPv6 bytes: %v", got[0].Address)
	}
}

func TestRendezvousNormalizesNodeAndObservation(t *testing.T) {
	now := time.Date(2026, time.August, 11, 12, 0, 0, 0, time.UTC)
	reporter := &recordingReporter{}
	rendezvous := NewRendezvous(reporter)
	rendezvous.clock = func() time.Time { return now }

	err := rendezvous.Report(context.Background(), "network", "node", "session", "1.0.0", []control.EndpointCandidate{{
		Address: netip.MustParseAddr("2001:db8::1").AsSlice(),
		Port:    51820,
		Family:  control.FamilyIPv6,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if reporter.networkID != "network" || reporter.nodeID != "node" || reporter.sessionToken != "session" {
		t.Fatalf("unexpected reporter request: %#v", reporter)
	}
	if len(reporter.endpoints) != 1 || reporter.endpoints[0].NodeID != "node" || !reporter.endpoints[0].ObservedAt.Equal(now) {
		t.Fatalf("unexpected normalized endpoint: %#v", reporter.endpoints)
	}
}

type recordingReporter struct {
	networkID    string
	nodeID       string
	sessionToken string
	endpoints    []control.EndpointCandidate
}

func (reporter *recordingReporter) Heartbeat(_ context.Context, networkID, nodeID, sessionToken, _ string, endpoints []control.EndpointCandidate) error {
	reporter.networkID = networkID
	reporter.nodeID = nodeID
	reporter.sessionToken = sessionToken
	reporter.endpoints = endpoints
	return nil
}
