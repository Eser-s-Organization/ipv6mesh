package service

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"reflect"
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
	joinCalls        int
	roomJoinCalls    int
	leaveCalls       int
	joinResult       JoinResult
	roomJoinResult   JoinResult
	joinErr          error
	leaveErr         error
	leaveContexts    []context.Context
	leaveContextErrs []error
	snapshot         control.NetworkSnapshot
	snapshotErr      error
	snapshotCalls    int
	snapshotContexts []context.Context
}

func (client *fakeControlClient) Join(context.Context, JoinRequest) (JoinResult, error) {
	client.joinCalls++
	return client.joinResult, client.joinErr
}

func (client *fakeControlClient) JoinRoom(context.Context, JoinRequest) (JoinResult, error) {
	client.roomJoinCalls++
	return client.roomJoinResult, client.joinErr
}

func (client *fakeControlClient) Leave(ctx context.Context, _ string) error {
	client.leaveCalls++
	client.leaveContexts = append(client.leaveContexts, ctx)
	client.leaveContextErrs = append(client.leaveContextErrs, ctx.Err())
	return client.leaveErr
}

func (client *fakeControlClient) Snapshot(ctx context.Context, _ string) (control.NetworkSnapshot, error) {
	client.snapshotCalls++
	client.snapshotContexts = append(client.snapshotContexts, ctx)
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

func TestServiceRoomMembersProjectsLocalAndPeersWithoutSensitiveFields(t *testing.T) {
	controlClient := &fakeControlClient{
		roomJoinResult: JoinResult{DisplayName: "HOST-PC", NetworkID: "room-1", VirtualIPv4: "10.42.0.2", ConfigGeneration: 3},
		snapshot: control.NetworkSnapshot{
			NetworkID: "room-1", Generation: 3, LocalNodeID: "host-id", LocalVirtualIPv4: net.ParseIP("10.42.0.2"),
			Peers: []control.Peer{
				{NodeID: "peer-z", DisplayName: "alice", PublicKey: "must-not-leak", VirtualIPv4: net.ParseIP("10.42.0.4"), Endpoints: []control.EndpointCandidate{{Address: net.ParseIP("2001:db8::4"), Port: 51820}}},
				{NodeID: "peer-a", DisplayName: "Alice", PublicKey: "must-not-leak", VirtualIPv4: net.ParseIP("10.42.0.3")},
			},
		},
	}
	service := New(Options{Identity: &fakeIdentityStore{identity: identity.Identity{PublicKey: "public-key"}}, Control: controlClient, ControlURL: "http://[2001:db8::1]:8080"})
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	joined := service.Handle(context.Background(), ipc.Request{Type: ipc.CommandJoinRoom, ControlURL: "http://[2001:db8::1]:8080", DisplayName: "HOST-PC"})
	if !joined.OK {
		t.Fatalf("join = %#v", joined)
	}

	response := service.Handle(context.Background(), ipc.Request{Type: ipc.CommandRoomMembers})
	if !response.OK || response.NetworkID != "room-1" || len(response.Members) != 3 {
		t.Fatalf("members = %#v", response)
	}
	want := []ipc.RoomMember{
		{DisplayName: "HOST-PC", VirtualIPv4: "10.42.0.2", IsLocal: true, State: ipc.RoomMemberOnline},
		{DisplayName: "Alice", VirtualIPv4: "10.42.0.3", State: ipc.RoomMemberOnline},
		{DisplayName: "alice", VirtualIPv4: "10.42.0.4", State: ipc.RoomMemberOnline},
	}
	if !reflect.DeepEqual(response.Members, want) {
		t.Fatalf("members = %#v, want %#v", response.Members, want)
	}
	if controlClient.snapshotCalls != 1 {
		t.Fatalf("snapshot calls = %d, want 1", controlClient.snapshotCalls)
	}
	encoded, err := ipc.MarshalResponse(response)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"peer-z", "must-not-leak", "2001:db8::4", "session"} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("leaked %q: %s", forbidden, encoded)
		}
	}
}

type timeoutNetError struct{}

func (timeoutNetError) Error() string   { return "timeout" }
func (timeoutNetError) Timeout() bool   { return true }
func (timeoutNetError) Temporary() bool { return true }

func joinedRoomServiceForMembers(t *testing.T, snapshotErr error) *Service {
	t.Helper()
	controlClient := &fakeControlClient{
		roomJoinResult: JoinResult{DisplayName: "MEMBER-PC", NetworkID: "room-1", VirtualIPv4: "10.42.0.2", ConfigGeneration: 2},
		snapshot: control.NetworkSnapshot{
			NetworkID: "room-1", Generation: 2, LocalNodeID: "local", LocalVirtualIPv4: net.ParseIP("10.42.0.2"),
		},
		snapshotErr: snapshotErr,
	}
	service := New(Options{
		Identity:   &fakeIdentityStore{identity: identity.Identity{PublicKey: "public-key"}},
		Control:    controlClient,
		ControlURL: "http://[2001:db8::1]:8080",
	})
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	response := service.Handle(context.Background(), ipc.Request{Type: ipc.CommandJoinRoom, ControlURL: "http://[2001:db8::1]:8080", DisplayName: "MEMBER-PC"})
	if !response.OK {
		t.Fatalf("join = %#v", response)
	}
	return service
}

func TestServiceRoomMembersMapsSafeErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "deadline", err: context.DeadlineExceeded, want: ipc.CodeOperationTimeout},
		{name: "url timeout", err: &url.Error{Op: "Get", URL: "http://[2001:db8::1]:8080", Err: timeoutNetError{}}, want: ipc.CodeOperationTimeout},
		{name: "unreachable", err: &url.Error{Op: "Get", URL: "http://[2001:db8::1]:8080", Err: errors.New("no route")}, want: ipc.CodeControlUnreachable},
		{name: "http", err: &control.HTTPError{StatusCode: http.StatusBadGateway}, want: CodeControlFailed},
		{name: "unknown", err: errors.New("secret detail"), want: CodeControlFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := joinedRoomServiceForMembers(t, test.err)
			response := service.Handle(context.Background(), ipc.Request{Type: ipc.CommandRoomMembers})
			if response.OK || response.Error == nil || response.Error.Code != test.want || response.Error.Message != "" {
				t.Fatalf("response = %#v", response)
			}
		})
	}
}

func TestServiceRoomMembersRejectsInvalidSnapshotAndClearsAfterLeave(t *testing.T) {
	controlClient := &fakeControlClient{
		roomJoinResult: JoinResult{DisplayName: "MEMBER-PC", NetworkID: "room-1", VirtualIPv4: "10.42.0.2", ConfigGeneration: 2},
		snapshot:       control.NetworkSnapshot{NetworkID: "other-room", Generation: 2, LocalNodeID: "local", LocalVirtualIPv4: net.ParseIP("10.42.0.2")},
	}
	service := New(Options{Identity: &fakeIdentityStore{identity: identity.Identity{PublicKey: "public-key"}}, Control: controlClient, ControlURL: "http://[2001:db8::1]:8080"})
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	notJoined := service.Handle(context.Background(), ipc.Request{Type: ipc.CommandRoomMembers})
	if notJoined.OK || notJoined.Error == nil || notJoined.Error.Code != CodeNotJoined {
		t.Fatalf("not joined response = %#v", notJoined)
	}
	joined := service.Handle(context.Background(), ipc.Request{Type: ipc.CommandJoinRoom, ControlURL: "http://[2001:db8::1]:8080", DisplayName: "MEMBER-PC"})
	if !joined.OK {
		t.Fatalf("join = %#v", joined)
	}
	invalid := service.Handle(context.Background(), ipc.Request{Type: ipc.CommandRoomMembers})
	if invalid.OK || invalid.Error == nil || invalid.Error.Code != CodeControlFailed {
		t.Fatalf("invalid snapshot response = %#v", invalid)
	}
	controlClient.snapshot = control.NetworkSnapshot{NetworkID: "room-1", Generation: 2, LocalNodeID: "local", LocalVirtualIPv4: net.ParseIP("10.42.0.2"), Peers: []control.Peer{{DisplayName: "", VirtualIPv4: net.ParseIP("10.42.0.3")}}}
	emptyName := service.Handle(context.Background(), ipc.Request{Type: ipc.CommandRoomMembers})
	if emptyName.OK || emptyName.Error == nil || emptyName.Error.Code != CodeControlFailed {
		t.Fatalf("empty peer response = %#v", emptyName)
	}
	controlClient.snapshot = control.NetworkSnapshot{NetworkID: "room-1", Generation: 2, LocalNodeID: "local", LocalVirtualIPv4: net.ParseIP("10.42.0.2"), Peers: []control.Peer{{DisplayName: "peer", VirtualIPv4: net.ParseIP("2001:db8::3")}}}
	invalidIPv4 := service.Handle(context.Background(), ipc.Request{Type: ipc.CommandRoomMembers})
	if invalidIPv4.OK || invalidIPv4.Error == nil || invalidIPv4.Error.Code != CodeControlFailed {
		t.Fatalf("invalid peer IPv4 response = %#v", invalidIPv4)
	}
	controlClient.snapshot = control.NetworkSnapshot{NetworkID: "room-1", Generation: 2, LocalNodeID: "local", LocalVirtualIPv4: net.ParseIP("10.42.0.2")}
	before := service.Handle(context.Background(), ipc.Request{Type: ipc.CommandStatus})
	members := service.Handle(context.Background(), ipc.Request{Type: ipc.CommandRoomMembers})
	if !members.OK || before.NetworkID != members.NetworkID || before.VirtualIPv4 != members.VirtualIPv4 || before.PathState != members.PathState {
		t.Fatalf("members changed status: before=%#v members=%#v", before, members)
	}
	left := service.Handle(context.Background(), ipc.Request{Type: ipc.CommandLeave, NetworkID: "room-1"})
	if !left.OK {
		t.Fatalf("leave = %#v", left)
	}
	notJoined = service.Handle(context.Background(), ipc.Request{Type: ipc.CommandRoomMembers})
	if notJoined.OK || notJoined.Error == nil || notJoined.Error.Code != CodeNotJoined {
		t.Fatalf("after leave response = %#v", notJoined)
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

func TestServiceFinishJoinSnapshotErrorsUseSafeCodesAndFreshRollbackContext(t *testing.T) {
	tests := []struct {
		name        string
		snapshotErr error
		wantCode    string
	}{
		{name: "timeout", snapshotErr: context.DeadlineExceeded, wantCode: ipc.CodeOperationTimeout},
		{name: "unreachable", snapshotErr: &url.Error{Op: "GET", URL: "http://control.invalid/", Err: errors.New("connection refused")}, wantCode: ipc.CodeControlUnreachable},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			controlClient := &fakeControlClient{snapshotErr: test.snapshotErr, leaveErr: errors.New("rollback secret should stay hidden")}
			reconciler := &fakeSnapshotApplier{}
			service := New(Options{Control: controlClient, Reconciler: reconciler})
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			response := service.finishJoin(ctx, JoinResult{NetworkID: "room-1", VirtualIPv4: "10.42.0.9", DisplayName: "MEMBER-PC"})
			if response.OK || response.Error == nil || response.Error.Code != test.wantCode {
				t.Fatalf("finishJoin response = %#v, want safe code %q", response, test.wantCode)
			}
			if response.Error.Message != "" {
				t.Fatalf("finishJoin error message = %q, want empty", response.Error.Message)
			}
			if len(controlClient.leaveContextErrs) != 1 || controlClient.leaveContextErrs[0] != nil {
				t.Fatalf("rollback context = %#v, want a live bounded context", controlClient.leaveContexts)
			}
		})
	}
}

func TestServiceFinishJoinApplyFailureKeepsAdapterCodeAndFreshRollbackContext(t *testing.T) {
	controlClient := &fakeControlClient{snapshot: control.NetworkSnapshot{NetworkID: "room-1", LocalVirtualIPv4: net.ParseIP("10.42.0.9")}, leaveErr: errors.New("rollback secret should stay hidden")}
	reconciler := &fakeSnapshotApplier{applyErr: errors.New("adapter apply failed")}
	service := New(Options{Control: controlClient, Reconciler: reconciler})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	response := service.finishJoin(ctx, JoinResult{NetworkID: "room-1", VirtualIPv4: "10.42.0.9", DisplayName: "MEMBER-PC"})
	if response.OK || response.Error == nil || response.Error.Code != CodeAdapterFailed {
		t.Fatalf("finishJoin apply response = %#v, want adapter_failed", response)
	}
	if response.Error.Message != "" {
		t.Fatalf("finishJoin apply error message = %q, want empty", response.Error.Message)
	}
	if len(controlClient.leaveContextErrs) != 1 || controlClient.leaveContextErrs[0] != nil {
		t.Fatalf("apply rollback context = %#v, want a live bounded context", controlClient.leaveContexts)
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
