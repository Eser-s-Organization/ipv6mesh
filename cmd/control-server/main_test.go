package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Eser-s-Organization/ipv6mesh/internal/control"
)

func TestOpenRepositoryUsesExplicitMemoryMode(t *testing.T) {
	config := Config{
		RepositoryMode: "memory",
		BootstrapToken: "bootstrap-token",
		SessionTTL:     time.Hour,
		InviteTTL:      time.Hour,
	}
	repository, closeRepository, err := openRepository(config)
	if err != nil {
		t.Fatalf("open memory repository: %v", err)
	}
	if repository == nil {
		t.Fatal("open memory repository returned nil repository")
	}
	if closeRepository == nil {
		t.Fatal("open memory repository returned nil close function")
	}
	if err := closeRepository(); err != nil {
		t.Fatalf("close memory repository: %v", err)
	}
}

func TestNewHTTPServerServesControlHandler(t *testing.T) {
	config := Config{
		BootstrapToken: "bootstrap-token",
		SessionTTL:     time.Hour,
		InviteTTL:      time.Hour,
	}
	repository, closeRepository, err := openRepository(Config{RepositoryMode: "memory"})
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	defer closeRepository()

	server := newHTTPServer(config, repository)
	if server == nil || server.Handler == nil {
		t.Fatal("newHTTPServer returned an incomplete server")
	}
	testServer := httptest.NewServer(server.Handler)
	defer testServer.Close()

	client, err := control.NewClient(testServer.URL)
	if err != nil {
		t.Fatalf("new control client: %v", err)
	}
	client.Token = "bootstrap-token"
	if _, err := client.CreateNetwork(context.Background(), "friends", "10.42.0.0/24", ""); err != nil {
		t.Fatalf("create network through runtime handler: %v", err)
	}

	healthRequest := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	healthResponse := httptest.NewRecorder()
	server.Handler.ServeHTTP(healthResponse, healthRequest)
	if healthResponse.Code != http.StatusOK || healthResponse.Body.String() != "ok\n" {
		t.Fatalf("health response = (%d, %q), want (200, %q)", healthResponse.Code, healthResponse.Body.String(), "ok\n")
	}
}

func TestNewHTTPServerRejectsNilRepository(t *testing.T) {
	server := newHTTPServer(Config{}, nil)
	if server != nil {
		t.Fatal("newHTTPServer(nil repository) returned a server")
	}
}

func TestNewHTTPServerWiresRoomMode(t *testing.T) {
	config := Config{
		BootstrapToken: "bootstrap-token",
		SessionTTL:     time.Hour,
		InviteTTL:      time.Hour,
		RoomMode:       true,
	}
	repository, closeRepository, err := openRepository(Config{RepositoryMode: "memory"})
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	defer closeRepository()
	server := newHTTPServer(config, repository)
	if server == nil {
		t.Fatal("newHTTPServer returned nil")
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/room", strings.NewReader(`{"name":"IPv6Mesh-HOST","ipv4_pool":"10.42.0.0/24"}`))
	request.Header.Set("Authorization", "Bearer bootstrap-token")
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("room creation = %d %s, want 201", response.Code, response.Body.String())
	}
}
