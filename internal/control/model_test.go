package control_test

import (
	"errors"
	"net"
	"testing"
	"time"

	"github.com/Eser-s-Organization/ipv6mesh/internal/control"
)

func testNetwork() control.Network {
	return control.Network{
		ID:            "network-1",
		Name:          "test network",
		IPv4Pool:      "10.42.0.0/24",
		OwnerID:       "owner-1",
		ConfigVersion: 1,
		CreatedAt:     time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC),
	}
}

func testNode(id string) control.Node {
	return control.Node{
		ID:            id,
		DisplayName:   "node " + id,
		PublicKey:     "public-key-" + id,
		Platform:      "windows",
		ClientVersion: "0.1.0",
		LastSeen:      time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC),
	}
}

func testMembership(nodeID string, address string) control.Membership {
	return control.Membership{
		NetworkID:   "network-1",
		NodeID:      nodeID,
		VirtualIPv4: net.ParseIP(address).To4(),
		Role:        control.RoleMember,
		Status:      control.MembershipActive,
	}
}

func testEndpoint(nodeID string) control.EndpointCandidate {
	return control.EndpointCandidate{
		NodeID:     nodeID,
		Address:    net.ParseIP("2001:db8::10"),
		Port:       51820,
		Family:     control.FamilyIPv6,
		Interface:  "Ethernet",
		Priority:   10,
		ObservedAt: time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC),
	}
}

func testInvite() control.Invite {
	return control.Invite{
		ID:        "invite-1",
		NetworkID: "network-1",
		TokenHash: "sha256:invite-hash",
		ExpiresAt: time.Date(2026, time.January, 2, 0, 0, 0, 0, time.UTC),
		CreatedAt: time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC),
	}
}

func testRelayAssignment() control.RelayAssignment {
	return control.RelayAssignment{
		ID:          "relay-1",
		NetworkID:   "network-1",
		NodeID:      "node-1",
		RelayNodeID: "node-2",
		Address:     net.ParseIP("2001:db8::20"),
		Port:        51820,
		Family:      control.FamilyIPv6,
		Status:      control.RelayAssignmentActive,
		AssignedAt:  time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC),
	}
}

func TestValidControlModelsPassValidation(t *testing.T) {
	validations := []struct {
		name string
		err  error
	}{
		{name: "network", err: control.ValidateNetwork(testNetwork())},
		{name: "node", err: control.ValidateNode(testNode("node-1"))},
		{name: "membership", err: control.ValidateMembership(testMembership("node-1", "10.42.0.2"))},
		{name: "endpoint", err: control.ValidateEndpointCandidate(testEndpoint("node-1"))},
		{name: "invite", err: control.ValidateInvite(testInvite())},
		{name: "relay assignment", err: control.ValidateRelayAssignment(testRelayAssignment())},
	}

	for _, validation := range validations {
		t.Run(validation.name, func(t *testing.T) {
			if validation.err != nil {
				t.Fatalf("valid model rejected: %v", validation.err)
			}
		})
	}
}

func TestValidateNetworkRejectsIPv6Pool(t *testing.T) {
	network := testNetwork()
	network.IPv4Pool = "2001:db8::/64"

	err := control.ValidateNetwork(network)
	if !errors.Is(err, control.ErrValidation) {
		t.Fatalf("expected validation error, got %v", err)
	}
}

func TestValidateNetworkRejectsIPv4PoolWithHostBits(t *testing.T) {
	network := testNetwork()
	network.IPv4Pool = "10.42.0.1/24"

	err := control.ValidateNetwork(network)
	if !errors.Is(err, control.ErrValidation) {
		t.Fatalf("expected validation error for non-canonical IPv4 pool, got %v", err)
	}
}

func TestValidateNodeRejectsMissingPublicKey(t *testing.T) {
	node := testNode("node-1")
	node.PublicKey = ""

	err := control.ValidateNode(node)
	if !errors.Is(err, control.ErrValidation) {
		t.Fatalf("expected validation error, got %v", err)
	}
}

func TestValidateMembershipRejectsIPv6Address(t *testing.T) {
	membership := testMembership("node-1", "10.42.0.2")
	membership.VirtualIPv4 = net.ParseIP("2001:db8::2")

	err := control.ValidateMembership(membership)
	if !errors.Is(err, control.ErrValidation) {
		t.Fatalf("expected validation error, got %v", err)
	}
}

func TestValidateIPv4InPoolRejectsAddressOutsideNetwork(t *testing.T) {
	if err := control.ValidateIPv4InPool(net.ParseIP("10.42.0.2"), "10.42.0.0/24"); err != nil {
		t.Fatalf("address in pool was rejected: %v", err)
	}
	if err := control.ValidateIPv4InPool(net.ParseIP("10.43.0.2"), "10.42.0.0/24"); !errors.Is(err, control.ErrValidation) {
		t.Fatalf("expected out-of-pool address to be rejected, got %v", err)
	}
}

func TestValidateMembershipRejectsUnknownRoleAndStatus(t *testing.T) {
	membership := testMembership("node-1", "10.42.0.2")
	membership.Role = "operator"
	membership.Status = "connected"

	err := control.ValidateMembership(membership)
	if !errors.Is(err, control.ErrValidation) {
		t.Fatalf("expected validation error, got %v", err)
	}
}

func TestValidateEndpointRejectsPortZeroAndFamilyMismatch(t *testing.T) {
	endpoint := testEndpoint("node-1")
	endpoint.Port = 0
	endpoint.Family = control.FamilyIPv4

	err := control.ValidateEndpointCandidate(endpoint)
	if !errors.Is(err, control.ErrValidation) {
		t.Fatalf("expected validation error, got %v", err)
	}
}

func TestValidateEndpointPriorityMatchesPostgresIntegerRange(t *testing.T) {
	endpoint := testEndpoint("node-1")
	maxPriority := int(1<<31 - 1)
	endpoint.Priority = maxPriority
	if err := control.ValidateEndpointCandidate(endpoint); err != nil {
		t.Fatalf("PostgreSQL INTEGER maximum priority was rejected: %v", err)
	}

	if int64(^uint(0)>>1) > int64(maxPriority) {
		endpoint.Priority = maxPriority + 1
		if err := control.ValidateEndpointCandidate(endpoint); !errors.Is(err, control.ErrValidation) {
			t.Fatalf("expected priority above PostgreSQL INTEGER maximum to be rejected, got %v", err)
		}
	}
}

func TestValidateInviteRequiresHashedTokenAndFutureExpiry(t *testing.T) {
	invite := testInvite()
	invite.TokenHash = ""
	invite.ExpiresAt = invite.CreatedAt

	err := control.ValidateInvite(invite)
	if !errors.Is(err, control.ErrValidation) {
		t.Fatalf("expected validation error, got %v", err)
	}
}

func TestInviteIsExpiredAtExpiryTime(t *testing.T) {
	invite := testInvite()

	if invite.IsExpired(invite.ExpiresAt.Add(-time.Nanosecond)) {
		t.Fatal("invite expired before its expiry time")
	}
	if !invite.IsExpired(invite.ExpiresAt) {
		t.Fatal("invite remained valid at its expiry time")
	}
}

func TestValidateRelayAssignmentRejectsInvalidPort(t *testing.T) {
	assignment := testRelayAssignment()
	assignment.Port = 0

	err := control.ValidateRelayAssignment(assignment)
	if !errors.Is(err, control.ErrValidation) {
		t.Fatalf("expected validation error, got %v", err)
	}
}

func TestSnapshotModelCarriesVersionAndLocalAddress(t *testing.T) {
	snapshot := control.NetworkSnapshot{
		NetworkID:        "network-1",
		Generation:       7,
		ConfigVersion:    7,
		LocalNodeID:      "node-1",
		LocalVirtualIPv4: net.ParseIP("10.42.0.2").To4(),
		Peers:            []control.Peer{{NodeID: "node-2", VirtualIPv4: net.ParseIP("10.42.0.3").To4()}},
		RelayAssignment:  &control.RelayAssignment{ID: "relay-1"},
		GeneratedAt:      time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC),
	}

	if snapshot.Generation != snapshot.ConfigVersion || snapshot.LocalVirtualIPv4.To4() == nil {
		t.Fatalf("snapshot did not retain versioned local state: %+v", snapshot)
	}
}
