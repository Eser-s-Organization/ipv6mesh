package control_test

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/Eser-s-Organization/ipv6mesh/internal/control"
	"github.com/Eser-s-Organization/ipv6mesh/internal/db"
)

func TestRoomLifecycleEnrollsHostAndMemberWithoutVisibleInvites(t *testing.T) {
	repository := db.NewMemoryRepository()
	handler := control.NewHandler(repository, control.HandlerOptions{
		BootstrapToken: "internal-bootstrap",
		RoomMode:       true,
		NewID:          sequentialIDs("room-1", "invite-host", "node-host", "invite-member", "node-member"),
		TokenRandom:    bytes.NewReader(bytes.Repeat([]byte{0x41}, 128)),
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	client, err := control.NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	roomNetwork, err := client.CreateRoom(context.Background(), "IPv6Mesh-HOST", "10.42.0.0/24", "internal-bootstrap")
	if err != nil {
		t.Fatal(err)
	}

	host, err := client.JoinRoom(context.Background(), control.RoomJoinRequest{
		DisplayName:   "HOST",
		PublicKey:     "host-public",
		Platform:      "windows",
		ClientVersion: "0.1.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	member, err := client.JoinRoom(context.Background(), control.RoomJoinRequest{
		DisplayName:   "MEMBER",
		PublicKey:     "member-public",
		Platform:      "windows",
		ClientVersion: "0.1.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if host.Network.ID != roomNetwork.ID || member.Network.ID != roomNetwork.ID {
		t.Fatalf("networks: host=%q member=%q room=%q", host.Network.ID, member.Network.ID, roomNetwork.ID)
	}
	if host.Membership.VirtualIPv4.Equal(member.Membership.VirtualIPv4) {
		t.Fatalf("duplicate virtual IPv4: %s", host.Membership.VirtualIPv4)
	}
	if host.SessionToken == "" || member.SessionToken == "" {
		t.Fatal("room enrollment did not return node sessions")
	}

	hostSnapshot, err := client.Snapshot(context.Background(), roomNetwork.ID, host.SessionToken)
	if err != nil {
		t.Fatal(err)
	}
	memberSnapshot, err := client.Snapshot(context.Background(), roomNetwork.ID, member.SessionToken)
	if err != nil {
		t.Fatal(err)
	}
	if len(hostSnapshot.Peers) != 1 || len(memberSnapshot.Peers) != 1 {
		t.Fatalf("peer counts: host=%d member=%d", len(hostSnapshot.Peers), len(memberSnapshot.Peers))
	}
}

func TestRoomJoinOnFreshHandlerReturnsRoomNotReady(t *testing.T) {
	handler := control.NewHandler(db.NewMemoryRepository(), control.HandlerOptions{RoomMode: true})
	server := httptest.NewServer(handler)
	defer server.Close()

	client, err := control.NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.JoinRoom(context.Background(), control.RoomJoinRequest{
		DisplayName:   "MEMBER",
		PublicKey:     "member-public",
		Platform:      "windows",
		ClientVersion: "0.1.0",
	})
	var httpErr *control.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != "room_not_ready" {
		t.Fatalf("fresh room join error = %v, want room_not_ready", err)
	}
}

func sequentialIDs(values ...string) func() string {
	index := 0
	return func() string {
		if index >= len(values) {
			panic("sequential ID source exhausted")
		}
		value := values[index]
		index++
		return value
	}
}
