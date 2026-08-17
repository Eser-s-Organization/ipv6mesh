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

type slowNetworkTestPipeHandler struct{}

func (slowNetworkTestPipeHandler) HandleJSON(_ context.Context, data []byte) ([]byte, error) {
	request, err := DecodeRequest(data)
	if err != nil {
		return MarshalResponse(ErrorResponse(CodeInvalidRequest))
	}
	if request.Type == CommandConnect || request.Type == CommandRoomMembers {
		time.Sleep(5200 * time.Millisecond)
	}
	return MarshalResponse(SuccessResponse(Status{NetworkID: string(request.Type), PathState: PathStateDirect, ConfigGeneration: 1}))
}

func TestNamedPipeRoundTrip(t *testing.T) {
	path := fmt.Sprintf(`\\.\pipe\ipv6mesh-test-%d`, time.Now().UnixNano())
	// The permissive descriptor is test-only; production uses NewServer's
	// SYSTEM/Administrators-only default.
	server, err := newServerWithOptions(path, testPipeHandler{}, testPipeAuthorizer{}, serverOptions{SecurityDescriptor: "D:P(A;;GA;;;WD)"})
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

func TestNamedPipeSlowNetworkCommandUsesExtendedDeadline(t *testing.T) {
	path := fmt.Sprintf(`\\.\pipe\ipv6mesh-slow-%d`, time.Now().UnixNano())
	server, err := newServerWithOptions(path, slowNetworkTestPipeHandler{}, testPipeAuthorizer{}, serverOptions{SecurityDescriptor: "D:P(A;;GA;;;WD)"})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	serverContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = server.Serve(serverContext) }()

	callContext, callCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer callCancel()
	response, err := NewClient(path).Call(callContext, Request{Type: CommandRoomMembers})
	if err != nil {
		t.Fatalf("slow room-members Call: %v", err)
	}
	if !response.OK || response.NetworkID != string(CommandRoomMembers) {
		t.Fatalf("slow room-members response = %#v", response)
	}
}

func TestNamedPipeSlowConnectCommandUsesExtendedDeadline(t *testing.T) {
	path := fmt.Sprintf(`\\.\pipe\ipv6mesh-slow-connect-%d`, time.Now().UnixNano())
	server, err := newServerWithOptions(path, slowNetworkTestPipeHandler{}, testPipeAuthorizer{}, serverOptions{SecurityDescriptor: "D:P(A;;GA;;;WD)"})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	serverContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = server.Serve(serverContext) }()

	callContext, callCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer callCancel()
	response, err := NewClient(path).Call(callContext, Request{Type: CommandConnect, NetworkID: "network-a"})
	if err != nil {
		t.Fatalf("slow connect Call: %v", err)
	}
	if !response.OK || response.NetworkID != string(CommandConnect) {
		t.Fatalf("slow connect response = %#v", response)
	}
}

func TestNamedPipeCallHonorsEarlierCallerDeadlineAcrossRead(t *testing.T) {
	path := fmt.Sprintf(`\\.\pipe\ipv6mesh-caller-deadline-%d`, time.Now().UnixNano())
	server, err := newServerWithOptions(path, slowNetworkTestPipeHandler{}, testPipeAuthorizer{}, serverOptions{SecurityDescriptor: "D:P(A;;GA;;;WD)"})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	serverContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = server.Serve(serverContext) }()

	callContext, callCancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer callCancel()
	started := time.Now()
	_, err = NewClient(path).Call(callContext, Request{Type: CommandRoomMembers})
	if err == nil {
		t.Fatal("slow room-members Call unexpectedly succeeded after caller deadline")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("caller deadline was not applied to the read phase: elapsed=%s err=%v", elapsed, err)
	}
}

func TestNamedPipeConnectHonorsEarlierCallerDeadlineAcrossRead(t *testing.T) {
	path := fmt.Sprintf(`\\.\pipe\ipv6mesh-connect-caller-deadline-%d`, time.Now().UnixNano())
	server, err := newServerWithOptions(path, slowNetworkTestPipeHandler{}, testPipeAuthorizer{}, serverOptions{SecurityDescriptor: "D:P(A;;GA;;;WD)"})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	serverContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = server.Serve(serverContext) }()

	callContext, callCancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer callCancel()
	started := time.Now()
	_, err = NewClient(path).Call(callContext, Request{Type: CommandConnect, NetworkID: "network-a"})
	if err == nil {
		t.Fatal("slow connect Call unexpectedly succeeded after caller deadline")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("caller deadline was not applied to connect read phase: elapsed=%s err=%v", elapsed, err)
	}
}

func TestDefaultServerConnectionTimeoutCoversNetworkCommandBudget(t *testing.T) {
	path := fmt.Sprintf(`\\.\pipe\ipv6mesh-budget-%d`, time.Now().UnixNano())
	server, err := NewServer(path, testPipeHandler{}, testPipeAuthorizer{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	if server.connectionTimeout != 60*time.Second {
		t.Fatalf("connection timeout = %s, want 60s", server.connectionTimeout)
	}
}
