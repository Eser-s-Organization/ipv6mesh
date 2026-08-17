//go:build !windows

package ipc

import (
	"context"
	"errors"
	"time"
)

var ErrUnsupported = errors.New("named-pipe IPC is only available on Windows")

type Client struct {
	Path           string
	Timeout        time.Duration
	NetworkTimeout time.Duration
}

func NewClient(path string) *Client {
	return &Client{Path: path, Timeout: 5 * time.Second, NetworkTimeout: 45 * time.Second}
}

func (client *Client) Call(context.Context, Request) (Response, error) {
	return Response{}, ErrUnsupported
}
