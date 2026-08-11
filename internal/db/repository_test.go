package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Eser-s-Organization/ipv6mesh/internal/control"
)

var repositoryTestNow = time.Date(2026, time.January, 10, 12, 0, 0, 0, time.UTC)

func repositoryTestTimePointer(value time.Time) *time.Time {
	return &value
}

func repositoryTestNetwork() control.Network {
	return control.Network{
		ID:       "network-1",
		Name:     "test network",
		IPv4Pool: "10.42.0.0/24",
		OwnerID:  "owner-1",
		CreatedAt: time.Date(
			2026, time.January, 1, 0, 0, 0, 0, time.UTC,
		),
	}
}

func repositoryTestNode(id, publicKey string) control.Node {
	return control.Node{
		ID:            id,
		DisplayName:   id,
		PublicKey:     publicKey,
		Platform:      "windows",
		ClientVersion: "0.1.0",
		LastSeen:      repositoryTestNow,
	}
}

func repositoryTestMembership(nodeID, address string, status control.MembershipStatus) control.Membership {
	return control.Membership{
		NetworkID:   "network-1",
		NodeID:      nodeID,
		VirtualIPv4: net.ParseIP(address).To4(),
		Role:        control.RoleMember,
		Status:      status,
	}
}

func repositoryTestInvite() control.Invite {
	return control.Invite{
		ID:        "invite-1",
		NetworkID: "network-1",
		TokenHash: "hash-1",
		CreatedAt: repositoryTestNow.Add(-time.Hour),
		ExpiresAt: repositoryTestNow.Add(time.Hour),
	}
}

func repositoryTestInviteRows(invite control.Invite) *sqlmock.Rows {
	var consumedAt any
	if invite.ConsumedAt != nil {
		consumedAt = *invite.ConsumedAt
	}
	var revokedAt any
	if invite.RevokedAt != nil {
		revokedAt = *invite.RevokedAt
	}
	var consumedByNodeID any
	if invite.ConsumedByNodeID != "" {
		consumedByNodeID = invite.ConsumedByNodeID
	}
	return sqlmock.NewRows([]string{
		"id", "network_id", "token_hash", "expires_at", "consumed_at", "revoked_at", "consumed_by_node_id", "created_at",
	}).AddRow(
		invite.ID, invite.NetworkID, invite.TokenHash, invite.ExpiresAt,
		consumedAt, revokedAt, consumedByNodeID, invite.CreatedAt,
	)
}

func repositoryTestEndpoint(nodeID string, address net.IP, observedAt time.Time) control.EndpointCandidate {
	family := control.FamilyIPv6
	if address.To4() != nil {
		family = control.FamilyIPv4
	}
	return control.EndpointCandidate{
		NodeID:     nodeID,
		Address:    address,
		Port:       51820,
		Family:     family,
		Interface:  "Ethernet",
		Priority:   1,
		ObservedAt: observedAt,
	}
}

func addRepositoryTestNetwork(t *testing.T, repository *MemoryRepository) {
	t.Helper()
	if err := repository.CreateNetwork(context.Background(), repositoryTestNetwork()); err != nil {
		t.Fatalf("create network: %v", err)
	}
}

func TestMemoryRepositoryRejectsDuplicatePublicKeys(t *testing.T) {
	repository := NewMemoryRepository()
	first := repositoryTestNode("node-1", "same-public-key")
	second := repositoryTestNode("node-2", "same-public-key")

	if err := repository.AddNode(context.Background(), first); err != nil {
		t.Fatalf("add first node: %v", err)
	}
	if err := repository.AddNode(context.Background(), second); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected duplicate public key conflict, got %v", err)
	}
}

func TestMemoryRepositoryTouchNodeUpdatesLastSeen(t *testing.T) {
	repository := NewMemoryRepository()
	original := repositoryTestNode("node-1", "key-1")
	if err := repository.AddNode(context.Background(), original); err != nil {
		t.Fatalf("add node: %v", err)
	}
	updatedAt := repositoryTestNow.Add(time.Hour)
	if err := repository.TouchNode(context.Background(), original.ID, updatedAt); err != nil {
		t.Fatalf("touch node: %v", err)
	}
	updated, err := repository.GetNode(context.Background(), original.ID)
	if err != nil {
		t.Fatalf("get touched node: %v", err)
	}
	if !updated.LastSeen.Equal(updatedAt) {
		t.Fatalf("LastSeen = %v, want %v", updated.LastSeen, updatedAt)
	}
}

func TestMemoryRepositoryGetNodeNetworkIDsReturnsNotFoundForUnknownNode(t *testing.T) {
	repository := NewMemoryRepository()
	networkIDs, err := repository.GetNodeNetworkIDs(context.Background(), "missing-node")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown node error = %v, want ErrNotFound", err)
	}
	if networkIDs != nil {
		t.Fatalf("unknown node network IDs = %#v, want nil", networkIDs)
	}
}

func TestMemoryRepositoryGetNodeNetworkIDsReturnsNonNilEmptyForNodeWithoutMembership(t *testing.T) {
	repository := NewMemoryRepository()
	if err := repository.AddNode(context.Background(), repositoryTestNode("node-1", "key-1")); err != nil {
		t.Fatalf("add node: %v", err)
	}
	networkIDs, err := repository.GetNodeNetworkIDs(context.Background(), "node-1")
	if err != nil {
		t.Fatalf("get node network IDs: %v", err)
	}
	if networkIDs == nil || len(networkIDs) != 0 {
		t.Fatalf("node without membership network IDs = %#v, want non-nil empty slice", networkIDs)
	}
}

func TestMemoryRepositoryGetNodeNetworkIDsReturnsSortedMultipleNetworks(t *testing.T) {
	repository := NewMemoryRepository()
	firstNetwork := repositoryTestNetwork()
	secondNetwork := firstNetwork
	secondNetwork.ID = "network-2"
	secondNetwork.IPv4Pool = "10.43.0.0/24"
	if err := repository.CreateNetwork(context.Background(), firstNetwork); err != nil {
		t.Fatalf("create first network: %v", err)
	}
	if err := repository.CreateNetwork(context.Background(), secondNetwork); err != nil {
		t.Fatalf("create second network: %v", err)
	}
	node := repositoryTestNode("node-1", "key-1")
	if err := repository.AddNode(context.Background(), node); err != nil {
		t.Fatalf("add node: %v", err)
	}
	firstMembership := repositoryTestMembership(node.ID, "10.42.0.2", control.MembershipActive)
	secondMembership := firstMembership
	secondMembership.NetworkID = secondNetwork.ID
	secondMembership.VirtualIPv4 = net.ParseIP("10.43.0.2").To4()
	if err := repository.AddMembership(context.Background(), firstMembership); err != nil {
		t.Fatalf("add first membership: %v", err)
	}
	if err := repository.AddMembership(context.Background(), secondMembership); err != nil {
		t.Fatalf("add second membership: %v", err)
	}
	networkIDs, err := repository.GetNodeNetworkIDs(context.Background(), node.ID)
	if err != nil {
		t.Fatalf("get node network IDs: %v", err)
	}
	if len(networkIDs) != 2 || networkIDs[0] != "network-1" || networkIDs[1] != "network-2" {
		t.Fatalf("node network IDs = %#v, want [network-1 network-2]", networkIDs)
	}
}

func TestPostgresTouchNodeUpdatesLastSeen(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer database.Close()
	repository := NewPostgresRepository(database)
	updatedAt := repositoryTestNow.Add(time.Hour)
	mock.ExpectExec(regexp.QuoteMeta("UPDATE nodes SET last_seen = $2 WHERE id = $1")).
		WithArgs("node-1", updatedAt).
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := repository.TouchNode(context.Background(), "node-1", updatedAt); err != nil {
		t.Fatalf("touch node: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected PostgreSQL calls: %v", err)
	}
}

func TestPostgresRepositoryGetNodeNetworkIDsMapsUnknownNodeToNotFound(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer database.Close()
	repository := NewPostgresRepository(database)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM nodes WHERE id = $1")).
		WithArgs("missing-node").
		WillReturnError(sql.ErrNoRows)
	networkIDs, err := repository.GetNodeNetworkIDs(context.Background(), "missing-node")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown node error = %v, want ErrNotFound", err)
	}
	if networkIDs != nil {
		t.Fatalf("unknown node network IDs = %#v, want nil", networkIDs)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected PostgreSQL calls: %v", err)
	}
}

func TestPostgresRepositoryGetNodeNetworkIDsReturnsNonNilEmptyForNodeWithoutMembership(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer database.Close()
	repository := NewPostgresRepository(database)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM nodes WHERE id = $1")).
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("node-1"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT network_id FROM memberships WHERE node_id = $1 ORDER BY network_id")).
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"network_id"}))
	networkIDs, err := repository.GetNodeNetworkIDs(context.Background(), "node-1")
	if err != nil {
		t.Fatalf("get node network IDs: %v", err)
	}
	if networkIDs == nil || len(networkIDs) != 0 {
		t.Fatalf("node without membership network IDs = %#v, want non-nil empty slice", networkIDs)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected PostgreSQL calls: %v", err)
	}
}

func TestPostgresRepositoryGetNodeNetworkIDsReturnsMultipleNetworksInOrder(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer database.Close()
	repository := NewPostgresRepository(database)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM nodes WHERE id = $1")).
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("node-1"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT network_id FROM memberships WHERE node_id = $1 ORDER BY network_id")).
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"network_id"}).AddRow("network-1").AddRow("network-2"))
	networkIDs, err := repository.GetNodeNetworkIDs(context.Background(), "node-1")
	if err != nil {
		t.Fatalf("get node network IDs: %v", err)
	}
	if len(networkIDs) != 2 || networkIDs[0] != "network-1" || networkIDs[1] != "network-2" {
		t.Fatalf("node network IDs = %#v, want [network-1 network-2]", networkIDs)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected PostgreSQL calls: %v", err)
	}
}

func TestMemoryRepositoryUpdatesNodeClientVersion(t *testing.T) {
	repository := NewMemoryRepository()
	node := repositoryTestNode("node-1", "key-1")
	if err := repository.AddNode(context.Background(), node); err != nil {
		t.Fatalf("add node: %v", err)
	}
	if err := repository.UpdateNodeClientVersion(context.Background(), node.ID, "0.2.0"); err != nil {
		t.Fatalf("update client version: %v", err)
	}
	updated, err := repository.GetNode(context.Background(), node.ID)
	if err != nil {
		t.Fatalf("get updated node: %v", err)
	}
	if updated.ClientVersion != "0.2.0" {
		t.Fatalf("ClientVersion = %q, want 0.2.0", updated.ClientVersion)
	}
}

func TestPostgresRepositoryUpdatesNodeClientVersion(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer database.Close()
	repository := NewPostgresRepository(database)
	mock.ExpectExec(regexp.QuoteMeta("UPDATE nodes SET client_version = $2 WHERE id = $1")).
		WithArgs("node-1", "0.2.0").
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := repository.UpdateNodeClientVersion(context.Background(), "node-1", "0.2.0"); err != nil {
		t.Fatalf("update client version: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected PostgreSQL calls: %v", err)
	}
}

func TestMemoryRepositoryRejectsDuplicateNetworkVirtualIPv4(t *testing.T) {
	repository := NewMemoryRepository()
	addRepositoryTestNetwork(t, repository)
	for _, node := range []control.Node{
		repositoryTestNode("node-1", "key-1"),
		repositoryTestNode("node-2", "key-2"),
	} {
		if err := repository.AddNode(context.Background(), node); err != nil {
			t.Fatalf("add node %s: %v", node.ID, err)
		}
	}

	address := "10.42.0.2"
	if err := repository.AddMembership(context.Background(), repositoryTestMembership("node-1", address, control.MembershipActive)); err != nil {
		t.Fatalf("add first membership: %v", err)
	}
	if err := repository.AddMembership(context.Background(), repositoryTestMembership("node-2", address, control.MembershipActive)); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected duplicate virtual IPv4 conflict, got %v", err)
	}
}

func TestMemoryRepositoryRejectsMembershipOutsideNetworkIPv4Pool(t *testing.T) {
	repository := NewMemoryRepository()
	addRepositoryTestNetwork(t, repository)
	if err := repository.AddNode(context.Background(), repositoryTestNode("node-1", "key-1")); err != nil {
		t.Fatalf("add node: %v", err)
	}

	membership := repositoryTestMembership("node-1", "10.43.0.2", control.MembershipActive)
	if err := repository.AddMembership(context.Background(), membership); !errors.Is(err, control.ErrValidation) {
		t.Fatalf("expected out-of-pool membership to be rejected, got %v", err)
	}
}

func TestMemoryRepositoryRejectsDuplicateEndpointsWithinReplacement(t *testing.T) {
	repository := NewMemoryRepository()
	if err := repository.AddNode(context.Background(), repositoryTestNode("node-1", "key-1")); err != nil {
		t.Fatalf("add node: %v", err)
	}
	endpoint := repositoryTestEndpoint("node-1", net.ParseIP("2001:db8::10"), repositoryTestNow)
	duplicate := endpoint

	if err := repository.ReplaceEndpoints(context.Background(), "node-1", []control.EndpointCandidate{endpoint, duplicate}); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected duplicate endpoint conflict, got %v", err)
	}
}

func TestMemoryRepositoryEnforcesGlobalRelayAssignmentID(t *testing.T) {
	repository := NewMemoryRepository()
	networkTwo := repositoryTestNetwork()
	networkTwo.ID = "network-2"
	networkTwo.IPv4Pool = "10.43.0.0/24"
	for _, network := range []control.Network{repositoryTestNetwork(), networkTwo} {
		if err := repository.CreateNetwork(context.Background(), network); err != nil {
			t.Fatalf("create network %s: %v", network.ID, err)
		}
	}
	for _, node := range []control.Node{
		repositoryTestNode("local-1", "key-local-1"),
		repositoryTestNode("relay-1", "key-relay-1"),
		repositoryTestNode("local-2", "key-local-2"),
		repositoryTestNode("relay-2", "key-relay-2"),
	} {
		if err := repository.AddNode(context.Background(), node); err != nil {
			t.Fatalf("add node %s: %v", node.ID, err)
		}
	}
	for _, membership := range []control.Membership{
		{NetworkID: "network-1", NodeID: "local-1", VirtualIPv4: net.ParseIP("10.42.0.2").To4(), Role: control.RoleMember, Status: control.MembershipActive},
		{NetworkID: "network-1", NodeID: "relay-1", VirtualIPv4: net.ParseIP("10.42.0.3").To4(), Role: control.RoleMember, Status: control.MembershipActive},
		{NetworkID: "network-2", NodeID: "local-2", VirtualIPv4: net.ParseIP("10.43.0.2").To4(), Role: control.RoleMember, Status: control.MembershipActive},
		{NetworkID: "network-2", NodeID: "relay-2", VirtualIPv4: net.ParseIP("10.43.0.3").To4(), Role: control.RoleMember, Status: control.MembershipActive},
	} {
		if err := repository.AddMembership(context.Background(), membership); err != nil {
			t.Fatalf("add membership %s/%s: %v", membership.NetworkID, membership.NodeID, err)
		}
	}
	assignment := control.RelayAssignment{
		ID:          "global-relay-id",
		NetworkID:   "network-1",
		NodeID:      "local-1",
		RelayNodeID: "relay-1",
		Address:     net.ParseIP("2001:db8::20"),
		Port:        51820,
		Family:      control.FamilyIPv6,
		Status:      control.RelayAssignmentActive,
		AssignedAt:  repositoryTestNow,
	}
	if err := repository.SetRelayAssignment(context.Background(), assignment); err != nil {
		t.Fatalf("set first relay assignment: %v", err)
	}
	if err := repository.SetRelayAssignment(context.Background(), assignment); err != nil {
		t.Fatalf("replacing same target with same relay ID should succeed: %v", err)
	}
	assignment.NetworkID = "network-2"
	assignment.NodeID = "local-2"
	assignment.RelayNodeID = "relay-2"
	if err := repository.SetRelayAssignment(context.Background(), assignment); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected global relay ID conflict, got %v", err)
	}
}

func TestMemoryRepositoryRejectsRelayAssignmentForInactiveMember(t *testing.T) {
	repository := NewMemoryRepository()
	addRepositoryTestNetwork(t, repository)
	for _, node := range []control.Node{
		repositoryTestNode("local", "key-local"),
		repositoryTestNode("relay", "key-relay"),
	} {
		if err := repository.AddNode(context.Background(), node); err != nil {
			t.Fatalf("add node %s: %v", node.ID, err)
		}
	}
	if err := repository.AddMembership(context.Background(), repositoryTestMembership("local", "10.42.0.2", control.MembershipActive)); err != nil {
		t.Fatalf("add local membership: %v", err)
	}
	if err := repository.AddMembership(context.Background(), repositoryTestMembership("relay", "10.42.0.3", control.MembershipPending)); err != nil {
		t.Fatalf("add pending relay membership: %v", err)
	}

	err := repository.SetRelayAssignment(context.Background(), control.RelayAssignment{
		ID:          "relay-assignment-1",
		NetworkID:   "network-1",
		NodeID:      "local",
		RelayNodeID: "relay",
		Address:     net.ParseIP("2001:db8::20"),
		Port:        51820,
		Family:      control.FamilyIPv6,
		Status:      control.RelayAssignmentActive,
		AssignedAt:  repositoryTestNow,
	})
	if !errors.Is(err, control.ErrValidation) {
		t.Fatalf("expected inactive relay membership to be rejected, got %v", err)
	}
}

func TestMemoryRepositoryRemovesRelayAssignmentWhenMembershipIsRemoved(t *testing.T) {
	repository := NewMemoryRepository()
	addRepositoryTestNetwork(t, repository)
	for _, node := range []control.Node{
		repositoryTestNode("local", "key-local"),
		repositoryTestNode("relay", "key-relay"),
	} {
		if err := repository.AddNode(context.Background(), node); err != nil {
			t.Fatalf("add node %s: %v", node.ID, err)
		}
	}
	for _, nodeID := range []string{"local", "relay"} {
		if err := repository.AddMembership(context.Background(), repositoryTestMembership(nodeID, map[string]string{"local": "10.42.0.2", "relay": "10.42.0.3"}[nodeID], control.MembershipActive)); err != nil {
			t.Fatalf("add membership %s: %v", nodeID, err)
		}
	}
	if err := repository.SetRelayAssignment(context.Background(), control.RelayAssignment{
		ID:          "relay-assignment-1",
		NetworkID:   "network-1",
		NodeID:      "local",
		RelayNodeID: "relay",
		Address:     net.ParseIP("2001:db8::20"),
		Port:        51820,
		Family:      control.FamilyIPv6,
		Status:      control.RelayAssignmentActive,
		AssignedAt:  repositoryTestNow,
	}); err != nil {
		t.Fatalf("set relay assignment: %v", err)
	}
	before, err := repository.GetNetwork(context.Background(), "network-1")
	if err != nil {
		t.Fatalf("get network before membership removal: %v", err)
	}
	if err := repository.RemoveMembership(context.Background(), "network-1", "relay"); err != nil {
		t.Fatalf("remove relay membership: %v", err)
	}
	after, err := repository.GetNetwork(context.Background(), "network-1")
	if err != nil {
		t.Fatalf("get network after membership removal: %v", err)
	}
	if after.ConfigVersion != before.ConfigVersion+1 {
		t.Fatalf("membership removal changed version by %d, want 1", after.ConfigVersion-before.ConfigVersion)
	}
	snapshot, err := repository.BuildSnapshotAt(context.Background(), "network-1", "local", repositoryTestNow)
	if err != nil {
		t.Fatalf("build snapshot after membership removal: %v", err)
	}
	if snapshot.RelayAssignment != nil {
		t.Fatalf("snapshot returned relay assignment after relay membership removal: %+v", snapshot.RelayAssignment)
	}
}

func TestMemoryRepositorySnapshotRejectsFutureAndExpiredRelayAssignments(t *testing.T) {
	tests := []struct {
		name       string
		assignedAt time.Time
		expiresAt  *time.Time
		wantRelay  bool
	}{
		{name: "future", assignedAt: repositoryTestNow.Add(time.Minute), wantRelay: false},
		{name: "current", assignedAt: repositoryTestNow, expiresAt: repositoryTestTimePointer(repositoryTestNow.Add(time.Minute)), wantRelay: true},
		{name: "expired", assignedAt: repositoryTestNow.Add(-2 * time.Minute), expiresAt: repositoryTestTimePointer(repositoryTestNow.Add(-time.Second)), wantRelay: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := NewMemoryRepository()
			addRepositoryTestNetwork(t, repository)
			for _, node := range []control.Node{
				repositoryTestNode("local", "key-local"),
				repositoryTestNode("relay", "key-relay"),
			} {
				if err := repository.AddNode(context.Background(), node); err != nil {
					t.Fatalf("add node %s: %v", node.ID, err)
				}
			}
			for _, membership := range []control.Membership{
				repositoryTestMembership("local", "10.42.0.2", control.MembershipActive),
				repositoryTestMembership("relay", "10.42.0.3", control.MembershipActive),
			} {
				if err := repository.AddMembership(context.Background(), membership); err != nil {
					t.Fatalf("add membership %s: %v", membership.NodeID, err)
				}
			}
			if err := repository.SetRelayAssignment(context.Background(), control.RelayAssignment{
				ID:          "relay-assignment-1",
				NetworkID:   "network-1",
				NodeID:      "local",
				RelayNodeID: "relay",
				Address:     net.ParseIP("2001:db8::20"),
				Port:        51820,
				Family:      control.FamilyIPv6,
				Status:      control.RelayAssignmentActive,
				AssignedAt:  test.assignedAt,
				ExpiresAt:   test.expiresAt,
			}); err != nil {
				t.Fatalf("set relay assignment: %v", err)
			}

			snapshot, err := repository.BuildSnapshotAt(context.Background(), "network-1", "local", repositoryTestNow)
			if err != nil {
				t.Fatalf("build snapshot: %v", err)
			}
			if gotRelay := snapshot.RelayAssignment != nil; gotRelay != test.wantRelay {
				t.Fatalf("relay presence = %t, want %t: %+v", gotRelay, test.wantRelay, snapshot.RelayAssignment)
			}
		})
	}
}

func TestMemoryRepositoryRemovingRelayNodeAdvancesVersionOnceAndRemovesAssignment(t *testing.T) {
	repository := NewMemoryRepository()
	addRepositoryTestNetwork(t, repository)
	for _, node := range []control.Node{
		repositoryTestNode("local", "key-local"),
		repositoryTestNode("relay", "key-relay"),
	} {
		if err := repository.AddNode(context.Background(), node); err != nil {
			t.Fatalf("add node %s: %v", node.ID, err)
		}
	}
	for _, nodeID := range []string{"local", "relay"} {
		if err := repository.AddMembership(context.Background(), repositoryTestMembership(nodeID, map[string]string{"local": "10.42.0.2", "relay": "10.42.0.3"}[nodeID], control.MembershipActive)); err != nil {
			t.Fatalf("add membership %s: %v", nodeID, err)
		}
	}
	if err := repository.SetRelayAssignment(context.Background(), control.RelayAssignment{
		ID:          "relay-assignment-1",
		NetworkID:   "network-1",
		NodeID:      "local",
		RelayNodeID: "relay",
		Address:     net.ParseIP("2001:db8::20"),
		Port:        51820,
		Family:      control.FamilyIPv6,
		Status:      control.RelayAssignmentActive,
		AssignedAt:  repositoryTestNow,
	}); err != nil {
		t.Fatalf("set relay assignment: %v", err)
	}
	before, err := repository.GetNetwork(context.Background(), "network-1")
	if err != nil {
		t.Fatalf("get network before node removal: %v", err)
	}
	if err := repository.RemoveNode(context.Background(), "relay"); err != nil {
		t.Fatalf("remove relay node: %v", err)
	}
	after, err := repository.GetNetwork(context.Background(), "network-1")
	if err != nil {
		t.Fatalf("get network after node removal: %v", err)
	}
	if after.ConfigVersion != before.ConfigVersion+1 {
		t.Fatalf("node removal changed version by %d, want 1", after.ConfigVersion-before.ConfigVersion)
	}
	snapshot, err := repository.BuildSnapshotAt(context.Background(), "network-1", "local", repositoryTestNow)
	if err != nil {
		t.Fatalf("build snapshot after node removal: %v", err)
	}
	if snapshot.RelayAssignment != nil {
		t.Fatalf("snapshot returned relay assignment after relay node removal: %+v", snapshot.RelayAssignment)
	}
}

func TestMemoryRepositoryReturnsNotFoundForUnknownDeletion(t *testing.T) {
	repository := NewMemoryRepository()

	if err := repository.RemoveNode(context.Background(), "missing-node"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected missing node to be not found, got %v", err)
	}
	if err := repository.RemoveMembership(context.Background(), "missing-network", "missing-node"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected missing membership to be not found, got %v", err)
	}
}

func TestMemoryRepositoryRejectsExpiredInvite(t *testing.T) {
	repository := NewMemoryRepository()
	addRepositoryTestNetwork(t, repository)
	invite := repositoryTestInvite()
	invite.ExpiresAt = repositoryTestNow
	if err := repository.CreateInvite(context.Background(), invite); err != nil {
		t.Fatalf("create invite: %v", err)
	}

	if _, err := repository.ConsumeInvite(context.Background(), invite.ID, invite.TokenHash, repositoryTestNow); !errors.Is(err, ErrInviteExpired) {
		t.Fatalf("expected expired invite error, got %v", err)
	}
}

func TestMemoryRepositoryUsesCurrentTimeWhenConsumptionTimeIsZero(t *testing.T) {
	repository := NewMemoryRepository()
	addRepositoryTestNetwork(t, repository)
	invite := repositoryTestInvite()
	invite.ExpiresAt = time.Now().Add(-time.Hour)
	if err := repository.CreateInvite(context.Background(), invite); err != nil {
		t.Fatalf("create invite: %v", err)
	}

	if _, err := repository.ConsumeInvite(context.Background(), invite.ID, invite.TokenHash, time.Time{}); !errors.Is(err, ErrInviteExpired) {
		t.Fatalf("expected expired invite error when time is omitted, got %v", err)
	}
}

func TestMemoryRepositoryRejectsInviteConsumptionBeforeCreation(t *testing.T) {
	repository := NewMemoryRepository()
	addRepositoryTestNetwork(t, repository)
	invite := repositoryTestInvite()
	if err := repository.CreateInvite(context.Background(), invite); err != nil {
		t.Fatalf("create invite: %v", err)
	}

	if _, err := repository.ConsumeInvite(context.Background(), invite.ID, invite.TokenHash, invite.CreatedAt.Add(-time.Nanosecond)); !errors.Is(err, control.ErrValidation) {
		t.Fatalf("expected early consumption to be rejected, got %v", err)
	}
	if _, err := repository.ConsumeInvite(context.Background(), invite.ID, invite.TokenHash, repositoryTestNow); err != nil {
		t.Fatalf("invite should remain available after rejected consumption: %v", err)
	}
}

func TestMemoryRepositoryRejectsInviteForMissingConsumedNode(t *testing.T) {
	repository := NewMemoryRepository()
	addRepositoryTestNetwork(t, repository)
	invite := repositoryTestInvite()
	invite.ConsumedByNodeID = "missing-node"
	consumedAt := repositoryTestNow
	invite.ConsumedAt = &consumedAt

	if err := repository.CreateInvite(context.Background(), invite); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected missing consumed node to return ErrNotFound, got %v", err)
	}
}

func TestMemoryRepositoryClearsConsumedInviteNodeWhenNodeIsRemoved(t *testing.T) {
	repository := NewMemoryRepository()
	addRepositoryTestNetwork(t, repository)
	if err := repository.AddNode(context.Background(), repositoryTestNode("node-1", "key-1")); err != nil {
		t.Fatalf("add node: %v", err)
	}
	invite := repositoryTestInvite()
	invite.ConsumedByNodeID = "node-1"
	consumedAt := repositoryTestNow
	invite.ConsumedAt = &consumedAt
	if err := repository.CreateInvite(context.Background(), invite); err != nil {
		t.Fatalf("create consumed invite: %v", err)
	}

	if err := repository.RemoveNode(context.Background(), "node-1"); err != nil {
		t.Fatalf("remove node: %v", err)
	}

	repository.mu.RLock()
	storedInvite := repository.invites[invite.ID]
	repository.mu.RUnlock()
	if storedInvite.ConsumedByNodeID != "" {
		t.Fatalf("removed node remained on consumed invite: %q", storedInvite.ConsumedByNodeID)
	}
}

func TestMemoryRepositoryConsumesInviteOnlyOnceConcurrently(t *testing.T) {
	repository := NewMemoryRepository()
	addRepositoryTestNetwork(t, repository)
	invite := repositoryTestInvite()
	if err := repository.CreateInvite(context.Background(), invite); err != nil {
		t.Fatalf("create invite: %v", err)
	}

	const attempts = 32
	results := make(chan error, attempts)
	var waitGroup sync.WaitGroup
	for range attempts {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			_, err := repository.ConsumeInvite(context.Background(), invite.ID, invite.TokenHash, repositoryTestNow)
			results <- err
		}()
	}
	waitGroup.Wait()
	close(results)

	successes := 0
	consumedErrors := 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrInviteConsumed) {
			consumedErrors++
		} else {
			t.Errorf("unexpected concurrent consumption error: %v", err)
		}
	}
	if successes != 1 || consumedErrors != attempts-1 {
		t.Fatalf("expected one successful consumption and %d consumed errors, got %d and %d", attempts-1, successes, consumedErrors)
	}
}

func TestMemoryRepositoryInviteIDTakesPrecedenceOverNetworkLookup(t *testing.T) {
	repository := NewMemoryRepository()
	addRepositoryTestNetwork(t, repository)
	network := repositoryTestNetwork()
	network.ID = "shared-scope"
	if err := repository.CreateNetwork(context.Background(), network); err != nil {
		t.Fatalf("create overlapping-scope network: %v", err)
	}
	idInvite := repositoryTestInvite()
	idInvite.ID = "shared-scope"
	idInvite.TokenHash = "id-token"
	networkInvite := repositoryTestInvite()
	networkInvite.ID = "network-invite"
	networkInvite.NetworkID = "shared-scope"
	networkInvite.TokenHash = "network-token"
	for _, invite := range []control.Invite{idInvite, networkInvite} {
		if err := repository.CreateInvite(context.Background(), invite); err != nil {
			t.Fatalf("create invite %s: %v", invite.ID, err)
		}
	}

	if _, err := repository.ConsumeInvite(context.Background(), "shared-scope", "network-token", repositoryTestNow); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected conflicting invite ID to take precedence, got %v", err)
	}
}

func TestMemoryRepositoryConsumeInviteForNodeRecordsConsumer(t *testing.T) {
	repository := NewMemoryRepository()
	addRepositoryTestNetwork(t, repository)
	if err := repository.AddNode(context.Background(), repositoryTestNode("consumer-node", "key-consumer")); err != nil {
		t.Fatalf("add consumer node: %v", err)
	}
	invite := repositoryTestInvite()
	if err := repository.CreateInvite(context.Background(), invite); err != nil {
		t.Fatalf("create invite: %v", err)
	}

	consumed, err := repository.ConsumeInviteForNode(context.Background(), invite.ID, invite.TokenHash, repositoryTestNow, "consumer-node")
	if err != nil {
		t.Fatalf("consume invite for node: %v", err)
	}
	if consumed.ConsumedByNodeID != "consumer-node" || consumed.ConsumedAt == nil {
		t.Fatalf("consumer was not recorded: %+v", consumed)
	}
}

func TestMemoryRepositoryConsumeInviteForNodeRejectsMissingConsumer(t *testing.T) {
	repository := NewMemoryRepository()
	addRepositoryTestNetwork(t, repository)
	invite := repositoryTestInvite()
	if err := repository.CreateInvite(context.Background(), invite); err != nil {
		t.Fatalf("create invite: %v", err)
	}

	if _, err := repository.ConsumeInviteForNode(context.Background(), invite.ID, invite.TokenHash, repositoryTestNow, "missing-node"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected missing consumer to return ErrNotFound, got %v", err)
	}
	consumed, err := repository.ConsumeInvite(context.Background(), invite.ID, invite.TokenHash, repositoryTestNow)
	if err != nil {
		t.Fatalf("compatibility consume after rejected consumer: %v", err)
	}
	if consumed.ConsumedByNodeID != "" {
		t.Fatalf("rejected consumer was recorded: %+v", consumed)
	}
}

func TestMemoryRepositoryFiltersStaleEndpointsAndInactiveMembers(t *testing.T) {
	repository := NewMemoryRepository()
	addRepositoryTestNetwork(t, repository)
	for _, node := range []control.Node{
		repositoryTestNode("local", "key-local"),
		repositoryTestNode("active-peer", "key-active"),
		repositoryTestNode("pending-peer", "key-pending"),
		repositoryTestNode("revoked-peer", "key-revoked"),
	} {
		if err := repository.AddNode(context.Background(), node); err != nil {
			t.Fatalf("add node %s: %v", node.ID, err)
		}
	}
	for _, membership := range []control.Membership{
		repositoryTestMembership("local", "10.42.0.2", control.MembershipActive),
		repositoryTestMembership("active-peer", "10.42.0.3", control.MembershipActive),
		repositoryTestMembership("pending-peer", "10.42.0.4", control.MembershipPending),
		repositoryTestMembership("revoked-peer", "10.42.0.5", control.MembershipRevoked),
	} {
		if err := repository.AddMembership(context.Background(), membership); err != nil {
			t.Fatalf("add membership %s: %v", membership.NodeID, err)
		}
	}

	fresh := repositoryTestNow.Add(-DefaultEndpointMaxAge + time.Second)
	stale := repositoryTestNow.Add(-DefaultEndpointMaxAge - time.Second)
	endpoints := []control.EndpointCandidate{
		repositoryTestEndpoint("active-peer", net.ParseIP("2001:db8::10"), fresh),
		repositoryTestEndpoint("active-peer", net.ParseIP("2001:db8::11"), stale),
	}
	if err := repository.ReplaceEndpoints(context.Background(), "active-peer", endpoints); err != nil {
		t.Fatalf("replace endpoints: %v", err)
	}

	if err := repository.SetRelayAssignment(context.Background(), control.RelayAssignment{
		ID:          "relay-1",
		NetworkID:   "network-1",
		NodeID:      "active-peer",
		RelayNodeID: "local",
		Address:     net.ParseIP("2001:db8::20"),
		Port:        51820,
		Family:      control.FamilyIPv6,
		Status:      control.RelayAssignmentActive,
		AssignedAt:  repositoryTestNow.Add(-time.Minute),
	}); err != nil {
		t.Fatalf("set relay assignment: %v", err)
	}

	snapshot, err := repository.BuildSnapshotAt(context.Background(), "network-1", "local", repositoryTestNow)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	if len(snapshot.Peers) != 1 || snapshot.Peers[0].NodeID != "active-peer" {
		t.Fatalf("snapshot included non-active members: %+v", snapshot.Peers)
	}
	if len(snapshot.Peers[0].Endpoints) != 1 || !snapshot.Peers[0].Endpoints[0].Address.Equal(net.ParseIP("2001:db8::10")) {
		t.Fatalf("snapshot did not filter stale endpoint: %+v", snapshot.Peers[0].Endpoints)
	}
	if snapshot.RelayAssignment != nil {
		t.Fatalf("snapshot exposed another peer's relay assignment: %+v", snapshot.RelayAssignment)
	}
	if err := repository.SetRelayAssignment(context.Background(), control.RelayAssignment{
		ID:          "relay-local",
		NetworkID:   "network-1",
		NodeID:      "local",
		RelayNodeID: "active-peer",
		Address:     net.ParseIP("2001:db8::21"),
		Port:        51820,
		Family:      control.FamilyIPv6,
		Status:      control.RelayAssignmentActive,
		AssignedAt:  repositoryTestNow.Add(-time.Minute),
	}); err != nil {
		t.Fatalf("set local relay assignment: %v", err)
	}
	snapshot, err = repository.BuildSnapshotAt(context.Background(), "network-1", "local", repositoryTestNow)
	if err != nil {
		t.Fatalf("build snapshot with local relay: %v", err)
	}
	if snapshot.RelayAssignment == nil || snapshot.RelayAssignment.ID != "relay-local" {
		t.Fatalf("snapshot did not include local relay assignment: %+v", snapshot.RelayAssignment)
	}
}

func TestMemoryRepositoryOrdersSnapshotEndpointsDeterministically(t *testing.T) {
	repository := NewMemoryRepository()
	addRepositoryTestNetwork(t, repository)
	for _, node := range []control.Node{
		repositoryTestNode("local", "key-local"),
		repositoryTestNode("peer", "key-peer"),
	} {
		if err := repository.AddNode(context.Background(), node); err != nil {
			t.Fatalf("add node %s: %v", node.ID, err)
		}
	}
	for _, membership := range []control.Membership{
		repositoryTestMembership("local", "10.42.0.2", control.MembershipActive),
		repositoryTestMembership("peer", "10.42.0.3", control.MembershipActive),
	} {
		if err := repository.AddMembership(context.Background(), membership); err != nil {
			t.Fatalf("add membership %s: %v", membership.NodeID, err)
		}
	}
	endpoints := []control.EndpointCandidate{
		{NodeID: "peer", Address: net.ParseIP("2001:db8::20"), Port: 51820, Family: control.FamilyIPv6, Interface: "WiFi", Priority: 2, ObservedAt: repositoryTestNow},
		{NodeID: "peer", Address: net.ParseIP("2001:db8::10"), Port: 51821, Family: control.FamilyIPv6, Interface: "Ethernet", Priority: 1, ObservedAt: repositoryTestNow},
		{NodeID: "peer", Address: net.ParseIP("2001:db8::30"), Port: 51820, Family: control.FamilyIPv6, Interface: "Ethernet", Priority: 1, ObservedAt: repositoryTestNow},
		{NodeID: "peer", Address: net.ParseIP("2001:db8::10"), Port: 51820, Family: control.FamilyIPv6, Interface: "WiFi", Priority: 1, ObservedAt: repositoryTestNow},
		{NodeID: "peer", Address: net.ParseIP("2001:db8::10"), Port: 51820, Family: control.FamilyIPv6, Interface: "Ethernet", Priority: 1, ObservedAt: repositoryTestNow},
	}
	if err := repository.ReplaceEndpoints(context.Background(), "peer", endpoints); err != nil {
		t.Fatalf("replace endpoints: %v", err)
	}

	snapshot, err := repository.BuildSnapshotAt(context.Background(), "network-1", "local", repositoryTestNow)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	want := []struct {
		address       string
		port          uint16
		interfaceName string
	}{
		{address: "2001:db8::10", port: 51820, interfaceName: "Ethernet"},
		{address: "2001:db8::10", port: 51820, interfaceName: "WiFi"},
		{address: "2001:db8::10", port: 51821, interfaceName: "Ethernet"},
		{address: "2001:db8::30", port: 51820, interfaceName: "Ethernet"},
		{address: "2001:db8::20", port: 51820, interfaceName: "WiFi"},
	}
	if len(snapshot.Peers) != 1 || len(snapshot.Peers[0].Endpoints) != len(want) {
		t.Fatalf("unexpected snapshot endpoint count: %+v", snapshot.Peers)
	}
	for index, expected := range want {
		got := snapshot.Peers[0].Endpoints[index]
		if got.Address.String() != expected.address || got.Port != expected.port || got.Interface != expected.interfaceName {
			t.Errorf("endpoint %d = %s:%d/%s, want %s:%d/%s", index, got.Address, got.Port, got.Interface, expected.address, expected.port, expected.interfaceName)
		}
	}
}

func TestMemoryRepositoryIncrementsSnapshotVersionAndIsolatesCopies(t *testing.T) {
	repository := NewMemoryRepository()
	addRepositoryTestNetwork(t, repository)
	for _, node := range []control.Node{
		repositoryTestNode("local", "key-local"),
		repositoryTestNode("peer", "key-peer"),
	} {
		if err := repository.AddNode(context.Background(), node); err != nil {
			t.Fatalf("add node %s: %v", node.ID, err)
		}
	}

	initial, err := repository.GetNetwork(context.Background(), "network-1")
	if err != nil {
		t.Fatalf("get initial network: %v", err)
	}
	if initial.ConfigVersion == 0 {
		t.Fatal("new network must have a non-zero config version")
	}
	if err := repository.AddMembership(context.Background(), repositoryTestMembership("local", "10.42.0.2", control.MembershipActive)); err != nil {
		t.Fatalf("add local membership: %v", err)
	}
	if err := repository.AddMembership(context.Background(), repositoryTestMembership("peer", "10.42.0.3", control.MembershipActive)); err != nil {
		t.Fatalf("add peer membership: %v", err)
	}
	withMemberships, err := repository.GetNetwork(context.Background(), "network-1")
	if err != nil {
		t.Fatalf("get network after memberships: %v", err)
	}
	if withMemberships.ConfigVersion <= initial.ConfigVersion {
		t.Fatalf("membership change did not increment version: %d -> %d", initial.ConfigVersion, withMemberships.ConfigVersion)
	}

	endpoints := []control.EndpointCandidate{repositoryTestEndpoint("peer", net.ParseIP("2001:db8::10"), repositoryTestNow)}
	if err := repository.ReplaceEndpoints(context.Background(), "peer", endpoints); err != nil {
		t.Fatalf("replace endpoints: %v", err)
	}
	withEndpoints, err := repository.GetNetwork(context.Background(), "network-1")
	if err != nil {
		t.Fatalf("get network after endpoints: %v", err)
	}
	if withEndpoints.ConfigVersion <= withMemberships.ConfigVersion {
		t.Fatalf("endpoint change did not increment version: %d -> %d", withMemberships.ConfigVersion, withEndpoints.ConfigVersion)
	}

	snapshot, err := repository.BuildSnapshotAt(context.Background(), "network-1", "local", repositoryTestNow)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	if snapshot.Generation != withEndpoints.ConfigVersion || snapshot.ConfigVersion != withEndpoints.ConfigVersion {
		t.Fatalf("snapshot version mismatch: snapshot=%+v network=%d", snapshot, withEndpoints.ConfigVersion)
	}

	snapshot.Peers[0].VirtualIPv4[0] = 192
	snapshot.Peers[0].Endpoints[0].Address[0] = 0
	if snapshot.RelayAssignment != nil {
		snapshot.RelayAssignment.ID = "mutated"
	}
	secondSnapshot, err := repository.BuildSnapshotAt(context.Background(), "network-1", "local", repositoryTestNow)
	if err != nil {
		t.Fatalf("build second snapshot: %v", err)
	}
	if secondSnapshot.Peers[0].VirtualIPv4.Equal(net.ParseIP("10.42.0.3")) == false {
		t.Fatalf("snapshot virtual address leaked internal slice: %v", secondSnapshot.Peers[0].VirtualIPv4)
	}
	if !secondSnapshot.Peers[0].Endpoints[0].Address.Equal(net.ParseIP("2001:db8::10")) {
		t.Fatalf("snapshot endpoint leaked internal slice: %v", secondSnapshot.Peers[0].Endpoints[0].Address)
	}
}

func TestMemoryRepositoryOrdersPeersByNodeID(t *testing.T) {
	repository := NewMemoryRepository()
	addRepositoryTestNetwork(t, repository)
	for _, node := range []control.Node{
		repositoryTestNode("local", "key-local"),
		repositoryTestNode("z-peer", "key-z"),
		repositoryTestNode("A-peer", "key-A"),
		repositoryTestNode("a-peer", "key-a"),
	} {
		if err := repository.AddNode(context.Background(), node); err != nil {
			t.Fatalf("add node %s: %v", node.ID, err)
		}
	}
	for _, membership := range []control.Membership{
		repositoryTestMembership("local", "10.42.0.2", control.MembershipActive),
		repositoryTestMembership("z-peer", "10.42.0.3", control.MembershipActive),
		repositoryTestMembership("A-peer", "10.42.0.4", control.MembershipActive),
		repositoryTestMembership("a-peer", "10.42.0.5", control.MembershipActive),
	} {
		if err := repository.AddMembership(context.Background(), membership); err != nil {
			t.Fatalf("add membership %s: %v", membership.NodeID, err)
		}
	}

	snapshot, err := repository.BuildSnapshotAt(context.Background(), "network-1", "local", repositoryTestNow)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	want := []string{"A-peer", "a-peer", "z-peer"}
	if len(snapshot.Peers) != len(want) {
		t.Fatalf("unexpected peer count: %+v", snapshot.Peers)
	}
	for index, nodeID := range want {
		if snapshot.Peers[index].NodeID != nodeID {
			t.Errorf("peer %d = %q, want %q", index, snapshot.Peers[index].NodeID, nodeID)
		}
	}
}

func TestSchemaFixtureMatchesCanonicalSchemaAndProtectsSecrets(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", ".."))
	canonicalPath := filepath.Join(repoRoot, "internal", "db", "schema.sql")
	fixturePath := filepath.Join(repoRoot, "test", "testdata", "schema.sql")
	canonical, err := os.ReadFile(canonicalPath)
	if err != nil {
		t.Fatalf("read canonical schema: %v", err)
	}
	fixture, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read test schema: %v", err)
	}
	if string(canonical) != string(fixture) {
		t.Fatal("test schema drifted from canonical schema")
	}

	schema := strings.ToLower(string(canonical))
	for _, requiredFragment := range []string{
		"create table if not exists networks",
		"create table if not exists nodes",
		"create table if not exists memberships",
		"create table if not exists invites",
		"create table if not exists endpoint_candidates",
		"create table if not exists relay_assignments",
		"create table if not exists audit_events",
		"token_hash",
		"address inet",
		"references",
		"consumed_by_node_id is null or consumed_at is not null",
		"foreign key (network_id, node_id)",
		"foreign key (network_id, relay_node_id)",
		"references memberships(network_id, node_id)",
	} {
		if !strings.Contains(schema, requiredFragment) {
			t.Errorf("schema missing required fragment %q", requiredFragment)
		}
	}
	if strings.Contains(schema, "token text") || strings.Contains(schema, "raw_token") {
		t.Fatal("schema exposes a plaintext invite token column")
	}
}

type fakeRowScanner struct {
	values    []any
	scanError error
}

func (row fakeRowScanner) Scan(dest ...any) error {
	if row.scanError != nil {
		return row.scanError
	}
	if len(dest) != len(row.values) {
		return errors.New("scan destination count mismatch")
	}
	for index, value := range row.values {
		switch target := dest[index].(type) {
		case *string:
			var ok bool
			*target, ok = value.(string)
			if !ok {
				return errors.New("expected string destination")
			}
		case *int64:
			var ok bool
			*target, ok = value.(int64)
			if !ok {
				return errors.New("expected int64 destination")
			}
		case *time.Time:
			var ok bool
			*target, ok = value.(time.Time)
			if !ok {
				return errors.New("expected time destination")
			}
		case *sql.NullTime:
			target.Valid = false
			if value == nil {
				continue
			}
			var ok bool
			target.Time, ok = value.(time.Time)
			if !ok {
				return errors.New("expected nullable time destination")
			}
			target.Valid = true
		default:
			return errors.New("unsupported scan destination")
		}
	}
	return nil
}

func TestScanNetworkRowParsesIPv4PoolAtSQLBoundary(t *testing.T) {
	createdAt := repositoryTestNetwork().CreatedAt
	network, err := scanNetworkRow(fakeRowScanner{values: []any{
		"network-1",
		"test network",
		"10.42.0.0/24",
		"owner-1",
		int64(9),
		createdAt,
	}})
	if err != nil {
		t.Fatalf("scan network: %v", err)
	}
	if network.IPv4Pool != "10.42.0.0/24" || network.ConfigVersion != 9 {
		t.Fatalf("unexpected scanned network: %+v", network)
	}
}

func TestScanNetworkRowMapsMissingNetworkToNotFound(t *testing.T) {
	_, err := scanNetworkRow(fakeRowScanner{scanError: sql.ErrNoRows})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected missing network to map to ErrNotFound, got %v", err)
	}
}

func TestPostgresRemoveNodeQueryPlanCoversMembershipAndRelayNetworks(t *testing.T) {
	queryPlan := strings.ToLower(strings.Join(removeNodeMutationQueries(), "\n"))
	for _, fragment := range []string{
		"select distinct network_id",
		"from memberships",
		"relay_node_id",
		"delete from nodes",
		"update networks set config_version = config_version + 1",
	} {
		if !strings.Contains(queryPlan, fragment) {
			t.Errorf("remove-node query plan missing %q", fragment)
		}
	}
}

func TestPostgresSnapshotReadUsesRepeatableReadOnlyTransaction(t *testing.T) {
	options := snapshotReadTransactionOptions()
	if options.Isolation != sql.LevelRepeatableRead || !options.ReadOnly {
		t.Fatalf("unexpected snapshot transaction options: %+v", options)
	}
}

func TestPostgresRelayQueriesRequireActiveTargetAndRelayMemberships(t *testing.T) {
	membershipQuery := strings.ToLower(membershipStatusQuery)
	for _, fragment := range []string{
		"select status",
		"from memberships",
		"where network_id = $1 and node_id = $2",
	} {
		if !strings.Contains(membershipQuery, fragment) {
			t.Errorf("membership status query missing %q: %s", fragment, membershipStatusQuery)
		}
	}
	snapshotQuery := strings.ToLower(snapshotRelayAssignmentsQuery)
	for _, fragment := range []string{
		"join memberships as target_membership",
		"join memberships as relay_membership",
		"target_membership.status = 'active'",
		"relay_membership.status = 'active'",
	} {
		if !strings.Contains(snapshotQuery, fragment) {
			t.Errorf("snapshot relay query missing %q", fragment)
		}
	}
}

func TestScanNodeRowMapsNullableLastSeen(t *testing.T) {
	node, err := scanNodeRow(fakeRowScanner{values: []any{
		"node-1",
		"node",
		"public-key",
		"windows",
		"0.1.0",
		nil,
	}})
	if err != nil {
		t.Fatalf("scan node: %v", err)
	}
	if !node.LastSeen.IsZero() {
		t.Fatalf("expected null last_seen to map to zero time, got %v", node.LastSeen)
	}
}

func TestPostgresRepositoryCanBeConstructedWithoutConnecting(t *testing.T) {
	repository := NewPostgresRepository(nil)
	if repository == nil {
		t.Fatal("nil database should still construct an explicit repository")
	}
	if err := repository.CreateNetwork(context.Background(), repositoryTestNetwork()); !errors.Is(err, ErrDatabaseUnavailable) {
		t.Fatalf("expected explicit database-unavailable error, got %v", err)
	}

	var _ Repository = repository
	var _ SQLExecutor = (*sql.DB)(nil)
}

func TestPostgresCreateNetworkBindsInsertParameters(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer database.Close()
	repository := NewPostgresRepository(database)
	network := repositoryTestNetwork()
	network.ConfigVersion = 7

	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO networks")).
		WithArgs(network.ID, network.Name, network.IPv4Pool, network.OwnerID, int64(7), network.CreatedAt).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := repository.CreateNetwork(context.Background(), network); err != nil {
		t.Fatalf("create network: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected PostgreSQL calls: %v", err)
	}
}

func TestPostgresAddNodeBindsInsertParameters(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer database.Close()
	repository := NewPostgresRepository(database)
	node := repositoryTestNode("node-1", "key-1")

	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO nodes")).
		WithArgs(node.ID, node.DisplayName, node.PublicKey, node.Platform, node.ClientVersion, node.LastSeen).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := repository.AddNode(context.Background(), node); err != nil {
		t.Fatalf("add node: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected PostgreSQL calls: %v", err)
	}
}

func TestPostgresCreateInviteBindsInsertParameters(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer database.Close()
	repository := NewPostgresRepository(database)
	invite := repositoryTestInvite()
	consumedAt := repositoryTestNow
	invite.ConsumedAt = &consumedAt

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM networks WHERE id = $1 FOR UPDATE")).
		WithArgs(invite.NetworkID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(invite.NetworkID))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO invites")).
		WithArgs(invite.ID, invite.NetworkID, invite.TokenHash, invite.ExpiresAt, consumedAt, nil, "", invite.CreatedAt).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := repository.CreateInvite(context.Background(), invite); err != nil {
		t.Fatalf("create invite: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected PostgreSQL calls: %v", err)
	}
}

func TestPostgresCreateInviteWithConsumerLocksNodeBeforeNetwork(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer database.Close()
	repository := NewPostgresRepository(database)
	invite := repositoryTestInvite()
	consumedAt := repositoryTestNow
	invite.ConsumedAt = &consumedAt
	invite.ConsumedByNodeID = "consumer-node"

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM nodes WHERE id = $1 FOR UPDATE")).
		WithArgs(invite.ConsumedByNodeID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(invite.ConsumedByNodeID))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM networks WHERE id = $1 FOR UPDATE")).
		WithArgs(invite.NetworkID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(invite.NetworkID))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO invites")).
		WithArgs(invite.ID, invite.NetworkID, invite.TokenHash, invite.ExpiresAt, consumedAt, nil, invite.ConsumedByNodeID, invite.CreatedAt).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := repository.CreateInvite(context.Background(), invite); err != nil {
		t.Fatalf("create invite: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected PostgreSQL calls: %v", err)
	}
}

func TestPostgresAddMembershipLocksNodeBeforeNetwork(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer database.Close()
	repository := NewPostgresRepository(database)
	membership := repositoryTestMembership("node-1", "10.42.0.2", control.MembershipActive)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM nodes WHERE id = $1 FOR UPDATE")).
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("node-1"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, ipv4_pool::text FROM networks WHERE id = $1 FOR UPDATE")).
		WithArgs("network-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "ipv4_pool"}).AddRow("network-1", "10.42.0.0/24"))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO memberships")).
		WithArgs("network-1", "node-1", "10.42.0.2", control.RoleMember, control.MembershipActive).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE networks SET config_version = config_version + 1 WHERE id = $1")).
		WithArgs("network-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := repository.AddMembership(context.Background(), membership); err != nil {
		t.Fatalf("add membership: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected PostgreSQL calls: %v", err)
	}
}

func TestPostgresAddMembershipRejectsAddressOutsideLockedNetworkPool(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer database.Close()
	repository := NewPostgresRepository(database)
	membership := repositoryTestMembership("node-1", "10.43.0.2", control.MembershipActive)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM nodes WHERE id = $1 FOR UPDATE")).
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("node-1"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, ipv4_pool::text FROM networks WHERE id = $1 FOR UPDATE")).
		WithArgs("network-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "ipv4_pool"}).AddRow("network-1", "10.42.0.0/24"))
	mock.ExpectRollback()

	if err := repository.AddMembership(context.Background(), membership); !errors.Is(err, control.ErrValidation) {
		t.Fatalf("expected out-of-pool membership to be rejected, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected PostgreSQL calls: %v", err)
	}
}

func TestPostgresSetRelayAssignmentLocksNodesInIDOrderBeforeNetwork(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer database.Close()
	repository := NewPostgresRepository(database)
	assignment := control.RelayAssignment{
		ID:          "relay-assignment-1",
		NetworkID:   "network-1",
		NodeID:      "z-target",
		RelayNodeID: "a-relay",
		Address:     net.ParseIP("2001:db8::20"),
		Port:        51820,
		Family:      control.FamilyIPv6,
		Status:      control.RelayAssignmentActive,
		AssignedAt:  repositoryTestNow,
	}
	expiresAt := repositoryTestNow.Add(time.Hour)
	assignment.ExpiresAt = &expiresAt

	mock.ExpectBegin()
	for _, nodeID := range []string{"a-relay", "z-target"} {
		mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM nodes WHERE id = $1 FOR UPDATE")).
			WithArgs(nodeID).
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(nodeID))
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM networks WHERE id = $1 FOR UPDATE")).
		WithArgs("network-1").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("network-1"))
	for _, nodeID := range []string{"z-target", "a-relay"} {
		mock.ExpectQuery(regexp.QuoteMeta("SELECT status FROM memberships WHERE network_id = $1 AND node_id = $2")).
			WithArgs("network-1", nodeID).
			WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow(control.MembershipActive))
	}
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO relay_assignments")).
		WithArgs(assignment.ID, assignment.NetworkID, assignment.NodeID, assignment.RelayNodeID, assignment.Address.String(), assignment.Port, int64(6), assignment.Status, assignment.AssignedAt, expiresAt).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE networks SET config_version = config_version + 1 WHERE id = $1")).
		WithArgs("network-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := repository.SetRelayAssignment(context.Background(), assignment); err != nil {
		t.Fatalf("set relay assignment: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected PostgreSQL calls: %v", err)
	}
}

func TestPostgresRemoveMembershipLocksNodeBeforeNetwork(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer database.Close()
	repository := NewPostgresRepository(database)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM nodes WHERE id = $1 FOR UPDATE")).
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("node-1"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM networks WHERE id = $1 FOR UPDATE")).
		WithArgs("network-1").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("network-1"))
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM memberships")).
		WithArgs("network-1", "node-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM relay_assignments")).
		WithArgs("network-1", "node-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE networks SET config_version = config_version + 1 WHERE id = $1")).
		WithArgs("network-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := repository.RemoveMembership(context.Background(), "network-1", "node-1"); err != nil {
		t.Fatalf("remove membership: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected PostgreSQL calls: %v", err)
	}
}

func TestPostgresReplaceEndpointsLocksNodeBeforeNetworks(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer database.Close()
	repository := NewPostgresRepository(database)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM nodes WHERE id = $1 FOR UPDATE")).
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("node-1"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT network_id FROM memberships WHERE node_id = $1 ORDER BY network_id")).
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"network_id"}).AddRow("network-1").AddRow("network-2"))
	for _, networkID := range []string{"network-1", "network-2"} {
		mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM networks WHERE id = $1 FOR UPDATE")).
			WithArgs(networkID).
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(networkID))
	}
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM endpoint_candidates WHERE node_id = $1")).
		WithArgs("node-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE networks SET config_version = config_version + 1")).
		WithArgs("node-1").
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectCommit()

	if err := repository.ReplaceEndpoints(context.Background(), "node-1", nil); err != nil {
		t.Fatalf("replace endpoints: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected PostgreSQL calls: %v", err)
	}
}

func TestPostgresReplaceEndpointsMapsDuplicateTupleToConflict(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer database.Close()
	repository := NewPostgresRepository(database)
	endpoint := repositoryTestEndpoint("node-1", net.ParseIP("2001:db8::10"), repositoryTestNow)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM nodes WHERE id = $1 FOR UPDATE")).
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("node-1"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT network_id FROM memberships WHERE node_id = $1 ORDER BY network_id")).
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"network_id"}))
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM endpoint_candidates WHERE node_id = $1")).
		WithArgs("node-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	for _, rowsAffected := range []int64{1, 0} {
		mock.ExpectExec(regexp.QuoteMeta("ON CONFLICT (node_id, address, port, interface_name) DO NOTHING")).
			WithArgs(endpoint.NodeID, endpoint.Address.String(), endpoint.Port, 6, endpoint.Interface, endpoint.Priority, endpoint.ObservedAt).
			WillReturnResult(sqlmock.NewResult(0, rowsAffected))
	}
	mock.ExpectRollback()

	if err := repository.ReplaceEndpoints(context.Background(), "node-1", []control.EndpointCandidate{endpoint, endpoint}); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected duplicate endpoint tuple conflict, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected PostgreSQL calls: %v", err)
	}
}

func TestPostgresSetRelayAssignmentMapsCrossTargetIDConflict(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer database.Close()
	repository := NewPostgresRepository(database)
	assignment := control.RelayAssignment{
		ID:          "relay-assignment-1",
		NetworkID:   "network-1",
		NodeID:      "z-target",
		RelayNodeID: "a-relay",
		Address:     net.ParseIP("2001:db8::20"),
		Port:        51820,
		Family:      control.FamilyIPv6,
		Status:      control.RelayAssignmentActive,
		AssignedAt:  repositoryTestNow,
	}

	mock.ExpectBegin()
	for _, nodeID := range []string{"a-relay", "z-target"} {
		mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM nodes WHERE id = $1 FOR UPDATE")).
			WithArgs(nodeID).
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(nodeID))
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM networks WHERE id = $1 FOR UPDATE")).
		WithArgs("network-1").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("network-1"))
	for _, nodeID := range []string{"z-target", "a-relay"} {
		mock.ExpectQuery(regexp.QuoteMeta("SELECT status FROM memberships WHERE network_id = $1 AND node_id = $2")).
			WithArgs("network-1", nodeID).
			WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow(control.MembershipActive))
	}
	mock.ExpectExec(regexp.QuoteMeta("ON CONFLICT DO NOTHING")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT network_id, node_id FROM relay_assignments WHERE id = $1 FOR UPDATE")).
		WithArgs(assignment.ID).
		WillReturnRows(sqlmock.NewRows([]string{"network_id", "node_id"}).AddRow("network-2", "other-target"))
	mock.ExpectRollback()

	if err := repository.SetRelayAssignment(context.Background(), assignment); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected cross-target relay ID conflict, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected PostgreSQL calls: %v", err)
	}
}

func TestPostgresSetRelayAssignmentDistinguishesInactiveAndMissingMemberships(t *testing.T) {
	tests := []struct {
		name          string
		relayRows     *sqlmock.Rows
		expectedError error
	}{
		{
			name:          "inactive relay",
			relayRows:     sqlmock.NewRows([]string{"status"}).AddRow(control.MembershipPending),
			expectedError: control.ErrValidation,
		},
		{
			name:          "missing relay",
			relayRows:     sqlmock.NewRows([]string{"status"}),
			expectedError: ErrNotFound,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("create sql mock: %v", err)
			}
			defer database.Close()
			repository := NewPostgresRepository(database)
			assignment := control.RelayAssignment{
				ID:          "relay-assignment-1",
				NetworkID:   "network-1",
				NodeID:      "z-target",
				RelayNodeID: "a-relay",
				Address:     net.ParseIP("2001:db8::20"),
				Port:        51820,
				Family:      control.FamilyIPv6,
				Status:      control.RelayAssignmentActive,
				AssignedAt:  repositoryTestNow,
			}

			mock.ExpectBegin()
			for _, nodeID := range []string{"a-relay", "z-target"} {
				mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM nodes WHERE id = $1 FOR UPDATE")).
					WithArgs(nodeID).
					WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(nodeID))
			}
			mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM networks WHERE id = $1 FOR UPDATE")).
				WithArgs("network-1").
				WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("network-1"))
			mock.ExpectQuery(regexp.QuoteMeta("SELECT status FROM memberships WHERE network_id = $1 AND node_id = $2")).
				WithArgs("network-1", "z-target").
				WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow(control.MembershipActive))
			mock.ExpectQuery(regexp.QuoteMeta("SELECT status FROM memberships WHERE network_id = $1 AND node_id = $2")).
				WithArgs("network-1", "a-relay").
				WillReturnRows(test.relayRows)
			mock.ExpectRollback()

			if err := repository.SetRelayAssignment(context.Background(), assignment); !errors.Is(err, test.expectedError) {
				t.Fatalf("expected %v, got %v", test.expectedError, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unexpected PostgreSQL calls: %v", err)
			}
		})
	}
}

func TestPostgresRemoveRelayAssignmentLocksNodesBeforeNetwork(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer database.Close()
	repository := NewPostgresRepository(database)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT node_id FROM relay_assignments WHERE network_id = $1")).
		WithArgs("network-1").
		WillReturnRows(sqlmock.NewRows([]string{"node_id"}).AddRow("z-target").AddRow("a-relay"))
	for _, nodeID := range []string{"a-relay", "z-target"} {
		mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM nodes WHERE id = $1 FOR UPDATE")).
			WithArgs(nodeID).
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(nodeID))
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM networks WHERE id = $1 FOR UPDATE")).
		WithArgs("network-1").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("network-1"))
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM relay_assignments WHERE network_id = $1")).
		WithArgs("network-1").
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE networks SET config_version = config_version + 1 WHERE id = $1")).
		WithArgs("network-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := repository.RemoveRelayAssignment(context.Background(), "network-1"); err != nil {
		t.Fatalf("remove relay assignments: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected PostgreSQL calls: %v", err)
	}
}

func TestPostgresRemoveNodeUsesOneTransactionAndUpdatesAffectedNetworks(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer database.Close()
	repository := NewPostgresRepository(database)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM nodes WHERE id = $1 FOR UPDATE")).
		WithArgs("relay-node").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("relay-node"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT DISTINCT network_id")).
		WithArgs("relay-node").
		WillReturnRows(sqlmock.NewRows([]string{"network_id"}).AddRow("network-1").AddRow("network-2"))
	for _, networkID := range []string{"network-1", "network-2"} {
		mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM networks WHERE id = $1 FOR UPDATE")).
			WithArgs(networkID).
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(networkID))
	}
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM nodes WHERE id = $1")).
		WithArgs("relay-node").
		WillReturnResult(sqlmock.NewResult(0, 1))
	for _, networkID := range []string{"network-1", "network-2"} {
		mock.ExpectExec(regexp.QuoteMeta("UPDATE networks SET config_version = config_version + 1 WHERE id = $1")).
			WithArgs(networkID).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectCommit()

	if err := repository.RemoveNode(context.Background(), "relay-node"); err != nil {
		t.Fatalf("remove node: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected PostgreSQL calls: %v", err)
	}
}

func TestPostgresAddMembershipMapsMissingNetworkAndNodeToNotFound(t *testing.T) {
	tests := []struct {
		name          string
		nodeID        string
		nodeExists    bool
		networkExists bool
	}{
		{name: "network", nodeID: "node-1", nodeExists: true, networkExists: false},
		{name: "node", nodeID: "missing-node", nodeExists: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("create sql mock: %v", err)
			}
			defer database.Close()
			repository := NewPostgresRepository(database)
			membership := repositoryTestMembership(test.nodeID, "10.42.0.2", control.MembershipActive)

			mock.ExpectBegin()
			if test.nodeExists {
				mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM nodes WHERE id = $1 FOR UPDATE")).
					WithArgs(membership.NodeID).
					WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(membership.NodeID))
				if test.networkExists {
					mock.ExpectQuery(regexp.QuoteMeta("SELECT id, ipv4_pool::text FROM networks WHERE id = $1 FOR UPDATE")).
						WithArgs(membership.NetworkID).
						WillReturnRows(sqlmock.NewRows([]string{"id", "ipv4_pool"}).AddRow(membership.NetworkID, "10.42.0.0/24"))
				} else {
					mock.ExpectQuery(regexp.QuoteMeta("SELECT id, ipv4_pool::text FROM networks WHERE id = $1 FOR UPDATE")).
						WithArgs(membership.NetworkID).
						WillReturnRows(sqlmock.NewRows([]string{"id", "ipv4_pool"}))
				}
			} else {
				mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM nodes WHERE id = $1 FOR UPDATE")).
					WithArgs(membership.NodeID).
					WillReturnRows(sqlmock.NewRows([]string{"id"}))
			}
			mock.ExpectRollback()

			if err := repository.AddMembership(context.Background(), membership); !errors.Is(err, ErrNotFound) {
				t.Fatalf("expected ErrNotFound, got %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unexpected PostgreSQL calls: %v", err)
			}
		})
	}
}

func TestPostgresCreateInviteMapsMissingNetworkAndConsumedNodeToNotFound(t *testing.T) {
	tests := []struct {
		name           string
		consumedNodeID string
	}{
		{name: "network"},
		{name: "consumed node", consumedNodeID: "missing-node"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("create sql mock: %v", err)
			}
			defer database.Close()
			repository := NewPostgresRepository(database)
			invite := repositoryTestInvite()
			invite.ConsumedByNodeID = test.consumedNodeID
			consumedAt := repositoryTestNow
			if test.consumedNodeID != "" {
				invite.ConsumedAt = &consumedAt
			}

			mock.ExpectBegin()
			if test.consumedNodeID != "" {
				mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM nodes WHERE id = $1 FOR UPDATE")).
					WithArgs(test.consumedNodeID).
					WillReturnRows(sqlmock.NewRows([]string{"id"}))
			} else {
				mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM networks WHERE id = $1 FOR UPDATE")).
					WithArgs(invite.NetworkID).
					WillReturnRows(sqlmock.NewRows([]string{"id"}))
			}
			mock.ExpectRollback()

			if err := repository.CreateInvite(context.Background(), invite); !errors.Is(err, ErrNotFound) {
				t.Fatalf("expected ErrNotFound, got %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unexpected PostgreSQL calls: %v", err)
			}
		})
	}
}

func TestPostgresConsumeInviteConsumesWithBoundParameters(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer database.Close()
	repository := NewPostgresRepository(database)
	consumeAt := repositoryTestNow
	expected := repositoryTestInvite()
	expected.ConsumedAt = &consumeAt

	mock.ExpectQuery(regexp.QuoteMeta("UPDATE invites")).
		WithArgs(expected.ID, expected.TokenHash, consumeAt, "").
		WillReturnRows(repositoryTestInviteRows(expected))

	got, err := repository.ConsumeInvite(context.Background(), expected.ID, expected.TokenHash, consumeAt)
	if err != nil {
		t.Fatalf("consume invite: %v", err)
	}
	if got.ID != expected.ID || got.ConsumedAt == nil || !got.ConsumedAt.Equal(consumeAt) {
		t.Fatalf("unexpected consumed invite: %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected PostgreSQL calls: %v", err)
	}
}

func TestPostgresConsumeInviteClassifiesLookupAndLifecycleStates(t *testing.T) {
	consumedAt := repositoryTestNow.Add(-time.Minute)
	revokedAt := repositoryTestNow.Add(-time.Minute)
	tests := []struct {
		name          string
		lookup        string
		tokenHash     string
		state         control.Invite
		stateReturned bool
		expectedError error
	}{
		{
			name:          "wrong token",
			lookup:        "invite-1",
			tokenHash:     "wrong-hash",
			expectedError: ErrNotFound,
		},
		{
			name:          "unknown invite",
			lookup:        "unknown-invite",
			tokenHash:     "hash-1",
			expectedError: ErrNotFound,
		},
		{
			name:      "expired",
			lookup:    "invite-1",
			tokenHash: "hash-1",
			state: func() control.Invite {
				invite := repositoryTestInvite()
				invite.ExpiresAt = repositoryTestNow.Add(-time.Second)
				return invite
			}(),
			stateReturned: true,
			expectedError: ErrInviteExpired,
		},
		{
			name:      "already consumed",
			lookup:    "invite-1",
			tokenHash: "hash-1",
			state: func() control.Invite {
				invite := repositoryTestInvite()
				invite.ConsumedAt = &consumedAt
				return invite
			}(),
			stateReturned: true,
			expectedError: ErrInviteConsumed,
		},
		{
			name:      "revoked",
			lookup:    "invite-1",
			tokenHash: "hash-1",
			state: func() control.Invite {
				invite := repositoryTestInvite()
				invite.RevokedAt = &revokedAt
				return invite
			}(),
			stateReturned: true,
			expectedError: ErrInviteRevoked,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("create sql mock: %v", err)
			}
			defer database.Close()
			repository := NewPostgresRepository(database)

			mock.ExpectQuery(regexp.QuoteMeta("UPDATE invites")).
				WithArgs(test.lookup, test.tokenHash, repositoryTestNow, "").
				WillReturnRows(sqlmock.NewRows([]string{
					"id", "network_id", "token_hash", "expires_at", "consumed_at", "revoked_at", "consumed_by_node_id", "created_at",
				}))
			stateQuery := mock.ExpectQuery(regexp.QuoteMeta("FROM invites WHERE "+inviteLookupPredicate)).
				WithArgs(test.lookup, test.tokenHash)
			if test.stateReturned {
				stateQuery.WillReturnRows(repositoryTestInviteRows(test.state))
			} else {
				stateQuery.WillReturnRows(sqlmock.NewRows([]string{
					"id", "network_id", "token_hash", "expires_at", "consumed_at", "revoked_at", "consumed_by_node_id", "created_at",
				}))
			}

			_, err = repository.ConsumeInvite(context.Background(), test.lookup, test.tokenHash, repositoryTestNow)
			if !errors.Is(err, test.expectedError) {
				t.Fatalf("expected %v, got %v", test.expectedError, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unexpected PostgreSQL calls: %v", err)
			}
		})
	}
}

func TestPostgresConsumeInvitePrefersInviteIDOverNetworkScope(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer database.Close()
	repository := NewPostgresRepository(database)

	lookup := "shared-scope"
	tokenHash := "network-token"
	updateQuery := regexp.QuoteMeta("UPDATE invites") + ".*" + regexp.QuoteMeta("NOT EXISTS (SELECT 1 FROM invites AS id_scope WHERE id_scope.id = $1)")
	mock.ExpectQuery(updateQuery).
		WithArgs(lookup, tokenHash, repositoryTestNow, "").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "network_id", "token_hash", "expires_at", "consumed_at", "revoked_at", "consumed_by_node_id", "created_at",
		}))
	mock.ExpectQuery(regexp.QuoteMeta("NOT EXISTS (SELECT 1 FROM invites AS id_scope WHERE id_scope.id = $1)")).
		WithArgs(lookup, tokenHash).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "network_id", "token_hash", "expires_at", "consumed_at", "revoked_at", "consumed_by_node_id", "created_at",
		}))

	if _, err := repository.ConsumeInvite(context.Background(), lookup, tokenHash, repositoryTestNow); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected invite ID precedence to block network fallback, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected PostgreSQL calls: %v", err)
	}
}

func TestPostgresConsumeInviteForNodeRecordsConsumer(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer database.Close()
	repository := NewPostgresRepository(database)
	consumedAt := repositoryTestNow
	invite := repositoryTestInvite()
	invite.ConsumedAt = &consumedAt
	invite.ConsumedByNodeID = "consumer-node"

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM nodes WHERE id = $1 FOR UPDATE")).
		WithArgs("consumer-node").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("consumer-node"))
	mock.ExpectQuery(regexp.QuoteMeta("UPDATE invites")).
		WithArgs(invite.ID, invite.TokenHash, consumedAt, "consumer-node").
		WillReturnRows(repositoryTestInviteRows(invite))
	mock.ExpectCommit()

	consumed, err := repository.ConsumeInviteForNode(context.Background(), invite.ID, invite.TokenHash, consumedAt, "consumer-node")
	if err != nil {
		t.Fatalf("consume invite for node: %v", err)
	}
	if consumed.ConsumedByNodeID != "consumer-node" || consumed.ConsumedAt == nil || !consumed.ConsumedAt.Equal(consumedAt) {
		t.Fatalf("consumer was not recorded: %+v", consumed)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected PostgreSQL calls: %v", err)
	}
}

func TestPostgresConsumeInviteForNodeRejectsMissingConsumer(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer database.Close()
	repository := NewPostgresRepository(database)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM nodes WHERE id = $1 FOR UPDATE")).
		WithArgs("missing-node").
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectRollback()

	if _, err := repository.ConsumeInviteForNode(context.Background(), "invite-1", "hash-1", repositoryTestNow, "missing-node"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected missing consumer to return ErrNotFound, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected PostgreSQL calls: %v", err)
	}
}

func TestPostgresBuildSnapshotUsesReadTransactionBoundary(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer database.Close()
	repository := NewPostgresRepository(database)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, name, ipv4_pool::text, owner_id, config_version, created_at")).
		WithArgs("network-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "ipv4_pool", "owner_id", "config_version", "created_at"}).
			AddRow("network-1", "network", "10.42.0.0/24", "owner-1", int64(1), repositoryTestNetwork().CreatedAt))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT m.network_id")).
		WithArgs("network-1").
		WillReturnRows(sqlmock.NewRows([]string{"network_id", "node_id", "virtual_ipv4", "role", "status", "id", "display_name", "public_key", "platform", "client_version", "last_seen"}))
	mock.ExpectRollback()

	if _, err := repository.BuildSnapshotAt(context.Background(), "network-1", "local", repositoryTestNow); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected missing local membership, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected PostgreSQL calls: %v", err)
	}
}

func TestPostgresBuildSnapshotOrdersPeersByNodeID(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer database.Close()
	repository := NewPostgresRepository(database)
	columns := []string{"network_id", "node_id", "virtual_ipv4", "role", "status", "id", "display_name", "public_key", "platform", "client_version", "last_seen"}
	emptyEndpointRows := func() *sqlmock.Rows {
		return sqlmock.NewRows([]string{"node_id", "address", "port", "address_family", "interface_name", "priority", "observed_at"})
	}

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, name, ipv4_pool::text, owner_id, config_version, created_at")).
		WithArgs("network-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "ipv4_pool", "owner_id", "config_version", "created_at"}).
			AddRow("network-1", "network", "10.42.0.0/24", "owner-1", int64(3), repositoryTestNetwork().CreatedAt))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT m.network_id")).
		WithArgs("network-1").
		WillReturnRows(sqlmock.NewRows(columns).
			AddRow("network-1", "a-peer", "10.42.0.3", control.RoleMember, control.MembershipActive, "a-peer", "a-peer", "key-a", "windows", "0.1.0", repositoryTestNow).
			AddRow("network-1", "A-peer", "10.42.0.4", control.RoleMember, control.MembershipActive, "A-peer", "A-peer", "key-A", "windows", "0.1.0", repositoryTestNow).
			AddRow("network-1", "z-peer", "10.42.0.5", control.RoleMember, control.MembershipActive, "z-peer", "z-peer", "key-z", "windows", "0.1.0", repositoryTestNow).
			AddRow("network-1", "local", "10.42.0.2", control.RoleMember, control.MembershipActive, "local", "local", "key-local", "windows", "0.1.0", repositoryTestNow)).
		RowsWillBeClosed()
	for _, nodeID := range []string{"a-peer", "A-peer", "z-peer"} {
		mock.ExpectQuery(regexp.QuoteMeta("SELECT node_id, address::text, port, address_family, interface_name, priority, observed_at")).
			WithArgs(nodeID).
			WillReturnRows(emptyEndpointRows()).
			RowsWillBeClosed()
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT relay.id, relay.network_id")).
		WithArgs("network-1", "local").
		WillReturnRows(sqlmock.NewRows([]string{"id", "network_id", "node_id", "relay_node_id", "address", "port", "address_family", "status", "assigned_at", "expires_at"})).
		RowsWillBeClosed()
	mock.ExpectCommit()

	snapshot, err := repository.BuildSnapshotAt(context.Background(), "network-1", "local", repositoryTestNow)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	want := []string{"A-peer", "a-peer", "z-peer"}
	if len(snapshot.Peers) != len(want) {
		t.Fatalf("unexpected peer count: %+v", snapshot.Peers)
	}
	for index, nodeID := range want {
		if snapshot.Peers[index].NodeID != nodeID {
			t.Errorf("peer %d = %q, want %q", index, snapshot.Peers[index].NodeID, nodeID)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected PostgreSQL calls: %v", err)
	}
}

func TestPostgresBuildSnapshotRejectsFutureAndExpiredRelayAssignments(t *testing.T) {
	columns := []string{"network_id", "node_id", "virtual_ipv4", "role", "status", "id", "display_name", "public_key", "platform", "client_version", "last_seen"}
	tests := []struct {
		name       string
		assignedAt time.Time
		expiresAt  *time.Time
		wantRelay  bool
	}{
		{name: "future", assignedAt: repositoryTestNow.Add(time.Minute), wantRelay: false},
		{name: "current", assignedAt: repositoryTestNow, expiresAt: repositoryTestTimePointer(repositoryTestNow.Add(time.Minute)), wantRelay: true},
		{name: "expired", assignedAt: repositoryTestNow.Add(-2 * time.Minute), expiresAt: repositoryTestTimePointer(repositoryTestNow.Add(-time.Second)), wantRelay: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database, mock, err := sqlmock.New()
			if err != nil {
				t.Fatalf("create sql mock: %v", err)
			}
			defer database.Close()
			repository := NewPostgresRepository(database)

			mock.ExpectBegin()
			mock.ExpectQuery(regexp.QuoteMeta("SELECT id, name, ipv4_pool::text, owner_id, config_version, created_at")).
				WithArgs("network-1").
				WillReturnRows(sqlmock.NewRows([]string{"id", "name", "ipv4_pool", "owner_id", "config_version", "created_at"}).
					AddRow("network-1", "network", "10.42.0.0/24", "owner-1", int64(3), repositoryTestNetwork().CreatedAt))
			mock.ExpectQuery(regexp.QuoteMeta("SELECT m.network_id")).
				WithArgs("network-1").
				WillReturnRows(sqlmock.NewRows(columns).
					AddRow("network-1", "local", "10.42.0.2", control.RoleMember, control.MembershipActive, "local", "local", "key-local", "windows", "0.1.0", repositoryTestNow).
					AddRow("network-1", "relay", "10.42.0.3", control.RoleMember, control.MembershipActive, "relay", "relay", "key-relay", "windows", "0.1.0", repositoryTestNow)).
				RowsWillBeClosed()
			mock.ExpectQuery(regexp.QuoteMeta("SELECT node_id, address::text, port, address_family, interface_name, priority, observed_at")).
				WithArgs("relay").
				WillReturnRows(sqlmock.NewRows([]string{"node_id", "address", "port", "address_family", "interface_name", "priority", "observed_at"})).
				RowsWillBeClosed()
			relayRows := sqlmock.NewRows([]string{"id", "network_id", "node_id", "relay_node_id", "address", "port", "address_family", "status", "assigned_at", "expires_at"})
			relayRows.AddRow("relay-assignment-1", "network-1", "local", "relay", "2001:db8::20", int64(51820), int64(6), control.RelayAssignmentActive, test.assignedAt, test.expiresAt)
			mock.ExpectQuery(regexp.QuoteMeta("SELECT relay.id, relay.network_id")).
				WithArgs("network-1", "local").
				WillReturnRows(relayRows).
				RowsWillBeClosed()
			mock.ExpectCommit()

			snapshot, err := repository.BuildSnapshotAt(context.Background(), "network-1", "local", repositoryTestNow)
			if err != nil {
				t.Fatalf("build snapshot: %v", err)
			}
			if gotRelay := snapshot.RelayAssignment != nil; gotRelay != test.wantRelay {
				t.Fatalf("relay presence = %t, want %t: %+v", gotRelay, test.wantRelay, snapshot.RelayAssignment)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unexpected PostgreSQL calls: %v", err)
			}
		})
	}
}

func TestPostgresBuildSnapshotClosesMembershipRowsBeforeReadingPeerEndpoints(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer database.Close()
	repository := NewPostgresRepository(database)
	columns := []string{"network_id", "node_id", "virtual_ipv4", "role", "status", "id", "display_name", "public_key", "platform", "client_version", "last_seen"}

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, name, ipv4_pool::text, owner_id, config_version, created_at")).
		WithArgs("network-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "ipv4_pool", "owner_id", "config_version", "created_at"}).
			AddRow("network-1", "network", "10.42.0.0/24", "owner-1", int64(3), repositoryTestNetwork().CreatedAt))
	membershipRows := sqlmock.NewRows(columns).
		AddRow("network-1", "local", "10.42.0.2", control.RoleMember, control.MembershipActive, "local", "local", "key-local", "windows", "0.1.0", repositoryTestNow).
		AddRow("network-1", "peer", "10.42.0.3", control.RoleMember, control.MembershipActive, "peer", "peer", "key-peer", "windows", "0.1.0", repositoryTestNow)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT m.network_id")).
		WithArgs("network-1").
		WillReturnRows(membershipRows).
		RowsWillBeClosed()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT node_id, address::text, port, address_family, interface_name, priority, observed_at")).
		WithArgs("peer").
		WillReturnRows(sqlmock.NewRows([]string{"node_id", "address", "port", "address_family", "interface_name", "priority", "observed_at"}).
			AddRow("peer", "2001:db8::42", int64(51820), int64(6), "Ethernet", int64(1), repositoryTestNow)).
		RowsWillBeClosed()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT relay.id, relay.network_id")).
		WithArgs("network-1", "local").
		WillReturnRows(sqlmock.NewRows([]string{"id", "network_id", "node_id", "relay_node_id", "address", "port", "address_family", "status", "assigned_at", "expires_at"})).
		RowsWillBeClosed()
	mock.ExpectCommit()

	snapshot, err := repository.BuildSnapshotAt(context.Background(), "network-1", "local", repositoryTestNow)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	if len(snapshot.Peers) != 1 || snapshot.Peers[0].NodeID != "peer" {
		t.Fatalf("unexpected peers: %+v", snapshot.Peers)
	}
	if len(snapshot.Peers[0].Endpoints) != 1 || !snapshot.Peers[0].Endpoints[0].Address.Equal(net.ParseIP("2001:db8::42")) {
		t.Fatalf("peer endpoint was not loaded after membership rows: %+v", snapshot.Peers[0].Endpoints)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected PostgreSQL calls: %v", err)
	}
}

func TestPostgresBuildSnapshotUsesConfiguredEndpointMaxAge(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer database.Close()
	repository := NewPostgresRepositoryWithEndpointMaxAge(database, 20*time.Minute)
	columns := []string{"network_id", "node_id", "virtual_ipv4", "role", "status", "id", "display_name", "public_key", "platform", "client_version", "last_seen"}

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, name, ipv4_pool::text, owner_id, config_version, created_at")).
		WithArgs("network-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "ipv4_pool", "owner_id", "config_version", "created_at"}).
			AddRow("network-1", "network", "10.42.0.0/24", "owner-1", int64(3), repositoryTestNetwork().CreatedAt))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT m.network_id")).
		WithArgs("network-1").
		WillReturnRows(sqlmock.NewRows(columns).
			AddRow("network-1", "local", "10.42.0.2", control.RoleMember, control.MembershipActive, "local", "local", "key-local", "windows", "0.1.0", repositoryTestNow).
			AddRow("network-1", "peer", "10.42.0.3", control.RoleMember, control.MembershipActive, "peer", "peer", "key-peer", "windows", "0.1.0", repositoryTestNow)).
		RowsWillBeClosed()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT node_id, address::text, port, address_family, interface_name, priority, observed_at")).
		WithArgs("peer").
		WillReturnRows(sqlmock.NewRows([]string{"node_id", "address", "port", "address_family", "interface_name", "priority", "observed_at"}).
			AddRow("peer", "2001:db8::42", int64(51820), int64(6), "Ethernet", int64(1), repositoryTestNow.Add(-15*time.Minute))).
		RowsWillBeClosed()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT relay.id, relay.network_id")).
		WithArgs("network-1", "local").
		WillReturnRows(sqlmock.NewRows([]string{"id", "network_id", "node_id", "relay_node_id", "address", "port", "address_family", "status", "assigned_at", "expires_at"})).
		RowsWillBeClosed()
	mock.ExpectCommit()

	snapshot, err := repository.BuildSnapshotAt(context.Background(), "network-1", "local", repositoryTestNow)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	if len(snapshot.Peers) != 1 || len(snapshot.Peers[0].Endpoints) != 1 {
		t.Fatalf("expected configured freshness to keep endpoint, got %+v", snapshot.Peers)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected PostgreSQL calls: %v", err)
	}
}

func TestPostgresBuildSnapshotOrdersFreshEndpointsDeterministically(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	defer database.Close()
	repository := NewPostgresRepository(database)
	columns := []string{"network_id", "node_id", "virtual_ipv4", "role", "status", "id", "display_name", "public_key", "platform", "client_version", "last_seen"}

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, name, ipv4_pool::text, owner_id, config_version, created_at")).
		WithArgs("network-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "ipv4_pool", "owner_id", "config_version", "created_at"}).
			AddRow("network-1", "network", "10.42.0.0/24", "owner-1", int64(3), repositoryTestNetwork().CreatedAt))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT m.network_id")).
		WithArgs("network-1").
		WillReturnRows(sqlmock.NewRows(columns).
			AddRow("network-1", "local", "10.42.0.2", control.RoleMember, control.MembershipActive, "local", "local", "key-local", "windows", "0.1.0", repositoryTestNow).
			AddRow("network-1", "peer", "10.42.0.3", control.RoleMember, control.MembershipActive, "peer", "peer", "key-peer", "windows", "0.1.0", repositoryTestNow)).
		RowsWillBeClosed()
	endpointQuery := regexp.QuoteMeta("SELECT node_id, address::text, port, address_family, interface_name, priority, observed_at") + ".*" + regexp.QuoteMeta("ORDER BY priority, address::text, port, interface_name")
	mock.ExpectQuery(endpointQuery).
		WithArgs("peer").
		WillReturnRows(sqlmock.NewRows([]string{"node_id", "address", "port", "address_family", "interface_name", "priority", "observed_at"}).
			AddRow("peer", "2001:db8::20", int64(51820), int64(6), "WiFi", int64(2), repositoryTestNow).
			AddRow("peer", "2001:db8::10", int64(51821), int64(6), "Ethernet", int64(1), repositoryTestNow).
			AddRow("peer", "2001:db8::30", int64(51820), int64(6), "Ethernet", int64(1), repositoryTestNow).
			AddRow("peer", "2001:db8::10", int64(51820), int64(6), "WiFi", int64(1), repositoryTestNow).
			AddRow("peer", "2001:db8::10", int64(51820), int64(6), "Ethernet", int64(1), repositoryTestNow)).
		RowsWillBeClosed()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT relay.id, relay.network_id")).
		WithArgs("network-1", "local").
		WillReturnRows(sqlmock.NewRows([]string{"id", "network_id", "node_id", "relay_node_id", "address", "port", "address_family", "status", "assigned_at", "expires_at"})).
		RowsWillBeClosed()
	mock.ExpectCommit()

	snapshot, err := repository.BuildSnapshotAt(context.Background(), "network-1", "local", repositoryTestNow)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	want := []struct {
		address       string
		port          uint16
		interfaceName string
	}{
		{address: "2001:db8::10", port: 51820, interfaceName: "Ethernet"},
		{address: "2001:db8::10", port: 51820, interfaceName: "WiFi"},
		{address: "2001:db8::10", port: 51821, interfaceName: "Ethernet"},
		{address: "2001:db8::30", port: 51820, interfaceName: "Ethernet"},
		{address: "2001:db8::20", port: 51820, interfaceName: "WiFi"},
	}
	if len(snapshot.Peers) != 1 || len(snapshot.Peers[0].Endpoints) != len(want) {
		t.Fatalf("unexpected snapshot endpoint count: %+v", snapshot.Peers)
	}
	for index, expected := range want {
		got := snapshot.Peers[0].Endpoints[index]
		if got.Address.String() != expected.address || got.Port != expected.port || got.Interface != expected.interfaceName {
			t.Errorf("endpoint %d = %s:%d/%s, want %s:%d/%s", index, got.Address, got.Port, got.Interface, expected.address, expected.port, expected.interfaceName)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected PostgreSQL calls: %v", err)
	}
}

func TestPostgresBuildSnapshotReadsPeerEndpointsAfterMembershipRowsClose(t *testing.T) {
	state := &snapshotDriverState{}
	database := sql.OpenDB(snapshotDriverConnector{state: state})
	defer database.Close()
	repository := NewPostgresRepository(database)

	snapshot, err := repository.BuildSnapshotAt(context.Background(), "network-1", "local", repositoryTestNow)
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	if len(snapshot.Peers) != 1 || len(snapshot.Peers[0].Endpoints) != 1 {
		t.Fatalf("expected one peer with one endpoint, got %+v", snapshot.Peers)
	}
	state.mu.Lock()
	endpointQueryBeforeClose := state.endpointQueryBeforeMembershipClose
	state.mu.Unlock()
	if endpointQueryBeforeClose {
		t.Fatal("endpoint query ran before membership rows were closed")
	}
}

type snapshotDriverState struct {
	mu                                 sync.Mutex
	membershipRowsOpen                 bool
	endpointQueryBeforeMembershipClose bool
}

type snapshotDriverConnector struct {
	state *snapshotDriverState
}

func (connector snapshotDriverConnector) Connect(context.Context) (driver.Conn, error) {
	return &snapshotDriverConn{state: connector.state}, nil
}

func (connector snapshotDriverConnector) Driver() driver.Driver {
	return snapshotDriver{}
}

type snapshotDriver struct{}

func (snapshotDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("snapshot test driver requires a connector")
}

type snapshotDriverConn struct {
	state *snapshotDriverState
}

func (connection *snapshotDriverConn) Prepare(query string) (driver.Stmt, error) {
	return snapshotDriverStmt{connection: connection, query: query}, nil
}

func (*snapshotDriverConn) Close() error { return nil }

func (*snapshotDriverConn) Begin() (driver.Tx, error) {
	return snapshotDriverTx{}, nil
}

func (*snapshotDriverConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return snapshotDriverTx{}, nil
}

func (connection *snapshotDriverConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	normalized := strings.ToLower(query)
	connection.state.mu.Lock()
	defer connection.state.mu.Unlock()

	switch {
	case strings.Contains(normalized, "from endpoint_candidates"):
		if connection.state.membershipRowsOpen {
			connection.state.endpointQueryBeforeMembershipClose = true
			return nil, errors.New("endpoint query while membership rows are open")
		}
		return &snapshotDriverRows{
			columns: []string{"node_id", "address", "port", "address_family", "interface_name", "priority", "observed_at"},
			values:  [][]driver.Value{{"peer", "2001:db8::42", int64(51820), int64(6), "Ethernet", int64(1), repositoryTestNow}},
			state:   connection.state,
		}, nil
	case strings.Contains(normalized, "from networks where id"):
		return &snapshotDriverRows{
			columns: []string{"id", "name", "ipv4_pool", "owner_id", "config_version", "created_at"},
			values:  [][]driver.Value{{"network-1", "network", "10.42.0.0/24", "owner-1", int64(3), repositoryTestNetwork().CreatedAt}},
			state:   connection.state,
		}, nil
	case strings.Contains(normalized, "from memberships as m"):
		connection.state.membershipRowsOpen = true
		return &snapshotDriverRows{
			columns: []string{"network_id", "node_id", "virtual_ipv4", "role", "status", "id", "display_name", "public_key", "platform", "client_version", "last_seen"},
			values: [][]driver.Value{
				{"network-1", "local", "10.42.0.2", control.RoleMember, control.MembershipActive, "local", "local", "key-local", "windows", "0.1.0", repositoryTestNow},
				{"network-1", "peer", "10.42.0.3", control.RoleMember, control.MembershipActive, "peer", "peer", "key-peer", "windows", "0.1.0", repositoryTestNow},
			},
			state:      connection.state,
			membership: true,
		}, nil
	case strings.Contains(normalized, "from relay_assignments as relay"):
		return &snapshotDriverRows{
			columns: []string{"id", "network_id", "node_id", "relay_node_id", "address", "port", "address_family", "status", "assigned_at", "expires_at"},
			state:   connection.state,
		}, nil
	default:
		return nil, errors.New("unexpected snapshot query: " + query)
	}
}

func (*snapshotDriverConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return driver.RowsAffected(0), nil
}

type snapshotDriverStmt struct {
	connection *snapshotDriverConn
	query      string
}

func (statement snapshotDriverStmt) Close() error { return nil }

func (snapshotDriverStmt) NumInput() int { return -1 }

func (statement snapshotDriverStmt) Exec([]driver.Value) (driver.Result, error) {
	return driver.RowsAffected(0), nil
}

func (statement snapshotDriverStmt) Query([]driver.Value) (driver.Rows, error) {
	return statement.connection.QueryContext(context.Background(), statement.query, nil)
}

type snapshotDriverTx struct{}

func (snapshotDriverTx) Commit() error   { return nil }
func (snapshotDriverTx) Rollback() error { return nil }

type snapshotDriverRows struct {
	columns    []string
	values     [][]driver.Value
	state      *snapshotDriverState
	membership bool
	position   int
	closed     bool
}

func (rows *snapshotDriverRows) Columns() []string { return rows.columns }

func (rows *snapshotDriverRows) Close() error {
	if rows.closed {
		return nil
	}
	rows.closed = true
	if rows.membership {
		rows.state.mu.Lock()
		rows.state.membershipRowsOpen = false
		rows.state.mu.Unlock()
	}
	return nil
}

func (rows *snapshotDriverRows) Next(destination []driver.Value) error {
	if rows.position >= len(rows.values) {
		return io.EOF
	}
	copy(destination, rows.values[rows.position])
	rows.position++
	return nil
}

func TestRepositoriesExposeTransactionBoundary(t *testing.T) {
	var memory TransactionalRepository = NewMemoryRepository()
	if err := memory.WithTransaction(context.Background(), func(_ context.Context, transaction Repository) error {
		return transaction.CreateNetwork(context.Background(), repositoryTestNetwork())
	}); err != nil {
		t.Fatalf("memory transaction boundary failed: %v", err)
	}
	var repository Repository = NewMemoryRepository()
	if _, err := repository.GetNodeNetworkIDs(context.Background(), "missing-node"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("repository network scope contract error = %v, want ErrNotFound", err)
	}

	postgres := NewPostgresRepository(nil)
	var _ TransactionalRepository = postgres
	if err := postgres.WithTransaction(context.Background(), func(_ context.Context, _ Repository) error {
		return nil
	}); !errors.Is(err, ErrDatabaseUnavailable) {
		t.Fatalf("expected unavailable PostgreSQL transaction, got %v", err)
	}
}
