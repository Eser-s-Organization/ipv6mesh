//go:build windows

package ipc

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/Microsoft/go-winio"
)

var ErrUnsupported = errors.New("named-pipe IPC is unavailable")

type Client struct {
	Path           string
	Timeout        time.Duration
	NetworkTimeout time.Duration
}

func NewClient(path string) *Client {
	if path == "" {
		path = DefaultPipeName
	}
	return &Client{Path: path, Timeout: 5 * time.Second, NetworkTimeout: 45 * time.Second}
}

func (client *Client) timeoutFor(command Command) time.Duration {
	if commandTimeoutClass(command) == networkCommandTimeout && client.NetworkTimeout > 0 {
		return client.NetworkTimeout
	}
	return client.Timeout
}

func (client *Client) callDeadline(ctx context.Context, command Command, now time.Time) (time.Time, bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	deadline, hasDeadline := ctx.Deadline()
	if timeout := client.timeoutFor(command); timeout > 0 {
		budgetDeadline := now.Add(timeout)
		if !hasDeadline || budgetDeadline.Before(deadline) {
			deadline = budgetDeadline
			hasDeadline = true
		}
	}
	return deadline, hasDeadline
}

func (client *Client) Call(ctx context.Context, request Request) (Response, error) {
	if client == nil || client.Path == "" {
		return Response{}, ErrUnsupported
	}
	payload, err := MarshalRequest(request)
	if err != nil {
		return Response{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	deadline, hasDeadline := client.callDeadline(ctx, request.Type, time.Now())
	callContext := ctx
	var cancel context.CancelFunc
	if hasDeadline {
		callContext, cancel = context.WithDeadline(ctx, deadline)
		defer cancel()
	}
	connection, err := winio.DialPipeContext(callContext, client.Path)
	if err != nil {
		return Response{}, err
	}
	defer connection.Close()
	if hasDeadline {
		_ = connection.SetDeadline(deadline)
	}
	if _, err := connection.Write(payload); err != nil {
		return Response{}, err
	}
	if closeWriter, ok := connection.(interface{ CloseWrite() error }); ok {
		if err := closeWriter.CloseWrite(); err != nil {
			return Response{}, err
		}
	}
	responseBytes, err := io.ReadAll(io.LimitReader(connection, MaxMessageSize+1))
	if err != nil {
		return Response{}, err
	}
	if len(responseBytes) > MaxMessageSize {
		return Response{}, ErrMessageTooLarge
	}
	return DecodeResponse(responseBytes)
}
