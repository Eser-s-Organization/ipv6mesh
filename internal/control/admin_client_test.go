package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAdminClientCreatesNetworkAndInviteWithBearerToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer admin-token" {
			t.Errorf("authorization = %q", request.Header.Get("Authorization"))
		}
		if request.URL.Path == "/v1/networks" {
			var body map[string]string
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Errorf("network request decode: %v", err)
			}
			if body["name"] != "mesh" || body["pool"] != "10.42.0.0/24" {
				t.Errorf("network body = %#v", body)
			}
			if body["id"] != "" && body["id"] != "requested-network-id" {
				t.Errorf("network id = %#v", body["id"])
			}
			writeTestJSON(writer, http.StatusCreated, map[string]any{"id": "network-1", "name": "mesh", "pool": "10.42.0.0/24", "ipv4_pool": "10.42.0.0/24", "owner_id": "owner", "config_version": 1, "created_at": "2026-08-11T12:00:00Z"})
			return
		}
		if request.URL.Path == "/v1/networks/network-1/invites" {
			var body map[string]string
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Errorf("invite request decode: %v", err)
			}
			if body["expires_in"] != "1h" {
				t.Errorf("invite body = %#v", body)
			}
			writeTestJSON(writer, http.StatusCreated, map[string]any{"invite_id": "invite-1", "network_id": "network-1", "token": "one-time-token", "expires_at": "2026-08-11T13:00:00Z"})
			return
		}
		http.NotFound(writer, request)
	}))
	defer server.Close()

	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	network, err := client.CreateNetwork(context.Background(), "mesh", "10.42.0.0/24", "admin-token")
	if err != nil {
		t.Fatal(err)
	}
	if network.ID != "network-1" || network.IPv4Pool != "10.42.0.0/24" {
		t.Fatalf("unexpected network result: %#v", network)
	}
	requested, err := client.CreateNetworkWithID(context.Background(), "mesh", "10.42.0.0/24", "requested-network-id", "admin-token")
	if err != nil {
		t.Fatal(err)
	}
	if requested.ID != "network-1" {
		t.Fatalf("unexpected requested network result: %#v", requested)
	}
	invite, err := client.CreateInvite(context.Background(), network.ID, "1h", "admin-token")
	if err != nil {
		t.Fatal(err)
	}
	if invite.Token != "one-time-token" || invite.NetworkID != network.ID {
		t.Fatalf("unexpected invite result: %#v", invite)
	}
}
