package control_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Eser-s-Organization/ipv6mesh/internal/auth"
	"github.com/Eser-s-Organization/ipv6mesh/internal/control"
	"github.com/Eser-s-Organization/ipv6mesh/internal/db"
)

func TestRoomCreationRequiresModeAndBootstrapToken(t *testing.T) {
	enabled := newRoomHTTPFixture(t, true)
	response := enabled.do(http.MethodPost, "/v1/room", `{"name":"IPv6Mesh-HOST","ipv4_pool":"10.42.0.0/24"}`, "")
	assertRoomError(t, response, http.StatusUnauthorized, "unauthorized")

	disabled := newRoomHTTPFixture(t, false)
	response = disabled.do(http.MethodPost, "/v1/room", `{"name":"IPv6Mesh-HOST","ipv4_pool":"10.42.0.0/24"}`, "Bearer bootstrap")
	assertRoomError(t, response, http.StatusNotFound, "room_mode_disabled")
}

func TestRoomCreationAllowsExactlyOneActiveRoom(t *testing.T) {
	fixture := newRoomHTTPFixture(t, true)
	first := fixture.do(http.MethodPost, "/v1/room", `{"name":"IPv6Mesh-HOST","ipv4_pool":"10.42.0.0/24"}`, "Bearer bootstrap")
	if first.Code != http.StatusCreated {
		t.Fatalf("first room = %d %s", first.Code, first.Body.String())
	}
	second := fixture.do(http.MethodPost, "/v1/room", `{"name":"second","ipv4_pool":"10.42.0.0/24"}`, "Bearer bootstrap")
	assertRoomError(t, second, http.StatusConflict, "room_already_exists")
}

func TestRoomJoinCreatesAndConsumesInternalInvite(t *testing.T) {
	fixture := newRoomHTTPFixture(t, true)
	fixture.createRoom(t)
	response := fixture.do(http.MethodPost, "/v1/room/join",
		`{"public_key":"member-public","display_name":"MEMBER-PC","platform":"windows","client_version":"0.1.0"}`, "")
	if response.Code != http.StatusCreated {
		t.Fatalf("join = %d %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, forbidden := range []string{"invite_id", "invite-", ".room-secret"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("room join leaked %q: %s", forbidden, body)
		}
	}
	var result struct {
		Membership struct {
			VirtualIPv4 string `json:"virtual_ipv4"`
		} `json:"membership"`
		SessionToken string `json:"session_token"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Membership.VirtualIPv4 == "" || result.SessionToken == "" {
		t.Fatalf("incomplete result: %+v", result)
	}
}

func TestRoomJoinBeforeCreationReturnsRoomNotReady(t *testing.T) {
	fixture := newRoomHTTPFixture(t, true)
	response := fixture.do(http.MethodPost, "/v1/room/join",
		`{"public_key":"member-public","display_name":"MEMBER-PC","platform":"windows","client_version":"0.1.0"}`, "")
	assertRoomError(t, response, http.StatusNotFound, "room_not_ready")
}

func TestRoomJoinRevokesInviteAfterValidationFailure(t *testing.T) {
	fixture := newRoomHTTPFixture(t, true)
	fixture.createRoom(t)
	response := fixture.do(http.MethodPost, "/v1/room/join",
		`{"public_key":"","display_name":"MEMBER-PC","platform":"windows","client_version":"0.1.0"}`, "")
	assertRoomError(t, response, http.StatusUnprocessableEntity, "invalid_node")

	rawToken := "invite-1." + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x41}, 32))
	_, err := fixture.repository.ConsumeInvite(
		context.Background(), "invite-1", auth.HashToken(rawToken),
		time.Date(2026, 8, 16, 12, 1, 0, 0, time.UTC),
	)
	if !errors.Is(err, control.ErrInviteRevoked) {
		t.Fatalf("consume cleaned invite error = %v, want ErrInviteRevoked", err)
	}
}

func TestRoomJoinRateLimitsPerSourceAndResets(t *testing.T) {
	fixture := newRoomHTTPFixtureWithLimits(t, 10, 100)
	fixture.createRoom(t)
	invalid := `{"public_key":"","display_name":"PC","platform":"windows","client_version":"0.1.0"}`
	for attempt := 0; attempt < 10; attempt++ {
		response := fixture.doFrom("[2001:db8::20]:50000", http.MethodPost, "/v1/room/join", invalid, "")
		if response.Code == http.StatusTooManyRequests {
			t.Fatalf("attempt %d was limited early", attempt+1)
		}
	}
	limited := fixture.doFrom("[2001:db8::20]:50000", http.MethodPost, "/v1/room/join", invalid, "")
	assertRoomError(t, limited, http.StatusTooManyRequests, "join_rate_limited")
	fixture.now = fixture.now.Add(61 * time.Second)
	reset := fixture.doFrom("[2001:db8::20]:50000", http.MethodPost, "/v1/room/join", invalid, "")
	if reset.Code == http.StatusTooManyRequests {
		t.Fatal("limiter did not reset")
	}
}

func TestRoomJoinRateLimitsGlobally(t *testing.T) {
	fixture := newRoomHTTPFixtureWithLimits(t, 200, 100)
	fixture.createRoom(t)
	invalid := `{"public_key":"","display_name":"PC","platform":"windows","client_version":"0.1.0"}`
	for attempt := 0; attempt < 100; attempt++ {
		remote := fmt.Sprintf("[2001:db8::%x]:50000", attempt+1)
		if response := fixture.doFrom(remote, http.MethodPost, "/v1/room/join", invalid, ""); response.Code == http.StatusTooManyRequests {
			t.Fatalf("global attempt %d was limited early", attempt+1)
		}
	}
	response := fixture.doFrom("[2001:db8::ffff]:50000", http.MethodPost, "/v1/room/join", invalid, "")
	assertRoomError(t, response, http.StatusTooManyRequests, "join_rate_limited")
}

func TestRoomJoinRejectsOversizedBody(t *testing.T) {
	fixture := newRoomHTTPFixture(t, true)
	fixture.createRoom(t)
	response := fixture.do(http.MethodPost, "/v1/room/join", strings.Repeat("x", (64<<10)+1), "")
	assertRoomError(t, response, http.StatusRequestEntityTooLarge, "request_too_large")
}

func TestRoomJoinConcurrentAllocationsAreUnique(t *testing.T) {
	fixture := newConcurrentRoomHTTPFixture(t)
	fixture.createRoom(t)
	const attempts = 8
	responses := make(chan *httptest.ResponseRecorder, attempts)
	var waitGroup sync.WaitGroup
	for index := 0; index < attempts; index++ {
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			body := fmt.Sprintf(`{"public_key":"member-public-%d","display_name":"MEMBER-%d","platform":"windows","client_version":"0.1.0"}`, index, index)
			responses <- fixture.doFrom(fmt.Sprintf("[2001:db8::%x]:50000", index+10), http.MethodPost, "/v1/room/join", body, "")
		}(index)
	}
	waitGroup.Wait()
	close(responses)

	addresses := make(map[string]struct{}, attempts)
	for response := range responses {
		if response.Code != http.StatusCreated {
			t.Fatalf("concurrent join = %d %s", response.Code, response.Body.String())
		}
		var body struct {
			Membership struct {
				VirtualIPv4 string `json:"virtual_ipv4"`
			} `json:"membership"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if _, exists := addresses[body.Membership.VirtualIPv4]; exists {
			t.Fatalf("duplicate virtual IPv4 %q", body.Membership.VirtualIPv4)
		}
		addresses[body.Membership.VirtualIPv4] = struct{}{}
	}
	if len(addresses) != attempts {
		t.Fatalf("unique addresses = %d, want %d", len(addresses), attempts)
	}
}

func TestRoomJoinRejectsDuplicatePublicKey(t *testing.T) {
	fixture := newRoomHTTPFixture(t, true)
	fixture.createRoom(t)
	body := `{"public_key":"duplicate-key","display_name":"PC","platform":"windows","client_version":"0.1.0"}`
	first := fixture.do(http.MethodPost, "/v1/room/join", body, "")
	if first.Code != http.StatusCreated {
		t.Fatalf("first join = %d %s", first.Code, first.Body.String())
	}
	second := fixture.do(http.MethodPost, "/v1/room/join", body, "")
	assertRoomError(t, second, http.StatusConflict, "node_already_joined")
}

func TestRoomJoinReportsPoolExhaustion(t *testing.T) {
	fixture := newRoomHTTPFixture(t, true)
	response := fixture.do(http.MethodPost, "/v1/room", `{"name":"IPv6Mesh-HOST","ipv4_pool":"10.42.0.0/30"}`, "Bearer bootstrap")
	if response.Code != http.StatusCreated {
		t.Fatalf("create small room = %d %s", response.Code, response.Body.String())
	}
	for index := 0; index < 2; index++ {
		body := fmt.Sprintf(`{"public_key":"pool-key-%d","display_name":"PC-%d","platform":"windows","client_version":"0.1.0"}`, index, index)
		joined := fixture.do(http.MethodPost, "/v1/room/join", body, "")
		if joined.Code != http.StatusCreated {
			t.Fatalf("pool join %d = %d %s", index, joined.Code, joined.Body.String())
		}
	}
	full := fixture.do(http.MethodPost, "/v1/room/join", `{"public_key":"pool-key-full","display_name":"PC-full","platform":"windows","client_version":"0.1.0"}`, "")
	assertRoomError(t, full, http.StatusConflict, "room_full")
}

type lockedReader struct {
	mu     sync.Mutex
	reader io.Reader
}

func (reader *lockedReader) Read(destination []byte) (int, error) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return reader.reader.Read(destination)
}

func newConcurrentRoomHTTPFixture(t *testing.T) *roomHTTPFixture {
	t.Helper()
	repository := db.NewMemoryRepository()
	fixture := &roomHTTPFixture{repository: repository, now: time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)}
	var idMu sync.Mutex
	index := 0
	fixture.handler = control.NewHandler(repository, control.HandlerOptions{
		BootstrapToken: "bootstrap",
		RoomMode:       true,
		Clock:          func() time.Time { return fixture.now },
		NewID: func() string {
			idMu.Lock()
			defer idMu.Unlock()
			value := fmt.Sprintf("generated-%d", index)
			index++
			return value
		},
		TokenRandom: &lockedReader{reader: rand.Reader},
	})
	return fixture
}

type roomHTTPFixture struct {
	handler    *control.Handler
	repository *db.MemoryRepository
	now        time.Time
}

func newRoomHTTPFixture(t *testing.T, enabled bool) *roomHTTPFixture {
	return newRoomHTTPFixtureWithOptions(t, enabled, 0, 0)
}

func newRoomHTTPFixtureWithLimits(t *testing.T, perIP, global int) *roomHTTPFixture {
	return newRoomHTTPFixtureWithOptions(t, true, perIP, global)
}

func newRoomHTTPFixtureWithOptions(t *testing.T, enabled bool, perIP, global int) *roomHTTPFixture {
	t.Helper()
	repository := db.NewMemoryRepository()
	fixture := &roomHTTPFixture{
		repository: repository,
		now:        time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC),
	}
	ids := []string{"room-1", "invite-1", "node-1", "invite-2", "node-2"}
	index := 0
	fixture.handler = control.NewHandler(repository, control.HandlerOptions{
		BootstrapToken: "bootstrap",
		RoomMode:       enabled,
		RoomJoinPerIP:  perIP,
		RoomJoinGlobal: global,
		Clock:          func() time.Time { return fixture.now },
		NewID: func() string {
			if index >= len(ids) {
				return fmt.Sprintf("generated-%d", index)
			}
			value := ids[index]
			index++
			return value
		},
		TokenRandom: bytes.NewReader(bytes.Repeat([]byte{0x41}, 8192)),
	})
	return fixture
}

func (fixture *roomHTTPFixture) do(method, path, body, authorization string) *httptest.ResponseRecorder {
	return fixture.doFrom("[2001:db8::10]:50000", method, path, body, authorization)
}

func (fixture *roomHTTPFixture) doFrom(remote, method, path, body, authorization string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.RemoteAddr = remote
	request.Header.Set("X-Request-ID", "room-test")
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	response := httptest.NewRecorder()
	fixture.handler.ServeHTTP(response, request)
	return response
}

func (fixture *roomHTTPFixture) createRoom(t *testing.T) {
	t.Helper()
	response := fixture.do(http.MethodPost, "/v1/room",
		`{"name":"IPv6Mesh-HOST","ipv4_pool":"10.42.0.0/24"}`,
		"Bearer bootstrap")
	if response.Code != http.StatusCreated {
		t.Fatalf("create room = %d %s", response.Code, response.Body.String())
	}
}

func assertRoomError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var body map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != status || body["error"] != code {
		t.Fatalf("response = %d %#v, want %d %q", response.Code, body, status, code)
	}
}
