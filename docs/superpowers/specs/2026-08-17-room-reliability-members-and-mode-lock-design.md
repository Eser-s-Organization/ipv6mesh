# Room Reliability, Member List, and Mode Lock Design

## Goal

Make the host-bound Windows room flow reliable when the control plane is slow or unreachable, display the current room members without exposing credentials, and prevent create-network and join-network operations from conflicting.

The completed workflow must:

- check host reachability before changing the local service installation;
- allow a legitimate room join to take longer than five seconds without the local IPC client masking the control-plane result;
- wait for the Windows service's named pipe rather than treating Service Control Manager `Running` as application readiness;
- show every active room member's display name, virtual IPv4 address, and the fixed state `在线` on both operation pages;
- combine a persistent in-page member list with a right-side member column on wide windows;
- enforce one exclusive UI mode at a time and reject a second UI process;
- preserve the existing token, key, and raw-error secrecy boundaries.

This specification extends the host-bound room flow and responsive Windows UI specifications. It changes room reliability, local IPC, member presentation, and mode ownership only. The existing in-memory room lifetime, open enrollment by host IPv6, diagnostics splitter, and no-automatic-recovery decisions remain authoritative.

## Confirmed Product Decisions

- Use the privileged node service as the only authenticated source for the UI member list.
- Do not expose a public or unauthenticated control-plane member-list endpoint.
- Do not pass a session token, administrator token, invite, node public key, endpoint, or internal node ID to the PowerShell UI.
- A member is displayed as `在线` from successful enrollment until a later successful snapshot omits that membership, the local user explicitly leaves or ends the room, or the local UI shuts down. A remote host outage does not relabel or clear the last successful rows. There is no 30-second offline transition.
- Refresh the member list every two seconds while a create or join page owns an active room membership.
- On a transient member-list refresh failure, keep the last successful rows and show a deduplicated warning. Do not relabel members as offline.
- Use strict create/join mutual exclusion. Switching modes requires an explicit **结束房间** or **离开房间** action followed by successful cleanup.
- Do not automatically retry, restore a previous room, recover a mode after restart, or automatically clean and switch modes.
- Permit only one IPv6Mesh Windows UI process at a time.
- After verification, merge to `main`, push GitHub, and publish the next prerelease with refreshed Windows and macOS assets.

## Current Failures

### Timeout inversion

The Windows IPC client applies one five-second connection deadline. A room join can perform a control-plane enrollment request and a snapshot request, and each control-plane HTTP request may take up to fifteen seconds. The named-pipe client therefore closes before the service has enough time to return the real network result. The user sees `i/o timeout` instead of an actionable stable error.

### Service readiness race

`install.ps1` calls `Start-Service` and immediately reports installation success. The Windows service runner publishes `Running` after starting the service goroutine, before `ServeWindows` has necessarily created the named-pipe listener. Service Control Manager state is not proof that `vpnctl` can exchange a complete IPC message.

### Destructive preflight order

The member workflow installs or replaces the node service before proving that the host control plane responds. An unreachable host can therefore cause unnecessary service churn followed by cleanup.

### Missing UI member boundary

The authenticated control-plane snapshot already contains remote display names and virtual IPv4 addresses, while the service retains the session token. The local IPC response currently exposes only node status, so the UI cannot request a sanitized member view without violating the credential boundary.

### Conflicting mode entry

The UI disables primary buttons only during one operation. It does not model `Idle`, host, and member ownership explicitly, and it has no cross-process lock. A second window or navigation back to Welcome can attempt to reinstall, stop, or redirect the same machine-wide service while another flow owns it.

## Architecture

### Authenticated member data path

The member list uses this path:

1. `packaging/windows/ui.ps1` invokes `vpnctl room members` every two seconds only after a successful room join.
2. `vpnctl` sends a no-argument `room_members` request over the existing administrators-only named pipe.
3. The node service confirms that it is joined, uses its private in-memory session token to request the current authenticated network snapshot, and converts the snapshot into a sanitized list.
4. IPC returns only the network ID and member display fields.
5. The UI replaces the grid rows atomically after a successful response.

The session token never crosses the service boundary. The UI must not call the authenticated snapshot endpoint directly.

### IPC model

Add `CommandRoomMembers` with the wire name `room_members`. It accepts no arguments.

Extend the success response with an optional `members` array. Each item has exactly:

- `display_name`: the user-visible device name;
- `virtual_ipv4`: one allocated IPv4 address;
- `is_local`: whether the row represents the current node;
- `state`: always `online` for a returned active membership.

The response must not contain the session token, node ID, public key, control URL, observed endpoint, platform, client version, last-seen time, or raw control error. Strict request and response decoding continues to reject missing, duplicate, unknown, null, and oversized fields.

Rows are sorted deterministically: local node first, followed by case-insensitive display name, virtual IPv4, and a stable final comparison on the original display name. Equal display names are permitted.

### Service member projection

The service records the successful join display name with its joined state. For `room_members`:

- return `not_joined` when no room membership exists;
- request a fresh snapshot through the existing authenticated `SnapshotClient`;
- create the local row from the stored display name and joined virtual IPv4;
- create peer rows from `snapshot.Peers` using only `DisplayName` and `VirtualIPv4`;
- reject an empty display name, invalid IPv4, mismatched network ID, or malformed snapshot as `control_failed`;
- map a safe timeout to `operation_timeout` and a transport-level reachability failure to `control_unreachable`;
- never include the underlying error message in IPC.

The UI does not infer online state from `LastSeen`. The control-plane snapshot already excludes inactive or revoked memberships. An explicitly leaving member disappears when the next snapshot is returned.

## Timeout and Readiness Policy

### Host health preflight

Before `Install-NodeService` in the member workflow, request `<control-url>/healthz` with:

- no proxy;
- a five-second timeout;
- no authentication header;
- no automatic retry.

If the health check fails, show the safe message `房主控制面不可访问，请确认房主窗口仍在运行且 TCP 8080 可达。` and remain in member setup mode. Do not install, restart, or stop the Windows service. The host workflow retains its existing local control-plane readiness check before creating the room.

### Named-pipe readiness

After `Start-Service`, `install.ps1` waits up to fifteen seconds for a complete `vpnctl status` exchange. A valid success response proves readiness. A valid stable service response also proves the pipe is ready even if the service has no joined network. Process startup errors, malformed output, or no complete response by the deadline fail installation.

The readiness loop uses condition polling with a bounded interval; it does not sleep for one fixed startup delay. Installation output reports success only after readiness.

### End-to-end IPC deadlines

Use command-specific client budgets:

- local status, connect, and disconnect: five seconds;
- control-plane-backed join, room join, leave, and room members: forty-five seconds.

The named-pipe server connection deadline becomes sixty seconds so it cannot expire before a supported client command. Each handler receives a bounded per-connection context derived from that deadline. The forty-five-second client budget covers two sequential fifteen-second control-plane calls and bounded local reconciliation without permitting an unbounded UI hang.

Safe deadline mapping returns `operation_timeout`; a non-timeout transport failure returns `control_unreachable`. Known room HTTP error codes continue to pass through the existing allowlist. Unknown errors remain `control_failed`.

No retry loop is added to enrollment, snapshot, leave, or IPC commands.

## Exclusive UI Mode State Machine

### Cross-process ownership

At startup, `ui.ps1` acquires one named machine-wide mutex for the IPv6Mesh Windows UI. If another live UI owns it, the new process displays `IPv6Mesh 已在运行。请使用现有窗口。` and exits before detecting IPv6, starting timers, installing services, opening firewall rules, or starting the control plane.

An abandoned mutex can be acquired safely. The owning process releases and disposes the mutex during normal shutdown. The mutex is only a UI-process guard; the node service's IPC authorization remains unchanged.

### In-process states

Use an explicit state variable with these values:

- `Idle`: Welcome is available and either setup page may be opened.
- `HostSetup`: create page is open but does not yet own resources.
- `MemberSetup`: join page is open but does not yet own resources.
- `PreparingHost`: host creation is running; all navigation and primary actions are disabled.
- `PreparingMember`: member join is running; all navigation and primary actions are disabled.
- `Hosting`: the UI owns the control plane and joined host service.
- `JoinedMember`: the UI owns a joined member service.
- `Cleaning`: explicit end, leave, failure cleanup, or shutdown is in progress.

Allowed transitions are:

- `Idle -> HostSetup -> PreparingHost -> Hosting`;
- `Idle -> MemberSetup -> PreparingMember -> JoinedMember`;
- either setup state may return to `Idle` before preparation begins;
- a failed preparation performs bounded cleanup and returns to its setup state if the page remains open;
- `Hosting -> Cleaning -> Idle` only through **结束房间** or window shutdown;
- `JoinedMember -> Cleaning -> Idle` only through **离开房间** or window shutdown.

Every illegal transition is rejected without process, service, firewall, or network side effects. Repeated clicks while preparing or cleaning are no-ops with one deduplicated log entry.

### Host and member actions

In `Hosting`:

- the UI stays on the create page;
- create, join, Back, and member-mode navigation are disabled;
- **结束房间** leaves the host membership, stops only resources owned by this UI, clears the member list, and returns to Welcome.

In `JoinedMember`:

- the UI stays on the join page;
- join, create, Back, and host-mode navigation are disabled;
- **离开房间** leaves the membership, stops only the node service started by this UI, clears the member list, and returns to Welcome.

The UI never silently converts Hosting to JoinedMember or the reverse.

## Responsive Member List UI

### Shared member component

Create one reusable member-list component per operation page using a read-only `DataGridView` or an equivalent managed WinForms grid. It has:

- a header `房间成员（N）`;
- columns `名称`, `虚拟 IPv4`, and `状态`;
- no row editing, sorting interaction, add row, delete row, or clipboard exposure beyond normal visible text;
- full-row selection disabled unless needed for accessibility;
- automatic column sizing that guarantees the Chinese headers are visible;
- a placeholder `尚未加入房间` before enrollment;
- a safe refresh-status label that does not cover rows.

The local row may visually identify itself with `（本机）` appended to the display name. Its state still reads `在线`.

### Wide layout

When the operation viewport has sufficient logical width, use two columns inside the upper operation panel:

- left: the existing host or member setup/status controls;
- right: the persistent member list.

The right column has a bounded minimum width and receives a proportional share of extra space. Diagnostics remains exclusively in the lower split panel.

### Narrow and high-DPI layout

Below the deterministic logical-width breakpoint, or when preferred control widths cannot satisfy both columns, switch to one vertical column:

1. setup/status controls;
2. persistent member list;
3. the existing split boundary leading to diagnostics.

The upper operation panel remains scrollable. Layout changes reuse existing controls and rows; they do not recreate timers, invoke `vpnctl`, or append logs. Wide/narrow switching must be idempotent and must not reset the user's diagnostics splitter position.

The breakpoint decision uses client-space and preferred-size measurements after DPI scaling rather than raw screen pixels.

### Refresh lifecycle

The existing two-second operation-page timer owns both node status and member refresh:

- setup state without membership: refresh node status as today and keep the member placeholder;
- `Hosting` or `JoinedMember`: refresh node status, then request room members;
- Welcome, Cleaning, and shutdown: do not request members;
- successful leave or end: clear rows and the last member fingerprint;
- transient failure: retain rows, change only the refresh-status text, and log only on failure transition;
- recovery: replace rows and log one recovery message;
- changed membership fingerprint: replace rows and log the new count once.

Only display name, virtual IPv4, local flag, and online state participate in the UI fingerprint.

## Error Handling and Cleanup

- `control_unreachable` displays `房主控制面不可访问，请确认房主窗口仍在运行且 TCP 8080 可达。`
- `operation_timeout` displays `操作等待超时，请检查网络后重试。`
- Existing safe room errors retain their current Chinese mappings.
- No UI, CLI, IPC, service, or control log includes session tokens, administrator tokens, invites, private keys, public keys, or raw HTTP bodies.
- Preflight failure changes no service or control-plane resources.
- Preparation failure cleans only resources started by that attempt, resets the state deterministically, and leaves the user on the relevant setup page.
- Member-list refresh failure never tears down a healthy joined room.
- Explicit cleanup is idempotent. A partial leave failure is reported, cleanup still attempts local resource release, and the mode stays locked until the cleanup path reaches a deterministic terminal state.
- Closing the host window still ends the in-memory room. Closing a member window releases only resources that member UI started.

## Component Boundaries

### `internal/ipc`

- add the strict `room_members` command and sanitized response model;
- add command-specific Windows client deadlines;
- align the server connection deadline and request context;
- add the stable `control_unreachable` and `operation_timeout` codes.

### `internal/service`

- retain the successful local display name with joined state;
- project authenticated snapshots into sanitized, deterministic member rows;
- map only safe transport and timeout errors;
- preserve token ownership inside `HTTPControlClient`.

### `cmd/vpnctl`

- add `vpnctl room members` with no options;
- print the strict IPC JSON response and retain existing nonzero behavior for stable errors.

### `packaging/windows/install.ps1`

- wait for a complete named-pipe status response after starting the service;
- fail installation if application readiness is not reached in fifteen seconds.

### `packaging/windows/ui.ps1`

- add member preflight health checking before service installation;
- implement the named mutex and explicit UI state machine;
- add shared member grids and the approved wide/right, narrow/stacked responsive layout;
- integrate two-second member refresh with transition-based log deduplication;
- expose **结束房间** for hosts and keep **离开房间** for members.

### Documentation and packaging

- update root and Windows packaging READMEs with member display, explicit mode switching, host reachability, and timeout behavior;
- extend installer packaging tests so the released payload contains the updated UI and service/CLI binaries;
- do not commit generated installer payloads or release binaries.

## TDD Strategy

Every behavior change begins with a focused failing test and recorded RED evidence.

### IPC tests

- strict `room_members` request round trip and rejection of arguments or unknown fields;
- sanitized members response round trip, deterministic required fields, unknown-field rejection, and message-size bound;
- five-second local and forty-five-second network command budget selection;
- sixty-second server deadline and cancellation propagation;
- regression showing a room join that takes longer than five seconds can complete.

### Service tests

- local row plus peers are projected and deterministically ordered;
- duplicate names remain distinct by virtual IPv4;
- returned rows contain no node ID, key, token, endpoint, platform, or last-seen data;
- not joined, malformed snapshot, wrong network, timeout, unreachable control, and unknown control errors map to the specified safe codes;
- leave removes the membership from the next member view;
- member refresh does not mutate service join or path state.

### CLI tests

- `vpnctl room members` creates the correct no-argument IPC request;
- extra options are rejected;
- successful JSON contains only the documented member fields;
- stable errors remain on stderr with nonzero exit.

### Installer and UI tests

- install script waits for application-level status after `Start-Service` and fails on timeout;
- member health failure occurs before `Install-NodeService` and causes no start/stop action;
- a second UI instance exits before side effects;
- the state transition helper accepts every allowed transition and rejects illegal host/member switching;
- double primary clicks cannot launch two operations;
- explicit end and leave return to Idle only after cleanup;
- member refresh fingerprinting logs failure, recovery, and changed count once;
- member rows persist on transient failure and clear on leave/end;
- responsive audit verifies wide right-column and narrow stacked layouts at preferred, minimum, constrained, enlarged-font, and high-DPI sizes;
- member grid, primary actions, and diagnostics never overlap;
- changing layout does not reset the diagnostics splitter or start network activity.

### Integration tests

- host creates a room, joins, member joins, and both authenticated member views contain both names and distinct IPv4 addresses;
- member leaves and disappears from the next host member view;
- a delayed control server exceeding five seconds but remaining within the supported budget completes successfully;
- unreachable control produces `control_unreachable` without token or raw transport leakage.

## Verification Gates

Implementation is complete only after fresh evidence for:

- focused RED and GREEN output for every new behavior;
- deterministic focused tests repeated with `-count=20`;
- `go test -count=1 ./...`;
- `go vet ./...`;
- `GOOS=windows GOARCH=amd64 go test -run '^$' ./...`;
- PowerShell parser validation for every `packaging/windows/*.ps1` file;
- `gofmt -l` reports no Go formatting drift;
- `git diff --check` reports no whitespace errors;
- secret scans find no credential or key material in code, docs, logs, test fixtures, or generated assets;
- the real WinForms control tree passes the wide/narrow non-overlap layout audit;
- the Windows installer builds from final merged code using verified WireGuardNT DLL and license inputs;
- installer-focused tests pass against the rebuilt payload;
- generated `payload.zip` and embedded payload source are cleaned and untracked;
- GitHub branch checks complete before merge;
- the next prerelease tag points exactly at the merged `main` commit;
- Windows installer, Windows checksum, macOS DMG, and macOS checksum are attached to the updated Release.

Manual acceptance must verify the second-instance message, failed host preflight without service churn, a slow but successful join, host/member mode locking, explicit end/leave, two-second member updates, and wide/narrow resizing with no overlap.

Real two-machine public-IPv6, router, firewall, UAC, WireGuard, and DPI acceptance remains environment-dependent and must be reported as unverified unless actually performed.

## Non-Goals

- persistent rooms or automatic room restoration;
- manual enrollment approval or invitation-token UI;
- automatic retry, failover, or relay changes;
- heartbeat-based offline display;
- kick, ban, rename, role editing, or administrative member actions;
- exposing members before the local node has joined;
- public member enumeration from the control plane;
- changing the virtual IPv4 pool, UDP port, control TCP port, or open-room access boundary;
- redesigning the macOS developer-tools UI.
