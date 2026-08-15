// Package control contains the control-plane domain models and their pure
// validation rules. It deliberately does not contain VPN payloads or private
// key material.
package control

import (
	"net"
	"time"
)

// MembershipRole is the authority a node has within a network. It is an alias
// for string so HTTP and persistence boundaries can use the shared model
// without conversions.
type MembershipRole = string

const (
	RoleOwner  = "owner"
	RoleAdmin  = "admin"
	RoleMember = "member"
)

// MembershipStatus controls whether a membership is eligible for snapshots.
type MembershipStatus = string

const (
	MembershipPending   = "pending"
	MembershipActive    = "active"
	MembershipSuspended = "suspended"
	MembershipRevoked   = "revoked"
)

// EndpointFamily identifies the address family of an endpoint candidate.
type EndpointFamily = string

const (
	FamilyIPv4 = "ipv4"
	FamilyIPv6 = "ipv6"
)

// RelayAssignmentStatus controls whether a relay assignment can be used.
type RelayAssignmentStatus = string

const (
	RelayAssignmentActive  = "active"
	RelayAssignmentExpired = "expired"
	RelayAssignmentRevoked = "revoked"
)

// Network is a control-plane managed virtual network.
type Network struct {
	ID            string
	Name          string
	IPv4Pool      string
	OwnerID       string
	ConfigVersion int64
	CreatedAt     time.Time
}

// Node identifies a registered VPN client without containing its private key.
type Node struct {
	ID            string
	DisplayName   string
	PublicKey     string
	Platform      string
	ClientVersion string
	LastSeen      time.Time
}

// Membership assigns a node a virtual IPv4 address within a network.
type Membership struct {
	NetworkID   string
	NodeID      string
	VirtualIPv4 net.IP
	Role        MembershipRole
	Status      MembershipStatus
}

// EndpointCandidate is an observed transport endpoint for a node.
type EndpointCandidate struct {
	NodeID     string
	Address    net.IP
	Port       uint16
	Family     EndpointFamily
	Interface  string
	Priority   int
	ObservedAt time.Time
}

// Invite stores only a verifier for an invitation token. The raw token is
// intentionally not part of this model.
type Invite struct {
	ID               string
	NetworkID        string
	TokenHash        string
	ExpiresAt        time.Time
	ConsumedAt       *time.Time
	RevokedAt        *time.Time
	ConsumedByNodeID string
	CreatedAt        time.Time
}

// IsExpired reports whether an invite is unavailable at now. Expiration is
// inclusive: an invite is expired at exactly ExpiresAt.
func (i Invite) IsExpired(now time.Time) bool {
	return !now.Before(i.ExpiresAt)
}

// RelayAssignment identifies the relay endpoint currently assigned to a node
// in a network.
type RelayAssignment struct {
	ID          string
	NetworkID   string
	NodeID      string
	RelayNodeID string
	Address     net.IP
	Port        uint16
	Family      EndpointFamily
	Status      RelayAssignmentStatus
	AssignedAt  time.Time
	ExpiresAt   *time.Time
}

// AuditEvent records a control-plane action. Metadata is application data and
// must not contain raw invite tokens, private keys, or VPN payloads.
type AuditEvent struct {
	ID           string
	NetworkID    string
	ActorID      string
	Action       string
	ResourceType string
	ResourceID   string
	Metadata     map[string]string
	CreatedAt    time.Time
}

// Peer is the snapshot representation of an eligible remote node.
type Peer struct {
	NodeID      string
	DisplayName string
	PublicKey   string
	VirtualIPv4 net.IP
	Node        Node
	Membership  Membership
	Endpoints   []EndpointCandidate
}

// NetworkMember is a verbose alias-like snapshot helper for callers that
// prefer the nested model fields on Peer.
type NetworkMember struct {
	Node       Node
	Membership Membership
	Endpoints  []EndpointCandidate
}

// NetworkSnapshot is the versioned configuration delivered to a node.
type NetworkSnapshot struct {
	NetworkID        string
	Generation       int64
	ConfigVersion    int64
	LocalNodeID      string
	LocalVirtualIPv4 net.IP
	Peers            []Peer
	RelayAssignment  *RelayAssignment
	Relay            *RelayAssignment
	GeneratedAt      time.Time
}
