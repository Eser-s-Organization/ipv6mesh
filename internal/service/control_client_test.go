package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Eser-s-Organization/ipv6mesh/internal/control"
)

func TestHTTPControlClientBridgesEnrollmentAndLeave(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/enrollments":
			var body map[string]string
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Errorf("join body: %v", err)
			}
			if body["node_id"] != "node-1" || body["platform"] != "windows" {
				t.Errorf("unexpected join identity: %#v", body)
			}
			writeServiceJSON(writer, http.StatusCreated, map[string]any{
				"node":          map[string]any{"id": "node-1", "display_name": "Alice", "public_key": "public-key", "platform": "windows", "client_version": "0.1.0"},
				"membership":    map[string]any{"network_id": "network-1", "node_id": "node-1", "virtual_ipv4": "10.42.0.2", "role": "member", "status": "active"},
				"network":       map[string]any{"id": "network-1", "name": "mesh", "ipv4_pool": "10.42.0.0/24", "config_version": 7, "owner_id": "owner", "created_at": "2026-08-11T12:00:00Z"},
				"session":       map[string]any{"token": "session-token", "subject": "node-1", "network_id": "network-1", "expires_at": "2026-08-12T12:00:00Z"},
				"session_token": "session-token",
			})
		case "/v1/nodes/node-1/leave":
			if request.Header.Get("Authorization") != "Bearer session-token" {
				t.Errorf("leave authorization = %q", request.Header.Get("Authorization"))
			}
			writer.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	client, err := control.NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	bridge := NewHTTPControlClient(client, "node-1", "windows", "0.1.0")
	joined, err := bridge.Join(context.Background(), JoinRequest{Invite: "invite-token", DisplayName: "Alice", PublicKey: "public-key"})
	if err != nil {
		t.Fatal(err)
	}
	if joined.DisplayName != "Alice" || joined.NetworkID != "network-1" || joined.VirtualIPv4 != "10.42.0.2" || joined.ConfigGeneration != 7 {
		t.Fatalf("unexpected bridged join result: %#v", joined)
	}
	if err := bridge.Leave(context.Background(), "network-1"); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPControlClientBridgesRoomJoinWithoutInvite(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/room/join" || request.Header.Get("Authorization") != "" {
			t.Fatalf("room join request = %s %s auth=%q", request.Method, request.URL.Path, request.Header.Get("Authorization"))
		}
		var body map[string]string
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["public_key"] != "public-key" || body["display_name"] != "MEMBER-PC" || body["platform"] != "windows" || body["client_version"] != "0.1.0" || len(body) != 4 {
			t.Fatalf("room join body = %#v", body)
		}
		writeServiceJSON(writer, http.StatusCreated, map[string]any{
			"node":          map[string]any{"id": "node-1", "display_name": "MEMBER-PC", "public_key": "public-key", "platform": "windows", "client_version": "0.1.0"},
			"membership":    map[string]any{"network_id": "room-1", "node_id": "node-1", "virtual_ipv4": "10.42.0.9", "role": "member", "status": "active"},
			"network":       map[string]any{"id": "room-1", "name": "room", "ipv4_pool": "10.42.0.0/24", "config_version": 2, "created_at": "2026-08-16T12:00:00Z"},
			"session":       map[string]any{"token": "session-token", "subject": "node-1", "network_id": "room-1", "expires_at": "2026-08-16T13:00:00Z"},
			"session_token": "session-token",
		})
	}))
	defer server.Close()
	client, err := control.NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	bridge := NewHTTPControlClient(client, "", "windows", "0.1.0")
	joined, err := bridge.JoinRoom(context.Background(), JoinRequest{DisplayName: "MEMBER-PC", PublicKey: "public-key"})
	if err != nil {
		t.Fatal(err)
	}
	if joined.DisplayName != "MEMBER-PC" || joined.NetworkID != "room-1" || joined.VirtualIPv4 != "10.42.0.9" || joined.ConfigGeneration != 2 {
		t.Fatalf("unexpected room join result: %#v", joined)
	}
}

func writeServiceJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
