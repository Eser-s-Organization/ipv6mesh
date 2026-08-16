# Host-Bound Room Flow Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the three-role Windows workflow with an ephemeral host-created room and a member flow that joins by entering only the host's IPv6 address.

**Architecture:** The control server gains opt-in single-room mode with an authenticated room-creation endpoint and an unauthenticated, rate-limited room-join endpoint. Room join creates and consumes an internal invitation through the existing enrollment transaction, while the Windows service, CLI, and WinForms UI expose only create-room and join-by-IPv6 operations.

**Tech Stack:** Go 1.23 module (verified with the local Go 1.24.12 toolchain), standard-library HTTP and `net/netip`, existing PostgreSQL and memory repositories, Named Pipe JSON IPC, Windows PowerShell 5.1 WinForms, WireGuardNT, Go tests, PowerShell parser checks.

---

## Required baseline and execution rules

- Start from `main` at or after design commit `631cacd`.
- Read `docs/superpowers/specs/2026-08-16-host-bound-room-flow-design.md` before editing.
- In the persistent PowerShell execution session, bind the verified user-local toolchain before the first Go command:

  ```powershell
  Set-Alias -Name go -Value 'C:\Users\Eser\.cache\codex-runtimes\go1.24.12\bin\go.exe'
  go version
  ```

  Expected: `go version go1.24.12 windows/amd64`.
- Use strict red-green-refactor. For every behavior below, run the named focused test and observe the expected failure before production code is written.
- Preserve the existing `network create`, `invite create`, and invite-based `join` commands for developer compatibility.
- Do not commit generated installers, `wireguard.dll`, credentials, tokens, logs, or local runtime caches.
- Make one commit per task using the exact commit subject shown.

## File structure

### New files

- `internal/room/address.go`: validate a raw host IPv6 literal and construct the fixed room control URL.
- `internal/room/address_test.go`: table tests for accepted and rejected host input.
- `internal/control/room.go`: room coordinator, join limiter, room creation, internal-invite enrollment, and named room errors.
- `internal/control/room_http_test.go`: focused room-mode HTTP and concurrency tests.
- `internal/control/room_client.go`: client methods for room creation and room join.
- `internal/control/room_client_test.go`: strict client request/response and secret-boundary tests.
- `internal/control/room_integration_test.go`: two-node room lifecycle test with the real handler and memory repository.

### Modified files

- `internal/control/repository_contract.go`: add unused-invite revocation.
- `internal/db/repository.go`: implement memory invite revocation.
- `internal/db/postgres.go`: implement PostgreSQL invite revocation.
- `internal/db/repository_test.go`: shared memory/PostgreSQL revocation contract coverage.
- `internal/control/http.go`: route room endpoints and share enrollment response encoding.
- `internal/control/http_test.go`: keep existing enrollment regression coverage compiling and green.
- `cmd/control-server/config.go`: load `CONTROL_ROOM_MODE`.
- `cmd/control-server/config_test.go`: room-mode parsing tests.
- `cmd/control-server/runtime.go`: pass room mode and limits to the handler.
- `cmd/control-server/main_test.go`: runtime room-mode smoke test.
- `internal/control/client.go`: share enrollment response decoding.
- `internal/ipc/protocol.go`: add strict `join_room` request fields.
- `internal/ipc/protocol_test.go`: JSON compatibility and rejection tests.
- `internal/service/service.go`: dispatch room join through the active control client.
- `internal/service/service_test.go`: room-join state and rollback tests.
- `internal/service/control_client.go`: bridge public room enrollment.
- `internal/service/control_client_test.go`: room endpoint and session tests.
- `cmd/vpn-service/main_windows.go`: provide the configured control URL to the service.
- `cmd/vpn-service/service_config_windows_test.go`: verify room URL wiring.
- `cmd/vpnctl/commands.go`: parse `room create`, `room endpoint`, and `room join`.
- `cmd/vpnctl/commands_test.go`: strict room command tests.
- `cmd/vpnctl/main.go`: execute local endpoint, control-plane create, and service room-join commands.
- `cmd/vpnctl/output.go`: emit the room endpoint JSON without secrets.
- `packaging/windows/ui.ps1`: replace the role-combo form with welcome, host, and member pages.
- `cmd/ipv6mesh-installer/main_windows_test.go`: assert the new page flow and absence of manual secrets.
- `README.md`: replace the administrator/invite walkthrough with the two-path room workflow.
- `docs/operator.md`: retain legacy developer commands and document room-mode boundaries.

## Task 1: Add an unused-invite revocation primitive

**Files:**

- Modify: `internal/control/repository_contract.go:23-46`
- Modify: `internal/db/repository.go:557-640`
- Modify: `internal/db/postgres.go:625-750`
- Modify: `internal/db/repository_test.go`

- [ ] **Step 1: Write the failing memory-repository test**

Add this test beside the existing invite lifecycle tests in `internal/db/repository_test.go`:

```go
func TestMemoryRepositoryRevokesUnusedInvite(t *testing.T) {
    repository := NewMemoryRepository()
    now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
    network := control.Network{
        ID: "room-1", Name: "room", IPv4Pool: "10.42.0.0/24",
        OwnerID: "owner", ConfigVersion: 1, CreatedAt: now,
    }
    if err := repository.CreateNetwork(context.Background(), network); err != nil {
        t.Fatal(err)
    }
    invite := control.Invite{
        ID: "invite-1", NetworkID: network.ID, TokenHash: "hash",
        CreatedAt: now, ExpiresAt: now.Add(time.Hour),
    }
    if err := repository.CreateInvite(context.Background(), invite); err != nil {
        t.Fatal(err)
    }
    if err := repository.RevokeInvite(context.Background(), invite.ID, now.Add(time.Minute)); err != nil {
        t.Fatal(err)
    }
    if _, err := repository.ConsumeInvite(context.Background(), invite.ID, invite.TokenHash, now.Add(2*time.Minute)); !errors.Is(err, control.ErrInviteRevoked) {
        t.Fatalf("consume revoked invite error = %v, want ErrInviteRevoked", err)
    }
}
```

- [ ] **Step 2: Run the focused test and verify RED**

Run:

```text
go test ./internal/db -run TestMemoryRepositoryRevokesUnusedInvite -count=1 -v
```

Expected: compile failure because `MemoryRepository.RevokeInvite` does not exist.

- [ ] **Step 3: Extend the repository contract and memory implementation**

Add to `control.Repository` immediately after `CreateInvite`:

```go
RevokeInvite(context.Context, string, time.Time) error
```

Add to `internal/db/repository.go`:

```go
func (repository *MemoryRepository) RevokeInvite(ctx context.Context, inviteID string, revokedAt time.Time) error {
    if err := contextError(ctx); err != nil {
        return err
    }
    if inviteID == "" || revokedAt.IsZero() {
        return control.ErrValidation
    }
    repository.mu.Lock()
    defer repository.mu.Unlock()
    invite, ok := repository.invites[inviteID]
    if !ok {
        return ErrNotFound
    }
    if invite.ConsumedAt != nil {
        return ErrInviteConsumed
    }
    if invite.RevokedAt != nil {
        return nil
    }
    value := revokedAt.UTC()
    invite.RevokedAt = &value
    repository.invites[inviteID] = cloneInvite(invite)
    return nil
}
```

- [ ] **Step 4: Run the memory test and verify GREEN**

Run:

```text
go test ./internal/db -run TestMemoryRepositoryRevokesUnusedInvite -count=1 -v
```

Expected: PASS.

- [ ] **Step 5: Write the failing PostgreSQL revocation tests**

Following the repository's existing `sqlmock` style, add two cases:

```go
func TestPostgresRepositoryRevokesUnusedInvite(t *testing.T) {
    database, mock, err := sqlmock.New()
    if err != nil { t.Fatal(err) }
    defer database.Close()
    repository := NewPostgresRepository(database)
    revokedAt := time.Date(2026, 8, 16, 12, 1, 0, 0, time.UTC)

    mock.ExpectBegin()
    mock.ExpectExec("UPDATE invites").
        WithArgs("invite-1", revokedAt).
        WillReturnResult(sqlmock.NewResult(0, 1))
    mock.ExpectCommit()

    if err := repository.RevokeInvite(context.Background(), "invite-1", revokedAt); err != nil {
        t.Fatal(err)
    }
    if err := mock.ExpectationsWereMet(); err != nil { t.Fatal(err) }
}

func TestPostgresRepositoryRejectsRevokingConsumedInvite(t *testing.T) {
    database, mock, err := sqlmock.New()
    if err != nil { t.Fatal(err) }
    defer database.Close()
    repository := NewPostgresRepository(database)
    revokedAt := time.Date(2026, 8, 16, 12, 1, 0, 0, time.UTC)

    mock.ExpectBegin()
    mock.ExpectExec("UPDATE invites").
        WithArgs("invite-1", revokedAt).
        WillReturnResult(sqlmock.NewResult(0, 0))
    mock.ExpectQuery("SELECT consumed_at, revoked_at FROM invites").
        WithArgs("invite-1").
        WillReturnRows(sqlmock.NewRows([]string{"consumed_at", "revoked_at"}).AddRow(revokedAt, nil))
    mock.ExpectRollback()

    if err := repository.RevokeInvite(context.Background(), "invite-1", revokedAt); !errors.Is(err, control.ErrInviteConsumed) {
        t.Fatalf("error = %v, want ErrInviteConsumed", err)
    }
}
```

- [ ] **Step 6: Run the PostgreSQL tests and verify RED**

Run:

```text
go test ./internal/db -run 'TestPostgresRepository(RevokesUnusedInvite|RejectsRevokingConsumedInvite)' -count=1 -v
```

Expected: compile failure because `PostgresRepository.RevokeInvite` does not exist.

- [ ] **Step 7: Implement PostgreSQL revocation**

Add to `internal/db/postgres.go`:

```go
func (repository *PostgresRepository) RevokeInvite(ctx context.Context, inviteID string, revokedAt time.Time) error {
    if inviteID == "" || revokedAt.IsZero() {
        return control.ErrValidation
    }
    return repository.withTransaction(ctx, func(executor SQLExecutor) error {
        result, err := executor.ExecContext(ctx, `
            UPDATE invites
            SET revoked_at = $2
            WHERE id = $1 AND consumed_at IS NULL AND revoked_at IS NULL`,
            inviteID, revokedAt.UTC())
        if err != nil {
            return err
        }
        count, err := result.RowsAffected()
        if err != nil {
            return err
        }
        if count == 1 {
            return nil
        }
        var consumedAt, existingRevokedAt sql.NullTime
        err = executor.QueryRowContext(ctx,
            `SELECT consumed_at, revoked_at FROM invites WHERE id = $1`,
            inviteID,
        ).Scan(&consumedAt, &existingRevokedAt)
        if errors.Is(err, sql.ErrNoRows) {
            return ErrNotFound
        }
        if err != nil {
            return err
        }
        if consumedAt.Valid {
            return ErrInviteConsumed
        }
        if existingRevokedAt.Valid {
            return nil
        }
        return ErrConflict
    })
}
```

- [ ] **Step 8: Run repository tests and commit**

Run:

```text
go test ./internal/db -count=1
git diff --check
```

Expected: PASS and no whitespace errors.

Commit:

```text
git add internal/control/repository_contract.go internal/db/repository.go internal/db/postgres.go internal/db/repository_test.go
git commit -m "feat: add invite revocation boundary"
```

## Task 2: Add opt-in room mode and authenticated room creation

**Files:**

- Create: `internal/control/room.go`
- Create: `internal/control/room_http_test.go`
- Modify: `internal/control/http.go:41-177`
- Modify: `cmd/control-server/config.go:14-90`
- Modify: `cmd/control-server/config_test.go`
- Modify: `cmd/control-server/runtime.go:45-60`
- Modify: `cmd/control-server/main_test.go`

- [ ] **Step 1: Write failing configuration tests**

Add:

```go
func TestLoadConfigParsesRoomMode(t *testing.T) {
    environment := map[string]string{
        "CONTROL_BOOTSTRAP_TOKEN": "secret",
        "CONTROL_SESSION_TTL": "1h",
        "CONTROL_INVITE_TTL": "1h",
        "CONTROL_ROOM_MODE": "true",
    }
    config, err := LoadConfigFrom(func(name string) string { return environment[name] })
    if err != nil { t.Fatal(err) }
    if !config.RoomMode { t.Fatal("RoomMode = false, want true") }

    environment["CONTROL_ROOM_MODE"] = "not-a-bool"
    if _, err := LoadConfigFrom(func(name string) string { return environment[name] }); !errors.Is(err, ErrInvalidConfig) {
        t.Fatalf("invalid room mode error = %v", err)
    }
}
```

- [ ] **Step 2: Verify the config test fails**

Run:

```text
go test ./cmd/control-server -run TestLoadConfigParsesRoomMode -count=1 -v
```

Expected: compile failure because `Config.RoomMode` does not exist.

- [ ] **Step 3: Implement room-mode configuration**

Add `RoomMode bool` to `Config`. Parse `CONTROL_ROOM_MODE` only when non-empty:

```go
if raw := strings.TrimSpace(firstEnv(getenv, "CONTROL_ROOM_MODE")); raw != "" {
    value, parseErr := strconv.ParseBool(raw)
    if parseErr != nil {
        return Config{}, &configError{Field: "room_mode", Reason: "must be true or false"}
    }
    config.RoomMode = value
}
```

Pass `RoomMode: config.RoomMode` in `control.HandlerOptions` inside `newHTTPServer`.

- [ ] **Step 4: Write failing room-creation handler tests**

Create `internal/control/room_http_test.go` with a fixture using `db.NewMemoryRepository()`, deterministic IDs, and `HandlerOptions{BootstrapToken: "bootstrap", RoomMode: true}`. Add:

```go
func TestRoomCreationRequiresModeAndBootstrapToken(t *testing.T) {
    enabled := newRoomHTTPFixture(t, true)
    response := enabled.do(http.MethodPost, "/v1/room", `{"name":"IPv6Mesh-HOST","ipv4_pool":"10.42.0.0/24"}`, "")
    assertRoomError(t, response, http.StatusUnauthorized, "unauthorized")

    disabled := newRoomHTTPFixture(t, false)
    response = disabled.do(http.MethodPost, "/v1/room", `{"name":"IPv6Mesh-HOST","ipv4_pool":"10.42.0.0/24"}`, "Bearer bootstrap")
    assertRoomError(t, response, http.StatusNotFound, "room_mode_disabled")
}

func TestRoomCreationAllowsExactlyOneActiveRoom(t *testing.T) {
    fixture := newRoomHTTPFixture(t, true)
    first := fixture.do(http.MethodPost, "/v1/room", `{"name":"IPv6Mesh-HOST","ipv4_pool":"10.42.0.0/24"}`, "Bearer bootstrap")
    if first.Code != http.StatusCreated { t.Fatalf("first room = %d %s", first.Code, first.Body.String()) }
    second := fixture.do(http.MethodPost, "/v1/room", `{"name":"second","ipv4_pool":"10.42.0.0/24"}`, "Bearer bootstrap")
    assertRoomError(t, second, http.StatusConflict, "room_already_exists")
}
```

Use this external-package fixture so `internal/db` can be imported without a cycle:

```go
type roomHTTPFixture struct {
    handler *control.Handler
    repository *db.MemoryRepository
    now time.Time
}

func newRoomHTTPFixture(t *testing.T, enabled bool) *roomHTTPFixture {
    return newRoomHTTPFixtureWithOptions(t, enabled, 0, 0)
}

func newRoomHTTPFixtureWithLimits(t *testing.T, perIP, global int) *roomHTTPFixture {
    return newRoomHTTPFixtureWithOptions(t, true, perIP, global)
}

func newRoomHTTPFixtureWithOptions(t *testing.T, enabled bool, perIP, global int) *roomHTTPFixture {
    t.Helper()
    repository := db.NewMemoryRepository()
    fixture := &roomHTTPFixture{
        repository: repository,
        now: time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC),
    }
    ids := []string{"room-1", "invite-1", "node-1", "invite-2", "node-2"}
    index := 0
    fixture.handler = control.NewHandler(repository, control.HandlerOptions{
        BootstrapToken: "bootstrap",
        RoomMode: enabled,
        RoomJoinPerIP: perIP,
        RoomJoinGlobal: global,
        Clock: func() time.Time { return fixture.now },
        NewID: func() string {
            if index >= len(ids) { return fmt.Sprintf("generated-%d", index) }
            value := ids[index]
            index++
            return value
        },
        TokenRandom: bytes.NewReader(bytes.Repeat([]byte{0x41}, 8192)),
    })
    return fixture
}

func (fixture *roomHTTPFixture) do(method, path, body, authorization string) *httptest.ResponseRecorder {
    return fixture.doFrom("[2001:db8::10]:50000", method, path, body, authorization)
}

func (fixture *roomHTTPFixture) doFrom(remote, method, path, body, authorization string) *httptest.ResponseRecorder {
    request := httptest.NewRequest(method, path, strings.NewReader(body))
    request.RemoteAddr = remote
    request.Header.Set("X-Request-ID", "room-test")
    if authorization != "" { request.Header.Set("Authorization", authorization) }
    response := httptest.NewRecorder()
    fixture.handler.ServeHTTP(response, request)
    return response
}

func (fixture *roomHTTPFixture) createRoom(t *testing.T) {
    t.Helper()
    response := fixture.do(http.MethodPost, "/v1/room",
        `{"name":"IPv6Mesh-HOST","ipv4_pool":"10.42.0.0/24"}`,
        "Bearer bootstrap")
    if response.Code != http.StatusCreated {
        t.Fatalf("create room = %d %s", response.Code, response.Body.String())
    }
}

func assertRoomError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
    t.Helper()
    var body map[string]string
    if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil { t.Fatal(err) }
    if response.Code != status || body["error"] != code {
        t.Fatalf("response = %d %#v, want %d %q", response.Code, body, status, code)
    }
}
```

- [ ] **Step 5: Verify room-creation tests fail**

Run:

```text
go test ./internal/control -run 'TestRoomCreation' -count=1 -v
```

Expected: compile failure because `HandlerOptions.RoomMode` and room routes do not exist.

- [ ] **Step 6: Implement the room coordinator and named errors**

In `internal/control/room.go`, define:

```go
var (
    ErrRoomModeDisabled = errors.New("room mode disabled")
    ErrRoomNotReady = errors.New("room not ready")
    ErrRoomAlreadyExists = errors.New("room already exists")
)

type roomCoordinator struct {
    mu sync.RWMutex
    networkID string
}

func (room *roomCoordinator) active() (string, bool) {
    room.mu.RLock()
    defer room.mu.RUnlock()
    return room.networkID, room.networkID != ""
}

func (room *roomCoordinator) create(ctx context.Context, repository Repository, network Network) error {
    room.mu.Lock()
    defer room.mu.Unlock()
    if room.networkID != "" {
        return ErrRoomAlreadyExists
    }
    if err := repository.CreateNetwork(ctx, network); err != nil {
        return err
    }
    room.networkID = network.ID
    return nil
}
```

Add `RoomMode bool`, `RoomJoinPerIP int`, and `RoomJoinGlobal int` to `HandlerOptions`. Add `room *roomCoordinator` to `Handler` and construct it only when room mode is enabled. The two integer options remain unused until Task 3 adds the limiter, which keeps the shared fixture compiling across both tasks.

Route exact POST paths before the generic network cases:

```go
case request.Method == http.MethodPost && request.URL.Path == "/v1/room":
    handler.createRoom(writer, request)
case request.Method == http.MethodPost && request.URL.Path == "/v1/room/join":
    handler.joinRoom(writer, request)
```

Implement `createRoom` in `room.go`: require the bootstrap administrator, strictly decode `name` and `ipv4_pool`, validate the pool, create a random network ID, and call `room.create`. Use:

```go
func writeErrorCode(writer http.ResponseWriter, status int, code string) {
    writeJSON(writer, status, map[string]string{"error": code})
}
```

Return the exact named codes from the spec.

- [ ] **Step 7: Run room creation and runtime tests**

Run:

```text
go test ./internal/control ./cmd/control-server -run 'Room|Config|HTTPServer' -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit room-mode creation**

```text
git add internal/control/room.go internal/control/room_http_test.go internal/control/http.go cmd/control-server/config.go cmd/control-server/config_test.go cmd/control-server/runtime.go cmd/control-server/main_test.go
git commit -m "feat: add ephemeral room mode"
```

## Task 3: Add rate-limited internal-invite room enrollment

**Files:**

- Modify: `internal/control/room.go`
- Modify: `internal/control/room_http_test.go`
- Modify: `internal/control/http.go:356-526`
- Modify: `internal/control/http_test.go`
- Test: `internal/db/repository_test.go`

- [ ] **Step 1: Write failing room-join success and secret-boundary tests**

Add:

```go
func TestRoomJoinCreatesAndConsumesInternalInvite(t *testing.T) {
    fixture := newRoomHTTPFixture(t, true)
    fixture.createRoom(t)
    response := fixture.do(http.MethodPost, "/v1/room/join",
        `{"public_key":"member-public","display_name":"MEMBER-PC","platform":"windows","client_version":"0.1.0"}`, "")
    if response.Code != http.StatusCreated {
        t.Fatalf("join = %d %s", response.Code, response.Body.String())
    }
    body := response.Body.String()
    for _, forbidden := range []string{"invite_id", "invite-", ".room-secret"} {
        if strings.Contains(body, forbidden) { t.Fatalf("room join leaked %q: %s", forbidden, body) }
    }
    var result struct {
        Membership struct {
            VirtualIPv4 string `json:"virtual_ipv4"`
        } `json:"membership"`
        SessionToken string `json:"session_token"`
    }
    if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil { t.Fatal(err) }
    if result.Membership.VirtualIPv4 == "" || result.SessionToken == "" { t.Fatalf("incomplete result: %+v", result) }
}

func TestRoomJoinBeforeCreationReturnsRoomNotReady(t *testing.T) {
    fixture := newRoomHTTPFixture(t, true)
    response := fixture.do(http.MethodPost, "/v1/room/join",
        `{"public_key":"member-public","display_name":"MEMBER-PC","platform":"windows","client_version":"0.1.0"}`, "")
    assertRoomError(t, response, http.StatusNotFound, "room_not_ready")
}
```

- [ ] **Step 2: Run and verify RED**

Run:

```text
go test ./internal/control -run 'TestRoomJoin(Creates|Before)' -count=1 -v
```

Expected: failure because `joinRoom` is not implemented.

- [ ] **Step 3: Share enrollment response encoding**

Extract this helper from `enroll` so legacy enrollment and room enrollment return the same shape:

```go
func (handler *Handler) writeEnrollmentResult(writer http.ResponseWriter, result enrollmentResult, err error) {
    writer.Header().Set("Cache-Control", "no-store")
    if errors.Is(err, ErrEnrollmentRecoveryPending) && result.SessionToken != "" {
        writer.Header().Set("Retry-After", "1")
        writeJSON(writer, http.StatusServiceUnavailable, enrollmentRecoveryResponse{
            Error: "enrollment_recovery_pending", Retryable: true,
            Node: makeNodeResponse(result.Node),
            Membership: makeMembershipResponse(result.Membership),
            Network: makeNetworkResponse(result.Network),
            Session: sessionResponse{Token: result.SessionToken, Subject: result.Session.Subject, NetworkID: result.Session.NetworkID, ExpiresAt: result.Session.ExpiresAt},
            SessionToken: result.SessionToken,
        })
        return
    }
    if err != nil {
        writeAPIError(writer, statusForError(err), err)
        return
    }
    writeJSON(writer, http.StatusCreated, enrollmentResponse{
        Node: makeNodeResponse(result.Node),
        Membership: makeMembershipResponse(result.Membership),
        Network: makeNetworkResponse(result.Network),
        Session: sessionResponse{Token: result.SessionToken, Subject: result.Session.Subject, NetworkID: result.Session.NetworkID, ExpiresAt: result.Session.ExpiresAt},
        SessionToken: result.SessionToken,
    })
}
```

Update legacy `enroll` to decode its body, call `enrollControl`, and delegate to this helper. Run all existing enrollment tests before continuing.

- [ ] **Step 4: Implement internal invite generation and cleanup**

In `room.go`, add:

```go
func (handler *Handler) newInternalRoomInvite(ctx context.Context, networkID string) (string, string, error) {
    inviteID := handler.newID()
    secret := make([]byte, 32)
    if _, err := io.ReadFull(handler.tokenRandom, secret); err != nil {
        return "", "", err
    }
    token := inviteID + "." + base64.RawURLEncoding.EncodeToString(secret)
    now := handler.clock().UTC()
    invite := Invite{
        ID: inviteID, NetworkID: networkID, TokenHash: auth.HashToken(token),
        CreatedAt: now, ExpiresAt: now.Add(handler.inviteTTL),
    }
    if err := handler.repository.CreateInvite(ctx, invite); err != nil {
        return "", "", err
    }
    return inviteID, token, nil
}
```

Implement `joinRoom` to:

1. reject disabled/unready room mode;
2. enforce a 64 KiB body limit;
3. strictly decode the four fields from the spec;
4. create the internal invite;
5. call `enrollControl`;
6. call `RevokeInvite` only when enrollment failed and `result.SessionToken == ""`;
7. map pool exhaustion to `409 room_full`, other conflicts to `409 node_already_joined`, validation to `422 invalid_node`, and uncertain commit to the existing recovery response;
8. never serialize the internal invite.

Preserve oversized-body identity instead of collapsing it into generic invalid JSON. Add:

```go
var ErrRequestTooLarge = errors.New("request body too large")
```

In `decodeJSON`, use `errors.As(err, &maxBytesError)` for `*http.MaxBytesError` and return `ErrRequestTooLarge`. Add `http.StatusRequestEntityTooLarge` / `request_too_large` to `writeAPIError` and `statusForError`. The room handler then returns exactly `413 request_too_large`.

- [ ] **Step 5: Add failed-enrollment cleanup test**

Use an invalid empty public key after the deterministic `invite-1` is generated:

```go
func TestRoomJoinRevokesInviteAfterValidationFailure(t *testing.T) {
    fixture := newRoomHTTPFixture(t, true)
    fixture.createRoom(t)
    response := fixture.do(http.MethodPost, "/v1/room/join",
        `{"public_key":"","display_name":"MEMBER-PC","platform":"windows","client_version":"0.1.0"}`, "")
    assertRoomError(t, response, http.StatusUnprocessableEntity, "invalid_node")

    rawToken := "invite-1." + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x41}, 32))
    _, err := fixture.repository.ConsumeInvite(
        context.Background(), "invite-1", auth.HashToken(rawToken),
        time.Date(2026, 8, 16, 12, 1, 0, 0, time.UTC),
    )
    if !errors.Is(err, control.ErrInviteRevoked) {
        t.Fatalf("consume cleaned invite error = %v, want ErrInviteRevoked", err)
    }
}
```

Run:

```text
go test ./internal/control -run 'TestRoomJoin' -count=1 -v
```

Expected: PASS.

- [ ] **Step 6: Write failing limiter tests with an injected clock**

Add handler options `RoomJoinPerIP` and `RoomJoinGlobal` for deterministic tests. Extend the fixture so its clock reads a mutable `now` field and its `doFrom` helper accepts `RemoteAddr`. Add:

```go
func TestRoomJoinRateLimitsPerSourceAndResets(t *testing.T) {
    fixture := newRoomHTTPFixtureWithLimits(t, 10, 100)
    fixture.createRoom(t)
    invalid := `{"public_key":"","display_name":"PC","platform":"windows","client_version":"0.1.0"}`
    for attempt := 0; attempt < 10; attempt++ {
        response := fixture.doFrom("[2001:db8::20]:50000", http.MethodPost, "/v1/room/join", invalid, "")
        if response.Code == http.StatusTooManyRequests {
            t.Fatalf("attempt %d was limited early", attempt+1)
        }
    }
    limited := fixture.doFrom("[2001:db8::20]:50000", http.MethodPost, "/v1/room/join", invalid, "")
    assertRoomError(t, limited, http.StatusTooManyRequests, "join_rate_limited")
    fixture.now = fixture.now.Add(61 * time.Second)
    reset := fixture.doFrom("[2001:db8::20]:50000", http.MethodPost, "/v1/room/join", invalid, "")
    if reset.Code == http.StatusTooManyRequests { t.Fatal("limiter did not reset") }
}

func TestRoomJoinRateLimitsGlobally(t *testing.T) {
    fixture := newRoomHTTPFixtureWithLimits(t, 200, 100)
    fixture.createRoom(t)
    invalid := `{"public_key":"","display_name":"PC","platform":"windows","client_version":"0.1.0"}`
    for attempt := 0; attempt < 100; attempt++ {
        remote := fmt.Sprintf("[2001:db8::%x]:50000", attempt+1)
        if response := fixture.doFrom(remote, http.MethodPost, "/v1/room/join", invalid, ""); response.Code == http.StatusTooManyRequests {
            t.Fatalf("global attempt %d was limited early", attempt+1)
        }
    }
    response := fixture.doFrom("[2001:db8::ffff]:50000", http.MethodPost, "/v1/room/join", invalid, "")
    assertRoomError(t, response, http.StatusTooManyRequests, "join_rate_limited")
}
```

Also add an oversized-body case using `strings.Repeat("x", (64<<10)+1)` and assert `413 request_too_large`.

The production defaults are:

```go
const (
    defaultRoomJoinPerIP = 10
    defaultRoomJoinGlobal = 100
    roomJoinWindow = time.Minute
    roomLimiterEntryTTL = 2 * time.Minute
    roomJoinBodyLimit int64 = 64 << 10
)
```

- [ ] **Step 7: Implement the bounded fixed-window limiter**

Define:

```go
type roomLimitEntry struct {
    window time.Time
    count int
    lastSeen time.Time
}

type roomJoinLimiter struct {
    mu sync.Mutex
    now func() time.Time
    perIP int
    global int
    globalWindow time.Time
    globalCount int
    sources map[string]roomLimitEntry
}
```

`allow(remoteAddr string)` must parse the TCP peer with `net.SplitHostPort`, ignore forwarded headers, prune entries whose `lastSeen` is older than two minutes, reset fixed windows after one minute, and increment only accepted attempts. Invalid remote addresses are denied.

- [ ] **Step 8: Run control-plane tests and commit**

Run:

```text
go test ./internal/control ./internal/db -count=1
go test ./internal/control -run 'TestRoomJoin' -count=20
git diff --check
```

Expected: PASS on every run; no internal token appears in test output.

Commit:

```text
git add internal/control/room.go internal/control/room_http_test.go internal/control/http.go internal/control/http_test.go
git commit -m "feat: add open room enrollment"
```

## Task 4: Add shared IPv6 room addressing and control clients

**Files:**

- Create: `internal/room/address.go`
- Create: `internal/room/address_test.go`
- Create: `internal/control/room_client.go`
- Create: `internal/control/room_client_test.go`
- Modify: `internal/control/client.go:100-145`

- [ ] **Step 1: Write failing IPv6 address tests**

Create:

```go
func TestControlURLAcceptsGlobalIPv6(t *testing.T) {
    tests := map[string]string{
        "2001:db8::1": "http://[2001:db8::1]:8080",
        " [2001:db8:0:0::2] ": "http://[2001:db8::2]:8080",
    }
    for input, want := range tests {
        got, err := ControlURL(input)
        if err != nil { t.Fatalf("ControlURL(%q): %v", input, err) }
        if got != want { t.Fatalf("ControlURL(%q) = %q, want %q", input, got, want) }
    }
}

func TestControlURLRejectsNonPublicOrStructuredInput(t *testing.T) {
    for _, input := range []string{
        "", "192.0.2.1", "::", "::1", "fe80::1", "fc00::1",
        "::ffff:192.0.2.1", "ff02::1", "fe80::1%12",
        "http://[2001:db8::1]:8080", "[2001:db8::1]:9000", "2001:db8::1/path",
    } {
        if _, err := ControlURL(input); !errors.Is(err, ErrInvalidHostIPv6) {
            t.Fatalf("ControlURL(%q) error = %v", input, err)
        }
    }
}
```

- [ ] **Step 2: Verify address tests fail**

Run:

```text
go test ./internal/room -count=1 -v
```

Expected: compile failure because `ControlURL` does not exist.

- [ ] **Step 3: Implement canonical URL construction**

```go
package room

import (
    "errors"
    "fmt"
    "net/netip"
    "strings"
)

var ErrInvalidHostIPv6 = errors.New("invalid host IPv6 address")

func ControlURL(input string) (string, error) {
    value := strings.TrimSpace(input)
    if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
        value = strings.TrimSuffix(strings.TrimPrefix(value, "["), "]")
    }
    if value == "" || strings.ContainsAny(value, "/%") || strings.Contains(value, "://") {
        return "", ErrInvalidHostIPv6
    }
    address, err := netip.ParseAddr(value)
    if err != nil || !address.Is6() || address.Is4In6() ||
        !address.IsGlobalUnicast() || address.IsPrivate() ||
        address.IsLoopback() || address.IsLinkLocalUnicast() ||
        address.IsMulticast() || address.IsUnspecified() {
        return "", ErrInvalidHostIPv6
    }
    return fmt.Sprintf("http://[%s]:8080", address.String()), nil
}
```

- [ ] **Step 4: Run address tests and verify GREEN**

Run:

```text
go test ./internal/room -count=1 -v
```

Expected: PASS.

- [ ] **Step 5: Write failing control-client tests**

In `room_client_test.go`, use `httptest.Server` to assert:

- `CreateRoom` sends POST `/v1/room`, bearer authentication, `name`, and `ipv4_pool`;
- `JoinRoom` sends POST `/v1/room/join` without Authorization;
- join request contains exactly `public_key`, `display_name`, `platform`, and `client_version`;
- the returned `EnrollmentResult` is complete;
- a response containing `invite_id` is rejected by strict decoding.

Use these public types:

```go
type RoomJoinRequest struct {
    DisplayName string
    PublicKey string
    Platform string
    ClientVersion string
}

func (client *Client) CreateRoom(ctx context.Context, name, pool, token string) (Network, error)
func (client *Client) JoinRoom(ctx context.Context, request RoomJoinRequest) (EnrollmentResult, error)
```

- [ ] **Step 6: Verify room-client tests fail**

Run:

```text
go test ./internal/control -run 'TestClient(CreateRoom|JoinRoom)' -count=1 -v
```

Expected: compile failure because the methods do not exist.

- [ ] **Step 7: Implement room client and shared enrollment decoding**

Move the validation and conversion of `enrollmentWireResponse` into:

```go
func enrollmentResultFromWire(response enrollmentWireResponse) (EnrollmentResult, error)
```

Use it from both legacy `Join` and new `JoinRoom`. `JoinRoom` sends no token. `CreateRoom` sends the supplied token and validates the returned network.

- [ ] **Step 8: Run tests and commit**

```text
go test ./internal/room ./internal/control -count=1
git diff --check
git add internal/room internal/control/room_client.go internal/control/room_client_test.go internal/control/client.go
git commit -m "feat: add room addressing client"
```

## Task 5: Add room join to IPC and the privileged service

**Files:**

- Modify: `internal/ipc/protocol.go:29-150,269-290`
- Modify: `internal/ipc/protocol_test.go`
- Modify: `internal/service/service.go:24-80,169-242`
- Modify: `internal/service/service_test.go`
- Modify: `internal/service/control_client.go:20-60`
- Modify: `internal/service/control_client_test.go`
- Modify: `cmd/vpn-service/main_windows.go:55-85`
- Modify: `cmd/vpn-service/service_config_windows_test.go`

- [ ] **Step 1: Write failing IPC round-trip tests**

Add:

```go
func TestJoinRoomRequestRoundTrip(t *testing.T) {
    request := Request{
        Type: CommandJoinRoom,
        ControlURL: "http://[2001:db8::1]:8080",
        DisplayName: "MEMBER-PC",
    }
    encoded, err := MarshalRequest(request)
    if err != nil { t.Fatal(err) }
    decoded, err := DecodeRequest(encoded)
    if err != nil { t.Fatal(err) }
    if decoded != request { t.Fatalf("decoded = %#v, want %#v", decoded, request) }
}

func TestJoinRoomRejectsMissingAndUnknownFields(t *testing.T) {
    for _, value := range []string{
        `{"type":"join_room","display_name":"MEMBER-PC"}`,
        `{"type":"join_room","control_url":"http://[2001:db8::1]:8080"}`,
        `{"type":"join_room","control_url":"http://[2001:db8::1]:8080","display_name":"MEMBER-PC","invite":"secret"}`,
    } {
        if _, err := DecodeRequest([]byte(value)); !errors.Is(err, ErrInvalidRequest) {
            t.Fatalf("DecodeRequest(%s) error = %v", value, err)
        }
    }
}
```

- [ ] **Step 2: Verify IPC tests fail**

Run:

```text
go test ./internal/ipc -run TestJoinRoom -count=1 -v
```

Expected: compile failure because `CommandJoinRoom` and `ControlURL` do not exist.

- [ ] **Step 3: Implement strict IPC fields**

Add `CommandJoinRoom Command = "join_room"` and `ControlURL string` to `Request`. Marshal and decode only `control_url` and `display_name` for this command. Validation requires both non-empty, rejects all unrelated fields, and leaves legacy commands unchanged.

- [ ] **Step 4: Write failing service room-join test**

Extend `fakeControlClient` with `roomJoinCalls`, `roomJoinResult`, and:

```go
func (client *fakeControlClient) JoinRoom(context.Context, JoinRequest) (JoinResult, error) {
    client.roomJoinCalls++
    return client.roomJoinResult, client.joinErr
}
```

Add:

```go
func TestServiceJoinsRoomWithoutInvite(t *testing.T) {
    controlClient := &fakeControlClient{
        roomJoinResult: JoinResult{NetworkID: "room-1", VirtualIPv4: "10.42.0.9", ConfigGeneration: 2},
    }
    service := New(Options{
        Identity: &fakeIdentityStore{identity: identity.Identity{PublicKey: "public-key"}},
        Control: controlClient,
        ControlURL: "http://[2001:db8::1]:8080",
        Adapter: &fakeAdapter{},
    })
    if err := service.Start(context.Background()); err != nil { t.Fatal(err) }
    response := service.Handle(context.Background(), ipc.Request{
        Type: ipc.CommandJoinRoom,
        ControlURL: "http://[2001:db8::1]:8080",
        DisplayName: "MEMBER-PC",
    })
    if !response.OK || response.NetworkID != "room-1" || controlClient.roomJoinCalls != 1 {
        t.Fatalf("room join response=%#v calls=%d", response, controlClient.roomJoinCalls)
    }
}
```

Also test that a request URL different from `Options.ControlURL` returns `invalid_request` before the control client is called, and that reconciliation failure invokes `Leave` and leaves status unjoined.

- [ ] **Step 5: Verify service tests fail**

Run:

```text
go test ./internal/service -run 'TestServiceJoinsRoom' -count=1 -v
```

Expected: compile failure because service room joining is not defined.

- [ ] **Step 6: Extend service interfaces and dispatch**

Add:

```go
type RoomControlClient interface {
    JoinRoom(context.Context, JoinRequest) (JoinResult, error)
}

type Options struct {
    Identity IdentityStore
    Control ControlClient
    ControlURL string
    Adapter Adapter
    Reconciler SnapshotApplier
}
```

In `Handle`, dispatch `ipc.CommandJoinRoom` to `joinRoom`. `joinRoom` must:

- reject an already joined service;
- compare canonical configured and requested control URLs;
- require `service.options.Control` to implement `RoomControlClient`;
- call it with display name and the identity public key;
- reuse a new private `finishJoin` helper shared with legacy `join` for snapshot reconciliation, rollback, and status assignment.

- [ ] **Step 7: Bridge `HTTPControlClient.JoinRoom`**

Implement:

```go
func (client *HTTPControlClient) JoinRoom(ctx context.Context, request JoinRequest) (JoinResult, error) {
    if client == nil || client.client == nil { return JoinResult{}, ErrControlClient }
    result, err := client.client.JoinRoom(ctx, control.RoomJoinRequest{
        DisplayName: request.DisplayName,
        PublicKey: request.PublicKey,
        Platform: client.platform,
        ClientVersion: client.clientVersion,
    })
    if err != nil { return JoinResult{}, err }
    return client.rememberEnrollment(result)
}
```

Extract `rememberEnrollment` from legacy `Join` so node ID, network ID, and session are recorded identically.

- [ ] **Step 8: Wire Windows service configuration**

Pass the already validated environment URL into service options:

```go
localService := service.New(service.Options{
    Identity: identityStore,
    Control: controlBridge,
    ControlURL: controlURL,
    Adapter: dataPlane,
    Reconciler: dataPlane.Applier,
})
```

Add a Windows configuration test that asserts a room request must match the service's configured URL.

- [ ] **Step 9: Run service, IPC, and Windows compile tests**

```text
go test ./internal/ipc ./internal/service -count=1
$env:GOOS='windows'; $env:GOARCH='amd64'; go test ./cmd/vpn-service -run TestNonExistent -count=1
Remove-Item Env:GOOS,Env:GOARCH
```

Expected: Go tests pass and Windows package compiles.

- [ ] **Step 10: Commit**

```text
git add internal/ipc internal/service cmd/vpn-service
git commit -m "feat: join rooms through node service"
```

## Task 6: Add room CLI commands

**Files:**

- Modify: `cmd/vpnctl/commands.go`
- Modify: `cmd/vpnctl/commands_test.go`
- Modify: `cmd/vpnctl/main.go`
- Modify: `cmd/vpnctl/output.go`

- [ ] **Step 1: Write failing parser tests**

Extend the command table with:

```go
{name: "room create", args: []string{"room", "create", "--name", "IPv6Mesh-HOST", "--pool", "10.42.0.0/24"}, kind: controlCommand},
{name: "room endpoint", args: []string{"room", "endpoint", "--host-ipv6", "2001:db8::1"}, kind: localCommand},
{name: "room join", args: []string{"room", "join", "--host-ipv6", "2001:db8::1", "--name", "MEMBER-PC"}, kind: serviceCommand, want: ipc.CommandJoinRoom},
```

Add rejection cases for missing IPv6, IPv4, link-local IPv6, explicit port, duplicate `--host-ipv6`, unknown flags, and missing name on `room join`.

- [ ] **Step 2: Verify parser tests fail**

```text
go test ./cmd/vpnctl -run 'TestParseCommand' -count=1 -v
```

Expected: failures for unknown `room` command and undefined `localCommand`.

- [ ] **Step 3: Implement command parsing**

Add:

```go
const localCommand commandKind = "local"

type command struct {
    Kind commandKind
    Service ipc.Request
    NetworkName string
    Pool string
    NetworkID string
    Expires string
    ControlURL string
    RoomCreate bool
    RoomEndpoint bool
}
```

Parse:

- `room create --name --pool`: `controlCommand`, `RoomCreate: true`;
- `room endpoint --host-ipv6`: call `room.ControlURL`, `localCommand`, `RoomEndpoint: true`;
- `room join --host-ipv6 --name`: call `room.ControlURL`, create `ipc.CommandJoinRoom`.

- [ ] **Step 4: Write failing execution tests**

Add:

```go
func TestRunRoomEndpointPrintsOnlyControlURL(t *testing.T) {
    parsed, err := parseCommand([]string{"room", "endpoint", "--host-ipv6", "2001:db8::1"})
    if err != nil { t.Fatal(err) }
    var output bytes.Buffer
    if err := runLocalCommand(parsed, &output); err != nil { t.Fatal(err) }
    if got := output.String(); got != "{\"control_url\":\"http://[2001:db8::1]:8080\"}\n" {
        t.Fatalf("output = %q", got)
    }
}

func TestRunRoomCreateCallsRoomEndpoint(t *testing.T) {
    admin := &fakeAdminClient{}
    parsed, _ := parseCommand([]string{"room", "create", "--name", "IPv6Mesh-HOST", "--pool", "10.42.0.0/24"})
    if err := runControlCommand(context.Background(), parsed, io.Discard, admin); err != nil { t.Fatal(err) }
    if admin.roomCreates != 1 { t.Fatalf("room creates = %d", admin.roomCreates) }
}
```

Extend `controlAdminClient` and the fake with `CreateRoom`.

- [ ] **Step 5: Implement execution and secret-safe output**

- `runLocalCommand` JSON-encodes only `control_url`.
- `runControlCommand` calls `CreateRoom` when `RoomCreate` is true.
- `main` handles local commands without constructing an HTTP or IPC client.
- Room join remains a Named Pipe service command.
- No output type contains administrator or invite tokens.

- [ ] **Step 6: Run CLI tests and commit**

```text
go test ./cmd/vpnctl -count=1
git diff --check
git add cmd/vpnctl
git commit -m "feat: add room workflow commands"
```

## Task 7: Replace the Windows three-role UI with separate pages

**Files:**

- Modify: `packaging/windows/ui.ps1`
- Modify: `cmd/ipv6mesh-installer/main_windows_test.go:120-205`

- [ ] **Step 1: Replace old string-presence tests with failing page-flow tests**

Update `TestWindowsPackageIncludesChineseUI` to require:

```go
for _, required := range []string{
    "你想做什么？",
    "创建网络",
    "加入网络",
    "Show-WelcomePage",
    "Show-HostPage",
    "Show-MemberPage",
    "Start-HostRoom",
    "Join-MemberRoom",
    "重新检测",
    "复制房主 IPv6",
    "房主虚拟 IPv4",
    "本机虚拟 IPv4",
    "Set-PrimaryBusy",
    "Stop-AllResources",
} {
    if !strings.Contains(contents, required) {
        t.Fatalf("UI script missing %q", required)
    }
}
```

Add forbidden strings:

```go
for _, forbidden := range []string{
    "控制面管理员",
    "游戏房主",
    "游戏成员",
    "管理员令牌：",
    "房主邀请：",
    "成员邀请：",
    "复制 Network ID",
    "随机生成房主邀请",
    "随机生成成员邀请",
} {
    if strings.Contains(contents, forbidden) {
        t.Fatalf("normal room UI still exposes %q", forbidden)
    }
}
```

Keep BOM, packaging, current-payload preference, cleanup, and PowerShell compatibility assertions.

- [ ] **Step 2: Verify UI tests fail**

```text
go test ./cmd/ipv6mesh-installer -run 'TestWindows(PackageIncludesChineseUI|UIUses)' -count=1 -v
```

Expected: failures because the old role strings remain and new page functions are absent.

- [ ] **Step 3: Build the navigation shell**

Keep the UTF-8 BOM. Replace the role ComboBox and shared groups with three panels:

```powershell
function Show-Page {
    param([ValidateSet("Welcome", "Host", "Member")][string]$Name)
    $script:welcomePanel.Visible = ($Name -eq "Welcome")
    $script:hostPanel.Visible = ($Name -eq "Host")
    $script:memberPanel.Visible = ($Name -eq "Member")
}
function Show-WelcomePage { Show-Page "Welcome" }
function Show-HostPage { Show-Page "Host"; Refresh-LocalIPv6 }
function Show-MemberPage { Show-Page "Member" }
```

The welcome page contains only two primary buttons. Host and member pages each contain a Back action and a collapsed diagnostics/log group.

- [ ] **Step 4: Add busy-state behavior**

```powershell
function Set-PrimaryBusy {
    param([bool]$Busy, [string]$Status)
    $script:hostStartButton.Enabled = !$Busy
    $script:memberJoinButton.Enabled = !$Busy
    $script:backButtons | ForEach-Object { $_.Enabled = !$Busy }
    if (![string]::IsNullOrWhiteSpace($Status)) {
        Set-UiStatus $Status ([System.Drawing.Color]::DarkOrange)
    }
}
```

Wrap both primary workflows in `try/finally` and always call `Set-PrimaryBusy $false ""`.

- [ ] **Step 5: Update control-plane startup for internal room mode**

Remove administrator-token input and invite-generation functions from the normal UI. Generate one internal token when the host starts:

```powershell
$script:adminToken = New-RandomToken
$psi.EnvironmentVariables["CONTROL_BOOTSTRAP_TOKEN"] = $script:adminToken
$psi.EnvironmentVariables["CONTROL_ROOM_MODE"] = "true"
$psi.EnvironmentVariables["CONTROL_REPOSITORY_MODE"] = "memory"
```

`Get-ClientEnvironment` must read `$script:controlUrl` and `$script:adminToken` rather than removed text boxes and supply the same internal token to `vpnctl room create`. `Assert-ControlUrl` must validate `$script:controlUrl`; `Install-NodeService` must accept the resolved control URL as a parameter. `Add-UiLog`, dialogs, status labels, and exported logs must never include the token.

Remove the ULA/non-global fallback from `Get-DetectedIPv6Address`. It may return only preferred, non-`SkipAsSource`, `2000::/3` candidates. When none exists, the host page stays unready and shows the approved global-IPv6 error.

- [ ] **Step 6: Implement host orchestration**

`Start-HostRoom` executes exactly:

```powershell
Refresh-LocalIPv6
$hostIPv6 = Get-BoxText $script:ipv6AddressBox
$endpoint = Convert-ResultToJson (Invoke-VpnCtl -Arguments @("room", "endpoint", "--host-ipv6", $hostIPv6)) "验证房主 IPv6"
$script:controlUrl = [string]$endpoint.control_url
Start-ControlPlane
$roomName = "IPv6Mesh-$env:COMPUTERNAME"
$null = Convert-ResultToJson (Invoke-VpnCtl -Arguments @("room", "create", "--name", $roomName, "--pool", "10.42.0.0/24") -SuppressStandardOutput) "创建房间"
if (!(Install-NodeService -ControlUrl $script:controlUrl)) { throw "节点服务安装失败。" }
$joined = Convert-ResultToJson (Invoke-VpnCtl -Arguments @("room", "join", "--host-ipv6", $hostIPv6, "--name", $env:COMPUTERNAME) -SuppressStandardOutput) "房主加入房间"
$null = Convert-ResultToJson (Invoke-VpnCtl -Arguments @("connect", "--network", [string]$joined.network_id)) "连接虚拟网络"
```

On success, show only host IPv6, virtual IPv4, and path. On failure, call `Stop-NodeService` and `Stop-ControlPlane` only for resources this operation started.

- [ ] **Step 7: Implement member orchestration**

`Join-MemberRoom` executes:

```powershell
$hostIPv6 = Get-BoxText $script:memberHostIPv6Box
$endpoint = Convert-ResultToJson (Invoke-VpnCtl -Arguments @("room", "endpoint", "--host-ipv6", $hostIPv6)) "验证房主 IPv6"
$script:controlUrl = [string]$endpoint.control_url
if (!(Install-NodeService -ControlUrl $script:controlUrl)) { throw "节点服务安装失败。" }
$joined = Convert-ResultToJson (Invoke-VpnCtl -Arguments @("room", "join", "--host-ipv6", $hostIPv6, "--name", $env:COMPUTERNAME) -SuppressStandardOutput) "加入房间"
$null = Convert-ResultToJson (Invoke-VpnCtl -Arguments @("connect", "--network", [string]$joined.network_id)) "连接虚拟网络"
```

The member page's only editable required field is `$script:memberHostIPv6Box`. Preserve the input after reachability or firewall failures.

Replace `Get-NodeStatus` logging so it never prints `network_id`; log and display only virtual IPv4, path, and the stable error code. The internal `network_id` may remain in the parsed object solely for subsequent connect/disconnect IPC calls.

- [ ] **Step 8: Add actionable named-error mapping**

Map `HTTPError.Code` text from CLI stderr:

- `room_not_ready`: 房主尚未完成创建网络；
- `room_mode_disabled`: 目标不是房间模式控制面；
- `node_already_joined`: 本机已经加入当前房间；
- `room_full`: 房间地址池已满；
- `join_rate_limited`: 加入过于频繁，请一分钟后重试；
- `enrollment_recovery_pending`: 加入结果待恢复，请稍后刷新状态。

Do not include raw bearer or invite data in any message.

- [ ] **Step 9: Run UI and parser verification**

```powershell
$tokens = $null
$parseErrors = $null
[void][System.Management.Automation.Language.Parser]::ParseFile(
  (Resolve-Path 'packaging/windows/ui.ps1'),
  [ref]$tokens,
  [ref]$parseErrors
)
if ($parseErrors.Count -gt 0) { $parseErrors | Format-List | Out-String | Write-Error; exit 1 }
go test ./cmd/ipv6mesh-installer -count=1
```

Expected: parser returns without errors and tests pass.

- [ ] **Step 10: Commit**

```text
git add packaging/windows/ui.ps1 cmd/ipv6mesh-installer/main_windows_test.go
git commit -m "feat: add create-or-join room UI"
```

## Task 8: Add vertical integration coverage and update user documentation

**Files:**

- Create: `internal/control/room_integration_test.go`
- Modify: `README.md`
- Modify: `docs/operator.md`

- [ ] **Step 1: Write the failing two-node integration test**

The file must use external package `control_test` to avoid an import cycle while importing `internal/db`. The test must use `httptest.NewServer`, `db.NewMemoryRepository`, the real `control.Handler`, and real `control.Client` methods:

```go
func TestRoomLifecycleEnrollsHostAndMemberWithoutVisibleInvites(t *testing.T) {
    repository := db.NewMemoryRepository()
    handler := control.NewHandler(repository, control.HandlerOptions{
        BootstrapToken: "internal-bootstrap",
        RoomMode: true,
        NewID: sequentialIDs("room-1", "invite-host", "node-host", "invite-member", "node-member"),
        TokenRandom: bytes.NewReader(bytes.Repeat([]byte{0x41}, 128)),
    })
    server := httptest.NewServer(handler)
    defer server.Close()

    client, err := control.NewClient(server.URL)
    if err != nil { t.Fatal(err) }
    roomNetwork, err := client.CreateRoom(context.Background(), "IPv6Mesh-HOST", "10.42.0.0/24", "internal-bootstrap")
    if err != nil { t.Fatal(err) }

    host, err := client.JoinRoom(context.Background(), control.RoomJoinRequest{
        DisplayName: "HOST", PublicKey: "host-public", Platform: "windows", ClientVersion: "0.1.0",
    })
    if err != nil { t.Fatal(err) }
    member, err := client.JoinRoom(context.Background(), control.RoomJoinRequest{
        DisplayName: "MEMBER", PublicKey: "member-public", Platform: "windows", ClientVersion: "0.1.0",
    })
    if err != nil { t.Fatal(err) }
    if host.Network.ID != roomNetwork.ID || member.Network.ID != roomNetwork.ID {
        t.Fatalf("networks: host=%q member=%q room=%q", host.Network.ID, member.Network.ID, roomNetwork.ID)
    }
    if host.Membership.VirtualIPv4.Equal(member.Membership.VirtualIPv4) {
        t.Fatalf("duplicate virtual IPv4: %s", host.Membership.VirtualIPv4)
    }
    hostSnapshot, err := client.Snapshot(context.Background(), roomNetwork.ID, host.SessionToken)
    if err != nil { t.Fatal(err) }
    memberSnapshot, err := client.Snapshot(context.Background(), roomNetwork.ID, member.SessionToken)
    if err != nil { t.Fatal(err) }
    if len(hostSnapshot.Peers) != 1 || len(memberSnapshot.Peers) != 1 {
        t.Fatalf("peer counts: host=%d member=%d", len(hostSnapshot.Peers), len(memberSnapshot.Peers))
    }
}
```

Define the deterministic ID helper in the same external test package:

```go
func sequentialIDs(values ...string) func() string {
    index := 0
    return func() string {
        if index >= len(values) {
            panic("sequential ID source exhausted")
        }
        value := values[index]
        index++
        return value
    }
}
```

Add a second test that creates a new handler/repository after closing the first server and asserts `POST /v1/room/join` returns `room_not_ready`.

- [ ] **Step 2: Run integration tests and fix only integration defects**

```text
go test ./internal/control -run 'TestRoomLifecycle' -count=1 -v
```

Expected: PASS after the preceding tasks. If it fails, fix production code with a new focused failing regression test before changing implementation.

- [ ] **Step 3: Rewrite the primary README workflow**

The first Windows user section must state:

1. Both users open the installer.
2. Host selects **创建网络**, waits for ready, and copies the detected host IPv6.
3. Member selects **加入网络**, enters that IPv6, and clicks join.
4. Both pages show assigned virtual IPv4 addresses and current path.
5. Closing the host ends the room; reopening creates a new room.
6. No administrator token, Network ID, or invite token is required in the normal UI.
7. Knowing the host IPv6 grants join access while the room is open.
8. TCP 8080 and WireGuard UDP 51820 must be reachable.

Move legacy administrator and invitation commands into a clearly labeled developer compatibility section.

- [ ] **Step 4: Update operator documentation**

Document:

- `CONTROL_ROOM_MODE=true`;
- `vpnctl room endpoint --host-ipv6 <ipv6>`;
- `vpnctl room create --name <name> --pool 10.42.0.0/24`;
- `vpnctl room join --host-ipv6 <ipv6> --name <device>`;
- room-mode open-enrollment and non-persistence boundaries;
- legacy invite workflow remains supported outside room mode.

- [ ] **Step 5: Run documentation and secret scans**

```powershell
rg -n "管理员令牌：|房主邀请：|成员邀请：|复制 Network ID" README.md packaging/windows/ui.ps1
rg -n "CONTROL_BOOTSTRAP_TOKEN=.*[^<]|session-token|invite-token" README.md docs/operator.md packaging/windows/ui.ps1
git diff --check
```

Expected: the first command has no matches in the normal UI; examples use explicit placeholders rather than real credentials.

- [ ] **Step 6: Commit integration and docs**

```text
git add internal/control/room_integration_test.go README.md docs/operator.md
git commit -m "test: cover host-bound room workflow"
```

## Task 9: Run full verification and prepare the review handoff

**Files:**

- Modify only files required by a new failing regression test discovered during verification.

- [ ] **Step 1: Format and verify no formatting drift**

```powershell
& gofmt -w (rg --files -g '*.go')
$unformatted = & gofmt -l .
if ($unformatted) { throw "gofmt drift: $unformatted" }
git diff --check
```

Expected: no unformatted files and no whitespace errors.

- [ ] **Step 2: Run the complete Go suite**

```text
go test -count=1 ./...
go vet ./...
```

Expected: both commands exit 0 with no failures.

- [ ] **Step 3: Run repeated concurrency-sensitive room tests**

```text
go test ./internal/control ./internal/db -run 'Room|Invite|Enroll' -count=20
```

Expected: all 20 runs pass.

- [ ] **Step 4: Parse every PowerShell package script**

```powershell
$parseFailures = @()
Get-ChildItem 'packaging/windows' -Filter '*.ps1' | ForEach-Object {
    $tokens = $null
    $errors = $null
    [void][System.Management.Automation.Language.Parser]::ParseFile($_.FullName, [ref]$tokens, [ref]$errors)
    if ($errors.Count -gt 0) { $parseFailures += $errors }
}
if ($parseFailures.Count -gt 0) { $parseFailures | Format-List | Out-String | Write-Error; exit 1 }
```

Expected: exit 0 and no parse errors.

- [ ] **Step 5: Cross-compile all Windows packages**

```powershell
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'
go test -run '^$' ./...
$code = $LASTEXITCODE
Remove-Item Env:GOOS,Env:GOARCH
if ($code -ne 0) { exit $code }
```

Expected: exit 0.

- [ ] **Step 6: Build the Windows installer payload**

Use the repository's installer builder with the already downloaded official WireGuardNT 1.1 amd64 DLL and license:

```powershell
$wireGuardDll = 'C:\Users\Eser\Documents\Codex\2026-08-11\en\work\wireguard-nt-1.1\wireguard-nt\bin\amd64\wireguard.dll'
$wireGuardLicense = 'C:\Users\Eser\Documents\Codex\2026-08-11\en\work\wireguard-nt-1.1\wireguard-nt\LICENSE.txt'
$wireGuardArchive = 'C:\Users\Eser\Documents\Codex\2026-08-11\en\work\wireguard-nt-1.1.zip'
if ((Get-FileHash -LiteralPath $wireGuardArchive -Algorithm SHA256).Hash -ne 'DCEB30A9BC4BE48CCE0F74160FC88A585A2C2627366E8F846FC6658F9038DACE') {
    throw 'WireGuardNT SDK archive hash does not match packaging/windows/wireguardnt-manifest.json'
}
if ((Get-FileHash -LiteralPath $wireGuardDll -Algorithm SHA256).Hash -ne 'B1B85E072C45D81358BE29D94C599DC76652F912BE8C0F0A41E2D5D89A6461D3') {
    throw 'Extracted amd64 wireguard.dll hash changed'
}
& .\packaging\windows\build-installer.ps1 -WireGuardDll $wireGuardDll -WireGuardLicense $wireGuardLicense -Version '0.1.0-dev'
```

Expected: `packaging/windows/dist/ipv6mesh-installer.exe` and its SHA-256 file are produced. The embedded payload contains `vpn-service.exe`, `vpnctl.exe`, `control-server.exe`, UI/install scripts, the verified amd64 `wireguard.dll`, and its license. Do not add generated files to Git.

- [ ] **Step 7: Review requirements and secrets**

Verify each approved requirement against the diff:

- host page owns control-plane startup and network creation;
- host global IPv6 is detected automatically;
- host receives an automatic virtual IPv4;
- member enters only host IPv6;
- member invite is internal and never shown;
- member receives an automatic virtual IPv4;
- welcome page branches to separate host/member pages;
- no room recovery after host shutdown;
- no host approval;
- legacy developer APIs remain compatible.

Run:

```powershell
git grep -n -I -E "(BEGIN (RSA|OPENSSH|PRIVATE) KEY|Bearer [A-Za-z0-9_-]{20,}|[A-Za-z0-9_-]+\.[A-Za-z0-9_-]{32,})" -- ':!go.sum'
git status --short
```

Expected: no credential findings; only intentional tracked source changes are present.

- [ ] **Step 8: Report to the planner**

Send:

- branch name and final commit;
- concise per-task summary;
- full command outputs and exit codes for Steps 1–7;
- list of files changed;
- explicit statement that no real two-computer public-IPv6 test was performed unless it actually was;
- any remaining blocker.

Do not merge or push. The planner performs the independent final review and GitHub push.
