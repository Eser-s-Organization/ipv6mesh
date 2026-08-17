# Room Reliability, Member List, and Mode Lock Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix masked room-join timeouts, expose a credential-safe live member list, and enforce exclusive host/member UI modes without breaking the responsive diagnostics layout.

**Architecture:** Extend the strict local IPC protocol with a sanitized `room_members` response, keep authenticated snapshot access inside the privileged service, and give network-backed pipe commands a deadline longer than their HTTP work. The Windows UI gains an explicit flow state machine, a process-wide mutex, host health preflight, and a responsive member grid that sits beside settings on wide windows and below settings on narrow windows.

**Tech Stack:** Go 1.22+, strict JSON IPC, `go-winio` Windows named pipes, Go HTTP control client, PowerShell 5.1 WinForms, Windows services, Go tests, PowerShell AST/layout audits, GitHub Actions and GitHub CLI.

---

## Execution Rules

- Start from the approved design commit `0cb314f` on current `main`.
- Create an isolated worktree and a branch named `codex/room-members-reliability`.
- Follow RED -> GREEN -> REFACTOR for every production change. Record the focused failing command and the expected failure before implementation.
- Commit after every task using the exact commit subject listed in that task.
- Do not merge, push, tag, create a GitHub Release, or modify GitHub state. The primary reviewer owns final integration and release.
- Do not commit `.superpowers/`, `packaging/windows/dist/`, `payload.zip`, `payload_embed_windows.go`, installer binaries, DMGs, or checksum assets.
- Preserve all unrelated working-tree changes. Stop and report if the isolated worktree is not clean before a task begins.

## File Map

**Protocol and IPC**

- Modify `internal/ipc/protocol.go`: strict `room_members` request and sanitized members response.
- Modify `internal/ipc/protocol_test.go`: protocol RED/GREEN tests and secrecy assertions.
- Modify `internal/ipc/client_windows.go`: command-specific deadlines.
- Modify `internal/ipc/client_stub.go`: keep the cross-platform type API compile-compatible.
- Modify `internal/ipc/server_windows.go`: 60-second connection context.
- Modify `internal/ipc/pipe_windows_test.go`: slow network command and deadline behavior.

**Service and CLI**

- Modify `internal/service/service.go`: store display name, project member snapshots, and map safe transport errors.
- Modify `internal/service/service_test.go`: member projection, ordering, invalid data, and safe error tests.
- Modify `internal/service/control_client.go`: preserve joined display name without exposing the token.
- Modify `internal/service/control_client_test.go`: bridge display-name and snapshot-auth tests.
- Modify `cmd/vpnctl/commands.go`: parse `vpnctl room members`.
- Modify `cmd/vpnctl/commands_test.go`: parser and request tests.
- Modify `cmd/vpnctl/main.go` only if the existing service-command path needs no-argument command handling adjustments.

**Windows installation and UI**

- Modify `packaging/windows/install.ps1`: application-level service readiness wait.
- Modify `packaging/windows/ui.ps1`: host preflight, mutex, state machine, member polling, responsive A+C layout, explicit end/leave.
- Modify `cmd/ipv6mesh-installer/main_windows_test.go`: installer ordering, state helpers, mutex, member refresh, and layout audit tests.

**Integration and documentation**

- Modify `internal/control/room_integration_test.go`: two-member visibility and leave behavior.
- Modify `README.md`: user flow, errors, member list, and mode lock.
- Modify `packaging/windows/README.md`: Windows-specific readiness and UI behavior.
- Modify `docs/operator.md` only where stable diagnostic codes are enumerated.

### Task 1: Add the strict sanitized room-members IPC protocol

**Files:**

- Modify: `internal/ipc/protocol.go`
- Test: `internal/ipc/protocol_test.go`

- [ ] **Step 1: Write the failing request round-trip and rejection tests**

Add `CommandRoomMembers` cases to `TestDecodeRequestAcceptsSupportedCommands`, then add:

```go
func TestRoomMembersRequestRoundTripAndRejectsArguments(t *testing.T) {
	request := Request{Type: CommandRoomMembers}
	encoded, err := MarshalRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"type":"room_members"}` {
		t.Fatalf("encoded = %s", encoded)
	}
	decoded, err := DecodeRequest(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != request {
		t.Fatalf("decoded = %#v, want %#v", decoded, request)
	}
	for _, value := range []string{
		`{"type":"room_members","network_id":"room-1"}`,
		`{"type":"room_members","session_token":"secret"}`,
		`{"type":"room_members","display_name":"MEMBER-PC"}`,
	} {
		if _, err := DecodeRequest([]byte(value)); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("DecodeRequest(%s) error = %v", value, err)
		}
	}
}
```

- [ ] **Step 2: Write the failing response round-trip, strictness, and secrecy tests**

Add:

```go
func TestRoomMembersResponseRoundTripIsSanitized(t *testing.T) {
	response := SuccessMembersResponse("room-1", []RoomMember{
		{DisplayName: "HOST-PC", VirtualIPv4: "10.42.0.2", IsLocal: true, State: RoomMemberOnline},
		{DisplayName: "MEMBER-PC", VirtualIPv4: "10.42.0.3", State: RoomMemberOnline},
	})
	encoded, err := MarshalResponse(response)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"session_token", "public_key", "private_key", "node_id", "endpoint", "last_seen"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("response contains %q: %s", forbidden, encoded)
		}
	}
	decoded, err := DecodeResponse(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !decoded.OK || decoded.NetworkID != "room-1" || len(decoded.Members) != 2 {
		t.Fatalf("decoded = %#v", decoded)
	}
	if decoded.Members[0].DisplayName != "HOST-PC" || !decoded.Members[0].IsLocal || decoded.Members[0].State != RoomMemberOnline {
		t.Fatalf("local member = %#v", decoded.Members[0])
	}
}

func TestRoomMembersResponseRejectsUnknownNullAndInvalidFields(t *testing.T) {
	values := []string{
		`{"ok":true,"path_state":"disconnected","config_generation":0,"network_id":"room-1","members":null}`,
		`{"ok":true,"path_state":"disconnected","config_generation":0,"network_id":"room-1","members":[{"display_name":"A","virtual_ipv4":"10.42.0.2","is_local":true,"state":"online","token":"secret"}]}`,
		`{"ok":true,"path_state":"disconnected","config_generation":0,"network_id":"room-1","members":[{"display_name":"","virtual_ipv4":"10.42.0.2","is_local":true,"state":"online"}]}`,
		`{"ok":true,"path_state":"disconnected","config_generation":0,"network_id":"room-1","members":[{"display_name":"A","virtual_ipv4":"2001:db8::1","is_local":true,"state":"online"}]}`,
		`{"ok":true,"path_state":"disconnected","config_generation":0,"network_id":"room-1","members":[{"display_name":"A","virtual_ipv4":"10.42.0.2","is_local":true,"state":"offline"}]}`,
	}
	for _, value := range values {
		if _, err := DecodeResponse([]byte(value)); !errors.Is(err, ErrInvalidResponse) {
			t.Fatalf("DecodeResponse(%s) error = %v", value, err)
		}
	}
}
```

- [ ] **Step 3: Run the focused tests and verify RED**

Run:

```powershell
go test ./internal/ipc -run 'TestRoomMembers(Request|Response)' -count=1 -v
```

Expected: compile failure because `CommandRoomMembers`, `RoomMember`, `RoomMemberOnline`, and `SuccessMembersResponse` do not exist.

- [ ] **Step 4: Implement the minimal protocol model and strict codecs**

Add to `protocol.go`:

```go
const CommandRoomMembers Command = "room_members"

type RoomMemberState string

const RoomMemberOnline RoomMemberState = "online"

type RoomMember struct {
	DisplayName string          `json:"display_name"`
	VirtualIPv4 string          `json:"virtual_ipv4"`
	IsLocal     bool            `json:"is_local"`
	State       RoomMemberState `json:"state"`
}
```

Extend `Response`:

```go
type Response struct {
	OK bool `json:"ok"`
	Status
	Members []RoomMember `json:"members,omitempty"`
	Error   *Error       `json:"error,omitempty"`
}

func SuccessMembersResponse(networkID string, members []RoomMember) Response {
	copyMembers := append([]RoomMember(nil), members...)
	return Response{
		OK:      true,
		Status:  Status{NetworkID: networkID, PathState: PathStateDisconnected},
		Members: copyMembers,
	}
}
```

In `MarshalRequest`, `DecodeRequest`, and `validateRequest`, treat `CommandRoomMembers` like `CommandStatus`: no fields beyond `type`.

In `MarshalResponse`, validate each member with one helper before assigning `fields["members"]`. The helper must require a trimmed nonempty display name, `net.ParseIP(value.VirtualIPv4).To4() != nil`, and `State == RoomMemberOnline`. A successful members response must have a nonempty `NetworkID`; ordinary status responses keep `members` absent.

In `DecodeResponse`, add `members` to the allowlist, require a non-null JSON array, decode it with `DisallowUnknownFields`, reject trailing data, validate every member, and reject members on an error response.

Add `net` to imports. Do not add token, key, endpoint, node ID, platform, version, or last-seen fields.

- [ ] **Step 5: Run focused and package tests and verify GREEN**

Run:

```powershell
go test ./internal/ipc -run 'TestRoomMembers(Request|Response)|TestResponseNeverContains' -count=1 -v
go test ./internal/ipc -count=1
```

Expected: all tests pass.

- [ ] **Step 6: Commit Task 1**

```powershell
git add internal/ipc/protocol.go internal/ipc/protocol_test.go
git commit -m "feat: add sanitized room members IPC"
```

### Task 2: Project authenticated snapshots into safe member rows

**Files:**

- Modify: `internal/service/service.go`
- Modify: `internal/service/control_client.go`
- Test: `internal/service/service_test.go`
- Test: `internal/service/control_client_test.go`

- [ ] **Step 1: Extend the fake control client and write the failing projection test**

Add `snapshotCalls int` to `fakeControlClient`, increment it in `Snapshot`, then add:

```go
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
	if err := service.Start(context.Background()); err != nil { t.Fatal(err) }
	joined := service.Handle(context.Background(), ipc.Request{Type: ipc.CommandJoinRoom, ControlURL: "http://[2001:db8::1]:8080", DisplayName: "HOST-PC"})
	if !joined.OK { t.Fatalf("join = %#v", joined) }

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
	encoded, err := ipc.MarshalResponse(response)
	if err != nil { t.Fatal(err) }
	for _, forbidden := range []string{"peer-z", "must-not-leak", "2001:db8::4", "session"} {
		if bytes.Contains(encoded, []byte(forbidden)) { t.Fatalf("leaked %q: %s", forbidden, encoded) }
	}
}
```

Import `bytes` and `reflect`.

- [ ] **Step 2: Write failing error and lifecycle tests**

Add table tests that construct a joined service and assert:

```go
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
		Identity: &fakeIdentityStore{identity: identity.Identity{PublicKey: "public-key"}},
		Control: controlClient, ControlURL: "http://[2001:db8::1]:8080",
	})
	if err := service.Start(context.Background()); err != nil { t.Fatal(err) }
	response := service.Handle(context.Background(), ipc.Request{Type: ipc.CommandJoinRoom, ControlURL: "http://[2001:db8::1]:8080", DisplayName: "MEMBER-PC"})
	if !response.OK { t.Fatalf("join = %#v", response) }
	return service
}

func TestServiceRoomMembersMapsSafeErrors(t *testing.T) {
	tests := []struct { name string; err error; want string }{
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
```

Also cover: not joined -> `not_joined`; snapshot network mismatch -> `control_failed`; empty peer name -> `control_failed`; non-IPv4 peer -> `control_failed`; after successful leave -> `not_joined`; a members call does not change `PathState`, `NetworkID`, or `VirtualIPv4`.

Define the test-only timeout type exactly:

```go
type timeoutNetError struct{}
func (timeoutNetError) Error() string   { return "timeout" }
func (timeoutNetError) Timeout() bool   { return true }
func (timeoutNetError) Temporary() bool { return true }
```

- [ ] **Step 3: Run focused tests and verify RED**

```powershell
go test ./internal/service -run 'TestServiceRoomMembers' -count=1 -v
```

Expected: compile failure because `JoinResult.DisplayName`, the room-members command handler, and the stable IPC codes do not exist.

- [ ] **Step 4: Preserve display name in the control bridge**

Extend `JoinResult`:

```go
type JoinResult struct {
	DisplayName      string
	NetworkID        string
	VirtualIPv4      string
	ConfigGeneration int64
}
```

In `rememberEnrollment`, build it from the authenticated result:

```go
joined := JoinResult{
	DisplayName:      result.Node.DisplayName,
	NetworkID:        result.Network.ID,
	VirtualIPv4:      result.Membership.VirtualIPv4.String(),
	ConfigGeneration: result.Network.ConfigVersion,
}
```

Update `control_client_test.go` to assert the returned display name for both legacy enrollment and room join. Keep `sessionToken` inside `HTTPControlClient`.

- [ ] **Step 5: Implement safe error mapping and member projection**

Add IPC codes in `protocol.go`:

```go
CodeControlUnreachable = "control_unreachable"
CodeOperationTimeout   = "operation_timeout"
```

In `service.go`, add:

```go
func safeControlErrorCode(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return ipc.CodeOperationTimeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return ipc.CodeOperationTimeout
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return ipc.CodeControlUnreachable
	}
	return CodeControlFailed
}
```

Call this helper from `safeRoomControlErrorCode` only after the known `control.HTTPError` allowlist has been checked. Unknown HTTP errors stay `control_failed`; transport failures become safe stable codes.

Use the same safe helper in legacy `join` and `leave`: an HTTP application error remains `control_failed`, while a deadline or `*url.Error` becomes the two new stable codes. In both `join` and `joinRoom`, set the stored `JoinResult.DisplayName` to the validated request display name before `finishJoin`, so old fake clients and all real clients have one authoritative local name.

Add `ipc.CommandRoomMembers` to `Handle` and implement:

```go
func (service *Service) roomMembers(ctx context.Context) ipc.Response {
	service.mu.RLock()
	joined := service.joined
	controlClient := service.options.Control
	status := service.status
	service.mu.RUnlock()
	if joined == nil { return ipc.ErrorResponse(CodeNotJoined) }
	snapshotClient, ok := controlClient.(SnapshotClient)
	if !ok { return ipc.ErrorResponse(CodeControlFailed) }
	snapshot, err := snapshotClient.Snapshot(ctx, joined.NetworkID)
	if err != nil { return ipc.ErrorResponse(safeControlErrorCode(err)) }
	members, err := projectRoomMembers(*joined, snapshot)
	if err != nil { return ipc.ErrorResponse(CodeControlFailed) }
	response := ipc.SuccessMembersResponse(joined.NetworkID, members)
	response.Status = status
	return response
}
```

`projectRoomMembers` validates matching network ID, local IPv4, all peer names and IPv4s, creates one local row, creates online peer rows, and sorts local first, then `strings.ToLower(DisplayName)`, IPv4, and original display name. It returns fresh slices.

- [ ] **Step 6: Run focused tests repeatedly and verify GREEN**

```powershell
go test ./internal/service -run 'TestServiceRoomMembers|TestServiceRoomJoinMapsSafeControlRoomErrors|TestHTTPControlClientBridges' -count=1 -v
go test ./internal/service -run 'TestServiceRoomMembers|TestServiceRoomJoinMapsSafeControlRoomErrors' -count=20
```

Expected: all tests pass and no response contains an error message.

- [ ] **Step 7: Commit Task 2**

```powershell
git add internal/ipc/protocol.go internal/service/service.go internal/service/service_test.go internal/service/control_client.go internal/service/control_client_test.go
git commit -m "feat: expose safe room member snapshots"
```

### Task 3: Add `vpnctl room members`

**Files:**

- Modify: `cmd/vpnctl/commands.go`
- Test: `cmd/vpnctl/commands_test.go`

- [ ] **Step 1: Write failing parser tests**

Add `room members` to `TestParseCommandCoversServiceAndControlCommands`:

```go
{name: "room members", args: []string{"room", "members"}, kind: serviceCommand, want: ipc.CommandRoomMembers},
```

Add invalid cases:

```go
{"room", "members", "--network", "room-1"},
{"room", "members", "extra"},
```

Add:

```go
type recordingCaller struct {
	request  ipc.Request
	response ipc.Response
	err      error
}

func (caller *recordingCaller) Call(_ context.Context, request ipc.Request) (ipc.Response, error) {
	caller.request = request
	return caller.response, caller.err
}

func TestRunRoomMembersUsesSanitizedServiceResponse(t *testing.T) {
	parsed, err := parseCommand([]string{"room", "members"})
	if err != nil { t.Fatal(err) }
	if parsed.Service != (ipc.Request{Type: ipc.CommandRoomMembers}) { t.Fatalf("request = %#v", parsed.Service) }
	caller := &recordingCaller{response: ipc.SuccessMembersResponse("room-1", []ipc.RoomMember{{DisplayName: "HOST-PC", VirtualIPv4: "10.42.0.2", IsLocal: true, State: ipc.RoomMemberOnline}})}
	var output bytes.Buffer
	if err := runServiceRequest(parsed.Service, &output, caller); err != nil { t.Fatal(err) }
	if caller.request.Type != ipc.CommandRoomMembers { t.Fatalf("request = %#v", caller.request) }
	if !strings.Contains(output.String(), `"members":[{"display_name":"HOST-PC","virtual_ipv4":"10.42.0.2","is_local":true,"state":"online"}]`) {
		t.Fatalf("output = %s", output.String())
	}
}
```

- [ ] **Step 2: Run focused tests and verify RED**

```powershell
go test ./cmd/vpnctl -run 'TestParseCommand|TestRunRoomMembers' -count=1 -v
```

Expected: `room members` returns the room usage error.

- [ ] **Step 3: Implement the no-argument CLI command**

In the `room` switch:

```go
case "members":
	if len(args) != 2 {
		return command{}, errors.New("room members takes no arguments")
	}
	return command{Kind: serviceCommand, Service: ipc.Request{Type: ipc.CommandRoomMembers}}, nil
```

Update every room usage string to `vpnctl room create|endpoint|join|members`. No separate output path is needed because `runServiceRequest` already marshals strict IPC responses.

- [ ] **Step 4: Run package tests and verify GREEN**

```powershell
go test ./cmd/vpnctl -run 'Room|ParseCommand' -count=1 -v
go test ./cmd/vpnctl -count=1
```

- [ ] **Step 5: Commit Task 3**

```powershell
git add cmd/vpnctl/commands.go cmd/vpnctl/commands_test.go
git commit -m "feat: add room members command"
```

### Task 4: Align named-pipe deadlines and wait for service readiness

**Files:**

- Modify: `internal/ipc/client_windows.go`
- Modify: `internal/ipc/client_stub.go`
- Modify: `internal/ipc/server_windows.go`
- Test: `internal/ipc/pipe_windows_test.go`
- Modify: `packaging/windows/install.ps1`
- Test: `cmd/ipv6mesh-installer/main_windows_test.go`

- [ ] **Step 1: Write failing timeout-policy tests**

Extract a platform-neutral helper into `internal/ipc/protocol.go` so Linux CI can test it:

```go
type timeoutClass uint8

const (
	localCommandTimeout timeoutClass = iota
	networkCommandTimeout
)

func commandTimeoutClass(command Command) timeoutClass {
	switch command {
	case CommandJoin, CommandJoinRoom, CommandLeave, CommandRoomMembers:
		return networkCommandTimeout
	default:
		return localCommandTimeout
	}
}

func TestCommandTimeoutClass(t *testing.T) {
	for _, command := range []Command{CommandStatus, CommandConnect, CommandDisconnect} {
		if got := commandTimeoutClass(command); got != localCommandTimeout { t.Errorf("%s = %v", command, got) }
	}
	for _, command := range []Command{CommandJoin, CommandJoinRoom, CommandLeave, CommandRoomMembers} {
		if got := commandTimeoutClass(command); got != networkCommandTimeout { t.Errorf("%s = %v", command, got) }
	}
}
```

On Windows add a named-pipe regression handler that sleeps 5.2 seconds for `room_members`, then returns success. Assert a default client succeeds; set explicit test timeouts so the test cannot hang beyond 10 seconds.

- [ ] **Step 2: Write the failing installer readiness structure test**

In `main_windows_test.go`, add:

```go
func readWindowsPackagingFile(t *testing.T, name string) string {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok { t.Fatal("runtime.Caller failed") }
	path := filepath.Join(filepath.Dir(sourceFile), "..", "..", "packaging", "windows", name)
	contents, err := os.ReadFile(path)
	if err != nil { t.Fatalf("read %s: %v", path, err) }
	return string(contents)
}

func TestInstallScriptWaitsForNamedPipeReadinessAfterServiceStart(t *testing.T) {
	contents := readWindowsPackagingFile(t, "install.ps1")
	start := strings.Index(contents, "Start-Service -Name $ServiceName")
	wait := strings.Index(contents, "Wait-NodeServiceReady")
	success := strings.Index(contents, `Write-Host "IPv6Mesh installed`)
	if start < 0 || wait < 0 || success < 0 || wait < start || wait > success {
		t.Fatalf("readiness order start=%d wait=%d success=%d", start, wait, success)
	}
	for _, required := range []string{"vpnctl.exe", `@("status")`, "15", "ConvertFrom-Json", ".ok"} {
		if !strings.Contains(contents, required) { t.Errorf("readiness logic missing %q", required) }
	}
}
```

Use or add one exact helper that reads a file under `packaging/windows`; do not duplicate path resolution across new tests.

- [ ] **Step 3: Run focused tests and verify RED**

```powershell
go test ./internal/ipc ./cmd/ipv6mesh-installer -run 'TestCommandTimeoutClass|TestNamedPipeSlowNetworkCommand|TestInstallScriptWaitsForNamedPipeReadiness' -count=1 -v
```

Expected: missing timeout class/readiness function, and the current five-second client fails the slow command.

- [ ] **Step 4: Implement command-specific client deadlines**

Keep an explicit override for focused tests:

```go
type Client struct {
	Path           string
	Timeout        time.Duration
	NetworkTimeout time.Duration
}

func NewClient(path string) *Client {
	if path == "" { path = DefaultPipeName }
	return &Client{Path: path, Timeout: 5 * time.Second, NetworkTimeout: 45 * time.Second}
}

func (client *Client) timeoutFor(command Command) time.Duration {
	if commandTimeoutClass(command) == networkCommandTimeout && client.NetworkTimeout > 0 {
		return client.NetworkTimeout
	}
	return client.Timeout
}
```

Use `client.timeoutFor(request.Type)` for `SetDeadline`. Add the same public fields to the non-Windows stub so cross-platform callers compile.

- [ ] **Step 5: Implement the 60-second server context**

Change the default server connection timeout from 30 to 60 seconds. In `handleConnection`, derive a context with that deadline and pass it to both authorization and the handler:

```go
connectionContext := ctx
cancel := func() {}
if server.connectionTimeout > 0 {
	connectionContext, cancel = context.WithTimeout(ctx, server.connectionTimeout)
}
defer cancel()
if err := server.authorizer.Authorize(connectionContext); err != nil { return }
// read request, then:
response, err := server.handler.HandleJSON(connectionContext, data)
```

Keep the socket deadline aligned with the same duration.

- [ ] **Step 6: Implement condition-based service readiness**

Add to `install.ps1` before service mutation:

```powershell
function Wait-NodeServiceReady {
    param(
        [Parameter(Mandatory = $true)][string]$VpnCtl,
        [int]$TimeoutSeconds = 15
    )
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    do {
        try {
            $output = & $VpnCtl status 2>$null
            if ($LASTEXITCODE -eq 0 -and ![string]::IsNullOrWhiteSpace(($output -join ""))) {
                $response = ($output -join [Environment]::NewLine) | ConvertFrom-Json -ErrorAction Stop
                if ($response.ok -eq $true) { return }
            }
        } catch {}
        Start-Sleep -Milliseconds 250
    } while ((Get-Date) -lt $deadline)
    throw "Timed out waiting for IPv6Mesh node service IPC readiness"
}
```

After `Start-Service`, call:

```powershell
Wait-NodeServiceReady -VpnCtl (Join-Path $InstallDirectory "vpnctl.exe") -TimeoutSeconds 15
```

Only then print installation success.

- [ ] **Step 7: Run focused and repeated tests and verify GREEN**

```powershell
go test ./internal/ipc ./cmd/ipv6mesh-installer -run 'TestCommandTimeoutClass|TestNamedPipeRoundTrip|TestNamedPipeSlowNetworkCommand|TestInstallScriptWaitsForNamedPipeReadiness|TestInstallScriptStopsExistingService' -count=1 -v
go test ./internal/ipc -run 'TestCommandTimeoutClass|TestNamedPipeRoundTrip' -count=20
```

The 5.2-second slow test runs once only; do not include it in `-count=20`.

- [ ] **Step 8: Commit Task 4**

```powershell
git add internal/ipc/protocol.go internal/ipc/protocol_test.go internal/ipc/client_windows.go internal/ipc/client_stub.go internal/ipc/server_windows.go internal/ipc/pipe_windows_test.go packaging/windows/install.ps1 cmd/ipv6mesh-installer/main_windows_test.go
git commit -m "fix: align room IPC and service readiness timeouts"
```

### Task 5: Add host preflight and exclusive UI mode ownership

**Files:**

- Modify: `packaging/windows/ui.ps1`
- Test: `cmd/ipv6mesh-installer/main_windows_test.go`

- [ ] **Step 1: Write failing pure state-transition tests**

Add a PowerShell AST extraction test following `TestWindowsUIStatusLogDecision`. Extract `Test-UiFlowTransition` and run exactly:

```powershell
$allowed = @(
    @('Idle','HostSetup'), @('Idle','MemberSetup'),
    @('HostSetup','Idle'), @('HostSetup','PreparingHost'),
    @('MemberSetup','Idle'), @('MemberSetup','PreparingMember'),
    @('PreparingHost','Hosting'), @('PreparingHost','Cleaning'),
    @('PreparingMember','JoinedMember'), @('PreparingMember','Cleaning'),
    @('Hosting','Cleaning'), @('JoinedMember','Cleaning'),
    @('Cleaning','Idle'), @('Cleaning','HostSetup'), @('Cleaning','MemberSetup')
)
foreach ($pair in $allowed) {
    if (!(Test-UiFlowTransition -From $pair[0] -To $pair[1])) { throw ("rejected " + ($pair -join ' -> ')) }
}
$rejected = @(
    @('Hosting','MemberSetup'), @('Hosting','JoinedMember'),
    @('JoinedMember','HostSetup'), @('JoinedMember','Hosting'),
    @('PreparingHost','PreparingMember'), @('PreparingMember','PreparingHost')
)
foreach ($pair in $rejected) {
    if (Test-UiFlowTransition -From $pair[0] -To $pair[1]) { throw ("accepted " + ($pair -join ' -> ')) }
}
```

- [ ] **Step 2: Write failing mutex and preflight-order tests**

Add structural/AST tests requiring:

```go
for _, required := range []string{
	`Global\IPv6Mesh.WindowsUI`,
	`function Enter-UiInstance`,
	`function Exit-UiInstance`,
	`IPv6Mesh 已在运行。请使用现有窗口。`,
} { /* assert contained */ }
```

For preflight order, isolate the `Join-MemberRoom` function extent and assert `Assert-MemberControlReady` occurs after endpoint calculation and before `Install-NodeService`. Assert the preflight function calls `Test-ControlHealth -Quiet` and throws the exact safe Chinese message. Assert it contains no retry loop.

Add an AST ordering test for both primary functions. In `Start-HostRoom`, the first state-changing call must be `Set-UiFlowState 'PreparingHost'`, before `Refresh-LocalIPv6`, `Start-ControlPlane`, or `Invoke-VpnCtl`. In `Join-MemberRoom`, it must be `Set-UiFlowState 'PreparingMember'`, before endpoint validation, preflight, installation, or `Invoke-VpnCtl`. The function must return immediately unless the current state is `HostSetup` or `MemberSetup`. This proves two rapid clicks cannot launch two external operations.

Add a mutex helper test that holds `Global\IPv6Mesh.WindowsUI` in one PowerShell process, extracts `Enter-UiInstance`, and asserts a second call returns false without launching the form. Use a 10-second test context and always release the held mutex in cleanup.

- [ ] **Step 3: Run focused tests and verify RED**

```powershell
go test ./cmd/ipv6mesh-installer -run 'TestWindowsUI(FlowTransitions|SingleInstance|MemberPreflight)' -count=1 -v
```

Expected: missing transition, mutex, and preflight functions.

- [ ] **Step 4: Implement the mutex before UI side effects**

Add state:

```powershell
$script:uiMutex = $null
$script:ownsUiMutex = $false
```

Add:

```powershell
function Enter-UiInstance {
    $createdNew = $false
    try {
        $script:uiMutex = New-Object System.Threading.Mutex($true, 'Global\IPv6Mesh.WindowsUI', [ref]$createdNew)
        if (!$createdNew) {
            try { $script:ownsUiMutex = $script:uiMutex.WaitOne(0) } catch [System.Threading.AbandonedMutexException] { $script:ownsUiMutex = $true }
        } else { $script:ownsUiMutex = $true }
        return $script:ownsUiMutex
    } catch {
        if ($null -ne $script:uiMutex) { $script:uiMutex.Dispose(); $script:uiMutex = $null }
        return $false
    }
}

function Exit-UiInstance {
    if ($script:ownsUiMutex -and $null -ne $script:uiMutex) {
        try { $script:uiMutex.ReleaseMutex() } catch {}
    }
    $script:ownsUiMutex = $false
    if ($null -ne $script:uiMutex) { $script:uiMutex.Dispose(); $script:uiMutex = $null }
}
```

The `LayoutAudit` path must not acquire the production mutex. In the normal path, acquire it before IPv6 detection, timers, process launch, or resource changes. If acquisition fails, show only the approved message and return. Call `Exit-UiInstance` in the outermost `finally` after `Stop-AllResources`.

- [ ] **Step 5: Implement the explicit state machine**

Add:

```powershell
$script:uiFlowState = 'Idle'
$script:uiFlowStates = @('Idle','HostSetup','MemberSetup','PreparingHost','PreparingMember','Hosting','JoinedMember','Cleaning')
```

`Test-UiFlowTransition` uses a fixed allowlist of the pairs from the test. `Set-UiFlowState` rejects an illegal transition before changing controls. A single `Update-UiFlowControls` derives button enabled/text state from `uiFlowState`; do not scatter mode conditions through click handlers.

Required behavior:

- `Show-HostPage`: only `Idle -> HostSetup`.
- `Show-MemberPage`: only `Idle -> MemberSetup`.
- Back: only setup -> `Idle`.
- Start host: `HostSetup -> PreparingHost`; success -> `Hosting`; failure -> `Cleaning -> HostSetup`.
- Join member: `MemberSetup -> PreparingMember`; success -> `JoinedMember`; failure -> `Cleaning -> MemberSetup`.
- Shared exit button text is `结束房间` in Hosting and `离开房间` in JoinedMember.
- While preparing or cleaning, all navigation and primary actions are disabled and repeat clicks return without side effects.
- `Show-WelcomePage` refuses Hosting and JoinedMember.

- [ ] **Step 6: Implement safe host health preflight before installation**

Add:

```powershell
function Assert-MemberControlReady {
    if (!(Test-ControlHealth -Quiet)) {
        throw '房主控制面不可访问，请确认房主窗口仍在运行且 TCP 8080 可达。'
    }
}
```

In `Join-MemberRoom`, after setting `$script:controlUrl` and the read-only box but before `Install-NodeService`, call `Assert-MemberControlReady`. Preflight failure must not set `startedNodeService` and must not call `Stop-NodeService`.

Add `control_unreachable` and `operation_timeout` to the UI error-code allowlist and exact messages from the specification.

- [ ] **Step 7: Implement explicit exit cleanup**

Replace the shared `Leave-Node` click with `Exit-ActiveRoom`. It must:

1. reject calls outside Hosting/JoinedMember;
2. transition to Cleaning;
3. attempt `vpnctl leave` if `activeNetworkId` is nonempty;
4. always attempt local `Stop-StartedResources` in `finally`;
5. clear `activeNetworkId`, status text, virtual IPv4 labels, and later member rows;
6. transition Cleaning -> Idle and show Welcome only after cleanup ends;
7. log the leave error safely without raw stderr.

Window shutdown remains idempotent and does not try to navigate after the form begins disposal.

- [ ] **Step 8: Run focused state tests repeatedly and verify GREEN**

```powershell
go test ./cmd/ipv6mesh-installer -run 'TestWindowsUI(FlowTransitions|SingleInstance|MemberPreflight|UsesRoomWorkflow)' -count=1 -v
go test ./cmd/ipv6mesh-installer -run 'TestWindowsUI(FlowTransitions|MemberPreflight)' -count=20
```

- [ ] **Step 9: Commit Task 5**

```powershell
git add packaging/windows/ui.ps1 cmd/ipv6mesh-installer/main_windows_test.go
git commit -m "fix: lock Windows room modes and preflight hosts"
```

### Task 6: Add the live responsive A+C member list

**Files:**

- Modify: `packaging/windows/ui.ps1`
- Test: `cmd/ipv6mesh-installer/main_windows_test.go`

- [ ] **Step 1: Write failing member refresh decision tests**

Add a pure `Get-MemberLogDecision` test with the same transition matrix as status logging:

```powershell
$cases = @(
    @{ Automatic=$true; Succeeded=$true; Fingerprint='HOST-PC|10.42.0.2|True|online'; HasPrevious=$false; PreviousSucceeded=$false; PreviousFingerprint=''; Want='Changed' },
    @{ Automatic=$true; Succeeded=$true; Fingerprint='HOST-PC|10.42.0.2|True|online'; HasPrevious=$true; PreviousSucceeded=$true; PreviousFingerprint='HOST-PC|10.42.0.2|True|online'; Want='None' },
    @{ Automatic=$true; Succeeded=$false; Fingerprint=''; HasPrevious=$true; PreviousSucceeded=$true; PreviousFingerprint='x'; Want='Failed' },
    @{ Automatic=$true; Succeeded=$false; Fingerprint=''; HasPrevious=$true; PreviousSucceeded=$false; PreviousFingerprint=''; Want='None' },
    @{ Automatic=$true; Succeeded=$true; Fingerprint='x'; HasPrevious=$true; PreviousSucceeded=$false; PreviousFingerprint=''; Want='Recovered' }
)
```

The implementation may delegate to `Get-StatusLogDecision`, but the member behavior must have its own state variables.

- [ ] **Step 2: Write failing responsive mode tests**

Extract `Get-MemberLayoutMode` and assert:

```powershell
if ((Get-MemberLayoutMode -AvailableWidth 1120 -SettingsPreferredWidth 620 -MembersMinimumWidth 300 -Gap 16) -ne 'Wide') { throw 'wide failed' }
if ((Get-MemberLayoutMode -AvailableWidth 900 -SettingsPreferredWidth 620 -MembersMinimumWidth 300 -Gap 16) -ne 'Narrow') { throw 'narrow failed' }
if ((Get-MemberLayoutMode -AvailableWidth 0 -SettingsPreferredWidth 620 -MembersMinimumWidth 300 -Gap 16) -ne 'Narrow') { throw 'zero failed' }
```

The exact decision is `Wide` only when available width is at least settings preferred width + members minimum width + gap.

- [ ] **Step 3: Extend the real-control layout audit before implementation**

Add audit samples for each Host/Member case with:

- `MemberLayoutMode`;
- member panel and grid positive width/height;
- settings/member sibling intersection is empty;
- member panel lies completely in Panel1's content or scrollable virtual extent;
- diagnostics remains only in Panel2;
- wide case at 1120 and 1440 is `Wide`;
- 900 minimum, 760 constrained, and large-font cases are `Narrow`;
- splitter distance survives wide/narrow switches;
- no status timer, `vpnctl`, service, or control process starts during audit.

Update `TestWindowsUIResponsiveLayoutAudit` to require the new JSON fields and modes.

- [ ] **Step 4: Run focused tests and verify RED**

```powershell
go test ./cmd/ipv6mesh-installer -run 'TestWindowsUI(MemberLogDecision|MemberLayoutMode|ResponsiveLayoutAudit)' -count=1 -v
```

Expected: missing helper functions and missing member controls/audit fields.

- [ ] **Step 5: Build reusable member panels**

Add script state:

```powershell
$script:hostMemberGrid = $null
$script:memberMemberGrid = $null
$script:memberPanels = @()
$script:memberGrids = @()
$script:hasMemberRefreshResult = $false
$script:lastMemberRefreshSucceeded = $false
$script:lastMemberFingerprint = ''
$script:memberRefreshInProgress = $false
```

`New-RoomMembersPanel` returns a `GroupBox` with a fill-docked `TableLayoutPanel`, count/status labels, and a read-only `DataGridView`. Configure exactly three columns: `名称`, `虚拟 IPv4`, `状态`; hide row headers; disable add/delete/edit; use full-width autosizing; set a safe minimum size of 300 x 150.

Create one panel/grid for Host and one for Member. `Set-RoomMemberRows` clears and repopulates both grids only after a complete successful response. Append `（本机）` to local display text, map `online` to `在线`, and update both count labels.

- [ ] **Step 6: Implement responsive wide/right and narrow/stacked page shells**

Wrap each existing settings grid and its member panel in a fill-width `TableLayoutPanel` page shell. `Set-ResponsiveMemberLayout` must:

- call `Get-MemberLayoutMode` using DPI-scaled preferred sizes;
- suspend layout;
- in Wide mode use two columns: percent settings and bounded member column;
- in Narrow mode use one column with settings row then member row;
- reparent existing controls without recreating them;
- preserve `userSplitterDistance`;
- resume layout and perform one layout pass;
- never invoke status refresh or log.

Call it on operation viewport size changes and after Host/Member becomes visible. Guard recursion with `$script:updatingMemberLayout`.

- [ ] **Step 7: Implement safe two-second member refresh**

Add:

```powershell
function Get-RoomMembers {
    param([switch]$Automatic)
    if ($script:memberRefreshInProgress -or [string]::IsNullOrWhiteSpace($script:activeNetworkId)) { return $null }
    if ($script:uiFlowState -notin @('Hosting','JoinedMember')) { return $null }
    $script:memberRefreshInProgress = $true
    try {
        $result = Invoke-VpnCtl -Arguments @('room','members') -SuppressStandardOutput -Quiet:$Automatic
        $response = Convert-ResultToJson $result '读取房间成员' -Quiet:$Automatic
        $members = @($response.members)
        $fingerprint = (($members | ForEach-Object { '{0}|{1}|{2}|{3}' -f $_.display_name,$_.virtual_ipv4,$_.is_local,$_.state }) -join ';')
        Set-RoomMemberRows $members
        # Use Get-MemberLogDecision; update transition state and log changed/recovered once.
        return $members
    } catch {
        # Preserve existing rows; update only the safe refresh label; log a failure transition once.
        return $null
    } finally {
        $script:memberRefreshInProgress = $false
    }
}
```

`Invoke-AutomaticStatusRefresh` calls `Get-NodeStatus -Automatic`, then `Get-RoomMembers -Automatic` only in Hosting/JoinedMember. A successful join performs one immediate member refresh. `Clear-RoomMembers` resets both grids and fingerprints on explicit exit/end and shutdown.

- [ ] **Step 8: Run focused and repeated UI tests and verify GREEN**

```powershell
go test ./cmd/ipv6mesh-installer -run 'TestWindowsUI(Member|ResponsiveLayoutAudit|LiveStatusTimer)' -count=1 -v
go test ./cmd/ipv6mesh-installer -run 'TestWindowsUI(MemberLogDecision|MemberLayoutMode)' -count=20
```

Also parse every PowerShell file:

```powershell
$failures = 0
Get-ChildItem packaging/windows -Filter *.ps1 | ForEach-Object {
    $tokens = $null; $errors = $null
    [void][System.Management.Automation.Language.Parser]::ParseFile($_.FullName, [ref]$tokens, [ref]$errors)
    if ($errors.Count -gt 0) { $failures += $errors.Count; $errors | ForEach-Object { Write-Error $_.Message } }
}
if ($failures -ne 0) { exit 1 }
```

- [ ] **Step 9: Commit Task 6**

```powershell
git add packaging/windows/ui.ps1 cmd/ipv6mesh-installer/main_windows_test.go
git commit -m "feat: show responsive live room members"
```

### Task 7: Cover the complete room lifecycle and update operator documentation

**Files:**

- Modify: `internal/control/room_integration_test.go`
- Modify: `README.md`
- Modify: `packaging/windows/README.md`
- Modify: `docs/operator.md`
- Test: `cmd/ipv6mesh-installer/main_windows_test.go`

- [ ] **Step 1: Write the failing integration assertions**

Extend the existing room lifecycle integration fixture to create host and member, then use the authenticated snapshot or service bridge to assert:

```go
if got, want := []string{host.Node.DisplayName, member.Node.DisplayName}, []string{"HOST-PC", "MEMBER-PC"}; !reflect.DeepEqual(got, want) {
	t.Fatalf("names = %v, want %v", got, want)
}
if host.Membership.VirtualIPv4.Equal(member.Membership.VirtualIPv4) {
	t.Fatalf("duplicate address %s", host.Membership.VirtualIPv4)
}
hostSnapshot, err := client.Snapshot(context.Background(), room.ID, host.SessionToken)
if err != nil { t.Fatal(err) }
if len(hostSnapshot.Peers) != 1 || hostSnapshot.Peers[0].DisplayName != "MEMBER-PC" || !hostSnapshot.Peers[0].VirtualIPv4.Equal(member.Membership.VirtualIPv4) {
	t.Fatalf("host members = %#v", hostSnapshot.Peers)
}
if err := client.Leave(context.Background(), room.ID, member.Node.ID, member.SessionToken); err != nil { t.Fatal(err) }
hostSnapshot, err = client.Snapshot(context.Background(), room.ID, host.SessionToken)
if err != nil { t.Fatal(err) }
if len(hostSnapshot.Peers) != 0 { t.Fatalf("member survived leave: %#v", hostSnapshot.Peers) }
```

Use the fixture's actual variable names and existing API signatures; do not create a duplicate integration server.

- [ ] **Step 2: Add documentation-content tests before docs**

Extend the installer documentation test to require these Chinese concepts in the Windows README and equivalent English concepts in root README:

- room members show name, virtual IPv4, and online;
- wide window right column and narrow window stacked list;
- host/member modes cannot be active together;
- explicit end/leave before switching;
- preflight checks `/healthz` before service installation;
- `control_unreachable` and `operation_timeout` actions;
- no automatic retry, offline timeout, or room restoration.

Assert normal-user docs do not introduce token, invite, public-key, endpoint, or administrator fields into the member list.

- [ ] **Step 3: Run focused tests and verify RED**

```powershell
go test ./internal/control ./cmd/ipv6mesh-installer -run 'TestRoomLifecycle|TestWindowsDocumentation' -count=1 -v
```

Expected: documentation assertions fail until docs are updated. If lifecycle already satisfies the control-plane part, retain it as regression coverage and record that only the new documentation test is RED.

- [ ] **Step 4: Update documentation without changing the approved security boundary**

Document the exact normal workflow:

1. host creates and remains on the host page;
2. member enters the host IPv6;
3. UI checks `/healthz` before service installation;
4. successful join shows the live member list;
5. every returned active membership reads `在线`; no 30-second offline state exists;
6. host must **结束房间**, member must **离开房间**, before selecting the other mode;
7. a second UI process is rejected;
8. host window closure still ends the in-memory room.

Add troubleshooting for `control_unreachable` and `operation_timeout` without pasting raw errors or credentials. Keep legacy invite commands confined to the existing developer-compatibility section.

- [ ] **Step 5: Run integration and documentation tests repeatedly**

```powershell
go test ./internal/control -run 'TestRoomLifecycle' -count=20
go test ./cmd/ipv6mesh-installer -run 'TestWindowsDocumentation|TestWindowsPackageIncludesChineseUI' -count=20
```

- [ ] **Step 6: Commit Task 7**

```powershell
git add internal/control/room_integration_test.go README.md packaging/windows/README.md docs/operator.md cmd/ipv6mesh-installer/main_windows_test.go
git commit -m "test: cover reliable room membership workflow"
```

### Task 8: Final verification and installer build

**Files:**

- Modify only if a verification failure exposes a defect covered by the approved specification.
- Never modify generated release assets into Git.

- [ ] **Step 1: Format and run whitespace checks**

```powershell
gofmt -w internal/ipc/protocol.go internal/ipc/protocol_test.go internal/ipc/client_windows.go internal/ipc/client_stub.go internal/ipc/server_windows.go internal/ipc/pipe_windows_test.go internal/service/service.go internal/service/service_test.go internal/service/control_client.go internal/service/control_client_test.go cmd/vpnctl/commands.go cmd/vpnctl/commands_test.go internal/control/room_integration_test.go cmd/ipv6mesh-installer/main_windows_test.go
$drift = gofmt -l .
if ($drift) { $drift; exit 1 }
git diff --check
```

- [ ] **Step 2: Run focused race- and timing-sensitive tests repeatedly**

```powershell
go test ./internal/ipc ./internal/service ./internal/control ./cmd/vpnctl ./cmd/ipv6mesh-installer -run 'RoomMembers|RoomJoin|RoomLifecycle|FlowTransition|MemberLayout|MemberLog|MemberPreflight|ServiceReady' -count=20
```

Exclude only the deliberate 5.2-second slow-pipe test from the repeated expression; run it once separately.

- [ ] **Step 3: Run the complete Go verification**

```powershell
go test -count=1 ./...
go vet ./...
$env:GOOS='windows'; $env:GOARCH='amd64'; go test -run '^$' ./...
Remove-Item Env:GOOS,Env:GOARCH -ErrorAction SilentlyContinue
```

All commands must exit 0.

- [ ] **Step 4: Parse PowerShell and run the real layout audit**

```powershell
$failures = 0
Get-ChildItem packaging/windows -Filter *.ps1 | ForEach-Object {
    $tokens = $null; $errors = $null
    [void][System.Management.Automation.Language.Parser]::ParseFile($_.FullName, [ref]$tokens, [ref]$errors)
    if ($errors.Count -gt 0) { $failures += $errors.Count; $errors | ForEach-Object { Write-Error $_.Message } }
}
if ($failures -ne 0) { exit 1 }
go test ./cmd/ipv6mesh-installer -run 'TestWindowsUIResponsiveLayoutAudit' -count=1 -v
```

- [ ] **Step 5: Scan for secret leakage**

Search the changed range and generated logs for private keys, tokens, Bearer values, and prohibited member fields. Test fixtures may use explicit fake marker values only; no real values are allowed.

```powershell
git diff --unified=0 main...HEAD | rg -n 'Bearer [A-Za-z0-9_\-]{12,}|session_token.{0,8}[A-Za-z0-9_\-]{12,}|private_key|BEGIN .*PRIVATE KEY'
```

Expected: no real credential material. Any test-only field-name match must be reviewed and reported, not silently ignored.

- [ ] **Step 6: Rebuild the Windows installer from the final branch**

Use the verified local inputs:

```powershell
& .\packaging\windows\build-installer.ps1 `
  -WireGuardDll 'C:\Users\Eser\Documents\Codex\2026-08-11\en\work\wireguard-nt-1.1\wireguard-nt\bin\amd64\wireguard.dll' `
  -WireGuardLicense 'C:\Users\Eser\Documents\Codex\2026-08-11\en\work\wireguard-nt-1.1\wireguard-nt\LICENSE.txt' `
  -Version '0.1.0-dev' `
  -GoCommand 'C:\Users\Eser\.cache\codex-runtimes\go1.24.12\bin\go.exe'
```

Record exit code, final installer size, and SHA-256. Confirm `payload.zip` and `cmd/ipv6mesh-installer/payload_embed_windows.go` were cleaned.

- [ ] **Step 7: Run installer-focused tests after the build**

```powershell
go test ./cmd/ipv6mesh-installer -count=1
go test ./cmd/ipv6mesh-installer -run 'TestWindows(PackageIncludesChineseUI|UIUses|UIResponsive|InstallScript)' -count=1 -v
```

- [ ] **Step 8: Perform final Git hygiene checks**

```powershell
git diff --check main...HEAD
git status --short
git ls-files packaging/windows/dist cmd/ipv6mesh-installer/payload_embed_windows.go packaging/windows/payload.zip
```

Expected: clean worktree and no generated asset paths tracked.

- [ ] **Step 9: Route any verification failure back to its owning task**

Do not create a generic or empty verification commit. If a command fails, reopen the task that owns the behavior, add or rerun its focused RED test, implement the smallest fix, repeat that task's GREEN commands, and reuse that task's exact file list and commit subject with a `fix:` prefix. Then restart Task 8 from Step 1.

## Executor Handoff Report

Return all of the following to the primary reviewer:

- isolated worktree path and branch;
- base commit and final commit;
- ordered commit list with one-line task summaries;
- `git diff --name-status main...HEAD`;
- every RED command and its expected failure;
- every GREEN and final verification command with exit status;
- installer size and SHA-256;
- confirmation that payload-generated files and installer binaries are not tracked;
- any manual UI behavior that could not be exercised;
- explicit statement that no merge, push, tag, or Release action was performed.

The executor must not claim real two-machine public-IPv6, router/firewall, UAC, WireGuard tunnel, or DPI acceptance unless it was actually performed.
