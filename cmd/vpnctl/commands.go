package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Eser-s-Organization/ipv6mesh/internal/ipc"
	"github.com/Eser-s-Organization/ipv6mesh/internal/room"
)

var ErrControlCommand = errors.New("command targets the control plane")

type commandKind string

const (
	serviceCommand commandKind = "service"
	controlCommand commandKind = "control"
	localCommand   commandKind = "local"
)

type command struct {
	Kind         commandKind
	Service      ipc.Request
	NetworkName  string
	Pool         string
	NetworkID    string
	Expires      string
	ControlURL   string
	RoomCreate   bool
	RoomEndpoint bool
}

func parseCommand(args []string) (command, error) {
	if len(args) == 0 {
		return command{}, errors.New("usage: vpnctl status|join|leave|connect|disconnect|network create|invite create|room create|room endpoint|room join|room members")
	}
	switch args[0] {
	case "status":
		if len(args) != 1 {
			return command{}, errors.New("status takes no arguments")
		}
		return command{Kind: serviceCommand, Service: ipc.Request{Type: ipc.CommandStatus}}, nil
	case "join":
		values, err := parseOptions(args[1:], map[string]struct{}{"--invite": {}, "--name": {}})
		if err != nil {
			return command{}, err
		}
		request := ipc.Request{Type: ipc.CommandJoin, Invite: values["--invite"], DisplayName: values["--name"]}
		if _, err := ipc.MarshalRequest(request); err != nil {
			return command{}, err
		}
		return command{Kind: serviceCommand, Service: request}, nil
	case "leave", "connect", "disconnect":
		values, err := parseOptions(args[1:], map[string]struct{}{"--network": {}})
		if err != nil {
			return command{}, err
		}
		request := ipc.Request{NetworkID: values["--network"]}
		switch args[0] {
		case "leave":
			request.Type = ipc.CommandLeave
		case "connect":
			request.Type = ipc.CommandConnect
		case "disconnect":
			request.Type = ipc.CommandDisconnect
		}
		if _, err := ipc.MarshalRequest(request); err != nil {
			return command{}, err
		}
		return command{Kind: serviceCommand, Service: request}, nil
	case "network":
		if len(args) < 2 || args[1] != "create" {
			return command{}, errors.New("usage: vpnctl network create --name <name> --pool <cidr> [--id <network-id>]")
		}
		values, err := parseOptions(args[2:], map[string]struct{}{"--name": {}, "--pool": {}, "--id": {}}, "--name", "--pool")
		if err != nil {
			return command{}, err
		}
		return command{Kind: controlCommand, NetworkName: values["--name"], Pool: values["--pool"], NetworkID: values["--id"]}, nil
	case "invite":
		if len(args) < 2 || args[1] != "create" {
			return command{}, errors.New("usage: vpnctl invite create --network <id> --expires <duration>")
		}
		values, err := parseOptions(args[2:], map[string]struct{}{"--network": {}, "--expires": {}})
		if err != nil {
			return command{}, err
		}
		return command{Kind: controlCommand, NetworkID: values["--network"], Expires: values["--expires"]}, nil
	case "room":
		if len(args) < 2 {
			return command{}, errors.New("usage: vpnctl room create|endpoint|join|members")
		}
		switch args[1] {
		case "create":
			values, err := parseOptions(args[2:], map[string]struct{}{"--name": {}, "--pool": {}}, "--name", "--pool")
			if err != nil {
				return command{}, err
			}
			return command{Kind: controlCommand, NetworkName: values["--name"], Pool: values["--pool"], RoomCreate: true}, nil
		case "endpoint":
			values, err := parseOptions(args[2:], map[string]struct{}{"--host-ipv6": {}}, "--host-ipv6")
			if err != nil {
				return command{}, err
			}
			controlURL, err := room.ControlURL(values["--host-ipv6"])
			if err != nil {
				return command{}, err
			}
			return command{Kind: localCommand, ControlURL: controlURL, RoomEndpoint: true}, nil
		case "join":
			values, err := parseOptions(args[2:], map[string]struct{}{"--host-ipv6": {}, "--name": {}}, "--host-ipv6", "--name")
			if err != nil {
				return command{}, err
			}
			controlURL, err := room.ControlURL(values["--host-ipv6"])
			if err != nil {
				return command{}, err
			}
			request := ipc.Request{Type: ipc.CommandJoinRoom, ControlURL: controlURL, DisplayName: values["--name"]}
			if _, err := ipc.MarshalRequest(request); err != nil {
				return command{}, err
			}
			return command{Kind: serviceCommand, Service: request, ControlURL: controlURL}, nil
		case "members":
			if len(args) != 2 {
				return command{}, errors.New("room members takes no arguments")
			}
			return command{Kind: serviceCommand, Service: ipc.Request{Type: ipc.CommandRoomMembers}}, nil
		default:
			return command{}, errors.New("usage: vpnctl room create|endpoint|join|members")
		}
	default:
		return command{}, fmt.Errorf("unknown command %q", args[0])
	}
}

func parseArgs(args []string) (ipc.Request, error) {
	parsed, err := parseCommand(args)
	if err != nil {
		return ipc.Request{}, err
	}
	if parsed.Kind != serviceCommand {
		return ipc.Request{}, ErrControlCommand
	}
	return parsed.Service, nil
}

func parseOptions(args []string, allowed map[string]struct{}, required ...string) (map[string]string, error) {
	values := make(map[string]string, len(allowed))
	for index := 0; index < len(args); index++ {
		name := args[index]
		if !strings.HasPrefix(name, "--") {
			return nil, fmt.Errorf("unexpected argument %q", name)
		}
		if _, ok := allowed[name]; !ok {
			return nil, fmt.Errorf("unknown option %q", name)
		}
		if _, duplicate := values[name]; duplicate {
			return nil, fmt.Errorf("option %q was specified more than once", name)
		}
		if index+1 >= len(args) || strings.HasPrefix(args[index+1], "--") || strings.TrimSpace(args[index+1]) == "" {
			return nil, fmt.Errorf("option %q requires a value", name)
		}
		values[name] = args[index+1]
		index++
	}
	if len(required) == 0 {
		required = make([]string, 0, len(allowed))
		for name := range allowed {
			required = append(required, name)
		}
	}
	for _, name := range required {
		if _, ok := allowed[name]; !ok {
			return nil, fmt.Errorf("option %q is not allowed", name)
		}
		if strings.TrimSpace(values[name]) == "" {
			return nil, fmt.Errorf("option %q is required", name)
		}
	}
	return values, nil
}
