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
	Path    string
	Timeout time.Duration
}

func NewClient(path string) *Client {
	if path == "" {
		path = DefaultPipeName
	}
	return &Client{Path: path, Timeout: 5 * time.Second}
}

func (client *Client) Call(ctx context.Context, request Request) (Response, error) {
	if client == nil || client.Path == "" {
		return Response{}, ErrUnsupported
	}
	payload, err := MarshalRequest(request)
	if err != nil {
		return Response{}, err
	}
	connection, err := winio.DialPipeContext(ctx, client.Path)
	if err != nil {
		return Response{}, err
	}
	defer connection.Close()
	if client.Timeout > 0 {
		_ = connection.SetDeadline(time.Now().Add(client.Timeout))
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
