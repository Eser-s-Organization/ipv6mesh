package control_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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

var (
	errCommitAfterCommit   = errors.New("commit status unknown after commit")
	errTransactionRollback = errors.New("transaction rolled back")
)

type commitErrorAfterCommitRepository struct {
	*db.MemoryRepository
}

func (repository *commitErrorAfterCommitRepository) WithTransaction(ctx context.Context, operation func(context.Context, control.Repository) error) error {
	if err := repository.MemoryRepository.WithTransaction(ctx, operation); err != nil {
		return err
	}
	return errCommitAfterCommit
}

type uncertainReadbackRepository struct {
	*db.MemoryRepository
	readbackErr error
}

func (repository *uncertainReadbackRepository) WithTransaction(ctx context.Context, operation func(context.Context, control.Repository) error) error {
	if err := repository.MemoryRepository.WithTransaction(ctx, operation); err != nil {
		return err
	}
	return errCommitAfterCommit
}

func (repository *uncertainReadbackRepository) GetNode(context.Context, string) (control.Node, error) {
	return control.Node{}, repository.readbackErr
}

func (repository *uncertainReadbackRepository) GetNodeNetworkIDs(context.Context, string) ([]string, error) {
	return nil, repository.readbackErr
}

type rollbackAfterCallbackRepository struct {
	*db.MemoryRepository
	transaction *db.MemoryRepository
}

func (repository *rollbackAfterCallbackRepository) WithTransaction(ctx context.Context, operation func(context.Context, control.Repository) error) error {
	if err := operation(ctx, repository.transaction); err != nil {
		return err
	}
	return errTransactionRollback
}

func newHTTPFixture(t *testing.T, pool string) *httpFixture {
	repository := db.NewMemoryRepository()
	return newHTTPFixtureWithRepositories(t, pool, repository, repository, time.Hour)
}

func newHTTPFixtureWithSessionTTL(t *testing.T, pool string, sessionTTL time.Duration) *httpFixture {
	repository := db.NewMemoryRepository()
	return newHTTPFixtureWithRepositories(t, pool, repository, repository, sessionTTL)
}

func newHTTPFixtureWithRepositories(t *testing.T, pool string, repository *db.MemoryRepository, handlerRepository control.TransactionalRepository, sessionTTL time.Duration) *httpFixture {
	t.Helper()
	ids := &testIDSource{values: []string{"network-1", "invite-1", "invite-2", "invite-3", "node-a", "node-b", "node-c"}}
	sessions := auth.NewSessionStoreWithOptions(auth.SessionStoreOptions{
		TTL:    sessionTTL,
		Now:    func() time.Time { return httpTestNow },
		Random: &incrementingReader{},
	})
	handler := control.NewHandler(handlerRepository, control.HandlerOptions{
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

func TestHTTPScopedAdminCannotCrossNetworkOrCreateUnscopedNetwork(t *testing.T) {
	fixture := newHTTPFixture(t, "10.42.0.0/29")
	inviteA := fixture.createInvite(t)
	nodeA, _ := fixture.enroll(t, inviteA, "public-a")

	secondNetwork := fixture.doJSON(t, http.MethodPost, "/v1/networks", `{"name":"other","pool":"10.43.0.0/29"}`, "Bearer bootstrap-token", "scoped-network-two")
	if secondNetwork.StatusCode != http.StatusCreated {
		t.Fatalf("create second network status = %d, body=%s", secondNetwork.StatusCode, secondNetwork.Body)
	}
	secondNetworkID := objectString(t, responseObject(t, secondNetwork), "id")
	secondInvite := fixture.doJSON(t, http.MethodPost, "/v1/networks/"+secondNetworkID+"/invites", `{"expires_in":"1h"}`, "Bearer bootstrap-token", "scoped-invite-two")
	if secondInvite.StatusCode != http.StatusCreated {
		t.Fatalf("create second invite status = %d, body=%s", secondInvite.StatusCode, secondInvite.Body)
	}
	nodeB, _ := fixture.enroll(t, objectString(t, responseObject(t, secondInvite), "token"), "public-b")

	scopedToken, _, err := fixture.sessions.IssueAdminNetworkSession("scoped-admin", "network-1")
	if err != nil {
		t.Fatalf("issue scoped admin session: %v", err)
	}
	authorization := "Bearer " + scopedToken
	if response := fixture.doJSON(t, http.MethodPost, "/v1/networks", `{"name":"unscoped","pool":"10.44.0.0/29"}`, authorization, "scoped-create-network"); response.StatusCode != http.StatusForbidden {
		t.Fatalf("scoped create network status = %d, body=%s; want 403", response.StatusCode, response.Body)
	}
	if response := fixture.doJSON(t, http.MethodPost, "/v1/networks/"+secondNetworkID+"/invites", `{"expires_in":"1h"}`, authorization, "scoped-create-invite"); response.StatusCode != http.StatusForbidden {
		t.Fatalf("scoped create cross-network invite status = %d, body=%s; want 403", response.StatusCode, response.Body)
	}
	if response := fixture.doJSON(t, http.MethodPost, "/v1/nodes/"+nodeA+"/heartbeat", `{}`, authorization, "scoped-heartbeat-own"); response.StatusCode != http.StatusNoContent {
		t.Fatalf("scoped heartbeat own node status = %d, body=%s; want 204", response.StatusCode, response.Body)
	}
	if response := fixture.doJSON(t, http.MethodPost, "/v1/nodes/"+nodeB+"/heartbeat", `{}`, authorization, "scoped-heartbeat-other"); response.StatusCode != http.StatusForbidden {
		t.Fatalf("scoped heartbeat cross-network status = %d, body=%s; want 403", response.StatusCode, response.Body)
	}
	if response := fixture.doJSON(t, http.MethodDelete, "/v1/nodes/"+nodeB, "", authorization, "scoped-delete-other"); response.StatusCode != http.StatusForbidden {
		t.Fatalf("scoped delete cross-network status = %d, body=%s; want 403", response.StatusCode, response.Body)
	}
}

func TestHTTPEnrollmentCommitErrorAfterCommitReturnsRegisteredSession(t *testing.T) {
	repository := db.NewMemoryRepository()
	handlerRepository := &commitErrorAfterCommitRepository{MemoryRepository: repository}
	fixture := newHTTPFixtureWithRepositories(t, "10.42.0.0/29", repository, handlerRepository, time.Hour)
	invite := fixture.createInvite(t)
	response := fixture.doJSON(t, http.MethodPost, "/v1/enrollments", `{"invite":"`+invite+`","node_id":"committed-node","display_name":"committed","public_key":"committed-key","platform":"windows","client_version":"0.1.0"}`, "", "commit-after-commit")
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("commit-after-commit status = %d, body=%s; want 201", response.StatusCode, response.Body)
	}
	object := responseObject(t, response)
	token := objectString(t, object, "session_token")
	if _, err := fixture.sessions.Authenticate(token); err != nil {
		t.Fatalf("returned session is not usable: %v", err)
	}
	if _, err := repository.GetNode(context.Background(), "committed-node"); err != nil {
		t.Fatalf("committed node missing after commit error: %v", err)
	}
	networkIDs, err := repository.GetNodeNetworkIDs(context.Background(), "committed-node")
	if err != nil || len(networkIDs) != 1 || networkIDs[0] != "network-1" {
		t.Fatalf("committed node network IDs = %#v, %v; want [network-1]", networkIDs, err)
	}
}

func TestHTTPEnrollmentUncertainReadbackReturnsRecoveryCredential(t *testing.T) {
	repository := db.NewMemoryRepository()
	handlerRepository := &uncertainReadbackRepository{MemoryRepository: repository, readbackErr: errors.New("readback unavailable")}
	fixture := newHTTPFixtureWithRepositories(t, "10.42.0.0/29", repository, handlerRepository, time.Hour)
	invite := fixture.createInvite(t)
	response := fixture.doJSON(t, http.MethodPost, "/v1/enrollments", `{"invite":"`+invite+`","node_id":"uncertain-node","display_name":"uncertain","public_key":"uncertain-key","platform":"windows","client_version":"0.1.0"}`, "", "uncertain-readback")
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("uncertain readback status = %d, body=%s; want 503", response.StatusCode, response.Body)
	}
	object := responseObject(t, response)
	if objectString(t, object, "error") != "enrollment_recovery_pending" {
		t.Fatalf("uncertain readback error = %#v", object["error"])
	}
	token := objectString(t, object, "session_token")
	if _, err := fixture.sessions.Authenticate(token); err != nil {
		t.Fatalf("recovery session is not retained: %v", err)
	}
	if _, err := repository.GetNode(context.Background(), "uncertain-node"); err != nil {
		t.Fatalf("committed node missing from backing repository: %v", err)
	}
}

func TestHTTPEnrollmentRollbackRevokesIssuedSession(t *testing.T) {
	repository := db.NewMemoryRepository()
	transaction := db.NewMemoryRepository()
	handlerRepository := &rollbackAfterCallbackRepository{MemoryRepository: repository, transaction: transaction}
	fixture := newHTTPFixtureWithRepositories(t, "10.42.0.0/29", repository, handlerRepository, time.Hour)
	invite := fixture.createInvite(t)
	network, err := repository.GetNetwork(context.Background(), "network-1")
	if err != nil {
		t.Fatalf("get root network: %v", err)
	}
	if err := transaction.CreateNetwork(context.Background(), network); err != nil {
		t.Fatalf("seed transaction network: %v", err)
	}
	inviteID := strings.SplitN(invite, ".", 2)[0]
	if err := transaction.CreateInvite(context.Background(), control.Invite{
		ID:        inviteID,
		NetworkID: network.ID,
		TokenHash: auth.HashToken(invite),
		ExpiresAt: httpTestNow.Add(time.Hour),
		CreatedAt: httpTestNow,
	}); err != nil {
		t.Fatalf("seed transaction invite: %v", err)
	}
	response := fixture.doJSON(t, http.MethodPost, "/v1/enrollments", `{"invite":"`+invite+`","node_id":"rolled-back-node","display_name":"rolled-back","public_key":"rolled-back-key","platform":"windows","client_version":"0.1.0"}`, "", "transaction-rollback")
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("rollback status = %d, body=%s; want 500", response.StatusCode, response.Body)
	}
	rawSessionToken := make([]byte, 32)
	for index := range rawSessionToken {
		rawSessionToken[index] = byte(index + 1)
	}
	issuedToken := base64.RawURLEncoding.EncodeToString(rawSessionToken)
	if _, err := fixture.sessions.Authenticate(issuedToken); !errors.Is(err, auth.ErrInvalidCredential) {
		t.Fatalf("rolled-back session authentication error = %v, want ErrInvalidCredential", err)
	}
	if _, err := repository.GetNode(context.Background(), "rolled-back-node"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("rolled-back node = %v, want ErrNotFound", err)
	}
}

func TestHTTPEnrollmentSessionFailureRollsBackNodeMembershipAndInvite(t *testing.T) {
	fixture := newHTTPFixtureWithSessionTTL(t, "10.42.0.0/29", 0)
	invite := fixture.createInvite(t)
	response := fixture.doJSON(t, http.MethodPost, "/v1/enrollments", `{"invite":"`+invite+`","node_id":"rollback-node","display_name":"rollback","public_key":"rollback-key","platform":"windows","client_version":"0.1.0"}`, "", "enrollment-session-failure")
	if response.StatusCode != http.StatusInternalServerError {
		t.Fatalf("session failure status = %d, body=%s; want 500", response.StatusCode, response.Body)
	}
	if _, err := fixture.repository.GetNode(context.Background(), "rollback-node"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("node after session failure = %v; want ErrNotFound", err)
	}
	network, err := fixture.repository.GetNetwork(context.Background(), "network-1")
	if err != nil {
		t.Fatalf("get network after session failure: %v", err)
	}
	if network.ConfigVersion != 1 {
		t.Fatalf("network config version after session failure = %d; want 1", network.ConfigVersion)
	}
	inviteID := strings.SplitN(invite, ".", 2)[0]
	consumed, err := fixture.repository.ConsumeInvite(context.Background(), inviteID, auth.HashToken(invite), httpTestNow)
	if err != nil {
		t.Fatalf("consume invite after session failure: %v", err)
	}
	if consumed.ID != inviteID || consumed.ConsumedByNodeID != "" || consumed.ConsumedAt == nil {
		t.Fatalf("invite after session failure = %+v", consumed)
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
