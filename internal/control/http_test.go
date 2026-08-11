package control_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Eser-s-Organization/ipv6mesh/internal/auth"
	"github.com/Eser-s-Organization/ipv6mesh/internal/control"
	"github.com/Eser-s-Organization/ipv6mesh/internal/db"
	"github.com/gorilla/websocket"
)

var httpTestNow = time.Date(2026, time.August, 11, 3, 0, 0, 0, time.UTC)

type httpFixture struct {
	repository *db.MemoryRepository
	sessions   *auth.SessionStore
	handler    http.Handler
	ids        *testIDSource
}

type testIDSource struct {
	values []string
	index  int
}

func (source *testIDSource) Next() string {
	if source.index >= len(source.values) {
		source.index++
		return "generated-" + time.Now().Format("150405.000000000")
	}
	value := source.values[source.index]
	source.index++
	return value
}

type incrementingReader struct{ next uint32 }

func (reader *incrementingReader) Read(target []byte) (int, error) {
	for index := range target {
		target[index] = byte(atomic.AddUint32(&reader.next, 1))
	}
	return len(target), nil
}

func newHTTPFixture(t *testing.T, pool string) *httpFixture {
	t.Helper()
	repository := db.NewMemoryRepository()
	ids := &testIDSource{values: []string{"network-1", "invite-1", "invite-2", "invite-3", "node-a", "node-b", "node-c"}}
	sessions := auth.NewSessionStoreWithOptions(auth.SessionStoreOptions{
		TTL:    time.Hour,
		Now:    func() time.Time { return httpTestNow },
		Random: &incrementingReader{},
	})
	handler := control.NewHandler(repository, control.HandlerOptions{
		BootstrapToken: "bootstrap-token",
		SessionStore:   sessions,
		InviteTTL:      time.Hour,
		Clock:          func() time.Time { return httpTestNow },
		NewID:          ids.Next,
		TokenRandom:    &incrementingReader{},
	})
	fixture := &httpFixture{repository: repository, sessions: sessions, handler: handler, ids: ids}
	createNetworkResponse := fixture.doJSON(t, http.MethodPost, "/v1/networks", `{"name":"mesh","pool":"`+pool+`"}`, "Bearer bootstrap-token", "network-request")
	if createNetworkResponse.StatusCode != http.StatusCreated {
		t.Fatalf("create network status = %d, body=%s", createNetworkResponse.StatusCode, createNetworkResponse.Body)
	}
	return fixture
}

type httpResponse struct {
	StatusCode int
	Header     http.Header
	Body       string
}

func (fixture *httpFixture) doJSON(t *testing.T, method, path, body, authorization, requestID string) httpResponse {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	if requestID != "" {
		request.Header.Set("X-Request-ID", requestID)
	}
	response := httptest.NewRecorder()
	fixture.handler.ServeHTTP(response, request)
	result := httpResponse{StatusCode: response.Code, Header: response.Header(), Body: response.Body.String()}
	if result.Header.Get("X-Request-ID") == "" {
		t.Fatalf("%s %s response omitted X-Request-ID", method, path)
	}
	return result
}

func responseObject(t *testing.T, response httpResponse) map[string]any {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal([]byte(response.Body), &object); err != nil {
		t.Fatalf("decode response %d: %v; body=%s", response.StatusCode, err, response.Body)
	}
	return object
}

func objectString(t *testing.T, object map[string]any, key string) string {
	t.Helper()
	value, ok := object[key].(string)
	if !ok || value == "" {
		t.Fatalf("response field %q = %#v", key, object[key])
	}
	return value
}

func (fixture *httpFixture) createInvite(t *testing.T) string {
	t.Helper()
	response := fixture.doJSON(t, http.MethodPost, "/v1/networks/network-1/invites", `{"expires_in":"1h"}`, "Bearer bootstrap-token", "invite-request")
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create invite status = %d, body=%s", response.StatusCode, response.Body)
	}
	return objectString(t, responseObject(t, response), "token")
}

func (fixture *httpFixture) enroll(t *testing.T, invite, publicKey string) (string, string) {
	t.Helper()
	body := `{"invite":"` + invite + `","display_name":"` + publicKey + `","public_key":"` + publicKey + `","platform":"windows","client_version":"0.1.0"}`
	response := fixture.doJSON(t, http.MethodPost, "/v1/enrollments", body, "", "enroll-request")
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("enroll status = %d, body=%s", response.StatusCode, response.Body)
	}
	object := responseObject(t, response)
	nodeObject, ok := object["node"].(map[string]any)
	if !ok {
		t.Fatalf("enrollment node response = %#v", object["node"])
	}
	return objectString(t, nodeObject, "id"), objectString(t, object, "session_token")
}

func TestHTTPMilestoneCreatesInvitesEnrollsSnapshotsAndDeletesNode(t *testing.T) {
	fixture := newHTTPFixture(t, "10.42.0.0/29")
	inviteA := fixture.createInvite(t)
	inviteB := fixture.createInvite(t)
	nodeA, sessionA := fixture.enroll(t, inviteA, "public-a")
	nodeB, _ := fixture.enroll(t, inviteB, "public-b")
	if nodeA == "" || nodeB == "" || nodeA == nodeB {
		t.Fatalf("unexpected node IDs: %q, %q", nodeA, nodeB)
	}

	snapshotResponse := fixture.doJSON(t, http.MethodGet, "/v1/networks/network-1/snapshot", "", "Bearer "+sessionA, "snapshot-request")
	if snapshotResponse.StatusCode != http.StatusOK {
		t.Fatalf("snapshot status = %d, body=%s", snapshotResponse.StatusCode, snapshotResponse.Body)
	}
	snapshot := responseObject(t, snapshotResponse)
	if generation, ok := snapshot["generation"].(float64); !ok || generation < 3 {
		t.Fatalf("snapshot generation = %#v", snapshot["generation"])
	}
	peers, ok := snapshot["peers"].([]any)
	if !ok || len(peers) != 1 || peers[0].(map[string]any)["node_id"] != nodeB {
		t.Fatalf("snapshot peers = %#v", snapshot["peers"])
	}

	deleteResponse := fixture.doJSON(t, http.MethodDelete, "/v1/nodes/"+nodeB, "", "Bearer bootstrap-token", "delete-request")
	if deleteResponse.StatusCode != http.StatusNoContent || deleteResponse.Body != "" {
		t.Fatalf("delete response = %d %q", deleteResponse.StatusCode, deleteResponse.Body)
	}
	afterDelete := fixture.doJSON(t, http.MethodGet, "/v1/networks/network-1/snapshot", "", "Bearer "+sessionA, "snapshot-after-delete")
	if afterDelete.StatusCode != http.StatusOK {
		t.Fatalf("snapshot after delete status = %d, body=%s", afterDelete.StatusCode, afterDelete.Body)
	}
	if peers := responseObject(t, afterDelete)["peers"].([]any); len(peers) != 0 {
		t.Fatalf("deleted node remains in peers: %#v", peers)
	}
}

func TestHTTPMapsCredentialResourceAndJSONErrors(t *testing.T) {
	fixture := newHTTPFixture(t, "10.42.0.0/29")
	tests := []struct {
		name          string
		method        string
		path          string
		body          string
		authorization string
		wantStatus    int
	}{
		{name: "missing credential", method: http.MethodPost, path: "/v1/networks", body: `{}`, wantStatus: http.StatusUnauthorized},
		{name: "malformed credential", method: http.MethodPost, path: "/v1/networks", body: `{}`, authorization: "Basic abc", wantStatus: http.StatusUnauthorized},
		{name: "invalid JSON", method: http.MethodPost, path: "/v1/networks", body: `{"name":`, authorization: "Bearer bootstrap-token", wantStatus: http.StatusUnprocessableEntity},
		{name: "unknown network", method: http.MethodPost, path: "/v1/networks/missing/invites", body: `{"expires_in":"1h"}`, authorization: "Bearer bootstrap-token", wantStatus: http.StatusNotFound},
		{name: "invalid duration", method: http.MethodPost, path: "/v1/networks/network-1/invites", body: `{"expires_in":"not-duration"}`, authorization: "Bearer bootstrap-token", wantStatus: http.StatusUnprocessableEntity},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := fixture.doJSON(t, test.method, test.path, test.body, test.authorization, "error-request")
			if response.StatusCode != test.wantStatus {
				t.Fatalf("status = %d, body=%s; want %d", response.StatusCode, response.Body, test.wantStatus)
			}
		})
	}
}

func TestHTTPEnforcesNetworkAndRoleAuthorization(t *testing.T) {
	fixture := newHTTPFixture(t, "10.42.0.0/29")
	invite := fixture.createInvite(t)
	nodeID, nodeSession := fixture.enroll(t, invite, "public-a")
	if response := fixture.doJSON(t, http.MethodDelete, "/v1/nodes/"+nodeID, "", "Bearer "+nodeSession, "node-delete"); response.StatusCode != http.StatusForbidden {
		t.Fatalf("node delete status = %d, body=%s; want 403", response.StatusCode, response.Body)
	}

	secondNetwork := fixture.doJSON(t, http.MethodPost, "/v1/networks", `{"name":"other","pool":"10.43.0.0/29"}`, "Bearer bootstrap-token", "network-two")
	if secondNetwork.StatusCode != http.StatusCreated {
		t.Fatalf("create second network status = %d, body=%s", secondNetwork.StatusCode, secondNetwork.Body)
	}
	secondNetworkID := objectString(t, responseObject(t, secondNetwork), "id")
	if response := fixture.doJSON(t, http.MethodGet, "/v1/networks/"+secondNetworkID+"/snapshot", "", "Bearer "+nodeSession, "wrong-network"); response.StatusCode != http.StatusForbidden {
		t.Fatalf("wrong-network snapshot status = %d, body=%s; want 403", response.StatusCode, response.Body)
	}
	fixture.sessions.RevokeNetwork("network-1")
	if response := fixture.doJSON(t, http.MethodGet, "/v1/networks/network-1/snapshot", "", "Bearer "+nodeSession, "revoked-network"); response.StatusCode != http.StatusForbidden {
		t.Fatalf("revoked-network snapshot status = %d, body=%s; want 403", response.StatusCode, response.Body)
	}
}

func TestHTTPHeartbeatValidatesEndpointsAndAllowsOnlySelfForNode(t *testing.T) {
	fixture := newHTTPFixture(t, "10.42.0.0/29")
	inviteA := fixture.createInvite(t)
	inviteB := fixture.createInvite(t)
	nodeA, sessionA := fixture.enroll(t, inviteA, "public-a")
	nodeB, _ := fixture.enroll(t, inviteB, "public-b")
	invalid := `{"endpoints":[{"address":"not-an-ip","port":51820,"family":"ipv6","interface":"WiFi","priority":1,"observed_at":"not-rfc3339"}]}`
	if response := fixture.doJSON(t, http.MethodPost, "/v1/nodes/"+nodeA+"/heartbeat", invalid, "Bearer "+sessionA, "heartbeat-invalid"); response.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("invalid heartbeat status = %d, body=%s; want 422", response.StatusCode, response.Body)
	}
	if response := fixture.doJSON(t, http.MethodPost, "/v1/nodes/"+nodeB+"/heartbeat", `{}`, "Bearer "+sessionA, "heartbeat-other"); response.StatusCode != http.StatusForbidden {
		t.Fatalf("other-node heartbeat status = %d, body=%s; want 403", response.StatusCode, response.Body)
	}
	valid := `{"endpoints":[{"address":"2001:db8::10","port":51820,"family":"ipv6","interface":"WiFi","priority":1,"observed_at":"2026-08-11T03:00:00Z"}],"client_version":"0.2.0"}`
	if response := fixture.doJSON(t, http.MethodPost, "/v1/nodes/"+nodeA+"/heartbeat", valid, "Bearer "+sessionA, "heartbeat-valid"); response.StatusCode != http.StatusNoContent {
		t.Fatalf("valid heartbeat status = %d, body=%s; want 204", response.StatusCode, response.Body)
	}
	node, err := fixture.repository.GetNode(context.Background(), nodeA)
	if err != nil || node.ClientVersion != "0.2.0" || !node.LastSeen.Equal(httpTestNow) {
		t.Fatalf("heartbeat did not update node: %+v, %v", node, err)
	}
}

func TestHTTPRejectsDuplicateAndPoolExhaustion(t *testing.T) {
	fixture := newHTTPFixture(t, "10.42.0.0/30")
	inviteA := fixture.createInvite(t)
	inviteB := fixture.createInvite(t)
	inviteC := fixture.createInvite(t)
	_, _ = fixture.enroll(t, inviteA, "public-a")
	if response := fixture.doJSON(t, http.MethodPost, "/v1/enrollments", `{"invite":"`+inviteB+`","display_name":"b","public_key":"public-a","platform":"windows","client_version":"0.1.0"}`, "", "duplicate-key"); response.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate key status = %d, body=%s; want 409", response.StatusCode, response.Body)
	}
	_, _ = fixture.enroll(t, inviteB, "public-b")
	if response := fixture.doJSON(t, http.MethodPost, "/v1/enrollments", `{"invite":"`+inviteB+`","display_name":"again","public_key":"public-c","platform":"windows","client_version":"0.1.0"}`, "", "consumed-invite"); response.StatusCode != http.StatusConflict {
		t.Fatalf("consumed invite status = %d, body=%s; want 409", response.StatusCode, response.Body)
	}
	if response := fixture.doJSON(t, http.MethodPost, "/v1/enrollments", `{"invite":"`+inviteC+`","display_name":"c","public_key":"public-c","platform":"windows","client_version":"0.1.0"}`, "", "pool-exhausted"); response.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("pool exhaustion status = %d, body=%s; want 422", response.StatusCode, response.Body)
	}
}

func TestHTTPWebSocketSendsInitialControlSnapshot(t *testing.T) {
	fixture := newHTTPFixture(t, "10.42.0.0/29")
	invite := fixture.createInvite(t)
	_, session := fixture.enroll(t, invite, "public-a")
	server := httptest.NewServer(fixture.handler)
	defer server.Close()
	websocketURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/events"
	connection, response, err := websocket.DefaultDialer.Dial(websocketURL, http.Header{"Authorization": []string{"Bearer " + session}})
	if err != nil {
		if response == nil {
			t.Fatalf("dial events: %v", err)
		}
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("dial events: %v status=%d body=%s", err, response.StatusCode, body)
	}
	defer connection.Close()
	if response == nil || response.Header.Get("X-Request-ID") == "" {
		t.Fatal("WebSocket handshake omitted X-Request-ID")
	}
	_, message, err := connection.ReadMessage()
	if err != nil {
		t.Fatalf("read initial event: %v", err)
	}
	var event map[string]any
	if err := json.Unmarshal(message, &event); err != nil {
		t.Fatalf("decode initial event: %v", err)
	}
	if event["type"] != "snapshot" {
		t.Fatalf("initial event = %#v, want snapshot control event", event)
	}
}

func TestHTTPStrictJSONRejectsTrailingValues(t *testing.T) {
	fixture := newHTTPFixture(t, "10.42.0.0/29")
	response := fixture.doJSON(t, http.MethodPost, "/v1/networks", `{"name":"bad","pool":"10.42.0.0/29"}{}`, "Bearer bootstrap-token", "trailing-json")
	if response.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("trailing JSON status = %d, body=%s; want 422", response.StatusCode, response.Body)
	}
	if bytes.Contains([]byte(response.Body), []byte("bootstrap-token")) {
		t.Fatal("error response leaked bearer token")
	}
}
