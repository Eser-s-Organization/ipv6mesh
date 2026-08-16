package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/Eser-s-Organization/ipv6mesh/internal/control"
	"github.com/Eser-s-Organization/ipv6mesh/internal/ipc"
)

func main() {
	parsed, err := parseCommand(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if parsed.Kind == serviceCommand {
		err = runServiceRequest(parsed.Service, os.Stdout, ipc.NewClient(ipc.DefaultPipeName))
	} else if parsed.Kind == localCommand {
		err = runLocalCommand(parsed, os.Stdout)
	} else {
		baseURL := os.Getenv("IPV6MESH_CONTROL_URL")
		token := os.Getenv("IPV6MESH_ADMIN_TOKEN")
		client, clientErr := control.NewClient(baseURL)
		if clientErr == nil {
			client.Token = token
			err = runControlCommand(context.Background(), parsed, os.Stdout, client)
		} else {
			err = clientErr
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type caller interface {
	Call(context.Context, ipc.Request) (ipc.Response, error)
}

func run(args []string, output io.Writer, client caller) error {
	parsed, err := parseCommand(args)
	if err != nil {
		return err
	}
	if parsed.Kind != serviceCommand {
		return ErrControlCommand
	}
	return runServiceRequest(parsed.Service, output, client)
}

func runServiceRequest(request ipc.Request, output io.Writer, client caller) error {
	response, err := client.Call(context.Background(), request)
	if err != nil {
		return err
	}
	encoded, err := ipc.MarshalResponse(response)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(output, string(encoded)); err != nil {
		return err
	}
	if !response.OK {
		return errors.New(response.Error.Code)
	}
	return nil
}

func runLocalCommand(parsed command, output io.Writer) error {
	if parsed.Kind != localCommand || !parsed.RoomEndpoint || parsed.ControlURL == "" {
		return errors.New("unsupported local command")
	}
	return writeRoomEndpointOutput(output, parsed.ControlURL)
}

type controlAdminClient interface {
	CreateNetwork(context.Context, string, string, string) (control.Network, error)
	CreateNetworkWithID(context.Context, string, string, string, string) (control.Network, error)
	CreateRoom(context.Context, string, string, string) (control.Network, error)
	CreateInvite(context.Context, string, string, string) (control.InviteResult, error)
}

func runControlCommand(ctx context.Context, parsed command, output io.Writer, client controlAdminClient) error {
	if parsed.Kind != controlCommand || client == nil {
		return ErrControlCommand
	}
	if parsed.RoomCreate {
		network, err := client.CreateRoom(ctx, parsed.NetworkName, parsed.Pool, "")
		if err != nil {
			return err
		}
		return writeNetworkOutput(output, network)
	}
	switch {
	case parsed.NetworkName != "":
		var network control.Network
		var err error
		if parsed.NetworkID != "" {
			network, err = client.CreateNetworkWithID(ctx, parsed.NetworkName, parsed.Pool, parsed.NetworkID, "")
		} else {
			network, err = client.CreateNetwork(ctx, parsed.NetworkName, parsed.Pool, "")
		}
		if err != nil {
			return err
		}
		return writeNetworkOutput(output, network)
	default:
		invite, err := client.CreateInvite(ctx, parsed.NetworkID, parsed.Expires, "")
		if err != nil {
			return err
		}
		return writeInviteOutput(output, invite)
	}
}
