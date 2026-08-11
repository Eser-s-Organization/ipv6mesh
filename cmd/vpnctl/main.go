package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/Eser-s-Organization/ipv6mesh/internal/ipc"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, ipc.NewClient(ipc.DefaultPipeName)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type caller interface {
	Call(context.Context, ipc.Request) (ipc.Response, error)
}

func run(args []string, output io.Writer, client caller) error {
	request, err := parseArgs(args)
	if err != nil {
		return err
	}
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

func parseArgs(args []string) (ipc.Request, error) {
	if len(args) == 0 {
		return ipc.Request{}, errors.New("usage: vpnctl status|join|leave|connect|disconnect")
	}
	request := ipc.Request{}
	switch args[0] {
	case "status":
		request.Type = ipc.CommandStatus
	case "join":
		request.Type = ipc.CommandJoin
		request.Invite = flagValue(args[1:], "--invite")
		request.DisplayName = flagValue(args[1:], "--name")
	case "leave":
		request.Type = ipc.CommandLeave
		request.NetworkID = flagValue(args[1:], "--network")
	case "connect":
		request.Type = ipc.CommandConnect
		request.NetworkID = flagValue(args[1:], "--network")
	case "disconnect":
		request.Type = ipc.CommandDisconnect
		request.NetworkID = flagValue(args[1:], "--network")
	default:
		return ipc.Request{}, fmt.Errorf("unknown command %q", args[0])
	}
	if len(args) > 1 && args[0] == "status" {
		return ipc.Request{}, errors.New("status takes no arguments")
	}
	if _, err := ipc.MarshalRequest(request); err != nil {
		return ipc.Request{}, err
	}
	return request, nil
}

func flagValue(args []string, flag string) string {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == flag {
			return args[index+1]
		}
	}
	return ""
}
