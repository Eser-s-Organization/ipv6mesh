package service

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"

	"github.com/Eser-s-Organization/ipv6mesh/internal/control"
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
	joinCalls      int
	roomJoinCalls  int
	leaveCalls     int
	joinResult     JoinResult
	roomJoinResult JoinResult
	joinErr        error
	leaveErr       error
	snapshot       control.NetworkSnapshot
	snapshotErr    error
}

func (client *fakeControlClient) Join(context.Context, JoinRequest) (JoinResult, error) {
	client.joinCalls++
	return client.joinResult, client.joinErr
}

func (client *fakeControlClient) JoinRoom(context.Context, JoinRequest) (JoinResult, error) {
	client.roomJoinCalls++
	return client.roomJoinResult, client.joinErr
}

func (client *fakeControlClient) Leave(context.Context, string) error {
	client.leaveCalls++
	return client.leaveErr
}

func (client *fakeControlClient) Snapshot(context.Context, string) (control.NetworkSnapshot, error) {
	return client.snapshot, client.snapshotErr
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

type fakeSnapshotApplier struct {
	applyCalls   int
	clearCalls   int
	lastSnapshot control.NetworkSnapshot
	applyErr     error
}

func (applier *fakeSnapshotApplier) Apply(_ context.Context, snapshot control.NetworkSnapshot) error {
	applier.applyCalls++
	applier.lastSnapshot = snapshot
	return applier.applyErr
}

func (applier *fakeSnapshotApplier) Clear(context.Context) error {
	applier.clearCalls++
	return nil
}

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

func TestServiceJoinsRoomWithoutInvite(t *testing.T) {
	controlClient := &fakeControlClient{
		roomJoinResult: JoinResult{NetworkID: "room-1", VirtualIPv4: "10.42.0.9", ConfigGeneration: 2},
	}
	service := New(Options{
		Identity:   &fakeIdentityStore{identity: identity.Identity{PublicKey: "public-key"}},
		Control:    controlClient,
		ControlURL: "http://[2001:db8::1]:8080",
		Adapter:    &fakeAdapter{},
	})
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	response := service.Handle(context.Background(), ipc.Request{
		Type:        ipc.CommandJoinRoom,
		ControlURL:  "http://[2001:db8::1]:8080",
		DisplayName: "MEMBER-PC",
	})
	if !response.OK || response.NetworkID != "room-1" || controlClient.roomJoinCalls != 1 {
		t.Fatalf("room join response=%#v calls=%d", response, controlClient.roomJoinCalls)
	}
}

func TestServiceRoomJoinMapsSafeControlRoomErrors(t *testing.T) {
	tests := []struct {
		name        string
		controlCode string
		wantCode    string
	}{
		{name: "room not ready", controlCode: "room_not_ready", wantCode: "room_not_ready"},
		{name: "room full", controlCode: "room_full", wantCode: "room_full"},
		{name: "join rate limited", controlCode: "join_rate_limited", wantCode: "join_rate_limited"},
		{name: "unknown", controlCode: "unexpected-secret-code", wantCode: CodeControlFailed},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			controlClient := &fakeControlClient{
				joinErr: &control.HTTPError{StatusCode: http.StatusConflict, Code: test.controlCode},
			}
			service := New(Options{
				Identity:   &fakeIdentityStore{identity: identity.Identity{PublicKey: "public-key"}},
				Control:    controlClient,
				ControlURL: "http://[2001:db8::1]:8080",
				Adapter:    &fakeAdapter{},
			})
			if err := service.Start(context.Background()); err != nil {
				t.Fatal(err)
			}

			response := service.Handle(context.Background(), ipc.Request{
				Type:        ipc.CommandJoinRoom,
				ControlURL:  "http://[2001:db8::1]:8080",
				DisplayName: "MEMBER-PC",
			})
			if response.OK || response.Error == nil {
				t.Fatalf("room join response = %#v, want error", response)
			}
			if response.Error.Code != test.wantCode {
				t.Fatalf("room join error code = %q, want %q", response.Error.Code, test.wantCode)
			}
			if response.Error.Message != "" {
				t.Fatalf("room join error message = %q, want empty", response.Error.Message)
			}
		})
	}
}

func TestServiceRejectsRoomURLMismatchBeforeControlCall(t *testing.T) {
	controlClient := &fakeControlClient{roomJoinResult: JoinResult{NetworkID: "room-1", VirtualIPv4: "10.42.0.9"}}
	service := New(Options{
		Identity:   &fakeIdentityStore{identity: identity.Identity{PublicKey: "public-key"}},
		Control:    controlClient,
		ControlURL: "http://[2001:db8::1]:8080",
		Adapter:    &fakeAdapter{},
	})
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	response := service.Handle(context.Background(), ipc.Request{Type: ipc.CommandJoinRoom, ControlURL: "http://[2001:db8::2]:8080", DisplayName: "MEMBER-PC"})
	if response.OK || response.Error == nil || response.Error.Code != ipc.CodeInvalidRequest || controlClient.roomJoinCalls != 0 {
		t.Fatalf("mismatch response=%#v calls=%d", response, controlClient.roomJoinCalls)
	}
}

func TestServiceRoomJoinRollsBackAfterSnapshotFailure(t *testing.T) {
	controlClient := &fakeControlClient{
		roomJoinResult: JoinResult{NetworkID: "room-1", VirtualIPv4: "10.42.0.9", ConfigGeneration: 2},
		snapshot:       control.NetworkSnapshot{NetworkID: "room-1", Generation: 2, LocalNodeID: "node-1", LocalVirtualIPv4: net.ParseIP("10.42.0.9")},
	}
	reconciler := &fakeSnapshotApplier{applyErr: errors.New("apply failed")}
	service := New(Options{
		Identity:   &fakeIdentityStore{identity: identity.Identity{PublicKey: "public-key"}},
		Control:    controlClient,
		ControlURL: "http://[2001:db8::1]:8080",
		Reconciler: reconciler,
	})
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	response := service.Handle(context.Background(), ipc.Request{Type: ipc.CommandJoinRoom, ControlURL: "http://[2001:db8::1]:8080", DisplayName: "MEMBER-PC"})
	if response.OK || response.Error == nil || response.Error.Code != CodeAdapterFailed || controlClient.leaveCalls != 1 {
		t.Fatalf("rollback response=%#v leaves=%d", response, controlClient.leaveCalls)
	}
	status := service.Handle(context.Background(), ipc.Request{Type: ipc.CommandStatus})
	if status.OK && status.NetworkID != "" {
		t.Fatalf("room state retained after rollback: %#v", status)
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

func TestShutdownClearsAppliedDataPlaneWithoutLeavingMembership(t *testing.T) {
	controlClient := &fakeControlClient{joinResult: JoinResult{NetworkID: "network-a", VirtualIPv4: "10.42.0.2", ConfigGeneration: 7}, snapshot: control.NetworkSnapshot{NetworkID: "network-a", Generation: 7, LocalNodeID: "node-1", LocalVirtualIPv4: net.ParseIP("10.42.0.2")}}
	reconciler := &fakeSnapshotApplier{}
	service := New(Options{Identity: &fakeIdentityStore{identity: identity.Identity{PublicKey: "public-key"}}, Control: controlClient, Reconciler: reconciler})
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if response := service.Handle(context.Background(), ipc.Request{Type: ipc.CommandJoin, Invite: "invite-value", DisplayName: "device-a"}); !response.OK {
		t.Fatalf("join failed: %#v", response)
	}
	if err := service.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown failed: %v", err)
	}
	if reconciler.clearCalls != 1 {
		t.Fatalf("reconciler clear calls = %d, want 1", reconciler.clearCalls)
	}
	if controlClient.leaveCalls != 0 {
		t.Fatalf("shutdown unexpectedly left control-plane membership: %d calls", controlClient.leaveCalls)
	}
	status := service.Handle(context.Background(), ipc.Request{Type: ipc.CommandStatus})
	if status.OK || status.Error == nil || status.Error.Code != ipc.CodeNotStarted {
		t.Fatalf("status after shutdown = %#v, want not_started", status)
	}
}

func TestShutdownDisconnectsConnectedAdapterWhenNoReconciler(t *testing.T) {
	service, _, adapter := newTestService()
	if err := service.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if response := service.Handle(context.Background(), ipc.Request{Type: ipc.CommandJoin, Invite: "invite-value", DisplayName: "device-a"}); !response.OK {
		t.Fatalf("join failed: %#v", response)
	}
	if response := service.Handle(context.Background(), ipc.Request{Type: ipc.CommandConnect, NetworkID: "network-a"}); !response.OK {
		t.Fatalf("connect failed: %#v", response)
	}
	if err := service.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown failed: %v", err)
	}
	if adapter.disconnectCalls != 1 {
		t.Fatalf("adapter disconnect calls = %d, want 1", adapter.disconnectCalls)
	}
}

func TestServiceAppliesInitialSnapshotDuringJoin(t *testing.T) {
	controlClient := &fakeControlClient{joinResult: JoinResult{NetworkID: "network-a", VirtualIPv4: "10.42.0.2", ConfigGeneration: 7}, snapshot: control.NetworkSnapshot{NetworkID: "network-a", Generation: 7, LocalNodeID: "node-1", LocalVirtualIPv4: net.ParseIP("10.42.0.2")}}
	reconciler := &fakeSnapshotApplier{}
	service := New(Options{Identity: &fakeIdentityStore{identity: identity.Identity{PublicKey: "public-key"}}, Control: controlClient, Reconciler: reconciler})
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	response := service.Handle(context.Background(), ipc.Request{Type: ipc.CommandJoin, Invite: "invite-value", DisplayName: "device-a"})
	if !response.OK || reconciler.applyCalls != 1 || reconciler.lastSnapshot.Generation != 7 {
		t.Fatalf("join snapshot reconciliation = response %#v, reconciler %#v", response, reconciler)
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
