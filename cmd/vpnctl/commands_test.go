package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/Eser-s-Organization/ipv6mesh/internal/control"
	"github.com/Eser-s-Organization/ipv6mesh/internal/ipc"
)

func TestParseCommandCoversServiceAndControlCommands(t *testing.T) {
	tests := []struct {
		name string
		args []string
		kind commandKind
		want ipc.Command
	}{
		{name: "status", args: []string{"status"}, kind: serviceCommand, want: ipc.CommandStatus},
		{name: "join", args: []string{"join", "--invite", "invite-token", "--name", "Alice"}, kind: serviceCommand, want: ipc.CommandJoin},
		{name: "network create", args: []string{"network", "create", "--name", "mesh", "--pool", "10.42.0.0/24"}, kind: controlCommand},
		{name: "network create with id", args: []string{"network", "create", "--name", "mesh", "--pool", "10.42.0.0/24", "--id", "mesh-id"}, kind: controlCommand},
		{name: "invite create", args: []string{"invite", "create", "--network", "network-1", "--expires", "1h"}, kind: controlCommand},
		{name: "room create", args: []string{"room", "create", "--name", "IPv6Mesh-HOST", "--pool", "10.42.0.0/24"}, kind: controlCommand},
		{name: "room endpoint", args: []string{"room", "endpoint", "--host-ipv6", "2001:db8::1"}, kind: localCommand},
		{name: "room join", args: []string{"room", "join", "--host-ipv6", "2001:db8::1", "--name", "MEMBER-PC"}, kind: serviceCommand, want: ipc.CommandJoinRoom},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command, err := parseCommand(test.args)
			if err != nil {
				t.Fatal(err)
			}
			if command.Kind != test.kind || (test.kind == serviceCommand && command.Service.Type != test.want) {
				t.Fatalf("parsed command = %#v", command)
			}
		})
	}
}

func TestParseCommandRejectsUnknownDuplicateAndMissingOptions(t *testing.T) {
	tests := [][]string{
		{"status", "--unexpected"},
		{"join", "--invite", "one", "--invite", "two", "--name", "Alice"},
		{"join", "--invite", "one"},
		{"network", "create", "--name", "mesh"},
		{"invite", "create", "--network", "network-1", "--expires", ""},
		{"connect", "--network", "network-1", "extra"},
		{"room", "endpoint"},
		{"room", "endpoint", "--host-ipv6", "192.0.2.1"},
		{"room", "endpoint", "--host-ipv6", "fe80::1"},
		{"room", "endpoint", "--host-ipv6", "[2001:db8::1]:8080"},
		{"room", "endpoint", "--host-ipv6", "2001:db8::1", "--host-ipv6", "2001:db8::2"},
		{"room", "endpoint", "--host-ipv6", "2001:db8::1", "--unexpected", "value"},
		{"room", "join", "--host-ipv6", "2001:db8::1"},
	}
	for _, args := range tests {
		if _, err := parseCommand(args); err == nil {
			t.Fatalf("parseCommand(%q) unexpectedly succeeded", args)
		}
	}
}

func TestParseArgsKeepsIPCCompatibility(t *testing.T) {
	request, err := parseArgs([]string{"disconnect", "--network", "network-1"})
	if err != nil {
		t.Fatal(err)
	}
	if request.Type != ipc.CommandDisconnect || request.NetworkID != "network-1" {
		t.Fatalf("unexpected IPC request: %#v", request)
	}
	if _, err := parseArgs([]string{"network", "create", "--name", "mesh", "--pool", "10.42.0.0/24"}); !errors.Is(err, ErrControlCommand) {
		t.Fatalf("control command parse error = %v", err)
	}
}

func TestRunControlCommandFormatsAdminResults(t *testing.T) {
	admin := &fakeAdminClient{}
	var output bytes.Buffer
	parsed, err := parseCommand([]string{"network", "create", "--name", "mesh", "--pool", "10.42.0.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	if err := runControlCommand(context.Background(), parsed, &output, admin); err != nil {
		t.Fatal(err)
	}
	if output.String() == "" || !bytes.Contains(output.Bytes(), []byte(`"ipv4_pool":"10.42.0.0/24"`)) {
		t.Fatalf("network output = %q", output.String())
	}

	output.Reset()
	parsed, err = parseCommand([]string{"invite", "create", "--network", "network-1", "--expires", "1h"})
	if err != nil {
		t.Fatal(err)
	}
	if err := runControlCommand(context.Background(), parsed, &output, admin); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(output.Bytes(), []byte(`"token":"invite-token"`)) {
		t.Fatalf("invite output = %q", output.String())
	}
}

func TestRunControlCommandPassesRequestedNetworkID(t *testing.T) {
	admin := &fakeAdminClient{}
	parsed, err := parseCommand([]string{"network", "create", "--name", "mesh", "--pool", "10.42.0.0/24", "--id", "mesh-id"})
	if err != nil {
		t.Fatal(err)
	}
	if err := runControlCommand(context.Background(), parsed, &bytes.Buffer{}, admin); err != nil {
		t.Fatal(err)
	}
	if admin.requestedNetworkID != "mesh-id" {
		t.Fatalf("requested network ID = %q, want mesh-id", admin.requestedNetworkID)
	}
}

func TestRunRoomEndpointPrintsOnlyControlURL(t *testing.T) {
	parsed, err := parseCommand([]string{"room", "endpoint", "--host-ipv6", "2001:db8::1"})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runLocalCommand(parsed, &output); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != "{\"control_url\":\"http://[2001:db8::1]:8080\"}\n" {
		t.Fatalf("output = %q", got)
	}
}

func TestRunRoomCreateCallsRoomEndpoint(t *testing.T) {
	admin := &fakeAdminClient{}
	parsed, err := parseCommand([]string{"room", "create", "--name", "IPv6Mesh-HOST", "--pool", "10.42.0.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	if err := runControlCommand(context.Background(), parsed, io.Discard, admin); err != nil {
		t.Fatal(err)
	}
	if admin.roomCreates != 1 {
		t.Fatalf("room creates = %d", admin.roomCreates)
	}
}

type fakeAdminClient struct {
	requestedNetworkID string
	roomCreates        int
}

func (fakeAdminClient) CreateNetwork(context.Context, string, string, string) (control.Network, error) {
	return control.Network{ID: "network-1", Name: "mesh", IPv4Pool: "10.42.0.0/24", CreatedAt: time.Now(), OwnerID: "owner"}, nil
}

func (client *fakeAdminClient) CreateNetworkWithID(_ context.Context, _, _, networkID, _ string) (control.Network, error) {
	client.requestedNetworkID = networkID
	return control.Network{ID: networkID, Name: "mesh", IPv4Pool: "10.42.0.0/24", CreatedAt: time.Now(), OwnerID: "owner"}, nil
}

func (fakeAdminClient) CreateInvite(context.Context, string, string, string) (control.InviteResult, error) {
	return control.InviteResult{InviteID: "invite-1", NetworkID: "network-1", Token: "invite-token", ExpiresAt: time.Now().Add(time.Hour)}, nil
}

func (client *fakeAdminClient) CreateRoom(context.Context, string, string, string) (control.Network, error) {
	client.roomCreates++
	return control.Network{ID: "room-1", Name: "IPv6Mesh-HOST", IPv4Pool: "10.42.0.0/24", CreatedAt: time.Now(), OwnerID: "owner"}, nil
}
