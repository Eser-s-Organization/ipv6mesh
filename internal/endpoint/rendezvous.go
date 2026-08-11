package endpoint

import (
	"context"
	"net/netip"
	"time"

	"github.com/Eser-s-Organization/ipv6mesh/internal/control"
)

// Reporter is implemented by the control-plane client. Keeping this small
// interface lets discovery be tested without an HTTP server.
type Reporter interface {
	Heartbeat(context.Context, string, string, string, string, []control.EndpointCandidate) error
}

type Rendezvous struct {
	reporter Reporter
	clock    func() time.Time
}

func NewRendezvous(reporter Reporter) *Rendezvous {
	return &Rendezvous{reporter: reporter, clock: func() time.Time { return time.Now().UTC() }}
}

func (rendezvous *Rendezvous) Report(ctx context.Context, networkID, nodeID, sessionToken, clientVersion string, candidates []control.EndpointCandidate) error {
	if rendezvous == nil || rendezvous.reporter == nil {
		return ErrInvalidReporter
	}
	if networkID == "" || nodeID == "" || sessionToken == "" {
		return control.ErrValidation
	}
	// Normalize the observation timestamp at the edge so all candidates in a
	// single heartbeat have one coherent freshness point.
	now := rendezvous.clock().UTC()
	normalized := make([]control.EndpointCandidate, len(candidates))
	for index, candidate := range candidates {
		candidate.NodeID = nodeID
		if candidate.ObservedAt.IsZero() {
			candidate.ObservedAt = now
		}
		if address := candidate.Address; address != nil {
			if parsed, err := netip.ParseAddr(address.String()); err == nil {
				candidate.Address = netipToNetIP(parsed)
			}
		}
		normalized[index] = candidate
	}
	return rendezvous.reporter.Heartbeat(ctx, networkID, nodeID, sessionToken, clientVersion, normalized)
}
