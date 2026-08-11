package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Eser-s-Organization/ipv6mesh/internal/identity"
	"github.com/Eser-s-Organization/ipv6mesh/internal/ipc"
)

type fakeIdentityStore struct {
	identity identity.Identity
	loads    int
}

func (store *fakeIdentityStore) LoadOrCreate() (identity.Identity, error) {
	store.loads++
	return store.identity, nil
}

type fakeControlClient struct {
	joinCalls  int
	leaveCalls int
	joinResult JoinResult
	joinErr    error
	leaveErr   error
}

func (client *fakeControlClient) Join(context.Context, JoinRequest) (JoinResult, error) {
	client.joinCalls++
	return client.joinResult, client.joinErr
}

func (client *fakeControlClient) Leave(context.Context, string) error {
	client.leaveCalls++
	return client.leaveErr
}

type fakeAdapter struct {
	connectCalls    int
	disconnectCalls int
	connectErr      error
	disconnectErr   error
}

func (adapter *fakeAdapter) Connect(context.Context, string) error {
	adapter.connectCalls++
	return adapter.connectErr
}

func (adapter *fakeAdapter) Disconnect(context.Context, string) error {
	adapter.disconnectCalls++
	return adapter.disconnectErr
}

type denyAuthorizer struct{}

func (denyAuthorizer) Authorize(context.Context) error { return errors.New("caller is not allowed") }

func newTestService() (*Service, *fakeControlClient, *fakeAdapter) {
	controlClient := &fakeControlClient{joinResult: JoinResult{
		NetworkID:        "network-a",
		VirtualIPv4:      "100.64.0.2",
		ConfigGeneration: 7,
	}}
	adapter := &fakeAdapter{}
	service := New(Options{
		Identity: &fakeIdentityStore{identity: identity.Identity{PublicKey: "public-key"}},
		Control:  controlClient,
		Adapter:  adapter,
	})
	return service, controlClient, adapter
}

func TestServiceLoadsIdentityAndRejectsDuplicateJoin(t *testing.T) {
	service, controlClient, _ := newTestService()
	if err := service.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	first := service.Handle(context.Background(), ipc.Request{Type: ipc.CommandJoin, Invite: "invite-value", DisplayName: "device-a"})
	if !first.OK {
		t.Fatalf("first join failed: %#v", first)
	}
	second := service.Handle(context.Background(), ipc.Request{Type: ipc.CommandJoin, Invite: "another-invite", DisplayName: "device-a"})
	if second.OK || second.Error == nil || second.Error.Code != CodeAlreadyJoined {
		t.Fatalf("duplicate join response = %#v", second)
	}
	if controlClient.joinCalls != 1 {
		t.Fatalf("control Join calls = %d, want 1", controlClient.joinCalls)
	}
}

func TestServiceStartIsIdempotentAndPreservesMembership(t *testing.T) {
	store := &fakeIdentityStore{identity: identity.Identity{PublicKey: "public-key"}}
	controlClient := &fakeControlClient{joinResult: JoinResult{NetworkID: "network-a", VirtualIPv4: "100.64.0.2", ConfigGeneration: 7}}
	service := New(Options{Identity: store, Control: controlClient, Adapter: &fakeAdapter{}})
	if err := service.Start(context.Background()); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if response := service.Handle(context.Background(), ipc.Request{Type: ipc.CommandJoin, Invite: "invite-value", DisplayName: "device-a"}); !response.OK {
		t.Fatalf("join failed: %#v", response)
	}
	if err := service.Start(context.Background()); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	if store.loads != 1 {
		t.Fatalf("identity loads = %d, want 1", store.loads)
	}
	status := service.Handle(context.Background(), ipc.Request{Type: ipc.CommandStatus})
	if !status.OK || status.NetworkID != "network-a" || status.VirtualIPv4 != "100.64.0.2" {
		t.Fatalf("membership after idempotent Start = %#v", status)
	}
}

func TestLeaveCleansLocalState(t *testing.T) {
	service, controlClient, adapter := newTestService()
	if err := service.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	join := service.Handle(context.Background(), ipc.Request{Type: ipc.CommandJoin, Invite: "invite-value", DisplayName: "device-a"})
	if !join.OK {
		t.Fatalf("join failed: %#v", join)
	}
	if response := service.Handle(context.Background(), ipc.Request{Type: ipc.CommandConnect, NetworkID: "network-a"}); !response.OK {
		t.Fatalf("connect failed: %#v", response)
	}

	leave := service.Handle(context.Background(), ipc.Request{Type: ipc.CommandLeave, NetworkID: "network-a"})
	if !leave.OK {
		t.Fatalf("leave failed: %#v", leave)
	}
	if controlClient.leaveCalls != 1 || adapter.disconnectCalls != 1 {
		t.Fatalf("cleanup calls = control %d, adapter %d", controlClient.leaveCalls, adapter.disconnectCalls)
	}
	status := service.Handle(context.Background(), ipc.Request{Type: ipc.CommandStatus})
	if status.NetworkID != "" || status.VirtualIPv4 != "" || status.ConfigGeneration != 0 {
		t.Fatalf("local membership survived leave: %#v", status)
	}
}

func TestHandlerRejectsMalformedJSONAndUnauthorizedRequest(t *testing.T) {
	service, _, _ := newTestService()
	if err := service.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	handler := NewHandler(service, denyAuthorizer{})

	malformed, err := handler.HandleJSON(context.Background(), []byte(`{"type":"status","type":"status"}`))
	if err != nil {
		t.Fatalf("malformed HandleJSON: %v", err)
	}
	malformedResponse, err := ipc.DecodeResponse(malformed)
	if err != nil {
		t.Fatalf("decode malformed response: %v", err)
	}
	if malformedResponse.OK || malformedResponse.Error == nil || malformedResponse.Error.Code != ipc.CodeInvalidRequest {
		t.Fatalf("malformed response = %#v", malformedResponse)
	}

	unauthorized, err := handler.HandleJSON(context.Background(), []byte(`{"type":"status"}`))
	if err != nil {
		t.Fatalf("unauthorized HandleJSON: %v", err)
	}
	unauthorizedResponse, err := ipc.DecodeResponse(unauthorized)
	if err != nil {
		t.Fatalf("decode unauthorized response: %v", err)
	}
	if unauthorizedResponse.OK || unauthorizedResponse.Error == nil || unauthorizedResponse.Error.Code != ipc.CodeUnauthorized {
		t.Fatalf("unauthorized response = %#v", unauthorizedResponse)
	}
}
