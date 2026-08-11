//go:build windows

package ipc

import (
	"context"
	"fmt"
	"testing"
	"time"
)

type testPipeAuthorizer struct{}

func (testPipeAuthorizer) Authorize(context.Context) error { return nil }

type testPipeHandler struct{}

func (testPipeHandler) HandleJSON(_ context.Context, data []byte) ([]byte, error) {
	request, err := DecodeRequest(data)
	if err != nil {
		return MarshalResponse(ErrorResponse(CodeInvalidRequest))
	}
	return MarshalResponse(SuccessResponse(Status{NetworkID: string(request.Type), PathState: PathStateDirect, ConfigGeneration: 1}))
}

func TestNamedPipeRoundTrip(t *testing.T) {
	path := fmt.Sprintf(`\\.\pipe\ipv6mesh-test-%d`, time.Now().UnixNano())
	server, err := NewServerWithOptions(path, testPipeHandler{}, testPipeAuthorizer{}, ServerOptions{SecurityDescriptor: "D:P(A;;GA;;;WD)"})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	serverContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(serverContext) }()

	client := NewClient(path)
	client.Timeout = 5 * time.Second
	response, err := client.Call(context.Background(), Request{Type: CommandStatus})
	if err != nil {
		t.Fatalf("pipe Call: %v", err)
	}
	if !response.OK || response.NetworkID != string(CommandStatus) {
		t.Fatalf("pipe response = %#v", response)
	}
	cancel()
	select {
	case <-serveDone:
	case <-time.After(5 * time.Second):
		t.Fatal("pipe server did not stop")
	}
}
