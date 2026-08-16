# Host-Bound Room Flow Design

**Date:** 2026-08-16
**Status:** Approved interactively; pending written-spec review
**Target branch:** `main`

## Goal

Replace the current three-role Windows UI with a two-path room workflow:

1. A host selects **Create Network**. The application detects the host's global IPv6 address, starts an ephemeral control plane, creates one active network, enrolls the host, allocates the host's virtual IPv4 address, and connects the node.
2. A member selects **Join Network**, enters only the host's IPv6 address, and clicks **Join Network**. The control plane creates and consumes the required one-time invite internally, allocates the member's virtual IPv4 address, and connects the node.

The room is intentionally ephemeral. Closing the host application ends the room. A later application start creates a new room; it does not restore the previous network or memberships.

## Product Decisions

- The separate administrator role and host role become one host workflow.
- The initial screen contains only **Create Network** and **Join Network**.
- Host and member settings are shown on separate pages.
- Members do not enter an administrator token, Network ID, invite token, device name, scheme, or port.
- The member device name defaults to the Windows computer name.
- The member enters a raw global IPv6 address. The client constructs `http://[<ipv6>]:8080`.
- Port `8080` is fixed for this room workflow.
- The host page uses the detected global IPv6 address, with a **Detect Again** action when detection is wrong or stale.
- The network name is generated as `IPv6Mesh-<computer-name>`.
- The virtual IPv4 pool is `10.42.0.0/24`.
- Network IDs remain cryptographically random internal identifiers and are not shown in the normal UI.
- Joining requires no host approval. Anyone who knows the reachable host IPv6 address while the room is open can join.
- The host application uses an in-memory control repository. No room state is restored after shutdown.

## Existing System Context

The current Windows installer embeds `packaging/windows/ui.ps1`. That UI shows control-plane administrator, game host, and game member controls in one large form. It exposes administrator-token, Network ID, host-invite, and member-invite operations that the new normal flow no longer needs.

Existing control-plane enrollment already provides one-time invitation hashing, transactional invite consumption, virtual IPv4 allocation, node sessions, versioned snapshots, and rollback behavior. The room workflow must reuse these boundaries rather than inventing a second membership model.

Existing CLI, service IPC, WireGuardNT, route reconciliation, endpoint reporting, and snapshot application remain the data-plane foundation. This feature changes orchestration and UI flow, not WireGuard cryptography or overlay routing.

## Architecture

### Room mode

The control server gains an explicit room mode, enabled only for the host-bound UI process. Normal deployments keep the existing administrator/network/invite APIs and do not expose open room enrollment by default.

Room mode has exactly one active network:

- Before room creation, public room join returns `404 room_not_ready`.
- Creating the first room sets its network as active.
- Creating another room in the same process returns `409 room_already_exists`.
- Stopping the control-server process destroys the in-memory room state.

The UI starts the room-mode control server with:

- listen address `[::]:8080`;
- an in-process cryptographically random bootstrap administrator token;
- the memory repository;
- room mode enabled.

The token is held only in process memory and the child-process environment needed for existing administrative calls. It is never displayed, copied, logged, or written to disk.

### Host workflow

The host action is a single orchestrated operation:

1. Validate that the detected address is usable global unicast IPv6.
2. Install or update the packaged node service.
3. Start the room-mode control server and wait for `/healthz`.
4. Create the active network with the generated name and `10.42.0.0/24`.
5. Ask the local node service to join the active room at the detected IPv6 address.
6. The public room-join endpoint internally creates and consumes a one-time invite and allocates the host membership.
7. Connect the local node and wait for status to contain the network ID and virtual IPv4 address.
8. Show the shareable host IPv6 address and host virtual IPv4 address.

The host's network ID and internal invite are not shown in the normal UI.

### Member workflow

The member action is:

1. Trim optional surrounding whitespace and brackets from the entered IPv6 literal.
2. Reject IPv4, unspecified, loopback, multicast, link-local, IPv4-mapped IPv6, zone-qualified, scheme-containing, path-containing, and port-containing input.
3. Construct `http://[<ipv6>]:8080`.
4. Install or update the packaged node service.
5. Send a room-join IPC command containing the control URL and automatic device name.
6. The service submits its node public key and metadata to the public room-join endpoint.
7. Apply the returned session and snapshot, connect the adapter, and display the member virtual IPv4, host virtual IPv4, and current path.

The only required member field is the host IPv6 address.

## Control-Plane API

### Create the active room

`POST /v1/room`

- Authentication: existing bootstrap administrator bearer token.
- Availability: room mode only.
- Request:

```json
{
  "name": "IPv6Mesh-HOSTNAME",
  "ipv4_pool": "10.42.0.0/24"
}
```

- Response: the existing public `Network` representation.
- Errors:
  - `401 unauthorized` for a missing or incorrect bootstrap token;
  - `404 room_mode_disabled` outside room mode;
  - `409 room_already_exists` when an active room already exists;
  - `422 invalid_room` for invalid name or pool.

### Join the active room

`POST /v1/room/join`

- Authentication: intentionally unauthenticated while the ephemeral room is active.
- Availability: room mode only.
- Maximum request body: 64 KiB.
- Request:

```json
{
  "public_key": "<wireguard-public-key>",
  "display_name": "MEMBER-PC",
  "platform": "windows",
  "client_version": "0.1.0"
}
```

- Success response uses the existing enrollment response shape required by the control client, including node session, network ID, assigned virtual IPv4, and generation. It never includes the internally generated invite ID, secret, or token.
- The handler creates a cryptographically random one-time invite for the active network, enrolls the submitted node through the existing enrollment service, and ensures the invite is consumed.
- If enrollment fails before membership commit, the generated invite is revoked or removed.
- Existing uncertain-commit recovery behavior remains authoritative after the membership transaction starts.
- Stable errors:
  - `404 room_not_ready` when the host has not completed room creation;
  - `404 room_mode_disabled` outside room mode;
  - `409 node_already_joined` for a duplicate public key;
  - `409 room_full` when the IPv4 pool is exhausted;
  - `413 request_too_large`;
  - `422 invalid_node`;
  - `429 join_rate_limited`;
  - `503 enrollment_recovery_pending` for an uncertain commit.

### Abuse limits

The public join endpoint uses a bounded in-memory fixed-window limiter:

- at most 10 join attempts per source IP per minute;
- at most 100 total join attempts per minute;
- limiter entries expire after two minutes;
- forwarded-address headers are ignored; the TCP peer address is authoritative.

The limiter resets with the ephemeral control process.

## Component Boundaries

### `internal/control`

- Add room-mode configuration and a small active-room coordinator.
- Add authenticated room creation and public room join handlers.
- Reuse the existing repository, invitation hashing, enrollment service, request IDs, strict JSON decoding, error envelopes, and cache-control rules.
- Keep normal multi-network APIs unchanged when room mode is disabled.

### `internal/enrollment`

- Add an orchestration entry point for internally generated room invitations.
- Keep invite creation, consumption, address allocation, session issuance, rollback, and uncertain-commit recovery testable independently.
- Never return the generated room invite outside the server boundary.

### `internal/ipc` and `internal/service`

- Add a `join_room` request carrying only `control_url` and `display_name`.
- The service owns identity access and sends its public key to the control plane.
- Private key material remains local and never enters IPC or HTTP JSON.
- Reuse existing state persistence and snapshot application only for the lifetime of the current room.

### `cmd/vpnctl`

- Add `room create` for the host orchestration's authenticated room creation.
- Add `room join --host-ipv6 <address> [--name <device>]`; `--name` is for developer use, while the UI supplies the computer name automatically.
- Centralize raw IPv6 validation and URL construction in Go so the CLI, service, and tests share one implementation.

### Windows UI

- Continue using the packaged PowerShell WinForms UI for this increment.
- Replace the three-role combo box and shared administrator/node form with a navigation container that shows one page at a time.
- Keep diagnostics and logs in a collapsible secondary area.
- Preserve token redaction and add explicit checks that normal labels, logs, and dialogs never reveal bootstrap or invite tokens.

## UI States

### Welcome page

- Title: `你想做什么？`
- Primary cards:
  - `创建网络`
  - `加入网络`

### Create Network page

- Detected host IPv6, read-only.
- `重新检测` action.
- Generated network name and fixed IPv4 pool shown as non-required informational values.
- Primary action: `创建并启动网络`.
- Progress states:
  - detecting IPv6;
  - installing service;
  - starting control plane;
  - creating room;
  - assigning host IPv4;
  - connecting.
- Ready state shows:
  - shareable host IPv6 with copy action;
  - host virtual IPv4;
  - current path;
  - stop-room action.

### Join Network page

- One required input: `房主 IPv6`.
- Primary action: `加入网络`.
- Progress states:
  - validating address;
  - installing service;
  - contacting host;
  - assigning member IPv4;
  - connecting.
- Connected state shows:
  - member virtual IPv4;
  - host virtual IPv4;
  - current path;
  - disconnect and return actions.

All primary actions enter a busy state and disable repeat submission until completion or failure.

## Error Handling and Cleanup

- No usable global IPv6: remain on the host page and explain that a reachable global IPv6 address is required.
- Port `8080` already occupied: do not attach to an unknown control process; report the conflict and leave existing processes untouched.
- Control process fails health check: stop only the process started by this operation.
- Room creation fails: stop the newly started control process and do not join the node.
- Host or member enrollment fails: revoke the internally generated unused invite and keep the node unjoined.
- Adapter or route application fails: use existing rollback and owned-resource cleanup; never delete unrelated adapters, addresses, or routes.
- Member cannot reach host: keep the entered IPv6 and display firewall/reachability guidance.
- Duplicate node: explain that this device is already in the active room; do not allocate another address.
- UI close on the host: stop the local node service, clean owned adapter/address/routes, and stop the UI-owned control process.
- UI close on a member: stop the local node connection and clean owned network resources.

Logs may contain request IDs, stages, stable error codes, and non-secret addressing information. Logs must not contain bootstrap tokens, internal invite tokens, bearer sessions, private keys, or full HTTP authorization headers.

## Testing Strategy

Implementation follows red-green-refactor. Every new behavior begins with a failing test that is observed to fail for the intended reason.

### Control-plane tests

- Room creation requires bootstrap authentication.
- Room endpoints are unavailable when room mode is disabled.
- Only one active room can be created.
- Join before room creation returns `room_not_ready`.
- Room join internally creates and consumes a unique invite.
- Join responses and error bodies never contain invite material.
- Concurrent joins receive unique virtual IPv4 addresses.
- Duplicate public keys, invalid metadata, exhaustion, oversized bodies, per-IP limiting, and global limiting return the specified stable errors.
- Failed enrollment cleans the internally generated unused invite.
- Existing uncertain-commit recovery tests continue to pass.

### Service and IPC tests

- `join_room` accepts a valid room URL and automatic display name.
- Identity public key is sent; private key is not serialized.
- Successful enrollment updates status and applies the first snapshot.
- Failure leaves the node unjoined and does not retain a partial network state.

### IPv6 and CLI tests

- Accept canonical and compressed global IPv6 literals with optional surrounding brackets and whitespace.
- Construct exactly `http://[<canonical-ipv6>]:8080`.
- Reject IPv4, mapped IPv4, unspecified, loopback, multicast, link-local, zone identifiers, schemes, paths, and explicit ports.
- `room create` and `room join` reject missing, duplicate, and unknown flags before network or IPC calls.

### UI tests

- The welcome page exposes only Create Network and Join Network.
- Host and member controls are not visible at the same time.
- The member page has exactly one required input.
- Busy state prevents duplicate actions.
- Host orchestration runs stages in order and compensates in reverse order after injected failure.
- Normal UI text and captured logs contain no administrator token, invite token, Network ID, or bearer session.

### Integration test

Using the real HTTP handler, memory repository, enrollment service, and two generated node identities:

1. Start room mode.
2. Create the active room.
3. Join the host through the public room endpoint.
4. Join a member using only the host-address-derived control URL semantics.
5. Verify distinct virtual IPv4 addresses.
6. Verify each node's snapshot includes the other peer.
7. Stop the room server.
8. Start a fresh room-mode server and verify the old room is unavailable.

## Verification Gates

Before final review:

- `go test -count=1 ./...`
- `go vet ./...`
- `gofmt -l .` returns no files
- PowerShell parser validation for all `packaging/windows/*.ps1`
- Windows AMD64 compilation for every Go package
- Windows installer build succeeds with the packaged payload
- `git diff --check`
- requirement-by-requirement diff review
- secret-leak scan over UI strings, logs, fixtures, and test output

A real two-computer Windows test over reachable public IPv6 remains a hardware/network acceptance step. Automated integration tests and a single-host build do not justify claiming that live two-node connectivity has been proven.

## Non-Goals

- Persistent room recovery.
- Host approval or member allowlists.
- Passwords, room codes, or manual invite exchange.
- Custom control port in the normal UI.
- Multiple simultaneous rooms on one host control process.
- LAN broadcast discovery, subnet routing, exit nodes, or new Relay behavior.
- A WPF rewrite or visual redesign beyond the approved page separation.
- New cryptography or changes to WireGuard key handling.
