package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestClientCreateRoomSendsAuthenticatedRoomRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/room" {
			t.Fatalf("room create request = %s %s", request.Method, request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer bootstrap" {
			t.Fatalf("authorization = %q", got)
		}
		var body map[string]string
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		want := map[string]string{"name": "IPv6Mesh-HOST", "ipv4_pool": "10.42.0.0/24"}
		if !reflect.DeepEqual(body, want) {
			t.Fatalf("room create body = %#v, want %#v", body, want)
		}
		writeTestJSON(writer, http.StatusCreated, map[string]any{
			"id": "room-1", "name": "IPv6Mesh-HOST", "ipv4_pool": "10.42.0.0/24",
			"config_version": 1, "created_at": "2026-08-16T12:00:00Z",
		})
	}))
	defer server.Close()

	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	room, err := client.CreateRoom(context.Background(), "IPv6Mesh-HOST", "10.42.0.0/24", "bootstrap")
	if err != nil {
		t.Fatal(err)
	}
	if room.ID != "room-1" || room.IPv4Pool != "10.42.0.0/24" {
		t.Fatalf("room = %+v", room)
	}
}

func TestClientJoinRoomIsUnauthenticatedAndStrict(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/room/join" {
			t.Fatalf("room join request = %s %s", request.Method, request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "" {
			t.Fatalf("room join unexpectedly authenticated: %q", got)
		}
		var body map[string]string
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		want := map[string]string{
			"public_key": "member-public", "display_name": "MEMBER-PC",
			"platform": "windows", "client_version": "0.1.0",
		}
		if !reflect.DeepEqual(body, want) {
			t.Fatalf("room join body = %#v, want %#v", body, want)
		}
		writeTestJSON(writer, http.StatusCreated, map[string]any{
			"node":          map[string]any{"id": "node-1", "display_name": "MEMBER-PC", "public_key": "member-public", "platform": "windows", "client_version": "0.1.0"},
			"membership":    map[string]any{"network_id": "room-1", "node_id": "node-1", "virtual_ipv4": "10.42.0.2", "role": "member", "status": "active"},
			"network":       map[string]any{"id": "room-1", "name": "IPv6Mesh-HOST", "ipv4_pool": "10.42.0.0/24", "config_version": 2, "created_at": "2026-08-16T12:00:00Z"},
			"session":       map[string]any{"token": "session-token", "subject": "node-1", "network_id": "room-1", "expires_at": "2026-08-16T13:00:00Z"},
			"session_token": "session-token",
		})
	}))
	defer server.Close()

	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.JoinRoom(context.Background(), RoomJoinRequest{
		DisplayName: "MEMBER-PC", PublicKey: "member-public", Platform: "windows", ClientVersion: "0.1.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Network.ID != "room-1" || result.Membership.VirtualIPv4.String() != "10.42.0.2" || result.SessionToken != "session-token" {
		t.Fatalf("result = %+v", result)
	}

	badServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writeTestJSON(writer, http.StatusCreated, map[string]any{
			"node": map[string]any{"id": "node-1"}, "membership": map[string]any{"network_id": "room-1", "node_id": "node-1", "virtual_ipv4": "10.42.0.2"},
			"network": map[string]any{"id": "room-1", "ipv4_pool": "10.42.0.0/24"}, "session": map[string]any{"token": "session-token", "subject": "node-1", "network_id": "room-1"}, "session_token": "session-token", "invite_id": "invite-secret",
		})
	}))
	defer badServer.Close()
	badClient, err := NewClient(badServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = badClient.JoinRoom(context.Background(), RoomJoinRequest{DisplayName: "MEMBER-PC", PublicKey: "member-public", Platform: "windows", ClientVersion: "0.1.0"})
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unexpected secret response error = %v", err)
	}
}
