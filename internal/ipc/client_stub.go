//go:build !windows

package ipc

import (
	"context"
	"errors"
)

var ErrUnsupported = errors.New("named-pipe IPC is only available on Windows")

type Client struct{ Path string }

func NewClient(path string) *Client { return &Client{Path: path} }

func (client *Client) Call(context.Context, Request) (Response, error) {
	return Response{}, ErrUnsupported
}
