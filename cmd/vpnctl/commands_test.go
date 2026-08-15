package main

import (
	"bytes"
	"context"
	"errors"
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
		{name: "invite create", args: []string{"invite", "create", "--network", "network-1", "--expires", "1h"}, kind: controlCommand},
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

type fakeAdminClient struct{}

func (fakeAdminClient) CreateNetwork(context.Context, string, string, string) (control.Network, error) {
	return control.Network{ID: "network-1", Name: "mesh", IPv4Pool: "10.42.0.0/24", CreatedAt: time.Now(), OwnerID: "owner"}, nil
}

func (fakeAdminClient) CreateInvite(context.Context, string, string, string) (control.InviteResult, error) {
	return control.InviteResult{InviteID: "invite-1", NetworkID: "network-1", Token: "invite-token", ExpiresAt: time.Now().Add(time.Hour)}, nil
}
