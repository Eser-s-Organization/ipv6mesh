package control

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestClientJoinAndHeartbeatUseAuthenticatedWireFormat(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/enrollments":
			if request.Method != http.MethodPost {
				t.Errorf("join method = %s", request.Method)
			}
			var body map[string]string
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Errorf("decode join body: %v", err)
			}
			if body["invite"] != "invite-token" || body["public_key"] != "public-key" {
				t.Errorf("unexpected join body: %#v", body)
			}
			writeTestJSON(writer, http.StatusCreated, map[string]any{
				"node":          map[string]any{"id": "node-1", "display_name": "Alice", "public_key": "public-key", "platform": "windows", "client_version": "0.1.0"},
				"membership":    map[string]any{"network_id": "network-1", "node_id": "node-1", "virtual_ipv4": "10.42.0.2", "role": "member", "status": "active"},
				"network":       map[string]any{"id": "network-1", "name": "mesh", "ipv4_pool": "10.42.0.0/24", "owner_id": "owner", "config_version": 2, "created_at": "2026-08-11T12:00:00Z"},
				"session":       map[string]any{"token": "session-token", "subject": "node-1", "network_id": "network-1", "expires_at": "2026-08-12T12:00:00Z"},
				"session_token": "session-token",
			})
		case "/v1/nodes/node-1/heartbeat":
			if request.Method != http.MethodPost {
				t.Errorf("heartbeat method = %s", request.Method)
			}
			if got := request.Header.Get("Authorization"); got != "Bearer session-token" {
				t.Errorf("heartbeat authorization = %q", got)
			}
			var body struct {
				ClientVersion string           `json:"client_version"`
				Endpoints     []map[string]any `json:"endpoints"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Errorf("decode heartbeat body: %v", err)
			}
			if body.ClientVersion != "0.2.0" || len(body.Endpoints) != 1 || body.Endpoints[0]["family"] != FamilyIPv6 {
				t.Errorf("unexpected heartbeat body: %#v", body)
			}
			writer.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Join(context.Background(), JoinRequest{
		Invite:        "invite-token",
		NodeID:        "node-1",
		DisplayName:   "Alice",
		PublicKey:     "public-key",
		Platform:      "windows",
		ClientVersion: "0.1.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.SessionToken != "session-token" || result.Network.ID != "network-1" || !result.Membership.VirtualIPv4.Equal(net.ParseIP("10.42.0.2")) {
		t.Fatalf("unexpected join result: %#v", result)
	}

	err = client.Heartbeat(context.Background(), "network-1", "node-1", result.SessionToken, "0.2.0", []EndpointCandidate{{
		Address:    net.ParseIP("2001:db8::1"),
		Port:       51820,
		Family:     FamilyIPv6,
		Interface:  "Ethernet",
		Priority:   1,
		ObservedAt: time.Date(2026, time.August, 11, 12, 0, 0, 0, time.UTC),
	}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestClientSnapshotStrictlyDecodesNestedPeers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/networks/network-1/snapshot" || request.Header.Get("Authorization") != "Bearer session-token" {
			t.Errorf("unexpected snapshot request: %s %s", request.Method, request.URL.String())
		}
		writeTestJSON(writer, http.StatusOK, map[string]any{
			"network_id":         "network-1",
			"generation":         7,
			"config_version":     3,
			"local_node_id":      "node-1",
			"local_virtual_ipv4": "10.42.0.2",
			"peers": []any{map[string]any{
				"node_id": "node-2", "display_name": "Bob", "public_key": "peer-key", "virtual_ipv4": "10.42.0.3",
				"node":       map[string]any{"id": "node-2", "display_name": "Bob", "public_key": "peer-key", "platform": "windows", "client_version": "0.1.0"},
				"membership": map[string]any{"network_id": "network-1", "node_id": "node-2", "virtual_ipv4": "10.42.0.3", "role": "member", "status": "active"},
				"endpoints":  []any{map[string]any{"node_id": "node-2", "address": "2001:db8::2", "port": 51820, "family": "ipv6", "interface": "Ethernet", "priority": 1, "observed_at": "2026-08-11T12:00:00Z"}},
			}},
			"relay_assignment": nil,
			"generated_at":     "2026-08-11T12:00:01Z",
		})
	}))
	defer server.Close()

	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := client.Snapshot(context.Background(), "network-1", "session-token")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Generation != 7 || len(snapshot.Peers) != 1 || !snapshot.Peers[0].VirtualIPv4.Equal(net.ParseIP("10.42.0.3")) {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
	if len(snapshot.Peers[0].Endpoints) != 1 || snapshot.Peers[0].Endpoints[0].Family != FamilyIPv6 {
		t.Fatalf("unexpected peer endpoints: %#v", snapshot.Peers[0].Endpoints)
	}
}

func TestClientMapsUnauthorizedAndRejectsUnknownResponseFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/networks/network-1/snapshot" {
			writeTestJSON(writer, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		writeTestJSON(writer, http.StatusOK, map[string]any{"unknown": true})
	}))
	defer server.Close()

	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Snapshot(context.Background(), "network-1", "session-token")
	if !errors.Is(err, ErrControlUnauthorized) {
		t.Fatalf("snapshot error = %v, want unauthorized sentinel", err)
	}

	badServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writeTestJSON(writer, http.StatusOK, map[string]any{"unknown": true})
	}))
	defer badServer.Close()
	badClient, err := NewClient(badServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = badClient.Snapshot(context.Background(), "network-1", "session-token")
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown response error = %v", err)
	}
}

func TestDecodeEventSnapshot(t *testing.T) {
	event, err := DecodeEvent([]byte(`{"type":"snapshot","snapshot":{"network_id":"network-1","generation":9,"config_version":1,"local_node_id":"node-1","local_virtual_ipv4":"10.42.0.2","peers":[],"generated_at":"2026-08-11T12:00:00Z"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if event.Type != "snapshot" || event.Snapshot == nil || event.Snapshot.Generation != 9 {
		t.Fatalf("unexpected event: %#v", event)
	}
	if _, err := DecodeEvent([]byte(`{"type":"unknown"}`)); err == nil {
		t.Fatal("expected unknown event type to fail")
	}
}

func TestClientEventsOpensAuthenticatedWebSocket(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/events" || request.Header.Get("Authorization") != "Bearer session-token" {
			http.Error(writer, "unexpected request", http.StatusUnauthorized)
			return
		}
		connection, err := upgrader.Upgrade(writer, request, nil)
		if err != nil {
			return
		}
		defer connection.Close()
		_ = connection.WriteJSON(map[string]any{"type": "snapshot", "snapshot": map[string]any{
			"network_id": "network-1", "generation": 10, "config_version": 2, "local_node_id": "node-1", "local_virtual_ipv4": "10.42.0.2", "peers": []any{}, "generated_at": "2026-08-11T12:00:00Z",
		}})
	}))
	defer server.Close()

	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := client.Events(context.Background(), "network-1", "session-token")
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	_, message, err := connection.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	event, err := DecodeEvent(message)
	if err != nil {
		t.Fatal(err)
	}
	if event.Snapshot == nil || event.Snapshot.Generation != 10 {
		t.Fatalf("unexpected WebSocket event: %#v", event)
	}
}

func TestNewClientDoesNotInheritAmbientHTTPProxy(t *testing.T) {
	client, err := NewClient("http://[2001:db8::1]:8080")
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	transport, ok := client.HTTPClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("HTTP transport type = %T, want *http.Transport", client.HTTPClient.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("control HTTP client inherited an ambient proxy")
	}
	if client.WebSocketDialer.Proxy != nil {
		t.Fatal("control WebSocket client inherited an ambient proxy")
	}
}

func TestClientLeaveUsesNodeSession(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/nodes/node-1/leave" || request.Header.Get("Authorization") != "Bearer session-token" {
			t.Errorf("unexpected leave request: %s %s auth=%q", request.Method, request.URL.Path, request.Header.Get("Authorization"))
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Leave(context.Background(), "network-1", "node-1", "session-token"); err != nil {
		t.Fatal(err)
	}
}

func TestClientWatchReconnectsWithBoundedBackoff(t *testing.T) {
	upgrader := websocket.Upgrader{}
	connections := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		connections++
		connection, err := upgrader.Upgrade(writer, request, nil)
		if err != nil {
			return
		}
		defer connection.Close()
		_ = connection.WriteJSON(map[string]any{"type": "snapshot", "snapshot": map[string]any{
			"network_id": "network-1", "generation": connections, "config_version": 1, "local_node_id": "node-1", "local_virtual_ipv4": "10.42.0.2", "peers": []any{}, "generated_at": "2026-08-11T12:00:00Z",
		}})
		if connections == 1 {
			return
		}
		_, _, _ = connection.ReadMessage()
	}))
	defer server.Close()

	client, err := NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	received := make(chan int64, 2)
	err = client.WatchWithOptions(ctx, "network-1", "session-token", func(snapshot NetworkSnapshot) error {
		received <- snapshot.Generation
		if snapshot.Generation == 2 {
			cancel()
		}
		return nil
	}, WatchOptions{InitialBackoff: time.Millisecond, MaxBackoff: 5 * time.Millisecond})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("watch error = %v, want context cancellation", err)
	}
	if connections < 2 {
		t.Fatalf("watch did not reconnect, connections = %d", connections)
	}
	if len(received) != 2 {
		t.Fatalf("received generations = %d, want two", len(received))
	}
}

func writeTestJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
