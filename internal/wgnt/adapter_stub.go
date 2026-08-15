//go:build !windows

package wgnt

import "context"

// Client is the non-Windows placeholder. The Windows service is the only v0.1
// client implementation; keeping this type available lets common packages
// and tests compile on Linux.
type Client struct{}

func New() *Client                            { return &Client{} }
func NewWithDLL(string) *Client               { return &Client{} }
func (*Client) Ensure(string) (Handle, error) { return 0, ErrUnsupportedPlatform }
func (*Client) Configure(context.Context, Handle, Configuration) error {
	return ErrUnsupportedPlatform
}
func (*Client) SetUp(context.Context, Handle) error   { return ErrUnsupportedPlatform }
func (*Client) SetDown(context.Context, Handle) error { return ErrUnsupportedPlatform }
func (*Client) Delete(context.Context, Handle) error  { return ErrUnsupportedPlatform }
func (*Client) Status(context.Context, Handle) (Status, error) {
	return Status{}, ErrUnsupportedPlatform
}
