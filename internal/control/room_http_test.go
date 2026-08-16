package control_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
