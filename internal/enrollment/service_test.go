package enrollment_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Eser-s-Organization/ipv6mesh/internal/address"
	"github.com/Eser-s-Organization/ipv6mesh/internal/auth"
	"github.com/Eser-s-Organization/ipv6mesh/internal/control"
	"github.com/Eser-s-Organization/ipv6mesh/internal/db"
	"github.com/Eser-s-Organization/ipv6mesh/internal/enrollment"
)

var enrollmentTestNow = time.Date(2026, time.August, 11, 2, 0, 0, 0, time.UTC)

func newEnrollmentTestService(repository *db.MemoryRepository, ids ...string) *enrollment.Service {
	index := 0
	return enrollment.NewServiceWithOptions(repository, enrollment.ServiceOptions{
		Clock: func() time.Time { return enrollmentTestNow },
		NewID: func() string {
			if index >= len(ids) {
				return "generated-node"
			}
			value := ids[index]
			index++
			return value
		},
	})
}

func TestNewServiceAcceptsInjectedOptions(t *testing.T) {
	service := enrollment.NewService(db.NewMemoryRepository(), enrollment.ServiceOptions{
		Clock: func() time.Time { return enrollmentTestNow },
		NewID: func() string { return "node-1" },
	})
	if service == nil {
		t.Fatal("NewService returned nil")
	}
}

func enrollmentNetwork(pool string) control.Network {
	return control.Network{
		ID:            "network-1",
		Name:          "test network",
		IPv4Pool:      pool,
		OwnerID:       "administrator",
		ConfigVersion: 1,
		CreatedAt:     enrollmentTestNow.Add(-time.Hour),
	}
}

func createEnrollmentInvite(t *testing.T, repository *db.MemoryRepository, id, rawToken string, expiresAt time.Time) {
	t.Helper()
	invite := control.Invite{
		ID:        id,
		NetworkID: "network-1",
		TokenHash: auth.HashToken(rawToken),
		ExpiresAt: expiresAt,
		CreatedAt: enrollmentTestNow.Add(-time.Minute),
	}
	if err := repository.CreateInvite(context.Background(), invite); err != nil {
		t.Fatalf("create invite %s: %v", id, err)
	}
}

func enrollRequest(token, nodeID, publicKey string) enrollment.Request {
	return enrollment.Request{
		InviteToken:   token,
		NodeID:        nodeID,
		DisplayName:   "device-" + publicKey,
		PublicKey:     publicKey,
		Platform:      "windows",
		ClientVersion: "0.1.0",
	}
}

func TestEnrollAllocatesRandomDistinctAddressesAndSnapshotRemovesDeletedNode(t *testing.T) {
	repository := db.NewMemoryRepository()
	if err := repository.CreateNetwork(context.Background(), enrollmentNetwork("10.42.0.0/29")); err != nil {
		t.Fatalf("create network: %v", err)
	}
	createEnrollmentInvite(t, repository, "invite-a", "invite-a.secret", enrollmentTestNow.Add(time.Hour))
	createEnrollmentInvite(t, repository, "invite-b", "invite-b.secret", enrollmentTestNow.Add(time.Hour))
	service := newEnrollmentTestService(repository, "node-a", "node-b")

	a, err := service.Enroll(context.Background(), enrollRequest("invite-a.secret", "", "public-a"))
	if err != nil {
		t.Fatalf("enroll A: %v", err)
	}
	b, err := service.Enroll(context.Background(), enrollRequest("invite-b.secret", "", "public-b"))
	if err != nil {
		t.Fatalf("enroll B: %v", err)
	}
	if a.Node.ID != "node-a" || b.Node.ID != "node-b" {
		t.Fatalf("unexpected node IDs: %q, %q", a.Node.ID, b.Node.ID)
	}
	pool, err := address.NewPool("10.42.0.0/29")
	if err != nil {
		t.Fatalf("create verification pool: %v", err)
	}
	if !pool.Usable(a.Membership.VirtualIPv4) || !pool.Usable(b.Membership.VirtualIPv4) || a.Membership.VirtualIPv4.Equal(b.Membership.VirtualIPv4) {
		t.Fatalf("unexpected random allocations: %s, %s", a.Membership.VirtualIPv4, b.Membership.VirtualIPv4)
	}
	if a.Network.ID != "network-1" || a.Subject != "node-a" || a.NetworkID != "network-1" {
		t.Fatalf("enrollment result lacks session claims: %+v", a)
	}

	snapshot, err := repository.BuildSnapshotAt(context.Background(), "network-1", "node-a", enrollmentTestNow)
	if err != nil {
		t.Fatalf("build initial snapshot: %v", err)
	}
	if len(snapshot.Peers) != 1 || snapshot.Peers[0].NodeID != "node-b" || snapshot.Generation < 3 {
		t.Fatalf("unexpected initial snapshot: %+v", snapshot)
	}
	repeated, err := repository.BuildSnapshotAt(context.Background(), "network-1", "node-a", enrollmentTestNow)
	if err != nil {
		t.Fatalf("build repeated snapshot: %v", err)
	}
	if repeated.Generation != snapshot.Generation || len(repeated.Peers) != len(snapshot.Peers) {
		t.Fatalf("repeated snapshot changed control state: %+v vs %+v", repeated, snapshot)
	}

	if err := repository.RemoveNode(context.Background(), "node-b"); err != nil {
		t.Fatalf("remove B: %v", err)
	}
	afterDelete, err := repository.BuildSnapshotAt(context.Background(), "network-1", "node-a", enrollmentTestNow)
	if err != nil {
		t.Fatalf("build snapshot after deletion: %v", err)
	}
	if len(afterDelete.Peers) != 0 || afterDelete.Generation <= snapshot.Generation {
		t.Fatalf("deleted node remained in snapshot or generation did not advance: %+v", afterDelete)
	}
}

func TestEnrollRejectsDuplicatePublicKey(t *testing.T) {
	repository := db.NewMemoryRepository()
	if err := repository.CreateNetwork(context.Background(), enrollmentNetwork("10.42.0.0/29")); err != nil {
		t.Fatalf("create network: %v", err)
	}
	createEnrollmentInvite(t, repository, "invite-a", "invite-a.secret", enrollmentTestNow.Add(time.Hour))
	createEnrollmentInvite(t, repository, "invite-b", "invite-b.secret", enrollmentTestNow.Add(time.Hour))
	service := newEnrollmentTestService(repository, "node-a", "node-b")
	if _, err := service.Enroll(context.Background(), enrollRequest("invite-a.secret", "", "same-public-key")); err != nil {
		t.Fatalf("enroll A: %v", err)
	}
	if _, err := service.Enroll(context.Background(), enrollRequest("invite-b.secret", "", "same-public-key")); !errors.Is(err, db.ErrConflict) {
		t.Fatalf("duplicate public key error = %v, want db.ErrConflict", err)
	}
	if _, err := repository.GetNode(context.Background(), "node-b"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("duplicate enrollment left node B behind: %v", err)
	}
}

func TestEnrollPropagatesExpiredConsumedAndRevokedInviteErrors(t *testing.T) {
	repository := db.NewMemoryRepository()
	if err := repository.CreateNetwork(context.Background(), enrollmentNetwork("10.42.0.0/29")); err != nil {
		t.Fatalf("create network: %v", err)
	}
	createEnrollmentInvite(t, repository, "expired", "expired.secret", enrollmentTestNow)
	createEnrollmentInvite(t, repository, "consumed", "consumed.secret", enrollmentTestNow.Add(time.Hour))
	service := newEnrollmentTestService(repository, "node-expired", "node-consumed", "node-revoked")
	if _, err := service.Enroll(context.Background(), enrollRequest("expired.secret", "", "expired-key")); !errors.Is(err, db.ErrInviteExpired) {
		t.Fatalf("expired invite error = %v, want db.ErrInviteExpired", err)
	}
	if _, err := service.Enroll(context.Background(), enrollRequest("consumed.secret", "", "consumed-key")); err != nil {
		t.Fatalf("first consumed invite enrollment: %v", err)
	}
	if _, err := service.Enroll(context.Background(), enrollRequest("consumed.secret", "", "second-key")); !errors.Is(err, db.ErrInviteConsumed) {
		t.Fatalf("consumed invite error = %v, want db.ErrInviteConsumed", err)
	}

	revokedRepository := db.NewMemoryRepository()
	if err := revokedRepository.CreateNetwork(context.Background(), enrollmentNetwork("10.42.0.0/29")); err != nil {
		t.Fatalf("create revoked network: %v", err)
	}
	revokedInvite := control.Invite{
		ID:        "revoked",
		NetworkID: "network-1",
		TokenHash: auth.HashToken("revoked.secret"),
		ExpiresAt: enrollmentTestNow.Add(time.Hour),
		RevokedAt: ptrTime(enrollmentTestNow.Add(-time.Second)),
		CreatedAt: enrollmentTestNow.Add(-time.Minute),
	}
	if err := revokedRepository.CreateInvite(context.Background(), revokedInvite); err != nil {
		t.Fatalf("create revoked invite: %v", err)
	}
	if _, err := newEnrollmentTestService(revokedRepository, "node-revoked").Enroll(context.Background(), enrollRequest("revoked.secret", "", "revoked-key")); !errors.Is(err, db.ErrInviteRevoked) {
		t.Fatalf("revoked invite error = %v, want db.ErrInviteRevoked", err)
	}
}

func ptrTime(value time.Time) *time.Time { return &value }

func TestEnrollPoolExhaustionRollsBackNodeAndInviteConsumption(t *testing.T) {
	repository := db.NewMemoryRepository()
	if err := repository.CreateNetwork(context.Background(), enrollmentNetwork("10.42.0.0/30")); err != nil {
		t.Fatalf("create network: %v", err)
	}
	createEnrollmentInvite(t, repository, "invite-a", "invite-a.secret", enrollmentTestNow.Add(time.Hour))
	createEnrollmentInvite(t, repository, "invite-b", "invite-b.secret", enrollmentTestNow.Add(time.Hour))
	createEnrollmentInvite(t, repository, "invite-c", "invite-c.secret", enrollmentTestNow.Add(time.Hour))
	service := newEnrollmentTestService(repository, "node-a", "node-b", "node-c")
	for _, request := range []enrollment.Request{
		enrollRequest("invite-a.secret", "", "public-a"),
		enrollRequest("invite-b.secret", "", "public-b"),
	} {
		if _, err := service.Enroll(context.Background(), request); err != nil {
			t.Fatalf("enroll available node: %v", err)
		}
	}
	if _, err := service.Enroll(context.Background(), enrollRequest("invite-c.secret", "", "public-c")); !errors.Is(err, address.ErrPoolExhausted) {
		t.Fatalf("pool exhaustion error = %v", err)
	}
	if _, err := repository.GetNode(context.Background(), "node-c"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("pool exhaustion left node C behind: %v", err)
	}
	if _, err := repository.ConsumeInvite(context.Background(), "invite-c", auth.HashToken("invite-c.secret"), enrollmentTestNow); err != nil {
		t.Fatalf("invite was consumed despite rollback: %v", err)
	}
}
