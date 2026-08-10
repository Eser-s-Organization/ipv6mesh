package db

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Eser-s-Organization/ipv6mesh/internal/control"
)

var repositoryTestNow = time.Date(2026, time.January, 10, 12, 0, 0, 0, time.UTC)

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
	values []any
}

func (row fakeRowScanner) Scan(dest ...any) error {
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

func TestRepositoriesExposeTransactionBoundary(t *testing.T) {
	var memory TransactionalRepository = NewMemoryRepository()
	if err := memory.WithTransaction(context.Background(), func(_ context.Context, transaction Repository) error {
		return transaction.CreateNetwork(context.Background(), repositoryTestNetwork())
	}); err != nil {
		t.Fatalf("memory transaction boundary failed: %v", err)
	}

	postgres := NewPostgresRepository(nil)
	var _ TransactionalRepository = postgres
	if err := postgres.WithTransaction(context.Background(), func(_ context.Context, _ Repository) error {
		return nil
	}); !errors.Is(err, ErrDatabaseUnavailable) {
		t.Fatalf("expected unavailable PostgreSQL transaction, got %v", err)
	}
}
